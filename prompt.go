package main

import (
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/afittestide/asimi/court"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
)

// Vi mode constants
const (
	ViModeInsert      = "insert"
	ViModeNormal      = "normal"
	ViModeVisual      = "visual"
	ViModeScroll      = "scroll"
	ViModeCommandLine = "command"
	ViModeLearning    = "learning"
	ViModeAnswering   = "answering"
)

// Placeholder text constants
const (
	PlaceholderDefault = "Type your message here. Enter to send, ESC to change mode"
)

// PromptComponent represents the user input text area
type PromptComponent struct {
	TextArea         textarea.Model
	Placeholder      string
	Height           int
	Width            int
	MaxHeight        int // Maximum height (50% of screen height)
	ScreenHeight     int // Total screen height
	ExpandedHeight   int // Height to grow to when multiline (default: 10)
	Style            lipgloss.Style
	ViCurrentMode    string // Current vi mode: insert, normal, or command
	viPendingOp      string // Track pending operation (e.g., "d" or "c")
	viNormalKeyMap   textarea.KeyMap
	viInsertKeyMap   textarea.KeyMap
	answering        *AnsweringState   // currently rendered answering state
	pendingAnswering []*AnsweringState // Ruler requests waiting to be rendered
	// pendingMenu holds at most one local UI affordance (the edict action
	// menu) that is not on screen. It lives in its own slot, never in the
	// Ruler-request queue: a menu is not a decision awaiting an answer, so it
	// must not be counted, listed, or selected as one — and it must not be able
	// to strand the request set. At most one is kept, so reopening the menu
	// refreshes it instead of stacking duplicates.
	pendingMenu *AnsweringState
}

// SubmitPromptMsg is a message sent when the user submits a prompt
type SubmitPromptMsg struct {
	Prompt string
}

// AnsweredMsg is emitted when the user finishes answering all questions
type AnsweredMsg struct {
	RequestID string
	Answers   []string // option text or free-text per question
}

// AnsweringCancelMsg is emitted when the user cancels answering
type AnsweringCancelMsg struct {
	RequestID string
}

// AnsweringChatMsg is emitted when the user selects "Chat" in answering mode.
// It returns control to the chat input; the next submitted message is then
// delivered as the zhengming answer rather than opening a new turn.
type AnsweringChatMsg struct {
	RequestID string
}

// AnsweringEditMsg is emitted when the user selects "Edit" to edit a question in $EDITOR
type AnsweringEditMsg struct {
	RequestID string
	Question  string // The question text to edit
}

// AnsweringEditDoneMsg is emitted after editing is complete, with the (possibly modified) question text
type AnsweringEditDoneMsg struct {
	RequestID        string
	Question         string // The edited question text (may be modified or original if no changes)
	OriginalQuestion string // The original question text for comparison
}

// AnsweringState holds state for the answering mode UI
type AnsweringState struct {
	RequestID string
	Title     string
	Questions []AnsweringQuestion
	Answers   []string // collected answers
	Current   int      // index of current question being answered
	// NotARequest marks a local answering state that is not a Court
	// zhengming request (the edict action menu): it is a UI affordance, not a
	// decision awaiting the Ruler's answer. Such states are shown but never
	// counted or listed as open Ruler requests, and answering them is not an
	// answer to any request.
	NotARequest bool
}

// AnsweringQuestion holds a single question with selectable options
type AnsweringQuestion struct {
	Text     string
	Summary  string   // Short display text; Text is used if empty
	Options  []string // 2-4 options from LLM; "Other..." appended automatically
	Selected int      // cursor position (includes "Other..." entry)
}

