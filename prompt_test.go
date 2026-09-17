package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/reflow/wordwrap"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/storage"
)

// TestPromptHeightGrowsTo10LinesForMultilineInput tests that prompt grows to 10 lines
// when user input is more than one line (issue #31)
func TestPromptHeightGrowsTo10LinesForMultilineInput(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetScreenHeight(40) // Set screen height so MaxHeight = 20

	tests := []struct {
		name           string
		value          string
		mode           string
		expectedHeight int
	}{
		{
			name:           "empty prompt returns 2 lines",
			value:          "",
			mode:           ViModeInsert,
			expectedHeight: 2,
		},
		{
			name:           "single line content returns 2 lines",
			value:          "Hello World",
			mode:           ViModeInsert,
			expectedHeight: 2,
		},
		{
			name:           "two lines of content grows to 10 lines",
			value:          "Line 1\nLine 2",
			mode:           ViModeInsert,
			expectedHeight: 10,
		},
		{
			name:           "multiple lines of content grows to 10 lines",
			value:          "Line 1\nLine 2\nLine 3\nLine 4",
			mode:           ViModeInsert,
			expectedHeight: 10,
		},
		{
			name:           "scroll mode returns 2 lines regardless of content",
			value:          "Line 1\nLine 2\nLine 3",
			mode:           ViModeScroll,
			expectedHeight: 2,
		},
		{
			name:           "scroll mode with empty content returns 2 lines",
			value:          "",
			mode:           ViModeScroll,
			expectedHeight: 2,
		},
		{
			name:           "normal mode with multiline grows to 10 lines",
			value:          "Line 1\nLine 2",
			mode:           ViModeNormal,
			expectedHeight: 10,
		},
		{
			name:           "long text that wraps grows to 10 lines",
			value:          "This is a very long line of text that should wrap to multiple lines because it exceeds the width of the prompt component which is set to 80 characters",
			mode:           ViModeInsert,
			expectedHeight: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt.SetValue(tt.value)
			prompt.ViCurrentMode = tt.mode

			height := prompt.CalculateDesiredHeight()
			if height != tt.expectedHeight {
				t.Errorf("CalculateDesiredHeight() = %d, want %d", height, tt.expectedHeight)
			}
		})
	}
}

// TestPromptHeightRespectsMaxHeight tests that the 10-line expansion respects MaxHeight
func TestPromptHeightRespectsMaxHeight(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	// Set a small screen height so MaxHeight = 4 (50% of 8)
	prompt.SetScreenHeight(8)

	// Multi-line content should be capped at MaxHeight
	prompt.SetValue("Line 1\nLine 2\nLine 3")
	prompt.ViCurrentMode = ViModeInsert

	height := prompt.CalculateDesiredHeight()
	if height != 4 {
		t.Errorf("CalculateDesiredHeight() = %d, want %d (MaxHeight)", height, 4)
	}
}

// TestPromptHeightConfigurableExpandedHeight tests that ExpandedHeight can be configured
func TestPromptHeightConfigurableExpandedHeight(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetScreenHeight(40) // MaxHeight = 20

	// Set custom expanded height
	prompt.SetExpandedHeight(15)

	// Multi-line content should grow to configured expanded height
	prompt.SetValue("Line 1\nLine 2")
	prompt.ViCurrentMode = ViModeInsert

	height := prompt.CalculateDesiredHeight()
	if height != 15 {
		t.Errorf("CalculateDesiredHeight() = %d, want %d (custom ExpandedHeight)", height, 15)
	}
}

// TestPromptHeightClearedReturnsToMinimum tests that clearing the prompt returns height to 2
func TestPromptHeightClearedReturnsToMinimum(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetScreenHeight(40)

	// Start with multiline content
	prompt.SetValue("Line 1\nLine 2\nLine 3")
	prompt.ViCurrentMode = ViModeInsert

	height := prompt.CalculateDesiredHeight()
	if height != 10 {
		t.Errorf("CalculateDesiredHeight() with multiline = %d, want 10", height)
	}

	// Clear the prompt
	prompt.SetValue("")

	height = prompt.CalculateDesiredHeight()
	if height != 2 {
		t.Errorf("CalculateDesiredHeight() after clear = %d, want 2", height)
	}
}

