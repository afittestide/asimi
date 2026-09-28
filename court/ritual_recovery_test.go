package court

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "modernc.org/sqlite"
)

// --- Test helpers for promptForAbortedRituals tests ---

// setupRecoveryTestDB creates a test database with all tables needed for recovery tests.
func setupRecoveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := tmpDir + "/recovery_test.db"

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	// Serialize access through one connection: concurrent writers on separate
	// pool connections race file locks, and PRAGMA busy_timeout is
	// per-connection (see storage/db.go and court/court_test.go).
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("Failed to initialize gorm: %v", err)
	}

	err = db.AutoMigrate(
		&RitualExecution{},
		&RitualStepState{},
		&storage.Edict{},
		&storage.Seal{},
		&storage.Zhengming{},
	)
	if err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	return db
}

// mockZhengmingMinister implements Minister plus the zhengming interfaces
// for use in promptForAbortedRituals tests.
type mockZhengmingMinister struct {
	requestZhengmingFn func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error)
	waitForZhengmingFn func(ctx context.Context, requestID string) (string, error)
	MinisterBase
	id string
}

// newMockZhengmingMinister creates a mock minister with optional zhengming function overrides.
func newMockZhengmingMinister(requestFn func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error), waitFn func(ctx context.Context, requestID string) (string, error)) *mockZhengmingMinister {
	return &mockZhengmingMinister{
		MinisterBase:       MinisterBase{logger: slog.Default()},
		id:                 "secretary",
		requestZhengmingFn: requestFn,
		waitForZhengmingFn: waitFn,
	}
}

func (m *mockZhengmingMinister) ID() string              { return m.id }
func (m *mockZhengmingMinister) SystemPrompt() string    { return "" }
func (m *mockZhengmingMinister) Tools() []Tool           { return nil }
func (m *mockZhengmingMinister) Run(ctx context.Context) { <-ctx.Done() }

func (m *mockZhengmingMinister) RequestZhengming(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
	if m.requestZhengmingFn != nil {
		return m.requestZhengmingFn(ctx, key, questions, priority, callerMinisterID)
	}
	return "req-1", nil
}

func (m *mockZhengmingMinister) WaitForZhengming(ctx context.Context, requestID string) (string, error) {
	if m.waitForZhengmingFn != nil {
		return m.waitForZhengmingFn(ctx, requestID)
	}
	return "", nil
}

// newTestRitualGuard creates a RitualGuard wired for promptForAbortedRituals tests.
func newTestRitualGuard(t *testing.T, db *gorm.DB, getMinister func(id string) Minister, chancellor *mockZhengmingMinister) *RitualGuard {
	t.Helper()
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	opts := RitualGuardOpts{
		Base:         base,
		GetMinister:  getMinister,
		StreamingCtx: func(string) context.Context { return context.Background() },
	}
	if chancellor != nil {
		opts.RequestZhengming = chancellor.RequestZhengming
		opts.WaitForZhengming = chancellor.WaitForZhengming
	}
	rg := NewRitualGuard(opts)
	return rg
}

// --- Recovery detection tests (RitualRunner.Start) ---

// TestRitualRecoveryDetection tests that aborted executions are detected
// but Start() starts fresh (zhengming now lives in promptForAbortedRituals).
func TestRitualRecoveryDetection(t *testing.T) {
	db := setupRitualTestDB(t)

	// Migrate edict table (not done by setupRitualTestDB)
	if err := db.AutoMigrate(&storage.Edict{}); err != nil {
		t.Fatalf("failed to migrate edict table: %v", err)
	}

	// Create test edict
	edict := storage.Edict{
		Intent:   "Test recovery",
		Username: "testuser",
		Project:  "testproject",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	edictID := edict.ID
	edictKey := edict.Key()

	// Create ritual definition
	ritual := &RitualDef{
		Name:        "test-recovery",
		Description: "Test recovery ritual",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "Do step 1"},
			{Name: "step2", Minister: "judge", Act: "Do step 2"},
			{Name: "step3", Minister: "censor", Act: "Do step 3"},
		},
	}

	// Create aborted execution with one completed step
	abortedExecID := "ritual-aborted-123"
	err := db.Create(&RitualExecution{
		ID:          abortedExecID,
		RitualName:  "test-recovery",
		EdictID:     edictID,
		Username:    "testuser",
		Project:     "testproject",
		CurrentStep: 1,
		State:       RitualStateAborted,
		Data: storage.JSON{
			"inputs":       map[string]interface{}{"edict_id": edictID},
			"step1_result": "completed",
		},
	}).Error
	if err != nil {
		t.Fatalf("failed to create aborted execution: %v", err)
	}

	// Create step states - first step completed, second step incomplete
	err = db.Create(&RitualStepState{
		ExecutionID: abortedExecID,
		StepIndex:   0,
		Name:        "step1",
		Status:      "completed",
		Message:     "Step 1 completed successfully",
		RetryCount:  0,
	}).Error
	if err != nil {
		t.Fatalf("failed to create step state 1: %v", err)
	}

	err = db.Create(&RitualStepState{
		ExecutionID: abortedExecID,
		StepIndex:   1,
		Name:        "step2",
		Status:      "pending",
		Message:     "", // Incomplete
		RetryCount:  0,
	}).Error
	if err != nil {
		t.Fatalf("failed to create step state 2: %v", err)
	}

	// Setup ritual runner
	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("failed to register ritual: %v", err)
	}

	runner := NewRitualRunner(
		registry,
		nil, // getMinister - nil means no zhengming will be requested
		nil, // publishEvent
		db,
		nil, // runner
		slog.Default(),
		repo.RepoInfo{},
	)

	// Start ritual - should start fresh for aborted state
	ctx := context.Background()
	exec, err := runner.Start(ctx, "test-recovery", edictKey, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("failed to start ritual: %v", err)
	}

	// Verify NOT in recovery mode (aborted state no longer auto-recovers in Start)
	if exec.RecoveryMode {
		t.Error("expected RecoveryMode to be false for aborted state in Start()")
	}

	// Verify it starts from step 0 (fresh start)
	if exec.CurrentStep != 0 {
		t.Errorf("expected CurrentStep to be 0, got %d", exec.CurrentStep)
	}

	// Verify no previous execution ID
	if exec.PreviousExecutionID != "" {
		t.Errorf("expected PreviousExecutionID to be empty, got %q", exec.PreviousExecutionID)
	}
}

