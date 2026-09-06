package atif

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestDir creates a temp directory, changes to it, and returns a cleanup
// function. Tests run in the temp dir to avoid polluting the real agent/ directory.
func setupTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "atif-test-*")
	require.NoError(t, err)
	orig, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() {
		os.Chdir(orig)
		os.RemoveAll(dir)
	})
	err = os.Chdir(dir)
	require.NoError(t, err)
	return dir
}

// readLines reads a file and returns non-empty lines.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var result []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			result = append(result, l)
		}
	}
	return result
}

// --- AtifWriter tests ---

func TestNewAtifWriter(t *testing.T) {
	setupTestDir(t)
	w := NewAtifWriter("test-agent", "sess-001")
	require.NotNil(t, w)
	assert.Equal(t, "test-agent", w.agentName)
	assert.Equal(t, "sess-001", w.sessionID)
	assert.Equal(t, filepath.Join("agent", "test-agent.txt"), w.aggPath)
	assert.Equal(t, filepath.Join("agent", "test-agent", "sessions", "sess-001.jsonl"), w.sessPath)
}

func TestAtifWriter_OpenAndClose(t *testing.T) {
	setupTestDir(t)
	w := NewAtifWriter("test-agent", "sess-001")
	require.NotNil(t, w)

	assert.False(t, w.IsOpen(), "should not be open before Open()")

	w.Open()
	assert.True(t, w.IsOpen(), "should be open after Open()")

	// Verify files exist
	_, err := os.Stat(w.aggPath)
	assert.NoError(t, err, "aggregated file should exist")
	_, err = os.Stat(w.sessPath)
	assert.NoError(t, err, "session file should exist")

	w.Close()
	assert.False(t, w.IsOpen(), "should not be open after Close()")
}

func TestAtifWriter_WriteEvent(t *testing.T) {
	setupTestDir(t)
	w := NewAtifWriter("test-agent", "sess-001")
	w.Open()
	defer w.Close()

	// Write a session event
	w.WriteEvent(SessionEvent{
		Type:      TypeSession,
		Version:   Version,
		ID:        "sess-001",
		Timestamp: "2026-01-01T00:00:00Z",
		Cwd:       "/app",
	})

	// Write an agent_start event
	w.WriteEvent(AgentStartEvent{
		Type: TypeAgentStart,
	})

	w.Close()

	// Verify aggregated file
	aggLines := readLines(t, w.aggPath)
	require.Len(t, aggLines, 2, "aggregated file should have 2 lines")

	var sessEvent SessionEvent
	err := json.Unmarshal([]byte(aggLines[0]), &sessEvent)
	require.NoError(t, err)
	assert.Equal(t, TypeSession, sessEvent.Type)
	assert.Equal(t, 3, sessEvent.Version)

	var agentEvent AgentStartEvent
	err = json.Unmarshal([]byte(aggLines[1]), &agentEvent)
	require.NoError(t, err)
	assert.Equal(t, TypeAgentStart, agentEvent.Type)

	// Verify session file has same content
	sessLines := readLines(t, w.sessPath)
	require.Len(t, sessLines, 2, "session file should have 2 lines")
	assert.Equal(t, aggLines, sessLines, "both files should have identical content")
}

func TestAtifWriter_WriteEvent_NilSafe(t *testing.T) {
	var w *AtifWriter
	// These should not panic
	w.Open()
	w.WriteEvent("test")
	w.Flush()
	w.Close()
	assert.False(t, w.IsOpen())
}

func TestAtifWriter_WriteEvent_ClosedWriter(t *testing.T) {
	setupTestDir(t)
	w := NewAtifWriter("test-agent", "sess-001")
	w.Open()
	w.Close()

	// Should not panic or write
	w.WriteEvent(SessionEvent{Type: TypeSession})
	_, err := os.Stat(w.aggPath)
	assert.NoError(t, err, "file should exist from Open")
	// But it should be empty since we closed
	lines := readLines(t, w.aggPath)
	assert.Empty(t, lines, "should not write after close")
}

func TestAtifWriter_Open_EmptyAgentName(t *testing.T) {
	setupTestDir(t)
	w := NewAtifWriter("", "test-session")
	w.Open() // Should not panic even with empty agent name
	// With empty agent name, paths are "agent/.txt" and "agent//sessions/..."
	// This is valid but odd. Just verify no panic.
	w.Close()
}