// TestArrowKeysInViNormalMode tests that arrow keys work in vi normal mode
func TestArrowKeysInViNormalMode(t *testing.T) {
	prompt := NewPromptComponent(80, 5)

	// Set some initial text
	prompt.SetValue("Hello World")

	// Enter normal mode
	prompt.EnterViNormalMode()

	// Test that we're in normal mode
	if !prompt.IsViNormalMode() {
		t.Fatal("Expected to be in vi normal mode")
	}

	// Move cursor to start
	prompt.TextArea.SetCursor(0)

	// Test right arrow key
	rightMsg := tea.KeyMsg{Type: tea.KeyRight}
	prompt, _ = prompt.Update(rightMsg)

	// The cursor should have moved (we can't directly check cursor position,
	// but we can verify the update didn't fail and we're still in normal mode)
	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode after right arrow")
	}

	// Test left arrow key
	leftMsg := tea.KeyMsg{Type: tea.KeyLeft}
	prompt, _ = prompt.Update(leftMsg)

	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode after left arrow")
	}

	// Test down arrow key
	downMsg := tea.KeyMsg{Type: tea.KeyDown}
	prompt, _ = prompt.Update(downMsg)

	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode after down arrow")
	}

	// Test up arrow key
	upMsg := tea.KeyMsg{Type: tea.KeyUp}
	prompt, _ = prompt.Update(upMsg)

	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode after up arrow")
	}
}

// TestViMovementKeys tests that vi movement keys (h, j, k, l) work in normal mode
func TestViMovementKeys(t *testing.T) {
	prompt := NewPromptComponent(80, 5)

	// Set some initial text with multiple lines
	prompt.SetValue("Line 1\nLine 2\nLine 3")

	// Enter normal mode
	prompt.EnterViNormalMode()

	// Test h (left)
	hMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}}
	prompt, _ = prompt.Update(hMsg)
	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode after 'h'")
	}

	// Test l (right)
	lMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}}
	prompt, _ = prompt.Update(lMsg)
	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode after 'l'")
	}

	// Test j (down)
	jMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	prompt, _ = prompt.Update(jMsg)
	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode after 'j'")
	}

	// Test k (up)
	kMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	prompt, _ = prompt.Update(kMsg)
	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode after 'k'")
	}
}

// TestTextInputBlockedInNormalMode tests that regular text input is blocked in normal mode
func TestTextInputBlockedInNormalMode(t *testing.T) {
	prompt := NewPromptComponent(80, 5)

	// Set some initial text
	initialText := "Hello"
	prompt.SetValue(initialText)

	// Enter normal mode
	prompt.EnterViNormalMode()

	// Try to type a regular character (should be blocked)
	aMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	prompt, _ = prompt.Update(aMsg)

	// The text should not have changed (unless 'a' triggered insert mode)
	// Since 'a' is an action key that enters insert mode, let's test with a different character

	// Reset to normal mode
	prompt.SetValue(initialText)
	prompt.EnterViNormalMode()

	// Try a character that should be blocked (like 'z' which has no vi command)
	zMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}}
	prompt, _ = prompt.Update(zMsg)

	// Should still be in normal mode
	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode after blocked character")
	}

	// Text should not have changed
	if prompt.Value() != initialText {
		t.Errorf("Text should not have changed. Expected %q, got %q", initialText, prompt.Value())
	}
}

// TestArrowKeysInInsertMode tests that arrow keys work in insert mode
func TestArrowKeysInInsertMode(t *testing.T) {
	prompt := NewPromptComponent(80, 5)

	// Set some initial text
	prompt.SetValue("Hello World")

	// Should start in insert mode
	if !prompt.IsViInsertMode() {
		t.Fatal("Expected to start in vi insert mode")
	}

	// Test right arrow key
	rightMsg := tea.KeyMsg{Type: tea.KeyRight}
	prompt, _ = prompt.Update(rightMsg)

	if !prompt.IsViInsertMode() {
		t.Error("Should still be in insert mode after right arrow")
	}

	// Test left arrow key
	leftMsg := tea.KeyMsg{Type: tea.KeyLeft}
	prompt, _ = prompt.Update(leftMsg)

	if !prompt.IsViInsertMode() {
		t.Error("Should still be in insert mode after left arrow")
	}
}

