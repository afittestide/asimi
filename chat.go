package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/afittestide/asimi/internal/runners"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
)

// MessageType indicates the type/source of a chat message
type MessageType int

// FlushDirty forces a synchronous UpdateContent when content is dirty.
// Used by the TUI's chatRenderTickMsg handler and anywhere that needs an
// immediate render (resize, addMessage, finalize, etc.).
func (c *ChatComponent) FlushDirty() {
	if c.contentDirty {
		c.UpdateContent()
	}
}

const (
	MessageTypeSystem    MessageType = iota // System messages (tool calls, status, etc.)
	MessageTypeUser                         // User input
	MessageTypeAI                           // AI response (streaming, not finalized)
	MessageTypeAISuccess                    // AI response completed successfully
	MessageTypeAIFailure                    // AI response completed with failure
	MessageTypeThinking                     // AI thinking/reasoning content
	MessageTypeShell                        // Shell command input/output
	MessageTypeGreeting                     // Welcome/onboarding messages
)

// ChatMessage represents a single message with its indent level
type ChatMessage struct {
	Content string
	Indent  int
	Type    MessageType // Type of message for proper rendering
}

// ChatComponent represents the chat view
type ChatComponent struct {
	Viewport     viewport.Model
	Messages     []ChatMessage
	Width        int
	Height       int
	Style        lipgloss.Style
	AutoScroll   bool          // Track if auto-scrolling is enabled
	UserScrolled bool          // Track if user has manually scrolled
	ScrollLocked bool          // Prevent auto-scroll when user is in scroll mode
	GetStatus    func() string // Callback to get current status/mode from caller

	// Touch gesture support
	TouchStartY      int  // Y coordinate where touch/drag started
	TouchDragging    bool // Whether we're currently in a touch drag
	TouchScrollSpeed int  // Sensitivity for touch scrolling

	// Markdown rendering
	markdownRenderer *glamour.TermRenderer
	markdownEnabled  bool

	// Raw session history for debugging/inspection
	rawSessionHistory []string

	// Tool call tracking - maps tool call ID to chat message index
	toolCallMessageIndex map[string]int

	// Indentation for nested workflow output
	Indent     int
	blockLines [][]int

	// Debounced rendering: content mutations set contentDirty=true. The
	// TUI's chatRenderTickMsg handler flushes dirty chats via UpdateContent.
	contentDirty bool
}

const (
	userPrefix            = "👑  "
	approvalPrefix        = "🙋"
	asimiPrefix           = "🎏  "
	completeSuccessPrefix = "🐉  "
	completeFailurePrefix = "🦐  "
	failureToken          = "[[FAILURE]]"
	sealPrefix            = "🥂  "
	systemPrefix          = "🛠️  "
	checkPrefix           = "✓"
	treeFinalPrefix       = " ╰ "
	treeMidPrefix         = " │ "

	// Court court branding
	courtPrefix    = "🏯  "  // Court in session
	greetingPrefix = "🏯  "  // Greeting message block
	edictPrefix    = "📜  "  // Edict received
	ministerPrefix = "🔱  "  // Minister invoked
	ritualPrefix   = "⛩️  " // Ritual enacted
)

// ChatMsgBuilder builds multi-line messages with tree prefixes.
// It mimics strings.Builder but automatically adds treeMidPrefix to intermediate
// lines and treeFinalPrefix to the last line when String() is called.
type ChatMsgBuilder struct {
	prefix      string
	lines       []string
	currentLine strings.Builder
}

// NewChatMsgBuilder creates a new ChatMsgBuilder with the given prefix for the first line
func NewChatMsgBuilder(prefix string) *ChatMsgBuilder {
	return &ChatMsgBuilder{
		prefix: prefix,
		lines:  make([]string, 0),
	}
}

// WriteString appends text to the current line (without ending it)
func (b *ChatMsgBuilder) WriteString(s string) *ChatMsgBuilder {
	b.currentLine.WriteString(s)
	return b
}

// Writef appends formatted text to the current line (without ending it)
func (b *ChatMsgBuilder) Writef(format string, args ...interface{}) *ChatMsgBuilder {
	b.currentLine.WriteString(fmt.Sprintf(format, args...))
	return b
}

// WriteLn ends the current line and starts a new one.
// If called with arguments, appends them to the current line first.
func (b *ChatMsgBuilder) WriteLn(s ...string) *ChatMsgBuilder {
	for _, str := range s {
		b.currentLine.WriteString(str)
	}
	b.lines = append(b.lines, b.currentLine.String())
	b.currentLine.Reset()
	return b
}

