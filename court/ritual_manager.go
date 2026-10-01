package court

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/afittestide/asimi/internal"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
)

// RitualGuardPrompt defines the Ritual Guard's identity
const RitualGuardPrompt = `禁军，Jìnjūn. You are commanding ritual execution and event handling

You subscribe to events and trigger subscribers: rituals and ministers.

If you fail, the court enters flatline—detectable by overdue rituals. Your authority is time; your weapon is punctuality.

CRITICAL RULES:
- Process events in order, never skip
- Save checkpoints for crash recovery
- Detect flatlines (no events processed for 5 minutes)
- Escalate urgent Zhengming that times out
- Move failed events to DLQ after retries`

// EventNotificationMsg notifies the UI of significant Court events
type EventNotificationMsg struct {
	ChannelID string                 `msgpack:"channel_id,omitempty"`
	EventType storage.CourtEvent     `msgpack:"event_type"`
	EdictKey  storage.EdictKey       `msgpack:"edict_key"`
	Message   string                 `msgpack:"message,omitempty"`
	Payload   map[string]interface{} `msgpack:"payload,omitempty"`
}

// RitualGuard processes events and owns ritual/event infrastructure
type RitualGuard struct {
	*MinisterBase  // embedded base for database access and session creation
	ritualRegistry *RitualRegistry
	ritualRunner   *RitualRunner
	eventRegistry  *EventRegistry
	eventCh        chan Event
	ritualMu       sync.Mutex   // serializes ritual execution without blocking the event loop
	queuedCount    atomic.Int32 // number of rituals waiting for the lock
	maxRetries     int
	batchSize      int
	flatlineAge    time.Duration
	// Dependency injection (replaces *Court back-reference)
	getMinister  func(id string) Minister
	streamingCtx func(string) context.Context
	version      string // Application version for health checks

	// Zhengming functions injected by Court
	waitForZhengming func(ctx context.Context, requestID string) (string, error)
	requestZhengming func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error)
	deliverZhengming func(answer ZhengmingAnswer) bool

	// recoveryMu blocks event-driven rituals during recovery prompts
	recoveryMu       sync.RWMutex
	recoveryComplete bool
}

// RitualGuardOpts configures a new RitualGuard.
type RitualGuardOpts struct {
	Base             *MinisterBase
	Runner           runners.Runner
	GetMinister      func(id string) Minister
	OnRunnerUpgrade  func(runners.Runner) // propagates runner changes back to court
	StreamingCtx     func(string) context.Context
	Version          string // Application version for health checks
	WaitForZhengming func(ctx context.Context, requestID string) (string, error)
	RequestZhengming func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error)
	DeliverZhengming func(answer ZhengmingAnswer) bool
}

// NewRitualGuard creates a new Ritual Guard that owns all ritual/event infrastructure.
func NewRitualGuard(opts RitualGuardOpts) *RitualGuard {
	opts.Base.ministerID = "ritual_guard"
	registry := NewRitualRegistry()
	eventRegistry := NewEventRegistry()
	eventCh := make(chan Event, 256)

	rg := &RitualGuard{
		MinisterBase:     opts.Base,
		ritualRegistry:   registry,
		eventRegistry:    eventRegistry,
		eventCh:          eventCh,
		maxRetries:       3,
		batchSize:        100,
		flatlineAge:      5 * time.Minute,
		getMinister:      opts.GetMinister,
		streamingCtx:     opts.StreamingCtx,
		version:          opts.Version,
		waitForZhengming: opts.WaitForZhengming,
		requestZhengming: opts.RequestZhengming,
		deliverZhengming: opts.DeliverZhengming,
	}

	// Create ritual runner with injected functions
	rg.ritualRunner = NewRitualRunner(
		registry,
		opts.GetMinister,
		rg.PublishEvent,
		opts.Base.db,
		opts.Runner,
		opts.Base.logger,
		opts.Base.repoInfo,
	)
	rg.ritualRunner.onRunnerUpgrade = opts.OnRunnerUpgrade
	rg.ritualRunner.waitZhengming = opts.WaitForZhengming

	return rg
}

// ID returns the minister identifier (not technically a minister)
func (rg *RitualGuard) ID() string {
	return "ritual_guard"
}

