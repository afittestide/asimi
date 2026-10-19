package storage

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupSealServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	if err := db.AutoMigrate(&Edict{}, &Seal{}, &Zhengming{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	return db
}

func TestListActiveEdicts_ORSemantics(t *testing.T) {
	db := setupSealServiceTestDB(t)
	svc := NewSealService(db)

	// Create test edicts
	edicts := []Edict{
		{ID: 1, Username: "testuser", Project: "testproject", Intent: "Active edict (un-cancelled, unsealed)"},
		{ID: 2, Username: "testuser", Project: "testproject", Intent: "Cancelled but unsealed (should NOT be included)"},
		{ID: 3, Username: "testuser", Project: "testproject", Intent: "Sealed but un-cancelled (should NOT be included)"},
		{ID: 4, Username: "testuser", Project: "testproject", Intent: "Sealed and cancelled (should NOT be included)"},
	}
	for i := range edicts {
		if err := db.Create(&edicts[i]).Error; err != nil {
			t.Fatalf("failed to create edict: %v", err)
		}
	}

	// Cancel edict 2
	now := time.Now()
	db.Model(&Edict{}).Where("id = ?", 2).Update("cancelled_at", &now)

	// Seal edict 3 with all three seals (judge, chancellor, ruler)
	for _, ministerID := range []string{"judge", "chancellor", "ruler"} {
		if err := svc.GrantSeal(edicts[2].Key(), ministerID, JSON{}); err != nil {
			t.Fatalf("failed to grant seal: %v", err)
		}
	}

	// Seal edict 4 with all three seals
	for _, ministerID := range []string{"judge", "chancellor", "ruler"} {
		if err := svc.GrantSeal(edicts[3].Key(), ministerID, JSON{}); err != nil {
			t.Fatalf("failed to grant seal: %v", err)
		}
	}
	// Then cancel edict 4
	db.Model(&Edict{}).Where("id = ?", 4).Update("cancelled_at", &now)

	// Test ListActiveEdicts - should return only edict 1 (un-cancelled AND unsealed)
	activeEdicts, err := svc.ListActiveEdicts("testuser", "testproject")
	if err != nil {
		t.Fatalf("ListActiveEdicts failed: %v", err)
	}

	if len(activeEdicts) != 1 {
		t.Errorf("Expected 1 active edict, got %d", len(activeEdicts))
	}

	// Build a map of returned edict IDs for easy checking
	returnedIDs := make(map[uint]bool)
	for _, e := range activeEdicts {
		returnedIDs[e.ID] = true
	}

	// Edict 1 should be included (un-cancelled, unsealed)
	if !returnedIDs[1] {
		t.Error("Expected edict 1 (active) to be included")
	}

	// Edict 2 should NOT be included (cancelled)
	if returnedIDs[2] {
		t.Error("Edict 2 (cancelled) should NOT be included")
	}

	// Edict 3 should NOT be included (sealed with ruler seal)
	if returnedIDs[3] {
		t.Error("Edict 3 (sealed with ruler seal) should NOT be included")
	}

	// Edict 4 should NOT be included (cancelled AND sealed)
	if returnedIDs[4] {
		t.Error("Edict 4 (cancelled AND sealed) should NOT be included")
	}
}

func TestListActiveEdicts_Empty(t *testing.T) {
	db := setupSealServiceTestDB(t)
	svc := NewSealService(db)

	activeEdicts, err := svc.ListActiveEdicts("testuser", "testproject")
	if err != nil {
		t.Fatalf("ListActiveEdicts failed: %v", err)
	}

	if len(activeEdicts) != 0 {
		t.Errorf("Expected 0 edicts, got %d", len(activeEdicts))
	}
}

func TestListActiveEdicts_SealStatusPopulated(t *testing.T) {
	db := setupSealServiceTestDB(t)
	svc := NewSealService(db)

	// Create an edict
	edict := Edict{ID: 1, Username: "testuser", Project: "testproject", Intent: "Test"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// Grant only judge seal
	if err := svc.GrantSeal(edict.Key(), "judge", JSON{}); err != nil {
		t.Fatalf("failed to grant seal: %v", err)
	}

	activeEdicts, err := svc.ListActiveEdicts("testuser", "testproject")
	if err != nil {
		t.Fatalf("ListActiveEdicts failed: %v", err)
	}

	if len(activeEdicts) != 1 {
		t.Fatalf("Expected 1 edict, got %d", len(activeEdicts))
	}

	if !activeEdicts[0].HasJudgeSeal {
		t.Error("Expected HasJudgeSeal to be true")
	}
	if activeEdicts[0].HasChancellorSeal {
		t.Error("Expected HasChancellorSeal to be false")
	}
}

func TestListActiveEdicts_ChancellorSealPopulated(t *testing.T) {
	db := setupSealServiceTestDB(t)
	svc := NewSealService(db)

	// Create an edict
	edict := Edict{ID: 1, Username: "testuser", Project: "testproject", Intent: "Test"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// Grant chancellor seal
	if err := svc.GrantSeal(edict.Key(), "chancellor", JSON{}); err != nil {
		t.Fatalf("failed to grant chancellor seal: %v", err)
	}

	activeEdicts, err := svc.ListActiveEdicts("testuser", "testproject")
	if err != nil {
		t.Fatalf("ListActiveEdicts failed: %v", err)
	}

	if len(activeEdicts) != 1 {
		t.Fatalf("Expected 1 edict, got %d", len(activeEdicts))
	}

	if activeEdicts[0].HasJudgeSeal {
		t.Error("Expected HasJudgeSeal to be false (only chancellor seal granted)")
	}
	if !activeEdicts[0].HasChancellorSeal {
		t.Error("Expected HasChancellorSeal to be true — the SQL must fetch chancellor seals")
	}
}

func TestListActiveEdicts_ExcludesEmptyProject(t *testing.T) {
	db := setupSealServiceTestDB(t)
	svc := NewSealService(db)

	// Create an edict with empty project
	emptyProjectEdict := Edict{ID: 1, Username: "testuser", Project: "", Intent: "Empty project edict"}
	if err := db.Create(&emptyProjectEdict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// Create an edict with valid project
	validProjectEdict := Edict{ID: 2, Username: "testuser", Project: "testproject", Intent: "Valid project edict"}
	if err := db.Create(&validProjectEdict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// ListActiveEdicts should NOT include the empty project edict
	activeEdicts, err := svc.ListActiveEdicts("testuser", "testproject")
	if err != nil {
		t.Fatalf("ListActiveEdicts failed: %v", err)
	}

	if len(activeEdicts) != 1 {
		t.Fatalf("Expected 1 active edict, got %d", len(activeEdicts))
	}

	if activeEdicts[0].ID != 2 {
		t.Errorf("Expected edict 2 (valid project) to be included, got edict %d", activeEdicts[0].ID)
	}

	for _, e := range activeEdicts {
		if e.Project == "" {
			t.Error("Edict with empty project should NOT be in active edicts list")
		}
	}
}

func TestInvalidateSealsMarksExistingSealsStale(t *testing.T) {
	db := setupSealServiceTestDB(t)
	svc := NewSealService(db)

	edict := Edict{ID: 1, Username: "testuser", Project: "testproject", Intent: "Original intent"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// Grant judge and chancellor seals
	for _, ministerID := range []string{"judge", "chancellor"} {
		if err := svc.GrantSeal(edict.Key(), ministerID, JSON{}); err != nil {
			t.Fatalf("failed to grant %s seal: %v", ministerID, err)
		}
	}

	// Verify seals exist and are not stale
	hasJudge, err := svc.HasSeal(edict.Key(), "judge")
	if err != nil {
		t.Fatalf("HasSeal judge: %v", err)
	}
	if !hasJudge {
		t.Fatal("Expected judge seal to exist before invalidation")
	}

	// Invalidate seals (simulates intent change)
	if err := svc.InvalidateSeals(edict.Key()); err != nil {
		t.Fatalf("InvalidateSeals: %v", err)
	}

	// HasSeal should now return false for both
	hasJudge, err = svc.HasSeal(edict.Key(), "judge")
	if err != nil {
		t.Fatalf("HasSeal judge after invalidation: %v", err)
	}
	if hasJudge {
		t.Error("Expected HasSeal(judge) to return false after invalidation")
	}

	hasChancellor, err := svc.HasSeal(edict.Key(), "chancellor")
	if err != nil {
		t.Fatalf("HasSeal chancellor after invalidation: %v", err)
	}
	if hasChancellor {
		t.Error("Expected HasSeal(chancellor) to return false after invalidation")
	}

	// GetSeals should show stale_at set
	seals, err := svc.GetSeals(edict.Key())
	if err != nil {
		t.Fatalf("GetSeals: %v", err)
	}
	if len(seals) != 2 {
		t.Fatalf("Expected 2 seals, got %d", len(seals))
	}
	for _, seal := range seals {
		if seal.StaleAt == nil {
			t.Errorf("Expected seal %s to have StaleAt set, got nil", seal.MinisterID)
		}
	}
}

func TestGetActiveSealsExcludesStaleTombstones(t *testing.T) {
	db := setupSealServiceTestDB(t)
	svc := NewSealService(db)

	edict := Edict{ID: 1, Username: "testuser", Project: "testproject", Intent: "Original intent"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	for _, ministerID := range []string{"judge", "chancellor", "ruler"} {
		if err := svc.GrantSeal(edict.Key(), ministerID, JSON{}); err != nil {
			t.Fatalf("failed to grant %s seal: %v", ministerID, err)
		}
	}

	if err := svc.InvalidateSeals(edict.Key()); err != nil {
		t.Fatalf("InvalidateSeals: %v", err)
	}

	active, err := svc.GetActiveSeals(edict.Key())
	if err != nil {
		t.Fatalf("GetActiveSeals: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("Expected 0 active seals after invalidation, got %d", len(active))
	}

	// GetSeals remains the history accessor and still exposes the tombstones.
	history, err := svc.GetSeals(edict.Key())
	if err != nil {
		t.Fatalf("GetSeals: %v", err)
	}
	if len(history) != 3 {
		t.Errorf("Expected 3 historical seals after invalidation, got %d", len(history))
	}

	// Re-sealing only the ruler leaves judge/chancellor stale.
	if err := svc.GrantSeal(edict.Key(), "ruler", JSON{}); err != nil {
		t.Fatalf("failed to re-grant ruler seal: %v", err)
	}
	active, err = svc.GetActiveSeals(edict.Key())
	if err != nil {
		t.Fatalf("GetActiveSeals after re-seal: %v", err)
	}
	if len(active) != 1 || active[0].MinisterID != "ruler" || active[0].StaleAt != nil {
		t.Errorf("Expected only the fresh ruler seal, got %+v", active)
	}
}

func TestResealAfterStalenessRestoresStatus(t *testing.T) {
	db := setupSealServiceTestDB(t)
	svc := NewSealService(db)

	edict := Edict{ID: 1, Username: "testuser", Project: "testproject", Intent: "Original intent"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// Grant judge and chancellor seals
	for _, ministerID := range []string{"judge", "chancellor"} {
		if err := svc.GrantSeal(edict.Key(), ministerID, JSON{}); err != nil {
			t.Fatalf("failed to grant %s seal: %v", ministerID, err)
		}
	}

	// Invalidate seals (intent changed)
	if err := svc.InvalidateSeals(edict.Key()); err != nil {
		t.Fatalf("InvalidateSeals: %v", err)
	}

	// Status should be "active" (no valid seals)
	status, err := svc.GetEdictStatus(edict.Key())
	if err != nil {
		t.Fatalf("GetEdictStatus after invalidation: %v", err)
	}
	if status != EdictActive {
		t.Errorf("Expected status active after invalidation, got %s", status)
	}

	// Re-seal with all three
	for _, ministerID := range []string{"judge", "chancellor", "ruler"} {
		if err := svc.GrantSeal(edict.Key(), ministerID, JSON{}); err != nil {
			t.Fatalf("failed to re-grant %s seal: %v", ministerID, err)
		}
	}

	// Status should now be "sealed"
	status, err = svc.GetEdictStatus(edict.Key())
	if err != nil {
		t.Fatalf("GetEdictStatus after re-seal: %v", err)
	}
	if status != EdictSealed {
		t.Errorf("Expected status sealed after re-seal, got %s", status)
	}

	// GetSeals should return 5 seals total (2 stale + 3 fresh)
	seals, err := svc.GetSeals(edict.Key())
	if err != nil {
		t.Fatalf("GetSeals: %v", err)
	}
	if len(seals) != 5 {
		t.Errorf("Expected 5 seals (2 stale + 3 fresh), got %d", len(seals))
	}

	// Verify the fresh seals are not stale
	freshCount := 0
	for _, seal := range seals {
		if seal.StaleAt == nil {
			freshCount++
		}
	}
	if freshCount != 3 {
		t.Errorf("Expected 3 fresh (non-stale) seals, got %d", freshCount)
	}
}

func TestListActiveEdicts_SealedEdictReappearsAfterStaleness(t *testing.T) {
	db := setupSealServiceTestDB(t)
	svc := NewSealService(db)

	edict := Edict{ID: 1, Username: "testuser", Project: "testproject", Intent: "Sealed then refined"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// Seal with all three ministers
	for _, ministerID := range []string{"judge", "chancellor", "ruler"} {
		if err := svc.GrantSeal(edict.Key(), ministerID, JSON{}); err != nil {
			t.Fatalf("failed to grant %s seal: %v", ministerID, err)
		}
	}

	// Should NOT appear in active edicts (it's sealed)
	active, err := svc.ListActiveEdicts("testuser", "testproject")
	if err != nil {
		t.Fatalf("ListActiveEdicts before staleness: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("Expected 0 active edicts (sealed), got %d", len(active))
	}

	// Invalidate seals (intent changed)
	if err := svc.InvalidateSeals(edict.Key()); err != nil {
		t.Fatalf("InvalidateSeals: %v", err)
	}

	// Should reappear in active edicts (ruler seal is now stale)
	active, err = svc.ListActiveEdicts("testuser", "testproject")
	if err != nil {
		t.Fatalf("ListActiveEdicts after staleness: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("Expected 1 active edict after staleness, got %d", len(active))
	}
	if active[0].ID != 1 {
		t.Errorf("Expected edict 1, got edict %d", active[0].ID)
	}
}

func TestInvalidateSealsOnlyAffectsTargetEdict(t *testing.T) {
	db := setupSealServiceTestDB(t)
	svc := NewSealService(db)

	edict1 := Edict{ID: 1, Username: "testuser", Project: "testproject", Intent: "Edict 1"}
	edict2 := Edict{ID: 2, Username: "testuser", Project: "testproject", Intent: "Edict 2"}
	for _, e := range []Edict{edict1, edict2} {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("failed to create edict: %v", err)
		}
	}

	// Grant judge seal on both edicts
	for _, e := range []Edict{edict1, edict2} {
		if err := svc.GrantSeal(e.Key(), "judge", JSON{}); err != nil {
			t.Fatalf("failed to grant judge seal: %v", err)
		}
	}

	// Invalidate seals on edict1 only
	if err := svc.InvalidateSeals(edict1.Key()); err != nil {
		t.Fatalf("InvalidateSeals: %v", err)
	}

	// Edict1's seal should be stale
	hasJudge1, err := svc.HasSeal(edict1.Key(), "judge")
	if err != nil {
		t.Fatalf("HasSeal edict1: %v", err)
	}
	if hasJudge1 {
		t.Error("Expected edict1 judge seal to be stale")
	}

	// Edict2's seal should still be valid
	hasJudge2, err := svc.HasSeal(edict2.Key(), "judge")
	if err != nil {
		t.Fatalf("HasSeal edict2: %v", err)
	}
	if !hasJudge2 {
		t.Error("Expected edict2 judge seal to still be valid")
	}
}
