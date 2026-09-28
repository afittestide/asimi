package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/afittestide/asimi/storage"
)

// --- CreateManifestTool ---

// CreateManifestTool records a new file change in the forge manifest.
type CreateManifestTool struct {
	Ctx ToolContext
}

func (t CreateManifestTool) Name() string { return "create_manifest" }

func (t CreateManifestTool) Description() string {
	return "Records a new file change in the forge manifest. Input: JSON with 'edict_id', 'ling_id', 'file_path', 'func_name' (optional), and 'content_sha'."
}

func (t CreateManifestTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		EdictID    uint   `json:"edict_id"`
		LingID     string `json:"ling_id"`
		FilePath   string `json:"file_path"`
		FuncName   string `json:"func_name"`
		ContentSHA string `json:"content_sha"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.EdictID == 0 || params.FilePath == "" {
		return "", fmt.Errorf("edict_id and file_path are required")
	}

	manifestID := GenerateID("manifest", fmt.Sprintf("%d", params.EdictID), params.LingID, params.FilePath,
		fmt.Sprintf("%d", time.Now().UnixNano()))

	manifest := storage.ForgeManifest{
		ManifestID: manifestID,
		EdictID:    params.EdictID,
		Username:   t.Ctx.Username,
		Project:    t.Ctx.Project,
		LingID:     params.LingID,
		FilePath:   params.FilePath,
		FuncName:   params.FuncName,
		ContentSHA: params.ContentSHA,
		Status:     storage.ManifestForged,
	}

	if err := t.Ctx.DB.Create(&manifest).Error; err != nil {
		return "", fmt.Errorf("failed to stage manifest: %w", err)
	}
	return fmt.Sprintf("Created manifest %s for file %s (ling: %s)", manifestID, params.FilePath, params.LingID), nil
}

func (t CreateManifestTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edict_id":    map[string]any{"type": "integer", "description": "The edict ID this manifest belongs to"},
			"ling_id":     map[string]any{"type": "string", "description": "The ling ID this manifest implements"},
			"file_path":   map[string]any{"type": "string", "description": "Path to the modified file"},
			"func_name":   map[string]any{"type": "string", "description": "Name of the function being modified (optional)"},
			"content_sha": map[string]any{"type": "string", "description": "SHA of the file content"},
		},
		"required": []string{"edict_id", "file_path"},
	}
}

func (t CreateManifestTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("Create Manifest: Error: %v\n", err)
	}
	return fmt.Sprintf("Create Manifest: %s\n", result)
}

// --- RecordVerdictTool ---

// RecordVerdictTool records test results for an edict or specific Ling.
type RecordVerdictTool struct {
	Ctx ToolContext
}

func (t RecordVerdictTool) Name() string { return "record_verdict" }

func (t RecordVerdictTool) Description() string {
	return "Records test results for an edict or specific Ling. Input: JSON with 'edict_id', 'ling_id' (optional), 'passed' (boolean), and 'details' (optional)."
}

func (t RecordVerdictTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		EdictID uint   `json:"edict_id"`
		LingID  string `json:"ling_id"`
		Passed  bool   `json:"passed"`
		Details string `json:"details"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.EdictID == 0 {
		return "", fmt.Errorf("edict_id is required")
	}

	key := storage.EdictKey{ID: params.EdictID, Username: t.Ctx.Username, Project: t.Ctx.Project}

	// Get pending manifests
	var manifests []storage.ForgeManifest
	if err := t.Ctx.DB.Where("edict_id = ? AND username = ? AND project = ? AND status = ?", key.ID, key.Username, key.Project, storage.ManifestForged).
		Order("created_at ASC").
		Find(&manifests).Error; err != nil {
		return "", fmt.Errorf("failed to get pending manifests: %w", err)
	}

	outcome := storage.VerdictPassed
	if !params.Passed {
		outcome = storage.VerdictFailed
	}

	recordedCount := 0
	for _, m := range manifests {
		evidence := storage.JSON{"details": params.Details}
		if params.LingID != "" && m.LingID != params.LingID {
			continue
		}

		verdictID := GenerateID("verdict", m.ManifestID, "test")
		verdict := storage.JudgeVerdict{
			VerdictID:  verdictID,
			ManifestID: m.ManifestID,
			Username:   key.Username,
			Project:    key.Project,
			TestSuite:  "test",
			Outcome:    outcome,
			Evidence:   evidence,
		}
		if err := t.Ctx.DB.Create(&verdict).Error; err != nil {
			return "", fmt.Errorf("failed to record verdict: %w", err)
		}

		status := storage.ManifestQuenched
		if !params.Passed {
			status = storage.ManifestRejected
		}
		result := t.Ctx.DB.Model(&storage.ForgeManifest{}).
			Where("manifest_id = ? AND username = ? AND project = ?", m.ManifestID, key.Username, key.Project).
			Updates(map[string]interface{}{
				"status":     status,
				"verdict_id": verdictID,
			})
		if result.Error != nil {
			return "", fmt.Errorf("failed to update manifest status: %w", result.Error)
		}
		recordedCount++
	}

	// If no manifests, record verdict for edict directly (for rituals like project-init)
	if recordedCount == 0 {
		verdictID := GenerateID("verdict", fmt.Sprintf("%d", params.EdictID), "edict", fmt.Sprintf("%d", time.Now().UnixNano()))
		verdict := storage.JudgeVerdict{
			VerdictID:  verdictID,
			ManifestID: "",
			EdictID:    key.ID,
			Username:   key.Username,
			Project:    key.Project,
			TestSuite:  "edict",
			Outcome:    outcome,
			Evidence:   storage.JSON{"details": params.Details},
		}
		if err := t.Ctx.DB.Create(&verdict).Error; err != nil {
			return "", fmt.Errorf("failed to record edict verdict: %w", err)
		}
		recordedCount++
	}

	sealed := t.sealIfComplete(key)
	return fmt.Sprintf("Recorded verdict (passed=%v) for edict %d (sealed=%v)", params.Passed, params.EdictID, sealed), nil
}

