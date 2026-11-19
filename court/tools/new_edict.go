package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/storage"
)

// NewEdictTool routes a NEW edict suggestion to the Ruler via Zhengming
// (an async request). The tool writes nothing itself: on approval the Court
// creates a new edict; on rejection nothing is created. For suggestions over
// 500 chars, the Ruler reviews them in $EDITOR first (via reviewInEditor).
//
// The ruler_modified contract (Ruler ruling, 2026-11-18): if the returned
// status is "ruler_modified", NOTHING WAS SAVED. The Ruler's modified text
// arrives WITH the result — integrate it into the suggestion and call
// new_edict again with it. That is the designed loop, not a failure: a
// handed-back modified text with no instruction is the defect.
//
// NotifyFn is a getter, not a snapshot: it returns the *current* notify each
// time the tool runs. This matters in daemon mode where the live notify
// channel is installed lazily by court.Subscribe() on each client connect, and
// can be swapped out when a client reconnects. A snapshot captured at
// Tools()-construction would be nil (or stale) and silently bypass the
// editor branch.
type NewEdictTool struct {
	Ctx       ToolContext
	Requester ZhengmingRequester
	NotifyFn  func() func(any)
}

func (t NewEdictTool) Name() string { return "new_edict" }

func (t NewEdictTool) Description() string {
	return `Route a suggestion for a NEW edict to the Ruler via an async Zhengming request. ` +
		`This tool writes nothing itself — the Ruler's approval is what creates the edict. ` +
		`If status is "ruler_modified", nothing was saved: integrate the Ruler's modified text ` +
		`into the suggestion and call new_edict again — that is the designed loop.`
}

func (t NewEdictTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		Suggestion string `json:"suggestion"`
		Summary    string `json:"summary"`
		Priority   string `json:"priority"`
		Evidence   string `json:"evidence"`
		IssueRef   string `json:"issue_ref"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.Suggestion == "" {
		return "", fmt.Errorf("suggestion is required")
	}
	if params.Summary == "" {
		return "", fmt.Errorf("summary is required")
	}

	questionText, err := suggestionWithMetadata(params.Suggestion, params.Evidence, params.IssueRef)
	if err != nil {
		return "", err
	}

	// Large suggestions get an $EDITOR review pass before they hit Zhengming.
	// The Ruler can :wq to forward as-is, edit and :wq to bounce it back to
	// the minister, or — when the host can establish it — :q! to reject
	// outright. An unchanged document is a reviewed document: it falls
	// through to Zhengming exactly as before.
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

	key := storage.EdictKey{
		ID:       0, // a new edict; replace_edict is the only path to an existing one
		Username: t.Ctx.Username,
		Project:  t.Ctx.Project,
	}
	requestID, err := t.Requester.RequestZhengming(ctx, key, questions, parsePriority(params.Priority), t.Ctx.MinisterID)
	if err != nil {
		return "", fmt.Errorf("failed to suggest new edict: %w", err)
	}

	return fmt.Sprintf(`{"status":"suggested","request_id":"%s"}`, requestID), nil
}

func (t NewEdictTool) Format(input, result string, err error) string {
	return formatSuggestion("NewEdict", input, result, err)
}

func (t NewEdictTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"suggestion": map[string]any{
				"type":        "string",
				"description": "A session-independent, complete change suggestion",
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
				"description": "Optional. Canonical reference of the issue motivating this edict, e.g. 'afittestide/asimi-cli#168' or its GitHub URL. Stored verbatim on the created edict.",
			},
		},
		"required": []string{"suggestion", "summary"},
	}
}

// parsePriority maps the tool's priority string to the storage value,
// defaulting to normal.
func parsePriority(priority string) storage.ZhengmingPriority {
	if priority == "urgent" {
		return storage.PriorityUrgent
	}
	return storage.PriorityNormal
}

// suggestionWithMetadata renders the suggestion text with evidence and issue_ref
// annotations, if any. The annotations are plain-text markers the approval
// handler knows how to strip back off.
func suggestionWithMetadata(suggestion, evidence, issueRef string) (string, error) {
	if issueRef != "" {
		suggestion = fmt.Sprintf("%s\n\nIssue-Ref: %s", suggestion, issueRef)
	}
	if evidence != "" {
		suggestion = fmt.Sprintf("%s\n\nEvidence: %s", suggestion, evidence)
	}
	return suggestion, nil
}

// reviewSuggestionInEditor runs the >500-char $EDITOR review pass shared by
// new_edict and replace_edict. It returns (result, done, err): when done is
// true the caller returns result unchanged (ruler_modified or a deliberate
// rejection); when done is false the request continues to Zhengming with the
// original text.
func reviewSuggestionInEditor(ctx context.Context, notifyFn func() func(any), questionText, filename string) (string, bool, error) {
	if len(questionText) <= 500 {
		return "", false, nil
	}
	var notify func(any)
	if notifyFn != nil {
		notify = notifyFn()
	}
	if notify == nil {
		return "", false, nil
	}
	review, err := reviewInEditor(ctx, notify, questionText, filename)
	if err != nil {
		return "", false, fmt.Errorf("editor review failed: %w", err)
	}

	if review.Status == reviewModified {
		originalJSON, _ := json.Marshal(questionText)
		modifiedJSON, _ := json.Marshal(review.Content)
		diffJSON, _ := json.Marshal(review.Diff)
		return fmt.Sprintf(`{"status":"ruler_modified","original":%s,"modified":%s,"diff":%s}`,
			originalJSON, modifiedJSON, diffJSON), true, nil
	}
	// approved, or a deliberate rejection → fall through to zhengming with
	// the original text. The Court records the Ruler's answer.
	return "", false, nil
}

// formatSuggestion renders the Format block shared by NewEdictTool and
// ReplaceEdictTool.
func formatSuggestion(title, input, result string, err error) string {
	msg := utils.NewMsgBlockBuilder(title)
	msg.WriteLn()
	if err != nil {
		msg.Writef("Error: %v", err)
	} else {
		var params struct {
			Suggestion string `json:"suggestion"`
		}
		json.Unmarshal([]byte(input), &params)
		preview := params.Suggestion
		if len(preview) > 60 {
			preview = preview[:57] + "..."
		}
		msg.Writef("Suggested: %s", preview)
	}
	return msg.String() + "\n"
}
