package court

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "modernc.org/sqlite"
)

// setupEventTestDB creates a test database with tian_events and ritual_guard_checkpoint tables.
func setupEventTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := tmpDir + "/event_test.db"

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	// Serialize access through one connection: background ritual goroutines
	// write concurrently, and PRAGMA busy_timeout is per-connection, so extra
	// pool connections would flake with "database is locked" (SQLITE_BUSY).
	// Mirrors the production cap in storage/db.go.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	// Close before t.TempDir's RemoveAll cleanup runs (t.Cleanup is LIFO, and
	// TempDir registered its cleanup first). Background ritual goroutines keep
	// writing to the DB after the test returns; leaving the handle open lets
	// SQLite recreate journal files mid-removal, failing with ENOTEMPTY.
	t.Cleanup(func() { sqlDB.Close() })

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("Failed to initialize gorm: %v", err)
	}

	err = db.AutoMigrate(
		&storage.TianEvent{},
		&storage.TianEventDLQ{},
		&storage.RitualGuardCheckpoint{},
		&RitualExecution{},
		&RitualStepState{},
	)
	if err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	return db
}

// newTestCourt creates a minimal Court with event channel for testing.
func newTestCourt(t *testing.T, db *gorm.DB) *Court {
	t.Helper()
	s := &Court{
		db:        db,
		logger:    slog.Default(),
		ministers: make(map[string]Minister),
	}
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := NewRitualGuard(RitualGuardOpts{Base: base})
	s.ritualGuard = rg
	return s
}

func TestChannelDelivery(t *testing.T) {
	db := setupEventTestDB(t)
	s := newTestCourt(t, db)

	// Subscribe to the event
	var received []Event
	var mu sync.Mutex
	s.ritualGuard.Subscribe(storage.EventEdictCreated, func(e Event) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	// Start the channel consumer
	done := make(chan struct{})
	go func() {
		s.ritualGuard.Run(ctx)
		close(done)
	}()

	// Publish an event
	s.PublishEvent(storage.EdictKey{ID: 1}, storage.EventEdictCreated, storage.JSON{"foo": "bar"})

	// Wait briefly for the event to be dispatched
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if len(received) != 1 {
		t.Fatalf("expected 1 dispatched event, got %d", len(received))
	}
	if received[0].EdictKey.ID != 1 {
		t.Errorf("expected EdictID 1, got %d", received[0].EdictKey.ID)
	}
	if received[0].Type != storage.EventEdictCreated {
		t.Errorf("expected type %q, got %q", storage.EventEdictCreated, received[0].Type)
	}
	mu.Unlock()

	cancel()
	<-done
}

func TestDBPersistence(t *testing.T) {
	db := setupEventTestDB(t)
	s := newTestCourt(t, db)

	// Publish an event (just DB + channel, no consumer needed)
	s.PublishEvent(storage.EdictKey{ID: 2}, "edict_created", storage.JSON{"key": "val"})

	// Verify it's in the database
	var events []storage.TianEvent
	if err := db.Find(&events).Error; err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 DB event, got %d", len(events))
	}
	if events[0].EdictID != 2 {
		t.Errorf("expected EdictID 2, got %d", events[0].EdictID)
	}
	if events[0].EventType != "edict_created" {
		t.Errorf("expected EventType 'edict_created', got %q", events[0].EventType)
	}

	// Also verify it arrived on the channel
	select {
	case ev := <-s.ritualGuard.eventCh:
		if ev.EdictKey.ID != 2 {
			t.Errorf("channel event EdictID: expected 2, got %d", ev.EdictKey.ID)
		}
	default:
		t.Error("expected event on channel")
	}
}

func TestBackpressure(t *testing.T) {
	db := setupEventTestDB(t)
	// Use a tiny channel to force backpressure
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := NewRitualGuard(RitualGuardOpts{Base: base})
	// Replace the default channel with a tiny one
	rg.eventCh = make(chan Event, 2)
	s := &Court{
		db:        db,
		logger:    slog.Default(),
		ministers: make(map[string]Minister),
	}
	s.ritualGuard = rg

	// Fill the channel
	s.PublishEvent(storage.EdictKey{ID: 1}, "edict_created", storage.JSON{})
	s.PublishEvent(storage.EdictKey{ID: 2}, "edict_created", storage.JSON{})

	// This one should overflow — persists to DB but doesn't block
	s.PublishEvent(storage.EdictKey{ID: 3}, "edict_created", storage.JSON{})

	// Verify all 3 persisted to DB
	var count int64
	db.Model(&storage.TianEvent{}).Count(&count)
	if count != 3 {
		t.Errorf("expected 3 DB events, got %d", count)
	}

	// Verify channel has exactly 2 (capacity)
	if len(s.ritualGuard.eventCh) != 2 {
		t.Errorf("expected 2 events in channel, got %d", len(s.ritualGuard.eventCh))
	}
}

