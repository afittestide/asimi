package main

import (
	"strings"
	"testing"

	"github.com/afittestide/asimi/internal/runners"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestHandleToolCallSuccess_MarkdownTableEnvelope verifies the generic
// markdown_table opt-in: an envelope result is rendered as a table sized
// within the component width, and the tool-agnostic path needs no tool check.
func TestHandleToolCallSuccess_MarkdownTableEnvelope(t *testing.T) {
	envelope := `{"format":"markdown_table","title":"Heaven Ledger",` +
		`"columns":["id","edict_id","event_type","created_at","detail"],` +
		`"rows":[["1","5","ritual_started","2026-10-11T10:00:00Z","swift-strike"],` +
		`["2","5","step_started","2026-10-11T10:00:01Z","forge"]]}`

	chat := NewChatComponent(80, 20, true)
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-table", ToolName: "tian_ledger",
		Result: envelope, Formatted: "Found 2 events",
	})

	content := chat.Messages[0].Content
	assert.Contains(t, content, "Heaven Ledger", "title must be rendered")
	assert.Contains(t, content, "ritual_started", "row cells must be rendered")
	assert.Contains(t, content, "2026-10-11T10:00:00Z", "row cells must be rendered")
	assert.Contains(t, content, "forge", "row cells must be rendered")
	assert.NotContains(t, content, "Found 2 events",
		"the formatted summary must be overridden by the table")

	for _, line := range strings.Split(content, "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), chat.Width,
			"rendered table width must not exceed the component width")
	}
}

// TestHandleToolCallSuccess_MarkdownTableNarrowWidth verifies the table keeps
// fitting (by truncating cells) when the component is narrower than the
// natural table width.
func TestHandleToolCallSuccess_MarkdownTableNarrowWidth(t *testing.T) {
	envelope := `{"format":"markdown_table","title":"Heaven Ledger",` +
		`"columns":["id","edict_id","event_type","created_at","detail"],` +
		`"rows":[["1","5","ritual_started","2026-10-11T10:00:00Z","swift-strike"]]}`

	chat := NewChatComponent(50, 20, true)
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-narrow", ToolName: "tian_ledger",
		Result: envelope, Formatted: "Found 1 events",
	})

	content := chat.Messages[0].Content
	assert.Contains(t, content, "Heaven Ledger")
	for _, line := range strings.Split(content, "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), chat.Width,
			"narrow table width must not exceed the component width")
	}
}

// TestHandleToolCallSuccess_NonEnvelopeUsesFormatted guards that an ordinary
// result (or an envelope with an unknown format) keeps the old path.
func TestHandleToolCallSuccess_NonEnvelopeUsesFormatted(t *testing.T) {
	cases := map[string]string{
		"plain":          `not json at all`,
		"bare array":     `[{"id":1,"event_type":"x"}]`,
		"wrong format":   `{"format":"plain_text","title":"T","columns":["a"],"rows":[["1"]]}`,
		"missing cols":   `{"format":"markdown_table","title":"T","rows":[["1"]]}`,
		"unrelated obj":  `{"foo":"bar"}`,
		"empty markdown": `{"format":"markdown"}`,
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			chat := NewChatComponent(80, 20, true)
			chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
				CallID: "tc-" + name, ToolName: "some_tool",
				Result: result, Formatted: "tool: summary",
			})
			assert.Equal(t, checkPrefix+" tool: summary", chat.Messages[0].Content,
				"non-envelope results must fall back to the Formatted line")
			assert.Equal(t, MessageTypeSystem, chat.Messages[0].Type,
				"non-envelope results keep MessageTypeSystem")
		})
	}
}

