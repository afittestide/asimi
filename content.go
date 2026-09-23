package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/ministers"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/storage"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TabType represents the type of tab connection
type TabType string

// Tab represents a TUI tab with its own content buffer and stream target
type Tab struct {
	Label           string
	Type            TabType
	Target          string             // minister ID, edict ID, or ritual run ID
	Content         ContentComponent   // Own content buffer per tab
	EdictID         uint               // Current edict ID for this tab
	Streaming       bool               // True when this tab is actively receiving stream data
	Ctx             context.Context    // per-tab context, flows to rituals for ruling tab
	Cancel          context.CancelFunc // per-tab streaming cancellation
	CurrentMinister string             // For ritual tabs: the minister running the current step
	ChatMode        bool               // For ritual tabs: true when ruler is chatting (ritual paused)
}

// NewTab creates a Tab with its own context and cancellation.
// Every tab is an independent, cancellable unit of work.
func NewTab(label string, tabType TabType, target string, content ContentComponent) Tab {
	ctx, cancel := context.WithCancel(context.Background())
	return Tab{
		Label:   label,
		Type:    tabType,
		Target:  target,
		Content: content,
		Ctx:     ctx,
		Cancel:  cancel,
	}
}

// TabManager manages multiple tabs, each wrapping a ContentComponent
type TabManager struct {
	tabs             []Tab
	activeTab        int
	pendingG         bool
	width, height    int
	markdownEnabled  bool
	getStatus        func() string
	onTabSwitch      func()      // Called after tab switch to update TUI state
	showWelcome      bool        // True until the user presses any key; shows welcome screen
	getUpdateAvail   func() bool // Returns whether an update is available
	getConfigCreated func() bool // Returns whether config was created on first run
}

// initTabGreetings seeds each tab's ChatComponent with its minister welcome
// message, sourced from the Greeting field of each MinisterDef.
func initTabGreetings(tm *TabManager, defs []ministers.MinisterDef) {
	greetings := make(map[string]string, len(defs))
	for _, d := range defs {
		greetings[d.ID] = d.Greeting
	}
	for i := range tm.tabs {
		if greeting := greetings[tm.tabs[i].Target]; greeting != "" {
			tm.tabs[i].Content.Chat.AddGreetingMessage(greeting)
		}
	}
}

// NewTabManager creates a TabManager with default tabs built from the given
// minister defs. The default tabs are Forge, Chancellor, Judge, Secretary —
// using the English title as the label, derived from the defs.
func NewTabManager(w, h int, mdEnabled bool, getStatus func() string, defs []ministers.MinisterDef) TabManager {
	defsByID := ministers.LookupMap(defs)

	var tabs []Tab
	for _, id := range ministers.DefaultTabIDs {
		d := defsByID[id]
		tabs = append(tabs, NewTab(d.Label(), TabType(id), id,
			newContentComponent(w, h, mdEnabled, getStatus)))
	}

	tm := TabManager{
		tabs:            tabs,
		activeTab:       0,
		width:           w,
		height:          h,
		markdownEnabled: mdEnabled,
		getStatus:       getStatus,
		showWelcome:     true,
	}
	// Seed each tab with its minister welcome greeting
	return tm
}

// newContentComponent is a helper to create ContentComponent with status getter
func newContentComponent(w, h int, mdEnabled bool, getStatus func() string) ContentComponent {
	c := NewContentComponent(w, h, mdEnabled)
	c.Chat.GetStatus = getStatus
	return c
}

// Content returns a pointer to the active tab's ContentComponent.
// In welcome state, callers should use RenderWelcome instead.
func (tm *TabManager) Content() *ContentComponent {
	return &tm.tabs[tm.activeTab].Content
}

// IsWelcome returns true when the TabManager is showing the welcome screen.
func (tm *TabManager) IsWelcome() bool {
	return tm.showWelcome
}

// DismissWelcome hides the welcome screen and ensures the default tab
func (tm *TabManager) DismissWelcome() {
	tm.showWelcome = false
	tm.activeTab = 0
}

