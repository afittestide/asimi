package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/afittestide/asimi/storage"
)

// --- InsertLingTool ---

// InsertLingTool creates a new ling (task order) for an edict.
type InsertLingTool struct {
	Ctx     ToolContext
	Igniter LingIgniter
}

func (t InsertLingTool) Name() string { return "insert_ling" }

func (t InsertLingTool) Description() string {
	return "Creates a new ling (task order) for an edict. Lings are pending tasks; when a batch of insert_ling calls completes, ready lings run automatically, honouring dependencies. The input should be a JSON object with 'edict_id', 'description', optionally 'minister' (default 'forge'), and optionally 'dependencies' (array of FULL ling IDs, e.g. '74183c66ba0507ba', that must complete first — never use shorthand aliases like '470-1')."
}

func (t InsertLingTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		EdictID      uint     `json:"edict_id"`
		Description  string   `json:"description"`
		Minister     string   `json:"minister,omitempty"`
		Dependencies []string `json:"dependencies,omitempty"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.EdictID == 0 {
		return "", fmt.Errorf("edict_id is required")
	}
	if params.Description == "" {
		return "", fmt.Errorf("description is required")
	}
	if params.Minister == "" {
		params.Minister = "forge"
	}

	lingID := GenerateID("ling", fmt.Sprintf("%d", params.EdictID), t.Ctx.Username, t.Ctx.Project,
		params.Description, time.Now().String(), fmt.Sprintf("%d", rand.Int63()))

	ling := &storage.Ling{
		LingID:       lingID,
		EdictID:      params.EdictID,
		Username:     t.Ctx.Username,
		Project:      t.Ctx.Project,
		Description:  params.Description,
		Minister:     params.Minister,
		Dependencies: storage.StringArray(params.Dependencies),
		Status:       storage.LingPending,
	}

	if err := t.Ctx.DB.Create(ling).Error; err != nil {
		return "", fmt.Errorf("failed to insert ling: %w", err)
	}

	// Validate dependencies form a DAG (no cycles)
	if len(params.Dependencies) > 0 {
		var allLings []storage.Ling
		if err := t.Ctx.DB.Where("edict_id = ? AND username = ? AND project = ?", params.EdictID, t.Ctx.Username, t.Ctx.Project).
			Find(&allLings).Error; err == nil {
			if err := validateDependencies(allLings); err != nil {
				return "", fmt.Errorf("invalid dependencies: %w", err)
			}
		}
	}

	// Observability only — publishing never runs a session.
	key := storage.EdictKey{ID: params.EdictID, Username: t.Ctx.Username, Project: t.Ctx.Project}
	if t.Igniter != nil {
		t.Igniter.PublishLingCreated(key, lingID)
		// Batch boundary: fire the automatic ignition trigger. It returns
		// immediately and runs ready lings in the background.
		t.Igniter.TriggerLingIgnition(key)
	}

	return fmt.Sprintf("Created ling %s for edict %d (minister %s)", lingID, params.EdictID, params.Minister), nil
}

func (t InsertLingTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edict_id": map[string]any{
				"type":        "integer",
				"description": "The edict ID this ling belongs to",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Clear, atomic description of the task",
			},
			"minister": map[string]any{
				"type":        "string",
				"description": "Minister that executes this ling (e.g. 'forge', 'judge'). Defaults to 'forge' when omitted.",
			},
			"dependencies": map[string]any{
				"type":        "array",
				"description": "Array of FULL ling IDs (e.g. '74183c66ba0507ba') that must complete before this one. Use the exact ling_id returned by insert_ling — never invent shorthand aliases like '470-1'.",
				"items":       map[string]any{"type": "string"},
			},
		},
		"required": []string{"edict_id", "description"},
	}
}

func (t InsertLingTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("Insert Ling: Error: %v\n", err)
	}
	return fmt.Sprintf("Insert Ling: %s\n", result)
}

// validateDependencies ensures ling form a DAG (no cycles)
func validateDependencies(lingList []storage.Ling) error {
	// Build adjacency map
	deps := make(map[string][]string)
	for _, ling := range lingList {
		deps[ling.LingID] = ling.Dependencies
	}

	// Check for cycles using DFS
	visited := make(map[string]bool)
	inStack := make(map[string]bool)

	var hasCycle func(id string) bool
	hasCycle = func(id string) bool {
		visited[id] = true
		inStack[id] = true

		for _, dep := range deps[id] {
			if !visited[dep] {
				if hasCycle(dep) {
					return true
				}
			} else if inStack[dep] {
				return true
			}
		}

		inStack[id] = false
		return false
	}

	for _, ling := range lingList {
		if !visited[ling.LingID] {
			if hasCycle(ling.LingID) {
				return fmt.Errorf("circular dependency detected involving ling %s", ling.LingID)
			}
		}
	}

	return nil
}

// --- ListLingTool ---

// ListLingTool lists all ling for an edict.
type ListLingTool struct {
	Ctx ToolContext
}

func (t ListLingTool) Name() string { return "list_ling" }

func (t ListLingTool) Description() string {
	return "Lists all ling (task orders) for an edict. The input should be a JSON object with 'edict_id'."
}

func (t ListLingTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		EdictID uint `json:"edict_id"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.EdictID == 0 {
		return "", fmt.Errorf("edict_id is required")
	}

	var lingList []storage.Ling
	if err := t.Ctx.DB.Where("edict_id = ? AND username = ? AND project = ?", params.EdictID, t.Ctx.Username, t.Ctx.Project).
		Order("created_at ASC").
		Find(&lingList).Error; err != nil {
		return "", fmt.Errorf("failed to list lings: %w", err)
	}

	if len(lingList) == 0 {
		return "No ling found for this edict", nil
	}

	result, err := json.MarshalIndent(lingList, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format ling list: %w", err)
	}
	return string(result), nil
}

