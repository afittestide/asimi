package main

import (
	"strings"
	"testing"

	"github.com/afittestide/asimi/internal/runners"
	"github.com/stretchr/testify/assert"
)

// ===== Debounced Chat Render Tests =====

func TestAddAIChunk_SetsDirtyWithoutImmediateUpdate(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	// Baseline: viewport should have the initial session message
	baseline := chat.Viewport.View()

	chat.AddAIChunk("hello from AI")

	// contentDirty must be true
	assert.True(t, chat.contentDirty, "AddAIChunk should set contentDirty=true")

	// Viewport content should be stale (unchanged from baseline)
	assert.Equal(t, baseline, chat.Viewport.View(),
		"AddAIChunk should NOT immediately update the viewport")

	// After UpdateContent, viewport reflects the new message
	chat.UpdateContent()
	assert.Contains(t, chat.Viewport.View(), "hello from AI",
		"viewport should contain the AI chunk after UpdateContent")
}

func TestAddThinkingChunk_SetsDirtyWithoutImmediateUpdate(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	baseline := chat.Viewport.View()

	chat.AddThinkingChunk("pondering deeply")

	assert.True(t, chat.contentDirty, "AddThinkingChunk should set contentDirty=true")
	assert.Equal(t, baseline, chat.Viewport.View(),
		"AddThinkingChunk should NOT immediately update the viewport")

	chat.UpdateContent()
	assert.Contains(t, chat.Viewport.View(), "pondering deeply",
		"viewport should contain the thinking chunk after UpdateContent")
}

func TestAddAIChunk_AccumulatesContent(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.AddAIChunk("first ")
	chat.AddAIChunk("second ")
	chat.AddAIChunk("third")

	// All chunks should be accumulated in the last AI message
	assert.Len(t, chat.Messages, 1, "chunks should append to the same AI message")
	assert.Equal(t, "first second third", chat.Messages[0].Content,
		"chunks should accumulate in order")
	assert.Equal(t, MessageTypeAI, chat.Messages[0].Type)
}

func TestAddAIChunk_CreatesNewMessageAfterDifferentType(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.AddAIChunk("ai part one")
	chat.AddUserMessage("user interrupt")
	chat.AddAIChunk("ai part two")

	// Should have 3 messages: AI, User, AI
	assert.Len(t, chat.Messages, 3)
	assert.Equal(t, MessageTypeAI, chat.Messages[0].Type)
	assert.Equal(t, MessageTypeUser, chat.Messages[1].Type)
	assert.Equal(t, MessageTypeAI, chat.Messages[2].Type)
	assert.Equal(t, "ai part one", chat.Messages[0].Content)
	assert.Equal(t, "ai part two", chat.Messages[2].Content)
}

func TestUpdateContent_ViewportMatchesMessages(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.AddAIChunk("chunk1")
	chat.AddAIChunk(" chunk2")

	// Before UpdateContent, viewport is stale
	assert.False(t, strings.Contains(chat.Viewport.View(), "chunk1"),
		"viewport should be stale before UpdateContent")

	chat.UpdateContent()

	// After UpdateContent, viewport contains the accumulated content
	view := chat.Viewport.View()
	assert.True(t, strings.Contains(view, "chunk1"), "viewport should contain 'chunk1'")
	assert.True(t, strings.Contains(view, "chunk2"), "viewport should contain 'chunk2'")
}

func TestContentDirty_FalseAfterUpdateContent(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.AddAIChunk("dirty data")
	assert.True(t, chat.contentDirty, "should be dirty after AddAIChunk")

	chat.UpdateContent()
	assert.False(t, chat.contentDirty, "contentDirty should be false after UpdateContent")
}

func TestContentDirty_FalseAfterFlushDirty(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.AddAIChunk("dirty data")
	assert.True(t, chat.contentDirty, "should be dirty after AddAIChunk")

	chat.FlushDirty()
	assert.False(t, chat.contentDirty, "contentDirty should be false after FlushDirty")
	assert.Contains(t, chat.Viewport.View(), "dirty data",
		"viewport should reflect content after FlushDirty")
}

func TestAddMessage_SynchronousUpdate_BackwardCompatibility(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.AddMessage("sync message")

	// AddMessage calls UpdateContent synchronously, so contentDirty should remain false
	assert.False(t, chat.contentDirty,
		"AddMessage should not leave contentDirty=true")

	// Viewport should already reflect the message (no deferred render needed)
	assert.Contains(t, chat.Viewport.View(), "sync message",
		"AddMessage should synchronously update the viewport")
}