// sealIfComplete checks whether all manifests for the edict are quenched
// and, if so, grants the Judge's seal. Returns true when sealed.
func (t RecordVerdictTool) sealIfComplete(key storage.EdictKey) bool {
	var pendingCount int64
	err := t.Ctx.DB.Raw(`
		SELECT COUNT(*) FROM forge_manifests fm
		WHERE fm.edict_id = ? AND fm.username = ? AND fm.project = ?
		  AND fm.status != ?
		  AND fm.created_at = (
		    SELECT MAX(fm2.created_at) FROM forge_manifests fm2
		    WHERE fm2.edict_id = fm.edict_id
		      AND fm2.username = fm.username
		      AND fm2.project = fm.project
		      AND fm2.file_path = fm.file_path
		  )`,
		key.ID, key.Username, key.Project, storage.ManifestQuenched).
		Scan(&pendingCount).Error
	if err != nil {
		return false
	}

	var totalCount int64
	err = t.Ctx.DB.Raw(`
		SELECT COUNT(*) FROM forge_manifests fm
		WHERE fm.edict_id = ? AND fm.username = ? AND fm.project = ?
		  AND fm.created_at = (
		    SELECT MAX(fm2.created_at) FROM forge_manifests fm2
		    WHERE fm2.edict_id = fm.edict_id
		      AND fm2.username = fm.username
		      AND fm2.project = fm.project
		      AND fm2.file_path = fm.file_path
		  )`,
		key.ID, key.Username, key.Project).
		Scan(&totalCount).Error
	if err != nil {
		return false
	}

	if totalCount > 0 {
		if pendingCount > 0 {
			return false
		}
	} else {
		// No manifests: check the outcome of the latest edict-level verdict
		var latestVerdict storage.JudgeVerdict
		err = t.Ctx.DB.Where("manifest_id = '' AND test_suite = 'edict' AND username = ? AND project = ?", key.Username, key.Project).
			Order("created_at DESC").
			First(&latestVerdict).Error
		if err != nil {
			return false
		}
		if latestVerdict.Outcome != storage.VerdictPassed {
			return false
		}
	}

	// All quenched — grant judge seal. GrantSeal is idempotent and
	// resurrects tombstones rather than colliding with their IDs (e882).
	// The verdict tool is the judge's; tolerate contexts where the
	// minister id was never wired (tests, direct invocation).
	ministerID := t.Ctx.MinisterID
	if ministerID == "" {
		ministerID = "judge"
	}
	if err := storage.NewSealService(t.Ctx.DB).GrantSeal(key, ministerID, storage.JSON{"type": "judgment_complete"}); err != nil {
		// Surface the failure instead of silently returning false, which
		// previously masked both the e882 collision and empty-minister bugs.
		log.Printf("ERROR sealIfComplete: failed to grant %s seal for edict %d: %v",
			ministerID, key.ID, err)
		return false
	}
	return true
}

func (t RecordVerdictTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edict_id": map[string]any{"type": "integer", "description": "The edict ID"},
			"ling_id":  map[string]any{"type": "string", "description": "Optional Ling ID to record verdict for"},
			"passed":   map[string]any{"type": "boolean", "description": "Whether the tests passed"},
			"details":  map[string]any{"type": "string", "description": "Additional details about the verdict"},
		},
		"required": []string{"edict_id", "passed"},
	}
}

func (t RecordVerdictTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("Record Verdict: Error: %v\n", err)
	}
	return fmt.Sprintf("Record Verdict: %s\n", result)
}

// --- ListPendingManifestsTool ---

// ListPendingManifestsTool lists manifests awaiting judgment.
type ListPendingManifestsTool struct {
	Ctx ToolContext
}

func (t ListPendingManifestsTool) Name() string { return "list_pending_manifests" }

func (t ListPendingManifestsTool) Description() string {
	return "Lists manifests awaiting CI judgment. Input: JSON with 'edict_id'."
}