// TestRitualRecoveryNoAbortedExecution tests that fresh start occurs when no aborted execution exists
func TestRitualRecoveryNoAbortedExecution(t *testing.T) {
	db := setupRitualTestDB(t)

	// Migrate edict table
	if err := db.AutoMigrate(&storage.Edict{}); err != nil {
		t.Fatalf("failed to migrate edict table: %v", err)
	}

	// Create test edict
	edict := storage.Edict{
		Intent:   "Test fresh start",
		Username: "testuser",
		Project:  "testproject",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	edictKey := edict.Key()

	// Create ritual definition
	ritual := &RitualDef{
		Name:        "test-fresh",
		Description: "Test fresh start ritual",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "Do step 1"},
			{Name: "step2", Minister: "judge", Act: "Do step 2"},
		},
	}

	// Setup ritual runner
	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("failed to register ritual: %v", err)
	}

	runner := NewRitualRunner(
		registry,
		nil,
		nil,
		db,
		nil,
		slog.Default(),
		repo.RepoInfo{},
	)

	// Start ritual - should start fresh (no aborted execution)
	ctx := context.Background()
	exec, err := runner.Start(ctx, "test-fresh", edictKey, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("failed to start ritual: %v", err)
	}

	// Verify NOT in recovery mode
	if exec.RecoveryMode {
		t.Error("expected RecoveryMode to be false for fresh start")
	}

	// Verify it starts from step 0
	if exec.CurrentStep != 0 {
		t.Errorf("expected CurrentStep to be 0, got %d", exec.CurrentStep)
	}

	// Verify no previous execution ID
	if exec.PreviousExecutionID != "" {
		t.Errorf("expected PreviousExecutionID to be empty, got %q", exec.PreviousExecutionID)
	}
}

// TestRitualRecoveryAllStepsComplete tests that recovery is skipped if all steps completed
func TestRitualRecoveryAllStepsComplete(t *testing.T) {
	db := setupRitualTestDB(t)

	// Migrate edict table
	if err := db.AutoMigrate(&storage.Edict{}); err != nil {
		t.Fatalf("failed to migrate edict table: %v", err)
	}

	// Create test edict
	edict := storage.Edict{
		Intent:   "Test all complete",
		Username: "testuser",
		Project:  "testproject",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	edictID := edict.ID
	edictKey := edict.Key()

	// Create ritual definition
	ritual := &RitualDef{
		Name:        "test-all-complete",
		Description: "Test all complete ritual",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "Do step 1"},
			{Name: "step2", Minister: "judge", Act: "Do step 2"},
		},
	}

	// Create aborted execution with ALL steps completed
	abortedExecID := "ritual-aborted-complete-123"
	err := db.Create(&RitualExecution{
		ID:          abortedExecID,
		RitualName:  "test-all-complete",
		EdictID:     edictID,
		Username:    "testuser",
		Project:     "testproject",
		CurrentStep: 2,
		State:       RitualStateAborted,
		Data:        storage.JSON{"inputs": map[string]interface{}{"edict_id": edictID}},
	}).Error
	if err != nil {
		t.Fatalf("failed to create aborted execution: %v", err)
	}

	// Create step states - ALL steps completed
	err = db.Create(&RitualStepState{
		ExecutionID: abortedExecID,
		StepIndex:   0,
		Name:        "step1",
		Status:      "completed",
		Message:     "Step 1 completed",
		RetryCount:  0,
	}).Error
	if err != nil {
		t.Fatalf("failed to create step state 1: %v", err)
	}

	err = db.Create(&RitualStepState{
		ExecutionID: abortedExecID,
		StepIndex:   1,
		Name:        "step2",
		Status:      "completed",
		Message:     "Step 2 completed",
		RetryCount:  0,
	}).Error
	if err != nil {
		t.Fatalf("failed to create step state 2: %v", err)
	}

	// Setup ritual runner
	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("failed to register ritual: %v", err)
	}

	runner := NewRitualRunner(
		registry,
		nil,
		nil,
		db,
		nil,
		slog.Default(),
		repo.RepoInfo{},
	)

	// Start ritual - should start fresh since all steps completed
	ctx := context.Background()
	exec, err := runner.Start(ctx, "test-all-complete", edictKey, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("failed to start ritual: %v", err)
	}

	// Verify NOT in recovery mode (all steps were complete)
	if exec.RecoveryMode {
		t.Error("expected RecoveryMode to be false when all steps completed")
	}

	// Verify it starts from step 0
	if exec.CurrentStep != 0 {
		t.Errorf("expected CurrentStep to be 0, got %d", exec.CurrentStep)
	}
}

// TestRitualRecoveryWithRetry tests that Start() starts fresh for aborted state
// even when steps have retries (zhengming now lives in promptForAbortedRituals).
func TestRitualRecoveryWithRetry(t *testing.T) {
	db := setupRitualTestDB(t)

	// Migrate edict table
	if err := db.AutoMigrate(&storage.Edict{}); err != nil {
		t.Fatalf("failed to migrate edict table: %v", err)
	}

	// Create test edict
	edict := storage.Edict{
		Intent:   "Test retry",
		Username: "testuser",
		Project:  "testproject",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	edictID := edict.ID
	edictKey := edict.Key()

	// Create ritual definition
	ritual := &RitualDef{
		Name:        "test-retry",
		Description: "Test retry ritual",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "Do step 1"},
			{Name: "step2", Minister: "judge", Act: "Do step 2"},
			{Name: "step3", Minister: "censor", Act: "Do step 3"},
		},
	}

	// Create aborted execution
	abortedExecID := "ritual-aborted-retry-123"
	err := db.Create(&RitualExecution{
		ID:          abortedExecID,
		RitualName:  "test-retry",
		EdictID:     edictID,
		Username:    "testuser",
		Project:     "testproject",
		CurrentStep: 2,
		State:       RitualStateAborted,
		Data:        storage.JSON{"inputs": map[string]interface{}{"edict_id": edictID}},
	}).Error
	if err != nil {
		t.Fatalf("failed to create aborted execution: %v", err)
	}

	// Create step states - step1 completed, step2 has retries (incomplete), step3 not reached
	err = db.Create(&RitualStepState{
		ExecutionID: abortedExecID,
		StepIndex:   0,
		Name:        "step1",
		Status:      "completed",
		Message:     "Step 1 completed",
		RetryCount:  0,
	}).Error
	if err != nil {
		t.Fatalf("failed to create step state 1: %v", err)
	}

	err = db.Create(&RitualStepState{
		ExecutionID: abortedExecID,
		StepIndex:   1,
		Name:        "step2",
		Status:      "failed",
		Message:     "Step 2 failed",
		RetryCount:  2, // Has retries - considered incomplete
	}).Error
	if err != nil {
		t.Fatalf("failed to create step state 2: %v", err)
	}

	// Setup ritual runner
	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("failed to register ritual: %v", err)
	}

	runner := NewRitualRunner(
		registry,
		nil,
		nil,
		db,
		nil,
		slog.Default(),
		repo.RepoInfo{},
	)

	// Start ritual - should start fresh (aborted state no longer auto-recovers in Start)
	ctx := context.Background()
	exec, err := runner.Start(ctx, "test-retry", edictKey, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("failed to start ritual: %v", err)
	}

	// Verify NOT in recovery mode
	if exec.RecoveryMode {
		t.Error("expected RecoveryMode to be false for aborted state in Start()")
	}

	// Verify it starts from step 0
	if exec.CurrentStep != 0 {
		t.Errorf("expected CurrentStep to be 0 (fresh start), got %d", exec.CurrentStep)
	}
}

