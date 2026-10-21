package main

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/ministers"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/internal/worktree"
	"github.com/afittestide/asimi/storage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Verify that court.Session implements ExportableSession
var _ ExportableSession = (*court.Session)(nil)

//go:embed prompts/compact.txt
var compactPrompt string

// Command represents a slash command
type Command struct {
	Name        string
	Description string
	Handler     func(*TUIModel, []string) tea.Cmd
}

// compactConversationMsg is sent when the compact command is executed
type compactConversationMsg struct{}

// CommandRegistry holds all available commands
type CommandRegistry struct {
	Commands map[string]Command
	order    []string
}

func normalizeCommandName(name string) string {
	if name == "" {
		return ""
	}
	// Strip both : and / prefixes to store commands without prefix
	name = strings.TrimPrefix(name, ":")
	return name
}

func showSystemMsg(msg string) tea.Msg {
	return showContextMsg{content: systemPrefix + msg}
}

// NewCommandRegistry creates a new command registry
func NewCommandRegistry() CommandRegistry {
	registry := CommandRegistry{
		Commands: make(map[string]Command),
	}

	// Register built-in commands (stored without prefix)
	registry.RegisterCommand("help", "Show help (usage: :help [topic])", handleHelpCommand)
	registry.RegisterCommand("new", "Start a new session", handleNewSessionCommand)
	registry.RegisterCommand("quit", "Close current tab, or quit if last tab", handleQuitCommand)
	registry.RegisterCommand("q", "Close current tab, or quit if last tab", handleQuitCommand)
	registry.RegisterCommand("qa", "Quit the application (all tabs)", handleQuitAllCommand)
	registry.RegisterCommand("quitall", "Quit the application (all tabs)", handleQuitAllCommand)
	registry.RegisterCommand("models", "Select AI model (use :models refresh for a live refetch)", handleModelsCommand)
	registry.RegisterCommand("model", "Select AI model (alias; :model refresh for a live refetch)", handleModelsCommand)
	registry.RegisterCommand("context", "Show context usage details", handleContextCommand)
	registry.RegisterCommand("resume", "Resume a previous session", handleResumeCommand)
	registry.RegisterCommand("export", "Export conversation to file and open in $EDITOR (usage: :export [full|conversation])", handleExportCommand)
	registry.RegisterCommand("init", "Init project to work with asimi (usage: /init [clear])", handleInitCommand)
	registry.RegisterCommand("compact", "Compact conversation history to reduce context usage", handleCompactCommand)
	registry.RegisterCommand("1", "Jump to the beginning of the chat history", handleScrollTopCommand)
	registry.RegisterCommand("update", "Check for and install updates", handleUpdateCommand)
	registry.RegisterCommand("logout", "Logout from current provider and clear credentials", handleLogoutCommand)
	registry.RegisterCommand("login", "Authenticate with an AI provider", handleLoginCommand)
	registry.RegisterCommand("tabnew", "Open a new tab (usage: :tabnew [<minister>|ritual <run_id>])", handleTabNewCommand)
	registry.RegisterCommand("tabclose", "Close the current tab", handleTabCloseCommand)
	registry.RegisterCommand("edicts", "Manage edicts: read, enact, seal, resume, or cancel (usage: :edict [id] [enact|seal|resume|cancel])", handleEdictCommand)
	registry.RegisterCommand("continue", "Resume a paused ritual on the current tab", handleContinueCommand)
	registry.RegisterCommand("abort", "Abort a paused ritual on the current tab", handleAbortCommand)
	registry.RegisterCommand("switch", "Switch to a branch or edict worktree (usage: :switch <main|e123|branch>)", handleSwitchCommand)

	return registry
}

// RegisterCommand registers a new command
func (cr *CommandRegistry) RegisterCommand(name, description string, handler func(*TUIModel, []string) tea.Cmd) {
	normalized := normalizeCommandName(name)
	if normalized == "" {
		return
	}
	if _, exists := cr.Commands[normalized]; !exists {
		cr.order = append(cr.order, normalized)
	}
	cr.Commands[normalized] = Command{
		Name:        normalized,
		Description: description,
		Handler:     handler,
	}
}

// GetCommand gets a command by name
func (cr CommandRegistry) GetCommand(name string) (Command, bool) {
	normalized := normalizeCommandName(name)
	cmd, exists := cr.Commands[normalized]
	return cmd, exists
}

// FindCommand finds commands by prefix (like vim).
// Returns:
// - exactMatch: the matched command if exactly one match is found
// - matches: all commands that start with the prefix
// - found: true if exactly one match was found
func (cr CommandRegistry) FindCommand(prefix string) (exactMatch Command, matches []string, found bool) {
	normalized := normalizeCommandName(prefix)
	if normalized == "" {
		return Command{}, nil, false
	}

	// First try exact match
	if cmd, exists := cr.Commands[normalized]; exists {
		return cmd, []string{normalized}, true
	}

	// Try prefix matching
	var matchedCommands []string

	for _, cmdName := range cr.order {
		if strings.HasPrefix(cmdName, normalized) {
			matchedCommands = append(matchedCommands, cmdName)
		}
	}

	if len(matchedCommands) == 1 {
		cmd := cr.Commands[matchedCommands[0]]
		return cmd, matchedCommands, true
	}

	return Command{}, matchedCommands, false
}

// GetAllCommands returns all registered commands
func (cr CommandRegistry) GetAllCommands() []Command {
	var commands []Command
	for _, name := range cr.order {
		if cmd, ok := cr.Commands[name]; ok {
			commands = append(commands, cmd)
		}
	}
	return commands
}

// Command handlers

type showHelpMsg struct {
	topic string
}
type showContextMsg struct{ content string }

// toastMsg displays an ephemeral toast notification.
type toastMsg struct {
	message string
	msgType string // info, success, warning, error
	timeout time.Duration
}

// showToast returns a toastMsg for ephemeral user notifications.
func showToast(msg, msgType string, timeout time.Duration) toastMsg {
	return toastMsg{message: msg, msgType: msgType, timeout: timeout}
}

func handleHelpCommand(model *TUIModel, args []string) tea.Cmd {
	// Determine the help topic from args
	topic := "index" // Default topic
	if len(args) > 0 {
		topic = args[0]
	}

	return func() tea.Msg {
		return showHelpMsg{topic: topic}
	}
}

