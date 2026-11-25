package court

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/afittestide/asimi/internal/config"
	"github.com/blang/semver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

	var codeLines []string
	for _, line := range strings.Split(dockerfile, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		codeLines = append(codeLines, line)
	}
	code := strings.Join(codeLines, "\n")

	assert.Contains(t, code, "packages.cloud.google.com/apt",
		"gcloud must come from Google's auditable apt repo, not a hand-rolled tarball")
	assert.Regexp(t, `apt-get install -y --no-install-recommends(?:[^\n]*\\\n)*[^\n]*\bgoogle-cloud-cli\b`, code,
		"the Dockerfile must actually install google-cloud-cli — a mention in a comment is not an install")
	assert.Regexp(t, `(?m)^ENV\s+CLOUDSDK_CORE_DISABLE_PROMPTS=1`, code,
		"non-interactive exec would stall on a gcloud prompt without this ENV")

	// The Dockerfile must not use sudo: the image runs as root already.
	assert.NotContains(t, dockerfile, "sudo",
		"the Dockerfile must not use sudo — the image runs as root")
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

// TestCIBuildCacheWarmupUngated guards e1074: the Test job must run an
// unconditional `go build -tags containers_image_openpgp .` before
// `just test`, so the module build cache is populated before the suite runs.
// The warm-up must never be gated on a cache hit — an `if: cache hit` or
// `continue-on-error` style condition reintroduces the cold-cache stall it
// exists to prevent. (Since e1075's follow-up the suite no longer compiles the
// binary inline, but the warm-up still pays for the standalone e2e build step.)</｜DSML｜ parameter><｜DSML｜ parameter name=
func TestCIBuildCacheWarmupUngated(t *testing.T) {
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
	require.NotEqual(t, -1, runTests, "ci.yml must have a `Run tests` step")

	warmup := stepStart("Warm up Go build cache")
	require.NotEqual(t, -1, warmup, "ci.yml must have a `Warm up Go build cache` step (e1074)")
	assert.Less(t, warmup, runTests,
		"the build-cache warm-up must run before `Run tests`")

	// Isolate the warm-up step: from its `- name:` to the next `- name:`.
	end := len(lines)
	for i := warmup + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "- name: ") {
			end = i
			break
		}
	}
	body := strings.Join(lines[warmup:end], "\n")

	assert.Contains(t, body, "go build -tags containers_image_openpgp .",
		"warm-up must run the exact build buildAsimiBinary performs in tests")
	assert.NotContains(t, body, "if:",
		"warm-up must be unconditional — never gate it on a cache hit (e1074)")
	assert.NotContains(t, body, "continue-on-error",
		"warm-up failures must fail the job, not be swallowed")
}

// dependabotConfig mirrors the subset of .github/dependabot.yml the invariant
// guards. Only fields the invariant depends on are modelled; unknown keys are
// ignored by yaml.v3, so the config stays free to grow.
type dependabotConfig struct {
	Version int `yaml:"version"`
	Updates []struct {
		PackageEcosystem      string `yaml:"package-ecosystem"`
		Directory             string `yaml:"directory"`
		OpenPullRequestsLimit int    `yaml:"open-pull-requests-limit"`
		Ignore                []struct {
			DependencyName string `yaml:"dependency-name"`
		} `yaml:"ignore"`
	} `yaml:"updates"`
}

// dependabotIgnoredDependencies parses .github/dependabot.yml and returns the
// config plus the set of dependency names ignored in the gomod entry.
func dependabotIgnoredDependencies(t *testing.T) (dependabotConfig, []string) {
	t.Helper()
	var cfg dependabotConfig
	require.NoError(t,
		yaml.Unmarshal([]byte(readRootFile(t, ".github/dependabot.yml")), &cfg),
		".github/dependabot.yml must be valid YAML")

	var ignored []string
	for _, u := range cfg.Updates {
		for _, rule := range u.Ignore {
			ignored = append(ignored, rule.DependencyName)
		}
	}
	return cfg, ignored
}