// SystemPrompt returns the RitualGuard's system prompt template.
func (rg *RitualGuard) SystemPrompt() string {
	return RitualGuardPrompt
}

// Tools returns the RitualGuard's LLM tools for interactive sessions
// RitualGuard doesn't have LLM tools - it's an event processor, not an agent
func (rg *RitualGuard) Tools() []Tool {
	return []Tool{}
}

// Tasks returns a no-op channel (RitualGuard doesn't receive tasks)
func (rg *RitualGuard) Tasks() chan<- *Task {
	return make(chan *Task)
}

// --- Event lifecycle ---

// PublishEvent persists an event to the DB and sends it to the event channel.
func (rg *RitualGuard) PublishEvent(key storage.EdictKey, eventType storage.CourtEvent, payload storage.JSON) uint {
	if rg.db != nil {
		dbEvent := storage.TianEvent{
			EdictID:   key.ID,
			Username:  key.Username,
			Project:   key.Project,
			EventType: eventType,
			Payload:   payload,
		}
		if err := rg.db.Create(&dbEvent).Error; err != nil {
			rg.logger.Error("failed to persist event", "type", eventType, "error", err)
		}
	}
	select {
	case rg.eventCh <- Event{Type: eventType, EdictKey: key, Payload: map[string]interface{}(payload)}:
	default:
		rg.logger.Warn("event channel full, persisted to DB only", "type", eventType)
	}
	return key.ID
}

// ritualChannelID returns the streaming channel ID for a ritual's edict.
// All edicts use the "e<N>" convention matching commit message suffixes,
// including edict 1 (court infrastructure), which uses "e1".
func ritualChannelID(edictID uint) string {
	return fmt.Sprintf("e%d", edictID)
}

// isRitualChannelID reports whether channelID is a per-edict ritual channel
// ("e<digits>"). Mirrors the TUI's isRitualChannel so the Court can refuse to
// mint interactive sessions for ritual channels.
func isRitualChannelID(channelID string) bool {
	if len(channelID) < 2 || channelID[0] != 'e' {
		return false
	}
	_, err := strconv.ParseUint(channelID[1:], 10, 64)
	return err == nil
}

// startRitual starts and runs a ritual using the edict's own streaming context.
// It runs in a goroutine so ritual execution (including blocking waits for
// zhengming during recovery) cannot stall the event loop. Rituals remain
// serialized via ritualMu so only one runs at a time.
func (rg *RitualGuard) startRitual(ritualName string, key storage.EdictKey, inputs map[string]string) {
	go func() {
		// Try to acquire lock; if held, check for stale rituals and abort them
		if !rg.ritualMu.TryLock() {
			queueLen := int(rg.queuedCount.Add(1))
			rg.notify(RitualStepMsg{
				ChannelID:  ritualChannelID(key.ID),
				RitualName: ritualName,
				EdictID:    key.ID,
				Status:     "queued",
				Message:    fmt.Sprintf("Ritual %s queued — waiting for another ritual to finish", ritualName),
				QueueLen:   queueLen,
			})
			rg.abortStaleRitualsIfLocked()
			rg.ritualMu.Lock()
			rg.queuedCount.Add(-1)
		}
		defer rg.ritualMu.Unlock()
		channelID := ritualChannelID(key.ID)
		ctx := rg.streamingCtx(channelID)
		exec, err := rg.ritualRunner.Start(ctx, ritualName, key, inputs, rg.notify)
		if err != nil {
			rg.logger.Warn("failed to start ritual", "ritual", ritualName, "error", err)
			return
		}
		if err := rg.ritualRunner.Run(ctx, exec); err != nil {
			if errors.Is(err, context.Canceled) {
				// Context cancelled (user interrupt) — StreamInterruptedMsg
				// already surfaces as 🛑 ABORTED in the TUI. No duplicate
				// ritual_failed notification needed.
				rg.logger.Info("ritual cancelled by ruler", "ritual", ritualName)
				return
			}
			rg.logger.Warn("ritual failed", "ritual", ritualName, "error", err)
			rg.notify(RitualStepMsg{
				ChannelID:   ritualChannelID(key.ID),
				RitualName:  ritualName,
				ExecutionID: exec.ID,
				EdictID:     key.ID,
				Status:      "ritual_failed",
				Message:     getRulersError(err),
			})
			rg.ritualRunner.emitEvent(key, storage.EventRitualFailed, storage.JSON{
				"ritual":       ritualName,
				"execution_id": exec.ID,
				"error":        getRulersError(err),
			})
		}
	}()
}