func TestAddUserMessage_SynchronousUpdate_BackwardCompatibility(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.AddUserMessage("user says hi")

	assert.False(t, chat.contentDirty,
		"AddUserMessage should not leave contentDirty=true")
	assert.Contains(t, chat.Viewport.View(), "user says hi",
		"AddUserMessage should synchronously update the viewport")
}

func TestAddThinkingChunk_SkipsEmptyChunks(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	msgCountBefore := len(chat.Messages)

	chat.AddThinkingChunk("   ")
	assert.False(t, chat.contentDirty,
		"whitespace-only thinking chunks should not mark content dirty")
	assert.Len(t, chat.Messages, msgCountBefore,
		"no new message should be added for empty thinking chunks")
}

// ===== Batched (dirty-flag) Append API Tests (edict 771) =====

func TestAppendStringMessage_SetsDirtyWithoutImmediateRender(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	baseline := chat.Viewport.View()

	chat.AppendStringMessage("batched system message")

	// Appended but NOT eagerly rendered — viewport stays stale.
	assert.True(t, chat.contentDirty, "AppendStringMessage should set contentDirty=true")
	assert.Len(t, chat.Messages, 1)
	assert.Equal(t, MessageTypeSystem, chat.Messages[0].Type)
	assert.Equal(t, baseline, chat.Viewport.View(),
		"AppendStringMessage should NOT immediately update the viewport")

	chat.FlushDirty()
	assert.Contains(t, chat.Viewport.View(), "batched system message",
		"viewport should reflect the message after FlushDirty")
}

func TestAppendUserMessage_SetsContentDirtyWithoutImmediateRender(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	baseline := chat.Viewport.View()

	chat.AppendUserMessage("batched user text")

	assert.True(t, chat.contentDirty, "AppendUserMessage should set contentDirty=true")
	assert.Len(t, chat.Messages, 1)
	assert.Equal(t, MessageTypeUser, chat.Messages[0].Type)
	assert.Equal(t, "batched user text", chat.Messages[0].Content)
	assert.Equal(t, baseline, chat.Viewport.View(),
		"AppendUserMessage should NOT immediately update the viewport")

	chat.FlushDirty()
	assert.Contains(t, chat.Viewport.View(), "batched user text",
		"viewport should reflect the message after FlushDirty")
}

func TestAppendToolCallMessage_SetsContentDirtyWithoutImmediateRender(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	baseline := chat.Viewport.View()

	chat.AppendToolCallMessage("tool call line")

	assert.True(t, chat.contentDirty, "AppendToolCallMessage should set contentDirty=true")
	assert.Len(t, chat.Messages, 1)
	assert.Equal(t, MessageTypeSystem, chat.Messages[0].Type)
	assert.Equal(t, baseline, chat.Viewport.View(),
		"AppendToolCallMessage should NOT immediately update the viewport")

	chat.FlushDirty()
	assert.Contains(t, chat.Viewport.View(), "tool call line",
		"viewport should reflect the tool call line after FlushDirty")
}

func TestAppendBatch_SetsDirtyOnceAndPreservesTypes(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	baseline := chat.Viewport.View()

	msgs := []ChatMessage{
		{Content: "user one", Type: MessageTypeUser},
		{Content: "ai response", Type: MessageTypeAISuccess},
		{Content: "thinking…", Type: MessageTypeThinking},
		{Content: "tool line", Type: MessageTypeSystem},
	}
	chat.AppendBatch(msgs)

	// All appended in one call, contentDirty set once.
	assert.True(t, chat.contentDirty, "AppendBatch should set contentDirty=true")
	assert.Len(t, chat.Messages, 4, "AppendBatch should append all messages in one call")
	assert.Equal(t, MessageTypeUser, chat.Messages[0].Type)
	assert.Equal(t, MessageTypeAISuccess, chat.Messages[1].Type)
	assert.Equal(t, MessageTypeThinking, chat.Messages[2].Type)
	assert.Equal(t, MessageTypeSystem, chat.Messages[3].Type)
	assert.Equal(t, "user one", chat.Messages[0].Content)
	assert.Equal(t, baseline, chat.Viewport.View(),
		"AppendBatch should NOT immediately update the viewport")

	chat.FlushDirty()
	assert.Contains(t, chat.Viewport.View(), "user one",
		"viewport should reflect the batch after FlushDirty")
	assert.Contains(t, chat.Viewport.View(), "ai response")
}

