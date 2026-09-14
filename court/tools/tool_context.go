package tools

import (
	"context"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/storage"
	"gorm.io/gorm"
)

// SessionIDKey is a context key for propagating the executing session's ID
// to tools via context.Context. This allows tools (e.g. RequestZhengming) to
// know which session invoked them, even when called through the scheduler
// or from a ritual step where the minister's own interactive session differs.
type SessionIDKey struct{}

// SessionIDFromContext extracts the session ID from the context, or returns
// "" if none is set.
func SessionIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(SessionIDKey{}).(string); ok {
		return v
	}
	return ""
}

// ChannelIDKey is a context key for propagating the executing session's
// ChannelID to tools via context.Context. This allows tools (e.g.
// ConsultMinisterTool) to route streaming output to the correct tab,
// rather than defaulting to the minister's static ID.
type ChannelIDKey struct{}

// ChannelIDFromContext extracts the channel ID from the context, or returns
// "" if none is set.
func ChannelIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ChannelIDKey{}).(string); ok {
		return v
	}
	return ""
}

// ToolContext carries shared identity and daemon-level resources to all tools.
// RepoInfo is a pointer so all contexts see live state (ProjectRoot, Branch, etc.)
// MinisterID is the one field that varies per minister tool set.
type ToolContext struct {
	RepoInfo   *repo.RepoInfo // shared pointer — all contexts see live state
	MinisterID string         // per-minister: "forge", "chancellor", etc.
	Username   string         // daemon-level: OS username for DB scoping
	Project    string         // daemon-level: project slug for DB scoping
	DB         *gorm.DB       // daemon-level singleton

	// OutputLimit bounds heavy output fields (e.g. last_step_output) in
	// summary mode (court-level setting).
	// The number is for how many characters from the end of the fields to store
	OutputLimit int
}

// ProjectRoot returns the project root from the shared RepoInfo, or "" if unset.
func (tc ToolContext) ProjectRoot() string {
	if tc.RepoInfo != nil {
		return tc.RepoInfo.ProjectRoot
	}
	return ""
}

// RitualLauncher starts ritual workflows (implemented by MinisterBase).
type RitualLauncher interface {
	StartRitual(name string, key storage.EdictKey, inputs map[string]string) error
}

// MinisterConsultant dispatches work to a minister (implemented by MinisterBase).
// callerMinisterID is the minister ID of the caller (used for session lookup and
// self-consult guard), and channelID is the routing target for streaming output.
type MinisterConsultant interface {
	ConsultMinister(ctx context.Context, callerMinisterID, ministerID, channelID string, key storage.EdictKey, work string) (string, error)
}
