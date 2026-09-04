package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/courtapi"
	"github.com/afittestide/asimi/internal/ministers"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/storage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/maximhq/bifrost/core/schemas"
)

// TUIModel represents the bubbletea model for the TUI
type TUIModel struct {
	config        *Config
	width, height int
	theme         *Theme // Add theme here

	// UI Components
	status      StatusComponent
	prompts     map[string]*PromptComponent
	tabs        TabManager
	completions CompletionDialog
	commandLine *CommandLineComponent
	modal       *BaseModal

	// UI Flags & State
	Mode                 string // Current UI mode for status display
	showCompletionDialog bool
	completionMode       string // "file" or "command"
	sessionActive        bool
	rawMode              bool // Toggle between chat and raw session view
	updateAvailable      bool // True when a newer version is available
	configCreated        bool // True when config file was created on first run
	onboardingDeclined   bool // True when user declined model selection onboarding

	streamCompleteCallback func(*TUIModel) tea.Cmd // Optional callback to run after stream completes

	// Command registry
	commandRegistry CommandRegistry

	// Application services (passed in, not owned)
	sessionStore *SessionStore
	db           *storage.DB
	scheduler    *runners.CoreToolScheduler
	court        courtapi.Client

	// Court integration
	currentEdictKey storage.EdictKey // Tracks current edict for multi-turn conversations

	// creatingTab is non-zero when restoring a session onto an e<N> tab
	// or a minister's tab. The session is routed to the tab identified by this key
	// instead of switching to the minister's original interactive tab.
	// If pendingPrompt is also set, it is submitted to the minister after restore.
	creatingTab   string
	pendingPrompt string

	// Prompt history and rollback management
	// sessionPromptHistory stores prompts with snapshots for current session rollback
	sessionPromptHistory          []promptHistoryEntry
	historyCursor                 int
	historySaved                  bool
	historyPendingPrompt          string
	historyPresentSessionSnapshot int
	historyPresentChatSnapshot    int

	// Persistent history stores (survive app restarts)
	persistentPromptHistory  *PromptHistory
	persistentCommandHistory *CommandHistory

	// Waiting indicator state
	waitingForResponse bool
	waitingStart       time.Time

	ctrlCLastPress  time.Time // Time of last Ctrl-C press for double-press detection
	oscFilterActive time.Time // Tracks when we last filtered an OSC/CSI response

	// Host command approval state
	pendingHostApproval *runners.ApprovalRequestMsg
	// Ritual enactment confirmation state
	pendingRitualEnact *pendingRitualEnact
	// Seal override confirmation state
	pendingSealOverride *pendingSealOverride
	pendingEdictCancel  *pendingEdictCancel
	// Onboarding model selection confirmation state
	pendingOnboarding bool
	// Handsoff mode: auto-answer zhengming and YESNO prompts
	handsoff bool
	// Pending API key input: set when user selects a login_required non-OpenAI provider
	pendingAPIKeyProvider string
	// Pending model name entry: set when user selects a manual_entry model
	pendingModelNameProvider string
	repoInfo                 *repo.RepoInfo

	// Debounced render tick: prevents stacking multiple 50ms tick commands
	renderTickPending bool

	// Progressive session resume state. On sessionSelectedMsg the full chat
	// rebuild is spread across resumeRebuildBatchMsg ticks (a few messages per
	// tick) so the UI stays responsive. The cursor over the prepared display
	// messages is kept here; the final batch flushes dirty chats and submits
	// the pending prompt exactly as the old synchronous path did.
	resumeRebuildMessages []ChatMessage
	resumeRebuildCursor   int
	resumeRebuildSession  *court.Session
	resumeRebuildActive   bool

	// Connection drop recovery state. When the transport drops during an
	// active stream, each streaming tab's prompt is saved here for
	// auto-retry after reconnect.
	connDropPendingRetry map[string]pendingRetry
}

// pendingRetry stores the prompt text and context files from a stream
// that was in-flight when the connection dropped, so the TUI can
// re-submit it after reconnect.
type pendingRetry struct {
	prompt       string
	contextFiles map[string]string
}

// prompt returns the PromptComponent for the active tab, creating one if needed.
func (m *TUIModel) prompt() *PromptComponent {
	label := m.tabs.ActiveTab().Label
	p, ok := m.prompts[label]
	if !ok {
		np := NewPromptComponent(m.width, 5)
		if m.config != nil && m.config.UI.PromptExpandedHeight > 0 {
			np.SetExpandedHeight(m.config.UI.PromptExpandedHeight)
		}
		p = &np
		m.prompts[label] = p
	}
	return p
}

// addTab creates a new tab if one doesn't already exist for target,
// then calls onReady(tab) to handle post-creation behavior.
// Idempotent: if the tab already exists, onReady is not called.
func (m *TUIModel) addTab(label string, tabType TabType, target string, onReady func(tab *Tab) tea.Cmd) tea.Cmd {
	if m.tabs.TabByTarget(target) != nil {
		return nil
	}
	m.tabs.Add(label, tabType, target)
	tab := m.tabs.TabByTarget(target)
	if onReady != nil {
		return onReady(tab)
	}
	return nil
}

type promptHistoryEntry struct {
	Prompt          string
	SessionSnapshot int
	ChatSnapshot    int
}

type tickMsg struct{}

// connectionLostMsg is sent when the underlying RPC connection drops.
// The TUI uses it to detect mid-stream drops and trigger auto-retry.
type connectionLostMsg struct{}

// connectionRestoredMsg is sent after the RPC connection is re-established
// following a drop. The TUI uses it to re-submit pending prompts.
type connectionRestoredMsg struct{}

// connectionReconnectFailedMsg is sent when the connection drops and the
// ReconnectingClient fails to reconnect within the timeout window.
type connectionReconnectFailedMsg struct{}

// chatRenderTickMsg is produced by a debounce tick to flush dirty chat content.
// Only one tick is pending at a time (guarded by TUIModel.renderTickPending).
type chatRenderTickMsg struct{}

// resumeRebuildBatchMsg is produced by a tea.Tick while progressively rebuilding
// a resumed session's chat history. It carries the prepared display messages and
// the current index cursor so each tick appends a small batch without blocking
// the TUI update loop.
type resumeRebuildBatchMsg struct {
	messages []ChatMessage
	cursor   int
}

type shellCommandResultMsg struct {
	command  string
	output   string
	exitCode string
	err      error
}

// editorResultMsg is sent after tea.ExecProcess finishes running the editor.
type editorResultMsg struct {
	ResultChan chan tools.EditorResult
	TmpPath    string
	OrigBytes  []byte
	OrigMtime  time.Time
	Err        error
}

// pendingRitualEnact tracks state when waiting for Ruler confirmation to enact swift-strike
type pendingRitualEnact struct {
	edictID uint
	intent  string
}

// pendingSealOverride tracks state when waiting for Ruler confirmation to seal without prerequisites
type pendingSealOverride struct {
	edictID uint
	notes   string
}

// pendingEdictCancel tracks state when waiting for Ruler confirmation to cancel an edict
type pendingEdictCancel struct {
	edictID uint
}

// showEdictDashboardMsg requests showing the edict dashboard view
type showEdictDashboardMsg struct {
	content string
}

// enterYesNoOrAuto enters yes/no mode, unless handsoff mode is active.
// When handsoff is on, it short-circuits with the given answer via a
// yesNoResponseMsg instead of prompting the user.
func (m *TUIModel) enterYesNoOrAuto(question string, autoAnswer bool) tea.Cmd {
	if m.handsoff {
		slog.Info("handsoff: auto-answering yesno", "question", question, "answer", autoAnswer)
		return func() tea.Msg { return yesNoResponseMsg{answer: autoAnswer} }
	}
	return m.commandLine.EnterYesNoMode(question)
}

// NewTUIModel creates a new TUI model
// NewTUIModelWithStores creates a new TUI model with provided stores (for fx injection)
func NewTUIModel(cfg *Config, repoInfo *repo.RepoInfo, promptHistory *PromptHistory, commandHistory *CommandHistory, sessionStore *SessionStore, db *storage.DB, scheduler *runners.CoreToolScheduler, courtClient courtapi.Client) *TUIModel {

	registry := NewCommandRegistry()
	theme := NewTheme()

	prompts := map[string]*PromptComponent{}

	// Create status component and set repo info
	status := NewStatusComponent(80)
	status.SetRepoInfo(repoInfo)

	markdownEnabled := false
	if cfg != nil {
		markdownEnabled = cfg.UI.MarkdownEnabled
	}

	model := &TUIModel{
		config: cfg,
		theme:  theme,

		repoInfo: repoInfo,
		// Initialize components
		status:      status,
		prompts:     prompts,
		completions: NewCompletionDialog(),
		commandLine: NewCommandLineComponent(),
		modal:       nil,

		// UI Flags
		Mode:                 ViModeInsert, // Start in insert mode
		showCompletionDialog: false,
		completionMode:       "",
		sessionActive:        false,
		rawMode:              false,
		configCreated:        config.ConfigCreated, // Set from global flag

		// Command registry
		commandRegistry: registry,

		// Application services (injected)
		sessionStore:             sessionStore,
		db:                       db,
		scheduler:                scheduler,
		court:                    courtClient,
		waitingForResponse:       false,
		persistentPromptHistory:  promptHistory,
		persistentCommandHistory: commandHistory,
		connDropPendingRetry:     make(map[string]pendingRetry),
		handsoff:                 cli.Handsoff,
	}

	// Initialize tab system with default tabs built from minister defs
	projectDir := ""
	if repoInfo != nil {
		projectDir = repoInfo.ProjectRoot
	}
	ms, err := ministers.LoadAllMinisters(projectDir)
	if err != nil {
		slog.Warn("failed to load minister ms for tabs, using builtins", "error", err)
		ms, _ = ministers.LoadMinisters()
	}
	model.tabs = NewTabManager(80, 18, markdownEnabled, func() string { return model.Mode }, ms)
	// Wire loadSessionFn so :resume reuses the shared session store
	model.tabs.SetLoadSessionFn(func(sessionID string) tea.Cmd {
		return model.tabs.Content().resume.LoadSession(sessionID, model.sessionStore)
	})
	// Wire welcome screen notification callbacks
	model.tabs.getUpdateAvail = func() bool { return model.updateAvailable }
	model.tabs.getConfigCreated = func() bool { return model.configCreated }
	// Set up tab switch callback to update the status bar for the new tab
	model.tabs.onTabSwitch = func() {
		if state, ok := model.currentSessionState(); ok {
			model.status.ContextPercent = state.ContextUsagePercent
		} else {
			// No active session for this tab - context usage is 0%
			model.status.ContextPercent = 0
		}
		// Sync waiting/stopwatch state from the active tab's streaming flag
		if tab := model.tabs.ActiveTab(); tab != nil && tab.Streaming {
			model.status.StartWaiting()
			model.waitingForResponse = true
			model.waitingStart = time.Now()
		} else {
			model.status.StopWaiting()
			model.status.ResetStreamRate()
			model.waitingForResponse = false
		}
	}

	// Set initial status info - show disconnected state initially
	model.status.SetProvider(cfg.LLM.Provider, cfg.LLM.Model, false)
	model.initHistory()

	return model
}

// initHistory resets prompt history bookkeeping to its initial state and loads persistent history
func (m *TUIModel) initHistory() {
	m.sessionPromptHistory = make([]promptHistoryEntry, 0)
	m.historyCursor = 0
	m.historySaved = false
	m.historyPendingPrompt = ""
	m.historyPresentSessionSnapshot = 0
	m.historyPresentChatSnapshot = 0

	// Load persistent prompt history from disk
	if m.persistentPromptHistory != nil {
		entries, err := m.persistentPromptHistory.Load()
		if err != nil {
			slog.Warn("failed to load prompt history", "error", err)
		} else {
			// Convert persistent entries to in-memory format
			// Note: SessionSnapshot and ChatSnapshot are set to 0 for loaded entries
			// as they're only meaningful for the current session
			for _, entry := range entries {
				m.sessionPromptHistory = append(m.sessionPromptHistory, promptHistoryEntry{
					Prompt:          entry.Content,
					SessionSnapshot: 0,
					ChatSnapshot:    0,
				})
			}
			m.historyCursor = len(m.sessionPromptHistory)
			slog.Debug("loaded prompt history", "count", len(entries))
		}
	}

	// Load persistent command history from disk
	if m.persistentCommandHistory != nil {
		entries, err := m.persistentCommandHistory.Load()
		if err != nil {
			slog.Warn("failed to load command history", "error", err)
		} else {
			m.commandLine.LoadHistory(entries)
			slog.Debug("loaded command history", "count", len(entries))
		}
	}
}

// getCurrentSession returns the current court session based on active tab's target minister.
func (m *TUIModel) getCurrentSession() *court.Session {
	if m.court == nil {
		return nil
	}
	tab := m.tabs.ActiveTab()
	minister := m.court.GetMinister(string(tab.Type))
	if minister == nil {
		return nil
	}
	return minister.GetSession(tab.Target)
}

// currentTabTarget returns the target identifier of the active tab, or "".
func (m *TUIModel) currentTabTarget() string {
	if m.tabs.ActiveTab() == nil {
		return ""
	}
	return m.tabs.ActiveTab().Target
}

// currentSessionState returns a snapshot of the active tab's session.
// The bool reports whether a session actually exists for that tab.
func (m *TUIModel) currentSessionState() (court.SessionState, bool) {
	if m.court == nil {
		return court.SessionState{}, false
	}
	target := m.currentTabTarget()
	if target == "" {
		return court.SessionState{}, false
	}
	state := m.court.SessionState(target)
	return state, state.Exists
}

// autoCompactIfNeeded triggers a synchronous compaction pass when free
// context tokens drop below 10% and the conversation has more than two
// messages. Chat status messages are written directly to the active chat.
func (m *TUIModel) autoCompactIfNeeded(state court.SessionState) {
	info := state.ContextInfo
	threshold := float64(info.TotalTokens) * 0.10
	if float64(info.FreeTokens) >= threshold || state.MessageCount <= 2 {
		return
	}
	slog.Info("auto-compacting conversation", "free_tokens", info.FreeTokens, "threshold", threshold)
	m.tabs.Content().Chat.AddMessage("🗜️  Auto-compacting conversation history (low on context)...")

	ctx := context.Background()
	if _, err := m.court.CompactSession(ctx, m.currentTabTarget(), compactPrompt); err != nil {
		slog.Warn("auto-compaction failed", "error", err)
		m.tabs.Content().Chat.AddMessage(fmt.Sprintf("⚠️  Auto-compaction failed: %v", err))
		return
	}
	newState, ok := m.currentSessionState()
	if !ok {
		return
	}
	newInfo := newState.ContextInfo
	m.tabs.Content().Chat.AddMessage(fmt.Sprintf("✅ Conversation compacted! Context usage: %s/%s tokens (%.1f%%)",
		formatTokenCount(newInfo.UsedTokens),
		formatTokenCount(newInfo.TotalTokens),
		percentage(newInfo.UsedTokens, newInfo.TotalTokens)))
	slog.Info("auto-compaction completed", "old_used", info.UsedTokens, "new_used", newInfo.UsedTokens, "saved", info.UsedTokens-newInfo.UsedTokens)
}

// SetSession configures the Court with an LLM model from a session.
func (m *TUIModel) SetSession(session *court.Session) {
	if session != nil {
		m.status.SetProvider(m.config.LLM.Provider, m.config.LLM.Model, true)
		if m.court != nil {
			model := session.GetModel()
			if model != nil {
				cfg := &court.SessionConfig{
					LLM: config.LLMConfig{
						MaxTurns:          m.config.LLM.MaxTurns,
						MaxThinkingTokens: m.config.LLM.MaxThinkingTokens,
						Provider:          m.config.LLM.Provider,
						Model:             m.config.LLM.Model,
					},
				}
				repoInfo := repo.RepoInfo{
					ProjectRoot: func() string {
						if m.repoInfo != nil {
							return m.repoInfo.ProjectRoot
						}
						return ""
					}(),
				}
				m.court.ConfigureModel(model, cfg, repoInfo)
			}
		}
	} else {
		m.status.SetProvider(m.config.LLM.Provider, m.config.LLM.Model, false)
	}
}

// switchModel asks the Court to rebuild its Bifrost client with the
// current config. In daemon mode this happens over the wire; in
// single-process mode it happens inline.
func (m *TUIModel) switchModel() tea.Cmd {
	return func() tea.Msg {
		if m.court == nil {
			return llmInitErrorMsg{err: fmt.Errorf("court not initialised")}
		}
		// If using OpenAI OAuth credentials, check if the token needs refresh
		if m.config.LLM.Provider == "openai" {
			if key, err := GetAPIKeyFromKeyring("openai"); err == nil && key != "" {
				if cred, ok := parseCodexOAuthCredential(key); ok {
					if time.Now().Unix() >= cred.ExpiresAt {
						refreshed, err := refreshCodexToken()
						if err != nil {
							slog.Warn("failed to refresh Codex token during model switch", "error", err)
						} else {
							m.config.LLM.APIKey = refreshed
						}
					} else {
						m.config.LLM.APIKey = cred.AccessToken
					}
				}
			}
		}
		slog.Info("switching LLM model", "provider", m.config.LLM.Provider, "model", m.config.LLM.Model)
		if err := m.court.SetContext(context.Background(), m.setContextParams()); err != nil {
			return llmInitErrorMsg{err: err}
		}
		slog.Info("LLM model switched successfully")
		return llmInitSuccessMsg{}
	}
}

// setContextParams builds the params for a SetContext call from the TUI's
// current state. APIKeys is always populated from environment variables
// so that daemon mode receives the keys on every call (e.g., model switch).
func (m *TUIModel) setContextParams() types.SetContextParams {
	return buildSetContextParams(m.config, m.repoInfo)
}