// TestAnsweringModeEditOption tests that the Edit option appears and emits the correct message
func TestAnsweringModeEditOption(t *testing.T) {
	prompt := NewPromptComponent(80, 10)

	// Create answering state with a question
	state := &AnsweringState{
		RequestID: "test-request-1",
		Title:     "Zhengming: Sage asks",
		Questions: []AnsweringQuestion{
			{
				Text:     "What would you like to do?",
				Summary:  "What to do?",
				Options:  []string{"Accept", tools.AnswerReject, "Edit", "Chat"},
				Selected: 0,
			},
		},
		Answers: make([]string, 1),
	}
	prompt.EnterAnsweringMode(state)

	// View should include "Edit" option
	view := prompt.View()
	if !strings.Contains(view, "Edit") {
		t.Error("View should contain 'Edit' option")
	}

	// Navigate down to Edit option (Options: Accept, Reject, Chat, Edit)
	// Current selected is 0 (Accept), down to 1 (Reject), down to 2 (Chat), down to 3 (Edit)
	downMsg := tea.KeyMsg{Type: tea.KeyDown}
	prompt, _ = prompt.Update(downMsg) // Selected = 1
	prompt, _ = prompt.Update(downMsg) // Selected = 2

	// Press Enter to select Edit
	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	_, cmd := prompt.Update(enterMsg)

	// Should emit AnsweringEditMsg
	if cmd == nil {
		t.Fatal("Expected cmd to emit AnsweringEditMsg")
	}

	// Execute the command to get the message
	msg := cmd()
	if editMsg, ok := msg.(AnsweringEditMsg); ok {
		if editMsg.RequestID != "test-request-1" {
			t.Errorf("Expected RequestID 'test-request-1', got %q", editMsg.RequestID)
		}
		if editMsg.Question != "What to do?" {
			t.Errorf("Expected Question 'What to do?' (summary), got %q", editMsg.Question)
		}
	} else {
		t.Fatalf("Expected AnsweringEditMsg, got %T", msg)
	}
}

// TestAnsweringModeEditUpdatesQuestion tests that UpdateAnsweringQuestion updates the current question
func TestAnsweringModeEditUpdatesQuestion(t *testing.T) {
	prompt := NewPromptComponent(80, 10)

	// Create answering state
	state := &AnsweringState{
		RequestID: "test-request-2",
		Title:     "Zhengming: Sage asks",
		Questions: []AnsweringQuestion{
			{
				Text:     "Original question text",
				Summary:  "Original",
				Options:  []string{"Yes", "No"},
				Selected: 0,
			},
		},
		Answers: make([]string, 1),
	}
	prompt.EnterAnsweringMode(state)

	// Update the question
	modifiedText := "Modified question text"
	prompt.UpdateAnsweringQuestion(modifiedText)

	// Verify the question was updated
	if prompt.answering.Questions[0].Text != modifiedText {
		t.Errorf("Expected Text %q, got %q", modifiedText, prompt.answering.Questions[0].Text)
	}
	if prompt.answering.Questions[0].Summary != modifiedText {
		t.Errorf("Expected Summary %q, got %q", modifiedText, prompt.answering.Questions[0].Summary)
	}

	// Selection should be reset to 0
	if prompt.answering.Questions[0].Selected != 0 {
		t.Errorf("Expected Selected to be reset to 0, got %d", prompt.answering.Questions[0].Selected)
	}
}