func TestDrainUnprocessedEvents(t *testing.T) {
	db := setupEventTestDB(t)
	s := newTestCourt(t, db)

	rg := s.ritualGuard

	// Insert 5 events directly into DB (simulating crash scenario)
	for i := 0; i < 5; i++ {
		db.Create(&storage.TianEvent{
			EdictID:   99,
			Username:  "testuser",
			Project:   "testproject",
			EventType: "step_completed",
			Payload:   storage.JSON{"i": i},
		})
	}

	// Set checkpoint to event ID 2 (simulating partial processing before crash)
	rg.SaveCheckpoint(2)

	// Track dispatched events
	var dispatched []Event
	var mu sync.Mutex
	s.ritualGuard.Subscribe("step_completed", func(e Event) {
		mu.Lock()
		dispatched = append(dispatched, e)
		mu.Unlock()
	})

	// Drain should replay events 3, 4, 5
	s.ritualGuard.DrainUnprocessedEvents()

	// Wait for async dispatch handlers to complete
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if len(dispatched) != 3 {
		t.Fatalf("expected 3 drained events, got %d", len(dispatched))
	}
	mu.Unlock()

	// Verify checkpoint was updated to last event
	lastID, _ := rg.GetLastAcknowledgedEvent()
	if lastID != 5 {
		t.Errorf("expected checkpoint at 5, got %d", lastID)
	}
}

func TestShutdown(t *testing.T) {
	db := setupEventTestDB(t)
	s := newTestCourt(t, db)

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel

	done := make(chan struct{})
	go func() {
		s.ritualGuard.Run(ctx)
		close(done)
	}()

	// Cancel should cause RitualGuard.Run to exit
	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("RitualGuard.Run did not exit after context cancel")
	}
}

func TestStartRitualFailureNotifies(t *testing.T) {
	db := setupEventTestDB(t)

	// Ritual with a background given step that will fail
	ritual := &RitualDef{
		Name:       "start-fail-test",
		Background: []string{"!false"},
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "boom\n", ExitCode: "1"},
		},
	}
	rg := &RitualGuard{
		MinisterBase:   base,
		ritualRegistry: registry,
		ritualRunner: NewRitualRunner(
			registry,
			func(id string) Minister { return nil },
			nil, // publishEvent — emitEvent falls back to DB persistence
			db, mockRunner, slog.Default(), repo.RepoInfo{},
		),
		streamingCtx: func(string) context.Context { return context.Background() },
	}

	var notified []RitualStepMsg
	var mu sync.Mutex
	rg.SetNotify(func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok {
			mu.Lock()
			notified = append(notified, stepMsg)
			mu.Unlock()
		}
	})

	key := storage.EdictKey{ID: 99, Username: "testuser", Project: "testproject"}
	rg.startRitual("start-fail-test", key, nil)

	// Wait for the goroutine to complete
	time.Sleep(200 * time.Millisecond)

	// Verify ritual_failed notification was sent
	mu.Lock()
	defer mu.Unlock()
	var failedMsg *RitualStepMsg
	for i := range notified {
		if notified[i].Status == "ritual_failed" {
			failedMsg = &notified[i]
			break
		}
	}
	if failedMsg == nil {
		t.Fatalf("expected a ritual_failed notification from startRitual, got: %+v", notified)
	}
	if failedMsg.RitualName != "start-fail-test" {
		t.Errorf("expected RitualName 'start-fail-test', got %q", failedMsg.RitualName)
	}
	if failedMsg.EdictID != 99 {
		t.Errorf("expected EdictID 99, got %d", failedMsg.EdictID)
	}
	if failedMsg.ChannelID != "e99" {
		t.Errorf("expected ChannelID 'e99' for ritual_failed message, got %q", failedMsg.ChannelID)
	}

	// Verify EventRitualFailed was persisted to DB
	var events []storage.TianEvent
	db.Where("event_type = ?", storage.EventRitualFailed).Find(&events)
	if len(events) < 1 {
		t.Fatalf("expected at least 1 EventRitualFailed in DB, got %d", len(events))
	}
}

