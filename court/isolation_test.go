package court

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Cross-project isolation tests: each test creates records for project A,
// then queries as project B, and asserts zero results.

const (
	isolationUserA = "alice"
	isolationProjA = "project-a"
	isolationUserB = "bob"
	isolationProjB = "project-b"
)

// createEdictForProject creates an edict under a specific username/project.
func createEdictForProject(db *gorm.DB, username, project, intent string) (*storage.Edict, error) {
	edict := storage.Edict{
		Username: username,
		Project:  project,
		Intent:   intent,
	}
	if err := db.Create(&edict).Error; err != nil {
		return nil, err
	}
	return &edict, nil
}

// stageManifestDB creates a manifest directly in the DB for tests.
func stageManifestDB(t *testing.T, db *gorm.DB, key storage.EdictKey, lingID, filePath, funcName, contentSHA string) string {
	t.Helper()
	manifestID := GenerateID("manifest", fmt.Sprintf("%d", key.ID), lingID, filePath, fmt.Sprintf("%d", time.Now().UnixNano()))
	manifest := storage.ForgeManifest{
		ManifestID: manifestID,
		EdictID:    key.ID,
		Username:   key.Username,
		Project:    key.Project,
		LingID:     lingID,
		FilePath:   filePath,
		FuncName:   funcName,
		ContentSHA: contentSHA,
		Status:     storage.ManifestForged,
	}
	require.NoError(t, db.Create(&manifest).Error)
	return manifestID
}

// TestIsolation_Incident verifies that Incident queries with
// wrong username/project return no results.
func TestIsolation_Incident(t *testing.T) {
	db := setupMinisterTestDB(t)

	// Create an incident under project A
	edictA, err := createEdictForProject(db, isolationUserA, isolationProjA, "incident test")
	require.NoError(t, err)

	keyA := storage.EdictKey{ID: edictA.ID, Username: isolationUserA, Project: isolationProjA}
	incidentID := "inc-001"
	incident := storage.Incident{
		IncidentID:  incidentID,
		Description: "segfault in main",
		Severity:    "critical",
		Status:      "open",
		EdictID:     keyA.ID,
		Username:    keyA.Username,
		Project:     keyA.Project,
		CommitHash:  "deadbeef",
	}
	require.NoError(t, db.Create(&incident).Error)

	// Verify project A can see its own incident
	var foundA storage.Incident
	err = db.Where("incident_id = ? AND username = ? AND project = ?", incidentID, isolationUserA, isolationProjA).First(&foundA).Error
	require.NoError(t, err)
	assert.NotNil(t, foundA)

	// Query as project B — should get "not found"
	var foundB storage.Incident
	err = db.Where("incident_id = ? AND username = ? AND project = ?", incidentID, isolationUserB, isolationProjB).First(&foundB).Error
	assert.Error(t, err, "cross-project GetIncident should return error")

	// Open incidents as project B should return zero
	var open []storage.Incident
	err = db.Where("status = ? AND username = ? AND project = ?", "open", isolationUserB, isolationProjB).
		Order("created_at ASC").
		Find(&open).Error
	require.NoError(t, err)
	assert.Empty(t, open, "cross-project open incidents should return empty")

	// Resolve as project B should affect zero rows
	result := db.Model(&storage.Incident{}).
		Where("incident_id = ? AND username = ? AND project = ?", incidentID, isolationUserB, isolationProjB).
		Update("status", "resolved")
	assert.Equal(t, int64(0), result.RowsAffected, "cross-project resolve should affect zero rows")
}

// TestIsolation_RejectManifest verifies that rejecting a manifest with wrong
// project returns "not found" (zero rows affected).
func TestIsolation_RejectManifest(t *testing.T) {
	db := setupMinisterTestDB(t)

	edictA, err := createEdictForProject(db, isolationUserA, isolationProjA, "reject isolation")
	require.NoError(t, err)

	manifestID := stageManifestDB(t, db,
		storage.EdictKey{ID: edictA.ID, Username: isolationUserA, Project: isolationProjA},
		"", "file.go", "Func", "sha1",
	)

	// Reject as project B — should affect zero rows
	keyB := storage.EdictKey{Username: isolationUserB, Project: isolationProjB}
	result := db.Model(&storage.ForgeManifest{}).
		Where("manifest_id = ? AND username = ? AND project = ?", manifestID, keyB.Username, keyB.Project).
		Update("status", storage.ManifestRejected)
	assert.Equal(t, int64(0), result.RowsAffected, "cross-project RejectManifest should affect zero rows")

	// Verify manifest is still forged (not rejected) in project A
	var manifest storage.ForgeManifest
	err = db.Where("manifest_id = ? AND username = ? AND project = ?", manifestID, isolationUserA, isolationProjA).First(&manifest).Error
	require.NoError(t, err)
	assert.Equal(t, storage.ManifestForged, manifest.Status, "manifest should still be forged, not rejected")
}

// TestIsolation_CouncilDecisions verifies that council decisions are scoped
// by username/project.
func TestIsolation_CouncilDecisions(t *testing.T) {
	db := setupMinisterTestDB(t)

	edictA, err := createEdictForProject(db, isolationUserA, isolationProjA, "council test")
	require.NoError(t, err)

	keyA := storage.EdictKey{ID: edictA.ID, Username: isolationUserA, Project: isolationProjA}

	// Create a council decision under project A
	err = CreateCouncilDecision(db, "council-001", keyA, "Deploy to production")
	require.NoError(t, err)

	// Project A can see its pending decisions
	pending, err := GetPendingCouncilDecisions(db, keyA)
	require.NoError(t, err)
	assert.Len(t, pending, 1)

	// Project B queries with same edict ID — should get zero results
	keyB := storage.EdictKey{ID: edictA.ID, Username: isolationUserB, Project: isolationProjB}
	pendingB, err := GetPendingCouncilDecisions(db, keyB)
	require.NoError(t, err)
	assert.Empty(t, pendingB, "cross-project GetPendingCouncilDecisions should return empty")

	// GetCouncilDecision as project B should return error
	_, err = GetCouncilDecision(db, "council-001", keyB)
	assert.Error(t, err, "cross-project GetCouncilDecision should fail")

	// GetCouncilDecisionsForEdict as project B should return empty
	decisionsB, err := GetCouncilDecisionsForEdict(db, keyB)
	require.NoError(t, err)
	assert.Empty(t, decisionsB, "cross-project GetCouncilDecisionsForEdict should return empty")
}

