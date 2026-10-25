package court

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blang/semver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAsimiRuntimeNotEmpty verifies that the embedded asimi_runtime.sh is present.
func TestAsimiRuntimeNotEmpty(t *testing.T) {
	require.NotEmpty(t, dotagentsAsimiRuntime, "embedded asimi_runtime.sh must not be empty")
}

// TestBashrcSetsGitTerminalPrompt verifies that bashrc exports GIT_TERMINAL_PROMPT=0
// so git operations fail with a clear credential error instead of attempting an
// interactive prompt that can never be satisfied in the sandbox.
func TestBashrcSetsGitTerminalPrompt(t *testing.T) {
	require.NotEmpty(t, dotagentsBashrc, "embedded bashrc must not be empty")
	assert.Contains(t, dotagentsBashrc, "export GIT_TERMINAL_PROMPT=0",
		"bashrc must export GIT_TERMINAL_PROMPT=0 to prevent interactive git prompts")
}

// TestBashrcSetsTermDumb verifies that TERM=dumb is set to prevent interactive
// terminal features in the sandbox.
func TestBashrcSetsTermDumb(t *testing.T) {
	require.NotEmpty(t, dotagentsBashrc)
	assert.Contains(t, dotagentsBashrc, `export TERM="dumb"`,
		"bashrc must export TERM=dumb for non-interactive sandbox")
}

// TestBashrcGitShimPresent verifies that the git function wrapper (Li enforcement)
// is present in bashrc to block dangerous git flags.
func TestBashrcGitShimPresent(t *testing.T) {
	require.NotEmpty(t, dotagentsBashrc)
	assert.Contains(t, dotagentsBashrc, "git()",
		"bashrc must define git() function wrapper for Li enforcement")
	assert.Contains(t, dotagentsBashrc, "--hard",
		"git shim must block --hard flag")
	assert.Contains(t, dotagentsBashrc, "--force",
		"git shim must block --force flag")
}

// TestDockerfileInstallsRuntimeCorrectly verifies that the Dockerfile installs
// asimi_runtime.sh into /etc/bash.bashrc and bashrc as /root/.bashrc.
func TestDockerfileInstallsRuntimeCorrectly(t *testing.T) {
	require.NotEmpty(t, dotagentsDockerfile)
	assert.Contains(t, dotagentsDockerfile, "asimi_runtime.sh /tmp/asimi_runtime.sh",
		"Dockerfile must COPY asimi_runtime.sh to /tmp")
	assert.Contains(t, dotagentsDockerfile, "cat /tmp/asimi_runtime.sh >> /etc/bash.bashrc",
		"Dockerfile must append asimi_runtime.sh to /etc/bash.bashrc")
	assert.Contains(t, dotagentsDockerfile, "bashrc /root/.bashrc",
		"Dockerfile must COPY bashrc to /root/.bashrc")
}

// TestDockerfileThrottlesGoBuilds verifies that the sandbox image bounds Go
// build parallelism and runtime memory. Without these defaults, concurrent
// edict rituals compile at full parallelism inside one ~2 GiB podman VM and
// the kernel OOM-kills the compiler (`signal: killed` -> `[build failed]`),
// which the swift-strike judge gate misreads as a test failure.
func TestDockerfileThrottlesGoBuilds(t *testing.T) {
	require.NotEmpty(t, dotagentsDockerfile)
	assert.Contains(t, dotagentsDockerfile, "GOFLAGS=-p=1",
		"Dockerfile must serialize package builds to bound compiler memory")
	assert.Contains(t, dotagentsDockerfile, "GOMAXPROCS=2",
		"Dockerfile must bound concurrent compile goroutines")
	assert.Contains(t, dotagentsDockerfile, "GOMEMLIMIT=1500MiB",
		"Dockerfile must set a Go runtime memory budget below the VM limit")
}

