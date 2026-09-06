package court

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// defaultStepIdleTimeout is the max silence allowed during a ritual step
// before it is aborted. The timer resets on every notification from the
// minister (stream chunks, tool calls, etc.), so a minister that is
// actively producing output will never hit this limit.
const defaultStepIdleTimeout = 5 * time.Minute

//go:embed dotagents/Justfile
var dotagentsJustfile string

//go:embed dotagents/sandbox/Dockerfile
var dotagentsDockerfile string

//go:embed dotagents/sandbox/bashrc
var dotagentsBashrc string

//go:embed dotagents/sandbox/asimi_runtime.sh
var dotagentsAsimiRuntime string

// RitualStepMsg notifies the UI of ritual step progress
type RitualStepMsg struct {
	ChannelID   string `msgpack:"channel_id"`
	RitualName  string `msgpack:"ritual_name"`
	ExecutionID string `msgpack:"execution_id"`
	EdictID     uint   `msgpack:"edict_id,omitempty"`
	StepName    string `msgpack:"step_name,omitempty"`
	MinisterID  string `msgpack:"minister_id,omitempty"` // minister running the current step
	StepIndex   int    `msgpack:"step_index"`
	TotalSteps  int    `msgpack:"total_steps"`
	ForkItem    string `msgpack:"fork_item,omitempty"` // e.g. "1/3" — item index within the fork
	Status      string `msgpack:"status,omitempty"`
	Message     string `msgpack:"message,omitempty"`
	QueueLen    int    `msgpack:"queue_len,omitempty"` // number of rituals waiting (including this one), when Status=="queued"
}

// RitualState represents the current state of a ritual execution
type RitualState string

const (
	RitualStatePending    RitualState = "pending" // maybe replace with the number of seconds till activation
	RitualStateRunning    RitualState = "running"
	RitualStateStopped    RitualState = "stopped"
	RitualStateCompleted  RitualState = "completed"
	RitualStateFailed     RitualState = "failed"
	RitualStateAborted    RitualState = "aborted"
	RitualStateRecovering RitualState = "recovering" // user approved recovery, skip zhengming prompt
	RitualStateDismissed  RitualState = "dismissed"  // user chose "Pass" at recovery prompt
)

// OnFailureAction defines what happens when a step fails
type OnFailureAction string

const (
	OnFailureRetry     OnFailureAction = "retry"
	OnFailureZhengming OnFailureAction = "zhengming"
	OnFailureGoto      OnFailureAction = "goto"
	OnFailureAbort     OnFailureAction = "abort"
)

// ErrZhengmingPending is returned by runBuiltinThen when a zhengming request
// has been created and the ritual must block until the ruler answers.
var ErrZhengmingPending = errors.New("zhengming pending")

// ErrRitualPaused is returned by executeMinisterStep when the step was
// interrupted by a ruler interjection (PauseRitual). It signals that the
// ritual should retry the step from the beginning rather than treating
// the interruption as a real failure.
var ErrRitualPaused = errors.New("ritual paused for ruler interjection")

// ZhengmingAnswer carries the ruler's response to a pending zhengming request.
type ZhengmingAnswer struct {
	RequestID string
	Answer    string
	EdictID   uint
}

// getRulersError returns a user-friendly error message.
// Context cancellation errors are translated to "cancelled by ruler".
func getRulersError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled by ruler"
	}
	return err.Error()
}

// RitualDef represents a YAML-defined ritual
type RitualDef struct {
	Name        string              `yaml:"name"`
	Description string              `yaml:"description"`
	Triggers    []RitualTrigger     `yaml:"triggers,omitempty"`
	Inputs      map[string]InputDef `yaml:"inputs,omitempty"`
	Background  []string            `yaml:"background,omitempty"`  // Shared given steps that run before every execution
	OnFailure   string              `yaml:"on_failure,omitempty"`  // Default on_failure for all steps
	MaxRetries  int                 `yaml:"max_retries,omitempty"` // Default max_retries for all steps
	Steps       []RitualStep        `yaml:"steps"`
	Then        []string            `yaml:"then,omitempty"` // Ritual-level then steps (run after all steps succeed)
}

// Rituals is a list of ritual definitions.
type Rituals []*RitualDef

// RitualTrigger defines when a ritual can be invoked
type RitualTrigger struct {
	Event string `yaml:"event,omitempty"` // Event type that triggers this ritual
}

// InputDef defines an input parameter for a ritual
type InputDef struct {
	Type        string `yaml:"type"` // string, int, bool
	Required    bool   `yaml:"required,omitempty"`
	Default     string `yaml:"default,omitempty"`
	Description string `yaml:"description,omitempty"`
}

// RitualStep defines a single step in a ritual (Given → Act → Then)
type RitualStep struct {
	Name            string            `yaml:"name"`
	Minister        string            `yaml:"minister,omitempty"`          // Minister to dispatch to
	Given           []string          `yaml:"given,omitempty"`             // Given steps: "!" prefix = bash, else matched via step registry
	Act             string            `yaml:"act,omitempty"`               // The action: task text, command, or prompt
	Then            []string          `yaml:"then,omitempty"`              // Then steps: "!" prefix = bash, else matched via step registry
	Out             string            `yaml:"out,omitempty"`               // Output template rendered after then steps
	Task            string            `yaml:"task,omitempty"`              // Alias for Act (backward compat)
	OnFailure       string            `yaml:"on_failure,omitempty"`        // retry, zhengming, goto, abort
	OnFailureTarget string            `yaml:"on_failure_target,omitempty"` // Target step for goto
	MaxRetries      int               `yaml:"max_retries,omitempty"`       // Override default retries
	Scope           string            `yaml:"scope,omitempty"`             // Execution scope (e.g., "edict", "global")
	Model           string            `yaml:"model,omitempty"`             // LLM model override for this step
	Temperature     float64           `yaml:"temperature,omitempty"`       // LLM temperature override
	Effort          string            `yaml:"effort,omitempty"`            // Reasoning effort: "low", "medium" (default), "high" (provider/model must support reasoning)
	Env             map[string]string `yaml:"env,omitempty"`               // Environment variables for this step
	Fork            *ForkDef          `yaml:"fork,omitempty"`              // Fork/join parallel execution
	Work            []RitualStep      `yaml:"work,omitempty"`              // Steps to execute for each fork item
}

// ForkDef defines fork/join parallel execution
type ForkDef struct {
	Over      string `yaml:"over"`       // Output key to iterate over
	BatchSize int    `yaml:"batch_size"` // Number of concurrent workers: 0=default parallel, 1=sequential, >1=parallel
	Limit     string `yaml:"limit"`      // Max items to process (0 = unlimited)
}

// RitualRegistry stores loaded rituals
type RitualRegistry struct {
	mu      sync.RWMutex
	rituals map[string]*RitualDef
	byEvent map[string][]*RitualDef
}

// NewRitualRegistry creates a new ritual registry
func NewRitualRegistry() *RitualRegistry {
	return &RitualRegistry{
		rituals: make(map[string]*RitualDef),
		byEvent: make(map[string][]*RitualDef),
	}
}

// Register adds a ritual to the registry
func (r *RitualRegistry) Register(ritual *RitualDef) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ritual.Name == "" {
		return fmt.Errorf("ritual name is required")
	}

	r.rituals[ritual.Name] = ritual

	// Index by event triggers
	for _, trigger := range ritual.Triggers {
		if trigger.Event != "" {
			r.byEvent[trigger.Event] = append(r.byEvent[trigger.Event], ritual)
		}
	}

	return nil
}

// Get retrieves a ritual by name
func (r *RitualRegistry) Get(name string) *RitualDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rituals[name]
}

// GetByEvent retrieves all rituals triggered by an event
func (r *RitualRegistry) GetByEvent(event string) []*RitualDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byEvent[event]
}

// Clear removes all registered rituals.
func (r *RitualRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rituals = make(map[string]*RitualDef)
	r.byEvent = make(map[string][]*RitualDef)
}

// List returns all registered ritual names
func (r *RitualRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.rituals))
	for name := range r.rituals {
		names = append(names, name)
	}
	return names
}

// Summaries returns a formatted list of all rituals with their descriptions.
// Returns "None loaded\n" if the registry is empty.
func (r *RitualRegistry) Summaries() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.rituals) == 0 {
		return "None loaded\n"
	}

	var b strings.Builder
	// Sort names for deterministic output
	names := make([]string, 0, len(r.rituals))
	for name := range r.rituals {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		ritual := r.rituals[name]
		if ritual != nil {
			fmt.Fprintf(&b, "- %s: %s\n", name, ritual.Description)
		}
	}
	return b.String()
}

// ParseRitual parses YAML content into a RitualDef
func ParseRitual(content []byte) (*RitualDef, error) {
	var ritual RitualDef
	if err := yaml.Unmarshal(content, &ritual); err != nil {
		return nil, fmt.Errorf("failed to parse ritual YAML: %w", err)
	}
	return &ritual, nil
}

