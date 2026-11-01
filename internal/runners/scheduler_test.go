package runners

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockTool struct {
	name        string
	description string
	callFunc    func(ctx context.Context, input string) (string, error)
}

func (t *mockTool) Name() string {
	return t.name
}

func (t *mockTool) Description() string {
	return t.description
}

func (t *mockTool) Call(ctx context.Context, input string) (string, error) {
	if t.callFunc != nil {
		return t.callFunc(ctx, input)
	}
	return "", nil
}

func (t *mockTool) Format(input, result string, err error) string {
	return t.name + ": " + result
}

func (t *mockTool) ParameterSchema() map[string]any {
	return nil
}

func TestCoreToolScheduler(t *testing.T) {
	var msgs []any
	var msgMu sync.Mutex
	var terminalNotified atomic.Bool
	scheduler := NewCoreToolScheduler(func(msg any) {
		msgMu.Lock()
		msgs = append(msgs, msg)
		msgMu.Unlock()
		if _, ok := msg.(ToolCallSuccessMsg); ok {
			terminalNotified.Store(true)
		}
	})

	tool := &mockTool{
		name:        "test-tool",
		description: "A tool for testing",
		callFunc: func(ctx context.Context, input string) (string, error) {
			time.Sleep(10 * time.Millisecond)
			return "output for " + input, nil
		},
	}

	resultChan := scheduler.Schedule(context.Background(), tool, "test-input")

	result := <-resultChan

	// Ordering invariant: the terminal success notification must have been
	// dispatched before the result channel signalled.
	assert.True(t, terminalNotified.Load(),
		"ToolCallSuccessMsg must be dispatched before the result channel yields")

	assert.NoError(t, result.Error)
	assert.Equal(t, "output for test-input", result.Output)

	msgMu.Lock()
	defer msgMu.Unlock()
	assert.GreaterOrEqual(t, len(msgs), 3)
	_, ok := msgs[0].(ToolCallScheduledMsg)
	assert.True(t, ok)
	_, ok = msgs[1].(ToolCallExecutingMsg)
	assert.True(t, ok)
	_, ok = msgs[2].(ToolCallSuccessMsg)
	assert.True(t, ok)
}

// TestCoreToolScheduler_ErrorOrdering verifies the same ordering invariant for
// the error arm: ToolCallErrorMsg is dispatched before the result channel yields.
func TestCoreToolScheduler_ErrorOrdering(t *testing.T) {
	var msgMu sync.Mutex
	var msgs []any
	var terminalNotified atomic.Bool
	scheduler := NewCoreToolScheduler(func(msg any) {
		msgMu.Lock()
		msgs = append(msgs, msg)
		msgMu.Unlock()
		if _, ok := msg.(ToolCallErrorMsg); ok {
			terminalNotified.Store(true)
		}
	})

	failingTool := &mockTool{
		name:        "error-tool",
		description: "A tool that fails",
		callFunc: func(ctx context.Context, input string) (string, error) {
			time.Sleep(10 * time.Millisecond)
			return "", assert.AnError
		},
	}

	resultChan := scheduler.Schedule(context.Background(), failingTool, "bad-input")

	result := <-resultChan

	assert.True(t, terminalNotified.Load(),
		"ToolCallErrorMsg must be dispatched before the result channel yields")

	assert.Error(t, result.Error)
	assert.ErrorIs(t, result.Error, assert.AnError)

	msgMu.Lock()
	defer msgMu.Unlock()
	require.GreaterOrEqual(t, len(msgs), 3)
	_, ok := msgs[0].(ToolCallScheduledMsg)
	assert.True(t, ok)
	_, ok = msgs[1].(ToolCallExecutingMsg)
	assert.True(t, ok)
	_, ok = msgs[2].(ToolCallErrorMsg)
	assert.True(t, ok)
}

// TestCoreToolScheduler_ClearQueueOrdering verifies that aborted calls deliver
// their notifications before the result channel yields.
func TestCoreToolScheduler_ClearQueueOrdering(t *testing.T) {
	var msgMu sync.Mutex
	aborted := make(map[string]bool)
	scheduler := NewCoreToolScheduler(func(msg any) {
		msgMu.Lock()
		if m, ok := msg.(ToolCallAbortedMsg); ok {
			aborted[m.CallID] = true
		}
		msgMu.Unlock()
	})

	slowTool := &mockTool{
		name: "slow-tool",
		callFunc: func(ctx context.Context, input string) (string, error) {
			time.Sleep(500 * time.Millisecond)
			return "done", nil
		},
	}

	r1 := scheduler.Schedule(context.Background(), slowTool, "a")
	time.Sleep(20 * time.Millisecond)
	r2 := scheduler.Schedule(context.Background(), slowTool, "b")

	scheduler.ClearQueue()

	for _, ch := range []<-chan ToolCallResult{r1, r2} {
		res := <-ch
		_, ok := res.Error.(SandboxRestartedError)
		assert.True(t, ok)
		msgMu.Lock()
		assert.NotEmpty(t, aborted, "aborted notification must be dispatched before the result channel yields")
		msgMu.Unlock()
	}
}