// TestRitualRecoveryLogMessage tests that recovery logs appropriate messages
func TestRitualRecoveryLogMessage(t *testing.T) {
	db := setupRitualTestDB(t)

	// Migrate edict table
	if err := db.AutoMigrate(&storage.Edict{}); err != nil {
		t.Fatalf("failed to migrate edict table: %v", err)
	}

	// Create test edict
	edict := storage.Edict{
		Intent:   "Test log",
		Username: "testuser",
		Project:  "testproject",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	edictID := edict.ID
	edictKey := edict.Key()

	// Create ritual definition
	ritual := &RitualDef{
		Name:        "test-log",
		Description: "Test log ritual",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "Do step 1"},
			{Name: "step2", Minister: "judge", Act: "Do step 2"},
		},
	}

	// Create aborted execution
	abortedExecID := "ritual-aborted-log-123"
	err := db.Create(&RitualExecution{
		ID:          abortedExecID,
		RitualName:  "test-log",
		EdictID:     edictID,
		Username:    "testuser",
		Project:     "testproject",
		CurrentStep: 1,
		State:       RitualStateAborted,
		Data:        storage.JSON{"inputs": map[string]interface{}{"edict_id": edictID}},
	}).Error
	if err != nil {
		t.Fatalf("failed to create aborted execution: %v", err)
	}

	// Create step state - first step completed
	err = db.Create(&RitualStepState{
		ExecutionID: abortedExecID,
		StepIndex:   0,
		Name:        "step1",
		Status:      "completed",
		Message:     "Step 1 completed",
		RetryCount:  0,
	}).Error
	if err != nil {
		t.Fatalf("failed to create step state: %v", err)
	}

	// Setup ritual runner
	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("failed to register ritual: %v", err)
	}

	// Use a test logger to capture log messages
	var logMessages []string
	testLogger := slog.New(slog.NewTextHandler(&testWriter{messages: &logMessages}, nil))

	runner := NewRitualRunner(
		registry,
		nil,
		nil,
		db,
		nil,
		testLogger,
		repo.RepoInfo{},
	)

	// Start ritual
	ctx := context.Background()
	_, err = runner.Start(ctx, "test-log", edictKey, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("failed to start ritual: %v", err)
	}

	// Verify log message about finding previous execution
	foundPreviousLog := false
	for _, msg := range logMessages {
		if strings.Contains(msg, "found previous ritual execution") {
			foundPreviousLog = true
			break
		}
	}
	if !foundPreviousLog {
		t.Error("expected log message about finding previous ritual execution")
	}
}

func TestFindFirstIncompleteStep(t *testing.T) {
	tests := []struct {
		name   string
		states []RitualStepState
		want   int
	}{
		{
			name:   "all steps complete returns -1",
			states: []RitualStepState{{Status: "completed", Message: "ok"}, {Status: "completed", Message: "done"}},
			want:   -1,
		},
		{
			name:   "completed step 0 with empty message, pending step 1",
			states: []RitualStepState{{Status: "completed", Message: ""}, {Status: "pending"}},
			want:   1,
		},
		{
			name:   "pending status on step 0",
			states: []RitualStepState{{Status: "pending"}, {Status: "completed", Message: "done"}},
			want:   0,
		},
		{
			name:   "failed status on step 0",
			states: []RitualStepState{{Status: "failed", Message: "error"}, {Status: "completed", Message: "ok"}},
			want:   0,
		},
		{
			name:   "retry count > 0 on step 0 despite completed status",
			states: []RitualStepState{{Status: "completed", Message: "failed", RetryCount: 1}, {Status: "completed", Message: "ok"}},
			want:   0,
		},
		{
			name:   "retry count > 0 on step 2",
			states: []RitualStepState{{Status: "completed", Message: "ok"}, {Status: "completed", Message: "ok"}, {Status: "completed", Message: "failed", RetryCount: 3}},
			want:   2,
		},
		{
			name:   "context canceled message",
			states: []RitualStepState{{Status: "completed", Message: "ok"}, {Status: "completed", Message: "context canceled"}},
			want:   1,
		},
		{
			name:   "context canceled embedded in longer message",
			states: []RitualStepState{{Status: "completed", Message: "step failed: context canceled by upstream"}},
			want:   0,
		},
		{
			name:   "timeout message",
			states: []RitualStepState{{Status: "completed", Message: "ok"}, {Status: "completed", Message: "timeout waiting for response"}},
			want:   1,
		},
		{
			name:   "timeout as standalone message",
			states: []RitualStepState{{Status: "completed", Message: "timeout"}},
			want:   0,
		},
		{
			name:   "aborted message",
			states: []RitualStepState{{Status: "completed", Message: "ok"}, {Status: "completed", Message: "step was aborted mid-execution"}},
			want:   1,
		},
		{
			name:   "aborted as standalone message",
			states: []RitualStepState{{Status: "completed", Message: "aborted"}},
			want:   0,
		},
		{
			name:   "empty states returns -1",
			states: []RitualStepState{},
			want:   -1,
		},
		{
			name:   "mixed: step0 completed, step1 pending (incomplete)",
			states: []RitualStepState{{Status: "completed", Message: "ok"}, {Status: "pending", Message: ""}},
			want:   1,
		},
		{
			name:   "retry count takes priority over completed status",
			states: []RitualStepState{{Status: "completed", Message: "completed", RetryCount: 1}},
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findFirstIncompleteStep(tt.states)
			if got != tt.want {
				t.Errorf("findFirstIncompleteStep() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestRitualRecoveryRecoveringMarksCompleted verifies that when a previous execution
// has state "recovering", Start() marks it as completed in the database.
// This is the zombie "recovering" fix: the recovering execution should not linger.
func TestRitualRecoveryRecoveringMarksCompleted(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.Edict{}); err != nil {
		t.Fatalf("failed to migrate edict table: %v", err)
	}

	edict := storage.Edict{
		Intent:   "Test recovering completion",
		Username: "testuser",
		Project:  "testproject",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	edictID := edict.ID
	edictKey := edict.Key()

	ritual := &RitualDef{
		Name:        "test-recovering-complete",
		Description: "Test recovering marks completed",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "Do step 1"},
			{Name: "step2", Minister: "judge", Act: "Do step 2"},
		},
	}

	// Create a previous execution in "recovering" state with step1 completed and step2 incomplete
	recoveringExecID := "ritual-recovering-xyz"
	err := db.Create(&RitualExecution{
		ID:          recoveringExecID,
		RitualName:  "test-recovering-complete",
		EdictID:     edictID,
		Username:    "testuser",
		Project:     "testproject",
		CurrentStep: 1,
		State:       RitualStateRecovering,
		Data:        storage.JSON{"inputs": map[string]interface{}{"edict_id": edictID}, "step1_result": "done"},
	}).Error
	if err != nil {
		t.Fatalf("failed to create recovering execution: %v", err)
	}

	db.Create(&RitualStepState{
		ExecutionID: recoveringExecID,
		StepIndex:   0,
		Name:        "step1",
		Status:      "completed",
		Message:     "Step 1 completed",
		RetryCount:  0,
	})
	db.Create(&RitualStepState{
		ExecutionID: recoveringExecID,
		StepIndex:   1,
		Name:        "step2",
		Status:      "pending",
		Message:     "",
		RetryCount:  0,
	})

	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("failed to register ritual: %v", err)
	}

	runner := NewRitualRunner(
		registry,
		nil, // getMinister - nil, no zhengming needed for recovering state
		nil, // publishEvent
		db,
		nil, // runner
		slog.Default(),
		repo.RepoInfo{},
	)

	ctx := context.Background()
	exec, err := runner.Start(ctx, "test-recovering-complete", edictKey, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("failed to start ritual: %v", err)
	}

	// Verify recovery mode is active
	if !exec.RecoveryMode {
		t.Error("expected RecoveryMode to be true")
	}

	// The key assertion: the previous "recovering" execution must now be "completed" in the database
	var previousExec RitualExecution
	if err := db.First(&previousExec, "id = ?", recoveringExecID).Error; err != nil {
		t.Fatalf("failed to query previous execution: %v", err)
	}
	if previousExec.State != RitualStateCompleted {
		t.Errorf("expected previous execution state to be %q, got %q", RitualStateCompleted, previousExec.State)
	}
}

// --- skipZhengmingPrompt / RitualStateRecovering tests ---

// TestSkipZhengmingPrompt_RecoveringState verifies that when a previous execution
// has state "recovering", the ritual runner skips the zhengming prompt and
// enters recovery mode directly.
func TestSkipZhengmingPrompt_RecoveringState(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.Edict{}); err != nil {
		t.Fatalf("failed to migrate edict table: %v", err)
	}

	edict := storage.Edict{Intent: "Test skip zhengming", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	edictKey := edict.Key()

	ritual := &RitualDef{
		Name:        "test-skip-zhengming",
		Description: "Test skip zhengming ritual",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "Do step 1"},
			{Name: "step2", Minister: "judge", Act: "Do step 2"},
			{Name: "step3", Minister: "censor", Act: "Do step 3"},
		},
	}

	recoveringExecID := "ritual-recovering-123"
	err := db.Create(&RitualExecution{
		ID: recoveringExecID, RitualName: "test-skip-zhengming", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", CurrentStep: 1,
		State: RitualStateRecovering,
		Data:  storage.JSON{"inputs": map[string]interface{}{"edict_id": edict.ID}, "step1_result": "completed"},
	}).Error
	if err != nil {
		t.Fatalf("failed to create recovering execution: %v", err)
	}

	db.Create(&RitualStepState{ExecutionID: recoveringExecID, StepIndex: 0, Name: "step1", Status: "completed", Message: "Step 1 completed successfully", RetryCount: 0})
	db.Create(&RitualStepState{ExecutionID: recoveringExecID, StepIndex: 1, Name: "step2", Status: "pending", Message: "", RetryCount: 0})

	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("failed to register ritual: %v", err)
	}

	zhengmingCalled := false
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			zhengmingCalled = true
			return "req-should-not-be-called", nil
		},
		nil,
	)

	runner := NewRitualRunner(
		registry,
		func(id string) Minister {
			if id == "secretary" {
				return chancellor
			}
			return nil
		},
		nil,
		db,
		nil,
		slog.Default(),
		repo.RepoInfo{},
	)

	ctx := context.Background()
	exec, err := runner.Start(ctx, "test-skip-zhengming", edictKey, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("failed to start ritual: %v", err)
	}

	if !exec.RecoveryMode {
		t.Error("expected RecoveryMode to be true for recovering state")
	}
	if exec.CurrentStep != 1 {
		t.Errorf("expected CurrentStep to be 1, got %d", exec.CurrentStep)
	}
	if zhengmingCalled {
		t.Error("expected RequestZhengming NOT to be called when previous state is 'recovering'")
	}
}

