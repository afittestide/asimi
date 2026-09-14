package court

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/mocks"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "modernc.org/sqlite" // SQLite driver
)

func setupMinisterTestDB(t *testing.T) *gorm.DB {
	db, _ := setupMinisterTestDBWithPath(t)
	return db
}

func setupMinisterTestDBWithPath(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "minister_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	dbPath := tmpDir + "/test.db"

	// Open database using database/sql with modernc.org/sqlite driver
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	// Use gorm with the existing connection
	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("Failed to initialize gorm: %v", err)
	}

	// Run migrations
	err = db.AutoMigrate(
		&storage.Edict{},
		&storage.Zhengming{},
		&storage.TianEvent{},
		&storage.TianEventDLQ{},
		&storage.Ling{},
		&storage.ForgeManifest{},
		&storage.Seal{},
		&storage.JudgeVerdict{},
		&storage.CensorPrecedent{},
		&storage.Incident{},
		&storage.RulerCouncil{},
		&storage.RitualGuardCheckpoint{},
	)
	if err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	// Create checkpoint table
	db.Exec(`CREATE TABLE IF NOT EXISTS ritual_guard_checkpoint (id INTEGER PRIMARY KEY, event_id INTEGER NOT NULL, updated_at DATETIME)`)

	return db, dbPath
}

func TestChancellor_EdictLifecycle(t *testing.T) {
	db := setupMinisterTestDB(t)

	// Create chancellor
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	chancellor := NewSecretary(base)

	// Create an edict (starts in brewing phase)
	edict, err := CreateEdictForTest(db, "Add a simple hello world function")
	assert.NoError(t, err)
	assert.NotNil(t, edict)

	// Verify edict was created
	edict2, err := chancellor.MinisterBase.GetEdict(edict.Key())
	assert.NoError(t, err)
	assert.NotNil(t, edict2)
	// Status is now derived - new edicts are active by default (no seals, no zhengming, not cancelled)
}

func TestStrategist_ProcessTask(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	// Create edict
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	edict, err := CreateEdictForTest(db, "Implement user authentication with login and logout")
	assert.NoError(t, err)
	assert.NotNil(t, edict)

	// Create strategist (no LLM)
	strategist := NewWar(base)

	// Send task via processTask with ritual-built Work
	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:        ctx,
		EdictKey:   edict.Key(),
		Work:       "Analyze the edict and produce a Battle Plan.",
		Scratchpad: "# Edict\nImplement user authentication",
		Done:       doneCh,
	}

	strategist.processTask(ctx, task)
	result := <-doneCh

	assert.NoError(t, result.Err)
	assert.True(t, result.Sealed)
	assert.Contains(t, result.Output, "no LLM configured")
}

func TestJudge_VerdictFlow(t *testing.T) {
	db := setupMinisterTestDB(t)

	// Setup: create edict and manifest
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	edict, err := CreateEdictForTest(db, "Test feature")
	assert.NoError(t, err)
	assert.NotNil(t, edict)

	manifestID := stageManifestForTest(t, db, edict.Key(), "", "test.go", "TestFunc", "hash1")

	// Record a passing verdict (simulates what the ritual's record_verdict step does)
	verdictID := GenerateID("verdict", manifestID, "auto")
	verdict := storage.JudgeVerdict{
		VerdictID:  verdictID,
		ManifestID: manifestID,
		Username:   edict.Key().Username,
		Project:    edict.Key().Project,
		TestSuite:  "auto",
		Outcome:    storage.VerdictPassed,
		Evidence:   storage.JSON{"reason": "no CI configured"},
	}
	if err := db.Create(&verdict).Error; err != nil {
		t.Fatalf("Failed to insert verdict: %v", err)
	}
	if err := db.Model(&storage.ForgeManifest{}).
		Where("manifest_id = ?", manifestID).
		Updates(map[string]interface{}{"status": storage.ManifestQuenched, "verdict_id": verdictID}).Error; err != nil {
		t.Fatalf("Failed to update manifest status: %v", err)
	}

	// Check manifest is quenched — use tools RecordVerdictTool to verify sealing
	tool := tools.RecordVerdictTool{Ctx: toolContextForTest(base)}
	result, err := tool.Call(context.Background(), fmt.Sprintf(`{"edict_id": %d, "passed": true}`, edict.ID))
	assert.NoError(t, err)
	assert.Contains(t, result, "sealed=true")
}

// works correctly when no preprocessor is set (nil = pass through unchanged).
func TestProcessPrompt_NoPreprocessor(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockLLM := mocks.NewLLMProvider()
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	sage := NewChancellor(base)
	sage.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})

	// Sage does not set a preprocessor — promptPreprocessor is nil.
	require.Nil(t, base.promptPreprocessor)

	var mu sync.Mutex
	var doneCount int
	sage.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := msg.(StreamDoneMsg); ok {
			doneCount++
		}
	})

	// Use the shared ProcessPrompt (Sage.Run now passes nil for processPrompt).
	prompt := &Prompt{
		EdictKey: storage.EdictKey{ID: 0, Username: "testuser", Project: "testproject"},
		Message:  "hello",
	}
	base.ProcessPrompt(ctx, sage, prompt)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, doneCount, "should complete streaming without error")
}

// TestProcessPrompt_ChannelIDRouting verifies that when a prompt has a
// ChannelID set (e.g. "e633" for a ritual tab), stream notifications
// use that ChannelID instead of the minister's own ID.
func TestProcessPrompt_ChannelIDRouting(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockLLM := mocks.NewLLMProvider()
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	sage := NewChancellor(base)
	sage.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})

	var mu sync.Mutex
	var startChannelID, doneChannelID string
	sage.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		switch m := msg.(type) {
		case StreamStartMsg:
			startChannelID = m.ChannelID
		case StreamDoneMsg:
			doneChannelID = m.ChannelID
		}
	})

	prompt := &Prompt{
		EdictKey:  storage.EdictKey{ID: 0, Username: "testuser", Project: "testproject"},
		Message:   "hello",
		ChannelID: "e633",
	}
	base.ProcessPrompt(ctx, sage, prompt)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "e633", startChannelID, "StreamStartMsg should use prompt ChannelID")
	assert.Equal(t, "e633", doneChannelID, "StreamDoneMsg should use prompt ChannelID")
}

// TestProcessPrompt_DefaultChannelID verifies that when ChannelID is empty,
// stream notifications fall back to the minister's own ID (existing behavior).
func TestProcessPrompt_DefaultChannelID(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockLLM := mocks.NewLLMProvider()
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	sage := NewChancellor(base)
	sage.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})

	var mu sync.Mutex
	var doneChannelID string
	sage.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		if m, ok := msg.(StreamDoneMsg); ok {
			doneChannelID = m.ChannelID
		}
	})

	prompt := &Prompt{
		EdictKey:  storage.EdictKey{ID: 0, Username: "testuser", Project: "testproject"},
		Message:   "hello",
		ChannelID: "", // empty — should fall back to minister ID
	}
	base.ProcessPrompt(ctx, sage, prompt)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "chancellor", doneChannelID, "StreamDoneMsg should use minister ID when ChannelID is empty")
}

// TestProcessPrompt_NewSessionSavesEdictSessionID verifies that when a new
// session is created (m.session == nil) and the prompt carries an edict key,
// the session ID is saved to the edict's session_id column.
func TestProcessPrompt_NewSessionSavesEdictSessionID(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create an edict with empty SessionID
	edict := storage.Edict{
		ID:       1,
		Username: "testuser",
		Project:  "testproject",
		Intent:   "test intent",
	}
	require.NoError(t, db.Create(&edict).Error)

	mockLLM := mocks.NewLLMProvider()
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	sage := NewChancellor(base)
	sage.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})

	var mu sync.Mutex
	var doneCount int
	sage.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := msg.(StreamDoneMsg); ok {
			doneCount++
		}
	})

	prompt := &Prompt{
		EdictKey:  storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"},
		Message:   "hello",
		ChannelID: "e1",
	}
	base.ProcessPrompt(ctx, sage, prompt)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, doneCount, "should complete streaming without error")

	// Verify the edict's session_id was updated
	var updated storage.Edict
	require.NoError(t, db.First(&updated, "id = ? AND username = ? AND project = ?", 1, "testuser", "testproject").Error)
	assert.NotEmpty(t, updated.SessionID, "edict session_id should be set after new session creation")
}

// TestProcessPrompt_ExistingSessionDoesNotOverwriteEdictSessionID verifies that
// when m.session is already set (restored session), the edict's session_id is
// NOT overwritten even if the prompt carries an edict key.
func TestProcessPrompt_ExistingSessionDoesNotOverwriteEdictSessionID(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create an edict with an existing SessionID
	edict := storage.Edict{
		ID:        2,
		Username:  "testuser",
		Project:   "testproject",
		Intent:    "test intent",
		SessionID: "existing-session-id",
	}
	require.NoError(t, db.Create(&edict).Error)

	mockLLM := mocks.NewLLMProvider()
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	sage := NewChancellor(base)
	sage.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})

	// Pre-create a session so ProcessPrompt finds m.sessions[channelID] != nil
	existingSession, err := CreateSession(sage, mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, func(any) {}, "e2")
	require.NoError(t, err)
	base.SetSession(existingSession, "e2")

	var mu sync.Mutex
	var doneCount int
	sage.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := msg.(StreamDoneMsg); ok {
			doneCount++
		}
	})

	prompt := &Prompt{
		EdictKey:  storage.EdictKey{ID: 2, Username: "testuser", Project: "testproject"},
		Message:   "hello",
		ChannelID: "e2",
	}
	base.ProcessPrompt(ctx, sage, prompt)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, doneCount, "should complete streaming without error")

	// Verify the edict's session_id was NOT overwritten
	var updated storage.Edict
	require.NoError(t, db.First(&updated, "id = ? AND username = ? AND project = ?", 2, "testuser", "testproject").Error)
	assert.Equal(t, "existing-session-id", updated.SessionID,
		"edict session_id should not be overwritten when session already exists")
}

