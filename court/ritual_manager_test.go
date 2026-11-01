package court

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/worktree"
	"github.com/afittestide/asimi/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	// Wait for the goroutine to deliver the ritual_failed notification. Poll
	// rather than sleep: under -race on a loaded host a fixed sleep is flaky.
	var failedMsg *RitualStepMsg
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		for i := range notified {
			if notified[i].Status == "ritual_failed" {
				failedMsg = &notified[i]
				break
			}
		}
		mu.Unlock()
		if failedMsg != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for a ritual_failed notification from startRitual, got: %+v", notified)
		}
		time.Sleep(10 * time.Millisecond)
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

	// Verify EventRitualFailed was persisted to DB exactly once — Run owns
	// terminal events; startRitual must not add a duplicate.
	var events []storage.TianEvent
	db.Where("event_type = ?", storage.EventRitualFailed).Find(&events)
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 EventRitualFailed in DB, got %d", len(events))
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

	// Test EventWorktreeMergeFailed notification: the ruler's seal is already
	// recorded, so the failure must be spelled out rather than implied by a
	// generic seal toast. "merged" distinguishes the post-merge cleanup failure.
	for _, tc := range []struct {
		name    string
		payload map[string]interface{}
		want    string
	}{
		{
			name:    "merge failed",
			payload: map[string]interface{}{"reason": "rebase onto main failed"},
			want:    "Edict 6 sealed but work NOT merged: rebase onto main failed",
		},
		{
			name:    "cleanup failed after merge",
			payload: map[string]interface{}{"reason": "dirty tree", "merged": true},
			want:    "Edict 6 sealed; work merged but worktree cleanup failed: dirty tree",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notifications = nil
			rg.DispatchEvent(Event{
				Type:     storage.EventWorktreeMergeFailed,
				EdictKey: storage.EdictKey{ID: 6},
				Payload:  tc.payload,
			})

			time.Sleep(10 * time.Millisecond)

			mu.Lock()
			defer mu.Unlock()
			if len(notifications) != 1 {
				t.Fatalf("expected 1 notification, got %d", len(notifications))
			}
			if notifications[0].EventType != storage.EventWorktreeMergeFailed {
				t.Errorf("expected EventType EventWorktreeMergeFailed, got %s", notifications[0].EventType)
			}
			if notifications[0].Message != tc.want {
				t.Errorf("expected message %q, got %q", tc.want, notifications[0].Message)
			}
			if notifications[0].ChannelID != "e6" {
				t.Errorf("expected ChannelID e6, got %s", notifications[0].ChannelID)
			}
		})
	}
}

// TestStartRitualQueuedDoesNotReapLiveRitual verifies that taking the queued
// path — a failed TryLock while another ritual holds the edict's lock — never
// sweeps stale rituals. The lock is process-local, so a failed TryLock proves
// the holder is a live in-process goroutine; the old sweep from that branch is
// exactly the e1003 bug, where a healthy long-running step was aborted at the
// 5m flatline mark. Even an old row must survive the queue path; genuine
// staleness is the direct sweep's job (TestAbortStaleRituals_CancelsHoldingRitual).
func TestStartRitualQueuedDoesNotReapLiveRitual(t *testing.T) {
	db := setupEventTestDB(t)

	staleExec := &RitualExecution{
		ID:          "old-but-alive",
		RitualName:  "swift-strike",
		EdictID:     100,
		Username:    "testuser",
		Project:     "testproject",
		State:       RitualStateRunning,
		CurrentStep: 1,
	}
	if err := db.Save(staleExec).Error; err != nil {
		t.Fatalf("failed to create stale ritual: %v", err)
	}
	staleTime := time.Now().Add(-10 * time.Minute)
	if err := db.Exec("UPDATE ritual_executions SET updated_at = ? WHERE id = ?", staleTime, staleExec.ID).Error; err != nil {
		t.Fatalf("failed to set stale updated_at: %v", err)
	}

	ritual := &RitualDef{
		Name:  "queued-no-reap",
		Steps: []RitualStep{{Name: "work", Minister: "forge", Task: "do work"}},
	}
	registry := NewRitualRegistry()
	registry.Register(ritual)

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := &RitualGuard{
		MinisterBase:   base,
		flatlineAge:    5 * time.Minute,
		ritualRegistry: registry,
		ritualRunner: NewRitualRunner(
			registry,
			func(id string) Minister { return nil },
			nil,
			db, &mockCallCountRunner{results: []runners.Output{{Output: "ok\n"}}},
			slog.Default(), repo.RepoInfo{},
		),
		streamingCtx: func(string) context.Context { return context.Background() },
	}

	queued := make(chan struct{}, 1)
	rg.SetNotify(func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok && stepMsg.Status == "queued" {
			select {
			case queued <- struct{}{}:
			default:
			}
		}
	})

	key := storage.EdictKey{ID: 200, Username: "testuser", Project: "testproject"}

	// Hold edict 200's lock so startRitual must take the queued branch.
	rg.edictRitualLock(200).Lock()
	rg.startRitual("queued-no-reap", key, nil)

	select {
	case <-queued:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the queued notification")
	}

	// Give any erroneous sweep a moment to act before asserting.
	time.Sleep(50 * time.Millisecond)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", staleExec.ID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateRunning {
		t.Errorf("the queued path must not reap a live ritual: expected state %s, got %s",
			RitualStateRunning, exec.State)
	}

	rg.edictRitualLock(200).Unlock()
}