// TestHandleToolCallSuccess_MarkdownEnvelope verifies the generic markdown
// opt-in: an envelope's content is retained raw and rendered through the shared
// renderer, overriding the tool's Formatted step name.
func TestHandleToolCallSuccess_MarkdownEnvelope(t *testing.T) {
	envelope := `{"format":"markdown","content":"# Title\n\nbody"}`
	md, ok := markdownEnvelopeContent(envelope)
	assert.True(t, ok, "a markdown envelope must be recognized")
	assert.Equal(t, "# Title\n\nbody", md, "raw markdown must be returned verbatim")

	chat := NewChatComponent(80, 20, true)
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-md", ToolName: "edict",
		Result: envelope, Formatted: "the edict details",
	})

	assert.Equal(t, "# Title\n\nbody", chat.Messages[0].Content,
		"the raw markdown must be retained as Content")
	assert.Equal(t, MessageTypeMarkdown, chat.Messages[0].Type,
		"a markdown envelope must yield MessageTypeMarkdown")

	rendered := chat.Messages[0].Content
	chat.UpdateContent()
	rendered = chat.Viewport.View()
	assert.Contains(t, rendered, "Title", "the markdown title must be rendered")
	assert.Contains(t, rendered, "body", "the markdown body must be rendered")
	assert.NotContains(t, rendered, "the edict details",
		"the Formatted step name must be overridden by the markdown block")
}

// TestHandleToolCallSuccess_MarkdownEnvelopeReflowsOnResize pins the edict's
// width-sizing invariant for the markdown reply: the block is re-rendered from
// its raw Content at the live component width, so a narrow component re-wraps
// it rather than clipping it.
func TestHandleToolCallSuccess_MarkdownEnvelopeReflowsOnResize(t *testing.T) {
	long := strings.Repeat("word ", 40)
	envelope := `{"format":"markdown","content":"` + strings.TrimSpace(long) + `"}`

	chat := NewChatComponent(30, 20, true)
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-md-narrow", ToolName: "edict",
		Result: envelope, Formatted: "the edict details",
	})
	chat.UpdateContent()

	for _, line := range strings.Split(chat.Viewport.View(), "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), chat.Width,
			"narrow markdown block must not exceed the component width")
	}
	assert.Contains(t, chat.Messages[0].Content, "word word",
		"the raw Content must stay unwrapped so it can reflow")
}

// TestHandleToolCallSuccess_MarkdownEnvelopeUpdatesInPlace covers the branch
// where a scheduled tool call is updated in place with a markdown envelope:
// the message must flip to MessageTypeMarkdown, keep the raw markdown as
// Content (so it re-renders at the live width), and not append a duplicate.
func TestHandleToolCallSuccess_MarkdownEnvelopeUpdatesInPlace(t *testing.T) {
	envelope := `{"format":"markdown","content":"# Title\n\nbody"}`

	chat := NewChatComponent(80, 20, true)
	chat.HandleToolCallScheduled(runners.ToolCallScheduledMsg{
		CallID: "tc-md-upd", ToolName: "edict", Formatted: "the edict details",
	})
	chat.UpdateContent()
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-md-upd", ToolName: "edict",
		Result: envelope, Formatted: "the edict details",
	})

	assert.Len(t, chat.Messages, 1, "update-in-place must not append a new message")
	assert.Equal(t, MessageTypeMarkdown, chat.Messages[0].Type,
		"the updated message must flip to MessageTypeMarkdown")
	assert.Equal(t, "# Title\n\nbody", chat.Messages[0].Content,
		"the raw markdown must be retained as Content")

	chat.UpdateContent()
	assert.Contains(t, chat.Viewport.View(), "Title", "the markdown title must be rendered")
	assert.NotContains(t, chat.Viewport.View(), "the edict details",
		"the Formatted step name must be overridden on the update path too")
}

// TestHandleToolCallSuccess_MalformedEnvelopeFallsBack ensures malformed JSON
// degrades gracefully rather than panicking or dropping the result.
func TestHandleToolCallSuccess_MalformedEnvelopeFallsBack(t *testing.T) {
	chat := NewChatComponent(80, 20, true)
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-bad", ToolName: "tian_ledger",
		Result: `{"format":"markdown_table","columns":["a",`, Formatted: "Found events",
	})
	assert.Equal(t, checkPrefix+" Found events", chat.Messages[0].Content)
}

