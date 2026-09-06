package court

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal"
	"github.com/afittestide/asimi/internal/atif"
	internalconfig "github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/vmihailenco/msgpack/v5"
	"gorm.io/gorm"
)

//go:embed context/realm.md
var realm string

//go:embed context/system_base.tmpl
var systemBase string
var ministerTmpl = template.Must(template.New("minister").Parse(systemBase))

// ZhengmingConn provides clarification request capabilities (behavioral interface)
type ZhengmingConn interface {
	RequestZhengming(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (requestID string, err error)
	IsZhengmingPending(key storage.EdictKey) (bool, error)
}

// EventEmitter emits events to the Ritual Guard's ledger (behavioral interface)
type EventEmitter interface {
	EmitEvent(key storage.EdictKey, eventType storage.CourtEvent, payload storage.JSON) error
}

// === UNIFIED TYPES ===

// Prompt carries the user's message to the Chancellor
type Prompt struct {
	Ctx          context.Context   // Per-prompt context for cancellation (CTRL-C)
	Message      string            // The Ruler's words
	EdictKey     storage.EdictKey  // Zero = new edict, set = continue existing
	ChannelID    string            // Channel target for stream routing
	ContextFiles map[string]string // Files loaded via @ references
}

// Task carries work from Chancellor to a Minister
type Task struct {
	Ctx          context.Context     // Per-task cancellation (e.g. CTRL-C)
	EdictKey     storage.EdictKey    // The edict this task belongs to
	Work         string              // Specific instructions for the minister (renamed from Task to avoid Task.Task)
	Scratchpad   string              // Pre-formatted markdown added to the context
	Session      *Session            // Existing session for multi-turn (nil = create new)
	Done         chan<- Result       // For completion signal
	Notify       internal.NotifyFunc // Routing-aware notify override (nil = use minister's default)
	ChannelID    string              // Routing target for stream messages (set by caller)
	ExcludeTools []string            // Tool names to exclude from the minister's session (e.g. consult_minister to prevent recursion)
}

// Result signals a Minister has completed a Task
type Result struct {
	MinisterID string
	Sealed     bool // phase complete
	Output     string
	Session    *Session // Return session for reuse by ritual runner
	Err        error
}

// Minister is the shared interface for all Court ministers
type Minister interface {
	// ID returns the minister's unique identifier (e.g., "war", "forge")
	ID() string
	// Logger returns the minister's logger with scoped metadata.
	Logger() *slog.Logger
	// SystemPrompt returns the minister's system prompt template string.
	SystemPrompt() string
	// Scratchpad returns dynamic per-minister context (e.g., available rituals, rules)
	Scratchpad() string
	// Tools returns the minister's LLM tools for interactive sessions
	Tools() []Tool
	// Tasks returns the channel for submitting Tasks
	Tasks() chan<- *Task
	// PromptsChan returns the channel for prompts via SubmitPrompt
	PromptsChan() <-chan *Prompt
	// SubmitPrompt sends a prompt to the minister
	SubmitPrompt(p *Prompt)
	// RepoInfo returns the repository information
	RepoInfo() repo.RepoInfo
	// Runner returns the shell runner (may be nil)
	Runner() runners.Runner
	// Model returns the minister's LLM client
	Model() LLMProvider
	// GetConfig returns the minister's LLM configuration
	GetConfig() internalconfig.LLMConfig
	// Run starts the minister's processing loop (blocks until context cancelled)
	Run(ctx context.Context)
	// GetSession returns the interactive session for the given channel ID.
	// If channelID is empty, returns the minister's own interactive session.
	GetSession(channelID ...string) *Session
	// RestoreSession creates a session and injects loaded history.
	RestoreSession(minister Minister, msgs []schemas.ChatMessage, channelID ...string) error
}

// --- External Dependencies ---

// LLMClient generates text using a language model
type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// GitOps provides git operations
type GitOps interface {
	Commit(ctx context.Context, changes []FileChange, message string) (commitHash string, err error)
}

// FileOp represents a file operation type
type FileOp int

const (
	FileOpCreate FileOp = iota
	FileOpModify
	FileOpDelete
)

// FileChange represents a file modification
type FileChange struct {
	Path    string
	Content []byte
	Op      FileOp
}

// EdictLock provides distributed locking for edicts
type EdictLock interface {
	Lock(edictID string) error
	Unlock(edictID string) error
}

// --- Tool Interface ---

// Tool defines a tool that can be invoked by ministers
type Tool interface {
	Name() string
	Description() string
	Call(ctx context.Context, input string) (string, error)
	Format(input, result string, err error) string
	ParameterSchema() map[string]any
}

// ZhengmingPendingMsg notifies the UI of a pending clarification request
type ZhengmingPendingMsg struct {
	RequestID  string                     `msgpack:"request_id"`
	EdictKey   storage.EdictKey           `msgpack:"edict_key"`
	MinisterID string                     `msgpack:"minister_id"`
	Questions  storage.ZhengmingQuestions `msgpack:"questions"`
	Priority   storage.ZhengmingPriority  `msgpack:"priority,omitempty"`
}

// ZhengmingAnsweredMsg notifies the UI that a clarification was answered
type ZhengmingAnsweredMsg struct {
	RequestID string `msgpack:"request_id"`
	Answer    string `msgpack:"answer,omitempty"`
}

// StreamDoneMsg signals that streaming has completed
type StreamDoneMsg struct {
	ChannelID string `msgpack:"channel_id"`
}

// PreTaskHook runs before the main task work. If handled=true is returned,
// the main streamTask is skipped.
type PreTaskHook func(ctx context.Context, task *Task, notify internal.NotifyFunc) (handled bool, result *Result)

// PostTaskHook runs after the main task work completes (e.g., validation
// steps). Returns a failure string for soft failures and an error for
// hard failures.
type PostTaskHook func(ctx context.Context, task *Task, session *Session, output string) (failure string, err error)

// TaskFallback is a no-LLM fallback for ministers that have deterministic
// execution paths (e.g., Judge's CI).
// Returns (sealed, error).
type TaskFallback func(ctx context.Context, task *Task) (bool, error)

// MinisterBase provides shared functionality for all ministers.
// Ministers embed this struct to gain database access and session creation capabilities.
type MinisterBase struct {
	db           *gorm.DB
	ministerID   string
	client       LLMProvider // LLM client for chat completions
	config       *SessionConfig
	repoInfo     repo.RepoInfo
	runner       runners.Runner
	msgChan      *chan<- runners.Msg // pointer to Court.msgChan — single source of truth
	logger       *slog.Logger
	notify       internal.NotifyFunc
	prompts      chan *Prompt
	tasks        chan *Task
	publish      func(key storage.EdictKey, eventType storage.CourtEvent, payload storage.JSON) uint // routes events through Court when set
	toolRegistry *tools.ToolRegistry                                                                 // central tool registry with permission classifications
	getMinister  func(string) Minister                                                               // minister lookup injected by Court

	zhengmingMu         sync.Mutex
	onZhengmingRaised   func()
	onZhengmingResolved func()

	sessionMu sync.RWMutex
	sessions  map[string]*Session // Per-channel sessions: "chancellor", "e633", "i456", etc.

	username string
	project  string

	// persister is attached to interactive sessions when they are created
	// in ProcessPrompt (and in the minister-specific RestoreSession /
	// brewWithStreaming paths). Ephemeral ritual-task sessions never get
	// it set and skip storage.
	persister SessionPersister

	// Hooks for unified processTask
	preTaskHook   PreTaskHook
	postTaskHook  PostTaskHook
	taskFallback  TaskFallback
	ctxMiddleware func(context.Context) context.Context // wraps ctx before streaming (e.g., Chancellor's failure accumulator)

	// promptPreprocessor transforms a prompt before streaming.
	// Set by ministers that need edict-specific preprocessing (e.g., Chancellor).
	// nil = no preprocessing.
	promptPreprocessor func(key storage.EdictKey, message string) string

	// getRitualSummaries returns formatted ritual summaries for the scratchpad.
	// Injected by the Court for ministers that need ritual context.
	getRitualSummaries func() string

	// getSkills returns formatted skills index for a given minister ID.
	// Injected by the Court. Returns empty string when no skills match.
	getSkills func(ministerID string) string

	self Minister // concrete minister, set by RunLoop for session creation
}

// NewMinisterBase creates a base for all ministers with shared dependencies.
func NewMinisterBase(db *gorm.DB, runner runners.Runner, logger *slog.Logger, username string, project string, msgChan *chan<- runners.Msg) *MinisterBase {
	if logger == nil {
		logger = slog.Default()
	}
	return &MinisterBase{
		db:       db,
		runner:   runner,
		logger:   logger,
		msgChan:  msgChan,
		prompts:  make(chan *Prompt),
		tasks:    make(chan *Task, 10),
		username: username,
		project:  project,
		sessions: make(map[string]*Session),
	}
}

// SubmitPrompt sends a prompt to the minister's channel.
func (m *MinisterBase) SubmitPrompt(p *Prompt) {
	m.prompts <- p
}

// PromptsChan returns the receive end of the prompts channel for Run() loops.
func (m *MinisterBase) PromptsChan() <-chan *Prompt {
	return m.prompts
}

// Tasks returns the channel for submitting Tasks.
func (m *MinisterBase) Tasks() chan<- *Task {
	return m.tasks
}

// RunLoop is the shared processing loop for ministers. It listens on both
// prompts (via SubmitPrompt) and tasks (from Chancellor).
// The minister parameter is the concrete Minister, used by the default prompt
// handler to create sessions. processPrompt may be nil — in that case RunLoop
// uses ProcessPrompt(minister, ...) as the default. processTask may also be nil.
func (m *MinisterBase) RunLoop(
	ctx context.Context,
	minister Minister,
	processPrompt func(context.Context, *Prompt),
	processTask func(context.Context, *Task),
) {
	m.self = minister
	if processPrompt == nil {
		processPrompt = func(ctx context.Context, p *Prompt) {
			m.ProcessPrompt(ctx, minister, p)
		}
	}
	m.logger.Info("minister started", "minister_id", m.ministerID)

	var taskWg sync.WaitGroup
	var promptWg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("minister stopping, waiting for in-flight prompts and tasks", "minister_id", m.ministerID)
			promptWg.Wait()
			taskWg.Wait()
			m.logger.Info("minister stopped", "minister_id", m.ministerID)
			return
		case prompt := <-m.PromptsChan():
			merged, mergedCancel := context.WithCancel(ctx)
			if prompt.Ctx != nil {
				context.AfterFunc(prompt.Ctx, func() { mergedCancel() })
			}
			promptWg.Add(1)
			go func() {
				defer promptWg.Done()
				defer mergedCancel()
				processPrompt(merged, prompt)
			}()
		case task := <-m.tasks:
			if processTask == nil {
				m.logger.Warn("received task but no handler", "minister_id", m.ministerID)
				continue
			}
			merged, mergedCancel := context.WithCancel(ctx)
			if task.Ctx != nil {
				context.AfterFunc(task.Ctx, func() { mergedCancel() })
			}
			taskWg.Add(1)
			go func() {
				defer taskWg.Done()
				defer mergedCancel()
				processTask(merged, task)
			}()
		}
	}
}

