package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/storage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/maximhq/bifrost/core/schemas"
)

type sessionsLoadedMsg struct {
	sessions []court.Session
}

type sessionSelectedMsg struct {
	session *court.Session
}

type sessionResumeErrorMsg struct {
	err error
}

// ResumeWindow is a simplified component for displaying session selection
// Navigation is handled by ContentComponent
type ResumeWindow struct {
	SelectWindow[*court.Session]
	loadingSession bool
}

func NewResumeWindow() ResumeWindow {
	sw := NewSelectWindow[*court.Session]()
	sw.Height = 15 // Default height
	sw.SetSize(70, 15)

	return ResumeWindow{
		SelectWindow:   sw,
		loadingSession: false,
	}
}

// SetSessions stores pointers to the sessions so the lock-bearing court.Session
// value is never copied through the generic render callback (see copylocks).
func (r *ResumeWindow) SetSessions(sessions []court.Session) {
	items := make([]*court.Session, len(sessions))
	for i := range sessions {
		items[i] = &sessions[i]
	}
	r.SetItems(items)
	r.loadingSession = false
}

func (r *ResumeWindow) SetError(err error) {
	r.SelectWindow.SetError(err)
	r.loadingSession = false
}

func (r *ResumeWindow) GetSelectedSession(index int) *court.Session {
	p := r.GetSelectedItem(index)
	if p == nil {
		return nil
	}
	return *p
}

func sessionTitlePreview(session *court.Session) string {

	snippet := session.FirstPrompt
	msgs := session.GetMessages()
	if len(msgs) > 0 {
		snippet = lastHumanMessage(msgs)
	}

	snippet = cleanSnippet(snippet)
	if snippet == "" {
		return "Recent activity"
	}
	return truncateSnippet(snippet, 60)
}

func lastHumanMessage(messages []schemas.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != schemas.ChatMessageRoleUser {
			continue
		}
		if messages[i].Content != nil && messages[i].Content.ContentStr != nil {
			text := strings.TrimSpace(*messages[i].Content.ContentStr)
			if text != "" {
				return text
			}
		}
	}
	return ""
}

func cleanSnippet(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "---") && strings.HasSuffix(trimmed, "---") {
			continue
		}
		if strings.HasPrefix(trimmed, "Context from:") {
			continue
		}
		return trimmed
	}

	return strings.TrimSpace(lines[0])
}

func truncateSnippet(text string, limit int) string {
	if limit <= 0 {
		return ""
	}

	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	if limit <= 3 {
		return string(runes[:limit])
	}

	return string(runes[:limit-3]) + "..."
}

// RenderList renders the session list with the given selection
// Always renders exactly visibleSlots lines to maintain consistent height
func (r *ResumeWindow) RenderList(selectedIndex, scrollOffset, visibleSlots int) string {
	// Update maxVisible to match requested visibleSlots if needed,
	// although SelectWindow usually manages this via SetSize.
	// But RenderList takes visibleSlots arg which comes from the caller (who might have calculated it differently).
	// The caller (ContentComponent) usually calls SetSize, then RenderList.
	// But let's just use what SelectWindow has or respect the arg?
	// In the original code: `lr := lineRenderer{targetLines: visibleSlots}`
	// So we should pass visibleSlots to SetSize or trust it matches?
	// Actually SelectWindow.Render uses s.MaxVisible.
	// We should probably ensure s.MaxVisible is synced or just rely on s.MaxVisible.

	// Wait, RenderList signature in ResumeWindow takes `visibleSlots`.
	// SelectWindow.Render uses s.MaxVisible.
	// If we want to support dynamic visibleSlots per render, SelectWindow.Render should maybe take it?
	// But `SetSize` updates `MaxVisible`.
	// Let's assume `SetSize` was called correctly.

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(globalTheme.PromptBorder).
		Background(globalTheme.PaneBackground).
		Padding(0, 1)

	config := RenderConfig[*court.Session]{
		ConstructTitle: func(selectedIndex, totalItems int) string {
			return titleStyle.Render(fmt.Sprintf("Choose a session to resume [%3d/%3d]:", selectedIndex+1, totalItems))
		},
		OnLoading: func(sb *strings.Builder) {
			sb.WriteString("Loading sessions...\n")
			sb.WriteString("\n")
			sb.WriteString("⏳ Fetching previous sessions...\n")
		},
		CustomState: func(sb *strings.Builder) bool {
			if r.loadingSession {
				sb.WriteString("Loading selected session...\n")
				sb.WriteString("Please wait...\n")
				return true
			}
			return false
		},
		OnError: func(sb *strings.Builder, err error) {
			sb.WriteString(fmt.Sprintf("Error loading sessions: %v\n", err))
			sb.WriteString("\n")
		},
		OnEmpty: func(sb *strings.Builder) {
			sb.WriteString("No previous sessions found.\n")
			sb.WriteString("Start chatting to create a new session!\n")
			sb.WriteString("\n")
		},
		RenderItem: func(i int, session *court.Session, isSelected bool, sb *strings.Builder) {
			prefix := "  "
			if isSelected {
				prefix = "▶ "
			}

			timeStr := formatRelativeTime(session.LastUpdated)
			sessionTitle := sessionTitlePreview(session)

			var line strings.Builder
			line.WriteString(prefix)
			line.WriteString(fmt.Sprintf("[%s] %4d %s", timeStr, session.MessageCount, sessionTitle))

			lineStyle := lipgloss.NewStyle()
			if isSelected {
				lineStyle = lineStyle.Foreground(globalTheme.SuccessColor).Bold(true)
			}

			sb.WriteString(lineStyle.Render(line.String()) + "\n")
		},
	}

	return r.Render(selectedIndex, scrollOffset, config)
}

