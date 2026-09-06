package court

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/afittestide/asimi/court/skills"
	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// Event is a dispatched event carrying type, edict key, and payload.
type Event struct {
	Type     storage.CourtEvent
	EdictKey storage.EdictKey
	Payload  map[string]interface{}
}

// EventHandler handles dispatched events.
type EventHandler func(event Event)

// EventRegistry manages event subscriptions and dispatch.
type EventRegistry struct {
	mu          sync.RWMutex
	subscribers map[storage.CourtEvent][]EventHandler
}

// NewEventRegistry creates a new event registry.
func NewEventRegistry() *EventRegistry {
	return &EventRegistry{
		subscribers: make(map[storage.CourtEvent][]EventHandler),
	}
}

// Subscribe registers a handler for an event type.
func (r *EventRegistry) Subscribe(eventType storage.CourtEvent, handler EventHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscribers[eventType] = append(r.subscribers[eventType], handler)
}

// Dispatch sends an event to all registered handlers asynchronously.
func (r *EventRegistry) Dispatch(event Event) {
	r.mu.RLock()
	handlers := r.subscribers[event.Type]
	r.mu.RUnlock()

	for _, handler := range handlers {
		go handler(event)
	}
}

// DrainedEvent describes a single event recovered from the DB at startup.
type DrainedEvent struct {
	EventType storage.CourtEvent     `msgpack:"event_type"`
	EdictKey  storage.EdictKey       `msgpack:"edict_key"`
	Payload   map[string]interface{} `msgpack:"payload,omitempty"`
}

// EventsDrainedMsg notifies the TUI that crash-recovery drained events from the DB.
type EventsDrainedMsg struct {
	ChannelID string         `msgpack:"channel_id,omitempty"`
	Events    []DrainedEvent `msgpack:"events"`
}

// Court coordinates ministers and manages lifecycle.
type Court struct {
	db     *gorm.DB
	logger *slog.Logger
	config *config.CourtConfig
	runner runners.Runner

	ministers   map[string]Minister
	ritualGuard *RitualGuard

	// toolRegistry holds the central tool registry with permission classifications.
	// Ministers use it via ForPermissions() to get their tool sets.
	toolRegistry *tools.ToolRegistry

	// toolCtxRepoInfo is the shared *repo.RepoInfo backing all ToolContext
	// instances. Updated by SetRepoInfo so tools see live project root.
	toolCtxRepoInfo *repo.RepoInfo

	notify        internal.NotifyFunc
	drainedEvents []DrainedEvent // events recovered from DB at startup

	// persister, if set, is attached to interactive sessions when they
	// are created so messages flow into durable storage in near-real time.
	persister SessionPersister

	// hostChecker determines whether a command should run on the host
	// (from config run_on_host/safe_run_on_host patterns). Owned by the
	// Court and passed into ToolRegistrationOpts.
	hostChecker func(string) (bool, bool)

	// zhengming holds the Court-owned pending zhengming dispatch state,
	// replacing the per-MinisterBase map that was previously accessed
	// through the chancellor.
	zhengming *zhengmingDispatch

	// msgChan is the approval channel for ephemeral HostRunner instances
	// used by the shell tool. Set by SetRunnerMessageChannel (via Subscribe).
	// All holders (tools, ministers) access this via pointer so they see
	// the updated value without explicit propagation.
	msgChan chan<- runners.Msg

	ctx    context.Context
	cancel context.CancelFunc

	// Per-channel cancel registry; CancelTab(channelID) invokes the
	// corresponding cancel func. Populated by CancellableStreamCtx.
	tabCancelsMu sync.Mutex
	tabCancels   map[string]context.CancelFunc

	// sessionCfg holds the session-level config (sandbox, LLM, agents file)
	// set via ConfigureModel. Used by CheckHostCommand for RunOnHost patterns.
	sessionCfg *SessionConfig

	// isolatedHost, when true, runs all shell commands on the host
	// without podman sandbox or approval gates. Set from CourtConfig.
	isolatedHost bool

	// llmClient holds the current LLM provider (typically *bifrost.Bifrost)
	// so the court can serve ListAllModels requests from the TUI.
	llmClient LLMProvider
}

