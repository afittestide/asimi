package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewResumeWindowDefaults(t *testing.T) {
	window := NewResumeWindow()

	assert.Equal(t, 70, window.Width)
	assert.Equal(t, 15, window.Height)
	assert.False(t, window.Loading)
	assert.Empty(t, window.Items)
}

func TestResumeWindowSetSizeAdjustsVisibleSlots(t *testing.T) {
	window := NewResumeWindow()

	window.SetSize(80, 10)
	assert.Equal(t, 80, window.Width)
	assert.Equal(t, 10, window.Height)

	window.SetSize(50, 2)
	assert.Equal(t, 2, window.Height) // min clamp
}

func TestResumeWindowSetSessionsAndRender(t *testing.T) {
	window := NewResumeWindow()
	now := time.Now()

	sessions := []court.Session{
		*testSession("s-1", "Refactor prompt", now, "Need to refactor"),
		*testSession("s-2", "Investigate bug", now.Add(-2*time.Hour), "Bug details"),
	}

	window.SetSessions(sessions)
	assert.False(t, window.Loading)
	assert.Equal(t, 2, window.GetItemCount())

	render := window.RenderList(0, 0, window.GetVisibleSlots())
	assert.Contains(t, render, sessionTitlePreview(&sessions[0]))
	assert.Contains(t, render, sessionTitlePreview(&sessions[1]))
	assert.Contains(t, render, "▶ ")
	assert.Contains(t, render, "]    1 Need to refactor")
}

func TestResumeWindowLoadingAndErrorStates(t *testing.T) {
	window := NewResumeWindow()

	window.SetLoading(true)
	assert.Contains(t, window.RenderList(0, 0, window.GetVisibleSlots()), "Loading sessions")

	window.SetLoading(false)
	window.SetError(assert.AnError)
	render := window.RenderList(0, 0, window.GetVisibleSlots())
	assert.Contains(t, render, "Error loading sessions")
	assert.NotContains(t, render, "Loading sessions")
}

func TestResumeWindowEmptyState(t *testing.T) {
	window := NewResumeWindow()
	window.SetSessions(nil)

	render := window.RenderList(0, 0, window.GetVisibleSlots())
	assert.Contains(t, render, "No previous sessions found")
	assert.Contains(t, render, "Start chatting to create a new session")
}

func TestResumeWindowScrollInfo(t *testing.T) {
	window := NewResumeWindow()
	now := time.Now()

	var sessions []court.Session
	for i := 0; i < 20; i++ {
		sessions = append(sessions, *testSession(
			fmt.Sprintf("s-%d", i+1),
			fmt.Sprintf("Prompt %d", i+1),
			now.Add(-time.Duration(i)*time.Minute),
			fmt.Sprintf("Message %d", i+1),
		))
	}

	window.SetSessions(sessions)
	render := window.RenderList(5, 5, 5)

	assert.Contains(t, render, "Message 6")    // Uses last human message when Messages is populated
	assert.NotContains(t, render, "Message 2") // scrolled past
}

func TestResumeWindowGetSelectedSession(t *testing.T) {
	window := NewResumeWindow()
	window.SetSessions([]court.Session{
		*testSession("one", "First", time.Now(), "msg"),
	})

	assert.Nil(t, window.GetSelectedSession(-1))
	assert.Nil(t, window.GetSelectedSession(2))

	session := window.GetSelectedSession(0)
	assert.NotNil(t, session)
	assert.Equal(t, "one", session.ID)
}

func TestSessionTitlePreviewFallbacks(t *testing.T) {
	session := testSession("s-1", "", time.Now(), "")
	session.SetMessages(nil)
	assert.Equal(t, "Recent activity", sessionTitlePreview(session))

	session.FirstPrompt = " initial "
	assert.Equal(t, "initial", sessionTitlePreview(session))

	session.SetMessages([]schemas.ChatMessage{
		textMessage(schemas.ChatMessageRoleUser, "User question"),
	})
	assert.Equal(t, "User question", sessionTitlePreview(session))
}

func testSession(id, prompt string, updated time.Time, messageTexts ...string) *court.Session {
	var messages []schemas.ChatMessage
	for _, text := range messageTexts {
		messages = append(messages, textMessage(schemas.ChatMessageRoleUser, text))
	}

	s := &court.Session{
		ID:           id,
		FirstPrompt:  prompt,
		LastUpdated:  updated,
		MessageCount: len(messages),
		Model:        "test",
	}
	s.SetMessages(messages)
	return s
}

// ===== Progressive Session Resume Pager Tests (edict 771) =====

