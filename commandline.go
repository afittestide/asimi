package main

import (
	"time"

	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/storage"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Toast represents a single toast notification
type Toast struct {
	ID      string
	Message string
	Type    string // info, success, warning, error
	Created time.Time
	Timeout time.Duration
}

// CommandLine messages for TUI coordination
type (
	commandReadyMsg       struct{ command string }
	commandCancelledMsg   struct{}
	commandTextChangedMsg struct{}                  // Signals completion update needed
	navigateCompletionMsg struct{ direction int }   // -1 for up, +1 for down
	acceptCompletionMsg   struct{}                  // Tab pressed
	navigateHistoryMsg    struct{ direction int }   // For completion or history
	yesNoResponseMsg      struct{ answer bool }     // true for yes, false for no
	inputResponseMsg      struct{ text string }     // response from free text input mode
	apiKeyPromptMsg       struct{ provider string } // request API key input for a provider
	apiKeySavedMsg        struct{ provider string } // API key saved to keyring for a provider
	enterModelNameMsg     struct{ provider string } // request manual model name entry for a provider
	enterModelNameRawMsg  struct{}                  // request manual "provider/model" entry (pinned picker row)

	// Search messages
	searchExecutedMsg struct {
		pattern   string
		direction int
	}
	searchCancelledMsg struct{}
)

// Mode management - single unified message for all mode changes
type ChangeModeMsg struct {
	NewMode string // "insert", "normal", "visual", "command", "help", "models", "resume", "scroll"
}

// CommandLineMode represents the state of the command line
type CommandLineMode int

const (
	CommandLineIdle CommandLineMode = iota
	CommandLineCommand
	CommandLineToast
	CommandLineYesNo
	CommandLineInput
	CommandLineSearch // vi-style search input (/ or ?)
)

// CommandLineComponent manages the bottom command line
// Handles both : commands and toast notifications
type CommandLineComponent struct {
	mode       CommandLineMode
	toasts     []Toast
	textInput  textinput.Model // Text input for command mode
	yesNoInput string          // Simple input for yes/no mode (just y/n)
	width      int
	toastStyle lipgloss.Style

	// History support
	history        []string // Command history
	historyCursor  int      // Current position in history
	historySaved   bool     // Whether we've saved the current input
	historyPending string   // The input being typed before navigating history

	// Yes/No prompt support
	yesNoQuestion string // The question being asked

	// Free text input support
	inputPrompt    string          // The prompt message to display
	inputTextInput textinput.Model // Text input for free text mode

	// Search support
	searchDirection int             // 1 = forward (/), -1 = backward (?)
	searchTextInput textinput.Model // Text input for search pattern
}

// NewCommandLineComponent creates a new command line component
func NewCommandLineComponent() *CommandLineComponent {
	ti := textinput.New()
	ti.Prompt = ":"
	ti.Focus()

	inputTi := textinput.New()
	inputTi.Prompt = "> "
	inputTi.Focus()

	searchTi := textinput.New()
	searchTi.Prompt = ""
	searchTi.Focus()

	return &CommandLineComponent{
		mode:            CommandLineIdle,
		toasts:          make([]Toast, 0),
		textInput:       ti,
		inputTextInput:  inputTi,
		searchTextInput: searchTi,
		history:         make([]string, 0),
		historyCursor:   0,
		historySaved:    false,
		historyPending:  "",
		toastStyle: lipgloss.NewStyle().
			Background(globalTheme.SuccessColor).
			Foreground(globalTheme.ToastTextColor).
			Padding(0, 1).
			MaxWidth(50),
	}
}

// AddToast adds a new toast notification
func (cl *CommandLineComponent) AddToast(message, toastType string, timeout time.Duration) {
	toast := Toast{
		ID:      time.Now().String(),
		Message: message,
		Type:    toastType,
		Created: time.Now(),
		Timeout: timeout,
	}
	cl.toasts = append(cl.toasts, toast)
}

// RemoveToast removes a toast by ID
func (cl *CommandLineComponent) RemoveToast(id string) {
	for i, toast := range cl.toasts {
		if toast.ID == id {
			cl.toasts = append(cl.toasts[:i], cl.toasts[i+1:]...)
			break
		}
	}
}

// ClearToasts removes all existing toast notifications
func (cl *CommandLineComponent) ClearToasts() {
	cl.toasts = nil
}

// ActiveToasts returns the current toast notifications.
func (cl *CommandLineComponent) ActiveToasts() []Toast {
	return cl.toasts
}

// EnterCommandMode enters command mode with optional initial text
func (cl *CommandLineComponent) EnterCommandMode(initialText string) tea.Cmd {
	cl.mode = CommandLineCommand
	cl.textInput.SetValue(initialText)
	cl.textInput.CursorEnd()
	cl.textInput.Focus()
	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "command"}
	}
}