// TestDependabotIgnoresOnlyUnsatisfiableDependency guards e1016's invariant: an
// open Dependabot alert for a dependency with no available patched version,
// whose vulnerable code this module does not build, must not leave `main` with
// a failing status check. The durable mechanism is a single, exactly-scoped
// `ignore` entry in .github/dependabot.yml.
//
// This test is deliberately adversarial about breadth. The failure mode the
// edict forbids is an over-broad ignore that also silences *actionable*
// findings — above all github.com/go-jose/go-jose/v4, whose alerts 49/50/51 are
// patched while go.mod pins it below the floor. A wildcard, an ecosystem-wide
// suppression, or an extra ignored name must fail here.
func TestDependabotIgnoresOnlyUnsatisfiableDependency(t *testing.T) {
	const unsatisfiable = "github.com/docker/distribution"

	cfg, ignored := dependabotIgnoredDependencies(t)

	require.Equal(t, 2, cfg.Version, "dependabot.yml must declare `version: 2`")
	require.Len(t, cfg.Updates, 1, "exactly one update entry is expected")
	entry := cfg.Updates[0]
	assert.Equal(t, "gomod", entry.PackageEcosystem)
	assert.Equal(t, "/", entry.Directory)
	assert.Equal(t, 0, entry.OpenPullRequestsLimit,
		"open-pull-requests-limit must stay 0: adding dependabot.yml must not enable "+
			"version-update PRs this repo has never had (security updates use a separate limit)")

	assert.Equal(t, []string{unsatisfiable}, ignored,
		"ignore must name exactly one dependency; a wildcard or an extra name is a regression")

	for _, name := range ignored {
		assert.NotContains(t, name, "*",
			"ignore entry %q must be an exact module path, never a wildcard", name)
	}

	// The actionable dependency must stay loud: go-jose alerts 49/50/51 are
	// patched (GHSA-mgcc-cww5-fp2q in 4.1.6; the other two in 4.1.5) while
	// go.mod pins v4.1.4, so silencing it would suppress real, fixable findings.
	// The exact-list assertion above is what enforces this — it rejects any
	// second ignore name unconditionally. Asserting the same thing again with a
	// version comparison would be dead code: the list check always fires first
	// and fires regardless of which version go.mod happens to pin.
	assert.NotContains(t, ignored, "github.com/go-jose/go-jose/v4",
		"go-jose/v4 is actionable (alerts 49/50/51) and must never be ignored")

	// The justification for ignoring the dependency at all is that the
	// vulnerable code is not vendored. Make that falsifiable: if
	// registry/client (the package carrying the advisory) ever enters the
	// vendor tree, the ignore rule is no longer safe and must be revisited.
	// NoDirExists, not NoFileExists — testify's NoFileExists passes for a
	// directory, which is exactly what this path would be.
	assert.NoDirExists(t, filepath.Join("..", "vendor", unsatisfiable, "registry", "client"),
		"registry/client is vendored — the vulnerable code is now built and the ignore rule is unsafe")

	modules := readRootFile(t, "vendor/modules.txt")
	prefix := unsatisfiable + "/"
	for _, line := range strings.Split(modules, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		assert.Contains(t, []string{
			unsatisfiable + "/registry/api/errcode",
			unsatisfiable + "/registry/api/v2",
		}, line, "unexpected vendored package %q — the ignore rule assumes only API types are built", line)
	}
}

// vulnScanFixFloors maps each module bumped for edict 1032 to the release that
// fixes the advisory the 0.16.0 threat scan reported (GO-2026-6865 for
// klauspost/compress, GO-2026-6888 for andybalholm/brotli). Pinning below a
// floor reopens the finding and reddens `just get-vuln`.
var vulnScanFixFloors = map[string]semver.Version{
	"github.com/klauspost/compress": semver.MustParse("1.20.2"),
	"github.com/andybalholm/brotli": semver.MustParse("1.2.7"),
}

// justRecipes returns every recipe name defined in a Justfile. A recipe header
// is an unindented line whose first word is the name and which carries a ':'
// (possibly with arguments after the name, or dependencies after the colon).
// Assignments (':='), comments and indented body lines are not recipes.
func justRecipes(justfile string) map[string]bool {
	recipes := map[string]bool{}
	for _, line := range strings.Split(justfile, "\n") {
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") ||
			strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, ":")
		if i <= 0 || strings.HasPrefix(line[i:], ":=") {
			continue
		}
		if fields := strings.Fields(line[:i]); len(fields) > 0 {
			recipes[fields[0]] = true
		}
	}
	return recipes
}

// recipeInvocations returns the recipe name of every `just <recipe>` mention in
// text. Only command-context mentions count — a leading `!` (a ritual given
// step), a backtick (a documented command) or a line-leading invocation — so
// English prose such as "just the recipe" cannot trip the guard.
func recipeInvocations(text string) []string {
	re := regexp.MustCompile("[!`]just\\s+([a-z][a-z0-9_-]*)|^just\\s+([a-z][a-z0-9_-]*)")
	var names []string
	for _, line := range strings.Split(text, "\n") {
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			names = append(names, m[1]+m[2])
		}
	}
	return names
}