// TestResumeRebuildBatch_CursorAdvancesAndCompletes drives the pager across
// enough messages to span multiple batches and verifies the cursor advances to
// the end, the content is flushed (no loss), and the pager deactivates.
func TestResumeRebuildBatch_CursorAdvancesAndCompletes(t *testing.T) {
	model := newTestModel(t)
	// 10 messages -> batches of 4: 4+4+2.
	msgs := make([]ChatMessage, 10)
	for i := range msgs {
		msgs[i] = ChatMessage{Content: fmt.Sprintf("msg-%d", i), Type: MessageTypeUser}
	}
	model.resumeRebuildMessages = msgs
	model.resumeRebuildCursor = 0
	model.resumeRebuildActive = true
	session := testSession("resume-batch", "First", time.Now(), "msg")
	model.resumeRebuildSession = session

	// First batch appends 4 and continues paging.
	cmd := model.handleResumeRebuildBatch(resumeRebuildBatchMsg{messages: msgs, cursor: 0})
	assert.Equal(t, 4, model.resumeRebuildCursor, "cursor should advance by one batch")
	require.NotNil(t, cmd, "pager should continue while messages remain")
	assert.True(t, model.resumeRebuildActive)
	assert.Len(t, model.tabs.Content().Chat.Messages, 4, "first batch should append 4 messages")
	// The debounce render tick must be scheduled so intermediate content is
	// visible before the final flush — guards the progressive-render contract.
	assert.True(t, model.renderTickPending, "intermediate batch should schedule a chatRenderTickMsg")

	// Second batch appends next 4 -> cursor 8. The pager tick keeps firing so
	// renderTickPending stays true (a fresh tick is rescheduled each batch).
	cmd = model.handleResumeRebuildBatch(resumeRebuildBatchMsg{messages: msgs, cursor: model.resumeRebuildCursor})
	assert.Equal(t, 8, model.resumeRebuildCursor, "cursor should advance to 8 after second batch")
	require.NotNil(t, cmd)
	assert.True(t, model.renderTickPending, "every intermediate batch should keep the render tick scheduled")

	// Final batch appends the last 2 and completes.
	cmd = model.handleResumeRebuildBatch(resumeRebuildBatchMsg{messages: msgs, cursor: model.resumeRebuildCursor})
	assert.Nil(t, cmd, "final batch should not continue paging")
	assert.False(t, model.resumeRebuildActive, "pager should deactivate on final batch")
	assert.Equal(t, 0, model.resumeRebuildCursor, "cursor should reset on completion")
	assert.Nil(t, model.resumeRebuildMessages, "prepared messages should be cleared on completion")
	assert.Nil(t, model.resumeRebuildSession, "session should be cleared on completion")
	assert.Len(t, model.tabs.Content().Chat.Messages, 10, "all messages must be appended, no content loss")

	// Now flush the dirty content via the render tick — simulating the TUI
	// message loop consuming the chatRenderTickMsg produced by intermediate
	// batches. Everything accumulated (even before the pager finished) becomes
	// visible.
	model.renderTickPending = false
	model.tabs.FlushDirtyChats()
	assert.False(t, model.tabs.Content().Chat.contentDirty,
		"render tick flush should clear contentDirty")
	assert.Len(t, model.tabs.Content().Chat.Messages, 10, "no messages lost through debounce flush")
}

// TestResumeRebuildBatch_Inactive_ReturnsNil verifies a deactivated pager no
// longer appends messages (guards against stray lingering ticks).
func TestResumeRebuildBatch_Inactive_ReturnsNil(t *testing.T) {
	model := newTestModel(t)
	model.resumeRebuildActive = false
	model.tabs.Content().Chat.Clear()

	cmd := model.handleResumeRebuildBatch(resumeRebuildBatchMsg{
		messages: []ChatMessage{{Content: "stray", Type: MessageTypeSystem}},
	})
	assert.Nil(t, cmd, "inactive pager should return nil")
	assert.Empty(t, model.tabs.Content().Chat.Messages, "inactive pager should not append")
}

// TestResumeRebuildBatch_NoSession_Deactivates verifies that a missing session
// safely deactivates the pager rather than panicking or appending.
func TestResumeRebuildBatch_NoSession_Deactivates(t *testing.T) {
	model := newTestModel(t)
	model.resumeRebuildActive = true
	model.resumeRebuildSession = nil

	cmd := model.handleResumeRebuildBatch(resumeRebuildBatchMsg{
		messages: []ChatMessage{{Content: "stray", Type: MessageTypeSystem}},
	})
	assert.Nil(t, cmd)
	assert.False(t, model.resumeRebuildActive, "pager should deactivate when session is nil")
}