func TestAtifWriter_Flush(t *testing.T) {
	setupTestDir(t)
	w := NewAtifWriter("test-agent", "sess-001")
	w.Open()
	defer w.Close()

	w.WriteEvent(SessionEvent{Type: TypeSession, Version: Version, ID: "sess-001", Timestamp: "now", Cwd: "/app"})
	w.Flush()

	// Verify data was flushed
	lines := readLines(t, w.aggPath)
	assert.Len(t, lines, 1)
}

func TestAtifWriter_IsOpen(t *testing.T) {
	tests := []struct {
		name string
		w    *AtifWriter
		want bool
	}{
		{"nil receiver", nil, false},
		{"unopened", NewAtifWriter("a", "b"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.w.IsOpen())
		})
	}
}

// --- TrajectoryRecorder tests ---

func TestNewTrajectoryRecorder(t *testing.T) {
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	require.NotNil(t, r)
	assert.Equal(t, "test-agent", r.agentName)
	assert.Equal(t, "sess-001", r.sessionID)
	assert.NotNil(t, r.writer)
	assert.Equal(t, 0, r.eventID)
	assert.False(t, r.turnOpen)
}

func TestTrajectoryRecorder_Start(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	// Verify the file has session + agent_start
	lines := readLines(t, r.writer.aggPath)
	require.Len(t, lines, 2, "should have session and agent_start events")

	var sess SessionEvent
	err := json.Unmarshal([]byte(lines[0]), &sess)
	require.NoError(t, err)
	assert.Equal(t, TypeSession, sess.Type)
	assert.Equal(t, Version, sess.Version)
	assert.Equal(t, "sess-001", sess.ID)
	assert.NotEmpty(t, sess.Timestamp)
	assert.NotEmpty(t, sess.Cwd)

	var agent AgentStartEvent
	err = json.Unmarshal([]byte(lines[1]), &agent)
	require.NoError(t, err)
	assert.Equal(t, TypeAgentStart, agent.Type)
}

func TestTrajectoryRecorder_Start_NilSafe(t *testing.T) {
	var r *TrajectoryRecorder
	r.Start() // should not panic
	r.Close() // should not panic
}

func TestTrajectoryRecorder_FullTurnLifecycle(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	// Turn lifecycle
	r.TurnStarted()
	r.MessageStarted(RecorderMessage{
		Role: "user",
		Content: []RecorderContentBlock{
			{Type: "text", Text: strPtr("Hello")},
		},
	})
	r.MessageEnded(RecorderMessage{
		Role: "user",
		Content: []RecorderContentBlock{
			{Type: "text", Text: strPtr("Hello")},
		},
	})
	r.MessageStarted(RecorderMessage{
		Role: "assistant",
		Content: []RecorderContentBlock{
			{Type: "text", Text: strPtr("Hi there!")},
		},
		Usage: &RecorderUsage{
			Input:       10,
			Output:      5,
			TotalTokens: 15,
		},
		StopReason: "stop",
	})
	r.MessageEnded(RecorderMessage{
		Role: "assistant",
		Content: []RecorderContentBlock{
			{Type: "text", Text: strPtr("Hi there!")},
		},
		Usage: &RecorderUsage{
			Input:       10,
			Output:      5,
			TotalTokens: 15,
		},
		StopReason: "stop",
	})
	r.TurnEnded(RecorderMessage{
		Role:       "assistant",
		StopReason: "stop",
	}, nil)

	// Verify events
	lines := readLines(t, r.writer.aggPath)
	// session + agent_start + turn_start + msg_start(user) + msg_end(user) + msg_start(assistant) + msg_end(assistant) + turn_end = 8
	require.Len(t, lines, 8, "should have 8 events")

	var turnStart TurnStartEvent
	err := json.Unmarshal([]byte(lines[2]), &turnStart)
	require.NoError(t, err)
	assert.Equal(t, TypeTurnStart, turnStart.Type)

	var turnEnd TurnEndEvent
	err = json.Unmarshal([]byte(lines[7]), &turnEnd)
	require.NoError(t, err)
	assert.Equal(t, TypeTurnEnd, turnEnd.Type)
	assert.Equal(t, "assistant", turnEnd.Message.Role)
	assert.Empty(t, turnEnd.ToolResults, "tool results should be empty")
}