// TestRitualStateDismissed_ConstantValue verifies that RitualStateDismissed
// has the expected string value.
func TestRitualStateDismissed_ConstantValue(t *testing.T) {
	if RitualStateDismissed != "dismissed" {
		t.Errorf("expected RitualStateDismissed to be %q, got %q", "dismissed", RitualStateDismissed)
	}
}

// TestRitualStart_DismissedStateReturnsError verifies that Start() starts
// a fresh execution when the previous execution has state "dismissed".
func TestRitualStart_DismissedStateStartsFresh(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.Edict{}); err != nil {
		t.Fatalf("failed to migrate edict table: %v", err)
	}

	edict := storage.Edict{Intent: "Test dismissed", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	edictKey := edict.Key()

	ritual := &RitualDef{
		Name:        "test-dismissed-start",
		Description: "Test dismissed in Start",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "Do step 1"},
			{Name: "step2", Minister: "judge", Act: "Do step 2"},
		},
	}

	dismissedExecID := "ritual-dismissed-123"
	err := db.Create(&RitualExecution{
		ID: dismissedExecID, RitualName: "test-dismissed-start", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", CurrentStep: 1,
		State: RitualStateDismissed,
		Data:  storage.JSON{"inputs": map[string]interface{}{"edict_id": edict.ID}},
	}).Error
	if err != nil {
		t.Fatalf("failed to create dismissed execution: %v", err)
	}

	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("failed to register ritual: %v", err)
	}

	runner := NewRitualRunner(
		registry,
		nil,
		nil,
		db,
		nil,
		slog.Default(),
		repo.RepoInfo{},
	)

	ctx := context.Background()
	exec, err := runner.Start(ctx, "test-dismissed-start", edictKey, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("expected dismissed ritual to start fresh, got error: %v", err)
	}
	if exec == nil {
		t.Fatal("expected a new execution, got nil")
	}
	if exec.State != RitualStatePending {
		t.Errorf("expected fresh execution with state pending, got %q", exec.State)
	}
	if exec.PreviousExecutionID != "" {
		t.Errorf("expected fresh execution with no previous ID, got %q", exec.PreviousExecutionID)
	}
}

// --- promptForAbortedRituals tests ---

