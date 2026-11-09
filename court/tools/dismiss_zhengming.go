package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/afittestide/asimi/internal/utils"
)

// ZhengmingDisposer retires a pending zhengming request without the Ruler
// answering it, recording a reason that names why. Implemented by the Court
// (Court.DismissZhengming). It is the sanctioned alternative to direct SQL for
// closing a superseded prompt (e1010): the request is marked dismissed and the
// reason is recorded — never as an answer, which would claim the Ruler spoke.
type ZhengmingDisposer interface {
	DismissZhengming(requestID, reason string) error
}

// DismissZhengmingTool closes a pending zhengming request on the Court's
// authority. It exists so a minister can clear a superseded abort prompt
// without reaching for direct SQL, as the Ruler's ruling requires.
type DismissZhengmingTool struct {
	Ctx      ToolContext
	Disposer ZhengmingDisposer
}

func (t DismissZhengmingTool) Name() string { return "dismiss_zhengming" }

func (t DismissZhengmingTool) Description() string {
	return `Closes a pending zhengming (clarification) request without the Ruler answering it.
	Use it to retire a stale or superseded prompt — for example an abort-recovery
	prompt whose edict already ascended — so it stops blocking the edict.
	Input: JSON with 'request_id' and 'reason'. The reason is recorded in the
	request's disposition_reason field and in the ledger (never in 'answer',
	which keeps meaning the Ruler replied).`
}

func (t DismissZhengmingTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		RequestID string `json:"request_id"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.RequestID == "" {
		return "", fmt.Errorf("request_id is required")
	}
	if t.Disposer == nil {
		return "", fmt.Errorf("zhengming disposer not wired")
	}
	if err := t.Disposer.DismissZhengming(params.RequestID, params.Reason); err != nil {
		return "", err
	}
	return fmt.Sprintf("Dismissed zhengming request %s", params.RequestID), nil
}

func (t DismissZhengmingTool) Format(input, result string, err error) string {
	msg := utils.NewMsgBlockBuilder("DismissZhengming")
	msg.WriteLn()
	if err != nil {
		msg.Writef("Error: %v", err)
	} else {
		msg.Writef("%s", result)
	}
	return msg.String() + "\n"
}

func (t DismissZhengmingTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"request_id": map[string]any{
				"type":        "string",
				"description": "The zhengming request_id to close",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Why the request is being retired; recorded in disposition_reason and the ledger",
			},
		},
		"required": []string{"request_id"},
	}
}