// collectAPIKeys gathers API keys from environment variables and the OS keyring
// for all supported providers. Environment variables take precedence over keyring.
// This runs in the TUI process and passes keys to the daemon via SetContext,
// since the daemon no longer reads env vars directly.
//
// Uses convention: PROVIDER_API_KEY for env vars, with special cases for
// googleai (GEMINI_API_KEY / GOOGLE_API_KEY) and bedrock (AWS credential pair).
// The map uses Bifrost provider strings ("gemini" for Google AI).
func collectAPIKeys() map[string]string {
	keys := make(map[string]string)

	// Collect keys for all standard providers using convention-based env vars
	for _, sp := range schemas.StandardProviders {
		bifrostProvider := string(sp)
		asimiProvider := bifrostProviderToAsimi(bifrostProvider)
		envVar := providerEnvVar(asimiProvider)

		// Bedrock uses AWS credential pairs, not single API keys
		if bifrostProvider == "bedrock" {
			continue
		}
		// Vertex uses gcloud ADC, not a single API key
		if bifrostProvider == "vertex" {
			continue
		}
		// Ollama is keyless
		if bifrostProvider == "ollama" {
			continue
		}

		// Google AI: check both GEMINI_API_KEY and GOOGLE_API_KEY
		if asimiProvider == "googleai" {
			key := os.Getenv("GEMINI_API_KEY")
			if key == "" {
				key = os.Getenv("GOOGLE_API_KEY")
			}
			if key == "" {
				if krKey, err := GetAPIKeyFromKeyring("googleai"); err == nil {
					key = krKey
				}
			}
			if key != "" {
				keys["gemini"] = key
			}
			continue
		}

		// Standard convention: env var first, then keyring
		key := os.Getenv(envVar)
		if key == "" {
			if krKey, err := GetAPIKeyFromKeyring(asimiProvider); err == nil && krKey != "" {
				key = krKey
			}
		}

		if key != "" {
			// OpenAI: if the keyring value is a JSON OAuth credential, extract
			// just the access token. The account ID is handled separately
			// via getCodexAccountID().
			if asimiProvider == "openai" {
				if cred, ok := parseCodexOAuthCredential(key); ok {
					keys["openai"] = cred.AccessToken
				} else {
					keys["openai"] = key
				}
			} else {
				keys[bifrostProvider] = key
			}
		}
	}

	// AWS credentials for Bedrock
	if key := os.Getenv("AWS_ACCESS_KEY_ID"); key != "" {
		keys["AWS_ACCESS_KEY_ID"] = key
	}
	if key := os.Getenv("AWS_SECRET_ACCESS_KEY"); key != "" {
		keys["AWS_SECRET_ACCESS_KEY"] = key
	}
	if key := os.Getenv("AWS_SESSION_TOKEN"); key != "" {
		keys["AWS_SESSION_TOKEN"] = key
	}

	// gcloud credentials for Vertex AI (Application Default Credentials)
	if key := os.Getenv("GOOGLE_CLOUD_PROJECT"); key != "" {
		keys["GOOGLE_CLOUD_PROJECT"] = key
	}
	if key := os.Getenv("GOOGLE_CLOUD_REGION"); key != "" {
		keys["GOOGLE_CLOUD_REGION"] = key
	}
	if key := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); key != "" {
		keys["GOOGLE_APPLICATION_CREDENTIALS"] = key
	}

	return keys
}

// getCodexAccountID extracts the Codex account ID from the OpenAI keyring
// credential if it's an OAuth JSON blob. Returns empty string for plain API keys.
func getCodexAccountID() string {
	// Env var API key takes precedence — no account ID for plain keys
	if os.Getenv("OPENAI_API_KEY") != "" {
		return ""
	}
	raw, err := GetAPIKeyFromKeyring("openai")
	if err != nil || raw == "" {
		return ""
	}
	if cred, ok := parseCodexOAuthCredential(raw); ok {
		return cred.AccountID
	}
	return ""
}

func (m *TUIModel) saveSession() {
	if m.sessionStore == nil || !m.config.Session.Enabled || !m.config.Session.AutoSave {
		return
	}
	if m.court == nil {
		return
	}

	tab := m.tabs.ActiveTab()
	if tab == nil {
		return
	}

	minister := m.court.GetMinister(string(tab.Type))
	if minister == nil {
		return
	}

	session := minister.GetSession(tab.Target)
	if session == nil {
		return
	}

	m.sessionStore.SaveSession(session)
	slog.Debug("session auto-saved", "tab", tab.Target)
}

// shutdown performs graceful shutdown of the TUI, ensuring all pending saves complete
func (m *TUIModel) shutdown() {
	// Save the current session before closing
	m.saveSession()

	if m.sessionStore != nil {
		m.sessionStore.Close()
	}
}

// onboardingPromptMsg triggers the YES/NO model selection prompt on the home view.
type onboardingPromptMsg struct{}

// onboardingState determines what onboarding step is needed based on config.
const (
	onboardingNeedsLogin = "needs_login"
	onboardingNeedsModel = "needs_model"
	onboardingConfigured = "configured"
)

func onboardingState(config *Config) string {
	if config == nil || config.LLM.Provider == "" {
		return onboardingNeedsLogin
	}
	if config.LLM.Model == "" {
		return onboardingNeedsModel
	}
	return onboardingConfigured
}

// onboardingPromptText returns the appropriate prompt message for the current onboarding state.
func onboardingPromptText(state string) string {
	switch state {
	case onboardingNeedsLogin:
		return "No provider is configured. Would you like to log in now?"
	default:
		return "No model is configured. Would you like to select one now?"
	}
}

// Init implements bubbletea.Model. It asks the court to build its
// Bifrost client (daemon-side in daemon mode, inline otherwise).
func (m TUIModel) Init() tea.Cmd {
	tick := tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
	llmInit := func() tea.Msg {
		if m.court == nil {
			return llmInitErrorMsg{err: fmt.Errorf("court not initialised")}
		}
		slog.Info("connecting to LLM", "provider", m.config.LLM.Provider)
		if err := m.court.SetContext(context.Background(), m.setContextParams()); err != nil {
			return llmInitErrorMsg{err: err}
		}
		slog.Info("LLM client connected")
		return llmInitSuccessMsg{}
	}

	// On first run or unconfigured, show the onboarding prompt shortly after startup
	if m.configCreated || onboardingState(m.config) != onboardingConfigured {
		return tea.Batch(llmInit, tick, func() tea.Msg { return onboardingPromptMsg{} }, watchConnDrop(m.court))
	}
	return tea.Batch(llmInit, tick, watchConnDrop(m.court))
}

// Update implements bubbletea.Model
func (m TUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	start := time.Now()

	// Log all messages in debug mode

	defer func() {
		duration := time.Since(start)
		if duration > 100*time.Millisecond {
			slog.Warn("[bubbletea] Update() SLOW", "duration", duration, "msg_type", fmt.Sprintf("%T", msg))
		}
	}()

	// Update command line to remove expired toasts
	m.commandLine.Update()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyMsg(msg)

	case tea.MouseMsg:
		// Handle mouse wheel scrolling - switch to SCROLL mode when scrolling up
		if msg.Type == tea.MouseWheelUp && m.Mode != "scroll" && m.tabs.Content().GetActiveView() == ViewChat {
			// Only enter scroll mode if we're not already at the top
			if !m.tabs.Content().Chat.Viewport.AtTop() {
				// First, let the content handle the scroll
				contentCmd := m.tabs.UpdateContent(msg)
				// Then enter scroll mode
				enterScrollCmd := func() tea.Msg { return ChangeModeMsg{NewMode: "scroll"} }
				return m, tea.Batch(contentCmd, enterScrollCmd)
			}
		}
		contentCmd := m.tabs.UpdateContent(msg)
		return m, contentCmd

	case tea.WindowSizeMsg:
		return m.handleWindowSizeMsg(msg)

	default:
		return m.handleCustomMessages(msg)
	}
}

// handleKeyMsg processes keyboard input filtering out escape sequences
func (m TUIModel) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Filter out terminal escape sequences that shouldn't be processed.
	// These are responses to terminal queries (background color, cursor position, etc.)
	// They arrive as key events in alt-screen mode and would otherwise be typed
	// into the prompt textarea, producing garbage characters like `bg:1e1e/1e1e/1e1e`.
	keyStr := msg.String()

	// Swallow the lone backslash from an OSC String Terminator (ST = ESC \).
	// When the terminal responds to an OSC 11 query, the response ends with \x1b\
	// (the ST). Bubble Tea delivers the main response body and the trailing \ as
	// separate key events. The body is caught by the filters below, but the lone
	// backslash would otherwise be typed into the prompt.
	if keyStr == "\\" && time.Since(m.oscFilterActive) < 200*time.Millisecond {
		return m, nil
	}

	// Ignore OSC (Operating System Command) responses like ]11;rgb:...
	// Also catch `bg:` — some terminals truncate the leading `r` from `rgb:`,
	// leaving the response as `bg:1e1e/1e1e/1e1e`.
	if strings.HasPrefix(keyStr, "]") ||
		strings.Contains(keyStr, "rgb:") ||
		strings.Contains(keyStr, ";rgb") ||
		strings.Contains(keyStr, "bg:") {
		m.oscFilterActive = time.Now()
		return m, nil
	}

	// Ignore CSI (Control Sequence Introducer) responses like cursor position
	// reports [1;1R. Pattern: starts with `[`, ends with `R`, contains `;`.
	if (strings.HasPrefix(keyStr, "[") || strings.HasPrefix(keyStr, "\x1b[")) &&
		strings.HasSuffix(keyStr, "R") && strings.Contains(keyStr, ";") {
		m.oscFilterActive = time.Now()
		return m, nil
	}

	// Ignore any key containing an escape character that isn't the normal
	// escape key. This catches malformed or partial terminal responses.
	if len(keyStr) > 3 && strings.Contains(keyStr, "\x1b") {
		if keyStr != "esc" && keyStr != "escape" {
			m.oscFilterActive = time.Now()
			return m, nil
		}
	}

	// Always handle Ctrl+C first
	var cmd tea.Cmd

	if keyStr == "ctrl+c" {
		return m.handleCtrlC()
	}

	// Handle Ctrl+Z for background mode
	if keyStr == "ctrl+z" {
		return m.handleCtrlZ()
	}

	// Dismiss the welcome screen on any keypress
	if m.tabs.IsWelcome() {
		m.tabs.DismissWelcome()
	}

	// Handle command line input when in command mode or yes/no mode - MUST be before other handlers
	if m.commandLine.IsInCommandMode() || m.commandLine.IsInYesNoMode() || m.commandLine.IsInInputMode() || m.commandLine.IsInSearchMode() {
		cmd, handled := m.commandLine.HandleKey(msg)
		if handled {
			// Component handled the key
			return m, cmd
		}
		return m, nil
	}

	// Handle non-chat views (help, models, resume)
	if m.tabs.Content().GetActiveView() != ViewChat {
		// Allow `:` to enter command line mode even in non-chat views
		if keyStr == ":" {
			m.prompt().Blur()
			return m, m.commandLine.EnterCommandMode("")
		}
		// Vi-style search in models view
		if m.tabs.Content().GetActiveView() == ViewModels {
			if keyStr == "/" {
				m.prompt().Blur()
				return m, m.commandLine.EnterSearchMode(1) // forward
			}
			if keyStr == "?" {
				m.prompt().Blur()
				return m, m.commandLine.EnterSearchMode(-1) // backward
			}
		}
		cmd = m.tabs.UpdateContent(msg)
		// If view switched back to chat, restore focus to prompt
		if m.tabs.Content().GetActiveView() == ViewChat {
			m.prompt().Focus()
		}
		return m, cmd
	}

	// Handle modal close with 'q' or 'esc'
	if m.modal != nil && (keyStr == "q" || keyStr == "esc") {
		m.modal = nil
		// If esc was pressed, continue to handleEscape to clear completion dialog
		if keyStr == "esc" {
			// Don't return early - let handleEscape() also process this
		} else {
			// For 'q', return immediately
			return m, nil
		}
	}

	// Scroll mode activation and handling
	if (keyStr == "ctrl+b" || keyStr == "ctrl+f") && m.Mode != "scroll" {
		return m.enterScrollMode()
	}
	if m.Mode == "scroll" {
		if newModel, cmd, handled := m.handleScrollModeKey(msg); handled {
			return newModel, cmd
		}
	}

	// Handle escape key for vi mode transitions BEFORE other escape handling
	// ESC in Insert mode -> Normal mode
	if keyStr == "esc" && m.Mode == "insert" {
		// Also clear completion dialog and modal if present
		m.modal = nil
		if m.showCompletionDialog {
			m.showCompletionDialog = false
			m.completions.Hide()
			m.completionMode = ""
		}
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "normal"} }
	}
	// ESC in Visual mode -> Normal mode
	if keyStr == "esc" && m.Mode == "visual" {
		// Also clear completion dialog and modal if present
		m.modal = nil
		if m.showCompletionDialog {
			m.showCompletionDialog = false
			m.completions.Hide()
			m.completionMode = ""
		}
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "normal"} }
	}
	// ESC in Command-line mode -> Normal mode
	if keyStr == "esc" && m.Mode == "command" {
		// Hide completion dialog
		m.showCompletionDialog = false
		m.completions.Hide()
		m.completionMode = ""
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "normal"} }
	}
	// ESC in Learning mode -> Normal mode
	if keyStr == "esc" && m.Mode == "learning" {
		m.prompt().SetValue("")
		// Also clear completion dialog and modal if present
		m.modal = nil
		if m.showCompletionDialog {
			m.showCompletionDialog = false
			m.completions.Hide()
			m.completionMode = ""
		}
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "normal"} }
	}
	// ESC in Replace mode -> Normal mode
	if keyStr == "esc" && m.Mode == "replace" {
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "normal"} }
	}
	// ESC in Normal mode -> Insert mode (issue #70)
	if keyStr == "esc" && m.prompt().IsViNormalMode() {
		m.prompt().EnterViInsertMode()
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "insert"} }
	}

	// Handle escape key after modals have had a chance to process it
	if keyStr == "esc" {
		return m.handleEscape()
	}

	// Handle completion dialog
	if m.showCompletionDialog {
		return m.handleCompletionDialog(msg)
	}

	// Global Tab / Shift+Tab tab navigation for chat view
	if m.tabs.Content().GetActiveView() == ViewChat &&
		!m.showCompletionDialog &&
		m.modal == nil {
		switch keyStr {
		case "tab":
			m.tabs.NextTab()
			return m, nil
		case "shift+tab":
			m.tabs.PrevTab()
			return m, nil
		}
	}

	// Handle vi mode key bindings when in normal or visual mode
	if m.Mode == "normal" || m.Mode == "visual" {
		return m.handleViNormalMode(msg)
	}

	// Handle replace mode - replace character under cursor with next typed character
	if m.Mode == "replace" {
		return m.handleViReplaceMode(msg)
	}

	// Handle command-line mode
	if m.Mode == "command" {
		return m.handleViCommandLineMode(msg)
	}

	// Handle regular key input (when in insert mode)
	// Arrow keys only move cursor within the prompt (no history navigation in insert mode)
	switch keyStr {
	case "ctrl+o":
		m.rawMode = !m.rawMode
		return m, nil
	case ":":
		// Only enter command mode if at the beginning of input
		if m.prompt().Value() == "" {
			return m.handleColonKey(msg)
		}
		// Otherwise, just insert the colon character
		var cmd tea.Cmd
		*m.prompt(), cmd = m.prompt().Update(msg)
		return m, cmd
	case "@":
		return m.handleAtKey(msg)
	default:
		var cmd tea.Cmd
		*m.prompt(), cmd = m.prompt().Update(msg)
		return m, cmd
	}

}

func (m TUIModel) enterScrollMode() (tea.Model, tea.Cmd) {
	if m.Mode == "scroll" || m.tabs.Content().GetActiveView() != ViewChat {
		return m, nil
	}
	chat := m.tabs.Content().Chat
	if chat.Viewport.AtBottom() {
		chat.ScrollHalfPageUp()
	}
	if m.showCompletionDialog {
		m.showCompletionDialog = false
		m.completions.Hide()
		m.completionMode = ""
	}
	return m, func() tea.Msg { return ChangeModeMsg{NewMode: "scroll"} }
}

func (m TUIModel) exitScrollModeToInsert() (tea.Model, tea.Cmd) {
	if m.Mode != "scroll" {
		return m, nil
	}
	if m.showCompletionDialog {
		m.showCompletionDialog = false
		m.completions.Hide()
		m.completionMode = ""
	}
	m.prompt().Focus()
	return m, func() tea.Msg { return ChangeModeMsg{NewMode: "insert"} }
}

func (m TUIModel) handleScrollModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.Mode != "scroll" {
		return m, nil, false
	}
	chat := m.tabs.Content().Chat

	// Handle pending 'g' prefix in scroll mode
	if m.tabs.IsPendingG() {
		m.status.ViPendingOp = ""
		m.tabs.HandlePendingG(msg.String(), func() { chat.ScrollToTop() })
		return m, nil, true
	}

	switch msg.String() {
	case "ctrl+f":
		chat.ScrollPageDown()
		return m, nil, true
	case "ctrl+b":
		chat.ScrollPageUp()
		return m, nil, true
	case "ctrl+d":
		chat.ScrollHalfPageDown()
		return m, nil, true
	case "ctrl+u":
		chat.ScrollHalfPageUp()
		return m, nil, true
	case "G":
		chat.ScrollToBottom()
		return m, nil, true
	case "g":
		// Start pending g-prefix for gt/gT/gg
		m.tabs.SetPendingG()
		m.status.ViPendingOp = "g"
		return m, nil, true
	case "j", "down":
		chat.ScrollDownOneLine()
		return m, nil, true
	case "k", "up":
		chat.ScrollUpOneLine()
		return m, nil, true
	case ":":
		// Exit scroll mode before entering command mode
		// The command mode will be set by handleColonKey
		newModel, cmd := m.handleColonKey(msg)
		return newModel, cmd, true
	case "esc", "escape", "i":
		newModel, cmd := m.exitScrollModeToInsert()
		return newModel, cmd, true
	}

	return m, nil, false
}