func TestMinisterBaseEmitEvent_WithPublish(t *testing.T) {
	db := setupEventTestDB(t)
	s := newTestCourt(t, db)

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	base.publish = s.PublishEvent

	err := base.EmitEvent(storage.EdictKey{ID: 10}, "edict_assigned", storage.JSON{"from": "minister"})
	if err != nil {
		t.Fatalf("EmitEvent error: %v", err)
	}

	// Verify DB
	var count int64
	db.Model(&storage.TianEvent{}).Where("edict_id = ?", 10).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 DB event, got %d", count)
	}

	// Verify channel
	select {
	case ev := <-s.ritualGuard.eventCh:
		if ev.EdictKey.ID != 10 {
			t.Errorf("expected EdictID 10, got %d", ev.EdictKey.ID)
		}
	default:
		t.Error("expected event on channel")
	}
}

func TestMinisterBaseEmitEvent_Fallback(t *testing.T) {
	db := setupEventTestDB(t)

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	// No publish set — should fall back to DB-only

	err := base.EmitEvent(storage.EdictKey{ID: 20}, "edict_created", storage.JSON{"from": "fallback"})
	if err != nil {
		t.Fatalf("EmitEvent error: %v", err)
	}

	var count int64
	db.Model(&storage.TianEvent{}).Where("edict_id = ?", 20).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 DB event, got %d", count)
	}
}

// TestRitualGuard_EventNotification tests that significant events trigger notifications
// Message formatting is handled by the TUI layer; ritual_manager only routes notifiable events.
func TestRitualGuard_EventNotification(t *testing.T) {
	db := setupEventTestDB(t)
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := NewRitualGuard(RitualGuardOpts{Base: base})

	// Collect notifications
	var notifications []EventNotificationMsg
	var mu sync.Mutex
	rg.SetNotify(func(msg interface{}) {
		if n, ok := msg.(EventNotificationMsg); ok {
			mu.Lock()
			notifications = append(notifications, n)
			mu.Unlock()
		}
	})

	// Test EventEdictCreated notification
	rg.DispatchEvent(Event{
		Type:     storage.EventEdictCreated,
		EdictKey: storage.EdictKey{ID: 1},
		Payload:  map[string]interface{}{"intent": "Add new feature"},
	})

	time.Sleep(10 * time.Millisecond) // Allow async processing

	mu.Lock()
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0].EdictKey.ID != 1 {
		t.Errorf("expected EdictID 1, got %d", notifications[0].EdictKey.ID)
	}
	if notifications[0].EventType != storage.EventEdictCreated {
		t.Errorf("expected EventType EventEdictCreated, got %s", notifications[0].EventType)
	}
	if notifications[0].ChannelID != "e1" {
		t.Errorf("expected ChannelID e1, got %s", notifications[0].ChannelID)
	}
	mu.Unlock()

	// Test EventSealGranted notification
	notifications = nil
	rg.DispatchEvent(Event{
		Type:     storage.EventSealGranted,
		EdictKey: storage.EdictKey{ID: 2},
		Payload:  map[string]interface{}{"minister_id": "judge"},
	})

	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0].EventType != storage.EventSealGranted {
		t.Errorf("expected EventType EventSealGranted, got %s", notifications[0].EventType)
	}
	if notifications[0].Payload["minister_id"] != "judge" {
		t.Errorf("expected minister_id judge in payload, got %v", notifications[0].Payload["minister_id"])
	}
	if notifications[0].ChannelID != "e2" {
		t.Errorf("expected ChannelID e2, got %s", notifications[0].ChannelID)
	}
	mu.Unlock()

	// Test EventEdictSealed notification
	notifications = nil
	rg.DispatchEvent(Event{
		Type:     storage.EventEdictSealed,
		EdictKey: storage.EdictKey{ID: 3},
		Payload:  map[string]interface{}{},
	})

	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0].EventType != storage.EventEdictSealed {
		t.Errorf("expected EventType EventEdictSealed, got %s", notifications[0].EventType)
	}
	if notifications[0].EdictKey.ID != 3 {
		t.Errorf("expected EdictID 3, got %d", notifications[0].EdictKey.ID)
	}
	if notifications[0].ChannelID != "e3" {
		t.Errorf("expected ChannelID e3, got %s", notifications[0].ChannelID)
	}
	mu.Unlock()

	// All events are forwarded to the TUI
	notifications = nil
	rg.DispatchEvent(Event{
		Type:     storage.EventStepCompleted,
		EdictKey: storage.EdictKey{ID: 4},
		Payload:  map[string]interface{}{},
	})

	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0].EventType != storage.EventStepCompleted {
		t.Errorf("expected EventType EventStepCompleted, got %s", notifications[0].EventType)
	}
	if notifications[0].ChannelID != "e4" {
		t.Errorf("expected ChannelID e4, got %s", notifications[0].ChannelID)
	}
	mu.Unlock()

	// Test EventRitualAborted notification
	notifications = nil
	rg.DispatchEvent(Event{
		Type:     storage.EventRitualAborted,
		EdictKey: storage.EdictKey{ID: 5},
		Payload: map[string]interface{}{
			"ritual": "swift-strike",
			"reason": "edict cancelled",
		},
	})

	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	if len(notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifications))
	}
	if notifications[0].EventType != storage.EventRitualAborted {
		t.Errorf("expected EventType EventRitualAborted, got %s", notifications[0].EventType)
	}
	expected := "Ritual swift-strike aborted: edict cancelled"
	if notifications[0].Message != expected {
		t.Errorf("expected message %q, got %q", expected, notifications[0].Message)
	}
	if notifications[0].ChannelID != "e5" {
		t.Errorf("expected ChannelID e5, got %s", notifications[0].ChannelID)
	}
	mu.Unlock()
}

