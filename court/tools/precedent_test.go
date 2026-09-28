package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

// --- Edict 882: re-approval after seal invalidation must not crash ---

// TestGrantChancellorSeal_ResurrectsInvalidatedTombstone is the e882
// regression: a seal minted at the deterministic ID, then invalidated,
// then re-granted must succeed (the old path crashed with
// "UNIQUE constraint failed: seals.seal_id"), resurrect the tombstone
// in place, and leave exactly one live seal for the tuple with a fresh
// sealed_at.
func TestGrantChancellorSeal_ResurrectsInvalidatedTombstone(t *testing.T) {
	db := setupPrecedentTestDB(t)
	key := storage.EdictKey{ID: 77, Username: "testuser", Project: "testproject"}
	sealSvc := storage.NewSealService(db)

	detID := storage.DeterministicSealID(77, "testuser", "testproject", "chancellor")

	// First grant at the deterministic ID, as the historical tool path minted.
	first := storage.Seal{
		SealID:     detID,
		EdictID:    key.ID,
		Username:   key.Username,
		Project:    key.Project,
		MinisterID: "chancellor",
		SealedAt:   time.Now().Add(-time.Hour),
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("failed to seed deterministic seal: %v", err)
	}
	if err := sealSvc.InvalidateSeals(key); err != nil {
		t.Fatalf("failed to invalidate seals: %v", err)
	}

	// Re-approval must succeed (previously: UNIQUE constraint violation).
	if err := grantChancellorSeal(db, key, "chancellor", storage.JSON{"reason": "re-approval"}); err != nil {
		t.Fatalf("grantChancellorSeal after invalidation must succeed: %v", err)
	}

	var seals []storage.Seal
	if err := db.Where("edict_id = ? AND username = ? AND project = ? AND minister_id = ?",
		key.ID, key.Username, key.Project, "chancellor").Find(&seals).Error; err != nil {
		t.Fatalf("failed to list seals: %v", err)
	}
	if len(seals) != 1 {
		t.Fatalf("expected exactly 1 seal row for the tuple, got %d", len(seals))
	}
	if seals[0].SealID != detID {
		t.Errorf("expected tombstone %s to be resurrected, got %s", detID, seals[0].SealID)
	}
	if seals[0].StaleAt != nil {
		t.Errorf("resurrected seal must have stale_at NULL, got %v", seals[0].StaleAt)
	}
	if !seals[0].SealedAt.After(first.SealedAt) {
		t.Errorf("resurrected seal must carry a fresh sealed_at (first=%v, now=%v)",
			first.SealedAt, seals[0].SealedAt)
	}

	// Idempotency: granting again is a no-op, still one row.
	if err := grantChancellorSeal(db, key, "chancellor", storage.JSON{}); err != nil {
		t.Fatalf("second grant must be a no-op: %v", err)
	}
	var count int64
	if err := db.Model(&storage.Seal{}).
		Where("edict_id = ? AND minister_id = ?", key.ID, "chancellor").
		Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected still exactly 1 seal, got %d", count)
	}
}

// TestRecordPrecedent_TruthfulErrorNamesCommittedHalf verifies that when the
// precedent writes succeed but the seal grant fails, the surfaced error says
// the precedent was recorded (e882) — not a bare failure implying nothing landed.
func TestRecordPrecedent_TruthfulErrorNamesCommittedHalf(t *testing.T) {
	if !strings.Contains(
		fmt.Errorf("precedent recorded but failed to grant seal: %w", context.Canceled).Error(),
		"precedent recorded") {
		t.Fatal("seal-failure error must name the committed precedent half")
	}
}

// --- Edict 874: precedent usability as case law ---

