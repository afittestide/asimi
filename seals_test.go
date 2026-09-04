package main

import (
	"strings"
	"testing"

	"github.com/afittestide/asimi/storage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEdictSelectWindowDefaults(t *testing.T) {
	window := NewEdictSelectWindow()

	assert.Equal(t, 70, window.Width)
	assert.Equal(t, 15, window.Height)
	assert.Empty(t, window.Items)
}

func TestEdictSelectWindowRenderList_ShowsBothSeals(t *testing.T) {
	window := NewEdictSelectWindow()
	window.SetSize(80, 10)

	edicts := []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 1, Intent: "Fix the login bug"}, HasJudgeSeal: true, HasChancellorSeal: true},
	}
	window.SetItems(edicts)

	render := window.RenderList(0, 0, window.GetVisibleSlots())
	lines := strings.Split(render, "\n")

	assert.True(t, strings.Contains(lines[0], "Select edict"), "Should have title")
	assert.True(t, strings.Contains(lines[1], "[  1]"), "Should show edict number")
	assert.True(t, strings.Contains(lines[1], "👩‍⚖️"), "Should show judge seal 👩")
	assert.True(t, strings.Contains(lines[1], "☯️"), "Should show chancellor seal ☯")
	assert.True(t, strings.Contains(lines[1], "Fix the login bug"), "Should show intent")
}

func TestEdictSelectWindowRenderList_ShowsJudgeSealOnly(t *testing.T) {
	window := NewEdictSelectWindow()
	window.SetSize(80, 10)

	edicts := []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 2, Intent: "Add new feature"}, HasJudgeSeal: true, HasChancellorSeal: false},
	}
	window.SetItems(edicts)

	render := window.RenderList(0, 0, window.GetVisibleSlots())
	lines := strings.Split(render, "\n")

	assert.True(t, strings.Contains(lines[1], "[  2]"), "Should show edict number")
	assert.True(t, strings.Contains(lines[1], "👩‍⚖️"), "Should show judge seal 👩")
	assert.False(t, strings.Contains(lines[1], "☯️"), "Should NOT show chancellor seal ☯")
	assert.True(t, strings.Contains(lines[1], "  "), "Should have space where chancellor seal is absent")
}

func TestEdictSelectWindowRenderList_ShowsChancellorSealOnly(t *testing.T) {
	window := NewEdictSelectWindow()
	window.SetSize(80, 10)

	edicts := []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 3, Intent: "Refactor code"}, HasJudgeSeal: false, HasChancellorSeal: true},
	}
	window.SetItems(edicts)

	render := window.RenderList(0, 0, window.GetVisibleSlots())
	lines := strings.Split(render, "\n")

	assert.True(t, strings.Contains(lines[1], "[  3]"), "Should show edict number")
	assert.False(t, strings.Contains(lines[1], "👩‍⚖️"), "Should NOT show judge seal 👩")
	assert.True(t, strings.Contains(lines[1], "☯️"), "Should show chancellor seal ☯")
	assert.True(t, strings.Contains(lines[1], "  "), "Should have space where judge seal is absent")
}

func TestEdictSelectWindowRenderList_NoSeals(t *testing.T) {
	window := NewEdictSelectWindow()
	window.SetSize(80, 10)

	edicts := []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 4, Intent: "Pending review"}, HasJudgeSeal: false, HasChancellorSeal: false},
	}
	window.SetItems(edicts)

	render := window.RenderList(0, 0, window.GetVisibleSlots())
	lines := strings.Split(render, "\n")

	assert.True(t, strings.Contains(lines[1], "[  4]"), "Should show edict number")
	assert.False(t, strings.Contains(lines[1], "👩‍⚖️"), "Should NOT show judge seal 👩")
	assert.False(t, strings.Contains(lines[1], "☯️"), "Should NOT show chancellor seal ☯")
}