// NewCourt creates a new Court coordinator.
func NewCourt(db *gorm.DB, cfg *config.CourtConfig, runner runners.Runner, logger *slog.Logger) *Court {
	s := &Court{
		db:           db,
		logger:       logger,
		config:       cfg,
		runner:       runner,
		ministers:    make(map[string]Minister),
		zhengming:    newZhengmingDispatch(),
		isolatedHost: cfg != nil && cfg.IsolatedHost,
	}
	s.ensureDefaults()

	// Reserve edict 1 for Court Infrastructure operations
	s.ensureCourtInfrastructureEdict()

	// Create all ministers — each needs its own base (channels/maps are reference types).
	// publish uses a closure so it works even before ritualGuard is assigned.
	newBase := func() *MinisterBase {
		base := NewMinisterBase(db, runner, logger, s.config.Username, s.config.Project, &s.msgChan)
		base.publish = func(key storage.EdictKey, eventType storage.CourtEvent, payload storage.JSON) uint {
			return s.PublishEvent(key, eventType, payload)
		}
		return base
	}

	// Load minister definitions from YAML (builtin + user + project overrides)
	defs, err := LoadAllMinisters("")
	if err != nil {
		s.logger.Warn("failed to load minister definitions, using builtin defaults", "error", err)
		defs, _ = LoadMinisters()
	}

	// Construct all ministers from the YAML defs — each gets its own base.
	for _, def := range defs {
		m := NewMinister(def, newBase())
		s.ministers[m.ID()] = m
	}

	// Wire up the ritual guard — it owns all ritual/event infrastructure
	s.ritualGuard = NewRitualGuard(RitualGuardOpts{
		Base:            newBase(),
		Runner:          runner,
		GetMinister:     s.GetMinister,
		OnRunnerUpgrade: s.SetRunner,
		// Each ritual startup gets a fresh cancellable ctx registered
		// under the ritual's edict channel (e.g. "e123"). Edict 1
		// (court infrastructure) uses "e1" and routes to its own
		// per-edict tab. A subsequent ritual on the same channel
		// replaces it; an explicit CancelTab from the TUI stops the
		// current one.
		StreamingCtx: func(channelID string) context.Context {
			return s.CancellableStreamCtx(channelID)
		},
		WaitForZhengming: s.WaitForZhengming,
		RequestZhengming: s.RequestZhengming,
		DeliverZhengming: s.DeliverZhengmingAnswer,
	})
	s.ritualGuard.ritualRunner.isolatedHost = s.isolatedHost

	// Handle zhengming_answered events: merged handler for ritual delivery, edict creation, and legacy path
	s.ritualGuard.Subscribe(storage.EventZhengmingAnswered, func(e Event) {
		requestID, _ := e.Payload["request_id"].(string)
		answer, _ := e.Payload["answer"].(string)
		key := e.EdictKey

		// 1. Try to deliver to a waiting caller via the Court's dispatch
		if requestID != "" {
			zhAnswer := ZhengmingAnswer{RequestID: requestID, Answer: answer, EdictID: key.ID}
			if s.DeliverZhengmingAnswer(zhAnswer) {
				s.logger.Info("zhengming answer delivered via court dispatch",
					"request_id", requestID, "edict_id", key.ID)
				return
			}
		}

		// 2. Handle "Approve edict" for suggestion-based edict creation or refinement
		if answer == tools.AnswerApproveEdict {
			var req storage.Zhengming
			if err := s.db.First(&req, "request_id = ?", requestID).Error; err == nil {
				if len(req.Questions) > 0 {
					suggestion := req.Questions[0].Text
					summary := req.Questions[0].Summary
					// Remove "Evidence:" part if present
					if idx := strings.Index(suggestion, "\n\nEvidence:"); idx != -1 {
						suggestion = suggestion[:idx]
					}
					if req.EdictID > 0 {
						// Refine existing edict: append suggestion to intent
						if err := s.appendToIntent(storage.EdictKey{
							ID:       req.EdictID,
							Username: req.Username,
							Project:  req.Project,
						}, suggestion); err != nil {
							s.logger.Warn("failed to append suggestion to edict intent", "edict_id", req.EdictID, "error", err)
						} else {
							s.logger.Info("appended suggestion to edict intent", "edict_id", req.EdictID, "request_id", requestID)
						}
					} else {
						// Create new edict from the suggestion
						edict, err := s.CreateEdict("", suggestion, req.SessionID)
						if err != nil {
							s.logger.Warn("failed to create edict from zhengming approval", "error", err)
						} else if summary != "" {
							if saveErr := s.db.Model(edict).Update("summary", summary).Error; saveErr != nil {
								s.logger.Warn("failed to save edict summary", "edict_id", edict.ID, "error", saveErr)
							}
						}
					}
				}
			}
			return
		}

		// 2b. Handle rejection — user dismissed the suggestion
		if answer == tools.AnswerReject || answer == tools.AnswerChat {
			s.logger.Info("zhengming suggestion rejected", "request_id", requestID)
			return
		}

		// 3. Handle system ritual path (e.g., wakeup) — no edict, user chose a path forward
		if key.ID == 0 && answer != "" {
			var req storage.Zhengming
			sessionID := ""
			if err := s.db.First(&req, "request_id = ?", requestID).Error; err == nil {
				sessionID = req.SessionID
			}
			if edict, err := s.CreateEdict("", answer, sessionID); err != nil {
				s.logger.Warn("failed to create edict from zhengming answer", "error", err)
			} else {
				s.logger.Info("created edict from zhengming answer", "edict_id", edict.ID, "answer", answer)
			}
			return
		}

		// 4. Forward to chancellor as fallback for legacy path
		s.logger.Info("Default forwarding zhengming answer to secretary")
		work := fmt.Sprintf("Resume edict %d with clarification: %s", key.ID, answer)
		if ch := s.GetMinister("secretary"); ch != nil {
			if rs, ok := ch.(interface {
				ResumeEdict(context.Context, storage.EdictKey, string)
			}); ok {
				go rs.ResumeEdict(s.ctx, key, work)
			}
		}
	})

	// Build and populate the tool registry with permission classifications.
	s.toolRegistry = s.buildToolRegistry()

	return s
}

// buildToolRegistry creates a ToolRegistry, registers all builtin tools with
// permission classifications, and returns it. Called once during NewCourt
// after all ministers are constructed.
func (s *Court) buildToolRegistry() *tools.ToolRegistry {
	registry := tools.NewToolRegistry()

	// Determine DBPath for asimisql
	dbPath := storage.DBPath(s.db)

	// Shared ToolContext — RepoInfo is a pointer so all tools see live state
	// (project root is set later via SetRepoInfo).
	repoInfo := &repo.RepoInfo{}
	ctx := tools.ToolContext{
		RepoInfo: repoInfo,
		Username: s.config.Username,
		Project:  s.config.Project,
		DB:       s.db,
	}

	// The Court owns the runtime-dispatch interfaces directly — no type
	// assertion from the chancellor's MinisterBase. The Court has access
	// to the notification function, minister map, ritual runner, and the
	// zhengming dispatch state.
	hostChecker := s.CheckHostCommand
	s.hostChecker = hostChecker

	// RitualLauncher is only meaningful when a ritual runner exists.
	var ritualLauncher tools.RitualLauncher
	if s.GetRitualRunner() != nil {
		ritualLauncher = s
	}

	// NotifyFn — lazy getter for the current notify, used by suggest_edict
	notifyFn := func() func(any) { return s.notify }

	// Collect registered minister IDs so tool descriptions can list them dynamically.
	ministerIDs := make([]string, 0, len(s.ministers))
	for id := range s.ministers {
		ministerIDs = append(ministerIDs, id)
	}

	opts := tools.ToolRegistrationOpts{
		Ctx:                ctx,
		DBPath:             dbPath,
		Runner:             s.runner,
		HostChecker:        hostChecker,
		MsgChan:            &s.msgChan,
		ZhengmingRequester: s,
		WaitForZhengming:   s.WaitForZhengming,
		NotifyFn:           notifyFn,
		MinisterConsultant: s,
		RitualLauncher:     ritualLauncher,
		MinisterIDs:        ministerIDs,
	}

	tools.RegisterBuiltinTools(registry, opts)

	// Propagate registry to all ministers so they can use ForPermissions
	for _, m := range s.ministers {
		if base, ok := m.(interface{ SetToolRegistry(*tools.ToolRegistry) }); ok {
			base.SetToolRegistry(registry)
		}
	}

	// Store repoInfo pointer so SetRepoInfo can update it later.
	s.toolCtxRepoInfo = repoInfo

	return registry
}

