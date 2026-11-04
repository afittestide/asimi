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

// throttleEnvRe captures the single ENV instruction that sets the Go build
// throttle. The values are a tuning knob (the Ruler may retune them without a
// test change); the shape is the invariant. Group 1 is the three throttle keys
// in order, GOFLAGS then GOMAXPROCS then GOMEMLIMIT, each `KEY=<value>`.
var throttleEnvRe = regexp.MustCompile(
	`(?ms)^ENV\s+(GOFLAGS=-p=\d+\s+\\\s*\n\s*GOMAXPROCS=\d+\s+\\\s*\n\s*GOMEMLIMIT=\d+(?:MiB|GiB))`)

// throttleBlock returns the whitespace-normalised throttle keys from a
// Dockerfile, e.g. "GOFLAGS=-p=2 GOMAXPROCS=8 GOMEMLIMIT=3000MiB". It fails
// the test unless the throttle is present as one well-formed ENV default, so
// the assertion tests the invariant (keys present, well-formed, ENV-scoped)
// rather than brittle literals — retuning the throttle must not fail the suite.
func throttleBlock(t *testing.T, dockerfile string) string {
	t.Helper()
	m := throttleEnvRe.FindStringSubmatch(dockerfile)
	require.NotNil(t, m,
		"Dockerfile must set a single well-formed ENV default for GOFLAGS/GOMAXPROCS/GOMEMLIMIT")
	return strings.Join(strings.Fields(m[1]), " ")
}

// TestDockerfileThrottlesGoBuilds verifies that the sandbox image bounds Go
// build parallelism and runtime memory. Without these defaults, concurrent
// edict rituals compile at full parallelism inside one ~2 GiB podman VM and
// the kernel OOM-kills the compiler (`signal: killed` -> `[build failed]`),
// which the swift-strike judge gate misreads as a test failure. The specific
// values are a tuning knob; only their presence and shape are guarded.
func TestDockerfileThrottlesGoBuilds(t *testing.T) {
	require.NotEmpty(t, dotagentsDockerfile)
	assert.NotEmpty(t, throttleBlock(t, dotagentsDockerfile),
		"Dockerfile must set a well-formed Go build throttle")
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

	assert.NotEmpty(t, throttleBlock(t, string(data)),
		"live Dockerfile must set a well-formed Go build throttle")
}

// TestSandboxThrottlesMatchTemplate guards against drift between the two
// copies of the Dockerfile. Both must carry the same throttle defaults, or a
// project seeded by the court would get different OOM behaviour than this repo.
// The comparison is live-vs-template equality of the throttle block, so the
// throttle can be retuned in one place without touching this test.
func TestSandboxThrottlesMatchTemplate(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, ".agents", "sandbox", "Dockerfile"))
	require.NoError(t, err, "live .agents/sandbox/Dockerfile must exist")

	assert.Equal(t, throttleBlock(t, string(data)), throttleBlock(t, dotagentsDockerfile),
		"live Dockerfile and embedded template must agree on the Go build throttle")
}

// TestDockerfileThrottleIsEnvDefault ensures the throttle is expressed as a
// Dockerfile ENV default, not as a per-RUN flag. The edict's whole point is
// that every go/just invocation inherits the budget without caller-side flags,
// so a throttle scoped to one RUN instruction would not satisfy the intent.
func TestDockerfileThrottleIsEnvDefault(t *testing.T) {
	require.NotEmpty(t, dotagentsDockerfile)
	// The ENV line may be continued across lines with backslashes; throttleBlock
	// only matches the three keys as one ENV instruction.
	assert.NotEmpty(t, throttleBlock(t, dotagentsDockerfile),
		"throttle must be a single ENV default so all sandbox commands inherit it")
	assert.False(t, strings.Contains(dotagentsDockerfile, "RUN go env -w"),
		"throttle must not be scoped to a RUN via go env -w")
}

