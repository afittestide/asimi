// Package courtapi defines the interface the TUI uses to talk to the
// Court coordinator. Today *court.Court satisfies it directly in
// the same process. In later phases an RPC client will satisfy it and the TUI
// will be unaware of the transport.
package courtapi

import (
	"context"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
)

// Client is everything the TUI needs from the Court.
//
// Every method below is designed so its parameters and return values can
// travel over a MessagePack wire unchanged. GetMinister and ConfigureModel
// are the remaining exceptions: they hand back or consume non-marshallable
// types (Minister interface, bifrost.LLMProvider) and are kept in-process
// only pending a session/model refactor of their own.
type Client interface {
	// Scope.
	EdictKey(edictID uint) storage.EdictKey
	CourtEdictKey() storage.EdictKey

	// Minister presence and reset. Fully wire-safe.
	HasMinister(id string) bool
	ResetMinisterSession(id string, channelID ...string)

	// In-process only; needed by getCurrentSession(). Scheduled for a
	// future session-narrowing phase.
	GetMinister(id string) court.Minister

	// Edicts and events.
	CreateEdict(issueRef, intent, sessionID string) (*storage.Edict, error)
	CreateEdictSilent(issueRef, intent, sessionID string) (*storage.Edict, error)
	GetEdict(edictID uint) (*storage.Edict, error)
	CancelEdict(edictID uint) error
	AppendToIntent(edictID uint, clarification string) error
	SetIntent(edictID uint, intent string) error
	PublishEvent(key storage.EdictKey, eventType storage.CourtEvent, payload storage.JSON) uint

	// Seals.
	GrantRulerSeal(edictID uint, notes string) error
	GetEdictSeals(key storage.EdictKey) ([]storage.Seal, error)
	ListActiveEdicts() ([]storage.ActiveEdict, error)

	// Prompts and sessions.
	SubmitPrompt(targetID string, p *court.Prompt) error
	RestoreMinisterSession(tabType string, msgs []schemas.ChatMessage, channelID ...string) error

	// Session state (wire-safe bulk read + targeted mutators).
	SessionState(tabTarget string) court.SessionState
	AddSessionContextFile(tabTarget, path, content string) error
	AddSessionMessage(tabTarget, role, content string) error
	ClearSessionHistory(tabTarget string) error
	RollbackSession(tabTarget string, snapshot int) error
	CompactSession(ctx context.Context, tabTarget, prompt string) (string, error)
	GetSessionExport(tabTarget string) (*court.SessionExport, error)

	// Zhengming.
	HandleZhengmingResponse(ctx context.Context, requestID, answer string) error
	CancelZhengming(requestID string)

	// Shell runner.
	AllowRunnerFallback(allow bool)
	RunShellCommand(ctx context.Context, input runners.Input) (runners.Output, error)

	// SetContext sends client-side credentials and project context to
	// the court. In daemon mode this travels over the wire; in
	// single-process mode it initialises Bifrost inline. Idempotent —
	// each call reconfigures the LLM client.
	SetContext(ctx context.Context, params types.SetContextParams) error

	// ListModels lists models for a specific provider via the court's LLM
	// client (bifrost). Used by the TUI's :models command.
	ListModels(provider string) (*schemas.BifrostListModelsResponse, error)

	// ApproximateContextSize resolves an approximate context window for a
	// provider:model from the court's regex registry. Returns 0 when unknown.
	// Used for providers whose model listing carries no context length; the
	// TUI renders a non-zero result as an estimate, never as an exact value.
	ApproximateContextSize(provider, model string) int

	// In-process only; LLM client setup. Kept for in-process callers;
	// the wire-safe path is SetContext above.
	//
	// Deprecated: use SetContext. ConfigureModel only works in the
	// same process because it takes a bifrost.LLMProvider pointer.
	ConfigureModel(client court.LLMProvider, config *court.SessionConfig, repoInfo repo.RepoInfo)

	// Snapshots for the court debug view.
	TakeSnapshot() court.Snapshot

	// CancelTab cancels any in-flight work registered under the given
	// channel (tab). Wire-safe; the server-side Court maintains a
	// per-channel cancel registry populated by CancellableStreamCtx.
	CancelTab(channelID string)

	// PauseRitual pauses the ritual running on channelID for ruler
	// interjection. The step's LLM stream is interrupted but the
	// ritual goroutine stays alive, waiting for ResumeRitual.
	PauseRitual(channelID string) bool

	// ResumeRitual resumes a paused ritual on channelID.
	ResumeRitual(channelID string) bool

	// CancellableStreamCtx mints a context registered under channelID
	// in the server-side cancel registry. Server-side only — used by
	// the RPC SubmitPrompt handler to give each prompt a ctx that a
	// later CancelTab(channelID) can actually cancel. The TUI-side
	// LoopbackCourt has no use for this and delegates to its local
	// court purely to satisfy the interface.
	CancellableStreamCtx(channelID string) context.Context

	// Subscribe returns a channel that carries every TUI-bound notification:
	// streaming chunks, court events, and runner messages.
	Subscribe(ctx context.Context) <-chan any

	// ConnDone returns a channel that is closed when the underlying
	// transport connection drops. For in-process implementations it
	// returns a channel that is never closed. The TUI watches it to
	// detect mid-stream connection drops and trigger auto-retry.
	ConnDone() <-chan struct{}
}

var _ Client = (*court.Court)(nil)