func TestCoreToolScheduler_ClearQueue(t *testing.T) {
	var abortedCalls []ToolCallAbortedMsg
	var abortedMu sync.Mutex

	scheduler := NewCoreToolScheduler(func(msg any) {
		if aborted, ok := msg.(ToolCallAbortedMsg); ok {
			abortedMu.Lock()
			abortedCalls = append(abortedCalls, aborted)
			abortedMu.Unlock()
		}
	})

	slowTool := &mockTool{
		name:        "slow-tool",
		description: "A slow tool",
		callFunc: func(ctx context.Context, input string) (string, error) {
			time.Sleep(500 * time.Millisecond)
			return "done", nil
		},
	}

	fastTool := &mockTool{
		name:        "fast-tool",
		description: "A fast tool",
		callFunc: func(ctx context.Context, input string) (string, error) {
			return "fast", nil
		},
	}

	// Schedule the slow tool first (it will start executing)
	slowResult := scheduler.Schedule(context.Background(), slowTool, "slow-input")

	// Give it a moment to start executing
	time.Sleep(10 * time.Millisecond)

	// Schedule multiple fast tools (they will be queued)
	fastResult1 := scheduler.Schedule(context.Background(), fastTool, "fast-input-1")
	fastResult2 := scheduler.Schedule(context.Background(), fastTool, "fast-input-2")
	fastResult3 := scheduler.Schedule(context.Background(), fastTool, "fast-input-3")

	// Clear the queue — aborts all calls: the active slow tool + 3 queued fast tools
	abortedCount := scheduler.ClearQueue()

	assert.Equal(t, 4, abortedCount, "should have aborted 1 active + 3 queued tool calls")

	for _, result := range []<-chan ToolCallResult{fastResult1, fastResult2, fastResult3, slowResult} {
		res := <-result
		assert.Error(t, res.Error)
		_, ok := res.Error.(SandboxRestartedError)
		assert.True(t, ok, "error should be SandboxRestartedError")
	}

	abortedMu.Lock()
	defer abortedMu.Unlock()
	assert.Equal(t, 4, len(abortedCalls), "should have received 4 aborted notifications")

	toolNames := make(map[string]int)
	for _, aborted := range abortedCalls {
		assert.Equal(t, string(StatusAborted), aborted.Status)
		toolNames[aborted.ToolName]++
	}
	assert.Equal(t, 1, toolNames["slow-tool"], "should have 1 aborted slow-tool notification")
	assert.Equal(t, 3, toolNames["fast-tool"], "should have 3 aborted fast-tool notifications")
}

func TestCoreToolScheduler_ClearQueue_EmptyQueue(t *testing.T) {
	scheduler := NewCoreToolScheduler(nil)

	abortedCount := scheduler.ClearQueue()
	assert.Equal(t, 0, abortedCount, "clearing empty queue should return 0")
}

// TestCoreToolScheduler_ConcurrentDispatch verifies that multiple tool calls
// run concurrently (up to maxConcurrency) rather than serially.
func TestCoreToolScheduler_ConcurrentDispatch(t *testing.T) {
	scheduler := NewCoreToolScheduler(nil)

	var concurrentCount int32
	var maxConcurrent int32

	started := make(chan struct{})
	var startedOnce sync.Once

	tool := &mockTool{
		name: "concurrent-tool",
		callFunc: func(ctx context.Context, input string) (string, error) {
			cur := atomic.AddInt32(&concurrentCount, 1)
			for {
				max := atomic.LoadInt32(&maxConcurrent)
				if cur <= max || atomic.CompareAndSwapInt32(&maxConcurrent, max, cur) {
					break
				}
			}
			startedOnce.Do(func() { close(started) })
			time.Sleep(50 * time.Millisecond)
			atomic.AddInt32(&concurrentCount, -1)
			return input, nil
		},
	}

	// Schedule 4 tools — all should run concurrently
	chans := make([]<-chan ToolCallResult, 4)
	for i := 0; i < 4; i++ {
		chans[i] = scheduler.Schedule(context.Background(), tool, string(rune('A'+i)))
	}

	// Wait until at least one tool starts so we know dispatch has begun
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tools did not start within timeout")
	}

	// Wait for all to complete
	for _, ch := range chans {
		<-ch
	}

	// maxConcurrent should be >= 2 (with maxConcurrency=4, all 4 should run at once)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&maxConcurrent), int32(2),
		"at least 2 tools should have run concurrently")
}

