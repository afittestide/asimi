package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSearchModels returns a small set of models suitable for search dispatch tests
func testSearchModels() []Model {
	return []Model{
		{ID: "claude-3-5-sonnet", DisplayName: "Claude 3.5 Sonnet", Provider: "anthropic", Status: "ready"},
		{ID: "gpt-4o", DisplayName: "GPT-4o", Provider: "openai", Status: "ready"},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini", Provider: "openai", Status: "ready"},
		{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Provider: "googleai", Status: "ready"},
	}
}

// showModelsInTUI sets up the TUI model in the ViewModels state with the given models.
func showModelsInTUI(t *testing.T, models []Model, currentModel string) *TUIModel {
	t.Helper()
	model := newTestModel(t)
	cmd := model.tabs.Content().ShowUnifiedModels(models, currentModel, nil)
	require.NotNil(t, cmd)
	msg := cmd()
	newModel, cmd := model.Update(msg)
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)
	require.Nil(t, cmd)
	require.Equal(t, ViewModels, updatedModel.tabs.Content().GetActiveView())
	return &updatedModel
}

// TestSearchExecutedMsg_FindsMatch verifies that searchExecutedMsg dispatch in
// handleCustomMessages calls ModelsWindow.Search and updates selectedItem.
func TestSearchExecutedMsg_FindsMatch(t *testing.T) {
	model := showModelsInTUI(t, testSearchModels(), "")

	// selectedItem should be 0 by default
	require.Equal(t, 0, model.tabs.Content().selectedItem)

	// Dispatch searchExecutedMsg for "gpt" forward
	newModel, cmd := model.handleCustomMessages(searchExecutedMsg{pattern: "gpt", direction: 1})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)
	require.Nil(t, cmd)

	// Should have landed on index 1 (gpt-4o)
	assert.Equal(t, 1, updatedModel.tabs.Content().selectedItem)
	// Should have match state
	assert.True(t, updatedModel.tabs.Content().models.HasSearch())
	assert.Equal(t, 2, updatedModel.tabs.Content().models.MatchCount())
}

// TestSearchExecutedMsg_NoMatch verifies that a search with no matches shows a toast
// and does not change selectedItem.
func TestSearchExecutedMsg_NoMatch(t *testing.T) {
	model := showModelsInTUI(t, testSearchModels(), "")
	originalSelected := model.tabs.Content().selectedItem

	newModel, cmd := model.handleCustomMessages(searchExecutedMsg{pattern: "nonexistent", direction: 1})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)
	require.Nil(t, cmd)

	// selectedItem should not change
	assert.Equal(t, originalSelected, updatedModel.tabs.Content().selectedItem)
	// Should have a toast about pattern not found
	assert.NotEmpty(t, updatedModel.commandLine.toasts)
}

// TestSearchExecutedMsg_UpdatesScrollOffset verifies that scroll offset is adjusted
// when the match is beyond the visible window.
func TestSearchExecutedMsg_UpdatesScrollOffset(t *testing.T) {
	// Create enough models to require scrolling
	models := make([]Model, 20)
	for i := range models {
		models[i] = Model{
			ID:          "model-" + string(rune('a'+i)),
			DisplayName: "Model " + string(rune('A'+i)),
			Provider:    "anthropic",
			Status:      "ready",
		}
	}
	// Put a unique searchable model at the end
	models[19] = Model{ID: "zzz-found", DisplayName: "ZZZ Found", Provider: "anthropic", Status: "ready"}

	model := showModelsInTUI(t, models, "")
	model.tabs.Content().SetSize(80, 10)

	newModel, cmd := model.handleCustomMessages(searchExecutedMsg{pattern: "zzz", direction: 1})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)
	require.Nil(t, cmd)

	// Should have selected the match at index 19
	assert.Equal(t, 19, updatedModel.tabs.Content().selectedItem)
	// Scroll offset should have been adjusted
	assert.Greater(t, updatedModel.tabs.Content().scrollOffset, 0)
}

// TestSearchCancelledMsg_NoOp verifies that searchCancelledMsg is a no-op
// and does not change view or state.
func TestSearchCancelledMsg_NoOp(t *testing.T) {
	model := showModelsInTUI(t, testSearchModels(), "")

	newModel, cmd := model.handleCustomMessages(searchCancelledMsg{})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)
	require.Nil(t, cmd)

	// Should still be in ViewModels
	assert.Equal(t, ViewModels, updatedModel.tabs.Content().GetActiveView())
}

// TestSearchModeKeyDispatch_Forward verifies that '/' key in ViewModels
// triggers EnterSearchMode(1).
func TestSearchModeKeyDispatch_Forward(t *testing.T) {
	model := showModelsInTUI(t, testSearchModels(), "")
	model.Mode = "models"

	newModel, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)

	// Should enter search mode
	assert.True(t, updatedModel.commandLine.IsInSearchMode())
	assert.Equal(t, 1, updatedModel.commandLine.GetSearchDirection())

	// cmd should produce a ChangeModeMsg
	if cmd != nil {
		msg := cmd()
		modeMsg, ok := msg.(ChangeModeMsg)
		require.True(t, ok)
		assert.Equal(t, "search", modeMsg.NewMode)
	}
}

// TestSearchModeKeyDispatch_Backward verifies that '?' key in ViewModels
// triggers EnterSearchMode(-1).
func TestSearchModeKeyDispatch_Backward(t *testing.T) {
	model := showModelsInTUI(t, testSearchModels(), "")
	model.Mode = "models"

	newModel, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)

	// Should enter search mode with backward direction
	assert.True(t, updatedModel.commandLine.IsInSearchMode())
	assert.Equal(t, -1, updatedModel.commandLine.GetSearchDirection())

	if cmd != nil {
		msg := cmd()
		modeMsg, ok := msg.(ChangeModeMsg)
		require.True(t, ok)
		assert.Equal(t, "search", modeMsg.NewMode)
	}
}