// handleViNormalMode handles key presses when in vi normal or visual mode
func (m TUIModel) handleViNormalMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Handle pending 'g' prefix for tab navigation (gt/gT)
	if m.tabs.IsPendingG() {
		m.status.ViPendingOp = ""
		scrollToTop := func() {
			if m.tabs.Content().GetActiveView() == ViewChat {
				m.tabs.Content().Chat.ScrollToTop()
			}
		}
		m.tabs.HandlePendingG(key, scrollToTop)
		return m, nil
	}

	// Handle history navigation with arrow keys first
	// When prompt is empty, always try history navigation
	promptEmpty := m.prompt().Value() == ""
	switch key {
	case "up", "k":
		// Handle history navigation if prompt is empty or we're on the first line
		if promptEmpty || m.prompt().TextArea.Line() == 0 {
			if handled := m.handleHistoryNavigation(-1); handled {
				return m, nil
			}
		}
		// If not handled by history, pass to textarea for navigation
		var cmd tea.Cmd
		*m.prompt(), cmd = m.prompt().Update(msg)
		return m, cmd
	case "down", "j":
		// Handle history navigation if prompt is empty or we're on the last line
		lineCount := m.prompt().TextArea.LineCount()
		if promptEmpty || lineCount == 0 || m.prompt().TextArea.Line() == lineCount-1 {
			if handled := m.handleHistoryNavigation(1); handled {
				return m, nil
			}
		}
		// If not handled by history, pass to textarea for navigation
		var cmd tea.Cmd
		*m.prompt(), cmd = m.prompt().Update(msg)
		return m, cmd
	case "enter":
		// Only submit from actual normal mode to avoid interfering with visual selections
		if m.Mode != "normal" {
			break
		}
		if m.prompt().Value() == "" {
			return m, nil
		}
		return m.handleEnterKey()
	}

	// Handle mode switching keys
	switch key {
	case "i":
		// Enter insert mode at cursor
		m.prompt().EnterViInsertMode()
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "insert"} }
	case "I":
		// Enter insert mode at beginning of line
		m.prompt().TextArea.CursorStart()
		m.prompt().EnterViInsertMode()
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "insert"} }
	case "a":
		// Enter insert mode after cursor (move cursor forward first)
		// Note: In vi, 'a' appends after the current character
		m.prompt().EnterViInsertMode()
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "insert"} }
	case "A":
		// Enter insert mode at end of line
		m.prompt().TextArea.CursorEnd()
		m.prompt().EnterViInsertMode()
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "insert"} }
	case "o":
		// Open new line below and enter insert mode
		m.prompt().TextArea.CursorEnd()
		m.prompt().TextArea.InsertString("\n")
		m.prompt().EnterViInsertMode()
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "insert"} }
	case "O":
		// Open new line above and enter insert mode
		m.prompt().TextArea.CursorStart()
		m.prompt().TextArea.InsertString("\n")
		m.prompt().TextArea.CursorUp()
		m.prompt().EnterViInsertMode()
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "insert"} }
	case ":":
		// Enter command mode in the command line (bottom of screen)
		enterCmd := m.commandLine.EnterCommandMode("")
		m.prompt().Blur()
		return m, enterCmd
	case "?":
		// Show help modal
		helpText := "\n\n"
		helpText += "  j/↓     - Next history\n"
		helpText += "  k/↑     -  Previous history\n"
		helpText += "  :       - Enter command mode\n"
		helpText += "  i       - Insert mode at cursor\n"
		helpText += "  I       - Insert mode at line start\n"
		helpText += "  a       - Insert mode after cursor\n"
		helpText += "  A       - Insert mode at line end\n"
		helpText += "  o       - Open new line below\n"
		helpText += "  O       - Open new line above\n"
		helpText += "  Tab     - Next tab\n"
		helpText += "  Shift+Tab - Previous tab\n"
		helpText += "  gt      - Next tab\n"
		helpText += "  gT      - Previous tab\n"
		helpText += "  gg      - Scroll to top\n"
		helpText += "  ?       - Show this help\n\n"
		m.modal = NewBaseModal("Shortcuts Help", helpText, 80, 30)
		return m, nil
	case "#":
		// Enter learning mode
		m.prompt().EnterViLearningMode()
		m.prompt().SetValue("#")
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "learning"} }
	case "r":
		// Enter replace mode - next character will replace char under cursor
		if m.prompt().Value() != "" {
			return m, func() tea.Msg { return ChangeModeMsg{NewMode: "replace"} }
		}
		return m, nil
	case "g":
		// Start pending g-prefix for tab navigation (gt/gT) and gg
		m.tabs.SetPendingG()
		m.status.ViPendingOp = "g"
		return m, nil
	default:
		// Pass other keys to the textarea for navigation
		var cmd tea.Cmd
		*m.prompt(), cmd = m.prompt().Update(msg)
		return m, cmd
	}
}

// handleViReplaceMode handles the replace mode where next character replaces char under cursor
func (m TUIModel) handleViReplaceMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Only handle printable runes
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		replacement := msg.Runes[0]

		// Get current value and cursor position
		value := m.prompt().Value()
		lineInfo := m.prompt().TextArea.LineInfo()
		row := m.prompt().TextArea.Line()
		col := lineInfo.StartColumn + lineInfo.ColumnOffset

		// Convert to absolute position in the string
		lines := strings.Split(value, "\n")
		if row >= 0 && row < len(lines) {
			lineRunes := []rune(lines[row])
			if col >= 0 && col < len(lineRunes) {
				// Replace the character at cursor position
				lineRunes[col] = replacement
				lines[row] = string(lineRunes)
				newValue := strings.Join(lines, "\n")
				m.prompt().SetValue(newValue)

				// Restore cursor position (SetValue resets it)
				m.prompt().TextArea.SetCursor(0)
				currentRow := m.prompt().TextArea.Line()
				for currentRow < row {
					m.prompt().TextArea.CursorDown()
					currentRow = m.prompt().TextArea.Line()
				}
				m.prompt().TextArea.SetCursor(col)
			}
		}
	}

	// Return to normal mode after any key (including non-printable)
	return m, func() tea.Msg { return ChangeModeMsg{NewMode: "normal"} }
}

// handleViCommandLineMode handles key presses when in vi command-line mode
func (m TUIModel) handleViCommandLineMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "enter":
		// Execute the command and return to insert mode
		content := m.prompt().Value()
		if strings.HasPrefix(content, ":") {
			// Parse the command (keep the : prefix for display)
			parts := strings.Fields(content)
			if len(parts) > 0 {
				cmdName := parts[0]
				m.tabs.Content().Chat.AddToRawHistory("COMMAND", content)
				cmd, exists := m.commandRegistry.GetCommand(cmdName)
				if exists {
					command := cmd.Handler(&m, parts[1:])
					m.prompt().SetValue("")
					m.prompt().EnterViInsertMode() // Return to insert mode after command
					// Hide completion dialog
					m.showCompletionDialog = false
					m.completions.Hide()
					m.completionMode = ""
					return m, command
				}
				m.commandLine.AddToast(fmt.Sprintf("Unknown command: %s", cmdName), "error", time.Second*3)
				m.prompt().SetValue("")
				m.prompt().EnterViInsertMode()
				// Hide completion dialog
				m.showCompletionDialog = false
				m.completions.Hide()
				m.completionMode = ""
				return m, nil
			}
		}
		// If no command, just return to insert mode
		m.prompt().SetValue("")
		m.prompt().EnterViInsertMode()
		// Hide completion dialog
		m.showCompletionDialog = false
		m.completions.Hide()
		m.completionMode = ""
		return m, nil
	default:
		// Pass other keys to the textarea for editing the command
		var cmd tea.Cmd
		*m.prompt(), cmd = m.prompt().Update(msg)
		// Update completion dialog if it's shown
		if m.showCompletionDialog && m.completionMode == "command" {
			m.updateCommandCompletions()
		}
		return m, cmd
	}
}

// handleCtrlZ handles Ctrl+Z to send the application to background
func (m TUIModel) handleCtrlZ() (tea.Model, tea.Cmd) {
	// TODO: Fix Ctrl+Z message not showing. tea.Println doesn't work here.
	// Need to investigate proper way to show message before suspension.
	return m, tea.Sequence(
		tea.Println("⏸️  Asimi is now in the background. Use `fg` to restore."),
		tea.Suspend,
	)
}

// handleCtrlC implements the "quiet period" detection for CTRL-C handling.
// iOS and some terminals send multiple CTRL-C events for a single keypress.
// This state machine waits for a quiet period to determine when a "burst" of
// events represents a single intentional press.
//
// State machine:
//
//	Idle -> InBurst: First CTRL-C received, start quiet period timer
//	InBurst -> InBurst: More CTRL-C events, reset quiet period timer
//	InBurst -> WaitingSecond: Quiet period elapsed, first press completed
//	WaitingSecond -> InBurst: Second burst started
//	InBurst (from WaitingSecond) -> Quit: Second press completed
//	WaitingSecond -> Idle: Window expired without second press
//
// handleCtrlC: single press stops streaming, double press quits.
// Debounce ignores duplicate events arriving within CtrlCDebounceTime (terminals
// and iOS sometimes send multiple KeyCtrlC for a single physical press).
func (m TUIModel) handleCtrlC() (tea.Model, tea.Cmd) {
	now := time.Now()
	debounce := m.config.UI.CtrlCDebounceTime
	windowTime := m.config.UI.CtrlCWindowTime

	// Ignore duplicate events within debounce window
	if !m.ctrlCLastPress.IsZero() && now.Sub(m.ctrlCLastPress) < debounce {
		return m, nil
	}

	// Double press within window — quit
	if !m.ctrlCLastPress.IsZero() && now.Sub(m.ctrlCLastPress) <= windowTime {
		slog.Info("CTRL-C: double press, quitting")
		m.stopStreaming()
		m.shutdown()
		return m, tea.Quit
	}

	// Single press — cancel streaming on active tab only.
	// The daemon emits the ABORTED chat line on StreamInterruptedMsg once
	// the session actually stops; the toast below confirms the keystroke.
	m.ctrlCLastPress = now
	activeTab := m.tabs.ActiveTab()
	if activeTab.Streaming {
		// On a ritual tab, pause instead of killing the ritual goroutine.
		// This cancels only the step's LLM stream, keeping the ritual alive
		// for :continue / :abort.
		if activeTab.Type == "ritual" && isRitualChannel(activeTab.Target) && m.court != nil {
			if m.court.PauseRitual(activeTab.Target) {
				m.tabs.SetTabChatMode(activeTab.Target, true)
				chat := m.tabs.ChatByTab(activeTab.Target)
				ministerID := activeTab.CurrentMinister
				if ministerID == "" {
					ministerID = "chancellor"
				}
				chat.AddMessage(fmt.Sprintf("%s💬 Now chatting with %s — use `:continue` to resume the ritual or `:abort` to cancel",
					systemPrefix, ministerID))
			} else {
				// PauseRitual returned false — already paused.
				// Fall back to stopStreamingTab to abort the ritual.
				slog.Info("ctrl_c_ritual_not_paused_no_active_step", "tab", activeTab.Target)
				m.stopStreamingTab(activeTab.Target)
			}
		} else {
			slog.Info("ctrl_c_during_streaming", "cancelling_active_tab", activeTab.Target)
			m.stopStreamingTab(activeTab.Target)
		}
	}

	m.commandLine.AddToast(
		fmt.Sprintf("Press CTRL-C again within %.1fs to exit", windowTime.Seconds()),
		"info",
		windowTime,
	)
	return m, nil
}

// handleEscape handles the escape key and the first ctrl-c
func (m TUIModel) handleEscape() (tea.Model, tea.Cmd) {
	activeTab := m.tabs.ActiveTab()
	if activeTab.Streaming {
		// On a ritual tab, pause instead of killing the ritual goroutine.
		if activeTab.Type == "ritual" && isRitualChannel(activeTab.Target) && m.court != nil {
			if m.court.PauseRitual(activeTab.Target) {
				m.tabs.SetTabChatMode(activeTab.Target, true)
				chat := m.tabs.ChatByTab(activeTab.Target)
				ministerID := activeTab.CurrentMinister
				if ministerID == "" {
					ministerID = "chancellor"
				}
				chat.AddMessage(fmt.Sprintf("%s💬 Now chatting with %s — use `:continue` to resume the ritual or `:abort` to cancel",
					systemPrefix, ministerID))
			} else {
				// PauseRitual returned false — already paused.
				// Fall back to stopStreamingTab to abort the ritual.
				slog.Info("escape_ritual_not_paused_no_active_step", "tab", activeTab.Target)
				m.stopStreamingTab(activeTab.Target)
			}
		} else {
			slog.Info("escape_during_streaming", "cancelling_active_tab", activeTab.Target)
			m.stopStreamingTab(activeTab.Target)
		}
		return m, nil
	}

	m.modal = nil
	if m.showCompletionDialog {
		m.showCompletionDialog = false
		m.completions.Hide()
		m.completionMode = ""
	}
	return m, nil
}

// handleCompletionDialog handles the completion dialog interactions
func (m TUIModel) handleCompletionDialog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "tab":
		return m.handleCompletionSelection()
	case "down":
		m.completions.SelectNext()
		return m, nil
	case "up":
		m.completions.SelectPrev()
		return m, nil
	default:
		// Any other key press updates the completion list
		var cmd tea.Cmd
		*m.prompt(), cmd = m.prompt().Update(msg)
		if m.completionMode == "file" {
			files, err := utils.GetFileTree(".")
			if err == nil {
				m.updateFileCompletions(files)
			}
		} else if m.completionMode == "command" {
			m.updateCommandCompletions()
		}
		return m, cmd
	}
}

// handleCompletionSelection handles when a completion is selected
func (m TUIModel) handleCompletionSelection() (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	selected := m.completions.GetSelected()
	if selected != "" {
		if m.completionMode == "file" {
			filePath := selected
			content, err := os.ReadFile(filePath)
			if err != nil {
				m.commandLine.AddToast(fmt.Sprintf("Error reading file: %v", err), "error", time.Second*3)
			} else if m.court != nil {
				if err := m.court.AddSessionContextFile(m.currentTabTarget(), filePath, string(content)); err == nil {
					m.tabs.Content().Chat.AddMessage(fmt.Sprintf("Loaded file: %s", filePath))
				}
			}
			currentValue := m.prompt().Value()
			lastAt := strings.LastIndex(currentValue, "@")
			if lastAt != -1 {
				// Ensure we correctly handle the text before the @
				prefix := currentValue[:lastAt]
				// Find the end of the word being completed
				wordEnd := -1
				for i := lastAt + 1; i < len(currentValue); i++ {
					if currentValue[i] == ' ' {
						wordEnd = i
						break
					}
				}
				if wordEnd == -1 {
					wordEnd = len(currentValue)
				}
				// Replace the partial file name with the full one
				newValue := prefix + "@" + selected + " " + currentValue[wordEnd:]
				m.prompt().SetValue(strings.TrimSpace(newValue) + " ")
			} else {
				// Fallback, though we should always find an @
				m.prompt().SetValue("@" + selected + " ")
			}
		} else if m.completionMode == "command" {
			// Get command name (already has : prefix)
			cmdName := selected

			// It's a command completion
			cmd, exists := m.commandRegistry.GetCommand(cmdName)
			if exists {
				// Execute command
				cmds = append(cmds, cmd.Handler(&m, []string{}))
			}
			m.prompt().SetValue("")
		}
	}
	m.showCompletionDialog = false
	m.completions.Hide()
	m.completionMode = ""
	return m, tea.Batch(cmds...)
}

func (m *TUIModel) maybeScheduleRenderTick(chat *ChatComponent) tea.Cmd {
	if chat != nil && chat.contentDirty && !m.renderTickPending {
		m.renderTickPending = true
		// Debounce re-renders while streaming to coalesce rapid content updates.
		interval := 100 * time.Millisecond
		if m.config != nil && m.config.UI.TickInterval > 0 {
			interval = m.config.UI.TickInterval
		}
		return tea.Tick(interval, func(time.Time) tea.Msg { return chatRenderTickMsg{} })
	}
	return nil
}

func (m *TUIModel) startWaitingForResponse() tea.Cmd {
	if m.waitingForResponse {
		return nil
	}
	now := time.Now()
	m.waitingForResponse = true
	m.waitingStart = now
	m.status.StartWaiting()
	return nil
}

func (m *TUIModel) stopWaitingForResponse() {
	if !m.waitingForResponse {
		return
	}
	m.waitingForResponse = false
	m.status.StopWaiting()
	m.status.ResetStreamRate()
}

// connErrorStrings are the raw Go error strings from rpc.ErrPeerDisconnected
// and rpc.ErrClosed. We match by string because StreamErrorMsg.Err crosses
// the wire as a plain string (reconstructed via errors.New), losing the
// original error type.
var connErrorStrings = map[string]bool{
	"rpc: peer disconnected": true,
	"rpc: conn closed":       true,
}

// isConnError returns true if err is a connection-level failure
// (rpc.ErrPeerDisconnected or rpc.ErrClosed), matched by string content.
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	return connErrorStrings[err.Error()]
}

// isRitualChannel returns true if channelID matches the "e<digits>" pattern
// used by per-edict ritual channels (e.g. "e123"). All edicts, including
// edict 1 ("e1"), use this pattern for per-edict ritual tabs.
func isRitualChannel(channelID string) bool {
	if len(channelID) < 2 || channelID[0] != 'e' {
		return false
	}
	_, err := strconv.ParseUint(channelID[1:], 10, 64)
	return err == nil
}

// ritualTabLabel returns the display label for a ritual tab.
// Edict 1 is the reserved Court Infrastructure edict, so its
// tab shows as "Court" instead of "e1".
func ritualTabLabel(channelID string) string {
	if channelID == "e1" {
		return "e1·Court"
	}
	return channelID
}

// friendlyConnError converts a raw RPC connection error string into a
// user-friendly message.
func friendlyConnError(err error) string {
	switch err.Error() {
	case "rpc: peer disconnected":
		return "Connection lost"
	case "rpc: conn closed":
		return "Connection lost"
	default:
		return err.Error()
	}
}

// watchConnDrop returns a tea.Cmd that blocks on the court's ConnDone
// channel and emits a connectionLostMsg when it fires, then polls for
// reconnect and emits connectionRestoredMsg. In loopback/in-process
// mode the channel never fires, so the goroutine idles.
func watchConnDrop(courtClient courtapi.Client) tea.Cmd {
	if courtClient == nil {
		return nil
	}
	return func() tea.Msg {
		done := courtClient.ConnDone()
		<-done
		slog.Debug("tui: connection dropped, watching for reconnect")
		// Poll for reconnect: ConnDone returns a new channel after
		// reconnect completes (ReconnectingClient swaps conn). When
		// the new channel is already closed (shouldn't happen), or
		// when we detect it's still open, we wait for it to fire.
		// We poll at 200ms intervals — reconnect has backoff up to 5s.
		for i := 0; i < 150; i++ { // 30s max
			time.Sleep(200 * time.Millisecond)
			newDone := courtClient.ConnDone()
			select {
			case <-newDone:
				// Still down, keep polling
				continue
			default:
				// Reconnected
				slog.Debug("tui: connection restored")
				return connectionRestoredMsg{}
			}
		}
		// Timed out waiting for reconnect
		return connectionReconnectFailedMsg{}
	}
}

