package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/afittestide/asimi/storage"
	"gorm.io/gorm"
)

// RecordPrecedentTool records the Sage's ethics review outcome for every
// quenched manifest on an edict, granting or withholding the Sage's seal.
type RecordPrecedentTool struct {
	Ctx ToolContext
}

func (t RecordPrecedentTool) Name() string { return "record_precedent" }

func (t RecordPrecedentTool) Description() string {
	// TODO: make `principal` non optional and provide the list of principals
	return "Records an ethics review with reasoning. Input: JSON with 'edict_id', 'approved' (boolean), 'reasoning', and optional 'principle' (defaults to 'ethics_review')."
}

func (t RecordPrecedentTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		EdictID   uint   `json:"edict_id"`
		Approved  bool   `json:"approved"`
		Reasoning string `json:"reasoning"`
		Principle string `json:"principle"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.EdictID == 0 || params.Reasoning == "" {
		return "", fmt.Errorf("edict_id and reasoning are required")
	}
	if params.Principle == "" {
		params.Principle = "ethics_review"
	}

	key := storage.EdictKey{ID: params.EdictID, Username: t.Ctx.Username, Project: t.Ctx.Project}

	var manifests []storage.ForgeManifest
	if err := t.Ctx.DB.Where("edict_id = ? AND username = ? AND project = ? AND status = ?", key.ID, key.Username, key.Project, storage.ManifestQuenched).
		Order("created_at ASC").
		Find(&manifests).Error; err != nil {
		return "", fmt.Errorf("failed to get quenched manifests: %w", err)
	}

	ruling := storage.PrecedentApproved
	if !params.Approved {
		ruling = storage.PrecedentRejected
	}

	if len(manifests) == 0 {
		// No manifests: record edict-level precedent (like edict-level verdicts)
		precedentID := GenerateID("precedent", fmt.Sprintf("%d", key.ID), params.Principle, fmt.Sprintf("%d", time.Now().UnixNano()))
		precedent := storage.CensorPrecedent{
			PrecedentID:   precedentID,
			ManifestID:    "", // empty = edict-level
			EdictID:       key.ID,
			Username:      key.Username,
			Project:       key.Project,
			Principle:     params.Principle,
			Ruling:        ruling,
			Justification: params.Reasoning,
		}
		if err := t.Ctx.DB.Create(&precedent).Error; err != nil {
			return "", fmt.Errorf("failed to log edict-level precedent: %w", err)
		}
	} else {
		for _, m := range manifests {
			precedentID := GenerateID("precedent", m.ManifestID, params.Principle, fmt.Sprintf("%d", time.Now().UnixNano()))
			precedent := storage.CensorPrecedent{
				PrecedentID:   precedentID,
				ManifestID:    m.ManifestID,
				EdictID:       key.ID,
				Principle:     params.Principle,
				Ruling:        ruling,
				Justification: params.Reasoning,
			}
			if err := t.Ctx.DB.Create(&precedent).Error; err != nil {
				return "", fmt.Errorf("failed to log precedent: %w", err)
			}
			if !params.Approved {
				result := t.Ctx.DB.Model(&storage.ForgeManifest{}).
					Where("manifest_id = ? AND username = ? AND project = ?", m.ManifestID, key.Username, key.Project).
					Update("status", storage.ManifestRejected)
				if result.Error != nil {
					return "", fmt.Errorf("failed to reject manifest: %w", result.Error)
				}
			}
		}
	}

	if params.Approved {
		if err := grantChancellorSeal(t.Ctx.DB, key, "chancellor", storage.JSON{"reason": params.Reasoning}); err != nil {
			// The precedent rows above are already committed; say so
			// explicitly instead of implying total failure (e882).
			return "", fmt.Errorf("precedent recorded but failed to grant seal: %w", err)
		}
	}

	status := "approved"
	if !params.Approved {
		status = "rejected"
	}
	return fmt.Sprintf("Recorded precedent (%s) for edict %d", status, key.ID), nil
}

func (t RecordPrecedentTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edict_id":  map[string]any{"type": "integer", "description": "The edict ID"},
			"approved":  map[string]any{"type": "boolean", "description": "Whether the code is approved"},
			"reasoning": map[string]any{"type": "string", "description": "The reasoning for the decision"},
			"principle": map[string]any{"type": "string", "description": "The principle being reviewed (optional, defaults to 'ethics_review')"},
		},
		"required": []string{"edict_id", "approved", "reasoning"},
	}
}

func (t RecordPrecedentTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("Record Precedent: Error: %v\n", err)
	}
	return fmt.Sprintf("Record Precedent: %s\n", result)
}

// ListQuenchedManifestsTool lists manifests ready for ethics review.
type ListQuenchedManifestsTool struct {
	Ctx ToolContext
}

func (t ListQuenchedManifestsTool) Name() string { return "list_quenched_manifests" }

func (t ListQuenchedManifestsTool) Description() string {
	return "Lists manifests that passed testing and are ready for ethics review. Input: JSON with 'edict_id'."
}

func (t ListQuenchedManifestsTool) Call(ctx context.Context, input string) (string, error) {
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
	if err := t.Ctx.DB.Where("edict_id = ? AND username = ? AND project = ? AND status = ?", params.EdictID, t.Ctx.Username, t.Ctx.Project, storage.ManifestQuenched).
		Order("created_at ASC").
		Find(&manifests).Error; err != nil {
		return "", fmt.Errorf("failed to get quenched manifests: %w", err)
	}

	if len(manifests) == 0 {
		return "No quenched manifests found", nil
	}

	result, err := json.MarshalIndent(manifests, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format manifests: %w", err)
	}
	return string(result), nil
}

func (t ListQuenchedManifestsTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edict_id": map[string]any{"type": "integer", "description": "The edict ID to list manifests for"},
		},
		"required": []string{"edict_id"},
	}
}

func (t ListQuenchedManifestsTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("List Quenched Manifests: Error: %v\n", err)
	}
	return fmt.Sprintf("List Quenched Manifests: %s\n", result)
}

// QueryPrecedentsTool searches precedents by principle.
type QueryPrecedentsTool struct {
	Ctx ToolContext
}

func (t QueryPrecedentsTool) Name() string { return "query_precedents" }

func (t QueryPrecedentsTool) Description() string {
	return "Searches precedents by principle for case law lookup. Input: JSON with 'principle', and optional 'ruling' ('approved'/'rejected', empty = both) and 'limit'."
}

func (t QueryPrecedentsTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		Principle string `json:"principle"`
		Ruling    string `json:"ruling"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.Principle == "" {
		return "", fmt.Errorf("principle is required")
	}
	if params.Limit == 0 {
		params.Limit = 10
	}

	// LEFT JOIN so edict-level (manifest_id = '') and legacy rows with no
	// backing forge_manifests row stay visible. Ownership falls back to the
	// precedent's own username/project when the manifest is absent.
	var precedents []storage.CensorPrecedent
	query := t.Ctx.DB.
		Joins("LEFT JOIN forge_manifests ON forge_manifests.manifest_id = censor_precedents.manifest_id").
		Where("censor_precedents.principle LIKE ?", "%"+params.Principle+"%").
		Where("COALESCE(NULLIF(forge_manifests.username, ''), censor_precedents.username) = ?", t.Ctx.Username).
		Where("COALESCE(NULLIF(forge_manifests.project, ''), censor_precedents.project) = ?", t.Ctx.Project).
		Order("censor_precedents.created_at DESC").
		Limit(params.Limit)
	if params.Ruling != "" {
		query = query.Where("censor_precedents.ruling = ?", params.Ruling)
	}
	if err := query.Find(&precedents).Error; err != nil {
		return "", fmt.Errorf("failed to query precedents: %w", err)
	}

	if len(precedents) == 0 {
		return "No precedents found for this principle", nil
	}

	result, err := json.MarshalIndent(precedents, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format precedents: %w", err)
	}
	return string(result), nil
}

func (t QueryPrecedentsTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"principle": map[string]any{"type": "string", "description": "The principle to search for"},
			"ruling":    map[string]any{"type": "string", "description": "Filter by ruling: 'approved' or 'rejected' (optional, empty = both)"},
			"limit":     map[string]any{"type": "integer", "description": "Maximum number of results (default 10)"},
		},
		"required": []string{"principle"},
	}
}

func (t QueryPrecedentsTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("Query Precedents: Error: %v\n", err)
	}
	return fmt.Sprintf("Query Precedents: %s\n", result)
}

// grantChancellorSeal records the Chancellor's seal on an edict (idempotent).
// Converges on SealService.GrantSeal, which resurrects tombstones left
// by InvalidateSeals instead of colliding with their IDs (e882).
func grantChancellorSeal(db *gorm.DB, key storage.EdictKey, ministerID string, metadata storage.JSON) error {
	return storage.NewSealService(db).GrantSeal(key, ministerID, metadata)
}