// TestProcessPrompt_NewSessionDoesNotOverwriteExistingEdictSessionID verifies
// that when a new session is created but the edict already has a session_id,
// the edict's session_id is NOT overwritten.
func TestProcessPrompt_NewSessionDoesNotOverwriteExistingEdictSessionID(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create an edict that already has a SessionID
	edict := storage.Edict{
		ID:        3,
		Username:  "testuser",
		Project:   "testproject",
		Intent:    "test intent",
		SessionID: "original-session-id",
	}
	require.NoError(t, db.Create(&edict).Error)

	mockLLM := mocks.NewLLMProvider()
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	sage := NewChancellor(base)
	sage.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})

	var mu sync.Mutex
	var doneCount int
	sage.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := msg.(StreamDoneMsg); ok {
			doneCount++
		}
	})

	prompt := &Prompt{
		EdictKey:  storage.EdictKey{ID: 3, Username: "testuser", Project: "testproject"},
		Message:   "hello",
		ChannelID: "e3",
	}
	base.ProcessPrompt(ctx, sage, prompt)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, doneCount, "should complete streaming without error")

	// Verify the edict's session_id was NOT overwritten
	var updated storage.Edict
	require.NoError(t, db.First(&updated, "id = ? AND username = ? AND project = ?", 3, "testuser", "testproject").Error)
	assert.Equal(t, "original-session-id", updated.SessionID,
		"edict session_id should not be overwritten when it already has one")
}

func TestStrategist_CircularDependencyDetection(t *testing.T) {
	// Create ling with circular dependency
	ling := []storage.Ling{
		{LingID: "a", Dependencies: storage.StringArray{"b"}},
		{LingID: "b", Dependencies: storage.StringArray{"c"}},
		{LingID: "c", Dependencies: storage.StringArray{"a"}}, // Circular!
	}

	err := checkLingDAG(ling)
	if err == nil {
		t.Error("Expected error for circular dependency")
	}
}

func TestStrategist_ValidDependencies(t *testing.T) {
	// Create ling with valid DAG
	ling := []storage.Ling{
		{LingID: "a", Dependencies: storage.StringArray{}},
		{LingID: "b", Dependencies: storage.StringArray{"a"}},
		{LingID: "c", Dependencies: storage.StringArray{"a", "b"}},
	}

	err := checkLingDAG(ling)
	if err != nil {
		t.Errorf("Expected no error for valid dependencies: %v", err)
	}
}

// TestHappyFlowE2E tests the complete Task flow:
// Chancellor -> consult_minister tool -> Minister receives Task -> Minister sends Result (synchronous)
func TestHappyFlowE2E(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create Chancellor and Court
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	chancellor := NewSecretary(base)

	// Create a simple Court with just Forge for this test
	forge := NewForge(base)
	court := &Court{
		db: db,
		ministers: map[string]Minister{
			chancellor.ID(): chancellor,
			forge.ID():      forge,
		},
	}
	chancellor.SetMinisterLookup(court.GetMinister)

	// Create an edict for the test
	edict, err := CreateEdictForTest(db, "E2E test edict")
	assert.NoError(t, err)
	assert.NotNil(t, edict)
	// Start the Forge's Run loop in a goroutine
	go forge.Run(ctx)

	// Create the ConsultMinisterTool
	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}, Consultant: court}

	// Invoke the Forge minister with a trivial task
	// With synchronous blocking, this call blocks until minister replies
	taskInput := fmt.Sprintf(`{"minister_id": "forge", "edict_id": %d, "task": "please reply with 'hello world'"}`, edict.ID)
	result, err := tool.Call(ctx, taskInput)
	if err != nil {
		t.Fatalf("Failed to invoke minister: %v", err)
	}

	// Verify the tool returned success with completed status
	if result == "" {
		t.Error("Expected non-empty result from consult_minister")
	}
	t.Logf("consult_minister result: %s", result)

	// Parse the result to verify it contains completion info
	var response struct {
		MinisterID string `json:"minister_id"`
		EdictID    uint   `json:"edict_id"`
		Status     string `json:"status"`
		Sealed     bool   `json:"sealed"`
		Output     string `json:"output"`
	}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	if response.Status != "completed" {
		t.Errorf("Expected status 'completed', got %s", response.Status)
	}
	if response.MinisterID != "forge" {
		t.Errorf("Expected MinisterID 'forge', got %s", response.MinisterID)
	}
	if response.EdictID != edict.ID {
		t.Errorf("Expected EdictID %d, got %d", edict.ID, response.EdictID)
	}
	if !response.Sealed {
		t.Error("Expected Sealed=true from Forge")
	}
	t.Logf("Received response: minister=%s, edict=%d, sealed=%v, output=%s",
		response.MinisterID, response.EdictID, response.Sealed, response.Output)
}

// TestConsultMinisterTool_InvalidMinister tests error handling for unknown ministers
func TestConsultMinisterTool_InvalidMinister(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	chancellor := NewSecretary(base)
	court := &Court{
		db: db,
		ministers: map[string]Minister{
			chancellor.ID(): chancellor,
		},
	}
	chancellor.SetMinisterLookup(court.GetMinister)

	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}, Consultant: court}

	// Try to invoke a non-existent minister
	taskInput := `{"minister_id": "unknown", "edict_id": 1, "task": "hello"}`
	_, err := tool.Call(ctx, taskInput)
	if err == nil {
		t.Error("Expected error for unknown minister")
	}
}

// TestConsultMinisterTool_MissingTask tests error handling for missing task parameter
func TestConsultMinisterTool_MissingTask(t *testing.T) {
	ctx := context.Background()

	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}}

	// Missing task parameter
	taskInput := `{"minister_id": "forge", "edict_id": 1}`
	_, err := tool.Call(ctx, taskInput)
	if err == nil {
		t.Error("Expected error for missing task parameter")
	}
}

// TestConsultMinisterTool_InvalidJSON tests error handling for malformed JSON input
func TestConsultMinisterTool_InvalidJSON(t *testing.T) {
	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}}

	_, err := tool.Call(context.Background(), `not json`)
	if err == nil {
		t.Fatal("Expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("Expected 'invalid input' error, got: %v", err)
	}
}

// TestConsultMinisterTool_MissingMinisterID tests error handling for missing minister_id
func TestConsultMinisterTool_MissingMinisterID(t *testing.T) {
	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}}

	_, err := tool.Call(context.Background(), `{"edict_id": 1, "task": "do something"}`)
	if err == nil {
		t.Fatal("Expected error for missing minister_id")
	}
	if err.Error() != "minister_id is required" {
		t.Errorf("Expected 'minister_id is required', got: %v", err)
	}
}

// TestConsultMinisterTool_MissingEdictID tests error handling for missing edict_id
func TestConsultMinisterTool_MissingEdictID(t *testing.T) {
	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}}

	_, err := tool.Call(context.Background(), `{"minister_id": "forge", "task": "do something"}`)
	if err == nil {
		t.Fatal("Expected error for missing edict_id")
	}
	if err.Error() != "edict_id is required" {
		t.Errorf("Expected 'edict_id is required', got: %v", err)
	}
}

// TestConsultMinisterTool_MinisterReturnsError tests that a Result with Err is propagated
func TestConsultMinisterTool_MinisterReturnsError(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a fake minister that returns an error in Result
	fake := &fakeMinister{id: "failing", tasks: make(chan *Task, 1)}
	court := &Court{
		db:        db,
		ministers: map[string]Minister{"failing": fake},
	}
	// Start the fake minister: reads task, sends error result
	go func() {
		task := <-fake.tasks
		task.Done <- Result{
			MinisterID: "failing",
			Err:        errors.New("something broke"),
		}
	}()

	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}, Consultant: court}
	_, err := tool.Call(ctx, `{"minister_id": "failing", "edict_id": 1, "task": "break"}`)
	if err == nil {
		t.Fatal("Expected error when minister returns Result.Err")
	}
	if !strings.Contains(err.Error(), "something broke") {
		t.Errorf("Expected 'something broke' in error, got: %v", err)
	}
}

// TestConsultMinisterTool_ContextCancelledDuringSend tests cancellation while sending task
func TestConsultMinisterTool_ContextCancelledDuringSend(t *testing.T) {
	db := setupMinisterTestDB(t)

	// Create a fake minister with a full task channel (buffer 0, no reader)
	fake := &fakeMinister{id: "blocked", tasks: make(chan *Task)} // unbuffered, no goroutine reading
	court := &Court{
		db:        db,
		ministers: map[string]Minister{"blocked": fake},
	}

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}, Consultant: court}
	_, err := tool.Call(ctx, `{"minister_id": "blocked", "edict_id": 1, "task": "go"}`)
	if err == nil {
		t.Fatal("Expected error when context is cancelled during send")
	}
	if !strings.Contains(err.Error(), "context cancelled") {
		t.Errorf("Expected 'context cancelled' in error, got: %v", err)
	}
}

// TestConsultMinisterTool_ContextCancelledDuringWait tests cancellation while waiting for result
func TestConsultMinisterTool_ContextCancelledDuringWait(t *testing.T) {
	db := setupMinisterTestDB(t)

	// Create a fake minister that accepts but never replies
	fake := &fakeMinister{id: "slow", tasks: make(chan *Task, 1)}
	court := &Court{
		db:        db,
		ministers: map[string]Minister{"slow": fake},
	}

	// Drain the task channel so the send succeeds, but never reply
	go func() { <-fake.tasks }()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay to let the send succeed
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}, Consultant: court}
	_, err := tool.Call(ctx, `{"minister_id": "slow", "edict_id": 1, "task": "wait"}`)
	if err == nil {
		t.Fatal("Expected error when context is cancelled during wait")
	}

	assert.Contains(t, err.Error(), "context canceled")
}