// TestPromptForAbortedRituals_NoAbortedRituals verifies that promptForAbortedRituals
// returns immediately when there are no aborted rituals.
func TestPromptForAbortedRituals_NoAbortedRituals(t *testing.T) {
	db := setupRecoveryTestDB(t)
	rg := newTestRitualGuard(t, db, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	rg.recoveryMu.RLock()
	if !rg.recoveryComplete {
		t.Error("expected recoveryComplete to be true after promptForAbortedRituals returns")
	}
	rg.recoveryMu.RUnlock()
}

// TestPromptForAbortedRituals_NilDB verifies that promptForAbortedRituals
// returns safely when db is nil.
func TestPromptForAbortedRituals_NilDB(t *testing.T) {
	base := NewMinisterBase(nil, nil, slog.Default(), "testuser", "testproject", nil)
	rg := &RitualGuard{
		MinisterBase: base,
		getMinister:  nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	rg.recoveryMu.RLock()
	if !rg.recoveryComplete {
		t.Error("expected recoveryComplete to be true even with nil db")
	}
	rg.recoveryMu.RUnlock()
}

// TestPromptForAbortedRituals_NilGetMinister verifies that promptForAbortedRituals
// returns safely when getMinister is nil.
func TestPromptForAbortedRituals_NilGetMinister(t *testing.T) {
	db := setupRecoveryTestDB(t)
	rg := newTestRitualGuard(t, db, nil, nil)

	abortedExec := &RitualExecution{
		ID:          "aborted-nil-minister",
		RitualName:  "test-ritual",
		EdictID:     1,
		Username:    "testuser",
		Project:     "testproject",
		State:       RitualStateAborted,
		CurrentStep: 0,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	rg.recoveryMu.RLock()
	if !rg.recoveryComplete {
		t.Error("expected recoveryComplete to be true even with nil getMinister")
	}
	rg.recoveryMu.RUnlock()
}

// TestPromptForAbortedRituals_AutoCompleteSealedEdict verifies that aborted
// rituals for sealed edicts are auto-completed without prompting.
func TestPromptForAbortedRituals_AutoCompleteSealedEdict(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test sealed edict", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	seal := storage.Seal{
		SealID: "seal-1", EdictID: edict.ID, Username: "testuser",
		Project: "testproject", MinisterID: "ruler", SealedAt: time.Now(),
	}
	if err := db.Create(&seal).Error; err != nil {
		t.Fatalf("failed to create seal: %v", err)
	}

	abortedExec := &RitualExecution{
		ID: "aborted-sealed-edict", RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	// Even though we don't need the chancellor for auto-completion,
	// getMinister must be non-nil for promptForAbortedRituals to proceed past the nil check.
	rg := newTestRitualGuard(t, db, func(id string) Minister { return nil }, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", "aborted-sealed-edict").Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateCompleted {
		t.Errorf("expected state %s, got %s", RitualStateCompleted, exec.State)
	}
}

// TestPromptForAbortedRituals_AutoCompleteCancelledEdict verifies that aborted
// rituals for cancelled edicts are auto-completed without prompting.
func TestPromptForAbortedRituals_AutoCompleteCancelledEdict(t *testing.T) {
	db := setupRecoveryTestDB(t)

	now := time.Now()
	edict := storage.Edict{Intent: "Test cancelled edict", Username: "testuser", Project: "testproject", CancelledAt: &now}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExec := &RitualExecution{
		ID: "aborted-cancelled-edict", RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateFailed, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	rg := newTestRitualGuard(t, db, func(id string) Minister { return nil }, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", "aborted-cancelled-edict").Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateCompleted {
		t.Errorf("expected state %s, got %s", RitualStateCompleted, exec.State)
	}
}

// TestPromptForAbortedRituals_RecoverAnswer verifies that answering "Recover from step N"
// sets the ritual state to "recovering" and updates CurrentStep.
func TestPromptForAbortedRituals_RecoverAnswer(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test recover answer", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-recover-answer"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 2,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 0, Name: "step0", Status: "completed", Message: "completed"})
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 1, Name: "step1", Status: "pending", Message: ""})

	var requestedQuestions storage.ZhengmingQuestions
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			requestedQuestions = questions
			return "req-recover", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Recover from step 1", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	if len(requestedQuestions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(requestedQuestions))
	}
	if !strings.Contains(requestedQuestions[0].Text, "aborted at step 1") {
		t.Errorf("expected question text to mention step 1, got %q", requestedQuestions[0].Text)
	}

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", abortedExecID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateRecovering {
		t.Errorf("expected state %s, got %s", RitualStateRecovering, exec.State)
	}
	if exec.CurrentStep != 1 {
		t.Errorf("expected CurrentStep to be 1, got %d", exec.CurrentStep)
	}
}

// TestPromptForAbortedRituals_MarkAsCompletedAnswer verifies that answering
// "Mark as completed" sets the ritual state to completed.
func TestPromptForAbortedRituals_MarkAsCompletedAnswer(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test mark completed", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-mark-completed"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 0, Name: "step0", Status: "completed", Message: "ok"})
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 1, Name: "step1", Status: "pending", Message: ""})

	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			return "req-mark", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Mark as completed", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", abortedExecID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateCompleted {
		t.Errorf("expected state %s, got %s", RitualStateCompleted, exec.State)
	}
}

// TestPromptForAbortedRituals_PassAnswer verifies that answering "Pass"
// sets the ritual state to dismissed.
func TestPromptForAbortedRituals_PassAnswer(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test pass answer", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-pass-answer"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 0, Name: "step0", Status: "completed", Message: "ok"})
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 1, Name: "step1", Status: "pending", Message: ""})

	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			return "req-pass", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Pass", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", abortedExecID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateDismissed {
		t.Errorf("expected state %s, got %s", RitualStateDismissed, exec.State)
	}
}

// TestPromptForAbortedRituals_ChancellorNil verifies that
// promptForAbortedRituals returns gracefully when the chancellor is nil.
func TestPromptForAbortedRituals_ChancellorNil(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test nil chancellor", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-nil-chancellor"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		return nil // chancellor not available
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", abortedExecID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateAborted {
		t.Errorf("expected state %s, got %s", RitualStateAborted, exec.State)
	}
}

// TestPromptForAbortedRituals_RequestZhengmingFails verifies that
// promptForAbortedRituals continues when RequestZhengming returns an error.
func TestPromptForAbortedRituals_RequestZhengmingFails(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test req fail", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-req-fail"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			return "", fmt.Errorf("zhengming unavailable")
		},
		nil,
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", abortedExecID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateAborted {
		t.Errorf("expected state %s, got %s", RitualStateAborted, exec.State)
	}
}

// TestPromptForAbortedRituals_ContextCancelled verifies that
// promptForAbortedRituals respects context cancellation.
func TestPromptForAbortedRituals_ContextCancelled(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test ctx cancel", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	aborted1 := &RitualExecution{
		ID: "aborted-ctx-1", RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	aborted2 := &RitualExecution{
		ID: "aborted-ctx-2", RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	db.Save(aborted1)
	db.Save(aborted2)

	var mu sync.Mutex
	chancellorCallCount := 0
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			mu.Lock()
			chancellorCallCount++
			mu.Unlock()
			return "req-1", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		rg.promptForAbortedRituals(ctx)
		close(done)
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("promptForAbortedRituals did not return after context cancellation")
	}

	mu.Lock()
	if chancellorCallCount > 1 {
		t.Errorf("expected at most 1 zhengming call before cancellation, got %d", chancellorCallCount)
	}
	mu.Unlock()
}