func (t ListLingTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edict_id": map[string]any{
				"type":        "integer",
				"description": "The edict ID to list ling for",
			},
		},
		"required": []string{"edict_id"},
	}
}

func (t ListLingTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("List Ling: Error: %v\n", err)
	}
	return fmt.Sprintf("List Ling: %s\n", result)
}

// --- UpdateLingStatusTool ---

// UpdateLingStatusTool updates the status of a ling.
type UpdateLingStatusTool struct {
	Ctx ToolContext
}

func (t UpdateLingStatusTool) Name() string { return "update_ling_status" }

func (t UpdateLingStatusTool) Description() string {
	return "Updates the status of a ling. Valid statuses: pending, in_progress, completed, blocked. The input should be a JSON object with 'ling_id' and 'status'."
}

func (t UpdateLingStatusTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		LingID string `json:"ling_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.LingID == "" {
		return "", fmt.Errorf("ling_id is required")
	}
	if params.Status == "" {
		return "", fmt.Errorf("status is required")
	}

	validStatuses := map[string]storage.LingStatus{
		"pending":     storage.LingPending,
		"in_progress": storage.LingInProgress,
		"completed":   storage.LingDone,
		"blocked":     storage.LingBlocked,
	}
	newStatus, ok := validStatuses[params.Status]
	if !ok {
		return "", fmt.Errorf("invalid status: %s (valid: pending, in_progress, completed, blocked)", params.Status)
	}

	result := t.Ctx.DB.Model(&storage.Ling{}).
		Where("ling_id = ?", params.LingID).
		Update("status", newStatus)
	if result.Error != nil {
		return "", fmt.Errorf("failed to update ling status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return "", fmt.Errorf("ling not found: %s", params.LingID)
	}

	return fmt.Sprintf("Updated ling %s status to %s", params.LingID, params.Status), nil
}

func (t UpdateLingStatusTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ling_id": map[string]any{
				"type":        "string",
				"description": "The ling ID to update",
			},
			"status": map[string]any{
				"type":        "string",
				"description": "New status: pending, in_progress, completed, or blocked",
				"enum":        []string{"pending", "in_progress", "completed", "blocked"},
			},
		},
		"required": []string{"ling_id", "status"},
	}
}

func (t UpdateLingStatusTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("Update Ling Status: Error: %v\n", err)
	}
	return fmt.Sprintf("Update Ling Status: %s\n", result)
}
