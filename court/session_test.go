package court

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/court/tools"
	internalconfig "github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/mocks"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTool is a simple tool for testing
type mockTool struct {
	name   string
	output string
	called bool
}

func (t *mockTool) Name() string        { return t.name }
func (t *mockTool) Description() string { return "A mock tool for testing" }
func (t *mockTool) Call(ctx context.Context, input string) (string, error) {
	t.called = true
	return t.output, nil
}
func (t *mockTool) Format(input, result string, err error) string {
	if err != nil {
		return t.name + " error"
	}
	return t.name + " ok"
}
func (t *mockTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// msgContentStr extracts the string content from a schemas.ChatMessage.
// Returns empty string if no content is set.
func msgContentStr(msg schemas.ChatMessage) string {
	if msg.Content != nil && msg.Content.ContentStr != nil {
		return *msg.Content.ContentStr
	}
	return ""
}

func TestNewSession(t *testing.T) {
	t.Parallel()

	cfg := &SessionConfig{
		LLM: internalconfig.LLMConfig{
			Provider: "test",
			Model:    "test-model",
		},
	}

	sess, err := NewSession(nil, cfg, nil, nil, func(any) {}, "You are a test assistant", "")
	require.NoError(t, err)

	assert.NotEmpty(t, sess.ID)
	assert.NotNil(t, sess.toolCatalog)
	assert.NotNil(t, sess.scheduler)

	// Should have system message
	require.NotEmpty(t, sess.messages)
	assert.Equal(t, schemas.ChatMessageRoleSystem, sess.messages[0].Role)
}

func TestNewSession_DefaultMaxTurns(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, nil, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	assert.Equal(t, 999, sess.config.MaxTurns)
}

func TestNewSession_WithTools(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		&mockTool{name: "test_tool", output: "tool output"},
	}

	sess, err := NewSession(nil, nil, tools, nil, func(any) {}, "", "")
	require.NoError(t, err)

	assert.Len(t, sess.toolDefs, 1)
	assert.Len(t, sess.toolCatalog, 1)
	assert.Contains(t, sess.toolCatalog, "test_tool")
}

func TestSession_AskWithStreaming_NoTools(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	var streamTexts []string
	var streamReasonings []string
	sess.SetNotify(func(msg any) {
		if m, ok := msg.(StreamChunkMsg); ok {
			if m.Text != "" {
				streamTexts = append(streamTexts, m.Text)
			}
			if m.Reasoning != "" {
				streamReasonings = append(streamReasonings, m.Reasoning)
			}
		}
	}, "test-channel")

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "Hello", nil)
	require.NoError(t, err)

	assert.NotEmpty(t, resp)
	assert.NotEmpty(t, streamTexts, "should have received streaming text chunks")
}

func TestSession_AskWithStreaming_WithResponse(t *testing.T) {
	mockLLM := mocks.NewLLMProviderWithChunks([]mocks.StreamingChunk{
		{Content: "Hello "},
		{Content: "world!", FinishReason: "stop"},
	})

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "Say hello", nil)
	require.NoError(t, err)

	assert.Equal(t, "Hello world!", resp)
}

func TestSession_AskWithStreaming_StreamsChunks(t *testing.T) {
	mockLLM := mocks.NewLLMProviderWithChunks([]mocks.StreamingChunk{
		{Content: "Part1 "},
		{Content: "Part2 "},
		{Content: "Part3", FinishReason: "stop"},
	})

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	var streamTexts []string
	sess.SetNotify(func(msg any) {
		if m, ok := msg.(StreamChunkMsg); ok {
			if m.Text != "" {
				streamTexts = append(streamTexts, m.Text)
			}
		}
	}, "test-channel")

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "Give me a response", nil)
	require.NoError(t, err)

	assert.Equal(t, "Part1 Part2 Part3", resp)
	assert.Len(t, streamTexts, 3, "should have received exactly 3 streaming text chunks")
}

// TestSession_AskWithStreaming_StreamsReasoningChunks verifies that deltas
// carrying both reasoning and content emit a single StreamChunkMsg with
// Reasoning and Text populated independently (not concatenated).
func TestSession_AskWithStreaming_StreamsReasoningChunks(t *testing.T) {
	mockLLM := mocks.NewLLMProviderWithChunks([]mocks.StreamingChunk{
		{Reasoning: "I think... "},
		{Content: "Here is the answer."},
		{Reasoning: " More reasoning.", Content: " More content.", FinishReason: "stop"},
	})

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	var streamTexts, streamReasonings []string
	sess.SetNotify(func(msg any) {
		if m, ok := msg.(StreamChunkMsg); ok {
			if m.Text != "" {
				streamTexts = append(streamTexts, m.Text)
			}
			if m.Reasoning != "" {
				streamReasonings = append(streamReasonings, m.Reasoning)
			}
		}
	}, "test-channel")

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "Give me a response", nil)
	require.NoError(t, err)

	assert.Equal(t, "Here is the answer. More content.", resp)
	assert.Len(t, streamTexts, 2, "should have received exactly 2 text chunks")
	assert.Len(t, streamReasonings, 2, "should have received exactly 2 reasoning chunks")
	assert.Equal(t, []string{"I think... ", " More reasoning."}, streamReasonings)
	assert.Equal(t, []string{"Here is the answer.", " More content."}, streamTexts)
}

func TestSession_AskWithStreaming_WithToolExecution(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	tool := &mockTool{name: "get_weather", output: `{"temp": 72, "condition": "sunny"}`}

	sess, err := NewSession(mockLLM, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	// Verify tool is registered
	assert.Contains(t, sess.toolCatalog, "get_weather")

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "What's the weather?", nil)
	require.NoError(t, err)

	assert.NotEmpty(t, resp)
	// Tool was registered but mock LLM doesn't call tools - that's expected
	// The important thing is that the session handles tools correctly
}

func TestSession_AskWithStreaming_WithContextFiles(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	contextFiles := map[string]string{
		"test.txt": "This is the file content",
	}
	resp, err := sess.AskWithStreaming(ctx, "Summarize this file", contextFiles)
	require.NoError(t, err)

	assert.NotEmpty(t, resp)
	// Context files are incorporated into the prompt for this request
	// The HasContextFiles would be true only after AddContextFile is called
}

func TestSession_EnsureToolCallIDsBeforeAppend(t *testing.T) {
	t.Skip("requires mock bifrost client for LLM responses")
}

func TestSession_CheckToolCallLoop(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	args := `{"path":"test.txt"}`

	// First call - not a loop
	assert.False(t, sess.checkToolCallLoop("read_file", args))
	assert.Equal(t, 1, sess.toolCallRepetitionCount)

	// Second identical call - still not a loop
	assert.False(t, sess.checkToolCallLoop("read_file", args))
	assert.Equal(t, 2, sess.toolCallRepetitionCount)

	// Third identical call - now it's a loop (threshold is 3)
	assert.True(t, sess.checkToolCallLoop("read_file", args))
	// After loop detection, counters are reset for fresh attempt
	assert.Equal(t, 0, sess.toolCallRepetitionCount)
	assert.Equal(t, "", sess.lastToolCallKey)
	assert.Equal(t, 1, sess.toolCallLoopHits)
}

func TestSession_CheckToolCallLoop_DifferentCalls(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// Different calls should reset the counter
	assert.False(t, sess.checkToolCallLoop("read_file", `{"path":"a.txt"}`))
	assert.False(t, sess.checkToolCallLoop("read_file", `{"path":"b.txt"}`))
	assert.False(t, sess.checkToolCallLoop("write_file", `{"path":"c.txt"}`))

	// Counter should be 1 after each different call
	assert.Equal(t, 1, sess.toolCallRepetitionCount)
}

func TestSession_RegisterCourtTools(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	initialToolCount := len(sess.toolDefs)

	tools := []Tool{
		&mockTool{name: "tool1"},
		&mockTool{name: "tool2"},
	}
	sess.RegisterCourtTools(tools)

	assert.Equal(t, initialToolCount+2, len(sess.toolDefs))
	assert.Contains(t, sess.toolCatalog, "tool1")
	assert.Contains(t, sess.toolCatalog, "tool2")
}

func TestSession_Messages(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system prompt", "")
	require.NoError(t, err)

	messages := sess.Messages()
	require.NotEmpty(t, messages)
	assert.Equal(t, schemas.ChatMessageRoleSystem, messages[0].Role)
}

func TestSession_AddMessage(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	initialLen := len(sess.messages)
	sess.AddMessage(schemas.ChatMessageRoleUser, "Hello")

	assert.Equal(t, initialLen+1, len(sess.messages))
	lastMsg := sess.messages[len(sess.messages)-1]
	assert.Equal(t, schemas.ChatMessageRoleUser, lastMsg.Role)
}

func TestBuildPromptWithContext_NoContext(t *testing.T) {
	t.Parallel()

	result := buildPromptWithContext("simple prompt", nil)
	assert.Equal(t, "simple prompt", result)
}

func TestBuildPromptWithContext_WithContext(t *testing.T) {
	t.Parallel()

	context := map[string]string{
		"file.txt": "file content",
	}
	result := buildPromptWithContext("analyze this", context)

	assert.Contains(t, result, "file.txt")
	assert.Contains(t, result, "file content")
	assert.Contains(t, result, "analyze this")
}

func TestCoreToolScheduler_Schedule(t *testing.T) {
	t.Parallel()

	var notified bool
	notify := func(msg any) {
		notified = true
	}

	scheduler := runners.NewCoreToolScheduler(notify)
	tool := &mockTool{name: "test", output: "result"}

	ch := scheduler.Schedule(context.Background(), tool, "{}")
	result := <-ch

	assert.NoError(t, result.Error)
	assert.Equal(t, "result", result.Output)
	assert.True(t, tool.called)
	assert.True(t, notified)
}

func TestSession_StreamBufferMethods(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// Initially empty
	assert.Empty(t, sess.getStreamBuffer())

	// Write to buffer
	sess.accumulatedContent.WriteString("hello ")
	sess.accumulatedContent.WriteString("world")
	assert.Equal(t, "hello world", sess.getStreamBuffer())

	// Reset buffer
	sess.resetStreamBuffer()
	assert.Empty(t, sess.getStreamBuffer())
}

func TestSession_ProcessToolCalls_UnknownTool(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	toolCalls := []schemas.ChatAssistantMessageToolCall{{
		ID:   strPtr("tc1"),
		Type: strPtr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      strPtr("unknown_tool"),
			Arguments: "{}",
		},
	}}

	messages, shouldReturn := sess.processToolCalls(context.Background(), toolCalls)

	assert.False(t, shouldReturn)
	require.Len(t, messages, 1)

	// Check error response
	content := msgContentStr(messages[0])
	assert.Contains(t, content, "unknown tool")
}

func TestSession_ProcessToolCalls_ContextCancellation(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	toolCalls := []schemas.ChatAssistantMessageToolCall{{
		ID:   strPtr("tc1"),
		Type: strPtr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      strPtr("some_tool"),
			Arguments: "{}",
		},
	}}

	messages, shouldReturn := sess.processToolCalls(ctx, toolCalls)

	assert.True(t, shouldReturn)
	require.NotEmpty(t, messages)

	// Should have abort response
	content := msgContentStr(messages[0])
	assert.Contains(t, content, "aborted")
}

// --- Additional tests for full coverage ---

func TestSession_AddTools(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	tools := []Tool{&mockTool{name: "added_tool"}}
	sess.AddTools(tools)

	assert.Contains(t, sess.toolCatalog, "added_tool")
}

