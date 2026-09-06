package court

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal"
	"github.com/afittestide/asimi/internal/atif"
	internalconfig "github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/vmihailenco/msgpack/v5"
)

// atifRecorder is satisfied by *atif.TrajectoryRecorder. We use a local
// interface aliasing the atif package to avoid coupling the court
// package to the atif serialization types while still matching methods.
type atifRecorder = atif.AtifRecorder

// atifRecorderMessage is an alias for the atif recorder message type.
type atifRecorderMessage = atif.RecorderMessage

// atifRecorderContentBlock is an alias for the atif content block type.
type atifRecorderContentBlock = atif.RecorderContentBlock

// atifRecorderUsage is an alias for the atif usage type.
type atifRecorderUsage = atif.RecorderUsage

// atifRecorderCost is an alias for the atif cost type.
type atifRecorderCost = atif.RecorderCost

// atifRecorderToolResult is an alias for the atif tool result type.
type atifRecorderToolResult = atif.RecorderToolResult

// SessionPersister durably stores a Session. The court calls it as
// messages are added so :resume sees the conversation in near-real time.
// Implementations must be non-blocking: SaveSession is invoked from the
// streaming goroutine and must not stall it (the in-tree implementation
// queues onto a worker channel).
type SessionPersister interface {
	SaveSession(*Session)
}

func strPtr(s string) *string { return &s }
func textContent(s string) *schemas.ChatMessageContent {
	return &schemas.ChatMessageContent{ContentStr: &s}
}

// bifrostErrorToGoError extracts a non-nil error from a *schemas.BifrostError.
// Bifrost's ErrorField.Error is `error` tagged json:"-", so when the failure
// comes back from an HTTP response only ErrorField.Message is populated.
// Returning bifrostErr.Error.Error directly would silently drop the failure.
func bifrostErrorToGoError(be *schemas.BifrostError) error {
	if be == nil {
		return nil
	}
	if be.Error != nil {
		if be.Error.Error != nil {
			return be.Error.Error
		}
		if be.Error.Message != "" {
			return fmt.Errorf("%s", be.Error.Message)
		}
	}
	return fmt.Errorf("bifrost error: %s", be.String())
}

// responseChoice holds the result of an LLM generation
type responseChoice struct {
	Content          string
	ReasoningContent string
	StopReason       string
	ToolCalls        []schemas.ChatAssistantMessageToolCall
	PromptTokens     int // actual prompt token count from provider (0 if unavailable)
	CompletionTokens int // actual completion token count from provider (0 if unavailable)

	CacheRead  int // prompt cache-read tokens from provider (0 if unavailable)
	CacheWrite int // prompt cache-write tokens from provider (0 if unavailable)
	Reasoning  int // reasoning/output token count from provider (0 if unavailable)
	InputCost  float64
	OutputCost float64
	TotalCost  float64
}

// --- Stream notification message types ---
//
// These carry across the RPC wire as MessagePack notifications. Field
// tags match the canonical JSON-ish on-wire names; keep them stable.

// StreamChunkMsg contains a streaming text chunk from the LLM
type StreamChunkMsg struct {
	ChannelID          string  `msgpack:"channel_id"`
	Text               string  `msgpack:"text"`
	Reasoning          string  `msgpack:"reasoning,omitempty"`
	PercentContextUsed float64 `msgpack:"percent_context_used,omitempty"`
}

// StreamStartMsg signals that streaming has begun
type StreamStartMsg struct {
	ChannelID string `msgpack:"channel_id"`
	EdictID   uint   `msgpack:"edict_id,omitempty"`
}

// StreamCompleteMsg signals that streaming has completed successfully
type StreamCompleteMsg struct {
	ChannelID string `msgpack:"channel_id"`
}

// StreamInterruptedMsg signals that streaming was interrupted
type StreamInterruptedMsg struct {
	ChannelID      string `msgpack:"channel_id"`
	PartialContent string `msgpack:"partial_content,omitempty"`
}

// StreamErrorMsg signals an error during streaming. Err crosses the wire
// as a string; decoded values reconstruct a simple errors.New error.
// PartialContent carries any text accumulated before the failure.
type StreamErrorMsg struct {
	ChannelID      string `msgpack:"-"`
	Err            error  `msgpack:"-"`
	PartialContent string `msgpack:"-"`
}

type streamErrorMsgWire struct {
	ChannelID      string `msgpack:"channel_id"`
	Err            string `msgpack:"err,omitempty"`
	PartialContent string `msgpack:"partial_content,omitempty"`
}

// MarshalMsgpack encodes StreamErrorMsg with Err as a plain string.
func (s StreamErrorMsg) MarshalMsgpack() ([]byte, error) {
	w := streamErrorMsgWire{
		ChannelID:      s.ChannelID,
		PartialContent: s.PartialContent,
	}
	if s.Err != nil {
		w.Err = s.Err.Error()
	}
	return msgpack.Marshal(w)
}

// UnmarshalMsgpack decodes StreamErrorMsg, reconstructing Err via errors.New.
func (s *StreamErrorMsg) UnmarshalMsgpack(b []byte) error {
	var w streamErrorMsgWire
	if err := msgpack.Unmarshal(b, &w); err != nil {
		return err
	}
	s.ChannelID = w.ChannelID
	s.PartialContent = w.PartialContent
	if w.Err != "" {
		s.Err = errors.New(w.Err)
	}
	return nil
}

// StreamMaxTokensReachedMsg signals that the response was truncated due to token limit
type StreamMaxTokensReachedMsg struct {
	ChannelID string `msgpack:"channel_id"`
	Content   string `msgpack:"content,omitempty"`
}

// SessionConfig holds configuration for minister sessions
type SessionConfig struct {
	LLM           internalconfig.LLMConfig
	AgentsFile    string
	Sandbox       internalconfig.SandboxConfig
	WorkingDir    string
	AtifAgentName string // non-empty enables ATIF trajectory recording
}

// Session represents a chat session for a minister
type Session struct {
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	LastUpdated time.Time `json:"last_updated"`
	FirstPrompt string    `json:"first_prompt"`
	Provider    string    `json:"provider"`
	Model       string    `json:"model"`
	WorkingDir  string    `json:"working_dir"`
	ProjectSlug string    `json:"project_slug,omitempty"`
	TabType     string    `json:"tab_type,omitempty"`

	// ReasoningEffort, when non-empty, sets the reasoning.effort parameter on
	// outgoing chat requests ("low" | "medium" | "high"). Set by ritual steps
	// via their `effort:` key. Cleared automatically is not done here — callers
	// own the lifecycle (per-step, per-turn, etc.).
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	model  LLMProvider // LLM client (implements ChatCompletionRequest/ChatCompletionStreamRequest)
	config *internalconfig.LLMConfig

	// Per-session model context-size memo. Holds the resolved window for the
	// current provider:model (including registry/default resolutions) so the
	// TUI's per-render GetContextInfo() never re-probes bifrost on each
	// keystroke. Unlike the package-level modelContextByKey cache, a registry
	// or "guessed" default is stored here and never shared across sessions;
	// a model change displaces it. See getModelContextSize.
	ctxSizeKey   string
	ctxSize      int
	ctxSizeSet   bool
	atifRecorder atifRecorder // ATIF trajectory recorder (non-nil when --atif is set)
	tools        []Tool
	messages     []schemas.ChatMessage
	notify       internal.NotifyFunc
	systemPrompt string
	channelID    string

	// Tool execution
	toolCatalog map[string]Tool
	toolDefs    []schemas.ChatTool
	scheduler   *runners.CoreToolScheduler

	// Streaming
	accumulatedContent strings.Builder

	// Loop detection
	lastToolCallKey         string
	toolCallRepetitionCount int
	toolCallLoopHits        int

	// MessageCount is the number of messages (used for display in session listings)
	MessageCount int `json:"message_count,omitempty"`

	// PersistedMsgCount tracks how many messages have been persisted to DB.
	// Used by the incremental upsert save strategy to avoid re-inserting
	// messages that are already stored, and to prevent data loss when a
	// save fails mid-way.
	PersistedMsgCount int `json:"-"`

	// Context files - dynamically added via @ references
	ContextFiles map[string]string `json:"context_files"`

	// Transient scratchpad — injected once into the next user message and
	// cleared. Never persisted. Used by the Court to carry structured data
	// (e.g. precedent feedback) across turns without baking it into the
	// immutable system prompt.
	scratchpad string

	// Write protection - track files read during session
	filesRead map[string]bool

	// Token counts - updated when messages/context changes
	systemPromptTokens int
	systemToolsTokens  int
	memoryFilesTokens  int
	messagesTokens     int

	// Provider-reported token usage (from last LLM response)
	lastPromptTokens int // total input tokens reported by provider

	// Session timing
	startTime time.Time

	// persister, if set, receives the session after every message append.
	// Sessions without a TabType never persist; see persist().
	// Implementations are expected to snapshot synchronously — the
	// streaming goroutine is the sole writer of messages, so reading
	// inside SaveSession before returning avoids races with later appends.
	persister SessionPersister
}

