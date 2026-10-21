package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/storage"
)

// tianMarkdownTableFormat is the generic TUI opt-in format. Any daemon-side
// tool may emit an envelope carrying this value to have the TUI lay its
// result out as a width-aware markdown table.
const tianMarkdownTableFormat = "markdown_table"

// tianLedgerEnvelope is the human-facing result shape. Columns and rows are
// plain strings so the TUI owns all layout; the raw summaries ride along under
// "events" so the model keeps full fidelity (payload included).
type tianLedgerEnvelope struct {
	Format  string             `json:"format"`
	Title   string             `json:"title"`
	Columns []string           `json:"columns"`
	Rows    [][]string         `json:"rows"`
	Events  []tianEventSummary `json:"events"`
}

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

	envelope := tianLedgerEnvelope{
		Format:  tianMarkdownTableFormat,
		Title:   "Heaven Ledger",
		Columns: []string{"edict_id", "event", "time", "detail"},
		Rows:    make([][]string, len(summaries)),
		Events:  summaries,
	}
	for i, ev := range events {
		envelope.Rows[i] = []string{
			strconv.FormatUint(uint64(ev.EdictID), 10),
			tianEventCell(ev.EventType, ev.Payload),
			tianRelativeTime(ev.CreatedAt),
			summaries[i].Detail,
		}
	}

	resultJSON, _ := json.MarshalIndent(envelope, "", "  ") //nolint:errcheck // envelope is always marshallable
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
		var envelope tianLedgerEnvelope
		// Best-effort parse; format degrades gracefully on failure.
		if e := json.Unmarshal([]byte(result), &envelope); e == nil && envelope.Format != "" {
			msg.Writef("Found %d events", len(envelope.Events))
		} else {
			// Legacy bare-array shape (or unparseable result).
			var events []tianEventSummary
			_ = json.Unmarshal([]byte(result), &events) //nolint:errcheck // best-effort for display
			msg.Writef("Found %d events", len(events))
		}
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

// tianEventCell builds the "event" column: the event type paired with the
// per-type payload datum that identifies the specific occurrence — the step
// number and name, the ritual name, or the acting minister.
func tianEventCell(eventType storage.CourtEvent, payload storage.JSON) string {
	p := map[string]interface{}(payload)
	switch eventType {
	case storage.EventStepStarted, storage.EventStepCompleted, storage.EventStepFailed:
		step, _ := p["step"].(string)
		if step == "" {
			return string(eventType)
		}
		if idx, ok := tianPayloadInt(p["step_index"]); ok {
			return fmt.Sprintf("step #%d: %s", idx, step)
		}
		return "step: " + step
	case storage.EventRitualStarted, storage.EventRitualCompleted,
		storage.EventRitualFailed, storage.EventRitualAborted, storage.EventRitualEnacted:
		if ritual, ok := p["ritual"].(string); ok && ritual != "" {
			return ritual
		}
	case storage.EventSealGranted, storage.EventZhengmingAnswered, "zhengming_requested":
		if minister, ok := p["minister_id"].(string); ok && minister != "" {
			return minister
		}
	}
	return string(eventType)
}

// tianPayloadInt coerces a JSON-decoded numeric payload value to an int.
// encoding/json yields float64; in-process publishers may pass int/int64.
func tianPayloadInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

// tianRelativeTime renders a timestamp relative to now, e.g. "12s ago",
// "5m ago", "3h ago", "2d ago".
func tianRelativeTime(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// tianDetailMaxLen bounds the detail cell so one chatty event cannot dominate
// the table.
const tianDetailMaxLen = 80

// tianEventDetail renders a per-type human summary for the "detail" column.
func tianEventDetail(ev storage.TianEvent) string {
	p := map[string]interface{}(ev.Payload)
	switch ev.EventType {
	case storage.EventEdictCreated:
		if intent, ok := p["intent"].(string); ok && intent != "" {
			return tianHeadline(intent)
		}
	case storage.EventRitualCompleted, storage.EventStepFailed, storage.EventRitualFailed:
		if out, ok := p["last_step_output"].(string); ok && strings.TrimSpace(out) != "" {
			return tianSnippet(tianLastLine(out))
		}
		if e, ok := p["error"].(string); ok && e != "" {
			return tianSnippet(e)
		}
	case storage.EventZhengmingNeeded, "zhengming_requested":
		if q := tianQuestionSummary(p["questions"]); q != "" {
			return q
		}
	case storage.EventZhengmingAnswered:
		if a, ok := p["answer"].(string); ok && a != "" {
			return tianSnippet(a)
		}
	case storage.EventCourtStarted:
		for _, key := range []string{"current_version", "version"} {
			if v, ok := p[key].(string); ok && v != "" {
				return v
			}
		}
	}
	// Fallback for event types without a tailored summary: the same generic
	// keys the column used before, so nothing regresses to empty.
	for _, key := range []string{"ritual", "step", "minister_id", "intent"} {
		if v, ok := p[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// tianHeadline reduces an intent body to its first meaningful line, stripping
// a leading markdown heading prefix such as "# Bug:".
func tianHeadline(intent string) string {
	for _, line := range strings.Split(intent, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, prefix := range []string{"# Bug:", "# Feature:", "# feat:", "# bug:", "# chore:"} {
			if strings.HasPrefix(line, prefix) {
				line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
				break
			}
		}
		return tianSnippet(line)
	}
	return ""
}

// tianQuestionSummary picks the first zhengming question's short display text.
func tianQuestionSummary(v interface{}) string {
	items, ok := v.([]interface{})
	if !ok || len(items) == 0 {
		return ""
	}
	first, ok := items[0].(map[string]interface{})
	if !ok {
		return ""
	}
	for _, key := range []string{"summary", "text"} {
		if s, ok := first[key].(string); ok && s != "" {
			return tianSnippet(s)
		}
	}
	return ""
}

// tianLastLine returns the last non-empty line of s, where verdicts land.
func tianLastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return s
}

// tianSnippet collapses whitespace and bounds length so the cell stays single-line.
func tianSnippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > tianDetailMaxLen {
		return string(runes[:tianDetailMaxLen-1]) + "…"
	}
	return s
}