func handleNewSessionCommand(model *TUIModel, args []string) tea.Cmd {
	model.saveSession()

	model.sessionActive = true

	// Reset the appropriate minister session based on active tab
	if model.court != nil {
		tab := model.tabs.ActiveTab()
		if model.court.HasMinister(string(tab.Type)) {
			model.court.ResetMinisterSession(string(tab.Type), tab.Target)
		} else {
			slog.Debug("Failed to get session", "miniter", tab.Type)
		}
	}

	// Use the generic startConversationMsg to reset the session
	// The tryUpgradeToSandbox flag tells the handler to attempt upgrading
	// from host to sandbox runner asynchronously
	return func() tea.Msg {
		return startConversationMsg{
			clearHistory:        true,
			tryUpgradeToSandbox: true,
		}
	}
}

func handleQuitCommand(model *TUIModel, args []string) tea.Cmd {
	// If multiple tabs exist, close the current tab (like Vim's :quit)
	if model.tabs.TabCount() > 1 {
		abortRitualOnClose(model)
		err := model.tabs.Close()
		if err != nil {
			model.commandLine.AddToast(err.Error(), "error", time.Second*3)
			return nil
		}
		model.commandLine.AddToast("Tab closed", "success", time.Second*2)
		return nil
	}
	// Single tab: quit the application
	model.shutdown()
	return tea.Quit
}

func handleQuitAllCommand(model *TUIModel, args []string) tea.Cmd {
	// Always quit the application, regardless of tab count
	model.shutdown()
	return tea.Quit
}

func handleContextCommand(model *TUIModel, args []string) tea.Cmd {
	return func() tea.Msg {
		state, ok := model.currentSessionState()
		if !ok {
			return showSystemMsg("No active session. Use :models to configure a model and start chatting.")
		}
		ci := state.ContextInfo
		info := ContextInfo{
			Model:              ci.Model,
			TotalTokens:        ci.TotalTokens,
			TotalTokensApprox:  ci.TotalTokensApprox,
			UsedTokens:         ci.UsedTokens,
			SystemPromptTokens: ci.SystemPromptTokens,
			SystemToolsTokens:  ci.SystemToolsTokens,
			MemoryFilesTokens:  ci.MemoryFilesTokens,
			MessagesTokens:     ci.MessagesTokens,
			FreeTokens:         ci.FreeTokens,
			AutocompactBuffer:  ci.AutocompactBuffer,
		}
		return showContextMsg{content: renderContextInfo(info)}
	}
}

func handleResumeCommand(model *TUIModel, args []string) tea.Cmd {
	// Immediately show the resume view with loading state
	showResumeCmd := model.tabs.Content().ShowResume([]court.Session{})
	model.tabs.Content().resume.SetLoading(true)

	// Load sessions in the background
	loadCmd := func() tea.Msg {
		if model == nil || model.config == nil {
			return sessionResumeErrorMsg{err: fmt.Errorf("resume unavailable: missing configuration")}
		}

		if !model.config.Session.Enabled {
			return showSystemMsg("Session resume is disabled in configuration.")
		}

		repoInfo := repo.GetRepoInfo()
		currentBranch := repoInfo.BranchSlugOrDefault()
		if model.sessionStore == nil ||
			model.sessionStore.ProjectRoot != repoInfo.ProjectRoot ||
			model.sessionStore.Branch != currentBranch {

			maxSessions := 50
			if model.config.Session.MaxSessions > 0 {
				maxSessions = model.config.Session.MaxSessions
			}

			maxAgeDays := 30
			if model.config.Session.MaxAgeDays > 0 {
				maxAgeDays = model.config.Session.MaxAgeDays
			}

			store, err := NewSessionStore(model.db, repoInfo, maxSessions, maxAgeDays)
			if err != nil {
				return sessionResumeErrorMsg{err: fmt.Errorf("failed to initialize session store: %w", err)}
			}

			if model.sessionStore != nil {
				model.sessionStore.Close()
			}
			model.sessionStore = store
		}

		if model.sessionStore == nil {
			return sessionResumeErrorMsg{err: fmt.Errorf("session store not initialized")}
		}

		listLimit := 0
		if model.config.Session.ListLimit >= 0 {
			listLimit = model.config.Session.ListLimit
		}

		currentTabType := string(model.tabs.ActiveTab().Type)

		sessions, err := model.sessionStore.ListSessions(listLimit, currentTabType)
		if err != nil {
			return sessionResumeErrorMsg{err: fmt.Errorf("failed to list sessions: %w", err)}
		}

		return sessionsLoadedMsg{sessions: sessions}
	}

	// Return both commands - show view immediately, then load data
	return tea.Batch(showResumeCmd, loadCmd)
}

func handleExportCommand(model *TUIModel, args []string) tea.Cmd {
	var session ExportableSession

	if model.court != nil {
		if exp, err := model.court.GetSessionExport(model.currentTabTarget()); err == nil && exp != nil {
			session = exp
			slog.Debug("using Court session for export", "edict_id", model.currentEdictKey.ID)
		}
	}

	if session == nil {
		return func() tea.Msg {
			return showSystemMsg("No active session to export. Start a conversation first.")
		}
	}

	// Determine export type from args, default to conversation
	exportType := ExportTypeConversation
	if len(args) > 0 {
		switch args[0] {
		case "full":
			exportType = ExportTypeFull
		case "conversation":
			exportType = ExportTypeConversation
		default:
			model.commandLine.AddToast(fmt.Sprintf("Unknown export type '%s'. Use 'full' or 'conversation'", args[0]), "error", 3000)
			return nil
		}
	}

	// Export the session to a file
	filepath, err := exportSession(session, exportType)
	if err != nil {
		return func() tea.Msg {
			return showSystemMsg(fmt.Sprintf("Export failed: %v", err))
		}
	}

	// Open the file in the editor using ExecProcess
	cmd := openInEditor(filepath)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return showSystemMsg(fmt.Sprintf("Editor exited with error: %v", err))
		}
		model.commandLine.AddToast(fmt.Sprintf("Conversation exported successfully (%s).", exportType), "success", 3000)
		return nil
	})
}