// ExitCommandMode exits command mode and returns to idle
func (cl *CommandLineComponent) ExitCommandMode() tea.Cmd {
	cl.mode = CommandLineIdle
	cl.textInput.SetValue("")
	cl.textInput.Blur()
	cl.historySaved = false
	cl.historyPending = ""
	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "insert"}
	}
}

// IsInCommandMode returns true if in command mode
func (cl *CommandLineComponent) IsInCommandMode() bool {
	return cl.mode == CommandLineCommand
}

// Blur removes focus from the command line
func (cl *CommandLineComponent) Blur() {
	cl.textInput.Blur()
}

// EnterYesNoMode enters yes/no prompt mode with a question
func (cl *CommandLineComponent) EnterYesNoMode(question string) tea.Cmd {
	cl.mode = CommandLineYesNo
	cl.yesNoQuestion = question
	cl.yesNoInput = ""
	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "yesno"}
	}
}

// ExitYesNoMode exits yes/no mode and returns to idle
func (cl *CommandLineComponent) ExitYesNoMode() tea.Cmd {
	cl.mode = CommandLineIdle
	cl.yesNoQuestion = ""
	cl.yesNoInput = ""
	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "insert"}
	}
}

// IsInYesNoMode returns true if in yes/no mode
func (cl *CommandLineComponent) IsInYesNoMode() bool {
	return cl.mode == CommandLineYesNo
}

// EnterInputMode enters free text input mode with a prompt message
func (cl *CommandLineComponent) EnterInputMode(prompt string) tea.Cmd {
	cl.mode = CommandLineInput
	cl.inputPrompt = prompt
	cl.inputTextInput.SetValue("")
	cl.inputTextInput.Focus()
	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "input"}
	}
}

// ExitInputMode exits input mode and returns to idle
func (cl *CommandLineComponent) ExitInputMode() tea.Cmd {
	cl.mode = CommandLineIdle
	cl.inputPrompt = ""
	cl.inputTextInput.SetValue("")
	cl.inputTextInput.Blur()
	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "insert"}
	}
}

// IsInInputMode returns true if in free text input mode
func (cl *CommandLineComponent) IsInInputMode() bool {
	return cl.mode == CommandLineInput
}

// EnterSearchMode enters vi-style search mode with the given direction
func (cl *CommandLineComponent) EnterSearchMode(direction int) tea.Cmd {
	cl.mode = CommandLineSearch
	cl.searchDirection = direction
	cl.searchTextInput.SetValue("")
	cl.searchTextInput.Focus()
	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "search"}
	}
}

// ExitSearchMode exits search mode and returns to models view
func (cl *CommandLineComponent) ExitSearchMode() tea.Cmd {
	cl.mode = CommandLineIdle
	cl.searchTextInput.SetValue("")
	cl.searchTextInput.Blur()
	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "models"}
	}
}

// IsInSearchMode returns true if in search mode
func (cl *CommandLineComponent) IsInSearchMode() bool {
	return cl.mode == CommandLineSearch
}

// GetSearchPattern returns the current search text input value
func (cl *CommandLineComponent) GetSearchPattern() string {
	return cl.searchTextInput.Value()
}