// NewSession creates a new minister session
func NewSession(
	model LLMProvider,
	cfg *SessionConfig,
	tools []Tool,
	scheduler *runners.CoreToolScheduler,
	notify internal.NotifyFunc,
	systemPrompt string,
	channelID string,
) (*Session, error) {
	now := time.Now()
	workingDir := ""
	if cfg != nil && cfg.WorkingDir != "" {
		workingDir = cfg.WorkingDir
	}

	session := &Session{
		ID:           GenerateID("session", now.String()),
		CreatedAt:    now,
		LastUpdated:  now,
		WorkingDir:   workingDir,
		model:        model,
		tools:        tools,
		messages:     []schemas.ChatMessage{},
		notify:       notify,
		systemPrompt: systemPrompt,
		channelID:    channelID,
		toolCatalog:  make(map[string]Tool),
		ContextFiles: make(map[string]string),
		filesRead:    make(map[string]bool),
		startTime:    now,
	}

	// Set config
	if cfg != nil {
		session.config = &cfg.LLM
		session.Provider = cfg.LLM.Provider
		session.Model = cfg.LLM.Model
		// The resolved reasoning effort (CLI flag > env var > config > default)
		// becomes the session's default. Ritual-level `effort:` overrides (set on
		// the session per step) take precedence for the steps that declare one.
		session.ReasoningEffort = cfg.LLM.ReasoningEffort
	} else {
		session.config = &internalconfig.LLMConfig{}
	}
	if session.config.MaxTurns <= 0 {
		session.config.MaxTurns = 999
	}

	// Add system prompt as first message
	if systemPrompt != "" {
		session.messages = append(session.messages, schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleSystem,
			Content: textContent(systemPrompt),
		})
	}

	// Build tool catalog and definitions
	session.toolDefs, session.toolCatalog = buildLLMTools(tools)

	// Use provided scheduler or create a new one
	if scheduler != nil {
		session.scheduler = scheduler
		session.scheduler.SetNotify(notify, channelID)
	} else {
		session.scheduler = runners.NewCoreToolScheduler(notify)
		session.scheduler.SetNotify(notify, channelID)
	}

	// Initialize token counts
	session.updateTokenCounts()

	return session, nil
}

// GetModel returns the LLM client for this session
func (s *Session) GetModel() LLMProvider {
	return s.model
}

// Messages returns the session messages
func (s *Session) Messages() []schemas.ChatMessage {
	return s.messages
}

// SetMessages replaces the session messages (used when loading from storage).
// Does not trigger persist — restored sessions shouldn't immediately write
// themselves back to the DB.
func (s *Session) SetMessages(msgs []schemas.ChatMessage) {
	s.messages = msgs
}

// SetPersister attaches a persister; subsequent message appends will
// trigger a save. Call after SetMessages on restore so the load itself
// doesn't generate a write.
func (s *Session) SetPersister(p SessionPersister) {
	s.persister = p
}

// SetAtifRecorder attaches an ATIF trajectory recorder. The recorder is
// started when the session is created and closed on session teardown.
func (s *Session) SetAtifRecorder(r atifRecorder) {
	s.atifRecorder = r
	if r != nil {
		r.Start()
	}
}

// UpdateModel sets the session's provider/model. When the model actually
// changes (or is first set after a recorder is attached) it records an
// ATIF model_change so downstream consumers observe the switch.
func (s *Session) UpdateModel(provider, modelID string) {
	if s.Provider != provider || s.Model != modelID {
		s.Provider = provider
		s.Model = modelID
		// The resolved context window is keyed by provider:model; a change must
		// displace the stale per-session memo so the next lookup resolves fresh.
		s.ctxSizeSet = false
		s.atifModelChanged(provider, modelID)
	}
}

// UpdateReasoningEffort sets the session's reasoning-effort level and
// records an ATIF thinking_level_change when the value changes.
func (s *Session) UpdateReasoningEffort(level string) {
	if level != "" && level != s.ReasoningEffort {
		s.ReasoningEffort = level
		s.atifThinkingLevelChanged(level)
	}
}

// Closeat recordings if attached.
func (s *Session) closeAtif() {
	if s.atifRecorder != nil {
		s.atifRecorder.Close()
		s.atifRecorder = nil
	}
}

// persist asks the persister to save the session. Called from the
// streaming goroutine after every message-list mutation. Sessions with
// no persister attached (ephemeral ritual tasks, sage diff reviews,
// forge/judge inner task sessions) silently skip; the call sites in
// chancellor.go, sage.go, and MinisterBase.ProcessPrompt are the only
// places that attach one.
func (s *Session) persist() {
	if s.persister == nil {
		return
	}
	s.LastUpdated = time.Now()
	if s.FirstPrompt == "" {
		s.FirstPrompt = s.ExtractFirstPrompt()
	}
	s.persister.SaveSession(s)
}

// SetNotify updates the session's notify function and the scheduler's notify
func (s *Session) SetNotify(notify internal.NotifyFunc, channelID string) {
	s.notify = notify
	if channelID != "" {
		s.channelID = channelID
	}
	if s.scheduler != nil {
		s.scheduler.SetNotify(notify, s.channelID)
	}
}

// ChannelID returns the routing destination for stream messages
func (s *Session) ChannelID() string {
	return s.channelID
}

// SetChannelID updates the routing destination for stream messages
func (s *Session) SetChannelID(channelID string) {
	s.channelID = channelID
}

// AddMessage adds a message to the session
func (s *Session) AddMessage(role schemas.ChatMessageRole, content string) {
	s.messages = append(s.messages, schemas.ChatMessage{
		Role:    role,
		Content: textContent(content),
	})
	s.LastUpdated = time.Now()
	s.persist()
}

// RegisterCourtTools adds court-specific tools to the session's tool catalog.
func (s *Session) RegisterCourtTools(tools []Tool) {
	for _, tool := range tools {
		s.toolCatalog[tool.Name()] = tool
		desc := tool.Description()

		var params *schemas.ToolFunctionParameters
		paramSchema := tool.ParameterSchema()
		if paramSchema != nil {
			data, err := json.Marshal(paramSchema)
			if err == nil {
				params = &schemas.ToolFunctionParameters{}
				json.Unmarshal(data, params)
			}
		}

		s.toolDefs = append(s.toolDefs, schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        tool.Name(),
				Description: &desc,
				Parameters:  params,
			},
		})
	}
}

// AddTools adds court tools to the session
func (s *Session) AddTools(tools []Tool) {
	slog.Info("adding court tools to session", "count", len(tools))
	s.RegisterCourtTools(tools)
	slog.Info("session tools after adding", "toolDefs", len(s.toolDefs), "toolCatalog", len(s.toolCatalog))
}

// --- Context Files Management ---

// AddContextFile adds file content to the context for the next prompt
func (s *Session) AddContextFile(path, content string) {
	s.ContextFiles[path] = content
	s.updateTokenCounts()
}

// ClearContext removes all dynamically added file content from the context
func (s *Session) ClearContext() {
	s.ContextFiles = make(map[string]string)
	s.updateTokenCounts()
}

// HasContextFiles returns true if there are files in the context
func (s *Session) HasContextFiles() bool {
	return len(s.ContextFiles) > 0
}

// GetContextFiles returns a copy of the context files map
func (s *Session) GetContextFiles() map[string]string {
	result := make(map[string]string)
	for k, v := range s.ContextFiles {
		result[k] = v
	}
	return result
}

// --- Session Persistence Helpers ---

// HasUserContent returns true if the session contains any Human or AI messages.
// Used to skip saving empty sessions.
func (s *Session) HasUserContent() bool {
	for _, msg := range s.messages {
		if msg.Role == schemas.ChatMessageRoleUser || msg.Role == schemas.ChatMessageRoleAssistant {
			return true
		}
	}
	return false
}

// ExtractFirstPrompt returns the text of the first Human message,
// truncated to 100 characters with "..." appended if longer.
func (s *Session) ExtractFirstPrompt() string {
	for _, msg := range s.messages {
		if msg.Role == schemas.ChatMessageRoleUser {
			if msg.Content != nil && msg.Content.ContentStr != nil {
				return *msg.Content.ContentStr
			}
		}
	}
	return ""
}

// --- Write Protection ---

// MarkFileAsRead records that a file has been read during this session
func (s *Session) MarkFileAsRead(path string) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	s.filesRead[absPath] = true
	slog.Debug("file marked as read", "path", absPath)
}

// HasFileBeenRead checks if a file has been read during this session
func (s *Session) HasFileBeenRead(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	return s.filesRead[absPath]
}

// CanWriteFile checks if a file can be written based on session rules:
// - File does not exist (new file)
// - OR file was read earlier in this session
func (s *Session) CanWriteFile(path string) (bool, string) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Sprintf("cannot resolve path: %v", err)
	}

	// Check if file exists
	_, err = os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, ""
		}
		return false, fmt.Sprintf("cannot check file status: %v", err)
	}

	// File exists - check if it was read in this session
	if s.HasFileBeenRead(absPath) {
		return true, ""
	}

	return false, fmt.Sprintf("file '%s' already exists and was not read in this session. Use read_file first to review the existing content", filepath.Base(path))
}

// --- Rollback Support ---

// GetMessageSnapshot returns the current size of the message history for rollback purposes
func (s *Session) GetMessageSnapshot() int {
	return len(s.messages)
}

// RollbackTo truncates the message history back to the provided snapshot index
func (s *Session) RollbackTo(snapshot int) {
	if snapshot < 1 {
		snapshot = 1 // always preserve the system prompt
	}
	if snapshot > len(s.messages) {
		snapshot = len(s.messages)
	}
	if snapshot < len(s.messages) {
		s.messages = s.messages[:snapshot]
		s.updateTokenCounts()
	}

	// Reset tool loop detection state when rolling back
	s.lastToolCallKey = ""
	s.toolCallRepetitionCount = 0
	s.toolCallLoopHits = 0
}