// TestHandleToolCallSuccess_MarkdownTableTooNarrowFallsBack covers the
// degrade path the edict requires when the component cannot hold even a
// minimal table: the caller must fall back to the plain Formatted line rather
// than emit a broken table.
func TestHandleToolCallSuccess_MarkdownTableTooNarrowFallsBack(t *testing.T) {
	envelope := `{"format":"markdown_table","title":"Heaven Ledger",` +
		`"columns":["id","edict_id","event_type","created_at","detail"],` +
		`"rows":[["1","5","ritual_started","2026-10-11T10:00:00Z","swift-strike"]]}`

	chat := NewChatComponent(10, 20, true)
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-too-narrow", ToolName: "tian_ledger",
		Result: envelope, Formatted: "Found 1 events",
	})
	assert.Equal(t, checkPrefix+" Found 1 events", chat.Messages[0].Content,
		"a component too narrow for a minimal table must fall back to Formatted")
}

// TestHandleToolCallSuccess_MarkdownTableEscapesPipes covers the cell-escaping
// the edict requires so that a pipe inside a cell cannot open a phantom column
// or push the row past the component width.
func TestHandleToolCallSuccess_MarkdownTableEscapesPipes(t *testing.T) {
	envelope := `{"format":"markdown_table","title":"T",` +
		`"columns":["a","b"],"rows":[["x|y","z"],["p q","r|s"]]}`

	chat := NewChatComponent(40, 20, true)
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-pipe", ToolName: "some_tool",
		Result: envelope, Formatted: "F",
	})
	content := chat.Messages[0].Content
	for _, line := range strings.Split(content, "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), chat.Width,
			"an escaped pipe must not push a row past the component width")
	}
	assert.Contains(t, content, "x|y", "cell content must survive escaping")
	assert.Contains(t, content, "r|s", "cell content must survive escaping")
}

// TestHandleToolCallSuccess_MarkdownTableReflowsOnResize pins the edict's
// width-sizing invariant: "rendered table width ≤ component width, and it
// re-lays-out on resize". The table is built from the live component width, so
// growing must reveal previously truncated cells and shrinking must produce a
// single-row header (not a word-wrapped fragment of the old layout).
func TestHandleToolCallSuccess_MarkdownTableReflowsOnResize(t *testing.T) {
	envelope := `{"format":"markdown_table","title":"Heaven Ledger",` +
		`"columns":["id","edict_id","event_type","created_at","detail"],` +
		`"rows":[["1","5","ritual_started","2026-10-11T10:00:00Z","swift-strike"]]}`

	chat := NewChatComponent(50, 20, true)
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-resize", ToolName: "tian_ledger",
		Result: envelope, Formatted: "Found 1 events",
	})

	// Grow: the wider component must re-lay-out the table, so no cell is still
	// truncated from the narrow render.
	chat.SetSize(120, 20)
	content := chat.Messages[0].Content
	assert.NotContains(t, content, "…",
		"growing the component must re-lay-out the table, not keep narrow truncation")
	assert.Contains(t, content, "edict_id", "header must be whole after growing")
	assert.Contains(t, content, "created_at", "header must be whole after growing")

	// Shrink: the table must re-lay-out to the new width — one header row, one
	// separator, one data row — while still fitting the component.
	chat.SetSize(60, 20)
	content = chat.Messages[0].Content
	tableLines := 0
	for _, line := range strings.Split(content, "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), chat.Width,
			"re-laid-out table must not exceed the component width")
		if strings.Contains(line, "|") {
			tableLines++
		}
	}
	assert.Equal(t, 3, tableLines,
		"shrinking must re-lay-out to header + separator + one row, got wrapped fragments")
}