// GetSearchDirection returns the current search direction (1=forward, -1=backward)
func (cl *CommandLineComponent) GetSearchDirection() int {
	return cl.searchDirection
}

// SetCommand sets the current command being entered
func (cl *CommandLineComponent) SetCommand(cmd string) {
	cl.textInput.SetValue(cmd)
	cl.textInput.CursorEnd()
	if cmd != "" {
		cl.mode = CommandLineCommand
	} else {
		cl.mode = CommandLineIdle
	}
}

// GetCommand returns the current command
func (cl *CommandLineComponent) GetCommand() string {
	return cl.textInput.Value()
}

// ClearCommand clears the current command
func (cl *CommandLineComponent) ClearCommand() {
	cl.textInput.SetValue("")
	cl.mode = CommandLineIdle
}

// SetWidth sets the width for rendering
func (cl *CommandLineComponent) SetWidth(width int) {
	cl.width = width
	cl.textInput.Width = width - 1 // Account for the ":" prompt
}

// Update handles updating the command line (e.g., removing expired toasts)
func (cl *CommandLineComponent) Update() {
	now := time.Now()

	// Remove expired toasts
	activeToasts := make([]Toast, 0)
	for _, toast := range cl.toasts {
		if now.Sub(toast.Created) < toast.Timeout {
			activeToasts = append(activeToasts, toast)
		}
	}
	cl.toasts = activeToasts
}

// View renders the command line
func (cl *CommandLineComponent) View() string {
	// Priority 1: Show yes/no prompt if in yes/no mode
	if cl.mode == CommandLineYesNo {
		promptText := cl.yesNoQuestion + " (y/n) " + cl.yesNoInput
		cursorStyle := lipgloss.NewStyle().Reverse(true)
		displayText := promptText + cursorStyle.Render(" ")
		promptStyle := lipgloss.NewStyle().
			Foreground(globalTheme.Warning).
			Width(cl.width)
		return promptStyle.Render(displayText)
	}

	// Priority 2: Show command if in command mode
	if cl.mode == CommandLineCommand {
		cmdStyle := lipgloss.NewStyle().
			Foreground(globalTheme.ToastTextColor).
			Width(cl.width)
		return cmdStyle.Render(cl.textInput.View())
	}

	// Priority 2.5: Show free text input if in input mode
	if cl.mode == CommandLineInput {
		promptStyle := lipgloss.NewStyle().
			Foreground(globalTheme.TextColor).
			Width(cl.width)
		displayText := cl.inputPrompt + " " + cl.inputTextInput.View()
		return promptStyle.Render(displayText)
	}

	// Priority 2.6: Show search input if in search mode
	if cl.mode == CommandLineSearch {
		prefix := "/"
		if cl.searchDirection < 0 {
			prefix = "?"
		}
		searchStyle := lipgloss.NewStyle().
			Foreground(globalTheme.ToastTextColor).
			Width(cl.width)
		return searchStyle.Render(prefix + cl.searchTextInput.View())
	}

	// Priority 3: Show toast if active
	if len(cl.toasts) > 0 {
		toast := cl.toasts[len(cl.toasts)-1]
		style := cl.toastStyle

		// Truncate message if it's too long for the available width
		// Account for padding (2 chars) when calculating available width
		message := toast.Message
		availableWidth := cl.width - 2
		if availableWidth > 0 && len([]rune(message)) > availableWidth {
			message = utils.TruncateMiddle(message, availableWidth)
		}

		contentWidth := lipgloss.Width(message)
		frameWidth, _ := style.GetFrameSize()
		maxWidth := style.GetMaxWidth()
		if maxWidth > 0 && contentWidth+frameWidth > maxWidth {
			style = style.Copy().MaxWidth(contentWidth + frameWidth)
		}

		switch toast.Type {
		case "info":
			style = style.Background(lipgloss.NoColor{}).Foreground(globalTheme.TextColor)
		case "success":
			style = style.Background(lipgloss.NoColor{}).Foreground(globalTheme.TextColor)
		case "warning":
			style = style.Background(lipgloss.NoColor{}).Foreground(globalTheme.Warning)
		case "error":
			style = style.Background(globalTheme.Error)
		}

		return style.Render(message)
	}

	// Default: Show blank line
	return ""
}

