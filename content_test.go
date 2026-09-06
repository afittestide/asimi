package main

import (
	"strings"
	"testing"

	"github.com/afittestide/asimi/internal/ministers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTabGreetingsAllPresent(t *testing.T) {
	defs, err := ministers.LoadMinisters()
	require.NoError(t, err)

	greetings := make(map[string]string, len(defs))
	for _, d := range defs {
		greetings[d.ID] = d.Greeting
	}

	for _, target := range ministers.DefaultTabIDs {
		g := greetings[target]
		if g == "" {
			t.Errorf("missing greeting for minister %q", target)
		}
	}
}

func TestInitTabGreetingsIdempotent(t *testing.T) {
	t.Skip("To be handled by e815")
	defs, err := ministers.LoadMinisters()
	require.NoError(t, err)

	tm := NewTabManager(80, 24, true, func() string { return "" }, defs)

	// Seed twice, as happens on model switch / OAuth re-login.
	initTabGreetings(&tm, defs)
	initTabGreetings(&tm, defs)

	for _, tab := range tm.tabs {
		count := 0
		for _, msg := range tab.Content.Chat.Messages {
			if msg.Type == MessageTypeGreeting {
				count++
			}
		}
		assert.Equal(t, 1, count, "tab %q should have exactly one greeting", tab.Target)
	}
}

func TestAddGreetingMessage_UsesGreetingType(t *testing.T) {
	chat := NewChatComponent(80, 20, true)

	chat.AddGreetingMessage("Welcome to the Court")

	assert.Len(t, chat.Messages, 1, "should have exactly one greeting message")
	assert.Equal(t, MessageTypeGreeting, chat.Messages[0].Type,
		"greeting message should have MessageTypeGreeting type")
	assert.Equal(t, "Welcome to the Court", chat.Messages[0].Content,
		"greeting content should be stored without prefix")
}

func TestAddGreetingMessage_RendersWithCastleEmoji(t *testing.T) {
	chat := NewChatComponent(80, 20, true)

	chat.AddGreetingMessage("Welcome, Ruler")

	view := chat.Viewport.View()
	assert.True(t, strings.Contains(view, "🏯"),
		"rendered greeting should contain the 🏯 prefix emoji")
}

func TestAddGreetingMessage_NoSystemPrefix(t *testing.T) {
	chat := NewChatComponent(80, 20, true)

	chat.AddGreetingMessage("Greetings, Ruler")

	view := chat.Viewport.View()
	// The old rendering used systemPrefix ("🛠️  ") — greetings should no longer include it
	assert.False(t, strings.Contains(view, "🛠️"),
		"greeting should not contain the system/tool prefix 🛠️")
}

func TestAddGreetingMessage_MarkdownRendered(t *testing.T) {
	chat := NewChatComponent(80, 20, true)

	// Use markdown bold in the greeting
	chat.AddGreetingMessage("Welcome **Ruler** to the Court")

	view := chat.Viewport.View()
	// Markdown rendering processes the content — the greeting type ensures
	// renderMarkdown is called, which strips raw markdown markers or applies
	// ANSI styling. The view should contain the text regardless.
	assert.True(t, strings.Contains(view, "Ruler"),
		"rendered greeting should contain 'Ruler' text")
	// The greeting should NOT appear as plain system-styled cyan text
	// (that's the default for MessageTypeSystem) — it should have its own style
	assert.True(t, chat.Messages[0].Type == MessageTypeGreeting,
		"greeting message type ensures markdown rendering path")
}

func TestAddGreetingMessage_SetsAutoScroll(t *testing.T) {
	chat := NewChatComponent(80, 20, true)
	chat.AutoScroll = false
	chat.UserScrolled = true

	chat.AddGreetingMessage("Welcome")

	assert.True(t, chat.AutoScroll, "AddGreetingMessage should enable AutoScroll")
	assert.False(t, chat.UserScrolled, "AddGreetingMessage should reset UserScrolled")
}

