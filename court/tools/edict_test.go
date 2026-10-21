package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/afittestide/asimi/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupEdictTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	// Migrate all required tables
	if err := db.AutoMigrate(&storage.Edict{}, &storage.Seal{}, &storage.Zhengming{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	return db
}

func TestTransitionEdictTool_Cancel(t *testing.T) {
	db := setupEdictTestDB(t)

	// Create an edict
	edict := storage.Edict{
		ID:       1,
		Username: "testuser",
		Project:  "testproject",
		Intent:   "Test edict",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	tool := TransitionEdictTool{DB: db, Username: "testuser", Project: "testproject"}

	// Test cancelling
	result, err := tool.Call(context.Background(), fmt.Sprintf(`{"edict_id": %d, "status": "cancelled", "reason": "no longer needed"}`, edict.ID))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify result
	if result == "" {
		t.Fatal("Expected non-empty result")
	}

	// Verify edict was cancelled
	var updated storage.Edict
	if err := db.First(&updated, "id = ?", edict.ID).Error; err != nil {
		t.Fatalf("failed to get updated edict: %v", err)
	}
	if updated.CancelledAt == nil {
		t.Errorf("Expected cancelled_at to be set")
	}

	// Verify derived status is cancelled
	sealService := storage.NewSealService(db)
	status, err := sealService.GetEdictStatus(edict.Key())
	if err != nil {
		t.Fatalf("Failed to get edict status: %v", err)
	}
	if status != storage.EdictCancelled {
		t.Errorf("Expected status to be cancelled, got: %s", status)
	}
}

func TestTransitionEdictTool_InvalidStatus(t *testing.T) {
	db := setupEdictTestDB(t)

	// Create an edict
	edict := storage.Edict{
		ID:       1,
		Username: "testuser",
		Project:  "testproject",
		Intent:   "Test edict",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	tool := TransitionEdictTool{DB: db, Username: "testuser", Project: "testproject"}

	// Test invalid status
	_, err := tool.Call(context.Background(), fmt.Sprintf(`{"edict_id": %d, "status": "invalid_status"}`, edict.ID))
	if err == nil {
		t.Fatal("Expected error for invalid status")
	}
}

func TestTransitionEdictTool_BlockedToSealed(t *testing.T) {
	db := setupEdictTestDB(t)

	// Create an edict (not blocked)
	edict := storage.Edict{
		ID:       1,
		Username: "testuser",
		Project:  "testproject",
		Intent:   "Test edict",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	tool := TransitionEdictTool{DB: db, Username: "testuser", Project: "testproject"}

	// Test that we can seal a non-blocked edict
	result, err := tool.Call(context.Background(), fmt.Sprintf(`{"edict_id": %d, "status": "sealed"}`, edict.ID))
	if err != nil {
		t.Fatalf("Expected no error when sealing edict, got: %v", err)
	}
	if result == "" {
		t.Fatal("Expected non-empty result")
	}

	// Verify edict is now sealed
	sealService := storage.NewSealService(db)
	status, err := sealService.GetEdictStatus(edict.Key())
	if err != nil {
		t.Fatalf("Failed to get edict status: %v", err)
	}
	if status != storage.EdictSealed {
		t.Errorf("Expected status to be sealed, got: %s", status)
	}
}

func TestTransitionEdictTool_NonExistent(t *testing.T) {
	db := setupEdictTestDB(t)

	tool := TransitionEdictTool{DB: db, Username: "testuser", Project: "testproject"}

	// Test non-existent edict
	_, err := tool.Call(context.Background(), `{"edict_id": 999, "status": "active"}`)
	if err == nil {
		t.Fatal("Expected error for non-existent edict")
	}
}

func TestTransitionEdictTool_MissingFields(t *testing.T) {
	db := setupEdictTestDB(t)

	tool := TransitionEdictTool{DB: db, Username: "testuser", Project: "testproject"}

	// Test missing edict_id
	_, err := tool.Call(context.Background(), `{"status": "active"}`)
	if err == nil {
		t.Fatal("Expected error for missing edict_id")
	}

	// Test missing status
	_, err = tool.Call(context.Background(), `{"edict_id": 1}`)
	if err == nil {
		t.Fatal("Expected error for missing status")
	}
}

func TestListEdictsTool_FilterByStatus(t *testing.T) {
	db := setupEdictTestDB(t)

	// Create edicts
	edicts := []storage.Edict{
		{ID: 1, Username: "testuser", Project: "testproject", Intent: "Active edict"},
		{ID: 2, Username: "testuser", Project: "testproject", Intent: "Blocked edict"},
		{ID: 3, Username: "testuser", Project: "testproject", Intent: "Another blocked"},
		{ID: 4, Username: "testuser", Project: "testproject", Intent: "Sealed edict"},
	}
	for i := range edicts {
		if err := db.Create(&edicts[i]).Error; err != nil {
			t.Fatalf("failed to create edict: %v", err)
		}
	}

	// Create a pending zhengming for the second edict to make it blocked
	zhengming := storage.Zhengming{
		RequestID:  "test-blocked",
		EdictID:    edicts[1].ID,
		Username:   "testuser",
		Project:    "testproject",
		MinisterID: "forge",
		Status:     storage.ZhengmingPending,
		TimeoutAt:  time.Now().Add(time.Hour),
	}
	if err := db.Create(&zhengming).Error; err != nil {
		t.Fatalf("failed to create zhengming: %v", err)
	}

	// Seal the fourth edict
	sealService := storage.NewSealService(db)
	if err := sealService.GrantSeal(edicts[3].Key(), "judge", storage.JSON{}); err != nil {
		t.Fatalf("failed to grant judge seal: %v", err)
	}
	if err := sealService.GrantSeal(edicts[3].Key(), "chancellor", storage.JSON{}); err != nil {
		t.Fatalf("failed to grant chancellor seal: %v", err)
	}
	if err := sealService.GrantSeal(edicts[3].Key(), "ruler", storage.JSON{}); err != nil {
		t.Fatalf("failed to grant ruler seal: %v", err)
	}

	tool := ListEdictsTool{DB: db, Username: "testuser", Project: "testproject"}

	// Test filtering by blocked status
	result, err := tool.Call(context.Background(), `{"status": "blocked"}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify result contains only blocked edicts
	if result == "" {
		t.Fatal("Expected non-empty result")
	}
	// Should contain the blocked edict
	if !containsSubstring(result, "Blocked edict") {
		t.Errorf("Expected result to contain blocked edict, got: %s", result)
	}
	// Should not contain active or sealed edicts
	if containsSubstring(result, "Active edict") || containsSubstring(result, "Sealed edict") {
		t.Errorf("Result should not contain non-blocked edicts, got: %s", result)
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestGetEdictStatusTool_ReturnsStatus(t *testing.T) {
	db := setupEdictTestDB(t)

	edict := storage.Edict{
		ID:       1,
		Username: "testuser",
		Project:  "testproject",
		Intent:   "Test edict",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	tool := GetEdictStatusTool{DB: db, Username: "testuser", Project: "testproject"}

	result, err := tool.Call(context.Background(), `{"edict_id": 1}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if !containsSubstring(result, "active") {
		t.Errorf("Expected result to contain 'active', got: %s", result)
	}
	if !containsSubstring(result, "Test edict") {
		t.Errorf("Expected result to contain intent, got: %s", result)
	}
}

func TestGetEdictStatusTool_NonExistentEdict(t *testing.T) {
	db := setupEdictTestDB(t)

	tool := GetEdictStatusTool{DB: db, Username: "testuser", Project: "testproject"}

	_, err := tool.Call(context.Background(), `{"edict_id": 999}`)
	if err == nil {
		t.Fatal("Expected error for non-existent edict")
	}
}

// TestListEdictsTool_EmptyScopeReportsScope covers edict 968: when a scoped read
// matches zero rows it must name the queried (username, project) and any nearby
// match, never silently look like "no rows at all".
func TestListEdictsTool_EmptyScopeReportsScope(t *testing.T) {
	db := setupEdictTestDB(t)

	// Rows live under another scope — exactly the reported split-brain: origin
	// slug (daonb/...) vs pinned slug (oct7-io/...).
	edict := storage.Edict{ID: 927, Username: "daonb", Project: "oct7-io/data-poisoning-benchmark", Intent: "Give the sandbox gh CLI"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	tool := ListEdictsTool{DB: db, Username: "daonb", Project: "daonb/data-poisoning-benchmark"}
	result, err := tool.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	var report ScopeReport
	if err := json.Unmarshal([]byte(result), &report); err != nil {
		t.Fatalf("empty-scope result is not the scope-report shape: %v\n%s", err, result)
	}
	if report.Rows != 0 {
		t.Errorf("report.Rows = %d, want 0", report.Rows)
	}
	if report.Username != "daonb" || report.Project != "daonb/data-poisoning-benchmark" {
		t.Errorf("report queried scope = %q/%q, want daonb/daonb/data-poisoning-benchmark", report.Username, report.Project)
	}
	if len(report.Nearby) != 1 {
		t.Fatalf("report.Nearby = %+v, want 1 nearby match", report.Nearby)
	}
	if report.Nearby[0].Project != "oct7-io/data-poisoning-benchmark" || report.Nearby[0].Count != 1 {
		t.Errorf("nearby = %+v, want 1 row for oct7-io/data-poisoning-benchmark", report.Nearby[0])
	}

	// The Format path must surface the report rather than "Found 0 edicts".
	rendered := tool.Format(`{}`, result, nil)
	if !containsSubstring(rendered, "oct7-io/data-poisoning-benchmark") {
		t.Errorf("Format output %q should surface the scope report", rendered)
	}
}

// TestGetEdictStatusTool_EmptyScopeReportsScope covers edict 968 for the
// single-row reader: a wrong-scope lookup must not collapse into a bare
// "record not found".
func TestGetEdictStatusTool_EmptyScopeReportsScope(t *testing.T) {
	db := setupEdictTestDB(t)

	edict := storage.Edict{ID: 927, Username: "daonb", Project: "oct7-io/data-poisoning-benchmark", Intent: "Give the sandbox gh CLI"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	tool := GetEdictStatusTool{DB: db, Username: "daonb", Project: "daonb/data-poisoning-benchmark"}
	result, err := tool.Call(context.Background(), `{"edict_id": 927}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	var report ScopeReport
	if err := json.Unmarshal([]byte(result), &report); err != nil {
		t.Fatalf("empty-scope result is not the scope-report shape: %v\n%s", err, result)
	}
	if report.Rows != 0 || len(report.Nearby) != 1 {
		t.Errorf("report = %+v, want 0 rows and 1 nearby match", report)
	}
	if report.Nearby[0].Project != "oct7-io/data-poisoning-benchmark" {
		t.Errorf("nearby project = %q, want oct7-io/data-poisoning-benchmark", report.Nearby[0].Project)
	}

	rendered := tool.Format(`{"edict_id": 927}`, result, nil)
	if !containsSubstring(rendered, "oct7-io/data-poisoning-benchmark") {
		t.Errorf("Format output %q should surface the scope report", rendered)
	}
}

// TestListEdictsTool_StatusFilterMissDoesNotLie covers the Chancellor-found
// blocking defect in edict 968: when rows DO exist in scope but the optional
// status filter excludes them all, list_edicts must NOT emit the "0 rows for
// username=… project=…" report — that message is the very false "edicts are
// gone" the edict exists to eliminate.
func TestListEdictsTool_StatusFilterMissDoesNotLie(t *testing.T) {
	db := setupEdictTestDB(t)

	// One row in scope, active (no seal, no pending zhengming).
	edict := storage.Edict{ID: 1, Username: "testuser", Project: "testproject", Intent: "Active edict"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	tool := ListEdictsTool{DB: db, Username: "testuser", Project: "testproject"}
	result, err := tool.Call(context.Background(), `{"status": "blocked"}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Must not claim the scope is empty: the row exists, it just does not match.
	if containsSubstring(result, `0 rows for username="testuser" project="testproject"`) {
		t.Errorf("status-filter miss emitted a FALSE empty-scope report: %s", result)
	}

	var payload struct {
		InScope int    `json:"in_scope"`
		Matched int    `json:"matched"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("filter-miss result is not the expected shape: %v\n%s", err, result)
	}
	if payload.InScope != 1 || payload.Matched != 0 || payload.Status != "blocked" {
		t.Errorf("payload = %+v, want in_scope=1 matched=0 status=blocked", payload)
	}
	if !containsSubstring(payload.Message, "1 edicts in scope") || !containsSubstring(payload.Message, `status="blocked"`) {
		t.Errorf("message %q should name the in-scope count and filter", payload.Message)
	}

	rendered := tool.Format(`{"status": "blocked"}`, result, nil)
	if !containsSubstring(rendered, "in scope") {
		t.Errorf("Format output %q should surface the filter-miss message", rendered)
	}
}