// LoadHistory loads command history from a history store
func (cl *CommandLineComponent) LoadHistory(entries []storage.HistoryEntry) {
	cl.history = make([]string, 0, len(entries))
	for _, entry := range entries {
		cl.history = append(cl.history, entry.Content)
	}
	cl.historyCursor = len(cl.history)
}

// AddToHistory adds a command to the history
func (cl *CommandLineComponent) AddToHistory(cmd string) {
	// Don't add empty commands or duplicates of the last command
	if cmd == "" {
		return
	}
	if len(cl.history) > 0 && cl.history[len(cl.history)-1] == cmd {
		return
	}
	cl.history = append(cl.history, cmd)
	cl.historyCursor = len(cl.history)
}

// NavigateHistory navigates through command history
// direction: -1 for previous (up), +1 for next (down)
// Returns true if navigation occurred
func (cl *CommandLineComponent) NavigateHistory(direction int) bool {
	if len(cl.history) == 0 {
		return false
	}

	currentValue := cl.textInput.Value()

	switch {
	case direction < 0:
		// Navigate backwards (older commands)
		if !cl.historySaved {
			cl.historyPending = currentValue
			cl.historySaved = true
		}
		if cl.historyCursor == len(cl.history) {
			cl.historyCursor = len(cl.history) - 1
		} else if cl.historyCursor > 0 {
			cl.historyCursor--
		}
		if cl.historyCursor >= 0 && cl.historyCursor < len(cl.history) {
			cl.textInput.SetValue(cl.history[cl.historyCursor])
			cl.textInput.CursorEnd()
			return true
		}
	case direction > 0:
		// Navigate forwards (newer commands)
		if !cl.historySaved {
			return false
		}
		if cl.historyCursor < len(cl.history)-1 {
			cl.historyCursor++
			cl.textInput.SetValue(cl.history[cl.historyCursor])
			cl.textInput.CursorEnd()
			return true
		}
		// Reached the end, restore pending command
		cl.historyCursor = len(cl.history)
		cl.textInput.SetValue(cl.historyPending)
		cl.textInput.CursorEnd()
		cl.historySaved = false
		return true
	}

	return false
}