// Rollback discards all messages except the system prompt, keeping token counts updated
func (s *Session) Rollback() {
	s.RollbackTo(1)
}

// ClearHistory clears the conversation history but keeps the system message
func (s *Session) ClearHistory() {
	if len(s.messages) > 0 && s.messages[0].Role == schemas.ChatMessageRoleSystem {
		s.messages = s.messages[:1]
	} else {
		s.messages = []schemas.ChatMessage{}
	}
	s.lastToolCallKey = ""
	s.toolCallRepetitionCount = 0
	s.toolCallLoopHits = 0
	s.updateTokenCounts()
	s.startTime = time.Now()
	s.ClearContext()
}

// --- Token Counting ---

const (
	contextBarWidth          = 10
	autocompactBufferRatio   = 0.225
	memoryFileOverheadTokens = 20
	defaultUnknownContextRef = 8192
)

// modelContextRule pairs a compiled regexp with a context window size.
// Rules are matched in order and the first match wins.
type modelContextRule struct {
	pattern *regexp.Regexp
	size    int
}

// modelContextSizes maps a "<provider>:<model>" key to a context window size.
// Rules are matched in order and the first match wins, so per-model custom
// sizes must be listed before their family general size, and family sizes
// before the provider-level fallback. OpenRouter models carry their own base
// provider inside the model half ("provider/model"), while direct providers
// use the bare model name; the optional provider prefix keeps one rule able to
// match a family across both delivery routes.
var modelContextSizes = []modelContextRule{
	// --- Per-model overrides (before family defaults) ---
	// Gemini 1.5 Pro: a 2M window (rest of the gemini family is 1M).
	{regexp.MustCompile(`^[^:]+:(google/)?gemini-1\.5-pro.*$`), 2_000_000},
	// Moonshot Kimi k2.6: a 262k window (rest of the kimi family is 128k).
	{regexp.MustCompile(`^[^:]+:moonshotai/kimi-k2\.6$`), 262_000},
	// OpenAI gpt-4o: 128k (below the openai default and the 1M gpt-4.1 line).
	{regexp.MustCompile(`^[^:]+:(openai/)?gpt-4o$`), 128_000},
	// DeepSeek v3.2 and R1: 128k (below the 1M deepseek-v4 line).
	{regexp.MustCompile(`^[^:]+:deepseek/(deepseek-)?v3\.2$`), 128_000},
	{regexp.MustCompile(`^[^:]+:deepseek/(deepseek-)?r1$`), 128_000},
	// MiniMax (AWS Bedrock bedrock-mantle endpoint, dotted form): 196k.
	{regexp.MustCompile(`^[^:]+:minimax\.minimax-m2\.5$`), 196_000},

	// --- General family sizes (shared across providers) ---
	// DeepSeek v4 line: 1M.
	{regexp.MustCompile(`deepseek-v4`), 1_000_000},
	// Anthropic Claude: 200k.
	{regexp.MustCompile(`^[^:]+:(anthropic/)?claude-.*$`), 200_000},
	// OpenAI gpt-4.1 line: 1M.
	{regexp.MustCompile(`^[^:]+:(openai/)?gpt-4\.1.*$`), 1_000_000},
	// Google Gemini (broad family): 1M.
	{regexp.MustCompile(`^[^:]+:(google/)?gemini-.*$`), 1_000_000},
	// MiniMax via OpenRouter (slash form): 1M.
	{regexp.MustCompile(`^[^:]+:minimax/minimax-m2\.[57]$`), 1_000_000},
	{regexp.MustCompile(`^[^:]+:z-ai/glm-5\.[23]`), 1_000_000},
	{regexp.MustCompile(`^[^:]+:mistralai/.*$`), 128_000},
	{regexp.MustCompile(`^[^:]+:moonshotai/kimi-.*$`), 128_000},
	{regexp.MustCompile(`^[^:]+:qwen/qwen3\.5-397b-a17b$`), 128_000},

	// --- General provider fallbacks ---
	{regexp.MustCompile(`^anthropic:.*$`), 200_000},
	{regexp.MustCompile(`^bedrock:.*$`), 200_000},
	{regexp.MustCompile(`^openai:.*$`), 128_000},
	{regexp.MustCompile(`^openrouter:.*$`), 128_000},
	{regexp.MustCompile(`^googleai:.*$`), 1_000_000},
}

// modelMaxOutputTokens caps MaxCompletionTokens for models with provider-side
// output limits below the default. Key matches the model ID exactly.
var modelMaxOutputTokens = map[string]int{
	"minimax.minimax-m2.5": 8192,
}

// ContextInfo holds information about context usage.
type ContextInfo struct {
	Model              string
	TotalTokens        int
	UsedTokens         int
	SystemPromptTokens int
	SystemToolsTokens  int
	MemoryFilesTokens  int
	MessagesTokens     int
	FreeTokens         int
	AutocompactBuffer  int
}

// GetContextInfo returns detailed information about context usage.
func (s *Session) GetContextInfo() ContextInfo {
	info := ContextInfo{
		Model:              s.getModelName(),
		TotalTokens:        s.getModelContextSize(),
		SystemPromptTokens: s.systemPromptTokens,
		SystemToolsTokens:  s.systemToolsTokens,
		MemoryFilesTokens:  s.memoryFilesTokens,
		MessagesTokens:     s.messagesTokens,
	}

	// Use provider-reported token count when available (more accurate than estimation)
	if s.lastPromptTokens > 0 {
		info.UsedTokens = s.lastPromptTokens
	} else {
		info.UsedTokens = info.SystemPromptTokens + info.SystemToolsTokens + info.MemoryFilesTokens + info.MessagesTokens
	}

	buffer := int(math.Round(float64(info.TotalTokens) * autocompactBufferRatio))
	maxBuffer := info.TotalTokens - info.UsedTokens
	if maxBuffer < 0 {
		maxBuffer = 0
	}
	if buffer > maxBuffer {
		buffer = maxBuffer
	}
	info.AutocompactBuffer = buffer

	free := info.TotalTokens - info.UsedTokens - info.AutocompactBuffer
	if free < 0 {
		free = 0
	}
	info.FreeTokens = free

	return info
}

// getModelName returns the configured model name when available.
func (s *Session) getModelName() string {
	if s.config != nil && s.config.Model != "" {
		return s.config.Model
	}
	return "Unknown"
}

// matchContextRule returns the first rule whose pattern matches the full
// "<provider>:<model>" key, or 0 when no rule matches.
func matchContextRule(rules []modelContextRule, key string) int {
	for _, r := range rules {
		if r.pattern.MatchString(key) {
			return r.size
		}
	}
	return 0
}

// modelContextByKey caches context window sizes resolved from bifrost,
// keyed "provider:model". Lazily populated on first miss from the regex
// registry. Shared across all sessions — one network call per model.
var modelContextByKey sync.Map // map[string]int