// TestLiveDockerfileInstallsGoogleCloudCLI guards edict 1023: this repo's
// live sandbox image must install the gcloud CLI from Google's apt repo so
// ministers and rituals can run `gcloud` inside the sandbox, and it must
// disable gcloud's interactive prompts because `podman exec` has no TTY.
// Only the live file is guarded — the embedded template stays minimal, as the
// Ruler scoped gcloud to this repo.
func TestLiveDockerfileInstallsGoogleCloudCLI(t *testing.T) {
	dockerfile := readRootFile(t, ".agents/sandbox/Dockerfile")

	rawLayer, layer := gcloudDockerfileLayer(t, dockerfile)

	assert.Contains(t, layer, "packages.cloud.google.com/apt",
		"gcloud must come from Google's auditable apt repo, not a hand-rolled tarball")
	assert.Regexp(t, `apt-get install -y --no-install-recommends[^\n]*\bgoogle-cloud-cli\b`, layer,
		"the gcloud layer must actually install google-cloud-cli — a mention in the header comment is not an install")
	assert.Regexp(t, `(?m)^ENV\s+CLOUDSDK_CORE_DISABLE_PROMPTS=1`, layer,
		"non-interactive exec would stall on a gcloud prompt without this ENV")

	// The gcloud layer must not use sudo: the image runs as root already.
	assert.NotContains(t, rawLayer, "sudo",
		"the gcloud layer must not use sudo — the image runs as root")
}

// gcloudDockerfileLayer returns the gcloud install block of a sandbox
// Dockerfile: raw includes the comment header, code has the comment lines
// stripped. Assertions run against code so prose in the header comment (which
// names google-cloud-cli and CLOUDSDK_CORE_DISABLE_PROMPTS) cannot satisfy a
// guard whose real subject — the RUN/ENV instruction — has been deleted.
func gcloudDockerfileLayer(t *testing.T, dockerfile string) (raw, code string) {
	t.Helper()
	start := strings.Index(dockerfile, "# Google Cloud CLI")
	require.NotEqual(t, -1, start, "Dockerfile must carry the gcloud install layer")
	end := strings.Index(dockerfile[start:], "# install gh")
	require.NotEqual(t, -1, end, "gcloud layer must sit with its own comment header")
	// Slice only after the guard: a reworded "# install gh" header must fail
	// with the message above, never panic on a negative index.
	raw = dockerfile[start:]
	if end >= 0 {
		raw = dockerfile[start : start+end]
	}

	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return raw, b.String()
}

// TestLiveProjectConfigMountsGcloudADC guards the second half of edict 1023:
// the image's gcloud is useless unless the live project config binds the host's
// Application Default Credentials at /root/.config/gcloud — gcloud's default
// CLOUDSDK_CONFIG for the root user the sandbox runs as. Deleting that mount
// leaves every other test green while the sandbox silently runs
// unauthenticated, so the mount itself is asserted, not just the image.
func TestLiveProjectConfigMountsGcloudADC(t *testing.T) {
	conf := readRootFile(t, ".agents/asimi.conf")

	// Parse the mounts by hand rather than pulling a TOML dependency into the
	// court package: the file is small and the assertions are about the mount
	// pair, not about TOML semantics.
	fieldRe := regexp.MustCompile(`(?m)^\s*(source|destination)\s*=\s*"([^"]*)"`)
	var mounted bool
	for _, block := range strings.Split(conf, "[[sandbox.additional_mounts]]")[1:] {
		var text strings.Builder
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "[") {
				break // next table
			}
			text.WriteString(line)
			text.WriteString("\n")
		}
		fields := map[string]string{}
		for _, f := range fieldRe.FindAllStringSubmatch(text.String(), -1) {
			fields[f[1]] = f[2]
		}
		if fields["destination"] == "/root/.config/gcloud" {
			mounted = true
			assert.Equal(t, "~/.config/gcloud", fields["source"],
				"the ADC mount must source the host's ~/.config/gcloud")
		}
	}
	assert.True(t, mounted,
		"project config must bind the host's gcloud ADC to /root/.config/gcloud")

	// The ruling forbids moving CLOUDSDK_CONFIG into passthrough_env:
	// /root/.config/gcloud is already gcloud's default config dir for root, and
	// an inherited host value would point gcloud at an unmounted path.
	for _, line := range strings.Split(conf, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "passthrough_env") {
			continue
		}
		assert.NotContains(t, line, "CLOUDSDK_CONFIG",
			"CLOUDSDK_CONFIG must not be passed through — the container path is gcloud's default")
	}
}