// TestHandleToolCallSuccess_MarkdownTableReflowsOnUpdatePath covers the branch
// where a scheduled message is updated in place: the table must still be
// retained on the message so it re-lays-out on resize.
func TestHandleToolCallSuccess_MarkdownTableReflowsOnUpdatePath(t *testing.T) {
	envelope := `{"format":"markdown_table","title":"Heaven Ledger",` +
		`"columns":["id","edict_id","event_type","created_at","detail"],` +
		`"rows":[["1","5","ritual_started","2026-10-11T10:00:00Z","swift-strike"]]}`

	chat := NewChatComponent(50, 20, true)
	chat.HandleToolCallScheduled(runners.ToolCallScheduledMsg{
		CallID: "tc-upd", ToolName: "tian_ledger", Formatted: "scheduled",
	})
	chat.UpdateContent()
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-upd", ToolName: "tian_ledger",
		Result: envelope, Formatted: "Found 1 events",
	})
	assert.Len(t, chat.Messages, 1, "update-in-place must not append a new message")
	assert.NotNil(t, chat.Messages[0].Table, "in-place update must retain the envelope")

	chat.SetSize(120, 20)
	assert.NotContains(t, chat.Messages[0].Content, "…",
		"growing must re-lay-out the table on the update path too")
	assert.Contains(t, chat.Messages[0].Content, "created_at",
		"header must be whole after growing")
}

// TestHandleToolCallSuccess_MarkdownTableWideGlyphWidth pins the §3 invariant
// for the ledger's real data: `detail` is derived from the event payload's
// `intent` (see tianEventDetail), which routinely carries wide glyphs — CJK and
// emoji are present in ~10% of this court's edict intents. A cell's width must
// be measured in display columns, not runes, or a 2-column glyph overruns the
// component and the separator wraps, corrupting the table.
func TestHandleToolCallSuccess_MarkdownTableWideGlyphWidth(t *testing.T) {
	// 17 CJK glyphs = 34 display columns but only 17 runes.
	envelope := `{"format":"markdown_table","title":"Heaven Ledger",` +
		`"columns":["id","edict_id","event_type","created_at","detail"],` +
		`"rows":[["26","26","edict_created","2026-10-14T11:40:32Z","三界正名与封印链天书台宽度自适应"]]}`

	chat := NewChatComponent(80, 20, true)
	chat.HandleToolCallSuccess(runners.ToolCallSuccessMsg{
		CallID: "tc-cjk", ToolName: "tian_ledger",
		Result: envelope, Formatted: "Found 1 events",
	})
	chat.UpdateContent()

	for _, line := range strings.Split(chat.Messages[0].Content, "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), chat.Width,
			"a wide-glyph cell must not push the rendered table past the component width")
	}
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

// ===== Ling output attribution tests =====

// TestAddAIChunkOwned_KeepsConcurrentLingsSeparate verifies the core
// attribution property: chunks from different lings never merge into one
// anonymous block, so a reader can tell which ling produced which text.
func TestAddAIChunkOwned_KeepsConcurrentLingsSeparate(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.AddAIChunkOwned("forge ", "ling-a", "forge")
	chat.AddAIChunkOwned("work", "ling-a", "forge")
	chat.AddAIChunkOwned("judge ", "ling-b", "judge")
	chat.AddAIChunkOwned("verdict", "ling-b", "judge")
	chat.AddAIChunkOwned(" done", "ling-a", "forge")

	assert.Len(t, chat.Messages, 3, "owner change must start a new block")
	assert.Equal(t, "forge work", chat.Messages[0].Content)
	assert.Equal(t, "ling-a", chat.Messages[0].Owner)
	assert.Equal(t, "forge", chat.Messages[0].OwnerMinister)
	assert.Equal(t, "judge verdict", chat.Messages[1].Content)
	assert.Equal(t, "ling-b", chat.Messages[1].Owner)
	assert.Equal(t, " done", chat.Messages[2].Content)
	assert.Equal(t, "ling-a", chat.Messages[2].Owner)
}