// TestRitualAndDocInvocationsNameExistingRecipes guards the first defect of
// edict 1032: `0e6f61f15` renamed `vuln:` to `get-vuln:` and the release
// ritual kept invoking the dead name. A `!`-prefixed given step that exits
// non-zero hard-fails the step, and `verify-release-readiness` loops back to
// `prepare-changelog`, so the ritual could never reach `commit-and-tag`. Every
// `just` invocation in the project rituals and in the release/security docs must
// therefore name a recipe the Justfile actually defines.
func TestRitualAndDocInvocationsNameExistingRecipes(t *testing.T) {
	recipes := justRecipes(readRootFile(t, "Justfile"))
	require.True(t, recipes["test"], "could not parse the `test` recipe out of Justfile")

	rituals := readRootFile(t, ".agents/rituals.yaml")
	for _, name := range recipeInvocations(rituals) {
		assert.True(t, recipes[name],
			".agents/rituals.yaml invokes `just %s`, which no Justfile recipe defines — the given step hard-fails and the ritual loops", name)
	}

	// Parse rather than grep: a file-wide Contains stays green if the gate
	// migrates to another step while verify-release-readiness loses it — the
	// exact regression acceptance #5 guards.
	var defs Rituals
	require.NoError(t, yaml.Unmarshal([]byte(rituals), &defs), "parse .agents/rituals.yaml")
	readiness := findStep(defs, "release-version", "verify-release-readiness")
	require.NotNil(t, readiness, "release-version has no verify-release-readiness step")
	assert.Contains(t, strings.Join(readiness.Given, "\n"), "!just get-vuln",
		"verify-release-readiness must invoke the real `just get-vuln` gate (edict 1032)")

	for _, doc := range []string{"intent/docs/release-management.md", "intent/docs/security.md"} {
		for _, name := range recipeInvocations(readRootFile(t, doc)) {
			assert.True(t, recipes[name],
				"%s names `just %s`, which no Justfile recipe defines", doc, name)
		}
	}
}

// findStep returns the step named stepName in the ritual named ritualName, or
// nil when either is absent.
func findStep(rituals Rituals, ritualName, stepName string) *RitualStep {
	for _, r := range rituals {
		if r.Name != ritualName {
			continue
		}
		for i := range r.Steps {
			if r.Steps[i].Name == stepName {
				return &r.Steps[i]
			}
		}
	}
	return nil
}

// TestSecurityDocNamesTheThreatReportPath guards the Chancellor's B1 finding:
// intent/docs/security.md claimed scan reports land in heaven/security/*.txt while the
// recipe writes heaven/threats/*.out, sending a reader hunting release evidence
// to a directory that does not exist. Derive both facts from the recipe itself so
// the doc cannot drift from it again.
func TestSecurityDocNamesTheThreatReportPath(t *testing.T) {
	body := recipeBody(readRootFile(t, "Justfile"), "get-vuln:")
	require.NotEmpty(t, body, "could not read the get-vuln recipe body")

	dir := regexp.MustCompile(`(?m)^\s*report_dir="([^"]+)"`).FindStringSubmatch(body)
	require.Len(t, dir, 2, "get-vuln defines no report_dir=\"...\" — update this guard")
	dirName := dir[1]

	require.Regexp(t, `report="\$report_dir/\$\{tool\}-\{\{now\}\}\.out"`, body,
		"get-vuln uses a report filename other than ${tool}-{{now}}.out — update this guard")

	doc := readRootFile(t, "intent/docs/security.md")
	assert.Contains(t, doc, dirName+"/",
		"intent/docs/security.md must name the real report directory %q the get-vuln recipe writes", dirName)
	assert.Contains(t, doc, "<tool>-<datetime>.out",
		"intent/docs/security.md must give the real report extension (.out), not the old .txt")
}

// TestRPCProtocolDocContractCitationsAreReal guards the proofread requirement of
// the Channel↔Tab contract (e1058): every file:line reference cited in
// intent/docs/rpc-protocol.md's contract section must name a real symbol in the cited
// file. The first forged revision cited `ritualChannelID (court/court.go)` —
// the function lives in court/ritual_manager.go — and the claim passed because
// nothing checked it.
func TestRPCProtocolDocContractCitationsAreReal(t *testing.T) {
	doc := readRootFile(t, "intent/docs/rpc-protocol.md")

	// The contract section must exist and must cite the right defining file for
	// each named symbol. A symbol moving files without the doc following is the
	// exact regression this guards.
	for _, tc := range []struct{ symbol, wantFile string }{
		{"ritualChannelID", "court/ritual_manager.go"},
	} {
		// The cited file must exist and must define the symbol.
		body, err := os.ReadFile(filepath.Join("..", tc.wantFile))
		require.NoError(t, err, "intent/docs/rpc-protocol.md cites %s; that file must exist", tc.wantFile)
		assert.Contains(t, string(body), "func "+tc.symbol,
			"intent/docs/rpc-protocol.md's contract cites %s in %s, which no longer defines it — fix the citation",
			tc.symbol, tc.wantFile)

		// The contract must pair the symbol with the correct file.
		assert.Regexp(t, regexp.MustCompile("`"+tc.symbol+"`\\s*\\n\\s*\\(`"+regexp.QuoteMeta(tc.wantFile)+"`\\)"),
			doc, "contract section must cite %s with (%s)", tc.symbol, tc.wantFile)
	}
}