// TestCourt_ConsultMinister_RoutesToCallerTab verifies that when a non-chancellor
// minister calls ConsultMinister, the ChannelID in notifications and the Task
// dispatched to the target minister are set to the caller's ID, not "secretary".
// This is the core fix for edict 686.
func TestCourt_ConsultMinister_RoutesToCallerTab(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var notifications []any
	notifyFunc := internal.NotifyFunc(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		notifications = append(notifications, msg)
	})

	// Target minister that captures the task and replies
	var capturedTask *Task
	fake := &fakeMinister{id: "target", tasks: make(chan *Task, 1)}
	court := &Court{
		db:        db,
		ministers: map[string]Minister{"target": fake},
		notify:    notifyFunc,
	}

	go func() {
		task := <-fake.tasks
		capturedTask = task
		task.Done <- Result{MinisterID: "target", Sealed: true, Output: "done"}
	}()

	// Simulate the "judge" minister calling consult_minister
	_, err := court.ConsultMinister(ctx, "judge", "target", "judge",
		storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"}, "do work")
	assert.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	// Verify MinisterInvokingMsg is routed to the caller's tab
	require.GreaterOrEqual(t, len(notifications), 1, "expected at least 1 notification")
	invoking, ok := notifications[0].(MinisterInvokingMsg)
	require.True(t, ok, "first notification should be MinisterInvokingMsg, got %T", notifications[0])
	assert.Equal(t, "judge", invoking.ChannelID,
		"MinisterInvokingMsg ChannelID should be callerID='judge', not 'chancellor'")

	// Verify MinisterCompletedMsg is routed to the caller's tab
	require.GreaterOrEqual(t, len(notifications), 2, "expected at least 2 notifications")
	completed, ok := notifications[1].(MinisterCompletedMsg)
	require.True(t, ok, "second notification should be MinisterCompletedMsg, got %T", notifications[1])
	assert.Equal(t, "judge", completed.ChannelID,
		"MinisterCompletedMsg ChannelID should be callerID='judge', not 'chancellor'")

	// Verify the Task dispatched to the target has ChannelID set to callerID
	require.NotNil(t, capturedTask, "task should have been captured")
	assert.Equal(t, "judge", capturedTask.ChannelID,
		"Task.ChannelID should be callerID='judge', not 'chancellor'")
}

// TestConsultMinisterTool_PassesMinisterIDAsCallerID verifies that
// ConsultMinisterTool.Call passes its embedded Ctx.MinisterID as the
// callerMinisterID to the consultant interface, and uses the channel ID
// from context (falling back to MinisterID when context is empty).
func TestConsultMinisterTool_PassesMinisterIDAsCallerID(t *testing.T) {
	var recordedCallerID, recordedChannelID, recordedMinisterID string

	consultant := &callerIDCapturingConsultant{callerIDPtr: &recordedCallerID, channelIDPtr: &recordedChannelID, ministerIDPtr: &recordedMinisterID}
	tool := tools.ConsultMinisterTool{
		Ctx:         tools.ToolContext{MinisterID: "judge", Username: "testuser", Project: "testproject"},
		Consultant:  consultant,
		MinisterIDs: []string{"forge"},
	}

	_, err := tool.Call(context.Background(), `{"minister_id": "forge", "edict_id": 1, "task": "do work"}`)
	assert.NoError(t, err)

	assert.Equal(t, "judge", recordedCallerID,
		"ConsultMinisterTool.Call should pass Ctx.MinisterID as callerMinisterID")
	assert.Equal(t, "judge", recordedChannelID,
		"ConsultMinisterTool.Call should fall back to MinisterID as channelID when context is empty")
	assert.Equal(t, "forge", recordedMinisterID,
		"ConsultMinisterTool.Call should pass the target minister_id")
}

// TestConsultMinisterTool_ChannelIDFromContext verifies that when the context
// carries an explicit ChannelID, it is used for routing instead of the minister's ID.
func TestConsultMinisterTool_ChannelIDFromContext(t *testing.T) {
	var recordedCallerID, recordedChannelID, recordedMinisterID string

	consultant := &callerIDCapturingConsultant{callerIDPtr: &recordedCallerID, channelIDPtr: &recordedChannelID, ministerIDPtr: &recordedMinisterID}
	tool := tools.ConsultMinisterTool{
		Ctx:         tools.ToolContext{MinisterID: "judge", Username: "testuser", Project: "testproject"},
		Consultant:  consultant,
		MinisterIDs: []string{"forge"},
	}

	ctx := context.WithValue(context.Background(), tools.ChannelIDKey{}, "e633")
	_, err := tool.Call(ctx, `{"minister_id": "forge", "edict_id": 1, "task": "do work"}`)
	assert.NoError(t, err)

	assert.Equal(t, "judge", recordedCallerID,
		"ConsultMinisterTool.Call should pass Ctx.MinisterID as callerMinisterID")
	assert.Equal(t, "e633", recordedChannelID,
		"ConsultMinisterTool.Call should use ChannelID from context when present")
	assert.Equal(t, "forge", recordedMinisterID,
		"ConsultMinisterTool.Call should pass the target minister_id")
}

// callerIDCapturingConsultant records the callerID passed to ConsultMinister.
type callerIDCapturingConsultant struct {
	callerIDPtr   *string
	channelIDPtr  *string
	ministerIDPtr *string
}

func (c *callerIDCapturingConsultant) ConsultMinister(ctx context.Context, callerMinisterID, ministerID, channelID string, key storage.EdictKey, work string) (string, error) {
	*c.callerIDPtr = callerMinisterID
	*c.channelIDPtr = channelID
	*c.ministerIDPtr = ministerID
	return `{"status":"completed"}`, nil
}

// TestConsultMinisterTool_Notifications verifies MinisterInvokingMsg and MinisterCompletedMsg are sent
func TestConsultMinisterTool_Notifications(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Collect notifications
	var mu sync.Mutex
	var notifications []any
	notifyFunc := internal.NotifyFunc(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		notifications = append(notifications, msg)
	})

	// Create a fake minister that succeeds
	fake := &fakeMinister{id: "notifier", tasks: make(chan *Task, 1)}
	court := &Court{
		db:        db,
		ministers: map[string]Minister{"notifier": fake},
		notify:    notifyFunc,
	}

	go func() {
		task := <-fake.tasks
		task.Done <- Result{MinisterID: "notifier", Sealed: true, Output: "done"}
	}()

	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}, Consultant: court}
	_, err := tool.Call(ctx, `{"minister_id": "notifier", "edict_id": 1, "task": "notify me"}`)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(notifications) != 2 {
		t.Fatalf("Expected 2 notifications, got %d", len(notifications))
	}

	// First: MinisterInvokingMsg
	invoking, ok := notifications[0].(MinisterInvokingMsg)
	if !ok {
		t.Fatalf("Expected MinisterInvokingMsg, got %T", notifications[0])
	}
	if invoking.MinisterID != "notifier" || invoking.EdictKey.ID != 1 || invoking.Task != "notify me" {
		t.Errorf("Unexpected invoking msg: %+v", invoking)
	}

	// Second: MinisterCompletedMsg
	completed, ok := notifications[1].(MinisterCompletedMsg)
	if !ok {
		t.Fatalf("Expected MinisterCompletedMsg, got %T", notifications[1])
	}
	if completed.MinisterID != "notifier" || completed.EdictKey.ID != 1 || completed.Error != nil {
		t.Errorf("Unexpected completed msg: %+v", completed)
	}
	if !completed.Sealed {
		t.Error("Expected Sealed=true in completed notification")
	}
}

// TestConsultMinisterTool_NotificationsOnError verifies error notifications are sent
func TestConsultMinisterTool_NotificationsOnError(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	var mu sync.Mutex
	var notifications []any
	notifyFunc := internal.NotifyFunc(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		notifications = append(notifications, msg)
	})

	// No ministers registered -> unknown minister error
	court := &Court{
		db:        db,
		ministers: map[string]Minister{},
	}
	court.SetNotify(notifyFunc)

	tool := tools.ConsultMinisterTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}, Consultant: court}
	_, _ = tool.Call(ctx, `{"minister_id": "ghost", "edict_id": 1, "task": "haunt"}`)

	mu.Lock()
	defer mu.Unlock()

	// Should have invoking + completed-with-error
	if len(notifications) != 2 {
		t.Fatalf("Expected 2 notifications, got %d", len(notifications))
	}

	completed, ok := notifications[1].(MinisterCompletedMsg)
	if !ok {
		t.Fatalf("Expected MinisterCompletedMsg, got %T", notifications[1])
	}
	if completed.Error == nil {
		t.Error("Expected error in completed notification for unknown minister")
	}
}