// TestAddAIChunkOwned_EmptyOwnerIsUnchanged locks backwards compatibility:
// an unattributed chunk renders and accumulates exactly like AddAIChunk.
func TestAddAIChunkOwned_EmptyOwnerIsUnchanged(t *testing.T) {
	owned := NewChatComponent(80, 20, false)
	plain := NewChatComponent(80, 20, false)

	owned.AddAIChunkOwned("hello ", "", "")
	owned.AddAIChunkOwned("world", "", "")
	plain.AddAIChunk("hello ")
	plain.AddAIChunk("world")

	assert.Len(t, owned.Messages, 1)
	assert.Equal(t, plain.Messages[0].Content, owned.Messages[0].Content)
	assert.Empty(t, owned.Messages[0].Owner)
	owned.UpdateContent()
	plain.UpdateContent()
	assert.Equal(t, plain.Viewport.View(), owned.Viewport.View(),
		"unattributed output must render identically to before")
}

// TestRenderSystemMessage_LingPrefixOnlyWhenAttributed verifies the visible
// token: when the chat holds several owners, attributed blocks are labelled with
// minister/ling; unattributed blocks carry no prefix at all.
func TestRenderSystemMessage_LingPrefixOnlyWhenAttributed(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	attributed := chat.renderSystemMessage("text", MessageTypeAI, "ling-a", "forge", true)
	assert.Contains(t, attributed, "[forge/ling-a]")

	unattributed := chat.renderSystemMessage("text", MessageTypeAI, "", "", true)
	assert.NotContains(t, unattributed, "[")
}

// TestRenderSystemMessage_SingleOwnerHasNoPrefix pins backwards compatibility at
// the point where it now lives: a tab whose blocks all belong to one ling shows
// no attribution token, so a single-ling edict renders byte-for-byte as before,
// even though the court always attributes its lings.
func TestRenderSystemMessage_SingleOwnerHasNoPrefix(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	chat.AddAIChunkOwned("solo answer", "ling-solo", "forge")
	chat.AddThinkingChunkOwned("solo reasoning", "ling-solo", "forge")

	require.True(t, chat.HasAttributedMessages(), "the ling is attributed on the wire")
	chat.UpdateContent()

	view := chat.Viewport.View()
	assert.NotContains(t, view, "[", "a single owner needs no attribution token")
	assert.Contains(t, view, "solo answer")

	// A second owner makes the prefix informative and it appears for both.
	chat.AddAIChunkOwned("second answer", "ling-two", "judge")
	chat.UpdateContent()
	view = chat.Viewport.View()
	assert.Contains(t, view, "[forge/ling-solo]")
	assert.Contains(t, view, "[judge/ling-two]")

	// Block splitting itself is unconditional, even for one owner.
	assert.Len(t, chat.Messages, 3, "per-owner blocks are always separate")
}

// TestFinalizeLingAIMessages_SingleOwnerChannelWide pins the behaviour delta of
// unconditional attribution: a single-ling tab takes the attributed
// channel-wide finalize path (FinalizeLingAIMessages("")) rather than
// FinalizeLastAIMessage. Both end with the block complete and no owner left
// streaming-looking, which is what the TUI's StreamCompleteMsg path relies on.
func TestFinalizeLingAIMessages_SingleOwnerChannelWide(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	chat.AddAIChunkOwned("only answer", "ling-solo", "forge")

	require.True(t, chat.HasAttributedMessages(),
		"a single-ling tab is attributed, so the TUI takes the attributed path")
	assert.False(t, chat.FinalizeLingAIMessages(""))

	require.Len(t, chat.Messages, 1)
	assert.Equal(t, MessageTypeAISuccess, chat.Messages[0].Type,
		"the sole block is finalized exactly as FinalizeLastAIMessage would")
	assert.Equal(t, "only answer", chat.Messages[0].Content)
	assert.Equal(t, "ling-solo", chat.Messages[0].Owner)
}

