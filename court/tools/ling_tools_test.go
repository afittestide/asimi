package tools

import (
	"context"
	"encoding/json"
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

// TestListLingTool_EmptyScopeReportsScope covers edict 968: a scoped list_ling
// that matches zero rows must name the queried scope and any nearby match, not
// return a bare "No ling found for this edict".
func TestListLingTool_EmptyScopeReportsScope(t *testing.T) {
	db := setupLingTestDB(t)

	ling := storage.Ling{
		LingID: "f0a1", EdictID: 7, Username: "daonb",
		Project: "oct7-io/data-poisoning-benchmark", Description: "task", Status: storage.LingDone,
	}
	if err := db.Create(&ling).Error; err != nil {
		t.Fatalf("failed to create ling: %v", err)
	}

	tool := ListLingTool{Ctx: ToolContext{DB: db, Username: "daonb", Project: "daonb/data-poisoning-benchmark"}}
	result, err := tool.Call(context.Background(), `{"edict_id":7}`)
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
	if !strings.Contains(result, "oct7-io/data-poisoning-benchmark") {
		t.Errorf("result %q should name the nearby scope", result)
	}

	// A key that matches nowhere still looks like an empty-scope report, so the
	// caller can never confuse it with a populated list.
	result, err = tool.Call(context.Background(), `{"edict_id":999}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	var noMatch ScopeReport
	if err := json.Unmarshal([]byte(result), &noMatch); err != nil {
		t.Fatalf("no-match result is not the scope-report shape: %v\n%s", err, result)
	}
	if noMatch.Rows != 0 || len(noMatch.Nearby) != 0 {
		t.Errorf("no-match report = %+v, want 0 rows and no nearby match", noMatch)
	}
}

// TestListLingTool_PopulatedReturnsTableEnvelope verifies a populated list_ling
// opts into the TUI markdown-table envelope (edict 988) while raw still carries
// the full storage.Ling slice.
func TestListLingTool_PopulatedReturnsTableEnvelope(t *testing.T) {
	db := setupLingTestDB(t)

	lings := []storage.Ling{
		{LingID: "l1", EdictID: 7, Username: "testuser", Project: "testproject",
			Description: "first", Minister: "forge", Status: storage.LingPending,
			Dependencies: storage.StringArray{"l0"}},
		{LingID: "l2", EdictID: 7, Username: "testuser", Project: "testproject",
			Description: "second", Minister: "judge", Status: storage.LingDone},
	}
	for i := range lings {
		if err := db.Create(&lings[i]).Error; err != nil {
			t.Fatalf("failed to create ling: %v", err)
		}
	}

	tool := ListLingTool{Ctx: ToolContext{DB: db, Username: "testuser", Project: "testproject"}}
	result, err := tool.Call(context.Background(), `{"edict_id":7}`)
	if err != nil {
		t.Fatalf("list_ling failed: %v", err)
	}

	var env markdownTableEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("result is not a table envelope: %v\n%s", err, result)
	}
	if env.Format != markdownTableFormat {
		t.Errorf("Format = %q, want %q", env.Format, markdownTableFormat)
	}
	wantCols := []string{"id", "status", "minister", "description", "deps"}
	if len(env.Columns) != len(wantCols) {
		t.Fatalf("Columns = %v, want %v", env.Columns, wantCols)
	}
	for i, c := range wantCols {
		if env.Columns[i] != c {
			t.Errorf("Columns[%d] = %q, want %q", i, env.Columns[i], c)
		}
	}
	if len(env.Rows) != 2 {
		t.Fatalf("Rows = %d, want 2", len(env.Rows))
	}
	if env.Rows[0][3] != "first" {
		t.Errorf("first row description = %q, want first", env.Rows[0][3])
	}
	if env.Rows[0][4] != "l0" {
		t.Errorf("first row deps = %q, want l0", env.Rows[0][4])
	}
	if env.Rows[1][4] != "-" {
		t.Errorf("second row deps = %q, want -", env.Rows[1][4])
	}

	raw, err := json.Marshal(env.Raw)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	var rawLings []storage.Ling
	if err := json.Unmarshal(raw, &rawLings); err != nil {
		t.Fatalf("raw is not a []storage.Ling: %v\n%s", err, raw)
	}
	if len(rawLings) != 2 || rawLings[0].LingID != "l1" || rawLings[1].LingID != "l2" {
		t.Errorf("raw = %+v, want both lings in order", rawLings)
	}
}

// TestListLingTool_FormatCoversEnvelopeAndScopeReport covers the Format fallback
// added in edict 988: a table envelope reports its row count, a fail-loud scope
// report surfaces its message (not a JSON blob), and an error stays an error.
func TestListLingTool_FormatCoversEnvelopeAndScopeReport(t *testing.T) {
	tool := ListLingTool{}

	env, err := newMarkdownTableEnvelope("Ling", []string{"id"}, [][]string{{"l1"}}, nil)
	if err != nil {
		t.Fatalf("newMarkdownTableEnvelope: %v", err)
	}
	if got := tool.Format("{}", env, nil); !strings.Contains(got, "List Ling: 1 rows") {
		t.Errorf("Format(envelope) = %q, want counted summary", got)
	}

	report := `{"rows":0,"message":"0 rows for username=\"u\" project=\"p\"; no rows exist for this key under any other scope"}`
	got := tool.Format("{}", report, nil)
	if !strings.Contains(got, `username="u"`) {
		t.Errorf("Format(scope report) = %q, want the human message", got)
	}
	if strings.Contains(got, `"rows"`) {
		t.Errorf("Format(scope report) = %q, must not leak the JSON blob", got)
	}

	if got := tool.Format("{}", "", assertError("boom")); !strings.Contains(got, "Error:") {
		t.Errorf("Format(err) = %q, want error line", got)
	}
}