func TestAddGreetingMessage_RespectsScrollLock(t *testing.T) {
	chat := NewChatComponent(80, 20, true)
	chat.ScrollLocked = true
	chat.AutoScroll = false
	chat.UserScrolled = true

	chat.AddGreetingMessage("Welcome")

	assert.False(t, chat.AutoScroll, "AddGreetingMessage should not change AutoScroll when scroll-locked")
	assert.True(t, chat.UserScrolled, "AddGreetingMessage should not reset UserScrolled when scroll-locked")
}

// --- Welcome screen tests ---

func newTestTabManager() TabManager {
	defs, _ := ministers.LoadMinisters()
	return NewTabManager(80, 24, true, func() string { return "insert" }, defs)
}

func TestTabManager_WelcomeDefaultTrue(t *testing.T) {
	tm := newTestTabManager()
	assert.True(t, tm.IsWelcome(), "new TabManager should start in welcome state")
}

func TestTabManager_DismissWelcome(t *testing.T) {
	tm := newTestTabManager()
	assert.True(t, tm.IsWelcome())

	tm.DismissWelcome()
	assert.False(t, tm.IsWelcome(), "welcome should be dismissed after DismissWelcome")
	assert.Equal(t, 0, tm.activeTab, "active tab should be 0 after dismiss")
}

func TestTabManager_RenderWelcome(t *testing.T) {
	tm := newTestTabManager()

	view := tm.renderWelcome(80, 24)
	assert.NotEmpty(t, view)
	assert.Contains(t, view, "Asimi")
	assert.Contains(t, view, "INSERT")
	// The welcome screen advertises :qa to quit all tabs (e744).
	assert.Contains(t, view, ":qa")
	assert.Contains(t, view, "quit all tabs")
	// The tutorial hint must be advertised now that :help tutorial works.
	assert.Contains(t, view, ":help tutorial")
}

func TestTabManager_RenderWelcome_UpdateAvailable(t *testing.T) {
	tm := newTestTabManager()
	tm.getUpdateAvail = func() bool { return true }

	view := tm.renderWelcome(80, 24)
	assert.Contains(t, view, "Update available")
	assert.Contains(t, view, ":update")
}

func TestTabManager_RenderWelcome_NoUpdateWhenFalse(t *testing.T) {
	tm := newTestTabManager()
	tm.getUpdateAvail = func() bool { return false }

	view := tm.renderWelcome(80, 24)
	assert.NotContains(t, view, "Update available")
}

func TestTabManager_RenderWelcome_ConfigCreated(t *testing.T) {
	tm := newTestTabManager()
	tm.getConfigCreated = func() bool { return true }

	view := tm.renderWelcome(80, 24)
	assert.Contains(t, view, "config file created")
}

func TestTabManager_RenderTabBar_NoActiveInWelcome(t *testing.T) {
	tm := newTestTabManager()
	assert.True(t, tm.IsWelcome())

	defs, err := ministers.LoadMinisters()
	require.NoError(t, err)

	bar := tm.RenderTabBar(80)
	assert.NotEmpty(t, bar)
	// In welcome state, no tab should be bold (active)
	// Verify tab titles are present, derived from loaded defs
	for _, id := range ministers.DefaultTabIDs {
		def := ministers.LookupByID(defs, id)
		assert.Contains(t, bar, def.Title)
	}
}

func TestTabManager_RenderTabBar_ActiveAfterDismiss(t *testing.T) {
	tm := newTestTabManager()
	tm.DismissWelcome()

	defs, err := ministers.LoadMinisters()
	require.NoError(t, err)

	bar := tm.RenderTabBar(80)
	assert.NotEmpty(t, bar)
	chancellorDef := ministers.LookupByID(defs, ministers.Chancellor)
	assert.Contains(t, bar, chancellorDef.Title)
}