// TestHandleZhengmingPendingAppendsEditChat verifies that HandleZhengmingPending
// appends "Edit" and "Chat" to each question's Options.
func TestHandleZhengmingPendingAppendsEditChat(t *testing.T) {
	prompt := NewPromptComponent(80, 10)

	msg := court.ZhengmingPendingMsg{
		RequestID:  "zhengming-1",
		MinisterID: "chancellor",
		Questions: storage.ZhengmingQuestions{
			{Text: "Which approach?", Summary: "Approach?", Options: []string{"Option A", "Option B"}},
			{Text: "Confirm?", Summary: "Confirm?", Options: []string{"Yes", "No"}},
		},
	}
	prompt.HandleZhengmingPending(msg)

	if prompt.answering == nil {
		t.Fatal("Expected answering state to be set")
	}
	if prompt.answering.RequestID != "zhengming-1" {
		t.Errorf("Expected RequestID 'zhengming-1', got %q", prompt.answering.RequestID)
	}
	if len(prompt.answering.Questions) != 2 {
		t.Fatalf("Expected 2 questions, got %d", len(prompt.answering.Questions))
	}

	for i, q := range prompt.answering.Questions {
		expected := len(msg.Questions[i].Options) + 2 // +Edit +Chat
		if len(q.Options) != expected {
			t.Errorf("Question %d: expected %d options, got %d", i, expected, len(q.Options))
		}
		if q.Options[len(q.Options)-2] != "Edit" {
			t.Errorf("Question %d: expected second-to-last option 'Edit', got %q", i, q.Options[len(q.Options)-2])
		}
		if q.Options[len(q.Options)-1] != "Chat" {
			t.Errorf("Question %d: expected last option 'Chat', got %q", i, q.Options[len(q.Options)-1])
		}
	}
}

// TestAnsweringModeChatOption verifies that selecting "Chat" arms chat-capture
// instead of immediately answering with the AnswerChat sentinel.
func TestAnsweringModeChatOption(t *testing.T) {
	prompt := NewPromptComponent(80, 10)

	state := &AnsweringState{
		RequestID: "test-chat-1",
		Title:     "Zhengming: Sage asks",
		Questions: []AnsweringQuestion{
			{
				Text:     "Pick one",
				Summary:  "Pick",
				Options:  []string{"Accept", tools.AnswerReject, "Edit", "Chat"},
				Selected: 0,
			},
		},
		Answers: make([]string, 1),
	}
	prompt.EnterAnsweringMode(state)

	// Navigate to "Chat" (index 3)
	downMsg := tea.KeyMsg{Type: tea.KeyDown}
	prompt, _ = prompt.Update(downMsg) // Selected = 1
	prompt, _ = prompt.Update(downMsg) // Selected = 2
	prompt, _ = prompt.Update(downMsg) // Selected = 3 (Chat)

	// Press Enter to select Chat
	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	_, cmd := prompt.Update(enterMsg)

	if cmd == nil {
		t.Fatal("Expected cmd to emit AnsweringChatMsg")
	}
	msg := cmd()
	chatMsg, ok := msg.(AnsweringChatMsg)
	if !ok {
		t.Fatalf("Expected AnsweringChatMsg, got %T", msg)
	}
	if chatMsg.RequestID != "test-chat-1" {
		t.Errorf("Expected RequestID 'test-chat-1', got %q", chatMsg.RequestID)
	}
}

// TestCalculateDesiredHeightAnsweringMode verifies the height calculation in answering mode.
func TestCalculateDesiredHeightAnsweringMode(t *testing.T) {
	prompt := NewPromptComponent(80, 10)
	prompt.SetScreenHeight(40) // MaxHeight = 20

	state := &AnsweringState{
		RequestID: "test-height-1",
		Title:     "Zhengming",
		Questions: []AnsweringQuestion{
			{
				Text:    "Question?",
				Summary: "Q?",
				Options: []string{"A", "B", "C"},
			},
		},
		Answers: make([]string, 1),
	}
	prompt.EnterAnsweringMode(state)

	// Expected: 3 (title + blank + question) + 3 (options) = 6
	height := prompt.CalculateDesiredHeight()
	if height != 6 {
		t.Errorf("Expected height 6 (3 + 3 options), got %d", height)
	}
}

// getCursorPos returns the current (row, col) of the textarea cursor.
func getCursorPos(p PromptComponent) (int, int) {
	row := p.TextArea.Line()
	li := p.TextArea.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	return row, col
}

// TestWordBackwardEmptyBuffer tests that wordBackward on an empty buffer
// does not infinite-loop (the original bubbles wordLeft() bug).
func TestWordBackwardEmptyBuffer(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetValue("")
	prompt.TextArea.SetCursor(0)

	// This should return immediately without hanging
	prompt.wordBackward()

	row, col := getCursorPos(prompt)
	if row != 0 || col != 0 {
		t.Errorf("Expected cursor at (0,0), got (%d,%d)", row, col)
	}
}