// abortStaleRitualsIfLocked checks for running rituals older than flatlineAge
// and aborts them directly in the database. This handles the case where a
// previous ritual goroutine died while holding the lock.
func (rg *RitualGuard) abortStaleRitualsIfLocked() {
	if rg.db == nil {
		return
	}

	cutoff := time.Now().Add(-rg.flatlineAge)
	var staleRituals []RitualExecution
	if err := rg.db.Where("state = ? AND username = ? AND project = ? AND updated_at < ?", RitualStateRunning, rg.Username(), rg.Project(), cutoff).
		Find(&staleRituals).Error; err != nil {
		rg.logger.Warn("failed to query stale rituals", "error", err)
		return
	}

	for _, ritual := range staleRituals {
		rg.logger.Info("aborting stale ritual",
			"execution_id", ritual.ID,
			"ritual", ritual.RitualName,
			"edict_id", ritual.EdictID,
			"updated_at", ritual.UpdatedAt)
		if err := rg.abortRitual(context.Background(), &ritual, "ritual exceeded flatline age"); err != nil {
			rg.logger.Warn("failed to abort stale ritual",
				"execution_id", ritual.ID,
				"error", err)
		}
	}
}

// DispatchEvent dispatches an event to all subscribers and triggers event-driven rituals.
// All rituals run sequentially on the Chancellor tab's streaming context.
func (rg *RitualGuard) DispatchEvent(event Event) {
	if rg.eventRegistry == nil {
		return
	}
	rg.eventRegistry.Dispatch(event)

	// Send notification for significant events
	rg.notifyEvent(event)

	// Handle ritual enactment (from chancellor's enact_ritual tool)
	if event.Type == storage.EventRitualEnacted {
		ritualName, _ := event.Payload["ritual_name"].(string)
		inputs := make(map[string]string)
		if rawInputs, ok := event.Payload["inputs"].(map[string]interface{}); ok {
			for k, v := range rawInputs {
				inputs[k] = fmt.Sprintf("%v", v)
			}
		}
		rg.startRitual(ritualName, event.EdictKey, inputs)
		return
	}
	// Trigger event-driven rituals (skip if recovery prompts are in progress)
	rg.recoveryMu.RLock()
	recoveryComplete := rg.recoveryComplete
	rg.recoveryMu.RUnlock()
	if !recoveryComplete {
		rituals := rg.ritualRegistry.GetByEvent(string(event.Type))
		if len(rituals) > 0 {
			rg.logger.Debug("skipping event-driven rituals during recovery prompts", "event", event.Type)
		}
		return
	}
	if rg.ritualRegistry != nil && rg.ritualRunner != nil {
		rituals := rg.ritualRegistry.GetByEvent(string(event.Type))
		sourceRitual, _ := event.Payload["ritual"].(string)
		for _, ritual := range rituals {
			if ritual.Name == sourceRitual {
				rg.logger.Debug("skipping self-triggered ritual",
					"ritual", ritual.Name, "event", event.Type)
				continue
			}
			inputs := map[string]string{"edict_id": fmt.Sprint(event.EdictKey.ID)}
			rg.startRitual(ritual.Name, event.EdictKey, inputs)
		}
	}
}

// notifyEvent sends a notification to the UI for significant events
func (rg *RitualGuard) notifyEvent(event Event) {
	if rg.notify == nil {
		return
	}
	msg := rg.buildEventNotification(event)
	rg.notify(msg)
}