func TestTrajectoryRecorder_TurnLifecycleWithToolExecution(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	// Record a turn with tool execution
	r.TurnStarted()

	// User message
	r.MessageStarted(RecorderMessage{
		Role: "user",
		Content: []RecorderContentBlock{
			{Type: "text", Text: strPtr("Read the file")},
		},
	})
	r.MessageEnded(RecorderMessage{
		Role: "user",
		Content: []RecorderContentBlock{
			{Type: "text", Text: strPtr("Read the file")},
		},
	})

	// Assistant message with tool call
	r.MessageStarted(RecorderMessage{
		Role: "assistant",
		Content: []RecorderContentBlock{
			{Type: "text", Text: strPtr("I'll read the file")},
			{
				Type:       "toolCall",
				ToolCallID: "call_123",
				ToolName:   "read",
				Arguments:  map[string]any{"path": "/app/file.txt"},
			},
		},
		Usage:      &RecorderUsage{Input: 10, Output: 5, TotalTokens: 15},
		StopReason: "toolUse",
	})
	r.MessageEnded(RecorderMessage{
		Role: "assistant",
		Content: []RecorderContentBlock{
			{Type: "text", Text: strPtr("I'll read the file")},
			{
				Type:       "toolCall",
				ToolCallID: "call_123",
				ToolName:   "read",
				Arguments:  map[string]any{"path": "/app/file.txt"},
			},
		},
		Usage:      &RecorderUsage{Input: 10, Output: 5, TotalTokens: 15},
		StopReason: "toolUse",
	})

	// Tool execution
	r.ToolExecutionStarted("call_123", "read", map[string]any{"path": "/app/file.txt"})
	r.ToolExecutionEnded("call_123", "read", "file content", false)

	// Tool result message
	r.MessageStarted(RecorderMessage{
		Role:       "toolResult",
		ToolCallID: "call_123",
		ToolName:   "read",
		Content:    []RecorderContentBlock{{Type: "text", Text: strPtr("file content")}},
	})
	r.MessageEnded(RecorderMessage{
		Role:       "toolResult",
		ToolCallID: "call_123",
		ToolName:   "read",
		Content:    []RecorderContentBlock{{Type: "text", Text: strPtr("file content")}},
	})

	// Turn end
	r.TurnEnded(RecorderMessage{
		Role:       "assistant",
		StopReason: "toolUse",
	}, []RecorderToolResult{
		{
			Role:       "toolResult",
			ToolCallID: "call_123",
			ToolName:   "read",
			Content:    []RecorderContentBlock{{Type: "text", Text: strPtr("file content")}},
			IsError:    false,
			Timestamp:  1000,
		},
	})

	lines := readLines(t, r.writer.aggPath)
	// session + agent_start + turn_start + msg_start(user) + msg_end(user) + msg_start(assistant) + msg_end(assistant)
	// + tool_exec_start + tool_exec_end + msg_start(toolResult) + msg_end(toolResult) + turn_end = 12
	require.Len(t, lines, 12, "should have 12 events")

	// Verify tool execution events
	var toolStart ToolExecutionStartEvent
	err := json.Unmarshal([]byte(lines[7]), &toolStart)
	require.NoError(t, err)
	assert.Equal(t, TypeToolExecutionStart, toolStart.Type)
	assert.Equal(t, "call_123", toolStart.ToolCallID)
	assert.Equal(t, "read", toolStart.ToolName)

	var toolEnd ToolExecutionEndEvent
	err = json.Unmarshal([]byte(lines[8]), &toolEnd)
	require.NoError(t, err)
	assert.Equal(t, TypeToolExecutionEnd, toolEnd.Type)
	assert.Equal(t, "call_123", toolEnd.ToolCallID)
	assert.False(t, toolEnd.IsError)
	assert.Len(t, toolEnd.Result.Content, 1)
	assert.Equal(t, "file content", toolEnd.Result.Content[0].Text)

	// Verify turn_end has tool results
	var turnEnd TurnEndEvent
	err = json.Unmarshal([]byte(lines[11]), &turnEnd)
	require.NoError(t, err)
	require.Len(t, turnEnd.ToolResults, 1)
	assert.Equal(t, "call_123", turnEnd.ToolResults[0].ToolCallID)
	assert.Equal(t, "read", turnEnd.ToolResults[0].ToolName)
}