// submitToCourt sends a prompt to the appropriate minister based on active tab
// and returns a command that listens for streaming responses.
func (m *TUIModel) submitToCourt(ctx context.Context, prompt string, contextFiles map[string]string) tea.Cmd {
	if m.court == nil {
		return func() tea.Msg {
			return court.StreamErrorMsg{Err: fmt.Errorf("Court not initialized")}
		}
	}

	tab := m.tabs.ActiveTab()

	p := &court.Prompt{
		Ctx:          ctx,
		Message:      prompt,
		EdictKey:     m.currentEdictKey,
		ChannelID:    tab.Target,
		ContextFiles: contextFiles,
	}

	// On a ritual tab, route the prompt to the minister running the current
	// step (ruler interjection). The ritual is paused so the ruler can chat
	// with the minister. Stream output routes back to the ritual tab via
	// ChannelID = tab.Target (e.g. "e633").
	if tab.Type == "ritual" && isRitualChannel(tab.Target) {
		ministerID := tab.CurrentMinister
		if ministerID == "" {
			ministerID = "secretary" // fallback
		}

		// Try to pause the ritual — cancels the step's LLM stream but keeps
		// the ritual goroutine alive waiting for :continue or :abort.
		paused := false
		if m.court != nil {
			paused = m.court.PauseRitual(tab.Target)
		}
		if paused {
			m.tabs.SetTabChatMode(tab.Target, true)
			chat := m.tabs.ChatByTab(tab.Target)
			chat.AddMessage(fmt.Sprintf("%s💬 Now chatting with %s — use `:continue` to resume the ritual or `:abort` to cancel",
				systemPrefix, ministerID))

			// Submit the prompt to the minister, not the channel ID.
			// ChannelID stays as tab.Target so stream chunks route to the ritual tab.
			if err := m.court.SubmitPrompt(ministerID, p); err != nil {
				return func() tea.Msg {
					return court.StreamErrorMsg{Err: err}
				}
			}
			m.tabs.SetStreamingTabByTab(tab.Target)
			return nil
		}

		// No active ritual — restore the edict's birth session so the Ruler
		// can continue the conversation that created the edict.
		edictID, err := strconv.ParseUint(tab.Target[1:], 10, 64)
		if err != nil {
			return func() tea.Msg {
				return showSystemMsg(fmt.Sprintf("Invalid edict channel: %s", tab.Target))
			}
		}
		edict, err := m.court.GetEdict(uint(edictID))
		if err != nil || edict == nil {
			return func() tea.Msg {
				return showSystemMsg(fmt.Sprintf("Edict not found: %d", edictID))
			}
		}

		if edict.SessionID != "" {
			// Store pending prompt and creatingTab for handleSessionSelected
			m.creatingTab = tab.Target
			m.pendingPrompt = prompt
			// Trigger session loading → sessionSelectedMsg → handleSessionSelected
			return m.tabs.Content().resume.LoadSession(edict.SessionID, m.sessionStore)
		}

		// Fallback: no birth session — create a fresh session with edict context.
		// ProcessPrompt will create a new session under channelID (e.g. "e633")
		// without touching the minister's interactive session.
		p.EdictKey = m.court.EdictKey(uint(edictID))
		if err := m.court.SubmitPrompt(ministerID, p); err != nil {
			return func() tea.Msg {
				return court.StreamErrorMsg{Err: err}
			}
		}
		m.tabs.SetStreamingTabByTab(tab.Target)
		chat := m.tabs.ChatByTab(tab.Target)
		chat.AddMessage(fmt.Sprintf("%sStarted new session for edict %d (no prior session found).",
			systemPrefix, edictID))
		return nil
	}

	if err := m.court.SubmitPrompt(string(tab.Type), p); err != nil {
		return func() tea.Msg {
			return court.StreamErrorMsg{Err: err}
		}
	}

	// Mark the target tab as streaming after successful submission
	m.tabs.SetStreamingTabByTab(tab.Target)

	return nil
}

func (m *TUIModel) saveHistoryPresentState() {
	if m.historySaved {
		return
	}
	m.historyPendingPrompt = m.prompt().Value()
	if state, ok := m.currentSessionState(); ok {
		m.historyPresentSessionSnapshot = state.MessageSnapshot
	} else {
		m.historyPresentSessionSnapshot = 0
	}
	m.historyPresentChatSnapshot = len(m.tabs.Content().Chat.Messages)
	m.historySaved = true
}

func (m *TUIModel) applyHistoryEntry(entry promptHistoryEntry) {
	// Only set the prompt value, don't rollback session/chat yet
	// That will happen when user presses Enter
	m.prompt().SetValue(entry.Prompt)
	m.prompt().TextArea.CursorEnd()
}

func (m *TUIModel) restoreHistoryPresent() {
	// Only restore the prompt value, don't rollback session/chat yet
	// That will happen when user presses Enter
	if m.historySaved {
		m.prompt().SetValue(m.historyPendingPrompt)
		m.prompt().TextArea.CursorEnd()
		m.historySaved = false
		return
	}

	m.prompt().SetValue(m.historyPendingPrompt)
	m.prompt().TextArea.CursorEnd()
}

func (m *TUIModel) handleHistoryNavigation(direction int) bool {
	if len(m.sessionPromptHistory) == 0 {
		return false
	}

	switch {
	case direction < 0:
		// Navigate backwards in history (older prompts)
		if !m.historySaved {
			m.saveHistoryPresentState()
		}
		if m.historyCursor == len(m.sessionPromptHistory) {
			m.historyCursor = len(m.sessionPromptHistory) - 1
		} else if m.historyCursor > 0 {
			m.historyCursor--
		}
		if m.historyCursor >= 0 && m.historyCursor < len(m.sessionPromptHistory) {
			m.applyHistoryEntry(m.sessionPromptHistory[m.historyCursor])
			return true
		}
	case direction > 0:
		// Navigate forwards in history (newer prompts)
		if !m.historySaved {
			return false
		}
		if m.historyCursor < len(m.sessionPromptHistory)-1 {
			m.historyCursor++
			m.applyHistoryEntry(m.sessionPromptHistory[m.historyCursor])
			return true
		}
		// Reached the end of history, restore the present state
		m.historyCursor = len(m.sessionPromptHistory)
		m.restoreHistoryPresent()
		return true
	}

	return false
}

// handleEnterKey handles the enter key press
func (m TUIModel) handleEnterKey() (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	content := m.prompt().Value()
	if content == "" {
		return m, nil
	}

	// Guard: block session start if not fully configured — show YES/NO
	// prompt (same as startup onboarding) instead of a toast
	state := onboardingState(m.config)
	if state != onboardingConfigured {
		m.pendingOnboarding = true
		return m, m.enterYesNoOrAuto(onboardingPromptText(state), false)
	}

	// Handle learning mode - append to agents file
	if m.Mode == "learning" {
		// Remove the leading "#" and trim whitespace
		learningNote := strings.TrimSpace(strings.TrimPrefix(content, "#"))
		if learningNote != "" {
			// Determine agents file from config
			agentsPath := "AGENTS.md"
			if m.config != nil && m.config.Session.AgentsFile != "" {
				agentsPath = m.config.Session.AgentsFile
			}
			f, err := os.OpenFile(agentsPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
			if err != nil {
				m.commandLine.AddToast(fmt.Sprintf("Failed to open %s: %v", agentsPath, err), "error", time.Second*3)
			} else {
				defer f.Close()
				_, err = f.WriteString("\n" + learningNote + "\n")
				if err != nil {
					m.commandLine.AddToast(fmt.Sprintf("Failed to write to %s: %v", agentsPath, err), "error", time.Second*3)
				} else {
					m.commandLine.AddToast(fmt.Sprintf("Added to %s", agentsPath), "success", time.Second*2)
					m.tabs.Content().Chat.AddMessage(fmt.Sprintf("📝 Learning added: %s", learningNote))
					m.sessionActive = true
				}
			}
		}
		// Return to normal mode
		m.prompt().EnterViNormalMode()
		m.prompt().SetValue("")
		return m, func() tea.Msg { return ChangeModeMsg{NewMode: "normal"} }
	}

	if strings.HasPrefix(content, ":") {
		// Parse the command (keep the : prefix for display)
		parts := strings.Fields(content)
		if len(parts) > 0 {
			cmdName := parts[0]
			m.tabs.Content().Chat.AddToRawHistory("COMMAND", content)
			cmd, exists := m.commandRegistry.GetCommand(cmdName)
			if exists {
				command := cmd.Handler(&m, parts[1:])
				cmds = append(cmds, command)
				m.prompt().SetValue("")
				m.prompt().EnterViInsertMode()
				cmds = append(cmds, func() tea.Msg { return ChangeModeMsg{NewMode: "insert"} })
				// Ensure prompt has focus after command
				m.prompt().Focus()
			} else {
				m.commandLine.AddToast(fmt.Sprintf("Unknown command: %s", cmdName), "error", time.Second*3)
			}
		}
	} else {
		// Clear any lingering toast notifications before handling a new prompt
		m.commandLine.ClearToasts()
		m.repoInfo.RefreshDiff()

		// Check if we're submitting a historical prompt (user navigated history)
		if m.historySaved && m.historyCursor < len(m.sessionPromptHistory) {
			// User is submitting a historical prompt - rollback to that state
			entry := m.sessionPromptHistory[m.historyCursor]
			m.stopStreamingTab(m.tabs.ActiveTab().Target)
			if m.court != nil {
				_ = m.court.RollbackSession(m.currentTabTarget(), entry.SessionSnapshot)
			}
			m.tabs.Content().Chat.TruncateTo(entry.ChatSnapshot)
			m.tabs.Content().Chat.ClearToolCallMessageIndex()

			// Now continue with the normal flow from this rolled-back state
			m.historySaved = false
		}

		// Add user input to raw history
		m.tabs.Content().Chat.AddToRawHistory("USER", content)
		chatSnapshot := len(m.tabs.Content().Chat.Messages)
		state, hasSession := m.currentSessionState()
		var sessionSnapshot int
		if hasSession {
			sessionSnapshot = state.MessageSnapshot
		}
		if m.historyCursor < len(m.sessionPromptHistory) {
			m.sessionPromptHistory = m.sessionPromptHistory[:m.historyCursor]
		}
		m.tabs.Content().Chat.AddUserMessage(content)
		if m.court != nil {
			// Check if we need to auto-compact before sending the prompt (#54)
			if hasSession {
				m.autoCompactIfNeeded(state)
			}

			m.sessionActive = true
			m.prompt().SetValue("")
			// In vi mode, stay in insert mode for continued conversation
			if waitCmd := m.startWaitingForResponse(); waitCmd != nil {
				cmds = append(cmds, waitCmd)
			}
			ctx := m.tabs.ActiveTab().Ctx

			// Get context files from session (populated via @ references)
			var contextFiles map[string]string
			if hasSession {
				contextFiles = state.ContextFiles
			}
			courtCmd := m.submitToCourt(ctx, content, contextFiles)
			cmds = append(cmds, courtCmd)
		} else {
			m.commandLine.AddToast("No model configured, use :models to configure a model", "error", time.Second*5)
			m.prompt().SetValue("")
		}
		m.sessionPromptHistory = append(m.sessionPromptHistory, promptHistoryEntry{
			Prompt:          content,
			SessionSnapshot: sessionSnapshot,
			ChatSnapshot:    chatSnapshot,
		})
		m.historyCursor = len(m.sessionPromptHistory)
		m.historySaved = false
		m.historyPendingPrompt = ""

		// Save to persistent history
		if m.persistentPromptHistory != nil {
			if err := m.persistentPromptHistory.Append(content); err != nil {
				slog.Warn("failed to save prompt to history", "error", err)
			}
		}
	}
	return m, tea.Batch(cmds...)
}

// handleColonKey handles the colon key - enters command mode in command line
func (m TUIModel) handleColonKey(_ tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Enter command mode in the command line
	enterCmd := m.commandLine.EnterCommandMode("")
	m.prompt().Blur()

	// Show command completions immediately
	m.updateCommandLineCompletions()

	return m, enterCmd
}

// handleAtKey handles the @ key for file completion
func (m TUIModel) handleAtKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	*m.prompt(), _ = m.prompt().Update(msg)
	// Show completion dialog with files
	m.showCompletionDialog = true
	m.completionMode = "file"
	files, err := utils.GetFileTree(".")
	if err != nil {
		m.tabs.Content().Chat.AddMessage(fmt.Sprintf("Error scanning files: %v", err))
	} else {
		m.updateFileCompletions(files)
	}
	m.completions.Show()
	return m, nil
}

// handleShellCommand executes a shell command using the run_shell_command tool
func (m TUIModel) handleShellCommand(command string) (tea.Model, tea.Cmd) {
	// Extract the shell command (everything after !)
	shellCmd := strings.TrimSpace(strings.TrimPrefix(command, "!"))
	if shellCmd == "" {
		m.commandLine.AddToast("No command specified after !", "error", time.Second*3)
		m.prompt().Focus()
		return m, nil
	}

	m.tabs.Content().Chat.AddToRawHistory("SHELL_COMMAND", shellCmd)

	// Make session active so chat is visible (not welcome screen)
	m.sessionActive = true

	// Display the command in chat similar to a shell prompt
	m.tabs.Content().Chat.AddShellCommandInput(shellCmd)

	// Execute the shell command using the current shell runner (sandbox or host)
	return m, func() tea.Msg {
		ctx := context.Background()

		// User-initiated commands never need approval
		params := runners.Input{
			Command:        shellCmd,
			Description:    "User shell command",
			BypassApproval: true, // User explicitly requested this command
		}

		output, err := m.court.RunShellCommand(ctx, params)

		if err != nil {
			return shellCommandResultMsg{
				command:  shellCmd,
				output:   "",
				exitCode: "-1",
				err:      err,
			}
		}

		return shellCommandResultMsg{
			command:  shellCmd,
			output:   output.Output,
			exitCode: output.ExitCode,
			err:      nil,
		}
	}
}

// handleWindowSizeMsg handles window resize events
func (m TUIModel) handleWindowSizeMsg(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	m.updateComponentDimensions()

	return m, nil
}

