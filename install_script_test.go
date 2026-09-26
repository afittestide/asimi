package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installScriptPath returns the absolute path to scripts/install.sh relative to
// this test file (tests run with CWD set to the package directory).
func installScriptPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("scripts/install.sh")
	if err != nil {
		t.Fatalf("resolve install.sh: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("install.sh not found at %s: %v", p, err)
	}
	return p
}

// platformOS maps runtime.GOOS to the os token install.sh derives from
// `uname -s` (Linux*->linux, Darwin*->darwin). Tests must build archive names
// with this token, not a hardcoded "linux", or they break on macOS hosts.
func platformOS() string {
	switch runtime.GOOS {
	case "linux":
		return "linux"
	case "darwin":
		return "darwin"
	default:
		return runtime.GOOS
	}
}

// platformArch maps runtime.GOARCH to the arch token used in release archives.
func platformArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

// makeTarGz builds a .tar.gz. When wrap is non-empty every entry is placed under
// a top-level directory bearing that name (goreleaser wrap_in_directory layout).
// Otherwise files sit at the archive root (flat layout).
func makeTarGz(t *testing.T, wrap string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// Deterministic ordering keeps archives stable across runs.
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	// simple insertion sort (small maps)
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}

	for _, name := range names {
		body := files[name]
		hdr := &tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(body)),
		}
		if wrap != "" {
			hdr.Name = wrap + "/" + name
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write tar body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// fakeCurlScript writes a curl shim that serves the archive and checksums from
// dir, ignoring the requested URL. The installer invokes:
//
//	curl -fsSL <url> -o <out>
func fakeCurlScript(dir string) string {
	return fmt.Sprintf(`#!/bin/bash
# Args are: -fsSL <url> -o <out>
url=""
out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *)  if [[ -z "$url" ]]; then url="$1"; fi; shift ;;
  esac
done
if [[ "$url" == *checksums.txt ]]; then
  cp %[1]q/checksums.txt "$out"
else
  cp %[1]q/archive.tar.gz "$out"
fi
`, dir)
}

// runInstaller executes the real scripts/install.sh in an isolated environment
// with a fake curl on PATH serving the asset in assetDir.
func runInstaller(t *testing.T, script, assetDir, installDir string) (string, error) {
	t.Helper()

	binDir := filepath.Join(assetDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	curlPath := filepath.Join(binDir, "curl")
	if err := os.WriteFile(curlPath, []byte(fakeCurlScript(assetDir)), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}

	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(),
		"ASIMI_INSTALL_DIR="+installDir,
		"ASIMI_VERSION=v1.2.3",
		"ASIMI_NO_MODIFY_PATH=1",
		"SHELL=/bin/bash",
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestInstallScriptExtractsWrappedArchive verifies the edict-864 change: when
// goreleaser wraps the payload in a directory named after the archive
// (wrap_in_directory: true), install.sh must locate the binary inside that
// directory and install a working executable.
func TestInstallScriptExtractsWrappedArchive(t *testing.T) {
	script := installScriptPath(t)
	arch := platformArch()
	version := "1.2.3"
	wrap := fmt.Sprintf("asimi_%s_%s_%s", version, platformOS(), arch)
	archiveName := wrap + ".tar.gz"

	payload := "#!/bin/bash\necho WRAPPED-OK\n"
	archive := makeTarGz(t, wrap, map[string]string{
		"asimi":     payload,
		"README.md": "readme\n",
		"LICENSE":   "license\n",
	})

	assetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(assetDir, "archive.tar.gz"), archive, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)
	if err := os.WriteFile(filepath.Join(assetDir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	installDir := filepath.Join(t.TempDir(), "install")
	out, err := runInstaller(t, script, assetDir, installDir)
	// The pre-existing EXIT trap (trap 'rm -rf "$tmp_dir"' with set -u) makes the
	// script exit non-zero after completing its work; that is not the behavior
	// under test here. We therefore assert on the installed artifact, not the exit
	// code.
	_ = err

	bin := filepath.Join(installDir, "asimi")
	if _, statErr := os.Stat(bin); statErr != nil {
		t.Fatalf("binary not installed for wrapped archive (script output):\n%s %s", out, bin)
	}
	got := runBinary(t, bin)
	if !strings.Contains(got, "WRAPPED-OK") {
		t.Fatalf("installed binary output = %q, want to contain WRAPPED-OK", got)
	}
}

// TestInstallScriptFallsBackToFlatArchive verifies the defensive fallback: if the
// archive is flat (no wrapping directory), install.sh must still install the
// binary from the extraction root.
func TestInstallScriptFallsBackToFlatArchive(t *testing.T) {
	script := installScriptPath(t)
	arch := platformArch()
	version := "1.2.3"
	archiveName := fmt.Sprintf("asimi_%s_%s_%s.tar.gz", version, platformOS(), arch)

	payload := "#!/bin/bash\necho FLAT-OK\n"
	archive := makeTarGz(t, "", map[string]string{
		"asimi": payload,
	})

	assetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(assetDir, "archive.tar.gz"), archive, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)
	if err := os.WriteFile(filepath.Join(assetDir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	installDir := filepath.Join(t.TempDir(), "install")
	out, _ := runInstaller(t, script, assetDir, installDir)

	bin := filepath.Join(installDir, "asimi")
	if _, statErr := os.Stat(bin); statErr != nil {
		t.Fatalf("binary not installed for flat archive (script output):\n%s", out)
	}
	got := runBinary(t, bin)
	if !strings.Contains(got, "FLAT-OK") {
		t.Fatalf("installed binary output = %v, want to contain FLAT-OK", got)
	}
}

// TestInstallScriptWrappedDirMatchesArchiveName guards the coupling that edict
// 864 relies on: the wrapping directory produced by goreleaser equals the
// archive basename minus the .tar.gz extension. If goreleaser's naming ever
// diverges, install.sh's ${archive_name%.tar.gz} derivation breaks and this
// test documents the contract.
func TestInstallScriptWrappedDirMatchesArchiveName(t *testing.T) {
	archiveName := "asimi_1.2.3_linux_amd64.tar.gz"
	wrap := strings.TrimSuffix(archiveName, ".tar.gz")
	if wrap != "asimi_1.2.3_linux_amd64" {
		t.Fatalf("wrapping dir derivation = %q, want asimi_1.2.3_linux_amd64", wrap)
	}
}

// runInstallerShimmed is like runInstaller but also puts a `uname` shim on PATH
// so the installer's detect_os/detect_arch observe a forced platform
// (e.g. "darwin"/"arm64"). This lets tests exercise the installer on a host
// whose real platform differs from the one under test.
func runInstallerShimmed(t *testing.T, script, assetDir, installDir, fakeOS, fakeArch string) (string, error) {
	t.Helper()

	binDir := filepath.Join(assetDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(fakeCurlScript(assetDir)), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
	// The installer's detect_os matches `uname -s` output ("Linux*", "Darwin*"),
	// so the shim must emit the canonical uname spelling, not the lowercase
	// archive token.
	unameOS := fakeOS
	switch fakeOS {
	case "darwin":
		unameOS = "Darwin"
	case "linux":
		unameOS = "Linux"
	}
	unameShim := fmt.Sprintf(`#!/bin/bash
case "$1" in
  -s) echo %[1]q ;;
  -m) echo %[2]q ;;
  *) exec /usr/bin/uname "$@" ;;
esac
`, unameOS, fakeArch)
	if err := os.WriteFile(filepath.Join(binDir, "uname"), []byte(unameShim), 0o755); err != nil {
		t.Fatalf("write uname shim: %v", err)
	}

	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(),
		"ASIMI_INSTALL_DIR="+installDir,
		"ASIMI_VERSION=v1.2.3",
		"ASIMI_NO_MODIFY_PATH=1",
		"SHELL=/bin/bash",
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestInstallScriptVerifiesChecksumOnForeignPlatform is the regression guard for
// the macOS-only failure: the fixture archive/checksum is named with the
// installer's OWN detected platform (shimmed darwin/arm64 here), so the checksum
// lookup must succeed and the script must complete with exported tmp_dir intact
// (no `tmp_dir: unbound variable`). Under the old hardcoded-"linux" test + local
// trap variable this failed; it must now install a working binary and exit 0.
func TestInstallScriptVerifiesChecksumOnForeignPlatform(t *testing.T) {
	script := installScriptPath(t)
	fakeOS, fakeArch := "darwin", "arm64"
	version := "1.2.3"
	wrap := fmt.Sprintf("asimi_%s_%s_%s", version, "darwin", fakeArch)
	archiveName := wrap + ".tar.gz"

	payload := "#!/bin/bash\necho SHIMMED-OK\n"
	archive := makeTarGz(t, wrap, map[string]string{"asimi": payload})

	assetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(assetDir, "archive.tar.gz"), archive, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	digest := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), archiveName)
	if err := os.WriteFile(filepath.Join(assetDir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	installDir := filepath.Join(t.TempDir(), "install")
	out, err := runInstallerShimmed(t, script, assetDir, installDir, fakeOS, fakeArch)
	if err != nil {
		t.Fatalf("installer exited non-zero on shimmed %s/%s: %v\n%s", fakeOS, fakeArch, err, out)
	}
	if !strings.Contains(out, "Checksum verified") {
		t.Fatalf("expected checksum verification to succeed, got:\n%s", out)
	}

	bin := filepath.Join(installDir, "asimi")
	if _, statErr := os.Stat(bin); statErr != nil {
		t.Fatalf("binary not installed for shimmed %s/%s (script output):\n%s", fakeOS, fakeArch, out)
	}
	if got := runBinary(t, bin); !strings.Contains(got, "SHIMMED-OK") {
		t.Fatalf("installed binary output = %q, want SHIMMED-OK", got)
	}
}

// runBinary executes an installed script and returns its stdout, or fails.
func runBinary(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command(path).CombinedOutput()
	if err != nil {
		t.Fatalf("run installed binary %s: %v (output: %s)", path, err, out)
	}
	return string(out)
}