func TestAppendMethods_DoNotWeakenSynchronousContract(t *testing.T) {
	// The batched Append* methods must NOT alter the behaviour of the
	// existing synchronous AddMessage/AddUserMessage: those still update the
	// viewport eagerly and leave contentDirty=false.
	chat := NewChatComponent(80, 20, false)

	chat.AddMessage("sync system")
	assert.False(t, chat.contentDirty, "AddMessage must remain synchronous (contentDirty=false)")
	assert.Contains(t, chat.Viewport.View(), "sync system")

	chat.AddUserMessage("sync user")
	assert.False(t, chat.contentDirty, "AddUserMessage must remain synchronous (contentDirty=false)")
	assert.Contains(t, chat.Viewport.View(), "sync user")
}

// ===== Clear() Tests =====

func TestClear_ResetsMessagesAndState(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	chat.AddUserMessage("some content")
	chat.AddMessage("another message")
	chat.AutoScroll = false
	chat.UserScrolled = true
	chat.ScrollLocked = true

	chat.Clear()

	assert.Empty(t, chat.Messages, "Clear should empty Messages")
	assert.True(t, chat.AutoScroll, "Clear should reset AutoScroll to true")
	assert.False(t, chat.UserScrolled, "Clear should reset UserScrolled")
	assert.False(t, chat.ScrollLocked, "Clear should reset ScrollLocked")
	assert.Empty(t, chat.rawSessionHistory, "Clear should clear rawSessionHistory")
	assert.Empty(t, chat.toolCallMessageIndex, "Clear should clear toolCallMessageIndex")
}

func TestClear_SynchronouslyUpdatesViewport(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	chat.AddUserMessage("this should disappear after Clear")

	chat.Clear()

	// Clear() now calls UpdateContent(), so viewport should reflect the empty state
	assert.False(t, chat.contentDirty,
		"Clear should not leave contentDirty=true — it calls UpdateContent()")
	assert.False(t, strings.Contains(chat.Viewport.View(), "this should disappear after Clear"),
		"viewport should not contain old messages after Clear")
}

func TestClear_ViewportAtTop(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	// Add enough messages to scroll
	for i := 0; i < 50; i++ {
		chat.AddMessage("line " + string(rune('A'+i%26)))
	}
	chat.Viewport.GotoBottom()

	chat.Clear()

	assert.True(t, chat.Viewport.AtTop(),
		"Clear should scroll viewport to top via GotoTop")
}

func TestClear_AllowsSubsequentMessages(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	chat.AddUserMessage("before clear")
	chat.Clear()
	chat.AddUserMessage("after clear")

	assert.Len(t, chat.Messages, 1, "should have exactly one message after Clear + AddUserMessage")
	assert.Equal(t, "after clear", chat.Messages[0].Content)
	assert.Contains(t, chat.Viewport.View(), "after clear",
		"viewport should show new messages after Clear")
}

// ===== Original Tests =====

func TestChatComponent_StartBlock(t *testing.T) {
	t.Run("starts block with height limit", func(t *testing.T) {
		chat := NewChatComponent(80, 20, false)
		// Start with one message
		chat.AddMessage("header message")

		// Start a block with height limit of 5
		// Block records len(messages)-1 = 0
		chat.StartBlock(5)

		assert.Len(t, chat.blockLines, 1)
		assert.Equal(t, 0, chat.blockLines[0][0]) // Starting line index
		assert.Equal(t, 5, chat.blockLines[0][1]) // Height limit
	})

	t.Run("starts block with unlimited height (0)", func(t *testing.T) {
		chat := NewChatComponent(80, 20, false)
		chat.AddMessage("first")

		// Start an unlimited block
		chat.StartBlock(0)

		assert.Len(t, chat.blockLines, 1)
		assert.Equal(t, 0, chat.blockLines[0][0])
		assert.Equal(t, 0, chat.blockLines[0][1]) // Unlimited
	})

	t.Run("multiple blocks with different heights", func(t *testing.T) {
		chat := NewChatComponent(80, 20, false)

		// First block: after 2 messages, block starts at index 1
		chat.AddMessage("block1-msg1")
		chat.AddMessage("block1-msg2")
		chat.StartBlock(3)

		// Second block: after 4 total messages, block starts at index 3
		chat.AddMessage("block2-msg1")
		chat.AddMessage("block2-msg2")
		chat.StartBlock(5)

		assert.Len(t, chat.blockLines, 2)
		// First block starts after 2 messages (at index 1)
		assert.Equal(t, 1, chat.blockLines[0][0])
		assert.Equal(t, 3, chat.blockLines[0][1])
		// Second block starts after 4 messages (at index 3)
		assert.Equal(t, 3, chat.blockLines[1][0])
		assert.Equal(t, 5, chat.blockLines[1][1])
	})

	t.Run("start block on empty chat", func(t *testing.T) {
		chat := NewChatComponent(80, 20, false)
		// No messages added yet
		chat.StartBlock(10)

		assert.Len(t, chat.blockLines, 1)
		assert.Equal(t, -1, chat.blockLines[0][0]) // No messages yet, so -1
		assert.Equal(t, 10, chat.blockLines[0][1])
	})
}