// WriteLnf appends formatted text to the current line and ends it
func (b *ChatMsgBuilder) WriteLnf(format string, args ...interface{}) *ChatMsgBuilder {
	b.currentLine.WriteString(fmt.Sprintf(format, args...))
	b.lines = append(b.lines, b.currentLine.String())
	b.currentLine.Reset()
	return b
}

// String returns the formatted message with tree prefixes.
// The first line gets the configured prefix, intermediate lines get treeMidPrefix,
// and the last line gets treeFinalPrefix.
func (b *ChatMsgBuilder) String() string {
	// Include any pending content in currentLine
	lines := b.lines
	if b.currentLine.Len() > 0 {
		lines = append(lines, b.currentLine.String())
	}

	if len(lines) == 0 {
		return ""
	}

	if len(lines) == 1 {
		return b.prefix + lines[0]
	}

	var result strings.Builder
	result.WriteString(b.prefix)
	result.WriteString(lines[0])

	for i := 1; i < len(lines)-1; i++ {
		result.WriteString("\n")
		result.WriteString(treeMidPrefix)
		result.WriteString(lines[i])
	}

	result.WriteString("\n")
	result.WriteString(treeFinalPrefix)
	result.WriteString(lines[len(lines)-1])

	return result.String()
}

// cachedMarkdownRenderer is created once at first use and reused across all
// ChatComponent and HelpWindow instances. This prevents glamour.WithAutoStyle()
// from sending an OSC 11 background-color query every time a new tab is created.
var cachedMarkdownRenderer *glamour.TermRenderer

// getOrCreateGlamourRenderer returns the cached glamour renderer, creating it
// on first call. Shared by ChatComponent and HelpWindow so that NewHelpWindow
// never triggers a new OSC 11 query.
func getOrCreateGlamourRenderer() *glamour.TermRenderer {
	if cachedMarkdownRenderer != nil {
		return cachedMarkdownRenderer
	}
	rendererStart := time.Now()
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(0), // 0 disables glamour's word wrapping
	)
	slog.Debug("[TIMING] Markdown renderer initialized", "load time", time.Since(rendererStart), "err", err)
	if err == nil {
		cachedMarkdownRenderer = renderer
	}
	return renderer
}

// NewChatComponent creates a new chat component
func NewChatComponent(width, height int, markdownEnabled bool) *ChatComponent {
	return NewChatComponentWithStatus(width, height, markdownEnabled, func() string { return "insert" })
}

func NewChatComponentWithStatus(width, height int, markdownEnabled bool, getStatus func() string) *ChatComponent {
	// Viewport is 1 column narrower to leave room for the gutter
	vp := viewport.New(width-1, height)

	var renderer *glamour.TermRenderer
	if markdownEnabled {
		renderer = getOrCreateGlamourRenderer()
	}

	ret := &ChatComponent{
		Viewport:             vp,
		Width:                width,
		Height:               height,
		AutoScroll:           true,  // Enable auto-scroll by default
		UserScrolled:         false, // User hasn't scrolled yet
		GetStatus:            getStatus,
		TouchStartY:          0, // Initialize touch tracking
		TouchDragging:        false,
		TouchScrollSpeed:     3,        // Lines to scroll per touch movement unit
		markdownRenderer:     renderer, // Only set when markdown rendering is enabled
		markdownEnabled:      markdownEnabled,
		rawSessionHistory:    make([]string, 0),
		toolCallMessageIndex: make(map[string]int),
		Style: lipgloss.NewStyle().
			Width(width).
			Height(height),
	}

	// Initialize with the new session message
	ret.Clear()

	return ret
}

// Clear resets the chat component to its initial state without recreating the markdown renderer.
// This is much faster than creating a new ChatComponent when you just need to clear the chat.
func (c *ChatComponent) Clear() {
	c.Messages = []ChatMessage{}
	c.AutoScroll = true
	c.UserScrolled = false
	c.ScrollLocked = false
	c.TouchStartY = 0
	c.TouchDragging = false
	c.rawSessionHistory = make([]string, 0)
	c.toolCallMessageIndex = make(map[string]int)
	c.Viewport.GotoTop()
	c.UpdateContent()
}

// SetSize updates the width & height of the chat component
func (c *ChatComponent) SetSize(width, height int) {
	if height < 0 {
		height = 0
	}

	needsRender := c.Width != width || c.Height != height

	c.Width = width
	c.Style = c.Style.Width(width)
	// Viewport is 1 column narrower to leave room for the gutter
	c.Viewport.Width = width - 1
	c.Height = height
	c.Style = c.Style.Height(c.Height)
	c.Viewport.Height = c.Height

	if !needsRender {
		return
	}
	c.UpdateContent()
}

