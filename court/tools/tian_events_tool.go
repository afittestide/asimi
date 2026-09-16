package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/storage"
)

// tianEventTruncationSuffix marks a tail-truncated last_step_output in
// summary mode. A leading ellipsis (via the ellipsis rune) signals that the
// head of the review report was dropped and the tail — which carries the
// verdict — survives.
const tianEventTruncationSuffix = "…(truncated)"

// TianLedgerTool queries the Tian ledger (tian_events table) for
// recent court events. Read-only; scoped by username and project.
type TianLedgerTool struct {
	Ctx ToolContext
}

// tianEventSummary is the JSON shape returned for each ledger event.
type tianEventSummary struct {
	ID        uint                   `json:"id"`
	EdictID   uint                   `json:"edict_id"`
	EventType string                 `json:"event_type"`
	CreatedAt time.Time              `json:"created_at"`
	Detail    string                 `json:"detail,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
}

// Name returns the tool identifier.
func (t TianLedgerTool) Name() string { return "tian_ledger" }

// Description returns a human-readable explanation of what the tool does.
func (t TianLedgerTool) Description() string {
	return `Query the Tian event ledger for recent court events. Returns events
ordered by most recent first. Optionally filter by edict, event type, and
paginate with limit/offset.

The default detail=summary bounds the heavy last_step_output payload key to
the court-configured length (default 500 chars), keeping the tail and marking
it "…(truncated)". Other payload keys are returned verbatim. Use detail=full
to receive every payload key untruncated.

Pass since_id to return only events newer than that id — a cursor for
incremental polling without re-walking history. Use this for a bird's eye
view of what happened in the Court.`
}

// Call executes the ledger query and returns a JSON snapshot.
func (t TianLedgerTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		EdictID   uint   `json:"edict_id"`
		EventType string `json:"event_type"`
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
		SinceID   uint   `json:"since_id"`
		Detail    string `json:"detail"`
	}
	// Input may be empty; ignore unmarshal errors and fall back to defaults.
	_ = json.Unmarshal([]byte(input), &params) //nolint:errcheck // intentional: empty input is valid

	if t.Ctx.DB == nil {
		return "", fmt.Errorf("database connection not initialized")
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := params.Offset
	if offset < 0 {
		offset = 0
	}

	query := t.Ctx.DB.Where("username = ? AND project = ?", t.Ctx.Username, t.Ctx.Project)
	if params.EdictID != 0 {
		query = query.Where("edict_id = ?", params.EdictID)
	}
	if params.EventType != "" {
		query = query.Where("event_type = ?", params.EventType)
	}
	// id is the auto-increment primary key and monotonic in practice on this
	// ledger, so it is a reliable cursor for "what's new".
	if params.SinceID > 0 {
		query = query.Where("id > ?", params.SinceID)
	}

	var events []storage.TianEvent
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&events).Error; err != nil {
		return "", fmt.Errorf("querying tian events: %w", err)
	}

	if len(events) == 0 {
		return "No Tian events found matching the given filters.", nil
	}

	summaries := make([]tianEventSummary, len(events))
	for i, ev := range events {
		summaries[i] = tianEventSummary{
			ID:        ev.ID,
			EdictID:   ev.EdictID,
			EventType: string(ev.EventType),
			CreatedAt: ev.CreatedAt,
			Detail:    tianEventDetail(ev),
			Payload:   tianEventPayload(ev.Payload, t.Ctx.outputLimit(), params.Detail == "full"),
		}
	}

	resultJSON, _ := json.MarshalIndent(summaries, "", "  ") //nolint:errcheck // summaries are always marshallable
	return string(resultJSON), nil
}

// outputLimit resolves the court-level truncation length, falling back to
// the config default when unset.
func (t ToolContext) outputLimit() int {
	if t.OutputLimit > 0 {
		return t.OutputLimit
	}
	return config.DefaultOutputLimit
}

// tianEventPayload returns the payload object for a single event. Other keys
// are copied verbatim (preserving e842 fidelity); in summary mode only
// last_step_output is tail-truncated to maxChars.
func tianEventPayload(payload storage.JSON, maxChars int, full bool) map[string]interface{} {
	if payload == nil {
		return nil
	}
	out := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		out[k] = v
	}
	if full {
		return out
	}
	if s, ok := out["last_step_output"].(string); ok && len([]rune(s)) > maxChars {
		out["last_step_output"] = truncateTail(s, maxChars)
	}
	return out
}

// truncateTail keeps the last maxChars runes of s, prefixing an ellipsis
// marker since the head was dropped.
func truncateTail(s string, maxChars int) string {
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return "…" + string(runes[len(runes)-maxChars:]) + tianEventTruncationSuffix
}

// Format renders the query result for display in the TUI.
func (t TianLedgerTool) Format(input, result string, err error) string {
	msg := utils.NewMsgBlockBuilder("Heaven Ledger")
	msg.WriteLn()
	if err != nil {
		msg.Writef("Error: %v", err)
	} else {
		var events []tianEventSummary
		// Best-effort parse; format degrades gracefully on failure.
		_ = json.Unmarshal([]byte(result), &events) //nolint:errcheck // best-effort for display
		msg.Writef("Found %d events", len(events))
	}
	return msg.String() + "\n"
}

// ParameterSchema returns the JSON schema for the tool's input parameters.
func (t TianLedgerTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edict_id": map[string]any{
				"type":        "integer",
				"description": "Optional: filter events by edict ID",
			},
			"event_type": map[string]any{
				"type":        "string",
				"description": "Optional: filter events by event type (e.g. 'ritual_started')",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Optional: maximum number of events to return (default 50)",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Optional: number of events to skip for pagination (default 0)",
			},
			"since_id": map[string]any{
				"type":        "integer",
				"description": "Optional: return only events with id greater than this — a cursor for incremental polling (default 0, no filter)",
			},
			"detail": map[string]any{
				"type":        "string",
				"enum":        []string{"summary", "full"},
				"description": "Optional: 'summary' (default) tail-truncates last_step_output to the court-configured length; 'full' returns every payload key untruncated",
			},
		},
	}
}

// tianEventDetail extracts a human-readable detail from an event payload.
func tianEventDetail(ev storage.TianEvent) string {
	if ev.Payload == nil {
		return ""
	}
	payload := map[string]interface{}(ev.Payload)
	for _, key := range []string{"ritual", "step", "minister_id", "intent"} {
		if v, ok := payload[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