// HandleKey handles keyboard input for the command line component
func (cl *CommandLineComponent) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	// Handle yes/no mode
	if cl.IsInYesNoMode() {
		keyStr := msg.String()
		switch keyStr {
		case "y", "Y":
			cl.yesNoInput = "y"
			return nil, true
		case "n", "N":
			cl.yesNoInput = "n"
			return nil, true
		case "enter":
			if cl.yesNoInput == "y" {
				return tea.Batch(
					cl.ExitYesNoMode(),
					func() tea.Msg { return yesNoResponseMsg{answer: true} },
				), true
			} else if cl.yesNoInput != "" {
				return tea.Batch(
					cl.ExitYesNoMode(),
					func() tea.Msg { return yesNoResponseMsg{answer: false} },
				), true
			}
			// No answer typed yet, ignore enter
			return nil, true
		case "backspace", "ctrl+h":
			cl.yesNoInput = ""
			return nil, true
		case "esc":
			exitCmd := cl.ExitYesNoMode()
			return tea.Batch(
				exitCmd,
				func() tea.Msg { return yesNoResponseMsg{answer: false} },
			), true
		}
		// Ignore other keys in yes/no mode
		return nil, true
	}

	// Handle input mode (free text input)
	if cl.IsInInputMode() {
		keyStr := msg.String()
		switch keyStr {
		case "enter":
			// Capture text before ExitInputMode clears it
			text := cl.inputTextInput.Value()
			return tea.Batch(
				cl.ExitInputMode(),
				func() tea.Msg { return inputResponseMsg{text: text} },
			), true
		case "esc":
			// Cancel input mode
			exitCmd := cl.ExitInputMode()
			return tea.Batch(
				exitCmd,
				func() tea.Msg { return inputResponseMsg{text: ""} },
			), true
		default:
			// Let textinput handle character input
			var cmd tea.Cmd
			cl.inputTextInput, cmd = cl.inputTextInput.Update(msg)
			return cmd, true
		}
	}

	// Handle search mode (vi-style / and ? search)
	if cl.IsInSearchMode() {
		keyStr := msg.String()
		switch keyStr {
		case "enter":
			pattern := cl.searchTextInput.Value()
			direction := cl.searchDirection
			return tea.Batch(
				cl.ExitSearchMode(),
				func() tea.Msg { return searchExecutedMsg{pattern: pattern, direction: direction} },
			), true
		case "esc":
			return tea.Batch(
				cl.ExitSearchMode(),
				func() tea.Msg { return searchCancelledMsg{} },
			), true
		case "backspace", "ctrl+h":
			if cl.searchTextInput.Value() == "" {
				return tea.Batch(
					cl.ExitSearchMode(),
					func() tea.Msg { return searchCancelledMsg{} },
				), true
			}
			var cmd tea.Cmd
			cl.searchTextInput, cmd = cl.searchTextInput.Update(msg)
			return cmd, true
		default:
			var cmd tea.Cmd
			cl.searchTextInput, cmd = cl.searchTextInput.Update(msg)
			return cmd, true
		}
	}

	if !cl.IsInCommandMode() {
		return nil, false
	}

	keyStr := msg.String()

	// Handle special keys before passing to textinput
	switch keyStr {
	case "esc":
		// Cancel command mode
		exitCmd := cl.ExitCommandMode()
		return tea.Batch(
			exitCmd,
			func() tea.Msg { return commandCancelledMsg{} },
		), true

	case "enter":
		// Execute command
		cmdText := cl.GetCommand()
		exitCmd := cl.ExitCommandMode()
		if cmdText != "" {
			cl.AddToHistory(cmdText)
			return tea.Batch(
				exitCmd,
				func() tea.Msg { return commandReadyMsg{command: cmdText} },
			), true
		}
		return tea.Batch(
			exitCmd,
			func() tea.Msg { return commandCancelledMsg{} },
		), true

	case "backspace", "ctrl+h":
		// Exit command mode if input is empty
		if cl.textInput.Value() == "" {
			exitCmd := cl.ExitCommandMode()
			return tea.Batch(
				exitCmd,
				func() tea.Msg { return commandCancelledMsg{} },
			), true
		}
		// Let textinput handle the backspace
		var cmd tea.Cmd
		cl.textInput, cmd = cl.textInput.Update(msg)
		return tea.Batch(cmd, func() tea.Msg { return commandTextChangedMsg{} }), true

	case "up":
		// Navigate history
		direction := -1
		if cl.NavigateHistory(direction) {
			return func() tea.Msg { return commandTextChangedMsg{} }, true
		}
		return func() tea.Msg { return navigateHistoryMsg{direction: direction} }, true

	case "down":
		// Navigate history
		direction := 1
		if cl.NavigateHistory(direction) {
			return func() tea.Msg { return commandTextChangedMsg{} }, true
		}
		return func() tea.Msg { return navigateHistoryMsg{direction: direction} }, true

	case "tab":
		// Accept completion
		return func() tea.Msg { return acceptCompletionMsg{} }, true

	case "ctrl+n":
		// Navigate completion down
		return func() tea.Msg { return navigateCompletionMsg{direction: 1} }, true

	case "shift+tab", "ctrl+p":
		// Navigate completion up
		return func() tea.Msg { return navigateCompletionMsg{direction: -1} }, true

	default:
		// Let textinput handle all other keys (including cursor movement, deletion, etc.)
		prevValue := cl.textInput.Value()
		var cmd tea.Cmd
		cl.textInput, cmd = cl.textInput.Update(msg)

		// Signal text change if value changed
		if cl.textInput.Value() != prevValue {
			return tea.Batch(cmd, func() tea.Msg { return commandTextChangedMsg{} }), true
		}
		return cmd, true
	}
}