// StartBlock starts a block of messages with a height limit. use 0 for unlimited
func (c *ChatComponent) StartBlock(height int) {
	currentLine := len(c.Messages) - 1
	c.blockLines = append(c.blockLines, []int{currentLine, height})
}

// AddMessage adds a new message to the chat component
func (c *ChatComponent) AddMessage(message string) {
	c.Messages = append(c.Messages, ChatMessage{Content: message, Indent: c.Indent, Type: MessageTypeSystem})
	c.UpdateContent()
	// Reset auto-scroll when new message is added
	if !c.ScrollLocked {
		c.AutoScroll = true
		c.UserScrolled = false
	}
}

// AddAIChunk adds or appends to an AI response message (used during streaming).
// Sets contentDirty=true; the TUI's debounce tick flushes via UpdateContent.
func (c *ChatComponent) AddAIChunk(chunk string) {
	// Check if last message is an AI message we can append to
	if len(c.Messages) > 0 && c.Messages[len(c.Messages)-1].Type == MessageTypeAI {
		c.Messages[len(c.Messages)-1].Content += chunk
	} else {
		// Start a new AI message
		c.Messages = append(c.Messages, ChatMessage{
			Content: chunk,
			Indent:  c.Indent,
			Type:    MessageTypeAI,
		})
	}
	if !c.ScrollLocked {
		c.AutoScroll = true
		c.UserScrolled = false
	}
	c.contentDirty = true
}

// AddUserMessage adds a user message to the chat component
func (c *ChatComponent) AddUserMessage(text string) {
	c.Messages = append(c.Messages, ChatMessage{
		Content: text,
		Indent:  c.Indent,
		Type:    MessageTypeUser,
	})
	c.UpdateContent()
	if !c.ScrollLocked {
		c.AutoScroll = true
		c.UserScrolled = false
	}
}

// ===== Buffered (dirty-flag) Append API =====
//
// These batched methods are used ONLY by the bulk rebuild/resume path. Unlike
// AddMessage/AddUserMessage they do NOT force an immediate full re-render;
// instead they set contentDirty=true and let the TUI's debounce render tick
// (chatRenderTickMsg) flush them, keeping the loop responsive.

// AppendStringMessage appends a system message without re-rendering, marking
// the content dirty for the debounced flush.
func (c *ChatComponent) AppendStringMessage(content string) {
	c.Messages = append(c.Messages, ChatMessage{Content: content, Indent: c.Indent, Type: MessageTypeSystem})
	if !c.ScrollLocked {
		c.AutoScroll = true
		c.UserScrolled = false
	}
	c.contentDirty = true
}

// AppendUserMessage appends a user message without re-rendering, marking the
// content dirty for the debounced flush.
func (c *ChatComponent) AppendUserMessage(text string) {
	c.Messages = append(c.Messages, ChatMessage{Content: text, Indent: c.Indent, Type: MessageTypeUser})
	if !c.ScrollLocked {
		c.AutoScroll = true
		c.UserScrolled = false
	}
	c.contentDirty = true
}

// AppendToolCallMessage appends a formatted tool-call line (like AddMessage)
// but without re-rendering, marking the content dirty for the debounced flush.
func (c *ChatComponent) AppendToolCallMessage(formatted string) {
	c.Messages = append(c.Messages, ChatMessage{Content: formatted, Indent: c.Indent, Type: MessageTypeSystem})
	if !c.ScrollLocked {
		c.AutoScroll = true
		c.UserScrolled = false
	}
	c.contentDirty = true
}

// AppendBatch appends a prepared slice of messages in one go, setting
// contentDirty=true once so the debounced render tick flushes them together.
// Used only by the bulk resume rebuild path.
func (c *ChatComponent) AppendBatch(msgs []ChatMessage) {
	for _, m := range msgs {
		c.Messages = append(c.Messages, ChatMessage{Content: m.Content, Indent: c.Indent, Type: m.Type})
	}
	if !c.ScrollLocked {
		c.AutoScroll = true
		c.UserScrolled = false
	}
	c.contentDirty = true
}

// AddThinkingChunk adds or appends to a thinking/reasoning message (used during streaming).
// Sets contentDirty=true; the TUI's debounce tick flushes via UpdateContent.
func (c *ChatComponent) AddThinkingChunk(chunk string) {
	if strings.TrimSpace(chunk) == "" {
		return // Skip empty thinking chunks
	}
	// Check if last message is a thinking message we can append to
	if len(c.Messages) > 0 && c.Messages[len(c.Messages)-1].Type == MessageTypeThinking {
		c.Messages[len(c.Messages)-1].Content += chunk
	} else {
		// Start a new thinking message
		c.Messages = append(c.Messages, ChatMessage{
			Content: chunk,
			Indent:  c.Indent,
			Type:    MessageTypeThinking,
		})
	}
	if !c.ScrollLocked {
		c.AutoScroll = true
		c.UserScrolled = false
	}
	c.contentDirty = true
}

