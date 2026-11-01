package runners

import (
	"context"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

// defaultMaxConcurrency is the default concurrency limit for the scheduler.
const defaultMaxConcurrency = 4

// Tool defines a tool that can be invoked by the scheduler.
type Tool interface {
	Name() string
	Description() string
	Call(ctx context.Context, input string) (string, error)
	Format(input, result string, err error) string
	ParameterSchema() map[string]any
}

// ToolCallStatus represents the status of a tool call
type ToolCallStatus string

const (
	StatusValidating         ToolCallStatus = "validating"
	StatusScheduled          ToolCallStatus = "scheduled"
	StatusExecuting          ToolCallStatus = "executing"
	StatusWaitingForApproval ToolCallStatus = "awaiting_approval"
	StatusSuccess            ToolCallStatus = "success"
	StatusError              ToolCallStatus = "error"
	StatusCancelled          ToolCallStatus = "cancelled"
	StatusAborted            ToolCallStatus = "aborted"
)

// ToolCall represents a single tool call task
type ToolCall struct {
	ID     string
	Ctx    context.Context
	Cancel context.CancelFunc
	Tool   Tool
	Input  string
	Status ToolCallStatus
	Result string
	Error  error
}

// ToolCallResult is used to send the result of a tool call back to the caller
type ToolCallResult struct {
	Output string
	Error  error
}

// CoreToolScheduler manages a queue of tool calls and orchestrates their
// concurrent execution with in-flight tracking for abort support.
type CoreToolScheduler struct {
	mu             sync.Mutex
	toolCalls      map[string]*ToolCall
	queue          []*ToolCall
	activeCalls    map[string]*ToolCall
	maxConcurrency int
	resultChans    map[string]chan ToolCallResult
	notify         func(any)
	channelID      string
}

// NewCoreToolScheduler creates a new CoreToolScheduler
func NewCoreToolScheduler(toolNotify func(any)) *CoreToolScheduler {
	return &CoreToolScheduler{
		toolCalls:      make(map[string]*ToolCall),
		queue:          make([]*ToolCall, 0),
		activeCalls:    make(map[string]*ToolCall),
		maxConcurrency: defaultMaxConcurrency,
		resultChans:    make(map[string]chan ToolCallResult),
		notify:         toolNotify,
	}
}

// SetNotify sets the notification function and channel ID for tool call status updates.
func (s *CoreToolScheduler) SetNotify(notify func(any), channelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notify = notify
	s.channelID = channelID
}

// sendNotifications dispatches notifications outside the scheduler mutex
// to avoid deadlocks if the callback blocks.
func (s *CoreToolScheduler) sendNotifications(msgs []any) {
	if s.notify == nil {
		return
	}
	for _, msg := range msgs {
		s.notify(msg)
	}
}

// Schedule adds a new tool call to the scheduler and returns a channel for the result
func (s *CoreToolScheduler) Schedule(ctx context.Context, tool Tool, input string) <-chan ToolCallResult {
	slog.Debug("scheduler.enqueue", "tool", tool.Name())
	s.mu.Lock()

	id := uuid.New().String()
	callCtx, cancel := context.WithCancel(ctx)
	call := &ToolCall{
		ID:     id,
		Ctx:    callCtx,
		Cancel: cancel,
		Tool:   tool,
		Input:  input,
		Status: StatusScheduled,
	}
	s.toolCalls[id] = call
	s.queue = append(s.queue, call)

	resultChan := make(chan ToolCallResult, 1)
	s.resultChans[id] = resultChan

	var notifications []any
	if s.notify != nil {
		notifications = append(notifications, ToolCallScheduledMsg{
			ChannelID: s.channelID,
			CallID:    id,
			ToolName:  call.Tool.Name(),
			Input:     call.Input,
			Status:    string(StatusScheduled),
			Formatted: call.Tool.Format(call.Input, "", nil),
		})
	}
	notifications = append(notifications, s.processQueue()...)
	s.mu.Unlock()

	s.sendNotifications(notifications)
	return resultChan
}

// processQueue dispatches up to maxConcurrency calls simultaneously.
// Must be called with s.mu held. Returns notifications to send after unlock.
func (s *CoreToolScheduler) processQueue() []any {
	var notifications []any
	for len(s.queue) > 0 && len(s.activeCalls) < s.maxConcurrency {
		call := s.queue[0]
		s.queue = s.queue[1:]
		s.activeCalls[call.ID] = call

		call.Status = StatusExecuting
		if s.notify != nil {
			notifications = append(notifications, ToolCallExecutingMsg{
				ChannelID: s.channelID,
				CallID:    call.ID,
				ToolName:  call.Tool.Name(),
				Input:     call.Input,
				Status:    string(StatusExecuting),
				Formatted: call.Tool.Format(call.Input, "", nil),
			})
		}

		go s.executeCall(call)
	}
	return notifications
}

// executeCall runs a single tool call and handles its result.
func (s *CoreToolScheduler) executeCall(call *ToolCall) {
	slog.Debug("scheduler.exec", "tool", call.Tool.Name())
	output, err := call.Tool.Call(call.Ctx, call.Input)

	s.mu.Lock()

	resultChan := s.resultChans[call.ID]
	var notifications []any

	if err != nil {
		call.Status = StatusError
		call.Error = err
		if s.notify != nil {
			notifications = append(notifications, ToolCallErrorMsg{
				ChannelID: s.channelID,
				CallID:    call.ID,
				ToolName:  call.Tool.Name(),
				Input:     call.Input,
				Status:    string(StatusError),
				Error:     err.Error(),
				Formatted: call.Tool.Format(call.Input, "", err),
			})
		}
	} else {
		call.Status = StatusSuccess
		call.Result = output
		if s.notify != nil {
			notifications = append(notifications, ToolCallSuccessMsg{
				ChannelID: s.channelID,
				CallID:    call.ID,
				ToolName:  call.Tool.Name(),
				Input:     call.Input,
				Status:    string(StatusSuccess),
				Result:    output,
				Formatted: call.Tool.Format(call.Input, output, nil),
			})
		}
	}
	if resultChan != nil {
		delete(s.resultChans, call.ID)
	}

	delete(s.activeCalls, call.ID)
	delete(s.toolCalls, call.ID)
	notifications = append(notifications, s.processQueue()...)
	s.mu.Unlock()

	// Dispatch the terminal notification before signalling the result channel so
	// that a consumer which receives the result is guaranteed the complete
	// terminal message sequence (scheduled, executing, success/error) is already
	// visible. sendNotifications stays outside the mutex.
	s.sendNotifications(notifications)

	if resultChan != nil {
		resultChan <- ToolCallResult{Output: output, Error: err}
		close(resultChan)
	}
}

// SandboxRestartedError is returned when a tool call is aborted due to sandbox restart
type SandboxRestartedError struct{}

func (e SandboxRestartedError) Error() string {
	return "sandbox restarted - tool call aborted"
}

// ClearQueue aborts all pending and in-flight tool calls. This should be called
// when the sandbox needs to be reinitialized (e.g., after a timeout).
// It returns the number of aborted calls.
func (s *CoreToolScheduler) ClearQueue() int {
	s.mu.Lock()

	abortedCount := len(s.queue) + len(s.activeCalls)
	if abortedCount == 0 {
		slog.Debug("scheduler.clear_queue", "aborted", 0)
		s.mu.Unlock()
		return 0
	}

	slog.Info("scheduler.clear_queue", "aborting", abortedCount)

	abortErr := SandboxRestartedError{}
	var notifications []any
	// Result channels are signalled only after the abort notifications have been
	// dispatched (below the mutex), so consumers observe the complete terminal
	// message sequence before the result channel yields.
	var resultChans []chan ToolCallResult

	// Abort all active (in-flight) calls via context cancellation
	for _, call := range s.activeCalls {
		call.Status = StatusAborted
		call.Error = abortErr
		if call.Cancel != nil {
			call.Cancel()
		}

		if s.notify != nil {
			notifications = append(notifications, ToolCallAbortedMsg{
				ChannelID: s.channelID,
				CallID:    call.ID,
				ToolName:  call.Tool.Name(),
				Input:     call.Input,
				Status:    string(StatusAborted),
				Reason:    abortErr.Error(),
				Formatted: call.Tool.Format(call.Input, "", abortErr),
			})
		}

		if resultChan, exists := s.resultChans[call.ID]; exists {
			resultChans = append(resultChans, resultChan)
			delete(s.resultChans, call.ID)
		}
		delete(s.toolCalls, call.ID)
	}
	s.activeCalls = make(map[string]*ToolCall)

	// Abort all queued tool calls
	for _, call := range s.queue {
		call.Status = StatusAborted
		call.Error = abortErr

		if s.notify != nil {
			notifications = append(notifications, ToolCallAbortedMsg{
				ChannelID: s.channelID,
				CallID:    call.ID,
				ToolName:  call.Tool.Name(),
				Input:     call.Input,
				Status:    string(StatusAborted),
				Reason:    abortErr.Error(),
				Formatted: call.Tool.Format(call.Input, "", abortErr),
			})
		}

		if resultChan, exists := s.resultChans[call.ID]; exists {
			resultChans = append(resultChans, resultChan)
			delete(s.resultChans, call.ID)
		}
		delete(s.toolCalls, call.ID)
	}
	s.queue = make([]*ToolCall, 0)

	slog.Info("scheduler.queue_cleared", "aborted_count", abortedCount)
	s.mu.Unlock()

	s.sendNotifications(notifications)

	for _, resultChan := range resultChans {
		resultChan <- ToolCallResult{Error: abortErr}
		close(resultChan)
	}
	return abortedCount
}

// Messages for bubbletea
type ToolCallScheduledMsg struct {
	ChannelID string `msgpack:"channel_id"`
	CallID    string `msgpack:"call_id"`
	ToolName  string `msgpack:"tool_name"`
	Input     string `msgpack:"input"`
	Status    string `msgpack:"status"`
	Formatted string `msgpack:"formatted,omitempty"`
}
type ToolCallExecutingMsg struct {
	ChannelID string `msgpack:"channel_id"`
	CallID    string `msgpack:"call_id"`
	ToolName  string `msgpack:"tool_name"`
	Input     string `msgpack:"input"`
	Status    string `msgpack:"status"`
	Formatted string `msgpack:"formatted,omitempty"`
}
type ToolCallWaitingForApprovalMsg struct {
	ChannelID string `msgpack:"channel_id"`
	CallID    string `msgpack:"call_id"`
	ToolName  string `msgpack:"tool_name"`
	Input     string `msgpack:"input"`
	Status    string `msgpack:"status"`
	Command   string `msgpack:"command"`
	Formatted string `msgpack:"formatted,omitempty"`
}
type ToolCallSuccessMsg struct {
	ChannelID string `msgpack:"channel_id"`
	CallID    string `msgpack:"call_id"`
	ToolName  string `msgpack:"tool_name"`
	Input     string `msgpack:"input"`
	Status    string `msgpack:"status"`
	Result    string `msgpack:"result"`
	Formatted string `msgpack:"formatted,omitempty"`
}
type ToolCallErrorMsg struct {
	ChannelID string `msgpack:"channel_id"`
	CallID    string `msgpack:"call_id"`
	ToolName  string `msgpack:"tool_name"`
	Input     string `msgpack:"input"`
	Status    string `msgpack:"status"`
	Error     string `msgpack:"error"`
	Formatted string `msgpack:"formatted,omitempty"`
}
type ToolCallAbortedMsg struct {
	ChannelID string `msgpack:"channel_id"`
	CallID    string `msgpack:"call_id"`
	ToolName  string `msgpack:"tool_name"`
	Input     string `msgpack:"input"`
	Status    string `msgpack:"status"`
	Reason    string `msgpack:"reason"`
	Formatted string `msgpack:"formatted,omitempty"`
}