// TestRPCProtocolDocSectionsAreSequential guards the numbering gap the first
// forged revision of e1058 introduced: the contract section was inserted as an
// unnumbered ### while sections 6-11 were renumbered to 7-12, leaving no ## 6.
func TestRPCProtocolDocSectionsAreSequential(t *testing.T) {
	doc := readRootFile(t, "intent/docs/rpc-protocol.md")

	re := regexp.MustCompile(`(?m)^## (\d+)\. `)
	matches := re.FindAllStringSubmatch(doc, -1)
	require.NotEmpty(t, matches, "intent/docs/rpc-protocol.md must have numbered sections")

	want := 1
	for _, m := range matches {
		got, err := strconv.Atoi(m[1])
		require.NoError(t, err, "parse section number %q", m[1])
		assert.Equal(t, want, got,
			"intent/docs/rpc-protocol.md section numbering breaks at ## %d: sections must run 1,2,3,... with no gaps (a section inserted or removed must renumber the rest)", got)
		want++
	}
}

// TestRPCProtocolDocSectionRefsResolve guards the regression the chancellor
// caught in e1058's second revision: inserting the contract as § 6 renumbered
// every later section but left a `§ 10` pointer self-referencing the Cookbook
// it lives in. Every `§ N` cross-reference must name an existing section and
// must not point at the section it appears in.
func TestRPCProtocolDocSectionRefsResolve(t *testing.T) {
	doc := readRootFile(t, "intent/docs/rpc-protocol.md")

	sections := map[int]string{} // number -> title, for messages
	re := regexp.MustCompile(`(?m)^## (\d+)\. (.+)$`)
	for _, m := range re.FindAllStringSubmatch(doc, -1) {
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err, "parse section number %q", m[1])
		sections[n] = m[2]
	}
	require.NotEmpty(t, sections, "intent/docs/rpc-protocol.md must have numbered sections")

	type ref struct {
		n    int
		in   int
		line int
	}
	var refs []ref
	lines := strings.Split(doc, "\n")
	current := 0
	for i, line := range lines {
		if m := re.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			current = n
			continue
		}
		for _, m := range regexp.MustCompile(`§ (\d+)`).FindAllStringSubmatch(line, -1) {
			n, err := strconv.Atoi(m[1])
			require.NoError(t, err, "parse § reference %q on line %d", m[1], i+1)
			refs = append(refs, ref{n: n, in: current, line: i + 1})
		}
	}
	require.NotEmpty(t, refs, "intent/docs/rpc-protocol.md must reference at least one section by § — if the last § pointer was removed intentionally, update this guard")

	for _, r := range refs {
		_, exists := sections[r.n]
		assert.True(t, exists,
			"intent/docs/rpc-protocol.md line %d references § %d, which does not exist (sections: %v) — renumbering must sweep every § pointer in the same commit",
			r.line, r.n, sectionKeys(sections))
		assert.NotEqual(t, r.in, r.n,
			"intent/docs/rpc-protocol.md line %d references § %d from inside § %d — a self-referencing pointer is a broken cross-reference",
			r.line, r.n, r.in)
	}
}

// sectionKeys returns the sorted section numbers, for failure messages.
func sectionKeys(sections map[int]string) []int {
	keys := make([]int, 0, len(sections))
	for n := range sections {
		keys = append(keys, n)
	}
	sort.Ints(keys)
	return keys
}

// moduleVersions returns every version recorded for module in one file. go.mod
// carries it as a require line, vendor/modules.txt as a `# module vX` header,
// and go.sum as one or more hash lines.
func moduleVersions(t *testing.T, file, module string) []semver.Version {
	t.Helper()
	pattern := regexp.MustCompile(`(?m)^[ \t#]*` + regexp.QuoteMeta(module) + ` v([0-9][0-9.]*)`)
	var versions []semver.Version
	for _, m := range pattern.FindAllStringSubmatch(readRootFile(t, file), -1) {
		v, err := semver.ParseTolerant(m[1])
		require.NoError(t, err, "parse %s version %q in %s", module, m[1], file)
		versions = append(versions, v)
	}
	return versions
}