// SetScrollLock toggles scroll locking (prevents auto-scroll when true)
func (c *ChatComponent) SetScrollLock(lock bool) {
	c.ScrollLocked = lock
	if lock {
		c.AutoScroll = false
		c.UserScrolled = true
		return
	}
	if c.Viewport.AtBottom() {
		c.AutoScroll = true
		c.UserScrolled = false
	}
}

// IsScrollLocked returns true if the chat is currently scroll-locked
func (c *ChatComponent) IsScrollLocked() bool {
	return c.ScrollLocked
}

// ScrollHalfPageUp scrolls the viewport up by half a page
func (c *ChatComponent) ScrollHalfPageUp() {
	c.Viewport.HalfPageUp()
	c.UserScrolled = true
}

// ScrollHalfPageDown scrolls the viewport down by half a page
func (c *ChatComponent) ScrollHalfPageDown() {
	c.Viewport.HalfPageDown()
	if c.Viewport.AtBottom() {
		c.UserScrolled = false
		if !c.ScrollLocked {
			c.AutoScroll = true
		}
	} else {
		c.UserScrolled = true
	}
}

// ScrollPageUp scrolls the viewport up by a full page
func (c *ChatComponent) ScrollPageUp() {
	c.Viewport.PageUp()
	c.UserScrolled = true
}

// ScrollPageDown scrolls the viewport down by a full page
func (c *ChatComponent) ScrollPageDown() {
	c.Viewport.PageDown()
	if c.Viewport.AtBottom() {
		c.UserScrolled = false
		if !c.ScrollLocked {
			c.AutoScroll = true
		}
	} else {
		c.UserScrolled = true
	}
}

// ScrollToTop scrolls to the beginning of the chat history
func (c *ChatComponent) ScrollToTop() {
	c.Viewport.GotoTop()
	c.UserScrolled = true
}

// ScrollToBottom scrolls to the latest message
func (c *ChatComponent) ScrollToBottom() {
	c.Viewport.GotoBottom()
	c.UserScrolled = false
	// Always enable autoscroll when user explicitly goes to bottom (e.g., pressing 'G')
	// This is an intentional action meaning "follow new content"
	c.AutoScroll = true
}

// ScrollUpOneLine scrolls up by one line
func (c *ChatComponent) ScrollUpOneLine() {
	c.Viewport.ScrollUp(1)
	c.UserScrolled = true
}

// ScrollDownOneLine scrolls down by one line
func (c *ChatComponent) ScrollDownOneLine() {
	c.Viewport.ScrollDown(1)
	if c.Viewport.AtBottom() {
		c.UserScrolled = false
		if !c.ScrollLocked {
			c.AutoScroll = true
		}
	} else {
		c.UserScrolled = true
	}
}

// AddShellCommandInput adds the entered shell command at column 0
func (c *ChatComponent) AddShellCommandInput(command string) {
	c.Messages = append(c.Messages, ChatMessage{
		Content: command,
		Indent:  c.Indent,
		Type:    MessageTypeShell,
	})
	c.UpdateContent()
	/*
		if !c.ScrollLocked {
			c.AutoScroll = true
			c.UserScrolled = false
		}
	*/
}

// AddShellCommandResult formats and displays the result of an inline shell command
func (c *ChatComponent) AddShellCommandResult(msg shellCommandResultMsg) {
	c.AddToRawHistory("SHELL_RESULT", fmt.Sprintf("Command: %s\nExit Code: %s\nOutput: %s\n",
		msg.command, msg.exitCode, msg.output))

	if msg.err != nil {
		c.AddMessage(renderShellLines([]string{fmt.Sprintf("bash: Error executing command: %v", msg.err)}))
		return
	}

	var lines []string

	if msg.output != "" {
		lines = append(lines, splitShellLines(msg.output)...)
	}

	if len(lines) == 0 {
		lines = append(lines, fmt.Sprintf("Command `%s` completed with no output (exit code: %s)",
			msg.command, msg.exitCode))
	} else if msg.exitCode != "0" {
		lines = append(lines, fmt.Sprintf("(exit code: %s)", msg.exitCode))
	}

	c.AddMessage(renderShellLines(lines))
}

