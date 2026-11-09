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

// TestSealIfComplete_ResurrectsInvalidatedJudgeTombstone is the e882
// regression for the judge-side grant path: a judge seal minted at the
// deterministic ID, then invalidated, then re-granted via sealIfComplete
// must succeed and resurrect the tombstone instead of colliding with it.
func TestSealIfComplete_ResurrectsInvalidatedJudgeTombstone(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&storage.ForgeManifest{}, &storage.JudgeVerdict{}, &storage.Seal{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	key := storage.EdictKey{ID: 55, Username: "testuser", Project: "testproject"}
	tool := RecordVerdictTool{Ctx: ToolContext{
		MinisterID: "judge",
		Username:   key.Username,
		Project:    key.Project,
		DB:         db,
	}}

	// Latest verdict for the edict: passed, so sealIfComplete proceeds with
	// no manifests.
	if err := db.Create(&storage.JudgeVerdict{
		VerdictID: "v1", ManifestID: "", Username: key.Username, Project: key.Project,
		TestSuite: "edict", Outcome: storage.VerdictPassed,
	}).Error; err != nil {
		t.Fatalf("verdict: %v", err)
	}

	// Seed + invalidate a judge tombstone at the deterministic ID.
	detID := storage.DeterministicSealID(key.ID, key.Username, key.Project, "judge")
	if err := db.Create(&storage.Seal{
		SealID: detID, EdictID: key.ID, Username: key.Username, Project: key.Project,
		MinisterID: "judge",
	}).Error; err != nil {
		t.Fatalf("seal: %v", err)
	}
	sealSvc := storage.NewSealService(db)
	if err := sealSvc.InvalidateSeals(key); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	// Re-grant must succeed and resurrect the tombstone.
	if !tool.sealIfComplete(key) {
		t.Fatal("sealIfComplete must return true after invalidation (e882)")
	}

	var seals []storage.Seal
	if err := db.Where("edict_id = ? AND minister_id = ?", key.ID, "judge").Find(&seals).Error; err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(seals) != 1 || seals[0].SealID != detID || seals[0].StaleAt != nil {
		t.Fatalf("expected one resurrected live seal at %s, got %+v", detID, seals)
	}

	// Idempotent: calling again stays true, still one row.
	if !tool.sealIfComplete(key) {
		t.Fatal("second sealIfComplete must be a true no-op")
	}
	var count int64
	db.Model(&storage.Seal{}).Where("edict_id = ? AND minister_id = ?", key.ID, "judge").Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 seal, got %d", count)
	}
}