// renderWelcome renders the welcome screen shown before the user starts interacting.
func (tm *TabManager) renderWelcome(width, height int) string {

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(globalTheme.PromptBorder).
		Align(lipgloss.Center).
		Width(width)

	title := titleStyle.Render("Asimi - A sandboxed imperial court for project rulers")

	versionStyle := lipgloss.NewStyle().
		Foreground(globalTheme.DimTextColor).
		Align(lipgloss.Center).
		Width(width)

	versionDisplay := versionStyle.Render("Version: " + utils.AsimiVersion)

	commands := []string{
		"▶ vi-like UI",
		"▶ Starts in INSERT mode (press ESC for NORMAL)",
		"▶ Prompt using the royal 'We'",
		"▶ `:qa` to quit all tabs and exit",
		"▶ `CTRL-C` to stop the model, twice to exit",
		"▶ `ESC` to switch modes",
		"▶ `:help tutorial` for the tutorial",
		"▶ `CTRL-B/F` to scroll the chat",
		"▶ `:!uname` to run uname in the sandbox's shell",
		"⇒ `TAB` to switch tabs",
	}

	commandStyle := lipgloss.NewStyle().
		Foreground(globalTheme.ChatBorder).
		PaddingLeft(2)

	subStyle := lipgloss.NewStyle().
		Foreground(globalTheme.TextColor).
		Align(lipgloss.Center).
		Width(width)

	var commandViews []string
	for _, command := range commands {
		commandViews = append(commandViews, commandStyle.Render(command))
	}

	contentParts := []string{
		subStyle.Render("🎂  Happy 50th Birthday to visual mode  🎂")}

	contentParts = append(contentParts, lipgloss.JoinVertical(
		lipgloss.Left, commandViews...))

	if tm.getUpdateAvail != nil && tm.getUpdateAvail() {
		updateStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(globalTheme.SuccessColor).
			Align(lipgloss.Center).
			Width(width)
		contentParts = append(contentParts,
			updateStyle.Render("🚀 Update available! Run :update to install the latest version"))
	}
	if tm.getConfigCreated != nil && tm.getConfigCreated() {
		configStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(globalTheme.InfoColor).
			Align(lipgloss.Center).
			Width(width)
		contentParts = append(contentParts,
			configStyle.Render("📝 User's config file created at ~/.config/asimi/asimi.conf"))
	}

	contentParts = append([]string{title, versionDisplay}, contentParts...)

	content := lipgloss.JoinVertical(lipgloss.Center, contentParts...)

	container := lipgloss.NewStyle().
		Width(width).
		Height(height).
		MaxHeight(height). // Truncate when content overflows allocated height
		Align(lipgloss.Center, lipgloss.Center).
		Render(content)

	return container
}

// StreamingChat returns the ChatComponent receiving stream data.
// If the active tab is streaming, return it. Otherwise find any streaming tab.
// Falls back to the active tab if nothing is streaming.
func (tm *TabManager) StreamingChat() *ChatComponent {
	if tm.tabs[tm.activeTab].Streaming {
		return tm.tabs[tm.activeTab].Content.Chat
	}
	for i := range tm.tabs {
		if tm.tabs[i].Streaming {
			return tm.tabs[i].Content.Chat
		}
	}
	return tm.tabs[tm.activeTab].Content.Chat
}

// StreamingChatByTab returns the ChatComponent for a specific tab.
// Matches by tab.Target == tabID && tab.Streaming.
// Falls back to StreamingChat() if no match is found.
func (tm *TabManager) StreamingChatByTab(tabID string) *ChatComponent {
	if tabID != "" {
		for i := range tm.tabs {
			if tm.tabs[i].Target == tabID && tm.tabs[i].Streaming {
				return tm.tabs[i].Content.Chat
			}
		}
	}
	return tm.StreamingChat()
}

// ChatByTab returns the ChatComponent for the tab matching target.
// Falls back to active tab if no match.
func (tm *TabManager) ChatByTab(tabID string) *ChatComponent {
	if tabID != "" {
		for i := range tm.tabs {
			if tm.tabs[i].Target == tabID {
				return tm.tabs[i].Content.Chat
			}
		}
	}
	return tm.tabs[tm.activeTab].Content.Chat
}

// FlushDirtyChats calls UpdateContent on every dirty tab chat. Used by the
// debounce tick to flush all streaming tabs, not just the active one —
// chunks can land on a non-active tab.
func (tm *TabManager) FlushDirtyChats() {
	for i := range tm.tabs {
		chat := tm.tabs[i].Content.Chat
		if chat != nil && chat.contentDirty {
			chat.UpdateContent()
		}
	}
}

// SetStreamingTabByTab marks the tab with matching Target as streaming
func (tm *TabManager) SetStreamingTabByTab(tabID string) {
	for i := range tm.tabs {
		if tm.tabs[i].Target == tabID {
			tm.tabs[i].Streaming = true
			return
		}
	}
	tm.tabs[tm.activeTab].Streaming = true // fallback
}