// updateProjectRootTools re-registers file and shell tools with the new project root.
// Called when SetRepoInfo receives a non-empty ProjectRoot after initial construction.
func (s *Court) updateProjectRootTools(projectRoot string) {
	if s == nil || s.toolRegistry == nil || projectRoot == "" {
		return
	}

	// Earth/Read — file exploration tools
	s.toolRegistry.Update(tools.NewReadFileTool(projectRoot))
	s.toolRegistry.Update(tools.ReadFileTool{ProjectRoot: projectRoot})
	s.toolRegistry.Update(tools.GlobTool{ProjectRoot: projectRoot})
	s.toolRegistry.Update(tools.GrepTool{ProjectRoot: projectRoot})

	// Earth/Write — file modification tools
	s.toolRegistry.Update(tools.WriteFileTool{ProjectRoot: projectRoot})
	s.toolRegistry.Update(tools.ReplaceTextTool{ProjectRoot: projectRoot})

	// Earth/Execute — shell command execution (needs runner)
	if s.runner != nil {
		s.toolRegistry.Update(tools.NewRunShellCommand(s.hostChecker, s.runner, &s.msgChan, projectRoot))
	}

	s.logger.Debug("updated project-root-dependent tools", "projectRoot", projectRoot)
}

// GetToolRegistry returns the central tool registry.
func (s *Court) GetToolRegistry() *tools.ToolRegistry {
	if s == nil {
		return nil
	}
	return s.toolRegistry
}

// SetSessionPersister wires a persister into the court. Sessions
// created afterwards (and any currently held by interactive ministers)
// receive it; sessions without a TabType are silently skipped at save
// time. Idempotent — safe to call again with a different persister.
func (s *Court) SetSessionPersister(p SessionPersister) {
	if s == nil {
		return
	}
	s.persister = p
	for _, minister := range s.Ministers() {
		if setter, ok := minister.(interface{ SetSessionPersister(SessionPersister) }); ok {
			setter.SetSessionPersister(p)
		}
	}
}

// SetNotify sets the notification callback for all ministers.
func (s *Court) SetNotify(notify internal.NotifyFunc) {
	if s == nil {
		return
	}
	s.notify = notify
	if s.ritualGuard != nil {
		s.ritualGuard.SetNotify(notify)
	}
	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface{ SetNotify(internal.NotifyFunc) }); ok {
			base.SetNotify(notify)
		}
	}
	// Send deferred drain notification if events were recovered at startup.
	// Use goroutine because notify may call program.Send before program.Run.
	if len(s.drainedEvents) > 0 {
		events := s.drainedEvents
		go func() { notify(EventsDrainedMsg{Events: events}) }()
	}
}

// Start initializes the Court lifecycle.
func (s *Court) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.ensureDefaults()
	if ctx == nil {
		ctx = context.Background()
	}
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Load rituals
	if err := s.ritualGuard.LoadRituals(); err != nil {
		s.logger.Warn("failed to load rituals", "error", err)
	}

	for _, minister := range s.Ministers() {
		go minister.Run(s.ctx)
	}

	// Wire minister lookup into all MinisterBase instances so ConsultMinister works
	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface{ SetMinisterLookup(func(string) Minister) }); ok {
			base.SetMinisterLookup(s.GetMinister)
		}
	}

	// Inject ritual summaries hook into the secretary so its Scratchpad
	// includes available rituals.
	if ch := s.GetMinister("secretary"); ch != nil {
		if base, ok := ch.(interface{ SetRitualSummaries(func() string) }); ok {
			base.SetRitualSummaries(func() string {
				if s.GetRitualRegistry() == nil {
					return "None loaded\n"
				}
				return s.GetRitualRegistry().Summaries()
			})
		}
	}

	// Discover and inject skills into all ministers.
	s.injectSkills()

	if s.ritualGuard != nil {
		go s.ritualGuard.Run(s.ctx)
	}

	s.logger.Info("court started",
		"ministers", s.ministerIDs(),
		"rituals", s.ritualGuard.RitualRegistry().List())

	return nil
}

// SetRepoInfo sets the repo info on the court and all ministers.
// This must be called before Start() to ensure rituals load from the
// correct project root. ConfigureModel will also set repoInfo, but
// that happens after Start() in the normal flow.
func (s *Court) SetRepoInfo(repoInfo repo.RepoInfo) {
	if s == nil {
		return
	}
	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface{ SetRepoInfo(repo.RepoInfo) }); ok {
			base.SetRepoInfo(repoInfo)
		}
	}
	if s.ritualGuard != nil {
		s.ritualGuard.repoInfo = repoInfo
	}
	// Update the shared ToolContext RepoInfo so all tools see the new root
	if s.toolCtxRepoInfo != nil {
		*s.toolCtxRepoInfo = repoInfo
	}
	// Update project-root-dependent tools when the root becomes available
	if repoInfo.ProjectRoot != "" {
		s.updateProjectRootTools(repoInfo.ProjectRoot)
	}
}

// injectSkills discovers skills and injects them into all ministers.
func (s *Court) injectSkills() {
	// Use repoInfo from the first minister — all share the same project root.
	projectRoot := ""
	for _, m := range s.Ministers() {
		if m.RepoInfo().ProjectRoot != "" {
			projectRoot = m.RepoInfo().ProjectRoot
			break
		}
	}
	if projectRoot == "" {
		return
	}

	skillsMap := skills.Discover(projectRoot)
	if skillsMap == nil {
		return
	}

	for _, m := range s.Ministers() {
		if base, ok := m.(interface {
			SetSkills(func(ministerID string) string)
		}); ok {
			minID := m.ID()
			base.SetSkills(func(ministerID string) string {
				// Always use the actual minister ID from the closure
				mid := minID
				if ministerID != "" {
					mid = ministerID
				}
				matchingSkills := skills.ForMinister(skillsMap, mid)
				return skills.FormatIndex(matchingSkills)
			})
		}
	}
}

// Stop gracefully shuts down the Court.
func (s *Court) Stop() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	// Close the runner — stops and removes any sandbox containers
	if s.runner != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.runner.Close(ctx); err != nil {
			s.logger.Warn("runner close failed", "error", err)
		}
	}
	s.logger.Info("court stopped")
	return nil
}

// GetMinister returns a minister by ID.
func (s *Court) GetMinister(id string) Minister {
	if s == nil || id == "" {
		return nil
	}
	for _, minister := range s.Ministers() {
		if minister.ID() == id {
			return minister
		}
	}
	return nil
}