// TestPromptForAbortedRituals_IncompleteStepDetection verifies that
// promptForAbortedRituals correctly identifies the first incomplete step
// based on step states (empty message, retries, or error messages).
func TestPromptForAbortedRituals_IncompleteStepDetection(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test step detection", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-step-detect"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 3,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	// Step 0: completed. Step 1: has retry (incomplete). Step 2: context canceled (incomplete).
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 0, Name: "step0", Status: "completed", Message: "ok"})
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 1, Name: "step1", Status: "failed", Message: "failed", RetryCount: 2})
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 2, Name: "step2", Status: "completed", Message: "context canceled"})

	var capturedStepIdx int
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			for _, opt := range questions[0].Options {
				if strings.HasPrefix(opt, "Recover from step ") {
					if _, err := fmt.Sscanf(opt, "Recover from step %d", &capturedStepIdx); err != nil {
						continue
					}
				}
			}
			return "req-step", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return fmt.Sprintf("Recover from step %d", capturedStepIdx), nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	if capturedStepIdx != 1 {
		t.Errorf("expected incomplete step at index 1 (retry count), got %d", capturedStepIdx)
	}
}

// TestPromptForAbortedRituals_EdictDescriptionTruncation verifies that
// long edict descriptions are truncated to 60 chars in the zhengming prompt.
func TestPromptForAbortedRituals_EdictDescriptionTruncation(t *testing.T) {
	db := setupRecoveryTestDB(t)

	longIntent := strings.Repeat("a", 100)
	edict := storage.Edict{Intent: longIntent, Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-desc-trunc"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 0, Name: "step0", Status: "pending", Message: ""})

	var capturedText string
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			capturedText = questions[0].Text
			return "req-trunc", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Pass", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	if !strings.Contains(capturedText, "...") {
		t.Errorf("expected truncated description with '...' in question text, got %q", capturedText)
	}
}

// TestPromptForAbortedRituals_UsesSummaryOverIntent verifies that
// promptForAbortedRituals prefers edict.Summary over edict.Intent.
func TestPromptForAbortedRituals_UsesSummaryOverIntent(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{
		Intent: "This is the long intent text that should be ignored", Summary: "Short summary",
		Username: "testuser", Project: "testproject",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-summary-preferred"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 0, Name: "step0", Status: "pending", Message: ""})

	var capturedText string
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			capturedText = questions[0].Text
			return "req-summary", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Pass", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	if !strings.Contains(capturedText, "Short summary") {
		t.Errorf("expected question to contain edict Summary 'Short summary', got %q", capturedText)
	}
}

// TestPromptForAbortedRituals_StoppedStateIncluded verifies that stopped rituals
// are also included in the aborted rituals query.
func TestPromptForAbortedRituals_StoppedStateIncluded(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test stopped state", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	stoppedExec := &RitualExecution{
		ID: "stopped-ritual-1", RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateStopped, CurrentStep: 1,
	}
	if err := db.Save(stoppedExec).Error; err != nil {
		t.Fatalf("failed to create stopped ritual: %v", err)
	}

	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			return "req-stopped", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Mark as completed", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", "stopped-ritual-1").Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateCompleted {
		t.Errorf("expected state %s, got %s", RitualStateCompleted, exec.State)
	}
}

// TestPromptForAbortedRituals_FailedStateIncluded verifies that failed rituals
// are also included in the aborted rituals query.
func TestPromptForAbortedRituals_FailedStateIncluded(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test failed state", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	failedExec := &RitualExecution{
		ID: "failed-ritual-1", RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateFailed, CurrentStep: 1,
	}
	if err := db.Save(failedExec).Error; err != nil {
		t.Fatalf("failed to create failed ritual: %v", err)
	}

	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			return "req-failed", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Mark as completed", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", "failed-ritual-1").Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateCompleted {
		t.Errorf("expected state %s, got %s", RitualStateCompleted, exec.State)
	}
}

// TestPromptForAbortedRituals_LimitFive verifies that at most 5 aborted
// rituals are prompted at a time.
func TestPromptForAbortedRituals_LimitFive(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test limit 5", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	for i := 0; i < 7; i++ {
		exec := &RitualExecution{
			ID: fmt.Sprintf("aborted-limit-%d", i), RitualName: fmt.Sprintf("test-ritual-%d", i),
			EdictID: edict.ID, Username: "testuser", Project: "testproject",
			State: RitualStateAborted, CurrentStep: 1,
		}
		if err := db.Save(exec).Error; err != nil {
			t.Fatalf("failed to create aborted ritual %d: %v", i, err)
		}
	}

	var mu sync.Mutex
	promptCount := 0
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			mu.Lock()
			promptCount++
			mu.Unlock()
			return fmt.Sprintf("req-%d", promptCount), nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Pass", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	mu.Lock()
	if promptCount > 5 {
		t.Errorf("expected at most 5 zhengming prompts, got %d", promptCount)
	}
	mu.Unlock()
}

// TestPromptForAbortedRituals_RecoveryCompleteOnReturn verifies that
// recoveryComplete is always set to true after promptForAbortedRituals returns.
func TestPromptForAbortedRituals_RecoveryCompleteOnReturn(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test recovery complete", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExec := &RitualExecution{
		ID: "aborted-recovery-complete", RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			return "req-1", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Pass", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	rg.recoveryMu.RLock()
	if !rg.recoveryComplete {
		t.Error("expected recoveryComplete to be true after promptForAbortedRituals returns")
	}
	rg.recoveryMu.RUnlock()
}

// TestPromptForAbortedRituals_ZeroEdictIDExcluded verifies that rituals
// with edict_id = 0 are excluded from the recovery prompt.
func TestPromptForAbortedRituals_ZeroEdictIDExcluded(t *testing.T) {
	db := setupRecoveryTestDB(t)

	abortedExec := &RitualExecution{
		ID: "aborted-zero-edict", RitualName: "test-ritual", EdictID: 0,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	zhengmingCalled := false
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			zhengmingCalled = true
			return "req-zero", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Pass", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	if zhengmingCalled {
		t.Error("expected RequestZhengming NOT to be called for ritual with edict_id=0")
	}
}

// TestPromptForAbortedRituals_WaitForZhengmingFails verifies that
// promptForAbortedRituals continues when WaitForZhengming returns an error.
func TestPromptForAbortedRituals_WaitForZhengmingFails(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test wait fail", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-wait-fail"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			return "req-wait-fail", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "", fmt.Errorf("zhengming wait failed")
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", abortedExecID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateAborted {
		t.Errorf("expected state %s, got %s", RitualStateAborted, exec.State)
	}
}