// resolveContextSizeFromBifrost queries the LLM provider for the configured
// model's context length and returns the resolved int. Best-effort: on any
// error, empty result, or model without a resolvable context window, it
// returns defaultUnknownContextRef.
func (s *Session) resolveContextSizeFromBifrost(provider, modelName string) int {
	if s.model == nil || s.config == nil || s.config.Provider == "" || s.config.Model == "" {
		return defaultUnknownContextRef
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	prov := schemas.ModelProvider(asimiProviderToBifrostCourt(strings.ToLower(provider)))
	resp, bifrostErr := s.model.ListModelsRequest(ctx, &schemas.BifrostListModelsRequest{
		Provider: prov,
	})
	if bifrostErr != nil {
		slog.Debug("failed to list models from bifrost", "error", bifrostErrorToGoError(bifrostErr))
		return defaultUnknownContextRef
	}
	if resp == nil {
		return defaultUnknownContextRef
	}

	for _, m := range resp.Data {
		if m.ID != modelName {
			continue
		}
		if m.ContextLength != nil && *m.ContextLength > 0 {
			return *m.ContextLength
		}
		if m.MaxInputTokens != nil && m.MaxOutputTokens != nil &&
			*m.MaxInputTokens > 0 && *m.MaxOutputTokens > 0 {
			return *m.MaxInputTokens + *m.MaxOutputTokens
		}
	}
	return defaultUnknownContextRef
}

// getModelContextSize returns the context window size for the current model.
// The resolved size (including provider defaults) lives in modelContextSizes;
// a provider:model key lets one general family rule serve many delivery routes.
// The regex registry is the primary deterministic fast path; bifrost only
// supplements known models with a shared, lazy per-provider:model cache.
func (s *Session) getModelContextSize() int {
	modelName := strings.ToLower(s.getModelName())

	// Strip routing tag (":nitro", ":free") before matching model rules.
	if idx := strings.Index(modelName, ":"); idx > 0 {
		modelName = modelName[:idx]
	}

	var provider string
	if s.config != nil {
		provider = strings.ToLower(s.config.Provider)
	}
	key := provider + ":" + modelName

	// Per-session fast path: the window was already resolved for this exact
	// provider:model during this session's lifetime. This is what keeps the
	// TUI's per-render GetContextInfo() from re-probing bifrost on keystrokes.
	if s.ctxSizeSet && s.ctxSizeKey == key {
		return s.ctxSize
	}

	// Shared lazy path: one network lookup per provider:model, stored at
	// package level for all sessions. Only genuine bifrost-reported values
	// are stored here — never a registry guess or a fabricated default.
	if size, ok := modelContextByKey.Load(key); ok {
		s.rememberContextSize(key, size.(int))
		return size.(int)
	}

	// Deterministic registry first — the primary fast path, no I/O. Trying
	// the registry before bifrost keeps a known model's first resolution free
	// of a network round-trip that previously happened on every UI render.
	if size := matchContextRule(modelContextSizes, key); size > 0 {
		slog.Info("Using context size from registry", "size", size)
		s.rememberContextSize(key, size)
		return size
	}

	// Lazy bifrost probe, only for models the registry does not cover.
	if size := s.resolveContextSizeFromBifrost(provider, modelName); size != defaultUnknownContextRef {
		modelContextByKey.Store(key, size) // real bifrost value, safe to share
		s.rememberContextSize(key, size)
		return size
	}

	// Unresolvable. A "guessed" default must never enter the shared package
	// cache (a later session would inherit a fabricated value), but we still
	// memoize it per-session so the render loop doesn't re-probe bifrost on
	// every keystroke.
	slog.Warn("context window unknown; not guessing", "provider", provider, "model", modelName)
	s.rememberContextSize(key, defaultUnknownContextRef)
	return defaultUnknownContextRef
}

// rememberContextSize memoizes the resolved window size for the active
// provider:model in the per-session cache. Only the current key is kept; a
// provider/model change naturally displaces the stale entry.
func (s *Session) rememberContextSize(key string, size int) {
	s.ctxSizeKey = key
	s.ctxSize = size
	s.ctxSizeSet = true
}

// updateTokenCounts recalculates and stores token counts for all context components
func (s *Session) updateTokenCounts() {
	s.systemPromptTokens = s.countSystemPromptTokens()
	s.systemToolsTokens = s.countSystemToolsTokens()
	s.memoryFilesTokens = s.countMemoryFilesTokens()
	s.messagesTokens = s.countMessagesTokens()
}

// countSystemPromptTokens counts tokens in the system prompt.
func (s *Session) countSystemPromptTokens() int {
	if len(s.messages) == 0 {
		return 0
	}
	if s.messages[0].Role != schemas.ChatMessageRoleSystem {
		return 0
	}

	if s.messages[0].Content != nil && s.messages[0].Content.ContentStr != nil {
		return s.countTokens(*s.messages[0].Content.ContentStr)
	}
	return 0
}

// countSystemToolsTokens counts tokens in tool definitions.
func (s *Session) countSystemToolsTokens() int {
	if len(s.toolDefs) == 0 {
		return 0
	}
	toolsJSON, err := json.Marshal(s.toolDefs)
	if err != nil {
		return 0
	}
	return s.countTokens(string(toolsJSON))
}

// countMemoryFilesTokens counts tokens in dynamically added context files.
func (s *Session) countMemoryFilesTokens() int {
	if len(s.ContextFiles) == 0 {
		return 0
	}
	totalTokens := 0
	for path, content := range s.ContextFiles {
		totalTokens += s.countTokens(path)
		totalTokens += s.countTokens(content)
		totalTokens += memoryFileOverheadTokens
	}
	return totalTokens
}

// countMessagesTokens counts tokens in conversation history (excluding the system message).
func (s *Session) countMessagesTokens() int {
	if len(s.messages) <= 1 {
		return 0
	}
	totalTokens := 0
	for i := 1; i < len(s.messages); i++ {
		msg := s.messages[i]
		if msg.Content != nil && msg.Content.ContentStr != nil {
			totalTokens += s.countTokens(*msg.Content.ContentStr)
		}
		if msg.ChatAssistantMessage != nil {
			for _, tc := range msg.ChatAssistantMessage.ToolCalls {
				if tc.Function.Name != nil {
					totalTokens += s.countTokens(*tc.Function.Name)
				}
				totalTokens += s.countTokens(tc.Function.Arguments)
			}
		}
		if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil {
			totalTokens += s.countTokens(*msg.ChatToolMessage.ToolCallID)
		}
	}
	return totalTokens
}

// countTokens provides a simple word-based token estimation.
func (s *Session) countTokens(text string) int {
	if text == "" {
		return 0
	}
	return len(strings.Fields(text)) * 4 / 3
}

// GetContextUsagePercent returns the percentage of context used (0-100)
func (s *Session) GetContextUsagePercent() float64 {
	info := s.GetContextInfo()
	if info.TotalTokens <= 0 {
		return 0
	}
	return (float64(info.UsedTokens) / float64(info.TotalTokens)) * 100
}

// --- History Compaction ---

// CompactHistory summarizes the conversation history to reduce context usage
func (s *Session) CompactHistory(ctx context.Context, compactPrompt string) (string, error) {
	if len(s.messages) <= 2 {
		return "", fmt.Errorf("not enough conversation history to compact")
	}

	// Build the content to summarize
	var contentBuilder strings.Builder

	contentBuilder.WriteString("## File Changes and Diffs\n\n")
	fileChanges := s.extractFileChanges()
	if len(fileChanges) > 0 {
		for path, changes := range fileChanges {
			contentBuilder.WriteString(fmt.Sprintf("### %s\n\n", path))
			for _, change := range changes {
				contentBuilder.WriteString(change)
				contentBuilder.WriteString("\n\n")
			}
		}
	} else {
		contentBuilder.WriteString("No file changes recorded.\n\n")
	}

	contentBuilder.WriteString("## Conversation History\n\n")
	for i := 1; i < len(s.messages); i++ {
		msg := s.messages[i]
		switch msg.Role {
		case schemas.ChatMessageRoleUser:
			contentBuilder.WriteString("**User:**\n")
			if msg.Content != nil && msg.Content.ContentStr != nil {
				contentBuilder.WriteString(*msg.Content.ContentStr)
				contentBuilder.WriteString("\n\n")
			}
		case schemas.ChatMessageRoleAssistant:
			contentBuilder.WriteString("**Assistant:**\n")
			if msg.Content != nil && msg.Content.ContentStr != nil {
				contentBuilder.WriteString(*msg.Content.ContentStr)
				contentBuilder.WriteString("\n\n")
			}
		}
	}

	fullPrompt := fmt.Sprintf("%s\n\n---\n\n%s", compactPrompt, contentBuilder.String())

	originalMessages := s.messages
	systemMessage := s.messages[0]

	s.messages = []schemas.ChatMessage{
		systemMessage,
		{
			Role:    schemas.ChatMessageRoleUser,
			Content: textContent(fullPrompt),
		},
	}

	choice, err := s.generateLLMResponse(ctx, false)
	if err != nil {
		s.messages = originalMessages
		s.updateTokenCounts()
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	summary := choice.Content
	if choice.ReasoningContent != "" {
		summary = choice.ReasoningContent + "\n\n" + choice.Content
	}

	s.messages = []schemas.ChatMessage{
		systemMessage,
		{
			Role:    schemas.ChatMessageRoleUser,
			Content: textContent("Previous conversation summary:\n\n" + summary),
		},
		{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: textContent("I understand. I have the context from the previous conversation and am ready to continue."),
		},
	}

	s.lastToolCallKey = ""
	s.toolCallRepetitionCount = 0
	s.toolCallLoopHits = 0
	s.updateTokenCounts()

	return summary, nil
}

// extractFileChanges extracts all file changes from tool call responses
func (s *Session) extractFileChanges() map[string][]string {
	changes := make(map[string][]string)

	for _, msg := range s.messages {
		if msg.Role != schemas.ChatMessageRoleTool {
			continue
		}

		if msg.Content != nil && msg.Content.ContentStr != nil {
			content := *msg.Content.ContentStr
			if strings.Contains(content, "Successfully") || strings.Contains(content, "wrote") {
				lines := strings.Split(content, "\n")
				for _, line := range lines {
					if strings.Contains(line, "Successfully") || strings.Contains(line, "wrote") {
						changes["file-changes"] = append(changes["file-changes"], content)
						break
					}
				}
			}
		}
	}

	return changes
}

// --- Session Duration ---

// GetSessionDuration returns the duration since the session started
func (s *Session) GetSessionDuration() time.Duration {
	return time.Since(s.startTime)
}

// --- Export Support ---

// GetID returns the session ID (for ExportableSession interface)
func (s *Session) GetID() string {
	return s.ID
}

// GetMessages returns the session messages (for ExportableSession interface)
func (s *Session) GetMessages() []schemas.ChatMessage {
	return s.messages
}

// FormatMetadata returns formatted metadata for export (for ExportableSession interface)
// Note: exportType is a string here to avoid circular import with main package
func (s *Session) FormatMetadata(exportType, exportedAt string) string {
	var b strings.Builder
	exported := exportedAt

	b.WriteString(fmt.Sprintf("**Export Type:** %s\n", exportType))
	b.WriteString(fmt.Sprintf("**Session ID:** %s | **Working Directory:** %s\n", s.ID, s.WorkingDir))
	b.WriteString(fmt.Sprintf("**Provider:** %s | **Model:** %s\n", s.Provider, s.Model))
	b.WriteString(fmt.Sprintf("**Created:** %s | **Last Updated:** %s | **Exported:** %s\n",
		s.CreatedAt.Format("2006-01-02 15:04:05"),
		s.LastUpdated.Format("2006-01-02 15:04:05"),
		exported))
	if s.ProjectSlug != "" {
		b.WriteString(fmt.Sprintf("**Project:** %s\n", s.ProjectSlug))
	}

	return b.String()
}

// --- Streaming Support ---

// resetStreamBuffer safely resets the accumulated content buffer
func (s *Session) resetStreamBuffer() {
	s.accumulatedContent.Reset()
}

// getStreamBuffer returns the current accumulated content
func (s *Session) getStreamBuffer() string {
	return s.accumulatedContent.String()
}

// --- Message Preparation ---

// sanitizeMessages removes any trailing assistant messages with tool calls
// that don't have corresponding tool responses, and any trailing tool
// responses without a preceding assistant tool-call message. This prevents
// API errors when the agent is interrupted mid-execution.
func (s *Session) sanitizeMessages() {
	if s.config != nil && s.config.DisableContextSanitization {
		return
	}

	for len(s.messages) > 0 {
		lastIdx := len(s.messages) - 1
		lastMsg := s.messages[lastIdx]

		// Remove trailing assistant messages that carry tool calls
		if lastMsg.Role == schemas.ChatMessageRoleAssistant && lastMsg.ChatAssistantMessage != nil && len(lastMsg.ChatAssistantMessage.ToolCalls) > 0 {
			slog.Debug("removing unmatched tool call from context")
			s.messages = s.messages[:lastIdx]
			continue
		}

		// Remove trailing tool responses that are orphaned
		if lastMsg.Role == schemas.ChatMessageRoleTool {
			if lastIdx == 0 {
				slog.Debug("removing tool result without prior messages")
				s.messages = s.messages[:lastIdx]
				continue
			}

			// Look backwards past other tool messages to find the AI message with tool calls
			var aiMsg *schemas.ChatMessage
			for i := lastIdx - 1; i >= 0; i-- {
				if s.messages[i].Role == schemas.ChatMessageRoleAssistant {
					aiMsg = &s.messages[i]
					break
				}
				if s.messages[i].Role != schemas.ChatMessageRoleTool {
					break
				}
			}

			if aiMsg == nil || aiMsg.ChatAssistantMessage == nil || len(aiMsg.ChatAssistantMessage.ToolCalls) == 0 {
				slog.Debug("removing tool result without prior AI tool call")
				s.messages = s.messages[:lastIdx]
				continue
			}

			// Build set of tool call IDs from the AI message
			toolCallIDs := make(map[string]struct{})
			for _, tc := range aiMsg.ChatAssistantMessage.ToolCalls {
				if tc.ID != nil && *tc.ID != "" {
					toolCallIDs[*tc.ID] = struct{}{}
				}
			}

			// Check that the tool response references one of those IDs
			valid := len(toolCallIDs) > 0
			if lastMsg.ChatToolMessage != nil && lastMsg.ChatToolMessage.ToolCallID != nil {
				if _, exists := toolCallIDs[*lastMsg.ChatToolMessage.ToolCallID]; !exists || *lastMsg.ChatToolMessage.ToolCallID == "" {
					valid = false
				}
			}

			if !valid {
				slog.Debug("removing dangling tool result from context")
				s.messages = s.messages[:lastIdx]
				continue
			}
		}

		return
	}
}

// prepareUserMessage builds the prompt with context and adds it to the message history
func (s *Session) prepareUserMessage(prompt string, contextFiles map[string]string) {
	// Before adding a new user message, remove any unmatched tool calls
	s.sanitizeMessages()

	// Reset loop detection for new user prompt
	s.toolCallRepetitionCount = 0
	s.lastToolCallKey = ""
	s.toolCallLoopHits = 0

	fullPrompt := buildPromptWithContext(prompt, contextFiles)
	if s.scratchpad != "" {
		fullPrompt = s.scratchpad + "\n\n" + fullPrompt
		s.scratchpad = ""
	}
	s.messages = append(s.messages, schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleUser,
		Content: textContent(fullPrompt),
	})
	s.persist()
}

// SetScratchpad sets the transient scratchpad buffer. Its content is injected
// once into the next user message and then cleared automatically.
func (s *Session) SetScratchpad(content string) {
	s.scratchpad = content
}

// buildPromptWithContext builds a prompt that includes all file content
func buildPromptWithContext(userPrompt string, contextFiles map[string]string) string {
	if len(contextFiles) == 0 {
		return userPrompt
	}

	var fileContents []string
	for path, content := range contextFiles {
		fileContents = append(fileContents, fmt.Sprintf("--- Context from: %s ---\n%s\n--- End of Context from: %s ---", path, content, path))
	}

	return strings.Join(fileContents, "\n\n") + "\n" + userPrompt
}

// --- LLM Response Generation ---

// generateLLMResponse calls the LLM and returns the response. When stream is
// true it takes the streaming path, emitting content and reasoning chunks via
// s.notify as deltas arrive.
func (s *Session) generateLLMResponse(ctx context.Context, stream bool) (*responseChoice, error) {
	// Remove any unmatched tool calls from context before sending to API
	s.sanitizeMessages()

	if s.model == nil {
		return nil, fmt.Errorf("LLM model not configured")
	}

	autoStr := "auto"
	maxTokens := 64000
	if cap, ok := modelMaxOutputTokens[s.Model]; ok && cap < maxTokens {
		maxTokens = cap
	}
	params := &schemas.ChatParameters{}
	if len(s.toolDefs) > 0 {
		params.Tools = s.toolDefs
		params.MaxCompletionTokens = &maxTokens
		params.ToolChoice = &schemas.ChatToolChoice{ChatToolChoiceStr: &autoStr}
	}
	if s.ReasoningEffort != "" {
		effort := s.ReasoningEffort
		params.Reasoning = &schemas.ChatReasoning{Effort: &effort}
	}

	req := &schemas.BifrostChatRequest{
		Provider: schemas.ModelProvider(s.Provider),
		Model:    s.Model,
		Input:    s.messages,
		Params:   params,
	}

	if stream {
		// Streaming path
		bifrostCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		ch, bifrostErr := s.model.ChatCompletionStreamRequest(bifrostCtx, req)
		if bifrostErr != nil {
			return nil, bifrostErrorToGoError(bifrostErr)
		}

		var content strings.Builder
		var reasoning strings.Builder
		var toolCalls []schemas.ChatAssistantMessageToolCall
		var finishReason string
		var promptTokens, completionTokens int
		var promptDetails *schemas.ChatPromptTokensDetails
		var completionDetails *schemas.ChatCompletionTokensDetails
		var reportedCost *schemas.BifrostCost
		toolCallMap := make(map[int]*schemas.ChatAssistantMessageToolCall)

	streamLoop:
		for {
			var chunk *schemas.BifrostStreamChunk
			var ok bool
			select {
			case <-ctx.Done():
				// Free the minister loop even if Bifrost never closes ch
				// (e.g., provider HTTP hang that doesn't honor context).
				return nil, ctx.Err()
			case chunk, ok = <-ch:
				if !ok {
					break streamLoop
				}
			}
			if chunk.BifrostError != nil {
				var errMsg string
				if chunk.BifrostError.Error != nil && chunk.BifrostError.Error.Error != nil {
					errMsg = chunk.BifrostError.Error.Error.Error()
				} else if chunk.BifrostError.Error != nil {
					errMsg = chunk.BifrostError.Error.Message
				} else {
					errMsg = "unknown streaming error"
				}
				return nil, fmt.Errorf("streaming error: %s", errMsg)
			}
			if chunk.BifrostChatResponse == nil || len(chunk.BifrostChatResponse.Choices) == 0 {
				// Check for usage on chunks without choices (some providers send usage separately)
				if chunk.BifrostChatResponse != nil && chunk.BifrostChatResponse.Usage != nil {
					promptTokens = chunk.BifrostChatResponse.Usage.PromptTokens
					completionTokens = chunk.BifrostChatResponse.Usage.CompletionTokens
					promptDetails = chunk.BifrostChatResponse.Usage.PromptTokensDetails
					completionDetails = chunk.BifrostChatResponse.Usage.CompletionTokensDetails
					reportedCost = chunk.BifrostChatResponse.Usage.Cost
				}
				continue
			}
			// Capture usage from any chunk that has it
			if chunk.BifrostChatResponse.Usage != nil {
				promptTokens = chunk.BifrostChatResponse.Usage.PromptTokens
				completionTokens = chunk.BifrostChatResponse.Usage.CompletionTokens
				promptDetails = chunk.BifrostChatResponse.Usage.PromptTokensDetails
				completionDetails = chunk.BifrostChatResponse.Usage.CompletionTokensDetails
				reportedCost = chunk.BifrostChatResponse.Usage.Cost
			}
			choice := chunk.BifrostChatResponse.Choices[0]
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
			if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
				continue
			}
			delta := choice.ChatStreamResponseChoice.Delta

			// Emit reasoning and content as separate fields in a single
			// StreamChunkMsg per delta for consistent ordering in the TUI.
			var chunkReasoning, chunkContent string
			if delta.Reasoning != nil && *delta.Reasoning != "" {
				reasoning.WriteString(*delta.Reasoning)
				chunkReasoning = *delta.Reasoning
			}
			if delta.Content != nil && *delta.Content != "" {
				content.WriteString(*delta.Content)
				s.accumulatedContent.WriteString(*delta.Content)
				chunkContent = *delta.Content
			}
			if (chunkReasoning != "" || chunkContent != "") && s.notify != nil {
				s.notify(StreamChunkMsg{
					ChannelID:          s.channelID,
					Reasoning:          chunkReasoning,
					Text:               chunkContent,
					PercentContextUsed: s.GetContextUsagePercent(),
				})
			}

			// Accumulate tool calls from deltas
			for _, tc := range delta.ToolCalls {
				idx := int(tc.Index)
				existing, ok := toolCallMap[idx]
				if !ok {
					newTC := schemas.ChatAssistantMessageToolCall{
						Index: tc.Index,
						ID:    tc.ID,
						Type:  tc.Type,
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					}
					toolCallMap[idx] = &newTC
				} else {
					if tc.ID != nil {
						existing.ID = tc.ID
					}
					if tc.Type != nil {
						existing.Type = tc.Type
					}
					if tc.Function.Name != nil {
						existing.Function.Name = tc.Function.Name
					}
					existing.Function.Arguments += tc.Function.Arguments
				}
			}
		}

		// Collect tool calls in order
		for i := 0; i < len(toolCallMap); i++ {
			if tc, ok := toolCallMap[i]; ok {
				toolCalls = append(toolCalls, *tc)
			}
		}

		return &responseChoice{
			Content:          content.String(),
			ReasoningContent: reasoning.String(),
			StopReason:       finishReason,
			ToolCalls:        toolCalls,
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			CacheRead:        cacheReadTokens(promptDetails),
			CacheWrite:       cacheWriteTokens(promptDetails),
			Reasoning:        reasoningTokens(completionDetails),
			InputCost:        usageCost(reportedCost, func(c *schemas.BifrostCost) float64 { return c.InputTokensCost }),
			OutputCost:       usageCost(reportedCost, func(c *schemas.BifrostCost) float64 { return c.OutputTokensCost }),
			TotalCost:        usageCost(reportedCost, func(c *schemas.BifrostCost) float64 { return c.TotalCost }),
		}, nil
	}

	// Non-streaming path
	bifrostCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	resp, bifrostErr := s.model.ChatCompletionRequest(bifrostCtx, req)
	if bifrostErr != nil {
		return nil, bifrostErrorToGoError(bifrostErr)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty response choices")
	}

	choice := resp.Choices[0]
	result := &responseChoice{}
	if choice.FinishReason != nil {
		result.StopReason = *choice.FinishReason
	}
	if choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil {
		msg := choice.ChatNonStreamResponseChoice.Message
		if msg.Content != nil && msg.Content.ContentStr != nil {
			result.Content = *msg.Content.ContentStr
		}
		if msg.ChatAssistantMessage != nil {
			result.ToolCalls = msg.ChatAssistantMessage.ToolCalls
			if msg.ChatAssistantMessage.Reasoning != nil {
				result.ReasoningContent = *msg.ChatAssistantMessage.Reasoning
			}
		}
	}
	if resp.Usage != nil {
		result.PromptTokens = resp.Usage.PromptTokens
		result.CompletionTokens = resp.Usage.CompletionTokens
		result.CacheRead = cacheReadTokens(resp.Usage.PromptTokensDetails)
		result.CacheWrite = cacheWriteTokens(resp.Usage.PromptTokensDetails)
		result.Reasoning = reasoningTokens(resp.Usage.CompletionTokensDetails)
		result.InputCost = usageCost(resp.Usage.Cost, func(c *schemas.BifrostCost) float64 { return c.InputTokensCost })
		result.OutputCost = usageCost(resp.Usage.Cost, func(c *schemas.BifrostCost) float64 { return c.OutputTokensCost })
		result.TotalCost = usageCost(resp.Usage.Cost, func(c *schemas.BifrostCost) float64 { return c.TotalCost })
	}
	return result, nil
}