// ClearStreamingByTab clears the streaming flag on the tab with matching Target
func (tm *TabManager) ClearStreamingByTab(tabID string) {
	for i := range tm.tabs {
		if tm.tabs[i].Target == tabID {
			tm.tabs[i].Streaming = false
			return
		}
	}
}

// SetTabMinister sets the CurrentMinister field on the tab matching tabID.
func (tm *TabManager) SetTabMinister(tabID, ministerID string) {
	for i := range tm.tabs {
		if tm.tabs[i].Target == tabID {
			tm.tabs[i].CurrentMinister = ministerID
			return
		}
	}
}

// SetTabChatMode sets the ChatMode flag on the tab matching tabID.
func (tm *TabManager) SetTabChatMode(tabID string, chatMode bool) {
	for i := range tm.tabs {
		if tm.tabs[i].Target == tabID {
			tm.tabs[i].ChatMode = chatMode
			return
		}
	}
}

// CancelTabByID cancels streaming on the tab matching the given target.
func (tm *TabManager) CancelTabByID(tabID string) {
	for i := range tm.tabs {
		tab := &tm.tabs[i]
		if tab.Target == tabID {
			if tab.Cancel != nil {
				tab.Cancel()
			}
			tab.Ctx, tab.Cancel = context.WithCancel(context.Background())
			slog.Debug("Created a new cancelable context", "tab", tabID)
			tab.Streaming = false
			return
		}
	}
	slog.Error("Failed to cancel tab streaming, tabID not found", "tabID", tabID)
}

// CancelAllTabs cancels streaming on all tabs.
// The Secretary tab gets a fresh context for future rituals.
func (tm *TabManager) CancelAllTabs() {
	for i := range tm.tabs {
		if tm.tabs[i].Cancel != nil {
			tm.tabs[i].Cancel()
		}
		if tm.tabs[i].Target == "secretary" {
			tm.tabs[i].Ctx, tm.tabs[i].Cancel = context.WithCancel(context.Background())
		} else {
			tm.tabs[i].Cancel = nil
		}
		tm.tabs[i].Streaming = false
	}
}

// SwitchTo saves current tab state and switches to the target index
func (tm *TabManager) SwitchTo(index int) {
	if index < 0 || index >= len(tm.tabs) || index == tm.activeTab {
		return
	}
	tm.activeTab = index
	// Notify TUI of tab switch so it can update context percent
	if tm.onTabSwitch != nil {
		tm.onTabSwitch()
	}
}

// UniqueTarget returns a target ID for the given minister ID that does not
// collide with any existing tab's Target. The first occurrence uses the
// bare minister ID (e.g. "chancellor"); subsequent ones get a suffix counter
// (e.g. "chancellor-2", "chancellor-3").
func (tm *TabManager) UniqueTarget(ministerID string) string {
	existing := map[string]bool{}
	for i := range tm.tabs {
		existing[tm.tabs[i].Target] = true
	}
	if !existing[ministerID] {
		return ministerID
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", ministerID, n)
		if !existing[candidate] {
			return candidate
		}
	}
}

// Add creates a new tab and switches to it
func (tm *TabManager) Add(label string, tabType TabType, target string) {
	newContent := newContentComponent(tm.width, tm.height, tm.markdownEnabled, tm.getStatus)
	// Wire loadSessionFn into dynamically-added tabs too
	for i := range tm.tabs {
		if tm.tabs[i].Content.loadSessionFn != nil {
			newContent.loadSessionFn = tm.tabs[i].Content.loadSessionFn
			break
		}
	}
	tm.tabs = append(tm.tabs, NewTab(label, tabType, target, newContent))
	tm.SwitchTo(len(tm.tabs) - 1)
}

// SetLoadSessionFn wires a session-loading function into every tab's
// ContentComponent so :resume uses the shared session store.
func (tm *TabManager) SetLoadSessionFn(fn func(sessionID string) tea.Cmd) {
	for i := range tm.tabs {
		tm.tabs[i].Content.loadSessionFn = fn
	}
}

// Close closes the active tab if safe to do so
func (tm *TabManager) Close() error {
	if len(tm.tabs) <= 1 {
		return errors.New("cannot close the last tab")
	}
	if tm.tabs[tm.activeTab].Streaming {
		return errors.New("cannot close tab while streaming")
	}

	closingIdx := tm.activeTab
	// Switch to adjacent tab before removing
	if closingIdx > 0 {
		tm.SwitchTo(closingIdx - 1)
	} else {
		tm.SwitchTo(closingIdx + 1)
	}

	// Remove the closed tab
	tm.tabs = append(tm.tabs[:closingIdx], tm.tabs[closingIdx+1:]...)

	// Adjust activeTab index if needed
	if tm.activeTab > closingIdx {
		tm.activeTab--
	}
	return nil
}