// ConfigureModel sets the LLM client for all ministers.
// This should be called once the LLM client is initialized.
func (s *Court) ConfigureModel(client LLMProvider, config *SessionConfig, repoInfo repo.RepoInfo) {
	if s == nil {
		return
	}
	s.llmClient = client
	s.sessionCfg = config
	if config != nil {
		// Re-broadcast the resolved provider/model to live sessions so they
		// record an ATIF model_change when the switch actually happens (the
		// recorder lives on the session, not the court), and propagate the
		// reasoning-effort level as a thinking_level_change.
		s.propagateModelToSessions(config)
	}
	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface {
			SetMinisterConfig(LLMProvider, *SessionConfig, repo.RepoInfo)
		}); ok {
			base.SetMinisterConfig(client, config, repoInfo)
		}
	}
	// Propagate sandbox config, project slug, and repoInfo to the
	// RitualRunner so it never needs to reload config from disk.
	// repoInfo is especially important because the runner is created
	// with an empty ProjectRoot at daemon
	// startup and only receives the real root via SetContext/ConfigureModel.
	if s.ritualGuard != nil && s.ritualGuard.RitualRunner() != nil {
		sandboxCfg := &config.Sandbox
		projectSlug := ""
		if s.config != nil {
			projectSlug = s.config.Project
		}
		s.ritualGuard.RitualRunner().SetConfig(sandboxCfg, projectSlug, repoInfo, s.config)
		if config != nil {
			s.ritualGuard.RitualRunner().SetAtifAgentName(config.AtifAgentName)
		}
	}
	// Update repoInfo on the RitualGuard (not covered by the Ministers
	// loop since RitualGuard is stored separately) and reload rituals
	// when the project root becomes available for the first time.
	// Start() skips loading if repoInfo.ProjectRoot was empty, so we
	// retry here once SetContext/ConfigureModel has the real root.
	// We only reload when the registry is still empty (i.e., no one
	// has manually registered rituals since Start).
	if s.ritualGuard != nil && repoInfo.ProjectRoot != "" {
		wasEmpty := s.ritualGuard.repoInfo.ProjectRoot == ""
		s.ritualGuard.repoInfo = repoInfo
		if wasEmpty && len(s.ritualGuard.RitualRegistry().List()) == 0 {
			if err := s.ritualGuard.LoadRituals(); err != nil {
				s.logger.Warn("failed to load rituals after ConfigureModel", "error", err)
			}
		}
	}

	// Inject skills when project root becomes available the first time
	s.injectSkills()

	s.logger.Info("court model configured", "ministers", s.ministerIDs())
}

// propagateModelToSessions broadcasts the resolved provider/model and
// reasoning-effort level to every live minister session so the attached
// ATIF recorder can emit model_change / thinking_level_change events.
func (s *Court) propagateModelToSessions(config *SessionConfig) {
	for _, minister := range s.Ministers() {
		gs, ok := minister.(interface{ GetSessions() map[string]*Session })
		if !ok {
			continue
		}
		for _, sess := range gs.GetSessions() {
			if sess == nil {
				continue
			}
			sess.UpdateModel(config.LLM.Provider, config.LLM.Model)
			sess.UpdateReasoningEffort(config.LLM.ReasoningEffort)
		}
	}
}

// SetContext reconfigures the Court with the given credentials and
// project context. In single-process mode this initialises Bifrost
// inline using the APIKeys from the params. It is idempotent — each
// call re-initialises the LLM client.
func (s *Court) SetContext(ctx context.Context, params types.SetContextParams) error {
	if s == nil {
		return fmt.Errorf("court not initialised")
	}

	// Load project config from ProjectRoot.
	projectRoot := params.ProjectRoot
	if projectRoot == "" {
		projectRoot = "."
	}
	// Validate that the path exists and is a directory before loading config.
	if info, err := os.Stat(projectRoot); err != nil {
		return fmt.Errorf("invalid project_root %q: %w", projectRoot, err)
	} else if !info.IsDir() {
		return fmt.Errorf("invalid project_root %q: not a directory", projectRoot)
	}

	projectCfg, err := config.LoadProjectConfig(projectRoot, false)
	if err != nil {
		return fmt.Errorf("load project config: %w", err)
	}

	// Apply per-run CLI/model overrides handed through the handshake.
	// These take precedence over config-file values (and env vars, which
	// have already been folded into the config by LoadProjectConfig).
	if params.Model != "" {
		projectCfg.LLM.Model = params.Model
	}
	if params.Provider != "" {
		projectCfg.LLM.Provider = params.Provider
	}
	if params.ReasoningEffort != "" {
		if err := config.ValidateReasoningEffort(params.ReasoningEffort); err != nil {
			return err
		}
		projectCfg.LLM.ReasoningEffort = params.ReasoningEffort
	}

	// When params.Project is empty (e.g., during init before config
	// exists), derive the slug from the git remote so the sandbox image
	// name is always correctly derived. This mirrors what loopback.go
	// does — it passes repoInfo.Slug from repo.GetRepoInfo().
	slug := params.Project
	if slug == "" && params.ProjectRoot != "" {
		slug = repo.GetRepoInfoForRoot(params.ProjectRoot).Slug
	}

	repoInfo := repo.RepoInfo{
		ProjectRoot:  params.ProjectRoot,
		WorktreePath: params.WorktreePath,
		Branch:       params.Branch,
		Slug:         slug,
	}

	// Init Bifrost with the APIKeys provided by the client.
	bifrostClient, err := InitBifrost(
		ctx,
		projectCfg.LLM.RequestTimeoutSeconds,
		projectCfg.LLM.StreamIdleTimeoutSeconds,
		projectCfg.LLM.MaxRetries,
		projectCfg.LLM.BaseURL,
		params.APIKeys,
		params.CodexAccountID,
	)
	if err != nil {
		return fmt.Errorf("init bifrost: %w", err)
	}

	sessionCfg := &SessionConfig{
		LLM:           projectCfg.LLM,
		Sandbox:       projectCfg.Sandbox,
		AgentsFile:    projectCfg.Session.AgentsFile,
		WorkingDir:    params.ProjectRoot,
		AtifAgentName: params.AtifAgentName,
	}

	s.ConfigureModel(bifrostClient, sessionCfg, repoInfo)
	return nil
}

// ListModels delegates to the configured LLM provider (bifrost) to list
// models for a specific provider. Returns an error if the LLM client has
// not been initialized yet.
func (s *Court) ListModels(provider string) (*schemas.BifrostListModelsResponse, error) {
	if s.llmClient == nil {
		return nil, fmt.Errorf("LLM client not initialized — use :login to configure a provider")
	}
	ctx := schemas.NewBifrostContext(s.ctx, schemas.NoDeadline)
	bifrostProvider := schemas.ModelProvider(asimiProviderToBifrostCourt(provider))
	resp, bifrostErr := s.llmClient.ListModelsRequest(ctx, &schemas.BifrostListModelsRequest{
		Provider: bifrostProvider,
	})
	if bifrostErr != nil {
		return nil, bifrostErrorToGoError(bifrostErr)
	}
	return resp, nil
}

