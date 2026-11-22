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

// TestListEdictsTool_PopulatedReturnsTableEnvelope verifies a populated
// list_edicts opts into the TUI markdown-table envelope (edict 988) with raw
// carrying the full row data.
func TestListEdictsTool_PopulatedReturnsTableEnvelope(t *testing.T) {
	db := setupEdictTestDB(t)

	edict := storage.Edict{ID: 1, Username: "testuser", Project: "testproject", Intent: "Active edict"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	tool := ListEdictsTool{DB: db, Username: "testuser", Project: "testproject"}
	result, err := tool.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("list_edicts failed: %v", err)
	}

	var env markdownTableEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("result is not a table envelope: %v\n%s", err, result)
	}
	if env.Format != markdownTableFormat {
		t.Errorf("Format = %q, want %q", env.Format, markdownTableFormat)
	}
	wantCols := []string{"edict_id", "status", "intent"}
	for i, c := range wantCols {
		if i >= len(env.Columns) || env.Columns[i] != c {
			t.Fatalf("Columns = %v, want %v", env.Columns, wantCols)
		}
	}
	if len(env.Rows) != 1 {
		t.Fatalf("Rows = %d, want 1", len(env.Rows))
	}
	if env.Rows[0][0] != "1" || env.Rows[0][1] != "active" || env.Rows[0][2] != "Active edict" {
		t.Errorf("row = %v, want [1 active Active edict]", env.Rows[0])
	}

	raw, err := json.Marshal(env.Raw)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	var rawResults []map[string]any
	if err := json.Unmarshal(raw, &rawResults); err != nil {
		t.Fatalf("raw is not a list of edict results: %v\n%s", err, raw)
	}
	if len(rawResults) != 1 || rawResults[0]["intent"] != "Active edict" {
		t.Errorf("raw = %+v, want the full edict result", rawResults)
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

// TestListEdictsTool_FilterAppliesBeforeLimit covers edict 1080: the derived
// status filter must run over the whole scope, not over whatever LIMIT rows
// the SQL happened to return first. With more cancelled edicts than Limit, all
// up to Limit are returned; with Limit above the count, all are returned.
func TestListEdictsTool_FilterAppliesBeforeLimit(t *testing.T) {
	db := setupEdictTestDB(t)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 4; i++ {
		cancelled := base.Add(time.Duration(i) * time.Hour)
		e := storage.Edict{
			ID:          uint(i),
			Username:    "testuser",
			Project:     "testproject",
			Intent:      fmt.Sprintf("Cancelled edict %d", i),
			CancelledAt: &cancelled,
			CreatedAt:   base.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("failed to create edict: %v", err)
		}
	}

	tool := ListEdictsTool{DB: db, Username: "testuser", Project: "testproject"}

	// Limit below the number of matches: all up to Limit are returned.
	result, err := tool.Call(context.Background(), `{"status": "cancelled", "limit": 2}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	env, ok := isMarkdownTableEnvelope(result)
	if !ok {
		t.Fatalf("result is not a table envelope: %s", result)
	}
	if len(env.Rows) != 2 {
		t.Fatalf("Rows = %d, want 2 (Limit)", len(env.Rows))
	}
	for _, row := range env.Rows {
		if row[1] != "cancelled" {
			t.Errorf("row = %v, want status cancelled", row)
		}
	}

	// Limit above the number of matches: all are returned.
	result, err = tool.Call(context.Background(), `{"status": "cancelled", "limit": 10}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	env, ok = isMarkdownTableEnvelope(result)
	if !ok {
		t.Fatalf("result is not a table envelope: %s", result)
	}
	if len(env.Rows) != 4 {
		t.Errorf("Rows = %d, want 4 (all cancelled edicts)", len(env.Rows))
	}
}

// TestListEdictsTool_FilterFindsCancelledBehindNewerRows is the regression from
// edict 1080: the newest rows are not cancelled, yet cancelled edicts must
// still be found instead of a false "none match".
func TestListEdictsTool_FilterFindsCancelledBehindNewerRows(t *testing.T) {
	db := setupEdictTestDB(t)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	cancelled := base.Add(time.Hour)
	oldCancelled := storage.Edict{
		ID: 1, Username: "testuser", Project: "testproject", Intent: "Old cancelled edict",
		CancelledAt: &cancelled, CreatedAt: base,
	}
	if err := db.Create(&oldCancelled).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	for i := 2; i <= 3; i++ {
		e := storage.Edict{
			ID: uint(i), Username: "testuser", Project: "testproject",
			Intent:    fmt.Sprintf("Newer active edict %d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		}
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("failed to create edict: %v", err)
		}
	}

	tool := ListEdictsTool{DB: db, Username: "testuser", Project: "testproject"}

	// Limit 1 inspects only the newest row, which is active — yet the cancelled
	// edict must be returned, not a false none-match.
	result, err := tool.Call(context.Background(), `{"status": "cancelled", "limit": 1}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	env, ok := isMarkdownTableEnvelope(result)
	if !ok {
		t.Fatalf("result is not a table envelope (filter reported nothing?): %s", result)
	}
	if len(env.Rows) != 1 {
		t.Fatalf("Rows = %d, want 1", len(env.Rows))
	}
	if env.Rows[0][0] != "1" || env.Rows[0][1] != "cancelled" {
		t.Errorf("row = %v, want [1 cancelled ...]", env.Rows[0])
	}
}

// TestListEdictsTool_InScopeIsScopeSizeNotLimit covers edict 1080: in_scope
// must be the scope size computed independently, never len(edicts) — so it is
// unaffected by Limit and by the status filter.
func TestListEdictsTool_InScopeIsScopeSizeNotLimit(t *testing.T) {
	db := setupEdictTestDB(t)

	for i := 1; i <= 3; i++ {
		e := storage.Edict{
			ID: uint(i), Username: "testuser", Project: "testproject",
			Intent: fmt.Sprintf("Active edict %d", i),
		}
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("failed to create edict: %v", err)
		}
	}

	tool := ListEdictsTool{DB: db, Username: "testuser", Project: "testproject"}
	result, err := tool.Call(context.Background(), `{"status": "blocked", "limit": 1}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
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
	if payload.InScope != 3 {
		t.Errorf("in_scope = %d, want 3 (the scope size, not the Limit)", payload.InScope)
	}
	if payload.Matched != 0 || payload.Status != "blocked" {
		t.Errorf("payload = %+v, want matched=0 status=blocked", payload)
	}
	if !containsSubstring(payload.Message, "3 edicts in scope") {
		t.Errorf("message %q should name the true in-scope count", payload.Message)
	}
}

// TestListEdictsTool_EmptyScopeWithFilterStillReportsScope covers edict 1080
// acceptance 3: a genuinely empty scope keeps the fail-loud scope report even
// when a status filter is supplied.
func TestListEdictsTool_EmptyScopeWithFilterStillReportsScope(t *testing.T) {
	db := setupEdictTestDB(t)

	edict := storage.Edict{ID: 927, Username: "daonb", Project: "oct7-io/data-poisoning-benchmark", Intent: "Give the sandbox gh CLI"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	tool := ListEdictsTool{DB: db, Username: "daonb", Project: "daonb/data-poisoning-benchmark"}
	result, err := tool.Call(context.Background(), `{"status": "cancelled"}`)
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
	if report.Project != "daonb/data-poisoning-benchmark" {
		t.Errorf("report queried project = %q, want daonb/data-poisoning-benchmark", report.Project)
	}
	if len(report.Nearby) != 1 || report.Nearby[0].Project != "oct7-io/data-poisoning-benchmark" {
		t.Errorf("report.Nearby = %+v, want the oct7-io scope", report.Nearby)
	}
}

// TestListEdictsTool_FormatCoversEnvelope covers the populated table-envelope
// branch added to Format in edict 988 while the scope-report path stays intact.
func TestListEdictsTool_FormatCoversEnvelope(t *testing.T) {
	tool := ListEdictsTool{}

	env, err := newMarkdownTableEnvelope("Edicts", []string{"edict_id"}, [][]string{{"1"}}, nil)
	if err != nil {
		t.Fatalf("newMarkdownTableEnvelope: %v", err)
	}
	if got := tool.Format("{}", env, nil); !containsSubstring(got, "Found 1 edicts") {
		t.Errorf("Format(envelope) = %q, want counted summary", got)
	}
}

// TestTransitionEdictTool_SealedAfterSupersededPromptRetired covers e1010
// acceptance 1 and 2: an edict whose only pending request is an abort prompt
// for a superseded execution is not blocked, and transition_edict -> sealed
// succeeds once the prompt is retired with a recorded reason (never
// 'answered').
func TestTransitionEdictTool_SealedAfterSupersededPromptRetired(t *testing.T) {
	db := setupEdictTestDB(t)

	edict := storage.Edict{ID: 1010, Username: "testuser", Project: "testproject", Intent: "Retire stale abort prompts"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// A pending abort prompt for an execution that a later run of the same
	// ritual superseded. This is what blocks ascent.
	execID := "exec-aborted"
	prompt := storage.Zhengming{
		RequestID:  "abort-prompt-" + execID,
		EdictID:    edict.ID,
		Username:   "testuser",
		Project:    "testproject",
		MinisterID: "secretary",
		Questions: storage.ZhengmingQuestions{{
			Text: "Ritual was aborted at step 0. What should we do? [exec:" + execID + "]",
		}},
		Status:    storage.ZhengmingPending,
		Priority:  storage.PriorityUrgent,
		TimeoutAt: time.Now().Add(time.Hour),
	}
	if err := db.Create(&prompt).Error; err != nil {
		t.Fatalf("failed to create abort prompt: %v", err)
	}

	sealService := storage.NewSealService(db)
	status, err := sealService.GetEdictStatus(edict.Key())
	if err != nil {
		t.Fatalf("get edict status: %v", err)
	}
	if status != storage.EdictBlocked {
		t.Fatalf("precondition: status = %q, want blocked", status)
	}

	// The superseding cause: the Court retires the prompt on its own
	// authority, recording why. DisposeZhengming is the sanctioned path — no
	// direct SQL.
	wrote, err := sealService.DisposeZhengming(prompt.RequestID, storage.ZhengmingDismissed, "court: superseded by execution exec-done")
	if err != nil {
		t.Fatalf("dispose zhengming: %v", err)
	}
	if !wrote {
		t.Fatal("dispose reported no row written, want one")
	}

	// The prompt no longer blocks: the edict is no longer blocked.
	status, err = sealService.GetEdictStatus(edict.Key())
	if err != nil {
		t.Fatalf("get edict status: %v", err)
	}
	if status == storage.EdictBlocked {
		t.Fatalf("status = blocked after retirement, want unblocked")
	}

	// Ascent succeeds without manual SQL.
	tool := TransitionEdictTool{DB: db, Username: "testuser", Project: "testproject"}
	if _, err := tool.Call(context.Background(), fmt.Sprintf(`{"edict_id": %d, "status": "sealed"}`, edict.ID)); err != nil {
		t.Fatalf("sealing a retired-prompt edict failed: %v", err)
	}

	status, err = sealService.GetEdictStatus(edict.Key())
	if err != nil {
		t.Fatalf("get edict status: %v", err)
	}
	if status != storage.EdictSealed {
		t.Errorf("status = %q, want sealed", status)
	}

	// The retirement is recorded, and it is NOT 'answered'.
	var retired storage.Zhengming
	if err := db.First(&retired, "request_id = ?", prompt.RequestID).Error; err != nil {
		t.Fatalf("failed to reload prompt: %v", err)
	}
	if retired.Status != storage.ZhengmingDismissed {
		t.Errorf("status = %q, want %q", retired.Status, storage.ZhengmingDismissed)
	}
	if retired.Status == storage.ZhengmingAnswered {
		t.Errorf("retirement marked the prompt answered; the Court retired it, the Ruler did not speak")
	}
	if retired.DispositionReason == "" {
		t.Errorf("disposition_reason is empty; the retirement must explain itself")
	}
	if retired.Answer != "" {
		t.Errorf("answer = %q, want empty on a Court retirement", retired.Answer)
	}
}

// recordingDisposer captures the request_id and reason it was asked to
// dispose, so the tool's contract can be asserted without a live Court.
type recordingDisposer struct {
	requestID string
	reason    string
	called    bool
	err       error
}

func (d *recordingDisposer) DismissZhengming(requestID, reason string) error {
	d.called = true
	d.requestID = requestID
	d.reason = reason
	return d.err
}

// TestDismissZhengmingTool_Call covers e1010 change 6/acceptance 5: the tool a
// minister uses to close a superseded prompt without direct SQL passes the
// request_id and reason through to the disposer.
func TestDismissZhengmingTool_Call(t *testing.T) {
	d := &recordingDisposer{}
	tool := DismissZhengmingTool{Ctx: ToolContext{Username: "testuser", Project: "testproject"}, Disposer: d}

	result, err := tool.Call(context.Background(), `{"request_id": "req-1", "reason": "court: superseded"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !d.called {
		t.Fatal("disposer was not called")
	}
	if d.requestID != "req-1" || d.reason != "court: superseded" {
		t.Errorf("disposer got (%q,%q), want (req-1, court: superseded)", d.requestID, d.reason)
	}
	if result == "" {
		t.Error("Call returned an empty result")
	}
}

// TestDismissZhengmingTool_CallErrors covers the tool's guards: malformed
// input, a missing request_id, and an unwired disposer must all surface an
// error rather than pretend the prompt was closed.
func TestDismissZhengmingTool_CallErrors(t *testing.T) {
	tool := DismissZhengmingTool{Ctx: ToolContext{Username: "testuser", Project: "testproject"}, Disposer: &recordingDisposer{}}

	if _, err := tool.Call(context.Background(), `not json`); err == nil {
		t.Error("malformed input must error")
	}
	if _, err := tool.Call(context.Background(), `{"reason": "no id"}`); err == nil {
		t.Error("missing request_id must error")
	}

	unwired := DismissZhengmingTool{Ctx: ToolContext{Username: "testuser", Project: "testproject"}}
	if _, err := unwired.Call(context.Background(), `{"request_id": "req-1"}`); err == nil {
		t.Error("an unwired disposer must error, not silently succeed")
	}
}