// NextTab switches to the next tab (wraps around)
func (tm *TabManager) NextTab() {
	if len(tm.tabs) <= 1 {
		return
	}
	next := (tm.activeTab + 1) % len(tm.tabs)
	tm.SwitchTo(next)
}

// PrevTab switches to the previous tab (wraps around)
func (tm *TabManager) PrevTab() {
	if len(tm.tabs) <= 1 {
		return
	}
	prev := tm.activeTab - 1
	if prev < 0 {
		prev = len(tm.tabs) - 1
	}
	tm.SwitchTo(prev)
}

// RenderTabBar renders the tab bar; returns "" for single tab.
// In welcome state, all tabs render in the inactive style.
func (tm *TabManager) RenderTabBar(width int) string {
	if len(tm.tabs) <= 1 {
		return ""
	}
	fillBg := globalTheme.TabFill
	var parts []string
	for i, tab := range tm.tabs {
		label := tab.Label
		if i == tm.activeTab && !tm.showWelcome {
			// Active tab: bold, bright foreground, stands out from the fill
			style := lipgloss.NewStyle().
				Bold(true).
				Foreground(globalTheme.TextColor).
				Background(fillBg).
				Padding(0, 1)
			parts = append(parts, style.Render(label))
		} else {
			// Inactive tab: dimmer text on the same grey fill
			style := lipgloss.NewStyle().
				Foreground(globalTheme.DimTextColor).
				Background(fillBg).
				Padding(0, 1)
			parts = append(parts, style.Render(label))
		}
	}
	tabBar := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	// Fill the rest of the line with the same grey background
	return lipgloss.NewStyle().Width(width).Background(fillBg).Render(tabBar)
}

// TabBarHeight returns 1 if multiple tabs, 0 otherwise
func (tm *TabManager) TabBarHeight() int {
	if len(tm.tabs) > 1 {
		return 1
	}
	return 0
}

// SetSize updates dimensions for all tabs
func (tm *TabManager) SetSize(w, h int) {
	tm.width = w
	tm.height = h
	for i := range tm.tabs {
		tm.tabs[i].Content.SetSize(w, h)
	}
}

// ActiveTab returns a pointer to the active Tab
func (tm *TabManager) ActiveTab() *Tab {
	return &tm.tabs[tm.activeTab]
}

// TabByTarget returns the tab whose Target matches, or nil
func (tm *TabManager) TabByTarget(target string) *Tab {
	for i := range tm.tabs {
		if tm.tabs[i].Target == target {
			return &tm.tabs[i]
		}
	}
	return nil
}

// SwitchToTabType switches to the first tab matching the given type
func (tm *TabManager) SwitchToTabType(tabType TabType) {
	for i := range tm.tabs {
		if tm.tabs[i].Type == tabType {
			tm.SwitchTo(i)
			return
		}
	}
}

// SwitchToTarget switches to the first tab whose Target matches the given target.
// Returns true if a matching tab was found and switched to.
func (tm *TabManager) SwitchToTarget(target string) bool {
	for i := range tm.tabs {
		if tm.tabs[i].Target == target {
			tm.SwitchTo(i)
			return true
		}
	}
	return false
}

// SwitchToZhengmingTarget routes a zhengming prompt to the tab that owns its
// context. Ritual work routes by channel ("e<N>") when present; otherwise it
// falls back to the minister's own tab. Returns the target that was switched
// to, or "" when no matching tab exists (the active tab stays put).
func (tm *TabManager) SwitchToZhengmingTarget(channelID, ministerID string) string {
	if channelID != "" && tm.SwitchToTarget(channelID) {
		return channelID
	}
	if ministerID != "" && tm.SwitchToTarget(ministerID) {
		return ministerID
	}
	return ""
}

// TabCount returns the number of tabs
func (tm *TabManager) TabCount() int {
	return len(tm.tabs)
}

// ActiveEdictID returns the active tab's edict ID
func (tm *TabManager) ActiveEdictID() uint {
	return tm.tabs[tm.activeTab].EdictID
}

// SetActiveEdictID sets the edict ID on the active tab
func (tm *TabManager) SetActiveEdictID(id uint) {
	tm.tabs[tm.activeTab].EdictID = id
}