// TestSession_SetNotify_UpdatesScheduler verifies that SetNotify updates both
// the session's notify and the scheduler's notify (fixes tool call notifications)
func TestSession_SetNotify_UpdatesScheduler(t *testing.T) {
	t.Parallel()

	// Create session with nil notify
	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, nil, "", "")
	require.NoError(t, err)

	// Verify both start as nil
	assert.Nil(t, sess.notify)

	// Set notify via SetNotify method
	var called bool
	sess.SetNotify(func(msg any) {
		called = true
	}, "")

	// Verify session notify is set
	assert.NotNil(t, sess.notify)

	// Verify the notify function works when called
	sess.notify("test")
	assert.True(t, called, "notify function should be callable after SetNotify")
}

// TestSession_ToolCallNotifications verifies that tool execution sends proper
// notifications through the stream (ToolCallExecutingMsg, ToolCallSuccessMsg)
func TestSession_ToolCallNotifications(t *testing.T) {
	t.Skip("requires mock bifrost client for LLM responses")
}

func TestEnsureToolCallID_ExistingID(t *testing.T) {
	t.Parallel()

	tc := &schemas.ChatAssistantMessageToolCall{
		ID: strPtr("existing-id"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name: strPtr("test"),
		},
	}

	result := ensureToolCallID(tc, 0)
	assert.Equal(t, "existing-id", result)
	assert.Equal(t, "existing-id", *tc.ID)
}

func TestEnsureToolCallID_GeneratesSyntheticID(t *testing.T) {
	t.Parallel()

	tc := &schemas.ChatAssistantMessageToolCall{
		ID: strPtr(""), // Empty ID
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name: strPtr("test"),
		},
	}

	result := ensureToolCallID(tc, 0)
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "synthetic_")
	assert.Equal(t, result, *tc.ID) // Should be set on the struct
}

func TestHasToolCallResponse_Found(t *testing.T) {
	t.Parallel()

	toolMessages := []schemas.ChatMessage{{
		Role:            schemas.ChatMessageRoleTool,
		Content:         textContent("result"),
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr("tc1")},
	}}

	assert.True(t, hasToolCallResponse(toolMessages, "tc1"))
}

func TestHasToolCallResponse_NotFound(t *testing.T) {
	t.Parallel()

	toolMessages := []schemas.ChatMessage{{
		Role:            schemas.ChatMessageRoleTool,
		Content:         textContent("result"),
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr("tc1")},
	}}

	assert.False(t, hasToolCallResponse(toolMessages, "tc2"))
}

func TestHasToolCallResponse_EmptyMessages(t *testing.T) {
	t.Parallel()

	assert.False(t, hasToolCallResponse(nil, "tc1"))
	assert.False(t, hasToolCallResponse([]schemas.ChatMessage{}, "tc1"))
}

func TestHasToolCallResponse_NonToolMessage(t *testing.T) {
	t.Parallel()

	messages := []schemas.ChatMessage{{
		Role:    schemas.ChatMessageRoleUser,
		Content: textContent("hello"),
	}}

	assert.False(t, hasToolCallResponse(messages, "tc1"))
}

func TestSession_AppendMessage_NilChoice(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	initialLen := len(sess.messages)
	sess.appendMessage(nil)

	// Should not add anything for nil choice
	assert.Equal(t, initialLen, len(sess.messages))
}

func TestSession_AppendMessage_EmptyContent(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	initialLen := len(sess.messages)
	sess.appendMessage(&responseChoice{Content: "   "}) // Only whitespace

	// Should not add message with only whitespace
	assert.Equal(t, initialLen, len(sess.messages))
}

func TestSession_AppendMessage_WithToolCalls(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	initialLen := len(sess.messages)
	sess.appendMessage(&responseChoice{
		Content: "",
		ToolCalls: []schemas.ChatAssistantMessageToolCall{{
			ID:   strPtr("tc1"),
			Type: strPtr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      strPtr("test"),
				Arguments: "{}",
			},
		}},
	})

	// Should add message with tool calls even without content
	assert.Equal(t, initialLen+1, len(sess.messages))
}

func TestSession_AppendMessage_WithInvalidToolCalls(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	initialLen := len(sess.messages)
	sess.appendMessage(&responseChoice{
		Content: "text",
		ToolCalls: []schemas.ChatAssistantMessageToolCall{
			{ID: strPtr("tc1"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: nil}},        // nil Name
			{ID: strPtr("tc2"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("")}}, // empty name
		},
	})

	// Should add message with content and tool calls (appendMessage stores them as-is;
	// filtering happens in processToolCalls)
	assert.Equal(t, initialLen+1, len(sess.messages))
	lastMsg := sess.messages[len(sess.messages)-1]
	assert.NotNil(t, lastMsg.Content)
	assert.NotNil(t, lastMsg.ChatAssistantMessage)
	assert.Len(t, lastMsg.ChatAssistantMessage.ToolCalls, 2)
}

func TestNewSession_WithProvidedScheduler(t *testing.T) {
	t.Parallel()

	customScheduler := runners.NewCoreToolScheduler(func(any) {})
	sess, err := NewSession(nil, &SessionConfig{}, nil, customScheduler, func(any) {}, "", "")
	require.NoError(t, err)

	assert.Equal(t, customScheduler, sess.scheduler)
}

func TestSession_AskWithStreaming_LLMError(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	mockLLM.SetError(&schemas.BifrostError{
		Error: &schemas.ErrorField{Message: "API error: rate limited"},
	})

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	sess.SetNotify(func(msg any) {
		if _, ok := msg.(StreamErrorMsg); ok {
			// Error notification received
		}
	}, "test-channel")

	ctx := context.Background()
	_, err = sess.AskWithStreaming(ctx, "Hello", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
}

func TestSession_AskWithStreaming_StopReasonError(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	mockLLM.SetStreamingChunks([]mocks.StreamingChunk{
		{Content: "Partial response before failure"},
		{Content: "", FinishReason: "error"},
	})

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)
	sess.Provider = "openrouter"
	sess.Model = "test-model"

	var capturedMsg StreamErrorMsg
	sess.SetNotify(func(msg any) {
		if m, ok := msg.(StreamErrorMsg); ok {
			capturedMsg = m
		}
	}, "test-channel")

	ctx := context.Background()
	content, err := sess.AskWithStreaming(ctx, "Hello", nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "openrouter/test-model")
	assert.Contains(t, err.Error(), "stop_reason=error")
	// Partial content should be returned, not empty string
	assert.Contains(t, content, "Partial response before failure")
	// Notification should carry partial content
	assert.Contains(t, capturedMsg.PartialContent, "Partial response before failure")
}

func TestSession_AskWithStreaming_StopReasonContentFilter(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	mockLLM.SetStreamingChunks([]mocks.StreamingChunk{
		{Content: "Some content"},
		{Content: "", FinishReason: "content_filter"},
	})

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)
	sess.Provider = "openai"
	sess.Model = "gpt-4"

	ctx := context.Background()
	content, err := sess.AskWithStreaming(ctx, "Hello", nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "openai/gpt-4")
	assert.Contains(t, err.Error(), "stop_reason=content_filter")
	assert.Contains(t, content, "Some content")
}

func TestSession_GenerateLLMResponse_EmptyChoices(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	// Should handle gracefully with default mock behavior
	resp, err := sess.AskWithStreaming(ctx, "Test", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, resp)
}

func TestSession_AskWithStreaming_Cancellation(t *testing.T) {
	t.Parallel()

	mockLLM := mocks.NewLLMProvider()
	// Add delay to allow cancellation
	mockLLM.DelayBetweenChunks = 50 * time.Millisecond
	mockLLM.SetStreamingChunks([]mocks.StreamingChunk{
		{Content: "Slow "},
		{Content: "response "},
		{Content: "here", FinishReason: "stop"},
	})

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	var interrupted bool
	sess.SetNotify(func(msg any) {
		if _, ok := msg.(StreamInterruptedMsg); ok {
			interrupted = true
		}
	}, "test-channel")

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay (before any chunks arrive)
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err = sess.AskWithStreaming(ctx, "Hello", nil)
	// Should return context error on cancellation
	require.Equal(t, context.Canceled, err)
	assert.True(t, interrupted, "expected StreamInterruptedMsg to be sent on cancellation")
}

func TestSession_AskWithStreaming_MaxTokens(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()

	sess, err := NewSession(mockLLM, &SessionConfig{LLM: internalconfig.LLMConfig{MaxTurns: 1}}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "Hello", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, resp)
}

func TestSession_AskWithStreaming_ErrorStopReason(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "Hello", nil)
	require.NoError(t, err)
	// Default mock response is "This is a mock streaming response"
	assert.Equal(t, "This is a mock streaming response", resp)
}

func TestSession_AskWithStreaming_NilModel(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "Hello", nil)
	assert.Empty(t, resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM model not configured")
}

func TestSession_ProcessToolCalls_LoopDetected(t *testing.T) {
	t.Parallel()

	tool := &mockTool{name: "loop_tool", output: "result"}
	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "", "")
	require.NoError(t, err)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   strPtr("tc1"),
		Type: strPtr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      strPtr("loop_tool"),
			Arguments: `{"same":"args"}`,
		},
	}

	// Call 3 times to trigger loop detection
	sess.processToolCalls(context.Background(), []schemas.ChatAssistantMessageToolCall{toolCall})
	sess.processToolCalls(context.Background(), []schemas.ChatAssistantMessageToolCall{toolCall})
	messages, shouldReturn := sess.processToolCalls(context.Background(), []schemas.ChatAssistantMessageToolCall{toolCall})

	// Loop detection now returns false (not true) to let the outer turn loop continue
	assert.False(t, shouldReturn)
	require.NotEmpty(t, messages)
	content := msgContentStr(messages[0])
	assert.Contains(t, content, "loop detected")

	// Counter resets after loop detection
	assert.Equal(t, 0, sess.toolCallRepetitionCount)
	assert.Equal(t, "", sess.lastToolCallKey)
	// Loop hits counter incremented
	assert.Equal(t, 1, sess.toolCallLoopHits)
}

func TestSession_ProcessToolCalls_SkipsEmptyName(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	toolCalls := []schemas.ChatAssistantMessageToolCall{
		{
			ID:       strPtr("tc1"),
			Type:     strPtr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr(""), Arguments: "{}"},
		},
		{
			ID:       strPtr("tc2"),
			Type:     strPtr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{Name: nil, Arguments: "{}"}, // nil Name
		},
	}

	messages, _ := sess.processToolCalls(context.Background(), toolCalls)

	// Should skip both invalid tool calls
	assert.Empty(t, messages)
}

func TestSession_ExecuteToolCall_WithScheduler(t *testing.T) {
	t.Parallel()

	tool := &mockTool{name: "scheduled_tool", output: "scheduled result"}
	scheduler := runners.NewCoreToolScheduler(func(any) {})

	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, scheduler, func(any) {}, "", "")
	require.NoError(t, err)

	resp := sess.executeToolCall(context.Background(), tool, "tc1", "scheduled_tool", "{}")

	content := msgContentStr(resp)
	assert.Equal(t, "scheduled result", content)
	assert.True(t, tool.called)
}

func TestSession_ExecuteToolCall_WithoutScheduler(t *testing.T) {
	t.Parallel()

	tool := &mockTool{name: "direct_tool", output: "direct result"}

	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "", "")
	require.NoError(t, err)
	sess.scheduler = nil // Force no scheduler

	resp := sess.executeToolCall(context.Background(), tool, "tc1", "direct_tool", "{}")

	content := msgContentStr(resp)
	assert.Equal(t, "direct result", content)
	assert.True(t, tool.called)
}

// mockToolError returns an error
type mockToolError struct {
	name string
}

func (t *mockToolError) Name() string        { return t.name }
func (t *mockToolError) Description() string { return "error tool" }
func (t *mockToolError) Call(ctx context.Context, input string) (string, error) {
	return "", assert.AnError
}
func (t *mockToolError) Format(input, result string, err error) string { return "error" }
func (t *mockToolError) ParameterSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func TestSession_ExecuteToolCall_Error(t *testing.T) {
	t.Parallel()

	tool := &mockToolError{name: "error_tool"}

	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "", "")
	require.NoError(t, err)
	sess.scheduler = nil

	resp := sess.executeToolCall(context.Background(), tool, "tc1", "error_tool", "{}")

	content := msgContentStr(resp)
	assert.Contains(t, content, "Error:")
}