// TestCoreToolScheduler_ConcurrentClearQueueWithMultipleActive verifies that
// ClearQueue correctly aborts multiple in-flight calls (not just one).
func TestCoreToolScheduler_ConcurrentClearQueueWithMultipleActive(t *testing.T) {
	scheduler := NewCoreToolScheduler(nil)

	slowTool := &mockTool{
		name: "slow-tool",
		callFunc: func(ctx context.Context, input string) (string, error) {
			time.Sleep(500 * time.Millisecond)
			return "done", nil
		},
	}

	// Schedule 3 slow tools — with maxConcurrency=4, all should start immediately
	r1 := scheduler.Schedule(context.Background(), slowTool, "a")
	r2 := scheduler.Schedule(context.Background(), slowTool, "b")
	r3 := scheduler.Schedule(context.Background(), slowTool, "c")

	// Give them time to start
	time.Sleep(20 * time.Millisecond)

	abortedCount := scheduler.ClearQueue()
	assert.Equal(t, 3, abortedCount, "should have aborted 3 active calls")

	for _, ch := range []<-chan ToolCallResult{r1, r2, r3} {
		res := <-ch
		_, ok := res.Error.(SandboxRestartedError)
		assert.True(t, ok, "error should be SandboxRestartedError")
	}
}

// TestCoreToolScheduler_AbortCancelsContext verifies that ClearQueue cancels
// the context of in-flight tool calls, allowing tools to detect cancellation.
func TestCoreToolScheduler_AbortCancelsContext(t *testing.T) {
	scheduler := NewCoreToolScheduler(nil)

	cancelled := make(chan struct{})

	tool := &mockTool{
		name: "ctx-tool",
		callFunc: func(ctx context.Context, input string) (string, error) {
			<-ctx.Done()
			close(cancelled)
			return "", ctx.Err()
		},
	}

	scheduler.Schedule(context.Background(), tool, "test")
	time.Sleep(20 * time.Millisecond) // let it start

	scheduler.ClearQueue()

	select {
	case <-cancelled:
		// success — context was cancelled
	case <-time.After(2 * time.Second):
		t.Fatal("tool context was not cancelled within timeout")
	}
}

// TestCoreToolScheduler_QueueExceedsMaxConcurrency verifies that when more
// tools are scheduled than maxConcurrency, excess tools are queued and
// dispatched as active ones complete.
func TestCoreToolScheduler_QueueExceedsMaxConcurrency(t *testing.T) {
	scheduler := NewCoreToolScheduler(nil)
	scheduler.maxConcurrency = 2

	var concurrentCount int32
	var maxConcurrent int32
	var orderMu sync.Mutex
	var executionOrder []string

	tool := &mockTool{
		name: "ordered-tool",
		callFunc: func(ctx context.Context, input string) (string, error) {
			cur := atomic.AddInt32(&concurrentCount, 1)
			for {
				max := atomic.LoadInt32(&maxConcurrent)
				if cur <= max || atomic.CompareAndSwapInt32(&maxConcurrent, max, cur) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&concurrentCount, -1)
			orderMu.Lock()
			executionOrder = append(executionOrder, input)
			orderMu.Unlock()
			return input, nil
		},
	}

	// Schedule 4 tools with maxConcurrency=2
	chans := make([]<-chan ToolCallResult, 4)
	for i := 0; i < 4; i++ {
		chans[i] = scheduler.Schedule(context.Background(), tool, string(rune('A'+i)))
	}

	// Wait for all to complete
	for _, ch := range chans {
		<-ch
	}

	orderMu.Lock()
	defer orderMu.Unlock()
	require.Len(t, executionOrder, 4, "all 4 tools should have executed")

	// At no point should more than maxConcurrency (2) tools run simultaneously
	assert.LessOrEqual(t, atomic.LoadInt32(&maxConcurrent), int32(2),
		"concurrent execution should never exceed maxConcurrency=2")
	assert.GreaterOrEqual(t, atomic.LoadInt32(&maxConcurrent), int32(1),
		"at least 1 tool should have run")
}