// --- ATIF usage helpers ---

// usageCost returns the cost component identified by sel, or 0 when the
// provider did not report a cost breakdown.
func usageCost(c *schemas.BifrostCost, sel func(*schemas.BifrostCost) float64) float64 {
	if c == nil {
		return 0
	}
	return sel(c)
}

// cacheReadTokens extracts prompt cache-read tokens from prompt details.
func cacheReadTokens(d *schemas.ChatPromptTokensDetails) int {
	if d == nil {
		return 0
	}
	return d.CachedReadTokens
}

// cacheWriteTokens extracts prompt cache-write tokens from prompt details.
func cacheWriteTokens(d *schemas.ChatPromptTokensDetails) int {
	if d == nil {
		return 0
	}
	return d.CachedWriteTokens
}

// reasoningTokens extracts output reasoning tokens from completion details.
func reasoningTokens(d *schemas.ChatCompletionTokensDetails) int {
	if d == nil {
		return 0
	}
	return d.ReasoningTokens
}

// appendMessage adds LLM response content and tool calls to the message history
func (s *Session) appendMessage(choice *responseChoice) {
	if choice == nil {
		return
	}

	msg := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
	}

	if strings.TrimSpace(choice.Content) != "" {
		msg.Content = textContent(choice.Content)
	}

	if len(choice.ToolCalls) > 0 {
		msg.ChatAssistantMessage = &schemas.ChatAssistantMessage{
			ToolCalls: choice.ToolCalls,
		}
	}

	if msg.Content != nil || msg.ChatAssistantMessage != nil {
		s.messages = append(s.messages, msg)
		s.persist()
	}

	// Update token counts from provider-reported usage
	if choice.PromptTokens > 0 {
		s.lastPromptTokens = choice.PromptTokens
	}
}