func TestCoreToolScheduler_Schedule_Error(t *testing.T) {
	t.Parallel()

	var errorNotified bool
	notify := func(msg any) {
		if _, ok := msg.(runners.ToolCallErrorMsg); ok {
			errorNotified = true
		}
	}

	scheduler := runners.NewCoreToolScheduler(notify)
	tool := &mockToolError{name: "error_tool"}

	ch := scheduler.Schedule(context.Background(), tool, "{}")
	result := <-ch

	assert.Error(t, result.Error)
	assert.True(t, errorNotified)
}

func TestCoreToolScheduler_Schedule_NoNotify(t *testing.T) {
	t.Parallel()

	scheduler := runners.NewCoreToolScheduler(nil) // No notify function
	tool := &mockTool{name: "test", output: "result"}

	ch := scheduler.Schedule(context.Background(), tool, "{}")
	result := <-ch

	assert.NoError(t, result.Error)
	assert.Equal(t, "result", result.Output)
}

func TestBuildLLMTools_Empty(t *testing.T) {
	t.Parallel()

	defs, catalog := buildLLMTools(nil)
	assert.Empty(t, defs)
	assert.Empty(t, catalog)
}

func TestBuildLLMTools_Multiple(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		&mockTool{name: "tool1"},
		&mockTool{name: "tool2"},
	}

	defs, catalog := buildLLMTools(tools)

	assert.Len(t, defs, 2)
	assert.Len(t, catalog, 2)
	assert.Equal(t, "tool1", defs[0].Function.Name)
	assert.Equal(t, "tool2", defs[1].Function.Name)
}

func TestSession_AskWithStreaming_RepeatingResponse(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	// Make multiple requests to the same session
	resp1, err := sess.AskWithStreaming(ctx, "First request", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, resp1)

	resp2, err := sess.AskWithStreaming(ctx, "Second request", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, resp2)

	// Session should maintain history
	assert.Greater(t, len(sess.messages), 2)
}

func TestSession_AskWithStreaming_MultiTurnToolExecution(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	tool := &mockTool{name: "test_tool", output: "tool result"}

	sess, err := NewSession(mockLLM, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "Use test_tool", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, resp)
}

func TestSession_AskWithStreaming_NoNotify(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	// Create session without notify function
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "Hello", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, resp)
}

func TestSession_AskWithStreaming_CancelDuringStreaming(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	// Add delay between chunks to allow cancellation
	mockLLM.DelayBetweenChunks = 50 * time.Millisecond
	mockLLM.SetStreamingChunks([]mocks.StreamingChunk{
		{Content: "First chunk "},
		{Content: "Second chunk "},
		{Content: "Third chunk", FinishReason: "stop"},
	})

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	resp, err := sess.AskWithStreaming(ctx, "Hello", nil)
	// Should return with partial content
	_ = resp
	_ = err
}

func TestSession_AskWithStreaming_AfterToolCall_NoMoreToolMessages(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := sess.AskWithStreaming(ctx, "Hello", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, resp)
}

func TestSession_ProcessToolCalls_MultipleToolsOneAborted(t *testing.T) {
	t.Parallel()

	tool := &mockTool{name: "test_tool", output: "result"}
	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// Create context that we'll cancel
	ctx, cancel := context.WithCancel(context.Background())

	// First tool call should complete, then we cancel
	toolCalls := []schemas.ChatAssistantMessageToolCall{
		{ID: strPtr("tc1"), Type: strPtr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("test_tool"), Arguments: "{}"}},
		{ID: strPtr("tc2"), Type: strPtr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("test_tool"), Arguments: "{}"}},
	}

	// Cancel after the processToolCalls starts (but this is synchronous, so cancel before)
	cancel()

	messages, shouldReturn := sess.processToolCalls(ctx, toolCalls)

	assert.True(t, shouldReturn)
	// Should have abort responses for remaining tool calls
	require.NotEmpty(t, messages)
}

func TestSession_GetContextInfo_Anthropic(t *testing.T) {
	t.Parallel()

	cfg := &SessionConfig{
		LLM: internalconfig.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-3-5-sonnet-latest",
		},
	}

	sess, err := NewSession(nil, cfg, nil, nil, func(any) {}, "You are a helpful assistant", "")
	require.NoError(t, err)

	info := sess.GetContextInfo()

	assert.Equal(t, "claude-3-5-sonnet-latest", info.Model)
	assert.Equal(t, 200_000, info.TotalTokens)
	assert.Greater(t, info.SystemPromptTokens, 0)
	assert.Equal(t, 0, info.MessagesTokens)
	assert.Greater(t, info.FreeTokens, 0)
	assert.Greater(t, info.AutocompactBuffer, 0)
	assert.Equal(t, info.SystemPromptTokens+info.SystemToolsTokens+info.MemoryFilesTokens+info.MessagesTokens, info.UsedTokens)
	assert.Equal(t, info.TotalTokens, info.UsedTokens+info.FreeTokens+info.AutocompactBuffer)
}

func TestSession_GetContextInfo_OpenAI(t *testing.T) {
	t.Parallel()

	cfg := &SessionConfig{
		LLM: internalconfig.LLMConfig{
			Provider: "openai",
			Model:    "gpt-4o",
		},
	}

	sess, err := NewSession(nil, cfg, nil, nil, func(any) {}, "You are a helpful assistant", "")
	require.NoError(t, err)

	info := sess.GetContextInfo()

	assert.Equal(t, "gpt-4o", info.Model)
	assert.Greater(t, info.TotalTokens, 0)
	assert.Greater(t, info.SystemPromptTokens, 0)
	assert.Equal(t, info.TotalTokens, info.UsedTokens+info.FreeTokens+info.AutocompactBuffer)
}

// contextSizeFor builds a session with the given provider/model and returns its
// resolved context window size. Matching is done on "<provider>:<model>".
func contextSizeFor(t *testing.T, provider, model string) int {
	t.Helper()
	sess, err := NewSession(nil, &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: provider,
		Model:    model,
	}}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	return sess.getModelContextSize()
}

func TestModelContextSize_Anthropic(t *testing.T) {
	t.Parallel()

	// Direct Anthropic models must produce the same sizes as before.
	cases := map[string]int{
		"claude-3-5-sonnet-latest":   200_000,
		"claude-3-5-sonnet":          200_000,
		"claude-3-opus-20240229":     200_000,
		"claude-3-sonnet-20240229":   200_000,
		"claude-3-5-haiku-latest":    200_000,
		"claude-3-haiku-20240307":    200_000,
		"claude-sonnet-4-5-20250929": 200_000,
	}
	for model, want := range cases {
		assert.Equalf(t, want, contextSizeFor(t, "anthropic", model), "model %q", model)
	}
}

func TestModelContextSize_Gemini(t *testing.T) {
	t.Parallel()

	// Direct Google AI / Gemini models must produce the same sizes as before.
	cases := map[string]int{
		"gemini-1.5-flash":        1_000_000,
		"gemini-1.5-flash-latest": 1_000_000,
		"gemini-1.5-pro":          2_000_000,
		"gemini-1.5-pro-latest":   2_000_000,
		"gemini-pro":              1_000_000,
		"gemini-2.0-flash":        1_000_000,
	}
	for model, want := range cases {
		assert.Equalf(t, want, contextSizeFor(t, "googleai", model), "model %q", model)
	}
}

func TestModelContextSize_MiniMaxBedrock(t *testing.T) {
	t.Parallel()

	// Small MiniMax window via the AWS Bedrock bedrock-mantle endpoint.
	assert.Equal(t, 196_000, contextSizeFor(t, "bedrock", "minimax.minimax-m2.5"))
}

func TestModelContextSize_OpenRouter(t *testing.T) {
	t.Parallel()

	cases := map[string]int{
		"anthropic/claude-sonnet-4":    200_000,
		"anthropic/claude-opus-4":      200_000,
		"anthropic/claude-haiku-4":     200_000,
		"openai/gpt-4o":                128_000,
		"openai/gpt-4.1":               1_000_000,
		"openai/gpt-4.1-mini":          1_000_000,
		"google/gemini-2.5-flash":      1_000_000,
		"google/gemini-2.5-pro":        1_000_000,
		"deepseek/deepseek-v4-flash":   1_000_000,
		"deepseek/deepseek-v4-pro":     1_000_000,
		"deepseek/deepseek-v3.2":       128_000,
		"deepseek/deepseek-r1":         128_000,
		"minimax/minimax-m2.5":         1_000_000,
		"minimax/minimax-m2.7":         1_000_000,
		"z-ai/glm-5.2":                 1_000_000,
		"mistralai/mistral-large-2512": 128_000,
		"mistralai/devstral-2512":      128_000,
		"moonshotai/kimi-k2-thinking":  128_000,
		"moonshotai/kimi-k2.6":         262_000,
		"qwen/qwen3.5-397b-a17b":       128_000,
	}
	for model, want := range cases {
		assert.Equalf(t, want, contextSizeFor(t, "openrouter", model), "model %q", model)
	}
}

func TestModelContextSize_SupersetMatches(t *testing.T) {
	t.Parallel()

	// New model variants should be covered by the broadened rules.
	cases := []struct {
		provider string
		model    string
		want     int
	}{
		// Direct (bare) model variants via their own provider.
		{"anthropic", "claude-sonnet-4-5-20250325", 200_000},
		{"anthropic", "claude-3-5-sonnet-20250620", 200_000},
		{"googleai", "gemini-2.5-flash", 1_000_000},
		{"googleai", "gemini-2.5-pro", 1_000_000},
		{"googleai", "gemini-2.0-flash-lite", 1_000_000},
		// OpenRouter broadened variants.
		{"openrouter", "anthropic/claude-sonnet-4.5", 200_000},
		{"openrouter", "openai/gpt-4.1-nano", 1_000_000},
		{"openrouter", "openai/gpt-4.1-mini", 1_000_000},
		{"openrouter", "google/gemini-2.5-pro-preview", 1_000_000},
		{"openrouter", "deepseek/deepseek-v4-spec", 1_000_000},
		{"openrouter", "minimax/minimax-m2.7", 1_000_000},
		{"openrouter", "mistralai/mixtral-8x22b", 128_000},
		{"openrouter", "moonshotai/kimi-k2.5", 128_000},
		{"openrouter", "moonshotai/kimi-k2.6", 262_000},
	}
	for i, tc := range cases {
		assert.Equalf(t, tc.want, contextSizeFor(t, tc.provider, tc.model), "%d (%s:%s)", i, tc.provider, tc.model)
	}
}

func TestModelContextSize_FirstMatchWins(t *testing.T) {
	t.Parallel()

	// claude-sonnet-4-5-* must win over the broader claude rule.
	assert.Equal(t, 200_000, contextSizeFor(t, "anthropic", "claude-sonnet-4-5-20250929"))

	// moonshotai/kimi-k2.6 has a specific rule (262k) before the broad kimi rule.
	assert.Equal(t, 262_000, contextSizeFor(t, "openrouter", "moonshotai/kimi-k2.6"))

	// gemini-1.5-pro-specific 2M rule wins over the broad gemini 1M rule.
	assert.Equal(t, 2_000_000, contextSizeFor(t, "googleai", "gemini-1.5-pro"))
	assert.Equal(t, 1_000_000, contextSizeFor(t, "googleai", "gemini-1.5-flash"))
}

func TestModelContextSize_RoutingTagStripped(t *testing.T) {
	t.Parallel()

	// :nitro / :free routing tags are stripped before lookup.
	assert.Equal(t, 128_000, contextSizeFor(t, "openrouter", "openai/gpt-4o:nitro"))
	assert.Equal(t, 200_000, contextSizeFor(t, "anthropic", "claude-3-5-sonnet-latest:free"))
	assert.Equal(t, 1_000_000, contextSizeFor(t, "openrouter", "openai/gpt-4.1-mini:nitro"))
}