// handleCustomMessages handles all custom message types
func (m TUIModel) handleCustomMessages(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SubmitPromptMsg:
		var cmds []tea.Cmd
		content := msg.Prompt

		// Guard: block session start if not fully configured — show YES/NO
		// prompt (same as startup onboarding) instead of a toast
		obState := onboardingState(m.config)
		if obState != onboardingConfigured {
			m.pendingOnboarding = true
			return m, m.enterYesNoOrAuto(onboardingPromptText(obState), false)
		}

		// This logic is adapted from handleEnterKey
		m.commandLine.ClearToasts()
		m.repoInfo.RefreshDiff()

		if m.historySaved && m.historyCursor < len(m.sessionPromptHistory) {
			entry := m.sessionPromptHistory[m.historyCursor]
			m.stopStreamingTab(m.tabs.ActiveTab().Target)
			if m.court != nil {
				_ = m.court.RollbackSession(m.currentTabTarget(), entry.SessionSnapshot)
			}
			m.tabs.Content().Chat.TruncateTo(entry.ChatSnapshot)
			m.tabs.Content().Chat.ClearToolCallMessageIndex()
			m.historySaved = false
		}

		m.tabs.Content().Chat.AddToRawHistory("USER", content)
		chatSnapshot := len(m.tabs.Content().Chat.Messages)
		state, hasSession := m.currentSessionState()
		var sessionSnapshot int
		if hasSession {
			sessionSnapshot = state.MessageSnapshot
		}
		if m.historyCursor < len(m.sessionPromptHistory) {
			m.sessionPromptHistory = m.sessionPromptHistory[:m.historyCursor]
		}
		m.tabs.Content().Chat.AddUserMessage(content)
		if m.court != nil {
			if hasSession {
				m.autoCompactIfNeeded(state)
			}

			m.sessionActive = true
			if waitCmd := m.startWaitingForResponse(); waitCmd != nil {
				cmds = append(cmds, waitCmd)
			}
			ctx := m.tabs.ActiveTab().Ctx

			// Get context files from session (populated via @ references)
			var contextFiles map[string]string
			if hasSession {
				contextFiles = state.ContextFiles
			}
			courtCmd := m.submitToCourt(ctx, content, contextFiles)
			cmds = append(cmds, courtCmd)
		} else {
			m.commandLine.AddToast("No model configured. Use :models to select a model", "error", time.Second*5)
		}
		m.sessionPromptHistory = append(m.sessionPromptHistory, promptHistoryEntry{
			Prompt:          content,
			SessionSnapshot: sessionSnapshot,
			ChatSnapshot:    chatSnapshot,
		})
		m.historyCursor = len(m.sessionPromptHistory)
		m.historySaved = false
		m.historyPendingPrompt = ""

		if m.persistentPromptHistory != nil {
			if err := m.persistentPromptHistory.Append(content); err != nil {
				slog.Warn("failed to save prompt to history", "error", err)
			}
		}

		return m, tea.Batch(cmds...)

	case runners.ToolCallScheduledMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("TOOL_SCHEDULED", fmt.Sprintf("%s with input: %s", msg.ToolName, msg.Input))
		chat.HandleToolCallScheduled(msg)
		return m, m.maybeScheduleRenderTick(chat)

	case runners.ToolCallExecutingMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("TOOL_EXECUTING", fmt.Sprintf("%s with input: %s", msg.ToolName, msg.Input))
		chat.HandleToolCallExecuting(msg)
		return m, m.maybeScheduleRenderTick(chat)

	case runners.ToolCallSuccessMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("TOOL_SUCCESS", fmt.Sprintf("%s\nInput: %s\nOutput: %s", msg.ToolName, msg.Input, msg.Result))
		chat.HandleToolCallSuccess(msg)
		m.repoInfo.RefreshDiffWithTTL(500 * time.Millisecond)
		return m, m.maybeScheduleRenderTick(chat)

	case runners.ToolCallErrorMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("TOOL_ERROR", fmt.Sprintf("%s\nInput: %s\nError: %v", msg.ToolName, msg.Input, msg.Error))
		chat.HandleToolCallError(msg)
		return m, m.maybeScheduleRenderTick(chat)

	case runners.ToolCallAbortedMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("TOOL_ABORTED", fmt.Sprintf("%s\nInput: %s\nReason: %s", msg.ToolName, msg.Input, msg.Reason))
		chat.HandleToolCallAborted(msg)
		return m, m.maybeScheduleRenderTick(chat)

	case errMsg:
		chat := m.tabs.Content().Chat
		chat.AddToRawHistory("ERROR", fmt.Sprintf("%v", msg.err))
		chat.AddMessage(fmt.Sprintf("Error: %v", msg.err))

	case court.StreamStartMsg:
		// Streaming has started — capture edict ID for multi-turn
		m.tabs.SetStreamingTabByTab(msg.ChannelID)
		if msg.EdictID != 0 {
			m.currentEdictKey = m.court.EdictKey(msg.EdictID)
			m.tabs.SetActiveEdictID(msg.EdictID)
		}
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("STREAM_START", "AI streaming response started")
		slog.Debug("streamStartMsg", "starting_stream", true, "edict_id", msg.EdictID)
		m.status.ClearError() // Clear any previous error state

	case court.StreamCompleteMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("STREAM_COMPLETE", "AI streaming response completed")
		slog.Debug("streamCompleteMsg", "messages_count", len(chat.Messages))
		m.tabs.ClearStreamingByTab(msg.ChannelID)
		if !m.tabs.AnyStreaming() {
			m.stopWaitingForResponse()
		}

		// Finalize the last AI message with success/failure prefix
		isFailure := chat.FinalizeLastAIMessage()
		if isFailure {
			slog.Debug("AI response marked as failure")
		}

		// Run guardrail callback if one was set
		var guardrailCmd tea.Cmd
		if m.streamCompleteCallback != nil {
			slog.Debug("running stream complete callback")
			guardrailCmd = m.streamCompleteCallback(&m)
			m.streamCompleteCallback = nil // Clear after running
		}

		m.saveSession()
		m.repoInfo.RefreshDiff()

		return m, guardrailCmd

	case court.StreamInterruptedMsg:
		// Session already stopped — just reset UI state, don't re-cancel ctx.
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("STREAM_INTERRUPTED", fmt.Sprintf("AI streaming interrupted, partial content: %s", msg.PartialContent))
		// Suppress the "ABORTED" message when the ritual tab is in chat mode
		// (paused for ruler interjection) — the step was intentionally
		// interrupted, not aborted. The pause message already told the user.
		if isRitualChannel(msg.ChannelID) {
			if tab := m.tabs.TabByTarget(msg.ChannelID); tab != nil && tab.ChatMode {
				slog.Debug("streamInterruptedMsg suppressed (ritual paused)", "channel", msg.ChannelID)
			} else {
				chat.AddMessage(systemPrefix + "ABORTED")
			}
		} else {
			chat.AddMessage(systemPrefix + "ABORTED")
		}
		slog.Debug("streamInterruptedMsg", "partial_content_length", len(msg.PartialContent))
		m.tabs.ClearStreamingByTab(msg.ChannelID)
		if !m.tabs.AnyStreaming() {
			m.stopWaitingForResponse()
		}
		m.streamCompleteCallback = nil // Clear callback on interrupt
		m.repoInfo.RefreshDiff()

	case court.StreamErrorMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("STREAM_ERROR", fmt.Sprintf("AI streaming error: %v", msg.Err))
		slog.Error("court.StreamErrorMsg", "error", msg.Err)

		connErr := isConnError(msg.Err)

		// Show partial content as a dimmed block if any was accumulated
		if strings.TrimSpace(msg.PartialContent) != "" {
			dimStyle := lipgloss.NewStyle().Foreground(globalTheme.DimTextColor)
			chat.AddMessage(dimStyle.Render(msg.PartialContent))
		}

		if connErr {
			friendlyErr := friendlyConnError(msg.Err)
			chat.AddMessage(fmt.Sprintf("\n%s⚠️  %s", systemPrefix, friendlyErr))
			chat.AddMessage(fmt.Sprintf("%s[Reconnecting… Your last message will be retried.]", systemPrefix))
			m.commandLine.AddToast(friendlyErr+" — Reconnecting…", "warning", time.Second*5)
			// Reset status but don't show error icon for connection drops
			m.clearStreamingTab(msg.ChannelID)
			m.repoInfo.RefreshDiff()
		} else {
			fullError := fmt.Sprintf("Model Error: %v", msg.Err)
			chat.AddMessage(fmt.Sprintf("\n%s❌ %s", systemPrefix, fullError))
			m.commandLine.AddToast(fullError, "error", time.Second*5)
			m.status.SetError()
			m.clearStreamingTab(msg.ChannelID)
			m.repoInfo.RefreshDiff()
		}

		/* TODO: Add the message bellow
		case streamMaxTurnsExceededMsg:
			// Max turns exceeded, mark session as inactive and show warning
			m.tabs.Content().Chat.AddToRawHistory("STREAM_MAX_TURNS_EXCEEDED", fmt.Sprintf("AI streaming ended after reaching max turns limit: %d", msg.maxTurns))
			slog.Warn("streamMaxTurnsExceededMsg", "max_turns", msg.maxTurns)
			m.tabs.Content().Chat.AddMessage(fmt.Sprintf("\n⚠️  Conversation ended after reaching maximum turn limit (%d turns)", msg.maxTurns))
			m.stopStreaming()
			m.streamCompleteCallback = nil // Clear callback on max turns
			m.repoInfo.RefreshDiff()
		*/
	case court.StreamMaxTokensReachedMsg:
		// Max tokens reached, mark session as inactive and show warning
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("STREAM_MAX_TOKENS_REACHED", fmt.Sprintf("AI response truncated due to length limit: %s", msg.Content))
		slog.Warn("streamMaxTokensReachedMsg", "content_length", len(msg.Content))
		chat.AddMessage("\n\n⚠️  Response truncated due to length limit")
		// Session already stopped — just reset UI state, don't re-cancel ctx.
		m.clearStreamingTab(msg.ChannelID)
		m.streamCompleteCallback = nil // Clear callback on max tokens
		m.repoInfo.RefreshDiff()

	// Court streaming message handlers
	case court.StreamChunkMsg:
		// Auto-create a ritual tab for "e<N>" channels that don't have one yet
		if isRitualChannel(msg.ChannelID) {
			m.addTab(ritualTabLabel(msg.ChannelID), "ritual", msg.ChannelID, nil)
		}
		// Drop chunks for tabs the user already cancelled; daemon may still
		// emit a few in-flight chunks before its ctx-cancel takes effect.
		if tab := m.tabs.TabByTarget(msg.ChannelID); tab != nil && !tab.Streaming {
			return m, nil
		}
		// Handle text chunks from Court — route to correct tab
		chat := m.tabs.ChatByTab(msg.ChannelID)
		if msg.Reasoning != "" {
			chat.AddToRawHistory("COURT_REASONING", msg.Reasoning)
			chat.AddThinkingChunk(msg.Reasoning)
		}
		if msg.Text != "" {
			chat.AddToRawHistory("COURT_TEXT", msg.Text)
			chat.AddAIChunk(msg.Text)
		}
		m.status.AddStreamChars(len(msg.Text) + len(msg.Reasoning))
		m.status.SetVerified() // LLM returned content — provider is confirmed working
		m.status.ContextPercent = msg.PercentContextUsed
		var cmds []tea.Cmd
		// Schedule the debounce tick if dirty and none pending
		if cmd := m.maybeScheduleRenderTick(chat); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.tabs.AnyStreaming() {
			m.waitingStart = time.Now()
			if !m.waitingForResponse {
				cmds = append(cmds, m.startWaitingForResponse())
			}
		}
		return m, tea.Batch(cmds...)

	case chatRenderTickMsg:
		// Debounce tick fired — flush every dirty chat. Chunks can land on a
		// non-active tab, so flushing only Content().Chat misses them.
		m.renderTickPending = false
		m.tabs.FlushDirtyChats()

	case court.MinisterInvokingMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("MINISTER_INVOKING",
			fmt.Sprintf("Minister %s invoked for edict %d", msg.MinisterID, msg.EdictKey.ID))
		taskPreview := msg.Task
		if len(taskPreview) > 60 {
			taskPreview = taskPreview[:57] + "..."
		}
		chat.AddMessage(fmt.Sprintf("%s:  %s", msg.MinisterID, taskPreview))
		chat.Indent++
		return m, nil

	case court.MinisterCompletedMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		if msg.Error != nil {
			chat.AddToRawHistory("MINISTER_FAILED",
				fmt.Sprintf("Minister %s failed: %v", msg.MinisterID, msg.Error))
			chat.AddMessage(fmt.Sprintf("%s%s %s failed: %v", systemPrefix, completeFailurePrefix, msg.MinisterID, msg.Error))
		} else {
			chat.AddToRawHistory("MINISTER_COMPLETED",
				fmt.Sprintf("Minister %s completed", msg.MinisterID))
			chat.AddMessage(fmt.Sprintf("%s%s %s completed", systemPrefix, checkPrefix, msg.MinisterID))
		}
		if chat.Indent > 0 {
			chat.Indent--
		}
		return m, nil

	case court.RitualStepMsg:
		// Auto-create a ritual tab for "e<N>" channels that don't have one yet
		if isRitualChannel(msg.ChannelID) {
			m.addTab(ritualTabLabel(msg.ChannelID), "ritual", msg.ChannelID, nil)
		}
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddToRawHistory("RITUAL_STEP",
			fmt.Sprintf("Ritual %s step %s [%d/%d] %s",
				msg.RitualName, msg.StepName, msg.StepIndex+1, msg.TotalSteps, msg.Status))
		switch msg.Status {
		// TODO: remove this and use a different Msg type
		case "info":
			chat.AddMessage(fmt.Sprintf("  %s", msg.Message))
		case "queued":
			text := fmt.Sprintf("⏳ Ritual %s queued for edict %d — waiting for another ritual to finish",
				msg.RitualName, msg.EdictID)
			m.commandLine.AddToast(text, "warning", 3*time.Second)
			queueMsg := fmt.Sprintf("⏳ Queued at %s", time.Now().Format("15:04:05"))
			if msg.QueueLen > 1 {
				queueMsg += fmt.Sprintf(" — position %d in queue", msg.QueueLen)
			} else {
				queueMsg += " — waiting for active ritual to finish"
			}
			chat.AddMessage(queueMsg)
		case "started":
			// Track the minister running the current step for ruler interjection
			if msg.MinisterID != "" {
				m.tabs.SetTabMinister(msg.ChannelID, msg.MinisterID)
			}
			text := ""
			if msg.StepName == "" {
				text = fmt.Sprintf("%sRitual %s started",
					ritualPrefix, msg.RitualName)
				if msg.EdictID != 0 {
					text = fmt.Sprintf("%s for edict %d",
						text, msg.EdictID)
				}
				// Mark the tab as streaming so CTRL-C can cancel the ritual
				m.tabs.SetStreamingTabByTab(msg.ChannelID)
			} else if msg.ForkItem != "" {
				text = fmt.Sprintf("  ⚙ %s [%s]: %s", msg.StepName, msg.ForkItem, msg.Message)
				// Re-set streaming flag for each step, since a prior step's
				// StreamCompleteMsg clears it and would cause chunks to drop.
				m.tabs.SetStreamingTabByTab(msg.ChannelID)
			} else {
				if msg.TotalSteps == 1 {
					text = fmt.Sprintf("single step: %s", msg.StepName)
				} else {
					text = fmt.Sprintf("step: %d/%d: %s",
						msg.StepIndex+1, msg.TotalSteps, msg.StepName)
					if msg.Message != "" {
						text = text + " - " + msg.Message
					}
				}
				// Re-set streaming flag for each step, since a prior step's
				// StreamCompleteMsg clears it and would cause chunks to drop.
				m.tabs.SetStreamingTabByTab(msg.ChannelID)
			}
			chat.AddMessage(text)
			if msg.StepName == "" {
				chat.Indent++
			}
		case "completed":
			if msg.Message != "" {
				chat.AddMessage(fmt.Sprintf("  %s %s", checkPrefix, msg.Message))
			} else {
				chat.AddMessage(fmt.Sprintf("  Completed: %s of %s", msg.StepName, msg.RitualName))
			}
		case "failed":
			chat.AddMessage(fmt.Sprintf("  Failed: %s of %s", msg.StepName, msg.RitualName))
		case "aborted":
			chat.AddMessage(fmt.Sprintf("  Aborted: %s of %s", msg.StepName, msg.RitualName))
		case "retrying":
			chat.AddMessage(fmt.Sprintf("  Retrying: %s of %s", msg.StepName, msg.RitualName))
		case "ritual_completed":
			m.tabs.SetTabChatMode(msg.ChannelID, false)
			text := fmt.Sprintf("%sRitual %s for edict %d completed in %s",
				ritualPrefix, msg.RitualName, msg.EdictID, msg.Message)
			chat.AddMessage(text)
			chat.Indent--
		case "ritual_failed":
			m.tabs.SetTabChatMode(msg.ChannelID, false)
			chat.AddMessage(fmt.Sprintf("%s Ritual %s failed: %s", completeFailurePrefix, msg.RitualName, msg.Message))
			if chat.Indent > 0 {
				chat.Indent--
			}
		}
		return m, nil

	case court.EventNotificationMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		var cmds []tea.Cmd

		switch msg.EventType {
		case storage.EventCourtStarted:
			projectDir := ""
			if m.repoInfo != nil {
				projectDir = m.repoInfo.ProjectRoot
			}
			ms, err := ministers.LoadAllMinisters(projectDir)
			if err != nil {
				slog.Warn("failed to load ministers for greetings", "error", err)
			}
			initTabGreetings(&m.tabs, ms)
			return m, nil
		case storage.EventCourtReady:
			text := fmt.Sprintf("%sREADY at %s", courtPrefix, time.Now().Format("2 January, 3:04 PM MST"))
			chat.AddToRawHistory("CONTEXT", text)
			m.commandLine.AddToast(text, "info", 3*time.Second)
		case storage.EventEdictCreated:
			intent, _ := msg.Payload["intent"].(string)
			id := msg.EdictKey.ID
			if intent == "" {
				intent = "New edict"
			}
			// Truncate long intents for display
			// TODO: Replace it with and edict's summary
			if len(intent) > 60 {
				intent = intent[:57] + "..."
			}
			text := fmt.Sprintf("%sEdict %d Created: %s", "📜 ", id, intent)
			chat.AddToRawHistory("CONTEXT", text)
			m.commandLine.AddToast(text, "info", 3*time.Second)
			// Prompt the ruler to enact swift-strike
			m.pendingRitualEnact = &pendingRitualEnact{edictID: id, intent: intent}
			cmds = append(cmds, m.enterYesNoOrAuto(fmt.Sprintf("Enact swift-strike for edict %d?", id), true))
		case storage.EventEdictSealed:
			text := fmt.Sprintf("✅ Edict %d sealed and ascended to Heaven", msg.EdictKey.ID)
			chat.AddToRawHistory("CONTEXT", text)
			m.commandLine.AddToast(text, "success", 3*time.Second)
		case storage.EventSealGranted:
			minister, _ := msg.Payload["minister_id"].(string)
			if minister == "" {
				minister = "Unknown"
			}
			if minister == "ruler" {
				// Ruler's seal is final — show an ephemeral toast instead of
				// a lingering chat message.
				m.commandLine.AddToast(
					fmt.Sprintf("%s Edict %d sealed", sealPrefix, msg.EdictKey.ID),
					"success", 4*time.Second,
				)
				return m, nil
			}
			text := fmt.Sprintf("%sMinister %s sealed edict %d", sealPrefix, minister, msg.EdictKey.ID)
			// Re-query seals to show fresh seal chain with the minister's seal
			updatedSeals, err := m.court.GetEdictSeals(msg.EdictKey)
			if err != nil {
				text += fmt.Sprintf("\n  (failed to refresh seal chain: %v)", err)
			} else {
				text += fmt.Sprintf("\n  %s", renderSealChain(updatedSeals, 60))
			}
			chat.AddToRawHistory("CONTEXT", text)
			m.commandLine.AddToast(text, "success", 3*time.Second)
		case storage.EventRitualCompleted:
			ritualName, _ := msg.Payload["ritual"].(string)
			duration, _ := msg.Payload["duration"].(string)
			m.commandLine.AddToast(
				fmt.Sprintf("✅ Ritual %s completed in %s", ritualName, duration),
				"success", 4*time.Second,
			)
			return m, nil
		case storage.EventManifestCommitted:
			text := fmt.Sprintf("🔨 Forge committed manifest for edict %d", msg.EdictKey.ID)
			chat.AddToRawHistory("CONTEXT", text)
			m.commandLine.AddToast(text, "info", 3*time.Second)
		case storage.EventZhengmingNeeded:
			summary, _ := msg.Payload["summary"].(string)
			if summary == "" {
				summary = "clarification needed"
			}
			chat.AddToRawHistory("EVENT_NOTIFICATION",
				fmt.Sprintf("Zhengming requested for edict %d: %s", msg.EdictKey.ID, summary))
			chat.AddMessage(fmt.Sprintf("❓ Zhengming requested for edict %d: %s", msg.EdictKey.ID, summary))
		case storage.EventZhengmingAnswered:
			if msg.EdictKey.ID == 0 {
				text := checkPrefix + " Zhengming answered for the court"
				chat.AddToRawHistory("CONTEXT", text)
				m.commandLine.AddToast(text, "info", 3*time.Second)
			} else {
				answer, _ := msg.Payload["answer"].(string)
				text := fmt.Sprintf("%sAnswered for e%d: %s", checkPrefix, msg.EdictKey.ID, answer)
				chat.AddToRawHistory("CONTEXT", text)
				m.commandLine.AddToast(text, "info", 3*time.Second)
			}
		case storage.EventEdictCancelled:
			text := fmt.Sprintf("⛔ Edict %d cancelled", msg.EdictKey.ID)
			chat.AddToRawHistory("CONTEXT", text)
			m.commandLine.AddToast(text, "info", 3*time.Second)
		}

		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, nil

	case court.StreamDoneMsg:
		return m, nil

	case court.EventsDrainedMsg:
		chat := m.tabs.ChatByTab(msg.ChannelID)
		chat.AddMessage(fmt.Sprintf("%sRecovered %d event(s) from previous session:", systemPrefix, len(msg.Events)))
		for _, ev := range msg.Events {
			detail := fmt.Sprintf("%s  %s", systemPrefix, ev.EventType)
			if ev.EdictKey.ID != 0 {
				detail += fmt.Sprintf(" [edict:%d]", ev.EdictKey.ID)
			}
			chat.AddMessage(detail)
		}
		return m, nil

	case court.ZhengmingPendingMsg:
		// A zhengming halts the court — route to the minister's own tab
		// so the Ruler sees the context, then auto-scroll the chat.
		// Falls back to the active tab if no tab exists for the minister.
		if m.tabs.SwitchToTarget(msg.MinisterID) {
			if chat := m.tabs.ChatByTab(msg.MinisterID); chat != nil {
				chat.ScrollToBottom()
			}
		}
		m.prompt().HandleZhengmingPending(msg)
		return m, nil

	case AnsweredMsg:
		m.prompt().ExitAnsweringMode()
		// Check if this is an edict action menu response
		if edictID, ok := parseEdictActionRequestID(msg.RequestID); ok {
			return m, dispatchEdictAction(&m, edictID, msg.Answers)
		}
		go m.handleAnsweringComplete(msg)
		return m, nil

	case AnsweringCancelMsg:
		m.prompt().ExitAnsweringMode()
		// Edict action menu cancel — return to edicts list
		if _, ok := parseEdictActionRequestID(msg.RequestID); ok {
			return m, reloadEdictsListCmd(&m)
		}
		go m.handleAnsweringComplete(AnsweredMsg{RequestID: msg.RequestID, Answers: []string{tools.AnswerChat}})
		return m, nil

	case AnsweringEditMsg:
		// Check if this is an edict action menu edit
		if edictID, ok := parseEdictActionRequestID(msg.RequestID); ok {
			m.prompt().ExitAnsweringMode()
			return m, editEdictIntentCmd(&m, edictID)
		}

		// Open the question text in $EDITOR and update it if modified
		originalText := msg.Question
		return m, func() tea.Msg {
			// Create temp file with question content
			tmpFile, err := os.CreateTemp("", "zhengming_edit_*.md")
			if err != nil {
				slog.Error("failed to create temp file for editing", "error", err)
				return AnsweringEditDoneMsg{RequestID: msg.RequestID, Question: originalText, OriginalQuestion: originalText}
			}
			tmpPath := tmpFile.Name()
			tmpFile.Close()

			if err := os.WriteFile(tmpPath, []byte(originalText), 0644); err != nil {
				slog.Error("failed to write temp file for editing", "error", err)
				os.Remove(tmpPath)
				return AnsweringEditDoneMsg{RequestID: msg.RequestID, Question: originalText, OriginalQuestion: originalText}
			}

			// Get editor
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}

			// Run the editor via tea.ExecProcess
			cmd := exec.Command(editor, tmpPath)
			return tea.ExecProcess(cmd, func(err error) tea.Msg {
				defer os.Remove(tmpPath)
				if err != nil {
					slog.Error("editor failed", "error", err)
					return AnsweringEditDoneMsg{RequestID: msg.RequestID, Question: originalText, OriginalQuestion: originalText}
				}

				// Read modified content
				modifiedContent, err := os.ReadFile(tmpPath)
				if err != nil {
					slog.Error("failed to read modified file", "error", err)
					return AnsweringEditDoneMsg{RequestID: msg.RequestID, Question: originalText, OriginalQuestion: originalText}
				}

				// Trim trailing whitespace for comparison
				modified := strings.TrimRight(string(modifiedContent), " \t\n\r")
				original := strings.TrimRight(originalText, " \t\n\r")
				if modified == original {
					// No changes, keep original
					return AnsweringEditDoneMsg{RequestID: msg.RequestID, Question: originalText, OriginalQuestion: originalText}
				}

				// Update the question with modified content
				return AnsweringEditDoneMsg{RequestID: msg.RequestID, Question: modified, OriginalQuestion: originalText}
			})
		}

	case AnsweringEditDoneMsg:
		// Exit answering mode
		m.prompt().ExitAnsweringMode()

		// Check if content was modified
		modified := strings.TrimRight(msg.Question, " \t\n\r")
		original := strings.TrimRight(msg.OriginalQuestion, " \t\n\r")

		if modified != original {
			// Content was modified - send it back to the Sage as a chat message
			if m.court != nil {
				_ = m.court.AddSessionMessage(m.currentTabTarget(), "human", msg.Question)
			}
			// Cancel the pending zhengming so the Sage can re-suggest with modified content
			if m.court != nil {
				m.court.CancelZhengming(msg.RequestID)
			}
		}
		return m, nil

	case tools.EditorRequest:
		// In handsoff mode, auto-approve without opening $EDITOR. The tool
		// (e.g. approve_doc → suggest_edict for large payloads) blocks on
		// ResultChan, so we must reply or the call hangs forever.
		if m.handsoff {
			msg.ResultChan <- tools.EditorResult{Content: msg.Content, Saved: true}
			return m, nil
		}
		// A tool wants to open content in $EDITOR — TUI owns the temp-file
		// lifecycle so the host running the editor doesn't need to share a
		// filesystem with whoever generated the content (daemon case).
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		ext := ".md"
		if msg.Filename != "" {
			if i := strings.LastIndex(msg.Filename, "."); i >= 0 {
				ext = msg.Filename[i:]
			}
		}
		f, err := os.CreateTemp("", "approve_doc_*"+ext)
		if err != nil {
			msg.ResultChan <- tools.EditorResult{Err: fmt.Errorf("create temp file: %w", err)}
			return m, nil
		}
		tmpPath := f.Name()
		f.Close()
		orig := []byte(msg.Content)
		if err := os.WriteFile(tmpPath, orig, 0644); err != nil {
			os.Remove(tmpPath)
			msg.ResultChan <- tools.EditorResult{Err: fmt.Errorf("write temp file: %w", err)}
			return m, nil
		}
		stat, err := os.Stat(tmpPath)
		if err != nil {
			os.Remove(tmpPath)
			msg.ResultChan <- tools.EditorResult{Err: fmt.Errorf("stat temp file: %w", err)}
			return m, nil
		}
		cmd := exec.Command(editor, tmpPath)
		resultChan := msg.ResultChan
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			return editorResultMsg{
				ResultChan: resultChan,
				TmpPath:    tmpPath,
				OrigBytes:  orig,
				OrigMtime:  stat.ModTime(),
				Err:        err,
			}
		})

	case editorResultMsg:
		defer os.Remove(msg.TmpPath)
		if msg.Err != nil {
			msg.ResultChan <- tools.EditorResult{Err: msg.Err}
			return m, nil
		}
		stat, err := os.Stat(msg.TmpPath)
		if err != nil {
			msg.ResultChan <- tools.EditorResult{Err: fmt.Errorf("stat after edit: %w", err)}
			return m, nil
		}
		// mtime unchanged → user quit without saving (vi :q!).
		if stat.ModTime().Equal(msg.OrigMtime) {
			msg.ResultChan <- tools.EditorResult{Saved: false}
			return m, nil
		}
		content, err := os.ReadFile(msg.TmpPath)
		if err != nil {
			msg.ResultChan <- tools.EditorResult{Err: fmt.Errorf("read after edit: %w", err)}
			return m, nil
		}
		msg.ResultChan <- tools.EditorResult{Content: string(content), Saved: true}
		return m, nil

	case showHelpMsg:
		// Show the help viewer with the requested topic
		return m, m.tabs.Content().ShowHelp(msg.topic)

	case showContextMsg:
		m.tabs.Content().Chat.AddToRawHistory("CONTEXT", msg.content)
		m.tabs.Content().Chat.AddMessage(msg.content)
		m.sessionActive = true

	case toastMsg:
		m.commandLine.AddToast(msg.message, msg.msgType, msg.timeout)

	case updateCheckMsg:
		if msg.err != nil {
			m.tabs.Content().Chat.AddMessage(fmt.Sprintf("%s❌ Failed to check for updates: %v", systemPrefix, msg.err))
			return m, nil
		}

		if !msg.hasUpdate {
			m.tabs.Content().Chat.AddMessage(fmt.Sprintf("%s✓ You're running the latest version (%s)", systemPrefix, msg.latest))
			return m, nil
		}

		// Update available - ask for confirmation
		question := fmt.Sprintf("%sUpdate available: %s → %s. Do you want to update now?", systemPrefix, utils.AsimiVersion, msg.latest)
		return m, m.enterYesNoOrAuto(question, true)

	case updateAvailableMsg:
		// Background update check found a new version - just set the flag
		// The home view will display the notification
		m.updateAvailable = true
		return m, nil

	case onboardingPromptMsg:
		// Show YES/NO prompt on home view when unconfigured (first run or missing provider/model)
		state := onboardingState(m.config)
		if m.onboardingDeclined || state == onboardingConfigured {
			return m, nil
		}
		m.pendingOnboarding = true
		return m, m.enterYesNoOrAuto(onboardingPromptText(state), false)

	case yesNoResponseMsg:
		// Check if this is a response to the onboarding model selection prompt
		if m.pendingOnboarding {
			m.pendingOnboarding = false
			if msg.answer {
				if onboardingState(m.config) == onboardingNeedsLogin {
					return m, handleLoginCommand(&m, nil)
				}
				return m, handleModelsCommand(&m, nil)
			}
			// User declined — quit with a clear message
			m.onboardingDeclined = true
			if onboardingState(m.config) == onboardingNeedsLogin {
				fmt.Fprintln(os.Stderr, "Asimi needs a provider to function. Run `asimi` again and use :login, or edit ~/.config/asimi/asimi.conf manually.")
			} else {
				fmt.Fprintln(os.Stderr, "Asimi needs a model to function. Run `asimi` again and use :models, or edit ~/.config/asimi/asimi.conf manually.")
			}
			return m, tea.Quit
		}

		// Check if this is a response to a ritual enactment request
		if m.pendingRitualEnact != nil {
			edictID := m.pendingRitualEnact.edictID
			m.pendingRitualEnact = nil
			if msg.answer {
				// User confirmed - pre-create the ritual tab immediately
				// for instant visual feedback, then delegate PublishEvent
				// to the async tea.Cmd wrapper (same pattern as the
				// "Implement" action path).
				channelID := fmt.Sprintf("e%d", edictID)
				m.addTab(ritualTabLabel(channelID), "ritual", channelID, func(tab *Tab) tea.Cmd {
					chat := m.tabs.ChatByTab(channelID)
					chat.AddMessage(fmt.Sprintf("%sPreparing ritual for edict %d…", ritualPrefix, edictID))
					return nil
				})
				return m, enactRitualForEdict(&m, edictID, "swift-strike")
			}
			// No explicit "declined" message needed; the 📜 notification already shows
			return m, nil
		}

		// Check if this is a response to a seal override request
		if m.pendingSealOverride != nil {
			if msg.answer {
				// User confirmed - proceed with sealing, then return to edicts list
				edictID := m.pendingSealOverride.edictID
				notes := m.pendingSealOverride.notes
				m.pendingSealOverride = nil
				return m, grantRulerSealCmd(&m, edictID, notes)
			} else {
				// User declined — go back to edicts list
				m.pendingSealOverride = nil
				return m, reloadEdictsListCmd(&m)
			}
		}

		// Check if this is a response to an edict cancel request
		if m.pendingEdictCancel != nil {
			edictID := m.pendingEdictCancel.edictID
			m.pendingEdictCancel = nil
			if msg.answer {
				return m, cancelEdictCmd(&m, edictID)
			}
			return m, reloadEdictsListCmd(&m)
		}

		// Check if this is a response to a host command approval request
		if m.pendingHostApproval != nil {
			// Send the response back to the waiting goroutine
			m.pendingHostApproval.ResponseChan <- msg.answer
			if msg.answer {
				// User approved - update the emoji to ⚙️ (executing)
				m.tabs.Content().Chat.UpdateLastToolCallEmoji(m.pendingHostApproval.Command, "⚙️")
			} else {
				// User denied - update the emoji to ⛔︎
				m.tabs.Content().Chat.UpdateLastToolCallEmoji(m.pendingHostApproval.Command, "⛔︎")
			}
			m.pendingHostApproval = nil
			return m, nil
		}

		// Otherwise, this is an update confirmation
		if msg.answer {
			// User confirmed update
			return m, handleUpdateConfirm(&m)
		}
		// User declined
		cancelMsg := NewChatMsgBuilder(systemPrefix)
		cancelMsg.WriteLn("Update cancelled.")
		cancelMsg.WriteLn("Please run :update again when ready")
		m.tabs.Content().Chat.AddMessage(cancelMsg.String())
		return m, nil

	case inputResponseMsg:
		// Handle API key input if pending
		if m.pendingAPIKeyProvider != "" {
			provider := m.pendingAPIKeyProvider
			m.pendingAPIKeyProvider = ""
			return m, handleAPIKeyInput(&m, provider, msg.text)
		}

		// Handle manual model name entry if pending
		if m.pendingModelNameProvider != "" {
			provider := m.pendingModelNameProvider
			m.pendingModelNameProvider = ""
			if msg.text == "" {
				return m, nil
			}
			return m, func() tea.Msg {
				return modelSelectedMsg{model: &Model{ID: msg.text, Provider: provider, Status: "active"}}
			}
		}

	case apiKeyPromptMsg:
		m.pendingAPIKeyProvider = msg.provider
		prompt := fmt.Sprintf("Enter your %s API key:", providerDisplayName(msg.provider))
		return m, m.commandLine.EnterInputMode(prompt)

	case enterModelNameMsg:
		m.pendingModelNameProvider = msg.provider
		prompt := fmt.Sprintf("Enter model name for %s:", providerDisplayName(msg.provider))
		return m, m.commandLine.EnterInputMode(prompt)

	case runners.ApprovalRequestMsg:
		// Store the pending approval request
		m.pendingHostApproval = &msg
		m.tabs.Content().Chat.UpdateLastToolCallEmoji(msg.Command, approvalPrefix)
		displayCmd := msg.Command
		maxLen := 50
		// TODO: truncate the middle
		if len(displayCmd) > maxLen {
			displayCmd = displayCmd[:maxLen] + "..."
		}
		return m, m.enterYesNoOrAuto(fmt.Sprintf("Allow `%s` to run?", displayCmd), true)

	case updateCompleteMsg:
		if msg.err != nil {
			errMsg := NewChatMsgBuilder(systemPrefix)
			errMsg.WriteLnf("❌ Update failed: %v", msg.err)
			errMsg.WriteLnf("Try updating manually with: %s", GetUpdateCommand())
			m.tabs.Content().Chat.AddMessage(errMsg.String())
			return m, nil
		}

		successMsg := NewChatMsgBuilder(systemPrefix)
		successMsg.WriteLn("✓ Update successful!")
		successMsg.WriteLn("Please restart asimi to use the new version.")
		m.tabs.Content().Chat.AddMessage(successMsg.String())
		return m, nil

	case tickMsg:
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })

	case showOauthFailed:
		m.tabs.Content().Chat.AddToRawHistory("OAUTH_ERROR", msg.err)
		errToast := fmt.Sprintf("OAuth failed: %s", msg.err)
		m.commandLine.AddToast(errToast, "error", 4000)
		m.tabs.Content().Chat.AddMessage(errToast)
		m.sessionActive = false

	case modelSelectedMsg:
		if msg.onSelect != nil {
			return m, msg.onSelect
		}
		oldProvider := m.config.LLM.Provider
		oldModel := m.config.LLM.Model

		// Update provider and model
		m.config.LLM.Provider = msg.model.Provider
		m.config.LLM.Model = msg.model.ID

		// Load API key for the new provider if needed
		if msg.model.Provider != oldProvider {
			// Clear existing API key when switching providers
			m.config.LLM.APIKey = ""

			// Credentials will be loaded by getModelClient() from keyring
		}

		// Save config and reinitialize session
		if err := config.SaveConfig(m.config); err != nil {
			slog.Error("Failed to save config", "error", err)
			m.commandLine.AddToast("Failed to save config", "error", 4000)
			// Revert changes
			m.config.LLM.Provider = oldProvider
			m.config.LLM.Model = oldModel
		} else {
			modelName := msg.model.DisplayName
			if modelName == "" {
				modelName = msg.model.ID
			}
			providerChanged := ""
			if msg.model.Provider != oldProvider {
				providerChanged = fmt.Sprintf(" (switched to %s)", msg.model.Provider)
			}
			m.commandLine.AddToast(fmt.Sprintf("Model changed to %s%s", modelName, providerChanged), "success", 3000)
			return m, m.switchModel()
		}
		return m, nil

	case modelsLoadedMsg:
		return m, m.tabs.Content().ShowUnifiedModels(msg.models, m.config.LLM.Model)

	case showModelSelectionMsg:
		// Trigger model selection after login completes
		return m, handleModelsCommand(&m, nil)

	case apiKeySavedMsg:
		m.commandLine.AddToast(fmt.Sprintf("API key saved for %s", providerDisplayName(msg.provider)), "success", 3000)
		return m, handleModelsCommand(&m, nil)

	case modelsLoadErrorMsg:
		m.tabs.Content().SetModelsError(msg.error)

	case searchExecutedMsg:
		c := m.tabs.Content()
		newIndex := c.models.Search(msg.pattern, msg.direction, c.selectedItem)
		if newIndex < 0 {
			m.commandLine.AddToast("Pattern not found", "warning", 3*time.Second)
		} else {
			c.selectedItem = newIndex
			visibleSlots := c.models.GetVisibleSlots()
			if c.selectedItem < c.scrollOffset {
				c.scrollOffset = c.selectedItem
			} else if c.selectedItem >= c.scrollOffset+visibleSlots {
				c.scrollOffset = c.selectedItem - visibleSlots + 1
			}
			total := c.models.MatchCount()
			cur := c.models.CurrentMatchNumber()
			m.commandLine.AddToast(fmt.Sprintf("Match %d/%d: %q", cur, total, msg.pattern), "info", 3*time.Second)
		}
		return m, nil

	case searchCancelledMsg:
		// No-op, stay in models view
		return m, nil

	case sessionsLoadedMsg:
		return m, m.tabs.Content().ShowResume(msg.sessions)

	case edictsLoadedMsg:
		return m, m.tabs.Content().ShowEdictSelection(msg.edicts)

	case edictSelectedMsg:
		// Show the edict action menu for the selected edict
		return m, showEdictActionMenu(&m, msg.edictID)

	case showEdictDashboardMsg:
		return m, m.tabs.Content().ShowEdictDashboard(msg.content)

	case edictIntentUpdatedMsg:
		m.tabs.Content().Chat.AddToRawHistory("CONTEXT", systemPrefix+msg.message)
		toastType := "success"
		switch {
		case strings.Contains(msg.message, "Failed"):
			toastType = "error"
		case strings.Contains(msg.message, "No changes"):
			toastType = "info"
		}
		m.commandLine.AddToast(msg.message, toastType, 3*time.Second)
		m.sessionActive = true
		return m, reloadEdictsListCmd(&m)

	case reloadEdictsMsg:
		return m, reloadEdictsListCmd(&m)

	case ChangeModeMsg:
		// Centralized mode change handling
		oldMode := m.Mode
		newMode := msg.NewMode

		// Update mode
		m.Mode = newMode
		m.status.SetMode(newMode)

		if newMode != "command" && newMode != "yesno" && newMode != "input" {
			m.commandLine.Blur()
		}

		// Handle scroll lock state changes
		if oldMode == "scroll" && newMode != "scroll" {
			m.tabs.Content().Chat.SetScrollLock(false)
		} else if oldMode != "scroll" && newMode == "scroll" {
			m.tabs.Content().Chat.SetScrollLock(true)
		}

		// Update prompt component based on new mode
		switch newMode {
		case "insert":
			m.prompt().EnterViInsertMode()
		case "normal":
			m.prompt().EnterViNormalMode()
		case "visual":
			// Visual mode uses normal keymap but different styling
			m.prompt().ViCurrentMode = ViModeVisual
			m.prompt().TextArea.KeyMap = m.prompt().viNormalKeyMap
			m.prompt().TextArea.Placeholder = "Visual selection mode"
			// Trigger style update by calling the private method via a public interface
			// For now, we'll just set it directly since we're in the same package
			if globalTheme != nil {
				m.prompt().Style = m.prompt().Style.BorderForeground(globalTheme.PromptOffBorder)
			}
		case "scroll":
			m.prompt().Blur()
			m.prompt().EnterViScrollMode()
		case "command":
			m.prompt().EnterViCommandLineMode()
		case "yesno":
			m.prompt().Style = m.prompt().Style.BorderForeground(globalTheme.PromptOffBorder)
		case "input":
			// Input mode - hide prompt to show command line input
			m.prompt().Blur()
		case "learning":
			m.prompt().EnterViLearningMode()
		case "search":
			m.prompt().Blur()
			m.prompt().Style = m.prompt().Style.BorderForeground(globalTheme.PromptOffBorder)
		case "models":
			m.prompt().Blur()
			m.prompt().TextArea.Placeholder = "j/k navigate | /? search | n/N next match | Enter to select | ESC to abort"
			m.prompt().Style = m.prompt().Style.BorderForeground(globalTheme.PromptOffBorder)
		case "select", "resume", "help":
			// These modes don't need prompt updates, just placeholder changes
			m.prompt().Blur()
			m.prompt().TextArea.Placeholder = "j/k, CTRL-D/U to navigate | Enter to select | ESC to abort"
			m.prompt().Style = m.prompt().Style.BorderForeground(globalTheme.PromptOffBorder)
		}

		return m, nil

	case commandReadyMsg:
		// Command ready from command line component
		// Hide completion dialog
		m.showCompletionDialog = false
		m.completions.Hide()
		m.completionMode = ""

		// Save to persistent command history
		if m.persistentCommandHistory != nil {
			if err := m.persistentCommandHistory.Append(msg.command); err != nil {
				slog.Warn("failed to save command to history", "error", err)
			}
		}

		// Check if this is a shell command (starts with !)
		if strings.HasPrefix(msg.command, "!") {
			return m.handleShellCommand(msg.command)
		}

		// Parse and execute the command
		parts := strings.Fields(":" + msg.command)
		if len(parts) > 0 {
			cmdName := parts[0]
			m.tabs.Content().Chat.AddToRawHistory("COMMAND", ":"+msg.command)

			// Use FindCommand for vim-style partial matching
			cmd, matches, found := m.commandRegistry.FindCommand(cmdName)
			if found {
				c := cmd.Handler(&m, parts[1:])
				m.prompt().Focus()
				return m, c
			} else if len(matches) > 1 {
				// Ambiguous command
				displayMatches := make([]string, len(matches))
				for i, match := range matches {
					displayMatches[i] = ":" + strings.TrimPrefix(match, "/")
				}
				errorMsg := fmt.Sprintf("Ambiguous command '%s'. Matches: %s",
					strings.TrimPrefix(cmdName, ":"),
					strings.Join(displayMatches, ", "))
				m.commandLine.AddToast(errorMsg, "error", time.Second*3)
			} else {
				// Unknown command
				m.commandLine.AddToast(fmt.Sprintf("Unknown command: %s", strings.TrimPrefix(cmdName, ":")), "error", time.Second*3)
			}
		}
		m.prompt().Focus()
		return m, nil

	case commandCancelledMsg:
		// Command cancelled - hide completions and restore focus
		m.showCompletionDialog = false
		m.completions.Hide()
		m.completionMode = ""
		m.prompt().Focus()
		return m, nil

	case commandTextChangedMsg:
		// Command text changed - update completions
		m.updateCommandLineCompletions()
		return m, nil

	case navigateCompletionMsg:
		// Navigate completion dialog
		if msg.direction < 0 {
			m.completions.SelectPrev()
		} else {
			m.completions.SelectNext()
		}
		return m, nil

	case navigateHistoryMsg:
		// Navigate completion if visible, otherwise do nothing
		if m.showCompletionDialog {
			if msg.direction < 0 {
				m.completions.SelectPrev()
			} else {
				m.completions.SelectNext()
			}
		}
		return m, nil

	case acceptCompletionMsg:
		// Accept completion if visible
		if m.showCompletionDialog {
			selected := m.completions.GetSelected()
			if selected != "" {
				// Set the command text to the selected completion (without the : prefix)
				cmdText := strings.TrimPrefix(selected, ":")
				m.commandLine.SetCommand(cmdText)
			}
		}
		return m, nil

	case sessionSelectedMsg:
		cmd := m.handleSessionSelected(msg.session)
		if cmd != nil {
			return m, cmd
		}
		return m, nil

	case resumeRebuildBatchMsg:
		cmd := m.handleResumeRebuildBatch(msg)
		if cmd != nil {
			return m, cmd
		}
		return m, nil

	case sessionResumeErrorMsg:
		m.commandLine.AddToast(fmt.Sprintf("Failed to resume session: %v", msg.err), "error", 4000)
		return m, m.tabs.Content().ShowChat()

	case llmInitSuccessMsg:
		// LLM is configured daemon-side by now; just reflect readiness
		// in the status bar. Client init success doesn't mean verified.
		m.status.SetProvider(m.config.LLM.Provider, m.config.LLM.Model, false)

		// Fire court_started event to trigger wakeup ritual and health checks
		latest, hasUpdate, err := utils.CheckForUpdates(context.Background())
		latestVersion := ""
		if err == nil {
			latestVersion = latest.Version
		}
		m.raiseCourtEvent(storage.EventCourtStarted, storage.JSON{
			"latest_version":  latestVersion,
			"has_update":      hasUpdate,
			"current_version": utils.AsimiVersion})

		return m, nil

	case llmInitErrorMsg:
		slog.Warn("LLM initialization failed", "error", msg.err)
		m.commandLine.AddToast("Running without a model, use `:help models` to configure", "warning", 5000)

	case startConversationMsg:
		// Handle starting a new conversation (used by init, new, and other commands)
		slog.Debug("got startConversationMsg", "RunOnHost", msg.RunOnHost, "tryUpgradeToSandbox", msg.tryUpgradeToSandbox)

		// Clear history if requested - this can happen even without court
		if msg.clearHistory {
			m.sessionActive = true
			// Clear the chat instead of creating a new component to avoid re-initializing the markdown renderer
			m.tabs.Content().Chat.Clear()

			// Reset prompt history and waiting state
			m.initHistory()
			m.stopStreamingTab(m.tabs.ActiveTab().Target)

			// Reset session conversation history
			if m.court != nil {
				_ = m.court.ClearSessionHistory(m.currentTabTarget())
			}
		}

		// Display initial messages after clearing history (before streaming starts)
		for _, initialMsg := range msg.initialMessages {
			m.tabs.Content().Chat.AddMessage(initialMsg)
		}

		// The rest of the operations require a court
		if m.court == nil {
			m.commandLine.AddToast("No LLM session available", "error", 4000)
			return m, nil
		}

		// Set the shell runner based on RunOnHost flag
		if msg.RunOnHost {
			slog.Debug("using host shell runner for this conversation")
			m.court.AllowRunnerFallback(true)

			// Wrap the caller's func with code to restore the previous runner
			originalCallback := msg.onStreamComplete
			msg.onStreamComplete = func(model *TUIModel) tea.Cmd {
				model.court.AllowRunnerFallback(false)

				// Call the original callback if it exists
				if originalCallback != nil {
					return originalCallback(model)
				}
				return nil
			}
		}

		// Store the callback for later use
		m.streamCompleteCallback = msg.onStreamComplete

		// Try to upgrade from host to sandbox if requested (async)
		var upgradeCmd tea.Cmd
		if msg.tryUpgradeToSandbox {
			upgradeCmd = func() tea.Msg {
				upgraded := false // TODO: fix tryUpgradeToSandbox(m.config)
				return sandboxUpgradeMsg{upgraded: upgraded}
			}
		}

		// Add initialization message if this is an init command (has a prompt and callback)
		// If there's a prompt, send it to the AI
		if msg.prompt != "" {
			ctx := m.tabs.ActiveTab().Ctx
			m.sessionActive = true

			var streamCmd tea.Cmd
			// Route through Court
			streamCmd = m.submitToCourt(ctx, msg.prompt, nil)

			if waitCmd := m.startWaitingForResponse(); waitCmd != nil {
				return m, tea.Batch(waitCmd, streamCmd, upgradeCmd)
			} else if streamCmd != nil {
				return m, tea.Batch(streamCmd, upgradeCmd)
			}
		} else {
			// If RunOnHost is true and restoration callback is set, restore runner immediately
			if msg.RunOnHost && msg.onStreamComplete != nil {
				msg.onStreamComplete(&m)
			}
		}

		if upgradeCmd != nil {
			return m, upgradeCmd
		}
		return m, nil

	case sandboxUpgradeMsg:
		// Handle sandbox upgrade result
		if msg.upgraded {
			slog.Info("successfully upgraded from host to sandbox runner")
			m.commandLine.AddToast("🐳 Sandbox now available", "info", 3000)
			// Refresh the first message to show the updated sandbox status
			/* TODO: Remove
			if len(m.tabs.Content().Chat.Messages) > 0 {
				m.tabs.Content().Chat.Messages[0] = ChatMessage{Content: newSessionMessage(), Indent: 0}
				m.tabs.Content().Chat.UpdateContent()
			}
			*/
		}
		return m, nil

	case compactConversationMsg:
		// Handle conversation compaction
		slog.Debug("got compactConversationMsg")

		target := m.currentTabTarget()
		if _, ok := m.currentSessionState(); !ok {
			m.commandLine.AddToast("No LLM session available for compaction", "error", 4000)
			return m, nil
		}

		// Add a message to show we're compacting
		compactMsg := NewChatMsgBuilder("🗜️  Compacting conversation history...")
		compactMsg.WriteLn("This may take a moment as the model summarizes the chat.")
		m.tabs.Content().Chat.AddMessage(compactMsg.String())

		// Perform the compaction in a goroutine
		go func() {
			ctx := context.Background()
			summary, err := m.court.CompactSession(ctx, target, compactPrompt)
			if err != nil {
				if program != nil {
					program.Send(compactErrorMsg{err: err})
				}
				return
			}
			if program != nil {
				program.Send(compactCompleteMsg{summary: summary})
			}
		}()

	case compactCompleteMsg:
		// Compaction completed successfully
		slog.Debug("compaction completed")

		var info ContextInfo

		if state, ok := m.currentSessionState(); ok {
			si := state.ContextInfo
			info = ContextInfo{
				Model:              si.Model,
				TotalTokens:        si.TotalTokens,
				UsedTokens:         si.UsedTokens,
				SystemPromptTokens: si.SystemPromptTokens,
				SystemToolsTokens:  si.SystemToolsTokens,
				MemoryFilesTokens:  si.MemoryFilesTokens,
				MessagesTokens:     si.MessagesTokens,
				FreeTokens:         si.FreeTokens,
				AutocompactBuffer:  si.AutocompactBuffer,
			}
		}

		// Add success message
		m.tabs.Content().Chat.AddMessage(fmt.Sprintf("✅ Conversation compacted successfully!\n\nContext usage: %s/%s tokens (%.1f%%)",
			formatTokenCount(info.UsedTokens),
			formatTokenCount(info.TotalTokens),
			percentage(info.UsedTokens, info.TotalTokens)))

		m.commandLine.AddToast("Conversation history compacted", "success", 3000)

	case compactErrorMsg:
		// Compaction failed
		slog.Warn("compaction failed", "error", msg.err)
		m.tabs.Content().Chat.AddMessage(fmt.Sprintf("❌ Failed to compact conversation: %v\n\nYour conversation context was left unchanged.", msg.err))
		m.commandLine.AddToast("Compaction failed - context unchanged", "error", 3000)

	case runners.ContainerLaunchedMsg:
		// Container launch notification
		m.commandLine.AddToast(msg.Message, "info", 3*time.Second)
		// Update shell runner info in status bar
		m.status.ContainerID = msg.ContainerID
		return m, nil

	case shellCommandResultMsg:
		// Shell command execution completed
		m.tabs.Content().Chat.AddShellCommandResult(msg)
		m.repoInfo.RefreshDiff()
		m.prompt().Focus()
		return m, nil

	case connectionLostMsg:
		return m.handleConnectionLost()

	case connectionRestoredMsg:
		return m.handleConnectionRestored()

	case connectionReconnectFailedMsg:
		// Reconnect failed — show error and clear pending retries
		for tabTarget := range m.connDropPendingRetry {
			chat := m.tabs.ChatByTab(tabTarget)
			chat.AddMessage(fmt.Sprintf("%s❌ Connection lost — unable to reconnect. Please restart the daemon.", systemPrefix))
		}
		m.connDropPendingRetry = nil
		m.commandLine.AddToast("Unable to reconnect. Please restart the daemon.", "error", time.Second*10)
		m.status.SetError()
		return m, nil

	}

	// Restore focus to prompt if no modals are active and view is chat
	if !m.commandLine.IsInCommandMode() &&
		m.tabs.Content().GetActiveView() == ViewChat {
		m.prompt().Focus()
	}

	// Update content (which handles chat updates)
	contentCmd := m.tabs.UpdateContent(msg)
	return m, contentCmd
}