// LoadSession loads a session by ID using the provided session store.
// This avoids opening a second DB connection that would contend with the
// daemon's saves.
func (r *ResumeWindow) LoadSession(sessionID string, store *SessionStore) tea.Cmd {
	r.loadingSession = true

	return func() tea.Msg {
		if store == nil {
			return sessionResumeErrorMsg{err: fmt.Errorf("session store not initialized")}
		}

		mainSession, err := store.LoadSession(sessionID)
		if err != nil {
			return sessionResumeErrorMsg{err: fmt.Errorf("failed to load session: %w", err)}
		}

		if mainSession == nil {
			return sessionResumeErrorMsg{err: fmt.Errorf("session %s not found", sessionID)}
		}

		return sessionSelectedMsg{session: mainSession}
	}
}

func formatRelativeTime(t time.Time) string {
	now := time.Now()

	// Normalize to midnight for calendar day comparison
	todayMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tMidnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())

	daysDiff := int(todayMidnight.Sub(tMidnight).Hours() / 24)

	// Today
	if daysDiff == 0 {
		return fmt.Sprintf("Today %s", t.Format("15:04"))
	}

	// Yesterday
	if daysDiff == 1 {
		return fmt.Sprintf("Yesterday %s", t.Format("15:04"))
	}

	// This year - show month and day
	if t.Year() == now.Year() {
		return t.Format("Jan 2 15:04")
	}

	// Older - show full date
	return t.Format("Jan 2, 2006")
}