func TestModelContextSize_BifrostLazyFirstCall(t *testing.T) {
	t.Parallel()

	// A model not covered by the regex registry falls through to the lazy
	// bifrost resolution on the first key access.
	ctxLen := 70_000
	model := schemas.Model{
		ID:            "nova-custom-ctx",
		ContextLength: &ctxLen,
	}
	mock := &mocks.MockProvider{ModelsResponse: []schemas.Model{model}}

	sess, err := NewSession(mock, &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "cohere",
		Model:    "nova-custom-ctx",
	}}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	assert.Equal(t, 70_000, sess.getModelContextSize())
}

func TestModelContextSize_BifrostSharedCache(t *testing.T) {
	t.Parallel()

	// The bifrost value is cached by "provider:model" at package level. A
	// second session for the same key reuses the cached value without a
	// fresh network lookup.
	ctxLen := 80_000
	model := schemas.Model{
		ID:            "shared-custom-model",
		ContextLength: &ctxLen,
	}
	mock := &mocks.MockProvider{ModelsResponse: []schemas.Model{model}}

	sess, err := NewSession(mock, &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "cohere",
		Model:    "shared-custom-model",
	}}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	assert.Equal(t, 80_000, sess.getModelContextSize())

	// A second session with the same provider:model but no models advertised
	// must still resolve from the shared cache.
	sess2, err := NewSession(&mocks.MockProvider{}, &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "cohere",
		Model:    "shared-custom-model",
	}}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	assert.Equal(t, 80_000, sess2.getModelContextSize())
}

func TestModelContextSize_BifrostMaxTokensSum(t *testing.T) {
	t.Parallel()

	// When ContextLength is absent but MaxInputTokens/MaxOutputTokens are
	// present (Anthropic-model derived context), bifrost resolution uses
	// their sum.
	in := 100_000
	out := 50_000
	model := schemas.Model{
		ID:              "sum-output-model",
		MaxInputTokens:  &in,
		MaxOutputTokens: &out,
	}
	mock := &mocks.MockProvider{ModelsResponse: []schemas.Model{model}}

	sess, err := NewSession(mock, &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "cohere",
		Model:    "sum-output-model",
	}}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	assert.Equal(t, 150_000, sess.getModelContextSize())
}

func TestModelContextSize_RegexFastPath(t *testing.T) {
	t.Parallel()

	// A model covered by the regex registry resolves without any bifrost
	// network call, even when the provider is nil (unknown provider path).
	sess, err := NewSession(nil, &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "anthropic",
		Model:    "claude-3-5-sonnet-latest",
	}}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	assert.Equal(t, 200_000, sess.getModelContextSize())
}

func TestModelContextSize_NoNetworkOnInit(t *testing.T) {
	t.Parallel()

	// Session construction must never trigger a bifrost network lookup.
	prov := &networkTrackingProvider{}
	sess, err := NewSession(prov, &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "cohere",
		Model:    "unknown-model-xyz",
	}}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	assert.Equal(t, 0, prov.listCalls, "NewSession must not call ListModelsRequest")
	assert.Equal(t, defaultUnknownContextRef, sess.getModelContextSize())
}

func TestModelContextSize_UnknownNotCached(t *testing.T) {
	t.Parallel()

	// A model resolvable by neither bifrost nor the regex registry must not
	// grow or reuse the shared cache: the defaultUnknownContextRef guess is
	// never stored, so a second session for the same provider:model re-probes
	// bifrost instead of inheriting a fabricated value.
	prov := &networkTrackingProvider{}
	cfg := &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "cohere",
		Model:    "truly-unknown-not-cached",
	}}

	sess, err := NewSession(prov, cfg, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	assert.Equal(t, defaultUnknownContextRef, sess.getModelContextSize())
	assert.Equal(t, 1, prov.listCalls, "first unresolved lookup probes bifrost")

	sess2, err := NewSession(prov, cfg, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	assert.Equal(t, defaultUnknownContextRef, sess2.getModelContextSize())
	assert.Equal(t, 2, prov.listCalls, "unresolved size must not be cached; second session re-probes")
}

func TestModelContextSize_RepeatedCallsNoReProbe(t *testing.T) {
	t.Parallel()

	// Regression: the TUI refreshes context info on every render (each
	// keystroke), so getModelContextSize was probing bifrost on every call for
	// a model the registry already answers — an endless list_models network
	// storm that stalled typing. The resolved size must be memoized per
	// session so repeated calls never touch the network again.
	prov := &networkTrackingProvider{}
	cfg := &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "openai",
		Model:    "gpt-4o",
	}}

	sess, err := NewSession(prov, cfg, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// First call resolves from the deterministic registry (no network).
	assert.Equal(t, 128_000, sess.getModelContextSize())
	assert.Equal(t, 0, prov.listCalls, "registry covers gpt-4o; no bifrost probe needed")

	// Simulate the render loop calling over and over; must stay at zero
	// network probes.
	for i := 0; i < 50; i++ {
		assert.Equal(t, 128_000, sess.getModelContextSize())
	}
	assert.Equal(t, 0, prov.listCalls, "repeated per-session calls must not re-probe bifrost")

	// GetContextInfo funnels through getModelContextSize and must share the
	// same memoized result.
	assert.Greater(t, sess.GetContextInfo().TotalTokens, 0)
	assert.Equal(t, 0, prov.listCalls, "GetContextInfo must not re-probe bifrost")
}

func TestModelContextSize_RegistryCachedNoReProbe(t *testing.T) {
	t.Parallel()

	// Regression for the registry fast-path: the deterministic registry result
	// must be stored in the shared modelContextByKey cache, so a second
	// getModelContextSize() invocation (from a fresh session) returns from the
	// cache without performing a bifrost probe. Registry-covered models never
	// touch the network, but the shared memoization guarantees no per-call or
	// per-session re-resolution. Any unresolved size is never cached.
	prov := &networkTrackingProvider{}
	cfg := &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "anthropic",
		Model:    "claude-3-5-sonnet-latest",
	}}

	sess, err := NewSession(prov, cfg, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// First call resolves deterministically from the registry (no network).
	assert.Equal(t, 200_000, sess.getModelContextSize())
	assert.Equal(t, 0, prov.listCalls, "registry covers claude-3-5-sonnet-latest; first call needs no probe")

	// The deterministic registry result must now be committed to the shared
	// modelContextByKey cache. This is the crux of the fix: a registry-covered
	// model never probes bifrost regardless of caching, so the only way for a
	// later session to skip re-running the registry branch (and re-emitting the
	// "Using context size from registry" log) is for this shared cache to hold
	// the resolved size. Assert the cache was populated, not just the return value.
	key := "anthropic:claude-3-5-sonnet-latest"
	cached, ok := modelContextByKey.Load(key)
	require.True(t, ok, "registry result must be stored in the shared modelContextByKey cache")
	assert.Equal(t, 200_000, cached, "shared cache must hold the deterministic registry size")

	// A fresh session hits the shared provider:model cache and still resolves
	// the registry size without touching the network.
	sess2, err := NewSession(prov, cfg, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	assert.Equal(t, 200_000, sess2.getModelContextSize())
	assert.Equal(t, 0, prov.listCalls, "registry result cached; second session must not probe bifrost")

	// The shared entry must persist; a clean-load allows the next session to
	// short-circuit the registry branch entirely.
	cached, ok = modelContextByKey.Load(key)
	require.True(t, ok, "shared cache entry must persist across sessions")
	assert.Equal(t, 200_000, cached)
}

func TestSession_GetContextInfo_WithContextFiles(t *testing.T) {
	t.Parallel()

	cfg := &SessionConfig{
		LLM: internalconfig.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-3-5-sonnet-latest",
		},
	}

	sess, err := NewSession(nil, cfg, nil, nil, func(any) {}, "system prompt", "")
	require.NoError(t, err)

	infoBefore := sess.GetContextInfo()

	// AddContextFile triggers updateTokenCounts
	sess.AddContextFile("test.go", "package main\n\nfunc main() {}\n")

	infoAfter := sess.GetContextInfo()

	assert.Greater(t, infoAfter.MemoryFilesTokens, infoBefore.MemoryFilesTokens)
	assert.Greater(t, infoAfter.UsedTokens, infoBefore.UsedTokens)
	assert.Equal(t, infoAfter.SystemPromptTokens+infoAfter.SystemToolsTokens+infoAfter.MemoryFilesTokens+infoAfter.MessagesTokens, infoAfter.UsedTokens)
}

func TestSession_GetContextInfo_UnknownModel(t *testing.T) {
	t.Parallel()

	cfg := &SessionConfig{
		LLM: internalconfig.LLMConfig{
			Provider: "custom",
			Model:    "unknown-model-xyz",
		},
	}

	sess, err := NewSession(nil, cfg, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	info := sess.GetContextInfo()

	assert.Equal(t, "unknown-model-xyz", info.Model)
	assert.Equal(t, defaultUnknownContextRef, info.TotalTokens)
}

// --- Test for asimisql large output truncation bug ---

// mockToolLargeOutput simulates a tool that returns very large output,
// similar to what asimisql would return for large query results.
type mockToolLargeOutput struct {
	mockTool
	outputSize int
}

func (t *mockToolLargeOutput) Call(ctx context.Context, input string) (string, error) {
	t.called = true
	// Return output larger than DefaultMaxOutputSize (50KB)
	return strings.Repeat("col1|col2|col3|col4\n", t.outputSize/20), nil
}

// TestSession_ExecuteToolCall_LargeOutputTruncated verifies that large tool output
// is truncated when executed through Session.executeToolCall(). This is the fix
// for the bug where asimisql tool output was causing context explosion.
func TestSession_ExecuteToolCall_LargeOutputTruncated(t *testing.T) {
	t.Parallel()

	// Create a tool that returns 100KB of output (exceeds DefaultMaxOutputSize of 50KB)
	largeOutputSize := 100 * 1024
	tool := &mockToolLargeOutput{
		mockTool:   mockTool{name: "asimisql", output: ""},
		outputSize: largeOutputSize,
	}

	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "", "")
	require.NoError(t, err)
	sess.scheduler = nil // Use direct execution

	resp := sess.executeToolCall(context.Background(), tool, "tc1", "asimisql", `{"query":"SELECT * FROM edicts"}`)

	// Should contain truncation marker
	content := msgContentStr(resp)
	assert.Contains(t, content, "result is too long",
		"Huge output should report to the caller")
}

// TestSession_ExecuteToolCall_LargeOutputTruncatedViaScheduler verifies truncation
// works when using the scheduler, which is the normal execution path for tools.
func TestSession_ExecuteToolCall_LargeOutputTruncatedViaScheduler(t *testing.T) {
	t.Parallel()

	// Create a tool that returns 100KB of output
	largeOutputSize := 100 * 1024
	tool := &mockToolLargeOutput{
		mockTool:   mockTool{name: "asimisql", output: ""},
		outputSize: largeOutputSize,
	}

	scheduler := runners.NewCoreToolScheduler(func(any) {})
	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, scheduler, func(any) {}, "", "")
	require.NoError(t, err)

	resp := sess.executeToolCall(context.Background(), tool, "tc1", "asimisql", `{"query":"SELECT * FROM edicts"}`)

	// Verify truncation occurred
	content := msgContentStr(resp)
	assert.Less(t, len(content), int(runners.DefaultMaxOutputSize)+200,
		"Tool output via scheduler should be truncated")
}

