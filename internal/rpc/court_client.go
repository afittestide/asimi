package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/internal/wire"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
)

// CourtClient exposes the Court RPC surface as a typed API.
// It implements every wire-safe method on courtapi.Client. The
// non-wire-safe methods (GetMinister, ConfigureModel)
// live on LoopbackCourt, which composes a CourtClient with a
// local reference for those operations — used while the split is
// incremental.
type CourtClient struct {
	conn *Conn

	subMu  sync.Mutex
	events chan any
}

// NewCourtClient wraps a live Conn as a typed client.
func NewCourtClient(conn *Conn) *CourtClient {
	return &CourtClient{conn: conn}
}

func (c *CourtClient) callVoid(ctx context.Context, method string, params any) error {
	_, err := c.conn.Call(ctx, method, params)
	return err
}

func (c *CourtClient) HasMinister(id string) bool {
	raw, err := c.conn.Call(context.Background(), MethodHasMinister, HasMinisterParams{ID: id})
	if err != nil {
		return false
	}
	var r HasMinisterResult
	_ = wire.Decode(raw, &r)
	return r.Has
}

func (c *CourtClient) ResetMinisterSession(id string, channelID ...string) {
	_ = c.callVoid(context.Background(), MethodResetMinisterSession, ResetMinisterSessionParams{ID: id, ChannelID: firstOrEmpty(channelID)})
}

func (c *CourtClient) EdictKey(edictID uint) storage.EdictKey {
	raw, err := c.conn.Call(context.Background(), MethodEdictKey, EdictKeyParams{EdictID: edictID})
	if err != nil {
		return storage.EdictKey{}
	}
	var r EdictKeyResult
	_ = wire.Decode(raw, &r)
	return r.Key
}

func (c *CourtClient) CourtEdictKey() storage.EdictKey {
	raw, err := c.conn.Call(context.Background(), MethodCourtEdictKey, nil)
	if err != nil {
		return storage.EdictKey{}
	}
	var r CourtEdictKeyResult
	_ = wire.Decode(raw, &r)
	return r.Key
}

func (c *CourtClient) CreateEdict(issueRef, intent, sessionID string) (*storage.Edict, error) {
	return c.createEdict(MethodCreateEdict, issueRef, intent, sessionID)
}

func (c *CourtClient) CreateEdictSilent(issueRef, intent, sessionID string) (*storage.Edict, error) {
	return c.createEdict(MethodCreateEdictSilent, issueRef, intent, sessionID)
}

