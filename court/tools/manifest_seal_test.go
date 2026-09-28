package tools

import (
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