// TestThreatScanModulesMeetFixFloor guards the second defect of edict 1032: the
// scan reported reachable advisories in two indirect modules that had fixes
// available. Because the repository vendors, go.mod, go.sum and the vendored
// module list must move together — a bump that skips the vendor tree leaves the
// binary-mode scan complaining about the old code while the build uses the new.
func TestThreatScanModulesMeetFixFloor(t *testing.T) {
	for module, floor := range vulnScanFixFloors {
		mod := moduleVersions(t, "go.mod", module)
		require.Len(t, mod, 1, "go.mod must carry exactly one require line for %s", module)
		pinned := mod[0]
		assert.False(t, pinned.LT(floor),
			"go.mod pins %s v%s, must be >= v%s (the finding is fixed there and the gate stays red below it)",
			module, pinned, floor)

		vendored := moduleVersions(t, "vendor/modules.txt", module)
		require.Len(t, vendored, 1, "vendor/modules.txt must list %s exactly once", module)
		assert.True(t, vendored[0].EQ(pinned),
			"vendor/modules.txt pins %s v%s but go.mod pins v%s — run `go mod vendor`",
			module, vendored[0], pinned)

		sum := moduleVersions(t, "go.sum", module)
		require.NotEmpty(t, sum, "go.sum must record %s", module)
		assert.Contains(t, sum, pinned,
			"go.sum has no hash for %s v%s — run `go mod tidy`", module, pinned)
	}
}

// TestAllowlistEntriesAreJustifiedAndLive enforces the contract that
// `just get-vuln` and intent/docs/security.md promise: the allowlist carries only
// govulncheck IDs, each ID has a written justification section, and an ID whose
// section is marked retired is no longer listed. Edict 1032 retired
// GO-2026-5932 exactly this way rather than leaving a stale entry behind.
func TestAllowlistEntriesAreJustifiedAndLive(t *testing.T) {
	doc := readRootFile(t, "intent/docs/security.md")

	var accepted []string
	seen := map[string]bool{}
	for _, line := range strings.Split(readRootFile(t, "security/vuln-allowlist.txt"), "\n") {
		id := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if id == "" {
			continue
		}
		assert.Regexp(t, `^GO-\d{4}-\d+$`, id, "allowlist line %q is not a govulncheck ID", line)
		assert.False(t, seen[id], "%s is listed twice in security/vuln-allowlist.txt", id)
		seen[id] = true
		accepted = append(accepted, id)
	}

	for _, id := range accepted {
		assert.Contains(t, doc, "### "+id,
			"%s is on the allowlist with no `### %s` justification in intent/docs/security.md", id, id)
	}

	retired := regexp.MustCompile(`(?m)^### (GO-\d{4}-\d+) — retired`)
	for _, m := range retired.FindAllStringSubmatch(doc, -1) {
		assert.NotContains(t, accepted, m[1],
			"%s is retired in intent/docs/security.md but still on the allowlist — remove it or reinstate the acceptance", m[1])
	}
}

// nonVendorGoFiles lists the repository's non-test Go files, relative to the
// repo root, skipping the vendored tree and dot-directories (worktrees, agent
// state) so a guard sees only code Asimi itself compiles.
func nonVendorGoFiles(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != "." && (name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			rel, relErr := filepath.Rel(root, path)
			require.NoError(t, relErr)
			files = append(files, rel)
		}
		return nil
	})
	require.NoError(t, err, "walk the non-vendor Go tree")
	return files
}

