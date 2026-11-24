package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	bifrostmcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToolState represents the classification lifecycle state of an MCP tool.
type ToolState string

const (
	ToolStateUnclassified  ToolState = "unclassified"
	ToolStateProposed      ToolState = "proposed"
	ToolStateSealed        ToolState = "sealed"
	ToolStateDenied        ToolState = "denied"
	ToolStatePreclassified ToolState = "preclassified"
)

// ToolStatus is a wire-safe snapshot of a single discovered MCP tool's
// classification and registration state.
type ToolStatus struct {
	Name           string    `msgpack:"name" json:"name"`
	RegisteredName string    `msgpack:"registered_name,omitempty" json:"registered_name,omitempty"`
	Description    string    `msgpack:"description,omitempty" json:"description,omitempty"`
	State          ToolState `msgpack:"state" json:"state"`
	Permissions    string    `msgpack:"permissions,omitempty" json:"permissions,omitempty"`
	SchemaHash     string    `msgpack:"schema_hash,omitempty" json:"schema_hash,omitempty"`
	Registered     bool      `msgpack:"registered" json:"registered"`
}

// ServerStatus is a wire-safe snapshot of a configured MCP server and its
// discovered tools.
type ServerStatus struct {
	ID      string       `msgpack:"id" json:"id"`
	Name    string       `msgpack:"name,omitempty" json:"name,omitempty"`
	State   string       `msgpack:"state" json:"state"`
	Command []string     `msgpack:"command,omitempty" json:"command,omitempty"`
	Tools   []ToolStatus `msgpack:"tools,omitempty" json:"tools,omitempty"`
	Error   string       `msgpack:"error,omitempty" json:"error,omitempty"`
}

// ToolDescriptor carries discovered MCP tool metadata for classification.
type ToolDescriptor struct {
	ServerID    string
	Name        string
	Description string
	Parameters  *schemas.ToolFunctionParameters
	SchemaHash  string
}

// ToolProposal holds a proposed Three Realms permission classification for an
// MCP tool before Ruler sealing.
type ToolProposal struct {
	ToolName    string `msgpack:"tool_name" json:"tool_name"`
	Permissions string `msgpack:"permissions" json:"permissions"`
	Rationale   string `msgpack:"rationale,omitempty" json:"rationale,omitempty"`
	SchemaHash  string `msgpack:"schema_hash,omitempty" json:"schema_hash,omitempty"`
}

// RitesProposer invokes the Minister of Rites to classify newly discovered or
// schema-changed MCP tools for a server into Three Realms permissions.
type RitesProposer func(ctx context.Context, serverID string, discovered []ToolDescriptor) ([]ToolProposal, error)

// ZhengmingGate presents proposed MCP tool classifications to the Ruler for
// review and sealing, returning approved permissions keyed by tool name.
type ZhengmingGate func(ctx context.Context, serverID string, proposals []ToolProposal) (map[string]string, error)

// MCPClientManager abstracts Bifrost's MCPManager for bridge orchestration and
// testing. *bifrostmcp.MCPManager satisfies this interface directly.
type MCPClientManager interface {
	GetClients() []schemas.MCPClientState
	GetToolPerClient(ctx context.Context) map[string][]schemas.ChatTool
	ExecuteChatTool(ctx *schemas.BifrostContext, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError)
	Cleanup() error
}

// Compile-time check that Bifrost's *MCPManager satisfies MCPClientManager.
var _ MCPClientManager = (*bifrostmcp.MCPManager)(nil)

// SchemaHash computes a deterministic hex-encoded SHA-256 digest over canonical
// JSON of a tool's name, description, and parameter schema.
//
// ToolFunctionParameters preserves input key ordering via OrderedMap and
// keyOrder; round-tripping through encoding/json into generic maps ensures
// object keys at every nesting depth are sorted lexicographically so
// semantically identical schemas always produce the same hash.
func SchemaHash(name, description string, params *schemas.ToolFunctionParameters) string {
	var canonicalParams any
	if params != nil {
		if raw, err := json.Marshal(params); err == nil {
			_ = json.Unmarshal(raw, &canonicalParams)
		}
	}
	payload := map[string]any{
		"name":        name,
		"description": description,
		"parameters":  canonicalParams,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		sum := sha256.Sum256([]byte(name + "\x00" + description))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// CommandHash computes a deterministic hex-encoded SHA-256 digest over the
// canonical JSON encoding of a server's stdio command and arguments.
// Cross-project cache reuse requires both schema_hash and command_hash to
// match so a different binary under the same server_id cannot inherit another
// server's sealed permissions.
func CommandHash(command []string) string {
	if len(command) == 0 {
		return ""
	}
	canonical, err := json.Marshal(command)
	if err != nil {
		sum := sha256.Sum256([]byte(command[0]))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