func (m *TUIModel) updateFileCompletions(files []string) {
	inputValue := m.prompt().Value()

	// Find the last @ character to determine what we're completing
	lastAt := strings.LastIndex(inputValue, "@")
	if lastAt == -1 {
		m.completions.SetOptions([]string{})
		return
	}

	// Extract the text after the last @ for completion
	searchQuery := inputValue[lastAt+1:]

	// If there's a space in the search query, we're likely starting a new file reference
	if spaceIndex := strings.Index(searchQuery, " "); spaceIndex != -1 {
		searchQuery = searchQuery[spaceIndex+1:]
	}

	var filteredFiles []string
	for _, file := range files {
		if strings.Contains(strings.ToLower(file), strings.ToLower(searchQuery)) {
			filteredFiles = append(filteredFiles, file)
		}
	}

	// Sort by the position of the search query
	sort.Slice(filteredFiles, func(i, j int) bool {
		s1 := filteredFiles[i]
		s2 := filteredFiles[j]
		lowerS1 := strings.ToLower(s1)
		lowerS2 := strings.ToLower(s2)
		lowerSearch := strings.ToLower(searchQuery)

		i1 := strings.Index(lowerS1, lowerSearch)
		i2 := strings.Index(lowerS2, lowerSearch)

		if i1 == i2 {
			return s1 < s2
		}

		return i1 < i2
	})

	var options []string
	for _, file := range filteredFiles {
		options = append(options, file)
	}
	m.completions.SetOptions(options)
}