func (c *CourtClient) createEdict(method, issueRef, intent, sessionID string) (*storage.Edict, error) {
	raw, err := c.conn.Call(context.Background(), method, CreateEdictParams{IssueRef: issueRef, Intent: intent, SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	var r CreateEdictResult
	if err := wire.Decode(raw, &r); err != nil {
		return nil, fmt.Errorf("rpc: decode %s result: %w", method, err)
	}
	return r.Edict, nil
}

func (c *CourtClient) GetEdict(edictID uint) (*storage.Edict, error) {
	raw, err := c.conn.Call(context.Background(), MethodGetEdict, GetEdictParams{EdictID: edictID})
	if err != nil {
		return nil, err
	}
	var r GetEdictResult
	if err := wire.Decode(raw, &r); err != nil {
		return nil, fmt.Errorf("rpc: decode GetEdict result: %w", err)
	}
	return r.Edict, nil
}

func (c *CourtClient) GrantRulerSeal(edictID uint, notes string) error {
	return c.callVoid(context.Background(), MethodGrantRulerSeal, GrantRulerSealParams{EdictID: edictID, Notes: notes})
}

func (c *CourtClient) CancelEdict(edictID uint) error {
	return c.callVoid(context.Background(), MethodCancelEdict, CancelEdictParams{EdictID: edictID})
}

func (c *CourtClient) AppendToIntent(edictID uint, clarification string) error {
	return c.callVoid(context.Background(), MethodAppendToIntent, AppendToIntentParams{EdictID: edictID, Clarification: clarification})
}

func (c *CourtClient) SetIntent(edictID uint, intent string) error {
	return c.callVoid(context.Background(), MethodSetIntent, SetIntentParams{EdictID: edictID, Intent: intent})
}

func (c *CourtClient) ListActiveEdicts() ([]storage.ActiveEdict, error) {
	raw, err := c.conn.Call(context.Background(), MethodListActiveEdicts, nil)
	if err != nil {
		return nil, err
	}
	var r ListActiveEdictsResult
	if err := wire.Decode(raw, &r); err != nil {
		return nil, err
	}
	return r.Edicts, nil
}

func (c *CourtClient) ListModels(provider string) (*schemas.BifrostListModelsResponse, error) {
	raw, err := c.conn.Call(context.Background(), MethodListModels, ListModelsParams{Provider: provider})
	if err != nil {
		return nil, err
	}
	var r ListModelsResult
	if err := wire.Decode(raw, &r); err != nil {
		return nil, fmt.Errorf("rpc: decode ListModels result: %w", err)
	}
	return r.Response, nil
}

func (c *CourtClient) ApproximateContextSize(provider, model string) int {
	raw, err := c.conn.Call(context.Background(), MethodApproximateContextSize, ApproximateContextSizeParams{Provider: provider, Model: model})
	if err != nil {
		return 0
	}
	var r ApproximateContextSizeResult
	if err := wire.Decode(raw, &r); err != nil {
		return 0
	}
	return r.Size
}

func (c *CourtClient) CancelZhengming(requestID string) {
	_ = c.callVoid(context.Background(), MethodCancelZhengming, CancelZhengmingParams{RequestID: requestID})
}

func (c *CourtClient) HandleZhengmingResponse(ctx context.Context, requestID, answer string) error {
	slog.Debug("CourtClient.HandleZhengmingResponse: sending RPC", "request_id", requestID, "answer", answer)
	err := c.callVoid(ctx, MethodHandleZhengming, HandleZhengmingParams{RequestID: requestID, Answer: answer})
	if err != nil {
		slog.Warn("CourtClient.HandleZhengmingResponse: RPC failed", "error", err)
	}
	return err
}

func (c *CourtClient) AllowRunnerFallback(allow bool) {
	_ = c.callVoid(context.Background(), MethodAllowRunnerFallback, AllowRunnerFallbackParams{Allow: allow})
}

func (c *CourtClient) ClearSessionHistory(tabTarget string) error {
	return c.callVoid(context.Background(), MethodClearSessionHistory, ClearSessionHistoryParams{TabTarget: tabTarget})
}

func (c *CourtClient) RollbackSession(tabTarget string, snapshot int) error {
	return c.callVoid(context.Background(), MethodRollbackSession, RollbackSessionParams{TabTarget: tabTarget, Snapshot: snapshot})
}

func (c *CourtClient) AddSessionMessage(tabTarget, role, content string) error {
	return c.callVoid(context.Background(), MethodAddSessionMessage, AddSessionMessageParams{TabTarget: tabTarget, Role: role, Content: content})
}

func (c *CourtClient) AddSessionContextFile(tabTarget, path, content string) error {
	return c.callVoid(context.Background(), MethodAddSessionCtxFile, AddSessionContextFileParams{TabTarget: tabTarget, Path: path, Content: content})
}

func (c *CourtClient) CompactSession(ctx context.Context, tabTarget, prompt string) (string, error) {
	raw, err := c.conn.Call(ctx, MethodCompactSession, CompactSessionParams{TabTarget: tabTarget, Prompt: prompt})
	if err != nil {
		return "", err
	}
	var r CompactSessionResult
	if err := wire.Decode(raw, &r); err != nil {
		return "", fmt.Errorf("rpc: decode CompactSession result: %w", err)
	}
	return r.Summary, nil
}

func (c *CourtClient) SessionState(tabTarget string) court.SessionState {
	raw, err := c.conn.Call(context.Background(), MethodSessionState, SessionStateParams{TabTarget: tabTarget})
	if err != nil {
		return court.SessionState{}
	}
	var r SessionStateResult
	_ = wire.Decode(raw, &r)
	return r.State
}

func (c *CourtClient) GetEdictSeals(key storage.EdictKey) ([]storage.Seal, error) {
	raw, err := c.conn.Call(context.Background(), MethodGetEdictSeals, GetEdictSealsParams{Key: key})
	if err != nil {
		return nil, err
	}
	var r GetEdictSealsResult
	if err := wire.Decode(raw, &r); err != nil {
		return nil, err
	}
	return r.Seals, nil
}

func (c *CourtClient) PublishEvent(key storage.EdictKey, et storage.CourtEvent, payload storage.JSON) uint {
	raw, err := c.conn.Call(context.Background(), MethodPublishEvent, PublishEventParams{Key: key, EventType: et, Payload: payload})
	if err != nil {
		return key.ID
	}
	var r PublishEventResult
	_ = wire.Decode(raw, &r)
	return r.EventID
}

func (c *CourtClient) RunShellCommand(ctx context.Context, input runners.Input) (runners.Output, error) {
	raw, err := c.conn.Call(ctx, MethodRunShellCommand, RunShellCommandParams{Input: input})
	if err != nil {
		return runners.Output{}, err
	}
	var r RunShellCommandResult
	if err := wire.Decode(raw, &r); err != nil {
		return runners.Output{}, err
	}
	return r.Output, nil
}

func (c *CourtClient) SubmitPrompt(targetID string, p *court.Prompt) error {
	if p == nil {
		return fmt.Errorf("rpc: nil Prompt")
	}
	params := SubmitPromptParams{
		TargetID:     targetID,
		Message:      p.Message,
		EdictKey:     p.EdictKey,
		ChannelID:    p.ChannelID,
		ContextFiles: p.ContextFiles,
	}
	ctx := p.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return c.callVoid(ctx, MethodSubmitPrompt, params)
}

func (c *CourtClient) RestoreMinisterSession(tabType string, msgs []schemas.ChatMessage, channelID ...string) error {
	return c.callVoid(context.Background(), MethodRestoreMinisterSess, RestoreMinisterSessionParams{TabType: tabType, Messages: msgs, ChannelID: firstOrEmpty(channelID)})
}

func (c *CourtClient) TakeSnapshot() court.Snapshot {
	raw, err := c.conn.Call(context.Background(), MethodTakeSnapshot, nil)
	if err != nil {
		return court.Snapshot{}
	}
	var r TakeSnapshotResult
	_ = wire.Decode(raw, &r)
	return r.Snapshot
}

// firstOrEmpty returns the first element of a variadic string slice,
// or "" if the slice is empty.
func firstOrEmpty(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func (c *CourtClient) CancelTab(channelID string) {
	_ = c.callVoid(context.Background(), MethodCancelTab, CancelTabParams{ChannelID: channelID})
}

func (c *CourtClient) PauseRitual(channelID string) bool {
	raw, err := c.conn.Call(context.Background(), MethodPauseRitual, PauseRitualParams{ChannelID: channelID})
	if err != nil {
		return false
	}
	var r PauseRitualResult
	_ = wire.Decode(raw, &r)
	return r.Paused
}

func (c *CourtClient) ResumeRitual(channelID string) bool {
	raw, err := c.conn.Call(context.Background(), MethodResumeRitual, ResumeRitualParams{ChannelID: channelID})
	if err != nil {
		return false
	}
	var r ResumeRitualResult
	_ = wire.Decode(raw, &r)
	return r.Resumed
}

func (c *CourtClient) SetContext(ctx context.Context, params types.SetContextParams) error {
	return c.callVoid(ctx, MethodSetContext, params)
}

func (c *CourtClient) EnsureEdictWorktree(ctx context.Context, edictID uint) (string, error) {
	raw, err := c.conn.Call(ctx, MethodEnsureEdictWorktree, EnsureEdictWorktreeParams{EdictID: edictID})
	if err != nil {
		return "", err
	}
	var r EnsureEdictWorktreeResult
	if err := wire.Decode(raw, &r); err != nil {
		return "", fmt.Errorf("rpc: decode EnsureEdictWorktree result: %w", err)
	}
	return r.WorktreeRel, nil
}

func (c *CourtClient) GetSessionExport(tabTarget string) (*court.SessionExport, error) {
	raw, err := c.conn.Call(context.Background(), MethodGetSessionExport, GetSessionExportParams{TabTarget: tabTarget})
	if err != nil {
		return nil, err
	}
	var r GetSessionExportResult
	if err := wire.Decode(raw, &r); err != nil {
		return nil, fmt.Errorf("rpc: decode GetSessionExport result: %w", err)
	}
	return r.Export, nil
}

// Subscribe returns a channel that delivers every server→client
// notification decoded into its Go type. The first call installs
// handlers on the underlying Conn; subsequent calls return the same
// chan. The channel stays open for the Conn's lifetime.
func (c *CourtClient) Subscribe(ctx context.Context) <-chan any {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	if c.events == nil {
		c.events = make(chan any, 256)
		SubscribeAll(c.conn, c.events)
	}
	return c.events
}