func handleInitCommand(model *TUIModel, args []string) tea.Cmd {
	// Clear mode runs even without an active session so users can recover from
	// a broken init without first having a working LLM connection.
	if len(args) > 0 && args[0] == "clear" {
		errors := clearAsimiFiles(model)
		if len(errors) > 0 {
			return func() tea.Msg {
				return showContextMsg{content: fmt.Sprintf("Failed to clear files: %s", strings.Join(errors, ", "))}
			}
		}
	}

	if model.court == nil {
		return func() tea.Msg {
			return showContextMsg{content: "No model connection available. Please ensure a session is active."}
		}
	}

	// Check if project name is already set in config
	if model.config.Court.Project == "" {
		// Auto-derive slug with the shared resolver (tracked-remote → origin →
		// basename) rather than blindly trusting the origin remote (edict 968).
		ri := model.status.repoInfo
		slug := repo.ResolveProjectSlug("", ri.ProjectRoot, ri.HasVCS)
		if slug == "" {
			return func() tea.Msg {
				return showContextMsg{content: "No git remote found. Please set up a git remote origin before running :init."}
			}
		}
		return saveProjectNameAndInit(model, slug)
	}

	// Project name exists, proceed with creating the edict
	return createInitEdict(model)
}

// createInitEdict creates the edict for project-init ritual
func createInitEdict(model *TUIModel) tea.Cmd {
	// Use CreateEdictSilent: we already know the ritual (project-init) and will
	// dispatch it directly below. Publishing EventEdictCreated would make the
	// chancellor LLM also try to enact a ritual for this edict, starting it twice.
	edict, err := model.court.CreateEdictSilent("", "Initialize project with Asimi agent configuration", "")
	if err != nil {
		return func() tea.Msg {
			return showContextMsg{content: fmt.Sprintf("Failed to create edict: %v", err)}
		}
	}
	payload := storage.JSON{
		"ritual_name": "project-init",
		"edict_id":    edict.ID,
	}
	model.raiseCourtEvent(storage.EventRitualEnacted, payload)
	return nil
}

// clearAsimiFiles removes infrastructure files to allow a fresh init
func clearAsimiFiles(model *TUIModel) []string {
	projectRoot := model.status.repoInfo.ProjectRoot
	files := []string{
		filepath.Join(projectRoot, ".agents"),
		filepath.Join(projectRoot, "AGENTS.md"),
		filepath.Join(projectRoot, "Justfile"),
	}

	var errors []string
	for _, file := range files {
		if err := os.RemoveAll(file); err != nil && !os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("%s: %v", file, err))
		}
	}
	return errors
}

// handleAPIKeyInput saves an API key to the keyring and config, then refreshes models.
func handleAPIKeyInput(model *TUIModel, provider, apiKey string) tea.Cmd {
	if apiKey == "" {
		// User cancelled (Esc)
		return func() tea.Msg { return showModelSelectionMsg{} }
	}

	// Save to keyring and update config
	if err := UpdateUserLLMAuth(provider, apiKey, ""); err != nil {
		slog.Warn("Failed to save API key", "provider", provider, "error", err)
		return func() tea.Msg {
			return showContextMsg{content: fmt.Sprintf("%sFailed to save API key for %s: %v", systemPrefix, providerDisplayName(provider), err)}
		}
	}

	return func() tea.Msg { return apiKeySavedMsg{provider: provider} }
}

// saveProjectNameAndInit seeds the project config with the given project name,
// reloads the config, and creates the init edict.
func saveProjectNameAndInit(model *TUIModel, projectName string) tea.Cmd {
	// Seed .agents/asimi.conf from the embedded default template before writing
	// the project name. Without this, SetProjectConfig would create a stub file
	// containing only the [court] section, and the ritual's template-seeding
	// step would later skip the file (since it exists), leaving the user with a
	// near-empty config missing every default section.
	projectRoot := model.status.repoInfo.ProjectRoot
	if err := config.EnsureProjectConfig(projectRoot); err != nil {
		return func() tea.Msg {
			return showContextMsg{content: fmt.Sprintf("Failed to seed project config: %v", err)}
		}
	}

	// Save project name to .agents/asimi.conf
	if err := config.SetProjectConfig(projectRoot, "court", "project", projectName); err != nil {
		return func() tea.Msg {
			return showContextMsg{content: fmt.Sprintf("Failed to save project name: %v", err)}
		}
	}

	// Reload project config to pick up the new project name
	if reloaded, err := config.LoadProjectConfig(model.status.repoInfo.ProjectRoot, true); err != nil {
		slog.Warn("Failed to reload config after setting project name", "error", err)
	} else {
		model.config = reloaded
	}

	// Proceed with creating the init edict
	return createInitEdict(model)
}

// startConversationMsg is sent to start a new conversation with optional guardrails
type startConversationMsg struct {
	prompt              string
	clearHistory        bool
	initialMessages     []string                // Messages to display after clearing history (before streaming starts)
	onStreamComplete    func(*TUIModel) tea.Cmd // Optional guardrail function to run after stream completes
	RunOnHost           bool                    // When true, use host shell runner instead of podman
	tryUpgradeToSandbox bool                    // When true, attempt to upgrade from host to sandbox runner
}

// sandboxUpgradeMsg is sent after attempting to upgrade from host to sandbox runner
type sandboxUpgradeMsg struct {
	upgraded bool
}

// verifyInit runs validation checks after init completes
// It accepts a containerRunner parameter to run tests in the container
func verifyInit(model *TUIModel, containerRunner runners.Runner) tea.Cmd {
	return verifyInitWithRetry(model, containerRunner, 0)
}