func splitShellLines(text string) []string {
	if text == "" {
		return nil
	}

	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func renderShellLines(lines []string) string {
	if len(lines) == 0 {
		return treeFinalPrefix + "\n"
	}

	var builder strings.Builder
	for i, line := range lines {
		prefix := treeMidPrefix
		if i == len(lines)-1 {
			prefix = treeFinalPrefix
		}
		builder.WriteString(prefix)
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return builder.String()
}

// TruncateTo keeps only the first count messages and refreshes the viewport
func (c *ChatComponent) TruncateTo(count int) {
	if count < 0 {
		count = 0
	}
	if count > len(c.Messages) {
		count = len(c.Messages)
	}
	c.Messages = append([]ChatMessage(nil), c.Messages[:count]...)
	c.UpdateContent()
}

// AppendToLastMessage appends text to the last message (for streaming)
func (c *ChatComponent) AppendToLastMessage(text string) {
	if len(c.Messages) == 0 {
		c.AddMessage(text)
		return
	}
	c.Messages[len(c.Messages)-1].Content += text
	c.UpdateContent()
}

// FinalizeLastAIMessage marks the last AI message as complete, checking for failure token.
// If the message contains [[FAILURE]], it's marked as a failure response.
// Returns true if the message was a failure, false otherwise.
func (c *ChatComponent) FinalizeLastAIMessage() bool {
	if len(c.Messages) == 0 {
		return false
	}

	lastMsg := &c.Messages[len(c.Messages)-1]
	if lastMsg.Type != MessageTypeAI {
		return false
	}

	isFailure := strings.HasPrefix(lastMsg.Content, failureToken)
	if isFailure {
		lastMsg.Content = strings.TrimPrefix(lastMsg.Content, failureToken)
		lastMsg.Content = strings.TrimSpace(lastMsg.Content)
		lastMsg.Type = MessageTypeAIFailure
	} else {
		lastMsg.Type = MessageTypeAISuccess
	}
	c.UpdateContent()
	return isFailure
}

// UpdateContent updates the viewport content based on the messages.
// Clears contentDirty — this is the work the flag tracks.
func (c *ChatComponent) UpdateContent() {
	c.contentDirty = false
	var messageViews []string
	for msgIdx, msg := range c.Messages {
		var rendered string
		message := msg.Content

		// Route rendering based on message type
		switch msg.Type {
		case MessageTypeShell:
			// Shell command input styling
			messageStyle := lipgloss.NewStyle().
				Foreground(globalTheme.PromptBorder)
			rendered = messageStyle.Render(fmt.Sprintf("$ %s", message))

		case MessageTypeThinking:
			// Thinking/reasoning content styling
			thinkingStyle := lipgloss.NewStyle().
				Foreground(globalTheme.ThinkingColor)
			builder := NewChatMsgBuilder("💭   ")
			// Word wrap and split into lines (c.Width-1 for gutter, -6 for prefix)
			wrapped := wordwrap.String(message, c.Width-7)
			lines := strings.Split(wrapped, "\n")
			for i, line := range lines {
				if i < len(lines)-1 {
					builder.WriteLn(line)
				} else {
					builder.WriteString(line)
				}
			}
			rendered = thinkingStyle.Render(builder.String())

		case MessageTypeUser:
			// User message styling
			messageStyle := lipgloss.NewStyle().
				Foreground(globalTheme.PromptBorder)

			wrapWidth := c.Width - 1 // -1 for gutter
			const indentSpaces = 0
			if wrapWidth > indentSpaces {
				wrapWidth -= indentSpaces
			}
			if wrapWidth < 1 {
				wrapWidth = 1
			}

			wrapped := wordwrap.String(message, wrapWidth)
			userIndent := strings.Repeat(" ", indentSpaces)
			lines := strings.Split(wrapped, "\n")
			for i := range lines {
				lines[i] = userIndent + lines[i]
			}

			rendered = messageStyle.Render(fmt.Sprintf("%s %s", userPrefix, strings.Join(lines, "\n")))

		case MessageTypeAI, MessageTypeAISuccess, MessageTypeAIFailure:
			// Render AI messages with markdown using Type field
			var prefix string
			switch msg.Type {
			case MessageTypeAISuccess:
				prefix = lipgloss.NewStyle().Bold(true).Render(completeSuccessPrefix)
			case MessageTypeAIFailure:
				prefix = lipgloss.NewStyle().Bold(true).Render(completeFailurePrefix)
			default: // MessageTypeAI (streaming, not finalized)
				prefix = lipgloss.NewStyle().Bold(true).Render(asimiPrefix)
			}
			rendered = prefix + c.renderMarkdown(message)

		case MessageTypeGreeting:
			builder := NewChatMsgBuilder(greetingPrefix + " ")
			// Word wrap and split into lines (c.Width-1 for gutter, -6 for prefix)
			wrapped := wordwrap.String(message, c.Width-7)
			lines := strings.Split(wrapped, "\n")
			for i, line := range lines {
				if i < len(lines)-1 {
					builder.WriteLn(line)
				} else {
					builder.WriteString(line)
				}
			}
			rendered = c.renderMarkdown(builder.String())

		default:
			// Other messages (system, tool calls, etc.)
			messageStyle := lipgloss.NewStyle().
				Foreground(globalTheme.TextColor)
			rendered = messageStyle.Render(wordwrap.String(message, c.Width-1)) // -1 for gutter
		}

		// Apply indentation if needed
		if msg.Indent > 0 {
			// Check if this is the last message at this indent level
			isLastAtIndent := msgIdx == len(c.Messages)-1 || c.Messages[msgIdx+1].Indent < msg.Indent

			indent := strings.Repeat(treeMidPrefix, msg.Indent)
			lines := strings.Split(rendered, "\n")

			// Filter out empty lines to reduce blank line clutter
			var nonEmptyLines []string
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					nonEmptyLines = append(nonEmptyLines, line)
				}
			}
			lines = nonEmptyLines

			for lineIdx, line := range lines {
				if line != "" {
					// Use final prefix for the last line if this is the last message at this indent
					if isLastAtIndent && lineIdx == len(lines)-1 {
						// Replace the last treeMidPrefix with treeFinalPrefix
						finalIndent := strings.Repeat(treeMidPrefix, msg.Indent-1) + treeFinalPrefix
						lines[lineIdx] = finalIndent + line
					} else {
						lines[lineIdx] = indent + line
					}
				}
			}
			rendered = strings.Join(lines, "\n")
		}

		messageViews = append(messageViews, rendered)
	}
	content := lipgloss.JoinVertical(lipgloss.Left, messageViews...)
	c.Viewport.SetContent(content)

	// Only auto-scroll if user hasn't manually scrolled
	if c.AutoScroll && !c.UserScrolled {
		c.Viewport.GotoBottom()
	}
}