// NewPromptComponent creates a new prompt component
func NewPromptComponent(width, height int) PromptComponent {
	ta := textarea.New()
	ta.Placeholder = PlaceholderDefault
	ta.ShowLineNumbers = false

	// Remove background color from textarea styles
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()

	ta.Focus()

	// Set the dimensions
	ta.SetWidth(width - 2) // Account for borders
	ta.SetHeight(height)   // Account for borders

	// Create a vi normal mode keymap (navigation only, no text input)
	viNormalKeyMap := textarea.KeyMap{
		CharacterBackward:          key.NewBinding(key.WithKeys("h", "left")),
		CharacterForward:           key.NewBinding(key.WithKeys("l", "right")),
		DeleteAfterCursor:          key.NewBinding(key.WithKeys("D")),
		DeleteBeforeCursor:         key.NewBinding(key.WithKeys("d0")),
		DeleteCharacterBackward:    key.NewBinding(key.WithKeys("X")),
		DeleteCharacterForward:     key.NewBinding(key.WithKeys("x")),
		DeleteWordBackward:         key.NewBinding(key.WithKeys("db")),
		DeleteWordForward:          key.NewBinding(key.WithKeys("dw")),
		InsertNewline:              key.NewBinding(key.WithKeys()), // Disabled in normal mode
		LineEnd:                    key.NewBinding(key.WithKeys("$", "end")),
		LineStart:                  key.NewBinding(key.WithKeys("0", "^", "home")),
		LineNext:                   key.NewBinding(key.WithKeys("j", "down")),
		LinePrevious:               key.NewBinding(key.WithKeys("k", "up")),
		Paste:                      key.NewBinding(key.WithKeys("p")),
		WordBackward:               key.NewBinding(key.WithKeys("b")),
		WordForward:                key.NewBinding(key.WithKeys("w")),
		InputBegin:                 key.NewBinding(key.WithKeys("gg")),
		InputEnd:                   key.NewBinding(key.WithKeys("G")),
		UppercaseWordForward:       key.NewBinding(key.WithKeys()), // Disabled
		LowercaseWordForward:       key.NewBinding(key.WithKeys()), // Disabled
		CapitalizeWordForward:      key.NewBinding(key.WithKeys()), // Disabled
		TransposeCharacterBackward: key.NewBinding(key.WithKeys()), // Disabled
	}

	// Create a vi insert mode keymap (similar to normal editing)
	viInsertKeyMap := textarea.KeyMap{
		CharacterBackward:          key.NewBinding(key.WithKeys("left")),
		CharacterForward:           key.NewBinding(key.WithKeys("right")),
		DeleteAfterCursor:          key.NewBinding(key.WithKeys("ctrl+k")),
		DeleteBeforeCursor:         key.NewBinding(key.WithKeys("ctrl+u")),
		DeleteCharacterBackward:    key.NewBinding(key.WithKeys("backspace", "ctrl+h")),
		DeleteCharacterForward:     key.NewBinding(key.WithKeys("delete")),
		DeleteWordBackward:         key.NewBinding(key.WithKeys("ctrl+w")),
		DeleteWordForward:          key.NewBinding(key.WithKeys("alt+d")),
		InsertNewline:              key.NewBinding(key.WithKeys("enter", "ctrl+m")),
		LineEnd:                    key.NewBinding(key.WithKeys("end")),
		LineStart:                  key.NewBinding(key.WithKeys("home")),
		LineNext:                   key.NewBinding(key.WithKeys("down")),
		LinePrevious:               key.NewBinding(key.WithKeys("up")),
		Paste:                      key.NewBinding(key.WithKeys("ctrl+v")),
		WordBackward:               key.NewBinding(key.WithKeys("alt+left")),
		WordForward:                key.NewBinding(key.WithKeys("alt+right")),
		InputBegin:                 key.NewBinding(key.WithKeys("ctrl+home")),
		InputEnd:                   key.NewBinding(key.WithKeys("ctrl+end")),
		UppercaseWordForward:       key.NewBinding(key.WithKeys("ctrl+alt+u")),
		LowercaseWordForward:       key.NewBinding(key.WithKeys("ctrl+alt+l")),
		CapitalizeWordForward:      key.NewBinding(key.WithKeys("ctrl+alt+c")),
		TransposeCharacterBackward: key.NewBinding(key.WithKeys("ctrl+t")),
	}

	// Ensure globalTheme is initialized (for tests)
	if globalTheme == nil {
		globalTheme = NewTheme()
	}

	prompt := PromptComponent{
		TextArea:       ta,
		Height:         height,
		Width:          width,
		ExpandedHeight: 10,           // Default expanded height, can be overridden by config
		ViCurrentMode:  ViModeInsert, // Start in insert mode
		viPendingOp:    "",           // No pending operation
		viNormalKeyMap: viNormalKeyMap,
		viInsertKeyMap: viInsertKeyMap,
		Style: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(globalTheme.PromptOnBorder). // Use theme's on border for insert mode
			Width(width).
			Height(height),
	}

	// Set insert mode keymap
	prompt.TextArea.KeyMap = viInsertKeyMap

	return prompt
}

// SetWidth updates the width of the prompt component
func (p *PromptComponent) SetWidth(width int) {
	p.Width = width
	p.Style = p.Style.Width(width)
	p.TextArea.SetWidth(width - 2)
}

// SetHeight updates the height of the prompt component
// Height is constrained to MaxHeight (50% of screen)
func (p *PromptComponent) SetHeight(height int) {
	// Apply max height constraint if set
	if p.MaxHeight > 0 && height > p.MaxHeight {
		height = p.MaxHeight
	}
	p.Height = height
	p.Style = p.Style.Height(height)
	p.TextArea.SetHeight(height)
}

// SetScreenHeight updates the screen height and recalculates max height
func (p *PromptComponent) SetScreenHeight(screenHeight int) {
	p.ScreenHeight = screenHeight
	// Max height is 50% of screen height
	p.MaxHeight = screenHeight / 2
	// Re-apply height constraint
	if p.Height > p.MaxHeight {
		p.SetHeight(p.Height)
	}
}

// SetExpandedHeight sets the height the prompt grows to when content is multiline
func (p *PromptComponent) SetExpandedHeight(height int) {
	p.ExpandedHeight = height
}

// countVisualLines returns the number of visual lines a (possibly multi-line)
// string occupies when rendered. Each \n-separated line counts as one row
// minimum. Empty strings count as one line.
func countVisualLines(s string) int {
	if s == "" {
		return 1
	}
	return strings.Count(s, "\n") + 1
}