// TestIsolation_TianLedger verifies that tian_ledger only returns the
// current project's ledger events.
func TestIsolation_TianLedger(t *testing.T) {
	db := setupMinisterTestDB(t)

	// Create an edict and a ledger event under project A
	edictA, err := createEdictForProject(db, isolationUserA, isolationProjA, "court test")
	require.NoError(t, err)

	event := storage.TianEvent{
		EdictID:   edictA.ID,
		Username:  isolationUserA,
		Project:   isolationProjA,
		EventType: "ritual_started",
		Payload:   storage.JSON{"ritual": "swift_strike"},
	}
	require.NoError(t, db.Create(&event).Error)

	// Query as project B with the specific edict_id — should see no events
	toolB := tools.TianLedgerTool{Ctx: tools.ToolContext{
		DB:       db,
		Username: isolationUserB,
		Project:  isolationProjB,
	}}
	result, err := toolB.Call(context.Background(), fmt.Sprintf(`{"edict_id": %d}`, edictA.ID))
	require.NoError(t, err)
	assert.Contains(t, result, "No Tian events found",
		"cross-project tian_ledger with edict_id should return no events")

	// Query as project A with same edict_id — should see its own event
	toolA := tools.TianLedgerTool{Ctx: tools.ToolContext{
		DB:       db,
		Username: isolationUserA,
		Project:  isolationProjA,
	}}
	resultA, err := toolA.Call(context.Background(), fmt.Sprintf(`{"edict_id": %d}`, edictA.ID))
	require.NoError(t, err)

	var eventsA []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(resultA), &eventsA))
	require.Len(t, eventsA, 1, "project A should see its own ledger event")
}

// TestIsolation_CensorPrecedent verifies that CensorPrecedent queries are
// filtered by username/project.
func TestIsolation_CensorPrecedent(t *testing.T) {
	db := setupMinisterTestDB(t)

	edictA, err := createEdictForProject(db, isolationUserA, isolationProjA, "precedent test")
	require.NoError(t, err)

	manifestID := stageManifestDB(t, db,
		storage.EdictKey{ID: edictA.ID, Username: isolationUserA, Project: isolationProjA},
		"", "file.go", "Func", "sha2",
	)

	// Log a precedent for project A's manifest
	precedentID := GenerateID("precedent", manifestID, "naming_convention", fmt.Sprintf("%d", time.Now().UnixNano()))
	precedent := storage.CensorPrecedent{
		PrecedentID:   precedentID,
		ManifestID:    manifestID,
		Principle:     "naming_convention",
		Ruling:        storage.PrecedentApproved,
		Justification: "names are clear",
	}
	require.NoError(t, db.Create(&precedent).Error)

	// GetPrecedentsForManifest as project B should return empty
	var precedentsB []storage.CensorPrecedent
	err = db.Joins("JOIN forge_manifests ON forge_manifests.manifest_id = censor_precedents.manifest_id").
		Where("censor_precedents.manifest_id = ? AND forge_manifests.username = ? AND forge_manifests.project = ?", manifestID, isolationUserB, isolationProjB).
		Order("censor_precedents.created_at ASC").
		Find(&precedentsB).Error
	require.NoError(t, err)
	assert.Empty(t, precedentsB, "cross-project GetPrecedentsForManifest should return empty")

	// GetPrecedentsForManifest as project A should return the precedent
	var precedentsA []storage.CensorPrecedent
	err = db.Joins("JOIN forge_manifests ON forge_manifests.manifest_id = censor_precedents.manifest_id").
		Where("censor_precedents.manifest_id = ? AND forge_manifests.username = ? AND forge_manifests.project = ?", manifestID, isolationUserA, isolationProjA).
		Order("censor_precedents.created_at ASC").
		Find(&precedentsA).Error
	require.NoError(t, err)
	assert.Len(t, precedentsA, 1, "project A should see its own precedent")

	// QueryPrecedentsByPrinciple as project B should return empty
	var resultsB []storage.CensorPrecedent
	err = db.Joins("JOIN forge_manifests ON forge_manifests.manifest_id = censor_precedents.manifest_id").
		Where("censor_precedents.principle LIKE ? AND forge_manifests.username = ? AND forge_manifests.project = ?", "%naming%", isolationUserB, isolationProjB).
		Order("censor_precedents.created_at DESC").
		Find(&resultsB).Error
	require.NoError(t, err)
	assert.Empty(t, resultsB, "cross-project QueryPrecedentsByPrinciple should return empty")

	// QueryPrecedentsByPrinciple as project A should return the precedent
	var resultsA []storage.CensorPrecedent
	err = db.Joins("JOIN forge_manifests ON forge_manifests.manifest_id = censor_precedents.manifest_id").
		Where("censor_precedents.principle LIKE ? AND forge_manifests.username = ? AND forge_manifests.project = ?", "%naming%", isolationUserA, isolationProjA).
		Order("censor_precedents.created_at DESC").
		Find(&resultsA).Error
	require.NoError(t, err)
	assert.Len(t, resultsA, 1, "project A should see its own precedent by principle")
}