// ValidateRitual validates a ritual definition
func ValidateRitual(def *RitualDef) error {
	if def.Name == "" {
		return fmt.Errorf("ritual name is required")
	}

	if len(def.Steps) == 0 {
		return fmt.Errorf("ritual %q has no steps", def.Name)
	}

	// Build step name set for validation
	stepNames := make(map[string]bool)
	for _, step := range def.Steps {
		if step.Name == "" {
			return fmt.Errorf("ritual %q: step name is required", def.Name)
		}
		if stepNames[step.Name] {
			return fmt.Errorf("ritual %q: duplicate step name %q", def.Name, step.Name)
		}
		stepNames[step.Name] = true
	}

	// Validate step references
	for _, step := range def.Steps {
		// Validate on_failure_target
		onFailure := step.OnFailure
		if onFailure == "" {
			onFailure = def.OnFailure
		}
		if onFailure == "goto" && step.OnFailureTarget != "" {
			if !stepNames[step.OnFailureTarget] {
				return fmt.Errorf("ritual %q: step %q on_failure_target references unknown step %q", def.Name, step.Name, step.OnFailureTarget)
			}
		}

		// Validate fork steps vs regular steps
		if step.Fork != nil {
			// Fork step: requires work steps, no minister/act
			if len(step.Work) == 0 {
				return fmt.Errorf("ritual %q: fork step %q requires work steps", def.Name, step.Name)
			}
			if step.Fork.Over == "" {
				return fmt.Errorf("ritual %q: fork step %q requires 'over' field", def.Name, step.Name)
			}
			// Validate work steps recursively
			for i, workStep := range step.Work {
				if workStep.Name == "" {
					return fmt.Errorf("ritual %q: fork step %q work[%d] requires name", def.Name, step.Name, i)
				}
				if workStep.Minister == "" {
					return fmt.Errorf("ritual %q: fork step %q work[%d] requires minister", def.Name, step.Name, i)
				}
				if workStep.Act == "" && workStep.Task == "" {
					return fmt.Errorf("ritual %q: fork step %q work[%d] requires act or task", def.Name, step.Name, i)
				}
			}
		}
	}

	return nil
}

// LoadRitualsFromDir loads all .yaml/.yml files from a directory
func LoadRitualsFromDir(dir string) (Rituals, error) {
	var rituals []*RitualDef

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return rituals, nil // No rituals directory is OK
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read rituals directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := filepath.Ext(entry.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read ritual file %s: %w", path, err)
		}

		ritual, err := ParseRitual(content)
		if err != nil {
			return nil, fmt.Errorf("failed to parse ritual file %s: %w", path, err)
		}

		if err := ValidateRitual(ritual); err != nil {
			return nil, fmt.Errorf("invalid ritual file %s: %w", path, err)
		}

		rituals = append(rituals, ritual)
	}

	return rituals, nil
}

// RitualRunner executes rituals
type RitualRunner struct {
	registry        *RitualRegistry
	stepDefs        *StepDefRegistry
	getMinister     func(id string) Minister
	publishEvent    func(key storage.EdictKey, eventType storage.CourtEvent, payload storage.JSON) uint
	onRunnerUpgrade func(runners.Runner) // propagates runner changes back to court
	db              *gorm.DB
	runner          runners.Runner
	logger          *slog.Logger
	maxRetries      int
	repoInfo        repo.RepoInfo
	sandboxConfig   *config.SandboxConfig                                       // injected by ConfigureModel
	projectSlug     string                                                      // injected by ConfigureModel
	idleTimeout     time.Duration                                               // max silence before a step is aborted (0 = default)
	waitZhengming   func(ctx context.Context, requestID string) (string, error) // injected by Court
	isolatedHost    bool                                                        // skip sandbox steps when true
	atifAgentName   string                                                      // ATIF agent name for ritual step sessions

	// Pause/resume for ruler interjection. When the ruler prompts on a ritual
	// tab, the ritual pauses: the step context is cancelled (stopping the
	// LLM stream) but the ritual goroutine blocks on pauseChan until
	// resumeChan fires (:continue) or the parent context is cancelled (:abort).
	pauseMu     sync.Mutex
	pauseChans  map[string]chan struct{}      // keyed by channelID ("e633")
	stepCancels map[string]context.CancelFunc // keyed by channelID — cancels the active step's context
}

// NewRitualRunner creates a new ritual runner
func NewRitualRunner(
	registry *RitualRegistry,
	getMinister func(id string) Minister,
	publishEvent func(key storage.EdictKey, eventType storage.CourtEvent, payload storage.JSON) uint,
	db *gorm.DB,
	runner runners.Runner,
	logger *slog.Logger,
	repoInfo repo.RepoInfo,
) *RitualRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &RitualRunner{
		registry:     registry,
		stepDefs:     NewStepDefRegistry(),
		getMinister:  getMinister,
		publishEvent: publishEvent,
		db:           db,
		runner:       runner,
		logger:       logger,
		maxRetries:   3,
		repoInfo:     repoInfo,
	}
}

// SetConfig injects sandbox and project configuration from the court's
// SessionConfig. This is called by ConfigureModel so that the RitualRunner
// never needs to reload config from disk.
func (r *RitualRunner) SetConfig(sandboxCfg *config.SandboxConfig, projectSlug string, repoInfo repo.RepoInfo, courtCfg *config.CourtConfig) {
	r.sandboxConfig = sandboxCfg
	r.projectSlug = projectSlug
	if repoInfo.ProjectRoot != "" {
		r.repoInfo = repoInfo
	}
	if courtCfg != nil && courtCfg.StepIdleTimeout > 0 {
		r.idleTimeout = courtCfg.StepIdleTimeout
	}
}

// SetAtifAgentName sets the ATIF agent name so ritual step sessions get an
// ATIF trajectory recorder, matching the main/interactive sessions.
func (r *RitualRunner) SetAtifAgentName(name string) {
	r.atifAgentName = name
}

// waitForZhengming delegates to the Court's blocking wait.
// It pauses the ritual execution until the answer arrives.
func (r *RitualRunner) waitForZhengming(ctx context.Context, exec *RitualExecution, requestID string) (ZhengmingAnswer, error) {
	if r.waitZhengming == nil {
		return ZhengmingAnswer{}, fmt.Errorf("waitZhengming not wired on ritual runner")
	}

	exec.State = RitualStateStopped
	r.saveExecution(exec)
	r.logger.Info("ritual paused waiting for zhengming",
		"ritual", exec.RitualName,
		"step", exec.CurrentStep,
		"execution_id", exec.ID,
		"request_id", requestID)

	answerText, err := r.waitZhengming(ctx, requestID)

	exec.State = RitualStateRunning
	r.saveExecution(exec)

	if err != nil {
		return ZhengmingAnswer{}, err
	}

	r.logger.Info("ritual resumed after zhengming",
		"ritual", exec.RitualName,
		"execution_id", exec.ID,
		"request_id", requestID,
		"answer", answerText)
	return ZhengmingAnswer{RequestID: requestID, Answer: answerText}, nil
}