// TestLiveProjectConfigHeaderIsAccurate guards the load path description that
// heads the live project config. It is the file a user opens when a sandbox
// mount misbehaves, so the header is the instructions they follow at that
// moment — and it was wrong for asimi.conf's whole life (conf.toml, asimi.toml,
// and a general ASIMI_* layer that no longer exists).
//
// The e985 bad-boy-scout precedent the edict invokes covers this file, but the
// guard for the seeded template (TestDefaultConfContent_ReferencesCorrectPaths,
// internal/config/loader_test.go) only reads the embedded default.conf — nothing
// read the live copy, so the correction was unguarded and a reforge could
// revert it with a green suite. The variables named here are pinned to the
// loader's actual reads, not to prose: ASIMI_HOME (loader.go:51) and the three
// LLM overrides (loader.go:217-223).
func TestLiveProjectConfigHeaderIsAccurate(t *testing.T) {
	conf := readRootFile(t, ".agents/asimi.conf")
	header := conf
	if i := strings.Index(conf, "\n["); i >= 0 {
		header = conf[:i]
	}
	require.Contains(t, header, "asimi.conf",
		"live config must not be headed by stale filenames (the header names the load order)")

	for _, stale := range []string{"conf.toml", "asimi.toml", "ASIMI_LLM_PROVIDER"} {
		assert.NotContains(t, header, stale,
			"live config header still describes a path/env layer that no longer exists: %s", stale)
	}
	// No ASIMI_* layer exists beyond the individually read variables — koanf is
	// loaded from file providers only (loader.go:170-197), so a wildcard layer
	// claim is false. Allow it only as part of the list below.
	for _, line := range strings.Split(header, "\n") {
		if !strings.Contains(line, "ASIMI_*") {
			continue
		}
		assert.Contains(t, line, "no general ASIMI_*",
			"the header must not claim a general ASIMI_* override layer: %q", line)
	}
	// The variables it does name must be the ones the loader reads.
	for _, v := range []string{"ASIMI_MODEL", "ASIMI_PROVIDER", "ASIMI_REASONING_EFFORT"} {
		assert.Contains(t, header, v,
			"header must name the LLM env override %s that loader.go actually reads", v)
	}
	assert.Contains(t, header, "ASIMI_HOME",
		"header must name ASIMI_HOME, which loader.go reads to relocate storage")
	// The "everything is commented out" claim was false: live tables follow.
	assert.NotContains(t, header, "All options are commented out by default",
		"live config's [sandbox]/[run_shell_command]/[storage] tables are active; the header cannot say all options are commented out")
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

// TestCIDevPathStaysPlainAndFast guards edict 1014's central promise: with CI
// unset, `just test` runs the plain suite only. The `-race` gate is the
// expensive half, so it must sit INSIDE the `test:` recipe's CI branch — a
// regression that hoists it back out would silently restore the slow inner
// loop the edict exists to remove. The plain `go test` line must also keep the
// load-bearing openpgp build tag (dropping it pulls cgo gpgme; edict 1014) and
// must not carry `-v` (the tee archive is the record; -v only bloats the
// terminal). The tee target is asserted in both modes.
func TestCIDevPathStaysPlainAndFast(t *testing.T) {
	content := readRootFile(t, "Justfile")
	body := recipeBody(content, "test:")
	require.NotEmpty(t, body, "could not extract `test` recipe body from Justfile")

	lines := strings.Split(body, "\n")
	ciGuard, race, plain := -1, -1, -1
	for i, line := range lines {
		if strings.Contains(line, `if [ -n "${CI`) {
			ciGuard = i
		}
		if strings.Contains(line, "just test-race") {
			race = i
		}
		if strings.Contains(line, "go test ") && strings.Contains(line, "./...") {
			plain = i
		}
	}

	require.NotEqual(t, -1, ciGuard, "`test` recipe must branch on the CI env var")
	require.NotEqual(t, -1, race, "`test` recipe must invoke `just test-race`")
	require.NotEqual(t, -1, plain, "`test` recipe must run the plain `go test ./...` suite")

	// The race gate must be INSIDE the CI branch: it comes after the guard and
	// before the closing `fi`. Above the guard it would run on the dev station.
	assert.Greater(t, race, ciGuard, "`just test-race` must run after the CI guard, not before it")
	fi := -1
	for i := race; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "fi" {
			fi = i
			break
		}
	}
	require.NotEqual(t, -1, fi, "the CI branch containing `just test-race` must be closed with `fi`")
	assert.Less(t, race, fi, "`just test-race` must sit inside the CI branch (before `fi`)")

	// Plain suite: load-bearing tag kept, -v dropped.
	plainLine := lines[plain]
	assert.Contains(t, plainLine, "-tags containers_image_openpgp",
		"plain `go test` must keep the containers_image_openpgp tag")
	assert.NotContains(t, " "+plainLine+" ", " -v ",
		"plain `go test` must not carry -v (the tee archive is the record)")

	// Both modes archive under heaven/tests/.
	assert.Contains(t, body, "heaven/tests/", "`test` recipe must tee output to heaven/tests/")
}