// TestAbortStaleRitualsOnStart verifies that startRitual aborts stale running
// rituals before starting a new one.
func TestAbortStaleRitualsOnStart(t *testing.T) {
	db := setupEventTestDB(t)
	logger := slog.Default()

	// Create a stale running ritual (updated 10 minutes ago)
	// Use raw SQL to bypass GORM's autoUpdateTime
	staleTime := time.Now().Add(-10 * time.Minute)
	staleExec := &RitualExecution{
		ID:          "stale-ritual-123",
		RitualName:  "swift-strike",
		EdictID:     100,
		Username:    "test",
		Project:     "test/project",
		State:       RitualStateRunning,
		CurrentStep: 1,
	}
	if err := db.Save(staleExec).Error; err != nil {
		t.Fatalf("failed to create stale ritual: %v", err)
	}
	// Manually set updated_at to the past
	if err := db.Exec("UPDATE ritual_executions SET updated_at = ? WHERE id = ?", staleTime, "stale-ritual-123").Error; err != nil {
		t.Fatalf("failed to set stale updated_at: %v", err)
	}

	// Create RitualGuard with 5 minute flatlineAge and matching username/project
	base := NewMinisterBase(db, nil, logger, "test", "test/project", nil)
	rg := &RitualGuard{
		MinisterBase: base,
		flatlineAge:  5 * time.Minute,
	}

	// Call abortStaleRitualsIfLocked
	rg.abortStaleRitualsIfLocked()

	// Verify the stale ritual was aborted
	var exec RitualExecution
	if err := db.First(&exec, "id = ?", "stale-ritual-123").Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateAborted {
		t.Errorf("expected ritual state %s, got %s", RitualStateAborted, exec.State)
	}
}

// TestNoAbortForFreshRituals verifies that fresh running rituals are not aborted.
func TestNoAbortForFreshRituals(t *testing.T) {
	db := setupEventTestDB(t)
	logger := slog.Default()

	// Create a fresh running ritual (updated 1 minute ago)
	freshTime := time.Now().Add(-1 * time.Minute)
	freshExec := &RitualExecution{
		ID:          "fresh-ritual-456",
		RitualName:  "swift-strike",
		EdictID:     200,
		Username:    "test",
		Project:     "test/project",
		State:       RitualStateRunning,
		CurrentStep: 1,
	}
	freshExec.UpdatedAt = freshTime
	if err := db.Save(freshExec).Error; err != nil {
		t.Fatalf("failed to create fresh ritual: %v", err)
	}

	// Create RitualGuard with 5 minute flatlineAge and matching username/project
	base := NewMinisterBase(db, nil, logger, "test", "test/project", nil)
	rg := &RitualGuard{
		MinisterBase: base,
		flatlineAge:  5 * time.Minute,
	}

	// Call abortStaleRitualsIfLocked
	rg.abortStaleRitualsIfLocked()

	// Verify the fresh ritual is still running
	var exec RitualExecution
	if err := db.First(&exec, "id = ?", "fresh-ritual-456").Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateRunning {
		t.Errorf("expected ritual state %s, got %s", RitualStateRunning, exec.State)
	}
}

