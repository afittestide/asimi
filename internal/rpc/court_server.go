package rpc

import (
	"context"
	"log/slog"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/courtapi"
	"github.com/afittestide/asimi/internal/wire"
)

// RegisterCourtHandlers binds every supported Court RPC method
// on c to the given client. The client is the server-side, in-process
// implementation that actually does the work.
//
// This is the initial subset; remaining methods land incrementally.
func RegisterCourtHandlers(c *Conn, impl courtapi.Client) {
	c.Handle(MethodHasMinister, func(ctx context.Context, params []byte) ([]byte, error) {
		var p HasMinisterParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		return wire.Encode(HasMinisterResult{Has: impl.HasMinister(p.ID)})
	})

	c.Handle(MethodResetMinisterSession, func(ctx context.Context, params []byte) ([]byte, error) {
		var p ResetMinisterSessionParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		impl.ResetMinisterSession(p.ID, p.ChannelID)
		return nil, nil
	})

	c.Handle(MethodEdictKey, func(ctx context.Context, params []byte) ([]byte, error) {
		var p EdictKeyParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		return wire.Encode(EdictKeyResult{Key: impl.EdictKey(p.EdictID)})
	})

	c.Handle(MethodCourtEdictKey, func(ctx context.Context, _ []byte) ([]byte, error) {
		return wire.Encode(CourtEdictKeyResult{Key: impl.CourtEdictKey()})
	})

	c.Handle(MethodCreateEdict, func(ctx context.Context, params []byte) ([]byte, error) {
		var p CreateEdictParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		edict, err := impl.CreateEdict(p.IssueRef, p.Intent, p.SessionID)
		if err != nil {
			return nil, err
		}
		return wire.Encode(CreateEdictResult{Edict: edict})
	})

	c.Handle(MethodCreateEdictSilent, func(ctx context.Context, params []byte) ([]byte, error) {
		var p CreateEdictParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		edict, err := impl.CreateEdictSilent(p.IssueRef, p.Intent, p.SessionID)
		if err != nil {
			return nil, err
		}
		return wire.Encode(CreateEdictResult{Edict: edict})
	})

	c.Handle(MethodGetEdict, func(ctx context.Context, params []byte) ([]byte, error) {
		var p GetEdictParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		edict, err := impl.GetEdict(p.EdictID)
		if err != nil {
			return nil, err
		}
		return wire.Encode(GetEdictResult{Edict: edict})
	})

	c.Handle(MethodCancelEdict, func(ctx context.Context, params []byte) ([]byte, error) {
		var p CancelEdictParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		if err := impl.CancelEdict(p.EdictID); err != nil {
			return nil, err
		}
		return wire.Encode(nil)
	})

	c.Handle(MethodAppendToIntent, func(ctx context.Context, params []byte) ([]byte, error) {
		var p AppendToIntentParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		if err := impl.AppendToIntent(p.EdictID, p.Clarification); err != nil {
			return nil, err
		}
		return wire.Encode(nil)
	})

	c.Handle(MethodSetIntent, func(ctx context.Context, params []byte) ([]byte, error) {
		var p SetIntentParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		if err := impl.SetIntent(p.EdictID, p.Intent); err != nil {
			return nil, err
		}
		return wire.Encode(nil)
	})

	c.Handle(MethodGrantRulerSeal, func(ctx context.Context, params []byte) ([]byte, error) {
		var p GrantRulerSealParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		if err := impl.GrantRulerSeal(p.EdictID, p.Notes); err != nil {
			return nil, err
		}
		return nil, nil
	})

	c.Handle(MethodListActiveEdicts, func(ctx context.Context, _ []byte) ([]byte, error) {
		edicts, err := impl.ListActiveEdicts()
		if err != nil {
			return nil, err
		}
		return wire.Encode(ListActiveEdictsResult{Edicts: edicts})
	})

	c.Handle(MethodListModels, func(ctx context.Context, params []byte) ([]byte, error) {
		var p ListModelsParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		resp, err := impl.ListModels(p.Provider)
		if err != nil {
			return nil, err
		}
		return wire.Encode(ListModelsResult{Response: resp})
	})

	c.Handle(MethodApproximateContextSize, func(ctx context.Context, params []byte) ([]byte, error) {
		var p ApproximateContextSizeParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		return wire.Encode(ApproximateContextSizeResult{Size: impl.ApproximateContextSize(p.Provider, p.Model)})
	})

	c.Handle(MethodCancelZhengming, func(ctx context.Context, params []byte) ([]byte, error) {
		var p CancelZhengmingParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		impl.CancelZhengming(p.RequestID)
		return nil, nil
	})

	c.Handle(MethodHandleZhengming, func(ctx context.Context, params []byte) ([]byte, error) {
		var p HandleZhengmingParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		slog.Debug("RPC server: received HandleZhengmingResponse", "request_id", p.RequestID, "answer", p.Answer)
		if err := impl.HandleZhengmingResponse(ctx, p.RequestID, p.Answer); err != nil {
			return nil, err
		}
		return nil, nil
	})

	c.Handle(MethodAllowRunnerFallback, func(ctx context.Context, params []byte) ([]byte, error) {
		var p AllowRunnerFallbackParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		impl.AllowRunnerFallback(p.Allow)
		return nil, nil
	})

	c.Handle(MethodClearSessionHistory, func(ctx context.Context, params []byte) ([]byte, error) {
		var p ClearSessionHistoryParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		if err := impl.ClearSessionHistory(p.TabTarget); err != nil {
			return nil, err
		}
		return nil, nil
	})

	c.Handle(MethodRollbackSession, func(ctx context.Context, params []byte) ([]byte, error) {
		var p RollbackSessionParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		if err := impl.RollbackSession(p.TabTarget, p.Snapshot); err != nil {
			return nil, err
		}
		return nil, nil
	})

	c.Handle(MethodAddSessionMessage, func(ctx context.Context, params []byte) ([]byte, error) {
		var p AddSessionMessageParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		if err := impl.AddSessionMessage(p.TabTarget, p.Role, p.Content); err != nil {
			return nil, err
		}
		return nil, nil
	})

	c.Handle(MethodAddSessionCtxFile, func(ctx context.Context, params []byte) ([]byte, error) {
		var p AddSessionContextFileParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		if err := impl.AddSessionContextFile(p.TabTarget, p.Path, p.Content); err != nil {
			return nil, err
		}
		return nil, nil
	})

	c.Handle(MethodCompactSession, func(ctx context.Context, params []byte) ([]byte, error) {
		var p CompactSessionParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		summary, err := impl.CompactSession(ctx, p.TabTarget, p.Prompt)
		if err != nil {
			return nil, err
		}
		return wire.Encode(CompactSessionResult{Summary: summary})
	})

	c.Handle(MethodSessionState, func(ctx context.Context, params []byte) ([]byte, error) {
		var p SessionStateParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		return wire.Encode(SessionStateResult{State: impl.SessionState(p.TabTarget)})
	})

	c.Handle(MethodGetEdictSeals, func(ctx context.Context, params []byte) ([]byte, error) {
		var p GetEdictSealsParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		seals, err := impl.GetEdictSeals(p.Key)
		if err != nil {
			return nil, err
		}
		return wire.Encode(GetEdictSealsResult{Seals: seals})
	})

	c.Handle(MethodPublishEvent, func(ctx context.Context, params []byte) ([]byte, error) {
		var p PublishEventParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		id := impl.PublishEvent(p.Key, p.EventType, p.Payload)
		return wire.Encode(PublishEventResult{EventID: id})
	})

	c.Handle(MethodRunShellCommand, func(ctx context.Context, params []byte) ([]byte, error) {
		var p RunShellCommandParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		out, err := impl.RunShellCommand(ctx, p.Input)
		if err != nil {
			return nil, err
		}
		return wire.Encode(RunShellCommandResult{Output: out})
	})

	c.Handle(MethodSubmitPrompt, func(ctx context.Context, params []byte) ([]byte, error) {
		var p SubmitPromptParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		// The RPC handler's ctx is the connection ctx — alive for the
		// whole conn and unrelated to per-channel cancellation. Mint a
		// channel-keyed ctx so a later CancelTab(p.ChannelID) over the
		// wire actually reaches the minister's prompt loop.
		prompt := &court.Prompt{
			Ctx:          impl.CancellableStreamCtx(p.ChannelID),
			Message:      p.Message,
			EdictKey:     p.EdictKey,
			ChannelID:    p.ChannelID,
			ContextFiles: p.ContextFiles,
		}
		if err := impl.SubmitPrompt(p.TargetID, prompt); err != nil {
			return nil, err
		}
		return nil, nil
	})

	c.Handle(MethodRestoreMinisterSess, func(ctx context.Context, params []byte) ([]byte, error) {
		var p RestoreMinisterSessionParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		if err := impl.RestoreMinisterSession(p.TabType, p.Messages, p.ChannelID); err != nil {
			return nil, err
		}
		return nil, nil
	})

	c.Handle(MethodTakeSnapshot, func(ctx context.Context, _ []byte) ([]byte, error) {
		return wire.Encode(TakeSnapshotResult{Snapshot: impl.TakeSnapshot()})
	})

	c.Handle(MethodCancelTab, func(ctx context.Context, params []byte) ([]byte, error) {
		var p CancelTabParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		impl.CancelTab(p.ChannelID)
		return nil, nil
	})

	c.Handle(MethodPauseRitual, func(ctx context.Context, params []byte) ([]byte, error) {
		var p PauseRitualParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		return wire.Encode(PauseRitualResult{Paused: impl.PauseRitual(p.ChannelID)})
	})

	c.Handle(MethodResumeRitual, func(ctx context.Context, params []byte) ([]byte, error) {
		var p ResumeRitualParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		return wire.Encode(ResumeRitualResult{Resumed: impl.ResumeRitual(p.ChannelID)})
	})

	c.Handle(MethodGetSessionExport, func(ctx context.Context, params []byte) ([]byte, error) {
		var p GetSessionExportParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		exp, err := impl.GetSessionExport(p.TabTarget)
		if err != nil {
			return nil, err
		}
		return wire.Encode(GetSessionExportResult{Export: exp})
	})
}

// ServeCourtNotifications drives the server-side notification pump:
// subscribes to impl and forwards every message on conn as a wire
// notification. Blocks until ctx is done.
func ServeCourtNotifications(ctx context.Context, conn *Conn, impl courtapi.Client) {
	events := impl.Subscribe(ctx)
	PumpNotifications(ctx, conn, events)
}