// TestRaceRecipeKeepsRaceAndOpenPGP guards acceptance #3 for the CI half of the
// fork: the `test-race` invocation must keep both the race detector and the
// containers_image_openpgp build tag. Without the tag, -race would pull cgo
// gpgme and fail to build (see the test recipe's comment).
func TestRaceRecipeKeepsRaceAndOpenPGP(t *testing.T) {
	content := readRootFile(t, "Justfile")
	body := recipeBody(content, "test-race")
	require.NotEmpty(t, body, "could not extract `test-race` recipe body from Justfile")
	assert.Contains(t, body, "-race", "`test-race` must pass -race to go test")
	assert.Contains(t, body, "-tags containers_image_openpgp",
		"`test-race` must keep the containers_image_openpgp build tag")
}

// TestCIUploadsTestArtifactsAlways guards acceptance #5: the CI job uploads
// heaven/tests/ as the `heaven-tests` artifact even when the run is red — a
// failing run is exactly when the archived evidence is needed, so the step must
// carry `if: always()`. The step must live after `Run tests` in the same job
// (the recipe writes the file) and point at heaven/tests/.
func TestCIUploadsTestArtifactsAlways(t *testing.T) {
	content := readRootFile(t, ".github/workflows/ci.yml")
	lines := strings.Split(content, "\n")

	stepStart := func(name string) int {
		for i, line := range lines {
			if strings.TrimSpace(line) == "- name: "+name {
				return i
			}
		}
		return -1
	}

	runTests := stepStart("Run tests")
	upload := stepStart("Upload test artifacts")
	require.NotEqual(t, -1, runTests, "ci.yml must have a `Run tests` step")
	require.NotEqual(t, -1, upload, "ci.yml must have an `Upload test artifacts` step")
	assert.Greater(t, upload, runTests, "artifact upload must follow `Run tests` in the same job")

	// Isolate the upload step: from its `- name:` to the next `- name:`.
	end := len(lines)
	for i := upload + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "- name: ") {
			end = i
			break
		}
	}
	body := strings.Join(lines[upload:end], "\n")

	assert.Contains(t, body, "uses: actions/upload-artifact@v4", "upload step must use upload-artifact")
	assert.Contains(t, body, "if: always()", "upload step must run even when tests fail")
	assert.Contains(t, body, "name: heaven-tests", "artifact must be named heaven-tests")
	assert.Contains(t, body, "path: heaven/tests/", "artifact must upload heaven/tests/")
}