// TestLiveDockerfileThrottlesGoBuilds is the companion guard for the file
// `just build-sandbox` actually builds. TestDockerfileThrottlesGoBuilds only
// inspects the embedded template (court/dotagents/sandbox/Dockerfile), which
// the court seeds into fresh projects; this repo's live image is built from
// .agents/sandbox/Dockerfile. The two diverged before (different FROM base,
// different package set), so guarding only the template would let the throttle
// silently disappear from the image under real rituals — the exact regression
// edict 948 exists to prevent. Asserted against the on-disk file so a future
// edit to either copy fails loudly.
func TestLiveDockerfileThrottlesGoBuilds(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, ".agents", "sandbox", "Dockerfile"))
	require.NoError(t, err, "live .agents/sandbox/Dockerfile must exist")
	content := string(data)

	assert.Contains(t, content, "GOFLAGS=-p=1",
		"live Dockerfile must serialize package builds to bound compiler memory")
	assert.Contains(t, content, "GOMAXPROCS=2",
		"live Dockerfile must bound concurrent compile goroutines")
	assert.Contains(t, content, "GOMEMLIMIT=1500MiB",
		"live Dockerfile must set a Go runtime memory budget below the VM limit")
}

// TestSandboxThrottlesMatchTemplate guards against drift between the two
// copies of the Dockerfile. Both must carry the same throttle defaults, or a
// project seeded by the court would get different OOM behaviour than this repo.
func TestSandboxThrottlesMatchTemplate(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, ".agents", "sandbox", "Dockerfile"))
	require.NoError(t, err, "live .agents/sandbox/Dockerfile must exist")

	for _, want := range []string{"GOFLAGS=-p=1", "GOMAXPROCS=2", "GOMEMLIMIT=1500MiB"} {
		assert.Contains(t, string(data), want, "live Dockerfile missing throttle %q", want)
		assert.Contains(t, dotagentsDockerfile, want, "embedded template missing throttle %q", want)
	}
}

// TestDockerfileThrottleIsEnvDefault ensures the throttle is expressed as a
// Dockerfile ENV default, not as a per-RUN flag. The edict's whole point is
// that every go/just invocation inherits the budget without caller-side flags,
// so a throttle scoped to one RUN instruction would not satisfy the intent.
func TestDockerfileThrottleIsEnvDefault(t *testing.T) {
	require.NotEmpty(t, dotagentsDockerfile)
	// The ENV line may be continued across lines with backslashes; assert the
	// three keys appear after an ENV keyword in the template.
	assert.Regexp(t, `(?s)ENV\s+GOFLAGS=-p=1.*GOMEMLIMIT=1500MiB`, dotagentsDockerfile,
		"throttle must be a single ENV default so all sandbox commands inherit it")
	assert.False(t, strings.Contains(dotagentsDockerfile, "RUN go env -w"),
		"throttle must not be scoped to a RUN via go env -w")
}

// goFixFloor is the toolchain version that fixes GO-2026-6809
// (crypto/x509 intermediate-cert verification). Every Go pin site must be at
// least this version or the security gate e986 exists to pass regresses.
var goFixFloor = semver.MustParse("1.26.10")

// podmanFixFloor is the release that fixes GO-2026-6770 (`podman run` disabled
// sandboxing via image annotation). The sandbox is the runner's whole point, so
// the bindings must not fall below this floor or the vuln gate reopens.
var podmanFixFloor = semver.MustParse("6.1.3")

// readRootFile reads a file relative to the repository root (tests run with
// CWD set to the package directory, so ".." is the root).
func readRootFile(t *testing.T, rel string) string {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, rel))
	require.NoError(t, err, "read %s", rel)
	return string(data)
}

// goModToolchain returns the version from the `go` directive of go.mod.
func goModToolchain(t *testing.T, mod string) semver.Version {
	t.Helper()
	m := regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)`).FindStringSubmatch(mod)
	require.NotNil(t, m, "go.mod must carry a `go <version>` directive")
	v, err := semver.ParseTolerant(m[1])
	require.NoError(t, err, "parse go directive %q", m[1])
	return v
}

// ciGoVersions returns every `go-version: 'x.y.z'` pin from a workflow file.
func ciGoVersions(t *testing.T, workflow string) []semver.Version {
	t.Helper()
	matches := regexp.MustCompile(`go-version:\s*'([^']+)'`).FindAllStringSubmatch(workflow, -1)
	require.NotEmpty(t, matches, "workflow must pin the Go version via go-version")
	versions := make([]semver.Version, 0, len(matches))
	for _, m := range matches {
		v, err := semver.ParseTolerant(m[1])
		require.NoError(t, err, "parse go-version %q", m[1])
		versions = append(versions, v)
	}
	return versions
}

