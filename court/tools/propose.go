package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/storage"
)

// AnswerApproveProposal is the Zhengming approval option for general proposals
// submitted via ProposeTool.
const AnswerApproveProposal = "Approve proposal"

// ProposalHandler validates and applies one kind of Ruler-approved proposal
// submitted via the general propose tool.
type ProposalHandler interface {
	// Kind returns the unique kind identifier exposed in propose's kind enum.
	Kind() string
	// Allowed reports whether ministerID (with its parsed permissions) may
	// submit proposals of this kind.
	Allowed(ministerID string, perm Permissions) bool
	// Validate validates the structured JSON payload before bothering the Ruler
	// and renders a human-readable review document.
	Validate(ctx context.Context, payload json.RawMessage) (display string, err error)
	// Apply commits the approved proposal. Invoked ONLY by the Court's
	// HandleZhengmingResponse on Ruler approval; propose itself writes nothing.
	Apply(ctx context.Context, key storage.EdictKey, payload json.RawMessage) error
}

// ProposalSubmitHandler is an optional hook a ProposalHandler may implement to
// observe when a validated proposal has been queued as a Zhengming request.
type ProposalSubmitHandler interface {
	OnSubmitted(ctx context.Context, requestID string, key storage.EdictKey, payload json.RawMessage)
}

// ProposalRejectHandler is an optional hook a ProposalHandler may implement to
// observe when the Ruler rejects a pending proposal.
type ProposalRejectHandler interface {
	OnReject(ctx context.Context, key storage.EdictKey, payload json.RawMessage)
}

// PendingProposal records an in-flight proposal awaiting Ruler approval via
// Zhengming. Only the Court's HandleZhengmingResponse consumes it.
type PendingProposal struct {
	Kind       string
	Key        storage.EdictKey
	MinisterID string
	Payload    json.RawMessage
}

// ProposalRegistry owns registered ProposalHandlers and tracks pending
// Zhengming proposals for a Court instance.
type ProposalRegistry struct {
	mu       sync.RWMutex
	handlers map[string]ProposalHandler
	pending  map[string]PendingProposal // requestID -> PendingProposal
}

// NewProposalRegistry creates an empty ProposalRegistry.
func NewProposalRegistry() *ProposalRegistry {
	return &ProposalRegistry{
		handlers: make(map[string]ProposalHandler),
		pending:  make(map[string]PendingProposal),
	}
}

