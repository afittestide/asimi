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
	if err := db.AutoMigrate(&CensorPrecedent{}, &ForgeManifest{}, &Seal{}); err != nil {
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
		ManifestID: "m1", EdictID: 42, Username: "u", Project: "p", Status: ManifestQuenched,
	}).Error; err != nil {
		t.Fatalf("failed to seed manifest: %v", err)
	}

	// Per-manifest precedent with edict_id unset (backfill target).
	if err := db.Create(&CensorPrecedent{
		PrecedentID: "per-manifest", ManifestID: "m1", Username: "u", Project: "p",
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
		PrecedentID: "per-manifest-null", ManifestID: "m1", Username: "u", Project: "p",
		Principle: "ethics_review", Ruling: PrecedentApproved,
	}).Error; err != nil {
		t.Fatalf("failed to seed NULL per-manifest precedent: %v", err)
	}
	// Edict-level / legacy precedent (must stay 0, not be resurrected from a manifest).
	if err := db.Create(&CensorPrecedent{
		PrecedentID: "edict-level", ManifestID: "", Username: "u", Project: "p",
		Principle: "ethics_review", Ruling: PrecedentRejected,
	}).Error; err != nil {
		t.Fatalf("failed to seed edict-level precedent: %v", err)
	}
	// Force genuine NULLs to prove the normalization covers legacy rows.
	if err := db.Exec(`UPDATE censor_precedents SET edict_id = NULL WHERE precedent_id IN ('edict-level', 'per-manifest-null')`).Error; err != nil {
		t.Fatalf("failed to force NULL: %v", err)
	}

	if err := PostGormMigrate(db); err != nil {
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
	if edictLevel.Username != "u" || edictLevel.Project != "p" {
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
	if err := PostGormMigrate(db); err != nil {
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