// CalculateDesiredHeight returns the desired height based on content
// When user input is more than one line (including wrapped lines), grows to ExpandedHeight lines (if screen allows)
// Goes back to 2-line height when in scroll mode or when prompt is cleared
func (p *PromptComponent) CalculateDesiredHeight() int {
	// Scroll mode always minimizes to 2 lines
	if p.ViCurrentMode == ViModeScroll {
		return 2
	}

	// In answering mode, size for: title + question + options (accounting for word-wrap)
	if p.answering != nil && p.answering.Current < len(p.answering.Questions) {
		q := p.answering.Questions[p.answering.Current]

		// Content width inside the border
		contentWidth := p.Width - 2
		if contentWidth <= 0 {
			contentWidth = 1
		}
		// Options are indented by 4 chars ("▶ " or "    ")
		optionWidth := contentWidth - 4
		if optionWidth <= 0 {
			optionWidth = 1
		}

		// 1 title + 1 blank + question visual lines + sum of option visual lines
		h := 2

		// Count visual lines for question text (prefer summary)
		displayText := q.Text
		if q.Summary != "" {
			displayText = q.Summary
		}
		h += countVisualLines(wordwrap.String(displayText, contentWidth))

		// Count visual lines for each option
		for _, opt := range q.Options {
			h += countVisualLines(wordwrap.String(opt, optionWidth))
		}

		if p.MaxHeight > 0 && h > p.MaxHeight {
			return p.MaxHeight
		}
		return h
	}

	value := p.TextArea.Value()

	// Return to minimum height (2 lines) when prompt is cleared (empty)
	if value == "" {
		return p.withParkedHint(2)
	}

	// Calculate the number of visual lines (accounting for word wrap)
	// The textarea width minus some padding for the cursor
	textWidth := p.Width - 4 // Account for borders and cursor
	if textWidth <= 0 {
		textWidth = 1
	}

	visualLines := 0
	for _, line := range strings.Split(value, "\n") {
		if len(line) == 0 {
			visualLines++
		} else {
			// Calculate how many visual rows this line takes
			visualLines += (len(line) + textWidth - 1) / textWidth
		}
	}

	// Return to minimum height (2 lines) when content fits in a single line
	if visualLines <= 1 {
		return p.withParkedHint(2)
	}

	// When content is more than one line, grow to ExpandedHeight
	expandedHeight := p.ExpandedHeight
	if expandedHeight <= 0 {
		expandedHeight = 10 // Fallback default
	}

	// Apply max height constraint (50% of screen height)
	if p.MaxHeight > 0 && expandedHeight > p.MaxHeight {
		return p.MaxHeight
	}

	return p.withParkedHint(expandedHeight)
}

// withParkedHint grows a desired height by the rendered parked-gate hint line,
// so the hint cannot push the layout past its allotted space.
func (p PromptComponent) withParkedHint(height int) int {
	if p.PendingAnsweringCount() == 0 {
		return height
	}
	height++
	if p.MaxHeight > 0 && height > p.MaxHeight {
		return p.MaxHeight
	}
	return height
}

// SetValue sets the text value of the prompt
func (p *PromptComponent) SetValue(value string) {
	p.TextArea.SetValue(value)
}

// Value returns the current text value
func (p PromptComponent) Value() string {
	return p.TextArea.Value()
}

// Focus gives focus to the prompt
func (p *PromptComponent) Focus() {
	p.TextArea.Focus()
}

// Blur removes focus from the prompt
func (p *PromptComponent) Blur() {
	p.TextArea.Blur()
}

// EnterViNormalMode switches to vi normal mode (for navigation)
func (p *PromptComponent) EnterViNormalMode() {
	p.ViCurrentMode = ViModeNormal
	p.viPendingOp = ""
	p.TextArea.KeyMap = p.viNormalKeyMap
	p.TextArea.Placeholder = "i for insert mode, : for commands, ↑↓ for history"
	p.updateViModeStyle()
}

// EnterViInsertMode switches to vi insert mode
func (p *PromptComponent) EnterViInsertMode() {
	p.ViCurrentMode = ViModeInsert
	p.viPendingOp = ""
	p.TextArea.KeyMap = p.viInsertKeyMap
	p.TextArea.Placeholder = PlaceholderDefault
	p.updateViModeStyle()
}

// EnterViCommandLineMode switches to vi command-line mode
func (p *PromptComponent) EnterViCommandLineMode() {
	p.ViCurrentMode = ViModeCommandLine
	p.viPendingOp = ""
	// Use insert keymap for command-line editing
	p.TextArea.KeyMap = p.viInsertKeyMap
	p.TextArea.Placeholder = "Enter command below..."
	p.updateViModeStyle()
}

// IsViNormalMode returns true if in vi normal mode
func (p PromptComponent) IsViNormalMode() bool {
	return p.ViCurrentMode == ViModeNormal
}

// IsViInsertMode returns true if in vi insert mode
func (p PromptComponent) IsViInsertMode() bool {
	return p.ViCurrentMode == ViModeInsert
}

// IsViCommandLineMode returns true if in vi command-line mode
func (p PromptComponent) IsViCommandLineMode() bool {
	return p.ViCurrentMode == ViModeCommandLine
}

// EnterViLearningMode switches to vi learning mode
func (p *PromptComponent) EnterViLearningMode() {
	p.ViCurrentMode = ViModeLearning
	p.viPendingOp = ""
	// Use insert keymap for learning mode editing
	p.TextArea.KeyMap = p.viInsertKeyMap
	p.TextArea.Placeholder = "Enter learning note (will be appended to project's memory file)"
	p.updateViModeStyle()
}

// IsViLearningMode returns true if in vi learning mode
func (p PromptComponent) IsViLearningMode() bool {
	return p.ViCurrentMode == ViModeLearning
}

// EnterViScrollMode switches to vi scroll mode (for scrolling chat history)
func (p *PromptComponent) EnterViScrollMode() {
	p.ViCurrentMode = ViModeScroll
	p.viPendingOp = ""
	// Use normal keymap for scroll mode (no text editing)
	p.TextArea.KeyMap = p.viNormalKeyMap
	p.TextArea.Placeholder = "G for bottom | j/k to scroll | CTRL-f/b & d/u as in vi | i/:/ESC to stop scrolling"
	p.updateViModeStyle()
}