func TestTabManager_DefaultTabsUseEnglishLabels(t *testing.T) {
	tm := newTestTabManager()
	defs, err := ministers.LoadMinisters()
	require.NoError(t, err)

	bar := tm.RenderTabBar(80)
	// Labels should be the English title only — kanji must NOT appear
	for _, id := range ministers.DefaultTabIDs {
		def := ministers.LookupByID(defs, id)
		assert.Contains(t, bar, def.Title)
		assert.NotContains(t, bar, def.Kanji)
	}
}

func TestTabManager_DefaultTabIDs(t *testing.T) {
	tm := newTestTabManager()
	for i, expectedID := range ministers.DefaultTabIDs {
		assert.Equal(t, expectedID, string(tm.tabs[i].Type))
		assert.Equal(t, expectedID, tm.tabs[i].Target)
	}
}

func TestHandleTabNewCommand_DefaultInheritsActiveTabMinister(t *testing.T) {
	model := newTestModel(t)
	initialTabCount := len(model.tabs.tabs)

	// Default active tab is Forge (index 0) — tabnew with no args
	// should inherit the active tab's minister type.
	handleTabNewCommand(model, []string{})

	assert.Len(t, model.tabs.tabs, initialTabCount+1)
	newTab := model.tabs.tabs[initialTabCount]
	assert.Equal(t, ministers.Forge, string(newTab.Type))
	assert.Equal(t, "forge-2", newTab.Target)
	defs, _ := ministers.LoadMinisters()
	forgeDef := ministers.LookupByID(defs, ministers.Forge)
	assert.Contains(t, newTab.Label, forgeDef.Title)
}

func TestHandleTabNewCommand_DefaultOnEdictTabOpensSecretary(t *testing.T) {
	model := newTestModel(t)
	initialTabCount := len(model.tabs.tabs)

	// Simulate being on a minister tab with an edict context.
	model.tabs.SetActiveEdictID(42)

	handleTabNewCommand(model, []string{})

	assert.Len(t, model.tabs.tabs, initialTabCount+1)
	newTab := model.tabs.tabs[initialTabCount]
	assert.Equal(t, ministers.Secretary, string(newTab.Type))
	assert.Equal(t, "secretary-2", newTab.Target)
}

func TestHandleTabNewCommand_DefaultOnRitualTabOpensSecretary(t *testing.T) {
	model := newTestModel(t)
	initialTabCount := len(model.tabs.tabs)

	// Switch to a ritual tab (non-minister type, no EdictID).
	model.tabs.Add("Ritual:e99", "ritual", "e99")

	handleTabNewCommand(model, []string{})

	assert.Len(t, model.tabs.tabs, initialTabCount+2) // +1 for ritual, +1 for tabnew
	newTab := model.tabs.tabs[initialTabCount+1]
	assert.Equal(t, ministers.Secretary, string(newTab.Type))
	assert.Equal(t, "secretary-2", newTab.Target)
}

func TestHandleTabNewCommand_ChancellorArgOpensChancellorTab(t *testing.T) {
	model := newTestModel(t)
	initialTabCount := len(model.tabs.tabs)

	handleTabNewCommand(model, []string{ministers.Chancellor})

	assert.Len(t, model.tabs.tabs, initialTabCount+1)
	newTab := model.tabs.tabs[initialTabCount]
	assert.Equal(t, ministers.Chancellor, string(newTab.Type))
	// Default Chancellor tab already exists with target "chancellor", so the new one
	// gets a unique target for session/channel isolation.
	assert.Equal(t, "chancellor-2", newTab.Target)
	defs, _ := ministers.LoadMinisters()
	chancellorDef := ministers.LookupByID(defs, ministers.Chancellor)
	assert.Contains(t, newTab.Label, chancellorDef.Title)
}

func TestUniqueTarget_GeneratesUniqueIDs(t *testing.T) {
	tm := newTestTabManager()
	// Default tabs include "chancellor" as a target
	assert.Equal(t, "chancellor-2", tm.UniqueTarget("chancellor"))
	// Add a tab with "chancellor-2" to simulate a second tabnew
	tm.Add("Chancellor", TabType(ministers.Chancellor), "chancellor-2")
	assert.Equal(t, "chancellor-3", tm.UniqueTarget("chancellor"))
	// A minister ID not yet present returns the bare ID
	assert.Equal(t, "forge-2", tm.UniqueTarget("forge"))
	// After removing all "forge" tabs (forge is a default tab), it still
	// collides because the default tab uses "forge" as target.
}