func (t ListPendingManifestsTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		EdictID uint `json:"edict_id"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.EdictID == 0 {
		return "", fmt.Errorf("edict_id is required")
	}

	var manifests []storage.ForgeManifest
	if err := t.Ctx.DB.Where("edict_id = ? AND username = ? AND project = ? AND status = ?", params.EdictID, t.Ctx.Username, t.Ctx.Project, storage.ManifestForged).
		Order("created_at ASC").
		Find(&manifests).Error; err != nil {
		return "", fmt.Errorf("failed to get pending manifests: %w", err)
	}

	if len(manifests) == 0 {
		return "No pending manifests found", nil
	}

	result, err := json.MarshalIndent(manifests, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format manifests: %w", err)
	}
	return string(result), nil
}

func (t ListPendingManifestsTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edict_id": map[string]any{"type": "integer", "description": "The edict ID to list manifests for"},
		},
		"required": []string{"edict_id"},
	}
}

func (t ListPendingManifestsTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("List Pending Manifests: Error: %v\n", err)
	}
	var manifests []json.RawMessage
	if json.Unmarshal([]byte(result), &manifests) == nil {
		return fmt.Sprintf("Listed %d pending manifests\n", len(manifests))
	}
	return result + "\n"
}

// --- UpdateManifestStatusTool ---

// UpdateManifestStatusTool updates a manifest's status after judgment.
type UpdateManifestStatusTool struct {
	Ctx ToolContext
}

func (t UpdateManifestStatusTool) Name() string { return "update_manifest_status" }

func (t UpdateManifestStatusTool) Description() string {
	return "Updates a manifest's status to 'quenched' (passed) or 'rejected' (failed). Input: JSON with 'manifest_id', 'status', and 'verdict_id'."
}

func (t UpdateManifestStatusTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		ManifestID string `json:"manifest_id"`
		Status     string `json:"status"`
		VerdictID  string `json:"verdict_id"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.ManifestID == "" || params.Status == "" {
		return "", fmt.Errorf("manifest_id and status are required")
	}

	statusMap := map[string]storage.ManifestStatus{
		"quenched": storage.ManifestQuenched,
		"rejected": storage.ManifestRejected,
	}
	status, ok := statusMap[params.Status]
	if !ok {
		return "", fmt.Errorf("invalid status: %s (use 'quenched' or 'rejected')", params.Status)
	}

	result := t.Ctx.DB.Model(&storage.ForgeManifest{}).
		Where("manifest_id = ? AND username = ? AND project = ?", params.ManifestID, t.Ctx.Username, t.Ctx.Project).
		Updates(map[string]interface{}{
			"status":     status,
			"verdict_id": params.VerdictID,
		})
	if result.Error != nil {
		return "", fmt.Errorf("failed to update manifest status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return "", fmt.Errorf("manifest not found: %s (username=%s, project=%s)", params.ManifestID, t.Ctx.Username, t.Ctx.Project)
	}

	return fmt.Sprintf("Updated manifest %s status to %s", params.ManifestID, params.Status), nil
}

func (t UpdateManifestStatusTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"manifest_id": map[string]any{"type": "string", "description": "The manifest ID to update"},
			"status":      map[string]any{"type": "string", "description": "New status: 'quenched' or 'rejected'"},
			"verdict_id":  map[string]any{"type": "string", "description": "The verdict ID that determined this status"},
		},
		"required": []string{"manifest_id", "status"},
	}
}

func (t UpdateManifestStatusTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("Update Manifest Status: Error: %v\n", err)
	}
	return fmt.Sprintf("Update Manifest Status: %s\n", result)
}

// --- GetManifestByCommitTool ---

// GetManifestByCommitTool finds a manifest by its commit hash.
type GetManifestByCommitTool struct {
	Ctx ToolContext
}

func (t GetManifestByCommitTool) Name() string { return "get_manifest_by_commit" }

func (t GetManifestByCommitTool) Description() string {
	return "Finds the manifest associated with a commit hash. Input: JSON with 'commit_hash'."
}

func (t GetManifestByCommitTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		CommitHash string `json:"commit_hash"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.CommitHash == "" {
		return "", fmt.Errorf("commit_hash is required")
	}

	var manifest storage.ForgeManifest
	if err := t.Ctx.DB.Where("commit_hash = ? AND username = ? AND project = ?", params.CommitHash, t.Ctx.Username, t.Ctx.Project).
		First(&manifest).Error; err != nil {
		return "", fmt.Errorf("manifest not found for commit: %s", params.CommitHash)
	}

	result, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format manifest: %w", err)
	}
	return string(result), nil
}

func (t GetManifestByCommitTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"commit_hash": map[string]any{"type": "string", "description": "The git commit hash to look up"},
		},
		"required": []string{"commit_hash"},
	}
}

func (t GetManifestByCommitTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("Get Manifest By Commit: Error: %v\n", err)
	}
	return fmt.Sprintf("Get Manifest By Commit: %s\n", result)
}