// buildEventNotification maps event types to user-friendly notification messages
func (rg *RitualGuard) buildEventNotification(event Event) EventNotificationMsg {
	msg := EventNotificationMsg{
		ChannelID: ritualChannelID(event.EdictKey.ID),
		EventType: event.Type,
		EdictKey:  event.EdictKey,
		Payload:   event.Payload,
	}

	edictID := event.EdictKey.ID

	// Build message based on event type
	switch event.Type {
	case storage.EventEdictCreated:
		intent, _ := event.Payload["intent"].(string)
		if intent == "" {
			intent = "New edict"
		}
		// Truncate long intents for display
		if len(intent) > 60 {
			intent = intent[:57] + "..."
		}
		msg.Message = fmt.Sprintf("Edict %d created: %s", edictID, intent)

	case storage.EventEdictSealed:
		msg.Message = fmt.Sprintf("Edict %d sealed and ascended to Heaven", edictID)

	case storage.EventSealGranted:
		minister, _ := event.Payload["minister_id"].(string)
		if minister == "" {
			minister = "Unknown"
		}
		if minister == "ruler" {
			msg.Message = fmt.Sprintf("Ruler sealed edict %d", edictID)
		} else {
			msg.Message = fmt.Sprintf("Minister %s sealed edict %d", minister, edictID)
		}

	case storage.EventManifestCommitted:
		msg.Message = fmt.Sprintf("Forge committed manifest for edict %d", edictID)

	case storage.EventZhengmingNeeded:
		summary, _ := event.Payload["summary"].(string)
		if summary == "" {
			summary = "clarification needed"
		}
		msg.Message = fmt.Sprintf("Zhengming requested for edict %d: %s", edictID, summary)

	case storage.EventZhengmingAnswered:
		if edictID == 0 {
			msg.Message = fmt.Sprintf("Zhengming answered for the court")
		} else {
			msg.Message = fmt.Sprintf("Zhengming answered for edict %d", edictID)
		}

	case storage.EventRitualAborted:
		ritual, _ := event.Payload["ritual"].(string)
		reason, _ := event.Payload["reason"].(string)
		msg.Message = fmt.Sprintf("Ritual %s aborted: %s", ritual, reason)

	case storage.EventEdictCancelled:
		msg.Message = fmt.Sprintf("Edict %d cancelled", edictID)
	}

	return msg
}

// HealthCheckResult contains structured health check diagnostics
type HealthCheckResult struct {
	OK          bool              `json:"ok"`
	ModelOK     bool              `json:"model_ok,omitempty"`
	SandboxOK   bool              `json:"sandbox_ok,omitempty"`
	VersionOK   bool              `json:"version_ok,omitempty"`
	Failures    []string          `json:"failures,omitempty"`
	Remediation map[string]string `json:"remediation,omitempty"`
}

// Subscribe registers a handler for an event type.
func (rg *RitualGuard) Subscribe(eventType storage.CourtEvent, handler EventHandler) {
	rg.eventRegistry.Subscribe(eventType, handler)
}

// RunHealthCheck performs startup health checks and returns the result
func (rg *RitualGuard) RunHealthCheck(event Event) *HealthCheckResult {
	result := &HealthCheckResult{
		OK:          true,
		Remediation: make(map[string]string),
	}

	info := func(text string) {
		rg.notify(MinisterCompletedMsg{
			MinisterID: "ritual_guard",
			Output:     text,
		})
	}
	fail := func(text string) {
		result.OK = false
		result.Failures = append(result.Failures, text)
		rg.notify(MinisterCompletedMsg{
			MinisterID: "ritual_guard",
			Error:      errors.New(text),
		})
	}

	latest, _ := event.Payload["latest_version"].(string)
	hasUpdate, _ := event.Payload["has_update"].(bool)
	current, _ := event.Payload["current_version"].(string)

	// Check 1: Version
	if hasUpdate && latest != "" {
		result.VersionOK = false
		result.Remediation["version"] = "Run `:update` to upgrade to " + latest
		info(fmt.Sprintf("Asimi Update available: %s\n\tRun `:update` to get it", latest))
	} else {
		result.VersionOK = true
		info(fmt.Sprintf("Running latest Asimi version %s", current))
	}

	// Check 2: Model - Verify LLM connectivity with actual ping
	if rg.getMinister != nil {
		secretary := rg.getMinister("secretary")
		if secretary != nil {
			if secretary.Model() == nil {
				result.ModelOK = false
				result.Remediation["model"] = "Configure LLM model in settings"
				fail("✗ LLM model not configured")
			} else if !rg.pingLLM(secretary) {
				result.ModelOK = false
				result.Remediation["model"] = "Check LLM API endpoint and credentials"
				fail("✗ LLM model not responsive")
			} else {
				result.ModelOK = true
				info("✓ Model connectivity check passed")
			}
		}
	}

	// Check 3: Sandbox - Verify sandbox image exists using actual runner image name
	imageName := rg.getSandboxImageName()
	if imageName == "" {
		result.SandboxOK = false
		result.Remediation["sandbox"] = "No sandbox runner available; run `just build-sandbox` to create the image"
		fail("✗ Sandbox image not available (no PodmanRunner configured)")
	} else if !runners.IsPodmanAvailable(imageName) {
		result.SandboxOK = false
		result.Remediation["sandbox"] = "Run `just build-sandbox` to create the image"
		fail(fmt.Sprintf("Sandbox image not found: %s", imageName))
	} else {
		result.SandboxOK = true
		info("✓ Sandbox image check passed")
	}

	return result
}