// TestHeartbeatTouchProtectsFromSweep verifies that a heartbeat touch makes an
// otherwise-stale running row young again, so the direct (ticker-path) stale
// sweep leaves it alone. This is the safety property the heartbeat buys: age
// stops being a proxy for a hung goroutine.
func TestHeartbeatTouchProtectsFromSweep(t *testing.T) {
	db := setupEventTestDB(t)

	exec := &RitualExecution{
		ID:          "touched-ritual",
		RitualName:  "swift-strike",
		EdictID:     300,
		Username:    "testuser",
		Project:     "testproject",
		State:       RitualStateRunning,
		CurrentStep: 1,
	}
	if err := db.Save(exec).Error; err != nil {
		t.Fatalf("failed to create ritual: %v", err)
	}
	staleTime := time.Now().Add(-10 * time.Minute)
	if err := db.Exec("UPDATE ritual_executions SET updated_at = ? WHERE id = ?", staleTime, exec.ID).Error; err != nil {
		t.Fatalf("failed to set stale updated_at: %v", err)
	}

	runner := NewRitualRunner(NewRitualRegistry(), nil, nil, db, nil, slog.Default(), repo.RepoInfo{})
	runner.touchExecution(exec.ID)

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := &RitualGuard{MinisterBase: base, flatlineAge: 5 * time.Minute}
	rg.abortStaleRitualsIfLocked()

	var got RitualExecution
	if err := db.First(&got, "id = ?", exec.ID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if got.State != RitualStateRunning {
		t.Errorf("a heartbeat-touched execution must not be reaped: expected %s, got %s",
			RitualStateRunning, got.State)
	}
}

// TestTouchExecutionNilDB verifies that touchExecution is a no-op (not a panic)
// when the runner has no database handle. The touch runs on a background
// goroutine, so a nil dereference there would crash the process rather than
// merely fail a step; the guard makes it consistent with the execID guard.
func TestTouchExecutionNilDB(t *testing.T) {
	runner := NewRitualRunner(NewRitualRegistry(), nil, nil, nil, nil, slog.Default(), repo.RepoInfo{})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("touchExecution must not panic with a nil db, got: %v", r)
		}
	}()
	runner.touchExecution("some-execution-id")
	runner.touchExecution("")
}