// ProcessPrompt is the shared prompt handler for all ministers.
// It creates a session if needed and streams the LLM response.
// If a promptPreprocessor hook is set, it transforms the prompt message
// before streaming (e.g., adding edict context prefix).
func (m *MinisterBase) ProcessPrompt(ctx context.Context, minister Minister, prompt *Prompt) {
	if m.client == nil {
		m.notify(StreamErrorMsg{ChannelID: m.ministerID, Err: fmt.Errorf("LLM not configured for %s", m.ministerID)})
		return
	}

	// Use the prompt's ChannelID for stream routing when set (e.g. when
	// the ruler prompts on a ritual tab, ChannelID is "e633" so stream
	// chunks route to the ritual tab, not the minister's own tab).
	channelID := prompt.ChannelID
	if channelID == "" {
		channelID = m.ministerID
	}

	m.sessionMu.Lock()
	sess := m.sessions[channelID]
	if sess == nil {
		var err error
		sess, err = CreateSession(minister, m.client, m.config, m.notify, channelID, prompt.EdictKey)
		if err != nil {
			m.sessionMu.Unlock()
			m.notify(StreamErrorMsg{ChannelID: channelID, Err: fmt.Errorf("failed to create session: %w", err)})
			return
		}
		sess.TabType = m.ministerID
		sess.SetPersister(m.persister)
		m.sessions[channelID] = sess
		m.logger.Info("created interactive session", "minister_id", m.ministerID, "channel_id", channelID)

		// Link the new session to the edict so future prompts on the same
		// edict tab can restore it. Only runs when a new session is created
		// AND the prompt carries an edict key (i.e. the fallback path).
		if prompt.EdictKey.ID != 0 {
			if err := m.db.Model(&storage.Edict{}).Where("id = ? AND username = ? AND project = ? AND session_id = ?",
				prompt.EdictKey.ID, prompt.EdictKey.Username, prompt.EdictKey.Project, "").
				Update("session_id", sess.ID).Error; err != nil {
				m.logger.Warn("failed to link session to edict", "edict_id", prompt.EdictKey.ID, "error", err)
			}
		}
	} else {
		// Override the session's channel so stream chunks route to the
		// prompt's target tab (e.g. a ritual tab "e633").
		sess.SetChannelID(channelID)
	}
	m.sessionMu.Unlock()
	session := sess

	message := prompt.Message
	if m.promptPreprocessor != nil {
		message = m.promptPreprocessor(prompt.EdictKey, message)
	}

	m.notify(StreamStartMsg{ChannelID: channelID, EdictID: prompt.EdictKey.ID})

	_, err := session.AskWithStreaming(ctx, message, prompt.ContextFiles)
	if err != nil && ctx.Err() == nil {
		m.notify(StreamErrorMsg{ChannelID: channelID, Err: err})
		return
	}
	m.notify(StreamDoneMsg{ChannelID: channelID})
}

