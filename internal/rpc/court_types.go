package rpc

import (
	"fmt"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
)

// Wire DTOs for each Court RPC method. Every input/output struct
// maps 1:1 to a method, even when the method takes or returns a single
// value — keeps the on-wire schema self-describing and easy to extend.

type PingResult struct {
	Ok bool `msgpack:"ok"`
	// InstanceID identifies the daemon process that answered. A client
	// uses it to tell a transient socket drop (same process) from a
	// restart (new process, all in-flight state lost). Empty for an
	// in-process Court, which has no process restart to distinguish.
	InstanceID string `msgpack:"instance_id,omitempty"`
	// Protocol is the RPC protocol the answering build speaks. Zero
	// means the daemon is older than protocol declarations (edict 1056)
	// and cannot be assumed compatible.
	Protocol int `msgpack:"protocol"`
	// Version is the answering build's AsimiVersion — informational;
	// a different version alone is not a compatibility break.
	Version string `msgpack:"version,omitempty"`
}

// CurrentProtocol is the version of the Court RPC method surface this
// build speaks. Bump it whenever a method is added, removed, or its
// wire types change. A client refuses to adopt a daemon whose protocol
// differs — adopting it would turn every new method call into a
// permanent CodeUnknownMethod failure (edict 1056). A pure build
// difference (same protocol, different version) is fine.
const CurrentProtocol = 1

// ProtocolCompatible reports whether a PingResult describes a daemon
// this build can talk to: same protocol, and one was declared at all.
// Unknown (0) is incompatible — "absent" must never read as "allow".
func ProtocolCompatible(res PingResult) bool {
	return res.Protocol == CurrentProtocol
}

// ProtocolMismatchError describes a daemon that answers Ping but speaks
// a protocol this build does not.
type ProtocolMismatchError struct {
	Got      int
	Want     int
	Version  string
	Instance string
}

func (e *ProtocolMismatchError) Error() string {
	who := "an older daemon"
	if e.Version != "" {
		who = "daemon " + e.Version
	}
	if e.Got == 0 {
		return fmt.Sprintf("%s at the socket predates protocol declaration (this build speaks protocol %d); refusing to adopt it", who, e.Want)
	}
	return fmt.Sprintf("%s speaks protocol %d, this build speaks %d; refusing to adopt it", who, e.Got, e.Want)
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

type GrantIntentSealParams struct {
	EdictID    uint         `msgpack:"edict_id"`
	MinisterID string       `msgpack:"minister_id"`
	Metadata   storage.JSON `msgpack:"metadata,omitempty"`
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

type DismissZhengmingParams struct {
	RequestID string `msgpack:"request_id"`
	Reason    string `msgpack:"reason"`
}

type GetZhengmingParams struct {
	RequestID string `msgpack:"request_id"`
}

type GetZhengmingResult struct {
	Request *storage.Zhengming `msgpack:"request"`
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

type SessionSeamsResult struct {
	Seams []config.Seam `msgpack:"seams"`
}

type ApplyMandateParams struct {
	Selections config.Selections `msgpack:"selections"`
}