// verifyInitWithRetry is the internal implementation with retry tracking
func verifyInitWithRetry(model *TUIModel, containerRunner runners.Runner, retryCount int) tea.Cmd {
	const maxRetries = 5 // Maximum number of retry attempts

	return func() tea.Msg {
		slog.Debug("verifyInitWithRetry called", "retryCount", retryCount, "containerRunner", containerRunner)

		// Reload configuration on retry attempts to pick up any changes made by the LLM
		// (e.g., modifications to .agents/asimi.conf, Dockerfile, etc.)
		slog.Debug("Reloading configuration for retry attempt", "retryCount", retryCount)
		if model.status.repoInfo != nil {
			if reloaded, err := config.LoadProjectConfig(model.status.repoInfo.ProjectRoot, true); err != nil {
				slog.Warn("Failed to reload config during verifyInit retry", "error", err)
			} else {
				model.config = reloaded
				slog.Debug("Configuration reloaded successfully")
			}
		}

		var results []string

		report := func(message string) {
			results = append(results, message)
			if program != nil {
				program.Send(showContextMsg{content: treeMidPrefix + message})
			}
		}

		// Send initial message
		if program != nil {
			msg := "\n" + systemPrefix + "Testing infrastructure"
			if retryCount > 0 {
				msg += fmt.Sprintf(" (attempt %d/%d)", retryCount+1, maxRetries+1)
			}
			program.Send(showContextMsg{content: msg})
		}

		slog.Debug("Starting verification checks", "retryCount", retryCount)

		// Determine agents file from config
		agentsFile := "AGENTS.md"
		if model.config != nil && model.config.Session.AgentsFile != "" {
			agentsFile = model.config.Session.AgentsFile
		}

		// Check required files exist - collect all failures before returning
		slog.Debug("Checking required files")
		agentsMdExists := checkFileExists(agentsFile, agentsFile+" created", report)
		justfileExists := checkFileExists("Justfile", "Justfile created", report)

		if !agentsMdExists || !justfileExists {
			slog.Debug("Required files missing, handling failure")
			return handleVerificationFailure(model, containerRunner, retryCount, maxRetries, results)
		}

		// Without repoInfo we cannot run build-sandbox, reinitialize the runner,
		// or stage files — return early with what we have.
		if model.status.repoInfo == nil {
			slog.Debug("repoInfo is nil, skipping repo-dependent verification steps")
			return handleVerificationFailure(model, containerRunner, retryCount, maxRetries, results)
		}

		// Run build-sandbox
		slog.Debug("Running build-sandbox")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if !runBuildSandbox(ctx, model.status.repoInfo.ProjectRoot, report, &results) {
			slog.Debug("build-sandbox failed, handling failure")
			return handleVerificationFailure(model, containerRunner, retryCount, maxRetries, results)
		}

		// After build-sandbox succeeds, reinitialize the shell runner to get a fresh container
		// This is necessary because the previous container was closed and the image was rebuilt
		slog.Debug("Reinitializing shell runner after build-sandbox")
		// TODO: should the repoInfo be part of status?
		containerRunner = runners.InitShellRunner(&model.config.Sandbox, *model.status.repoInfo, strconv.FormatUint(uint64(os.Getpid()), 10))
		slog.Debug("Shell runner reinitialized", "containerRunner", containerRunner)

		// Run smoke test in container
		slog.Debug("Running smoke test in container", "containerRunner", containerRunner)
		if !runSmokeTest(ctx, containerRunner, report) {
			slog.Debug("Smoke test failed, handling failure")
			return handleVerificationFailure(model, containerRunner, retryCount, maxRetries, results)
		}

		// Run tests on host
		slog.Debug("Running tests on host")
		if !runHostTests(ctx, model.status.repoInfo.ProjectRoot, report, &results) {
			slog.Debug("Host tests failed, handling failure")
			return handleVerificationFailure(model, containerRunner, retryCount, maxRetries, results)
		}

		// Run tests in container
		slog.Debug("Running tests in container")
		if !runContainerTests(ctx, containerRunner, report, &results) {
			slog.Debug("Container tests failed, handling failure")
			return handleVerificationFailure(model, containerRunner, retryCount, maxRetries, results)
		}

		// All tests passed - stage the files
		slog.Debug("All verification tests passed! Staging files...")

		// Stage all added/changed files in .agents/ and root infrastructure files
		filesToStage := []string{
			agentsFile,
			"Justfile",
			".agents/",
		}

		ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel2()

		for _, file := range filesToStage {
			result, err := runners.HostRun(ctx2, runners.Input{
				Command:     fmt.Sprintf("git add %s", file),
				Description: fmt.Sprintf("Staging %s", file),
			}, model.status.repoInfo.ProjectRoot)

			if err != nil || result.ExitCode != "0" {
				slog.Warn("Failed to stage file", "file", file, "error", err, "exitCode", result.ExitCode)
				report(fmt.Sprintf("⚠️  Failed to stage %s", file))
			} else {
				slog.Debug("Staged file successfully", "file", file)
			}
		}

		if program != nil {
			msg := NewChatMsgBuilder(systemPrefix)
			msg.WriteString(checkPrefix).WriteLn(" Verified!")
			msg.WriteLn(strings.Join(filesToStage, ", ") + " staged")
			msg.WriteLn("Start fresh with `:new` and review project's recipes with `:!just -l`")

			program.Send(showContextMsg{content: msg.String()})
		}
		return nil
	}
}

// checkFileExists checks if a file exists and reports the result
func checkFileExists(filename, successMsg string, report func(string)) bool {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		report(fmt.Sprintf("❌ %s was not created", filename))
		return false
	}
	report(checkPrefix + " " + successMsg)
	return true
}

// runBuildSandbox runs the build-sandbox command on the host
func runBuildSandbox(ctx context.Context, projectRoot string, report func(string), results *[]string) bool {
	report("$ just build-sandbox # on host")
	result, err := runners.HostRun(ctx, runners.Input{
		Command:     "just build-sandbox",
		Description: "Building infrastructure files",
	}, projectRoot)

	if err != nil || result.ExitCode != "0" {
		report(fmt.Sprintf("❌ just build-sandbox failed (exit code: %s)", result.ExitCode))
		if result.Output != "" {
			*results = append(*results, fmt.Sprintf("   Output: %s", strings.TrimSpace(result.Output)))
		}
		return false
	}

	report(checkPrefix + " just build-sandbox completed successfully")
	return true
}

// runSmokeTest runs a basic smoke test in the container
func runSmokeTest(ctx context.Context, containerRunner runners.Runner, report func(string)) bool {
	slog.Debug("runSmokeTest called", "containerRunner", containerRunner)
	if containerRunner == nil {
		slog.Error("containerRunner is nil in runSmokeTest")
		report("❌ Container runner is not available")
		return false
	}

	slog.Debug("Calling containerRunner.Run for smoke test")
	result, err := containerRunner.Run(ctx, runners.Input{
		Command:     "uname",
		Description: "Running smoke test in container",
	})

	slog.Debug("Smoke test result", "output", result.Output, "exitCode", result.ExitCode, "error", err)
	isLinux := strings.Contains(result.Output, "Linux")
	if err != nil || result.ExitCode != "0" || !isLinux {
		report(fmt.Sprintf("❌ Sandbox smoke test failed (exit code: %s)", result.ExitCode))
		return false
	}

	report(checkPrefix + " Sandbox smoke test passed")
	return true
}