// updateCommandCompletions filters commands based on current input
func (m *TUIModel) updateCommandCompletions() {
	inputValue := m.prompt().Value()

	// Determine if we're using vi mode colon commands or regular slash commands
	var prefix string
	var searchQuery string

	if strings.HasPrefix(inputValue, "/") {
		prefix = "/"
		searchQuery = strings.ToLower(inputValue[1:])
	} else if strings.HasPrefix(inputValue, ":") {
		prefix = ":"
		searchQuery = strings.ToLower(inputValue[1:])
	} else {
		// No command prefix found
		m.completions.SetOptions([]string{})
		return
	}

	// Get all command names and filter them
	var filteredCommands []string
	for _, name := range m.commandRegistry.order {
		// Commands are now stored without prefix
		// Check if the command starts with the search query
		if strings.HasPrefix(strings.ToLower(name), searchQuery) {
			// Format the command with the appropriate prefix for display
			if prefix == ":" {
				filteredCommands = append(filteredCommands, ":"+name)
			} else {
				filteredCommands = append(filteredCommands, "/"+name)
			}
		}
	}

	m.completions.SetOptions(filteredCommands)
}

// updateCommandLineCompletions filters commands based on command line input
func (m *TUIModel) updateCommandLineCompletions() {
	commandText := m.commandLine.GetCommand()
	searchQuery := strings.ToLower(commandText)

	// Get all command names and filter them
	var filteredCommands []string
	for _, name := range m.commandRegistry.order {
		// Commands are now stored without prefix
		// Check if the command starts with the search query
		if strings.HasPrefix(strings.ToLower(name), searchQuery) {
			// Format with : prefix for command line mode
			filteredCommands = append(filteredCommands, ":"+name)
		}
	}

	m.completions.SetOptions(filteredCommands)

	// Show completion dialog if we have matches
	if len(filteredCommands) > 0 {
		m.showCompletionDialog = true
		m.completionMode = "command"
		m.completions.Show()
	} else {
		m.showCompletionDialog = false
		m.completions.Hide()
		m.completionMode = ""
	}
}

