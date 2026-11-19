package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/afittestide/asimi/storage"
)

// ReplaceEdictTool routes a REPLACEMENT for an existing edict's intent to the
// Ruler via Zhengming (an async request). The tool writes nothing itself: on
// approval the Court overwrites the target edict's intent (no Clarification
// block, no concatenation) and invalidates its seals; on rejection nothing
// changes. For suggestions over 500 chars, the Ruler reviews them in $EDITOR
// first (via reviewInEditor).
//
// Target eligibility: a cancelled or ruler-sealed edict is NOT a valid
// replacement target — the call fails before any request is raised. Use
// new_edict instead.
//
// The ruler_modified contract (Ruler ruling, 2026-11-18): if the returned
// status is "ruler_modified", NOTHING WAS SAVED. The Ruler's modified text
// arrives WITH the result — integrate it into the suggestion and call
// replace_edict again with it. That is the designed loop, not a failure: a
// handed-back modified text with no instruction is the defect.
type ReplaceEdictTool struct {
	Ctx       ToolContext
	Requester ZhengmingRequester
	NotifyFn  func() func(any)
}

func (t ReplaceEdictTool) Name() string { return "replace_edict" }

func (t ReplaceEdictTool) Description() string {
	return `Route a REPLACEMENT intent for an existing edict to the Ruler via an async Zhengming request. ` +
		`On approval the edict's intent is overwritten (no Clarification block) and its seals invalidated. ` +
		`A cancelled or ruler-sealed edict is not a valid target — use new_edict for those. ` +
		`This tool writes nothing itself — the Ruler's approval is what writes. ` +
		`If status is "ruler_modified", nothing was saved: integrate the Ruler's modified text ` +
		`into the suggestion and call replace_edict again — that is the designed loop.`
}

func (t ReplaceEdictTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		EdictID    uint   `json:"edict_id"`
		Suggestion string `json:"suggestion"`
		Summary    string `json:"summary"`
		Priority   string `json:"priority"`
		Evidence   string `json:"evidence"`
		IssueRef   string `json:"issue_ref"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.EdictID == 0 {
		return "", fmt.Errorf("edict_id is required and must be non-zero — use new_edict to suggest a new edict")
	}
	if params.Suggestion == "" {
		return "", fmt.Errorf("suggestion is required")
	}
	if params.Summary == "" {
		return "", fmt.Errorf("summary is required")
	}

	key := storage.EdictKey{
		ID:       params.EdictID,
		Username: t.Ctx.Username,
		Project:  t.Ctx.Project,
	}

	// Scope check + eligibility guard (Defect 5): refuse a target the Court
	// already knows is dead — a cancelled or ruler-sealed edict — BEFORE any
	// zhengming is raised. A prompt the Court knows cannot land must never
	// consume an approval. The guard reads the derived status; it does not
	// re-query the columns by hand.
	if err := ensureReplaceableTarget(t.Ctx, key); err != nil {
		return "", err
	}

	questionText, err := suggestionWithMetadata(params.Suggestion, params.Evidence, params.IssueRef)
	if err != nil {
		return "", err
	}

	// Large suggestions get an $EDITOR review pass before they hit Zhengming
	// (see new_edict.go for the ruler_modified contract).
	modified, done, err := reviewSuggestionInEditor(ctx, t.NotifyFn, questionText, "edict_suggestion.md")
	if err != nil || done {
		if err != nil {
			return "", err
		}
		return modified, nil
	}

	questions := storage.ZhengmingQuestions{{
		Text:    questionText,
		Summary: params.Summary,
		Options: []string{AnswerApproveEdict, AnswerReject},
	}}

	requestID, err := t.Requester.RequestZhengming(ctx, key, questions, parsePriority(params.Priority), t.Ctx.MinisterID)
	if err != nil {
		return "", fmt.Errorf("failed to suggest edict replacement: %w", err)
	}

	return fmt.Sprintf(`{"status":"suggested","request_id":"%s"}`, requestID), nil
}

func (t ReplaceEdictTool) Format(input, result string, err error) string {
	return formatSuggestion("ReplaceEdict", input, result, err)
}

func (t ReplaceEdictTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edict_id": map[string]any{
				"type":        "integer",
				"description": "Required. The edict whose intent the suggestion replaces. Must be an active (not cancelled, not ruler-sealed) edict in the caller's scope.",
			},
			"suggestion": map[string]any{
				"type":        "string",
				"description": "The complete replacement intent text",
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "A short one-line summary",
			},
			"priority": map[string]any{
				"type":        "string",
				"enum":        []string{"normal", "urgent"},
				"description": "Priority level (default: normal)",
			},
			"evidence": map[string]any{
				"type":        "string",
				"description": "Supporting evidence: file:line references, patterns found, etc.",
			},
			"issue_ref": map[string]any{
				"type":        "string",
				"description": "Optional. Canonical reference of the issue motivating the edict, e.g. 'afittestide/asimi-cli#168'. Sets/updates the target edict's issue_ref; empty leaves it unchanged.",
			},
		},
		"required": []string{"edict_id", "suggestion", "summary"},
	}
}

// ensureReplaceableTarget validates that the target edict exists in the
// caller's scope and is eligible for replacement: not cancelled, not
// ruler-sealed. It sits alongside the scope check (does not duplicate it) and
// uses the derived status from storage.SealService.
func ensureReplaceableTarget(tctx ToolContext, key storage.EdictKey) error {
	var count int64
	tctx.DB.Model(&storage.Edict{}).
		Where("id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).
		Count(&count)
	if count == 0 {
		return fmt.Errorf("edict %d not found for user %q project %q", key.ID, key.Username, key.Project)
	}

	status, err := storage.NewSealService(tctx.DB).GetEdictStatus(key)
	if err != nil {
		return fmt.Errorf("cannot determine status of edict %d: %w", key.ID, err)
	}
	switch status {
	case storage.EdictCancelled:
		var cancelledAt string
		var edict storage.Edict
		if tctx.DB.First(&edict, "id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).Error == nil && edict.CancelledAt != nil {
			cancelledAt = edict.CancelledAt.Format(timeFormat)
		}
		return fmt.Errorf("edict %d is cancelled (since %s) and cannot be a replacement target — use new_edict instead",
			key.ID, cancelledAt)
	case storage.EdictSealed:
		return fmt.Errorf("edict %d is ruler-sealed (ascended) and cannot be a replacement target — use new_edict instead", key.ID)
	}
	return nil
}

// timeFormat renders timestamps in error messages.
const timeFormat = "2006-01-02T15:04:05Z07:00"