// SetStreamingTab marks the active tab as streaming
func (tm *TabManager) SetStreamingTab() {
	tm.tabs[tm.activeTab].Streaming = true
}

// ClearStreaming clears the streaming flag on all tabs
func (tm *TabManager) ClearStreaming() {
	for i := range tm.tabs {
		tm.tabs[i].Streaming = false
	}
}

// StreamingTabIndex returns the index of a streaming tab, or -1 if none
func (tm *TabManager) StreamingTabIndex() int {
	for i := range tm.tabs {
		if tm.tabs[i].Streaming {
			return i
		}
	}
	return -1
}

// AnyStreaming returns true if any tab is currently streaming
func (tm *TabManager) AnyStreaming() bool {
	for i := range tm.tabs {
		if tm.tabs[i].Streaming {
			return true
		}
	}
	return false
}

// HandlePendingG handles the g-prefix key sequence for gt/gT/gg
// Returns (handled bool, cmd tea.Cmd)
func (tm *TabManager) HandlePendingG(key string, scrollToTop func()) (bool, tea.Cmd) {
	if !tm.pendingG {
		return false, nil
	}
	tm.pendingG = false
	switch key {
	case "t":
		tm.NextTab()
		return true, nil
	case "T":
		tm.PrevTab()
		return true, nil
	case "g":
		if scrollToTop != nil {
			scrollToTop()
		}
		return true, nil
	}
	return true, nil // consumed the pending g, unknown follow-up
}

// SetPendingG sets the pending g-prefix flag
func (tm *TabManager) SetPendingG() {
	tm.pendingG = true
}

// IsPendingG returns whether g-prefix is pending
func (tm *TabManager) IsPendingG() bool {
	return tm.pendingG
}

// ClearPendingG clears the pending g-prefix flag
func (tm *TabManager) ClearPendingG() {
	tm.pendingG = false
}

// UpdateContent calls Update on the active tab's ContentComponent and writes back
func (tm *TabManager) UpdateContent(msg tea.Msg) tea.Cmd {
	updated, cmd := tm.tabs[tm.activeTab].Content.Update(msg)
	tm.tabs[tm.activeTab].Content = updated
	return cmd
}

// ViewType represents the active view
type ViewType int

const (
	ViewChat ViewType = iota
	ViewHelp
	ViewModels
	ViewResume
	ViewEdict
)

// NavigationMode represents how navigation works in the current view
type NavigationMode int

const (
	NavText NavigationMode = iota // Text scrolling (chat, help)
	NavList                       // List selection (models, resume)
)

// ContentComponent manages all main content views with unified navigation
type ContentComponent struct {
	activeView ViewType
	width      int
	height     int

	// Sub-components
	Chat        *ChatComponent
	help        HelpWindow
	models      ModelsWindow
	resume      ResumeWindow
	edictSelect EdictSelectWindow

	// edictDashboard holds the rendered text for the ViewEdict dashboard.
	edictDashboard string

	// edictListActive is true when the dashboard was entered from the edicts
	// list; Esc returns to the list instead of chat.
	edictListActive bool

	// Unified navigation state
	navMode      NavigationMode
	activeList   ListNavigator           // current list view for NavList mode
	onSelect     func(index int) tea.Cmd // called on enter in list mode
	viewport     viewport.Model          // For text navigation
	selectedItem int                     // For list navigation
	scrollOffset int                     // For list navigation

	// loadSessionFn loads a session by ID for :resume. Set by TUIModel
	// to share the daemon's DB connection instead of opening a new one.
	loadSessionFn func(sessionID string) tea.Cmd
}

// NewContentComponent creates a new content component
func NewContentComponent(width, height int, markdownEnabled bool) ContentComponent {
	return ContentComponent{
		activeView:  ViewChat,
		width:       width,
		height:      height,
		Chat:        NewChatComponent(width, height, markdownEnabled),
		help:        NewHelpWindow(),
		models:      NewModelsWindow(),
		resume:      NewResumeWindow(),
		edictSelect: NewEdictSelectWindow(),
		navMode:     NavText,
		viewport:    viewport.New(width, height),
	}
}

// SetSize updates the dimensions
func (c *ContentComponent) SetSize(width, height int) {
	c.width = width
	c.height = height
	c.viewport.Width = width
	c.viewport.Height = height - 1
	// Help, models, resume, and edictSelect each render a title line,
	// so they get height-1. Chat has no title line and needs the full height.
	c.Chat.SetSize(width, height)
	c.help.SetSize(width, height-1)
	c.models.SetSize(width, height-1)
	c.resume.SetSize(width, height-1)
	c.edictSelect.SetSize(width, height-1)
}