func TestTrajectoryRecorder_ToolExecutionUpdate(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	r.ToolExecutionUpdated("call_456", "bash", "partial output", map[string]any{"command": "ls"})
	r.ToolExecutionEnded("call_456", "bash", "final output", false)

	lines := readLines(t, r.writer.aggPath)
	// session + agent_start + tool_exec_update + tool_exec_end = 4
	require.Len(t, lines, 4)

	var update ToolExecutionUpdateEvent
	err := json.Unmarshal([]byte(lines[2]), &update)
	require.NoError(t, err)
	assert.Equal(t, TypeToolExecutionUpdate, update.Type)
	assert.Equal(t, "call_456", update.ToolCallID)
	assert.Equal(t, "bash", update.ToolName)
	assert.Len(t, update.PartialResult.Content, 1)
	assert.Equal(t, "partial output", update.PartialResult.Content[0].Text)
}

func TestTrajectoryRecorder_ModelChanged(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	r.ModelChanged("openai", "gpt-4o")

	lines := readLines(t, r.writer.aggPath)
	require.Len(t, lines, 3) // session + agent_start + model_change

	var mc ModelChangeEvent
	err := json.Unmarshal([]byte(lines[2]), &mc)
	require.NoError(t, err)
	assert.Equal(t, TypeModelChange, mc.Type)
	assert.Equal(t, "openai", mc.Provider)
	assert.Equal(t, "gpt-4o", mc.ModelID)
	assert.NotEmpty(t, mc.ID)
	assert.NotEmpty(t, mc.Timestamp)
}

func TestTrajectoryRecorder_ThinkingLevelChanged(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	r.ThinkingLevelChanged("high")

	lines := readLines(t, r.writer.aggPath)
	require.Len(t, lines, 3) // session + agent_start + thinking_level_change

	var tc ThinkingLevelChangeEvent
	err := json.Unmarshal([]byte(lines[2]), &tc)
	require.NoError(t, err)
	assert.Equal(t, TypeThinkingLevelChange, tc.Type)
	assert.Equal(t, "high", tc.ThinkingLevel)
	assert.NotEmpty(t, tc.ID)
	assert.NotEmpty(t, tc.Timestamp)
}

func TestTrajectoryRecorder_MessageWithCost(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	r.MessageStarted(RecorderMessage{
		Role:     "assistant",
		Provider: "openrouter",
		Model:    "qwen/qwen3.7-flash",
		Usage: &RecorderUsage{
			Input:       1666,
			Output:      74,
			TotalTokens: 1740,
			Cost: &RecorderCost{
				Input:  0.00004998,
				Output: 0.00000962,
				Total:  0.00005960,
			},
		},
		StopReason: "pending",
	})
	r.MessageEnded(RecorderMessage{
		Role:     "assistant",
		Provider: "openrouter",
		Model:    "qwen/qwen3.7-flash",
		Usage: &RecorderUsage{
			Input:       1666,
			Output:      74,
			TotalTokens: 1740,
			Cost: &RecorderCost{
				Input:  0.00004998,
				Output: 0.00000962,
				Total:  0.00005960,
			},
		},
		StopReason: "toolUse",
	})

	lines := readLines(t, r.writer.aggPath)
	// session + agent_start + msg_start + msg_end = 4
	require.Len(t, lines, 4)

	for _, line := range lines[2:] {
		var msgStart MessageStartEvent
		if err := json.Unmarshal([]byte(line), &msgStart); err == nil && msgStart.Type == TypeMessageStart {
			require.NotNil(t, msgStart.Message.Usage)
			assert.Equal(t, 1666, msgStart.Message.Usage.Input)
			assert.Equal(t, 74, msgStart.Message.Usage.Output)
			require.NotNil(t, msgStart.Message.Usage.Cost)
			assert.InDelta(t, 0.00004998, msgStart.Message.Usage.Cost.Input, 0.00000001)
			assert.InDelta(t, 0.00005960, msgStart.Message.Usage.Cost.Total, 0.00000001)
		}
	}
}