// renderMarkdown renders markdown content with glamour
func (c *ChatComponent) renderMarkdown(content string) string {
	if !c.markdownEnabled || c.markdownRenderer == nil {
		return c.renderPlainText(content)
	}

	rendered, err := c.markdownRenderer.Render(content)
	if err != nil {
		// Fallback to plain text on error
		return c.renderPlainText(content)
	}

	// Apply word wrapping to the rendered output.
	// Glamour is configured with WordWrap(0) to disable its internal wrapping,
	// so we wrap here using the current viewport width.
	// wordwrap.String() preserves ANSI escape sequences, allowing proper
	// re-wrapping on terminal resize without recreating the renderer.
	// c.Width-1 for gutter, -2 for padding
	wrapped := wordwrap.String(rendered, c.Width-3)

	return strings.TrimSpace(wrapped)
}

func (c *ChatComponent) renderPlainText(content string) string {
	width := c.Width - 3 // -1 for gutter, -2 for padding
	if width < 1 {
		width = 1
	}
	return strings.TrimSpace(wordwrap.String(content, width))
}

// Update handles messages for the chat component
func (c ChatComponent) Update(msg tea.Msg) (ChatComponent, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.MouseMsg:
		switch msg.Type {
		case tea.MouseWheelUp:
			c.Viewport.ScrollUp(1)
			c.UserScrolled = true // User manually scrolled
		case tea.MouseWheelDown:
			c.Viewport.ScrollDown(1)
			// Check if we're at the bottom after scrolling down
			if c.Viewport.AtBottom() {
				c.UserScrolled = false
			} else {
				c.UserScrolled = true
			}
		case tea.MouseLeft:
			// Start of touch/drag gesture
			if msg.Action == tea.MouseActionPress {
				c.TouchStartY = msg.Y
				c.TouchDragging = true
			} else if msg.Action == tea.MouseActionRelease {
				c.TouchDragging = false
			}
		case tea.MouseMotion:
			// Handle touch drag scrolling
			if c.TouchDragging {
				deltaY := c.TouchStartY - msg.Y
				if deltaY != 0 {
					// Calculate scroll amount based on delta
					scrollLines := deltaY / c.TouchScrollSpeed
					if scrollLines > 0 {
						// Scroll down
						for i := 0; i < scrollLines; i++ {
							c.Viewport.ScrollDown(1)
						}
						// Check if we're at the bottom after scrolling down
						if c.Viewport.AtBottom() {
							c.UserScrolled = false
						} else {
							c.UserScrolled = true
						}
					} else if scrollLines < 0 {
						// Scroll up
						for i := 0; i < -scrollLines; i++ {
							c.Viewport.ScrollUp(1)
						}
						c.UserScrolled = true
					}
					// Update start position for next motion event
					c.TouchStartY = msg.Y
				}
			}
		}
	case tea.KeyMsg:
		// Track keyboard scrolling as well
		switch msg.String() {
		case "up", "k":
			c.Viewport.ScrollUp(1)
			c.UserScrolled = true
		case "down", "j":
			c.Viewport.ScrollDown(1)
			// Check if we're at the bottom after scrolling down
			if c.Viewport.AtBottom() {
				c.UserScrolled = false
			} else {
				c.UserScrolled = true
			}
		case "pgup":
			c.Viewport.HalfPageUp()
			c.UserScrolled = true
		case "pgdown":
			c.Viewport.HalfPageDown()
			// Check if we're at the bottom after page down
			if c.Viewport.AtBottom() {
				c.UserScrolled = false
			} else {
				c.UserScrolled = true
			}
		case "home":
			c.Viewport.GotoTop()
			c.UserScrolled = true
		case "end":
			c.Viewport.GotoBottom()
			// Always enable auto-scroll when user explicitly goes to bottom
			c.UserScrolled = false
		}
	}
	if c.Viewport.AtBottom() && !c.ScrollLocked {
		c.AutoScroll = true
	}
	c.Viewport, cmd = c.Viewport.Update(msg)
	return c, cmd
}