// TestGO20266911AcceptanceBasisStillHolds guards the premise the
// GO-2026-6911 acceptance rests on. The finding has no upstream fix, so it is
// accepted on the stdio-only MCP boundary (edict 661):
//  1. InitBifrost leaves schemas.BifrostConfig.MCPConfig nil so Bifrost never
//     builds an internal MCPManager that bypasses Asimi's validation or seal gate.
//  2. No production Go file references remote MCP transports
//     (MCPConnectionTypeHTTP, MCPConnectionTypeSSE, or NewStreamableHTTP).
//  3. config.ValidateMCPConfig rejects HTTP, SSE, Streamable-HTTP, and URL
//     configs fail-closed citing GO-2026-6911.
//  4. internal/config/default.conf documents the stdio-only policy citing
//     GO-2026-6911.
func TestGO20266911AcceptanceBasisStillHolds(t *testing.T) {
	accepted := false
	for _, line := range strings.Split(readRootFile(t, "security/vuln-allowlist.txt"), "\n") {
		if strings.TrimSpace(strings.SplitN(line, "#", 2)[0]) == "GO-2026-6911" {
			accepted = true
		}
	}
	if !accepted {
		t.Skip("GO-2026-6911 is no longer accepted — nothing to guard")
	}

	// Scan the whole non-vendor tree: no BifrostConfig may wire MCPConfig, and
	// no production file may construct HTTP/SSE/StreamableHTTP MCP transports.
	for _, file := range nonVendorGoFiles(t) {
		body := readRootFile(t, file)
		if strings.Contains(body, "BifrostConfig") && strings.Contains(body, "MCPConfig") {
			t.Errorf("%s wires MCPConfig into a BifrostConfig — bifrost would build an internal MCPManager "+
				"bypassing Asimi's stdio-only validation and seal gate; re-review GO-2026-6911", file)
		}
		for _, forbidden := range []string{"MCPConnectionTypeHTTP", "MCPConnectionTypeSSE", "NewStreamableHTTP"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s references %s — remote MCP transports exercise the vulnerable mcp-go "+
					"StreamableHTTP path covered by GO-2026-6911; re-review the acceptance", file, forbidden)
			}
		}
	}

	// Verify config validation actively rejects non-stdio transports citing GO-2026-6911.
	for _, bad := range []config.MCPServerConfig{
		{ID: "http-srv", Transport: "http", Command: []string{"mcp"}},
		{ID: "sse-srv", Transport: "sse", Command: []string{"mcp"}},
		{ID: "shttp-srv", Transport: "streamable-http", Command: []string{"mcp"}},
		{ID: "url-srv", URL: "https://example.com/mcp", Command: []string{"mcp"}},
	} {
		err := config.ValidateMCPConfig(config.MCPConfig{Servers: []config.MCPServerConfig{bad}})
		require.Error(t, err, "expected non-stdio MCP server %q to be rejected", bad.ID)
		assert.Contains(t, err.Error(), "GO-2026-6911")
	}

	defaultConf := readRootFile(t, "internal/config/default.conf")
	assert.Contains(t, defaultConf, "GO-2026-6911",
		"default.conf must document the stdio-only MCP restriction citing GO-2026-6911")
}