// TestGoToolchainPinMeetsSecurityFloor guards the Go bump that e986 exists to
// ship. GO-2026-6809 (stdlib crypto/x509 intermediate-cert verification) is
// fixed in go1.26.10; reverting any pin site reopens the finding and fails the
// vuln gate. Every site must be at least the floor, and all sites must agree —
// a stale CI job (the Release job was left at 1.26.6 when Test moved on) is the
// exact drift this catches. Asserted against the on-disk files so an edit to
// any one pin fails loudly instead of silently splitting the toolchain.
func TestGoToolchainPinMeetsSecurityFloor(t *testing.T) {
	mod := readRootFile(t, "go.mod")
	toolchain := goModToolchain(t, mod)
	assert.False(t, toolchain.LT(goFixFloor),
		"go.mod `go` directive = %s, must be >= %s (GO-2026-6809)", toolchain, goFixFloor)

	for _, version := range ciGoVersions(t, readRootFile(t, ".github/workflows/ci.yml")) {
		assert.False(t, version.LT(goFixFloor),
			"ci.yml go-version = %s, must be >= %s (GO-2026-6809)", version, goFixFloor)
		assert.True(t, version.EQ(toolchain),
			"ci.yml go-version %s must match go.mod toolchain %s", version, toolchain)
	}

	dockerfile := readRootFile(t, ".agents/sandbox/Dockerfile")
	m := regexp.MustCompile(`(?m)^FROM\s+golang:(\d+\.\d+(?:\.\d+)?)-`).FindStringSubmatch(dockerfile)
	require.NotNil(t, m, "sandbox Dockerfile must pin a golang:<version> base image")
	image, err := semver.ParseTolerant(m[1])
	require.NoError(t, err, "parse golang image tag %q", m[1])
	assert.False(t, image.LT(goFixFloor),
		"sandbox Dockerfile base golang:%s, must be >= %s (GO-2026-6809)", image, goFixFloor)
}

// TestPodmanDependencyMeetsSecurityFloor guards the podman bump for
// GO-2026-6770. The module must be at least v6.1.3 in go.mod, go.sum, and the
// vendored module list. The repository vendors, so a bump that moves only some
// of these reopens the finding (or breaks `go mod verify`); the three must move
// together.
func TestPodmanDependencyMeetsSecurityFloor(t *testing.T) {
	const module = "go.podman.io/podman/v6"

	modVersion := func(t *testing.T, text, header string) semver.Version {
		t.Helper()
		pattern := `(?m)^\s*` + regexp.QuoteMeta(header) + `\s*` + regexp.QuoteMeta(module) + `\s+v([0-9.]+)`
		m := regexp.MustCompile(pattern).FindStringSubmatch(text)
		require.NotNil(t, m, "%s must list %s", header, module)
		v, err := semver.ParseTolerant(m[1])
		require.NoError(t, err, "parse %s version %q", header, m[1])
		return v
	}

	checks := map[string]semver.Version{
		"go.mod":             modVersion(t, readRootFile(t, "go.mod"), ""),
		"go.sum":             modVersion(t, readRootFile(t, "go.sum"), ""),
		"vendor/modules.txt": modVersion(t, readRootFile(t, "vendor/modules.txt"), "#"),
	}

	var baseline semver.Version
	for _, name := range []string{"go.mod", "go.sum", "vendor/modules.txt"} {
		version := checks[name]
		assert.False(t, version.LT(podmanFixFloor),
			"%s pins %s v%s, must be >= v%s (GO-2026-6770)", name, module, version, podmanFixFloor)
		if baseline.EQ(semver.Version{}) {
			baseline = version
			continue
		}
		assert.True(t, version.EQ(baseline),
			"%s pins %s v%s but baseline is v%s — go.mod, go.sum, and vendor must move together",
			name, module, version, baseline)
	}
}
