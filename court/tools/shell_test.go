package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errHostExecRefused stands in for the Court's typed refusal
// (court.HostExecRefusedError). The tools package does not import court, so the
// tool-level contract is only that a non-nil error from the host checker is
// propagated unchanged and stops execution.
var errHostExecRefused = errors.New("refusing to run on the host: no host-execution grant")

func TestRunShellCommandUsesLocalRunner(t *testing.T) {
	// mockRunner is a per-court runner that records calls
	type call struct {
		cmd string
	}
	var calls []call
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			calls = append(calls, call{cmd: input.Command})
			return runners.Output{Output: "mock output", ExitCode: "0"}, nil
		},
	}

	tool := NewRunShellCommand(nil, mockRunner, nil, "")
	require.NotNil(t, tool)

	result, err := tool.Call(context.Background(), `{"command":"echo hello","description":"test"}`)
	require.NoError(t, err)

	// The mock runner was called with the right command
	require.Len(t, calls, 1)
	assert.Equal(t, "echo hello", calls[0].cmd)

	// The output comes from the mock, not from a global runner
	var output runners.Output
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Equal(t, "mock output", output.Output)
	assert.Equal(t, "0", output.ExitCode)
}

func TestRunShellCommandNoRunner(t *testing.T) {
	tool := NewRunShellCommand(nil, nil, nil, "")

	_, err := tool.Call(context.Background(), `{"command":"echo hello","description":"test"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no runner configured")
}

func TestRunShellCommandHostOverride(t *testing.T) {
	// When shouldRunOnHost returns true, we use an ephemeral HostRunner
	// regardless of the per-court runner
	var runnerCalls int
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			runnerCalls++
			return runners.Output{Output: "should not be called", ExitCode: "0"}, nil
		},
	}

	// hostChecker returns (runOnHost=true, needsApproval=false)
	hostChecker := func(cmd string) (bool, bool, error) { return true, false, nil }

	tool := NewRunShellCommand(hostChecker, mockRunner, nil, t.TempDir())
	result, err := tool.Call(context.Background(), `{"command":"echo hello","description":"test"}`)
	require.NoError(t, err)

	// The per-court runner should NOT be called
	assert.Equal(t, 0, runnerCalls)

	// Output comes from the real host runner
	var output runners.Output
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Contains(t, output.Output, "hello")
	assert.Equal(t, "0", output.ExitCode)
}

// TestRunShellCommandSandboxMissingFallback pins the failure direction of the
// host fallback (edict 1036, seam 7). A missing sandbox used to mean "run it on
// the host instead", which is host execution granted by nobody: the sandbox
// being absent says nothing about whether the Ruler consented to the host.
//
// With no host-execution grant (here: no checker at all) the sandbox error is
// returned unchanged, so the user sees the actionable "run :init" message
// rather than a silent escape.
func TestRunShellCommandSandboxMissingFallback(t *testing.T) {
	var runnerCalls int
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			runnerCalls++
			return runners.Output{}, runners.SandboxMissingError{}
		},
	}

	tool := NewRunShellCommand(nil, mockRunner, nil, t.TempDir())

	_, err := tool.Call(context.Background(), `{"command":"echo hello","description":"test"}`)
	require.Error(t, err, "an unavailable sandbox must not become an unsandboxed run")
	assert.IsType(t, runners.SandboxMissingError{}, err,
		"the sandbox error must reach the user, not be swallowed by a host fallback")
	assert.Equal(t, 1, runnerCalls)
}

func TestRunShellCommandHostOverrideWithMsgChan(t *testing.T) {
	// When msgChan is provided and host command requires approval,
	// the approval request flows through msgChan.
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			return runners.Output{Output: "should not be called", ExitCode: "0"}, nil
		},
	}

	// hostChecker returns (runOnHost=true, needsApproval=true)
	hostChecker := func(cmd string) (bool, bool, error) { return true, true, nil }

	// Create a message channel to capture approval requests
	ch := make(chan runners.Msg, 1)
	var msgChan chan<- runners.Msg = ch
	tool := NewRunShellCommand(hostChecker, mockRunner, &msgChan, t.TempDir())

	// Run in a goroutine since it blocks waiting for approval
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := tool.Call(context.Background(), `{"command":"echo hello","description":"test"}`)
		require.NoError(t, err)
		var output runners.Output
		require.NoError(t, json.Unmarshal([]byte(result), &output))
		assert.Contains(t, output.Output, "hello")
	}()

	// Wait for the approval request to arrive on msgChan
	select {
	case msg := <-ch:
		// Verify it's an approval request
		approvalMsg, ok := msg.(runners.ApprovalRequestMsg)
		require.True(t, ok, "expected ApprovalRequestMsg, got %T", msg)
		assert.Equal(t, "echo hello", approvalMsg.Command)
		// Approve the command
		approvalMsg.ResponseChan <- true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for approval request")
	}

	<-done
}

// TestRunShellCommandSandboxMissingFallbackWithMsgChan covers the granted case:
// with an approval channel available AND a host-execution grant, the fallback
// runs on the host, and it asks first. The approval channel is what makes the
// host run consensual — it is no longer inferred from the channel's nilness.
func TestRunShellCommandSandboxMissingFallbackWithMsgChan(t *testing.T) {
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			return runners.Output{}, runners.SandboxMissingError{}
		},
	}

	// The grant: this command belongs on the host.
	hostChecker := func(string) (bool, bool, error) { return true, true, nil }

	ch := make(chan runners.Msg, 1)
	var msgChan chan<- runners.Msg = ch
	tool := NewRunShellCommand(hostChecker, mockRunner, &msgChan, t.TempDir())

	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := tool.Call(context.Background(), `{"command":"echo hello","description":"test"}`)
		require.NoError(t, err)
		var output runners.Output
		require.NoError(t, json.Unmarshal([]byte(result), &output))
		assert.Contains(t, output.Output, "hello")
	}()

	// Wait for the approval request
	select {
	case msg := <-ch:
		approvalMsg, ok := msg.(runners.ApprovalRequestMsg)
		require.True(t, ok, "expected ApprovalRequestMsg, got %T", msg)
		assert.Equal(t, "echo hello", approvalMsg.Command)
		approvalMsg.ResponseChan <- true
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for approval request")
	}

	<-done
}

// TestRunShellCommandSandboxMissingFallback_NonNilPointerNilChannel covers the
// state right after buildToolRegistry and before Subscribe(), where the pointer
// to the approval channel is non-nil but the channel itself is nil.
//
// This is where the old code inferred a consent bypass from nilness: it treated
// "nobody can be asked" as "nobody needs to be asked" and ran on the host with
// bypassApproval=true. That is exactly the seam-7 hole, so the fallback now
// refuses and returns the sandbox error — and it must not hang doing it.
func TestRunShellCommandSandboxMissingFallback_NonNilPointerNilChannel(t *testing.T) {
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			return runners.Output{}, runners.SandboxMissingError{}
		},
	}

	var msgChan chan<- runners.Msg // nil channel
	tool := NewRunShellCommand(nil, mockRunner, &msgChan, t.TempDir())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := tool.Call(context.Background(), `{"command":"echo hello","description":"test"}`)
		assert.IsType(t, runners.SandboxMissingError{}, err,
			"with no one to ask, the fallback must refuse rather than bypass approval")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out — the refusal path must not block waiting for approval")
	}
}

// TestRunShellCommandHostOverride_NonNilPointerNilChannel tests the same
// nil-pointer-to-nil-channel scenario but through the host-override path
// (shouldRunOnHost=true, needsApproval=false).
func TestRunShellCommandHostOverride_NonNilPointerNilChannel(t *testing.T) {
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			return runners.Output{Output: "should not be called", ExitCode: "0"}, nil
		},
	}

	hostChecker := func(cmd string) (bool, bool, error) { return true, false, nil }

	var msgChan chan<- runners.Msg // nil channel
	tool := NewRunShellCommand(hostChecker, mockRunner, &msgChan, t.TempDir())

	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := tool.Call(context.Background(), `{"command":"echo hello","description":"test"}`)
		require.NoError(t, err)
		var output runners.Output
		require.NoError(t, json.Unmarshal([]byte(result), &output))
		assert.Contains(t, output.Output, "hello")
		assert.Equal(t, "0", output.ExitCode)
	}()

	select {
	case <-done:
		// Success — host override ran without blocking.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out — host override likely blocked waiting for approval on nil channel")
	}
}

// TestRunShellCommand_PrefersContextRunnerOverRegistered is the isolation
// regression guard: a runner carried in context (the edict's worktree-aware
// runner) must win over the tool's registered Court-wide runner, which is bound
// to the project root. Without this, a ritual step's shell command silently
// mutates the main tree (edict 987).
func TestRunShellCommand_PrefersContextRunnerOverRegistered(t *testing.T) {
	var registeredCalls, contextCalls int
	registered := &mockRunner{runFn: func(context.Context, runners.Input) (runners.Output, error) {
		registeredCalls++
		return runners.Output{Output: "root tree", ExitCode: "0"}, nil
	}}
	contextRunner := &mockRunner{runFn: func(context.Context, runners.Input) (runners.Output, error) {
		contextCalls++
		return runners.Output{Output: "worktree", ExitCode: "0"}, nil
	}}

	tool := NewRunShellCommand(nil, registered, nil, "/registered/root")
	ctx := context.WithValue(context.Background(), RunnerKey{}, contextRunner)

	result, err := tool.Call(ctx, `{"command":"echo hi","description":"test"}`)
	require.NoError(t, err)

	assert.Equal(t, 1, contextCalls, "the context (edict) runner must execute the command")
	assert.Equal(t, 0, registeredCalls, "the registered root runner must not execute the command")

	var output runners.Output
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Equal(t, "worktree", output.Output)
}

// TestRunShellCommand_ContextRepoInfoRoutesHostToWorktree asserts the other
// half of the invariant: when a command is routed to an ephemeral host runner,
// it must run in the edict's effective root (worktree) rather than the tool's
// registered project root.
func TestRunShellCommand_ContextRepoInfoRoutesHostToWorktree(t *testing.T) {
	root := t.TempDir()
	worktreeRel := filepath.Join(".agents", "worktrees", "e42")
	require.NoError(t, os.MkdirAll(filepath.Join(root, worktreeRel), 0o755))

	hostChecker := func(string) (bool, bool, error) { return true, false, nil }
	tool := NewRunShellCommand(hostChecker, nil, nil, root)
	ctx := context.WithValue(context.Background(), RepoInfoKey{}, repo.RepoInfo{
		ProjectRoot:  root,
		WorktreePath: worktreeRel,
		IsWorktree:   true,
	})

	result, err := tool.Call(ctx, `{"command":"pwd","description":"test"}`)
	require.NoError(t, err)

	var output runners.Output
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Equal(t, "0", output.ExitCode)
	assert.Contains(t, output.Output, worktreeRel,
		"host shell execution must land in the edict worktree, not the project root")
}

// mockRunner implements runners.Runner for testing
type mockRunner struct {
	runFn               func(ctx context.Context, input runners.Input) (runners.Output, error)
	restartFn           func(ctx context.Context) error
	closeFn             func(ctx context.Context) error
	runnerType          string
	allowFallbackCalled bool
	msgChan             chan<- runners.Msg
}

func (m *mockRunner) Run(ctx context.Context, input runners.Input) (runners.Output, error) {
	if m.runFn != nil {
		return m.runFn(ctx, input)
	}
	return runners.Output{}, nil
}

func (m *mockRunner) Restart(ctx context.Context) error {
	if m.restartFn != nil {
		return m.restartFn(ctx)
	}
	return nil
}

func (m *mockRunner) Close(ctx context.Context) error {
	if m.closeFn != nil {
		return m.closeFn(ctx)
	}
	return nil
}

func (m *mockRunner) AllowFallback(allow bool) {
	m.allowFallbackCalled = allow
}

func (m *mockRunner) RunnerType() string {
	if m.runnerType != "" {
		return m.runnerType
	}
	return "mock"
}

func (m *mockRunner) GetOS() string {
	return runtime.GOOS
}

func (m *mockRunner) SetMessageChannel(msgChan chan<- runners.Msg) {
	m.msgChan = msgChan
}

func (m *mockRunner) HealthCheck(ctx context.Context) error {
	return nil
}

// TestRunShellCommandTimeoutsReachHostRunner asserts the configured
// HostTimeouts propagate into every ephemeral HostRunner path — including
// the SandboxSetupMissingError fallback, which previously bypassed the
// newHostRunner() helper and silently used the 10m/2m defaults.
func TestRunShellCommandTimeoutsReachHostRunner(t *testing.T) {
	timeouts := runners.HostTimeouts{Command: 100 * time.Millisecond, Approval: 100 * time.Millisecond}
	getTimeouts := func() runners.HostTimeouts { return timeouts }

	// hostChecker routes every command to the host without approval
	hostChecker := func(cmd string) (bool, bool, error) { return true, false, nil }
	hostTool := NewRunShellCommand(hostChecker, nil, nil, t.TempDir(), getTimeouts)

	// sleep 5, not `sleep infinity`: BSD sleep rejects `infinity`. 5s is
	// far beyond the 100ms deadline yet bounded if the deadline ever breaks.
	result, err := hostTool.Call(context.Background(), `{"command":"sleep 5","description":"test"}`)
	require.NoError(t, err)
	var output runners.Output
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Equal(t, "124", output.ExitCode, "runOnHost path should hit the 100ms command deadline")
	assert.Contains(t, output.Output, "Command timed out after")

	// SandboxSetupMissingError fallback path, reached only with a host-execution
	// grant (seam 7). Timeouts must reach the ephemeral runner on this path too.
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			return runners.Output{}, runners.SandboxSetupMissingError{}
		},
	}
	grantHostExec := func(string) (bool, bool, error) { return true, false, nil }
	fallbackTool := NewRunShellCommand(grantHostExec, mockRunner, nil, t.TempDir(), getTimeouts)
	result, err = fallbackTool.Call(context.Background(), `{"command":"sleep 5","description":"test"}`)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Equal(t, "124", output.ExitCode, "fallback path should hit the 100ms command deadline")
	assert.Contains(t, output.Output, "Command timed out after")
}

// TestRunShellCommand_TimeoutsGetterInvokedAtCallTime asserts the lazy
// timeout getter is re-read on each ephemeral host runner creation, not
// snapshotted at tool construction.
func TestRunShellCommand_TimeoutsGetterInvokedAtCallTime(t *testing.T) {
	var calls int
	getTimeouts := func() runners.HostTimeouts {
		calls++
		// First call: default (no deadline). Second call: tight deadline.
		if calls == 1 {
			return runners.HostTimeouts{}
		}
		return runners.HostTimeouts{Command: 100 * time.Millisecond}
	}
	hostChecker := func(cmd string) (bool, bool, error) { return true, false, nil }
	tool := NewRunShellCommand(hostChecker, nil, nil, t.TempDir(), getTimeouts)

	// First call — no configured deadline, sleep runs to completion (or at
	// least isn't cut at 100ms).
	result, err := tool.Call(context.Background(), `{"command":"sleep 0.5","description":"test"}`)
	require.NoError(t, err)
	var output runners.Output
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Equal(t, "0", output.ExitCode, "first call should use zero timeouts (runner defaults), not the later value")

	// Second call — getter now returns a tight deadline.
	result, err = tool.Call(context.Background(), `{"command":"sleep 5","description":"test"}`)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Equal(t, "124", output.ExitCode, "second call should hit the freshly configured 100ms command deadline")
	assert.Contains(t, output.Output, "Command timed out after")
	assert.GreaterOrEqual(t, calls, 2, "getter should have been invoked at Call time")
}

// TestRunShellCommand_NilTimeoutsGetterKeepsRunnerDefaults asserts the
// zero-argument constructor path: a nil timeouts getter must leave the
// ephemeral host runner on its constructor defaults (10m command / 2m
// approval), and must not panic — newHostRunner guards SetTimeouts behind
// a nil check, so invoking the getter under a nil guard would crash.
func TestRunShellCommand_NilTimeoutsGetterKeepsRunnerDefaults(t *testing.T) {
	hostChecker := func(cmd string) (bool, bool, error) { return true, false, nil }
	tool := NewRunShellCommand(hostChecker, nil, nil, t.TempDir())

	hostRunner := tool.newHostRunner(t.TempDir())
	require.NotNil(t, hostRunner, "newHostRunner should always return a runner")

	// runner is a *runners.HostRunner from another package — read its
	// unexported deadline fields via the test-only unsafe-read idiom
	// (reflect.New(...).Elem() gives an addressable value whose fields
	// can be read even unexported).
	runnerVal := reflect.ValueOf(hostRunner).Elem()
	timeoutField := runnerVal.FieldByName("timeout")
	approvalField := runnerVal.FieldByName("approvalTimeout")
	require.True(t, timeoutField.IsValid(), "HostRunner should have timeout field")
	require.True(t, approvalField.IsValid(), "HostRunner should have approvalTimeout field")
	assert.Equal(t, runners.DefaultCommandTimeout, *(*time.Duration)(unsafe.Pointer(timeoutField.UnsafeAddr())),
		"nil getter must not override the runner's default command deadline")
	assert.Equal(t, runners.DefaultApprovalTimeout, *(*time.Duration)(unsafe.Pointer(approvalField.UnsafeAddr())),
		"nil getter must not override the runner's default approval deadline")

	// The guarded getter path must run without panicking.
	result, err := tool.Call(context.Background(), `{"command":"echo asimi","description":"test"}`)
	require.NoError(t, err)
	var output runners.Output
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Equal(t, "0", output.ExitCode, "command should run to completion under runner defaults")
}

// TestRunShellCommandRefusesHostWithoutGrant is seam 7's tool-level guarantee
// (edict 1036): when the host checker refuses (the session's mandate carries no
// host-execution grant), the command must not run anywhere. A refusal is not a
// routing decision — falling through to the sandbox runner would silently
// downgrade it, and running it on the host would be the very escape the gate
// exists to stop.
func TestRunShellCommandRefusesHostWithoutGrant(t *testing.T) {
	// The sandbox reports itself missing, which is the state that makes
	// shell.go re-run a command on the host. The refusal must happen before
	// that: a refusal caught by a fallback path would be re-run with strictly
	// more authority than the mandate granted — the same mistake the void
	// mandate had to avoid.
	var sandboxCalls int
	sandboxRunner := &mockRunner{
		runFn: func(context.Context, runners.Input) (runners.Output, error) {
			sandboxCalls++
			return runners.Output{}, runners.SandboxMissingError{}
		},
	}

	hostChecker := func(cmd string) (bool, bool, error) {
		return false, false, errHostExecRefused
	}

	tool := NewRunShellCommand(hostChecker, sandboxRunner, nil, t.TempDir())
	result, err := tool.Call(context.Background(), `{"command":"echo hello","description":"test"}`)

	require.Error(t, err, "an ungranted host command must be refused")
	assert.ErrorIs(t, err, errHostExecRefused, "the typed refusal must reach the caller unchanged")
	assert.Equal(t, "", result, "no command output may be produced for a refused command")
	assert.Zero(t, sandboxCalls,
		"the refusal must be decided before the sandbox runs, so no fallback can catch it")
}