// TestFinalizeLingAIMessages_OnlyFinalizesThatLing verifies that one ling's
// completion leaves the other concurrent lings' blocks still streaming, and
// that a channel-wide finalize clears whatever remains.
func TestFinalizeLingAIMessages_OnlyFinalizesThatLing(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.AddAIChunkOwned("a", "ling-a", "forge")
	chat.AddAIChunkOwned("b", "ling-b", "forge")
	require.True(t, chat.HasAttributedMessages())

	assert.False(t, chat.FinalizeLingAIMessages("ling-a"))
	assert.Equal(t, MessageTypeAISuccess, chat.Messages[0].Type)
	assert.Equal(t, MessageTypeAI, chat.Messages[1].Type,
		"another ling's block must stay streaming")

	assert.False(t, chat.FinalizeLingAIMessages(""))
	assert.Equal(t, MessageTypeAISuccess, chat.Messages[1].Type)
}

// TestAddThinkingChunkOwned_KeepsConcurrentLingsSeparate covers the reasoning
// half of attribution: thinking chunks of two lings sharing one tab must not
// merge, and each block must be labelled with its own minister/ling token.
// Without this a reader sees one long "💭" block and cannot tell who reasoned.
func TestAddThinkingChunkOwned_KeepsConcurrentLingsSeparate(t *testing.T) {
	chat := NewChatComponent(80, 20, false)

	chat.AddThinkingChunkOwned("weighing ", "ling-a", "forge")
	chat.AddThinkingChunkOwned("evidence", "ling-a", "forge")
	chat.AddThinkingChunkOwned("judging ", "ling-b", "judge")
	chat.AddThinkingChunkOwned("slowly", "ling-b", "judge")

	require.Len(t, chat.Messages, 2, "owner change must start a new thinking block")
	assert.Equal(t, "weighing evidence", chat.Messages[0].Content)
	assert.Equal(t, "ling-a", chat.Messages[0].Owner)
	assert.Equal(t, "forge", chat.Messages[0].OwnerMinister)
	assert.Equal(t, MessageTypeThinking, chat.Messages[1].Type)
	assert.Equal(t, "judging slowly", chat.Messages[1].Content)

	chat.UpdateContent()
	view := chat.Viewport.View()
	assert.Contains(t, view, "[forge/ling-a]", "attributed reasoning must be labelled")
	assert.Contains(t, view, "[judge/ling-b]")
}

// TestAddThinkingChunkOwned_EmptyOwnerAndBlankChunk locks the two delegating
// branches: a ritual step (no owner) must render exactly like AddThinkingChunk,
// and a blank chunk is dropped before any block is created.
func TestAddThinkingChunkOwned_EmptyOwnerAndBlankChunk(t *testing.T) {
	owned := NewChatComponent(80, 20, false)
	plain := NewChatComponent(80, 20, false)

	owned.AddThinkingChunkOwned("pondering", "", "")
	plain.AddThinkingChunk("pondering")
	owned.AddThinkingChunkOwned("   ", "ling-a", "forge")

	require.Len(t, owned.Messages, 1, "blank reasoning must not open a block")
	assert.Empty(t, owned.Messages[0].Owner)
	owned.UpdateContent()
	plain.UpdateContent()
	assert.Equal(t, plain.Viewport.View(), owned.Viewport.View(),
		"unattributed reasoning must render exactly as before")
}

// TestFinalizeLastAIMessage_FailureToken pins the success/failure split that the
// finalize refactor now shares between the last-message and per-ling paths: a
// block prefixed with the failure token must become an AI failure block with
// the token stripped, and no other block may be touched.
func TestFinalizeLastAIMessage_FailureToken(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	chat.AddAIChunk(failureToken + " the tool failed")

	require.True(t, chat.FinalizeLastAIMessage(), "failure token marks the block as failed")
	assert.Equal(t, MessageTypeAIFailure, chat.Messages[0].Type)
	assert.Equal(t, "the tool failed", chat.Messages[0].Content,
		"the failure token must not leak into the rendered text")
}