// TestWordBackwardLeadingWhitespace tests wordBackward with leading whitespace
// at (0,0) — another trigger for the original infinite-loop bug.
func TestWordBackwardLeadingWhitespace(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetValue("   hello")
	prompt.TextArea.SetCursor(0)

	prompt.wordBackward()

	row, col := getCursorPos(prompt)
	if row != 0 || col != 0 {
		t.Errorf("Expected cursor at (0,0), got (%d,%d)", row, col)
	}
}

// TestWordBackwardSimpleWord moves cursor from end of "hello world" to start of "hello".
func TestWordBackwardSimpleWord(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetValue("hello world")
	prompt.TextArea.SetCursor(len("hello world")) // cursor at end

	prompt.wordBackward()

	row, col := getCursorPos(prompt)
	if row != 0 || col != 6 { // "world" starts at index 6
		t.Errorf("Expected cursor at (0,6), got (%d,%d)", row, col)
	}

	// Move back once more to reach "hello"
	prompt.wordBackward()

	row, col = getCursorPos(prompt)
	if row != 0 || col != 0 {
		t.Errorf("Expected cursor at (0,0), got (%d,%d)", row, col)
	}
}

// TestWordBackwardMultiline tests wordBackward crossing line boundaries.
func TestWordBackwardMultiline(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetValue("foo bar\nbaz qux")
	prompt.TextArea.SetCursor(0)
	// Move cursor to "qux" on line 1 (col 8 = "baz qux", "qux" starts at index 4)
	prompt.TextArea.SetCursor(4) // on line 0, col 4
	// Move down to line 1
	prompt.TextArea.CursorDown()

	prompt.wordBackward()

	row, col := getCursorPos(prompt)
	// "baz" starts at index 0 on line 1
	if row != 1 || col != 0 {
		t.Errorf("Expected cursor at (1,0), got (%d,%d)", row, col)
	}

	// Move back again — should cross to end of line 0
	prompt.wordBackward()

	row, col = getCursorPos(prompt)
	// "bar" starts at index 4 on line 0
	if row != 0 || col != 4 {
		t.Errorf("Expected cursor at (0,4), got (%d,%d)", row, col)
	}
}

// TestWordBackwardInsertModeAltLeft tests the alt+left key binding in insert mode.
func TestWordBackwardInsertModeAltLeft(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetValue("hello world")
	prompt.TextArea.SetCursor(len("hello world"))

	// Press alt+left in insert mode
	prompt, _ = prompt.Update(tea.KeyMsg{
		Type: tea.KeyLeft,
		Alt:  true,
	})

	row, col := getCursorPos(prompt)
	if row != 0 || col != 6 {
		t.Errorf("Expected cursor at (0,6) after alt+left, got (%d,%d)", row, col)
	}
}

// TestWordBackwardNormalModeB tests the "b" key in vi normal mode.
func TestWordBackwardNormalModeB(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetValue("hello world")
	prompt.TextArea.SetCursor(len("hello world"))
	prompt.EnterViNormalMode()

	// Press "b" in normal mode
	bMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}
	prompt, _ = prompt.Update(bMsg)

	row, col := getCursorPos(prompt)
	if row != 0 || col != 6 {
		t.Errorf("Expected cursor at (0,6) after 'b', got (%d,%d)", row, col)
	}
}

// TestWordBackwardNormalModeBEmptyBuffer tests that "b" in normal mode
// with an empty buffer doesn't hang.
func TestWordBackwardNormalModeBEmptyBuffer(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetValue("")
	prompt.EnterViNormalMode()

	bMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}
	prompt, _ = prompt.Update(bMsg)

	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode")
	}
}

// TestWordBackwardNormalModeBLeadingWhitespace tests "b" in normal mode
// with leading whitespace at (0,0).
func TestWordBackwardNormalModeBLeadingWhitespace(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetValue("   hello")
	prompt.TextArea.SetCursor(0)
	prompt.EnterViNormalMode()

	bMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}
	prompt, _ = prompt.Update(bMsg)

	row, col := getCursorPos(prompt)
	if row != 0 || col != 0 {
		t.Errorf("Expected cursor at (0,0), got (%d,%d)", row, col)
	}
}