// TestSearchModeKeyDispatch_NotInModelsView verifies that '/' does not trigger
// search mode when not in ViewModels.
func TestSearchModeKeyDispatch_NotInModelsView(t *testing.T) {
	model := newTestModel(t)
	// Default view is ViewChat
	require.Equal(t, ViewChat, model.tabs.Content().GetActiveView())

	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)

	// Should NOT be in search mode
	assert.False(t, updatedModel.commandLine.IsInSearchMode())
}

// TestContentListNavigation_N_NextMatch verifies that 'n' key in ViewModels
// with active search navigates to the next match.
func TestContentListNavigation_N_NextMatch(t *testing.T) {
	model := showModelsInTUI(t, testSearchModels(), "")

	// Execute a search for "gpt" — matches at index 1 and 2
	newModel, _ := model.handleCustomMessages(searchExecutedMsg{pattern: "gpt", direction: 1})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)
	require.Equal(t, 1, updatedModel.tabs.Content().selectedItem)

	// Press 'n' to go to next match (should be index 2)
	newModel2, _ := updatedModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	updatedModel2, ok := newModel2.(TUIModel)
	require.True(t, ok)
	assert.Equal(t, 2, updatedModel2.tabs.Content().selectedItem)

	// Press 'n' again — should wrap to index 1
	newModel3, _ := updatedModel2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	updatedModel3, ok := newModel3.(TUIModel)
	require.True(t, ok)
	assert.Equal(t, 1, updatedModel3.tabs.Content().selectedItem)
}

// TestContentListNavigation_N_PreviousMatch verifies that 'N' key in ViewModels
// with active search navigates to the previous match.
func TestContentListNavigation_N_PreviousMatch(t *testing.T) {
	model := showModelsInTUI(t, testSearchModels(), "")

	// Search for "gpt" — lands on index 1
	newModel, _ := model.handleCustomMessages(searchExecutedMsg{pattern: "gpt", direction: 1})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)
	require.Equal(t, 1, updatedModel.tabs.Content().selectedItem)

	// Press 'N' (shift+n) — should wrap to index 2 (previous in backward direction)
	newModel2, _ := updatedModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	updatedModel2, ok := newModel2.(TUIModel)
	require.True(t, ok)
	assert.Equal(t, 2, updatedModel2.tabs.Content().selectedItem)
}

// TestContentListNavigation_N_NoSearch verifies that 'n' without active search
// does not change selection.
func TestContentListNavigation_N_NoSearch(t *testing.T) {
	model := showModelsInTUI(t, testSearchModels(), "")
	originalSelected := model.tabs.Content().selectedItem

	// Press 'n' without any active search — should be no-op
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)
	assert.Equal(t, originalSelected, updatedModel.tabs.Content().selectedItem)
}

// TestContentListNavigation_N_AdjustsScrollOffset verifies that n/N navigation
// adjusts scroll offset when the match is outside the visible window.
func TestContentListNavigation_N_AdjustsScrollOffset(t *testing.T) {
	// Create models with matches far apart
	models := make([]Model, 20)
	for i := range models {
		models[i] = Model{
			ID:          "filler-" + string(rune('a'+i)),
			DisplayName: "Filler " + string(rune('A'+i)),
			Provider:    "anthropic",
			Status:      "ready",
		}
	}
	models[0] = Model{ID: "target-a", DisplayName: "Target A", Provider: "anthropic", Status: "ready"}
	models[19] = Model{ID: "target-b", DisplayName: "Target B", Provider: "anthropic", Status: "ready"}

	model := showModelsInTUI(t, models, "")
	model.tabs.Content().SetSize(80, 10)

	// Search for "target" — should find index 0 first (forward from 0 wraps to first match > 0... actually 0 is > -1 not > 0)
	// Search forward from currentItem=0: first match > 0 is index 19 (wrap)
	// Actually, the search finds matches > currentItem=0, so index 19
	newModel, _ := model.handleCustomMessages(searchExecutedMsg{pattern: "target", direction: 1})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)

	// Now press 'n' — should go to the other match (index 0)
	newModel2, _ := updatedModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	updatedModel2, ok := newModel2.(TUIModel)
	require.True(t, ok)
	assert.Equal(t, 0, updatedModel2.tabs.Content().selectedItem)
	assert.Equal(t, 0, updatedModel2.tabs.Content().scrollOffset)
}

// TestSearchExecutedMsg_ToastMessage verifies the toast message format
// when a match is found.
func TestSearchExecutedMsg_ToastMessage(t *testing.T) {
	model := showModelsInTUI(t, testSearchModels(), "")

	newModel, _ := model.handleCustomMessages(searchExecutedMsg{pattern: "gpt", direction: 1})
	updatedModel, ok := newModel.(TUIModel)
	require.True(t, ok)

	// Should have a toast with match info
	require.NotEmpty(t, updatedModel.commandLine.toasts)
	toast := updatedModel.commandLine.toasts[len(updatedModel.commandLine.toasts)-1]
	assert.Contains(t, toast.Message, "1/2")
	assert.Contains(t, toast.Message, "gpt")
	assert.Equal(t, "info", toast.Type)
	assert.Equal(t, 3*time.Second, toast.Timeout)
}