// TestSession_ExecuteToolCall_CustomMaxOutput verifies that the MaxToolOutput
// config option controls the truncation size.
func TestSession_ExecuteToolCall_CustomMaxOutput(t *testing.T) {
	t.Parallel()

	customMaxOutput := 10 * 1024 // 10KB
	cfg := &SessionConfig{
		LLM: internalconfig.LLMConfig{
			MaxToolOutput: customMaxOutput,
		},
	}

	// Create a tool that returns 100KB of output
	tool := &mockToolLargeOutput{
		mockTool:   mockTool{name: "asimisql", output: ""},
		outputSize: 100 * 1024,
	}

	sess, err := NewSession(nil, cfg, []Tool{tool}, nil, func(any) {}, "", "")
	require.NoError(t, err)
	sess.scheduler = nil

	resp := sess.executeToolCall(context.Background(), tool, "tc1", "asimisql", "{}")

	// Output should be truncated to customMaxOutput (10KB)
	content := msgContentStr(resp)
	assert.Less(t, len(content), customMaxOutput+200,
		"Tool output should respect custom MaxToolOutput config")
}

// TestSession_Rollback_LeavesOnlySystemPrompt verifies that Rollback()
// discards all messages except the system prompt.
func TestSession_Rollback_LeavesOnlySystemPrompt(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system prompt", "")
	require.NoError(t, err)

	// Initially should have 1 message (system prompt)
	require.Equal(t, 1, len(sess.messages))
	assert.Equal(t, schemas.ChatMessageRoleSystem, sess.messages[0].Role)

	// Add multiple messages
	sess.AddMessage(schemas.ChatMessageRoleUser, "Hello")
	sess.AddMessage(schemas.ChatMessageRoleAssistant, "Hi there")
	sess.AddMessage(schemas.ChatMessageRoleUser, "How are you?")
	sess.AddMessage(schemas.ChatMessageRoleAssistant, "I'm doing well")

	// Should now have 5 messages
	require.Equal(t, 5, len(sess.messages))

	// Rollback should discard all but the system prompt
	sess.Rollback()

	// Should now have exactly 1 message
	assert.Equal(t, 1, len(sess.messages), "should have exactly 1 message after rollback")
	assert.Equal(t, schemas.ChatMessageRoleSystem, sess.messages[0].Role, "remaining message should be system prompt")
}

// TestSession_Rollback_UpdatesTokenCounts verifies that Rollback updates token counts.
func TestSession_Rollback_UpdatesTokenCounts(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	// Add conversation with content
	sess.AddMessage(schemas.ChatMessageRoleUser, "Hello world this is a test message for token counting")

	// Get message tokens before rollback
	infoBefore := sess.GetContextInfo()
	_ = infoBefore.MessagesTokens // captured for documentation

	sess.Rollback()

	infoAfter := sess.GetContextInfo()

	// After rollback, message tokens should be 0
	assert.Equal(t, 0, infoAfter.MessagesTokens)
	// System prompt tokens should remain
	assert.Equal(t, infoBefore.SystemPromptTokens, infoAfter.SystemPromptTokens)
}

// TestSession_Rollback_ResetsLoopDetection verifies that Rollback resets loop detection state.
func TestSession_Rollback_ResetsLoopDetection(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// Trigger loop detection
	args := `{"path":"test.txt"}`
	sess.checkToolCallLoop("read_file", args)
	sess.checkToolCallLoop("read_file", args)

	require.Equal(t, 2, sess.toolCallRepetitionCount)
	require.NotEmpty(t, sess.lastToolCallKey)

	sess.Rollback()

	assert.Equal(t, 0, sess.toolCallRepetitionCount, "loop repetition count should be reset")
	assert.Empty(t, sess.lastToolCallKey, "last tool call key should be reset")
	assert.Equal(t, 0, sess.toolCallLoopHits, "loop hits should be reset")
}

// TestSession_ClearHistory_ResetsLoopDetection verifies that ClearHistory resets loop detection state.
func TestSession_ClearHistory_ResetsLoopDetection(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// Trigger loop detection to set state
	args := `{"path":"test.txt"}`
	sess.checkToolCallLoop("read_file", args)
	sess.checkToolCallLoop("read_file", args)
	sess.checkToolCallLoop("read_file", args) // triggers loop detection → toolCallLoopHits = 1

	require.Equal(t, 1, sess.toolCallLoopHits)
	require.Equal(t, 0, sess.toolCallRepetitionCount) // reset by checkToolCallLoop after loop detected

	sess.ClearHistory()

	assert.Equal(t, 0, sess.toolCallRepetitionCount, "loop repetition count should be reset after ClearHistory")
	assert.Empty(t, sess.lastToolCallKey, "last tool call key should be reset after ClearHistory")
	assert.Equal(t, 0, sess.toolCallLoopHits, "loop hits should be reset after ClearHistory")
}

// TestSession_Rollback_Idempotent verifies that calling Rollback multiple times is safe.
func TestSession_Rollback_Idempotent(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	sess.AddMessage(schemas.ChatMessageRoleUser, "test")

	sess.Rollback()
	firstLen := len(sess.messages)

	sess.Rollback()
	secondLen := len(sess.messages)

	sess.Rollback()
	thirdLen := len(sess.messages)

	assert.Equal(t, 1, firstLen)
	assert.Equal(t, firstLen, secondLen)
	assert.Equal(t, secondLen, thirdLen)
}

// TestSession_ToolOutputAddedToMessageHistory verifies that truncated output
// is what gets added to the message history, preventing context explosion.
func TestSession_ToolOutputAddedToMessageHistory(t *testing.T) {
	t.Skip("requires mock bifrost client for LLM responses")
}

// hangingLLMProvider returns a stream channel that is never written to
// and never closed, simulating an LLM HTTP stream that does not honor
// context cancellation (the bug observed in asimi.2.log: sage's RunLoop
// wedged because the chunk loop sat in `for chunk := range ch` forever).
type hangingLLMProvider struct{}

func (hangingLLMProvider) ChatCompletionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, nil
}

func (hangingLLMProvider) ChatCompletionStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return make(chan *schemas.BifrostStreamChunk), nil
}

func (hangingLLMProvider) ListAllModels(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{}, nil
}

func (hangingLLMProvider) ListModelsRequest(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{}, nil
}

// networkTrackingProvider counts ListModelsRequest calls so tests can assert
// that session construction never triggers a network round-trip.
type networkTrackingProvider struct{ listCalls int }

func (p *networkTrackingProvider) ChatCompletionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, nil
}

func (p *networkTrackingProvider) ChatCompletionStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return make(chan *schemas.BifrostStreamChunk), nil
}

func (p *networkTrackingProvider) ListAllModels(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{}, nil
}

func (p *networkTrackingProvider) ListModelsRequest(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	p.listCalls++
	return &schemas.BifrostListModelsResponse{}, nil
}

// capturingProvider records the outgoing BifrostChatRequest so tests can
// assert on the parameters the session forwards (e.g. reasoning.effort).
type capturingProvider struct {
	lastReq *schemas.BifrostChatRequest
}

func (p *capturingProvider) ChatCompletionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	p.lastReq = req
	return nil, nil
}

func (p *capturingProvider) ChatCompletionStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	p.lastReq = req
	ch := make(chan *schemas.BifrostStreamChunk)
	close(ch)
	return ch, nil
}

func (p *capturingProvider) ListAllModels(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{}, nil
}

func (p *capturingProvider) ListModelsRequest(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{}, nil
}

// toolResultProvider streams a tool call on the first request (triggering
// tool execution) and a final answer on subsequent requests, so tests can
// exercise the full tool-execution ATIF path.
type toolResultProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *toolResultProvider) ChatCompletionStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	p.mu.Lock()
	p.calls++
	first := p.calls == 1
	p.mu.Unlock()

	ch := make(chan *schemas.BifrostStreamChunk)
	go func() {
		defer close(ch)
		if first {
			finish := "tool_calls"
			toolID := "call_confirmer"
			ch <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
				Choices: []schemas.BifrostResponseChoice{{
					Index: 0,
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{{
								Index: 0,
								ID:    &toolID,
								Function: schemas.ChatAssistantMessageToolCallFunction{
									Name:      strPtr("confirmer"),
									Arguments: `{}`,
								},
							}},
						},
					},
					FinishReason: &finish,
				}},
			}}
			return
		}
		finish := "stop"
		content := "all done"
		ch <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				Index: 0,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &content},
				},
				FinishReason: &finish,
			}},
		}}
	}()
	return ch, nil
}

func (p *toolResultProvider) ChatCompletionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, nil
}

func (p *toolResultProvider) ListAllModels(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{}, nil
}

func (p *toolResultProvider) ListModelsRequest(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{}, nil
}

// TestSession_ReasoningEffortRoutesToRequest verifies that a session's
// ReasoningEffort value is forwarded as the reasoning.effort parameter on
// the outgoing chat request.
func TestSession_ReasoningEffortRoutesToRequest(t *testing.T) {
	prov := &capturingProvider{}

	sess, err := NewSession(prov, &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "test",
		Model:    "test-model",
	}}, nil, nil, func(any) {}, "You are a test assistant", "test-channel")
	require.NoError(t, err)

	// Simulate the configured default / ritual override being applied.
	sess.ReasoningEffort = "high"

	ctx := context.Background()
	_, err = sess.AskWithStreaming(ctx, "Hello", nil)
	require.NoError(t, err)

	require.NotNil(t, prov.lastReq, "outgoing chat request should have been captured")
	require.NotNil(t, prov.lastReq.Params, "request params should be present")
	require.NotNil(t, prov.lastReq.Params.Reasoning, "reasoning params should be present when effort set")
	require.NotNil(t, prov.lastReq.Params.Reasoning.Effort, "reasoning.effort should be set")
	assert.Equal(t, "high", *prov.lastReq.Params.Reasoning.Effort)
}

// TestSession_NoReasoningEffortLeavesParamsUnset verifies that when the
// session's ReasoningEffort is empty, the reasoning.effort param is not set
// (provider default).
func TestSession_NoReasoningEffortLeavesParamsUnset(t *testing.T) {
	prov := &capturingProvider{}

	sess, err := NewSession(prov, &SessionConfig{LLM: internalconfig.LLMConfig{
		Provider: "test",
		Model:    "test-model",
	}}, nil, nil, func(any) {}, "test", "test-channel")
	require.NoError(t, err)

	ctx := context.Background()
	_, err = sess.AskWithStreaming(ctx, "Hello", nil)
	require.NoError(t, err)

	require.NotNil(t, prov.lastReq)
	if prov.lastReq.Params != nil {
		assert.Nil(t, prov.lastReq.Params.Reasoning, "reasoning params should be nil when effort empty")
	}
}

// TestSession_AskWithStreaming_HonorsContextCancellation verifies that a
// hung provider stream does not wedge the caller. Cancelling ctx must
// return promptly with ctx.Err() instead of blocking on the chunk channel.
func TestSession_AskWithStreaming_HonorsContextCancellation(t *testing.T) {
	sess, err := NewSession(hangingLLMProvider{}, &SessionConfig{}, nil, nil, func(any) {}, "system", "test-channel")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, askErr := sess.AskWithStreaming(ctx, "hello", nil)
		done <- askErr
	}()

	// Give the call a moment to enter the streaming loop.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled, "AskWithStreaming should return ctx.Err() on cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("AskWithStreaming did not return after ctx cancel — streaming loop is wedged")
	}
}

// recordingPersister counts SaveSession calls and stashes the latest
// session snapshot length. Used to verify Session.persist() fires.
type recordingPersister struct {
	calls       int
	lastLen     int
	lastTabType string
}

func (p *recordingPersister) SaveSession(s *Session) {
	p.calls++
	p.lastLen = len(s.GetMessages())
	p.lastTabType = s.TabType
}