// TestPromptForAbortedRituals_EmptyMessageStepIncomplete verifies that
// steps with empty messages are detected as incomplete.
func TestPromptForAbortedRituals_EmptyMessageStepIncomplete(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test empty msg", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-empty-msg"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 3,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	// Step 0 and 1 completed, step 2 has empty message (never executed)
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 0, Name: "step0", Status: "completed", Message: "ok"})
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 1, Name: "step1", Status: "completed", Message: "ok"})
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 2, Name: "step2", Status: "pending", Message: ""})

	var capturedStepIdx int
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			for _, opt := range questions[0].Options {
				if strings.HasPrefix(opt, "Recover from step ") {
					if _, err := fmt.Sscanf(opt, "Recover from step %d", &capturedStepIdx); err != nil {
						continue
					}
				}
			}
			return "req-empty", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Pass", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	if capturedStepIdx != 2 {
		t.Errorf("expected incomplete step at index 2 (empty message), got %d", capturedStepIdx)
	}
}

// TestPromptForAbortedRituals_AbortedMessageStepIncomplete verifies that
// steps with "aborted" in their message are detected as incomplete.
func TestPromptForAbortedRituals_AbortedMessageStepIncomplete(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test aborted msg", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-aborted-msg"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	// Step 0 completed, step 1 has "aborted" message
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 0, Name: "step0", Status: "completed", Message: "ok"})
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 1, Name: "step1", Status: "completed", Message: "step was aborted mid-execution"})

	var capturedStepIdx int
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			for _, opt := range questions[0].Options {
				if strings.HasPrefix(opt, "Recover from step ") {
					if _, err := fmt.Sscanf(opt, "Recover from step %d", &capturedStepIdx); err != nil {
						continue
					}
				}
			}
			return "req-abort-msg", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Pass", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	if capturedStepIdx != 1 {
		t.Errorf("expected incomplete step at index 1 (aborted message), got %d", capturedStepIdx)
	}
}

// testWriter is a simple io.Writer that captures log messages
type testWriter struct {
	messages *[]string
}

func (w *testWriter) Write(p []byte) (n int, err error) {
	*w.messages = append(*w.messages, string(p))
	return len(p), nil
}

// TestPromptForAbortedRituals_OtherProjectExcluded verifies that aborted
// rituals belonging to a different project are not included in the query results.
func TestPromptForAbortedRituals_OtherProjectExcluded(t *testing.T) {
	db := setupRecoveryTestDB(t)

	// Create edict and aborted ritual for the current user/project
	localEdict := storage.Edict{Intent: "Local edict", Username: "testuser", Project: "testproject"}
	if err := db.Create(&localEdict).Error; err != nil {
		t.Fatalf("failed to create local edict: %v", err)
	}
	localExec := &RitualExecution{
		ID: "aborted-local", RitualName: "test-ritual", EdictID: localEdict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(localExec).Error; err != nil {
		t.Fatalf("failed to create local aborted ritual: %v", err)
	}

	// Create edict and aborted ritual for a different project
	otherEdict := storage.Edict{Intent: "Other project edict", Username: "testuser", Project: "other-project"}
	if err := db.Create(&otherEdict).Error; err != nil {
		t.Fatalf("failed to create other-project edict: %v", err)
	}
	otherExec := &RitualExecution{
		ID: "aborted-other-project", RitualName: "test-ritual", EdictID: otherEdict.ID,
		Username: "testuser", Project: "other-project", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(otherExec).Error; err != nil {
		t.Fatalf("failed to create other-project aborted ritual: %v", err)
	}

	// Create edict and aborted ritual for a different username
	otherUserEdict := storage.Edict{Intent: "Other user edict", Username: "otheruser", Project: "testproject"}
	if err := db.Create(&otherUserEdict).Error; err != nil {
		t.Fatalf("failed to create other-user edict: %v", err)
	}
	otherUserExec := &RitualExecution{
		ID: "aborted-other-user", RitualName: "test-ritual", EdictID: otherUserEdict.ID,
		Username: "otheruser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(otherUserExec).Error; err != nil {
		t.Fatalf("failed to create other-user aborted ritual: %v", err)
	}

	zhengmingCalled := false
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			zhengmingCalled = true
			// Verify the key belongs to our user/project
			if key.Username != "testuser" || key.Project != "testproject" {
				t.Errorf("zhengming called for wrong user/project: %s/%s", key.Username, key.Project)
			}
			return "req-1", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Mark as completed", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	// The local ritual should be prompted and completed
	if !zhengmingCalled {
		t.Error("expected RequestZhengming to be called for local aborted ritual")
	}

	var localResult RitualExecution
	if err := db.First(&localResult, "id = ?", "aborted-local").Error; err != nil {
		t.Fatalf("failed to find local ritual: %v", err)
	}
	if localResult.State != RitualStateCompleted {
		t.Errorf("expected local ritual state %s, got %s", RitualStateCompleted, localResult.State)
	}

	// The other-project ritual should remain untouched
	var otherResult RitualExecution
	if err := db.First(&otherResult, "id = ?", "aborted-other-project").Error; err != nil {
		t.Fatalf("failed to find other-project ritual: %v", err)
	}
	if otherResult.State != RitualStateAborted {
		t.Errorf("expected other-project ritual state to remain %s, got %s", RitualStateAborted, otherResult.State)
	}

	// The other-user ritual should remain untouched
	var otherUserResult RitualExecution
	if err := db.First(&otherUserResult, "id = ?", "aborted-other-user").Error; err != nil {
		t.Fatalf("failed to find other-user ritual: %v", err)
	}
	if otherUserResult.State != RitualStateAborted {
		t.Errorf("expected other-user ritual state to remain %s, got %s", RitualStateAborted, otherUserResult.State)
	}
}

// TestPromptForAbortedRituals_DismissedStateSkipped verifies that
// rituals in dismissed state are not re-prompted in the per-ritual loop.
func TestPromptForAbortedRituals_DismissedStateSkipped(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test dismissed skip", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// Create a dismissed ritual
	dismissedExec := &RitualExecution{
		ID: "dismissed-skip", RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateDismissed, CurrentStep: 1,
	}
	if err := db.Save(dismissedExec).Error; err != nil {
		t.Fatalf("failed to create dismissed ritual: %v", err)
	}

	zhengmingCalled := false
	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			zhengmingCalled = true
			return "req-dismissed-skip", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Pass", nil
		},
	)

	rg := newTestRitualGuard(t, db, func(id string) Minister {
		if id == "secretary" {
			return chancellor
		}
		return nil
	}, chancellor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	if zhengmingCalled {
		t.Error("expected RequestZhengming NOT to be called for dismissed ritual")
	}

	// Verify the state remains dismissed
	var exec RitualExecution
	if err := db.First(&exec, "id = ?", "dismissed-skip").Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateDismissed {
		t.Errorf("expected state %s, got %s", RitualStateDismissed, exec.State)
	}
}

// --- Bug fix tests for edict 567 ---

// TestRecoverFromPreviousExec_Step0MarksCompleted verifies that when the
// previous "recovering" execution has firstIncompleteStep == 0 (no steps
// to preserve), the previous execution is still marked as completed,
// preventing zombie state.
func TestRecoverFromPreviousExec_Step0MarksCompleted(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.Edict{}); err != nil {
		t.Fatalf("failed to migrate edict table: %v", err)
	}

	edict := storage.Edict{Intent: "Test step0 zombie", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	edictKey := edict.Key()

	ritual := &RitualDef{
		Name: "test-step0-zombie",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "Do step 1"},
			{Name: "step2", Minister: "judge", Act: "Do step 2"},
		},
	}

	// Create a previous "recovering" execution where step 0 is incomplete
	recoveringExecID := "ritual-recovering-step0"
	err := db.Create(&RitualExecution{
		ID:          recoveringExecID,
		RitualName:  "test-step0-zombie",
		EdictID:     edict.ID,
		Username:    "testuser",
		Project:     "testproject",
		CurrentStep: 0,
		State:       RitualStateRecovering,
		Data:        storage.JSON{"inputs": map[string]interface{}{"edict_id": edict.ID}},
	}).Error
	if err != nil {
		t.Fatalf("failed to create recovering execution: %v", err)
	}

	// Step 0 has pending status → firstIncompleteStep == 0
	db.Create(&RitualStepState{
		ExecutionID: recoveringExecID,
		StepIndex:   0,
		Name:        "step1",
		Status:      "pending",
		Message:     "",
		RetryCount:  0,
	})

	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("failed to register ritual: %v", err)
	}

	runner := NewRitualRunner(
		registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{},
	)

	ctx := context.Background()
	exec, err := runner.Start(ctx, "test-step0-zombie", edictKey, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("failed to start ritual: %v", err)
	}

	// Fresh start: no recovery mode
	if exec.RecoveryMode {
		t.Error("expected RecoveryMode to be false when firstIncompleteStep == 0")
	}
	if exec.CurrentStep != 0 {
		t.Errorf("expected CurrentStep 0, got %d", exec.CurrentStep)
	}

	// Key assertion: previous "recovering" execution must be marked completed
	var prevExec RitualExecution
	if err := db.First(&prevExec, "id = ?", recoveringExecID).Error; err != nil {
		t.Fatalf("failed to query previous execution: %v", err)
	}
	if prevExec.State != RitualStateCompleted {
		t.Errorf("expected previous execution state %q, got %q (zombie state)",
			RitualStateCompleted, prevExec.State)
	}
}