// TestCourt_ConsultMinister_SetsExcludeTools verifies that Court.ConsultMinister
// dispatches a Task with ExcludeTools set to ["consult_minister"] to
// prevent recursive consultation.
func TestCourt_ConsultMinister_SetsExcludeTools(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a fake minister that captures the task
	fake := &fakeMinister{id: "target", tasks: make(chan *Task, 1)}
	court := &Court{
		db:        db,
		ministers: map[string]Minister{"target": fake},
		notify:    func(any) {},
	}

	// Call ConsultMinister in a goroutine (it blocks until result is sent)
	go func() {
		task := <-fake.tasks
		// Verify ExcludeTools is set
		assert.ElementsMatch(t, []string{"consult_minister"}, task.ExcludeTools,
			"ConsultMinister should set ExcludeTools to [consult_minister]")
		task.Done <- Result{MinisterID: "target", Sealed: true, Output: "done"}
	}()

	_, err := court.ConsultMinister(ctx, "secretary", "target", "secretary",
		storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"}, "do work")
	assert.NoError(t, err)
}

// TestConsultMinisterTool_Format tests the Format method
func TestConsultMinisterTool_Format(t *testing.T) {
	tool := tools.ConsultMinisterTool{}

	// Normal case
	output := tool.Format(`{"minister_id": "forge", "task": "build it"}`, `{"status":"ok"}`, nil)
	if !strings.Contains(output, "ConsultMinister") {
		t.Errorf("Expected 'ConsultMinister' in output, got: %s", output)
	}
	if !strings.Contains(output, "forge") {
		t.Errorf("Expected 'forge' in output, got: %s", output)
	}
	if !strings.Contains(output, "[build it]") {
		t.Errorf("Expected '[build it]' in output, got: %s", output)
	}

	// Error case
	output = tool.Format(`{"minister_id": "forge", "task": "x"}`, "", errors.New("boom"))
	if !strings.Contains(output, "Error: boom") {
		t.Errorf("Expected 'Error: boom' in output, got: %s", output)
	}

	// Long task truncation
	longTask := strings.Repeat("a", 50)
	output = tool.Format(`{"minister_id": "forge", "task": "`+longTask+`"}`, `{}`, nil)
	if !strings.Contains(output, "...") {
		t.Errorf("Expected truncation with '...' for long task, got: %s", output)
	}
}

// TestBuildSystemPrompt_EdictID verifies role text and realm are present in the prompt
// and that the edict ID is NOT injected into the scratchpad (edict 692 removed this).
func TestBuildSystemPrompt_EdictID(t *testing.T) {
	base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)

	fake := &fakeMinister{MinisterBase: base, id: "test"}

	// With edict ID — role text and realm should be present
	prompt := buildSystemPrompt(fake, nil, storage.EdictKey{ID: 123456})
	if !strings.Contains(prompt, "You are a test minister.") {
		t.Errorf("Expected role text in system prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "三省六部") {
		t.Errorf("Expected Realm (三省六部) in system prompt, got:\n%s", prompt)
	}
	// The edict ID must NOT be injected into the scratchpad (removed in edict 692)
	if strings.Contains(prompt, "Current Edict") {
		t.Errorf("System prompt must not contain 'Current Edict' injection, got:\n%s", prompt)
	}
}

// TestBuildSystemPrompt_MinisterIntro verifies the template renders the
// minister ID with the corrected "known as" phrasing (was "knwon").
func TestBuildSystemPrompt_MinisterIntro(t *testing.T) {
	base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)
	fake := &fakeMinister{MinisterBase: base, id: "judge"}

	prompt := buildSystemPrompt(fake, nil, storage.EdictKey{})
	if !strings.Contains(prompt, "known as judge") {
		t.Errorf("Expected 'known as judge' in system prompt, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "knwon") {
		t.Errorf("Template should not contain old 'knwon' typo, got:\n%s", prompt)
	}
}

// TestBuildSystemPrompt_Scratchpad verifies scratchpad is included with minister ID heading
func TestBuildSystemPrompt_Scratchpad(t *testing.T) {
	base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)

	fake := &fakeMinisterWithScratchpad{
		fakeMinister: fakeMinister{MinisterBase: base, id: "war"},
		scratchpad:   "# Available Rituals\n- implement: Run implementation",
	}

	prompt := buildSystemPrompt(fake, nil, storage.EdictKey{})
	if !regexp.MustCompile(`--- .* Scratchpad ---`).MatchString(prompt) {
		t.Errorf("Expected scratchpad heading matching '--- .* Scratchpad ---', got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Available Rituals") {
		t.Errorf("Expected scratchpad content in system prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "--- End of scratchpad ---") {
		t.Errorf("Expected scratchpad end marker, got:\n%s", prompt)
	}
}

// fakeMinisterWithScratchpad extends fakeMinister with a non-empty scratchpad
type fakeMinisterWithScratchpad struct {
	fakeMinister
	scratchpad string
}

func (f *fakeMinisterWithScratchpad) Scratchpad() string { return f.scratchpad }

// fakeMinister is a minimal Minister implementation for testing
type fakeMinister struct {
	*MinisterBase
	id    string
	tasks chan *Task
}

func (f *fakeMinister) ID() string                  { return f.id }
func (f *fakeMinister) SystemPrompt() string        { return "You are a test minister." }
func (f *fakeMinister) Title() string               { return "Fake" }
func (f *fakeMinister) Tools() []Tool               { return nil }
func (f *fakeMinister) Tasks() chan<- *Task         { return f.tasks }
func (f *fakeMinister) Model() LLMProvider          { return nil }
func (f *fakeMinister) GetConfig() config.LLMConfig { return config.LLMConfig{} }
func (f *fakeMinister) Run(ctx context.Context)     {}

// TestChancellor_ScratchpadIncludesRituals verifies the Chancellor's system prompt
// contains ritual names and descriptions from the registry.
func TestChancellor_ScratchpadIncludesRituals(t *testing.T) {
	db := setupMinisterTestDB(t)
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	chancellor := NewSecretary(base)

	court := &Court{
		db:        db,
		ministers: map[string]Minister{chancellor.ID(): chancellor},
	}
	rgBase := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	court.ritualGuard = NewRitualGuard(RitualGuardOpts{Base: rgBase})
	court.GetRitualRegistry().Register(&RitualDef{Name: "swift-strike", Description: "The Swift Strike (S)"})
	court.GetRitualRegistry().Register(&RitualDef{Name: "castle-siege", Description: "The Castle Siege (L)"})
	chancellor.SetRitualSummaries(func() string {
		return court.GetRitualRegistry().Summaries()
	})

	prompt := buildSystemPrompt(chancellor, nil, storage.EdictKey{})

	if !strings.Contains(prompt, "swift-strike") {
		t.Errorf("Expected ritual name 'swift-strike' in system prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "The Swift Strike (S)") {
		t.Errorf("Expected ritual description in system prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "castle-siege") {
		t.Errorf("Expected ritual name 'castle-siege' in system prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "The Castle Siege (L)") {
		t.Errorf("Expected ritual description in system prompt, got:\n%s", prompt)
	}

	// Verify secretary role text includes new guidance
	if !strings.Contains(prompt, "You harmonize the ruler's intent") {
		t.Errorf("Expected 'You harmonize the ruler's intent' in system prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "To summarize the session as a new edict call suggest_edict") {
		t.Errorf("Expected 'To summarize the session as a new edict call suggest_edict' in system prompt, got:\n%s", prompt)
	}
	// Verify old auto-selection guidance is gone
	if strings.Contains(prompt, "Use castle-siege for medium sized changes") {
		t.Errorf("Old auto-selection guidance for castle-siege should be removed, got:\n%s", prompt)
	}
}

// TestScratchpad_IncludesSkills verifies that skills are included in the
// scratchpad when the getSkills hook is set.
func TestScratchpad_IncludesSkills(t *testing.T) {
	base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)
	base.SetSkills(func(ministerID string) string {
		return "### Available Skills\n\n- **go-testing**: Test guidance"
	})

	sp := base.Scratchpad()
	assert.Contains(t, sp, "Available Skills")
	assert.Contains(t, sp, "go-testing")
	assert.NotContains(t, sp, "Available Rituals")
}

// TestScratchpad_SkillsWithEmptyString verifies that an empty string from
// the skills hook does not produce a superfluous section.
func TestScratchpad_SkillsWithEmptyString(t *testing.T) {
	base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)
	base.SetSkills(func(ministerID string) string {
		return "" // no skills match this minister
	})

	sp := base.Scratchpad()
	assert.Equal(t, "", sp, "empty skills should produce empty scratchpad")
}

// TestScratchpad_CombinesSkillsAndRituals verifies that both rituals and
// skills appear together in the scratchpad.
func TestScratchpad_CombinesSkillsAndRituals(t *testing.T) {
	base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)
	base.SetRitualSummaries(func() string {
		return "- swift-strike: The Swift Strike"
	})
	base.SetSkills(func(ministerID string) string {
		return "### Available Skills\n\n- **go-testing**: Test guidance"
	})

	sp := base.Scratchpad()
	assert.Contains(t, sp, "Available Rituals")
	assert.Contains(t, sp, "swift-strike")
	assert.Contains(t, sp, "Available Skills")
	assert.Contains(t, sp, "go-testing")

	// Skills section should come after rituals (rituals appended first)
	ritualsIdx := strings.Index(sp, "Available Rituals")
	skillsIdx := strings.Index(sp, "Available Skills")
	assert.Less(t, ritualsIdx, skillsIdx, "rituals should appear before skills")
}

// TestScratchpad_SkillsViaBuildSystemPrompt verifies that skills content
// flows through buildSystemPrompt when the getSkills hook is set.
func TestScratchpad_SkillsViaBuildSystemPrompt(t *testing.T) {
	base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)
	base.SetSkills(func(ministerID string) string {
		return "### Available Skills\n\n- **go-testing**: Test guidance"
	})

	fake := &fakeMinisterWithBase{
		MinisterBase: base,
		id:           "forge",
	}

	prompt := buildSystemPrompt(fake, nil, storage.EdictKey{})
	assert.Contains(t, prompt, "go-testing")
	assert.Contains(t, prompt, "Test guidance")
}

// fakeMinisterWithBase uses the real MinisterBase.Scratchpad() instead of
// overriding it, so tests can exercise the getSkills/getRitualSummaries hooks.
type fakeMinisterWithBase struct {
	*MinisterBase
	id    string
	tasks chan *Task
}

func (f *fakeMinisterWithBase) ID() string                  { return f.id }
func (f *fakeMinisterWithBase) SystemPrompt() string        { return "You are a test minister." }
func (f *fakeMinisterWithBase) Title() string               { return "Fake" }
func (f *fakeMinisterWithBase) Tools() []Tool               { return nil }
func (f *fakeMinisterWithBase) Tasks() chan<- *Task         { return f.tasks }
func (f *fakeMinisterWithBase) Model() LLMProvider          { return nil }
func (f *fakeMinisterWithBase) GetConfig() config.LLMConfig { return config.LLMConfig{} }
func (f *fakeMinisterWithBase) Run(ctx context.Context)     {}

// TestStorage_DBPath tests that storage.DBPath correctly extracts the database path from gorm.DB
func TestStorage_DBPath(t *testing.T) {
	db, expectedPath := setupMinisterTestDBWithPath(t)

	// Call DBPath and verify it returns the correct path
	gotPath := storage.DBPath(db)

	// Resolve symlinks for comparison (e.g., /tmp -> /private/tmp on macOS)
	expectedResolved, _ := filepath.EvalSymlinks(expectedPath)
	gotResolved, _ := filepath.EvalSymlinks(gotPath)

	if gotResolved != expectedResolved {
		t.Errorf("DBPath() = %q, want %q", gotPath, expectedPath)
	}
}

// TestStorage_DBPath_NilDB tests DBPath returns empty string when db is nil
func TestStorage_DBPath_NilDB(t *testing.T) {
	gotPath := storage.DBPath(nil)
	if gotPath != "" {
		t.Errorf("DBPath() with nil db = %q, want empty string", gotPath)
	}
}

// TestRunner_HealthCheck tests the runner health check function
func TestRunner_HealthCheck(t *testing.T) {
	ctx := context.Background()

	t.Run("successful_health_check", func(t *testing.T) {
		mockRunner := &mockHealthCheckRunner{
			output:   "Linux",
			exitCode: "0",
		}

		err := mockRunner.HealthCheck(ctx)
		if err != nil {
			t.Errorf("Expected nil error for healthy sandbox, got: %v", err)
		}
	})

	t.Run("non_zero_exit_code", func(t *testing.T) {
		mockRunner := &mockHealthCheckRunner{
			output:   "",
			exitCode: "1",
		}

		err := mockRunner.HealthCheck(ctx)
		if err == nil {
			t.Error("Expected error for non-zero exit code")
		}
		if !strings.Contains(err.Error(), "uname exited with 1") {
			t.Errorf("Expected 'uname exited with 1', got: %v", err)
		}
	})

	t.Run("missing_linux_in_output", func(t *testing.T) {
		mockRunner := &mockHealthCheckRunner{
			output:   "Darwin",
			exitCode: "0",
		}

		err := mockRunner.HealthCheck(ctx)
		if err == nil {
			t.Error("Expected error when Linux not in output")
		}
		if !strings.Contains(err.Error(), "expected Linux in output") {
			t.Errorf("Expected 'expected Linux in output', got: %v", err)
		}
	})

	t.Run("runner_run_error", func(t *testing.T) {
		mockRunner := &mockHealthCheckRunner{
			runErr: errors.New("container not running"),
		}

		err := mockRunner.HealthCheck(ctx)
		if err == nil {
			t.Error("Expected error when runner.Run fails")
		}
		if !strings.Contains(err.Error(), "container not running") {
			t.Errorf("Expected 'container not running', got: %v", err)
		}
	})
}

// mockHealthCheckRunner is a mock runner for testing HealthCheck
type mockHealthCheckRunner struct {
	output     string
	exitCode   string
	runErr     error
	runnerType string
}

func (m *mockHealthCheckRunner) Run(ctx context.Context, input runners.Input) (runners.Output, error) {
	if m.runErr != nil {
		return runners.Output{}, m.runErr
	}
	return runners.Output{
		Output:   m.output,
		ExitCode: m.exitCode,
	}, nil
}

func (m *mockHealthCheckRunner) AllowFallback(bool)            {}
func (m *mockHealthCheckRunner) Close(context.Context) error   { return nil }
func (m *mockHealthCheckRunner) Restart(context.Context) error { return nil }
func (m *mockHealthCheckRunner) RunnerType() string {
	if m.runnerType != "" {
		return m.runnerType
	}
	return "mock"
}
func (m *mockHealthCheckRunner) GetOS() string                                { return runtime.GOOS }
func (m *mockHealthCheckRunner) SetMessageChannel(msgChan chan<- runners.Msg) {}

// HealthCheck implements runners.Runner.HealthCheck
func (m *mockHealthCheckRunner) HealthCheck(ctx context.Context) error {
	result, err := m.Run(ctx, runners.Input{
		Command:     "uname",
		Description: "sandbox health check",
	})
	if err != nil {
		return fmt.Errorf("sandbox health check failed: %w", err)
	}
	if result.ExitCode != "0" {
		return fmt.Errorf("sandbox health check failed: uname exited with %s", result.ExitCode)
	}
	if !strings.Contains(result.Output, "Linux") {
		return fmt.Errorf("sandbox health check failed: expected Linux in output, got: %s", result.Output)
	}
	return nil
}

func TestZhengmingMultipleQuestions(t *testing.T) {
	questions := []storage.ZhengmingQuestion{
		{Text: "What should it do?", Options: []string{"Option A", "Option B"}},
		{Text: "How urgent?", Options: []string{"Now", "Later", "Never"}},
	}

	data, err := json.Marshal(questions)
	require.NoError(t, err)

	var parsed []storage.ZhengmingQuestion
	require.NoError(t, json.Unmarshal(data, &parsed))

	require.Len(t, parsed, 2)
	assert.Equal(t, "What should it do?", parsed[0].Text)
	assert.Len(t, parsed[0].Options, 2)
	assert.Equal(t, "How urgent?", parsed[1].Text)
	assert.Len(t, parsed[1].Options, 3)
}

// TestFormatGivenContext verifies edict details are rendered as readable markdown
func TestFormatGivenContext(t *testing.T) {
	ctx := map[string]interface{}{
		"edict": map[string]interface{}{
			"edict_id": "edict-abc123",
			"intent":   "Add user authentication with OAuth2",
			"status":   "active",
		},
		"manifests": []map[string]interface{}{
			{"manifest_id": "m-1", "file_path": "auth.go", "status": "staged"},
		},
	}

	result := formatScratchpad(ctx)

	// Should contain both section headings (sorted alphabetically)
	if !strings.Contains(result, "## edict") {
		t.Errorf("Expected '## edict' heading, got:\n%s", result)
	}
	if !strings.Contains(result, "## manifests") {
		t.Errorf("Expected '## manifests' heading, got:\n%s", result)
	}
	// Should contain the edict intent
	if !strings.Contains(result, "Add user authentication with OAuth2") {
		t.Errorf("Expected edict intent in output, got:\n%s", result)
	}
	// Should contain JSON formatting
	if !strings.Contains(result, "```json") {
		t.Errorf("Expected JSON code block, got:\n%s", result)
	}
	// Nil/empty context should return empty string
	if formatScratchpad(nil) != "" {
		t.Error("Expected empty string for nil context")
	}
	if formatScratchpad(map[string]interface{}{}) != "" {
		t.Error("Expected empty string for empty context")
	}
}

// TestFormatGivenContext_StringValues verifies plain strings are rendered without JSON wrapping
func TestFormatGivenContext_StringValues(t *testing.T) {
	ctx := map[string]interface{}{
		"notes": "some plain text output",
	}
	result := formatScratchpad(ctx)
	if !strings.Contains(result, "some plain text output") {
		t.Errorf("Expected plain text, got:\n%s", result)
	}
	if strings.Contains(result, "```json") {
		t.Errorf("Plain strings should not be wrapped in JSON code blocks, got:\n%s", result)
	}
}

// TestBuildSystemPrompt_GivenContext verifies that pre-formatted given context
// from a ritual is included in the system prompt when passed to buildSystemPrompt.
func TestBuildSystemPrompt_GivenContext(t *testing.T) {
	base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)
	fake := &fakeMinister{MinisterBase: base, id: "forge"}

	// Simulate what the ritual does: format given context map → markdown string
	scratchpad := formatScratchpad(map[string]interface{}{
		"edict": map[string]interface{}{
			"edict_id": "edict-xyz",
			"intent":   "Implement dark mode for the dashboard",
			"status":   "active",
		},
	})

	// With scratchpad — edict intent should appear in system prompt
	prompt := buildSystemPrompt(fake, nil, storage.EdictKey{ID: 42}, scratchpad)
	if !strings.Contains(prompt, "Implement dark mode for the dashboard") {
		t.Errorf("Expected edict intent in system prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "# Given Context") {
		t.Errorf("Expected '# Given Context' heading, got:\n%s", prompt)
	}

	// Without given context — should not contain "Given Context"
	prompt = buildSystemPrompt(fake, nil, storage.EdictKey{ID: 42})
	if strings.Contains(prompt, "# Given Context") {
		t.Errorf("Expected no scratchpad without scratchpad param, got:\n%s", prompt)
	}
}

// TestEnsureCourtInfrastructureEdict verifies that edict 1 is created on court initialization
func TestEnsureCourtInfrastructureEdict(t *testing.T) {
	db := setupMinisterTestDB(t)

	// Verify edict 1 doesn't exist yet
	var count int64
	db.Model(&storage.Edict{}).Where("id = ?", 1).Count(&count)
	assert.Equal(t, int64(0), count, "edict 1 should not exist before court creation")

	// Create court - this should create edict 1
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, court)

	// Verify edict 1 was created
	var edict storage.Edict
	err := db.First(&edict, "id = ?", 1).Error
	assert.NoError(t, err, "edict 1 should exist after court creation")
	assert.Equal(t, "Court Infrastructure - reserved for system-level operations (init, bootstrap, etc.)", edict.Intent)
	assert.Equal(t, "Court Infrastructure", edict.Summary)
	assert.Equal(t, "court-infra", edict.IssueRef)

	// Verify creating court again doesn't create duplicate (idempotent)
	court2 := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, court2)

	var count2 int64
	db.Model(&storage.Edict{}).Where("id = ?", 1).Count(&count2)
	assert.Equal(t, int64(1), count2, "edict 1 should still exist only once")
}

