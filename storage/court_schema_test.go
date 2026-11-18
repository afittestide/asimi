package storage

import (
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestJSONScan_ToleratesMalformed verifies a bad payload row degrades to a
// bounded marker instead of hard-erroring a multi-row scan (edict 857).
func TestJSONScan_ToleratesMalformed(t *testing.T) {
	// Valid input still unmarshals normally.
	var good JSON
	if err := good.Scan([]byte(`{"a":1}`)); err != nil {
		t.Fatalf("valid scan returned error: %v", err)
	}
	if good["a"] != float64(1) {
		t.Fatalf("valid scan of Payload = %#v, want a=1", good)
	}

	// A malformed value must not error the scan; it yields the marker.
	raw := []byte("{\"answer\": \"line one\nline two\"}")
	var bad JSON
	if err := bad.Scan(raw); err != nil {
		t.Fatalf("malformed scan returned a hard error: %v", err)
	}
	marker, ok := bad[MalformedMarkerKey].(string)
	if !ok || marker == "" {
		t.Fatalf("malformed scan of Payload = %#v, want %q marker", bad, MalformedMarkerKey)
	}

	// Nil stays nil and is not treated as malformed.
	var nilVal JSON
	if err := nilVal.Scan(nil); err != nil || nilVal != nil {
		t.Fatalf("nil scan = (%v, %#v), want (nil, nil)", err, nilVal)
	}
}

// TestJSONScan_MalformedDoesNotLeakRawValue verifies the scan neither returns
// the full raw payload in an error nor in the marker.
func TestJSONScan_MalformedDoesNotLeakRawValue(t *testing.T) {
	raw := []byte(`{"x": "` + strings.Repeat("z", 1000) + "\n" + `"}`)
	var j JSON
	err := j.Scan(raw)
	if err != nil && strings.Contains(err.Error(), string(raw)) {
		t.Fatalf("error leaked the full raw payload: %v", err)
	}
	marker, ok := j[MalformedMarkerKey].(string)
	if !ok {
		t.Fatalf("expected a string %q marker, got %#v", MalformedMarkerKey, j)
	}
	// Bounded to the marker limit (plus the truncation ellipsis), so a huge
	// poisoned value cannot bloat the marker or log output.
	if len([]rune(marker)) > malformedMarkerLimit+1 {
		t.Fatalf("marker length = %d runes, want at most %d", len([]rune(marker)), malformedMarkerLimit+1)
	}
}

// TestStringArrayScan_ToleratesMalformed verifies the same resilience for
// array-shaped rows (e.g. lings.dependencies).
func TestStringArrayScan_ToleratesMalformed(t *testing.T) {
	// Valid input still parses.
	var good StringArray
	if err := good.Scan([]byte(`["a","b"]`)); err != nil {
		t.Fatalf("valid scan returned error: %v", err)
	}
	if len(good) != 2 {
		t.Fatalf("valid scan = %#v, want 2 elements", good)
	}

	// Malformed input yields nil, not a hard error.
	var bad StringArray
	if err := bad.Scan([]byte(`["a",]`)); err != nil {
		t.Fatalf("malformed scan returned a hard error: %v", err)
	}
	if bad != nil {
		t.Fatalf("malformed scan = %#v, want nil", bad)
	}

	// A malformed value must not be echoed back in the returned error.
	raw := []byte(`["` + strings.Repeat("q", 500) + "\n")
	if err := bad.Scan(raw); err != nil && strings.Contains(err.Error(), string(raw)) {
		t.Fatalf("error leaked the full raw value: %v", err)
	}

	// Drivers may hand back a string rather than []byte.
	var fromStr StringArray
	if err := fromStr.Scan(`["x"]`); err != nil {
		t.Fatalf("string scan returned error: %v", err)
	}
	if len(fromStr) != 1 || fromStr[0] != "x" {
		t.Fatalf("string scan = %#v, want [x]", fromStr)
	}
}

// setupCourtSchemaTestDB opens an in-memory SQLite DB migrated for the
// censor_precedents / forge_manifests pair.
func setupCourtSchemaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	// Migrate every Court table PostGormMigrate touches: the slug migration
	// (edict 1005) spans all of them, so a partial schema would mask defects.
	if err := db.AutoMigrate(
		&Edict{}, &Seal{}, &Zhengming{}, &TianEvent{}, &TianEventDLQ{},
		&Ling{}, &ForgeManifest{}, &JudgeVerdict{}, &CensorPrecedent{},
		&Incident{}, &RulerCouncil{},
	); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

// TestCensorPrecedent_HasEdict covers the 正名 contract on EdictID: 0 = no
// owning edict (legacy OR deliberately edict-level), >0 = owning edict.
func TestCensorPrecedent_HasEdict(t *testing.T) {
	if (&CensorPrecedent{EdictID: 0}).HasEdict() {
		t.Errorf("EdictID 0 must report HasEdict() == false")
	}
	if !(&CensorPrecedent{EdictID: 7}).HasEdict() {
		t.Errorf("EdictID 7 must report HasEdict() == true")
	}
}

// TestPostGormMigrate_NormalizesNullEdictIDAndBackfills verifies the
// censor_precedents.edict_id normalization contract:
//   - NULL collapses to the single sentinel 0,
//   - the per-manifest backfill still fills per-manifest rows from their owning
//     forge_manifests row,
//   - both statements are idempotent (a second run changes nothing), and
//   - usernames/projects are never disturbed.
func TestPostGormMigrate_NormalizesNullEdictIDAndBackfills(t *testing.T) {
	db := setupCourtSchemaTestDB(t)

	// Seed a manifest to drive the backfill.
	if err := db.Create(&ForgeManifest{
		ManifestID: "m1", EdictID: 42, Username: "daonb", Project: "daonb/p", Status: ManifestQuenched,
	}).Error; err != nil {
		t.Fatalf("failed to seed manifest: %v", err)
	}

	// Per-manifest precedent with edict_id unset (backfill target).
	if err := db.Create(&CensorPrecedent{
		PrecedentID: "per-manifest", ManifestID: "m1", Username: "daonb", Project: "daonb/p",
		Principle: "ethics_review", Ruling: PrecedentApproved,
	}).Error; err != nil {
		t.Fatalf("failed to seed per-manifest precedent: %v", err)
	}
	// The dominant legacy shape: a genuine NULL edict_id row that DOES have a
	// resolvable manifest_id. The backfill must attribute it to 42; only the
	// normalization (which runs after) may send it to 0, and it must not,
	// because the owner is known. A NULL-blind backfill would miss this row and
	// the normalization would then wrongly collapse it to the sentinel.
	if err := db.Create(&CensorPrecedent{
		PrecedentID: "per-manifest-null", ManifestID: "m1", Username: "daonb", Project: "daonb/p",
		Principle: "ethics_review", Ruling: PrecedentApproved,
	}).Error; err != nil {
		t.Fatalf("failed to seed NULL per-manifest precedent: %v", err)
	}
	// Edict-level / legacy precedent (must stay 0, not be resurrected from a manifest).
	if err := db.Create(&CensorPrecedent{
		PrecedentID: "edict-level", ManifestID: "", Username: "daonb", Project: "daonb/p",
		Principle: "ethics_review", Ruling: PrecedentRejected,
	}).Error; err != nil {
		t.Fatalf("failed to seed edict-level precedent: %v", err)
	}
	// Force genuine NULLs to prove the normalization covers legacy rows.
	if err := db.Exec(`UPDATE censor_precedents SET edict_id = NULL WHERE precedent_id IN ('edict-level', 'per-manifest-null')`).Error; err != nil {
		t.Fatalf("failed to force NULL: %v", err)
	}

	if err := PostGormMigrate(db, "daonb"); err != nil {
		t.Fatalf("PostGormMigrate failed: %v", err)
	}

	var perManifest CensorPrecedent
	if err := db.Where("precedent_id = ?", "per-manifest").First(&perManifest).Error; err != nil {
		t.Fatalf("failed to reload per-manifest precedent: %v", err)
	}
	if perManifest.EdictID != 42 {
		t.Errorf("backfill: per-manifest edict_id = %d, want 42", perManifest.EdictID)
	}

	// The NULL + resolvable-manifest row must be attributed, not collapsed.
	var perManifestNull CensorPrecedent
	if err := db.Where("precedent_id = ?", "per-manifest-null").First(&perManifestNull).Error; err != nil {
		t.Fatalf("failed to reload NULL per-manifest precedent: %v", err)
	}
	if perManifestNull.EdictID != 42 {
		t.Errorf("backfill must attribute a NULL edict_id row with a resolvable manifest; got %d, want 42", perManifestNull.EdictID)
	}

	var edictLevel CensorPrecedent
	if err := db.Where("precedent_id = ?", "edict-level").First(&edictLevel).Error; err != nil {
		t.Fatalf("failed to reload edict-level precedent: %v", err)
	}
	if edictLevel.EdictID != 0 {
		t.Errorf("normalization: edict-level edict_id = %d, want 0 (NULL collapsed)", edictLevel.EdictID)
	}
	if edictLevel.Username != "daonb" || edictLevel.Project != "daonb/p" {
		t.Errorf("normalization must not disturb username/project, got %q/%q", edictLevel.Username, edictLevel.Project)
	}

	// No NULL may survive the migration (the column must have a single sentinel).
	var nullCount int64
	if err := db.Model(&CensorPrecedent{}).Where("edict_id IS NULL").Count(&nullCount).Error; err != nil {
		t.Fatalf("failed to count NULLs: %v", err)
	}
	if nullCount != 0 {
		t.Errorf("expected 0 NULL edict_id rows after migration, got %d", nullCount)
	}

	// Idempotency: a second run must not change any value.
	if err := PostGormMigrate(db, "daonb"); err != nil {
		t.Fatalf("second PostGormMigrate failed: %v", err)
	}
	var perManifest2 CensorPrecedent
	db.Where("precedent_id = ?", "per-manifest").First(&perManifest2)
	if perManifest2.EdictID != 42 {
		t.Errorf("idempotency: per-manifest edict_id drifted to %d", perManifest2.EdictID)
	}
	var perManifestNull2 CensorPrecedent
	db.Where("precedent_id = ?", "per-manifest-null").First(&perManifestNull2)
	if perManifestNull2.EdictID != 42 {
		t.Errorf("idempotency: NULL per-manifest edict_id drifted to %d", perManifestNull2.EdictID)
	}
	var edictLevel2 CensorPrecedent
	db.Where("precedent_id = ?", "edict-level").First(&edictLevel2)
	if edictLevel2.EdictID != 0 {
		t.Errorf("idempotency: edict-level edict_id drifted to %d", edictLevel2.EdictID)
	}
}

// TestMigrateProjectSlugs_ReshapesDocketAndCorrectsRogueUsernames covers edict
// 1005's required migration:
//   - "<old owner>/<repo>" becomes "<verified user>/<repo>" (the owner segment is
//     replaced, the basename preserved),
//   - a gitless project with no slash is kept as the basename,
//   - 'forge' and NULL/empty usernames are corrected to the verified user in
//     the same pass (the Ruler ruled the rogue rows are migrated, not
//     quarantined),
//   - a genuine other user's ledger is left alone (no data loss by merging),
//   - an EMPTY project is untouched (Chancellor waiver, ling 1c5f3b2dd8d8bace),
//   - a second run changes nothing (idempotent).
func TestMigrateProjectSlugs_ReshapesDocketAndCorrectsRogueUsernames(t *testing.T) {
	db := setupCourtSchemaTestDB(t)

	seed := []Edict{
		{ID: 1, Username: "daonb", Project: "afittestide/asimi-cli", Intent: "old owner shape"},
		{ID: 2, Username: "daonb", Project: "asimi-cli", Intent: "gitless basename"},
		{ID: 3, Username: "forge", Project: "daonb/crypto-new", Intent: "rogue role identity"},
		{ID: 4, Username: "", Project: "tuzig/terminal7", Intent: "empty identity"},
		{ID: 5, Username: "alice", Project: "alice/other-repo", Intent: "another user's ledger"},
		{ID: 6, Username: "daonb", Project: "", Intent: "empty project (waived)"},
	}
	for _, e := range seed {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("failed to seed edict %d: %v", e.ID, err)
		}
	}

	if err := PostGormMigrate(db, "daonb"); err != nil {
		t.Fatalf("PostGormMigrate failed: %v", err)
	}

	want := map[uint]struct{ user, project string }{
		1: {"daonb", "daonb/asimi-cli"},
		2: {"daonb", "daonb/asimi-cli"},
		3: {"daonb", "daonb/crypto-new"},
		4: {"daonb", "daonb/terminal7"},
		5: {"alice", "alice/other-repo"},
		6: {"daonb", ""},
	}
	for id, w := range want {
		var got Edict
		if err := db.Where("id = ?", id).First(&got).Error; err != nil {
			t.Fatalf("failed to reload edict %d: %v", id, err)
		}
		if got.Project != w.project {
			t.Errorf("edict %d project = %q, want %q", id, got.Project, w.project)
		}
		if got.Username != w.user {
			t.Errorf("edict %d username = %q, want %q", id, got.Username, w.user)
		}
	}

	// No row may keep the old owner-shaped slug for a verified user.
	var staleCount int64
	if err := db.Model(&Edict{}).Where("username = ? AND project LIKE ?", "daonb", "afittestide/%").Count(&staleCount).Error; err != nil {
		t.Fatalf("failed to count stale slugs: %v", err)
	}
	if staleCount != 0 {
		t.Errorf("expected 0 rows under the old owner shape, got %d", staleCount)
	}

	// Idempotency: a second run must not change any value.
	if err := PostGormMigrate(db, "daonb"); err != nil {
		t.Fatalf("second PostGormMigrate failed: %v", err)
	}
	var again Edict
	db.Where("id = ?", 1).First(&again)
	if again.Project != "daonb/asimi-cli" || again.Username != "daonb" {
		t.Errorf("idempotency: edict 1 drifted to %q/%q", again.Username, again.Project)
	}
}

