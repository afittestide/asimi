package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/afittestide/asimi/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTianEventsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := db.AutoMigrate(&storage.TianEvent{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func seedTianEvent(t *testing.T, db *gorm.DB, edictID uint, eventType storage.CourtEvent, payload storage.JSON) {
	t.Helper()
	ev := storage.TianEvent{
		EdictID:   edictID,
		Username:  "testuser",
		Project:   "testproject",
		EventType: eventType,
		Payload:   payload,
	}
	if err := db.Create(&ev).Error; err != nil {
		t.Fatalf("failed to seed event: %v", err)
	}
}

func TestTianLedgerTool_Filters(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	seedTianEvent(t, db, 5, "ritual_started", storage.JSON{"ritual": "swift-strike", "execution_id": 1})
	seedTianEvent(t, db, 5, "step_started", storage.JSON{"ritual": "swift-strike", "step": "forge", "step_index": 0})
	seedTianEvent(t, db, 6, "ritual_started", storage.JSON{"ritual": "castle-siege", "execution_id": 2})

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	// Filter by edict_id
	result, err := tool.Call(ctx, `{"edict_id": 5}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var byEdict []tianEventSummary
	if err := json.Unmarshal([]byte(result), &byEdict); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(byEdict) != 2 {
		t.Fatalf("edict_id filter: got %d events, want 2", len(byEdict))
	}
	for _, ev := range byEdict {
		if ev.EdictID != 5 {
			t.Errorf("edict_id filter returned event with edict_id %d", ev.EdictID)
		}
	}

	// Filter by event_type
	result, err = tool.Call(ctx, `{"event_type": "ritual_started"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var byType []tianEventSummary
	if err := json.Unmarshal([]byte(result), &byType); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(byType) != 2 {
		t.Fatalf("event_type filter: got %d events, want 2", len(byType))
	}
	for _, ev := range byType {
		if ev.EventType != "ritual_started" {
			t.Errorf("event_type filter returned event type %q", ev.EventType)
		}
	}

	// Limit caps the number of results
	result, err = tool.Call(ctx, `{"limit": 1}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var limited []tianEventSummary
	if err := json.Unmarshal([]byte(result), &limited); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limit filter: got %d events, want 1", len(limited))
	}
}

func TestTianLedgerTool_Detail(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	seedTianEvent(t, db, 5, "ritual_started", storage.JSON{"ritual": "swift-strike", "execution_id": 1})

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var events []tianEventSummary
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Detail != "swift-strike" {
		t.Errorf("Detail = %q, want %q", events[0].Detail, "swift-strike")
	}
}

func TestTianLedgerTool_Empty(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !strings.Contains(result, "No Tian events found") {
		t.Errorf("empty result should return a helpful message, got: %q", result)
	}
}

func TestTianLedgerTool_OffsetPagination(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	// Seed four events for the same edict; base time ensures deterministic order.
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 4; i++ {
		ev := storage.TianEvent{
			EdictID:   5,
			Username:  "testuser",
			Project:   "testproject",
			EventType: "step_started",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatalf("failed to seed event: %v", err)
		}
	}

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	// First page: limit 2, no offset — the two most recent events.
	page1, err := tool.Call(ctx, `{"limit": 2}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var first []tianEventSummary
	if err := json.Unmarshal([]byte(page1), &first); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("page 1: got %d events, want 2", len(first))
	}

	// Second page: limit 2, offset 2 — the next two, no overlap with page 1.
	page2, err := tool.Call(ctx, `{"limit": 2, "offset": 2}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var second []tianEventSummary
	if err := json.Unmarshal([]byte(page2), &second); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("page 2: got %d events, want 2", len(second))
	}
	for _, a := range first {
		for _, b := range second {
			if a.ID == b.ID {
				t.Errorf("offset pagination returned overlapping event id %d", a.ID)
			}
		}
	}

	// Offset beyond the result set yields the empty-result message.
	page3, err := tool.Call(ctx, `{"offset": 10}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !strings.Contains(page3, "No Tian events found") {
		t.Errorf("offset past end should return empty message, got: %q", page3)
	}
}

func TestTianLedgerTool_OrderedByCreatedAtDesc(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		ev := storage.TianEvent{
			EdictID:   5,
			Username:  "testuser",
			Project:   "testproject",
			EventType: "step_started",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatalf("failed to seed event: %v", err)
		}
	}

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var events []tianEventSummary
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].CreatedAt.After(events[i-1].CreatedAt) {
			t.Errorf("events not ordered by created_at DESC: index %d (%v) is after index %d (%v)",
				i, events[i].CreatedAt, i-1, events[i-1].CreatedAt)
		}
	}
}

func TestTianLedgerTool_DefaultLimit(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	// Seed 60 events — more than the default limit of 50.
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 60; i++ {
		ev := storage.TianEvent{
			EdictID:   5,
			Username:  "testuser",
			Project:   "testproject",
			EventType: "step_started",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatalf("failed to seed event: %v", err)
		}
	}

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	// No limit supplied — must fall back to the default of 50, not return all 60.
	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var events []tianEventSummary
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(events) != 50 {
		t.Fatalf("default limit: got %d events, want 50", len(events))
	}
}

func TestTianLedgerTool_DetailKeys(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	seedTianEvent(t, db, 5, "ritual_started", storage.JSON{"ritual": "swift-strike", "execution_id": 1})
	seedTianEvent(t, db, 6, "step_started", storage.JSON{"step": "forge", "step_index": 0})
	seedTianEvent(t, db, 7, "seal_granted", storage.JSON{"minister_id": "judge", "notes": "ok"})
	seedTianEvent(t, db, 8, "edict_created", storage.JSON{"intent": "fix it", "id": 8})

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var events []tianEventSummary
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}

	details := map[string]string{}
	for _, ev := range events {
		details[ev.EventType] = ev.Detail
	}
	if details["ritual_started"] != "swift-strike" {
		t.Errorf("ritual detail = %q, want %q", details["ritual_started"], "swift-strike")
	}
	if details["step_started"] != "forge" {
		t.Errorf("step detail = %q, want %q", details["step_started"], "forge")
	}
	if details["seal_granted"] != "judge" {
		t.Errorf("minister_id detail = %q, want %q", details["seal_granted"], "judge")
	}
	if details["edict_created"] != "fix it" {
		t.Errorf("intent detail = %q, want %q", details["edict_created"], "fix it")
	}

	// Payload must be returned verbatim so the minister can reason freely.
	for _, ev := range events {
		if ev.Payload == nil {
			t.Errorf("%s: Payload is nil, want raw payload", ev.EventType)
		}
	}
	for _, ev := range events {
		if ev.EventType == "seal_granted" && ev.Payload["notes"] != "ok" {
			t.Errorf("seal_granted Payload[notes] = %v, want %q", ev.Payload["notes"], "ok")
		}
	}
}

func TestTianLedgerTool_UnknownPayloadHasNoDetail(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	// Payload keys that the detail extractor does not recognise must yield
	// an empty detail rather than a panic or garbage.
	seedTianEvent(t, db, 5, "step_started", storage.JSON{"unrelated": "value"})

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var events []tianEventSummary
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Detail != "" {
		t.Errorf("Detail = %q, want empty for unrecognised payload", events[0].Detail)
	}
}

func TestTianLedgerTool_ScopedByUserAndProject(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	// Event belonging to a different project must not leak.
	other := storage.TianEvent{
		EdictID:   5,
		Username:  "otheruser",
		Project:   "otherproject",
		EventType: "ritual_started",
		Payload:   storage.JSON{"ritual": "swift-strike", "execution_id": 1},
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("failed to seed event: %v", err)
	}

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !strings.Contains(result, "No Tian events found") {
		t.Errorf("expected no events for scoped user, got: %q", result)
	}
}

func TestTianLedgerTool_SummaryTruncatesTail(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	fullBody := strings.Repeat("A", 100) + "VERDICT"
	seedTianEvent(t, db, 5, "ritual_completed", storage.JSON{
		"ritual":           "swift-strike",
		"step":             "review",
		"error":            "none",
		"duration":         "12s",
		"last_step_output": fullBody,
	})

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:          db,
		Username:    "testuser",
		Project:     "testproject",
		OutputLimit: 20,
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var events []tianEventSummary
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	got, _ := events[0].Payload["last_step_output"].(string)
	if !strings.Contains(got, "VERDICT") {
		t.Errorf("summary output lost the tail verdict: %q", got)
	}
	if strings.HasPrefix(got, "A") {
		t.Errorf("summary output kept the head, want tail only: %q", got)
	}
	if !strings.HasSuffix(got, tianEventTruncationSuffix) {
		t.Errorf("summary output missing truncation suffix: %q", got)
	}
	if got == fullBody {
		t.Errorf("summary output should be truncated, got full body")
	}

	// All other payload keys stay verbatim (e842 fidelity).
	for _, key := range []string{"ritual", "step", "error", "duration"} {
		if events[0].Payload[key] == nil {
			t.Errorf("summary dropped payload key %q", key)
		}
	}
	if events[0].Payload["error"] != "none" {
		t.Errorf("error key = %v, want %q", events[0].Payload["error"], "none")
	}
}

func TestTianLedgerTool_FullReturnsUntruncatedBody(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	fullBody := strings.Repeat("B", 4000)
	seedTianEvent(t, db, 5, "ritual_completed", storage.JSON{"last_step_output": fullBody})

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:          db,
		Username:    "testuser",
		Project:     "testproject",
		OutputLimit: 20,
	}}

	result, err := tool.Call(ctx, `{"detail": "full"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var events []tianEventSummary
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if got := events[0].Payload["last_step_output"]; got != fullBody {
		t.Errorf("detail=full should return the raw body, got %d chars (want %d)", len(got.(string)), len(fullBody))
	}
}

func TestTianLedgerTool_CourtLevelLimitHonoured(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	fullBody := strings.Repeat("C", 1000)
	seedTianEvent(t, db, 5, "ritual_completed", storage.JSON{"last_step_output": fullBody})

	const limit = 37
	tool := TianLedgerTool{Ctx: ToolContext{
		DB:          db,
		Username:    "testuser",
		Project:     "testproject",
		OutputLimit: limit,
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var events []tianEventSummary
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	got, _ := events[0].Payload["last_step_output"].(string)
	// The retained tail is exactly `limit` runes, plus the leading ellipsis
	// and the truncation suffix — a hardcoded constant would fail here.
	tail := strings.TrimPrefix(got, "…")
	tail = strings.TrimSuffix(tail, tianEventTruncationSuffix)
	if n := len([]rune(tail)); n != limit {
		t.Errorf("retained tail = %d runes, want %d (got %q)", n, limit, got)
	}
}

func TestTianLedgerTool_DetailDerivedFromFullPayloadInSummary(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	// Leading intent key plus a huge trailing last_step_output: detail must
	// still derive from the full payload even though the body is truncated.
	seedTianEvent(t, db, 8, "edict_created", storage.JSON{
		"intent":           "fix it",
		"last_step_output": strings.Repeat("D", 2000),
	})

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:          db,
		Username:    "testuser",
		Project:     "testproject",
		OutputLimit: 10,
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var events []tianEventSummary
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Detail != "fix it" {
		t.Errorf("Detail = %q, want %q", events[0].Detail, "fix it")
	}
}

func TestTianLedgerTool_SinceID(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	base := time.Now().Add(-time.Hour)
	ids := make([]uint, 0, 4)
	for i := 0; i < 4; i++ {
		ev := storage.TianEvent{
			EdictID:   5,
			Username:  "testuser",
			Project:   "testproject",
			EventType: "step_started",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatalf("failed to seed event: %v", err)
		}
		ids = append(ids, ev.ID)
	}

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	// since_id=0 is a no-op — all four events returned.
	result, err := tool.Call(ctx, `{"since_id": 0}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var all []tianEventSummary
	if err := json.Unmarshal([]byte(result), &all); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("since_id=0: got %d events, want 4", len(all))
	}

	// since_id=N returns only events newer than N.
	result, err = tool.Call(ctx, fmt.Sprintf(`{"since_id": %d}`, ids[1]))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var newer []tianEventSummary
	if err := json.Unmarshal([]byte(result), &newer); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(newer) != 2 {
		t.Fatalf("since_id=%d: got %d events, want 2", ids[1], len(newer))
	}
	for _, ev := range newer {
		if ev.ID <= ids[1] {
			t.Errorf("since_id=%d returned event id %d, want only id >", ids[1], ev.ID)
		}
	}
}