// TestCreateEdict_DoesNotReserveEdict1 verifies that user edicts don't use edict 1
func TestCreateEdict_DoesNotReserveEdict1(t *testing.T) {
	db := setupMinisterTestDB(t)

	// Create court with edict 1 already created
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)

	// Create a user edict
	userEdict, err := court.CreateEdict("user-issue", "Implement user feature", "")
	assert.NoError(t, err)
	assert.NotNil(t, userEdict)

	// User edict should NOT be edict 1 (it should get auto-incremented ID)
	assert.NotEqual(t, uint(1), userEdict.ID, "user edicts should not use reserved edict 1")
	assert.Greater(t, userEdict.ID, uint(1), "user edict should have ID > 1")
}

// TestMinisterBase_SessionMethods tests the session getter, setter, and reset methods.
func TestMinisterBase_SessionMethods(t *testing.T) {
	base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)

	// Initially nil
	assert.Nil(t, base.GetSession())

	// Create a mock session
	mockSess := &Session{ID: "test-session"}

	// Set session
	base.SetSession(mockSess)
	assert.NotNil(t, base.GetSession())
	assert.Equal(t, "test-session", base.GetSession().ID)

	// Reset session
	base.ResetSession()
	assert.Nil(t, base.GetSession())
}

// TestMinisterBase_PerChannelSessionIsolation verifies that two sessions
// on the same minister with different channel IDs are independent —
// prompting on "e633" doesn't affect "chancellor".
func TestMinisterBase_PerChannelSessionIsolation(t *testing.T) {
	base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)
	base.ministerID = "chancellor"

	sess1 := &Session{ID: "sess-chancellor"}
	sess1.SetChannelID("chancellor")
	sess2 := &Session{ID: "sess-e633"}
	sess2.SetChannelID("e633")

	base.SetSession(sess1, "chancellor")
	base.SetSession(sess2, "e633")

	// Both sessions exist under their respective channel keys
	assert.NotNil(t, base.GetSession("chancellor"))
	assert.NotNil(t, base.GetSession("e633"))
	assert.Equal(t, "sess-chancellor", base.GetSession("chancellor").ID)
	assert.Equal(t, "sess-e633", base.GetSession("e633").ID)

	// Default (no channelID) returns the minister's own session
	assert.Equal(t, "sess-chancellor", base.GetSession().ID)

	// Resetting "e633" doesn't affect "chancellor"
	base.ResetSession("e633")
	assert.Nil(t, base.GetSession("e633"))
	assert.NotNil(t, base.GetSession("chancellor"))
	assert.Equal(t, "sess-chancellor", base.GetSession("chancellor").ID)

	// GetSessions returns all remaining sessions
	all := base.GetSessions()
	assert.Len(t, all, 1)
	assert.Contains(t, all, "chancellor")
}