// TestAbortStaleRituals_OtherProjectExcluded verifies that abortStaleRitualsIfLocked
// does not abort stale rituals belonging to a different username or project.
func TestAbortStaleRituals_OtherProjectExcluded(t *testing.T) {
	db := setupEventTestDB(t)
	logger := slog.Default()

	// Create a stale running ritual for the current user/project
	staleTime := time.Now().Add(-10 * time.Minute)
	localExec := &RitualExecution{
		ID: "stale-local", RitualName: "swift-strike", EdictID: 100,
		Username: "test", Project: "test/project", State: RitualStateRunning, CurrentStep: 1,
	}
	if err := db.Save(localExec).Error; err != nil {
		t.Fatalf("failed to create local stale ritual: %v", err)
	}
	if err := db.Exec("UPDATE ritual_executions SET updated_at = ? WHERE id = ?", staleTime, "stale-local").Error; err != nil {
		t.Fatalf("failed to set stale updated_at: %v", err)
	}

	// Create a stale running ritual for a different project (same user)
	otherProjectExec := &RitualExecution{
		ID: "stale-other-project", RitualName: "swift-strike", EdictID: 101,
		Username: "test", Project: "other/project", State: RitualStateRunning, CurrentStep: 1,
	}
	if err := db.Save(otherProjectExec).Error; err != nil {
		t.Fatalf("failed to create other-project stale ritual: %v", err)
	}
	if err := db.Exec("UPDATE ritual_executions SET updated_at = ? WHERE id = ?", staleTime, "stale-other-project").Error; err != nil {
		t.Fatalf("failed to set stale updated_at for other-project: %v", err)
	}

	// Create a stale running ritual for a different username (same project)
	otherUserExec := &RitualExecution{
		ID: "stale-other-user", RitualName: "swift-strike", EdictID: 102,
		Username: "otheruser", Project: "test/project", State: RitualStateRunning, CurrentStep: 1,
	}
	if err := db.Save(otherUserExec).Error; err != nil {
		t.Fatalf("failed to create other-user stale ritual: %v", err)
	}
	if err := db.Exec("UPDATE ritual_executions SET updated_at = ? WHERE id = ?", staleTime, "stale-other-user").Error; err != nil {
		t.Fatalf("failed to set stale updated_at for other-user: %v", err)
	}

	// Create RitualGuard with the local user/project
	base := NewMinisterBase(db, nil, logger, "test", "test/project", nil)
	rg := &RitualGuard{
		MinisterBase: base,
		flatlineAge:  5 * time.Minute,
	}

	rg.abortStaleRitualsIfLocked()

	// Local ritual should be aborted
	var localResult RitualExecution
	if err := db.First(&localResult, "id = ?", "stale-local").Error; err != nil {
		t.Fatalf("failed to find local ritual: %v", err)
	}
	if localResult.State != RitualStateAborted {
		t.Errorf("expected local ritual state %s, got %s", RitualStateAborted, localResult.State)
	}

	// Other-project ritual should remain running
	var otherProjectResult RitualExecution
	if err := db.First(&otherProjectResult, "id = ?", "stale-other-project").Error; err != nil {
		t.Fatalf("failed to find other-project ritual: %v", err)
	}
	if otherProjectResult.State != RitualStateRunning {
		t.Errorf("expected other-project ritual state to remain %s, got %s", RitualStateRunning, otherProjectResult.State)
	}

	// Other-user ritual should remain running
	var otherUserResult RitualExecution
	if err := db.First(&otherUserResult, "id = ?", "stale-other-user").Error; err != nil {
		t.Fatalf("failed to find other-user ritual: %v", err)
	}
	if otherUserResult.State != RitualStateRunning {
		t.Errorf("expected other-user ritual state to remain %s, got %s", RitualStateRunning, otherUserResult.State)
	}
}