// IsViScrollMode returns true if in vi scroll mode
func (p PromptComponent) IsViScrollMode() bool {
	return p.ViCurrentMode == ViModeScroll
}

// ViModeStatus returns current vi mode status for display components
func (p PromptComponent) ViModeStatus() (enabled bool, mode string, pending string) {
	return true, p.ViCurrentMode, p.viPendingOp
}

// HandleZhengmingPending parses a ZhengmingPendingMsg and states it in
// answering mode.
//
// It never clobbers an unanswered request: if another gate is already open, the
// new one is queued behind it and becomes active when the current one is
// resolved. A Ruler gate may only be considered satisfied when the Ruler acted
// on that specific request, so dropping one on the floor is not allowed.
func (p *PromptComponent) HandleZhengmingPending(msg court.ZhengmingPendingMsg) {
	aq := make([]AnsweringQuestion, len(msg.Questions))
	for i, q := range msg.Questions {
		aq[i] = AnsweringQuestion{
			Text:    q.Text,
			Summary: q.Summary,
			Options: append(q.Options, "Edit", "Chat"),
		}
	}

	p.ShowAnswering(&AnsweringState{
		RequestID: msg.RequestID,
		Title:     fmt.Sprintf("Zhengming: %s asks", msg.MinisterID),
		Questions: aq,
		Answers:   make([]string, len(aq)),
	})
}

// hasPending reports whether requestID is already queued.
func (p *PromptComponent) hasPending(requestID string) bool {
	for _, st := range p.pendingAnswering {
		if st.RequestID == requestID {
			return true
		}
	}
	return false
}

// PendingAnsweringCount returns how many Ruler requests are open behind the one
// on screen. This is the number the Ruler is told is waiting. A local affordance
// lives in its own slot and is never counted.
func (p *PromptComponent) PendingAnsweringCount() int {
	return len(p.pendingAnswering)
}

// RotateAnswering switches the screen to the next (or previous) open request,
// sending the one on screen to the back of the queue. Every open request is
// therefore selectable: a Ruler facing several pending decisions can choose
// which to answer first instead of being forced through them in arrival order.
// When no request is on screen (the Ruler parked the last one, or a local menu
// is showing) rotation brings a request back. Reports whether anything changed.
func (p *PromptComponent) RotateAnswering(forward bool) bool {
	if p.answering == nil || p.answering.NotARequest {
		if len(p.pendingAnswering) == 0 {
			return false
		}
		p.pendingMenu = nil // a request takes the screen; the menu is regenerable
		next := p.pendingAnswering[0]
		p.pendingAnswering = p.pendingAnswering[1:]
		p.activateAnswering(next)
		return true
	}
	if len(p.pendingAnswering) == 0 {
		return false
	}
	if forward {
		p.pendingAnswering = append(p.pendingAnswering, p.answering)
		p.answering = p.pendingAnswering[0]
		p.pendingAnswering = p.pendingAnswering[1:]
	} else {
		last := p.pendingAnswering[len(p.pendingAnswering)-1]
		p.pendingAnswering = p.pendingAnswering[:len(p.pendingAnswering)-1]
		p.pendingAnswering = append([]*AnsweringState{p.answering}, p.pendingAnswering...)
		p.answering = last
	}
	p.viPendingOp = ""
	p.syncAnsweringMode()
	return true
}

// ActiveAnsweringRequest returns the Ruler request on screen, or "" when none is
// (nothing is open, a request is only parked, or a local menu is showing).
func (p *PromptComponent) ActiveAnsweringRequest() string {
	if p.answering == nil || p.answering.NotARequest {
		return ""
	}
	return p.answering.RequestID
}

// OpenAnsweringRequests returns the IDs of every state this prompt holds — the
// one on screen first, then queued requests, then a held local affordance. Used
// to assert nothing was silently dropped.
func (p *PromptComponent) OpenAnsweringRequests() []string {
	var out []string
	if p.answering != nil {
		out = append(out, p.answering.RequestID)
	}
	for _, st := range p.pendingAnswering {
		out = append(out, st.RequestID)
	}
	if p.pendingMenu != nil {
		out = append(out, p.pendingMenu.RequestID)
	}
	return out
}

// OpenRulerRequests returns the IDs of the Ruler requests this prompt holds — on
// screen or queued. This is what "awaiting the Ruler's answer" means; local UI
// states are excluded by construction.
func (p *PromptComponent) OpenRulerRequests() []string {
	var out []string
	if p.answering != nil && !p.answering.NotARequest {
		out = append(out, p.answering.RequestID)
	}
	for _, st := range p.pendingAnswering {
		out = append(out, st.RequestID)
	}
	return out
}

// ShowAnswering states a Ruler request without clobbering an open one. A request
// always outranks a local affordance for the screen: the newcomer is shown and a
// menu is moved to its own slot. If another request already owns the screen the
// newcomer is queued behind it, so a new request can never retire an unanswered
// one.
func (p *PromptComponent) ShowAnswering(state *AnsweringState) {
	if state == nil {
		return
	}
	if p.answering == nil {
		p.activateAnswering(state)
		return
	}
	if p.answering.NotARequest {
		p.pendingMenu = p.answering
		p.activateAnswering(state)
		return
	}
	if p.answering.RequestID == state.RequestID || p.hasPending(state.RequestID) {
		return
	}
	p.pendingAnswering = append(p.pendingAnswering, state)
}