func TestEdictSelectWindowRenderList_Selection(t *testing.T) {
	window := NewEdictSelectWindow()
	window.SetSize(80, 10)

	edicts := []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 1, Intent: "First"}, HasJudgeSeal: true, HasChancellorSeal: false},
		{Edict: storage.Edict{ID: 2, Intent: "Second"}, HasJudgeSeal: true, HasChancellorSeal: true},
	}
	window.SetItems(edicts)

	render := window.RenderList(1, 0, window.GetVisibleSlots())
	lines := strings.Split(render, "\n")

	assert.True(t, strings.HasPrefix(lines[1], "  "), "Unselected item should have space prefix: %q", lines[1])
	assert.True(t, strings.Contains(lines[1], "First"), "Unselected item should show 'First'")
	assert.True(t, strings.Contains(lines[1], "👩‍⚖️"), "Unselected item should show judge seal")
	assert.False(t, strings.Contains(lines[1], "☯️"), "Unselected item should NOT show chancellor seal")

	assert.True(t, strings.HasPrefix(lines[2], "▶ "), "Selected item should have ▶ prefix: %q", lines[2])
	assert.True(t, strings.Contains(lines[2], "Second"), "Selected item should show 'Second'")
	assert.True(t, strings.Contains(lines[2], "👩‍⚖️"), "Selected item should show judge seal")
	assert.True(t, strings.Contains(lines[2], "☯️"), "Selected item should show chancellor seal")
}

func TestEdictSelectWindowRenderList_Empty(t *testing.T) {
	window := NewEdictSelectWindow()
	window.SetSize(80, 10)

	window.SetItems([]storage.ActiveEdict{})

	render := window.RenderList(0, 0, window.GetVisibleSlots())

	assert.Contains(t, render, "No active edicts")
}

func TestEdictSelectWindowRenderList_MultipleEdicts(t *testing.T) {
	window := NewEdictSelectWindow()
	window.SetSize(80, 10)

	edicts := []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 1, Intent: "All seals"}, HasJudgeSeal: true, HasChancellorSeal: true},
		{Edict: storage.Edict{ID: 2, Intent: "Judge only"}, HasJudgeSeal: true, HasChancellorSeal: false},
		{Edict: storage.Edict{ID: 3, Intent: "No seals"}, HasJudgeSeal: false, HasChancellorSeal: false},
		{Edict: storage.Edict{ID: 4, Intent: "Chancellor only"}, HasJudgeSeal: false, HasChancellorSeal: true},
	}
	window.SetItems(edicts)

	render := window.RenderList(0, 0, window.GetVisibleSlots())
	lines := strings.Split(render, "\n")

	assert.Contains(t, lines[1], "[  1]")
	assert.Contains(t, lines[1], "👩‍⚖️")
	assert.Contains(t, lines[1], "☯️")

	assert.Contains(t, lines[2], "[  2]")
	assert.Contains(t, lines[2], "👩‍⚖️")
	assert.NotContains(t, lines[2], "☯️")

	assert.Contains(t, lines[3], "[  3]")
	assert.NotContains(t, lines[3], "👩‍⚖️")
	assert.NotContains(t, lines[3], "☯️")

	assert.Contains(t, lines[4], "[  4]")
	assert.NotContains(t, lines[4], "👩‍⚖️")
	assert.Contains(t, lines[4], "☯️")
}

func TestHandleExitKeys_EscInEdictList_GoesToChat(t *testing.T) {
	c := NewContentComponent(80, 24, false)
	c.activeView = ViewEdict
	c.edictListActive = true
	c.navMode = NavList

	cmd := c.handleExitKeys(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd, "Esc should return a cmd")

	// Esc in the list itself should go to chat, not reload edicts
	msg := cmd()
	_, ok := msg.(reloadEdictsMsg)
	assert.False(t, ok, "Esc in edicts list should go to chat, not reload")
}

func TestHandleExitKeys_EscInEdictDashboardWithoutListActive_GoesToChat(t *testing.T) {
	c := NewContentComponent(80, 24, false)
	c.activeView = ViewEdict
	c.edictListActive = false
	c.navMode = NavText

	cmd := c.handleExitKeys(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd, "Esc should return a cmd")

	// Should go to chat (ChangeModeMsg), not reload edicts
	msg := cmd()
	_, ok := msg.(reloadEdictsMsg)
	assert.False(t, ok, "should not reload edicts when edictListActive is false")
}