// updateComponentDimensions updates the dimensions of all components based on the window size
func (m *TUIModel) updateComponentDimensions() {
	// Calculate dimensions for vi-like layout (bottom to top):
	// - Command line: 1 line at bottom
	// - Status line: 1 line above command line
	// - Prompt: auto-growing (max 50% screen height)
	// - Empty line: 1 line above prompt
	// - Chat/File viewer: remaining space

	commandLineHeight := 1
	statusHeight := 1
	width := m.width - 2

	// Set screen height for prompt to calculate max height (50%)
	m.prompt().SetScreenHeight(m.height)

	// Calculate desired prompt height based on content
	promptHeight := m.prompt().CalculateDesiredHeight()

	// Account for borders (2 lines for top and bottom border)
	promptWithBorder := promptHeight + 2

	// Calculate chat height (account for tab bar when multiple tabs)
	tabBarHeight := m.tabs.TabBarHeight()
	contentHeight := m.height - commandLineHeight - statusHeight - promptWithBorder - tabBarHeight
	if contentHeight < 0 {
		contentHeight = 0
	}

	// Update component widths
	m.status.SetWidth(width + 2)
	m.commandLine.SetWidth(width + 2)

	// Full width layout - content handles chat and other views
	m.tabs.SetSize(width, contentHeight)

	m.prompt().SetWidth(width)
	m.prompt().SetHeight(promptHeight)

	// Update status info
	/* TODO: move this to a proper place and drop the currentEdictKey
	if m.court != nil && m.currentEdictKey.ID != 0 {
		m.status.SetProvider(m.config.LLM.Provider, m.config.LLM.Model, true)
	} else { */
	m.status.SetProvider(m.config.LLM.Provider, m.config.LLM.Model, false)
	// }
	slog.Debug("Updated dimensions", "m.height", m.height, "prompt height", promptHeight, "content height", contentHeight)
}

// View implements bubbletea.Model
func (m TUIModel) View() string {
	start := time.Now()

	defer func() {
		duration := time.Since(start)
		if duration > 100*time.Millisecond {
			slog.Warn("[bubbletea] View() SLOW", "duration", duration)
		}
	}()

	if m.width == 0 || m.height == 0 {
		return "Initializing..."
	}

	// Update prompt dimensions based on content before rendering
	// This ensures the prompt grows to 10 lines when multiline (#31)
	// SetWidth ensures the active tab's prompt matches the terminal width
	// (prompts are per-tab, but only the active one is sized on WindowSizeMsg)
	m.prompt().SetScreenHeight(m.height)
	m.prompt().SetWidth(m.width - 2)
	promptHeight := m.prompt().CalculateDesiredHeight()
	m.prompt().SetHeight(promptHeight)
	promptWithBorder := promptHeight + 2

	// Recalculate content height based on new prompt height
	commandLineHeight := 1
	statusHeight := 1
	tabBarHeight := m.tabs.TabBarHeight()
	contentHeight := m.height - commandLineHeight - statusHeight - promptWithBorder - tabBarHeight
	if contentHeight < 0 {
		contentHeight = 0
	}
	m.tabs.SetSize(m.width-2, contentHeight)

	modalHeight := 0
	if m.modal != nil {
		modalHeight = lipgloss.Height(m.modal.Render())
	}
	mainContent := m.renderMainContent(modalHeight)
	promptView := m.prompt().View()
	commandLineView := m.commandLine.View()
	view := m.composeBaseView(mainContent, promptView, commandLineView)
	if m.showCompletionDialog {
		view = m.overlayCompletionDialog(view, promptView, commandLineView)
	}

	// Note: m.modal (BaseModal) is now rendered in composeBaseView above the prompt

	return view
}

func (m TUIModel) renderMainContent(modalHeight int) string {
	// Account for prompt, status, vi mode/toast line, and command line dynamically
	commandLineHeight := 1
	statusHeight := 1
	promptWithBorder := m.prompt().Height + 2
	tabBarHeight := m.tabs.TabBarHeight()
	contentHeight := m.height - commandLineHeight - statusHeight - promptWithBorder - tabBarHeight - modalHeight
	if contentHeight < 0 {
		contentHeight = 0
	}

	// First check if we're viewing help/models/resume - these take precedence
	if m.tabs.Content().GetActiveView() != ViewChat {
		return m.tabs.Content().View()
	}

	// Then check for special modes
	switch {
	case m.rawMode:
		return m.renderRawSessionView(m.width, contentHeight)
	case m.tabs.IsWelcome():
		return m.tabs.renderWelcome(m.width, contentHeight)
	default:
		// Use content component which handles chat view
		// TODO: why doesn't it accept height?
		return m.tabs.Content().View()
	}
}

func (m TUIModel) composeBaseView(mainContent, promptView, commandLineView string) string {
	tabBar := m.tabs.RenderTabBar(m.width)
	statusView := m.status.View()

	// Build layout parts
	var layoutParts []string
	if tabBar != "" {
		layoutParts = append(layoutParts, tabBar)
	}
	layoutParts = append(layoutParts, mainContent)

	if m.modal != nil {
		modalRender := m.modal.Render()
		layoutParts = append(layoutParts, modalRender, "")
	}

	if promptView != "" {
		layoutParts = append(layoutParts, promptView)
	}
	layoutParts = append(layoutParts, statusView, commandLineView)

	return lipgloss.JoinVertical(lipgloss.Left, layoutParts...)
}

func (m TUIModel) overlayCompletionDialog(baseView, promptView, commandLineView string) string {
	dialog := m.completions.View()
	if dialog == "" {
		return baseView
	}

	// 1 is for the status line
	// TODO: bring it down and cover part of the prompt. Need to wait for bubbletea 2.0
	bottomOffset := 2 + lipgloss.Height(promptView)

	dialogHeight := lipgloss.Height(dialog)
	yPos := m.height - bottomOffset - dialogHeight

	// TODO: fix the overlaying so it looks good
	dialogOverlay := lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Top, baseView)
	dialogPositioned := lipgloss.Place(m.width, dialogHeight, lipgloss.Left, lipgloss.Top, dialog)

	lines := strings.Split(dialogOverlay, "\n")
	dialogLines := strings.Split(dialogPositioned, "\n")

	if yPos >= 0 && yPos < len(lines) {
		for i, dialogLine := range dialogLines {
			if yPos+i < len(lines) {
				lines[yPos+i] = dialogLine
			}
		}
	}

	return strings.Join(lines, "\n")
}

// renderRawSessionView renders the raw session view showing complete unfiltered history
func (m TUIModel) renderRawSessionView(width, height int) string {
	rawHistory := m.tabs.Content().Chat.GetRawHistory()
	if len(rawHistory) == 0 {
		// Show empty state
		emptyStyle := lipgloss.NewStyle().
			Foreground(globalTheme.TextError).
			Align(lipgloss.Center).
			Width(width)

		emptyContent := emptyStyle.Render("Raw session history is empty\nPress Ctrl+O to return to chat")

		container := lipgloss.NewStyle().
			Width(width).
			Height(height).
			Align(lipgloss.Center, lipgloss.Center).
			Render(emptyContent)

		return container
	}

	// Create title
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(globalTheme.ChatBorder).
		Align(lipgloss.Center).
		Width(width)

	title := titleStyle.Render("Raw Session History (Press Ctrl+O to return to chat)")

	// Style for raw history entries
	entryStyle := lipgloss.NewStyle().
		Foreground(globalTheme.TextColor).
		PaddingLeft(1).
		Width(width - 2)

	// Render all history entries
	var historyViews []string
	for _, entry := range rawHistory {
		// Word wrap long entries to fit the width
		wrappedEntry := entry
		if len(entry) > width-4 {
			// Simple word wrap - in real implementation you might use wordwrap.String
			for len(wrappedEntry) > width-4 {
				breakPoint := width - 4
				// Try to break at a space
				for i := breakPoint; i > breakPoint-20 && i > 0; i-- {
					if wrappedEntry[i] == ' ' {
						breakPoint = i
						break
					}
				}
				historyViews = append(historyViews, entryStyle.Render(wrappedEntry[:breakPoint]))
				wrappedEntry = "    " + wrappedEntry[breakPoint:] // Indent continuation lines
			}
			if len(wrappedEntry) > 0 {
				historyViews = append(historyViews, entryStyle.Render(wrappedEntry))
			}
		} else {
			historyViews = append(historyViews, entryStyle.Render(wrappedEntry))
		}
		historyViews = append(historyViews, "") // Add spacing between entries
	}

	historyContent := lipgloss.JoinVertical(lipgloss.Left, historyViews...)

	// Combine title and content
	content := lipgloss.JoinVertical(lipgloss.Left, title, "", historyContent)

	// Create scrollable container
	container := lipgloss.NewStyle().
		Width(width).
		Height(height).
		Render(content)

	return container
}

// stopStreamingTab cancels streaming on a single tab by target ID.
// Local tab context is cancelled for TUI-side state; CancelTab is
// forwarded so the daemon's ritual/stream context (registered under
// the same channelID via CancellableStreamCtx) also tears down.
func (m *TUIModel) stopStreamingTab(tabTarget string) {
	m.tabs.CancelTabByID(tabTarget)
	if m.court != nil {
		m.court.CancelTab(tabTarget)
	}
	if !m.tabs.AnyStreaming() {
		m.stopWaitingForResponse()
	}
	// Clear pending retry for this tab — user cancelled
	if m.connDropPendingRetry != nil {
		delete(m.connDropPendingRetry, tabTarget)
	}
}

// clearStreamingTab resets UI streaming state for a tab without touching its
// context. Use this when the session has already stopped itself (interrupted,
// errored, max-tokens) — cancelling again only creates feedback loops.
func (m *TUIModel) clearStreamingTab(tabTarget string) {
	m.tabs.ClearStreamingByTab(tabTarget)
	if !m.tabs.AnyStreaming() {
		m.stopWaitingForResponse()
	}
}

// stopStreaming cancels all streaming globally (used for shutdown).
func (m *TUIModel) stopStreaming() {
	m.tabs.CancelAllTabs()
	m.stopWaitingForResponse()
	// Clear pending connection-drop retries — user cancelled
	m.connDropPendingRetry = nil
}

// handleConnectionLost is called when the RPC connection drops. It saves
// the prompt text and context files from every actively streaming tab
// for auto-retry after reconnect, resets streaming state, shows a toast,
// and starts watching for reconnect.
func (m TUIModel) handleConnectionLost() (tea.Model, tea.Cmd) {
	if m.connDropPendingRetry == nil {
		m.connDropPendingRetry = make(map[string]pendingRetry)
	}

	// Save the last prompt for each streaming tab. We pull from
	// sessionPromptHistory — the most recent entry is the in-flight one.
	for i := range m.tabs.tabs {
		tab := &m.tabs.tabs[i]
		if !tab.Streaming {
			continue
		}

		// Find the last prompt submitted for this tab. The sessionPromptHistory
		// is tab-agnostic (appended in handleEnterKey), so we use the last entry.
		if len(m.sessionPromptHistory) > 0 {
			last := m.sessionPromptHistory[len(m.sessionPromptHistory)-1]
			// Gather context files from the current session state
			var ctxFiles map[string]string
			if state, ok := m.currentSessionState(); ok {
				ctxFiles = state.ContextFiles
			}
			m.connDropPendingRetry[tab.Target] = pendingRetry{
				prompt:       last.Prompt,
				contextFiles: ctxFiles,
			}
			slog.Debug("connection lost: saved prompt for retry", "tab", tab.Target, "prompt_len", len(last.Prompt))
		}

		chat := m.tabs.ChatByTab(tab.Target)
		chat.AddMessage(fmt.Sprintf("%s⚠️  Connection lost", systemPrefix))
		chat.AddMessage(fmt.Sprintf("%s[Reconnecting… Your message will be retried.]", systemPrefix))
		chat.AddToRawHistory("CONN_LOST", "connection dropped during stream")
	}

	m.tabs.CancelAllTabs()
	m.stopWaitingForResponse()
	m.commandLine.AddToast("Connection lost — Reconnecting…", "warning", time.Second*5)

	// Start watching for reconnect
	return m, watchConnDrop(m.court)
}

// handleConnectionRestored is called after the RPC connection is re-established.
// It re-submits the prompt for each tab that was streaming when the connection
// dropped.
func (m TUIModel) handleConnectionRestored() (tea.Model, tea.Cmd) {
	if len(m.connDropPendingRetry) == 0 {
		// No pending retries — nothing to do
		return m, nil
	}

	var cmds []tea.Cmd
	for tabTarget, retry := range m.connDropPendingRetry {
		chat := m.tabs.ChatByTab(tabTarget)
		chat.AddMessage(fmt.Sprintf("%s✓ Reconnected — retrying your message…", systemPrefix))

		ctx := context.Background()
		if tab := m.tabs.TabByTarget(tabTarget); tab != nil {
			ctx = tab.Ctx
		}
		cmd := m.submitToCourt(ctx, retry.prompt, retry.contextFiles)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	// Clear pending retries
	m.connDropPendingRetry = nil

	// Restart waiting indicator
	if waitCmd := m.startWaitingForResponse(); waitCmd != nil {
		cmds = append(cmds, waitCmd)
	}

	if len(cmds) > 0 {
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

// handleAnsweringComplete closes the zhengming waiter and updates the DB.
// Runs in a goroutine from the Update loop.
func (m *TUIModel) handleAnsweringComplete(msg AnsweredMsg) {
	if m.court == nil {
		return
	}
	answer := strings.Join(msg.Answers, "; ")
	slog.Debug("handleAnsweringComplete: calling HandleZhengmingResponse",
		"request_id", msg.RequestID,
		"answer", answer,
		"court_type", fmt.Sprintf("%T", m.court))
	if err := m.court.HandleZhengmingResponse(context.Background(), msg.RequestID, answer); err != nil {
		slog.Error("failed to handle zhengming response", "error", err)
	}
}

// parseEdictActionRequestID checks if a RequestID is a synthetic edict action
// menu ID (format: "edict-<id>") and returns the edict ID.
func parseEdictActionRequestID(requestID string) (uint, bool) {
	if !strings.HasPrefix(requestID, "edict-") {
		return 0, false
	}
	parsed, err := strconv.ParseUint(strings.TrimPrefix(requestID, "edict-"), 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(parsed), true
}

// dispatchEdictAction dispatches to the appropriate edict subcommand based on
// the selected menu option.
func dispatchEdictAction(m *TUIModel, edictID uint, answers []string) tea.Cmd {
	if len(answers) == 0 {
		return nil
	}
	action := answers[0]
	switch action {
	case "Status":
		return loadEdictDashboardCmd(m, edictID)
	case "Implement":
		// Pre-create the ritual tab immediately so the user sees instant
		// feedback rather than waiting for the daemon-side ritual manager
		// to send the first RitualStepMsg back. The ritual channel ID
		// follows the "e<N>" convention (see ritualChannelID).
		channelID := fmt.Sprintf("e%d", edictID)
		m.addTab(ritualTabLabel(channelID), "ritual", channelID, func(tab *Tab) tea.Cmd {
			chat := m.tabs.ChatByTab(channelID)
			chat.AddMessage(fmt.Sprintf("%sPreparing ritual for edict %d…", ritualPrefix, edictID))
			return nil
		})
		return tea.Batch(enactRitualForEdict(m, edictID, "swift-strike"), reloadEdictsListCmd(m))
	case "Chat":
		// Pre-create the edict tab and set restore flags so the session is
		// restored on the edict tab ("e<id>") instead of the minister's
		// original interactive tab.
		channelID := fmt.Sprintf("e%d", edictID)
		m.addTab(ritualTabLabel(channelID), "ritual", channelID, func(tab *Tab) tea.Cmd {
			chat := m.tabs.ChatByTab(channelID)
			chat.AddMessage(fmt.Sprintf("%sRestoring session for edict %d…", systemPrefix, edictID))
			return nil
		})
		m.creatingTab = channelID
		m.pendingPrompt = ""
		edict, err := m.court.GetEdict(edictID)
		if err != nil {
			return func() tea.Msg {
				return showToast(fmt.Sprintf("Edict not found: %d", edictID), "error", 3*time.Second)
			}
		}
		if edict.SessionID == "" {
			return func() tea.Msg {
				return showToast(fmt.Sprintf("No session linked to edict %d", edictID), "warning", 3*time.Second)
			}
		}
		if loadFn := m.tabs.Content().loadSessionFn; loadFn != nil {
			return loadFn(edict.SessionID)
		}
		return nil
	case "Seal":
		return handleEdictSeal(m, edictID, "")
	case "Cancel":
		return handleEdictCancel(m, edictID)
	case "Edit":
		return editEdictIntentCmd(m, edictID)
	case "Back":
		return reloadEdictsListCmd(m)
	default:
		return nil
	}
}

// editEdictIntentCmd opens the edict intent in $EDITOR and calls SetIntent
// with the modified text.
func editEdictIntentCmd(m *TUIModel, edictID uint) tea.Cmd {
	edict, err := m.court.GetEdict(edictID)
	if err != nil {
		return func() tea.Msg {
			return showSystemMsg(fmt.Sprintf("Edict not found: %d", edictID))
		}
	}
	originalText := edict.Intent

	tmpFile, err := os.CreateTemp("", "edict_edit_*.md")
	if err != nil {
		return func() tea.Msg {
			return showSystemMsg(fmt.Sprintf("Failed to create temp file: %v", err))
		}
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	if err := os.WriteFile(tmpPath, []byte(originalText), 0644); err != nil {
		os.Remove(tmpPath)
		return func() tea.Msg {
			return showSystemMsg(fmt.Sprintf("Failed to write temp file: %v", err))
		}
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.Command(editor, tmpPath)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(tmpPath)
		if err != nil {
			return showSystemMsg(fmt.Sprintf("Editor failed: %v", err))
		}

		content, err := os.ReadFile(tmpPath)
		if err != nil {
			return showSystemMsg(fmt.Sprintf("Failed to read edited file: %v", err))
		}

		modified := strings.TrimRight(string(content), " \t\n\r")
		original := strings.TrimRight(originalText, " \t\n\r")
		if modified == original {
			return edictIntentUpdatedMsg{edictID: edictID, message: "No changes made to edict intent"}
		}

		if err := m.court.SetIntent(edictID, modified); err != nil {
			return edictIntentUpdatedMsg{edictID: edictID, message: fmt.Sprintf("Failed to update edict: %v", err)}
		}
		return edictIntentUpdatedMsg{edictID: edictID, message: fmt.Sprintf("Edict %d intent updated", edictID)}
	})
}

func (m *TUIModel) raiseCourtEvent(event storage.CourtEvent, params storage.JSON) {
	if m.court == nil {
		slog.Warn("Failed to raise event as court is nil", "event", event)
		return
	}
	var key storage.EdictKey
	switch event {
	case storage.EventCourtStarted, storage.EventCourtReady:
		key = m.court.CourtEdictKey()
	default:
		// Derive edict key from payload instead of stale currentEdictKey
		if edictID, ok := params["edict_id"].(uint); ok {
			key = m.court.EdictKey(edictID)
		} else {
			key = m.currentEdictKey // fallback for backward compatibility
		}
	}
	m.court.PublishEvent(key, event, params)
}