// asimiProviderToBifrostCourt maps an asimi provider key to bifrost's provider string.
func asimiProviderToBifrostCourt(asimiProvider string) string {
	switch asimiProvider {
	case "googleai":
		return "gemini"
	default:
		return asimiProvider
	}
}

// Ministers returns the active ministers.
func (s *Court) Ministers() []Minister {
	if s == nil || s.ministers == nil {
		return nil
	}

	active := make([]Minister, 0, len(s.ministers))
	for _, minister := range s.ministers {
		if minister != nil {
			active = append(active, minister)
		}
	}
	return active
}

// EdictKey returns the current context as an EdictKey with the given edict ID.
func (s *Court) EdictKey(edictID uint) storage.EdictKey {
	return storage.EdictKey{ID: edictID, Username: s.config.Username, Project: s.config.Project}
}

// CourtEdictKey returns the Court Infrastructure edict key (edict 1).
// This is used for system-level operations like startup events.
func (s *Court) CourtEdictKey() storage.EdictKey {
	return storage.EdictKey{ID: 0, Username: s.config.Username, Project: s.config.Project}
}

// nextEdictID returns the next available edict ID (MAX+1).
func (s *Court) nextEdictID() uint {
	var maxID uint
	s.db.Model(&storage.Edict{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID)
	return maxID + 1
}

// CreateEdict creates a new active edict record in the database and publishes storage.EventEdictCreated.
// sessionID links the edict to the chat session that suggested it (empty for direct ruler-created edicts).
// TODO: Add a "summary" parameter which is already in the Edict
func (s *Court) CreateEdict(issueRef, intent, sessionID string) (*storage.Edict, error) {
	edict, err := s.createEdict(issueRef, intent, sessionID)
	if err != nil {
		return nil, err
	}
	s.PublishEvent(edict.Key(), storage.EventEdictCreated, storage.JSON{
		"intent": intent,
		"id":     edict.ID,
	})
	return edict, nil
}

// CreateEdictSilent creates an edict without publishing EventEdictCreated.
// Use this when the caller already knows which ritual to run and will
// dispatch it directly, so routing through the chancellor LLM would be
// redundant and would double-start the ritual.
func (s *Court) CreateEdictSilent(issueRef, intent, sessionID string) (*storage.Edict, error) {
	return s.createEdict(issueRef, intent, sessionID)
}

func (s *Court) createEdict(issueRef, intent, sessionID string) (*storage.Edict, error) {
	edict := storage.Edict{
		ID:        s.nextEdictID(),
		Username:  s.config.Username,
		Project:   s.config.Project,
		IssueRef:  issueRef,
		Intent:    intent,
		SessionID: sessionID,
	}
	if err := s.db.Create(&edict).Error; err != nil {
		return nil, fmt.Errorf("failed to create edict: %w", err)
	}
	return &edict, nil
}

// CreateEdictForTest creates an edict without publishing events (for unit tests).
func CreateEdictForTest(db *gorm.DB, intent string) (*storage.Edict, error) {
	var maxID uint
	db.Model(&storage.Edict{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID)
	edict := storage.Edict{
		ID:     maxID + 1,
		Intent: intent,
	}
	if err := db.Create(&edict).Error; err != nil {
		return nil, fmt.Errorf("failed to create edict: %w", err)
	}
	return &edict, nil
}

// CreateEdictForTestWithSession creates an edict with a session ID (for unit tests).
func CreateEdictForTestWithSession(db *gorm.DB, intent, sessionID string) (*storage.Edict, error) {
	var maxID uint
	db.Model(&storage.Edict{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID)
	edict := storage.Edict{
		ID:        maxID + 1,
		Intent:    intent,
		SessionID: sessionID,
	}
	if err := db.Create(&edict).Error; err != nil {
		return nil, fmt.Errorf("failed to create edict: %w", err)
	}
	return &edict, nil
}

// PublishEvent delegates to RitualGuard.
func (s *Court) PublishEvent(key storage.EdictKey, eventType storage.CourtEvent, payload storage.JSON) uint {
	if s == nil || s.ritualGuard == nil {
		return key.ID
	}
	return s.ritualGuard.PublishEvent(key, eventType, payload)
}

// CancelEdict marks an edict as cancelled and stops any running ritual.
func (s *Court) CancelEdict(edictID uint) error {
	now := time.Now()
	result := s.db.Model(&storage.Edict{}).
		Where("id = ? AND username = ? AND project = ?", edictID, s.config.Username, s.config.Project).
		Update("cancelled_at", now)
	if result.Error != nil {
		return fmt.Errorf("failed to cancel edict: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("edict not found: %d", edictID)
	}
	// Stop any running ritual for this edict's channel
	s.CancelTab(ritualChannelID(edictID))
	return nil
}

// AppendToIntent appends a clarification to an edict's intent (Ruler-initiated edit).
func (s *Court) AppendToIntent(edictID uint, clarification string) error {
	key := s.EdictKey(edictID)
	return s.appendToIntent(key, clarification)
}

// SetIntent replaces an edict's intent with the given text.
func (s *Court) SetIntent(edictID uint, intent string) error {
	key := s.EdictKey(edictID)
	var edict storage.Edict
	if err := s.db.Where("id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).First(&edict).Error; err != nil {
		return fmt.Errorf("get edict: %w", err)
	}
	if err := s.db.Model(&storage.Edict{}).Where("id = ?", edict.ID).Update("intent", intent).Error; err != nil {
		return fmt.Errorf("update edict intent: %w", err)
	}
	return nil
}

// appendToIntent is the shared implementation used by both the Ruler-facing
// method and the MinisterBase path.
func (s *Court) appendToIntent(key storage.EdictKey, clarification string) error {
	var edict storage.Edict
	if err := s.db.Where("id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).First(&edict).Error; err != nil {
		return fmt.Errorf("get edict: %w", err)
	}
	updatedIntent := edict.Intent + "\n\n---\n**Clarification:**\n" + clarification
	if err := s.db.Model(&storage.Edict{}).Where("id = ?", edict.ID).Update("intent", updatedIntent).Error; err != nil {
		return fmt.Errorf("update edict intent: %w", err)
	}

	// Invalidate existing seals — they were earned against the old intent
	if err := s.GetSealService().InvalidateSeals(key); err != nil {
		return fmt.Errorf("invalidate seals: %w", err)
	}
	return nil
}

// DispatchEvent delegates to RitualGuard.
func (s *Court) DispatchEvent(event Event) {
	if s == nil || s.ritualGuard == nil {
		return
	}
	s.ritualGuard.DispatchEvent(event)
}

func (s *Court) ensureDefaults() {
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.config == nil {
		s.config = config.DefaultCourtConfig()
	}
}

func (s *Court) ministerIDs() []string {
	ministers := s.Ministers()
	ids := make([]string, 0, len(ministers))
	for _, minister := range ministers {
		ids = append(ids, minister.ID())
	}
	return ids
}

// ensureCourtInfrastructureEdict creates edict 1 if it doesn't exist.
// Edict 1 is reserved for Court Infrastructure operations (init, bootstrap, etc.)
func (s *Court) ensureCourtInfrastructureEdict() {
	var edict storage.Edict
	if err := s.db.First(&edict, "id = ? AND username = ? AND project = ?", 1, s.config.Username, s.config.Project).Error; err == nil {
		return
	}

	courtEdict := storage.Edict{
		ID:       1,
		Username: s.config.Username,
		Project:  s.config.Project,
		Intent:   "Court Infrastructure - reserved for system-level operations (init, bootstrap, etc.)",
		Summary:  "Court Infrastructure",
		IssueRef: "court-infra",
	}
	if err := s.db.Create(&courtEdict).Error; err != nil {
		s.logger.Warn("failed to create court infrastructure edict", "error", err)
		return
	}

	s.logger.Info("court infrastructure edict created", "edict_id", 1)
}

// GetRunner returns the shell runner
func (s *Court) GetRunner() runners.Runner {
	if s == nil {
		return nil
	}
	return s.runner
}

// SetRunner updates the court's shell runner (e.g. after sandbox comes up)
// and propagates the change to all ministers so their shell tools run in the container.
func (s *Court) SetRunner(r runners.Runner) {
	if s == nil {
		return
	}
	s.runner = r
	// Set globally so runners package can be used directly
	runners.SetRunner(r)
	// Propagate to all ministers that implement RunnerSetter
	for _, m := range s.ministers {
		if setter, ok := m.(interface{ SetRunner(runners.Runner) }); ok {
			setter.SetRunner(r)
		}
	}
}

// SetRunnerMessageChannel sets the message channel on the runner for approval requests.
// Ministers and tools hold a pointer to s.msgChan, so updating s.msgChan here
// is automatically visible to all of them — no explicit propagation loop needed.
func (s *Court) SetRunnerMessageChannel(msgChan chan<- runners.Msg) {
	if s == nil || s.runner == nil {
		return
	}
	s.msgChan = msgChan
	s.runner.SetMessageChannel(msgChan)
}

// Subscribe returns a channel carrying every TUI-bound notification produced
// by the court: streaming chunks, events, and runner messages. It installs
// the underlying SetNotify callback and the runner message channel on the
// caller's behalf, so callers no longer touch either directly. The returned
// channel stays open for the Court's lifetime; the caller drains it until
// ctx is cancelled.
func (s *Court) Subscribe(ctx context.Context) <-chan any {
	if s == nil {
		closed := make(chan any)
		close(closed)
		return closed
	}
	out := make(chan any, 256)

	s.SetNotify(func(msg any) {
		select {
		case out <- msg:
		case <-ctx.Done():
		}
	})

	if s.runner != nil {
		runnerMsgChan := make(chan runners.Msg, 10)
		s.SetRunnerMessageChannel(runnerMsgChan)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok := <-runnerMsgChan:
					if !ok {
						return
					}
					// ClearSchedulerMsg is an in-process request/reply — handle it here
					if clearMsg, ok := msg.(runners.ClearSchedulerMsg); ok {
						count := s.clearAllSchedulers()
						clearMsg.ResultChan <- count
						continue // do NOT forward to out
					}
					select {
					case out <- msg:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	return out
}

// clearAllSchedulers iterates all ministers with sessions and calls
// scheduler.ClearQueue() on each, returning the total aborted count.
func (s *Court) clearAllSchedulers() int {
	total := 0
	for _, m := range s.ministers {
		if m == nil {
			continue
		}
		// GetSessions is on MinisterBase, not the Minister interface
		gs, ok := m.(interface{ GetSessions() map[string]*Session })
		if !ok {
			continue
		}
		for _, sess := range gs.GetSessions() {
			if sess == nil || sess.scheduler == nil {
				continue
			}
			total += sess.scheduler.ClearQueue()
		}
	}
	return total
}

// GetRitualRegistry returns the ritual registry
func (s *Court) GetRitualRegistry() *RitualRegistry {
	if s == nil || s.ritualGuard == nil {
		return nil
	}
	return s.ritualGuard.RitualRegistry()
}

// GetRitualRunner returns the ritual runner
func (s *Court) GetRitualRunner() *RitualRunner {
	if s == nil || s.ritualGuard == nil {
		return nil
	}
	return s.ritualGuard.RitualRunner()
}

// GetEventRegistry returns the event registry
func (s *Court) GetEventRegistry() *EventRegistry {
	if s == nil || s.ritualGuard == nil {
		return nil
	}
	return s.ritualGuard.EventRegistry()
}

// CancellableStreamCtx returns a context derived from the Court's
// root ctx and registers its cancel func under channelID. Any previous
// cancel for the same channelID is invoked first, so callers don't
// leak. Use CancelTab(channelID) to trigger cancellation externally.
func (s *Court) CancellableStreamCtx(channelID string) context.Context {
	if s == nil {
		return context.Background()
	}
	s.tabCancelsMu.Lock()
	defer s.tabCancelsMu.Unlock()
	if s.tabCancels == nil {
		s.tabCancels = make(map[string]context.CancelFunc)
	}
	if prev, ok := s.tabCancels[channelID]; ok {
		prev()
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.tabCancels[channelID] = cancel
	return ctx
}

// CancelTab invokes (and forgets) the cancel func registered under
// channelID. Idempotent; calling with an unknown channelID is a no-op.
func (s *Court) CancelTab(channelID string) {
	if s == nil {
		return
	}
	s.tabCancelsMu.Lock()
	cancel, ok := s.tabCancels[channelID]
	if ok {
		delete(s.tabCancels, channelID)
	}
	s.tabCancelsMu.Unlock()
	if ok {
		cancel()
	}
}

// PauseRitual pauses the ritual running on channelID for ruler interjection.
// The step's LLM stream is interrupted but the ritual goroutine stays alive.
func (s *Court) PauseRitual(channelID string) bool {
	if s == nil {
		return false
	}
	runner := s.GetRitualRunner()
	if runner == nil {
		return false
	}
	return runner.PauseRitual(channelID)
}

// ResumeRitual resumes a paused ritual on channelID.
func (s *Court) ResumeRitual(channelID string) bool {
	if s == nil {
		return false
	}
	runner := s.GetRitualRunner()
	if runner == nil {
		return false
	}
	return runner.ResumeRitual(channelID)
}

// SubmitPrompt routes a prompt to the specified minister by ID.
func (s *Court) SubmitPrompt(targetID string, p *Prompt) error {
	if s == nil {
		return fmt.Errorf("court not initialized")
	}
	minister := s.GetMinister(targetID)
	if minister == nil {
		return fmt.Errorf("minister not found: %s", targetID)
	}
	go minister.SubmitPrompt(p)
	return nil
}

// RestoreMinisterSession rebuilds the session of the minister identified
// by tabType (matches the saved Session.TabType, which is the minister id)
// and seeds it with msgs. Works for any minister that implements
func (s *Court) RestoreMinisterSession(tabType string, msgs []schemas.ChatMessage, channelID ...string) error {
	if s == nil {
		return fmt.Errorf("court not initialized")
	}
	minister := s.GetMinister(tabType)
	if minister == nil {
		return fmt.Errorf("minister not found: %s", tabType)
	}
	return minister.RestoreSession(minister, msgs, channelID...)
}

// ResetChancellor resets the secretary (formerly chancellor) session
func (s *Court) ResetChancellor() {
	if s == nil {
		return
	}
	s.ResetMinisterSession("secretary")
}

// ResetHunting resets the chancellor (formerly sage) session
func (s *Court) ResetHunting() {
	if s == nil {
		return
	}
	s.ResetMinisterSession("chancellor")
}

// GetSealService returns the seal service
func (s *Court) GetSealService() *storage.SealService {
	if s == nil || s.db == nil {
		return nil
	}
	return storage.NewSealService(s.db)
}

// HasMinister reports whether a minister with the given id is registered.
func (s *Court) HasMinister(id string) bool {
	return s != nil && s.GetMinister(id) != nil
}

// ResetMinisterSession resets the session of the minister with the given id.
// If channelID is provided, resets only that channel's session; otherwise
// resets the minister's own interactive session.
// No-op if the minister doesn't exist or doesn't expose ResetSession.
func (s *Court) ResetMinisterSession(id string, channelID ...string) {
	if s == nil {
		return
	}
	m := s.GetMinister(id)
	if m == nil {
		return
	}
	if rs, ok := m.(interface{ ResetSession(channelID ...string) }); ok {
		rs.ResetSession(channelID...)
	}
}

// GetEdict looks up an edict in the current court scope.
func (s *Court) GetEdict(edictID uint) (*storage.Edict, error) {
	if s == nil {
		return nil, fmt.Errorf("court not initialized")
	}
	key := s.EdictKey(edictID)
	var edict storage.Edict
	if err := s.db.First(&edict, "id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("edict not found: %d", key.ID)
		}
		return nil, fmt.Errorf("failed to get edict: %w", err)
	}
	return &edict, nil
}

// GrantRulerSeal stamps the Ruler's seal on an edict and publishes EventSealGranted.
func (s *Court) GrantRulerSeal(edictID uint, notes string) error {
	if s == nil {
		return fmt.Errorf("court not initialized")
	}
	sealer := s.GetSealService()
	if sealer == nil {
		return fmt.Errorf("seal service not available")
	}
	key := s.EdictKey(edictID)
	timestamp := time.Now().Format(time.RFC3339)
	if err := sealer.GrantSeal(key, "ruler", storage.JSON{
		"notes":     notes,
		"timestamp": timestamp,
	}); err != nil {
		return err
	}
	s.PublishEvent(key, storage.EventSealGranted, storage.JSON{
		"minister_id": "ruler",
		"notes":       notes,
		"timestamp":   timestamp,
	})
	return nil
}

// GetEdictSeals returns the seal chain for an edict.
func (s *Court) GetEdictSeals(key storage.EdictKey) ([]storage.Seal, error) {
	if s == nil {
		return nil, fmt.Errorf("court not initialized")
	}
	sealer := s.GetSealService()
	if sealer == nil {
		return nil, fmt.Errorf("seal service not available")
	}
	return sealer.GetSeals(key)
}

// ListActiveEdicts returns edicts in the current scope that aren't cancelled or sealed.
func (s *Court) ListActiveEdicts() ([]storage.ActiveEdict, error) {
	if s == nil {
		return nil, fmt.Errorf("court not initialized")
	}
	sealer := s.GetSealService()
	if sealer == nil {
		return nil, fmt.Errorf("seal service not available")
	}
	return sealer.ListActiveEdicts(s.config.Username, s.config.Project)
}

// AllowRunnerFallback toggles the host-fallback behaviour on the active runner.
func (s *Court) AllowRunnerFallback(allow bool) {
	if s == nil || s.runner == nil {
		return
	}
	s.runner.AllowFallback(allow)
}

// RunShellCommand executes a shell command on the active runner.
func (s *Court) RunShellCommand(ctx context.Context, input runners.Input) (runners.Output, error) {
	if s == nil || s.runner == nil {
		return runners.Output{}, fmt.Errorf("runner not initialized")
	}
	return s.runner.Run(ctx, input)
}

// HandleZhengmingResponse dispatches a user's zhengming answer to the first
// minister that knows how to handle one.
func (s *Court) HandleZhengmingResponse(ctx context.Context, requestID, answer string) error {
	if s == nil {
		return fmt.Errorf("court not initialized")
	}
	type zhengmingHandler interface {
		HandleZhengmingResponse(ctx context.Context, requestID, answer string) error
	}
	for _, m := range s.Ministers() {
		if h, ok := m.(zhengmingHandler); ok {
			return h.HandleZhengmingResponse(ctx, requestID, answer)
		}
	}
	return fmt.Errorf("no minister accepted zhengming response")
}

// CancelZhengming cancels a pending zhengming request on the Court's dispatch.
func (s *Court) CancelZhengming(requestID string) {
	if s == nil {
		return
	}
	s.CancelZhengmingDispatch(requestID)
}

// SessionState is a wire-safe snapshot of a minister's conversation state,
// aggregated in one call for cheap TUI-side access. Exists=false means no
// session exists for that tab.
type SessionState struct {
	Exists              bool
	ChannelID           string
	MessageCount        int
	MessageSnapshot     int
	ContextInfo         ContextInfo
	ContextUsagePercent float64
	ContextFiles        map[string]string
}

func (s *Court) sessionForTab(tabTarget string) *Session {
	if s == nil {
		return nil
	}
	// Try direct minister lookup first (interactive tabs: "chancellor", "secretary")
	m := s.GetMinister(tabTarget)
	if m != nil {
		return m.GetSession(tabTarget)
	}
	// Ritual/edict tabs: check all ministers for a session keyed by this channel
	for _, m := range s.ministers {
		if m == nil {
			continue
		}
		if sess := m.GetSession(tabTarget); sess != nil {
			return sess
		}
	}
	return nil
}

// SessionState returns a snapshot of the session attached to the given tab.
func (s *Court) SessionState(tabTarget string) SessionState {
	sess := s.sessionForTab(tabTarget)
	if sess == nil {
		return SessionState{}
	}
	return SessionState{
		Exists:              true,
		ChannelID:           sess.ChannelID(),
		MessageCount:        len(sess.GetMessages()),
		MessageSnapshot:     sess.GetMessageSnapshot(),
		ContextInfo:         sess.GetContextInfo(),
		ContextUsagePercent: sess.GetContextUsagePercent(),
		ContextFiles:        sess.GetContextFiles(),
	}
}

// AddSessionContextFile attaches a file's contents to the given tab's session.
func (s *Court) AddSessionContextFile(tabTarget, path, content string) error {
	sess := s.sessionForTab(tabTarget)
	if sess == nil {
		return fmt.Errorf("no session for tab %q", tabTarget)
	}
	sess.AddContextFile(path, content)
	return nil
}

// AddSessionMessage appends a message to the tab's conversation.
func (s *Court) AddSessionMessage(tabTarget, role, content string) error {
	sess := s.sessionForTab(tabTarget)
	if sess == nil {
		return fmt.Errorf("no session for tab %q", tabTarget)
	}
	sess.AddMessage(schemas.ChatMessageRole(role), content)
	return nil
}

// ClearSessionHistory resets the tab's conversation to an empty state.
func (s *Court) ClearSessionHistory(tabTarget string) error {
	sess := s.sessionForTab(tabTarget)
	if sess == nil {
		return fmt.Errorf("no session for tab %q", tabTarget)
	}
	sess.ClearHistory()
	return nil
}

// RollbackSession rewinds the conversation to the given message snapshot.
func (s *Court) RollbackSession(tabTarget string, snapshot int) error {
	sess := s.sessionForTab(tabTarget)
	if sess == nil {
		return fmt.Errorf("no session for tab %q", tabTarget)
	}
	sess.RollbackTo(snapshot)
	return nil
}

// CompactSession runs a summarisation pass and returns the summary, replacing
// older messages in the conversation with it.
func (s *Court) CompactSession(ctx context.Context, tabTarget, prompt string) (string, error) {
	sess := s.sessionForTab(tabTarget)
	if sess == nil {
		return "", fmt.Errorf("no session for tab %q", tabTarget)
	}
	return sess.CompactHistory(ctx, prompt)
}

// SessionExport is a wire-safe copy of everything an exporter needs:
// the session ID, its full message history, and the metadata fields
// FormatMetadata renders. It satisfies the ExportableSession interface
// used by export.go without the TUI ever touching the live *Session.
type SessionExport struct {
	ID           string                `msgpack:"id"`
	Messages     []schemas.ChatMessage `msgpack:"messages"`
	ContextFiles map[string]string     `msgpack:"context_files,omitempty"`
	Provider     string                `msgpack:"provider,omitempty"`
	Model        string                `msgpack:"model,omitempty"`
	WorkingDir   string                `msgpack:"working_dir,omitempty"`
	ProjectSlug  string                `msgpack:"project_slug,omitempty"`
	CreatedAt    time.Time             `msgpack:"created_at"`
	LastUpdated  time.Time             `msgpack:"last_updated"`
}

// GetID satisfies ExportableSession.
func (e *SessionExport) GetID() string { return e.ID }

// GetMessages satisfies ExportableSession.
func (e *SessionExport) GetMessages() []schemas.ChatMessage { return e.Messages }

// GetContextFiles satisfies ExportableSession.
func (e *SessionExport) GetContextFiles() map[string]string { return e.ContextFiles }

// FormatMetadata satisfies ExportableSession; mirrors Session.FormatMetadata
// so exports look identical whether sourced live or over the wire.
func (e *SessionExport) FormatMetadata(exportType, exportedAt string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Export Type:** %s\n", exportType))
	b.WriteString(fmt.Sprintf("**Session ID:** %s | **Working Directory:** %s\n", e.ID, e.WorkingDir))
	b.WriteString(fmt.Sprintf("**Provider:** %s | **Model:** %s\n", e.Provider, e.Model))
	b.WriteString(fmt.Sprintf("**Created:** %s | **Last Updated:** %s | **Exported:** %s\n",
		e.CreatedAt.Format("2006-01-02 15:04:05"),
		e.LastUpdated.Format("2006-01-02 15:04:05"),
		exportedAt))
	if e.ProjectSlug != "" {
		b.WriteString(fmt.Sprintf("**Project:** %s\n", e.ProjectSlug))
	}
	return b.String()
}

// GetSessionExport returns a wire-safe snapshot of the tab's session
// suitable for feeding to exportSession. Nil if no session exists.
func (s *Court) GetSessionExport(tabTarget string) (*SessionExport, error) {
	sess := s.sessionForTab(tabTarget)
	if sess == nil {
		return nil, fmt.Errorf("no session for tab %q", tabTarget)
	}
	msgs := make([]schemas.ChatMessage, len(sess.Messages()))
	copy(msgs, sess.Messages())
	return &SessionExport{
		ID:           sess.ID,
		Messages:     msgs,
		ContextFiles: sess.GetContextFiles(),
		Provider:     sess.Provider,
		Model:        sess.Model,
		WorkingDir:   sess.WorkingDir,
		ProjectSlug:  sess.ProjectSlug,
		CreatedAt:    sess.CreatedAt,
		LastUpdated:  sess.LastUpdated,
	}, nil
}

// connDoneNever is a channel that is never closed. Returned by ConnDone
// for in-process implementations where the connection never drops.
var connDoneNever = make(chan struct{})

// ConnDone returns a channel that is never closed for the in-process
// Court — there is no transport that can drop.
func (s *Court) ConnDone() <-chan struct{} { return connDoneNever }