// --- Tool Call Loop Detection ---

// getToolCallKey generates a unique key for a tool call based on name and arguments
func (s *Session) getToolCallKey(name, argsJSON string) string {
	keyString := fmt.Sprintf("%s:%s", name, argsJSON)
	hash := sha256.Sum256([]byte(keyString))
	return hex.EncodeToString(hash[:])
}

// checkToolCallLoop detects if the same tool call is being repeated
func (s *Session) checkToolCallLoop(name, argsJSON string) bool {
	const toolCallLoopThreshold = 3

	key := s.getToolCallKey(name, argsJSON)
	if s.lastToolCallKey == key {
		s.toolCallRepetitionCount++
	} else {
		s.lastToolCallKey = key
		s.toolCallRepetitionCount = 1
	}

	if s.toolCallRepetitionCount >= toolCallLoopThreshold {
		slog.Warn("tool call loop detected", "tool", name, "count", s.toolCallRepetitionCount)
		s.toolCallRepetitionCount = 0
		s.lastToolCallKey = ""
		s.toolCallLoopHits++
		return true
	}

	return false
}

// --- Tool Execution ---

// toolCallIDCounter is used to generate unique tool call IDs when provider doesn't supply one
var toolCallIDCounter int64

// ensureToolCallID returns the tool call ID if valid, or generates a synthetic one.
func ensureToolCallID(tc *schemas.ChatAssistantMessageToolCall, index int) string {
	if tc.ID != nil && *tc.ID != "" {
		return *tc.ID
	}
	toolCallIDCounter++
	syntheticID := fmt.Sprintf("synthetic_%d_%d", time.Now().UnixNano(), toolCallIDCounter)
	tc.ID = &syntheticID
	name := ""
	if tc.Function.Name != nil {
		name = *tc.Function.Name
	}
	slog.Debug("generated synthetic tool call ID", "tool", name, "synthetic_id", syntheticID)
	slog.Warn("provider returned empty tool_call_id, using synthetic ID",
		"index", index,
		"tool", name,
		"synthetic_id", syntheticID)
	return syntheticID
}

// hasToolCallResponse checks if toolMessages already contains a response for the given tool call ID
func hasToolCallResponse(toolMessages []schemas.ChatMessage, toolCallID string) bool {
	for _, msg := range toolMessages {
		if msg.Role != schemas.ChatMessageRoleTool {
			continue
		}
		if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil && *msg.ChatToolMessage.ToolCallID == toolCallID {
			return true
		}
	}
	return false
}

// executeToolCall executes a single tool call and returns the response content
func (s *Session) executeToolCall(ctx context.Context, tool Tool, toolCallID, toolName, argsJSON string) schemas.ChatMessage {
	ctx = context.WithValue(ctx, tools.SessionIDKey{}, s.ID)
	ctx = context.WithValue(ctx, tools.ChannelIDKey{}, s.channelID)
	var out string
	var callErr error

	if s.scheduler != nil {
		ch := s.scheduler.Schedule(ctx, tool, argsJSON)
		res := <-ch
		out, callErr = res.Output, res.Error
	} else {
		out, callErr = tool.Call(ctx, argsJSON)
	}

	if callErr != nil {
		return schemas.ChatMessage{
			Role:            schemas.ChatMessageRoleTool,
			Content:         textContent(fmt.Sprintf("Error: %v", callErr)),
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr(toolCallID)},
		}
	}

	// Apply centralized output sizing as safety net
	maxOutput := runners.DefaultMaxOutputSize
	if s.config != nil && s.config.MaxToolOutput > 0 {
		maxOutput = s.config.MaxToolOutput
	}
	out = TruncateOutput(out, maxOutput)

	return schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         textContent(out),
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr(toolCallID)},
	}
}