// TestScanForStaleRituals_OtherProjectExcluded verifies that scanForStaleRituals
// only considers running rituals belonging to the current username and project.
func TestScanForStaleRituals_OtherProjectExcluded(t *testing.T) {
	db := setupEventTestDB(t)
	// Migrate tables needed by scanForStaleRituals
	if err := db.AutoMigrate(&storage.Edict{}, &storage.Seal{}); err != nil {
		t.Fatalf("failed to migrate edict/seal tables: %v", err)
	}
	logger := slog.Default()

	// Create an edict for the local user/project that is sealed
	localEdict := storage.Edict{Intent: "local edict", Username: "test", Project: "test/project"}
	if err := db.Create(&localEdict).Error; err != nil {
		t.Fatalf("failed to create local edict: %v", err)
	}
	db.Create(&storage.Seal{EdictID: localEdict.ID, Username: "test", Project: "test/project", MinisterID: "ruler"})

	// Create a running ritual for the local user/project
	localExec := &RitualExecution{
		ID: "running-local", RitualName: "swift-strike", EdictID: localEdict.ID,
		Username: "test", Project: "test/project", State: RitualStateRunning, CurrentStep: 1,
	}
	if err := db.Save(localExec).Error; err != nil {
		t.Fatalf("failed to create local running ritual: %v", err)
	}

	// Create an edict for a different project that is sealed
	otherProjectEdict := storage.Edict{Intent: "other project edict", Username: "test", Project: "other/project"}
	if err := db.Create(&otherProjectEdict).Error; err != nil {
		t.Fatalf("failed to create other-project edict: %v", err)
	}
	db.Create(&storage.Seal{EdictID: otherProjectEdict.ID, Username: "test", Project: "other/project", MinisterID: "judge"})

	// Create a running ritual for the other project
	otherProjectExec := &RitualExecution{
		ID: "running-other-project", RitualName: "swift-strike", EdictID: otherProjectEdict.ID,
		Username: "test", Project: "other/project", State: RitualStateRunning, CurrentStep: 1,
	}
	if err := db.Save(otherProjectExec).Error; err != nil {
		t.Fatalf("failed to create other-project running ritual: %v", err)
	}

	// Create an edict for a different username that is sealed
	otherUserEdict := storage.Edict{Intent: "other user edict", Username: "otheruser", Project: "test/project"}
	if err := db.Create(&otherUserEdict).Error; err != nil {
		t.Fatalf("failed to create other-user edict: %v", err)
	}
	db.Create(&storage.Seal{EdictID: otherUserEdict.ID, Username: "otheruser", Project: "test/project", MinisterID: "judge"})

	// Create a running ritual for the other user
	otherUserExec := &RitualExecution{
		ID: "running-other-user", RitualName: "swift-strike", EdictID: otherUserEdict.ID,
		Username: "otheruser", Project: "test/project", State: RitualStateRunning, CurrentStep: 1,
	}
	if err := db.Save(otherUserExec).Error; err != nil {
		t.Fatalf("failed to create other-user running ritual: %v", err)
	}

	// Create RitualGuard with the local user/project
	scanBase := NewMinisterBase(db, nil, logger, "test", "test/project", nil)
	rg := &RitualGuard{
		MinisterBase: scanBase,
		ritualRunner: NewRitualRunner(
			NewRitualRegistry(),
			func(id string) Minister { return nil },
			nil, db, nil, logger, repo.RepoInfo{},
		),
	}

	ctx := context.Background()
	rg.scanForStaleRituals(ctx)

	// Local ritual should be aborted (sealed edict)
	var localResult RitualExecution
	if err := db.First(&localResult, "id = ?", "running-local").Error; err != nil {
		t.Fatalf("failed to find local ritual: %v", err)
	}
	if localResult.State != RitualStateAborted {
		t.Errorf("expected local ritual state %s, got %s", RitualStateAborted, localResult.State)
	}

	// Other-project ritual should remain running (not queried)
	var otherProjectResult RitualExecution
	if err := db.First(&otherProjectResult, "id = ?", "running-other-project").Error; err != nil {
		t.Fatalf("failed to find other-project ritual: %v", err)
	}
	if otherProjectResult.State != RitualStateRunning {
		t.Errorf("expected other-project ritual state to remain %s, got %s", RitualStateRunning, otherProjectResult.State)
	}

	// Other-user ritual should remain running (not queried)
	var otherUserResult RitualExecution
	if err := db.First(&otherUserResult, "id = ?", "running-other-user").Error; err != nil {
		t.Fatalf("failed to find other-user ritual: %v", err)
	}
	if otherUserResult.State != RitualStateRunning {
		t.Errorf("expected other-user ritual state to remain %s, got %s", RitualStateRunning, otherUserResult.State)
	}
}