// EnterAnsweringMode displays a state. Local affordances (NotARequest) are
// routed through ShowMenu so they can never masquerade as a request; real
// requests go through ShowAnswering, which preserves anything already open.
func (p *PromptComponent) EnterAnsweringMode(state *AnsweringState) {
	if state == nil {
		return
	}
	if state.NotARequest {
		p.ShowMenu(state)
		return
	}
	p.ShowAnswering(state)
}

// ShowMenu shows a local UI affordance (the edict action menu). It is not a
// Ruler request: it lives in its own slot, is never counted, listed, or selected
// as one, and can neither displace an open request nor be stranded by the
// request set. A request always keeps the screen; the menu waits in the slot and
// is dropped as soon as no request needs the screen (it is regenerable with
// :edict). At most one menu is retained, so reopening refreshes it.
func (p *PromptComponent) ShowMenu(state *AnsweringState) {
	if state == nil {
		return
	}
	state.NotARequest = true
	if p.answering == nil {
		p.activateAnswering(state)
		return
	}
	if p.answering.NotARequest {
		p.activateAnswering(state)
		return
	}
	// A request owns the screen: hold the menu aside without displacing it.
	p.pendingMenu = state
}

// activateAnswering makes state the one on screen.
func (p *PromptComponent) activateAnswering(state *AnsweringState) {
	p.answering = state
	p.ViCurrentMode = ViModeAnswering
	p.viPendingOp = ""
	p.TextArea.Blur()
	p.Style = p.Style.BorderForeground(globalTheme.PromptOnBorder)
}

// promoteNextAnswering brings the next queued request on screen when none is.
// When no request remains the prompt returns to the typable state and any held
// menu is discarded, so no state is ever held while nothing is shown.
func (p *PromptComponent) promoteNextAnswering() {
	if p.answering != nil {
		return
	}
	if len(p.pendingAnswering) > 0 {
		next := p.pendingAnswering[0]
		p.pendingAnswering = p.pendingAnswering[1:]
		p.pendingMenu = nil
		p.activateAnswering(next)
		return
	}
	p.pendingMenu = nil
	p.EnterViInsertMode()
	p.TextArea.Focus()
}

// syncAnsweringMode makes the prompt's vi mode match whether something is on
// screen.
func (p *PromptComponent) syncAnsweringMode() {
	if p.answering != nil {
		p.ViCurrentMode = ViModeAnswering
		p.TextArea.Blur()
		p.Style = p.Style.BorderForeground(globalTheme.PromptOnBorder)
		return
	}
	p.EnterViInsertMode()
	p.TextArea.Focus()
}

// ParkAnswering takes requestID off screen without retiring it: the Ruler chose
// an intermediate action (Edit / Chat) and has not acted on the request yet. The
// request stays open. If another request is queued it takes the screen so it
// stays visible and reachable; the parked request goes to the back of the queue.
// When it is the only open request, the typable prompt answers it.
func (p *PromptComponent) ParkAnswering(requestID string) {
	if p.answering == nil || p.answering.RequestID != requestID {
		return
	}
	parked := p.answering
	p.answering = nil
	// The parked request must not be re-promoted onto the screen it just left:
	// only a *different* waiting request may take over.
	if len(p.pendingAnswering) > 0 {
		next := p.pendingAnswering[0]
		p.pendingAnswering = p.pendingAnswering[1:]
		p.pendingAnswering = append(p.pendingAnswering, parked)
		p.pendingMenu = nil
		p.activateAnswering(next)
		return
	}
	p.pendingAnswering = append(p.pendingAnswering, parked)
	p.pendingMenu = nil
	p.EnterViInsertMode()
	p.TextArea.Focus()
}

// ResumeAnswering brings a parked request back on screen. It never clobbers a
// request that arrived while the editor was open: if one is on screen, the
// resumed request stays queued so both remain open.
func (p *PromptComponent) ResumeAnswering(requestID string) {
	idx := -1
	for i, st := range p.pendingAnswering {
		if st.RequestID == requestID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return // already on screen, or not ours to resume
	}
	resumed := p.pendingAnswering[idx]
	p.pendingAnswering = append(p.pendingAnswering[:idx], p.pendingAnswering[idx+1:]...)
	if p.answering == nil || p.answering.NotARequest {
		p.pendingMenu = nil
		p.activateAnswering(resumed)
		return
	}
	p.pendingAnswering = append(p.pendingAnswering, resumed)
}

// ResolveAnswering retires requestID — the Ruler acted on it (answered,
// cancelled, or rewrote it). If it was on screen and another request is queued,
// that one takes the screen so it stays answerable; otherwise the prompt returns
// to the typable state. A menu is discarded rather than revived, so nothing is
// ever held while nothing is shown.
func (p *PromptComponent) ResolveAnswering(requestID string) {
	p.pendingAnswering = removeAnswering(p.pendingAnswering, requestID)
	if p.pendingMenu != nil && p.pendingMenu.RequestID == requestID {
		p.pendingMenu = nil
	}
	if p.answering == nil || p.answering.RequestID != requestID {
		return
	}
	p.answering = nil
	p.promoteNextAnswering()
}

// removeAnswering returns states without requestID.
func removeAnswering(states []*AnsweringState, requestID string) []*AnsweringState {
	out := states[:0]
	for _, st := range states {
		if st.RequestID != requestID {
			out = append(out, st)
		}
	}
	return out
}