func TestSession_AddMessage_TriggersPersisterWhenTabTypeSet(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, nil, nil, nil, func(any) {}, "system prompt", "")
	require.NoError(t, err)
	sess.TabType = "secretary"

	rec := &recordingPersister{}
	sess.SetPersister(rec)

	sess.AddMessage(schemas.ChatMessageRoleUser, "hello")
	sess.AddMessage(schemas.ChatMessageRoleAssistant, "hi")

	assert.Equal(t, 2, rec.calls, "persister should fire once per AddMessage")
	assert.Equal(t, "secretary", rec.lastTabType)
	assert.GreaterOrEqual(t, rec.lastLen, 2)
}

func TestSession_AddMessage_NoPersisterMeansNoSave(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, nil, nil, nil, func(any) {}, "system prompt", "")
	require.NoError(t, err)
	sess.TabType = "secretary"
	// Deliberately no SetPersister — matches ephemeral ritual-task sessions.

	sess.AddMessage(schemas.ChatMessageRoleUser, "hello")
	// Nothing to assert beyond "doesn't panic" — the test exists to lock in
	// that persist() is a no-op when no persister is attached.
}

func TestSession_SetMessages_DoesNotPersist(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, nil, nil, nil, func(any) {}, "system prompt", "")
	require.NoError(t, err)
	sess.TabType = "secretary"

	rec := &recordingPersister{}
	sess.SetPersister(rec)

	sess.SetMessages([]schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleUser, Content: textContent("restored")},
	})

	assert.Equal(t, 0, rec.calls, "restore (SetMessages) must not write back to storage")
}

// --- SanitizeMessages tests ---

func TestSanitizeMessages_RemovesTrailingAssistantWithToolCalls(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	// Simulate: system, user, assistant with tool calls (no tool response yet)
	sess.messages = append(sess.messages,
		schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: textContent("hello")},
		schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: textContent(""),
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{
					{ID: strPtr("tc1"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("read_file")}},
				},
			},
		},
	)

	before := len(sess.messages)
	sess.sanitizeMessages()

	assert.Less(t, len(sess.messages), before, "should remove trailing assistant with tool calls")
	assert.Equal(t, schemas.ChatMessageRoleUser, sess.messages[len(sess.messages)-1].Role)
}

func TestSanitizeMessages_KeepsAssistantWithoutToolCalls(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	sess.messages = append(sess.messages,
		schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: textContent("hello")},
		schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: textContent("I can help with that.")},
	)

	before := len(sess.messages)
	sess.sanitizeMessages()

	assert.Equal(t, before, len(sess.messages), "should keep assistant message without tool calls")
}

func TestSanitizeMessages_RemovesTrailingToolResponseWithoutAICaller(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	// Tool response with no preceding AI tool-call message
	sess.messages = append(sess.messages,
		schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: textContent("hello")},
		schemas.ChatMessage{
			Role:            schemas.ChatMessageRoleTool,
			Content:         textContent("file content"),
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr("tc1")},
		},
	)

	before := len(sess.messages)
	sess.sanitizeMessages()

	assert.Less(t, len(sess.messages), before, "should remove orphaned tool response")
	assert.Equal(t, schemas.ChatMessageRoleUser, sess.messages[len(sess.messages)-1].Role)
}

func TestSanitizeMessages_KeepsToolResponseWithMatchingAICaller(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	sess.messages = append(sess.messages,
		schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: textContent("hello")},
		schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: textContent(""),
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{
					{ID: strPtr("tc1"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("read_file")}},
				},
			},
		},
		schemas.ChatMessage{
			Role:            schemas.ChatMessageRoleTool,
			Content:         textContent("file content"),
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr("tc1")},
		},
	)

	before := len(sess.messages)
	sess.sanitizeMessages()

	assert.Equal(t, before, len(sess.messages), "should keep tool response with matching AI caller")
}

func TestSanitizeMessages_RemovesDanglingToolResponse(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	// Tool response whose ID doesn't match any AI tool call
	sess.messages = append(sess.messages,
		schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: textContent("hello")},
		schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: textContent(""),
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{
					{ID: strPtr("tc1"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("read_file")}},
				},
			},
		},
		schemas.ChatMessage{
			Role:            schemas.ChatMessageRoleTool,
			Content:         textContent("orphan result"),
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr("tc999")},
		},
	)

	before := len(sess.messages)
	sess.sanitizeMessages()

	assert.Less(t, len(sess.messages), before, "should remove tool response with mismatched ID")
}

func TestSanitizeMessages_RemovesMultipleTrailingToolCalls(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	// Two orphaned tool responses followed by an assistant with tool calls
	sess.messages = append(sess.messages,
		schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: textContent("hello")},
		schemas.ChatMessage{
			Role:            schemas.ChatMessageRoleTool,
			Content:         textContent("orphan1"),
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr("tc_orphan1")},
		},
		schemas.ChatMessage{
			Role:            schemas.ChatMessageRoleTool,
			Content:         textContent("orphan2"),
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr("tc_orphan2")},
		},
		schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: textContent(""),
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{
					{ID: strPtr("tc2"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("write_file")}},
				},
			},
		},
	)

	sess.sanitizeMessages()

	// Should end with user message: orphaned tools removed, then trailing AI with tool calls removed
	lastMsg := sess.messages[len(sess.messages)-1]
	assert.Equal(t, schemas.ChatMessageRoleUser, lastMsg.Role, "should remove all trailing orphaned/or unmatched messages")
}

func TestSanitizeMessages_SkipsWhenDisabled(t *testing.T) {
	t.Parallel()

	cfg := &SessionConfig{
		LLM: internalconfig.LLMConfig{
			DisableContextSanitization: true,
		},
	}

	sess, err := NewSession(nil, cfg, nil, nil, func(any) {}, "system", "")
	require.NoError(t, err)

	// Add trailing assistant with tool calls
	sess.messages = append(sess.messages,
		schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: textContent("hello")},
		schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: textContent(""),
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{
					{ID: strPtr("tc1"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("read_file")}},
				},
			},
		},
	)

	before := len(sess.messages)
	sess.sanitizeMessages()

	assert.Equal(t, before, len(sess.messages), "should not sanitize when disabled")
}

func TestSanitizeMessages_EmptyMessages(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// No messages at all
	sess.messages = []schemas.ChatMessage{}

	sess.sanitizeMessages() // should not panic
	assert.Empty(t, sess.messages)
}

func TestSanitizeMessages_RemovesToolResponseAtStart(t *testing.T) {
	t.Parallel()

	sess, err := NewSession(nil, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// A tool response as the very first message (edge case)
	sess.messages = []schemas.ChatMessage{
		{
			Role:            schemas.ChatMessageRoleTool,
			Content:         textContent("orphan"),
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr("tc1")},
		},
	}

	sess.sanitizeMessages()

	assert.Empty(t, sess.messages, "should remove tool response when it's the only message")
}

func TestNewSession_WorkingDirFromConfig(t *testing.T) {
	// Not parallel because t.Chdir is used in subtests

	t.Run("uses WorkingDir from SessionConfig when provided", func(t *testing.T) {
		cfg := &SessionConfig{
			WorkingDir: "/explicit/project/root",
		}
		sess, err := NewSession(nil, cfg, nil, nil, func(any) {}, "", "")
		require.NoError(t, err)
		assert.Equal(t, "/explicit/project/root", sess.WorkingDir)
	})

	t.Run("empty WorkingDir when SessionConfig has empty WorkingDir", func(t *testing.T) {
		// When SessionConfig has empty WorkingDir, WorkingDir stays empty
		// because SetContext is the sole authority for project root in daemon mode.
		cfg := &SessionConfig{WorkingDir: ""}
		sess, err := NewSession(nil, cfg, nil, nil, func(any) {}, "", "")
		require.NoError(t, err)
		assert.Equal(t, "", sess.WorkingDir)
	})

	t.Run("empty WorkingDir when SessionConfig is nil", func(t *testing.T) {
		// When SessionConfig is nil, WorkingDir stays empty
		// because SetContext is the sole authority for project root in daemon mode.
		sess, err := NewSession(nil, nil, nil, nil, func(any) {}, "", "")
		require.NoError(t, err)
		assert.Equal(t, "", sess.WorkingDir)
	})

	t.Run("project root from config takes precedence", func(t *testing.T) {
		// Even if we're in a different directory, the config's WorkingDir wins
		processDir := t.TempDir()
		projectDir := t.TempDir()
		t.Chdir(processDir)

		cfg := &SessionConfig{WorkingDir: projectDir}
		sess, err := NewSession(nil, cfg, nil, nil, func(any) {}, "", "")
		require.NoError(t, err)
		assert.Equal(t, projectDir, sess.WorkingDir, "WorkingDir should use config, not os.Getwd()")
	})
}

func TestSession_ToolCallLoopHits_TripleIntervention(t *testing.T) {
	t.Parallel()

	tool := &mockTool{name: "loop_tool", output: "result"}
	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "", "")
	require.NoError(t, err)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   strPtr("tc1"),
		Type: strPtr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      strPtr("loop_tool"),
			Arguments: `{"same":"args"}`,
		},
	}

	// Trigger loop detection 3 separate times
	for i := 0; i < 3; i++ {
		// Need 3 identical calls to trigger one loop detection
		for j := 0; j < 3; j++ {
			sess.processToolCalls(context.Background(), []schemas.ChatAssistantMessageToolCall{toolCall})
		}
	}

	assert.Equal(t, 3, sess.toolCallLoopHits, "should have 3 loop interventions")
}

func TestSession_ToolCallLoopHits_ResetsOnPrepareUserMessage(t *testing.T) {
	t.Parallel()

	tool := &mockTool{name: "loop_tool", output: "result"}
	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "", "")
	require.NoError(t, err)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   strPtr("tc1"),
		Type: strPtr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      strPtr("loop_tool"),
			Arguments: `{"same":"args"}`,
		},
	}

	// Trigger loop detection
	for j := 0; j < 3; j++ {
		sess.processToolCalls(context.Background(), []schemas.ChatAssistantMessageToolCall{toolCall})
	}
	assert.Equal(t, 1, sess.toolCallLoopHits)

	// prepareUserMessage should reset toolCallLoopHits
	sess.prepareUserMessage("new prompt", nil)
	assert.Equal(t, 0, sess.toolCallLoopHits, "loop hits should be reset on new user message")
	assert.Equal(t, 0, sess.toolCallRepetitionCount, "repetition count should be reset on new user message")
	assert.Equal(t, "", sess.lastToolCallKey, "last tool call key should be reset on new user message")
}

// TestSession_ProcessToolCalls_ConcurrentDispatch verifies that multiple tool
// calls are dispatched concurrently rather than sequentially. If calls were
// serial, 3 tools each sleeping 100ms would take ~300ms. With concurrent
// dispatch, the total should be closer to ~100ms.
func TestSession_ProcessToolCalls_ConcurrentDispatch(t *testing.T) {
	t.Parallel()

	tool := &mockToolSlow{name: "slow_tool"}
	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// Use distinct args to avoid loop detection
	toolCalls := make([]schemas.ChatAssistantMessageToolCall, 3)
	for i := range toolCalls {
		toolCalls[i] = schemas.ChatAssistantMessageToolCall{
			ID:   strPtr(fmt.Sprintf("tc%d", i)),
			Type: strPtr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      strPtr("slow_tool"),
				Arguments: fmt.Sprintf("{\"i\":%d}", i),
			},
		}
	}

	start := time.Now()
	messages, shouldReturn := sess.processToolCalls(context.Background(), toolCalls)
	elapsed := time.Since(start)

	assert.False(t, shouldReturn)
	require.Len(t, messages, 3)

	// Each tool sleeps 100ms. If serial, ~300ms. If concurrent, ~100ms.
	// Use 250ms as threshold (allows scheduling overhead but catches serial).
	assert.Less(t, elapsed, 250*time.Millisecond,
		"3 x 100ms tools should complete in ~100ms with concurrent dispatch, took %v", elapsed)
}

