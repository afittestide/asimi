package court

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