// UpdateAnsweringQuestion updates the current question's text and resets selection
func (p *PromptComponent) UpdateAnsweringQuestion(text string) {
	if p.answering != nil && p.answering.Current < len(p.answering.Questions) {
		p.answering.Questions[p.answering.Current].Text = text
		p.answering.Questions[p.answering.Current].Summary = text
		p.answering.Questions[p.answering.Current].Selected = 0
	}
}

// updateViModeStyle updates the border color based on vi mode state
// Uses globalTheme.promptOnBorder when focused on prompt (INSERT/COMMAND/LEARNING)
// Uses globalTheme.promptOffBorder when focused away from prompt (NORMAL/VISUAL/SCROLL)
func (p *PromptComponent) updateViModeStyle() {
	switch p.ViCurrentMode {
	case ViModeInsert, ViModeNormal, ViModeLearning:
		// Focus on prompt input - use "on" border
		p.Style = p.Style.BorderForeground(globalTheme.PromptOnBorder)
	default:
		// Focus away from prompt (NORMAL/VISUAL/SCROLL/etc) - use "off" border
		p.Style = p.Style.BorderForeground(globalTheme.PromptOffBorder)
	}
}

// handleViCommand processes vi commands like dd, dw, cc, cw, etc.
func (p *PromptComponent) handleViCommand(key string) (bool, tea.Cmd) {
	// Handle pending operations
	if p.viPendingOp != "" {
		command := p.viPendingOp + key
		p.viPendingOp = "" // Clear pending operation

		switch command {
		case "dd":
			// Delete current line
			return p.deleteCurrentLine()
		case "dw":
			// Delete word forward
			return p.deleteWordForward()
		case "db":
			// Delete word backward
			var cmd tea.Cmd
			p.TextArea, cmd = p.TextArea.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
			return true, cmd
		case "d$", "dD":
			// Delete to end of line
			var cmd tea.Cmd
			p.TextArea, cmd = p.TextArea.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
			return true, cmd
		case "d0", "d^":
			// Delete to beginning of line
			var cmd tea.Cmd
			p.TextArea, cmd = p.TextArea.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
			return true, cmd
		case "cc":
			// Change current line (delete line and enter insert mode)
			handled, cmd := p.deleteCurrentLine()
			if handled {
				p.EnterViInsertMode()
			}
			return handled, cmd
		case "cw":
			// Change word forward (delete word and enter insert mode)
			handled, cmd := p.deleteWordForward()
			if handled {
				p.EnterViInsertMode()
			}
			return handled, cmd
		case "cb":
			// Change word backward (delete word and enter insert mode)
			var cmd tea.Cmd
			p.TextArea, cmd = p.TextArea.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
			p.EnterViInsertMode()
			return true, cmd
		case "c$", "cD":
			// Change to end of line
			var cmd tea.Cmd
			p.TextArea, cmd = p.TextArea.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
			p.EnterViInsertMode()
			return true, cmd
		case "c0", "c^":
			// Change to beginning of line
			var cmd tea.Cmd
			p.TextArea, cmd = p.TextArea.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
			p.EnterViInsertMode()
			return true, cmd
		default:
			// Unknown command, ignore
			return false, nil
		}
	}

	// Check if this is the start of a compound command
	if key == "d" || key == "c" {
		p.viPendingOp = key
		return true, nil
	}

	return false, nil
}

// deleteCurrentLine deletes the current line
func (p *PromptComponent) deleteCurrentLine() (bool, tea.Cmd) {
	lines := strings.Split(p.TextArea.Value(), "\n")
	row := p.TextArea.Line()
	lineInfo := p.TextArea.LineInfo()
	col := lineInfo.StartColumn + lineInfo.ColumnOffset

	if row >= 0 && row < len(lines) {
		// Delete the current line
		newLines := append(lines[:row], lines[row+1:]...)
		newValue := strings.Join(newLines, "\n")
		p.TextArea.SetValue(newValue)

		// Determine target row after deletion
		targetRow := row
		if targetRow >= len(newLines) {
			targetRow = len(newLines) - 1
		}
		if targetRow < 0 {
			targetRow = 0
		}

		// Determine target column, clamped to line length
		targetCol := 0
		if targetRow >= 0 && targetRow < len(newLines) {
			if col < len(newLines[targetRow]) {
				targetCol = col
			} else {
				targetCol = len(newLines[targetRow])
			}
		}

		// Move cursor to target row
		p.TextArea.SetCursor(0)
		currentRow := p.TextArea.Line()
		for currentRow > targetRow {
			p.TextArea.CursorUp()
			currentRow = p.TextArea.Line()
		}
		for currentRow < targetRow {
			p.TextArea.CursorDown()
			currentRow = p.TextArea.Line()
		}

		// Set cursor to target column
		p.TextArea.SetCursor(targetCol)

		return true, nil
	}

	return false, nil
}