// TestFinalizeLingAIMessages_FailureToken verifies the per-ling path keeps the
// same failure semantics as the channel-wide path, and that only the named
// ling's block is finalized.
func TestFinalizeLingAIMessages_FailureToken(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	chat.AddAIChunkOwned(failureToken+" ling one broke", "ling-1", "forge")
	chat.AddAIChunkOwned("ling two is fine", "ling-2", "forge")

	require.True(t, chat.FinalizeLingAIMessages("ling-1"))
	assert.Equal(t, MessageTypeAIFailure, chat.Messages[0].Type)
	assert.Equal(t, "ling one broke", chat.Messages[0].Content)
	assert.Equal(t, MessageTypeAI, chat.Messages[1].Type, "an unrelated ling must not be finalized")
}

// TestHasAttributedMessages_FalseForUnattributedChat verifies the predicate that
// routes the channel-wide completion path: an ordinary single-ling or ritual
// chat holds no owned block, so the old FinalizeLastAIMessage path is used.
func TestHasAttributedMessages_FalseForUnattributedChat(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	chat.AddAIChunk("plain streaming")
	assert.False(t, chat.HasAttributedMessages())

	chat.AddAIChunkOwned("owned", "ling-1", "forge")
	assert.True(t, chat.HasAttributedMessages(), "any owned block makes the tab attributed")
}

// TestFinalizeDeadLingAIMessages_ClosesOnlyDeadLings covers the terminal-path
// helper: blocks of lings still live must keep streaming, every other owned
// block is closed — as failure on error/cancel paths, as success otherwise.
func TestFinalizeDeadLingAIMessages_ClosesOnlyDeadLings(t *testing.T) {
	chat := NewChatComponent(80, 20, false)
	chat.AddAIChunkOwned("dead one", "ling-1", "forge")
	chat.AddAIChunkOwned("still running", "ling-2", "forge")
	chat.AddAIChunkOwned("dead two", "ling-3", "judge")

	live := map[string]bool{"ling-2": true}
	assert.True(t, chat.FinalizeDeadLingAIMessages(live, true))
	assert.Equal(t, MessageTypeAIFailure, chat.Messages[0].Type)
	assert.Equal(t, MessageTypeAI, chat.Messages[1].Type, "a live ling must keep streaming")
	assert.Equal(t, MessageTypeAIFailure, chat.Messages[2].Type)

	// Unattributed blocks are never touched: they are not a ling's to close.
	plain := NewChatComponent(80, 20, false)
	plain.AddAIChunk("plain streaming")
	assert.False(t, plain.FinalizeDeadLingAIMessages(nil, true))
	assert.Equal(t, MessageTypeAI, plain.Messages[0].Type)
}

// TestHasMultipleOwners_Semantics pins the predicate that decides whether the
// visible attribution prefix appears. It must count DISTINCT owners across the
// whole chat, not merely adjacent changes: concurrent lings interleave (A,B,A),
// so an adjacency-based test would hide the prefix exactly when it matters.
func TestHasMultipleOwners_Semantics(t *testing.T) {
	t.Run("interleaved owners", func(t *testing.T) {
		c := NewChatComponent(80, 20, false)
		c.AddAIChunkOwned("1", "ling-a", "forge")
		c.AddAIChunkOwned("2", "ling-b", "judge")
		c.AddAIChunkOwned("3", "ling-a", "forge")
		assert.True(t, c.hasMultipleOwners(), "A,B,A holds two distinct owners")
	})

	t.Run("repeated second owner", func(t *testing.T) {
		c := NewChatComponent(80, 20, false)
		c.AddAIChunkOwned("1", "ling-a", "forge")
		c.AddAIChunkOwned("2", "ling-b", "judge")
		c.AddAIChunkOwned("3", "ling-b", "judge")
		c.AddAIChunkOwned("4", "ling-a", "forge")
		assert.True(t, c.hasMultipleOwners())
	})

	t.Run("single owner", func(t *testing.T) {
		c := NewChatComponent(80, 20, false)
		c.AddAIChunkOwned("1", "ling-a", "forge")
		assert.False(t, c.hasMultipleOwners(), "one owner needs no prefix")
	})

	t.Run("no owners", func(t *testing.T) {
		c := NewChatComponent(80, 20, false)
		c.AddAIChunk("plain")
		assert.False(t, c.hasMultipleOwners())
	})

	t.Run("unowned messages between owned ones", func(t *testing.T) {
		c := NewChatComponent(80, 20, false)
		c.AddAIChunkOwned("1", "ling-a", "forge")
		c.AddAIChunk("plain")
		assert.False(t, c.hasMultipleOwners(),
			"unowned messages are not owners and must not trigger the prefix")
	})
}

