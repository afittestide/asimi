package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/afittestide/asimi/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupIncidentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := db.AutoMigrate(&storage.Incident{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func TestCreateIncidentTool(t *testing.T) {
	db := setupIncidentTestDB(t)
	ctx := context.Background()

	tool := CreateIncidentTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	result, err := tool.Call(ctx, `{"description":"segfault in main","severity":"critical"}`)
	if err != nil {
		t.Fatalf("CreateIncident failed: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}

	// Verify the incident was created with correct fields
	var incident storage.Incident
	if err := db.Where("username = ? AND project = ?", "testuser", "testproject").First(&incident).Error; err != nil {
		t.Fatalf("failed to query incident: %v", err)
	}

	if incident.Description != "segfault in main" {
		t.Errorf("Description = %q, want %q", incident.Description, "segfault in main")
	}
	if incident.Severity != "critical" {
		t.Errorf("Severity = %q, want %q", incident.Severity, "critical")
	}
	if incident.Status != "open" {
		t.Errorf("Status = %q, want %q", incident.Status, "open")
	}
	if incident.CommitHash != "" {
		t.Errorf("CommitHash should be empty, got %q", incident.CommitHash)
	}
}

func TestCreateIncidentTool_NoCollision(t *testing.T) {
	db := setupIncidentTestDB(t)
	ctx := context.Background()

	tool := CreateIncidentTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	// Create two incidents with identical description and severity
	_, err := tool.Call(ctx, `{"description":"segfault in main","severity":"critical"}`)
	if err != nil {
		t.Fatalf("first CreateIncident failed: %v", err)
	}
	_, err = tool.Call(ctx, `{"description":"segfault in main","severity":"critical"}`)
	if err != nil {
		t.Fatalf("second CreateIncident failed: %v", err)
	}

	// Both should exist as separate rows
	var count int64
	db.Model(&storage.Incident{}).Where("username = ? AND project = ?", "testuser", "testproject").Count(&count)
	if count != 2 {
		t.Fatalf("expected 2 incidents, got %d", count)
	}
}

func TestCreateIncidentTool_MissingFields(t *testing.T) {
	db := setupIncidentTestDB(t)
	ctx := context.Background()

	tool := CreateIncidentTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}

	// Missing severity
	_, err := tool.Call(ctx, `{"description":"something broke"}`)
	if err == nil {
		t.Error("expected error when severity is missing")
	}

	// Missing description
	_, err = tool.Call(ctx, `{"severity":"high"}`)
	if err == nil {
		t.Error("expected error when description is missing")
	}
}

func TestResolveIncidentTool(t *testing.T) {
	db := setupIncidentTestDB(t)
	ctx := context.Background()

	// Create an incident first
	createTool := CreateIncidentTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}
	_, err := createTool.Call(ctx, `{"description":"db down","severity":"high"}`)
	if err != nil {
		t.Fatalf("CreateIncident failed: %v", err)
	}

	var incident storage.Incident
	if err := db.First(&incident).Error; err != nil {
		t.Fatalf("failed to query incident: %v", err)
	}

	// Now resolve it
	resolveTool := ResolveIncidentTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}
	_, err = resolveTool.Call(ctx, `{"incident_id":"`+incident.IncidentID+`","resolution":"restarted db","root_cause":"connection pool exhaustion"}`)
	if err != nil {
		t.Fatalf("ResolveIncident failed: %v", err)
	}

	// Verify resolution
	var resolved storage.Incident
	if err := db.First(&resolved).Error; err != nil {
		t.Fatalf("failed to query resolved incident: %v", err)
	}
	if resolved.Status != "resolved" {
		t.Errorf("Status = %q, want %q", resolved.Status, "resolved")
	}
	if resolved.Resolution != "restarted db" {
		t.Errorf("Resolution = %q, want %q", resolved.Resolution, "restarted db")
	}
	if resolved.RootCause != "connection pool exhaustion" {
		t.Errorf("RootCause = %q, want %q", resolved.RootCause, "connection pool exhaustion")
	}
}

func TestResolveIncidentTool_NotFound(t *testing.T) {
	db := setupIncidentTestDB(t)
	ctx := context.Background()

	resolveTool := ResolveIncidentTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}
	_, err := resolveTool.Call(ctx, `{"incident_id":"nonexistent","resolution":"wont work"}`)
	if err == nil {
		t.Error("expected error for nonexistent incident")
	}
}