// deleteWordForward deletes from cursor to the end of the current word
func (p *PromptComponent) deleteWordForward() (bool, tea.Cmd) {
	value := p.TextArea.Value()
	if len(value) == 0 {
		return false, nil
	}

	// Get cursor position
	lineInfo := p.TextArea.LineInfo()
	row := p.TextArea.Line()
	col := lineInfo.ColumnOffset

	// Split into lines
	lines := strings.Split(value, "\n")
	if row < 0 || row >= len(lines) {
		return false, nil
	}

	currentLine := lines[row]
	if col >= len(currentLine) {
		// At end of line, delete the newline if there's a next line
		if row < len(lines)-1 {
			lines[row] = currentLine + lines[row+1]
			lines = append(lines[:row+1], lines[row+2:]...)
			newValue := strings.Join(lines, "\n")
			p.TextArea.SetValue(newValue)

			// Restore cursor position
			p.TextArea.SetCursor(0)
			currentRow := p.TextArea.Line()
			for currentRow < row {
				p.TextArea.CursorDown()
				currentRow = p.TextArea.Line()
			}
			p.TextArea.SetCursor(col)
			return true, nil
		}
		return false, nil
	}

	// Find the end of the current word
	endCol := col

	// Skip any leading whitespace
	for endCol < len(currentLine) && (currentLine[endCol] == ' ' || currentLine[endCol] == '\t') {
		endCol++
	}

	// Now find the end of the word
	if endCol < len(currentLine) {
		for endCol < len(currentLine) && currentLine[endCol] != ' ' && currentLine[endCol] != '\t' {
			endCol++
		}
	}

	// If we didn't move at all, just delete one character
	if endCol == col {
		endCol = col + 1
	}

	// Delete from col to endCol
	newLine := currentLine[:col] + currentLine[endCol:]
	lines[row] = newLine
	newValue := strings.Join(lines, "\n")
	p.TextArea.SetValue(newValue)

	// Restore cursor position
	p.TextArea.SetCursor(0)
	currentRow := p.TextArea.Line()
	for currentRow < row {
		p.TextArea.CursorDown()
		currentRow = p.TextArea.Line()
	}
	// Set cursor to the column position
	targetCol := col
	if targetCol > len(lines[row]) {
		targetCol = len(lines[row])
	}
	p.TextArea.SetCursor(targetCol)

	return true, nil
}

// wordBackward moves the cursor one word to the left using the textarea public API.
// This replaces the built-in textarea wordLeft() which has an infinite loop bug
// when the cursor is at (0,0) with an empty buffer or leading whitespace.
func (p *PromptComponent) wordBackward() {
	value := p.TextArea.Value()
	if value == "" {
		return
	}

	lines := strings.Split(value, "\n")
	row := p.TextArea.Line()
	lineInfo := p.TextArea.LineInfo()
	col := lineInfo.StartColumn + lineInfo.ColumnOffset

	// Already at the very beginning
	if row == 0 && col == 0 {
		return
	}

	for {
		// Move left by one character
		if col > 0 {
			col--
		} else if row > 0 {
			// Move to end of previous line
			row--
			col = len(lines[row])
			continue
		} else {
			// At (0,0), can't go further
			return
		}

		// Skip whitespace: if current char is not whitespace, we've found a word
		if col < len(lines[row]) && !unicode.IsSpace(rune(lines[row][col])) {
			break
		}
	}

	// Now skip backwards over the word (non-whitespace chars)
	for col > 0 && !unicode.IsSpace(rune(lines[row][col-1])) {
		col--
	}

	// Position the cursor
	p.TextArea.SetCursor(0)
	currentRow := p.TextArea.Line()
	for currentRow > row {
		p.TextArea.CursorUp()
		currentRow = p.TextArea.Line()
	}
	for currentRow < row {
		p.TextArea.CursorDown()
		currentRow = p.TextArea.Line()
	}
	p.TextArea.SetCursor(col)
}

// Update handles messages for the prompt component
func (p PromptComponent) Update(msg interface{}) (PromptComponent, tea.Cmd) {
	var cmd tea.Cmd

	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		// Answering mode: intercept all keys
		if p.answering != nil {
			return p.updateAnswering(keyMsg)
		}
		// No gate is on screen but some are parked (the Ruler chose Chat or
		// Edit on the last one). Offer the same switch keys so those gates stay
		// reachable instead of becoming invisible.
		if p.PendingAnsweringCount() > 0 {
			switch keyMsg.String() {
			case "ctrl+n":
				p.RotateAnswering(true)
				return p, nil
			case "ctrl+p":
				p.RotateAnswering(false)
				return p, nil
			}
		}
		if p.IsViInsertMode() || p.IsViNormalMode() {
			// Shift+Enter inserts a newline instead of submitting
			if keyMsg.Type == tea.KeyEnter && keyMsg.Alt {
				p.TextArea.InsertString("\n")
				return p, nil
			}
			if keyMsg.Type == tea.KeyEnter && !keyMsg.Alt {
				promptContent := strings.TrimSpace(p.TextArea.Value())
				if promptContent != "" {
					p.TextArea.SetValue("")
					p.TextArea.SetCursor(0)
					return p, func() tea.Msg { return SubmitPromptMsg{Prompt: promptContent} }
				}
				// If prompt is empty, consume the Enter key
				return p, nil
			}
		}
		// Intercept word-backward to bypass the buggy textarea.wordLeft()
		if p.IsViInsertMode() && keyMsg.String() == "alt+left" {
			p.wordBackward()
			return p, nil
		}
		if p.IsViNormalMode() {
			keyStr := keyMsg.String()

			// Always allow arrow keys and navigation keys, regardless of mode
			navigationKeys := map[string]bool{
				"up": true, "down": true, "left": true, "right": true,
				"home": true, "end": true,
				"pgup": true, "pgdown": true,
			}

			// If it's a navigation key, pass it through immediately
			if navigationKeys[keyStr] {
				p.TextArea, cmd = p.TextArea.Update(msg)
				return p, cmd
			}

			// Handle vi commands (d, c, dd, dw, cc, cw, etc.)
			handled, viCmd := p.handleViCommand(keyStr)
			if handled {
				return p, viCmd
			}

			// Intercept "b" (word-backward) to bypass the buggy textarea.wordLeft()
			if keyStr == "b" && p.viPendingOp == "" {
				p.wordBackward()
				return p, nil
			}

			// Allow only specific navigation and command keys in normal mode
			allowedKeys := map[string]bool{
				// Navigation (vi keys)
				"h": true, "j": true, "k": true, "l": true,
				"w": true, "b": true, "e": true,
				"0": true, "^": true, "$": true,
				"gg": true, "G": true,
				// Deletion (single character)
				"x": true, "X": true,
				// Capital commands
				"D": true, // Delete to end of line
				// Other
				"p": true,                                                        // paste
				":": true,                                                        // command mode (handled in tui.go)
				"i": true, "I": true, "a": true, "A": true, "o": true, "O": true, // insert mode triggers
			}

			// If it's not an allowed key and it's a single character (potential text input),
			// ignore it to prevent text insertion in normal mode
			if !allowedKeys[keyStr] && len(keyStr) == 1 && p.viPendingOp == "" {
				// Ignore this key in normal mode
				return p, nil
			}
		}
	}

	p.TextArea, cmd = p.TextArea.Update(msg)
	return p, cmd
}

