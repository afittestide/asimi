package runners

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostRunner(t *testing.T) {
	runner := NewHostRunner(0, "")
	require.NotNil(t, runner)
	assert.Equal(t, "host", runner.RunnerType())
}

func TestHostRunnerRunWithBypassApproval(t *testing.T) {
	runner := NewHostRunner(0, t.TempDir())

	output, err := runner.Run(context.Background(), Input{
		Command:        "echo hello",
		BypassApproval: true,
	})

	require.NoError(t, err)
	assert.Contains(t, output.Output, "hello")
	assert.Equal(t, "0", output.ExitCode)
}

// A spawned command must observe the non-interactive git env so no
// child can emit a credential prompt.
func TestHostRunnerForcesNonInteractiveGitEnv(t *testing.T) {
	runner := NewHostRunner(0, t.TempDir())

	output, err := runner.Run(context.Background(), Input{
		Command:        "printenv GIT_TERMINAL_PROMPT; printenv GIT_ASKPASS; printenv SSH_ASKPASS; printenv GCM_INTERACTIVE",
		BypassApproval: true,
	})

	require.NoError(t, err)
	assert.Equal(t, "0", output.ExitCode)
	assert.Contains(t, output.Output, "0")
	assert.Contains(t, output.Output, "/bin/false")
	assert.Contains(t, output.Output, "never")
}

// GoCacheEnv pins GOCACHE to the project root so worktrees share one cache.
func TestGoCacheEnv(t *testing.T) {
	assert.Nil(t, GoCacheEnv(""), "empty root must yield no GOCACHE fragment")
	got := GoCacheEnv("/repo")
	assert.Equal(t, []string{"GOCACHE=/repo/.gocache"}, got)
}

// EnvWithout removes every matching NAME=... entry so an override cannot be
// shadowed by a duplicate key.
func TestEnvWithout(t *testing.T) {
	env := []string{"PATH=/usr/bin", "GOCACHE=/dev/cache", "HOME=/root", "GOCACHE=/other"}
	got := EnvWithout(env, "GOCACHE")
	assert.NotContains(t, got, "GOCACHE=/dev/cache")
	assert.NotContains(t, got, "GOCACHE=/other")
	assert.Contains(t, got, "PATH=/usr/bin")
	assert.Contains(t, got, "HOME=/root")
}

// GOCACHE is anchored on the main project root even after the runner is
// re-targeted at a worktree, so all edicts share one build cache.
func TestHostRunnerGoCacheAnchorsOnProjectRoot(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".agents", "worktrees", "e42")
	require.NoError(t, os.MkdirAll(wt, 0o755))

	runner := NewHostRunner(0, root)
	runner.SetRepoInfo(repo.RepoInfo{
		ProjectRoot:  root,
		WorktreePath: filepath.Join(".agents", "worktrees", "e42"),
		IsWorktree:   true,
	})

	// A stale inherited GOCACHE must not shadow the pinned value.
	t.Setenv("GOCACHE", filepath.Join(wt, ".gocache"))
	out, err := runner.Run(context.Background(), Input{Command: "printenv GOCACHE", BypassApproval: true})
	require.NoError(t, err)
	assert.Equal(t, "0", out.ExitCode)
	assert.Contains(t, out.Output, filepath.Join(root, ".gocache"))
	assert.NotContains(t, out.Output, filepath.Join(wt, ".gocache"))
}

// The fragment must not clobber env vars the child legitimately needs.
func TestHostRunnerEnvPreservesPath(t *testing.T) {
	runner := NewHostRunner(0, t.TempDir())

	output, err := runner.Run(context.Background(), Input{
		Command:        "printenv PATH",
		BypassApproval: true,
	})

	require.NoError(t, err)
	assert.Equal(t, "0", output.ExitCode)
	assert.NotEmpty(t, output.Output)
}

func TestHostRunnerRunExitCode(t *testing.T) {
	runner := NewHostRunner(0, t.TempDir())

	output, err := runner.Run(context.Background(), Input{
		Command:        "exit 42",
		BypassApproval: true,
	})

	require.NoError(t, err)
	assert.Equal(t, "42", output.ExitCode)
}

