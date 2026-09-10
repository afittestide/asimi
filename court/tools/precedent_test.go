package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/afittestide/asimi/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupPrecedentTestDB creates an in-memory SQLite DB with the right schema.
func setupPrecedentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&storage.ForgeManifest{}, &storage.CensorPrecedent{}, &storage.Seal{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func TestRecordPrecedentTool_NoReasoningEcho(t *testing.T) {
	db := setupPrecedentTestDB(t)
	// Insert a quenched manifest
	db.Create(&storage.ForgeManifest{
		ManifestID: "abc123",
		EdictID:    5,
		Username:   "testuser",
		Project:    "testproject",
		Status:     storage.ManifestQuenched,
	})

	tool := RecordPrecedentTool{
		Ctx: ToolContext{
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
	}

	longReasoning := "This is a very long reasoning that should NOT appear in the tool output because the Sage already wrote it as conversational text."
	result, err := tool.Call(context.Background(),
		`{"edict_id": 5, "approved": true, "reasoning": "`+longReasoning+`"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(result, longReasoning) {
		t.Errorf("result should not echo reasoning, got: %s", result)
	}

	want := "Recorded precedent (approved) for edict 5"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}

func TestRecordPrecedentTool_RejectedNoReasoningEcho(t *testing.T) {
	db := setupPrecedentTestDB(t)
	db.Create(&storage.ForgeManifest{
		ManifestID: "abc123",
		EdictID:    5,
		Username:   "testuser",
		Project:    "testproject",
		Status:     storage.ManifestQuenched,
	})

	tool := RecordPrecedentTool{
		Ctx: ToolContext{
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
	}

	longReasoning := "Code has issues that need addressing."
	result, err := tool.Call(context.Background(),
		`{"edict_id": 5, "approved": false, "reasoning": "`+longReasoning+`"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(result, longReasoning) {
		t.Errorf("result should not echo reasoning, got: %s", result)
	}

	want := "Recorded precedent (rejected) for edict 5"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}

func TestRecordPrecedentTool_Format(t *testing.T) {
	tool := RecordPrecedentTool{}
	formatted := tool.Format("", "Recorded precedent (approved) for edict 5", nil)
	want := "Record Precedent: Recorded precedent (approved) for edict 5\n"
	if formatted != want {
		t.Errorf("Format() = %q, want %q", formatted, want)
	}
}

func TestRecordPrecedentTool_GrantsSageSealOnApproval(t *testing.T) {
	db := setupPrecedentTestDB(t)
	db.Create(&storage.ForgeManifest{
		ManifestID: "m1",
		EdictID:    7,
		Username:   "testuser",
		Project:    "testproject",
		Status:     storage.ManifestQuenched,
	})

	tool := RecordPrecedentTool{
		Ctx: ToolContext{
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
	}

	_, err := tool.Call(context.Background(),
		`{"edict_id": 7, "approved": true, "reasoning": "LGTM"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify sage seal was created
	var seal storage.Seal
	if err := db.Where("edict_id = ? AND minister_id = ?", 7, "chancellor").First(&seal).Error; err != nil {
		t.Errorf("expected sage seal to be granted: %v", err)
	}
}

func TestRecordPrecedentTool_RejectsManifestOnRejection(t *testing.T) {
	db := setupPrecedentTestDB(t)
	db.Create(&storage.ForgeManifest{
		ManifestID: "m1",
		EdictID:    9,
		Username:   "testuser",
		Project:    "testproject",
		Status:     storage.ManifestQuenched,
	})

	tool := RecordPrecedentTool{
		Ctx: ToolContext{
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
	}

	_, err := tool.Call(context.Background(),
		`{"edict_id": 9, "approved": false, "reasoning": "bad code"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify manifest was rejected
	var manifest storage.ForgeManifest
	db.Where("manifest_id = ?", "m1").First(&manifest)
	if manifest.Status != storage.ManifestRejected {
		t.Errorf("expected manifest status rejected, got %s", manifest.Status)
	}
}

// TestRecordPrecedentTool_NoManifests_Rejected verifies that when no manifests exist,
// a rejection creates an edict-level precedent and does NOT grant the sage seal.
func TestRecordPrecedentTool_NoManifests_Rejected(t *testing.T) {
	db := setupPrecedentTestDB(t)

	tool := RecordPrecedentTool{
		Ctx: ToolContext{
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
	}

	result, err := tool.Call(context.Background(),
		`{"edict_id": 11, "approved": false, "reasoning": "edict-level rejection"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "Recorded precedent (rejected) for edict 11"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}

	// Verify edict-level precedent was created with manifest_id = ''
	var precedent storage.CensorPrecedent
	if err := db.Where("manifest_id = ''").First(&precedent).Error; err != nil {
		t.Fatalf("expected edict-level precedent to be created: %v", err)
	}
	if precedent.Ruling != storage.PrecedentRejected {
		t.Errorf("expected ruling rejected, got %s", precedent.Ruling)
	}
	if precedent.Justification != "edict-level rejection" {
		t.Errorf("expected justification 'edict-level rejection', got %s", precedent.Justification)
	}
	if precedent.Username != "testuser" {
		t.Errorf("expected username 'testuser', got %s", precedent.Username)
	}
	if precedent.Project != "testproject" {
		t.Errorf("expected project 'testproject', got %s", precedent.Project)
	}

	// Verify NO sage seal was granted
	var sealCount int64
	db.Model(&storage.Seal{}).Where("edict_id = ? AND minister_id = ?", 11, "chancellor").Count(&sealCount)
	if sealCount != 0 {
		t.Errorf("expected no sage seal for rejected edict, got %d", sealCount)
	}
}

// TestRecordPrecedentTool_ZeroManifestsTagsEdictID verifies the zero-manifest
// branch writes edict_id on the edict-level precedent.
func TestRecordPrecedentTool_ZeroManifestsTagsEdictID(t *testing.T) {
	db := setupPrecedentTestDB(t)

	tool := RecordPrecedentTool{
		Ctx: ToolContext{
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
	}

	if _, err := tool.Call(context.Background(),
		`{"edict_id": 11, "approved": false, "reasoning": "edict-level rejection"}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var precedent storage.CensorPrecedent
	if err := db.Where("manifest_id = ''").First(&precedent).Error; err != nil {
		t.Fatalf("expected edict-level precedent to be created: %v", err)
	}
	if precedent.EdictID != 11 {
		t.Errorf("expected edict_id 11 on edict-level precedent, got %d", precedent.EdictID)
	}
}

// TestRecordPrecedentTool_PerManifestTagsEdictID verifies the per-manifest
// branch tags edict_id on each recorded precedent.
func TestRecordPrecedentTool_PerManifestTagsEdictID(t *testing.T) {
	db := setupPrecedentTestDB(t)
	db.Create(&storage.ForgeManifest{
		ManifestID: "m1",
		EdictID:    7,
		Username:   "testuser",
		Project:    "testproject",
		Status:     storage.ManifestQuenched,
	})

	tool := RecordPrecedentTool{
		Ctx: ToolContext{
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
	}

	if _, err := tool.Call(context.Background(),
		`{"edict_id": 7, "approved": true, "reasoning": "LGTM"}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var precedent storage.CensorPrecedent
	if err := db.Where("manifest_id = ?", "m1").First(&precedent).Error; err != nil {
		t.Fatalf("expected per-manifest precedent to be created: %v", err)
	}
	if precedent.EdictID != 7 {
		t.Errorf("expected edict_id 7 on per-manifest precedent, got %d", precedent.EdictID)
	}
}

// TestRecordPrecedentTool_NoManifests_Approved verifies that when no manifests exist,
// an approval creates an edict-level precedent and grants the sage seal.
func TestRecordPrecedentTool_NoManifests_Approved(t *testing.T) {
	db := setupPrecedentTestDB(t)

	tool := RecordPrecedentTool{
		Ctx: ToolContext{
			Username: "testuser",
			Project:  "testproject",
			DB:       db,
		},
	}

	_, err := tool.Call(context.Background(),
		`{"edict_id": 13, "approved": true, "reasoning": "edict-level approval"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify edict-level precedent was created
	var precedent storage.CensorPrecedent
	if err := db.Where("manifest_id = ''").First(&precedent).Error; err != nil {
		t.Fatalf("expected edict-level precedent to be created: %v", err)
	}
	if precedent.Ruling != storage.PrecedentApproved {
		t.Errorf("expected ruling approved, got %s", precedent.Ruling)
	}
	if precedent.Username != "testuser" {
		t.Errorf("expected username 'testuser', got %s", precedent.Username)
	}
	if precedent.Project != "testproject" {
		t.Errorf("expected project 'testproject', got %s", precedent.Project)
	}

	// Verify sage seal WAS granted
	var seal storage.Seal
	if err := db.Where("edict_id = ? AND minister_id = ?", 13, "chancellor").First(&seal).Error; err != nil {
		t.Errorf("expected sage seal to be granted: %v", err)
	}
}

// TestGrantChancellorSeal_StaleSealAllowsReseal verifies that
// grantChancellorSeal allows re-sealing when a previous chancellor seal
// is stale (invalidated by intent change). This is the idempotency fix
// from edict 714 — without AND stale_at IS NULL, the stale seal would
// block re-sealing and the seal chain could never be restored.
func TestGrantChancellorSeal_StaleSealAllowsReseal(t *testing.T) {
	db := setupPrecedentTestDB(t)

	key := storage.EdictKey{ID: 42, Username: "testuser", Project: "testproject"}

	// Grant a chancellor seal, then invalidate it
	sealSvc := storage.NewSealService(db)
	if err := sealSvc.GrantSeal(key, "chancellor", storage.JSON{}); err != nil {
		t.Fatalf("failed to grant chancellor seal: %v", err)
	}
	if err := sealSvc.InvalidateSeals(key); err != nil {
		t.Fatalf("failed to invalidate seals: %v", err)
	}

	// grantChancellorSeal must not find the stale seal and block
	if err := grantChancellorSeal(db, key, "chancellor", storage.JSON{}); err != nil {
		t.Fatalf("grantChancellorSeal should succeed after staleness: %v", err)
	}

	// Verify a new (non-stale) chancellor seal was created
	var count int64
	if err := db.Model(&storage.Seal{}).
		Where("edict_id = ? AND username = ? AND project = ? AND minister_id = ? AND stale_at IS NULL",
			key.ID, key.Username, key.Project, "chancellor").
		Count(&count).Error; err != nil {
		t.Fatalf("failed to count non-stale seals: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 non-stale chancellor seal after re-seal, got %d", count)
	}
}