func TestTrajectoryRecorder_MessageWithError(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	errMsg := "429: Provider returned error..."
	r.MessageEnded(RecorderMessage{
		Role:         "assistant",
		StopReason:   "error",
		ErrorMessage: &errMsg,
	})

	lines := readLines(t, r.writer.aggPath)
	require.Len(t, lines, 3) // session + agent_start + msg_end

	var msgEnd MessageEndEvent
	err := json.Unmarshal([]byte(lines[2]), &msgEnd)
	require.NoError(t, err)
	assert.Equal(t, TypeMessageEnd, msgEnd.Type)
	assert.Equal(t, "error", msgEnd.Message.StopReason)
	require.NotNil(t, msgEnd.Message.ErrorMessage)
	assert.Equal(t, errMsg, *msgEnd.Message.ErrorMessage)
}

func TestTrajectoryRecorder_MessageWithContentBlocks(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	thinking := "Let me think about this..."
	sig := "reasoning-1"
	text := "Here's my answer"

	r.MessageEnded(RecorderMessage{
		Role: "assistant",
		Content: []RecorderContentBlock{
			{Type: "thinking", Thinking: &thinking, ThinkingSignature: &sig},
			{Type: "text", Text: &text},
		},
		StopReason: "stop",
	})

	lines := readLines(t, r.writer.aggPath)
	require.Len(t, lines, 3)

	var msgEnd MessageEndEvent
	err := json.Unmarshal([]byte(lines[2]), &msgEnd)
	require.NoError(t, err)
	require.Len(t, msgEnd.Message.Content, 2)
	assert.Equal(t, "thinking", msgEnd.Message.Content[0].Type)
	assert.Equal(t, thinking, msgEnd.Message.Content[0].Thinking)
	assert.Equal(t, sig, msgEnd.Message.Content[0].ThinkingSignature)
	assert.Equal(t, "text", msgEnd.Message.Content[1].Type)
	assert.Equal(t, text, msgEnd.Message.Content[1].Text)
}

func TestTrajectoryRecorder_TurnEnded_WithoutOpenTurn(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	// TurnEnded without TurnStarted should be a no-op
	r.TurnEnded(RecorderMessage{Role: "assistant"}, nil)

	lines := readLines(t, r.writer.aggPath)
	require.Len(t, lines, 2, "no turn_end should be written without open turn")
}

func TestTrajectoryRecorder_NilSafe(t *testing.T) {
	var r *TrajectoryRecorder

	// None of these should panic
	r.Start()
	r.Close()
	r.TurnStarted()
	r.TurnEnded(RecorderMessage{}, nil)
	r.MessageStarted(RecorderMessage{})
	r.MessageEnded(RecorderMessage{})
	r.ToolExecutionStarted("id", "tool", nil)
	r.ToolExecutionEnded("id", "tool", "result", false)
	r.ToolExecutionUpdated("id", "tool", "partial", nil)
}

func TestTrajectoryRecorder_Close_Idempotent(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	r.Close()
	r.Close() // second close should not panic
}

// --- Conversion helpers ---

func TestRecorderMsgToSchema(t *testing.T) {
	thinking := "I think"
	sig := "sig-1"
	text := "Result"
	errMsg := "error!"

	msg := RecorderMessage{
		Role:     "assistant",
		Provider: "openrouter",
		Model:    "qwen/qwen3.7-flash",
		Content: []RecorderContentBlock{
			{Type: "thinking", Thinking: &thinking, ThinkingSignature: &sig},
			{Type: "text", Text: &text},
			{
				Type:       "toolCall",
				ToolCallID: "call_1",
				ToolName:   "read",
				Arguments:  map[string]any{"path": "/app/file.txt"},
			},
		},
		Usage: &RecorderUsage{
			Input:       100,
			Output:      50,
			TotalTokens: 150,
			Cost:        &RecorderCost{Input: 0.001, Output: 0.002, Total: 0.003},
		},
		StopReason:    "toolUse",
		ErrorMessage:  &errMsg,
		ResponseID:    "resp-1",
		RawStopReason: "tool_calls",
	}

	schema := recorderMsgToSchema(msg)
	assert.Equal(t, "assistant", schema.Role)
	assert.Equal(t, "openrouter", schema.Provider)
	assert.Equal(t, "qwen/qwen3.7-flash", schema.Model)
	assert.Equal(t, "toolUse", schema.StopReason)
	assert.Equal(t, "resp-1", schema.ResponseID)
	assert.Equal(t, "tool_calls", schema.RawStopReason)
	require.NotNil(t, schema.ErrorMessage)
	assert.Equal(t, "error!", *schema.ErrorMessage)

	require.Len(t, schema.Content, 3)
	assert.Equal(t, "thinking", schema.Content[0].Type)
	assert.Equal(t, "I think", schema.Content[0].Thinking)
	assert.Equal(t, "sig-1", schema.Content[0].ThinkingSignature)
	assert.Equal(t, "text", schema.Content[1].Type)
	assert.Equal(t, "Result", schema.Content[1].Text)
	assert.Equal(t, "toolCall", schema.Content[2].Type)
	assert.Equal(t, "call_1", schema.Content[2].ID)
	assert.Equal(t, "read", schema.Content[2].Name)

	require.NotNil(t, schema.Usage)
	assert.Equal(t, 100, schema.Usage.Input)
	assert.Equal(t, 50, schema.Usage.Output)
	assert.Equal(t, 150, schema.Usage.TotalTokens)
	require.NotNil(t, schema.Usage.Cost)
	assert.InDelta(t, 0.001, schema.Usage.Cost.Input, 0.0001)
	assert.InDelta(t, 0.003, schema.Usage.Cost.Total, 0.0001)
}