func TestTianLedgerTool_MalformedPayloadDoesNotFailQuery(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	// A row written out-of-band with a literal newline inside a JSON string —
	// the e836 poison that previously aborted the entire Find.
	poison := "{\"answer\": \"line one\nline two\"}"
	if err := db.Exec(
		`INSERT INTO tian_events (edict_id, username, project, event_type, payload, created_at)
		 VALUES (5, 'testuser', 'testproject', 'zhengming_answered', ?, datetime('now'))`,
		poison,
	).Error; err != nil {
		t.Fatalf("failed to insert poisoned event: %v", err)
	}

	seedTianEvent(t, db, 5, "ritual_started", storage.JSON{"ritual": "swift-strike"})
	seedTianEvent(t, db, 5, "edict_created", storage.JSON{"intent": "fix it"})

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(ctx, `{"edict_id": 5}`)
	if err != nil {
		t.Fatalf("malformed payload failed the whole query: %v", err)
	}
	var events []tianEventSummary
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (valid rows must survive)", len(events))
	}
	foundPoison := false
	for _, ev := range events {
		if ev.EventType == "zhengming_answered" {
			foundPoison = true
		}
		if ev.EventType == "ritual_started" && ev.Detail != "swift-strike" {
			t.Errorf("valid event detail = %q, want %q", ev.Detail, "swift-strike")
		}
	}
	if !foundPoison {
		t.Errorf("malformed row was dropped, want it returned with a marker payload")
	}

	// The poisoned row's payload must surface as the bounded marker rather
	// than being silently dropped by the tolerant scan.
	for _, ev := range events {
		if ev.EventType != "zhengming_answered" {
			continue
		}
		if ev.Payload == nil {
			t.Fatalf("malformed row payload is nil, want %q marker", storage.MalformedMarkerKey)
		}
		marker, ok := ev.Payload[storage.MalformedMarkerKey].(string)
		if !ok || marker == "" {
			t.Fatalf("malformed row payload = %#v, want %q marker", ev.Payload, storage.MalformedMarkerKey)
		}
	}
}

func TestTianLedgerTool_NilDB(t *testing.T) {
	tool := TianLedgerTool{Ctx: ToolContext{
		Username: "testuser",
		Project:  "testproject",
	}}
	if _, err := tool.Call(context.Background(), `{}`); err == nil {
		t.Fatal("expected an error for a nil DB handle")
	}
}

func TestTianLedgerTool_DBError(t *testing.T) {
	db := setupTianEventsTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("failed to get sql.DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("failed to close db: %v", err)
	}

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}
	if _, err := tool.Call(context.Background(), `{}`); err == nil {
		t.Fatal("expected a DB failure to surface as an error, got nil")
	}
}