// TestLingBanner_NamesLingOnlyWhenMultipleOwners pins the banner rule added for
// per-ling terminal events: on a multi-ling tab the ruler must see WHICH ling
// died, while a single-ling tab's banners stay byte-for-byte unchanged.
func TestLingBanner_NamesLingOnlyWhenMultipleOwners(t *testing.T) {
	single := NewChatComponent(80, 20, false)
	single.AddAIChunkOwned("a", "ling-1", "forge")
	single.UpdateContent()
	assert.Equal(t, "ABORTED", lingBanner(single, "ling-1", "ABORTED"),
		"a lone owner's banner carries no token")

	multi := NewChatComponent(80, 20, false)
	multi.AddAIChunkOwned("a", "ling-1", "forge")
	multi.AddAIChunkOwned("b", "ling-2", "judge")
	multi.UpdateContent()
	assert.Contains(t, lingBanner(multi, "ling-1", "ABORTED"), "[forge/ling-1]",
		"a multi-ling banner names the ling that died, with the token the blocks use")
	assert.NotContains(t, lingBanner(multi, "ling-1", "ABORTED"), "ling-2")

	// A ling with no recorded block has no known minister; the banner still
	// names the ling rather than swallowing the attribution.
	assert.Contains(t, lingBanner(multi, "ling-9", "ABORTED"), "[ling-9]",
		"an unattributed-minister banner still names the ling")

	assert.Equal(t, "ABORTED", lingBanner(multi, "", "ABORTED"),
		"a channel-wide event has no ling to name")
}

// TestOwnerMinisterFor_ResolvesFromTheLingsOwnBlocks pins the resolver that lets
// terminal banners use the same "[minister/ling]" token as the blocks without
// widening the terminal wire messages. It must never invent a minister: an
// unknown ling, a ling whose blocks recorded no minister, and the empty ling id
// all resolve to "" so lingPrefix falls back to the bare "[ling]" form.
func TestOwnerMinisterFor_ResolvesFromTheLingsOwnBlocks(t *testing.T) {
	c := NewChatComponent(80, 20, false)
	c.AddAIChunkOwned("a", "ling-1", "forge")
	c.AddAIChunkOwned("b", "ling-2", "judge")

	assert.Equal(t, "forge", c.OwnerMinisterFor("ling-1"))
	assert.Equal(t, "judge", c.OwnerMinisterFor("ling-2"))

	assert.Empty(t, c.OwnerMinisterFor(""), "no ling, no minister")
	assert.Empty(t, c.OwnerMinisterFor("ling-unknown"), "an unknown ling has no minister")

	// A ling whose block recorded no minister stays without one — the banner
	// must not borrow another ling's minister.
	bare := NewChatComponent(80, 20, false)
	bare.AddAIChunkOwned("a", "ling-3", "")
	assert.Empty(t, bare.OwnerMinisterFor("ling-3"))
	assert.Contains(t, lingPrefix("ling-3", bare.OwnerMinisterFor("ling-3")), "[ling-3]",
		"the bare form still names the ling")
}
