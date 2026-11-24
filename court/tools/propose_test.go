package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/afittestide/asimi/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProposalHandler struct {
	kind         string
	allowedFn    func(ministerID string, perm Permissions) bool
	validateFn   func(ctx context.Context, payload json.RawMessage) (string, error)
	applied      []json.RawMessage
	appliedKeys  []storage.EdictKey
	rejected     []json.RawMessage
	submittedIDs []string
	applyErr     error
}

func (h *fakeProposalHandler) Kind() string { return h.kind }

func (h *fakeProposalHandler) Allowed(ministerID string, perm Permissions) bool {
	if h.allowedFn != nil {
		return h.allowedFn(ministerID, perm)
	}
	return true
}

func (h *fakeProposalHandler) Validate(ctx context.Context, payload json.RawMessage) (string, error) {
	if h.validateFn != nil {
		return h.validateFn(ctx, payload)
	}
	return "Proposal review document", nil
}

func (h *fakeProposalHandler) Apply(ctx context.Context, key storage.EdictKey, payload json.RawMessage) error {
	if h.applyErr != nil {
		return h.applyErr
	}
	h.applied = append(h.applied, append(json.RawMessage(nil), payload...))
	h.appliedKeys = append(h.appliedKeys, key)
	return nil
}

func (h *fakeProposalHandler) OnSubmitted(_ context.Context, reqID string, _ storage.EdictKey, _ json.RawMessage) {
	h.submittedIDs = append(h.submittedIDs, reqID)
}

func (h *fakeProposalHandler) OnReject(_ context.Context, _ storage.EdictKey, payload json.RawMessage) {
	h.rejected = append(h.rejected, append(json.RawMessage(nil), payload...))
}

type fakeProposalRequester struct {
	lastKey      storage.EdictKey
	lastMinister string
	lastQs       storage.ZhengmingQuestions
	reqID        string
	calls        int
}

func (f *fakeProposalRequester) RequestZhengming(_ context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, _ storage.ZhengmingPriority, callerMinisterID string) (string, error) {
	f.calls++
	f.lastKey = key
	f.lastMinister = callerMinisterID
	f.lastQs = questions
	if f.reqID == "" {
		return "req-prop-1", nil
	}
	return f.reqID, nil
}