// TestWordBackwardDoesNotInterfereWithDB verifies that pressing "b" after "d"
// is treated as a compound command (db), not as a standalone word-backward.
func TestWordBackwardDoesNotInterfereWithDB(t *testing.T) {
	prompt := NewPromptComponent(80, 5)
	prompt.SetValue("hello world")
	prompt.TextArea.SetCursor(len("hello world"))
	prompt.EnterViNormalMode()

	// Press "d" to start a compound command
	dMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}
	prompt, _ = prompt.Update(dMsg)

	if prompt.viPendingOp != "d" {
		t.Fatalf("Expected pending op 'd', got %q", prompt.viPendingOp)
	}

	// Press "b" — should be consumed as part of "db", not as word-backward
	bMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}
	prompt, _ = prompt.Update(bMsg)

	// The pending op should be cleared (compound command completed)
	if prompt.viPendingOp != "" {
		t.Errorf("Expected pending op cleared, got %q", prompt.viPendingOp)
	}

	// Should still be in normal mode
	if !prompt.IsViNormalMode() {
		t.Error("Should still be in normal mode after db")
	}
}

// TestCalculateDesiredHeightAnsweringModeLongOptions tests that the height
// calculation accounts for word-wrapped multi-line option text.
func TestCalculateDesiredHeightAnsweringModeLongOptions(t *testing.T) {
	prompt := NewPromptComponent(80, 10)
	prompt.SetScreenHeight(40) // MaxHeight = 20

	// Width = 80, contentWidth = 78, optionWidth = 74
	// A 140-char option should wrap to 2 lines at width 74
	longOption := strings.Repeat("word ", 28) // ~140 chars
	shortQuestion := "Pick one"

	state := &AnsweringState{
		RequestID: "test-long-opts",
		Title:     "Zhengming",
		Questions: []AnsweringQuestion{
			{
				Text:    shortQuestion,
				Summary: shortQuestion,
				Options: []string{longOption, "Short", longOption},
			},
		},
		Answers: make([]string, 1),
	}
	prompt.EnterAnsweringMode(state)

	// title(1) + blank(1) + question(1) + option[0](2) + option[1](1) + option[2](2) = 8
	height := prompt.CalculateDesiredHeight()
	if height != 8 {
		t.Errorf("Expected height 8 (accounting for wrapped options), got %d", height)
	}
}

// TestViewAnsweringHeightMatchesCalculateDesiredHeight verifies that the
// rendered viewAnswering() output height matches CalculateDesiredHeight()
// when options are long and wrap to multiple lines.
func TestViewAnsweringHeightMatchesCalculateDesiredHeight(t *testing.T) {
	prompt := NewPromptComponent(80, 10)
	prompt.SetScreenHeight(40)

	longOption := strings.Repeat("word ", 28) // ~140 chars

	state := &AnsweringState{
		RequestID: "test-view-height",
		Title:     "Zhengming: Sage asks",
		Questions: []AnsweringQuestion{
			{
				Text:    "Which approach do you prefer for handling the edge case?",
				Summary: "Which approach?",
				Options: []string{longOption, "Short option", longOption},
			},
		},
		Answers: make([]string, 1),
	}
	prompt.EnterAnsweringMode(state)

	desired := prompt.CalculateDesiredHeight()
	prompt.SetHeight(desired) // simulate what View() in TUIModel does

	// Render the view and count content lines (inside the border)
	view := prompt.View()
	// The view is rendered with a bordered style. Strip ANSI and count lines.
	plain := stripANSI(view)
	// lipgloss adds border lines (top and bottom), so content lines = total - 2
	totalLines := strings.Count(plain, "\n") + 1
	contentLines := totalLines - 2 // subtract top and bottom border

	if contentLines != desired {
		t.Errorf("viewAnswering content height %d != CalculateDesiredHeight %d", contentLines, desired)
	}
}