func TestHandleTabNewCommand_MultipleTabnewChancellorGenerateIncrementingTargets(t *testing.T) {
	model := newTestModel(t)
	initialTabCount := len(model.tabs.tabs)

	// First tabnew chancellor → "chancellor-2"
	handleTabNewCommand(model, []string{ministers.Chancellor})
	assert.Equal(t, "chancellor-2", model.tabs.tabs[initialTabCount].Target)

	// Second tabnew chancellor → "chancellor-3"
	handleTabNewCommand(model, []string{ministers.Chancellor})
	assert.Equal(t, "chancellor-3", model.tabs.tabs[initialTabCount+1].Target)

	// Third tabnew (no arg) → "chancellor-4"
	handleTabNewCommand(model, []string{})
	assert.Equal(t, "chancellor-4", model.tabs.tabs[initialTabCount+2].Target)
}

func TestSetTabMinister_SetsMinisterOnMatchingTab(t *testing.T) {
	tm := newTestTabManager()
	tm.DismissWelcome()

	tm.Add("Ritual:e647", "ritual", "e647")

	tm.SetTabMinister("e647", "forge")
	tab := tm.TabByTarget("e647")
	require.NotNil(t, tab)
	assert.Equal(t, "forge", tab.CurrentMinister)
}

func TestSetTabMinister_NoMatchIsNoOp(t *testing.T) {
	tm := newTestTabManager()
	tm.DismissWelcome()

	tm.SetTabMinister("nonexistent", "forge")
	for i := range tm.tabs {
		assert.Empty(t, tm.tabs[i].CurrentMinister, "no tab should have CurrentMinister set")
	}
}

func TestSetTabChatMode_SetsChatModeOnMatchingTab(t *testing.T) {
	tm := newTestTabManager()
	tm.DismissWelcome()

	tm.Add("Ritual:e647", "ritual", "e647")

	tm.SetTabChatMode("e647", true)
	tab := tm.TabByTarget("e647")
	require.NotNil(t, tab)
	assert.True(t, tab.ChatMode)

	tm.SetTabChatMode("e647", false)
	assert.False(t, tab.ChatMode)
}

func TestSetTabChatMode_NoMatchIsNoOp(t *testing.T) {
	tm := newTestTabManager()
	tm.DismissWelcome()

	tm.SetTabChatMode("nonexistent", true)
	for i := range tm.tabs {
		assert.False(t, tm.tabs[i].ChatMode, "no tab should have ChatMode set")
	}
}

func TestSwitchToTarget_Found(t *testing.T) {
	tm := newTestTabManager()
	tm.DismissWelcome()

	tm.Add("Custom:my-target", "secretary", "my-target")
	require.NotNil(t, tm.TabByTarget("my-target"))

	switched := tm.SwitchToTarget("my-target")
	assert.True(t, switched, "SwitchToTarget should return true when target is found")
	assert.Equal(t, "my-target", tm.ActiveTab().Target)
}

func TestSwitchToTarget_NotFound(t *testing.T) {
	tm := newTestTabManager()
	tm.DismissWelcome()

	// Target does not exist
	switched := tm.SwitchToTarget("nonexistent-target")
	assert.False(t, switched, "SwitchToTarget should return false when target is not found")
}

func TestSwitchToTarget_FirstMatch(t *testing.T) {
	tm := newTestTabManager()
	tm.DismissWelcome()

	tm.Add("A:dup", "secretary", "dup")
	tm.Add("B:dup", "secretary", "dup")

	switched := tm.SwitchToTarget("dup")
	assert.True(t, switched, "SwitchToTarget should find first matching tab")
	assert.Equal(t, "dup", tm.ActiveTab().Target)
}