func TestHostRunnerRunWithStderr(t *testing.T) {
	runner := NewHostRunner(0, t.TempDir())

	output, err := runner.Run(context.Background(), Input{
		Command:        "echo 'stdout' && echo 'stderr' >&2",
		BypassApproval: true,
	})

	require.NoError(t, err)
	assert.Contains(t, output.Output, "stdout")
	assert.Contains(t, output.Output, "stderr")
	assert.Equal(t, "0", output.ExitCode)
}

func TestHostRunnerApprovalRequest(t *testing.T) {
	runner := NewHostRunner(0, "")
	msgChan := make(chan Msg, 10)
	runner.SetMessageChannel(msgChan)

	// Start a goroutine to handle the approval request
	go func() {
		select {
		case msg := <-msgChan:
			approvalReq, ok := msg.(ApprovalRequestMsg)
			require.True(t, ok, "Expected ApprovalRequestMsg, got %T", msg)
			assert.Equal(t, "echo hello", approvalReq.Command)
			approvalReq.ResponseChan <- true
		case <-time.After(5 * time.Second):
			t.Error("Timeout waiting for approval request")
		}
	}()

	output, err := runner.Run(context.Background(), Input{
		Command:        "echo hello",
		BypassApproval: false, // Requires approval
	})

	require.NoError(t, err)
	assert.Contains(t, output.Output, "hello")
	assert.Equal(t, "0", output.ExitCode)
}

func TestHostRunnerApprovalDenied(t *testing.T) {
	runner := NewHostRunner(0, "")
	msgChan := make(chan Msg, 10)
	runner.SetMessageChannel(msgChan)

	// Start a goroutine to deny the approval request
	go func() {
		select {
		case msg := <-msgChan:
			approvalReq, ok := msg.(ApprovalRequestMsg)
			require.True(t, ok, "Expected ApprovalRequestMsg, got %T", msg)
			approvalReq.ResponseChan <- false // Deny
		case <-time.After(5 * time.Second):
			t.Error("Timeout waiting for approval request")
		}
	}()

	output, err := runner.Run(context.Background(), Input{
		Command:        "echo hello",
		BypassApproval: false,
	})

	require.Error(t, err)
	_, ok := err.(CommandDeniedError)
	assert.True(t, ok, "Expected CommandDeniedError, got %T", err)
	assert.Equal(t, "1", output.ExitCode)
}