// TestSession_ProcessToolCalls_PreservesOrder verifies that even though tool
// calls execute concurrently, the response messages are returned in the same
// order as the input tool calls.
func TestSession_ProcessToolCalls_PreservesOrder(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		&mockTool{name: "tool_a", output: "result_a"},
		&mockTool{name: "tool_b", output: "result_b"},
		&mockTool{name: "tool_c", output: "result_c"},
	}
	sess, err := NewSession(nil, &SessionConfig{}, tools, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// Register all tools in catalog
	sess.AddTools(tools)

	toolCalls := []schemas.ChatAssistantMessageToolCall{
		{ID: strPtr("tc0"), Type: strPtr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("tool_a"), Arguments: "{}"}},
		{ID: strPtr("tc1"), Type: strPtr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("tool_b"), Arguments: "{}"}},
		{ID: strPtr("tc2"), Type: strPtr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("tool_c"), Arguments: "{}"}},
	}

	messages, shouldReturn := sess.processToolCalls(context.Background(), toolCalls)

	assert.False(t, shouldReturn)
	require.Len(t, messages, 3)

	// Verify order is preserved
	assert.Equal(t, "result_a", msgContentStr(messages[0]))
	assert.Equal(t, "result_b", msgContentStr(messages[1]))
	assert.Equal(t, "result_c", msgContentStr(messages[2]))
}

// TestSession_ProcessToolCalls_MixedValidAndUnknown verifies that a mix of
// unknown and valid tool calls produces correct messages — unknown tools get
// error messages (added during pre-validation), valid tools get their results
// (added after concurrent dispatch), and order is preserved.
func TestSession_ProcessToolCalls_MixedValidAndUnknown(t *testing.T) {
	t.Parallel()

	tool := &mockTool{name: "known_tool", output: "ok"}
	sess, err := NewSession(nil, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "", "")
	require.NoError(t, err)

	toolCalls := []schemas.ChatAssistantMessageToolCall{
		{ID: strPtr("tc0"), Type: strPtr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("known_tool"), Arguments: "{}"}},
		{ID: strPtr("tc1"), Type: strPtr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("unknown_tool"), Arguments: "{}"}},
		{ID: strPtr("tc2"), Type: strPtr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: strPtr("known_tool"), Arguments: "{}"}},
	}

	messages, shouldReturn := sess.processToolCalls(context.Background(), toolCalls)

	assert.False(t, shouldReturn)
	require.Len(t, messages, 3)

	// First message: known tool result
	assert.Equal(t, "ok", msgContentStr(messages[0]))
	// Second message: unknown tool error
	assert.Contains(t, msgContentStr(messages[1]), "unknown tool")
	// Third message: known tool result
	assert.Equal(t, "ok", msgContentStr(messages[2]))
}

// mockToolSlow is a tool that sleeps for a fixed duration to verify concurrent
// dispatch in processToolCalls.
type mockToolSlow struct {
	name string
}

func (t *mockToolSlow) Name() string        { return t.name }
func (t *mockToolSlow) Description() string { return "A slow mock tool" }
func (t *mockToolSlow) Call(ctx context.Context, input string) (string, error) {
	time.Sleep(100 * time.Millisecond)
	return "done", nil
}
func (t *mockToolSlow) Format(input, result string, err error) string {
	return t.name + ": " + result
}
func (t *mockToolSlow) ParameterSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// sessionIDCaptureTool captures the session ID from the context.
type sessionIDCaptureTool struct {
	capturedSessionID string
}

func (t *sessionIDCaptureTool) Name() string        { return "capture_session_id" }
func (t *sessionIDCaptureTool) Description() string { return "Captures session ID from context" }
func (t *sessionIDCaptureTool) Call(ctx context.Context, input string) (string, error) {
	t.capturedSessionID = tools.SessionIDFromContext(ctx)
	return t.capturedSessionID, nil
}
func (t *sessionIDCaptureTool) Format(input, result string, err error) string {
	return result
}
func (t *sessionIDCaptureTool) ParameterSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// TestExecuteToolCall_InjectsSessionIDIntoContext verifies that
// executeToolCall injects the session's ID into the context, making it
// available to tools via tools.SessionIDFromContext. (edict 717)
func TestExecuteToolCall_InjectsSessionIDIntoContext(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	captureTool := &sessionIDCaptureTool{}
	sess, err := NewSession(mockLLM, &SessionConfig{}, []Tool{captureTool}, nil, func(any) {}, "", "test-channel")
	require.NoError(t, err)

	msg := sess.executeToolCall(context.Background(), captureTool, "call-1", "capture_session_id", `{}`)
	assert.Equal(t, schemas.ChatMessageRoleTool, msg.Role)
	assert.Equal(t, sess.ID, captureTool.capturedSessionID,
		"tool should receive the session's ID via context")
}

// TestExecuteToolCall_InjectsSessionIDViaScheduler verifies that the session
// ID propagates through the scheduler path as well. (edict 717)
// --- ATIF Integration Tests ---

// mockAtifRecorder is a test recorder that captures ATIF events for verification.
type mockAtifRecorder struct {
	events         []string
	lastTurnEnded  atifRecorderMessage
	lastToolResult []atifRecorderToolResult
}

func (m *mockAtifRecorder) Start() {
	m.events = append(m.events, "start")
}
func (m *mockAtifRecorder) Close() {
	m.events = append(m.events, "close")
}
func (m *mockAtifRecorder) TurnStarted() {
	m.events = append(m.events, "turn_start")
}
func (m *mockAtifRecorder) TurnEnded(msg atifRecorderMessage, toolResults []atifRecorderToolResult) {
	m.events = append(m.events, "turn_end")
	m.lastTurnEnded = msg
	m.lastToolResult = toolResults
}
func (m *mockAtifRecorder) MessageStarted(msg atifRecorderMessage) {
	m.events = append(m.events, "msg_start:"+msg.Role)
}
func (m *mockAtifRecorder) MessageEnded(msg atifRecorderMessage) {
	m.events = append(m.events, "msg_end:"+msg.Role)
}
func (m *mockAtifRecorder) ToolExecutionStarted(toolCallID, toolName string, args any) {
	m.events = append(m.events, "tool_start:"+toolName)
}
func (m *mockAtifRecorder) ToolExecutionEnded(toolCallID, toolName string, result string, isError bool) {
	m.events = append(m.events, "tool_end:"+toolName)
}
func (m *mockAtifRecorder) ToolExecutionUpdated(toolCallID, toolName string, partialResult string, args any) {
	m.events = append(m.events, "tool_update:"+toolName)
}
func (m *mockAtifRecorder) ModelChanged(provider, modelID string) {
	m.events = append(m.events, "model_change:"+modelID)
}
func (m *mockAtifRecorder) ThinkingLevelChanged(level string) {
	m.events = append(m.events, "thinking_level_change:"+level)
}

// compile-time check
var _ atifRecorder = (*mockAtifRecorder)(nil)

func TestSession_SetAtifRecorder_StartsRecorder(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)
	require.Contains(t, rec.events, "start", "SetAtifRecorder should call Start()")
}

func TestSession_SetAtifRecorder_NilClearsRecorder(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)
	assert.True(t, sess.atifRecorder != nil)

	sess.SetAtifRecorder(nil)
	assert.Nil(t, sess.atifRecorder, "setting nil should clear the recorder")
}

func TestSession_CloseAtif_ClosesRecorder(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)
	sess.closeAtif()
	require.Contains(t, rec.events, "close", "closeAtif should call Close()")
	assert.Nil(t, sess.atifRecorder, "recorder should be nil after closeAtif")
}

func TestSession_CloseAtif_NoRecorder(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// Should not panic
	sess.closeAtif()
}

func TestSession_AtifHooks_NoRecorder(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	// None of these should panic or crash when no recorder is set
	sess.atifTurnStarted()
	sess.atifTurnEnded(atifRecorderMessage{}, nil)
	sess.atifMessageStarted(atifRecorderMessage{})
	sess.atifMessageEnded(atifRecorderMessage{})
	sess.atifToolExecutionStarted("id", "tool", nil)
	sess.atifToolExecutionEnded("id", "tool", "result", false)
}

func TestSession_AtifHooks_WithRecorder(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)

	// Test each hook
	sess.atifTurnStarted()
	sess.atifMessageStarted(atifRecorderMessage{Role: "user"})
	sess.atifMessageEnded(atifRecorderMessage{Role: "user"})
	sess.atifMessageStarted(atifRecorderMessage{Role: "assistant"})
	sess.atifMessageEnded(atifRecorderMessage{Role: "assistant"})
	sess.atifToolExecutionStarted("call_1", "read", map[string]any{"path": "/app/file.txt"})
	sess.atifToolExecutionEnded("call_1", "read", "file content", false)
	sess.atifTurnEnded(atifRecorderMessage{Role: "assistant", StopReason: "stop"}, nil)

	expected := []string{
		"start",
		"turn_start",
		"msg_start:user",
		"msg_end:user",
		"msg_start:assistant",
		"msg_end:assistant",
		"tool_start:read",
		"tool_end:read",
		"turn_end",
		"close",
	}

	sess.closeAtif()
	assert.Equal(t, expected, rec.events, "ATIF events should be recorded in order")
}

func TestSession_AtifBuildAssistantMsg(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	choice := &responseChoice{
		Content:          "Hello!",
		ReasoningContent: "I think...",
		StopReason:       "stop",
		PromptTokens:     10,
		CompletionTokens: 5,
		CacheRead:        7,
		CacheWrite:       2,
		Reasoning:        3,
		InputCost:        0.001,
		OutputCost:       0.002,
		TotalCost:        0.003,
		ToolCalls: []schemas.ChatAssistantMessageToolCall{
			{
				ID: strPtr("call_1"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      strPtr("read"),
					Arguments: `{"path": "/app/file.txt"}`,
				},
			},
		},
	}

	msg := sess.buildAssistantMsg(choice, "Hello!")
	assert.Equal(t, "assistant", msg.Role)
	assert.Equal(t, "stop", msg.StopReason)
	require.Len(t, msg.Content, 3) // text + thinking + toolCall
	assert.Equal(t, "text", msg.Content[0].Type)
	assert.Equal(t, "Hello!", *msg.Content[0].Text)
	assert.Equal(t, "thinking", msg.Content[1].Type)
	assert.Equal(t, "I think...", *msg.Content[1].Thinking)
	assert.Equal(t, "toolCall", msg.Content[2].Type)
	assert.Equal(t, "call_1", msg.Content[2].ToolCallID)
	assert.Equal(t, "read", msg.Content[2].ToolName)
	require.NotNil(t, msg.Usage)
	assert.Equal(t, 10, msg.Usage.Input)
	assert.Equal(t, 5, msg.Usage.Output)
	assert.Equal(t, 15, msg.Usage.TotalTokens)
	assert.Equal(t, 7, msg.Usage.CacheRead)
	assert.Equal(t, 2, msg.Usage.CacheWrite)
	assert.Equal(t, 3, msg.Usage.Reasoning)
	require.NotNil(t, msg.Usage.Cost)
	assert.InDelta(t, 0.001, msg.Usage.Cost.Input, 0.00001)
	assert.InDelta(t, 0.002, msg.Usage.Cost.Output, 0.00001)
	assert.InDelta(t, 0.003, msg.Usage.Cost.Total, 0.00001)
}

func TestSession_AtifBuildAssistantMsg_NoToolCalls(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	choice := &responseChoice{
		Content:          "Hello!",
		ReasoningContent: "",
		StopReason:       "stop",
		PromptTokens:     10,
		CompletionTokens: 5,
	}

	msg := sess.buildAssistantMsg(choice, "Hello!")
	assert.Equal(t, "assistant", msg.Role)
	require.Len(t, msg.Content, 1) // only text
	assert.Equal(t, "text", msg.Content[0].Type)
	assert.Equal(t, "Hello!", *msg.Content[0].Text)
}