// TestListPendingManifestsTool_EmptyScopeReportsScope covers edict 968: a scoped
// list_pending_manifests that matches zero rows must name the queried scope and
// any nearby match instead of a bare "No pending manifests found".
func TestListPendingManifestsTool_EmptyScopeReportsScope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&storage.ForgeManifest{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	if err := db.Create(&storage.ForgeManifest{
		ManifestID: "m1", EdictID: 7, Username: "daonb",
		Project: "oct7-io/data-poisoning-benchmark", FilePath: "a.go", Status: storage.ManifestForged,
	}).Error; err != nil {
		t.Fatalf("failed to create manifest: %v", err)
	}

	tool := ListPendingManifestsTool{Ctx: ToolContext{
		DB: db, Username: "daonb", Project: "daonb/data-poisoning-benchmark",
	}}
	result, err := tool.Call(context.Background(), `{"edict_id":7}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	var report ScopeReport
	if err := json.Unmarshal([]byte(result), &report); err != nil {
		t.Fatalf("empty-scope result is not the scope-report shape: %v\n%s", err, result)
	}
	if report.Rows != 0 || report.Username != "daonb" || report.Project != "daonb/data-poisoning-benchmark" {
		t.Errorf("report = %+v, want 0 rows scoped to daonb/daonb/data-poisoning-benchmark", report)
	}
	if len(report.Nearby) != 1 {
		t.Fatalf("report.Nearby = %+v, want 1 nearby match", report.Nearby)
	}
	if report.Nearby[0].Project != "oct7-io/data-poisoning-benchmark" || report.Nearby[0].Count != 1 {
		t.Errorf("nearby = %+v, want 1 forged row for oct7-io/data-poisoning-benchmark", report.Nearby[0])
	}

	// The Format path must surface the report rather than "Listed 0 ...".
	rendered := tool.Format(`{"edict_id":7}`, result, nil)
	if !strings.Contains(rendered, "oct7-io/data-poisoning-benchmark") {
		t.Errorf("Format output %q should surface the scope report", rendered)
	}
}

// TestListPendingManifestsTool_PopulatedReturnsTableEnvelope verifies a
// populated list_pending_manifests opts into the TUI markdown-table envelope
// (edict 988) with raw carrying the full manifest slice.
func TestListPendingManifestsTool_PopulatedReturnsTableEnvelope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&storage.ForgeManifest{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	if err := db.Create(&storage.ForgeManifest{
		ManifestID: "m1", EdictID: 7, Username: "testuser", Project: "testproject",
		LingID: "l1", FilePath: "a.go", Status: storage.ManifestForged,
	}).Error; err != nil {
		t.Fatalf("failed to create manifest: %v", err)
	}

	tool := ListPendingManifestsTool{Ctx: ToolContext{DB: db, Username: "testuser", Project: "testproject"}}
	result, err := tool.Call(context.Background(), `{"edict_id":7}`)
	if err != nil {
		t.Fatalf("list_pending_manifests failed: %v", err)
	}

	var env markdownTableEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil {
		t.Fatalf("result is not a table envelope: %v\n%s", err, result)
	}
	if env.Format != markdownTableFormat {
		t.Errorf("Format = %q, want %q", env.Format, markdownTableFormat)
	}
	wantCols := []string{"manifest_id", "edict_id", "ling_id", "file_path", "status"}
	if len(env.Columns) != len(wantCols) {
		t.Fatalf("Columns = %v, want %v", env.Columns, wantCols)
	}
	for i, c := range wantCols {
		if env.Columns[i] != c {
			t.Errorf("Columns[%d] = %q, want %q", i, env.Columns[i], c)
		}
	}
	if len(env.Rows) != 1 {
		t.Fatalf("Rows = %d, want 1", len(env.Rows))
	}
	if env.Rows[0][0] != "m1" || env.Rows[0][3] != "a.go" {
		t.Errorf("row = %v, want m1 ... a.go ...", env.Rows[0])
	}

	raw, err := json.Marshal(env.Raw)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	var rawManifests []storage.ForgeManifest
	if err := json.Unmarshal(raw, &rawManifests); err != nil {
		t.Fatalf("raw is not a []storage.ForgeManifest: %v\n%s", err, raw)
	}
	if len(rawManifests) != 1 || rawManifests[0].ManifestID != "m1" {
		t.Errorf("raw = %+v, want the manifest", rawManifests)
	}
}

// TestGetManifestByCommitTool_PopulatedReturnsMarkdownEnvelope verifies a
// populated lookup opts into the TUI markdown envelope (edict 1031) with raw
// carrying the full manifest (manifest_id unmarshals back to the record).
func TestGetManifestByCommitTool_PopulatedReturnsMarkdownEnvelope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&storage.ForgeManifest{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	manifest := storage.ForgeManifest{
		ManifestID: "m1", EdictID: 7, Username: "testuser", Project: "testproject",
		LingID: "l1", FilePath: "a.go", FuncName: "Foo", ContentSHA: "sha1",
		CommitHash: "abc123", Status: storage.ManifestForged, VerdictID: "v1",
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatalf("failed to create manifest: %v", err)
	}

	tool := GetManifestByCommitTool{Ctx: ToolContext{DB: db, Username: "testuser", Project: "testproject"}}
	result, err := tool.Call(context.Background(), `{"commit_hash":"abc123"}`)
	if err != nil {
		t.Fatalf("get_manifest_by_commit failed: %v", err)
	}

	env, ok := isMarkdownEnvelope(result)
	if !ok {
		t.Fatalf("result is not a markdown envelope: %s", result)
	}

	raw, err := json.Marshal(env.Raw)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	var rawManifest storage.ForgeManifest
	if err := json.Unmarshal(raw, &rawManifest); err != nil {
		t.Fatalf("raw is not a storage.ForgeManifest: %v\n%s", err, raw)
	}
	if rawManifest.ManifestID != "m1" || rawManifest.FilePath != "a.go" {
		t.Errorf("raw = %+v, want the manifest", rawManifest)
	}

	if strings.Contains(env.Content, "{") || strings.Contains(env.Content, `"file_path"`) {
		t.Errorf("content %q must be markdown, not JSON", env.Content)
	}
	if !strings.Contains(env.Content, "a.go") {
		t.Errorf("content %q should mention the file", env.Content)
	}
	// Every populated optional field must be present as its own bullet (edict 1031).
	for _, want := range []string{
		"**Manifest `m1`** — status: forged",
		"- **file**: a.go",
		"- **func**: Foo",
		"- **edict**: 7 · ling: l1",
		"- **commit**: abc123",
		"- **verdict**: v1",
		"- **content sha**: sha1",
	} {
		if !strings.Contains(env.Content, want) {
			t.Errorf("content %q, want it to contain %q", env.Content, want)
		}
	}
	// Username and project are the query scope, not manifest data (edict 1031).
	for _, leaked := range []string{manifest.Username, manifest.Project} {
		if leaked != "" && strings.Contains(env.Content, leaked) {
			t.Errorf("content %q must not echo query scope %q", env.Content, leaked)
		}
	}

	formatted := tool.Format(`{"commit_hash":"abc123"}`, result, nil)
	if strings.Contains(formatted, "{") || strings.Contains(formatted, `"file_path"`) {
		t.Errorf("Format output %q must not echo JSON", formatted)
	}
	// The Formatted line names the manifest and the commit it was fetched under.
	if !strings.Contains(formatted, "Manifest m1") || !strings.Contains(formatted, "abc123") {
		t.Errorf("Format output %q, want it to name the manifest and commit", formatted)
	}
}

// TestRenderManifestMarkdown covers the branch matrix of the shared renderer
// (edict 1031): a sparse record renders no empty bullets, and each optional
// field is omitted independently when its part is empty.
func TestRenderManifestMarkdown(t *testing.T) {
	sparse := renderManifestMarkdown(storage.ForgeManifest{
		ManifestID: "m2", FilePath: "b.go", Status: storage.ManifestForged,
	})
	if !strings.Contains(sparse, "**Manifest `m2`** — status: forged") {
		t.Errorf("sparse render = %q, want the Manifest header", sparse)
	}
	if !strings.Contains(sparse, "- **file**: b.go") {
		t.Errorf("sparse render = %q, want the file bullet", sparse)
	}
	for _, omitted := range []string{"**func**", "**edict**", "ling:", "**commit**", "**verdict**", "**content sha**"} {
		if strings.Contains(sparse, omitted) {
			t.Errorf("sparse render = %q must omit %q when the field is empty", sparse, omitted)
		}
	}

	// The edict/ling bullet joins only the parts that are present.
	lingOnly := renderManifestMarkdown(storage.ForgeManifest{
		ManifestID: "m3", FilePath: "c.go", LingID: "l9",
	})
	if !strings.Contains(lingOnly, "- ling: l9") || strings.Contains(lingOnly, "edict") {
		t.Errorf("lingOnly render = %q, want only the ling part", lingOnly)
	}
	edictOnly := renderManifestMarkdown(storage.ForgeManifest{
		ManifestID: "m4", FilePath: "d.go", EdictID: 5,
	})
	if !strings.Contains(edictOnly, "- **edict**: 5") || strings.Contains(edictOnly, "ling:") {
		t.Errorf("edictOnly render = %q, want only the edict part", edictOnly)
	}
}

// TestGetManifestByCommitTool_FormatScopeReport covers the Format branch that
// surfaces a bare scope report (edict 1031): the scope message is shown and the
// report's JSON must not leak into the Formatted line.
func TestGetManifestByCommitTool_FormatScopeReport(t *testing.T) {
	report := ScopeReport{
		Rows: 0, Username: "u", Project: "p",
		Nearby:  []ScopeMatch{{Username: "o", Project: "o/p", Count: 1}},
		Message: `0 rows for username="u" project="p"; 1 rows exist elsewhere`,
	}
	bare, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal scope report: %v", err)
	}

	got := GetManifestByCommitTool{}.Format(`{"commit_hash":"abc123"}`, string(bare), nil)
	if !strings.Contains(got, report.Message) {
		t.Errorf("Format = %q, want it to surface the scope message", got)
	}
	if strings.Contains(got, `"rows"`) {
		t.Errorf("Format = %q must not echo the scope report JSON", got)
	}
}

// TestGetManifestByCommitTool_EmptyScopeReportsScope covers edict 968 for the
// single-row manifest reader: a wrong-scope lookup must not collapse into a bare
// "manifest not found", while a commit that matches nowhere stays an error.
func TestGetManifestByCommitTool_EmptyScopeReportsScope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&storage.ForgeManifest{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	if err := db.Create(&storage.ForgeManifest{
		ManifestID: "m1", EdictID: 7, Username: "daonb",
		Project: "oct7-io/data-poisoning-benchmark", FilePath: "a.go",
		CommitHash: "abc123", Status: storage.ManifestForged,
	}).Error; err != nil {
		t.Fatalf("failed to create manifest: %v", err)
	}

	tool := GetManifestByCommitTool{Ctx: ToolContext{
		DB: db, Username: "daonb", Project: "daonb/data-poisoning-benchmark",
	}}
	result, err := tool.Call(context.Background(), `{"commit_hash":"abc123"}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	var report ScopeReport
	if err := json.Unmarshal([]byte(result), &report); err != nil {
		t.Fatalf("empty-scope result is not the scope-report shape: %v\n%s", err, result)
	}
	if report.Rows != 0 || len(report.Nearby) != 1 {
		t.Fatalf("report = %+v, want 0 rows and 1 nearby match", report)
	}
	if report.Nearby[0].Project != "oct7-io/data-poisoning-benchmark" {
		t.Errorf("nearby project = %q, want oct7-io/data-poisoning-benchmark", report.Nearby[0].Project)
	}

	// A commit that exists nowhere in any scope is a real not-found, not a
	// scope mismatch.
	if _, err := tool.Call(context.Background(), `{"commit_hash":"deadbeef"}`); err == nil {
		t.Error("expected an error for a commit that matches no scope")
	}
}

// TestEmptyScopeReport_LookupFailureDoesNotAssertAbsence covers the rework's
// error-degrade path (edict 968): when the nearby-scope lookup itself fails, the
// fail-loud report must say the check failed rather than assert the key matches
// nowhere — a fail-loud feature must never lie about what it could not look up.
func TestEmptyScopeReport_LookupFailureDoesNotAssertAbsence(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&storage.ForgeManifest{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("failed to get sql.DB: %v", err)
	}
	sqlDB.Close() // force every query to fail

	result := emptyScopeReport(db, &storage.ForgeManifest{}, "edict_id = ?", []any{7}, "daonb", "daonb/data-poisoning-benchmark")

	var report ScopeReport
	if err := json.Unmarshal([]byte(result), &report); err != nil {
		t.Fatalf("result is not the scope-report shape: %v\n%s", err, result)
	}
	if !strings.Contains(report.Message, "nearby-scope check failed") {
		t.Errorf("message %q must report the failed lookup, not assert absence", report.Message)
	}
	if strings.Contains(report.Message, "no rows exist for this key under any other scope") {
		t.Errorf("message %q must not assert the key matches nowhere when the lookup failed", report.Message)
	}
}

// TestListPendingManifestsTool_FormatCoversEnvelope covers the envelope branch
// added to Format in edict 988 (it previously only understood a bare array).
func TestListPendingManifestsTool_FormatCoversEnvelope(t *testing.T) {
	tool := ListPendingManifestsTool{}

	env, err := newMarkdownTableEnvelope("Pending Manifests", []string{"manifest_id"}, [][]string{{"m1"}, {"m2"}}, nil)
	if err != nil {
		t.Fatalf("newMarkdownTableEnvelope: %v", err)
	}
	if got := tool.Format("{}", env, nil); !strings.Contains(got, "Listed 2 pending manifests") {
		t.Errorf("Format(envelope) = %q, want counted summary", got)
	}

	legacy := `[{"manifest_id":"m1"}]`
	if got := tool.Format("{}", legacy, nil); !strings.Contains(got, "Listed 1 pending manifests") {
		t.Errorf("Format(legacy array) = %q, want counted summary", got)
	}
}