// ===== Tool Call Handler Tests (edict 780) =====
//
// These tests verify that the HandleToolCall* methods use a debounced
// contentDirty flag instead of forcing synchronous re-renders. The TUI's
// chatRenderTickMsg debounce flushes the dirty content at 50ms intervals.

func TestHandleToolCallScheduled_SetsContentDirtyAndRegistersIndex(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	baseline := chat.Viewport.View()

	chat.HandleToolCallScheduled(runners.ToolCallScheduledMsg{
		CallID:    "tc-1",
		ToolName:  "read_file",
		Formatted: "read_file: test.go",
	})

	// Must mark dirty (deferred render), NOT update content synchronously
	assert.True(t, chat.contentDirty,
		"HandleToolCallScheduled should set contentDirty=true (deferred render)")
	assert.Len(t, chat.Messages, 1,
		"Scheduled tool call should append exactly one message")
	assert.Equal(t, "📋 read_file: test.go", chat.Messages[0].Content,
		"Scheduled message content should include the 📋 prefix")

	// Viewport must be stale (not yet rendered)
	assert.Equal(t, baseline, chat.Viewport.View(),
		"Scheduled tool call should NOT immediately update the viewport")

	// Index should be registered for subsequent handlers
	idx, exists := chat.GetToolCallMessageIndex("tc-1")
	assert.True(t, exists, "tool call index should be registered")
	assert.Equal(t, 0, idx, "index should point to the first message")

	// After FlushDirty, viewport reflects the scheduled tool call
	chat.FlushDirty()
	assert.Contains(t, chat.Viewport.View(), "read_file: test.go",
		"viewport should show the scheduled tool call after FlushDirty")
}

func TestHandleToolCallExecuting_UpdatesExistingAndMarksDirty(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	// Seed with a scheduled message
	chat.HandleToolCallScheduled(runners.ToolCallScheduledMsg{
		CallID: "tc-2", ToolName: "write_file", Formatted: "write_file: x.go",
	})
	// Reset contentDirty so we can verify the executing handler marks it again
	chat.FlushDirty()
	assert.False(t, chat.contentDirty, "precondition: contentDirty should be false after flush")

	// Sent executing message for the same call
	chat.HandleToolCallExecuting(runners.ToolCallExecutingMsg{
		CallID: "tc-2", ToolName: "write_file", Input: "x.go", Formatted: "write_file: running",
	})

	assert.True(t, chat.contentDirty, "executing should set contentDirty=true")
	assert.Len(t, chat.Messages, 1, "executing should update existing message, not append a new one")
	assert.Equal(t, "⚙️ write_file: running", chat.Messages[0].Content,
		"executing should replace message content with the ⚙️-prefixed format")
}

func TestHandleToolCallExecuting_FallbackWhenNoIndex(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	baseline := chat.Viewport.View()

	// No toolCallMessageIndex set for tc-99 → falls back to append
	chat.HandleToolCallExecuting(runners.ToolCallExecutingMsg{
		CallID: "tc-99", ToolName: "unknown", Formatted: "unknown: noindex",
	})

	assert.True(t, chat.contentDirty, "fallback should use AppendToolCallMessage → dirty")
	assert.Len(t, chat.Messages, 1, "fallback should append a new message")
	assert.Equal(t, "⚙️ unknown: noindex", chat.Messages[0].Content)
	assert.Equal(t, baseline, chat.Viewport.View(),
		"fallback should NOT synchronously update the viewport")
}

func TestHandleToolCallSuccess_UpdatesAndClearsIndex(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	// Build with a scheduled message first
	chat.HandleToolCallScheduled(runners.ToolCallScheduledMsg{
		CallID: "tc-3", ToolName: "grep", Formatted: "grep: foo",
	})
	chat.UpdateContent() // clear dirty flag from scheduled

	// Success with same CallID
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-3", ToolName: "grep", Input: "foo", Result: "match",
		Formatted: "grep: match",
	})

	assert.True(t, chat.contentDirty, "success should set contentDirty=true")
	assert.Len(t, chat.Messages, 1, "success should update existing message, not append")
	assert.Equal(t, checkPrefix+" grep: match", chat.Messages[0].Content,
		"success should add the check prefix to the formatted content")

	// Index mapping must be cleaned up on success
	_, exists := chat.GetToolCallMessageIndex("tc-3")
	assert.False(t, exists, "tool call index should be removed after success")
}