// View renders the chat component
func (c ChatComponent) View() string {
	// Get the viewport content
	viewportContent := c.Viewport.View()

	// Adjust height
	c.Style = c.Style.Height(c.Height)
	c.Viewport.Height = c.Height

	// Add gutter column
	lines := strings.Split(viewportContent, "\n")
	hasScrollback := c.Viewport.YOffset > 0
	hasMoreBelow := !c.Viewport.AtBottom()

	gutterStyle := lipgloss.NewStyle().Foreground(globalTheme.Warning)

	for i := range lines {
		var gutterChar string
		if i == 0 && hasScrollback {
			gutterChar = "⇡"
		} else if i == len(lines)-1 && hasMoreBelow {
			gutterChar = "⇣"
		} else {
			gutterChar = " "
		}
		lines[i] = gutterStyle.Render(gutterChar) + lines[i]
	}

	contentWithGutter := strings.Join(lines, "\n")

	return c.Style.Render(contentWithGutter)
}

// ===== Raw History Management =====

// AddToRawHistory adds an entry to the raw session history with a timestamp
func (c *ChatComponent) AddToRawHistory(prefix, content string) {
	timestamp := time.Now().Format("15:04:05")
	entry := fmt.Sprintf("[%s] %s: %s", timestamp, prefix, content)
	c.rawSessionHistory = append(c.rawSessionHistory, entry)
}

// GetRawHistory returns the raw session history
func (c *ChatComponent) GetRawHistory() []string {
	return c.rawSessionHistory
}

// ClearRawHistory clears the raw session history
func (c *ChatComponent) ClearRawHistory() {
	c.rawSessionHistory = make([]string, 0)
}

// ===== Tool Call Tracking =====

// SetToolCallMessageIndex stores the message index for a tool call ID
func (c *ChatComponent) SetToolCallMessageIndex(toolCallID string, messageIndex int) {
	c.toolCallMessageIndex[toolCallID] = messageIndex
}

// GetToolCallMessageIndex retrieves the message index for a tool call ID
func (c *ChatComponent) GetToolCallMessageIndex(toolCallID string) (int, bool) {
	idx, exists := c.toolCallMessageIndex[toolCallID]
	return idx, exists
}

// DeleteToolCallMessageIndex removes the message index mapping for a tool call ID
func (c *ChatComponent) DeleteToolCallMessageIndex(toolCallID string) {
	delete(c.toolCallMessageIndex, toolCallID)
}

// ClearToolCallMessageIndex clears all tool call message index mappings
func (c *ChatComponent) ClearToolCallMessageIndex() {
	c.toolCallMessageIndex = make(map[string]int)
}

// ===== Tool Call Message Handling =====

// HandleToolCallScheduled handles a scheduled tool call message
func (c *ChatComponent) HandleToolCallScheduled(msg runners.ToolCallScheduledMsg) {
	c.AppendToolCallMessage("📋 " + msg.Formatted)
	c.SetToolCallMessageIndex(msg.CallID, len(c.Messages)-1)
}