// handleSessionSelected processes a resumed session and updates the TUI model.
// The full chat rebuild is split into small batches via resumeRebuildBatchMsg
// ticks so the UI remains responsive while the history assembles. The
// minister-session (court history) restore is done immediately/synchronously,
// exactly as before; only the chat RENDER is progressive.
//
// When m.creatingTab is set, the session is restored onto an edict tab (e<N>)
// instead of switching to the minister's original interactive tab. If
// m.pendingPrompt is also set, it is submitted to the minister after restore.
// When m.creatingTab is empty, this is a normal resume from the session list.
func (m *TUIModel) handleSessionSelected(session *court.Session) tea.Cmd {
	if session == nil {
		return nil
	}

	if m.creatingTab == "" {
		// Normal resume: clear edict key, switch to session's tab type
		m.currentEdictKey = storage.EdictKey{}
		if session.TabType != "" {
			m.tabs.SwitchToTabType(TabType(session.TabType))
		}
	} else {
		// Edict tab restore: set edict key, stay on the tab
		edictID, _ := strconv.ParseUint(m.creatingTab[1:], 10, 64)
		m.currentEdictKey = m.court.EdictKey(uint(edictID))
	}

	// Clear chat UI; the history is progressively appended below.
	m.tabs.Content().Chat.Clear()
	m.sessionActive = true

	// Restore minister session immediately/synchronously (court history).
	// This must happen before the first rebuild tick so the final batch can
	// submit the pending prompt in the same order as before.
	if m.court != nil {
		tabType := session.TabType
		if tabType == "" {
			tabType = "secretary"
		}
		tab := m.tabs.ActiveTab()
		if m.creatingTab == "" {
			m.court.RestoreMinisterSession(tabType, session.GetMessages())
		} else {
			m.court.RestoreMinisterSession(tabType, session.GetMessages(), tab.Target)
		}
	}

	// Reset in-session prompt history state to prevent rollback issues
	// when the user enters a new prompt after resuming.
	m.sessionPromptHistory = make([]promptHistoryEntry, 0)
	m.historyCursor = 0
	m.historySaved = false
	m.historyPendingPrompt = ""
	m.historyPresentSessionSnapshot = 0
	m.historyPresentChatSnapshot = 0

	// Prepare all display messages once (tool-result mapping needs the full set),
	// then start the progressive pager. The pending prompt and creating-tab
	// handling run on the final batch, after the full history has assembled.
	m.resumeRebuildMessages = buildResumeChatMessages(session.GetMessages())
	m.resumeRebuildCursor = 0
	m.resumeRebuildSession = session
	m.resumeRebuildActive = true

	return m.resumeRebuildTick()
}

// resumeRebuildConstMessages is the number of display messages appended per
// resumeRebuildBatchMsg tick.
const resumeRebuildConst = 4

// resumeRebuildTick returns a tea.Cmd that fires a resumeRebuildBatchMsg after
// a short delay, driving one batch of the progressive chat rebuild.
func (m *TUIModel) resumeRebuildTick() tea.Cmd {
	return tea.Tick(30*time.Millisecond, func(time.Time) tea.Msg {
		return resumeRebuildBatchMsg{messages: m.resumeRebuildMessages, cursor: m.resumeRebuildCursor}
	})
}

// handleResumeRebuildBatch processes the next batch of prepared messages for
// the progressive session resume. It appends a few messages at a time (leaving
// contentDirty so the render tick shows progress), and on the final batch
// flushes, restores the pending prompt, and cleans up.
func (m *TUIModel) handleResumeRebuildBatch(msg resumeRebuildBatchMsg) tea.Cmd {
	if !m.resumeRebuildActive {
		return nil
	}
	if m.resumeRebuildSession == nil {
		m.resumeRebuildActive = false
		return nil
	}

	cursor := msg.cursor
	messages := msg.messages
	chat := m.tabs.Content().Chat

	// Append the next window of prepared messages.
	end := cursor + resumeRebuildConst
	if end > len(messages) {
		end = len(messages)
	}
	if cursor < len(messages) {
		chat.AppendBatch(messages[cursor:end])
		m.resumeRebuildCursor = end
	}

	// Not done yet — continue paging.
	if end < len(messages) {
		cmds := []tea.Cmd{m.resumeRebuildTick()}
		// Schedule a debounce render tick so intermediate batches become
		// visible before the final flush — mirroring the streaming handler.
		if chat.contentDirty && !m.renderTickPending {
			m.renderTickPending = true
			cmds = append(cmds, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return chatRenderTickMsg{} }))
		}
		return tea.Batch(cmds...)
	}

	// Final batch: flush the accumulated dirty content, then handle pending
	// prompt / creatingTab exactly like the synchronous path did.
	m.tabs.Content().Chat.FlushDirty()
	session := m.resumeRebuildSession
	m.resumeRebuildMessages = nil
	m.resumeRebuildSession = nil
	m.resumeRebuildCursor = 0
	m.resumeRebuildActive = false

	// If restoring with a pending prompt, show it as a user message so the
	// Ruler can see their own input before the AI response arrives.
	if m.pendingPrompt != "" {
		m.tabs.Content().Chat.AddUserMessage(m.pendingPrompt)
	}

	// Submit pending prompt if set
	if m.pendingPrompt != "" {
		ministerID := session.TabType
		if ministerID == "" {
			ministerID = "secretary"
		}
		tab := m.tabs.ActiveTab()
		p := &court.Prompt{
			Ctx:       context.Background(),
			Message:   m.pendingPrompt,
			EdictKey:  m.currentEdictKey,
			ChannelID: tab.Target,
		}
		if err := m.court.SubmitPrompt(ministerID, p); err != nil {
			slog.Warn("failed to submit pending prompt", "error", err)
		} else {
			m.tabs.SetStreamingTabByTab(tab.Target)
		}
		m.pendingPrompt = ""
	} else if m.creatingTab != "" {
		// Edict restore without prompt — show toast
		timeStr := formatRelativeTime(session.LastUpdated)
		m.commandLine.AddToast(fmt.Sprintf("Resumed session for edict from %s", timeStr), "success", 3000)
	} else {
		// Normal resume — show toast
		timeStr := formatRelativeTime(session.LastUpdated)
		m.commandLine.AddToast(fmt.Sprintf("Resumed session from %s", timeStr), "success", 3000)
	}

	// Clear creatingTab
	m.creatingTab = ""
	return nil
}