// GetActiveView returns the current view type
func (c *ContentComponent) GetActiveView() ViewType {
	return c.activeView
}

// ShowChat switches to chat view
func (c *ContentComponent) ShowChat() tea.Cmd {
	c.activeView = ViewChat
	c.navMode = NavText
	c.edictListActive = false
	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "insert"}
	}
}

// ShowHelp switches to help view
func (c *ContentComponent) ShowHelp(topic string) tea.Cmd {
	c.activeView = ViewHelp
	c.navMode = NavText
	c.help.SetTopic(topic)

	// Setup viewport for help text
	c.viewport.SetContent(c.help.RenderContent())
	c.viewport.GotoTop()

	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "help"}
	}
}

// ShowUnifiedModels switches to models view with unified model list
func (c *ContentComponent) ShowUnifiedModels(models []Model, currentModel string) tea.Cmd {
	c.activeView = ViewModels
	c.navMode = NavList
	c.activeList = &c.models.SelectWindow
	c.models.SetModels(models, currentModel)
	c.models.ClearSearch()
	c.selectedItem = c.models.GetInitialSelection()
	c.scrollOffset = 0
	c.onSelect = func(index int) tea.Cmd {
		model := c.models.GetSelectedModel(index)
		if model == nil || !IsModelSelectable(*model) {
			return nil
		}
		if model.OnSelect != nil {
			return model.OnSelect
		}
		return func() tea.Msg {
			return modelSelectedMsg{model: model, onSelect: model.OnSelect}
		}
	}

	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "models"}
	}
}

// ShowResume switches to resume view
func (c *ContentComponent) ShowResume(sessions []court.Session) tea.Cmd {
	c.activeView = ViewResume
	c.navMode = NavList
	c.activeList = &c.resume.SelectWindow
	c.resume.SetSessions(sessions)
	c.selectedItem = 0
	c.scrollOffset = 0
	c.onSelect = func(index int) tea.Cmd {
		session := c.resume.GetSelectedSession(index)
		if session == nil {
			return nil
		}
		if c.loadSessionFn != nil {
			return c.loadSessionFn(session.ID)
		}
		return c.resume.LoadSession(session.ID, nil)
	}

	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "select"}
	}
}

// ShowEdictSelection switches to edict selection view
func (c *ContentComponent) ShowEdictSelection(edicts []storage.ActiveEdict) tea.Cmd {
	c.activeView = ViewEdict
	c.navMode = NavList
	c.edictListActive = true
	c.activeList = &c.edictSelect.SelectWindow
	c.edictSelect.SetItems(edicts)
	c.selectedItem = 0
	c.scrollOffset = 0
	c.onSelect = func(index int) tea.Cmd {
		edict := c.edictSelect.GetSelectedItem(index)
		if edict == nil {
			return nil
		}
		return func() tea.Msg {
			return edictSelectedMsg{edictID: edict.ID}
		}
	}

	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "select"}
	}
}

// ShowEdictDashboard switches to the read-only edict dashboard view
func (c *ContentComponent) ShowEdictDashboard(content string) tea.Cmd {
	c.activeView = ViewEdict
	c.navMode = NavText
	c.edictDashboard = content
	c.viewport.SetContent(content)
	c.viewport.GotoTop()
	return func() tea.Msg {
		return ChangeModeMsg{NewMode: "help"}
	}
}

// SetModelsLoading shows loading state for models
func (c *ContentComponent) SetModelsLoading() {
	c.models.SetLoading(true)
}

// SetModelsError shows error state for models
func (c *ContentComponent) SetModelsError(err string) {
	c.models.SetError(err)
}

// SetResumeLoading shows loading state for resume
func (c *ContentComponent) SetResumeLoading() {
	c.activeView = ViewResume
	c.navMode = NavList
	c.resume.SetLoading(true)
	c.selectedItem = 0
	c.scrollOffset = 0
}