// Runner returns the shell runner (may be nil)
func (m *MinisterBase) Runner() runners.Runner {
	return m.runner
}

// Logger returns the minister's logger with scoped metadata.
func (m *MinisterBase) Logger() *slog.Logger {
	if m.logger == nil {
		return slog.Default()
	}
	return m.logger
}

// Username returns the minister's username.
func (m *MinisterBase) Username() string {
	return m.username
}

// Project returns the minister's project name.
func (m *MinisterBase) Project() string {
	return m.project
}

// Scratchpad returns dynamic per-minister context. Default returns ritual
// summaries when the getRitualSummaries hook is injected, and skills
// when the getSkills hook is injected.
func (m *MinisterBase) Scratchpad() string {
	var parts []string
	if m.getRitualSummaries != nil {
		parts = append(parts, "# Available Rituals\n"+m.getRitualSummaries())
	}
	if m.getSkills != nil {
		if s := m.getSkills(m.ministerID); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

// SetRunner updates the shell runner
func (m *MinisterBase) SetRunner(r runners.Runner) {
	m.runner = r
}

// RepoInfo returns the repository information
func (m *MinisterBase) RepoInfo() repo.RepoInfo {
	return m.repoInfo
}

// SetToolRegistry sets the central tool registry for permission-based tool lookup.
func (m *MinisterBase) SetToolRegistry(registry *tools.ToolRegistry) {
	m.toolRegistry = registry
}

// GetToolRegistry returns the central tool registry.
func (m *MinisterBase) GetToolRegistry() *tools.ToolRegistry {
	return m.toolRegistry
}

// CreateSessionOpts holds optional parameters for CreateSession.
type CreateSessionOpts struct {
	EdictKey     storage.EdictKey
	ChannelID    string
	Scratchpad   string   // Pre-formatted markdown context from ritual
	ExcludeTools []string // Tool names to exclude from the session's tool list
}

// WithChannelID wraps a notify function to auto-set Session's ChannelID on first invocation.
// This allows the Session's routing target to be synchronized with whoever is driving it
// (e.g., Chancellor invoking Forge should route to Chancellor's tab).
func WithChannelID(notify internal.NotifyFunc, session *Session, channelID string) internal.NotifyFunc {
	mu := sync.Once{}
	return func(msg any) {
		mu.Do(func() {
			if session != nil && session.ChannelID() == "" {
				session.SetChannelID(channelID)
			}
		})
		notify(msg)
	}
}

// CreateSession creates a session for a minister with composed system prompt.
func CreateSession(minister Minister, client LLMProvider, config *SessionConfig, notify internal.NotifyFunc, channelID string, keys ...storage.EdictKey) (*Session, error) {
	key := storage.EdictKey{}
	if len(keys) > 0 {
		key = keys[0]
	}
	systemPrompt := buildSystemPrompt(minister, config, key)
	sess, err := NewSession(client, config, minister.Tools(), nil, notify, systemPrompt, channelID)
	if err != nil {
		return nil, err
	}
	attachAtifRecorder(sess, config)
	return sess, nil
}

// CreateSessionWithOpts creates a session with extended options including given context.
func CreateSessionWithOpts(minister Minister, client LLMProvider, config *SessionConfig, notify internal.NotifyFunc, opts CreateSessionOpts) (*Session, error) {
	systemPrompt := buildSystemPrompt(minister, config, opts.EdictKey, opts.Scratchpad)
	tools := minister.Tools()
	if len(opts.ExcludeTools) > 0 {
		excludeSet := make(map[string]bool, len(opts.ExcludeTools))
		for _, name := range opts.ExcludeTools {
			excludeSet[name] = true
		}
		filtered := make([]Tool, 0, len(tools))
		for _, t := range tools {
			if !excludeSet[t.Name()] {
				filtered = append(filtered, t)
			}
		}
		tools = filtered
	}
	sess, err := NewSession(client, config, tools, nil, notify, systemPrompt, opts.ChannelID)
	if err != nil {
		return nil, err
	}
	attachAtifRecorder(sess, config)
	return sess, nil
}

// attachAtifRecorder creates and attaches an ATIF trajectory recorder to the
// session if the config has an agent name set. Non-fatal on failure. The
// recorder roots its output at the canonical project root (config.WorkingDir)
// so trajectory files land in the project's agent/ directory regardless of the
// transient OS working directory of the session.
func attachAtifRecorder(sess *Session, cfg *SessionConfig) {
	if cfg == nil || cfg.AtifAgentName == "" {
		return
	}
	recorder := atif.NewTrajectoryRecorder(cfg.AtifAgentName, sess.ID, cfg.WorkingDir)
	sess.SetAtifRecorder(recorder)
}

// buildSystemPrompt composes the system prompt by rendering the shared template
// with the minister's Role, Scratchpad, and project context from AGENTS.md.
// Optional args, when provided, is appended to the scratchpad.
func buildSystemPrompt(minister Minister, config *SessionConfig, key storage.EdictKey, args ...string) string {
	agentsFile := "AGENTS.md"
	if config != nil && config.AgentsFile != "" {
		agentsFile = config.AgentsFile
	}

	scratchpad := minister.Scratchpad()
	if len(args) > 0 && args[0] != "" {
		scratchpad += "\n\n" + args[0]
	}

	// Get repo info and build environment block
	envBlock := sessBuildEnvBlock(minister.RepoInfo(), minister.Runner())

	var buf bytes.Buffer
	ministerTmpl.Execute(&buf, map[string]string{
		"Realm":          realm,
		"Role":           minister.SystemPrompt(),
		"MinisterID":     minister.ID(),
		"Scratchpad":     scratchpad,
		"ProjectContext": readProjectContext(agentsFile, minister.RepoInfo().ProjectRoot),
		"AgentsFile":     agentsFile,
		"EnvBlock":       envBlock,
	})
	return buf.String()
}

// formatScratchpad renders a given context map as readable markdown sections.
func formatScratchpad(ctx map[string]interface{}) string {
	if len(ctx) == 0 {
		return ""
	}
	var buf bytes.Buffer
	buf.WriteString("# Given Context\n\n")

	// Sort keys for deterministic output
	keys := make([]string, 0, len(ctx))
	for k := range ctx {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		buf.WriteString("## ")
		buf.WriteString(key)
		buf.WriteString("\n\n")
		switch v := ctx[key].(type) {
		case string:
			buf.WriteString(v)
		default:
			b, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				buf.WriteString(fmt.Sprintf("%v", v))
			} else {
				buf.WriteString("```json\n")
				buf.Write(b)
				buf.WriteString("\n```")
			}
		}
		buf.WriteString("\n\n")
	}
	return buf.String()
}

// readProjectContext reads the project context file (AGENTS.md or CLAUDE.md) from the project root directory.
func readProjectContext(agentsFile, projectRoot string) string {
	if projectRoot == "" {
		slog.Error("readProjectContext called with empty project root")
		return ""
	}
	b, err := os.ReadFile(filepath.Join(projectRoot, agentsFile))
	if err != nil {
		return ""
	}
	return string(b)
}

// SetMinisterConfig updates the MinisterBase configuration for session creation.
// This allows ministers to be configured with an LLM client after initialization.
func (m *MinisterBase) SetMinisterConfig(client LLMProvider, config *SessionConfig, repoInfo repo.RepoInfo) {
	m.client = client
	m.config = config
	m.repoInfo = repoInfo
}

// SetRepoInfo updates the repo info without changing the LLM client or config.
func (m *MinisterBase) SetRepoInfo(repoInfo repo.RepoInfo) {
	m.repoInfo = repoInfo
}

// SetNotify sets the notification callback.
func (m *MinisterBase) SetNotify(notify internal.NotifyFunc) {
	m.notify = notify
}

// SetMinisterLookup sets the minister lookup function injected by the Court.
func (m *MinisterBase) SetMinisterLookup(lookup func(string) Minister) {
	m.getMinister = lookup
}

// SetRitualSummaries sets the ritual summaries hook injected by the Court.
func (m *MinisterBase) SetRitualSummaries(fn func() string) {
	m.getRitualSummaries = fn
}

// SetSkills sets the skills hook injected by the Court.
// fn receives a minister ID and returns a formatted skills index string
// (empty string when no skills match that minister).
func (m *MinisterBase) SetSkills(fn func(ministerID string) string) {
	m.getSkills = fn
}

// SetSessionPersister stores the persister and propagates it to the
// currently-held session if there is one. Future sessions pick it up
// at the call sites that wire interactive sessions (chancellor.go,
// sage.go) via base.Persister().
func (m *MinisterBase) SetSessionPersister(p SessionPersister) {
	m.persister = p
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
	for _, sess := range m.sessions {
		if sess != nil {
			sess.SetPersister(p)
		}
	}
}

// Persister returns the configured persister (nil if none was set).
func (m *MinisterBase) Persister() SessionPersister {
	return m.persister
}

// ID returns the minister's unique identifier.
func (m *MinisterBase) ID() string {
	return m.ministerID
}

// SetPreTaskHook sets the pre-task hook for unified processTask.
func (m *MinisterBase) SetPreTaskHook(h PreTaskHook) {
	m.preTaskHook = h
}

// SetPostTaskHook sets the post-task hook for unified processTask.
func (m *MinisterBase) SetPostTaskHook(h PostTaskHook) {
	m.postTaskHook = h
}

// SetTaskFallback sets the no-LLM fallback for unified processTask.
func (m *MinisterBase) SetTaskFallback(f TaskFallback) {
	m.taskFallback = f
}

// SetContextMiddleware sets a function that wraps the context before streaming.
func (m *MinisterBase) SetContextMiddleware(f func(context.Context) context.Context) {
	m.ctxMiddleware = f
}

// sendResult sends a Result to the task's Done channel (non-blocking).
func (m *MinisterBase) sendResult(task *Task, result Result) {
	select {
	case task.Done <- result:
	default:
		m.logger.Warn("done channel full, dropping result", "edict_id", task.EdictKey.ID)
	}
}

// streamTask creates a session (or reuses existing) and streams the task through the LLM.
// This is the unified version used by all ministers via MinisterBase.
// excludeTools is a list of tool names to exclude from the session (e.g. consult_minister to prevent recursion).
// TODO: Too many params. Maybe we need a `Task` type?
func (m *MinisterBase) streamTask(ctx context.Context, work string, key storage.EdictKey, scratchpad string, notify internal.NotifyFunc, existingSession *Session, channelID string, excludeTools []string) (*Session, string, error) {
	var session *Session
	var output string
	var err error

	if existingSession != nil {
		// Reuse existing session for multi-turn conversation
		session = existingSession
		sessionChannelID := channelID
		if sessionChannelID == "" {
			sessionChannelID = session.ChannelID()
		}
		if sessionChannelID == "" {
			sessionChannelID = m.ministerID
		}
		session.SetNotify(notify, sessionChannelID)
		if scratchpad != "" {
			session.SetScratchpad(scratchpad)
		}
		output, err = session.AskWithStreaming(ctx, work, nil)
		if err != nil {
			return session, "", err
		}
	} else {
		// Create new session for first invocation
		if channelID == "" {
			channelID = m.ministerID
		}
		session, err = CreateSessionWithOpts(m.self, m.client, m.config, notify, CreateSessionOpts{
			EdictKey:     key,
			ChannelID:    channelID,
			Scratchpad:   scratchpad,
			ExcludeTools: excludeTools,
		})
		if err != nil {
			return nil, "", fmt.Errorf("failed to create %s session: %w", m.ministerID, err)
		}
		output, err = session.AskWithStreaming(ctx, work, nil)
		if err != nil {
			return session, "", err
		}
	}

	m.logger.Info("task completed", "minister_id", m.ministerID)
	return session, output, nil
}

// processTask is the unified task processor for all ministers.
// It delegates to hooks set by each minister constructor.
func (m *MinisterBase) processTask(ctx context.Context, task *Task) {
	m.logger.Info("processing task", "minister_id", m.ministerID, "edict_id", task.EdictKey.ID, "work", task.Work[:min(60, len(task.Work))])

	// Allow context middleware (e.g., Chancellor's failure accumulator) to wrap ctx
	if m.ctxMiddleware != nil {
		ctx = m.ctxMiddleware(ctx)
	}

	notify := m.notify
	if task.Notify != nil {
		notify = task.Notify
	}

	// Resolve channel ID for stream routing
	channelID := task.ChannelID
	if channelID == "" && task.Session != nil {
		channelID = task.Session.ChannelID()
	}
	if channelID == "" {
		channelID = m.ministerID
	}

	// Pre-task hook
	if m.preTaskHook != nil {
		if handled, result := m.preTaskHook(ctx, task, notify); handled {
			result.MinisterID = m.ministerID
			m.sendResult(task, *result)
			return
		}
	}

	var output string
	var taskErr error
	var session *Session
	sealed := true

	if m.client != nil {
		notify(StreamStartMsg{ChannelID: channelID, EdictID: task.EdictKey.ID})
		session, output, taskErr = m.streamTask(ctx, task.Work, task.EdictKey, task.Scratchpad, notify, task.Session, task.ChannelID, task.ExcludeTools)
		notify(StreamDoneMsg{ChannelID: channelID})
	} else if m.taskFallback != nil {
		sealed, taskErr = m.taskFallback(ctx, task)
		if sealed {
			output = m.ministerID + " task complete"
		}
	} else {
		output = m.ministerID + " task acknowledged (no LLM configured)"
	}

	// Post-task hook
	if taskErr == nil && m.postTaskHook != nil {
		_, taskErr = m.postTaskHook(ctx, task, session, output)
	}

	m.sendResult(task, Result{
		MinisterID: m.ministerID,
		Sealed:     sealed,
		Output:     output,
		Session:    session,
		Err:        taskErr,
	})
}

// Model returns the minister's LLM client.
func (m *MinisterBase) Model() LLMProvider {
	return m.client
}

// GetConfig returns the minister's LLM configuration.
func (m *MinisterBase) GetConfig() internalconfig.LLMConfig {
	if m.config != nil {
		return m.config.LLM
	}
	return internalconfig.LLMConfig{}
}

// GetSession returns the session for the given channel ID.
// If channelID is empty, defaults to m.ministerID (backward compat for
// callers that don't know the channel — e.g., clearAllSchedulers).
func (m *MinisterBase) GetSession(channelID ...string) *Session {
	key := m.ministerID
	if len(channelID) > 0 && channelID[0] != "" {
		key = channelID[0]
	}
	m.sessionMu.RLock()
	defer m.sessionMu.RUnlock()
	return m.sessions[key]
}

// SetSession sets the session for the given channel ID.
// If channelID is empty, defaults to m.ministerID.
func (m *MinisterBase) SetSession(s *Session, channelID ...string) {
	key := m.ministerID
	if len(channelID) > 0 && channelID[0] != "" {
		key = channelID[0]
	}
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
	m.sessions[key] = s
}

// ResetSession clears the session for the given channel ID.
// If channelID is empty, clears the minister's own interactive session.
func (m *MinisterBase) ResetSession(channelID ...string) {
	key := m.ministerID
	if len(channelID) > 0 && channelID[0] != "" {
		key = channelID[0]
	}
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
	if sess, ok := m.sessions[key]; ok {
		sess.closeAtif()
	}
	delete(m.sessions, key)
}

// GetSessions returns a snapshot copy of all sessions across all channels.
func (m *MinisterBase) GetSessions() map[string]*Session {
	m.sessionMu.RLock()
	defer m.sessionMu.RUnlock()
	out := make(map[string]*Session, len(m.sessions))
	for k, v := range m.sessions {
		out[k] = v
	}
	return out
}

// RestoreSession creates a fully-wired interactive session and injects loaded history.
func (m *MinisterBase) RestoreSession(minister Minister, msgs []schemas.ChatMessage, channelID ...string) error {
	key := m.ministerID
	if len(channelID) > 0 && channelID[0] != "" {
		key = channelID[0]
	}
	sess, err := CreateSession(minister, m.client, m.config, m.notify, key)
	if err != nil {
		return err
	}
	sess.SetMessages(msgs)
	sess.TabType = m.ministerID
	sess.SetPersister(m.persister)
	m.SetSession(sess, key)
	return nil
}

// SetOnZhengmingRaised sets a callback invoked when RequestZhengming is called.
// The ritual runner uses this to pause the step timeout while waiting for an answer.
func (m *MinisterBase) SetOnZhengmingRaised(cb func()) {
	m.zhengmingMu.Lock()
	defer m.zhengmingMu.Unlock()
	m.onZhengmingRaised = cb
}

// fireZhengmingRaised invokes the onZhengmingRaised callback if set.
// Called by the Court's RequestZhengming implementation to pause the
// ritual step timer on the minister that requested the clarification.
func (m *MinisterBase) fireZhengmingRaised() {
	m.zhengmingMu.Lock()
	cb := m.onZhengmingRaised
	m.zhengmingMu.Unlock()
	if cb != nil {
		cb()
	}
}

// SetOnZhengmingResolved sets a callback invoked when AnswerZhengming is called.
// The ritual runner uses this to resume the step timeout after an answer is delivered.
func (m *MinisterBase) SetOnZhengmingResolved(cb func()) {
	m.zhengmingMu.Lock()
	defer m.zhengmingMu.Unlock()
	m.onZhengmingResolved = cb
}

// GenerateID creates a unique ID using SHA256.
// Exported for use by session.go envelope pattern.
func GenerateID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// IsZhengmingPending checks if there are pending clarification requests for an edict
func (m *MinisterBase) IsZhengmingPending(key storage.EdictKey) (bool, error) {
	var count int64
	err := m.db.Model(&storage.Zhengming{}).
		Where("edict_id = ? AND username = ? AND project = ? AND status = ?", key.ID, key.Username, key.Project, storage.ZhengmingPending).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("failed to check pending zhengming: %w", err)
	}
	return count > 0, nil
}