// runHostTests runs the test suite on the host
func runHostTests(ctx context.Context, projectRoot string, report func(string), results *[]string) bool {
	report("$ just test # on host")
	result, err := runners.HostRun(ctx, runners.Input{
		Command:     "just test",
		Description: "Running tests on host",
	}, projectRoot)

	if err != nil || result.ExitCode != "0" {
		report(fmt.Sprintf("❌ just test on host failed (exit code: %s)", result.ExitCode))
		if result.Output != "" {
			*results = append(*results, fmt.Sprintf("   Output: %s", strings.TrimSpace(result.Output)))
		}
		return false
	}

	report(checkPrefix + " just test on host passed")
	return true
}

// runContainerTests runs the test suite in the container
func runContainerTests(ctx context.Context, containerRunner runners.Runner, report func(string), results *[]string) bool {
	report("$ just test # in container")
	result, err := containerRunner.Run(ctx, runners.Input{
		Command:     "just test",
		Description: "Running tests in container",
	})

	if err != nil || result.ExitCode != "0" {
		report(fmt.Sprintf("❌ just test in container failed (exit code: %s)", result.ExitCode))
		if result.Output != "" {
			*results = append(*results, fmt.Sprintf("   Output: %s", strings.TrimSpace(result.Output)))
		}
		return false
	}

	report(checkPrefix + " just test in container passed")
	return true
}

// handleVerificationFailure handles the case when verification fails
func handleVerificationFailure(model *TUIModel, containerRunner runners.Runner, retryCount, maxRetries int, results []string) tea.Msg {
	slog.Debug("In verifyInit - handleVerificationFailure", "hasErrors", true, "messages", results, "retryCount", retryCount)

	// Check if we've exceeded the maximum retry count
	if retryCount >= maxRetries {
		slog.Debug("Max retries exceeded, giving up", "retryCount", retryCount, "maxRetries", maxRetries)
		msg := NewChatMsgBuilder(systemPrefix)
		msg.WriteLnf("❌ Initialization failed after %d attempts.", maxRetries+1)
		msg.WriteLn("The following issues could not be resolved:")
		for _, result := range results {
			msg.WriteLn(result)
		}
		msg.WriteLn("For help check out the humans in Asimi's github discussions")
		return showContextMsg{content: msg.String()}
	}

	// Stop and remove the container so the next attempt will rebuild with fixes
	slog.Debug("Attempting to close container before retry", "containerRunner", containerRunner, "retryCount", retryCount)
	if containerRunner != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		slog.Debug("Calling containerRunner.Close()")
		if err := containerRunner.Close(ctx); err != nil {
			slog.Warn("Failed to close container during verifyInit", "error", err)
		} else {
			slog.Debug("Container closed successfully")
		}
	} else {
		slog.Debug("containerRunner is nil, skipping close")
	}

	// Build message for LLM to fix the issues
	var message strings.Builder
	message.WriteString("Issues found verifying initialization.\n" +
		"Please review the failures below and provide a fix.\n" +
		"If files need to be modified, use the appropriate tools.\n")
	for _, result := range results {
		message.WriteString(result + "\n")
	}

	// Return a startConversationMsg to send this message to the LLM session
	// TODO: refactor this as this is not really start of conversation but a hack
	return startConversationMsg{
		prompt:       message.String(),
		clearHistory: false,
		RunOnHost:    true,
		onStreamComplete: func(model *TUIModel) tea.Cmd {
			return verifyInitWithRetry(model, containerRunner, retryCount+1)
		},
	}
}

func handleCompactCommand(model *TUIModel, args []string) tea.Cmd {
	state, ok := model.currentSessionState()
	messageCount := 0
	if ok {
		messageCount = state.MessageCount
	}

	if messageCount == 0 {
		return func() tea.Msg {
			return showSystemMsg("No active session to compact. Start a conversation first.")
		}
	}

	return func() tea.Msg {
		// Check if there's enough conversation to compact
		if messageCount <= 2 {
			return showSystemMsg("Not enough conversation history to compact. Continue chatting first.")
		}

		// Send the compact request (message shown in tui.go handler)
		return compactConversationMsg{}
	}
}

func handleScrollTopCommand(model *TUIModel, args []string) tea.Cmd {
	if model == nil || model.tabs.Content().GetActiveView() != ViewChat {
		return nil
	}
	model.tabs.Content().Chat.ScrollToTop()
	return nil
}

type updateCheckMsg struct {
	hasUpdate bool
	latest    string
	err       error
}

type updateCompleteMsg struct {
	success bool
	err     error
}

func handleUpdateCommand(model *TUIModel, args []string) tea.Cmd {
	return func() tea.Msg {
		// Show checking message
		if program != nil {
			program.Send(showSystemMsg("Checking for updates..."))
		}

		// Check for updates
		latest, hasUpdate, err := utils.CheckForUpdates(context.Background())
		if err != nil {
			return updateCheckMsg{hasUpdate: false, err: err}
		}

		if !hasUpdate {
			return updateCheckMsg{hasUpdate: false, latest: utils.AsimiVersion}
		}

		// Update available - show confirmation
		return updateCheckMsg{hasUpdate: true, latest: latest.Version}
	}
}

func handleUpdateConfirm(model *TUIModel) tea.Cmd {
	return func() tea.Msg {
		// Show updating message
		if program != nil {
			msg := NewChatMsgBuilder(systemPrefix)
			msg.WriteLn("Downloading and installing update...")
			msg.WriteLn("This may take a moment.")
			program.Send(showContextMsg{content: msg.String()})
		}

		// Perform update
		err := SelfUpdate(utils.AsimiVersion)
		if err != nil {
			return updateCompleteMsg{success: false, err: err}
		}

		return updateCompleteMsg{success: true}
	}
}

func handleTabNewCommand(model *TUIModel, args []string) tea.Cmd {
	defs, _ := ministers.LoadMinisters()
	defsByID := ministers.LookupMap(defs)

	if len(args) == 0 {
		// Default: inherit the active tab's minister type.
		// If on a plain minister tab (no edict context), open a new tab
		// of the same minister. If on an edict or ritual tab, fall back
		// to Secretary so the ruler can draft a new edict.
		active := model.tabs.ActiveTab()
		ministerID := ministers.Secretary
		if _, ok := defsByID[string(active.Type)]; ok && active.EdictID == 0 {
			ministerID = string(active.Type)
		}
		addMinisterTab(model, defsByID, ministerID)
		return nil
	}

	target := args[0]
	switch target {
	case "ritual":
		if len(args) < 2 {
			model.commandLine.AddToast("Usage: :tabnew ritual <run_id>", "error", time.Second*3)
			return nil
		}
		runID := args[1]
		model.addTab("Ritual:"+runID, "ritual", runID, nil)
		model.commandLine.AddToast(fmt.Sprintf("Opened Ritual tab: %s", runID), "success", time.Second*2)
	default:
		// Treat as minister name — accept any minister known from defs
		// or registered on the court.
		if _, ok := defsByID[target]; ok || (model.court != nil && model.court.HasMinister(target)) {
			addMinisterTab(model, defsByID, target)
		} else {
			model.commandLine.AddToast(fmt.Sprintf("Unknown minister: %s", target), "error", time.Second*3)
		}
	}
	return nil
}