// TestMigrateProjectSlugs_CoversEveryCourtTable verifies acceptance 4 across all
// eleven Court tables, not just edicts: no row may be left under the old
// owner-shaped slug, and a rogue username anywhere is corrected.
func TestMigrateProjectSlugs_CoversEveryCourtTable(t *testing.T) {
	db := setupCourtSchemaTestDB(t)

	// One representative row per table, all under the old "owner/repo" shape.
	if err := db.Create(&Seal{SealID: "s1", EdictID: 1, Username: "daonb", Project: "afittestide/asimi-cli", MinisterID: "judge"}).Error; err != nil {
		t.Fatalf("seed seals: %v", err)
	}
	if err := db.Create(&Zhengming{RequestID: "z1", EdictID: 1, Username: "forge", Project: "tuzig/terminal7"}).Error; err != nil {
		t.Fatalf("seed zhengming: %v", err)
	}
	if err := db.Create(&TianEvent{EdictID: 1, Username: "daonb", Project: "bennydaon/idp", EventType: EventEdictCreated}).Error; err != nil {
		t.Fatalf("seed tian_events: %v", err)
	}
	if err := db.Create(&TianEventDLQ{EdictID: 1, Username: "daonb", Project: "BoG--IL/frontend", EventType: EventEdictCreated}).Error; err != nil {
		t.Fatalf("seed tian_event_dlq: %v", err)
	}
	if err := db.Create(&Ling{LingID: "l1", EdictID: 1, Username: "daonb", Project: "bennydaon/frontend"}).Error; err != nil {
		t.Fatalf("seed lings: %v", err)
	}
	if err := db.Create(&ForgeManifest{ManifestID: "m1", EdictID: 1, Username: "daonb", Project: "afittestide/asimi-cli"}).Error; err != nil {
		t.Fatalf("seed forge_manifests: %v", err)
	}
	if err := db.Create(&JudgeVerdict{VerdictID: "v1", ManifestID: "m1", Username: "daonb", Project: "afittestide/asimi-cli", Outcome: VerdictPassed}).Error; err != nil {
		t.Fatalf("seed judge_verdicts: %v", err)
	}
	if err := db.Create(&CensorPrecedent{PrecedentID: "p1", Username: "daonb", Project: "afittestide/asimi-cli", Principle: "ethics_review", Ruling: PrecedentApproved}).Error; err != nil {
		t.Fatalf("seed censor_precedents: %v", err)
	}
	if err := db.Create(&Incident{IncidentID: "i1", Username: "daonb", Project: "tuzig/terminal7"}).Error; err != nil {
		t.Fatalf("seed incidents: %v", err)
	}
	if err := db.Create(&RulerCouncil{CouncilID: "c1", EdictID: 1, Username: "daonb", Project: "bennydaon/idp"}).Error; err != nil {
		t.Fatalf("seed ruler_councils: %v", err)
	}

	if err := PostGormMigrate(db, "daonb"); err != nil {
		t.Fatalf("PostGormMigrate failed: %v", err)
	}

	// No row in any table may still carry an owner-shaped slug, and none may
	// keep the non-verified 'forge' identity.
	for _, table := range []string{
		"seals", "zhengming_requests", "tian_events", "tian_event_dlq", "lings",
		"forge_manifests", "judge_verdicts", "censor_precedents", "incidents", "ruler_councils",
	} {
		var stale int64
		if err := db.Table(table).Where("project NOT LIKE ?", "daonb/%").Count(&stale).Error; err != nil {
			t.Fatalf("count stale %s: %v", table, err)
		}
		if stale != 0 {
			t.Errorf("%s: %d rows left outside the daonb/ shape", table, stale)
		}
		var forge int64
		if err := db.Table(table).Where("username = ?", "forge").Count(&forge).Error; err != nil {
			t.Fatalf("count forge %s: %v", table, err)
		}
		if forge != 0 {
			t.Errorf("%s: %d rows still under the 'forge' role identity", table, forge)
		}
	}
}