// TestRootDockerfileCloudRunContract guards e1094: the root Dockerfile builds a
// multi-arch, non-root Cloud Run runtime image whose Go builder stays in lockstep
// with go.mod, whose .dockerignore keeps vendor/ available for -mod=vendor, and
// whose entrypoint and env defaults do not interfere with Cloud Run ADC.
func TestRootDockerfileCloudRunContract(t *testing.T) {
	dockerfile := readRootFile(t, "Dockerfile")
	var codeLines []string
	for _, line := range strings.Split(dockerfile, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		codeLines = append(codeLines, line)
	}
	code := strings.Join(codeLines, "\n")

	wantGo := goModToolchain(t, readRootFile(t, "go.mod"))
	fromMatch := regexp.MustCompile(`(?m)^FROM\s+--platform=\$BUILDPLATFORM\s+golang:(\d+\.\d+(?:\.\d+)?)-bookworm\s+AS\s+builder`).FindStringSubmatch(code)
	require.Len(t, fromMatch, 2, "Dockerfile builder must use `FROM --platform=$BUILDPLATFORM golang:<ver>-bookworm AS builder`")
	gotGo, err := semver.ParseTolerant(fromMatch[1])
	require.NoError(t, err, "parse builder Go version %q", fromMatch[1])
	assert.True(t, gotGo.EQ(wantGo), "Dockerfile Go builder %s must match go.mod %s", gotGo, wantGo)

	// BuildKit injects TARGETOS and TARGETARCH into the global scope; redeclaring
	// them with a default value (e.g. `ARG TARGETARCH=amd64`) overrides the
	// global automatic platform arg in BuildKit's dispatchArg and silently embeds
	// an amd64 binary into the linux/arm64 image. They must be bare declarations,
	// with fallback defaults applied in the shell expansion instead.
	var bareTargetOS, bareTargetArch bool
	for _, line := range codeLines {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "ARG") {
			continue
		}
		for _, tok := range fields[1:] {
			switch tok {
			case "TARGETOS":
				bareTargetOS = true
			case "TARGETARCH":
				bareTargetArch = true
			}
			assert.False(t, strings.HasPrefix(tok, "TARGETOS="),
				"Dockerfile must declare bare `ARG TARGETOS` without a default value (got %q), otherwise BuildKit overrides the automatic platform arg", tok)
			assert.False(t, strings.HasPrefix(tok, "TARGETARCH="),
				"Dockerfile must declare bare `ARG TARGETARCH` without a default value (got %q), otherwise BuildKit overrides the automatic platform arg and breaks linux/arm64 builds", tok)
		}
	}
	assert.True(t, bareTargetOS, "builder must declare bare `ARG TARGETOS`")
	assert.True(t, bareTargetArch, "builder must declare bare `ARG TARGETARCH`")

	assert.Regexp(t, `CGO_ENABLED=0\s+GOOS=\$\{TARGETOS:-linux\}\s+GOARCH=\$\{TARGETARCH:-amd64\}`, code,
		"builder must cross-compile statically with shell fallback defaults GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64}")
	assert.Contains(t, code, "-mod=vendor", "builder must compile offline with -mod=vendor")
	assert.Contains(t, code, "-tags containers_image_openpgp", "builder must pass -tags containers_image_openpgp")

	assert.Regexp(t, `(?m)^FROM\s+debian:(?:bookworm|trixie)-slim\b`, code,
		"runtime stage must use a minimal multi-arch Debian slim base image")
	assert.Contains(t, code, "COPY --from=builder /out/asimi /usr/local/bin/asimi",
		"runtime stage must copy only the compiled binary from builder")
	for _, pkg := range []string{"ca-certificates", "git", "curl"} {
		assert.Contains(t, code, pkg, "runtime stage must install %q", pkg)
	}
	assert.Contains(t, code, "/var/lib/apt/lists/*", "runtime stage must clean apt lists")
	assert.Contains(t, code, `org.opencontainers.image.source="https://github.com/afittestide/asimi"`,
		"runtime image must declare OCI source label so GHCR links the package")
	assert.Contains(t, code, `org.opencontainers.image.licenses="MIT"`,
		"runtime image must declare OCI license label")
	assert.Regexp(t, `(?m)^WORKDIR\s+/workspace\b`, code,
		"runtime image must set WORKDIR /workspace")
	assert.Regexp(t, `(?m)^ENV\s+ASIMI_HOME=/tmp/asimi\b`, code,
		"runtime image must default ASIMI_HOME=/tmp/asimi for ephemeral writable storage")
	userMatch := regexp.MustCompile(`(?m)^USER\s+(\S+)`).FindStringSubmatch(code)
	require.Len(t, userMatch, 2, "runtime image must declare a USER instruction")
	assert.NotEqual(t, "root", userMatch[1], "runtime image must run as a non-root user")
	assert.NotEqual(t, "0", userMatch[1], "runtime image must not run as UID 0")
	assert.Contains(t, code, `ENTRYPOINT ["asimi", "--isolated-host", "--handsoff"]`,
		"runtime image must keep the headless isolated-host entrypoint contract")
	assert.NotRegexp(t, `(?m)^ENV\s+.*\b(GOOGLE_APPLICATION_CREDENTIALS|ASIMI_PROVIDER|ASIMI_MODEL|GOOGLE_CLOUD_PROJECT|GOOGLE_CLOUD_REGION)\b`, code,
		"Dockerfile must not hardcode Cloud Run ADC or Vertex provider env vars")

	ignoreSet := map[string]bool{}
	for _, line := range strings.Split(readRootFile(t, ".dockerignore"), "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		ignoreSet[entry] = true
	}
	assert.False(t, ignoreSet["vendor"] || ignoreSet["vendor/"],
		".dockerignore must not exclude vendor/ because Dockerfile builds with -mod=vendor")
	for _, excluded := range []string{".git", ".agents/worktrees", "heaven", "jobs", "dist", ".gocache", "asimi", "asimi_linux", "*.log", "*.sqlite*"} {
		assert.True(t, ignoreSet[excluded], ".dockerignore must exclude %q from the build context", excluded)
	}
}