// Update handles messages and navigation
func (c *ContentComponent) Update(msg tea.Msg) (ContentComponent, tea.Cmd) {
	var cmds []tea.Cmd

	// Delegate to active view first for view-specific updates
	switch c.activeView {
	case ViewChat:
		var cmd tea.Cmd
		newChat, cmd := c.Chat.Update(msg)
		c.Chat = &newChat
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	// Handle navigation based on mode
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Exit handling for non-chat views
		if c.activeView != ViewChat {
			cmd := c.handleExitKeys(msg)
			if cmd != nil {
				return *c, cmd
			}
		}

		// Navigation
		switch c.navMode {
		case NavText:
			cmd := c.handleTextNavigation(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		case NavList:
			cmd := c.handleListNavigation(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

	case tea.MouseMsg:
		if c.navMode == NavText && c.activeView == ViewChat {
			// Chat handles its own mouse events
			// Already handled above in chat.Update
		} else if c.navMode == NavText {
			// Handle mouse scrolling for help view
			switch msg.Type {
			case tea.MouseWheelUp:
				c.viewport.ScrollUp(1)
			case tea.MouseWheelDown:
				c.viewport.ScrollDown(1)
			}
		}
	}

	return *c, tea.Batch(cmds...)
}

// handleExitKeys handles Esc, Ctrl+C for exiting views
func (c *ContentComponent) handleExitKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		// Ctrl+C exits to chat
		return c.ShowChat()
	case "esc":
		// In ViewEdict dashboard (NavText) with edictListActive, Esc returns
		// to the edicts list. When already in the list (NavList), Esc exits to chat.
		if c.activeView == ViewEdict && c.edictListActive && c.navMode == NavText {
			c.edictListActive = false
			return func() tea.Msg {
				return reloadEdictsMsg{}
			}
		}
		// Single ESC exits to chat
		return c.ShowChat()
	}
	return nil
}

// handleTextNavigation handles navigation for text views (chat, help)
func (c *ContentComponent) handleTextNavigation(msg tea.KeyMsg) tea.Cmd {
	if c.activeView == ViewChat {
		// Chat handles its own navigation via Update
		return nil
	}

	// Help view navigation
	switch msg.String() {
	case "j", "down":
		c.viewport.LineDown(1)
	case "k", "up":
		c.viewport.LineUp(1)
	case "ctrl+d":
		c.viewport.HalfPageDown()
	case "ctrl+u":
		c.viewport.HalfPageUp()
	case "ctrl+f", "pgdown":
		c.viewport.PageDown()
	case "ctrl+b", "pgup":
		c.viewport.PageUp()
	case "g":
		// Check if this is gg (go to top)
		// For now, single 'g' goes to top
		c.viewport.GotoTop()
	case "G":
		c.viewport.GotoBottom()
	}

	return nil
}

// handleListNavigation handles navigation for list views (models, resume, seal)
func (c *ContentComponent) handleListNavigation(msg tea.KeyMsg) tea.Cmd {
	nav := c.activeList
	if nav == nil {
		return nil
	}

	itemCount := nav.GetItemCount()
	visibleSlots := nav.GetVisibleSlots()
	var scrollInfoCmd tea.Cmd

	switch msg.String() {
	case "j", "down":
		newIndex := nav.NavNext(c.selectedItem)
		if newIndex != c.selectedItem {
			c.selectedItem = newIndex
			if c.selectedItem >= c.scrollOffset+visibleSlots {
				c.scrollOffset = c.selectedItem - visibleSlots + 1
			}
			scrollInfoCmd = c.getScrollInfoCmd()
		}
	case "k", "up":
		newIndex := nav.NavPrev(c.selectedItem)
		if newIndex != c.selectedItem {
			c.selectedItem = newIndex
			if c.selectedItem < c.scrollOffset {
				c.scrollOffset = c.selectedItem
			}
			scrollInfoCmd = c.getScrollInfoCmd()
		}
	case "ctrl+d": // Half page down
		move := visibleSlots / 2
		if move < 1 {
			move = 1
		}
		target := c.selectedItem + move
		if target >= itemCount {
			target = itemCount - 1
		}
		c.selectedItem = nav.NavNearest(target)
		if c.selectedItem >= c.scrollOffset+visibleSlots {
			c.scrollOffset = c.selectedItem - visibleSlots + 1
		}
		scrollInfoCmd = c.getScrollInfoCmd()
	case "ctrl+u": // Half page up
		move := visibleSlots / 2
		if move < 1 {
			move = 1
		}
		target := c.selectedItem - move
		if target < 0 {
			target = 0
		}
		c.selectedItem = nav.NavNearest(target)
		if c.selectedItem < c.scrollOffset {
			c.scrollOffset = c.selectedItem
		}
		scrollInfoCmd = c.getScrollInfoCmd()
	case "g", "home":
		c.selectedItem = nav.NavFirst()
		c.scrollOffset = 0
		scrollInfoCmd = c.getScrollInfoCmd()
	case "G", "end":
		c.selectedItem = nav.NavLast()
		if c.selectedItem >= visibleSlots {
			c.scrollOffset = c.selectedItem - visibleSlots + 1
		}
		scrollInfoCmd = c.getScrollInfoCmd()
	case "enter":
		return c.handleListSelect()
	case "n":
		// Next match in current search direction
		if c.activeView == ViewModels && c.models.HasSearch() {
			newIndex := c.models.NextMatch(c.selectedItem, c.models.searchDirection)
			if newIndex >= 0 {
				c.selectedItem = newIndex
				if c.selectedItem >= c.scrollOffset+visibleSlots {
					c.scrollOffset = c.selectedItem - visibleSlots + 1
				} else if c.selectedItem < c.scrollOffset {
					c.scrollOffset = c.selectedItem
				}
			}
		}
	case "N":
		// Previous match (opposite direction)
		if c.activeView == ViewModels && c.models.HasSearch() {
			newIndex := c.models.NextMatch(c.selectedItem, -c.models.searchDirection)
			if newIndex >= 0 {
				c.selectedItem = newIndex
				if c.selectedItem >= c.scrollOffset+visibleSlots {
					c.scrollOffset = c.selectedItem - visibleSlots + 1
				} else if c.selectedItem < c.scrollOffset {
					c.scrollOffset = c.selectedItem
				}
			}
		}
	}

	return scrollInfoCmd
}

// handleListSelect handles enter/selection for the active list view
func (c *ContentComponent) handleListSelect() tea.Cmd {
	if c.onSelect == nil {
		return nil
	}
	cmd := c.onSelect(c.selectedItem)
	if cmd == nil {
		return nil
	}
	return tea.Batch(c.ShowChat(), cmd)
}

// getScrollInfoCmd returns a command that sends scroll info as a message
func (c *ContentComponent) getScrollInfoCmd() tea.Cmd {
	return nil
}

// View renders the active view
func (c *ContentComponent) View() string {
	switch c.activeView {
	case ViewChat:
		return c.Chat.View()
	case ViewHelp:
		return c.renderHelpView()
	case ViewModels:
		return c.renderModelsView()
	case ViewResume:
		return c.renderResumeView()
	case ViewEdict:
		if c.navMode == NavList {
			return c.renderEdictSelectView()
		}
		return c.renderEdictView()
	}
	return ""
}

// renderHelpView renders the help view
func (c *ContentComponent) renderHelpView() string {
	// Title bar
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(globalTheme.PromptBorder).
		Background(globalTheme.PaneBackground).
		Padding(0, 1)

	title := titleStyle.Render(fmt.Sprintf(" Help: %s ", c.help.GetTopic()))

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		c.viewport.View(),
	)

	// Ensure the view fills the full height
	return lipgloss.NewStyle().
		Height(c.height).
		Render(content)
}