func TestHostRunnerNoMsgChannel(t *testing.T) {
	runner := NewHostRunner(0, "")

	// Without a message channel and requiring approval, it should fail
	output, err := runner.Run(context.Background(), Input{
		Command:        "echo hello",
		BypassApproval: false,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no approval mechanism configured")
	assert.Equal(t, "1", output.ExitCode)
}

func TestHostRunnerRestart(t *testing.T) {
	runner := NewHostRunner(0, "")

	// Restart should be a no-op for host runner
	err := runner.Restart(context.Background())
	assert.NoError(t, err)
}

func TestHostRunnerClose(t *testing.T) {
	runner := NewHostRunner(0, "")

	// Close should be a no-op for host runner
	err := runner.Close(context.Background())
	assert.NoError(t, err)
}

func TestHostRunnerContextCancellation(t *testing.T) {
	runner := NewHostRunner(0, "")

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context while sleep is still running
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := runner.Run(ctx, Input{
		Command:        "sleep 10",
		BypassApproval: true,
	})

	require.Error(t, err)
	assert.Equal(t, context.Canceled, err)
}

func TestHostRunnerSetsWorkingDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a file in the temp directory
	testContent := "hello-from-project-root"
	err := os.WriteFile(filepath.Join(tmpDir, "testfile.txt"), []byte(testContent), 0644)
	require.NoError(t, err)

	runner := NewHostRunner(0, tmpDir)

	output, err := runner.Run(context.Background(), Input{
		Command:        "cat testfile.txt",
		BypassApproval: true,
	})

	require.NoError(t, err)
	assert.Contains(t, output.Output, testContent)
	assert.Equal(t, "0", output.ExitCode)
}

// A long-running command must hit the configured deadline and return the
// timeout message with exit code 124 — a result, not an error, mirroring
// PodmanRunner's semantics. sleep 5, not `sleep infinity`: BSD sleep
// rejects `infinity`; 5s is far beyond the 100ms deadline yet bounded if
// the deadline ever breaks.
func TestHostRunnerCommandTimeout(t *testing.T) {
	runner := NewHostRunner(0, t.TempDir())
	runner.SetTimeouts(HostTimeouts{Command: 100 * time.Millisecond})

	start := time.Now()
	output, err := runner.Run(context.Background(), Input{
		Command:        "sleep 5",
		BypassApproval: true,
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "124", output.ExitCode)
	assert.Contains(t, output.Output, "Command timed out after")
	assert.Less(t, elapsed, 10*time.Second, "command should return at the deadline, not hang")
}

// timeout_minutes=0 (zero value) must fall back to the 10-minute default.
func TestHostRunnerTimeoutDefaults(t *testing.T) {
	runner := NewHostRunner(0, "")
	assert.Equal(t, DefaultCommandTimeout, runner.timeout)
	assert.Equal(t, DefaultApprovalTimeout, runner.approvalTimeout)

	// Explicit zero/negative values also fall back to defaults.
	runner.SetTimeouts(HostTimeouts{Command: 0, Approval: -time.Second})
	assert.Equal(t, DefaultCommandTimeout, runner.timeout)
	assert.Equal(t, DefaultApprovalTimeout, runner.approvalTimeout)
}

// An approval request that is never answered must fail with the
// approval-timeout error after approval_timeout instead of blocking forever.
func TestHostRunnerApprovalTimeout(t *testing.T) {
	runner := NewHostRunner(0, "")
	runner.SetTimeouts(HostTimeouts{Approval: 100 * time.Millisecond})
	msgChan := make(chan Msg, 10)
	runner.SetMessageChannel(msgChan)

	start := time.Now()
	_, err := runner.Run(context.Background(), Input{
		Command:        "echo hello",
		BypassApproval: false,
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "approval timed out after")
	assert.Contains(t, err.Error(), "echo hello")
	assert.Less(t, elapsed, 10*time.Second, "approval wait should return at the deadline, not hang")
}

// An approval arriving before the deadline still succeeds.
func TestHostRunnerApprovalBeforeDeadline(t *testing.T) {
	runner := NewHostRunner(0, "")
	runner.SetTimeouts(HostTimeouts{Approval: 5 * time.Second})
	msgChan := make(chan Msg, 10)
	runner.SetMessageChannel(msgChan)

	go func() {
		select {
		case msg := <-msgChan:
			approvalReq, ok := msg.(ApprovalRequestMsg)
			require.True(t, ok, "Expected ApprovalRequestMsg, got %T", msg)
			time.Sleep(50 * time.Millisecond)
			approvalReq.ResponseChan <- true
		case <-time.After(5 * time.Second):
			t.Error("Timeout waiting for approval request")
		}
	}()

	output, err := runner.Run(context.Background(), Input{
		Command:        "echo hello",
		BypassApproval: false,
	})

	require.NoError(t, err)
	assert.Contains(t, output.Output, "hello")
	assert.Equal(t, "0", output.ExitCode)
}

// TestHostRunnerSetRepoInfoRoutesToWorktree proves the isolation lever reaches
// the host execution layer: after SetRepoInfo with a WorktreePath, host commands
// run inside the worktree, not the main tree.
func TestHostRunnerSetRepoInfoRoutesToWorktree(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".agents", "worktrees", "e42")
	require.NoError(t, os.MkdirAll(wt, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wt, "marker.txt"), []byte("in-worktree"), 0o644))

	runner := NewHostRunner(0, root)

	// Before: commands resolve against the main tree (marker absent).
	out, err := runner.Run(context.Background(), Input{Command: "cat marker.txt", BypassApproval: true})
	require.NoError(t, err)
	assert.NotEqual(t, "0", out.ExitCode, "marker must not exist on the main tree")

	// After: the runner is re-targeted at the edict's worktree.
	runner.SetRepoInfo(repo.RepoInfo{
		ProjectRoot:  root,
		WorktreePath: filepath.Join(".agents", "worktrees", "e42"),
		IsWorktree:   true,
	})
	out, err = runner.Run(context.Background(), Input{Command: "cat marker.txt", BypassApproval: true})
	require.NoError(t, err)
	assert.Equal(t, "0", out.ExitCode)
	assert.Contains(t, out.Output, "in-worktree")
}