// every minister that owns a UI tab. Each tab saves with TabType =
// minister id; this test feeds those ids into RestoreMinisterSession and
// verifies the right minister ends up with a populated session whose
// TabType matches.
func TestRestoreMinisterSession_AllTabs(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, court)

	// Ministers need a SessionConfig to build tool sets — without it,
	// Chancellor.Tools panics on a nil config dereference. No LLM client
	// is required; the test only exercises session restore wiring.
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	for _, id := range []string{"secretary", "chancellor", "forge", "judge"} {
		t.Run(id, func(t *testing.T) {
			err := court.RestoreMinisterSession(id, nil)
			require.NoError(t, err, "%s tab should be resumable", id)

			m := court.GetMinister(id)
			require.NotNil(t, m)
			sess := m.GetSession()
			require.NotNil(t, sess, "%s should have a restored session", id)
			assert.Equal(t, id, sess.TabType)
		})
	}
}

func TestReadProjectContext_ExplicitProjectRoot(t *testing.T) {
	dir := t.TempDir()
	content := "# Project Guide\nUse Go 1.22+"
	err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0644)
	require.NoError(t, err)

	got := readProjectContext("AGENTS.md", dir)
	assert.Equal(t, content, got)
}

func TestReadProjectContext_EmptyProjectRoot_ReturnsEmpty(t *testing.T) {
	// When projectRoot is empty, readProjectContext returns empty string
	// because SetContext is the sole authority for project root in daemon mode.
	got := readProjectContext("AGENTS.md", "")
	assert.Equal(t, "", got, "should return empty string when projectRoot is empty")
}

func TestReadProjectContext_FileNotFound(t *testing.T) {
	dir := t.TempDir()

	got := readProjectContext("AGENTS.md", dir)
	assert.Equal(t, "", got, "should return empty string when file does not exist")
}

// TestRestoreMinisterSession_UnknownTabType verifies the dispatcher
// surfaces a clear error instead of silently restoring the wrong tab.
func TestRestoreMinisterSession_UnknownTabType(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)

	err := court.RestoreMinisterSession("not-a-minister", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minister not found")
}

// TestRestoreSession_SetsPersister verifies that the exported
// RestoreSession method wires the persister into the restored session.
func TestRestoreSession_SetsPersister(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	// Wire a persister into the court so all ministers get it.
	rec := &recordingPersister{}
	court.SetSessionPersister(rec)

	// Restore a session for the chancellor — this calls the exported
	// RestoreSession path.
	err := court.RestoreMinisterSession("secretary", []schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleUser, Content: textContent("hello")},
	})
	require.NoError(t, err)

	chancellor := court.GetMinister("secretary")
	require.NotNil(t, chancellor)
	sess := chancellor.GetSession()
	require.NotNil(t, sess, "chancellor should have a restored session")

	// The restored session must have the persister attached.
	assert.NotNil(t, sess.persister, "restored session must have persister wired")
}

func TestSessBuildEnvBlock_UsesRepoInfoProjectRoot(t *testing.T) {
	t.Run("uses ProjectRoot from repoInfo", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot: "/custom/project/root",
			Branch:      "feature-branch",
		}
		result := sessBuildEnvBlock(info, nil)
		assert.Contains(t, result, "feature-branch", "should contain branch from repoInfo")
		assert.Contains(t, result, "CWD:", "should include working copy path label")
	})

	t.Run("empty ProjectRoot omits working copy path", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot: "",
			Branch:      "main",
		}
		result := sessBuildEnvBlock(info, nil)
		assert.NotContains(t, result, "Working copy path:", "should not include working copy path when empty")
		assert.Contains(t, result, "main", "should still contain branch")
	})

	t.Run("includes OS and Shell info", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot: "/test/root",
			Branch:      "main",
		}
		result := sessBuildEnvBlock(info, nil)
		assert.Contains(t, result, "**OS:**", "should include OS info")
		assert.Contains(t, result, "**Shell:**", "should include Shell info")
	})

	t.Run("OS from runner GetOS", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot: "/test/root",
			Branch:      "main",
		}
		runner := &mockHealthCheckRunner{}
		result := sessBuildEnvBlock(info, runner)
		assert.Contains(t, result, fmt.Sprintf("**OS:** %s", runtime.GOOS), "should use runner GetOS for OS info")
	})

	t.Run("host runner includes no-sandbox note", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot: "/test/root",
			Branch:      "main",
		}
		runner := &mockHealthCheckRunner{runnerType: "host"}
		result := sessBuildEnvBlock(info, runner)
		assert.Contains(t, result, "Sandbox", "should indicate sandbox status for host runner")
		assert.Contains(t, result, "none", "should indicate no sandbox for host runner")
	})

	t.Run("podman runner omits no-sandbox note", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot: "/test/root",
			Branch:      "main",
		}
		runner := &mockHealthCheckRunner{runnerType: "podman"}
		result := sessBuildEnvBlock(info, runner)
		assert.NotContains(t, result, "Sandbox", "should not include no-sandbox note for podman runner")
	})

	t.Run("mock runner omits no-sandbox note", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot: "/test/root",
			Branch:      "main",
		}
		runner := &mockHealthCheckRunner{}
		result := sessBuildEnvBlock(info, runner)
		assert.NotContains(t, result, "Sandbox", "should not include no-sandbox note for non-host runner")
	})

	t.Run("OS is unknown when runner is nil", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot: "/test/root",
			Branch:      "main",
		}
		result := sessBuildEnvBlock(info, nil)
		assert.Contains(t, result, "**OS:** unknown", "should show unknown when runner is nil")
	})

	t.Run("worktree info when IsWorktree is true", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot: "/test/root",
			Branch:      "feature",
			IsWorktree:  true,
		}
		result := sessBuildEnvBlock(info, nil)
		assert.Contains(t, result, "worktree", "should mention worktree when IsWorktree is true")
	})

	t.Run("no worktree warning when not a worktree", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot: "/test/root",
			Branch:      "main",
			IsWorktree:  false,
		}
		result := sessBuildEnvBlock(info, nil)
		assert.NotContains(t, result, "worktree", "should not mention worktree when not a worktree")
	})
}

// === Edict 610: Unified processTask Hook Tests ===
// The following tests verify that the unified MinisterBase.processTask correctly
// dispatches to hooks (preTaskHook, postTaskHook, taskFallback, ctxMiddleware)
// for each minister type that was refactored to use the shared base.

// TestJudge_ProcessTask_NoLLM verifies that Judge's unified processTask
// returns an acknowledged result when no LLM is configured (no fallback,
// no postTaskHook — sealing is handled by ritual then steps).
func TestJudge_ProcessTask_NoLLM(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	edict, err := CreateEdictForTest(db, "Feature to judge")
	assert.NoError(t, err)

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	manifestID := stageManifestForTest(t, db, edict.Key(), "", "feature.go", "Feat", "sha1")
	_ = manifestID

	judge := NewJudge(base, nil)

	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      ctx,
		EdictKey: edict.Key(),
		Work:     "judge the manifests",
		Done:     doneCh,
	}

	judge.MinisterBase.processTask(ctx, task)
	result := <-doneCh

	assert.NoError(t, result.Err)
	assert.True(t, result.Sealed)
	assert.Equal(t, "judge", result.MinisterID)
	assert.Contains(t, result.Output, "no LLM configured")
}

// TestStrategist_ProcessTask_NoLLM_NoHookError verifies that with no LLM
// configured and no postTaskHook, processTask returns an acknowledged result.
// DAG validation is now handled by the ritual's "then: the lings form a
// valid DAG" step, not by a minister hook.
func TestStrategist_ProcessTask_NoLLM_NoHookError(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	edict, err := CreateEdictForTest(db, "Plan with cycle")
	assert.NoError(t, err)

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	strategist := NewWar(base)

	// Insert lings with a circular dependency — the postTaskHook is gone,
	// so processTask won't detect the cycle. The ritual's then step will.
	lingA := &storage.Ling{
		LingID:       "ling-a",
		EdictID:      edict.ID,
		Username:     "testuser",
		Project:      "testproject",
		Description:  "Task A",
		Dependencies: storage.StringArray{"ling-b"},
		Status:       storage.LingPending,
	}
	lingB := &storage.Ling{
		LingID:       "ling-b",
		EdictID:      edict.ID,
		Username:     "testuser",
		Project:      "testproject",
		Description:  "Task B",
		Dependencies: storage.StringArray{"ling-a"},
		Status:       storage.LingPending,
	}
	assert.NoError(t, db.Create(lingA).Error)
	assert.NoError(t, db.Create(lingB).Error)

	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}

	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      ctx,
		EdictKey: key,
		Work:     "Create battle plan",
		Done:     doneCh,
	}

	strategist.MinisterBase.processTask(ctx, task)
	result := <-doneCh

	// No LLM configured → streamTask is skipped, output is the "no LLM" ack.
	// No postTaskHook → no cycle detection in processTask.
	assert.NoError(t, result.Err)
	assert.Contains(t, result.Output, "no LLM configured")
}