// processToolCalls handles executing tool calls and building response messages.
// Valid tool calls are dispatched concurrently using a fan-out/collect pattern,
// preserving the original message order in the result.
func (s *Session) processToolCalls(ctx context.Context, toolCalls []schemas.ChatAssistantMessageToolCall) ([]schemas.ChatMessage, bool) {
	// results[i] holds the response for toolCalls[i], or nil if skipped.
	results := make([]*schemas.ChatMessage, len(toolCalls))

	// Phase 1: Pre-validation — filter, check for loops, unknown tools, and
	// context cancellation. This must be sequential because loop detection
	// depends on stateful counters.
	type pendingCall struct {
		index int
		id    string
		tool  Tool
		args  string
	}
	var pending []pendingCall

	for i := range toolCalls {
		tc := &toolCalls[i]
		if tc.Function.Name == nil {
			continue
		}

		name := *tc.Function.Name
		if name == "" {
			slog.Debug("skipping tool call with empty name", "index", i)
			continue
		}

		ensureToolCallID(tc, i)
		argsJSON := tc.Function.Arguments

		// Check for context cancellation
		select {
		case <-ctx.Done():
			slog.Debug("context cancelled during tool execution", "completed", i, "total", len(toolCalls))

			for j := i; j < len(toolCalls); j++ {
				remainingTC := &toolCalls[j]
				if remainingTC.Function.Name == nil {
					continue
				}
				remainingID := ""
				if remainingTC.ID != nil {
					remainingID = *remainingTC.ID
				}
				if results[j] == nil {
					msg := schemas.ChatMessage{
						Role:            schemas.ChatMessageRoleTool,
						Content:         textContent("error: session aborted by user"),
						ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr(remainingID)},
					}
					results[j] = &msg
				}
			}

			return collectMessages(results), true
		default:
		}

		// Check for tool call loops
		if s.checkToolCallLoop(name, argsJSON) {
			msg := schemas.ChatMessage{
				Role:            schemas.ChatMessageRoleTool,
				Content:         textContent(fmt.Sprintf("error: tool call loop detected (intervention #%d), please try a different approach", s.toolCallLoopHits)),
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: tc.ID},
			}
			results[i] = &msg
			return collectMessages(results), false
		}

		tool, ok := s.toolCatalog[name]
		if !ok {
			msg := schemas.ChatMessage{
				Role:            schemas.ChatMessageRoleTool,
				Content:         textContent(fmt.Sprintf("error: unknown tool %q", name)),
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: tc.ID},
			}
			results[i] = &msg
			continue
		}

		pending = append(pending, pendingCall{
			index: i,
			id:    *tc.ID,
			tool:  tool,
			args:  argsJSON,
		})
	}

	// Phase 2: Concurrent dispatch — execute all validated tool calls in parallel.
	if len(pending) > 0 {
		var wg sync.WaitGroup

		for _, pc := range pending {
			wg.Add(1)
			go func(p pendingCall) {
				defer wg.Done()
				msg := s.executeToolCall(ctx, p.tool, p.id, p.tool.Name(), p.args)
				results[p.index] = &msg
				slog.Debug("Called a tool", "tool", p.tool.Name(), "args", p.args)
				slog.Debug("creating tool response message", "tool_call_id", p.id, "tool_name", p.tool.Name())
			}(pc)
		}

		wg.Wait()
	}

	return collectMessages(results), false
}

// collectMessages gathers non-nil entries from a slice of message pointers,
// preserving order.
func collectMessages(results []*schemas.ChatMessage) []schemas.ChatMessage {
	msgs := make([]schemas.ChatMessage, 0, len(results))
	for _, m := range results {
		if m != nil {
			msgs = append(msgs, *m)
		}
	}
	return msgs
}

// --- Main API Methods ---

// AskWithStreaming sends a user prompt and streams the response while blocking.
// It streams chunks to the UI via the notify callback.
func (s *Session) AskWithStreaming(ctx context.Context, prompt string, contextFiles map[string]string) (string, error) {
	s.prepareUserMessage(prompt, contextFiles)

	// ATIF: turn_start
	s.atifTurnStarted()

	// ATIF: user message_start
	s.atifMessageStarted(atifRecorderMessage{
		Role: "user",
		Content: []atifRecorderContentBlock{
			{Type: "text", Text: strPtr(prompt)},
		},
	})

	// ATIF: user message_end
	s.atifMessageEnded(atifRecorderMessage{
		Role: "user",
		Content: []atifRecorderContentBlock{
			{Type: "text", Text: strPtr(prompt)},
		},
	})

	var finalText string
	maxTurns := s.config.MaxTurns

	// ATIF: the turn that started above must close with exactly one turn_end
	// describing the last assistant message on every return path (ctx cancel,
	// error, max-turns, tool-loop-max, and normal stop). We accumulate the
	// assistant message and all tool results across the tool loop and emit
	// a single turn_end via defer so no early return leaves the turn open.
	var lastAssistant atifRecorderMessage
	var allToolResults []atifRecorderToolResult
	turnEnded := false
	defer func() {
		if !turnEnded {
			turnEnded = true
			s.atifTurnEnded(lastAssistant, allToolResults)
		}
	}()

	for i := 0; i < maxTurns; i++ {
		s.resetStreamBuffer()

		// Check for cancellation
		select {
		case <-ctx.Done():
			accumulatedText := s.getStreamBuffer()
			if s.notify != nil {
				s.notify(StreamInterruptedMsg{ChannelID: s.channelID, PartialContent: accumulatedText})
			}
			return accumulatedText, ctx.Err()
		default:
		}

		// Re-check for cancellation before making expensive LLM call
		select {
		case <-ctx.Done():
			accumulatedText := s.getStreamBuffer()
			if s.notify != nil {
				s.notify(StreamInterruptedMsg{ChannelID: s.channelID, PartialContent: accumulatedText})
			}
			return accumulatedText, ctx.Err()
		default:
		}

		// ATIF: assistant message_start (pending)
		stopReason := "pending"
		s.atifMessageStarted(atifRecorderMessage{
			Role:       "assistant",
			Provider:   s.Provider,
			Model:      s.Model,
			StopReason: stopReason,
			Usage: &atifRecorderUsage{
				Input:       0,
				Output:      0,
				TotalTokens: 0,
			},
		})

		choice, err := s.generateLLMResponse(ctx, true)
		if err != nil {
			if ctx.Err() != nil {
				accumulatedText := s.getStreamBuffer()
				if s.notify != nil {
					s.notify(StreamInterruptedMsg{ChannelID: s.channelID, PartialContent: accumulatedText})
				}
				return accumulatedText, ctx.Err()
			}
			if s.notify != nil {
				s.notify(StreamErrorMsg{ChannelID: s.channelID, Err: err})
			}
			return "", err
		}

		if choice == nil {
			slog.Error("generateLLMResponse returned nil choice with no error")
			return "", fmt.Errorf("unexpected nil response from LLM")
		}

		responseContent := s.getStreamBuffer()

		// Check if response was truncated
		if choice.StopReason == "max_tokens" {
			if s.notify != nil {
				s.notify(StreamMaxTokensReachedMsg{ChannelID: s.channelID, Content: responseContent})
			}
			s.appendMessage(choice)
			// ATIF: assistant message_end (max_tokens)
			finalMsg := s.buildAssistantMsg(choice, responseContent)
			lastAssistant = finalMsg
			s.atifMessageEnded(finalMsg)
			turnEnded = true
			s.atifTurnEnded(finalMsg, nil)
			return responseContent + "\n\n[Response truncated due to length limit]", nil
		}

		// Check if LLM returned an error stop reason
		if choice.StopReason == "error" || choice.StopReason == "content_filter" {
			err := fmt.Errorf("LLM generation error (%s/%s): stop_reason=%s", s.Provider, s.Model, choice.StopReason)
			slog.Error("LLM returned error stop reason",
				"provider", s.Provider,
				"model", s.Model,
				"stop_reason", choice.StopReason,
				"content_length", len(responseContent),
			)
			if s.notify != nil {
				s.notify(StreamErrorMsg{ChannelID: s.channelID, Err: err, PartialContent: responseContent})
			}
			// ATIF: assistant message_end (error)
			errMsg := err.Error()
			msg := s.buildAssistantMsg(choice, responseContent)
			msg.StopReason = "error"
			msg.ErrorMessage = &errMsg
			lastAssistant = msg
			s.atifMessageEnded(msg)
			turnEnded = true
			s.atifTurnEnded(msg, nil)
			return responseContent, err
		}

		if strings.TrimSpace(responseContent) != "" {
			finalText = responseContent
		}

		// Ensure tool call IDs before appending to message history
		for i := range choice.ToolCalls {
			if choice.ToolCalls[i].Function.Name != nil && *choice.ToolCalls[i].Function.Name != "" {
				ensureToolCallID(&choice.ToolCalls[i], i)
			}
		}

		// ATIF: assistant message_end
		assistantMsg := s.buildAssistantMsg(choice, responseContent)
		lastAssistant = assistantMsg
		s.atifMessageEnded(assistantMsg)

		s.appendMessage(choice)

		// Handle tool calls - if no tool calls, we're done
		if len(choice.ToolCalls) == 0 {
			lastAssistant = assistantMsg
			break
		}

		// Process tool calls
		// ATIF: report tool exec started
		var toolResults []atifRecorderToolResult
		for _, tc := range choice.ToolCalls {
			toolCallID := ""
			if tc.ID != nil {
				toolCallID = *tc.ID
			}
			toolName := ""
			if tc.Function.Name != nil {
				toolName = *tc.Function.Name
			}
			argsJSON := tc.Function.Arguments

			// ATIF: tool_execution_start
			var args any
			if argsJSON != "" {
				var parsedArgs any
				if err := json.Unmarshal([]byte(argsJSON), &parsedArgs); err == nil {
					args = parsedArgs
				} else {
					args = argsJSON
				}
			}
			s.atifToolExecutionStarted(toolCallID, toolName, args)
		}

		toolMessages, shouldReturn := s.processToolCalls(ctx, choice.ToolCalls)
		if len(toolMessages) > 0 {
			s.messages = append(s.messages, toolMessages...)
			s.persist()
		}

		// ATIF: tool_execution_end + toolResult messages for each tool call
		for _, tm := range toolMessages {
			if tm.Role != schemas.ChatMessageRoleTool {
				continue
			}
			toolCallID := ""
			if tm.ChatToolMessage != nil && tm.ChatToolMessage.ToolCallID != nil {
				toolCallID = *tm.ChatToolMessage.ToolCallID
			}

			// Find the tool name from the original tool calls
			toolName := ""
			for _, tc := range choice.ToolCalls {
				tcID := ""
				if tc.ID != nil {
					tcID = *tc.ID
				}
				if tcID == toolCallID && tc.Function.Name != nil {
					toolName = *tc.Function.Name
					break
				}
			}

			content := ""
			isError := false
			if tm.Content != nil && tm.Content.ContentStr != nil {
				content = *tm.Content.ContentStr
				if strings.HasPrefix(content, "Error:") {
					isError = true
				}
			}

			// ATIF: tool_execution_end
			s.atifToolExecutionEnded(toolCallID, toolName, content, isError)

			// ATIF: toolResult message_start + message_end
			trMsg := atifRecorderMessage{
				Role:       "toolResult",
				ToolCallID: toolCallID,
				ToolName:   toolName,
				IsError:    isError,
				Content: []atifRecorderContentBlock{
					{Type: "text", Text: strPtr(content)},
				},
			}
			s.atifMessageStarted(trMsg)
			s.atifMessageEnded(trMsg)

			toolResults = append(toolResults, atifRecorderToolResult{
				Role:       "toolResult",
				ToolCallID: toolCallID,
				ToolName:   toolName,
				Content: []atifRecorderContentBlock{
					{Type: "text", Text: strPtr(content)},
				},
				IsError:   isError,
				Timestamp: time.Now().UnixMilli(),
			})
		}

		if shouldReturn {
			allToolResults = append(allToolResults, toolResults...)
			if s.notify != nil {
				s.notify(StreamCompleteMsg{ChannelID: s.channelID})
			}
			turnEnded = true
			s.atifTurnEnded(lastAssistant, allToolResults)
			return finalText, nil
		}

		if s.toolCallLoopHits >= 3 {
			err := fmt.Errorf("tool call loop persisted after %d interventions", s.toolCallLoopHits)
			if s.notify != nil {
				s.notify(StreamErrorMsg{ChannelID: s.channelID, Err: err})
			}
			allToolResults = append(allToolResults, toolResults...)
			turnEnded = true
			s.atifTurnEnded(lastAssistant, allToolResults)
			return "", err
		}

		if len(toolMessages) > 0 {
			allToolResults = append(allToolResults, toolResults...)
			continue
		}

		break
	}

	if s.notify != nil {
		s.notify(StreamCompleteMsg{ChannelID: s.channelID})
	}

	return finalText, nil
}