func TestRecorderMsgToSchema_EmptyContent(t *testing.T) {
	schema := recorderMsgToSchema(RecorderMessage{Role: "user"})
	assert.Equal(t, "user", schema.Role)
	assert.Empty(t, schema.Content)
	assert.Nil(t, schema.Usage)
}

func TestRecorderToolResultsToSchema(t *testing.T) {
	trs := []RecorderToolResult{
		{
			Role:       "toolResult",
			ToolCallID: "call_1",
			ToolName:   "read",
			Content:    []RecorderContentBlock{{Type: "text", Text: strPtr("output")}},
			IsError:    false,
			Timestamp:  1000,
		},
		{
			Role:       "toolResult",
			ToolCallID: "call_2",
			ToolName:   "bash",
			Content:    []RecorderContentBlock{{Type: "text", Text: strPtr("error occurred")}},
			IsError:    true,
			Timestamp:  2000,
		},
	}

	schema := recorderToolResultsToSchema(trs)
	require.Len(t, schema, 2)
	assert.Equal(t, "call_1", schema[0].ToolCallID)
	assert.Equal(t, "read", schema[0].ToolName)
	assert.False(t, schema[0].IsError)
	assert.Equal(t, int64(1000), schema[0].Timestamp)
	require.Len(t, schema[0].Content, 1)
	assert.Equal(t, "output", schema[0].Content[0].Text)

	assert.Equal(t, "call_2", schema[1].ToolCallID)
	assert.True(t, schema[1].IsError)
}

func TestRecorderToolResultsToSchema_Nil(t *testing.T) {
	result := recorderToolResultsToSchema(nil)
	require.NotNil(t, result)
	assert.Empty(t, result)
}

func TestRecorderToolResultsToSchema_EmptyContent(t *testing.T) {
	trs := []RecorderToolResult{
		{
			Role:       "toolResult",
			ToolCallID: "call_1",
			ToolName:   "read",
			IsError:    false,
			Timestamp:  1000,
		},
	}
	schema := recorderToolResultsToSchema(trs)
	require.Len(t, schema, 1)
	assert.Empty(t, schema[0].Content)
}

func TestTrajectoryRecorder_Start_CreatesDir(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("test-agent", "sess-001")
	r.Start()
	defer r.Close()

	// Verify the directories were created
	_, err := os.Stat(filepath.Join("agent", "test-agent"))
	assert.NoError(t, err, "agent directory should exist")
	_, err = os.Stat(filepath.Join("agent", "test-agent", "sessions"))
	assert.NoError(t, err, "sessions directory should exist")
}

func TestTrajectoryRecorder_AgentNameWithSpaces(t *testing.T) {
	setupTestDir(t)
	r := NewTrajectoryRecorder("my agent", "sess-001")
	r.Start()
	defer r.Close()

	// Verify file names
	assert.Contains(t, r.writer.aggPath, "my agent.txt")
	assert.Contains(t, r.writer.sessPath, "my agent")

	// Should still work
	lines := readLines(t, r.writer.aggPath)
	require.Len(t, lines, 2)
}

// strPtr is a helper for creating string pointers in tests
func strPtr(s string) *string { return &s }