func TestHandleToolCallSuccess_FallbackWhenNoIndex(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-unknown", ToolName: "shell", Result: "ok", Formatted: "shell: ok",
	})

	assert.True(t, chat.contentDirty, "fallback success should mark contentDirty=true")
	assert.Len(t, chat.Messages, 1, "fallback should append a new message")
	assert.Equal(t, checkPrefix+" shell: ok", chat.Messages[0].Content)
}

func TestHandleToolCallError_UpdatesAndClearsIndex(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.HandleToolCallScheduled(runners.ToolCallScheduledMsg{
		CallID: "tc-4", ToolName: "shell", Formatted: "shell: run",
	})
	chat.UpdateContent() // clear scheduled dirty

	chat.HandleToolCallError(runners.ToolCallErrorMsg{
		CallID: "tc-4", ToolName: "shell", Error: "boom", Formatted: "shell: boom",
	})

	assert.True(t, chat.contentDirty, "error should set contentDirty=true")
	assert.Len(t, chat.Messages, 1, "error should update existing message, not append")
	assert.Contains(t, chat.Messages[0].Content, "shell: boom",
		"error message should include the formatted content")
	assert.Contains(t, chat.Messages[0].Content, "⁉️",
		"error message should have a warning icon")

	_, exists := chat.GetToolCallMessageIndex("tc-4")
	assert.False(t, exists, "tool call index should be removed after error")
}

func TestHandleToolCallError_UserDeniedUsesBlockedIcon(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.HandleToolCallError(runners.ToolCallErrorMsg{
		CallID: "tc-deny", ToolName: "shell",
		Error:     "command denied by user",
		Formatted: "shell: denied",
	})

	assert.Contains(t, chat.Messages[0].Content, "⛔︎",
		"user-denied errors should use the ⛔ blocker icon")
	assert.True(t, chat.contentDirty, "denied error should mark content dirty")
}

func TestHandleToolCallAborted_UpdatesAndClearsIndex(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.HandleToolCallScheduled(runners.ToolCallScheduledMsg{
		CallID: "tc-5", ToolName: "shell", Formatted: "shell: run",
	})
	chat.UpdateContent()

	chat.HandleToolCallAborted(runners.ToolCallAbortedMsg{
		CallID: "tc-5", ToolName: "shell", Reason: "restart", Formatted: "shell: aborted",
	})

	assert.True(t, chat.contentDirty, "aborted should set contentDirty=true")
	assert.Len(t, chat.Messages, 1, "aborted should update existing message, not append")
	assert.Contains(t, chat.Messages[0].Content, "🚫 shell: aborted",
		"aborted message should have the 🚫 icon and formatted content")

	_, exists := chat.GetToolCallMessageIndex("tc-5")
	assert.False(t, exists, "tool call index should be removed after abort")
}

func TestHandleToolCallFallbacks_AllUseAppendToolCallMsg(t *testing.T) {
	// When no toolCallMessageIndex is found, all handlers should fall back to
	// AppendingToolCallMessage (deferred render) rather than AddMessage (sync render).
	chat := NewChatComponent(80, 20, false)
	baseline := chat.Viewport.View()

	// No index set → fallback append
	chat.HandleToolCallAborted(runners.ToolCallAbortedMsg{
		CallID: "x", ToolName: "tool", Reason: "r", Formatted: "tool: aborted",
	})

	assert.True(t, chat.contentDirty, "fallback must mark contentDirty")
	assert.Len(t, chat.Messages, 1)
	assert.Equal(t, baseline, chat.Viewport.View(),
		"deferred rendering must NOT synchronously update viewport")

	chat.FlushDirty()
	assert.Contains(t, chat.Viewport.View(), "tool: aborted",
		"viewport should reflect the appended message after flush")
}

// TestToolCallHandlers_ConsistentDirtyStateAfterFlush verifies that after
// FlushDirty (simulating the debounce tick), contentDirty is cleared and the
// viewport is up to date.
func TestToolCallHandlers_ConsistentDirtyStateAfterFlush(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.HandleToolCallScheduled(runners.ToolCallScheduledMsg{
		CallID: "tc-6", ToolName: "shell", Formatted: "shell: run",
	})
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-6", ToolName: "shell", Result: "ok", Formatted: "shell: ok",
	})

	assert.True(t, chat.contentDirty, "after handlers, contentDirty should be true")

	chat.FlushDirty()
	assert.False(t, chat.contentDirty, "after flush, contentDirty should be false")
	assert.Contains(t, chat.Viewport.View(), "shell: ok",
		"viewport should show the final tool call result")
}
