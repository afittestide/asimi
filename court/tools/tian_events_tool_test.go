package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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

// decodeTianEvents parses a Call result envelope and returns its event list.
// The result is a JSON object carrying the TUI table layout plus the raw
// summaries under "events".
func decodeTianEvents(t *testing.T, result string) []tianEventSummary {
	t.Helper()
	var env tianLedgerEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("failed to parse result envelope: %v", err)
	}
	return env.Events
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
	byEdict := decodeTianEvents(t, result)
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
	byType := decodeTianEvents(t, result)
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
	limited := decodeTianEvents(t, result)
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
	events := decodeTianEvents(t, result)
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
	// The empty case must stay a plain message, never a table envelope.
	if strings.Contains(result, tianMarkdownTableFormat) {
		t.Errorf("empty result must not be wrapped in a table envelope, got: %q", result)
	}
}

// TestTianLedgerTool_EnvelopeShape asserts the Call result is the generic
// markdown_table envelope: format/title/columns/ordered rows, with the raw
// summaries preserved under "events" so model fidelity is intact.
func TestTianLedgerTool_EnvelopeShape(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		ev := storage.TianEvent{
			EdictID:   5 + uint(i),
			Username:  "testuser",
			Project:   "testproject",
			EventType: "step_started",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			Payload:   storage.JSON{"step": "forge"},
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

	var env tianLedgerEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("result is not a JSON object envelope: %v", err)
	}
	if env.Format != tianMarkdownTableFormat {
		t.Errorf("Format = %q, want %q", env.Format, tianMarkdownTableFormat)
	}
	if env.Title != "Heaven Ledger" {
		t.Errorf("Title = %q, want %q", env.Title, "Heaven Ledger")
	}
	wantCols := []string{"edict_id", "event", "time", "detail"}
	if !reflect.DeepEqual(env.Columns, wantCols) {
		t.Errorf("Columns = %v, want %v", env.Columns, wantCols)
	}
	if len(env.Rows) != 3 {
		t.Fatalf("Rows length = %d, want 3", len(env.Rows))
	}
	for _, row := range env.Rows {
		if len(row) != len(env.Columns) {
			t.Errorf("row has %d cells, want %d: %v", len(row), len(env.Columns), row)
		}
	}
	// Rows must follow the same created_at DESC order as the events: the
	// highest edict_id (newest) comes first.
	if env.Rows[0][0] != "7" {
		t.Errorf("first row edict_id = %q, want newest (7)", env.Rows[0][0])
	}
	if env.Rows[len(env.Rows)-1][0] != "5" {
		t.Errorf("last row edict_id = %q, want oldest (5)", env.Rows[len(env.Rows)-1][0])
	}
	// The event cell carries the step number and name; time is relative.
	if env.Rows[0][1] != "step: forge" {
		t.Errorf("first row event = %q, want %q", env.Rows[0][1], "step: forge")
	}
	if !strings.HasSuffix(env.Rows[0][2], " ago") {
		t.Errorf("first row time = %q, want relative (… ago)", env.Rows[0][2])
	}
	if env.Rows[0][3] != "forge" {
		t.Errorf("first row detail = %q, want %q", env.Rows[0][3], "forge")
	}
	// Raw summaries (with payload) must still ride along for the model.
	if len(env.Events) != 3 {
		t.Fatalf("Events length = %d, want 3", len(env.Events))
	}
	if env.Events[0].Payload == nil {
		t.Errorf("Events[0].Payload is nil, want raw payload preserved")
	}
}