// pingLLM creates a session and sends a ping to verify LLM connectivity
func (rg *RitualGuard) pingLLM(minister Minister) bool {
	client := minister.Model()
	if client == nil {
		return false
	}

	// Create a minimal session for ping test
	config := &SessionConfig{
		LLM:        minister.GetConfig(),
		WorkingDir: minister.RepoInfo().ProjectRoot,
	}

	sess, err := CreateSession(rg, client, config, nil, "health_check")
	if err != nil {
		rg.logger.Debug("health check: failed to create session for ping", "error", err)
		return false
	}

	// Send a simple ping prompt with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use a simple ping message
	_, err = sess.AskWithStreaming(ctx, "Respond with just the word: PONG", nil)
	if err != nil {
		rg.logger.Debug("health check: LLM ping failed", "error", err)
		return false
	}

	rg.logger.Debug("health check: LLM ping successful")
	return true
}

// getSandboxImageName returns the sandbox image name from the runner
func (rg *RitualGuard) getSandboxImageName() string {
	if rg.getMinister != nil {
		secretary := rg.getMinister("secretary")
		if secretary != nil {
			if runnerProvider, ok := secretary.(interface{ Runner() runners.Runner }); ok {
				runner := runnerProvider.Runner()
				if runner != nil {
					if podmanRunner, ok := runner.(*runners.PodmanRunner); ok {
						return podmanRunner.GetImageName()
					}
				}
			}
		}
	}
	return ""
}

// --- Ritual management ---

// LoadRituals loads rituals from all sources using LoadAllRituals.
// It loads embedded rituals, user config (~/.config/asimi/rituals.yaml),
// and project config (.agents/rituals.yaml).
func (rg *RitualGuard) LoadRituals() error {
	// Get project directory from repo info; SetContext is the sole authority
	projectDir := rg.repoInfo.ProjectRoot
	if projectDir == "" {
		rg.logger.Error("LoadRituals called with empty project root")
		return fmt.Errorf("project root not set: SetContext is the sole authority for project root")
	}

	rituals, err := LoadAllRituals(projectDir)
	if err != nil {
		return fmt.Errorf("failed to load rituals: %w", err)
	}

	for _, ritual := range rituals {
		if err := rg.ritualRegistry.Register(ritual); err != nil {
			rg.logger.Warn("failed to register ritual",
				"ritual", ritual.Name,
				"error", err)
			continue
		}
		rg.logger.Debug("loaded ritual", "name", ritual.Name)
	}

	return nil
}

// RitualRegistry returns the ritual registry.
func (rg *RitualGuard) RitualRegistry() *RitualRegistry {
	return rg.ritualRegistry
}

// RitualRunner returns the ritual runner.
func (rg *RitualGuard) RitualRunner() *RitualRunner {
	return rg.ritualRunner
}

// EventRegistry returns the event registry.
func (rg *RitualGuard) EventRegistry() *EventRegistry {
	return rg.eventRegistry
}

// SetNotify sets the notification callback.
func (rg *RitualGuard) SetNotify(notify internal.NotifyFunc) {
	rg.notify = notify
}