// addMinisterTab creates a new tab for the given minister ID, deriving the
// label from defs (falling back to a capitalized ID), and shows a toast.
func addMinisterTab(model *TUIModel, defsByID map[string]ministers.MinisterDef, ministerID string) {
	d := defsByID[ministerID]
	label := d.Label()
	if label == "" {
		label = strings.ToUpper(ministerID[:1]) + ministerID[1:]
	}
	target := model.tabs.UniqueTarget(ministerID)
	model.addTab(label, TabType(ministerID), target, nil)
	model.commandLine.AddToast(fmt.Sprintf("Opened %s tab", label), "success", time.Second*2)
}

func handleTabCloseCommand(model *TUIModel, args []string) tea.Cmd {
	abortRitualOnClose(model)
	err := model.tabs.Close()
	if err != nil {
		model.commandLine.AddToast(err.Error(), "error", time.Second*3)
		return nil
	}
	model.commandLine.AddToast("Tab closed", "success", time.Second*2)
	return nil
}

// abortRitualOnClose tears down any ritual bound to the active tab before the
// tab is removed. A paused ritual keeps its goroutine alive (CTRL-C only
// pauses), so merely deleting the tab orphans it and leaves the court's
// single-flight latch held — every subsequent ritual would queue forever.
// Mirroring :abort, we resume the paused channel to unblock the goroutine, then
// cancel its context to abort the ritual. Streaming tabs are left alone: Close
// refuses those anyway.
func abortRitualOnClose(model *TUIModel) {
	tab := model.tabs.ActiveTab()
	if tab == nil || tab.Type != "ritual" || !isRitualChannel(tab.Target) {
		return
	}
	if tab.Streaming {
		// Close will refuse; leave the running ritual untouched.
		return
	}
	if model.court == nil {
		return
	}
	if tab.ChatMode {
		model.court.ResumeRitual(tab.Target)
		model.tabs.SetTabChatMode(tab.Target, false)
	}
	// CancelTab aborts the ritual goroutine; Close cancels the tab context.
	model.court.CancelTab(tab.Target)
}

// handleContinueCommand resumes a paused ritual or enacts a fresh swift-strike
// for an active (non-sealed, non-cancelled) edict.
func handleContinueCommand(model *TUIModel, args []string) tea.Cmd {
	tab := model.tabs.ActiveTab()
	if tab.Type != "ritual" || !isRitualChannel(tab.Target) {
		model.commandLine.AddToast(":continue only works on a ritual tab", "warning", 3*time.Second)
		return nil
	}
	if model.court == nil {
		model.commandLine.AddToast("Court not active", "error", 3*time.Second)
		return nil
	}

	// If paused, resume the ritual
	if tab.ChatMode {
		if model.court.ResumeRitual(tab.Target) {
			model.tabs.SetTabChatMode(tab.Target, false)
			chat := model.tabs.ChatByTab(tab.Target)
			chat.AddMessage(fmt.Sprintf("%s▶ Ritual resuming...", systemPrefix))
		} else {
			model.commandLine.AddToast("No paused ritual found on this tab", "warning", 3*time.Second)
		}
		return nil
	}

	// Not paused — extract edict ID and enact a fresh ritual
	edictID := uint64(tab.EdictID)
	edict, err := model.court.GetEdict(uint(edictID))
	if err != nil {
		model.commandLine.AddToast(fmt.Sprintf("Edict %d not found", edictID), "error", 3*time.Second)
		return nil
	}
	if edict.CancelledAt != nil {
		model.commandLine.AddToast(fmt.Sprintf("Edict %d is cancelled", edictID), "warning", 3*time.Second)
		return nil
	}

	// Check for Ruler seal (edict is sealed)
	key := model.court.EdictKey(uint(edictID))
	seals, err := model.court.GetEdictSeals(key)
	if err != nil {
		slog.Warn("failed to check edict seals", "edict_id", edictID, "error", err)
	}
	for _, s := range seals {
		if s.MinisterID == ministers.Ruler {
			model.commandLine.AddToast(fmt.Sprintf("Edict %d is already sealed", edictID), "warning", 3*time.Second)
			return nil
		}
	}

	return enactRitualForEdict(model, uint(edictID), "swift-strike")
}

// handleAbortCommand aborts a paused ritual on the current tab.
func handleAbortCommand(model *TUIModel, args []string) tea.Cmd {
	tab := model.tabs.ActiveTab()
	if tab.Type != "ritual" || !isRitualChannel(tab.Target) {
		model.commandLine.AddToast(":abort only works on a ritual tab", "warning", 3*time.Second)
		return nil
	}
	if model.court == nil {
		model.commandLine.AddToast("Court not active", "error", 3*time.Second)
		return nil
	}
	// Resume the ritual goroutine (unblocks it) then cancel the tab context
	// to abort the ritual entirely — same as CTRL-C.
	if tab.ChatMode {
		model.court.ResumeRitual(tab.Target)
		model.tabs.SetTabChatMode(tab.Target, false)
	}
	model.stopStreamingTab(tab.Target)
	chat := model.tabs.ChatByTab(tab.Target)
	chat.AddMessage(fmt.Sprintf("%s✋ Ritual aborted by ruler", systemPrefix))
	return nil
}

// resolveSwitchTarget maps a `:switch` target to its branch and worktree.
// It is a variable so tests can drive the handler without invoking git.
var resolveSwitchTarget = func(ctx context.Context, root, target string) (worktree.ResolvedTarget, error) {
	return worktree.Resolve(ctx, runners.NewHostRunner(0, root), root, target)
}

