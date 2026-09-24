package tools

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/afittestide/asimi/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupLingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := db.AutoMigrate(&storage.Ling{}, &storage.TianEvent{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

// mockLingIgniter records the calls InsertLingTool makes to its igniter.
type mockLingIgniter struct {
	mu        sync.Mutex
	published []string
	triggered []uint
}

func (m *mockLingIgniter) PublishLingCreated(_ storage.EdictKey, lingID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, lingID)
}

func (m *mockLingIgniter) TriggerLingIgnition(key storage.EdictKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.triggered = append(m.triggered, key.ID)
}

func (m *mockLingIgniter) publishedSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.published...)
}

func (m *mockLingIgniter) triggeredSnapshot() []uint {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]uint{}, m.triggered...)
}

// TestInsertLingTool_MinisterDefaultsToForge verifies that omitting the
// optional minister parameter persists "forge" on the ling (edict 847, B).
func TestInsertLingTool_MinisterDefaultsToForge(t *testing.T) {
	db := setupLingTestDB(t)
	tool := InsertLingTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(context.Background(), `{"edict_id":7,"description":"do the thing"}`)
	if err != nil {
		t.Fatalf("insert_ling failed: %v", err)
	}
	if !strings.Contains(result, "minister forge") {
		t.Errorf("result should report the default minister, got %q", result)
	}

	var ling storage.Ling
	if err := db.Where("edict_id = ?", 7).First(&ling).Error; err != nil {
		t.Fatalf("failed to load ling: %v", err)
	}
	if ling.Minister != "forge" {
		t.Errorf("Minister = %q, want %q", ling.Minister, "forge")
	}
	if ling.Status != storage.LingPending {
		t.Errorf("Status = %q, want %q", ling.Status, storage.LingPending)
	}
}

// TestInsertLingTool_MinisterIsHonoured verifies that an explicit minister is
// persisted verbatim (edict 847, B).
func TestInsertLingTool_MinisterIsHonoured(t *testing.T) {
	db := setupLingTestDB(t)
	tool := InsertLingTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	if _, err := tool.Call(context.Background(), `{"edict_id":8,"description":"judge the thing","minister":"judge"}`); err != nil {
		t.Fatalf("insert_ling failed: %v", err)
	}

	var ling storage.Ling
	if err := db.Where("edict_id = ?", 8).First(&ling).Error; err != nil {
		t.Fatalf("failed to load ling: %v", err)
	}
	if ling.Minister != "judge" {
		t.Errorf("Minister = %q, want %q", ling.Minister, "judge")
	}
}

// TestInsertLingTool_PublishesCreatedAndTriggersIgnition verifies the two
// side-effects of a completed insert: a ling_created publish (observability
// only) and an ignition trigger (edict 847, D+F).
func TestInsertLingTool_PublishesCreatedAndTriggersIgnition(t *testing.T) {
	db := setupLingTestDB(t)
	igniter := &mockLingIgniter{}
	tool := InsertLingTool{
		Ctx: ToolContext{
			DB:       db,
			Username: "testuser",
			Project:  "testproject",
		},
		Igniter: igniter,
	}

	result, err := tool.Call(context.Background(), `{"edict_id":9,"description":"task"}`)
	if err != nil {
		t.Fatalf("insert_ling failed: %v", err)
	}

	published := igniter.publishedSnapshot()
	if len(published) != 1 {
		t.Fatalf("expected exactly one ling_created publish, got %d", len(published))
	}
	if !strings.Contains(result, published[0]) {
		t.Errorf("published ling id %q should appear in result %q", published[0], result)
	}

	triggered := igniter.triggeredSnapshot()
	if len(triggered) != 1 || triggered[0] != 9 {
		t.Errorf("expected exactly one ignition for edict 9, got %v", triggered)
	}

	// The igniter must exist to publish/ignite, but publication must not itself
	// create a TianEvent (edict 847 rule D says "observability ONLY"): the tool
	// does not write events directly.
	var eventCount int64
	if err := db.Model(&storage.TianEvent{}).Count(&eventCount).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 0 {
		t.Errorf("insert_ling must not write Tian events directly, found %d", eventCount)
	}
}

// TestInsertLingTool_NilIgniterDoesNotPanic verifies the tool tolerates a nil
// igniter (e.g. a Court without ignition wiring) and unit tests that only
// exercise persistence.
func TestInsertLingTool_NilIgniterDoesNotPanic(t *testing.T) {
	db := setupLingTestDB(t)
	tool := InsertLingTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	if _, err := tool.Call(context.Background(), `{"edict_id":10,"description":"task"}`); err != nil {
		t.Fatalf("insert_ling with nil igniter failed: %v", err)
	}
}