// buildResumeChatMessages renders a slice of session ChatMessages into the
// []ChatMessage (Content+Type) form that the batched append API consumes. It
// reuses the same mapping as the old synchronous rebuildChatFromMessages.
func buildResumeChatMessages(allMessages []schemas.ChatMessage) []ChatMessage {
	// Build a map of tool call IDs to their responses for matching.
	toolResults := make(map[string]string)
	for _, msgContent := range allMessages {
		if msgContent.Role == schemas.ChatMessageRoleTool {
			if msgContent.ChatToolMessage != nil && msgContent.ChatToolMessage.ToolCallID != nil {
				content := ""
				if msgContent.Content != nil && msgContent.Content.ContentStr != nil {
					content = *msgContent.Content.ContentStr
				}
				toolResults[*msgContent.ChatToolMessage.ToolCallID] = content
			}
		}
	}

	var out []ChatMessage
	for _, msgContent := range allMessages {
		// Skip system messages
		if msgContent.Role == schemas.ChatMessageRoleSystem {
			continue
		}

		switch msgContent.Role {
		case schemas.ChatMessageRoleUser:
			if msgContent.Content != nil && msgContent.Content.ContentStr != nil {
				out = append(out, ChatMessage{Content: *msgContent.Content.ContentStr, Type: MessageTypeUser})
			}

		case schemas.ChatMessageRoleAssistant:
			// First collect reasoning content as a thinking chunk.
			if msgContent.ChatAssistantMessage != nil && msgContent.ChatAssistantMessage.Reasoning != nil {
				text := strings.TrimSpace(*msgContent.ChatAssistantMessage.Reasoning)
				if text != "" {
					out = append(out, ChatMessage{Content: text, Type: MessageTypeThinking})
				}
			}

			// Then collect all text content and add as a single AI message.
			var textContent strings.Builder
			if msgContent.Content != nil && msgContent.Content.ContentStr != nil {
				text := strings.TrimSpace(*msgContent.Content.ContentStr)
				if text != "" {
					textContent.WriteString(text)
				}
			}
			if textContent.Len() > 0 {
				out = append(out, ChatMessage{Content: textContent.String(), Type: MessageTypeAISuccess})
			}
			// Then add tool calls with their results.
			if msgContent.ChatAssistantMessage != nil {
				for _, tc := range msgContent.ChatAssistantMessage.ToolCalls {
					var result string
					var toolErr error
					tcID := ""
					if tc.ID != nil {
						tcID = *tc.ID
					}
					if resp, exists := toolResults[tcID]; exists {
						if strings.HasPrefix(resp, "Error:") || strings.HasPrefix(resp, "error:") {
							toolErr = fmt.Errorf("%s", resp)
						} else {
							result = resp
						}
					}
					args := tc.Function.Arguments
					name := ""
					if tc.Function.Name != nil {
						name = *tc.Function.Name
					}
					formatted := formatToolCallByName(name, checkPrefix, args, result, toolErr)
					out = append(out, ChatMessage{Content: formatted, Type: MessageTypeSystem})
				}
			}

		// Skip tool messages as they're already incorporated into tool call display
		case schemas.ChatMessageRoleTool:
			continue
		}
	}
	return out
}