// Register adds or replaces a ProposalHandler by Kind().
func (r *ProposalRegistry) Register(h ProposalHandler) error {
	if r == nil {
		return fmt.Errorf("proposal registry is nil")
	}
	if h == nil || strings.TrimSpace(h.Kind()) == "" {
		return fmt.Errorf("proposal handler with non-empty kind is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[h.Kind()] = h
	return nil
}

// Get returns the ProposalHandler registered for kind.
func (r *ProposalRegistry) Get(kind string) (ProposalHandler, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[kind]
	return h, ok
}

// Kinds returns the sorted list of registered proposal kinds.
func (r *ProposalRegistry) Kinds() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	kinds := make([]string, 0, len(r.handlers))
	for k := range r.handlers {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// RecordPending stores a pending proposal keyed by its Zhengming requestID.
func (r *ProposalRegistry) RecordPending(requestID string, p PendingProposal) {
	if r == nil || requestID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := p
	cp.Payload = append(json.RawMessage(nil), p.Payload...)
	r.pending[requestID] = cp
}

// TakePending removes and returns the pending proposal for requestID, if any.
func (r *ProposalRegistry) TakePending(requestID string) (PendingProposal, bool) {
	if r == nil || requestID == "" {
		return PendingProposal{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pending[requestID]
	if ok {
		delete(r.pending, requestID)
	}
	return p, ok
}

// HasPending reports whether requestID is a pending proposal.
func (r *ProposalRegistry) HasPending(requestID string) bool {
	if r == nil || requestID == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.pending[requestID]
	return ok
}

// ApplyResponse resolves a pending proposal upon receiving the Ruler's
// Zhengming response. It invokes ProposalHandler.Apply ONLY when answer equals
// AnswerApproveProposal. Returns handled=false if requestID is not a proposal.
func (r *ProposalRegistry) ApplyResponse(ctx context.Context, requestID, answer string) (bool, error) {
	if r == nil || requestID == "" {
		return false, nil
	}
	pending, ok := r.TakePending(requestID)
	if !ok {
		return false, nil
	}
	handler, exists := r.Get(pending.Kind)
	if !exists {
		return true, fmt.Errorf("proposal handler %q is not registered", pending.Kind)
	}
	if answer == AnswerApproveProposal {
		return true, handler.Apply(ctx, pending.Key, pending.Payload)
	}
	if rh, ok := handler.(ProposalRejectHandler); ok {
		rh.OnReject(ctx, pending.Key, pending.Payload)
	}
	return true, nil
}

// ProposeTool submits a validated, Ruler-gated proposal backed by the Court's
// ProposalRegistry. It writes nothing itself: on Ruler approval, the Court's
// HandleZhengmingResponse invokes the kind's ProposalHandler.Apply.
//
// Large proposals (or proposals with review_in_editor=true) undergo an $EDITOR
// review pass via reviewInEditor; if status is "ruler_modified", nothing was
// saved and the caller should integrate the Ruler's edits and call propose again.
type ProposeTool struct {
	Ctx       ToolContext
	Registry  *ProposalRegistry
	Requester ZhengmingRequester
	NotifyFn  func() func(any)
}

func (t ProposeTool) Name() string { return "propose" }

func (t ProposeTool) Description() string {
	kinds := t.Registry.Kinds()
	kindHint := ""
	if len(kinds) > 0 {
		kindHint = fmt.Sprintf(" Registered kinds: %s.", strings.Join(kinds, ", "))
	}
	return `Submit a structured proposal for Ruler review and sealing via Zhengming.` + kindHint + ` ` +
		`This tool validates the payload and writes nothing itself — only the Ruler's approval applies it. ` +
		`If status is "ruler_modified", nothing was saved: integrate the Ruler's modified text and call propose again.`
}

func (t ProposeTool) Call(ctx context.Context, input string) (string, error) {
	if t.Registry == nil {
		return "", fmt.Errorf("proposal registry is not configured")
	}
	if t.Requester == nil {
		return "", fmt.Errorf("zhengming requester is not configured")
	}

	var params struct {
		Kind           string          `json:"kind"`
		Summary        string          `json:"summary"`
		Payload        json.RawMessage `json:"payload"`
		Priority       string          `json:"priority"`
		ReviewInEditor bool            `json:"review_in_editor"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(params.Kind) == "" {
		return "", fmt.Errorf("kind is required")
	}
	if strings.TrimSpace(params.Summary) == "" {
		return "", fmt.Errorf("summary is required")
	}
	if len(strings.TrimSpace(string(params.Payload))) == 0 {
		return "", fmt.Errorf("payload is required")
	}

	handler, ok := t.Registry.Get(params.Kind)
	if !ok {
		return "", fmt.Errorf("unknown proposal kind %q", params.Kind)
	}

	perm, _ := PermissionsFromContext(ctx)
	if !handler.Allowed(t.Ctx.MinisterID, perm) {
		return "", fmt.Errorf("minister %q is not authorized to submit %q proposals", t.Ctx.MinisterID, params.Kind)
	}

	display, err := handler.Validate(ctx, params.Payload)
	if err != nil {
		return "", fmt.Errorf("proposal validation failed: %w", err)
	}

	if reviewed, done, err := reviewProposalInEditor(ctx, t.NotifyFn, display, params.Kind, params.ReviewInEditor); err != nil || done {
		if err != nil {
			return "", err
		}
		return reviewed, nil
	}

	questions := storage.ZhengmingQuestions{{
		Text:    display,
		Summary: params.Summary,
		Options: []string{AnswerApproveProposal, AnswerReject},
	}}

	key := storage.EdictKey{
		ID:       0, // CourtEdictKey
		Username: t.Ctx.Username,
		Project:  t.Ctx.Project,
	}
	requestID, err := t.Requester.RequestZhengming(ctx, key, questions, parsePriority(params.Priority), t.Ctx.MinisterID)
	if err != nil {
		return "", fmt.Errorf("failed to submit proposal zhengming: %w", err)
	}

	t.Registry.RecordPending(requestID, PendingProposal{
		Kind:       params.Kind,
		Key:        key,
		MinisterID: t.Ctx.MinisterID,
		Payload:    params.Payload,
	})
	if sh, ok := handler.(ProposalSubmitHandler); ok {
		sh.OnSubmitted(ctx, requestID, key, params.Payload)
	}

	out, _ := json.Marshal(map[string]string{
		"status":     "proposed",
		"request_id": requestID,
		"kind":       params.Kind,
	})
	return string(out), nil
}

func reviewProposalInEditor(ctx context.Context, notifyFn func() func(any), display, kind string, forceEditor bool) (string, bool, error) {
	if !forceEditor && len(display) <= 500 {
		return "", false, nil
	}
	var notify func(any)
	if notifyFn != nil {
		notify = notifyFn()
	}
	if notify == nil {
		return "", false, nil
	}

	filename := fmt.Sprintf("proposal_%s.md", kind)
	review, err := reviewInEditor(ctx, notify, display, filename)
	if err != nil {
		return "", false, fmt.Errorf("editor review failed: %w", err)
	}
	switch review.Status {
	case reviewModified:
		originalJSON, _ := json.Marshal(display)
		modifiedJSON, _ := json.Marshal(review.Content)
		diffJSON, _ := json.Marshal(review.Diff)
		return fmt.Sprintf(`{"status":"ruler_modified","original":%s,"modified":%s,"diff":%s}`,
			originalJSON, modifiedJSON, diffJSON), true, nil
	case reviewRejected:
		return `{"status":"rejected"}`, true, nil
	default:
		return "", false, nil
	}
}

func (t ProposeTool) Format(input, result string, err error) string {
	msg := utils.NewMsgBlockBuilder("Propose")
	msg.WriteLn()
	if err != nil {
		msg.Writef("Error: %v", err)
	} else {
		var params struct {
			Kind    string `json:"kind"`
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal([]byte(input), &params)
		msg.Writef("[%s] %s", params.Kind, params.Summary)
	}
	return msg.String() + "\n"
}

func (t ProposeTool) ParameterSchema() map[string]any {
	kindProp := map[string]any{
		"type":        "string",
		"description": "Registered proposal kind identifier",
	}
	if kinds := t.Registry.Kinds(); len(kinds) > 0 {
		kindProp["enum"] = kinds
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": kindProp,
			"summary": map[string]any{
				"type":        "string",
				"description": "Short one-line summary for the Ruler's Zhengming prompt",
			},
			"payload": map[string]any{
				"type":        "object",
				"description": "Structured proposal payload validated by the kind's handler",
			},
			"priority": map[string]any{
				"type":        "string",
				"enum":        []string{"normal", "urgent"},
				"description": "Priority level (default: normal)",
			},
			"review_in_editor": map[string]any{
				"type":        "boolean",
				"description": "Optional. Open the rendered review document in $EDITOR before Zhengming",
			},
		},
		"required": []string{"kind", "summary", "payload"},
	}
}