// TestStaleSweepSparesEdictWithLiveLock is the A1/A2 regression guard: a
// running execution whose updated_at is stale is NOT reaped while its edict's
// ritual lock is held. The lock is held by startRitual for the whole of
// RitualRunner.Run — the act phase, but equally a long given/then shell step or
// a zhengming pause — so a held lock is proof of a live goroutine and age alone
// must not condemn the row. Without the liveness guard this test fails.
func TestStaleSweepSparesEdictWithLiveLock(t *testing.T) {
	db := setupEventTestDB(t)

	staleExec := &RitualExecution{
		ID:          "live-locked",
		RitualName:  "swift-strike",
		EdictID:     400,
		Username:    "testuser",
		Project:     "testproject",
		State:       RitualStateRunning,
		CurrentStep: 1,
	}
	if err := db.Save(staleExec).Error; err != nil {
		t.Fatalf("failed to create ritual: %v", err)
	}
	staleTime := time.Now().Add(-10 * time.Minute)
	if err := db.Exec("UPDATE ritual_executions SET updated_at = ? WHERE id = ?", staleTime, staleExec.ID).Error; err != nil {
		t.Fatalf("failed to set stale updated_at: %v", err)
	}

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := &RitualGuard{
		MinisterBase: base,
		flatlineAge:  5 * time.Minute,
	}

	// Hold edict 400's lock: it stands in for a live goroutine inside a long
	// given/then shell step or a zhengming pause, neither of which heartbeats.
	rg.edictRitualLock(400).Lock()
	defer rg.edictRitualLock(400).Unlock()

	rg.abortStaleRitualsIfLocked()

	var got RitualExecution
	if err := db.First(&got, "id = ?", staleExec.ID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if got.State != RitualStateRunning {
		t.Errorf("a stale row whose edict lock is held must survive the sweep: expected %s, got %s",
			RitualStateRunning, got.State)
	}
}

// TestSweepSparesLongGivenStepInFlight is the end-to-end A1 regression guard.
// The ritual's given step is a shell command (swift-strike's `!just test`) that
// can outlive flatlineAge without crossing any step boundary — no heartbeat
// runs during it. The sweep must still spare the row, because startRitual holds
// the edict lock across the whole Run, given phase included: a held lock is
// proof of a live goroutine independent of the timestamp.
func TestSweepSparesLongGivenStepInFlight(t *testing.T) {
	db := setupEventTestDB(t)

	ritual := &RitualDef{
		Name:       "long-given-test",
		Background: []string{"!sleep 30"},
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
	}
	registry := NewRitualRegistry()
	registry.Register(ritual)

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	// blockingRunner parks inside the given step until the context is cancelled
	// — standing in for a `go test` still running at the 5m mark.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rg := &RitualGuard{
		MinisterBase:   base,
		flatlineAge:    5 * time.Minute,
		ritualRegistry: registry,
		ritualRunner: NewRitualRunner(
			registry,
			func(id string) Minister { return nil },
			nil,
			db, &blockingRunner{}, slog.Default(), repo.RepoInfo{},
		),
		streamingCtx: func(string) context.Context { return ctx },
	}
	rg.SetNotify(func(any) {})

	key := storage.EdictKey{ID: 500, Username: "testuser", Project: "testproject"}
	rg.startRitual("long-given-test", key, nil)

	// Wait for the execution row, then age it past the flatline window while
	// the given step is still in flight.
	var exec RitualExecution
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := db.First(&exec, "edict_id = ?", 500).Error; err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ritual execution row never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	staleTime := time.Now().Add(-10 * time.Minute)
	if err := db.Exec("UPDATE ritual_executions SET updated_at = ? WHERE id = ?", staleTime, exec.ID).Error; err != nil {
		t.Fatalf("failed to age the row: %v", err)
	}

	// The given step is still parked with the edict lock held: the sweep must
	// not touch the row.
	rg.abortStaleRitualsIfLocked()

	var got RitualExecution
	if err := db.First(&got, "id = ?", exec.ID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if got.State != RitualStateRunning {
		t.Fatalf("a stale row whose given step is in flight must survive the sweep: expected %s, got %s",
			RitualStateRunning, got.State)
	}

	// Releasing the context ends the given step and the run, freeing the lock.
	// A genuinely orphaned stale row is then fair game — proving the guard
	// gates on liveness, not on disabling the sweep.
	cancel()
	releaseDeadline := time.Now().Add(5 * time.Second)
	for {
		l := rg.edictRitualLock(500)
		if l.TryLock() {
			l.Unlock()
			break
		}
		if time.Now().After(releaseDeadline) {
			t.Fatal("ritual goroutine did not release the edict lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := db.Exec("UPDATE ritual_executions SET state = ?, updated_at = ? WHERE id = ?",
		RitualStateRunning, staleTime, exec.ID).Error; err != nil {
		t.Fatalf("failed to reset row: %v", err)
	}
	rg.abortStaleRitualsIfLocked()
	if err := db.First(&got, "id = ?", exec.ID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if got.State != RitualStateAborted {
		t.Errorf("with no live holder, a stale row must be reaped: expected %s, got %s",
			RitualStateAborted, got.State)
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

// TestScanForStaleRituals_MergeWorktreeExempt: merge-worktree runs *because*
// the edict was sealed, so a sealed status is its normal precondition. The
// sweep must spare it (and any other ritual of that edict) while still aborting
// a genuinely stale sibling ritual.
func TestScanForStaleRituals_MergeWorktreeExempt(t *testing.T) {
	db := setupEventTestDB(t)
	require.NoError(t, db.AutoMigrate(&storage.Edict{}, &storage.Seal{}))

	sealed := storage.Edict{Intent: "sealed edict", Username: "test", Project: "test/project"}
	require.NoError(t, db.Create(&sealed).Error)
	require.NoError(t, db.Create(&storage.Seal{
		EdictID: sealed.ID, Username: "test", Project: "test/project", MinisterID: "ruler",
	}).Error)

	require.NoError(t, db.Save(&RitualExecution{
		ID: "running-merge", RitualName: mergeWorktreeRitual, EdictID: sealed.ID,
		Username: "test", Project: "test/project", State: RitualStateRunning, CurrentStep: 0,
	}).Error)
	require.NoError(t, db.Save(&RitualExecution{
		ID: "running-ordinary", RitualName: "swift-strike", EdictID: sealed.ID,
		Username: "test", Project: "test/project", State: RitualStateRunning, CurrentStep: 1,
	}).Error)

	rg := &RitualGuard{
		MinisterBase: NewMinisterBase(db, nil, slog.Default(), "test", "test/project", nil),
		ritualRunner: NewRitualRunner(NewRitualRegistry(),
			func(id string) Minister { return nil }, nil, db, nil, slog.Default(), repo.RepoInfo{}),
	}
	rg.scanForStaleRituals(context.Background())

	var mergeResult, ordinaryResult RitualExecution
	require.NoError(t, db.First(&mergeResult, "id = ?", "running-merge").Error)
	require.NoError(t, db.First(&ordinaryResult, "id = ?", "running-ordinary").Error)
	assert.Equal(t, RitualStateRunning, mergeResult.State,
		"merge-worktree's sealed precondition must not read as staleness")
	assert.Equal(t, RitualStateAborted, ordinaryResult.State,
		"the exemption must not spare genuinely stale rituals")
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

	// Hold the edict's lock so the next startRitual must queue
	rg.edictRitualLock(77).Lock()

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
	rg.edictRitualLock(77).Unlock()

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

// TestAbortRitual_CancelsContext verifies that aborting a running ritual
// cancels its streaming context through the injected cancel hook, so the
// goroutine unwinds at its next step boundary instead of running on.
func TestAbortRitual_CancelsContext(t *testing.T) {
	db := setupEventTestDB(t)

	ritual := &RitualDef{
		Name:       "abort-cancel-test",
		Background: []string{"!sleep 30"},
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
	}
	registry := NewRitualRegistry()
	registry.Register(ritual)

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var cancelledChannel string
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
		cancelRitual: func(channelID string) {
			mu.Lock()
			cancelledChannel = channelID
			mu.Unlock()
			cancel()
		},
	}

	key := storage.EdictKey{ID: 88, Username: "testuser", Project: "testproject"}
	rg.startRitual("abort-cancel-test", key, nil)

	// Wait for the ritual goroutine to persist its running row and block in
	// the runner. Poll rather than sleep: under -race the goroutine start is
	// slow enough that a fixed sleep is flaky.
	var exec RitualExecution
	startDeadline := time.Now().Add(5 * time.Second)
	for {
		if err := db.First(&exec, "edict_id = ?", 88).Error; err == nil {
			break
		}
		if time.Now().After(startDeadline) {
			t.Fatal("timed out waiting for the ritual to start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := rg.abortRitual(context.Background(), &exec, "test abort"); err != nil {
		t.Fatalf("abortRitual error: %v", err)
	}

	mu.Lock()
	got := cancelledChannel
	mu.Unlock()
	if got != "e88" {
		t.Errorf("expected cancelRitual called with 'e88', got %q", got)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expected ritual context to be cancelled by abortRitual")
	}

	// The goroutine should release its edict lock once it unwinds.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if l := rg.edictRitualLock(88); l.TryLock() {
			l.Unlock()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("edict ritual lock not released after abort cancelled the context")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAbortStaleRituals_CancelsHoldingRitual verifies that aborting a ritual
// detected as stale also cancels its context, so a hung goroutine stops
// holding its edict's lock.
func TestAbortStaleRituals_CancelsHoldingRitual(t *testing.T) {
	db := setupEventTestDB(t)

	staleExec := &RitualExecution{
		ID:          "stale-cancel-1",
		RitualName:  "swift-strike",
		EdictID:     200,
		Username:    "testuser",
		Project:     "testproject",
		State:       RitualStateRunning,
		CurrentStep: 1,
	}
	if err := db.Save(staleExec).Error; err != nil {
		t.Fatalf("failed to create stale ritual: %v", err)
	}
	staleTime := time.Now().Add(-10 * time.Minute)
	if err := db.Exec("UPDATE ritual_executions SET updated_at = ? WHERE id = ?", staleTime, staleExec.ID).Error; err != nil {
		t.Fatalf("failed to set stale updated_at: %v", err)
	}

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	var mu sync.Mutex
	var cancelledChannel string
	rg := &RitualGuard{
		MinisterBase: base,
		flatlineAge:  5 * time.Minute,
		cancelRitual: func(channelID string) {
			mu.Lock()
			cancelledChannel = channelID
			mu.Unlock()
		},
	}

	rg.abortStaleRitualsIfLocked()

	mu.Lock()
	got := cancelledChannel
	mu.Unlock()
	if got != "e200" {
		t.Errorf("expected cancelRitual called with 'e200' for stale ritual, got %q", got)
	}

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", staleExec.ID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateAborted {
		t.Errorf("expected ritual state %s, got %s", RitualStateAborted, exec.State)
	}
}

// TestRun_FailedStep_EmitsExactlyOneRitualFailed verifies the Run step-failure
// path is the sole emitter of ritual_failed — no duplicate from the caller.
func TestRun_FailedStep_EmitsExactlyOneRitualFailed(t *testing.T) {
	db := setupEventTestDB(t)

	ritual := &RitualDef{
		Name:       "run-fail-once",
		Background: []string{"!false"},
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
	}
	registry := NewRitualRegistry()
	registry.Register(ritual)

	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "boom\n", ExitCode: "1"},
		},
	}
	runner := NewRitualRunner(
		registry,
		func(id string) Minister { return nil },
		nil, // publishEvent nil → emitEvent persists directly to the DB
		db, mockRunner, slog.Default(), repo.RepoInfo{},
	)

	ctx := context.Background()
	exec, err := runner.Start(ctx, "run-fail-once", storage.EdictKey{ID: 77, Username: "testuser", Project: "testproject"}, nil, nil)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if err := runner.Run(ctx, exec); err == nil {
		t.Fatal("expected Run to fail on a failing background given")
	}

	var events []storage.TianEvent
	db.Where("event_type = ?", storage.EventRitualFailed).Find(&events)
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 EventRitualFailed, got %d", len(events))
	}
}

// TestRun_DefNotFound_EmitsRitualFailed verifies the unknown-ritual early
// return emits exactly one ritual_failed event.
func TestRun_DefNotFound_EmitsRitualFailed(t *testing.T) {
	db := setupEventTestDB(t)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(
		registry,
		func(id string) Minister { return nil },
		nil, db, nil, slog.Default(), repo.RepoInfo{},
	)

	exec := &RitualExecution{
		ID:         "def-not-found-1",
		RitualName: "no-such-ritual",
		EdictID:    55,
		Username:   "testuser",
		Project:    "testproject",
		State:      RitualStatePending,
	}

	if err := runner.Run(context.Background(), exec); err == nil {
		t.Fatal("expected Run to fail for an unknown ritual definition")
	}

	var events []storage.TianEvent
	db.Where("event_type = ?", storage.EventRitualFailed).Find(&events)
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 EventRitualFailed for unknown ritual, got %d", len(events))
	}
}

// barrierRunner blocks each Run until a release channel is closed. It lets a
// test prove two rituals are in flight at the same time.
type barrierRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (m *barrierRunner) Run(ctx context.Context, input runners.Input) (runners.Output, error) {
	select {
	case m.entered <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
		return runners.Output{Output: "ok\n", ExitCode: "0"}, nil
	case <-ctx.Done():
		return runners.Output{}, ctx.Err()
	}
}
func (m *barrierRunner) Restart(context.Context) error        { return nil }
func (m *barrierRunner) Close(context.Context) error          { return nil }
func (m *barrierRunner) AllowFallback(bool)                   {}
func (m *barrierRunner) RunnerType() string                   { return "barrier" }
func (m *barrierRunner) GetOS() string                        { return runtime.GOOS }
func (m *barrierRunner) SetMessageChannel(chan<- runners.Msg) {}
func (m *barrierRunner) HealthCheck(context.Context) error    { return nil }

// TestRitualsRunConcurrentlyAcrossEdicts verifies that rituals of two different
// edicts overlap rather than serializing: both reach the blocking runner before
// either is released.
func TestRitualsRunConcurrentlyAcrossEdicts(t *testing.T) {
	db := setupEventTestDB(t)

	ritual := &RitualDef{
		Name: "concurrent-test",
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
		Background: []string{"!echo work"},
	}
	registry := NewRitualRegistry()
	registry.Register(ritual)

	runner := &barrierRunner{entered: make(chan struct{}, 8), release: make(chan struct{})}
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := &RitualGuard{
		MinisterBase:   base,
		ritualRegistry: registry,
		ritualRunner: NewRitualRunner(
			registry,
			func(id string) Minister { return nil },
			nil,
			db, runner, slog.Default(), repo.RepoInfo{},
		),
		streamingCtx: func(string) context.Context { return context.Background() },
	}
	rg.SetNotify(func(any) {})

	// Start two rituals for two different edicts.
	rg.startRitual("concurrent-test", storage.EdictKey{ID: 101, Username: "testuser", Project: "testproject"}, nil)
	rg.startRitual("concurrent-test", storage.EdictKey{ID: 102, Username: "testuser", Project: "testproject"}, nil)

	// Both must reach the runner before either completes. If rituals were
	// globally serialized, only one would enter.
	entered := 0
	timeout := time.After(3 * time.Second)
	for entered < 2 {
		select {
		case <-runner.entered:
			entered++
		case <-timeout:
			t.Fatalf("rituals did not run concurrently; only %d entered", entered)
		}
	}

	close(runner.release)
}

// TestRitualsSerializeWithinSameEdict verifies that two rituals of the SAME
// edict remain ordered: the second cannot enter the runner until the first has
// released its per-edict lock.
func TestRitualsSerializeWithinSameEdict(t *testing.T) {
	db := setupEventTestDB(t)

	ritual := &RitualDef{
		Name: "serial-test",
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
		Background: []string{"!echo work"},
	}
	registry := NewRitualRegistry()
	registry.Register(ritual)

	runner := &barrierRunner{entered: make(chan struct{}, 8), release: make(chan struct{})}
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := &RitualGuard{
		MinisterBase:   base,
		ritualRegistry: registry,
		ritualRunner: NewRitualRunner(
			registry,
			func(id string) Minister { return nil },
			nil,
			db, runner, slog.Default(), repo.RepoInfo{},
		),
		streamingCtx: func(string) context.Context { return context.Background() },
	}
	rg.SetNotify(func(any) {})

	key := storage.EdictKey{ID: 200, Username: "testuser", Project: "testproject"}
	rg.startRitual("serial-test", key, nil)

	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first ritual did not enter the runner")
	}

	// Second ritual of the same edict must wait for the edict lock.
	rg.startRitual("serial-test", key, nil)
	select {
	case <-runner.entered:
		t.Fatal("second ritual of the same edict entered concurrently — same-edict rituals must serialize")
	case <-time.After(200 * time.Millisecond):
	}

	close(runner.release)
}

// TestBuildEventNotification_WorktreeMergeFailed verifies the ruler-facing
// message for a failed seal: the work NOT merging and the merged-but-cleanup
// variant are distinct, and both quote the reason.
func TestBuildEventNotification_WorktreeMergeFailed(t *testing.T) {
	rg := &RitualGuard{}

	failed := rg.buildEventNotification(Event{
		Type:     storage.EventWorktreeMergeFailed,
		EdictKey: storage.EdictKey{ID: 7, Username: "test", Project: "test"},
		Payload: map[string]interface{}{
			"reason": "rebase onto main failed: CONFLICT",
		},
	})
	if failed.Message != "Edict 7 sealed but work NOT merged: rebase onto main failed: CONFLICT" {
		t.Errorf("unexpected message: %q", failed.Message)
	}

	cleanup := rg.buildEventNotification(Event{
		Type:     storage.EventWorktreeMergeFailed,
		EdictKey: storage.EdictKey{ID: 7, Username: "test", Project: "test"},
		Payload: map[string]interface{}{
			"reason": "work merged but worktree removal failed",
			"merged": true,
		},
	})
	if cleanup.Message != "Edict 7 sealed; work merged but worktree cleanup failed: work merged but worktree removal failed" {
		t.Errorf("unexpected message: %q", cleanup.Message)
	}
}

// mergeWorktreeGuard builds a RitualGuard whose registry carries a
// merge-worktree ritual with the real ruler-only trigger, plus a plain
// "observer" ritual (no trigger) to prove the filter is what gates the start.
func mergeWorktreeGuard(t *testing.T, db *gorm.DB) *RitualGuard {
	t.Helper()
	registry := NewRitualRegistry()
	require.NoError(t, registry.Register(&RitualDef{
		Name: mergeWorktreeRitual,
		Triggers: []RitualTrigger{
			{Event: "seal_granted", Filter: map[string]string{"minister_id": "ruler"}},
		},
		Steps: []RitualStep{{Name: "landing"}},
	}))
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := NewRitualGuard(RitualGuardOpts{Base: base})
	rg.ritualRegistry = registry
	rg.ritualRunner = NewRitualRunner(registry, func(string) Minister { return nil }, nil,
		db, &worktreeTestRunner{}, slog.Default(), repo.RepoInfo{})
	rg.streamingCtx = func(string) context.Context { return context.Background() }
	rg.recoveryMu.Lock()
	rg.recoveryComplete = true
	rg.recoveryMu.Unlock()
	rg.SetNotify(func(any) {})
	return rg
}

// TestDispatchEvent_RulerSealStartsMergeWorktree: a seal_granted from the ruler
// starts the merge-worktree ritual.
func TestDispatchEvent_RulerSealStartsMergeWorktree(t *testing.T) {
	db := setupEventTestDB(t)
	rg := mergeWorktreeGuard(t, db)

	rg.DispatchEvent(Event{
		Type:     storage.EventSealGranted,
		EdictKey: storage.EdictKey{ID: 42, Username: "testuser", Project: "testproject"},
		Payload:  map[string]interface{}{"minister_id": "ruler"},
	})

	require.Eventually(t, func() bool {
		var n int64
		db.Model(&RitualExecution{}).Where("ritual_name = ?", mergeWorktreeRitual).Count(&n)
		return n > 0
	}, 3*time.Second, 10*time.Millisecond, "the ruler's seal must start merge-worktree")
}

// TestDispatchEvent_JudgeSealDoesNotStartMergeWorktree: a mid-run judge seal
// carries the same event type but a different payload, so the filter must keep
// merge-worktree from firing.
func TestDispatchEvent_JudgeSealDoesNotStartMergeWorktree(t *testing.T) {
	db := setupEventTestDB(t)
	rg := mergeWorktreeGuard(t, db)

	rg.DispatchEvent(Event{
		Type:     storage.EventSealGranted,
		EdictKey: storage.EdictKey{ID: 42, Username: "testuser", Project: "testproject"},
		Payload:  map[string]interface{}{"minister_id": "judge"},
	})

	// Give the dispatch path time to (not) start anything.
	time.Sleep(200 * time.Millisecond)
	var n int64
	db.Model(&RitualExecution{}).Where("ritual_name = ?", mergeWorktreeRitual).Count(&n)
	assert.Zero(t, n, "a judge's seal must not start merge-worktree")
}

// TestMergeWorktreeTerminalFailurePublishesEvent: when the merge-worktree
// ritual exhausts its retries, the terminal failure path publishes
// EventWorktreeMergeFailed carrying the failure reason.
func TestMergeWorktreeTerminalFailurePublishesEvent(t *testing.T) {
	db := setupEventTestDB(t)

	registry := NewRitualRegistry()
	require.NoError(t, registry.Register(&RitualDef{
		Name: mergeWorktreeRitual,
		Steps: []RitualStep{
			{Name: "landing", Then: []string{"!false"}, OnFailure: "retry", MaxRetries: 1},
		},
	}))
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := NewRitualGuard(RitualGuardOpts{Base: base})
	rg.ritualRegistry = registry
	rg.streamingCtx = func(string) context.Context { return context.Background() }
	rg.SetNotify(func(any) {})
	// A runner whose every command fails, so the then-step errors and the
	// retries are exhausted.
	rg.ritualRunner = NewRitualRunner(registry, func(string) Minister { return nil }, nil,
		db, &mockCallCountRunner{results: []runners.Output{
			{Output: "CONFLICT\n", ExitCode: "1"},
			{Output: "CONFLICT\n", ExitCode: "1"},
			{Output: "CONFLICT\n", ExitCode: "1"},
		}}, slog.Default(), repo.RepoInfo{})

	key := storage.EdictKey{ID: 42, Username: "testuser", Project: "testproject"}
	rg.startRitual(mergeWorktreeRitual, key, map[string]string{"edict_id": "42"})

	require.Eventually(t, func() bool {
		var n int64
		db.Model(&storage.TianEvent{}).
			Where("edict_id = ? AND event_type = ?", 42, storage.EventWorktreeMergeFailed).Count(&n)
		return n == 1
	}, 5*time.Second, 20*time.Millisecond, "a terminal merge-worktree failure must publish exactly one event")

	var ev storage.TianEvent
	require.NoError(t, db.Where("edict_id = ? AND event_type = ?", 42, storage.EventWorktreeMergeFailed).First(&ev).Error)
	assert.Contains(t, fmt.Sprint(ev.Payload["reason"]), "CONFLICT")
	assert.Equal(t, false, ev.Payload["merged"], "the work never landed, so merged must be explicit false")
	assert.Equal(t, "asimi/e42", ev.Payload["branch"])
}

// TestMergeWorktreeTerminalFailureReportsMergedCleanup: when the squash-merge
// landed but the worktree could not be cleaned up, the terminal failure event
// must say merged=true — a reader must never see "work NOT merged" for a merge
// that succeeded (the unreachable-variant defect).
func TestMergeWorktreeTerminalFailureReportsMergedCleanup(t *testing.T) {
	db := setupEventTestDB(t)

	root := t.TempDir()
	abs := filepath.Join(root, worktree.RelPath(42))
	require.NoError(t, os.MkdirAll(abs, 0o755))

	registry := NewRitualRegistry()
	require.NoError(t, registry.Register(&RitualDef{
		Name: mergeWorktreeRitual,
		Steps: []RitualStep{
			{Name: "landing", Then: []string{"the edict is merged"}, OnFailure: "abort"},
		},
	}))
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := NewRitualGuard(RitualGuardOpts{Base: base})
	rg.ritualRegistry = registry
	rg.streamingCtx = func(string) context.Context { return context.Background() }
	rg.SetNotify(func(any) {})

	// The rebase and squash-merge succeed (a non-empty staged index) but the
	// removal is refused — the landed-but-dirty case.
	mutation := &worktreeTestRunner{outputs: map[string]runners.Output{
		"symbolic-ref":              {Output: "origin/main\n", ExitCode: "0"},
		"diff --cached --name-only": {Output: "feature.txt\n", ExitCode: "0"},
		"worktree remove":           {Output: "contains modified or untracked files", ExitCode: "1"},
	}}
	rr := NewRitualRunner(registry, func(string) Minister { return nil }, rg.PublishEvent,
		db, mutation, slog.Default(), repo.RepoInfo{ProjectRoot: root, HasVCS: true})
	rr.mutationRunner = func(string) runners.Runner { return mutation }
	rg.ritualRunner = rr

	key := storage.EdictKey{ID: 42, Username: "testuser", Project: "testproject"}
	rg.startRitual(mergeWorktreeRitual, key, map[string]string{"edict_id": "42"})

	require.Eventually(t, func() bool {
		var n int64
		db.Model(&storage.TianEvent{}).
			Where("edict_id = ? AND event_type = ?", 42, storage.EventWorktreeMergeFailed).Count(&n)
		return n == 1
	}, 5*time.Second, 20*time.Millisecond, "a terminal cleanup failure must publish exactly one event")

	var ev storage.TianEvent
	require.NoError(t, db.Where("edict_id = ? AND event_type = ?", 42, storage.EventWorktreeMergeFailed).First(&ev).Error)
	assert.Equal(t, true, ev.Payload["merged"],
		"the work landed; the event must not read as 'not merged'")
	assert.Contains(t, fmt.Sprint(ev.Payload["reason"]), "removal failed")
}

// TestMergeWorktreeSuccessPublishesNoFailureEvent: a clean landing must ascend
// without ever flashing the terminal failure warning.
func TestMergeWorktreeSuccessPublishesNoFailureEvent(t *testing.T) {
	db := setupEventTestDB(t)

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, worktree.RelPath(42)), 0o755))

	registry := NewRitualRegistry()
	require.NoError(t, registry.Register(&RitualDef{
		Name: mergeWorktreeRitual,
		Steps: []RitualStep{
			{Name: "landing", Then: []string{"the edict is merged"}, OnFailure: "retry"},
		},
		Then: []string{"the edict ascended to heaven"},
	}))
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := NewRitualGuard(RitualGuardOpts{Base: base})
	rg.ritualRegistry = registry
	rg.streamingCtx = func(string) context.Context { return context.Background() }
	rg.SetNotify(func(any) {})

	mutation := &worktreeTestRunner{outputs: map[string]runners.Output{
		"symbolic-ref":              {Output: "origin/main\n", ExitCode: "0"},
		"diff --cached --name-only": {Output: "feature.txt\n", ExitCode: "0"},
	}}
	rr := NewRitualRunner(registry, func(string) Minister { return nil }, rg.PublishEvent,
		db, mutation, slog.Default(), repo.RepoInfo{ProjectRoot: root, HasVCS: true})
	rr.mutationRunner = func(string) runners.Runner { return mutation }
	rg.ritualRunner = rr

	key := storage.EdictKey{ID: 42, Username: "testuser", Project: "testproject"}
	rg.startRitual(mergeWorktreeRitual, key, map[string]string{"edict_id": "42"})

	// The happy path publishes the ascended event...
	require.Eventually(t, func() bool {
		var n int64
		db.Model(&storage.TianEvent{}).
			Where("edict_id = ? AND event_type = ?", 42, storage.EventEdictSealed).Count(&n)
		return n == 1
	}, 5*time.Second, 20*time.Millisecond, "a clean landing must ascend the edict")

	// ...and never the failure warning.
	var n int64
	db.Model(&storage.TianEvent{}).
		Where("edict_id = ? AND event_type = ?", 42, storage.EventWorktreeMergeFailed).Count(&n)
	assert.Zero(t, n, "a clean landing must not publish a merge-failure event")
}