// DeliverZhengmingAnswer delivers a zhengming answer to a waiting ritual.
// Returns true if the answer was delivered to a waiting caller.
func (rg *RitualGuard) DeliverZhengmingAnswer(answer ZhengmingAnswer) bool {
	if rg.deliverZhengming != nil {
		return rg.deliverZhengming(answer)
	}
	return false
}

// DrainUnprocessedEvents replays events persisted to DB but never dispatched (crash recovery).
func (rg *RitualGuard) DrainUnprocessedEvents() []DrainedEvent {
	lastEventID, err := rg.GetLastAcknowledgedEvent()
	if err != nil {
		rg.logger.Warn("drain: failed to get last checkpoint", "error", err)
		return nil
	}

	events, err := rg.GetEventsFrom(lastEventID, 0, rg.Username(), rg.Project())
	if err != nil {
		rg.logger.Warn("drain: failed to get unprocessed events", "error", err)
		return nil
	}

	if len(events) == 0 {
		return nil
	}

	rg.logger.Info("draining unprocessed events", "count", len(events), "from_id", lastEventID)
	var drained []DrainedEvent
	for _, event := range events {
		t := event.EventType
		key := storage.EdictKey{ID: event.EdictID, Username: event.Username, Project: event.Project}
		rg.DispatchEvent(Event{
			Type:     t,
			EdictKey: key,
			Payload:  map[string]interface{}(event.Payload),
		})
		drained = append(drained, DrainedEvent{
			EventType: t,
			EdictKey:  key,
			Payload:   map[string]interface{}(event.Payload),
		})
		if err := rg.SaveCheckpoint(event.ID); err != nil {
			rg.logger.Warn("drain: failed to save checkpoint", "error", err)
		}
	}
	return drained
}

// --- Database Methods ---

// GetEventsFrom retrieves events starting from a given event ID, filtered by username and project
func (rg *RitualGuard) GetEventsFrom(fromEventID uint, limit int, username string, project string) ([]storage.TianEvent, error) {
	var events []storage.TianEvent
	query := rg.db.Where("id > ? AND username = ? AND project = ?", fromEventID, username, project).
		Order("id ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&events).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get events: %w", err)
	}
	return events, nil
}

// AcknowledgeEvent marks an event as processed (no-op for SQLite, used for checkpointing)
func (rg *RitualGuard) AcknowledgeEvent(eventID uint) error {
	return nil
}

// GetLastAcknowledgedEvent returns the last processed event ID
func (rg *RitualGuard) GetLastAcknowledgedEvent() (uint, error) {
	return rg.LoadCheckpoint()
}

// SaveCheckpoint persists the last processed event ID for crash recovery
func (rg *RitualGuard) SaveCheckpoint(eventID uint) error {
	result := rg.db.Exec(`
		INSERT OR REPLACE INTO ritual_guard_checkpoint (id, event_id, updated_at)
		VALUES (1, ?, datetime('now'))
	`, eventID)
	if result.Error != nil {
		return fmt.Errorf("failed to save checkpoint: %w", result.Error)
	}
	return nil
}

// LoadCheckpoint retrieves the last processed event ID
func (rg *RitualGuard) LoadCheckpoint() (uint, error) {
	var eventID uint
	err := rg.db.Raw(`SELECT COALESCE(event_id, 0) FROM ritual_guard_checkpoint WHERE id = 1`).
		Scan(&eventID).Error
	if err != nil {
		return 0, nil
	}
	return eventID, nil
}

// MoveToDLQ moves a failed event to the dead letter queue
func (rg *RitualGuard) MoveToDLQ(event storage.TianEvent, errMsg string, retryCount int) error {
	dlqEntry := storage.TianEventDLQ{
		OriginalID:   event.ID,
		EdictID:      event.EdictID,
		Username:     event.Username,
		Project:      event.Project,
		EventType:    event.EventType,
		Payload:      event.Payload,
		ErrorMessage: errMsg,
		RetryCount:   retryCount,
	}

	if err := rg.db.Create(&dlqEntry).Error; err != nil {
		return fmt.Errorf("failed to move to DLQ: %w", err)
	}
	return nil
}

