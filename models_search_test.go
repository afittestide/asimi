package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// testModels creates a set of models for search testing
func testModels() []Model {
	return []Model{
		{ID: "claude-3-5-sonnet", DisplayName: "Claude 3.5 Sonnet", Provider: "anthropic", Status: "ready"},
		{ID: "claude-3-5-haiku", DisplayName: "Claude 3.5 Haiku", Provider: "anthropic", Status: "ready"},
		{ID: "gpt-4o", DisplayName: "GPT-4o", Provider: "openai", Status: "ready"},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini", Provider: "openai", Status: "ready"},
		{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Provider: "googleai", Status: "ready"},
		{ID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", Provider: "googleai", Status: "ready"},
	}
}

// TestSearchForward verifies forward search finds the next match
func TestSearchForward(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// Search forward for "gpt" from item 0 — should find index 2 (gpt-4o)
	idx := window.Search("gpt", 1, 0)
	assert.Equal(t, 2, idx)
	assert.Equal(t, "gpt", window.searchPattern)
	assert.Equal(t, 2, len(window.matchIndices)) // gpt-4o, gpt-4o-mini
}

// TestSearchForwardWrap verifies forward search wraps around
func TestSearchForwardWrap(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// Search forward for "claude" from last item — should wrap to index 0
	idx := window.Search("claude", 1, 5)
	assert.Equal(t, 0, idx)
}

// TestSearchBackward verifies backward search finds the previous match
func TestSearchBackward(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// Search backward for "claude" from item 4 — should find index 1 (claude-3-5-haiku)
	idx := window.Search("claude", -1, 4)
	assert.Equal(t, 1, idx)
}

// TestSearchBackwardWrap verifies backward search wraps around
func TestSearchBackwardWrap(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// Search backward for "gemini" from item 0 — should wrap to index 5
	idx := window.Search("gemini", -1, 0)
	assert.Equal(t, 5, idx)
}

// TestSearchNoMatch verifies that searching with no matches returns -1
func TestSearchNoMatch(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	idx := window.Search("nonexistent", 1, 0)
	assert.Equal(t, -1, idx)
	assert.Equal(t, 0, len(window.matchIndices))
}

// TestSearchCaseInsensitive verifies search is case-insensitive
func TestSearchCaseInsensitive(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// Lowercase search
	idx := window.Search("GPT", 1, 0)
	assert.Equal(t, 2, idx)

	// Mixed case search
	idx = window.Search("gEmInI", 1, 0)
	assert.Equal(t, 4, idx)
}

// TestSearchClearPattern verifies that empty pattern clears search state
func TestSearchClearPattern(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// First do a search
	window.Search("gpt", 1, 0)
	assert.True(t, window.HasSearch())

	// Clear with empty pattern
	idx := window.Search("", 1, 3)
	assert.Equal(t, 3, idx) // returns currentItem unchanged
	assert.False(t, window.HasSearch())
	assert.Equal(t, 0, len(window.matchIndices))
}

// TestSearchMatchesDisplayName verifies search matches on DisplayName
func TestSearchMatchesDisplayName(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// "Flash" appears only in DisplayName of gemini-2.5-flash
	idx := window.Search("flash", 1, 0)
	assert.Equal(t, 5, idx)
}

// TestSearchMatchesID verifies search matches on ID
func TestSearchMatchesID(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// "haiku" appears in both ID and DisplayName of claude-3-5-haiku
	idx := window.Search("haiku", 1, 0)
	assert.Equal(t, 1, idx)
}

// TestNextMatchForward verifies NextMatch cycles forward through matches
func TestNextMatchForward(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// Search forward for "gpt" from item 0 — lands on index 2
	window.Search("gpt", 1, 0)

	// NextMatch should go to index 3 (gpt-4o-mini)
	idx := window.NextMatch(2, 1)
	assert.Equal(t, 3, idx)

	// NextMatch should wrap to index 2
	idx = window.NextMatch(3, 1)
	assert.Equal(t, 2, idx)
}

// TestNextMatchBackward verifies NextMatch cycles backward through matches
func TestNextMatchBackward(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// Search for "gpt" — matches at 2, 3
	window.Search("gpt", 1, 0)

	// NextMatch backward from 2 should wrap to 3
	idx := window.NextMatch(2, -1)
	assert.Equal(t, 3, idx)

	// NextMatch backward from 3 should go to 2
	idx = window.NextMatch(3, -1)
	assert.Equal(t, 2, idx)
}

// TestNextMatchNoSearch verifies NextMatch returns -1 when no search is active
func TestNextMatchNoSearch(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	idx := window.NextMatch(0, 1)
	assert.Equal(t, -1, idx)
}

// TestNextMatchMultipleCycles verifies cycling through all matches
func TestNextMatchMultipleCycles(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// Search for "claude" — matches at 0, 1
	window.Search("claude", 1, 5) // wraps to 0

	// Cycle forward: 0 -> 1 -> 0 -> 1
	assert.Equal(t, 1, window.NextMatch(0, 1))
	assert.Equal(t, 0, window.NextMatch(1, 1))
	assert.Equal(t, 1, window.NextMatch(0, 1))
}

// TestMatchCount verifies MatchCount returns correct count
func TestMatchCount(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	window.Search("gpt", 1, 0)
	assert.Equal(t, 2, window.MatchCount())

	window.Search("gemini", 1, 0)
	assert.Equal(t, 2, window.MatchCount())

	window.Search("nonexistent", 1, 0)
	assert.Equal(t, 0, window.MatchCount())
}

// TestCurrentMatchNumber verifies CurrentMatchNumber returns 1-based position
func TestCurrentMatchNumber(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	// Search for "gpt" — matches at 2, 3, cursor starts at 0 (index 2)
	window.Search("gpt", 1, 0)
	assert.Equal(t, 1, window.CurrentMatchNumber())

	// Move to next match (index 3, cursor 1)
	window.NextMatch(2, 1)
	assert.Equal(t, 2, window.CurrentMatchNumber())
}

// TestHasSearch verifies HasSearch reflects search state
func TestHasSearch(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	assert.False(t, window.HasSearch())

	window.Search("gpt", 1, 0)
	assert.True(t, window.HasSearch())

	window.ClearSearch()
	assert.False(t, window.HasSearch())
}

// TestClearSearch verifies ClearSearch resets all search state
func TestClearSearch(t *testing.T) {
	window := NewModelsWindow()
	window.SetModels(testModels(), "", nil)

	window.Search("gpt", 1, 0)
	window.ClearSearch()

	assert.Equal(t, "", window.searchPattern)
	assert.Equal(t, 0, window.searchDirection)
	assert.Nil(t, window.matchIndices)
	assert.Equal(t, 0, window.matchCursor)
}

// TestSearchRenderShowsMatchCount verifies the title shows match count during search
func TestSearchRenderShowsMatchCount(t *testing.T) {
	window := NewModelsWindow()
	window.SetSize(80, 10)
	window.SetModels(testModels(), "", nil)

	// Without search
	render := window.RenderList(0, 0, window.GetVisibleSlots())
	assert.Contains(t, render, "Select a model")
	assert.NotContains(t, render, "search")

	// With search
	window.Search("gpt", 1, 0)
	render = window.RenderList(2, 0, window.GetVisibleSlots())
	assert.Contains(t, render, "search")
	assert.Contains(t, render, "gpt")
	assert.Contains(t, render, "1/2")
}