// RitualExecution tracks a running ritual instance
type RitualExecution struct {
	ID          string       `gorm:"primaryKey;column:id"`
	RitualName  string       `gorm:"column:ritual_name"`
	EdictID     uint         `gorm:"column:edict_id;index"`
	Username    string       `gorm:"column:username"`
	Project     string       `gorm:"column:project"`
	SessionID   string       `gorm:"column:session_id"`
	CurrentStep int          `gorm:"column:current_step"`
	State       RitualState  `gorm:"column:state"`
	Data        storage.JSON `gorm:"column:data;type:json"`
	CreatedAt   time.Time    `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time    `gorm:"column:updated_at;autoUpdateTime"`

	// Runtime (not persisted)
	def        *RitualDef
	stepStates []RitualStepState
	notify     internal.NotifyFunc

	// Recovery tracking (not persisted)
	PreviousExecutionID string `gorm:"-"` // ID of the aborted execution being recovered
	RecoveryMode        bool   `gorm:"-"` // True if this execution is a recovery

	// Fork execution context (not persisted)
	forkItemLabel string // e.g. "1/3" — set by executeForkItem, read by Notify
	forkWorkIndex int    // index of current work step within the fork
	forkWorkTotal int    // total work steps in the fork
}

// TableName returns the table name for RitualExecution
func (RitualExecution) TableName() string {
	return "ritual_executions"
}

// getLastStepOutput returns the full output of the last step that produced a message.
func getLastStepOutput(exec *RitualExecution) string {
	if out, ok := exec.Data["act_result"].(string); ok {
		return out
	}
	return ""
}

// EdictKey returns the storage.EdictKey for this execution.
func (e *RitualExecution) EdictKey() storage.EdictKey {
	return storage.EdictKey{ID: e.EdictID, Username: e.Username, Project: e.Project}
}

// ChannelID returns the streaming channel ID for this ritual's edict.
// All edicts use the "e<N>" convention matching commit message suffixes,
// including edict 1 (court infrastructure), which uses "e1".
func (e *RitualExecution) ChannelID() string {
	return fmt.Sprintf("e%d", e.EdictID)
}

// Notify sends a ritual step message, pre-filling common fields from e.
// If no notify function is set, this is a no-op.
func (e *RitualExecution) Notify(msg RitualStepMsg) {
	if e.notify != nil {
		msg.ChannelID = e.ChannelID()
		msg.RitualName = e.RitualName
		msg.ExecutionID = e.ID
		msg.EdictID = e.EdictID
		if e.forkItemLabel != "" {
			msg.ForkItem = e.forkItemLabel
			msg.TotalSteps = e.forkWorkTotal
			msg.StepIndex = e.forkWorkIndex
		} else if e.def != nil {
			msg.TotalSteps = len(e.def.Steps)
		}
		e.notify(msg)
	}
}

// notifyAny sends an arbitrary message via the notify function.
// If no notify function is set, this is a no-op.
func (e *RitualExecution) notifyAny(msg any) {
	if e.notify != nil {
		e.notify(msg)
	}
}

// RitualStepState tracks the state of a step within an execution
type RitualStepState struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	ExecutionID string `gorm:"column:execution_id;index"`
	StepIndex   int    `gorm:"column:step_index"`
	Name        string `gorm:"column:name"`
	Status      string `gorm:"column:status"` // "pending", "completed", "failed"
	SessionID   string `gorm:"column:session_id"`
	RetryCount  int    `gorm:"column:retry_count"`
	Message     string `gorm:"column:message"`

	// Runtime only (not persisted)
	Session *Session `gorm:"-"` // LLM session for multi-turn
	Output  string   `gorm:"-"` // Step output (preserved on failure)
}

// TableName returns the table name for RitualStepState
func (RitualStepState) TableName() string {
	return "ritual_step_states"
}

// Start begins execution of a ritual
func (r *RitualRunner) Start(ctx context.Context, ritualName string, key storage.EdictKey, inputs map[string]string, notify internal.NotifyFunc) (*RitualExecution, error) {
	def := r.registry.Get(ritualName)
	// Validate required inputs
	if def == nil {
		return nil, fmt.Errorf("failed to get ritual %s", ritualName)
	}
	for name, inputDef := range def.Inputs {
		if _, ok := inputs[name]; !ok {
			if inputDef.Default != "" {
				inputs[name] = inputDef.Default
			} else if inputDef.Required {
				return nil, fmt.Errorf("required input %q not provided", name)
			}
		}
	}

	// Create execution record
	exec := &RitualExecution{
		ID:          GenerateID("ritual", ritualName, fmt.Sprint(key.ID), time.Now().String()),
		RitualName:  ritualName,
		EdictID:     key.ID,
		Username:    key.Username,
		Project:     key.Project,
		CurrentStep: 0,
		State:       RitualStatePending,
		Data:        storage.JSON{"inputs": inputs},
		def:         def,
		notify:      notify,
	}

	// Check for and recover from previous aborted/paused execution
	if _, err := r.recoverFromPreviousExec(ctx, ritualName, key, def, exec); err != nil {
		return nil, err
	}

	// Initialize step states
	exec.stepStates = make([]RitualStepState, len(def.Steps))
	for i, step := range def.Steps {
		exec.stepStates[i] = RitualStepState{
			ExecutionID: exec.ID,
			StepIndex:   i,
			Name:        step.Name,
			Status:      "pending",
		}
	}

	// Save to database
	if err := r.db.Save(exec).Error; err != nil {
		return nil, fmt.Errorf("failed to create ritual execution: %w", err)
	}

	for i := range exec.stepStates {
		if err := r.db.Save(&exec.stepStates[i]).Error; err != nil {
			return nil, fmt.Errorf("failed to create step state: %w", err)
		}
	}

	if exec.RecoveryMode {
		r.logger.Info("ritual started (recovery mode)",
			"ritual", ritualName,
			"execution_id", exec.ID,
			"edict_id", key.ID,
			"from_step", exec.CurrentStep)
	} else {
		r.logger.Info("ritual started",
			"ritual", ritualName,
			"execution_id", exec.ID,
			"edict_id", key.ID)
	}

	steps := 0
	if exec.def != nil {
		steps = len(exec.def.Steps)
	}

	exec.Notify(RitualStepMsg{
		TotalSteps: steps,
		Status:     "started",
	})
	return exec, nil
}

// Run executes a ritual to completion
func (r *RitualRunner) Run(ctx context.Context, exec *RitualExecution) error {
	if exec.def == nil {
		exec.def = r.registry.Get(exec.RitualName)
		if exec.def == nil {
			return fmt.Errorf("ritual definition not found: %s", exec.RitualName)
		}
	}

	exec.State = RitualStateRunning
	r.saveExecution(exec)

	// Emit ritual_started Tian event
	execKey := exec.EdictKey()
	r.emitEvent(execKey, storage.EventRitualStarted, storage.JSON{
		"ritual":       exec.RitualName,
		"execution_id": exec.ID,
	})

	// === BACKGROUND ===
	for _, raw := range exec.def.Background {
		entry, err := r.resolveStepDef(raw)
		if err != nil {
			exec.State = RitualStateFailed
			r.saveExecution(exec)
			exec.Notify(RitualStepMsg{
				StepName: raw,
				Status:   "ritual_failed",
				Message:  getRulersError(err),
			})
			r.emitEvent(execKey, storage.EventRitualFailed, storage.JSON{
				"ritual":       exec.RitualName,
				"execution_id": exec.ID,
				"step":         raw,
				"error":        getRulersError(err),
			})
			return fmt.Errorf("background given %q failed: %w", raw, err)
		}
		callID := GenerateID("ritualcmd", exec.ID, entry.Key, raw)
		exec.notifyAny(runners.ToolCallScheduledMsg{
			ChannelID: exec.ChannelID(),
			CallID:    callID,
			ToolName:  entry.Key,
			Input:     raw,
			Formatted: raw,
		})
		result, err := r.runGivenStep(ctx, exec, entry)
		if err != nil {
			// Context cancelled (user interrupt) — the session layer already
			// sent StreamInterruptedMsg which surfaces as 🛑 ABORTED in the
			// TUI. Skip the ritual_failed notification.
			if errors.Is(err, context.Canceled) {
				exec.State = RitualStateAborted
				r.saveExecution(exec)
				return fmt.Errorf("background given %q failed: %w", raw, err)
			}
			exec.State = RitualStateFailed
			r.saveExecution(exec)
			exec.notifyAny(runners.ToolCallErrorMsg{
				ChannelID: exec.ChannelID(),
				CallID:    callID,
				ToolName:  entry.Key,
				Input:     raw,
				Error:     err.Error(),
				Formatted: raw,
			})
			exec.Notify(RitualStepMsg{
				StepName: entry.Key,
				Status:   "ritual_failed",
				Message:  getRulersError(err),
			})
			r.emitEvent(execKey, storage.EventRitualFailed, storage.JSON{
				"ritual":       exec.RitualName,
				"execution_id": exec.ID,
				"step":         entry.Key,
				"error":        getRulersError(err),
			})
			return fmt.Errorf("background given %q failed: %w", raw, err)
		}
		exec.notifyAny(runners.ToolCallSuccessMsg{
			ChannelID: exec.ChannelID(),
			CallID:    callID,
			ToolName:  entry.Key,
			Input:     raw,
			Result:    "",
			Formatted: raw,
		})
		storeGivenResult(exec, entry.Key, result)
	}

	for exec.CurrentStep < len(exec.def.Steps) {
		select {
		case <-ctx.Done():
			exec.State = RitualStateAborted
			r.saveExecution(exec)
			exec.Notify(RitualStepMsg{
				Status:  "ritual_aborted",
				Message: "aborted by user",
			})
			return ctx.Err()
		default:
		}

		// Check if paused between steps (CTRL-C when no step is running).
		if err := r.waitIfPaused(ctx, exec.ChannelID()); err != nil {
			exec.State = RitualStateAborted
			r.saveExecution(exec)
			return ctx.Err()
		}
		r.clearPause(exec.ChannelID())

		step := exec.def.Steps[exec.CurrentStep]
		result, err := r.executeStep(ctx, exec, step)
		if err != nil {
			exec.stepStates[exec.CurrentStep].Status = "failed"
			exec.stepStates[exec.CurrentStep].Message = err.Error()
			exec.stepStates[exec.CurrentStep].Output = result

			// Context cancelled (user interrupt) — the session layer already
			// sent StreamInterruptedMsg which surfaces as 🛑 ABORTED in the
			// TUI.
			if ctx.Err() != nil {
				exec.State = RitualStateAborted
				r.saveExecution(exec)
				return err
			}

			// Ritual was paused for ruler interjection and resumed via
			// :continue. Re-run the current step from scratch.
			if errors.Is(err, ErrRitualPaused) {
				exec.stepStates[exec.CurrentStep].Status = "pending"
				exec.stepStates[exec.CurrentStep].Message = ""
				continue
			}

			exec.Notify(RitualStepMsg{
				StepName:  step.Name,
				StepIndex: exec.CurrentStep,
				Status:    "failed",
				// TODO: fix this
				Message: getRulersError(err),
			})

			// Emit step_failed Tian event
			r.emitEvent(execKey, storage.EventStepFailed, storage.JSON{
				"ritual":       exec.RitualName,
				"execution_id": exec.ID,
				"step":         step.Name,
				"step_index":   exec.CurrentStep,
				"error":        err.Error(),
			})

			// Handle failure action
			if !r.handleFailure(ctx, exec, step, err) {
				exec.State = RitualStateFailed
				r.saveExecution(exec)
				exec.Notify(RitualStepMsg{
					StepName:  step.Name,
					StepIndex: exec.CurrentStep,
					Status:    "ritual_failed",
					Message:   getRulersError(err),
				})
				// Emit ritual_failed Tian event
				lastStepOutput := getLastStepOutput(exec)
				r.emitEvent(execKey, storage.EventRitualFailed, storage.JSON{
					"ritual":           exec.RitualName,
					"execution_id":     exec.ID,
					"step":             step.Name,
					"error":            getRulersError(err),
					"last_step_output": lastStepOutput,
				})
				return err
			}
			continue
		}

		// Fork steps send their own "completed" notification with a summary,
		// so skip the duplicate here.
		if step.Fork == nil {
			exec.Notify(RitualStepMsg{
				StepName:  step.Name,
				StepIndex: exec.CurrentStep,
				Status:    "completed",
			})
		}
		// Emit step_completed Tian event
		r.emitEvent(execKey, storage.EventStepCompleted, storage.JSON{
			"ritual":       exec.RitualName,
			"execution_id": exec.ID,
			"step":         step.Name,
			"step_index":   exec.CurrentStep,
		})

		// Update step state
		exec.stepStates[exec.CurrentStep].Status = "completed"
		exec.stepStates[exec.CurrentStep].Message = result

		// Move to next step (considering dependencies)
		exec.CurrentStep++
		r.saveExecution(exec)
	}

	// === RITUAL-LEVEL THEN ===
	for _, raw := range exec.def.Then {
		entry, err := r.resolveStepDef(raw)
		if err != nil {
			r.logger.Warn("ritual-level then failed to resolve", "raw", raw, "error", err)
			continue
		}
		callID := GenerateID("ritualcmd", exec.ID, entry.Key, raw)
		exec.notifyAny(runners.ToolCallScheduledMsg{
			ChannelID: exec.ChannelID(),
			CallID:    callID,
			ToolName:  entry.Key,
			Input:     raw,
			Formatted: raw,
		})
		if err := r.runThenStep(ctx, exec, entry); errors.Is(err, ErrZhengmingPending) {
			requestID, ok := exec.Data["pending_zhengming"].(string)
			if !ok || requestID == "" {
				r.logger.Error("zhengming pending but no request_id in execution data")
				continue
			}
			answer, waitErr := r.waitForZhengming(ctx, exec, requestID)
			if waitErr != nil {
				r.logger.Warn("ritual-level then zhengming wait failed", "raw", raw, "error", waitErr)
				continue
			}
			if answer.Answer == tools.AnswerReject {
				r.logger.Warn("ritual-level then zhengming rejected", "raw", raw)
				continue
			}
		} else if err != nil {
			exec.notifyAny(runners.ToolCallErrorMsg{
				ChannelID: exec.ChannelID(),
				CallID:    callID,
				ToolName:  entry.Key,
				Input:     raw,
				Error:     err.Error(),
				Formatted: raw,
			})
			r.logger.Warn("ritual-level then step failed", "raw", raw, "error", err)
			continue
		}
		exec.notifyAny(runners.ToolCallSuccessMsg{
			ChannelID: exec.ChannelID(),
			CallID:    callID,
			ToolName:  entry.Key,
			Input:     raw,
			Result:    "",
			Formatted: raw,
		})
	}

	exec.State = RitualStateCompleted
	r.saveExecution(exec)

	duration := time.Since(exec.CreatedAt).Round(time.Millisecond)

	exec.Notify(RitualStepMsg{
		StepIndex: len(exec.def.Steps),
		Status:    "ritual_completed",
		Message:   duration.String(),
	})
	// Emit ritual_completed Tian event
	lastStepOutput := getLastStepOutput(exec)
	r.emitEvent(execKey, storage.EventRitualCompleted, storage.JSON{
		"ritual":           exec.RitualName,
		"execution_id":     exec.ID,
		"last_step_output": lastStepOutput,
		"duration":         duration.String(),
	})

	r.logger.Info("ritual completed",
		"ritual", exec.RitualName,
		"execution_id", exec.ID,
		"edict_id", exec.EdictID,
		"duration", duration)

	return nil
}

// resolveOnFailure returns the step's on_failure or the ritual-level default
func (step *RitualStep) resolveOnFailure(def *RitualDef) string {
	if step.OnFailure != "" {
		return step.OnFailure
	}
	return def.OnFailure
}

// resolveMaxRetries returns the step's max_retries or the ritual-level default
func (step *RitualStep) resolveMaxRetries(def *RitualDef) int {
	if step.MaxRetries > 0 {
		return step.MaxRetries
	}
	if def.MaxRetries > 0 {
		return def.MaxRetries
	}
	return 0
}

// executeStep runs a single ritual step using the Given → Act → Then model
func (r *RitualRunner) executeStep(ctx context.Context, exec *RitualExecution, step RitualStep) (string, error) {
	execKey := exec.EdictKey()

	// === GIVEN === (runs before fork check so fork steps can load data into exec.Data)
	if len(step.Given) > 0 {
		for _, raw := range step.Given {
			entry, err := r.resolveStepDef(raw)
			if err != nil {
				return "", fmt.Errorf("given %q failed: %w", raw, err)
			}
			callID := GenerateID("ritualcmd", exec.ID, entry.Key, raw)
			exec.notifyAny(runners.ToolCallScheduledMsg{
				ChannelID: exec.ChannelID(),
				CallID:    callID,
				ToolName:  entry.Key,
				Input:     raw,
				Formatted: raw,
			})
			result, err := r.runGivenStep(ctx, exec, entry)
			if err != nil {
				// TODO: trim `raw` to 60 chars
				exec.notifyAny(runners.ToolCallErrorMsg{
					ChannelID: exec.ChannelID(),
					CallID:    callID,
					ToolName:  entry.Key,
					Input:     raw,
					Error:     err.Error(),
					Formatted: raw,
				})
				return "", fmt.Errorf("given %q failed: %w", raw, err)
			}
			exec.notifyAny(runners.ToolCallSuccessMsg{
				ChannelID: exec.ChannelID(),
				CallID:    callID,
				ToolName:  entry.Key,
				Input:     raw,
				Result:    "",
				Formatted: raw,
			})
			storeGivenResult(exec, entry.Key, result)
		}
	}

	// Check if this is a fork step
	if step.Fork != nil {
		return r.executeForkStep(ctx, exec, step)
	}

	// Check edict status before executing step - abort if sealed or cancelled
	// TODO: move to the top of the ritual, before background handling
	if exec.EdictID != 0 {
		sealService := storage.NewSealService(r.db)
		status, err := sealService.GetEdictStatus(execKey)
		if err == nil {
			if status == storage.EdictSealed || status == storage.EdictCancelled {
				r.logger.Info("aborting ritual step due to edict state change",
					"ritual", exec.RitualName,
					"step", step.Name,
					"edict_id", exec.EdictID,
					"edict_status", status)
				return "", fmt.Errorf("ritual aborted: edict %d is %s", exec.EdictID, status)
			}
		}
	}

	r.saveExecution(exec)

	r.logger.Debug("executing ritual step",
		"ritual", exec.RitualName,
		"step", step.Name,
		"minister", step.Minister)

	exec.Notify(RitualStepMsg{
		StepName:   step.Name,
		MinisterID: step.Minister,
		StepIndex:  exec.CurrentStep,
		Status:     "started",
	})
	// Emit step_started Tian event
	r.emitEvent(execKey, storage.EventStepStarted, storage.JSON{
		"ritual":       exec.RitualName,
		"execution_id": exec.ID,
		"step":         step.Name,
		"step_index":   exec.CurrentStep,
	})

	// === ACT ===
	actResult, err := r.executeMinisterStep(ctx, exec, step)
	if err != nil {
		return actResult, err
	}
	if exec.Data == nil {
		r.logger.Debug("exec.Data is nil")
		exec.Data = storage.JSON{}
	}
	r.logger.Debug("act_result", "act_result", actResult[:min(60, len(actResult))])
	exec.Data["act_result"] = actResult
	exec.Data[step.Name] = actResult

	// === THEN ===
	for _, raw := range step.Then {
		entry, err := r.resolveStepDef(raw)
		if err != nil {
			return actResult, fmt.Errorf("then %q failed: %w", raw, err)
		}
		callID := GenerateID("ritualcmd", exec.ID, entry.Key, raw)
		exec.notifyAny(runners.ToolCallScheduledMsg{
			ChannelID: exec.ChannelID(),
			CallID:    callID,
			ToolName:  entry.Key,
			Input:     raw,
			Formatted: raw,
		})
		if err := r.runThenStep(ctx, exec, entry); errors.Is(err, ErrZhengmingPending) {
			// Block until the ruler answers the zhengming
			requestID, ok := exec.Data["pending_zhengming"].(string)
			if !ok || requestID == "" {
				r.logger.Error("zhengming pending but no request_id in execution data")
				continue
			}
			answer, waitErr := r.waitForZhengming(ctx, exec, requestID)
			if waitErr != nil {
				return actResult, fmt.Errorf("then %q zhengming wait failed: %w", raw, waitErr)
			}
			if answer.Answer == tools.AnswerReject {
				return actResult, fmt.Errorf("then %q zhengming rejected by ruler", raw)
			}
		} else if err != nil {
			exec.notifyAny(runners.ToolCallErrorMsg{
				ChannelID: exec.ChannelID(),
				CallID:    callID,
				ToolName:  entry.Key,
				Input:     raw,
				Error:     err.Error(),
				Formatted: raw,
			})
			return actResult, err
		}
		exec.notifyAny(runners.ToolCallSuccessMsg{
			ChannelID: exec.ChannelID(),
			CallID:    callID,
			ToolName:  entry.Key,
			Input:     raw,
			Result:    "",
			Formatted: raw,
		})
	}

	// === OUT ===
	if step.Out != "" {
		out := r.expandTemplate(step.Out, exec)
		exec.Notify(RitualStepMsg{
			StepName:  step.Name,
			StepIndex: exec.CurrentStep,
			Status:    "info",
			Message:   out,
		})
	}

	return actResult, nil
}

// ForkResult holds the result of a single fork item execution
type ForkResult struct {
	Item     interface{}
	Output   string
	Error    error
	_unitIdx int // internal: index in workUnits list for DAG tracking
}

// executeForkStep executes a fork/join parallel step
func (r *RitualRunner) executeForkStep(ctx context.Context, exec *RitualExecution, step RitualStep) (string, error) {
	execKey := exec.EdictKey()
	r.saveExecution(exec)

	r.logger.Info("executing fork step",
		"ritual", exec.RitualName,
		"step", step.Name,
		"over", step.Fork.Over)

	// Notify: fork started
	exec.Notify(RitualStepMsg{
		StepName:  step.Name,
		StepIndex: exec.CurrentStep,
		Status:    "started",
		Message:   fmt.Sprintf("fork over %s", step.Fork.Over),
	})

	// Emit step_started Tian event
	r.emitEvent(execKey, storage.EventStepStarted, storage.JSON{
		"ritual":       exec.RitualName,
		"execution_id": exec.ID,
		"step":         step.Name,
		"step_index":   exec.CurrentStep,
		"fork":         true,
	})

	// Get the work units to iterate over
	workUnits, err := r.getForkWorkUnits(exec, step.Fork.Over)
	if err != nil {
		return "", fmt.Errorf("failed to get fork work units: %w", err)
	}

	// Apply limit if specified
	limit := 0
	if step.Fork.Limit != "" {
		limitStr := r.expandTemplate(step.Fork.Limit, exec)
		if limitStr != "" {
			limit, _ = strconv.Atoi(limitStr)
		}
	}
	if limit > 0 && len(workUnits) > limit {
		workUnits = workUnits[:limit]
	}

	r.logger.Info("fork work units",
		"total", len(workUnits),
		"limit", limit,
		"batch_size", step.Fork.BatchSize)

	// Determine execution mode based on BatchSize:
	// BatchSize==0 → default parallel (5 workers)
	// BatchSize==1 → sequential (topological order for DAG items)
	// BatchSize>1 → parallel with specified batch size
	batchSize := step.Fork.BatchSize
	if batchSize == 0 {
		batchSize = 5 // default batch size
	}
	results, err := r.executeForkDAG(ctx, exec, step, workUnits, batchSize)
	if err != nil {
		return "", err
	}

	// Store fork results in execution context
	forkOut := []ForkResult{}
	forkErr := []ForkResult{}
	for _, res := range results {
		if res.Error != nil {
			forkErr = append(forkErr, res)
		} else {
			forkOut = append(forkOut, res)
		}
	}

	if exec.Data == nil {
		exec.Data = storage.JSON{}
	}
	exec.Data["fork"] = map[string]interface{}{
		"out": forkOut,
		"err": forkErr,
	}

	// Build summary result
	summary := fmt.Sprintf("Fork completed: %d successful, %d failed", len(forkOut), len(forkErr))

	// Notify: fork completed
	exec.Notify(RitualStepMsg{
		StepName:  step.Name,
		StepIndex: exec.CurrentStep,
		Status:    "completed",
		Message:   summary,
	})

	// Emit step_completed Tian event
	r.emitEvent(execKey, storage.EventStepCompleted, storage.JSON{
		"ritual":       exec.RitualName,
		"execution_id": exec.ID,
		"step":         step.Name,
		"step_index":   exec.CurrentStep,
		"fork":         true,
		"success":      len(forkOut),
		"failed":       len(forkErr),
	})

	return summary, nil
}

// getForkWorkUnits retrieves the work units from execution context
func (r *RitualRunner) getForkWorkUnits(exec *RitualExecution, over string) ([]interface{}, error) {
	var keys []string
	if exec.Data != nil {
		for k := range exec.Data {
			keys = append(keys, k)
		}
	}
	r.logger.Debug("resolving fork work units",
		"ritual", exec.RitualName,
		"step_index", exec.CurrentStep,
		"over", over,
		"data_keys", keys)

	// Look directly in exec.Data
	if exec.Data != nil {
		if val, ok := exec.Data[over]; ok {
			r.logger.Debug("found fork work units in exec.Data",
				"ritual", exec.RitualName,
				"over", over,
				"type", fmt.Sprintf("%T", val))
			return r.toInterfaceSlice(val)
		}
		// Also check step_results
		if stepResults, ok := exec.Data["step_results"].(map[string]interface{}); ok {
			if val, ok := stepResults[over]; ok {
				r.logger.Debug("found fork work units in step_results",
					"ritual", exec.RitualName,
					"over", over,
					"type", fmt.Sprintf("%T", val))
				return r.toInterfaceSlice(val)
			}
		}
	}
	r.logger.Warn("fork work units key not found in context",
		"ritual", exec.RitualName,
		"over", over,
		"data_keys", keys)
	return nil, fmt.Errorf("work units key %q not found in context", over)
}

// toInterfaceSlice converts various slice types to []interface{}
func (r *RitualRunner) toInterfaceSlice(val interface{}) ([]interface{}, error) {
	switch v := val.(type) {
	case []interface{}:
		r.logger.Debug("converted work units from []interface{}",
			"count", len(v))
		return v, nil
	case []map[string]interface{}:
		result := make([]interface{}, len(v))
		for i, m := range v {
			result[i] = m
		}
		r.logger.Debug("converted work units from []map[string]interface{}",
			"count", len(result))
		return result, nil
	default:
		r.logger.Debug("converting work units via JSON",
			"type", fmt.Sprintf("%T", val))
		// Try to convert via JSON marshaling
		data, err := json.Marshal(val)
		if err != nil {
			return nil, fmt.Errorf("cannot convert work units: %w", err)
		}
		var result []interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("cannot unmarshal work units: %w", err)
		}
		r.logger.Debug("converted work units via JSON",
			"count", len(result))
		return result, nil
	}
}

// forkWorkUnit wraps a work unit with its extracted ID and dependency IDs.
type forkWorkUnit struct {
	Item   interface{}
	ID     string   // extracted from item["ling_id"], or auto-generated
	DepIDs []string // extracted from item["dependencies"], or empty
}

// buildDependencyMap extracts IDs and dependency lists from work units.
// Items without a "ling_id" field get auto-generated IDs.
// Items without a "dependencies" field have empty deps → immediately ready.
func buildDependencyMap(workUnits []interface{}) []forkWorkUnit {
	units := make([]forkWorkUnit, len(workUnits))
	for i, item := range workUnits {
		units[i].Item = item

		m, ok := item.(map[string]interface{})
		if !ok {
			units[i].ID = fmt.Sprintf("_fork_%d", i)
			continue
		}

		if id, ok := m["ling_id"].(string); ok && id != "" {
			units[i].ID = id
		} else {
			units[i].ID = fmt.Sprintf("_fork_%d", i)
		}

		if deps, ok := m["dependencies"]; ok {
			switch d := deps.(type) {
			case []string:
				units[i].DepIDs = d
			case []interface{}:
				for _, v := range d {
					if s, ok := v.(string); ok {
						units[i].DepIDs = append(units[i].DepIDs, s)
					}
				}
			case storage.StringArray:
				units[i].DepIDs = []string(d)
			}
		}
	}
	return units
}

// seedReadyQueue returns the indices of work units whose dependencies are all
// satisfied. A unit with no dependencies is immediately ready.
func seedReadyQueue(units []forkWorkUnit, done map[string]bool) []int {
	var ready []int
	for i, u := range units {
		if done[u.ID] {
			continue // already completed
		}
		allSatisfied := true
		for _, dep := range u.DepIDs {
			if !done[dep] {
				allSatisfied = false
				break
			}
		}
		if allSatisfied {
			ready = append(ready, i)
		}
	}
	return ready
}

// executeForkDAG executes work units respecting dependency ordering.
// Items with no dependencies are immediately ready; items with dependencies
// wait until all their deps are done (as recorded in the DB via "record the ling completed").
// Concurrency is controlled by batchSize (semaphore).
func (r *RitualRunner) executeForkDAG(ctx context.Context, exec *RitualExecution, step RitualStep, workUnits []interface{}, batchSize int) ([]ForkResult, error) {
	units := buildDependencyMap(workUnits)

	if len(units) == 0 {
		r.logger.Debug("fork DAG has no work units",
			"ritual", exec.RitualName,
			"step", step.Name)
		return nil, nil
	}

	// Build a set of IDs for quick lookup
	idSet := make(map[string]bool, len(units))
	for _, u := range units {
		idSet[u.ID] = true
		r.logger.Debug("fork DAG unit",
			"ritual", exec.RitualName,
			"step", step.Name,
			"unit_id", u.ID,
			"dependencies", u.DepIDs)
	}

	// Track completed items by ID. Seed with lings already done from DB.
	done := make(map[string]bool)
	doneBefore := len(done)
	r.queryDoneLings(exec, idSet, done)
	r.logger.Debug("fork DAG seeded done lings from DB",
		"ritual", exec.RitualName,
		"step", step.Name,
		"already_done", len(done)-doneBefore,
		"total_units", len(units))

	// Track which indices have been dispatched
	dispatched := make(map[int]bool)
	// Track number of goroutines still running
	running := 0

	var results []ForkResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, batchSize)
	resultCh := make(chan ForkResult, len(workUnits))

	// Helper: check if all deps for a unit are satisfied
	depsSatisfied := func(u forkWorkUnit) bool {
		for _, dep := range u.DepIDs {
			if !done[dep] {
				return false
			}
		}
		return true
	}

	// Helper: find ready items that haven't been dispatched yet
	getReady := func() []int {
		var ready []int
		for i, u := range units {
			if dispatched[i] || done[u.ID] {
				continue
			}
			if depsSatisfied(u) {
				ready = append(ready, i)
			}
		}
		return ready
	}

	// Helper: collect one completion from resultCh
	collectOne := func() {
		select {
		case res := <-resultCh:
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
			unitID := ""
			if res._unitIdx >= 0 && res._unitIdx < len(units) {
				unitID = units[res._unitIdx].ID
				done[unitID] = true
			}
			running--
			errStr := ""
			if res.Error != nil {
				errStr = res.Error.Error()
			}
			r.logger.Debug("fork DAG collected unit result",
				"ritual", exec.RitualName,
				"step", step.Name,
				"unit_id", unitID,
				"unit_index", res._unitIdx,
				"error", errStr,
				"running", running)
			// Re-query DB in case "record the ling completed" marked other lings done
			r.queryDoneLings(exec, idSet, done)
		case <-ctx.Done():
		}
	}

	for {
		// Find and dispatch ready items
		ready := getReady()
		for _, idx := range ready {
			// Acquire semaphore — wait for a slot if at capacity
			select {
			case sem <- struct{}{}:
				// Acquired
			case res := <-resultCh:
				// A slot freed — collect the result first
				mu.Lock()
				results = append(results, res)
				mu.Unlock()
				unitID := ""
				if res._unitIdx >= 0 && res._unitIdx < len(units) {
					unitID = units[res._unitIdx].ID
					done[unitID] = true
				}
				running--
				errStr := ""
				if res.Error != nil {
					errStr = res.Error.Error()
				}
				r.logger.Debug("fork DAG unit finished before dispatch",
					"ritual", exec.RitualName,
					"step", step.Name,
					"unit_id", unitID,
					"unit_index", res._unitIdx,
					"error", errStr,
					"running", running)
				r.queryDoneLings(exec, idSet, done)
				// Now acquire the freed slot
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					wg.Wait()
					return results, ctx.Err()
				}
			case <-ctx.Done():
				wg.Wait()
				return results, ctx.Err()
			}

			dispatched[idx] = true
			running++
			unit := units[idx]
			r.markLingInProgress(exec, unit)
			r.logger.Debug("fork DAG dispatching unit",
				"ritual", exec.RitualName,
				"step", step.Name,
				"unit_id", unit.ID,
				"unit_index", idx,
				"dependencies", unit.DepIDs,
				"running", running,
				"batch_size", batchSize)

			wg.Add(1)
			go func(u forkWorkUnit, idx int) {
				defer wg.Done()
				defer func() { <-sem }()

				result := r.executeForkItem(ctx, exec, step, u.Item, idx, len(units))
				result._unitIdx = idx

				// Re-query DB after completion
				r.queryDoneLings(exec, idSet, done)

				resultCh <- result
			}(unit, idx)
		}

		// No more ready items. If nothing is running, check terminal conditions.
		if running == 0 {
			// Check for stuck items (undispatched items with unsatisfied deps)
			var blocked []string
			for _, u := range units {
				if !done[u.ID] {
					blocked = append(blocked, u.ID)
				}
			}
			if len(blocked) > 0 {
				r.logger.Warn("fork DAG stuck with unsatisfied dependencies",
					"ritual", exec.RitualName,
					"step", step.Name,
					"blocked_count", len(blocked),
					"blocked_units", blocked,
					"done_count", len(done))
				return results, fmt.Errorf("fork stuck: %d item(s) with unsatisfied dependencies: %v", len(blocked), blocked)
			}
			break // All done
		}

		// Wait for at least one completion to potentially unlock new items
		collectOne()
	}

	wg.Wait()

	// Drain any remaining results from the buffered channel
	for {
		select {
		case res := <-resultCh:
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		default:
			return results, nil
		}
	}
}

// queryDoneLings queries the DB for lings associated with this execution
// that have status "done", and populates the done map.
func (r *RitualRunner) queryDoneLings(exec *RitualExecution, idSet map[string]bool, done map[string]bool) {
	if exec.EdictID == 0 || r.db == nil {
		return
	}
	key := exec.EdictKey()
	var lings []storage.Ling
	if err := r.db.Where("edict_id = ? AND username = ? AND project = ? AND status = ?",
		key.ID, key.Username, key.Project, storage.LingDone).
		Find(&lings).Error; err != nil {
		return
	}
	for _, l := range lings {
		if idSet[l.LingID] {
			done[l.LingID] = true
		}
	}
}

// markLingInProgress updates the ling status to in_progress when the
// DAG executor dispatches it. This is a lifecycle transition owned by
// the executor for scheduling purposes.
func (r *RitualRunner) markLingInProgress(exec *RitualExecution, unit forkWorkUnit) {
	if exec.EdictID == 0 || r.db == nil {
		return
	}
	// Skip auto-generated IDs (non-ling items)
	if strings.HasPrefix(unit.ID, "_fork_") {
		return
	}
	key := exec.EdictKey()
	r.db.Model(&storage.Ling{}).
		Where("ling_id = ? AND username = ? AND project = ? AND status = ?",
			unit.ID, key.Username, key.Project, storage.LingPending).
		Update("status", storage.LingInProgress)
}

// executeForkItem executes a single fork item
func (r *RitualRunner) executeForkItem(ctx context.Context, exec *RitualExecution, step RitualStep, item interface{}, itemIndex, totalItems int) ForkResult {
	itemID := ""
	if m, ok := item.(map[string]interface{}); ok {
		if id, ok := m["ling_id"].(string); ok {
			itemID = id
		}
	}
	r.logger.Debug("fork item starting",
		"ritual", exec.RitualName,
		"step", step.Name,
		"item_id", itemID,
		"item_index", itemIndex,
		"total_items", totalItems)

	// Create a fork-specific execution context
	forkExec := &RitualExecution{
		ID:            exec.ID,
		RitualName:    exec.RitualName,
		EdictID:       exec.EdictID,
		Username:      exec.Username,
		Project:       exec.Project,
		Data:          storage.JSON{},
		def:           exec.def,
		stepStates:    exec.stepStates,
		notify:        exec.notify,
		CurrentStep:   exec.CurrentStep,
		forkItemLabel: fmt.Sprintf("%d/%d", itemIndex+1, totalItems),
	}

	// Copy existing context
	if exec.Data != nil {
		forkExec.Data = storage.JSON{}
		for k, v := range exec.Data {
			forkExec.Data[k] = v
		}
	}

	// Add .item to the context
	if forkExec.Data == nil {
		forkExec.Data = storage.JSON{}
	}
	forkExec.Data["item"] = item

	result := ForkResult{Item: item}

	// Execute each work step
	for i, workStep := range step.Work {
		// Track position within the fork's work list so Notify can override
		// StepIndex/TotalSteps to reflect the fork item context.
		forkExec.forkWorkIndex = i
		forkExec.forkWorkTotal = len(step.Work)

		// Create a temporary step state for this work item
		workStepState := RitualStepState{
			ExecutionID: exec.ID,
			StepIndex:   exec.CurrentStep,
			Name:        workStep.Name,
			Status:      "pending",
		}

		// Execute the work step
		output, err := r.executeStep(ctx, forkExec, workStep)
		workStepState.Message = output
		workStepState.Output = output

		if err != nil {
			result.Error = err
			result.Output = output
			r.logger.Warn("fork item failed",
				"item", item,
				"step", workStep.Name,
				"error", err)
			break
		}
		result.Output = output
	}

	errStr := ""
	if result.Error != nil {
		errStr = result.Error.Error()
	}
	r.logger.Debug("fork item completed",
		"ritual", exec.RitualName,
		"step", step.Name,
		"item_id", itemID,
		"item_index", itemIndex,
		"total_items", totalItems,
		"error", errStr)

	return result
}

// executeMinisterStep invokes a minister for a task, reusing the session on
// retry so the LLM retains its full conversation history (tool calls, edits,
// reasoning) from the previous attempt.
func (r *RitualRunner) executeMinisterStep(ctx context.Context, exec *RitualExecution, step RitualStep) (string, error) {
	actTemplate := step.Act
	if actTemplate == "" {
		actTemplate = step.Task
	}
	act := r.expandTemplate(actTemplate, exec)

	// Skip minister call when act is empty
	if act == "" {
		return "", nil
	}

	minister := r.getMinister(step.Minister)
	if minister == nil {
		return "", fmt.Errorf("minister not found: %s", step.Minister)
	}

	// Forward all messages to the TUI via the ritual's notify
	baseNotify := func(msg any) {
		if exec.notify == nil {
			return
		}
		switch m := msg.(type) {
		case RitualStepMsg:
			exec.Notify(m)
		case StreamChunkMsg:
			m.ChannelID = exec.ChannelID()
			exec.notify(m)
		default:
			exec.notify(msg)
		}
	}

	// Idle timeout: the step is aborted if the minister produces no output
	// for idleTimeout. The timer resets on every notification (stream chunks,
	// tool calls, etc.), so a minister that is actively working — even if
	// slowly — will never hit this limit. Only truly stuck steps abort.
	idleTimeout := r.idleTimeout
	if idleTimeout == 0 {
		idleTimeout = defaultStepIdleTimeout
	}

	stepTimer := time.NewTimer(idleTimeout)
	defer stepTimer.Stop()

	var timerMu sync.Mutex
	var paused bool

	pauseTimer := func() {
		timerMu.Lock()
		defer timerMu.Unlock()
		if paused {
			return
		}
		if !stepTimer.Stop() {
			select {
			case <-stepTimer.C:
			default:
			}
		}
		paused = true
		r.logger.Debug("idle timer paused for zhengming", "step", step.Name)
	}

	resumeTimer := func() {
		timerMu.Lock()
		defer timerMu.Unlock()
		if !paused {
			return
		}
		// Fresh idle window after zhengming — the minister just got an
		// answer and will start producing output again.
		stepTimer.Reset(idleTimeout)
		paused = false
		r.logger.Debug("idle timer resumed after zhengming", "step", step.Name, "idle_timeout", idleTimeout)
	}

	// Wrap notify to reset the idle timer on every message from the minister.
	notify := func(msg any) {
		timerMu.Lock()
		if !paused {
			stepTimer.Reset(idleTimeout)
		}
		timerMu.Unlock()
		baseNotify(msg)
	}

	// Reuse session on retry / goto re-invocation so the LLM keeps its full
	// conversation history. Create a new one only on the first invocation.
	actSession := exec.stepStates[exec.CurrentStep].Session
	if actSession == nil {
		cfg := minister.GetConfig()
		sessionConfig := &SessionConfig{
			LLM:           cfg,
			WorkingDir:    r.repoInfo.ProjectRoot,
			AtifAgentName: r.atifAgentName,
		}
		var err error
		actSession, err = CreateSession(minister, minister.Model(), sessionConfig, notify, exec.ChannelID(), exec.EdictKey())
		if err != nil {
			return "", fmt.Errorf("failed to create session for %s: %w", step.Minister, err)
		}
		exec.stepStates[exec.CurrentStep].Session = actSession
	}
	actSession.SetNotify(notify, exec.ChannelID())
	// The session is created with the user-configured reasoning effort as
	// its default (NewSession applies LLM.ReasoningEffort). A step that
	// declares an explicit `effort:` key takes precedence over that default;
	// a step with no `effort:` key inherits the user-configured default
	// rather than forcing a hard-coded "medium".
	if step.Effort != "" {
		actSession.ReasoningEffort = step.Effort
	}

	// Load court history into the scratchpad for this step.
	// The scratchpad is injected as a one-shot prefix to the next user message
	// and cleared automatically, so it never pollutes the system prompt.
	if exec.EdictID > 1 {
		scratchpad := r.buildStepScratchpad(exec, step)
		if scratchpad != "" {
			actSession.SetScratchpad(scratchpad)
		}
	}

	// Build work prompt with ritual context and previous step results
	prompt := r.buildWorkPrompt(exec, act)

	// Wire zhengming callbacks on the minister (if it supports them).
	// Access MinisterBase methods through the promoted interface.
	type zhengmingPauser interface {
		SetOnZhengmingRaised(func())
		SetOnZhengmingResolved(func())
	}
	if mb, ok := minister.(zhengmingPauser); ok {
		mb.SetOnZhengmingRaised(pauseTimer)
		mb.SetOnZhengmingResolved(resumeTimer)
		// Clear callbacks after the step to avoid stale references
		defer mb.SetOnZhengmingRaised(nil)
		defer mb.SetOnZhengmingResolved(nil)
	}

	// Create step context that cancels when the timer expires
	stepCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Register the step cancel so PauseRitual can interrupt the LLM stream
	// without cancelling the parent ritual context.
	channelID := exec.ChannelID()
	r.pauseMu.Lock()
	if r.stepCancels == nil {
		r.stepCancels = make(map[string]context.CancelFunc)
	}
	r.stepCancels[channelID] = cancel
	r.pauseMu.Unlock()
	defer func() {
		r.pauseMu.Lock()
		delete(r.stepCancels, channelID)
		r.pauseMu.Unlock()
	}()

	go func() {
		select {
		case <-stepTimer.C:
			cancel()
		case <-stepCtx.Done():
			// Step completed or parent context cancelled
		}
	}()

	// Route act through Task pattern to enable minister processing.
	// This ensures processTask runs, which handles failed verdicts and streams work through the LLM.
	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      stepCtx,
		EdictKey: exec.EdictKey(),
		Work:     prompt,
		Session:  actSession,
		Done:     doneCh,
		Notify:   notify,
	}

	select {
	case minister.Tasks() <- task:
		// Wait for result on doneCh
		result, ok := <-doneCh
		if !ok {
			return "", fmt.Errorf("task done channel closed for step %s", step.Name)
		}

		// If the ritual was paused for ruler interjection, block here until
		// resumed (:continue) or the parent context is cancelled (:abort).
		// The step's LLM stream was interrupted by PauseRitual cancelling
		// stepCtx, so result.Err will be a context-cancelled error.
		if r.pauseChannel(channelID) != nil {
			pausedErr := r.waitIfPaused(ctx, channelID)
			r.clearPause(channelID)
			if pausedErr != nil {
				// Parent context cancelled (:abort) — return the original error
				return result.Output, result.Err
			}
			// Resumed via :continue — return ErrRitualPaused so the ritual
			// loop retries the step from scratch.
			return result.Output, ErrRitualPaused
		}

		if result.Err != nil {
			return result.Output, result.Err
		}
		// Update session for potential reuse in next turn
		if result.Session != nil {
			exec.stepStates[exec.CurrentStep].Session = result.Session
		}
		return result.Output, nil
	default:
		return "", fmt.Errorf("minister %s task channel full", step.Minister)
	}
}

// workPromptTmpl is the template for building dynamic work messages.
// Task comes first so the model reads the instruction before the data.
var workPromptTmpl = template.Must(template.New("work").Parse(
	`# Task
{{ .Act }}
{{ if .previous_failure }}
---
# ⚠️ Previous Attempt Failed — Fix and Retry
Your previous attempt at this step failed with the following error. Analyze the
error carefully and CHANGE YOUR APPROACH this time — repeating the same edits
will fail the same way.

{{ .previous_failure }}
{{ end }}{{ if .step_results }}
---
# Reference Data
The following information has already been gathered for you. Use it directly — do NOT call tools to re-fetch this data.
{{ if .step_results }}
## Previous Step Results
{{ range $name, $result := .step_results }}
### {{ $name }}
{{ $result }}
{{ end }}{{ end }}{{ end }}`))

// buildWorkPrompt builds the dynamic work message from step results, given context, and the act.
// This is rebuilt on every invocation so goto re-invocations get fresh context.
func (r *RitualRunner) buildWorkPrompt(exec *RitualExecution, act string) string {
	data := map[string]interface{}{
		"Act": act,
	}
	// Check if this is a retry/goto - if so, don't include step_results since they're
	// already in the session history from the previous attempt.
	isRetry := false
	if exec.CurrentStep < len(exec.stepStates) {
		curState := exec.stepStates[exec.CurrentStep]
		if curState.RetryCount > 0 {
			isRetry = true
			// Surface the prior failure so the LLM can correct its approach
			if curState.Message != "" {
				data["previous_failure"] = curState.Message
			}
		}
	}
	// Previous step results (use != to include steps after CurrentStep that ran before a goto)
	// Skip on retry/goto to avoid duplicating context already in session history
	if !isRetry {
		for i, ss := range exec.stepStates {
			if ss.Message != "" && i != exec.CurrentStep {
				if data["step_results"] == nil {
					data["step_results"] = map[string]string{}
				}
				data["step_results"].(map[string]string)[ss.Name] = ss.Message
			}
		}
	}
	var buf bytes.Buffer
	if err := workPromptTmpl.Execute(&buf, data); err != nil {
		r.logger.Warn("failed to execute work prompt template", "error", err)
		return act
	}
	return buf.String()
}

// buildStepScratchpad queries the edict's court history and returns a markdown
// block suitable for the transient scratchpad. On goto retry (step has prior
// failure), rejected precedents are shown prominently with principle and
// justification so the LLM sees exactly what the Chancellor objected to.
func (r *RitualRunner) buildStepScratchpad(exec *RitualExecution, step RitualStep) string {
	key := exec.EdictKey()
	var parts []string

	// Precedents — show all, with rejected ones highlighted on retry
	precedents, _ := r.arrangeGetPrecedents(key)
	if preclist, ok := precedents.([]interface{}); ok && len(preclist) > 0 {
		var buf strings.Builder
		buf.WriteString("## Court Precedents\n")
		for _, p := range preclist {
			if pm, ok := p.(map[string]interface{}); ok {
				ruling, _ := pm["ruling"].(string)
				principle, _ := pm["principle"].(string)
				justification, _ := pm["justification"].(string)
				manifestID, _ := pm["manifest_id"].(string)

				isRejected := ruling == string(storage.PrecedentRejected)

				// Check if we're on a retry — highlight rejected precedents
				isRetry := exec.CurrentStep < len(exec.stepStates) &&
					exec.stepStates[exec.CurrentStep].RetryCount > 0

				if isRejected && isRetry {
					buf.WriteString(fmt.Sprintf("\n### ⚠️ Rejected (manifest: %s)\n", manifestID))
					buf.WriteString(fmt.Sprintf("- **Principle:** %s\n", principle))
					buf.WriteString(fmt.Sprintf("- **Justification:** %s\n", justification))
				} else {
					buf.WriteString(fmt.Sprintf("\n- **%s** (%s)", manifestID, ruling))
					if principle != "" {
						buf.WriteString(fmt.Sprintf(" — %s", principle))
					}
				}
				buf.WriteString("\n")
			}
		}
		parts = append(parts, buf.String())
	}

	// Manifests
	manifests, _ := r.arrangeGetManifests(key)
	if manlist, ok := manifests.([]map[string]interface{}); ok && len(manlist) > 0 {
		var buf strings.Builder
		buf.WriteString("## Manifests\n")
		for _, m := range manlist {
			filePath, _ := m["file_path"].(string)
			status, _ := m["status"].(string)
			funcName, _ := m["func_name"].(string)
			buf.WriteString(fmt.Sprintf("- **%s** (%s)", filePath, status))
			if funcName != "" {
				buf.WriteString(fmt.Sprintf(" — %s", funcName))
			}
			buf.WriteString("\n")
		}
		parts = append(parts, buf.String())
	}

	// Verdicts
	verdicts, _ := r.arrangeGetVerdicts(key)
	if verlist, ok := verdicts.([]map[string]interface{}); ok && len(verlist) > 0 {
		var buf strings.Builder
		buf.WriteString("## Verdicts\n")
		for _, v := range verlist {
			verdictID, _ := v["verdict_id"].(string)
			outcome, _ := v["outcome"].(string)
			buf.WriteString(fmt.Sprintf("- **%s**: %s\n", verdictID, outcome))
		}
		parts = append(parts, buf.String())
	}

	// Seals — show all non-stale seals with minister and timestamp
	seals, _ := r.arrangeGetSeals(key)
	if sealist, ok := seals.([]map[string]interface{}); ok && len(sealist) > 0 {
		var buf strings.Builder
		buf.WriteString("## Seals (Seal Chain)\n")
		for _, s := range sealist {
			ministerID, _ := s["minister_id"].(string)
			sealedAt, _ := s["sealed_at"].(time.Time)
			var icon string
			switch ministerID {
			case "judge":
				icon = "🪶"
			case "chancellor":
				icon = "📜"
			case "ruler":
				icon = "👑"
			default:
				icon = "🔏"
			}
			buf.WriteString(fmt.Sprintf("- %s **%s** — sealed %s\n", icon, ministerID, sealedAt.Format(time.RFC3339)))
		}
		parts = append(parts, buf.String())
	}

	// Lings — show all sub-tasks with their status
	lings, _ := r.arrangeGetLings(key)
	if linglist, ok := lings.([]map[string]interface{}); ok && len(linglist) > 0 {
		var buf strings.Builder
		buf.WriteString("## Lings (Sub-tasks)\n")
		for _, l := range linglist {
			lingID, _ := l["ling_id"].(string)
			desc, _ := l["description"].(string)
			status, _ := l["status"].(string)
			buf.WriteString(fmt.Sprintf("- **%s** (%s) — %s\n", lingID[:8], status, desc))
		}
		parts = append(parts, buf.String())
	}

	if len(parts) == 0 {
		return ""
	}
	return "---\n# Court History\n" + strings.Join(parts, "\n")
}

// handleFailure handles step failure based on on_failure action
func (r *RitualRunner) handleFailure(ctx context.Context, exec *RitualExecution, step RitualStep, err error) bool {
	action := OnFailureAction(step.resolveOnFailure(exec.def))
	if action == "" {
		action = OnFailureAbort
	}

	// Don't retry if context was cancelled (e.g. user pressed CTRL-C)
	if ctx.Err() != nil {
		return false
	}

	switch action {
	case OnFailureRetry:
		maxRetries := step.resolveMaxRetries(exec.def)
		if maxRetries == 0 {
			maxRetries = r.maxRetries
		}
		if exec.stepStates[exec.CurrentStep].RetryCount < maxRetries {
			exec.stepStates[exec.CurrentStep].RetryCount++
			// Notify: retrying
			exec.Notify(RitualStepMsg{
				StepName:  step.Name,
				StepIndex: exec.CurrentStep,
				Status:    "retrying",
				Message:   fmt.Sprintf("attempt %d/%d", exec.stepStates[exec.CurrentStep].RetryCount, maxRetries),
			})
			r.logger.Info("retrying step",
				"step", step.Name,
				"attempt", exec.stepStates[exec.CurrentStep].RetryCount)
			return true
		}
		// Retries exhausted — invoke report_failure ritual
		r.invokeReportFailure(ctx, exec, step, err)
		return false

	case OnFailureZhengming:
		// Request clarification
		r.logger.Info("requesting zhengming for failed step", "step", step.Name)
		// This would create a zhengming request
		return false

	case OnFailureGoto:
		if step.OnFailureTarget != "" {
			targetIdx := r.stepIndex(exec.def, step.OnFailureTarget)
			if targetIdx != -1 {
				maxRetries := step.resolveMaxRetries(exec.def)
				if maxRetries == 0 {
					maxRetries = r.maxRetries
				}
				state := &exec.stepStates[exec.CurrentStep]
				if state.RetryCount >= maxRetries {
					r.logger.Info("goto retries exhausted, aborting",
						"step", step.Name,
						"attempts", state.RetryCount,
						"max", maxRetries)
					r.invokeReportFailure(ctx, exec, step, err)
					return false
				}
				state.RetryCount++
				if state.Output != "" {
					state.Message = fmt.Sprintf("Step '%s' failed.\n\nOutput:\n%s\n\nError: %s",
						step.Name, state.Output, err.Error())
				} else {
					state.Message = fmt.Sprintf("Step '%s' failed with error: %s",
						step.Name, err.Error())
				}
				exec.CurrentStep = targetIdx
				r.logger.Info("jumping to step on failure",
					"from", step.Name,
					"to", step.OnFailureTarget,
					"attempt", state.RetryCount,
					"max", maxRetries)
				return true
			}
		}
		return false

	case OnFailureAbort:
		return false

	default:
		return false
	}
}

// emitEvent records a Tian event from the ritual runner.
// Routes through publishEvent for channel delivery when available.
func (r *RitualRunner) emitEvent(key storage.EdictKey, eventType storage.CourtEvent, payload storage.JSON) {
	if r.publishEvent != nil {
		r.publishEvent(key, eventType, payload)
		return
	}
	// Fallback: DB-only (for tests without court)
	event := storage.TianEvent{
		EdictID:   key.ID,
		Username:  key.Username,
		Project:   key.Project,
		EventType: eventType,
		Payload:   payload,
	}
	if err := r.db.Create(&event).Error; err != nil {
		r.logger.Warn("failed to emit ritual event", "type", eventType, "error", err)
	}
}

// invokeReportFailure triggers the report_failure ritual when retries are exhausted
func (r *RitualRunner) invokeReportFailure(ctx context.Context, exec *RitualExecution, step RitualStep, err error) {
	if r.registry.Get("report_failure") == nil {
		return
	}
	inputs := map[string]string{
		"edict_id":    fmt.Sprint(exec.EdictID),
		"ritual_name": exec.RitualName,
		"step_name":   step.Name,
		"error":       err.Error(),
	}
	go func() {
		failKey := exec.EdictKey()
		failExec, startErr := r.Start(ctx, "report_failure", failKey, inputs, nil)
		if startErr != nil {
			r.logger.Warn("failed to start report_failure ritual", "error", startErr)
			return
		}
		if runErr := r.Run(ctx, failExec); runErr != nil {
			r.logger.Warn("report_failure ritual failed", "error", runErr)
		}
	}()
}

// stepIndex returns the index of a step by name
func (r *RitualRunner) stepIndex(def *RitualDef, name string) int {
	for i, step := range def.Steps {
		if step.Name == name {
			return i
		}
	}
	return -1
}

// expandTemplate expands Go text/template syntax in a string
func (r *RitualRunner) expandTemplate(text string, exec *RitualExecution) string {
	data := map[string]interface{}{
		"edict_id": exec.EdictID,
	}

	// Copy all exec.Data into template context
	for k, v := range exec.Data {
		data[k] = v
	}

	// Flatten inputs into top-level template context.
	// Inputs may be map[string]string (in-memory from Start) or
	// map[string]interface{} (after DB round-trip via storage.JSON.Scan).
	switch inputs := exec.Data["inputs"].(type) {
	case map[string]interface{}:
		for k, v := range inputs {
			data[k] = v
		}
	case map[string]string:
		for k, v := range inputs {
			data[k] = v
		}
	}

	// Build step_results from completed steps
	stepResults := map[string]interface{}{}
	for i, ss := range exec.stepStates {
		if ss.Message != "" && i != exec.CurrentStep {
			stepResults[ss.Name] = ss.Message
		}
	}
	data["step_results"] = stepResults

	tmpl, err := template.New("ritual").Parse(text)
	if err != nil {
		r.logger.Warn("failed to parse template", "error", err, "text", text)
		return text
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		r.logger.Warn("failed to execute template", "error", err, "text", text)
		return text
	}
	return strings.TrimSpace(buf.String())
}

// PauseRitual signals the ritual running on channelID to pause.
// It cancels the active step's context (stopping the LLM stream) and
// creates a pause channel that the ritual goroutine will block on.
// Returns false if already paused.
func (r *RitualRunner) PauseRitual(channelID string) bool {
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	if r.pauseChans == nil {
		r.pauseChans = make(map[string]chan struct{})
	}
	if r.stepCancels == nil {
		r.stepCancels = make(map[string]context.CancelFunc)
	}
	if _, ok := r.pauseChans[channelID]; ok {
		return false // already paused
	}
	r.pauseChans[channelID] = make(chan struct{})
	// Cancel the active step's context if one is running, to interrupt the
	// LLM stream. The ritual goroutine stays alive because we don't cancel
	// the parent context.
	if cancel, ok := r.stepCancels[channelID]; ok {
		cancel()
	}
	return true
}

// ResumeRitual signals the paused ritual on channelID to continue.
// Returns false if no ritual is paused on that channel.
func (r *RitualRunner) ResumeRitual(channelID string) bool {
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	ch, ok := r.pauseChans[channelID]
	if !ok {
		return false
	}
	close(ch)
	delete(r.pauseChans, channelID)
	return true
}

// waitIfPaused blocks if the ritual on channelID has been asked to pause.
// Returns nil when resumed, or ctx.Err() if the context is cancelled.
func (r *RitualRunner) waitIfPaused(ctx context.Context, channelID string) error {
	r.pauseMu.Lock()
	ch, ok := r.pauseChans[channelID]
	r.pauseMu.Unlock()
	if !ok {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pauseChannel returns the pause channel for channelID, or nil if none.
func (r *RitualRunner) pauseChannel(channelID string) chan struct{} {
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	return r.pauseChans[channelID]
}

// clearPause removes the pause channel for channelID (cleanup on step exit).
func (r *RitualRunner) clearPause(channelID string) {
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	delete(r.pauseChans, channelID)
}

// saveExecution persists execution state to database
func (r *RitualRunner) saveExecution(exec *RitualExecution) {
	exec.UpdatedAt = time.Now()
	if err := r.db.Save(exec).Error; err != nil {
		r.logger.Warn("failed to save ritual execution", "error", err)
	}

	for i := range exec.stepStates {
		if err := r.db.Save(&exec.stepStates[i]).Error; err != nil {
			r.logger.Warn("failed to save step state", "error", err)
		}
	}
}

// GetExecution retrieves an execution by ID, scoped to username/project
func (r *RitualRunner) GetExecution(executionID, username, project string) (*RitualExecution, error) {
	var exec RitualExecution
	if err := r.db.First(&exec, "id = ? AND username = ? AND project = ?", executionID, username, project).Error; err != nil {
		return nil, err
	}

	// Load step states
	var states []RitualStepState
	if err := r.db.Where("execution_id = ?", executionID).Order("step_index").Find(&states).Error; err != nil {
		return nil, err
	}
	exec.stepStates = states

	// Load definition
	exec.def = r.registry.Get(exec.RitualName)

	return &exec, nil
}

// ListExecutions lists executions for an edict.
// When key.ID is zero, it lists all executions scoped to key.Username/key.Project.
func (r *RitualRunner) ListExecutions(key storage.EdictKey) ([]RitualExecution, error) {
	var executions []RitualExecution
	query := r.db.Order("created_at DESC")
	if key.ID != 0 {
		query = query.Where("edict_id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project)
	} else {
		// Even without an edict ID, scope by username/project to avoid cross-project leaks
		query = query.Where("username = ? AND project = ?", key.Username, key.Project)
	}
	if err := query.Find(&executions).Error; err != nil {
		return nil, err
	}
	return executions, nil
}