// renderModelsView renders the models selection view
func (c *ContentComponent) renderModelsView() string {
	content := c.models.RenderList(c.selectedItem, c.scrollOffset, c.models.GetVisibleSlots())

	// Apply height constraint to prevent overflow clipping from the top
	return lipgloss.NewStyle().
		Height(c.height).
		MaxHeight(c.height).
		Render(content)
}

// renderResumeView renders the session selection view
func (c *ContentComponent) renderResumeView() string {
	content := c.resume.RenderList(c.selectedItem, c.scrollOffset, c.resume.GetVisibleSlots())

	// Apply height constraint to prevent overflow clipping from the top
	return lipgloss.NewStyle().
		Height(c.height).
		MaxHeight(c.height).
		Render(content)
}

// renderEdictSelectView renders the edict selection list view
func (c *ContentComponent) renderEdictSelectView() string {
	content := c.edictSelect.RenderList(c.selectedItem, c.scrollOffset, c.edictSelect.GetVisibleSlots())

	// Apply height constraint to prevent overflow clipping from the top
	return lipgloss.NewStyle().
		Height(c.height).
		MaxHeight(c.height).
		Render(content)
}

// renderEdictView renders the edict dashboard view (read-only, scrollable)
func (c *ContentComponent) renderEdictView() string {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(globalTheme.PromptBorder).
		Background(globalTheme.PaneBackground).
		Padding(0, 1)

	title := titleStyle.Render(" Edict Dashboard ")

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		c.viewport.View(),
	)

	return lipgloss.NewStyle().
		Height(c.height).
		Render(content)
}