// TestStrategist_ProcessTask_ValidDAG_NoHookError verifies that the
// processTask does NOT produce an error when dependencies form a valid DAG.
func TestStrategist_ProcessTask_ValidDAG_NoHookError(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	edict, err := CreateEdictForTest(db, "Plan with valid DAG")
	assert.NoError(t, err)

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	strategist := NewWar(base)

	lingA := &storage.Ling{
		LingID:       "ling-x",
		EdictID:      edict.ID,
		Username:     "testuser",
		Project:      "testproject",
		Description:  "Task X",
		Dependencies: storage.StringArray{},
		Status:       storage.LingPending,
	}
	lingB := &storage.Ling{
		LingID:       "ling-y",
		EdictID:      edict.ID,
		Username:     "testuser",
		Project:      "testproject",
		Description:  "Task Y depends on X",
		Dependencies: storage.StringArray{"ling-x"},
		Status:       storage.LingPending,
	}
	assert.NoError(t, db.Create(lingA).Error)
	assert.NoError(t, db.Create(lingB).Error)

	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}

	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      ctx,
		EdictKey: key,
		Work:     "Create battle plan",
		Done:     doneCh,
	}

	strategist.MinisterBase.processTask(ctx, task)
	result := <-doneCh

	assert.NoError(t, result.Err, "valid DAG should not produce hook error")
	assert.Contains(t, result.Output, "no LLM configured")
}

// TestSage_ProcessTask_NoHooks verifies that the Sage's processTask works
// correctly without any hooks (no ctxMiddleware, no postTaskHook). Since
// there's no LLM, it should return an acknowledged result.
func TestSage_ProcessTask_NoHooks(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	edict, err := CreateEdictForTest(db, "Sage review edict")
	assert.NoError(t, err)

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	sage := NewChancellor(base)

	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}

	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      ctx,
		EdictKey: key,
		Work:     "review the diff",
		Done:     doneCh,
	}

	sage.MinisterBase.processTask(ctx, task)
	result := <-doneCh

	assert.NoError(t, result.Err)
	assert.True(t, result.Sealed)
	assert.Equal(t, "chancellor", result.MinisterID)
	assert.Contains(t, result.Output, "no LLM configured")
}

// TestForge_ProcessTask_PreTaskHookSkipsStream verifies that when the
// preTaskHook returns handled=true, processTask skips streamTask and sends
// the hook's result directly.
func TestForge_ProcessTask_PreTaskHookSkipsStream(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	edict, err := CreateEdictForTest(db, "Forge pre-hook edict")
	assert.NoError(t, err)

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	forge := NewForge(base)

	// Override the preTaskHook with a simple one that returns handled=true
	forge.SetPreTaskHook(func(ctx context.Context, task *Task, notify internal.NotifyFunc) (bool, *Result) {
		return true, &Result{
			Sealed: true,
			Output: "handled by pre-hook",
		}
	})

	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}

	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      ctx,
		EdictKey: key,
		Work:     "forge something",
		Done:     doneCh,
	}

	forge.MinisterBase.processTask(ctx, task)
	result := <-doneCh

	assert.NoError(t, result.Err)
	assert.True(t, result.Sealed)
	assert.Equal(t, "handled by pre-hook", result.Output)
	assert.Equal(t, "forge", result.MinisterID)
}

// TestProcessTask_NoLLM_NoFallback_Acknowledges verifies that when a minister
// has no LLM and no taskFallback, processTask produces an acknowledgement result.
func TestProcessTask_NoLLM_NoFallback_Acknowledges(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	edict, err := CreateEdictForTest(db, "No-config edict")
	assert.NoError(t, err)

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	strategist := NewWar(base)

	// Strategist has no LLM and no taskFallback
	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}

	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      ctx,
		EdictKey: key,
		Work:     "do something",
		Done:     doneCh,
	}

	strategist.MinisterBase.processTask(ctx, task)
	result := <-doneCh

	assert.NoError(t, result.Err)
	assert.Contains(t, result.Output, "no LLM configured")
	assert.True(t, result.Sealed)
}

// TestProcessTask_SendResult_NonBlocking verifies that sendResult doesn't block
// when the done channel is full — it should log a warning instead.
func TestProcessTask_SendResult_NonBlocking(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	edict, err := CreateEdictForTest(db, "Full channel edict")
	assert.NoError(t, err)

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	strategist := NewWar(base)

	// Create a done channel with buffer 1 and fill it
	doneCh := make(chan Result, 1)
	doneCh <- Result{Output: "stale"}

	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}

	task := &Task{
		Ctx:      ctx,
		EdictKey: key,
		Work:     "do something",
		Done:     doneCh,
	}

	// This should NOT block even though the channel is full
	strategist.MinisterBase.processTask(ctx, task)

	// The channel should still contain the stale result (new result was dropped)
	select {
	case r := <-doneCh:
		assert.Equal(t, "stale", r.Output, "stale result should remain; new result dropped")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("should not block reading from done channel")
	}
}

// TestRunLoop_ConcurrentTaskDispatch verifies that RunLoop dispatches tasks
// concurrently: a slow task should not block prompt processing.
func TestRunLoop_ConcurrentTaskDispatch(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	base.ministerID = "test"
	strategist := NewWar(base)
	strategist.SetNotify(func(any) {})

	var taskStarted sync.WaitGroup
	var taskRelease sync.WaitGroup
	taskStarted.Add(1)
	taskRelease.Add(1)

	slowTaskHandler := func(ctx context.Context, task *Task) {
		taskStarted.Done()
		taskRelease.Wait() // simulate slow work
		base.sendResult(task, Result{MinisterID: "test", Sealed: true, Output: "done"})
	}

	go base.RunLoop(ctx, strategist, nil, slowTaskHandler)

	// Send a slow task
	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      ctx,
		EdictKey: storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"},
		Work:     "slow task",
		Done:     doneCh,
	}
	base.tasks <- task

	// Wait for task to start
	taskStarted.Wait()

	// Now send a prompt — it should be processed even though the task is still running
	promptDone := make(chan struct{})
	base.SubmitPrompt(&Prompt{
		Ctx:      ctx,
		Message:  "hello while task runs",
		EdictKey: storage.EdictKey{ID: 0, Username: "testuser", Project: "testproject"},
	})

	// Give the prompt a moment to be received; if dispatch were blocking,
	// the prompt would never be processed until the task completes.
	select {
	case <-promptDone:
		// This is fine — prompt was received by the channel.
	case <-time.After(200 * time.Millisecond):
	}

	// Release the task and verify it completes
	taskRelease.Done()
	select {
	case r := <-doneCh:
		assert.Equal(t, "done", r.Output)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task result")
	}
}

// TestMinisterImpl_Tools_IncludesCommonTools verifies that every minister
// gets ask_ruler and tian_ledger via the commonTools constant, even when
// their extra_tools definition does not list them. Only the secretary should
// have consult_minister (via extra_tools).
func TestMinisterImpl_Tools_IncludesCommonTools(t *testing.T) {
	// Build a tool registry with consult_minister and ask_ruler registered as extra tools
	registry := tools.NewToolRegistry()
	registry.RegisterExtraFactory("consult_minister", func(mid string) tools.Tool {
		return tools.ConsultMinisterTool{Ctx: tools.ToolContext{MinisterID: mid}}
	})
	registry.RegisterExtra("enact_ritual", tools.InvokeRitualTool{})
	registry.RegisterExtra("tian_ledger", tools.TianLedgerTool{})
	registry.RegisterExtraFactory("ask_ruler", func(mid string) tools.Tool {
		return tools.AskRulerTool{MinisterID: mid}
	})

	// Test every minister defined in the YAML
	defs, err := LoadMinisters()
	require.NoError(t, err)

	for _, def := range defs {
		t.Run(def.ID, func(t *testing.T) {
			base := NewMinisterBase(nil, nil, nil, "testuser", "testproject", nil)
			base.SetToolRegistry(registry)
			m := NewMinister(def, base)

			ts := m.Tools()
			names := make(map[string]bool, len(ts))
			for _, tool := range ts {
				names[tool.Name()] = true
			}

			// Every minister should have ask_ruler and tian_ledger via commonTools
			assert.True(t, names["ask_ruler"],
				"%s should have ask_ruler via commonTools", def.ID)
			assert.True(t, names["tian_ledger"],
				"%s should have tian_ledger via commonTools", def.ID)

			// Only the secretary should have consult_minister (via extra_tools)
			if def.ID == "secretary" {
				assert.True(t, names["consult_minister"],
					"secretary should have consult_minister via extra_tools")
			} else {
				assert.False(t, names["consult_minister"],
					"%s should NOT have consult_minister", def.ID)
			}
		})
	}
}

// TestCreateSessionWithOpts_ExcludeTools verifies that excluded tools are
// actually filtered from the session's tool catalog.
func TestCreateSessionWithOpts_ExcludeTools(t *testing.T) {
	db := setupMinisterTestDB(t)

	// Build a tool registry with ask_ruler (common tool) registered
	registry := tools.NewToolRegistry()
	registry.RegisterExtraFactory("ask_ruler", func(mid string) tools.Tool {
		return tools.AskRulerTool{MinisterID: mid}
	})

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	base.SetToolRegistry(registry)

	// Create a forge minister (permissions rwxr---w-)
	forge := NewForge(base)
	forge.SetNotify(func(any) {})

	// Use a no-op LLM mock
	mockLLM := mocks.NewLLMProvider()
	forge.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})

	// Create session WITHOUT excluding tools — ask_ruler should be present
	session, err := CreateSessionWithOpts(forge, mockLLM,
		&SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}},
		func(any) {},
		CreateSessionOpts{
			EdictKey: storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"},
		})
	require.NoError(t, err)
	assert.NotNil(t, session)
	_, hasZhengming := session.toolCatalog["ask_ruler"]
	assert.True(t, hasZhengming,
		"session without ExcludeTools should have ask_ruler in toolCatalog")

	// Create session WITH ExcludeTools — ask_ruler should be absent
	session2, err := CreateSessionWithOpts(forge, mockLLM,
		&SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}},
		func(any) {},
		CreateSessionOpts{
			EdictKey:     storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"},
			ExcludeTools: []string{"ask_ruler"},
		})
	require.NoError(t, err)
	assert.NotNil(t, session2)
	_, hasZhengming2 := session2.toolCatalog["ask_ruler"]
	assert.False(t, hasZhengming2,
		"session with ExcludeTools=[ask_ruler] should NOT have ask_ruler in toolCatalog")
}