// --- Helper Functions ---

// buildLLMTools returns the LLM tool definitions and a catalog by name for execution.
func buildLLMTools(tools []Tool) ([]schemas.ChatTool, map[string]Tool) {
	execCatalog := map[string]Tool{}
	defs := make([]schemas.ChatTool, 0, len(tools))

	for i := range tools {
		tool := tools[i]
		execCatalog[tool.Name()] = tool
		desc := tool.Description()

		// Convert map[string]any parameters to ToolFunctionParameters
		var params *schemas.ToolFunctionParameters
		paramSchema := tool.ParameterSchema()
		if paramSchema != nil {
			data, err := json.Marshal(paramSchema)
			if err == nil {
				params = &schemas.ToolFunctionParameters{}
				json.Unmarshal(data, params)
			}
		}

		defs = append(defs, schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        tool.Name(),
				Description: &desc,
				Parameters:  params,
			},
		})
	}

	return defs, execCatalog
}

// --- Tool Input Types (for JSON parsing) ---

// ReadFileInput represents the input for read_file tool
type ReadFileInput struct {
	Path string `json:"path"`
}

// ReadManyFilesInput represents the input for read_many_files tool
type ReadManyFilesInput struct {
	Paths []string `json:"paths"`
}

// WriteFileInput represents the input for write_file tool
type WriteFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ToolResult represents a tool execution result
type ToolResult struct {
	Output string
	Error  error
}

// --- JSON Helper ---

// JSON is a map for storing arbitrary JSON data
type JSON = map[string]any

// --- ATIF helper methods ---

// atifTurnStarted records a turn_start event if the recorder is attached.
func (s *Session) atifTurnStarted() {
	if s.atifRecorder != nil {
		s.atifRecorder.TurnStarted()
	}
}

// atifTurnEnded records a turn_end event if the recorder is attached.
func (s *Session) atifTurnEnded(msg atifRecorderMessage, toolResults []atifRecorderToolResult) {
	if s.atifRecorder != nil {
		s.atifRecorder.TurnEnded(msg, toolResults)
	}
}

// atifMessageStarted records a message_start event if the recorder is attached.
func (s *Session) atifMessageStarted(msg atifRecorderMessage) {
	if s.atifRecorder != nil {
		s.atifRecorder.MessageStarted(msg)
	}
}

// atifMessageEnded records a message_end event if the recorder is attached.
func (s *Session) atifMessageEnded(msg atifRecorderMessage) {
	if s.atifRecorder != nil {
		s.atifRecorder.MessageEnded(msg)
	}
}

// atifToolExecutionStarted records a tool_execution_start event if the recorder is attached.
func (s *Session) atifToolExecutionStarted(toolCallID, toolName string, args any) {
	if s.atifRecorder != nil {
		s.atifRecorder.ToolExecutionStarted(toolCallID, toolName, args)
	}
}

// atifToolExecutionEnded records a tool_execution_end event if the recorder is attached.
func (s *Session) atifToolExecutionEnded(toolCallID, toolName string, result string, isError bool) {
	if s.atifRecorder != nil {
		s.atifRecorder.ToolExecutionEnded(toolCallID, toolName, result, isError)
	}
}

// atifModelChanged records a provider/model switch on the recorder.
func (s *Session) atifModelChanged(provider, modelID string) {
	if s.atifRecorder != nil {
		s.atifRecorder.ModelChanged(provider, modelID)
	}
}

// atifThinkingLevelChanged records a thinking-level change on the recorder.
func (s *Session) atifThinkingLevelChanged(level string) {
	if s.atifRecorder != nil {
		s.atifRecorder.ThinkingLevelChanged(level)
	}
}

// buildAssistantMsg builds an atifRecorderMessage from a responseChoice.
func (s *Session) buildAssistantMsg(choice *responseChoice, responseContent string) atifRecorderMessage {
	msg := atifRecorderMessage{
		Role:          "assistant",
		Provider:      s.Provider,
		Model:         s.Model,
		StopReason:    choice.StopReason,
		RawStopReason: choice.StopReason,
	}

	// Content blocks
	if responseContent != "" {
		msg.Content = append(msg.Content, atifRecorderContentBlock{
			Type: "text",
			Text: strPtr(responseContent),
		})
	}
	if choice.ReasoningContent != "" {
		msg.Content = append(msg.Content, atifRecorderContentBlock{
			Type:     "thinking",
			Thinking: strPtr(choice.ReasoningContent),
		})
	}
	for _, tc := range choice.ToolCalls {
		toolCallID := ""
		toolName := ""
		if tc.ID != nil {
			toolCallID = *tc.ID
		}
		if tc.Function.Name != nil {
			toolName = *tc.Function.Name
		}
		var args any
		if tc.Function.Arguments != "" {
			var parsedArgs any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsedArgs); err == nil {
				args = parsedArgs
			} else {
				args = tc.Function.Arguments
			}
		}
		msg.Content = append(msg.Content, atifRecorderContentBlock{
			Type:       "toolCall",
			ToolCallID: toolCallID,
			ToolName:   toolName,
			Arguments:  args,
		})
	}

	// Usage
	totalTokens := choice.PromptTokens + choice.CompletionTokens
	msg.Usage = &atifRecorderUsage{
		Input:       choice.PromptTokens,
		Output:      choice.CompletionTokens,
		CacheRead:   choice.CacheRead,
		CacheWrite:  choice.CacheWrite,
		Reasoning:   choice.Reasoning,
		TotalTokens: totalTokens,
	}
	if choice.InputCost != 0 || choice.OutputCost != 0 || choice.TotalCost != 0 {
		msg.Usage.Cost = &atifRecorderCost{
			Input:  choice.InputCost,
			Output: choice.OutputCost,
			Total:  choice.TotalCost,
		}
	}

	return msg
}

func GenerateSessionID() string {
	timestamp := time.Now().Format("2006-01-02-150405")

	randomBytes := make([]byte, 4)
	crand.Read(randomBytes)
	suffix := hex.EncodeToString(randomBytes)

	return fmt.Sprintf("%s-%s", timestamp, suffix)
}

// TruncateOutput caps s at maxBytes, keeping the first and last lines with a
// "... +N lines ..." marker in the middle. Returns s unchanged if within limit.
func TruncateOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}

	return "result is too long, please improve your call"
}