// View renders the prompt component
func (p PromptComponent) View() string {
	if p.answering != nil {
		return p.viewAnswering()
	}
	content := p.TextArea.View()
	// Parked gates are still open requests. Say so on the typable prompt so the
	// Ruler knows a decision is outstanding and how to get back to it.
	if n := p.PendingAnsweringCount(); n > 0 {
		hint := fmt.Sprintf("⏳ %d Ruler request(s) awaiting an answer — ctrl+n to switch, :gates to list", n)
		content = hint + "\n" + content
	}
	return p.Style.Render(wordwrap.String(content, p.Width))
}

// viewAnswering renders the answering mode UI
func (p PromptComponent) viewAnswering() string {
	a := p.answering
	if a.Current >= len(a.Questions) {
		return p.Style.Render("Submitting answers...")
	}
	q := a.Questions[a.Current]

	contentWidth := p.Width - 2
	if contentWidth <= 0 {
		contentWidth = 1
	}
	optionWidth := contentWidth - 4
	if optionWidth <= 0 {
		optionWidth = 1
	}

	var b strings.Builder
	// Title. A queued-request count keeps further open requests visible: they are
	// not hidden behind the one on screen, they are simply answered in turn. The
	// count speaks only of Ruler requests — a local affordance is never "waiting".
	title := a.Title
	if n := p.PendingAnsweringCount(); n > 0 {
		title = fmt.Sprintf("%s (+%d waiting)", title, n)
	}
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(title))
	b.WriteByte('\n')
	// Question text (prefer summary for compact display)
	displayText := q.Text
	if q.Summary != "" {
		displayText = q.Summary
	}
	b.WriteString(wordwrap.String(displayText, contentWidth))
	b.WriteByte('\n')

	// Render options
	for i, opt := range q.Options {
		wrappedOpt := wordwrap.String(opt, optionWidth)
		if i == q.Selected {
			b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(globalTheme.PromptOnBorder).Render(fmt.Sprintf("  ▶ %s", wrappedOpt)))
		} else {
			b.WriteString(fmt.Sprintf("    %s", wrappedOpt))
		}
		if i < len(q.Options)-1 {
			b.WriteByte('\n')
		}
	}

	return p.Style.Render(b.String())
}

// updateAnswering handles key input in answering mode
func (p PromptComponent) updateAnswering(keyMsg tea.KeyMsg) (PromptComponent, tea.Cmd) {
	a := p.answering
	if a == nil || a.Current >= len(a.Questions) {
		return p, nil
	}
	q := &a.Questions[a.Current]

	// Option selection mode
	totalOptions := len(q.Options)
	switch keyMsg.String() {
	case "j", "down":
		q.Selected = (q.Selected + 1) % totalOptions
	case "k", "up":
		q.Selected = (q.Selected - 1 + totalOptions) % totalOptions
	case "enter":
		slog.Info("User hit enter in answering", "selected", q.Selected, "len", len(q.Options))
		if q.Options[q.Selected] == "Edit" {
			// "Edit" selected — open question in external editor
			requestID := a.RequestID
			questionText := q.Text
			if q.Summary != "" {
				questionText = q.Summary
			}
			return p, func() tea.Msg {
				return AnsweringEditMsg{RequestID: requestID, Question: questionText}
			}
		} else if q.Options[q.Selected] == "Chat" {
			requestID := a.RequestID
			return p, func() tea.Msg {
				return AnsweringChatMsg{RequestID: requestID}
			}
		}
		return p.advanceAnswer(q.Options[q.Selected])
	case "esc":
		return p, func() tea.Msg { return AnsweringCancelMsg{RequestID: a.RequestID} }
	case "ctrl+n":
		// Switch to the next open gate (see RotateAnswering).
		p.RotateAnswering(true)
		return p, nil
	case "ctrl+p":
		p.RotateAnswering(false)
		return p, nil
	}
	return p, nil
}

// advanceAnswer records the answer and moves to the next question or completes
func (p PromptComponent) advanceAnswer(answer string) (PromptComponent, tea.Cmd) {
	a := p.answering
	a.Answers[a.Current] = answer
	a.Current++

	if a.Current >= len(a.Questions) {
		answers := make([]string, len(a.Answers))
		copy(answers, a.Answers)
		requestID := a.RequestID
		return p, func() tea.Msg {
			return AnsweredMsg{RequestID: requestID, Answers: answers}
		}
	}
	return p, nil
}