// TestRunLoop_GracefulShutdownWaitsInFlightTasks verifies that RunLoop's
// context cancellation waits for in-flight tasks to complete before returning.
func TestRunLoop_GracefulShutdownWaitsInFlightTasks(t *testing.T) {
	db := setupMinisterTestDB(t)

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	base.ministerID = "test"
	strategist := NewWar(base)
	strategist.SetNotify(func(any) {})

	var taskDone sync.WaitGroup
	taskDone.Add(1)

	taskHandler := func(ctx context.Context, task *Task) {
		defer taskDone.Done()
		time.Sleep(100 * time.Millisecond) // simulate work
		base.sendResult(task, Result{MinisterID: "test", Sealed: true, Output: "done"})
	}

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		base.RunLoop(ctx, strategist, nil, taskHandler)
		close(loopDone)
	}()

	// Send a task
	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      ctx,
		EdictKey: storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"},
		Work:     "work",
		Done:     doneCh,
	}
	base.tasks <- task

	// Cancel while the task is in-flight
	time.Sleep(20 * time.Millisecond) // ensure task started
	cancel()

	// RunLoop should wait for the task to finish before returning
	select {
	case <-loopDone:
		// RunLoop returned — but did the task complete?
	case <-time.After(2 * time.Second):
		t.Fatal("RunLoop did not return within timeout; may not be waiting for tasks")
	}

	select {
	case r := <-doneCh:
		assert.Equal(t, "done", r.Output, "in-flight task should have completed before shutdown")
	case <-time.After(time.Second):
		t.Fatal("in-flight task result was not delivered")
	}
}

// TestStreamTask_RitualTaskCreatesOwnSession verifies that when
// existingSession is nil, streamTask creates its own ephemeral session
// rather than reusing the minister's interactive session (m.session).
func TestStreamTask_RitualTaskCreatesOwnSession(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	mockLLM := mocks.NewLLMProvider()
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	base.ministerID = "chancellor"
	sage := NewChancellor(base)
	sage.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})
	sage.SetNotify(func(any) {})

	// Create an interactive session on m.session (simulating ProcessPrompt)
	interactiveSession, err := CreateSession(sage, mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, func(any) {}, "chancellor")
	require.NoError(t, err)
	base.SetSession(interactiveSession)
	interactiveMsgCount := len(interactiveSession.Messages())

	// Call streamTask with existingSession=nil (ritual task scenario)
	session, _, err := base.streamTask(ctx, "ritual work", storage.EdictKey{ID: 42, Username: "testuser", Project: "testproject"}, "# scratch", func(any) {}, nil, "chancellor", nil)
	require.NoError(t, err)

	// The returned session must NOT be the interactive session
	assert.NotNil(t, session)
	assert.NotEqual(t, interactiveSession, session, "ritual task must create its own ephemeral session, not reuse m.session")

	// The interactive session should be untouched
	assert.Equal(t, interactiveMsgCount, len(interactiveSession.Messages()), "interactive session messages must be unchanged")
}

// TestStreamTask_ExistingSessionStillReused verifies that when an
// existingSession is provided, it is still reused (multi-turn path).
func TestStreamTask_ExistingSessionStillReused(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	mockLLM := mocks.NewLLMProvider()
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	base.ministerID = "chancellor"
	sage := NewChancellor(base)
	sage.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})
	sage.SetNotify(func(any) {})

	existing, err := CreateSession(sage, mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, func(any) {}, "chancellor")
	require.NoError(t, err)

	session, _, err := base.streamTask(ctx, "more work", storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"}, "", func(any) {}, existing, "chancellor", nil)
	require.NoError(t, err)

	assert.Equal(t, existing, session, "existing session should be reused")
}

// TestRunLoop_SlowPromptDoesNotBlockTaskDispatch verifies that a slow prompt
// does not block task dispatch: while ProcessPrompt is blocked (simulating a
// slow LLM stream or interactive WaitForAnswer), a dispatched task should
// start processing immediately.
func TestRunLoop_SlowPromptDoesNotBlockTaskDispatch(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	base.ministerID = "test"
	strategist := NewWar(base)
	strategist.SetNotify(func(any) {})

	var promptStarted sync.WaitGroup
	var promptRelease sync.WaitGroup
	promptStarted.Add(1)
	promptRelease.Add(1)

	slowPromptHandler := func(ctx context.Context, prompt *Prompt) {
		promptStarted.Done()
		promptRelease.Wait() // simulate slow LLM / interactive blocking
	}

	var taskStarted sync.WaitGroup
	taskStarted.Add(1)

	taskHandler := func(ctx context.Context, task *Task) {
		taskStarted.Done()
		base.sendResult(task, Result{MinisterID: "test", Sealed: true, Output: "done"})
	}

	go base.RunLoop(ctx, strategist, slowPromptHandler, taskHandler)

	// Send a slow prompt
	base.SubmitPrompt(&Prompt{
		Ctx:      ctx,
		Message:  "slow prompt",
		EdictKey: storage.EdictKey{ID: 0, Username: "testuser", Project: "testproject"},
	})

	// Wait for the prompt to start processing
	promptStarted.Wait()

	// Now send a task — it should start immediately even though the prompt is still blocked
	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      ctx,
		EdictKey: storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"},
		Work:     "task while prompt blocked",
		Done:     doneCh,
	}
	base.tasks <- task

	// Verify the task starts within a reasonable timeout.
	// If ProcessPrompt were called synchronously, the loop would be blocked
	// and this task would never start until the prompt completes.
	taskDone := make(chan struct{})
	go func() {
		taskStarted.Wait()
		close(taskDone)
	}()

	select {
	case <-taskDone:
		// Task started successfully while prompt was still blocked
	case <-time.After(500 * time.Millisecond):
		t.Fatal("task did not start — slow prompt blocked task dispatch")
	}

	// Release the prompt and verify the task completes
	promptRelease.Done()
	select {
	case r := <-doneCh:
		assert.Equal(t, "done", r.Output)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task result")
	}
}

// TestProcessTask_EmitsStreamStartAndDone verifies that processTask emits
// StreamStartMsg (with the correct ChannelID) before streaming begins and
// StreamDoneMsg after streaming completes when an LLM is configured.
func TestProcessTask_EmitsStreamStartAndDone(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	mockLLM := mocks.NewLLMProvider()
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	sage := NewChancellor(base)
	sage.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})

	var mu sync.Mutex
	var msgs []any
	sage.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, msg)
	})

	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:       ctx,
		EdictKey:  storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"},
		Work:      "do the work",
		Done:      doneCh,
		ChannelID: "e633",
	}

	sage.MinisterBase.processTask(ctx, task)

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task result")
	}

	mu.Lock()
	defer mu.Unlock()

	var startIdx, doneIdx int = -1, -1
	for i, msg := range msgs {
		switch msg.(type) {
		case StreamStartMsg:
			if startIdx == -1 {
				startIdx = i
			}
		case StreamDoneMsg:
			if doneIdx == -1 {
				doneIdx = i
			}
		}
	}

	require.NotEqual(t, -1, startIdx, "StreamStartMsg should be emitted")
	require.NotEqual(t, -1, doneIdx, "StreamDoneMsg should be emitted")
	assert.Less(t, startIdx, doneIdx, "StreamStartMsg should come before StreamDoneMsg")

	startMsg, ok := msgs[startIdx].(StreamStartMsg)
	require.True(t, ok)
	assert.Equal(t, "e633", startMsg.ChannelID, "StreamStartMsg should use task ChannelID")
	assert.Equal(t, uint(1), startMsg.EdictID, "StreamStartMsg should carry the edict ID")

	doneMsg, ok := msgs[doneIdx].(StreamDoneMsg)
	require.True(t, ok)
	assert.Equal(t, "e633", doneMsg.ChannelID, "StreamDoneMsg should use task ChannelID")
}

// TestProcessTask_StreamStartDefaultChannelID verifies that when ChannelID is
// empty, processTask falls back to the minister's own ID for stream routing.
func TestProcessTask_StreamStartDefaultChannelID(t *testing.T) {
	db := setupMinisterTestDB(t)
	ctx := context.Background()

	mockLLM := mocks.NewLLMProvider()
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	sage := NewChancellor(base)
	sage.SetMinisterConfig(mockLLM, &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}, repo.RepoInfo{})

	var mu sync.Mutex
	var startChannelID, doneChannelID string
	sage.SetNotify(func(msg any) {
		mu.Lock()
		defer mu.Unlock()
		switch m := msg.(type) {
		case StreamStartMsg:
			startChannelID = m.ChannelID
		case StreamDoneMsg:
			doneChannelID = m.ChannelID
		}
	})

	doneCh := make(chan Result, 1)
	task := &Task{
		Ctx:      ctx,
		EdictKey: storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"},
		Work:     "do the work",
		Done:     doneCh,
		// ChannelID left empty — should fall back to minister ID
	}

	sage.MinisterBase.processTask(ctx, task)

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task result")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "chancellor", startChannelID, "StreamStartMsg should fall back to minister ID")
	assert.Equal(t, "chancellor", doneChannelID, "StreamDoneMsg should fall back to minister ID")
}

// TestMinisterBase_AppendToIntent_InvalidatesSeals verifies that the
// minister-path AppendToIntent invalidates seals after modifying the intent.
func TestMinisterBase_AppendToIntent_InvalidatesSeals(t *testing.T) {
	db := setupMinisterTestDB(t)

	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)

	edict := storage.Edict{Intent: "original intent", Username: "testuser", Project: "testproject"}
	require.NoError(t, db.Create(&edict).Error)

	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}
	sealSvc := storage.NewSealService(db)
	require.NoError(t, sealSvc.GrantSeal(key, "judge", storage.JSON{}))
	require.NoError(t, sealSvc.GrantSeal(key, "chancellor", storage.JSON{}))

	// Confirm seals are valid before append
	hasJudge, err := sealSvc.HasSeal(key, "judge")
	require.NoError(t, err)
	require.True(t, hasJudge)

	// Append — should invalidate seals
	require.NoError(t, base.AppendToIntent(key, "clarification from minister"))

	hasJudge, err = sealSvc.HasSeal(key, "judge")
	require.NoError(t, err)
	require.False(t, hasJudge, "judge seal should be stale after minister AppendToIntent")

	hasChancellor, err := sealSvc.HasSeal(key, "chancellor")
	require.NoError(t, err)
	require.False(t, hasChancellor, "chancellor seal should be stale after minister AppendToIntent")
}