func TestGetIncidentTool(t *testing.T) {
	db := setupIncidentTestDB(t)
	ctx := context.Background()

	// Create an incident first
	createTool := CreateIncidentTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}
	_, err := createTool.Call(ctx, `{"description":"memory leak","severity":"medium"}`)
	if err != nil {
		t.Fatalf("CreateIncident failed: %v", err)
	}

	var incident storage.Incident
	if err := db.First(&incident).Error; err != nil {
		t.Fatalf("failed to query incident: %v", err)
	}

	// Now get it
	getTool := GetIncidentTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}
	result, err := getTool.Call(ctx, `{"incident_id":"`+incident.IncidentID+`"}`)
	if err != nil {
		t.Fatalf("GetIncident failed: %v", err)
	}

	env, ok := isMarkdownEnvelope(result)
	if !ok {
		t.Fatalf("GetIncident result is not a markdown envelope: %s", result)
	}
	if !strings.Contains(env.Content, "memory leak") {
		t.Errorf("Content = %q, want it to contain the description", env.Content)
	}
	if !strings.Contains(env.Content, "medium") {
		t.Errorf("Content = %q, want it to contain the severity", env.Content)
	}
	// The rendered block must be the markdown the edict specifies, not merely a
	// string that happens to carry the description: header and Description
	// section must both be present.
	if !strings.Contains(env.Content, "**Incident `"+incident.IncidentID+"`**") {
		t.Errorf("Content = %q, want the Incident header", env.Content)
	}
	if !strings.Contains(env.Content, "**Description**") {
		t.Errorf("Content = %q, want the Description section", env.Content)
	}
	// Username and project are the query scope, not incident data (edict 1031).
	for _, leaked := range []string{incident.Username, incident.Project} {
		if leaked != "" && strings.Contains(env.Content, leaked) {
			t.Errorf("Content = %q must not echo query scope %q", env.Content, leaked)
		}
	}

	raw, err := json.Marshal(env.Raw)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	var fetched storage.Incident
	if err := json.Unmarshal(raw, &fetched); err != nil {
		t.Fatalf("raw is not a storage.Incident: %v\n%s", err, raw)
	}
	if fetched != incident {
		t.Errorf("raw = %+v, want the identical incident %+v", fetched, incident)
	}
	if fetched.Description != "memory leak" {
		t.Errorf("Description = %q, want %q", fetched.Description, "memory leak")
	}
	if fetched.Severity != "medium" {
		t.Errorf("Severity = %q, want %q", fetched.Severity, "medium")
	}
	if fetched.Status != "open" {
		t.Errorf("Status = %q, want %q", fetched.Status, "open")
	}

	formatted := getTool.Format(`{"incident_id":"`+incident.IncidentID+`"}`, result, nil)
	if strings.Contains(formatted, "{") || strings.Contains(formatted, `"description"`) {
		t.Errorf("Format output %q must not echo JSON", formatted)
	}
	if !strings.Contains(formatted, incident.IncidentID) {
		t.Errorf("Format output %q should name the incident", formatted)
	}
}

// TestRenderIncidentMarkdown covers the branch matrix of the shared renderer
// (edict 1031): optional short fields are omitted rather than left as empty
// bullets, a multi-line free-text field is rendered as its own verbatim
// paragraph instead of being flattened into a bullet, and the optional
// Root cause/Resolution sections appear only when populated.
func TestRenderIncidentMarkdown(t *testing.T) {
	sparse := renderIncidentMarkdown(storage.Incident{
		IncidentID: "inc-1", Severity: "high", Status: "open",
		Description: "short one\nline two",
	})
	if !strings.Contains(sparse, "**Incident `inc-1`**") ||
		!strings.Contains(sparse, "severity: `high`") ||
		!strings.Contains(sparse, "status: `open`") {
		t.Errorf("sparse render = %q, want the header to carry id, severity and status", sparse)
	}
	if strings.Contains(sparse, "**edict**") {
		t.Errorf("sparse render = %q must omit the edict bullet when EdictID is 0", sparse)
	}
	if strings.Contains(sparse, "**commit**") {
		t.Errorf("sparse render = %q must omit the commit bullet when CommitHash is empty", sparse)
	}
	if strings.Contains(sparse, "**Root cause**") || strings.Contains(sparse, "**Resolution**") {
		t.Errorf("sparse render = %q must omit empty Root cause/Resolution sections", sparse)
	}
	// Multi-line free text stays verbatim: no bullet may swallow its second line.
	if !strings.Contains(sparse, "\nshort one\nline two\n") {
		t.Errorf("sparse render = %q, want the description as a verbatim paragraph", sparse)
	}

	full := renderIncidentMarkdown(storage.Incident{
		IncidentID: "inc-2", Severity: "low", Status: "resolved",
		EdictID: 1234, CommitHash: "abc123", Description: "d",
		RootCause: "rc", Resolution: "fix",
	})
	for _, want := range []string{"- **edict**: 1234", "- **commit**: abc123", "**Root cause**\nrc", "**Resolution**\nfix"} {
		if !strings.Contains(full, want) {
			t.Errorf("full render = %q, want it to contain %q", full, want)
		}
	}
}

// TestGetIncidentTool_FormatScopeReport covers the Format branch that surfaces a
// bare scope report (edict 1031): the scope message must be shown and the
// envelope's JSON must not leak into the Formatted line.
func TestGetIncidentTool_FormatScopeReport(t *testing.T) {
	report := ScopeReport{
		Rows: 0, Username: "testuser", Project: "testproject",
		Nearby:  []ScopeMatch{{Username: "other", Project: "other/proj", Count: 2}},
		Message: `0 rows for username="testuser" project="testproject"; 2 rows exist elsewhere`,
	}
	bare, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal scope report: %v", err)
	}

	got := GetIncidentTool{}.Format(`{"incident_id":"inc-9"}`, string(bare), nil)
	if !strings.Contains(got, report.Message) {
		t.Errorf("Format = %q, want it to surface the scope message", got)
	}
	if strings.Contains(got, "{") || strings.Contains(got, `"rows"`) {
		t.Errorf("Format = %q must not echo the scope report JSON", got)
	}
}

func TestGetIncidentTool_NotFound(t *testing.T) {
	db := setupIncidentTestDB(t)
	ctx := context.Background()

	getTool := GetIncidentTool{Ctx: ToolContext{
		DB:       db,
		Username: "testuser",
		Project:  "testproject",
	}}
	_, err := getTool.Call(ctx, `{"incident_id":"nonexistent"}`)
	if err == nil {
		t.Error("expected error for nonexistent incident")
	}
}
