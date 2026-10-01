package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/afittestide/asimi/internal/runners"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	hostChecker := func(cmd string) (bool, bool) { return true, false }

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

func TestRunShellCommandSandboxMissingFallback(t *testing.T) {
	// When the sandbox is missing, we fall back to host execution
	var runnerCalls int
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			runnerCalls++
			return runners.Output{}, runners.SandboxMissingError{}
		},
	}

	tool := NewRunShellCommand(nil, mockRunner, nil, t.TempDir())

	result, err := tool.Call(context.Background(), `{"command":"echo hello","description":"test"}`)
	require.NoError(t, err)

	// The sandbox runner was called once
	assert.Equal(t, 1, runnerCalls)

	// Output comes from host fallback
	var output runners.Output
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Contains(t, output.Output, "hello")
	assert.Equal(t, "0", output.ExitCode)
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
	hostChecker := func(cmd string) (bool, bool) { return true, true }

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

func TestRunShellCommandSandboxMissingFallbackWithMsgChan(t *testing.T) {
	// When msgChan is provided and sandbox is missing,
	// the fallback host runner requests approval via msgChan.
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			return runners.Output{}, runners.SandboxMissingError{}
		},
	}

	ch := make(chan runners.Msg, 1)
	var msgChan chan<- runners.Msg = ch
	tool := NewRunShellCommand(nil, mockRunner, &msgChan, t.TempDir())

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

// TestRunShellCommandSandboxMissingFallback_NonNilPointerNilChannel tests the
// critical production path: buildToolRegistry passes &s.msgChan (non-nil pointer)
// but before Subscribe(), s.msgChan is still nil. If a sandbox-missing fallback
// fires in this state, bypassApproval must be true — the command must NOT block
// waiting for approval on a nil channel.
func TestRunShellCommandSandboxMissingFallback_NonNilPointerNilChannel(t *testing.T) {
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			return runners.Output{}, runners.SandboxMissingError{}
		},
	}

	// Non-nil pointer to a nil channel — the exact state after buildToolRegistry
	// but before Subscribe(). bypassApproval must be true so the host runner
	// doesn't block on a nil channel.
	var msgChan chan<- runners.Msg // nil channel
	tool := NewRunShellCommand(nil, mockRunner, &msgChan, t.TempDir())

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
		// Success — command ran on host with bypassApproval=true, no hang.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out — command likely blocked waiting for approval on nil channel (bypassApproval was not set)")
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

	hostChecker := func(cmd string) (bool, bool) { return true, false }

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
	hostChecker := func(cmd string) (bool, bool) { return true, false }
	hostTool := NewRunShellCommand(hostChecker, nil, nil, t.TempDir(), getTimeouts)

	result, err := hostTool.Call(context.Background(), `{"command":"sleep infinity","description":"test"}`)
	require.NoError(t, err)
	var output runners.Output
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Equal(t, "124", output.ExitCode, "runOnHost path should hit the 100ms command deadline")
	assert.Contains(t, output.Output, "Command timed out after")

	// SandboxSetupMissingError fallback path (nil msgChan → bypass approval)
	mockRunner := &mockRunner{
		runFn: func(ctx context.Context, input runners.Input) (runners.Output, error) {
			return runners.Output{}, runners.SandboxSetupMissingError{}
		},
	}
	fallbackTool := NewRunShellCommand(nil, mockRunner, nil, t.TempDir(), getTimeouts)
	result, err = fallbackTool.Call(context.Background(), `{"command":"sleep infinity","description":"test"}`)
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
	hostChecker := func(cmd string) (bool, bool) { return true, false }
	tool := NewRunShellCommand(hostChecker, nil, nil, t.TempDir(), getTimeouts)

	// First call — no configured deadline, sleep runs to completion (or at
	// least isn't cut at 100ms).
	result, err := tool.Call(context.Background(), `{"command":"sleep 0.5","description":"test"}`)
	require.NoError(t, err)
	var output runners.Output
	require.NoError(t, json.Unmarshal([]byte(result), &output))
	assert.Equal(t, "0", output.ExitCode, "first call should use zero timeouts (runner defaults), not the later value")

	// Second call — getter now returns a tight deadline.
	result, err = tool.Call(context.Background(), `{"command":"sleep infinity","description":"test"}`)
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
	hostChecker := func(cmd string) (bool, bool) { return true, false }
	tool := NewRunShellCommand(hostChecker, nil, nil, t.TempDir())

	hostRunner := tool.newHostRunner()
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