// handleSwitchCommand moves the whole Court to a branch or edict worktree in
// place, without exiting Asimi. The branch is global — every minister shares
// the Court's repo context — so after re-targeting it the tab set is reset to
// the default minister tabs, discarding sessions bound to the old branch.
func handleSwitchCommand(model *TUIModel, args []string) tea.Cmd {
	if len(args) != 1 {
		model.commandLine.AddToast("Usage: :switch <main|e123|branch>", "error", 3*time.Second)
		return nil
	}
	// Refuse mid-stream: switching would tear the ground out from under an
	// in-flight response.
	if model.tabs.AnyStreaming() {
		model.commandLine.AddToast("Cannot switch while a tab is streaming", "error", 3*time.Second)
		return nil
	}
	if model.repoInfo == nil {
		model.commandLine.AddToast("No repository information available", "error", 3*time.Second)
		return nil
	}

	target := args[0]
	projectRoot := model.repoInfo.ProjectRoot
	resolved, err := resolveSwitchTarget(context.Background(), projectRoot, target)
	if err != nil {
		model.commandLine.AddToast(fmt.Sprintf("Cannot switch to %s: %v", target, err), "error", 3*time.Second)
		return nil
	}

	// Create the edict worktree when it is missing (git worktree add -B,
	// `just install`, ignored-file copy).
	if resolved.EdictID > 0 && model.court != nil {
		if _, err := os.Stat(filepath.Join(projectRoot, resolved.WorktreeRel)); err != nil {
			if _, err := model.court.EnsureEdictWorktree(context.Background(), resolved.EdictID); err != nil {
				model.commandLine.AddToast(fmt.Sprintf("Failed to create worktree for edict %d: %v", resolved.EdictID, err), "error", 3*time.Second)
				return nil
			}
		}
	}

	// Mutate a copy so the status bar and setContextParams see the new target.
	newInfo := *model.repoInfo
	newInfo.Branch = resolved.Branch
	newInfo.WorktreePath = resolved.WorktreeRel
	newInfo.IsWorktree = resolved.IsWorktree
	newInfo.IsMain = repo.IsMainBranch(resolved.Branch)
	model.repoInfo = &newInfo
	model.status.SetRepoInfo(model.repoInfo)

	// Clear all tabs: sessions built on the old branch no longer apply.
	if !model.tabs.ResetToDefaults() {
		model.commandLine.AddToast("Cannot switch while a tab is streaming", "error", 3*time.Second)
		return nil
	}

	model.commandLine.AddToast(fmt.Sprintf("Switched to %s", resolved.Branch), "success", 2*time.Second)

	// Re-target the Court (daemon path over the wire, single-process inline)
	// and refresh the diff stats for the new root.
	params := model.setContextParams()
	court := model.court
	return func() tea.Msg {
		if court != nil {
			if err := court.SetContext(context.Background(), params); err != nil {
				return toastMsg{message: fmt.Sprintf("Switch failed: %v", err), msgType: "error", timeout: 3 * time.Second}
			}
		}
		return repoSwitchedMsg{}
	}
}

// repoSwitchedMsg triggers a diff refresh after a successful `:switch`.
type repoSwitchedMsg struct{}

// handleEdictCommand manages edicts: read, enact, seal, resume, or cancel.
func handleEdictCommand(model *TUIModel, args []string) tea.Cmd {
	if model.court == nil {
		return func() tea.Msg {
			return showSystemMsg("Court not active - cannot manage edicts")
		}
	}

	// No args: show selection of active edicts
	if len(args) == 0 {
		return func() tea.Msg {
			edicts, err := model.court.ListActiveEdicts()
			if err != nil {
				return showSystemMsg(fmt.Sprintf("Failed to list active edicts: %v", err))
			}
			return edictsLoadedMsg{edicts: edicts}
		}
	}

	// Parse edict ID
	parsed, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil {
		return func() tea.Msg {
			return showSystemMsg(fmt.Sprintf("Invalid edict ID '%s': must be a number", args[0]))
		}
	}
	edictID := uint(parsed)

	// Determine subcommand
	subcommand := ""
	if len(args) > 1 {
		subcommand = args[1]
	}

	switch subcommand {
	case "":
		// :edict <id> — show action menu (Status, Implement, Edit, Seal, Cancel)
		return showEdictActionMenu(model, edictID)

	case "seal":
		// :edict <id> seal — grant Ruler's seal
		notes := ""
		if len(args) > 2 {
			notes = strings.Join(args[2:], " ")
		}
		return handleEdictSeal(model, edictID, notes)

	case "enact":
		// :edict <id> enact — enact a ritual (default: swift-strike)
		return enactRitualForEdict(model, edictID, "swift-strike")

	case "resume":
		// :edict <id> resume — resume linked session
		edict, err := model.court.GetEdict(edictID)
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
		if loadFn := model.tabs.Content().loadSessionFn; loadFn != nil {
			return loadFn(edict.SessionID)
		}
		return nil

	case "cancel":
		// :edict <id> cancel — cancel the edict
		return handleEdictCancel(model, edictID)

	default:
		return func() tea.Msg {
			return showSystemMsg(fmt.Sprintf("Unknown subcommand '%s'. Usage: :edict [id] [enact|seal|resume|cancel]", subcommand))
		}
	}
}

// reloadEdictsListCmd fetches active edicts and returns edictsLoadedMsg,
// causing the edicts list to refresh. Used to return to the list after an action.
func reloadEdictsListCmd(model *TUIModel) tea.Cmd {
	return func() tea.Msg {
		edicts, err := model.court.ListActiveEdicts()
		if err != nil {
			return showSystemMsg(fmt.Sprintf("Failed to list active edicts: %v", err))
		}
		return edictsLoadedMsg{edicts: edicts}
	}
}

// loadEdictDashboardCmd loads edict detail and shows the dashboard view
func loadEdictDashboardCmd(model *TUIModel, edictID uint) tea.Cmd {
	return func() tea.Msg {
		edict, err := model.court.GetEdict(edictID)
		if err != nil {
			return showSystemMsg(fmt.Sprintf("Edict not found: %d", edictID))
		}

		key := model.court.EdictKey(edictID)

		// Get seals
		seals, _ := model.court.GetEdictSeals(key)

		// Build dashboard content
		content := renderEdictDashboard(edict, seals, 80)
		return showEdictDashboardMsg{content: content}
	}
}

