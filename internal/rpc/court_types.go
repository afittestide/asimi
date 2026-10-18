package rpc

import (
	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
)

// Wire DTOs for each Court RPC method. Every input/output struct
// maps 1:1 to a method, even when the method takes or returns a single
// value — keeps the on-wire schema self-describing and easy to extend.

type PingResult struct {
	Ok bool `msgpack:"ok"`
}

type HasMinisterParams struct {
	ID string `msgpack:"id"`
}
type HasMinisterResult struct {
	Has bool `msgpack:"has"`
}

type ResetMinisterSessionParams struct {
	ID        string `msgpack:"id"`
	ChannelID string `msgpack:"channel_id,omitempty"`
}

type EdictKeyParams struct {
	EdictID uint `msgpack:"edict_id"`
}
type EdictKeyResult struct {
	Key storage.EdictKey `msgpack:"key"`
}

type CourtEdictKeyResult struct {
	Key storage.EdictKey `msgpack:"key"`
}

type CreateEdictParams struct {
	IssueRef  string `msgpack:"issue_ref,omitempty"`
	Intent    string `msgpack:"intent,omitempty"`
	SessionID string `msgpack:"session_id,omitempty"`
}
type CreateEdictResult struct {
	Edict *storage.Edict `msgpack:"edict"`
}

type GetEdictParams struct {
	EdictID uint `msgpack:"edict_id"`
}
type GetEdictResult struct {
	Edict *storage.Edict `msgpack:"edict"`
}

type CancelEdictParams struct {
	EdictID uint `msgpack:"edict_id"`
}

type AppendToIntentParams struct {
	EdictID       uint   `msgpack:"edict_id"`
	Clarification string `msgpack:"clarification,omitempty"`
}

type SetIntentParams struct {
	EdictID uint   `msgpack:"edict_id"`
	Intent  string `msgpack:"intent,omitempty"`
}

type GrantRulerSealParams struct {
	EdictID uint   `msgpack:"edict_id"`
	Notes   string `msgpack:"notes,omitempty"`
}

type ListActiveEdictsResult struct {
	Edicts []storage.ActiveEdict `msgpack:"edicts"`
}

type ListModelsParams struct {
	Provider string `msgpack:"provider"`
}
type ListModelsResult struct {
	Response *schemas.BifrostListModelsResponse `msgpack:"response"`
}

type ApproximateContextSizeParams struct {
	Provider string `msgpack:"provider"`
	Model    string `msgpack:"model"`
}
type ApproximateContextSizeResult struct {
	Size int `msgpack:"size"`
}

type CancelZhengmingParams struct {
	RequestID string `msgpack:"request_id"`
}

type HandleZhengmingParams struct {
	RequestID string `msgpack:"request_id"`
	Answer    string `msgpack:"answer"`
}

type AllowRunnerFallbackParams struct {
	Allow bool `msgpack:"allow"`
}

type ClearSessionHistoryParams struct {
	TabTarget string `msgpack:"tab_target"`
}

type RollbackSessionParams struct {
	TabTarget string `msgpack:"tab_target"`
	Snapshot  int    `msgpack:"snapshot"`
}

type AddSessionMessageParams struct {
	TabTarget string `msgpack:"tab_target"`
	Role      string `msgpack:"role"`
	Content   string `msgpack:"content"`
}

type AddSessionContextFileParams struct {
	TabTarget string `msgpack:"tab_target"`
	Path      string `msgpack:"path"`
	Content   string `msgpack:"content"`
}

type CompactSessionParams struct {
	TabTarget string `msgpack:"tab_target"`
	Prompt    string `msgpack:"prompt"`
}
type CompactSessionResult struct {
	Summary string `msgpack:"summary"`
}

type SessionStateParams struct {
	TabTarget string `msgpack:"tab_target"`
}
type SessionStateResult struct {
	State court.SessionState `msgpack:"state"`
}

type GetEdictSealsParams struct {
	Key storage.EdictKey `msgpack:"key"`
}
type GetEdictSealsResult struct {
	Seals []storage.Seal `msgpack:"seals"`
}

type PublishEventParams struct {
	Key       storage.EdictKey   `msgpack:"key"`
	EventType storage.CourtEvent `msgpack:"event_type"`
	Payload   storage.JSON       `msgpack:"payload,omitempty"`
}
type PublishEventResult struct {
	EventID uint `msgpack:"event_id"`
}

type RunShellCommandParams struct {
	Input runners.Input `msgpack:"input"`
}
type RunShellCommandResult struct {
	Output runners.Output `msgpack:"output"`
}

// SubmitPromptParams mirrors court.Prompt but drops the Ctx field.
// The server rebuilds ctx from the connection/scope.
type SubmitPromptParams struct {
	TargetID     string            `msgpack:"target_id"`
	Message      string            `msgpack:"message"`
	EdictKey     storage.EdictKey  `msgpack:"edict_key"`
	ChannelID    string            `msgpack:"channel_id,omitempty"`
	ContextFiles map[string]string `msgpack:"context_files,omitempty"`
}

type RestoreMinisterSessionParams struct {
	TabType   string                `msgpack:"tab_type"`
	Messages  []schemas.ChatMessage `msgpack:"messages"`
	ChannelID string                `msgpack:"channel_id,omitempty"`
}

type TakeSnapshotResult struct {
	Snapshot court.Snapshot `msgpack:"snapshot"`
}

type CancelTabParams struct {
	ChannelID string `msgpack:"channel_id"`
}

type PauseRitualParams struct {
	ChannelID string `msgpack:"channel_id"`
}

type PauseRitualResult struct {
	Paused bool `msgpack:"paused"`
}

type ResumeRitualParams struct {
	ChannelID string `msgpack:"channel_id"`
}

type ResumeRitualResult struct {
	Resumed bool `msgpack:"resumed"`
}

type GetSessionExportParams struct {
	TabTarget string `msgpack:"tab_target"`
}
type GetSessionExportResult struct {
	Export *court.SessionExport `msgpack:"export"`
}

type EnsureEdictWorktreeParams struct {
	EdictID uint `msgpack:"edict_id"`
}
type EnsureEdictWorktreeResult struct {
	WorktreeRel string `msgpack:"worktree_rel"`
}