// TestTianLedgerTool_EnvelopeRowsAlignWithEvents ties the human-facing table
// to the model-facing summaries: the same events, same order, same values.
func TestTianLedgerTool_EnvelopeRowsAlignWithEvents(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	seedTianEvent(t, db, 7, "ritual_started", storage.JSON{"ritual": "swift-strike"})
	seedTianEvent(t, db, 8, "seal_granted", storage.JSON{"minister_id": "judge"})

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var env tianLedgerEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("result is not a JSON object envelope: %v", err)
	}
	if len(env.Rows) != len(env.Events) {
		t.Fatalf("Rows length %d != Events length %d", len(env.Rows), len(env.Events))
	}
	for i, ev := range env.Events {
		row := env.Rows[i]
		if row[0] != fmt.Sprintf("%d", ev.EdictID) {
			t.Errorf("row %d edict_id = %q, want %d", i, row[0], ev.EdictID)
		}
		if row[3] != ev.Detail {
			t.Errorf("row %d detail = %q, want %q", i, row[3], ev.Detail)
		}
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
	first := decodeTianEvents(t, page1)
	if len(first) != 2 {
		t.Fatalf("page 1: got %d events, want 2", len(first))
	}

	// Second page: limit 2, offset 2 — the next two, no overlap with page 1.
	page2, err := tool.Call(ctx, `{"limit": 2, "offset": 2}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	second := decodeTianEvents(t, page2)
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

	// A negative offset must be clamped to 0 (no SQL negative-offset error).
	page4, err := tool.Call(ctx, `{"offset": -5}`)
	if err != nil {
		t.Fatalf("negative offset should be clamped, got error: %v", err)
	}
	neg := decodeTianEvents(t, page4)
	if len(neg) != 4 {
		t.Fatalf("negative offset: got %d events, want all 4", len(neg))
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
	events := decodeTianEvents(t, result)
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
	events := decodeTianEvents(t, result)
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
	events := decodeTianEvents(t, result)
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
	events := decodeTianEvents(t, result)
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
	events := decodeTianEvents(t, result)
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

func TestTianLedgerTool_ShortBodyPassesThroughVerbatim(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	// A body shorter than the effective limit must be returned byte-for-byte:
	// no leading ellipsis and no truncation suffix. With OutputLimit unset the
	// tool must fall back to config.DefaultOutputLimit, not to zero/truncate-all.
	shortBody := "short verdict"
	seedTianEvent(t, db, 5, "ritual_completed", storage.JSON{"last_step_output": shortBody})

	tool := TianLedgerTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	events := decodeTianEvents(t, result)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	got, _ := events[0].Payload["last_step_output"].(string)
	if got != shortBody {
		t.Errorf("short body = %q, want verbatim %q", got, shortBody)
	}
	if strings.HasSuffix(got, tianEventTruncationSuffix) {
		t.Errorf("short body must not carry the truncation suffix: %q", got)
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
	events := decodeTianEvents(t, result)
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
	events := decodeTianEvents(t, result)
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
	events := decodeTianEvents(t, result)
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
	all := decodeTianEvents(t, result)
	if len(all) != 4 {
		t.Fatalf("since_id=0: got %d events, want 4", len(all))
	}

	// since_id=N returns only events newer than N.
	result, err = tool.Call(ctx, fmt.Sprintf(`{"since_id": %d}`, ids[1]))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	newer := decodeTianEvents(t, result)
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
	events := decodeTianEvents(t, result)
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

// TestTianLedgerTool_FormatStaysSummary asserts Format keeps rendering the
// compact "Found N events" line for both the envelope and legacy array shapes.
func TestTianLedgerTool_FormatStaysSummary(t *testing.T) {
	tool := TianLedgerTool{}
	envelope := `{"format":"markdown_table","title":"Heaven Ledger",` +
		`"columns":["id"],"rows":[["1"],["2"]],` +
		`"events":[{"id":1},{"id":2}]}`
	got := tool.Format("", envelope, nil)
	if !strings.Contains(got, "Found 2 events") {
		t.Errorf("Format(envelope) = %q, want counted summary", got)
	}

	legacy := `[{"id":1}]`
	got = tool.Format("", legacy, nil)
	if !strings.Contains(got, "Found 1 events") {
		t.Errorf("Format(legacy array) = %q, want counted summary", got)
	}

	got = tool.Format("", "", fmt.Errorf("boom"))
	if !strings.Contains(got, "Error:") {
		t.Errorf("Format(err) = %q, want error line", got)
	}
}

// TestTianRelativeTime covers the "time" column formatter boundaries.
func TestTianRelativeTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{"seconds", now.Add(-12 * time.Second), "12s ago"},
		{"minute boundary", now.Add(-59 * time.Second), "59s ago"},
		{"minutes", now.Add(-5 * time.Minute), "5m ago"},
		{"hour boundary", now.Add(-59 * time.Minute), "59m ago"},
		{"hours", now.Add(-3 * time.Hour), "3h ago"},
		{"day boundary", now.Add(-23 * time.Hour), "23h ago"},
		{"days", now.Add(-48 * time.Hour), "2d ago"},
		{"future clamps to zero", now.Add(time.Minute), "0s ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tianRelativeTime(tc.when); got != tc.want {
				t.Errorf("tianRelativeTime = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTianEventCell covers the per-type "event" column content.
func TestTianEventCell(t *testing.T) {
	cases := []struct {
		name      string
		eventType storage.CourtEvent
		payload   storage.JSON
		want      string
	}{
		{"step with index", "step_started", storage.JSON{"step": "reviewing", "step_index": float64(2)}, "step #2: reviewing"},
		{"step without index", "step_completed", storage.JSON{"step": "forge"}, "step: forge"},
		{"ritual", "ritual_started", storage.JSON{"ritual": "swift-strike"}, "swift-strike"},
		{"ritual completed", "ritual_completed", storage.JSON{"ritual": "castle-siege"}, "castle-siege"},
		{"seal granted", "seal_granted", storage.JSON{"minister_id": "judge"}, "judge"},
		{"zhengming answered", "zhengming_answered", storage.JSON{"minister_id": "forge"}, "forge"},
		{"unknown falls back to type", "worktree_merge_failed", storage.JSON{"foo": "bar"}, "worktree_merge_failed"},
		{"nil payload falls back to type", "ling_created", nil, "ling_created"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tianEventCell(tc.eventType, tc.payload); got != tc.want {
				t.Errorf("tianEventCell = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTianEventDetail covers the per-type human "detail" column summary.
func TestTianEventDetail(t *testing.T) {
	cases := []struct {
		name string
		ev   storage.TianEvent
		want string
	}{
		{
			"edict_created strips heading",
			storage.TianEvent{EventType: "edict_created", Payload: storage.JSON{"intent": "# Bug: fix reconnect race\n\nmore"}},
			"fix reconnect race",
		},
		{
			"edict_created plain first line",
			storage.TianEvent{EventType: "edict_created", Payload: storage.JSON{"intent": "add ledger polish\nsecond"}},
			"add ledger polish",
		},
		{
			"ritual_completed verdict tail",
			storage.TianEvent{EventType: "ritual_completed", Payload: storage.JSON{"last_step_output": "reviewing...\nPASS: all good"}},
			"PASS: all good",
		},
		{
			"step_failed error snippet",
			storage.TianEvent{EventType: "step_failed", Payload: storage.JSON{"error": "context canceled"}},
			"context canceled",
		},
		{
			"zhengming_requested question",
			storage.TianEvent{EventType: "zhengming_requested", Payload: storage.JSON{"questions": []interface{}{
				map[string]interface{}{"summary": "detail column", "text": "long text"},
			}}},
			"detail column",
		},
		{
			"zhengming_answered answer",
			storage.TianEvent{EventType: "zhengming_answered", Payload: storage.JSON{"answer": "use per-type"}},
			"use per-type",
		},
		{
			"court_started version",
			storage.TianEvent{EventType: "court_started", Payload: storage.JSON{"current_version": "v0.14.5"}},
			"v0.14.5",
		},
		{
			"generic fallback",
			storage.TianEvent{EventType: "some_event", Payload: storage.JSON{"ritual": "swift-strike"}},
			"swift-strike",
		},
		{
			"unknown empty",
			storage.TianEvent{EventType: "some_event", Payload: storage.JSON{"unrelated": "x"}},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tianEventDetail(tc.ev); got != tc.want {
				t.Errorf("tianEventDetail = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTianEventDetailBounded asserts a chatty payload is collapsed to one
// bounded line.
func TestTianEventDetailBounded(t *testing.T) {
	ev := storage.TianEvent{EventType: "edict_created", Payload: storage.JSON{
		"intent": "# Feature: " + strings.Repeat("word ", 100),
	}}
	got := tianEventDetail(ev)
	if strings.Contains(got, "\n") {
		t.Errorf("detail must be single-line, got %q", got)
	}
	if n := len([]rune(got)); n > tianDetailMaxLen {
		t.Errorf("detail length = %d runes, want <= %d", n, tianDetailMaxLen)
	}
}

// TestTianLedgerTool_EnvelopeEventAndTimeColumns ties the per-type event and
// relative-time cells into the envelope rows end to end.
func TestTianLedgerTool_EnvelopeEventAndTimeColumns(t *testing.T) {
	db := setupTianEventsTestDB(t)
	ctx := context.Background()

	ev := storage.TianEvent{
		EdictID:   9,
		Username:  "testuser",
		Project:   "testproject",
		EventType: "step_failed",
		CreatedAt: time.Now().Add(-5 * time.Minute),
		Payload:   storage.JSON{"step": "reviewing", "step_index": 2, "error": "boom"},
	}
	if err := db.Create(&ev).Error; err != nil {
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
	var env tianLedgerEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("result is not a JSON object envelope: %v", err)
	}
	if len(env.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(env.Rows))
	}
	row := env.Rows[0]
	if row[1] != "step #2: reviewing" {
		t.Errorf("event cell = %q, want %q", row[1], "step #2: reviewing")
	}
	if row[2] != "5m ago" {
		t.Errorf("time cell = %q, want %q", row[2], "5m ago")
	}
	if row[3] != "boom" {
		t.Errorf("detail cell = %q, want %q", row[3], "boom")
	}
	// Model-facing created_at stays an absolute timestamp.
	if env.Events[0].CreatedAt.IsZero() {
		t.Errorf("Events[0].CreatedAt is zero, want absolute timestamp")
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