// scanForStaleRituals finds running rituals on sealed/cancelled edicts and aborts them,
// as well as rituals older than 1 hour (except edict 145)
func (rg *RitualGuard) scanForStaleRituals(ctx context.Context) {
	if rg.db == nil || rg.ritualRunner == nil {
		return
	}

	// Find all running rituals
	var runningRituals []RitualExecution
	if err := rg.db.Where("state = ? AND username = ? AND project = ?", RitualStateRunning, rg.Username(), rg.Project()).Find(&runningRituals).Error; err != nil {
		rg.logger.Warn("failed to query running rituals", "error", err)
		return
	}

	if len(runningRituals) == 0 {
		return
	}

	// Collect edict IDs to check
	edictIDs := make([]uint, 0, len(runningRituals))
	for _, ritual := range runningRituals {
		if ritual.EdictID != 0 {
			edictIDs = append(edictIDs, ritual.EdictID)
		}
	}

	if len(edictIDs) == 0 {
		return
	}

	// Find edicts that are sealed or cancelled
	var edicts []storage.Edict
	if err := rg.db.Where("id IN ? AND username = ? AND project = ?", edictIDs, rg.Username(), rg.Project()).Find(&edicts).Error; err != nil {
		rg.logger.Warn("failed to query edicts", "error", err)
		return
	}

	if len(edicts) == 0 {
		return
	}

	// Build set of stale edict IDs (sealed or cancelled)
	sealService := storage.NewSealService(rg.db)
	staleEdictSet := make(map[uint]bool)
	staleStatuses := make(map[uint]storage.EdictStatus)

	for _, edict := range edicts {
		edictKey := storage.EdictKey{ID: edict.ID, Username: edict.Username, Project: edict.Project}
		status, err := sealService.GetEdictStatus(edictKey)
		if err != nil {
			continue
		}
		if status == storage.EdictSealed || status == storage.EdictCancelled {
			staleEdictSet[edict.ID] = true
			staleStatuses[edict.ID] = status
			rg.logger.Info("detected stale ritual on edict state change",
				"edict_id", edict.ID,
				"edict_status", status)
		}
	}

	if len(staleEdictSet) == 0 {
		return
	}

	// Abort rituals on stale edicts
	for _, ritual := range runningRituals {
		if staleEdictSet[ritual.EdictID] {
			status := staleStatuses[ritual.EdictID]
			if err := rg.abortRitual(ctx, &ritual, fmt.Sprintf("edict %d is %s", ritual.EdictID, status)); err != nil {
				rg.logger.Warn("failed to abort stale ritual",
					"execution_id", ritual.ID,
					"edict_id", ritual.EdictID,
					"error", err)
			}
		}
	}
}

// abortRitual marks a ritual as aborted and emits an event
func (rg *RitualGuard) abortRitual(ctx context.Context, exec *RitualExecution, reason string) error {
	// Update ritual state to aborted
	if err := rg.db.Model(&RitualExecution{}).
		Where("id = ?", exec.ID).
		Updates(map[string]interface{}{
			"state":      RitualStateAborted,
			"updated_at": time.Now(),
		}).Error; err != nil {
		return fmt.Errorf("failed to update ritual state: %w", err)
	}

	// Emit ritual_aborted event
	if rg.ritualRunner != nil {
		abortKey := exec.EdictKey()
		rg.ritualRunner.emitEvent(abortKey, storage.EventRitualAborted, storage.JSON{
			"ritual":       exec.RitualName,
			"execution_id": exec.ID,
			"reason":       reason,
		})
	}

	rg.logger.Info("ritual aborted due to edict state change",
		"ritual", exec.RitualName,
		"execution_id", exec.ID,
		"edict_id", exec.EdictID,
		"reason", reason)

	return nil
}

// Run consumes events from the event channel and dispatches them.
func (rg *RitualGuard) Run(ctx context.Context) {
	rg.logger.Info("ritual guard started (channel mode)")

	// Start aborted ritual recovery in background so event loop can process zhengming answers
	go rg.promptForAbortedRituals(ctx)

	// Create ticker for stale ritual scan every 2 minutes
	staleScanTicker := time.NewTicker(2 * time.Minute)
	defer staleScanTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			rg.logger.Info("ritual guard stopped")
			return
		case event := <-rg.eventCh:
			rg.DispatchEvent(event)
		case <-staleScanTicker.C:
			rg.scanForStaleRituals(ctx)
		}
	}
}