// TestPromptForAbortedRituals_RecoverRetriggersRitual verifies that when the
// user chooses "Recover from step N", promptForAbortedRituals calls
// startRitual to re-launch the ritual after setting the recovering state.
func TestPromptForAbortedRituals_RecoverRetriggersRitual(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test retrigger", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	abortedExecID := "aborted-retrigger"
	abortedExec := &RitualExecution{
		ID: abortedExecID, RitualName: "test-retrigger", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 2,
		Data: storage.JSON{"inputs": map[string]interface{}{"key1": "val1", "key2": "42"}},
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 0, Name: "step0", Status: "completed", Message: "completed"})
	db.Save(&RitualStepState{ExecutionID: abortedExecID, StepIndex: 1, Name: "step1", Status: "pending", Message: ""})

	chancellor := newMockZhengmingMinister(
		func(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
			return "req-retrigger", nil
		},
		func(ctx context.Context, requestID string) (string, error) {
			return "Recover from step 1", nil
		},
	)

	// We need a RitualGuard with a registered ritual so startRitual can find it.
	ritual := &RitualDef{
		Name: "test-retrigger",
		Steps: []RitualStep{
			{Name: "step0", Minister: "forge", Act: "Do step 0"},
			{Name: "step1", Minister: "forge", Act: "Do step 1"},
		},
	}

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := NewRitualGuard(RitualGuardOpts{
		Base: base,
		GetMinister: func(id string) Minister {
			if id == "secretary" {
				return chancellor
			}
			return nil
		},
		StreamingCtx:     func(string) context.Context { return context.Background() },
		RequestZhengming: chancellor.RequestZhengming,
		WaitForZhengming: chancellor.WaitForZhengming,
	})
	rg.ritualRegistry.Register(ritual)
	// Set a no-op notify so startRitual doesn't panic on failure
	rg.SetNotify(func(msg interface{}) {})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rg.promptForAbortedRituals(ctx)

	// Verify the exec state was set to recovering
	var exec RitualExecution
	if err := db.First(&exec, "id = ?", abortedExecID).Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State != RitualStateRecovering {
		t.Errorf("expected state %s, got %s", RitualStateRecovering, exec.State)
	}

	// Give the goroutine from startRitual time to run
	// It will call Start() which calls recoverFromPreviousExec, which should
	// find the "recovering" state, mark it completed, and resume from step 1.
	// Wait for the ritual to be processed.
	time.Sleep(200 * time.Millisecond)

	// The previous "recovering" execution should now be completed (recovered)
	var execAfter RitualExecution
	if err := db.First(&execAfter, "id = ?", abortedExecID).Error; err != nil {
		t.Fatalf("failed to find ritual after: %v", err)
	}
	if execAfter.State != RitualStateCompleted {
		t.Errorf("expected state %s after recovery, got %s (ritual may not have been re-triggered)",
			RitualStateCompleted, execAfter.State)
	}
}

// TestPromptForAbortedRituals_StaleSealsNotAutoCompleted verifies that an
// aborted ritual on an edict whose seals are all stale is NOT auto-completed
// — it should remain pending for re-execution because the seal chain is broken
// (edict 714).
func TestPromptForAbortedRituals_StaleSealsNotAutoCompleted(t *testing.T) {
	db := setupRecoveryTestDB(t)

	edict := storage.Edict{Intent: "Test stale-sealed edict", Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}
	sealSvc := storage.NewSealService(db)

	// Grant a ruler seal, then invalidate it (simulating intent change)
	if err := sealSvc.GrantSeal(key, "ruler", storage.JSON{}); err != nil {
		t.Fatalf("failed to grant ruler seal: %v", err)
	}
	if err := sealSvc.InvalidateSeals(key); err != nil {
		t.Fatalf("failed to invalidate seals: %v", err)
	}

	abortedExec := &RitualExecution{
		ID: "aborted-stale-sealed", RitualName: "test-ritual", EdictID: edict.ID,
		Username: "testuser", Project: "testproject", State: RitualStateAborted, CurrentStep: 1,
	}
	if err := db.Save(abortedExec).Error; err != nil {
		t.Fatalf("failed to create aborted ritual: %v", err)
	}

	rg := newTestRitualGuard(t, db, func(id string) Minister { return nil }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rg.promptForAbortedRituals(ctx)

	var exec RitualExecution
	if err := db.First(&exec, "id = ?", "aborted-stale-sealed").Error; err != nil {
		t.Fatalf("failed to find ritual: %v", err)
	}
	if exec.State == RitualStateCompleted {
		t.Error("aborted ritual with stale seals should NOT be auto-completed — it should remain pending for re-execution")
	}
}