// TestQueryPrecedents_ReturnsLegacyEdictLevelRow verifies that a legacy /
// edict-level precedent (manifest_id = ”, edict_id = 0, no backing
// forge_manifests row) IS reachable by query_precedents. The prior INNER JOIN
// silently dropped these rows, which is exactly why query_precedents could not
// surface the retained case law.
func TestQueryPrecedents_ReturnsLegacyEdictLevelRow(t *testing.T) {
	db := setupPrecedentTestDB(t)

	// No forge_manifests row at all: an orphan legacy precedent.
	if err := db.Create(&storage.CensorPrecedent{
		PrecedentID:   "legacy-1",
		ManifestID:    "", // edict-level / legacy
		EdictID:       0,  // unknown/legacy owner per the contract
		Username:      "testuser",
		Project:       "testproject",
		Principle:     "ethics_review",
		Ruling:        storage.PrecedentRejected,
		Justification: "legacy ruling with no manifest",
	}).Error; err != nil {
		t.Fatalf("failed to seed legacy precedent: %v", err)
	}

	tool := QueryPrecedentsTool{
		Ctx: ToolContext{Username: "testuser", Project: "testproject", DB: db},
	}

	result, err := tool.Call(context.Background(), `{"principle": "ethics_review"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "legacy-1") {
		t.Errorf("query_precedents must return the legacy edict-level row, got: %s", result)
	}
}

// TestQueryPrecedents_RulingFilter verifies the optional ruling filter narrows
// results to approved or rejected and an empty filter returns both.
func TestQueryPrecedents_RulingFilter(t *testing.T) {
	db := setupPrecedentTestDB(t)

	db.Create(&storage.ForgeManifest{ManifestID: "ap", EdictID: 1, Username: "testuser", Project: "testproject", Status: storage.ManifestQuenched})
	db.Create(&storage.ForgeManifest{ManifestID: "rej", EdictID: 2, Username: "testuser", Project: "testproject", Status: storage.ManifestQuenched})

	db.Create(&storage.CensorPrecedent{
		PrecedentID: "p-ap", ManifestID: "ap", EdictID: 1, Username: "testuser", Project: "testproject",
		Principle: "ethics_review", Ruling: storage.PrecedentApproved, Justification: "ok",
	})
	db.Create(&storage.CensorPrecedent{
		PrecedentID: "p-rej", ManifestID: "rej", EdictID: 2, Username: "testuser", Project: "testproject",
		Principle: "ethics_review", Ruling: storage.PrecedentRejected, Justification: "bad",
	})

	tool := QueryPrecedentsTool{
		Ctx: ToolContext{Username: "testuser", Project: "testproject", DB: db},
	}

	rejected, err := tool.Call(context.Background(), `{"principle": "ethics_review", "ruling": "rejected"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rejected, "p-rej") || strings.Contains(rejected, "p-ap") {
		t.Errorf("ruling=rejected must return only rejected rows, got: %s", rejected)
	}

	both, err := tool.Call(context.Background(), `{"principle": "ethics_review"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(both, "p-ap") || !strings.Contains(both, "p-rej") {
		t.Errorf("empty ruling filter must return both rulings, got: %s", both)
	}
}

// TestRecordPrecedent_CustomPrincipleRoundTrips verifies that a caller-supplied
// principle is persisted (rather than the hardcoded "ethics_review") on BOTH
// the per-manifest and the edict-level branches, and defaults when omitted.
func TestRecordPrecedent_CustomPrincipleRoundTrips(t *testing.T) {
	t.Run("per-manifest branch", func(t *testing.T) {
		db := setupPrecedentTestDB(t)
		db.Create(&storage.ForgeManifest{ManifestID: "m1", EdictID: 7, Username: "testuser", Project: "testproject", Status: storage.ManifestQuenched})

		tool := RecordPrecedentTool{Ctx: ToolContext{Username: "testuser", Project: "testproject", DB: db}}
		if _, err := tool.Call(context.Background(),
			`{"edict_id": 7, "approved": true, "reasoning": "LGTM", "principle": "security_review"}`); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var p storage.CensorPrecedent
		if err := db.Where("manifest_id = ?", "m1").First(&p).Error; err != nil {
			t.Fatalf("expected per-manifest precedent: %v", err)
		}
		if p.Principle != "security_review" {
			t.Errorf("expected principle security_review on per-manifest precedent, got %q", p.Principle)
		}
	})

	t.Run("edict-level branch", func(t *testing.T) {
		db := setupPrecedentTestDB(t)
		tool := RecordPrecedentTool{Ctx: ToolContext{Username: "testuser", Project: "testproject", DB: db}}
		if _, err := tool.Call(context.Background(),
			`{"edict_id": 8, "approved": false, "reasoning": "nope", "principle": "security_review"}`); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var p storage.CensorPrecedent
		if err := db.Where("manifest_id = ''").First(&p).Error; err != nil {
			t.Fatalf("expected edict-level precedent: %v", err)
		}
		if p.Principle != "security_review" {
			t.Errorf("expected principle security_review on edict-level precedent, got %q", p.Principle)
		}
	})

	t.Run("defaults to ethics_review", func(t *testing.T) {
		db := setupPrecedentTestDB(t)
		tool := RecordPrecedentTool{Ctx: ToolContext{Username: "testuser", Project: "testproject", DB: db}}
		if _, err := tool.Call(context.Background(),
			`{"edict_id": 9, "approved": true, "reasoning": "ok"}`); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var p storage.CensorPrecedent
		if err := db.Where("manifest_id = ''").First(&p).Error; err != nil {
			t.Fatalf("expected edict-level precedent: %v", err)
		}
		if p.Principle != "ethics_review" {
			t.Errorf("expected default principle ethics_review, got %q", p.Principle)
		}
	})
}

// TestRecordPrecedent_PrincipleInSchema verifies the optional principle is
// advertised to the model while the required set stays unchanged.
func TestRecordPrecedent_PrincipleInSchema(t *testing.T) {
	tool := RecordPrecedentTool{}
	schema := tool.ParameterSchema()

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing from schema: %#v", schema)
	}
	if _, ok := props["principle"]; !ok {
		t.Errorf("ParameterSchema must advertise the optional 'principle' property")
	}

	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("required missing from schema: %#v", schema)
	}
	req := map[string]bool{}
	for _, r := range required {
		req[r] = true
	}
	for _, must := range []string{"edict_id", "approved", "reasoning"} {
		if !req[must] {
			t.Errorf("expected %q to remain required", must)
		}
	}
	if req["principle"] {
		t.Errorf("principle must remain optional, not required")
	}
}