// showEdictActionMenu enters answering mode with edict action options.
func showEdictActionMenu(model *TUIModel, edictID uint) tea.Cmd {
	// Validate edict exists first
	_, err := model.court.GetEdict(edictID)
	if err != nil {
		return func() tea.Msg {
			return showSystemMsg(fmt.Sprintf("Edict not found: %d", edictID))
		}
	}

	state := &AnsweringState{
		RequestID: fmt.Sprintf("edict-%d", edictID),
		Title:     fmt.Sprintf("Edict %d", edictID),
		Questions: []AnsweringQuestion{
			{
				Text:    "Choose an action",
				Summary: "Choose an action",
				Options: []string{"Status", "Implement", "Chat", "Seal", "Cancel", "Edit", "Back"},
			},
		},
		Answers: []string{""},
	}
	model.prompt().EnterAnsweringMode(state)
	return nil
}

// handleEdictSeal grants the Ruler's seal to an edict (same logic as old handleSealCommand with ID)
func handleEdictSeal(model *TUIModel, edictID uint, notes string) tea.Cmd {
	key := model.court.EdictKey(edictID)

	// Validate edict exists
	if _, err := model.court.GetEdict(edictID); err != nil {
		return func() tea.Msg {
			return showSystemMsg(fmt.Sprintf("Edict not found %v", key))
		}
	}

	// Get current seals
	seals, err := model.court.GetEdictSeals(key)
	if err != nil {
		return func() tea.Msg {
			return showSystemMsg(fmt.Sprintf("Failed to get seals for %d: %v", edictID, err))
		}
	}

	sealChainMsg := renderSealChain(seals, 60)

	hasJudge := false
	hasChancellor := false
	hasRuler := false
	for _, seal := range seals {
		switch seal.MinisterID {
		case ministers.Judge:
			hasJudge = true
		case ministers.Chancellor:
			hasChancellor = true
		case ministers.Ruler:
			hasRuler = true
		}
	}

	if hasRuler {
		return tea.Batch(
			func() tea.Msg {
				return showSystemMsg(fmt.Sprintf("Ruler's seal already granted to %d\n%s", edictID, sealChainMsg))
			},
			reloadEdictsListCmd(model),
		)
	}

	var missingSeals []string
	if !hasJudge {
		missingSeals = append(missingSeals, "Judge")
	}
	if !hasChancellor {
		missingSeals = append(missingSeals, "Chancellor")
	}

	if len(missingSeals) > 0 {
		question := fmt.Sprintf("Edict is missing a seal by [%s], seal?", strings.Join(missingSeals, ", "))
		model.pendingSealOverride = &pendingSealOverride{
			edictID: edictID,
			notes:   notes,
		}
		return model.enterYesNoOrAuto(question, true)
	}

	return grantRulerSealCmd(model, edictID, notes)
}

// enactRitualForEdict publishes EventRitualEnacted for the given edict and ritual
func enactRitualForEdict(model *TUIModel, edictID uint, ritualName string) tea.Cmd {
	return func() tea.Msg {
		key := model.court.EdictKey(edictID)
		payload := storage.JSON{
			"ritual_name": ritualName,
			"edict_id":    edictID,
			"inputs": map[string]interface{}{
				"edict_id": fmt.Sprintf("%d", edictID),
			},
		}
		model.court.PublishEvent(key, storage.EventRitualEnacted, payload)
		// Stay silent — the ritual manager handles all user notifications
		return nil
	}
}

// handleEdictCancel enters YesNo mode to confirm edict cancellation
func handleEdictCancel(model *TUIModel, edictID uint) tea.Cmd {
	// Validate edict exists and is not already cancelled
	edict, err := model.court.GetEdict(edictID)
	if err != nil {
		return func() tea.Msg {
			return showSystemMsg(fmt.Sprintf("Edict not found: %d", edictID))
		}
	}
	if edict.CancelledAt != nil {
		return tea.Batch(
			func() tea.Msg {
				return showSystemMsg(fmt.Sprintf("Edict %d is already cancelled", edictID))
			},
			reloadEdictsListCmd(model),
		)
	}
	model.pendingEdictCancel = &pendingEdictCancel{edictID: edictID}
	return model.enterYesNoOrAuto(fmt.Sprintf("Cancel edict %d?", edictID), false)
}

// cancelEdictCmd cancels an edict, shows a confirmation, and returns
// showContextMsg so the caller can batch it with a list reload.
func cancelEdictCmd(model *TUIModel, edictID uint) tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			if err := model.court.CancelEdict(edictID); err != nil {
				return showSystemMsg(fmt.Sprintf("Failed to cancel edict %d: %v", edictID, err))
			}
			return showContextMsg{content: systemPrefix + fmt.Sprintf("Edict %d cancelled", edictID)}
		},
		reloadEdictsListCmd(model),
	)
}

// renderEdictDashboard builds the text content for the edict dashboard view
func renderEdictDashboard(edict *storage.Edict, seals []storage.Seal, width int) string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(globalTheme.PromptBorder)
	labelStyle := lipgloss.NewStyle().Foreground(globalTheme.DimTextColor)

	b.WriteString(headerStyle.Render(fmt.Sprintf("Edict %d", edict.ID)))
	b.WriteString("\n\n")

	// Status
	status := "active"
	if edict.CancelledAt != nil {
		status = "cancelled"
	} else {
		for _, s := range seals {
			if s.MinisterID == ministers.Ruler {
				status = "sealed"
				break
			}
		}
	}
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Status:"), status))
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Created:"), edict.CreatedAt.Format("2006-01-02 15:04")))
	if edict.Summary != "" {
		b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Summary:"), edict.Summary))
	}
	if edict.SessionID != "" {
		b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Session:"), edict.SessionID))
	}
	b.WriteString("\n")

	// Seal chain
	b.WriteString(headerStyle.Render("Seal Chain"))
	b.WriteString("\n")
	b.WriteString(renderSealChain(seals, width))
	b.WriteString("\n\n")

	// Intent
	b.WriteString(headerStyle.Render("Intent"))
	b.WriteString("\n")
	intent := edict.Intent
	if intent == "" {
		intent = "(no intent recorded)"
	}
	b.WriteString(intent)
	b.WriteString("\n")

	return b.String()
}

// grantRulerSealCmd creates a command that grants the Ruler's seal to an edict,
// then returns reloadEdictsListCmd so the list refreshes after the seal is committed.
func grantRulerSealCmd(model *TUIModel, edictID uint, notes string) tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			if err := model.court.GrantRulerSeal(edictID, notes); err != nil {
				return showSystemMsg(fmt.Sprintf("Failed to grant Ruler's seal: %v", err))
			}
			return nil
		},
		reloadEdictsListCmd(model),
	)
}
