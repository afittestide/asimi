package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/afittestide/asimi/storage"
)

// --- CreateIncidentTool ---

// CreateIncidentTool creates a new production incident.
type CreateIncidentTool struct {
	Ctx ToolContext
}

func (t CreateIncidentTool) Name() string { return "create_incident" }

func (t CreateIncidentTool) Description() string {
	return "Creates a new production incident. Input: JSON with 'description', 'severity', and optional 'edict_id'."
}

func (t CreateIncidentTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		Description string `json:"description"`
		Severity    string `json:"severity"`
		EdictID     uint   `json:"edict_id"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.Description == "" || params.Severity == "" {
		return "", fmt.Errorf("description and severity are required")
	}

	incidentID := GenerateID("incident", params.Severity, params.Description, time.Now().String())

	incident := storage.Incident{
		IncidentID:  incidentID,
		Description: params.Description,
		Severity:    params.Severity,
		Status:      "open",
		EdictID:     params.EdictID,
		Username:    t.Ctx.Username,
		Project:     t.Ctx.Project,
	}

	if err := t.Ctx.DB.Create(&incident).Error; err != nil {
		return "", fmt.Errorf("failed to log incident: %w", err)
	}

	return fmt.Sprintf("Created incident %s (severity: %s)", incidentID, params.Severity), nil
}

func (t CreateIncidentTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{"type": "string", "description": "Description of the incident"},
			"severity":    map[string]any{"type": "string", "description": "Severity level (critical, high, medium, low)"},
			"edict_id":    map[string]any{"type": "integer", "description": "Optional edict ID linked to this incident"},
		},
		"required": []string{"description", "severity"},
	}
}

func (t CreateIncidentTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("Create Incident: Error: %v\n", err)
	}
	return fmt.Sprintf("Create Incident: %s\n", result)
}

// --- ResolveIncidentTool ---

// ResolveIncidentTool marks an incident as resolved.
type ResolveIncidentTool struct {
	Ctx ToolContext
}

func (t ResolveIncidentTool) Name() string { return "resolve_incident" }

func (t ResolveIncidentTool) Description() string {
	return "Marks an incident as resolved with details. Input: JSON with 'incident_id', 'resolution', and optional 'root_cause'."
}

func (t ResolveIncidentTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		IncidentID string `json:"incident_id"`
		Resolution string `json:"resolution"`
		RootCause  string `json:"root_cause"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.IncidentID == "" || params.Resolution == "" {
		return "", fmt.Errorf("incident_id and resolution are required")
	}

	result := t.Ctx.DB.Model(&storage.Incident{}).
		Where("incident_id = ? AND username = ? AND project = ?", params.IncidentID, t.Ctx.Username, t.Ctx.Project).
		Updates(map[string]interface{}{
			"status":     "resolved",
			"resolution": params.Resolution,
			"root_cause": params.RootCause,
		})
	if result.Error != nil {
		return "", fmt.Errorf("failed to resolve incident: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return "", fmt.Errorf("incident not found: %s", params.IncidentID)
	}

	return fmt.Sprintf("Resolved incident %s", params.IncidentID), nil
}

func (t ResolveIncidentTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"incident_id": map[string]any{"type": "string", "description": "The incident ID to resolve"},
			"resolution":  map[string]any{"type": "string", "description": "Description of how the incident was resolved"},
			"root_cause":  map[string]any{"type": "string", "description": "Root cause analysis (optional)"},
		},
		"required": []string{"incident_id", "resolution"},
	}
}

func (t ResolveIncidentTool) Format(input, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("Resolve Incident: Error: %v\n", err)
	}
	return fmt.Sprintf("Resolve Incident: %s\n", result)
}

// --- GetIncidentTool ---

// GetIncidentTool retrieves an incident by ID.
type GetIncidentTool struct {
	Ctx ToolContext
}

func (t GetIncidentTool) Name() string { return "get_incident" }

func (t GetIncidentTool) Description() string {
	return "Gets details of an incident by ID. Input: JSON with 'incident_id'."
}

func (t GetIncidentTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		IncidentID string `json:"incident_id"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.IncidentID == "" {
		return "", fmt.Errorf("incident_id is required")
	}

	var incident storage.Incident
	if err := t.Ctx.DB.Where("incident_id = ? AND username = ? AND project = ?", params.IncidentID, t.Ctx.Username, t.Ctx.Project).
		First(&incident).Error; err != nil {
		return "", fmt.Errorf("incident not found: %s", params.IncidentID)
	}

	env, err := newMarkdownEnvelope(renderIncidentMarkdown(incident), incident)
	if err != nil {
		return "", fmt.Errorf("failed to format incident: %w", err)
	}
	return env, nil
}

// renderIncidentMarkdown renders an incident as a markdown block: short scope
// fields in a bullet list, free text as its own verbatim paragraph. Username
// and project are query scope, not incident data, so they never appear.
func renderIncidentMarkdown(incident storage.Incident) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Incident `%s`** — severity: `%s`, status: `%s`\n\n", incident.IncidentID, incident.Severity, incident.Status)

	if incident.EdictID != 0 {
		fmt.Fprintf(&b, "- **edict**: %d\n", incident.EdictID)
	}
	if incident.CommitHash != "" {
		fmt.Fprintf(&b, "- **commit**: %s\n", incident.CommitHash)
	}
	fmt.Fprintf(&b, "- **created**: %s\n", incident.CreatedAt.Format("2006-01-02 15:04"))

	b.WriteString("\n**Description**\n")
	b.WriteString(incident.Description)
	b.WriteString("\n")

	if incident.RootCause != "" {
		b.WriteString("\n**Root cause**\n")
		b.WriteString(incident.RootCause)
		b.WriteString("\n")
	}
	if incident.Resolution != "" {
		b.WriteString("\n**Resolution**\n")
		b.WriteString(incident.Resolution)
		b.WriteString("\n")
	}
	return b.String()
}

func (t GetIncidentTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"incident_id": map[string]any{"type": "string", "description": "The incident ID to retrieve"},
		},
		"required": []string{"incident_id"},
	}
}

func (t GetIncidentTool) Format(input, result string, err error) string {
	var params struct {
		IncidentID string `json:"incident_id"`
	}
	json.Unmarshal([]byte(input), &params)

	if err != nil {
		return fmt.Sprintf("Get Incident: Error: %v\n", err)
	}
	if scopeMsg, ok := scopeReportMessage(result); ok {
		return fmt.Sprintf("Get Incident: %s\n", scopeMsg)
	}
	return fmt.Sprintf("Get Incident %s\n", params.IncidentID)
}