func TestProposeTool_DynamicSchemaAndAuthorization(t *testing.T) {
	reg := NewProposalRegistry()
	require.NoError(t, reg.Register(&fakeProposalHandler{
		kind: "mcp_classification",
		allowedFn: func(ministerID string, _ Permissions) bool {
			return ministerID == "rites"
		},
	}))
	require.NoError(t, reg.Register(&fakeProposalHandler{kind: "allowlist_entry"}))

	req := &fakeProposalRequester{}
	tool := ProposeTool{
		Ctx:       ToolContext{MinisterID: "forge", Username: "daonb", Project: "daonb/asimi"},
		Registry:  reg,
		Requester: req,
	}

	// Dynamic enum exposes sorted registered kinds.
	schema := tool.ParameterSchema()
	props := schema["properties"].(map[string]any)
	kindProp := props["kind"].(map[string]any)
	assert.Equal(t, []string{"allowlist_entry", "mcp_classification"}, kindProp["enum"])

	// Unauthorized minister is blocked before Zhengming.
	_, err := tool.Call(context.Background(), `{"kind":"mcp_classification","summary":"classify","payload":{"a":1}}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not authorized")
	assert.Equal(t, 0, req.calls)
}

func TestProposeTool_ValidateBlocksBeforeRulerAndWritesNothing(t *testing.T) {
	reg := NewProposalRegistry()
	handler := &fakeProposalHandler{
		kind: "mcp_classification",
		validateFn: func(_ context.Context, payload json.RawMessage) (string, error) {
			if strings.Contains(string(payload), "bad") {
				return "", errors.New("invalid permission")
			}
			return "Table of classifications", nil
		},
	}
	require.NoError(t, reg.Register(handler))

	req := &fakeProposalRequester{reqID: "req-42"}
	tool := ProposeTool{
		Ctx:       ToolContext{MinisterID: "rites", Username: "daonb", Project: "daonb/asimi"},
		Registry:  reg,
		Requester: req,
	}

	// Invalid payload fails in Validate without creating a Zhengming request.
	_, err := tool.Call(context.Background(), `{"kind":"mcp_classification","summary":"bad","payload":{"bad":true}}`)
	require.Error(t, err)
	assert.Equal(t, 0, req.calls)
	assert.Empty(t, handler.applied)

	// Valid payload creates Zhengming under CourtEdictKey (ID=0) and writes NOTHING yet.
	res, err := tool.Call(context.Background(), `{"kind":"mcp_classification","summary":"Seal envoy tools","payload":{"server_id":"term"}}`)
	require.NoError(t, err)
	assert.Contains(t, res, `"status":"proposed"`)
	assert.Contains(t, res, `"request_id":"req-42"`)
	assert.Equal(t, 1, req.calls)
	assert.Equal(t, uint(0), req.lastKey.ID)
	assert.Equal(t, "rites", req.lastMinister)
	assert.Equal(t, []string{AnswerApproveProposal, AnswerReject}, req.lastQs[0].Options)
	assert.Empty(t, handler.applied, "propose itself must write nothing")
	assert.Equal(t, []string{"req-42"}, handler.submittedIDs)

	// Approval applies via ProposalRegistry.ApplyResponse (invoked by HandleZhengmingResponse).
	handled, err := reg.ApplyResponse(context.Background(), "req-42", AnswerApproveProposal)
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, handler.applied, 1)
	assert.Equal(t, `{"server_id":"term"}`, string(handler.applied[0]))
	assert.Equal(t, uint(0), handler.appliedKeys[0].ID)

	// Re-answering same requestID is a no-op.
	handled, err = reg.ApplyResponse(context.Background(), "req-42", AnswerApproveProposal)
	require.NoError(t, err)
	assert.False(t, handled)
}

func TestProposeTool_EditorReviewRulerModifiedAndReject(t *testing.T) {
	reg := NewProposalRegistry()
	handler := &fakeProposalHandler{
		kind: "mcp_classification",
		validateFn: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "Original proposal text", nil
		},
	}
	require.NoError(t, reg.Register(handler))

	req := &fakeProposalRequester{reqID: "req-ed"}
	editorOutcome := EditorModified
	editorContent := "Edited proposal text"
	notify := func(msg any) {
		if er, ok := msg.(EditorRequest); ok {
			er.ResultChan <- EditorResult{Outcome: editorOutcome, Content: editorContent}
		}
	}

	tool := ProposeTool{
		Ctx:       ToolContext{MinisterID: "rites", Username: "daonb", Project: "daonb/asimi"},
		Registry:  reg,
		Requester: req,
		NotifyFn:  func() func(any) { return notify },
	}

	// Ruler modifies in $EDITOR -> returns ruler_modified, nothing saved, no Zhengming.
	res, err := tool.Call(context.Background(), `{"kind":"mcp_classification","summary":"review","payload":{"ok":1},"review_in_editor":true}`)
	require.NoError(t, err)
	var parsed map[string]string
	require.NoError(t, json.Unmarshal([]byte(res), &parsed))
	assert.Equal(t, "ruler_modified", parsed["status"])
	assert.Equal(t, "Edited proposal text", parsed["modified"])
	assert.NotEmpty(t, parsed["diff"])
	assert.Equal(t, 0, req.calls)
	assert.Empty(t, handler.applied)

	// Ruler rejects in Zhengming -> OnReject called, Apply never called.
	editorOutcome = EditorUnchanged
	_, err = tool.Call(context.Background(), `{"kind":"mcp_classification","summary":"review","payload":{"ok":2},"review_in_editor":true}`)
	require.NoError(t, err)
	assert.Equal(t, 1, req.calls)

	handled, err := reg.ApplyResponse(context.Background(), "req-ed", AnswerReject)
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Empty(t, handler.applied)
	require.Len(t, handler.rejected, 1)
}