// TestCalculateDesiredHeightAnsweringModeLongQuestion tests that the height
// calculation accounts for word-wrapped multi-line question text. This is
// the case the original e643 fix was feared to miss — when the question
// itself (not just the options) spans more than one line.
func TestCalculateDesiredHeightAnsweringModeLongQuestion(t *testing.T) {
	prompt := NewPromptComponent(80, 10)
	prompt.SetScreenHeight(40) // MaxHeight = 20

	// Width = 80, contentWidth = 78
	// A 200-char question wraps to 3 lines at width 78
	longQuestion := strings.Repeat("word ", 40) // ~200 chars

	state := &AnsweringState{
		RequestID: "test-long-q",
		Title:     "Zhengming",
		Questions: []AnsweringQuestion{
			{
				Text:    longQuestion,
				Summary: longQuestion,
				Options: []string{"Yes", "No"},
			},
		},
		Answers: make([]string, 1),
	}
	prompt.EnterAnsweringMode(state)

	// Count the wrapped question lines
	wrapped := wordwrap.String(longQuestion, 78)
	questionLines := strings.Count(wrapped, "\n") + 1

	// title(1) + blank(1) + questionLines + option[0](1) + option[1](1)
	expected := 2 + questionLines + 2

	height := prompt.CalculateDesiredHeight()
	if height != expected {
		t.Errorf("Expected height %d (2 + %d question lines + 2 options), got %d",
			expected, questionLines, height)
	}
}

// TestViewAnsweringHeightMatchesCalculateDesiredHeightLongQuestion verifies
// that the rendered viewAnswering() output height matches
// CalculateDesiredHeight() when the question text wraps to multiple lines.
func TestViewAnsweringHeightMatchesCalculateDesiredHeightLongQuestion(t *testing.T) {
	prompt := NewPromptComponent(80, 10)
	prompt.SetScreenHeight(40)

	longQuestion := strings.Repeat("word ", 40) // ~200 chars, wraps to 3 lines

	state := &AnsweringState{
		RequestID: "test-view-long-q",
		Title:     "Zhengming: Sage asks",
		Questions: []AnsweringQuestion{
			{
				Text:    longQuestion,
				Summary: longQuestion,
				Options: []string{"Accept", "Reject", "Edit", "Chat"},
			},
		},
		Answers: make([]string, 1),
	}
	prompt.EnterAnsweringMode(state)

	desired := prompt.CalculateDesiredHeight()
	prompt.SetHeight(desired)

	view := prompt.View()
	plain := stripANSI(view)
	totalLines := strings.Count(plain, "\n") + 1
	contentLines := totalLines - 2 // subtract borders

	if contentLines != desired {
		t.Errorf("viewAnswering content height %d != CalculateDesiredHeight %d (long question)",
			contentLines, desired)
	}
}

// TestCalculateDesiredHeightAnsweringModeExplicitNewlineQuestion tests that
// a question with explicit \n characters is properly counted as multi-line.
func TestCalculateDesiredHeightAnsweringModeExplicitNewlineQuestion(t *testing.T) {
	prompt := NewPromptComponent(80, 10)
	prompt.SetScreenHeight(40)

	multiLineQuestion := "First line of question\nSecond line of question\nThird line of question"

	state := &AnsweringState{
		RequestID: "test-newline-q",
		Title:     "Zhengming",
		Questions: []AnsweringQuestion{
			{
				Text:    multiLineQuestion,
				Summary: multiLineQuestion,
				Options: []string{"Yes", "No"},
			},
		},
		Answers: make([]string, 1),
	}
	prompt.EnterAnsweringMode(state)

	// 3 explicit lines + 2 (title+blank) + 2 (options) = 7
	height := prompt.CalculateDesiredHeight()
	if height != 7 {
		t.Errorf("Expected height 7 (2 + 3 question lines + 2 options), got %d", height)
	}

	// Verify rendered output matches
	prompt.SetHeight(height)
	view := prompt.View()
	plain := stripANSI(view)
	totalLines := strings.Count(plain, "\n") + 1
	contentLines := totalLines - 2

	if contentLines != height {
		t.Errorf("viewAnswering content height %d != CalculateDesiredHeight %d (explicit newlines)",
			contentLines, height)
	}
}

// stripANSI removes ANSI escape sequences from a string for line counting.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip escape sequence
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++ // skip 'm'
			}
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}