// AnswerZhengming records the answer for a zhengming request
func (m *MinisterBase) AnswerZhengming(requestID, answer string) error {
	now := time.Now()
	result := m.db.Model(&storage.Zhengming{}).
		Where("request_id = ?", requestID).
		Updates(map[string]interface{}{
			"answer":      answer,
			"status":      storage.ZhengmingAnswered,
			"answered_at": &now,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to answer zhengming: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("zhengming request not found: %s", requestID)
	}

	// Notify ritual runner so it can resume the step timeout
	m.zhengmingMu.Lock()
	cb := m.onZhengmingResolved
	m.zhengmingMu.Unlock()
	if cb != nil {
		cb()
	}

	return nil
}

// AppendToIntent appends clarification to the edict's intent
func (m *MinisterBase) AppendToIntent(key storage.EdictKey, clarification string) error {
	var edict storage.Edict
	if err := m.db.First(&edict, "id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).Error; err != nil {
		return fmt.Errorf("failed to get edict: %w", err)
	}

	newIntent := edict.Intent + "\n\n---\n**Clarification:**\n" + clarification

	result := m.db.Model(&storage.Edict{}).
		Where("id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).
		Update("intent", newIntent)
	if result.Error != nil {
		return fmt.Errorf("failed to append to intent: %w", result.Error)
	}

	// Invalidate existing seals — they were earned against the old intent
	if err := storage.NewSealService(m.db).InvalidateSeals(key); err != nil {
		return fmt.Errorf("failed to invalidate seals: %w", err)
	}
	return nil
}

// HandleZhengmingResponse processes a clarification response
func (m *MinisterBase) HandleZhengmingResponse(ctx context.Context, requestID, answer string) error {
	if err := m.AnswerZhengming(requestID, answer); err != nil {
		return fmt.Errorf("answer zhengming: %w", err)
	}

	var req storage.Zhengming
	if err := m.db.First(&req, "request_id = ? AND username = ? AND project = ?", requestID, m.username, m.project).Error; err != nil {
		return fmt.Errorf("get request: %w", err)
	}

	reqKey := storage.EdictKey{ID: req.EdictID, Username: req.Username, Project: req.Project}
	if req.EdictID != 0 {
		if err := m.AppendToIntent(reqKey, answer); err != nil {
			slog.Warn("failed to append clarification to edict", "edict_id", req.EdictID, "error", err)
		}
	}

	// Emit zhengming_answered event
	m.EmitEvent(reqKey, "zhengming_answered", storage.JSON{
		"request_id": requestID,
		"answer":     answer,
		"edict_id":   req.EdictID,
	})

	return nil
}

// EmitEvent records an event in the Tian ledger and delivers it via channel when available.
func (m *MinisterBase) EmitEvent(key storage.EdictKey, eventType storage.CourtEvent, payload storage.JSON) error {
	slog.Debug("Emitting event", "type", eventType, "edict", key.ID)
	if m.publish != nil {
		_ = m.publish(key, eventType, payload)
		return nil
	}
	// Fallback: DB-only (for tests/standalone)
	event := storage.TianEvent{
		EdictID:   key.ID,
		Username:  key.Username,
		Project:   key.Project,
		EventType: eventType,
		Payload:   payload,
	}
	if err := m.db.Create(&event).Error; err != nil {
		return fmt.Errorf("failed to emit event: %w", err)
	}
	return nil
}

// --- Minister consultation and ritual launching (implements tools.MinisterConsultant, tools.RitualLauncher) ---

// MinisterInvokingMsg notifies the user that a minister is being invoked
type MinisterInvokingMsg struct {
	ChannelID  string           `msgpack:"channel_id"`
	MinisterID string           `msgpack:"minister_id"`
	EdictKey   storage.EdictKey `msgpack:"edict_key"`
	Task       string           `msgpack:"task,omitempty"`
}

// MinisterCompletedMsg notifies the user that a minister completed its task.
// Error rides the wire as a string; decoded values reconstruct via errors.New.
type MinisterCompletedMsg struct {
	ChannelID  string           `msgpack:"-"`
	MinisterID string           `msgpack:"-"`
	EdictKey   storage.EdictKey `msgpack:"-"`
	Output     string           `msgpack:"-"`
	Sealed     bool             `msgpack:"-"`
	Error      error            `msgpack:"-"`
}

type ministerCompletedMsgWire struct {
	ChannelID  string           `msgpack:"channel_id"`
	MinisterID string           `msgpack:"minister_id"`
	EdictKey   storage.EdictKey `msgpack:"edict_key"`
	Output     string           `msgpack:"output,omitempty"`
	Sealed     bool             `msgpack:"sealed,omitempty"`
	Error      string           `msgpack:"err,omitempty"`
}

// MarshalMsgpack encodes MinisterCompletedMsg with Error as a plain string.
func (m MinisterCompletedMsg) MarshalMsgpack() ([]byte, error) {
	w := ministerCompletedMsgWire{
		ChannelID:  m.ChannelID,
		MinisterID: m.MinisterID,
		EdictKey:   m.EdictKey,
		Output:     m.Output,
		Sealed:     m.Sealed,
	}
	if m.Error != nil {
		w.Error = m.Error.Error()
	}
	return msgpack.Marshal(w)
}

// UnmarshalMsgpack decodes MinisterCompletedMsg, reviving Error via errors.New.
func (m *MinisterCompletedMsg) UnmarshalMsgpack(b []byte) error {
	var w ministerCompletedMsgWire
	if err := msgpack.Unmarshal(b, &w); err != nil {
		return err
	}
	m.ChannelID = w.ChannelID
	m.MinisterID = w.MinisterID
	m.EdictKey = w.EdictKey
	m.Output = w.Output
	m.Sealed = w.Sealed
	if w.Error != "" {
		m.Error = errors.New(w.Error)
	}
	return nil
}

// GetEdict retrieves an edict by composite key
func (m *MinisterBase) GetEdict(key storage.EdictKey) (*storage.Edict, error) {
	var edict storage.Edict
	if err := m.db.First(&edict, "id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("edict not found: %d", key.ID)
		}
		return nil, fmt.Errorf("failed to get edict: %w", err)
	}
	return &edict, nil
}

// ResumeEdict resumes the minister's work on an edict after clarification.
// It looks up the edict, builds a resume-work task, and sends it to the task channel.
func (m *MinisterBase) ResumeEdict(ctx context.Context, key storage.EdictKey, work string) {
	if key.ID == 0 || work == "" {
		return
	}
	m.logger.Info("resuming edict after zhengming", "edict_id", key.ID)

	task := &Task{
		Ctx:      ctx,
		EdictKey: key,
		Work:     work,
		Done:     make(chan Result, 1),
	}

	select {
	case m.tasks <- task:
	default:
		m.logger.Warn("task channel full", "edict_id", key.ID)
	}
}

// sessBuildEnvBlock constructs a markdown summary of the OS, shell, and key paths.
func sessBuildEnvBlock(repoInfo repo.RepoInfo, runner runners.Runner) string {
	var env strings.Builder

	goos := "unknown"
	if runner != nil {
		goos = runner.GetOS()
	}
	env.WriteString(fmt.Sprintf("- **OS:** %s\n", goos))
	env.WriteString(fmt.Sprintf("- **CWD:** project's root\n"))

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
	}
	env.WriteString(fmt.Sprintf("- **Shell:** %s\n", shell))

	if runner != nil && runner.RunnerType() == "host" {
		env.WriteString("- **Sandbox:** none (commands run directly on host)\n")
	}

	if repoInfo.Branch != "" {
		env.WriteString(fmt.Sprintf("- **Branch:** %s\n", repoInfo.Branch))
	}

	if repoInfo.IsWorktree && repoInfo.Branch != "dev" {
		env.WriteString(
			`\n\n**IMPORTANT:** Working on worktree so commits will be quashed.
Feel free to commit whenever you can summarize the changes in a meaningful commit message.`)
	}

	return env.String()
}