// HandleToolCallExecuting handles an executing tool call message
func (c *ChatComponent) HandleToolCallExecuting(msg runners.ToolCallExecutingMsg) {
	formatted := "⚙️ " + msg.Formatted
	// Update the existing message if we have its index
	if idx, exists := c.GetToolCallMessageIndex(msg.CallID); exists && idx < len(c.Messages) {
		c.Messages[idx].Content = formatted
		c.contentDirty = true
	} else {
		// Fallback: add a new message if we don't have the index
		c.AppendToolCallMessage(formatted)
	}
}

// HandleToolCallSuccess handles a successful tool call message
func (c *ChatComponent) HandleToolCallSuccess(msg runners.ToolCallSuccessMsg) {
	formatted := checkPrefix + " " + msg.Formatted
	// Update the existing message if we have its index
	if idx, exists := c.GetToolCallMessageIndex(msg.CallID); exists && idx < len(c.Messages) {
		c.Messages[idx].Content = formatted
		c.contentDirty = true
		// Clean up the index mapping
		c.DeleteToolCallMessageIndex(msg.CallID)
	} else {
		// Fallback: add a new message if we don't have the index
		c.AppendToolCallMessage(formatted)
	}
}

// HandleToolCallError handles a failed tool call message
func (c *ChatComponent) HandleToolCallError(msg runners.ToolCallErrorMsg) {
	icon := "⁉️"
	if strings.Contains(msg.Error, "command denied by user") {
		icon = "⛔︎"
	}
	formatted := icon + " " + msg.Formatted
	// Update the existing message if we have its index
	if idx, exists := c.GetToolCallMessageIndex(msg.CallID); exists && idx < len(c.Messages) {
		c.Messages[idx].Content = formatted
		c.contentDirty = true
		// Clean up the index mapping
		c.DeleteToolCallMessageIndex(msg.CallID)
	} else {
		// Fallback: add a new message if we don't have the index
		c.AppendToolCallMessage(formatted)
	}
}

// HandleToolCallAborted handles an aborted tool call message (e.g., due to sandbox restart)
func (c *ChatComponent) HandleToolCallAborted(msg runners.ToolCallAbortedMsg) {
	formatted := "🚫 " + msg.Formatted
	// Update the existing message if we have its index
	if idx, exists := c.GetToolCallMessageIndex(msg.CallID); exists && idx < len(c.Messages) {
		c.Messages[idx].Content = formatted
		c.contentDirty = true
		// Clean up the index mapping
		c.DeleteToolCallMessageIndex(msg.CallID)
	} else {
		// Fallback: add a new message if we don't have the index
		c.AppendToolCallMessage(formatted)
	}
}
func (c *ChatComponent) AddMarkdownMessage(message string) {
	c.AddMessage(c.renderMarkdown(message))
}

// AddGreetingMessage adds a greeting/onboarding message rendered as a
// markdown block with the 🏯 prefix, matching AI output rendering style.
func (c *ChatComponent) AddGreetingMessage(message string) {
	c.Messages = append(c.Messages, ChatMessage{Content: message, Indent: 0, Type: MessageTypeGreeting})
	c.UpdateContent()
	if !c.ScrollLocked {
		c.AutoScroll = true
		c.UserScrolled = false
	}
}

// UpdateLastToolCallEmoji finds the last tool call message containing the given command
// and updates its emoji. Returns true if a message was found and updated.
func (c *ChatComponent) UpdateLastToolCallEmoji(command string, newEmoji string) bool {
	// Search from the end of messages to find the most recent matching tool call
	searchPattern := "$ " + command
	for i := len(c.Messages) - 1; i >= 0; i-- {
		msg := c.Messages[i].Content
		// Check if this message contains the command (tool call messages have "$ <command>")
		if strings.Contains(msg, searchPattern) {
			// Update the emoji at the start of the message
			// Tool call messages start with an emoji followed by space
			if len(msg) > 0 {
				// Find the first space to locate the emoji
				spaceIdx := strings.Index(msg, " ")
				if spaceIdx > 0 {
					// Replace the emoji (everything before the first space)
					c.Messages[i].Content = newEmoji + msg[spaceIdx:]
					c.UpdateContent()
					return true
				}
			}
		}
	}
	return false
}

// formatToolCallByName formats a tool call when only the tool name is available (e.g., resume)
func formatToolCallByName(toolName, icon string, input, result string, err error) string {
	display := result
	if err != nil {
		display = err.Error()
	}
	return fmt.Sprintf("%s %s: %s", icon, toolName, truncateSnippet(display, 120))
}