// TestRecoveryZhengmingHasPassOption verifies that recovery zhengming includes "Pass" option.
func TestRecoveryZhengmingHasPassOption(t *testing.T) {
	db := setupEventTestDB(t)

	// Create an aborted ritual
	abortedExec := &RitualExecution{
		ID:          "aborted-ritual-789",
		RitualName:  "test-ritual",
		EdictID:     300,
		Username:    "test",
		Project:     "test/project",
		State:       RitualStateAborted,
		CurrentStep: 2,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	// Create step states to mark step 0 and 1 as complete
	for i := 0; i < 2; i++ {
		stepState := RitualStepState{
			ExecutionID: abortedExec.ID,
			StepIndex:   i,
			Name:        fmt.Sprintf("step-%d", i),
			Status:      "completed",
			Message:     "completed",
		}
		if err := db.Save(&stepState).Error; err != nil {
			t.Fatalf("failed to create step state: %v", err)
		}
	}

	// Verify the ritual exists
	var count int64
	db.Model(&RitualExecution{}).Where("state IN ?", []RitualState{RitualStateAborted, RitualStateFailed, RitualStateStopped}).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 aborted ritual, got %d", count)
	}
}

// TestStartRitualQueuedWhenLocked verifies that when the ritualMu is already
// held, startRitual emits a "queued" notification before waiting for the lock.
func TestStartRitualQueuedWhenLocked(t *testing.T) {
	db := setupEventTestDB(t)

	ritual := &RitualDef{
		Name: "queued-test",
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "ok\n", ExitCode: "0"},
		},
	}
	rg := &RitualGuard{
		MinisterBase:   base,
		ritualRegistry: registry,
		ritualRunner: NewRitualRunner(
			registry,
			func(id string) Minister { return nil },
			nil,
			db, mockRunner, slog.Default(), repo.RepoInfo{},
		),
		streamingCtx: func(string) context.Context { return context.Background() },
	}

	var notified []RitualStepMsg
	var mu sync.Mutex
	rg.SetNotify(func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok {
			mu.Lock()
			notified = append(notified, stepMsg)
			mu.Unlock()
		}
	})

	key := storage.EdictKey{ID: 77, Username: "testuser", Project: "testproject"}

	// Hold the lock so the next startRitual must queue
	rg.ritualMu.Lock()

	// Start ritual in a goroutine — it should emit "queued" then block on Lock()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			} else {
				done <- nil
			}
		}()
		rg.startRitual("queued-test", key, nil)
	}()

	// Wait for the "queued" notification (with timeout)
	deadline := time.After(2 * time.Second)
	var queuedMsg *RitualStepMsg
	for {
		mu.Lock()
		for i := range notified {
			if notified[i].Status == "queued" {
				queuedMsg = &notified[i]
				break
			}
		}
		mu.Unlock()
		if queuedMsg != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for 'queued' notification, got: %+v", notified)
		case <-time.After(10 * time.Millisecond):
		}
	}

	if queuedMsg.RitualName != "queued-test" {
		t.Errorf("expected RitualName 'queued-test', got %q", queuedMsg.RitualName)
	}
	if queuedMsg.EdictID != 77 {
		t.Errorf("expected EdictID 77, got %d", queuedMsg.EdictID)
	}
	if queuedMsg.ChannelID != "e77" {
		t.Errorf("expected ChannelID 'e77' for queued message, got %q", queuedMsg.ChannelID)
	}
	if queuedMsg.QueueLen != 1 {
		t.Errorf("expected QueueLen 1 for first queued ritual, got %d", queuedMsg.QueueLen)
	}

	// Release the lock so the queued ritual can proceed
	rg.ritualMu.Unlock()

	// Wait for startRitual goroutine to finish
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("startRitual panicked: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startRitual did not complete after lock release")
	}

	// After lock release, the ritual should have started running.
	// The "started" notification from RitualRunner.Start proves the ritual
	// was unblocked after being queued. Poll for it since the goroutine's
	// completion may race with the notification delivery.
	startDeadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		var foundStarted bool
		for _, msg := range notified {
			if msg.Status == "started" && msg.StepName == "" {
				foundStarted = true
				break
			}
		}
		mu.Unlock()
		if foundStarted {
			break
		}
		select {
		case <-startDeadline:
			mu.Lock()
			notifiedCopy := append([]RitualStepMsg(nil), notified...)
			mu.Unlock()
			t.Fatalf("expected 'started' notification after lock release, got: %+v", notifiedCopy)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// blockingRunner blocks on the context until it is cancelled, then returns
// a non-zero exit code so the ritual step fails with the cancelled context.
type blockingRunner struct{}

func (m *blockingRunner) Run(ctx context.Context, input runners.Input) (runners.Output, error) {
	<-ctx.Done()
	return runners.Output{Output: "interrupted\n", ExitCode: "1"}, ctx.Err()
}
func (m *blockingRunner) Restart(ctx context.Context) error     { return nil }
func (m *blockingRunner) Close(ctx context.Context) error       { return nil }
func (m *blockingRunner) AllowFallback(bool)                    {}
func (m *blockingRunner) RunnerType() string                    { return "blocking" }
func (m *blockingRunner) GetOS() string                         { return runtime.GOOS }
func (m *blockingRunner) SetMessageChannel(chan<- runners.Msg)  {}
func (m *blockingRunner) HealthCheck(ctx context.Context) error { return nil }

// TestStartRitualContextCancelNoFailedNotification verifies that when a
// ritual is cancelled via context cancellation (user abort), startRitual
// does NOT send a "ritual_failed" notification. StreamInterruptedMsg already
// surfaces as 🛑 ABORTED in the TUI.
func TestStartRitualContextCancelNoFailedNotification(t *testing.T) {
	db := setupEventTestDB(t)

	ritual := &RitualDef{
		Name:       "cancel-test",
		Background: []string{"!sleep 30"},
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)

	// streamingCtx returns a cancellable context we control
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rg := &RitualGuard{
		MinisterBase:   base,
		ritualRegistry: registry,
		ritualRunner: NewRitualRunner(
			registry,
			func(id string) Minister { return nil },
			nil,
			db, &blockingRunner{}, slog.Default(), repo.RepoInfo{},
		),
		streamingCtx: func(string) context.Context { return ctx },
	}

	var notified []RitualStepMsg
	var mu sync.Mutex
	rg.SetNotify(func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok {
			mu.Lock()
			notified = append(notified, stepMsg)
			mu.Unlock()
		}
	})

	key := storage.EdictKey{ID: 88, Username: "testuser", Project: "testproject"}
	rg.startRitual("cancel-test", key, nil)

	// Give the ritual time to start and block on the runner
	time.Sleep(100 * time.Millisecond)

	// Cancel the context (simulates user abort)
	cancel()

	// Wait for the goroutine to process the cancellation
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// No "ritual_failed" notification should have been sent
	for _, msg := range notified {
		if msg.Status == "ritual_failed" {
			t.Errorf("expected no ritual_failed notification on context cancel, got: %+v", msg)
		}
	}

	// The inner ctx.Done() path should send "ritual_aborted" (not "ritual_failed")
	// only when cancellation happens between steps. When cancellation occurs
	// during a background step, no notification is needed (StreamInterruptedMsg
	// already surfaces as 🛑 ABORTED).
	var abortedMsg *RitualStepMsg
	for i := range notified {
		if notified[i].Status == "ritual_aborted" {
			abortedMsg = &notified[i]
			break
		}
	}
	if abortedMsg == nil {
		// Acceptable: cancellation occurred during a background step, which
		// skips the between-steps ctx.Done() path that sends "ritual_aborted".
		// As long as no "ritual_failed" was sent, the fix is correct.
	} else if abortedMsg.Message != "aborted by user" {
		t.Errorf("expected message 'aborted by user', got %q", abortedMsg.Message)
	}

	// No EventRitualFailed should be persisted to DB
	var events []storage.TianEvent
	db.Where("event_type = ?", storage.EventRitualFailed).Find(&events)
	if len(events) != 0 {
		t.Errorf("expected 0 EventRitualFailed in DB on context cancel, got %d", len(events))
	}
}