// TestCIDockerPublishJob guards e1094: .github/workflows/ci.yml must remain
// valid YAML, keep `test` and `goreleaser`, and delegate image publishing to
// the reusable .github/workflows/publish-image.yml workflow after `test`
// passes. The reusable workflow must support both `workflow_call` and
// `workflow_dispatch`, and enforce a build-only guard (no GHCR login or push)
// on pull requests and non-main branches.
func TestCIDockerPublishJob(t *testing.T) {
	type workflowJob struct {
		Name        string            `yaml:"name"`
		Needs       any               `yaml:"needs"`
		If          string            `yaml:"if"`
		RunsOn      string            `yaml:"runs-on"`
		Uses        string            `yaml:"uses"`
		Permissions map[string]string `yaml:"permissions"`
		Steps       []struct {
			Name string            `yaml:"name"`
			If   string            `yaml:"if"`
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	}
	type workflowFile struct {
		Name string                 `yaml:"name"`
		On   map[string]any         `yaml:"on"`
		Jobs map[string]workflowJob `yaml:"jobs"`
	}

	var ci workflowFile
	require.NoError(t,
		yaml.Unmarshal([]byte(readRootFile(t, ".github/workflows/ci.yml")), &ci),
		".github/workflows/ci.yml must be valid YAML")

	_, hasTest := ci.Jobs["test"]
	assert.True(t, hasTest, "ci.yml must keep the existing `test` job")
	_, hasRelease := ci.Jobs["goreleaser"]
	assert.True(t, hasRelease, "ci.yml must keep the existing `goreleaser` job")
	_, hasLegacyDocker := ci.Jobs["docker"]
	assert.False(t, hasLegacyDocker, "ci.yml must extract the legacy `docker` job into publish-image.yml")

	ciJob, ok := ci.Jobs["publish-image"]
	require.True(t, ok, "ci.yml must define a `publish-image` job")
	assert.Equal(t, "Publish Image", ciJob.Name)
	assert.Equal(t, "test", ciJob.Needs, "publish-image job must depend on `test` so broken commits never publish")
	assert.Equal(t, "./.github/workflows/publish-image.yml", ciJob.Uses,
		"publish-image job must call the reusable publish-image.yml workflow")
	assert.Empty(t, ciJob.Steps, "reusable workflow caller job must not define inline steps")
	assert.Contains(t, ciJob.If, "pull_request", "publish-image job must validate pull requests")
	assert.Contains(t, ciJob.If, "refs/heads/main", "publish-image job must run on main pushes")
	assert.Contains(t, ciJob.If, "refs/tags/v", "publish-image job must run on v* tag pushes")
	assert.Equal(t, map[string]string{
		"contents": "read",
		"packages": "write",
	}, ciJob.Permissions, "ci.yml publish-image caller must grant only least-privilege permissions")

	var pub workflowFile
	require.NoError(t,
		yaml.Unmarshal([]byte(readRootFile(t, ".github/workflows/publish-image.yml")), &pub),
		".github/workflows/publish-image.yml must be valid YAML")

	assert.Equal(t, "Publish Image", pub.Name)
	_, hasCall := pub.On["workflow_call"]
	assert.True(t, hasCall, "publish-image.yml must declare `workflow_call` trigger")
	_, hasDispatch := pub.On["workflow_dispatch"]
	assert.True(t, hasDispatch, "publish-image.yml must declare `workflow_dispatch` trigger")

	job, ok := pub.Jobs["publish-image"]
	require.True(t, ok, "publish-image.yml must define a `publish-image` job")
	assert.Equal(t, "Publish Image", job.Name)
	assert.Equal(t, "ubuntu-latest", job.RunsOn)
	assert.Equal(t, map[string]string{
		"contents": "read",
		"packages": "write",
	}, job.Permissions, "publish-image job must grant only least-privilege permissions")

	const wantPushGuard = "github.event_name != 'pull_request' && (github.ref == 'refs/heads/main' || startsWith(github.ref, 'refs/tags/v'))"

	var sawQEMU, sawBuildx, sawLogin, sawMeta, sawBuild bool
	for _, step := range job.Steps {
		switch {
		case strings.HasPrefix(step.Uses, "docker/setup-qemu-action@"):
			sawQEMU = true
		case strings.HasPrefix(step.Uses, "docker/setup-buildx-action@"):
			sawBuildx = true
		case strings.HasPrefix(step.Uses, "docker/login-action@"):
			sawLogin = true
			assert.Equal(t, "ghcr.io", step.With["registry"])
			assert.Equal(t, wantPushGuard, strings.TrimSpace(step.If),
				"GHCR login must be skipped on pull_request and non-main branches")
		case strings.HasPrefix(step.Uses, "docker/metadata-action@"):
			sawMeta = true
			assert.Equal(t, "ghcr.io/afittestide/asimi", step.With["images"])
			tags := step.With["tags"]
			assert.Contains(t, tags, "value=main,enable={{is_default_branch}}")
			assert.Contains(t, tags, "prefix=sha-,format=short,enable={{is_default_branch}}")
			assert.Contains(t, tags, "type=semver,pattern=v{{version}}")
			assert.Contains(t, tags, "value=latest,enable=${{ startsWith(github.ref, 'refs/tags/v') }}")
		case strings.HasPrefix(step.Uses, "docker/build-push-action@"):
			sawBuild = true
			assert.Equal(t, "linux/amd64,linux/arm64", step.With["platforms"])
			assert.Equal(t, "${{ "+wantPushGuard+" }}", strings.TrimSpace(step.With["push"]),
				"pull_request and non-main manual dispatch builds must be build-only (never push)")
			assert.Contains(t, step.With["cache-from"], "type=gha")
			assert.Contains(t, step.With["cache-to"], "type=gha")
		}
	}
	assert.True(t, sawQEMU, "publish-image job must set up QEMU for multi-arch runtime stage builds")
	assert.True(t, sawBuildx, "publish-image job must set up Docker Buildx")
	assert.True(t, sawLogin, "publish-image job must log in to GHCR")
	assert.True(t, sawMeta, "publish-image job must extract Docker metadata")
	assert.True(t, sawBuild, "publish-image job must build and push the multi-arch image")
}
