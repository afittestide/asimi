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