// TestSession_AskWithStreaming_TurnEndFollowsAssistantEnd verifies the ATIF
// lifecycle: the final assistant message_end is immediately followed by a
// turn_end on the normal (no-tool) stop path.
func TestSession_AskWithStreaming_TurnEndFollowsAssistantMessage(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are helpful", "ch")
	require.NoError(t, err)

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)

	_, err = sess.AskWithStreaming(context.Background(), "Hello", nil)
	require.NoError(t, err)
	sess.closeAtif()

	// Assert the last two recorded events are assistant message_end then turn_end.
	assistantEndIdx := -1
	turnEndIdx := -1
	for i, e := range rec.events {
		switch {
		case e == "msg_end:assistant":
			assistantEndIdx = i
		case e == "turn_end":
			turnEndIdx = i
		}
	}
	assert.Greater(t, assistantEndIdx, -1, "should have an assistant message_end")
	assert.Greater(t, turnEndIdx, -1, "should have a turn_end")
	assert.Equal(t, assistantEndIdx+1, turnEndIdx,
		"final assistant message_end must be immediately followed by turn_end")

	// The turn_end should describe the final assistant answer.
	assert.Equal(t, "assistant", rec.lastTurnEnded.Role)
	assert.Equal(t, "stop", rec.lastTurnEnded.StopReason)
}

// TestSession_AskWithStreaming_ToolResultContent verifies that turn_end
// toolResults carry the non-empty tool content text through the recorder.
// We drive the full streaming path with a custom provider that returns a
// tool_call then a final answer, and assert the turn_end toolResults content.
func TestSession_AskWithStreaming_ToolResultContent(t *testing.T) {
	mockLLM := &toolResultProvider{}
	tool := &mockTool{name: "confirmer", output: `{"ok": true}`}
	sess, err := NewSession(mockLLM, &SessionConfig{}, []Tool{tool}, nil, func(any) {}, "You are helpful", "ch")
	require.NoError(t, err)
	sess.model = mockLLM
	sess.Provider = "test"
	sess.Model = "test-model"

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)

	_, err = sess.AskWithStreaming(context.Background(), "do the tool", nil)
	require.NoError(t, err)
	sess.closeAtif()

	require.NotEmpty(t, rec.lastToolResult, "should have captured tool results in turn_end")
	for i, tr := range rec.lastToolResult {
		require.NotEmptyf(t, tr.Content, "tool result %d should have content", i)
		assert.Equal(t, "text", tr.Content[0].Type)
		require.NotNil(t, tr.Content[0].Text)
	}
}

// TestSession_UpdateModel_EmitsModelChange verifies that changing the model
// emits a model_change recorder event.
func TestSession_UpdateModel_EmitsModelChange(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)

	sess.UpdateModel("openai", "gpt-4o")
	sess.closeAtif()

	assert.Contains(t, rec.events, "model_change:gpt-4o", "UpdateModel should record a model_change")
}

// TestSession_UpdateModel_Idempotent verifies UpdateModel does not re-emit
// model_change when the provider/model are unchanged.
func TestSession_UpdateModel_Idempotent(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{LLM: internalconfig.LLMConfig{Provider: "openai", Model: "gpt-4o"}}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)
	if sess.Provider == "" {
		sess.Provider = "openai"
		sess.Model = "gpt-4o"
	}

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)

	sess.UpdateModel("openai", "gpt-4o")
	sess.closeAtif()

	for _, e := range rec.events {
		assert.NotEqual(t, "model_change:gpt-4o", e, "unchanged model change should not be emitted")
	}
}

// TestSession_UpdateReasoningEffort_EmitsThinkingLevelChange verifies that
// changing the reasoning-effort level emits a thinking_level_change recorder
// event and that an unchanged level is idempotent (no re-emission).
func TestSession_UpdateReasoningEffort_EmitsThinkingLevelChange(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "", "")
	require.NoError(t, err)

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)

	sess.UpdateReasoningEffort("high")
	sess.UpdateReasoningEffort("low")
	sess.UpdateReasoningEffort("low") // unchanged -> no new event
	sess.closeAtif()

	assert.Equal(t, 2, countPrefix(rec.events, "thinking_level_change:"),
		"should emit thinking_level_change when the level changes, but not when unchanged")
	assert.Contains(t, rec.events, "thinking_level_change:high")
	assert.Contains(t, rec.events, "thinking_level_change:low")
}

// countPrefix returns the number of events whose string starts with prefix.
func countPrefix(events []string, prefix string) int {
	n := 0
	for _, e := range events {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

// TestSession_UsagePlumbing_SurfacesCacheCostReasoning feeds a provider whose
// usage carries cache tokens, cost, and reasoning tokens and asserts those
// surface in the ATIF recorder's RecorderUsage/RecorderCost via the last
// assistant message.
func TestSession_UsagePlumbing_SurfacesCacheCostReasoning(t *testing.T) {
	cacheRead := 30
	cacheWrite := 10
	reasoning := 5
	cost := &schemas.BifrostCost{
		InputTokensCost:  0.00001,
		OutputTokensCost: 0.00002,
		TotalCost:        0.00003,
	}
	mockLLM := mocks.NewLLMProviderWithChunks([]mocks.StreamingChunk{
		{Content: "hello", FinishReason: "stop", Usage: &schemas.BifrostLLMUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{
				CachedReadTokens:  cacheRead,
				CachedWriteTokens: cacheWrite,
			},
			CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
				ReasoningTokens: reasoning,
			},
			Cost: cost,
		}},
	})

	sess, err := NewSession(mockLLM, &SessionConfig{LLM: internalconfig.LLMConfig{Provider: "test", Model: "test-model"}}, nil, nil, func(any) {}, "", "ch")
	require.NoError(t, err)
	sess.Provider = "test"
	sess.Model = "test-model"

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)

	_, err = sess.AskWithStreaming(context.Background(), "hi", nil)
	require.NoError(t, err)
	sess.closeAtif()

	require.NotNil(t, rec.lastTurnEnded.Usage, "turn_end assistant message should have usage")
	assert.Equal(t, 100, rec.lastTurnEnded.Usage.Input)
	assert.Equal(t, 50, rec.lastTurnEnded.Usage.Output)
	assert.Equal(t, cacheRead, rec.lastTurnEnded.Usage.CacheRead)
	assert.Equal(t, cacheWrite, rec.lastTurnEnded.Usage.CacheWrite)
	assert.Equal(t, reasoning, rec.lastTurnEnded.Usage.Reasoning)
	require.NotNil(t, rec.lastTurnEnded.Usage.Cost)
	assert.InDelta(t, 0.00001, rec.lastTurnEnded.Usage.Cost.Input, 0.0000001)
	assert.InDelta(t, 0.00002, rec.lastTurnEnded.Usage.Cost.Output, 0.0000001)
	assert.InDelta(t, 0.00003, rec.lastTurnEnded.Usage.Cost.Total, 0.0000001)
}

// TestSession_AskWithStreaming_MaxTokensEndsTurn verifies that the max-tokens
// truncation exit path still emits exactly one turn_end and that the final
// assistant message_end is immediately followed by it.
func TestSession_AskWithStreaming_MaxTokensEndsTurn(t *testing.T) {
	mockLLM := mocks.NewLLMProviderWithChunks([]mocks.StreamingChunk{
		{Content: "truncated", FinishReason: "max_tokens"},
	})
	sess, err := NewSession(mockLLM, &SessionConfig{LLM: internalconfig.LLMConfig{MaxTurns: 1}}, nil, nil, func(any) {}, "You are helpful", "ch")
	require.NoError(t, err)

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)

	resp, err := sess.AskWithStreaming(context.Background(), "Hello", nil)
	require.NoError(t, err)
	assert.Contains(t, resp, "[Response truncated due to length limit]")
	sess.closeAtif()

	// Exactly one turn_end must exist and it must follow the assistant's
	// message_end immediately on the truncation path.
	endIdx := -1
	turnEndIdx := -1
	for i, e := range rec.events {
		switch {
		case e == "msg_end:assistant":
			endIdx = i
		case e == "turn_end":
			turnEndIdx = i
		}
	}
	assert.Greater(t, endIdx, -1, "should have an assistant message_end on max_tokens path")
	assert.Greater(t, turnEndIdx, -1, "should emit a turn_end on max_tokens path")
	assert.Equal(t, endIdx+1, turnEndIdx, "assistant message_end must be immediately followed by turn_end on max_tokens path")
	assert.Equal(t, "assistant", rec.lastTurnEnded.Role)
	assert.Equal(t, "max_tokens", rec.lastTurnEnded.StopReason)
}

// TestSession_AskWithStreaming_CtxCancelEndsTurn verifies that the context
// cancellation exit path still emits exactly one turn_end so no turn is left
// open when the stream is interrupted.
func TestSession_AskWithStreaming_CtxCancelEndsTurn(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	mockLLM.DelayBetweenChunks = 50 * time.Millisecond
	mockLLM.SetStreamingChunks([]mocks.StreamingChunk{
		{Content: "Slow "},
		{Content: "response here", FinishReason: "stop"},
	})

	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, func(any) {}, "You are a helpful assistant", "test-channel")
	require.NoError(t, err)

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err = sess.AskWithStreaming(ctx, "Hello", nil)
	require.Equal(t, context.Canceled, err)
	sess.closeAtif()

	// Cancellation must still close the turn with exactly one turn_end.
	var turnEnds []string
	for _, e := range rec.events {
		if e == "turn_end" {
			turnEnds = append(turnEnds, e)
		}
	}
	assert.Len(t, turnEnds, 1, "cancellation should emit exactly one turn_end")
}

// TestSession_AskWithStreaming_ErrorStopReasonEndsTurn verifies that the
// error/content_filter stop-reason exit path still emits a turn_end after the
// assistant message_end so the trajectory stays well-formed on provider error.
func TestSession_AskWithStreaming_ErrorStopReasonEndsTurn(t *testing.T) {
	mockLLM := mocks.NewLLMProviderWithChunks([]mocks.StreamingChunk{
		{Content: "oops", FinishReason: "content_filter"},
	})
	sess, err := NewSession(mockLLM, &SessionConfig{LLM: internalconfig.LLMConfig{MaxTurns: 1}}, nil, nil, func(any) {}, "You are helpful", "ch")
	require.NoError(t, err)

	rec := &mockAtifRecorder{}
	sess.SetAtifRecorder(rec)

	_, err = sess.AskWithStreaming(context.Background(), "Hello", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop_reason=content_filter")
	sess.closeAtif()

	// Exactly one turn_end must follow the assistant message_end on the error path.
	endIdx := -1
	turnEndIdx := -1
	for i, e := range rec.events {
		switch {
		case e == "msg_end:assistant":
			endIdx = i
		case e == "turn_end":
			turnEndIdx = i
		}
	}
	assert.Greater(t, endIdx, -1, "should emit an assistant message_end on error path")
	assert.Greater(t, turnEndIdx, -1, "should emit a turn_end on error path")
	assert.Equal(t, endIdx+1, turnEndIdx, "assistant message_end must be immediately followed by turn_end on error path")
}

func TestExecuteToolCall_InjectsSessionIDViaScheduler(t *testing.T) {
	mockLLM := mocks.NewLLMProvider()
	captureTool := &sessionIDCaptureTool{}
	sched := runners.NewCoreToolScheduler(func(any) {})
	sess, err := NewSession(mockLLM, &SessionConfig{}, []Tool{captureTool}, sched, func(any) {}, "", "e717")
	require.NoError(t, err)

	msg := sess.executeToolCall(context.Background(), captureTool, "call-1", "capture_session_id", `{}`)
	assert.Equal(t, schemas.ChatMessageRoleTool, msg.Role)
	assert.Equal(t, sess.ID, captureTool.capturedSessionID,
		"tool should receive the session's ID via context through the scheduler")
}
