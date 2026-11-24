package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/storage"
)

// ProposalKindMCPClassification is the registered proposal kind used by the
// Minister of Rites to submit MCP tool classification tables through the
// general propose tool.
const ProposalKindMCPClassification = "mcp_classification"

// ClassificationProposalPayload is the structured JSON payload accepted by the
// "mcp_classification" ProposalHandler.
type ClassificationProposalPayload struct {
	ServerID  string         `json:"server_id"`
	Proposals []ToolProposal `json:"proposals"`
}

// ClassificationProposalHandler integrates MCP tool classification with the
// Court's general propose tool. It validates 9-char rwx permissions, server
// and tool names, and schema hashes before bothering the Ruler, and commits
// seals ONLY when the Court's HandleZhengmingResponse invokes Apply on Ruler
// approval.
type ClassificationProposalHandler struct {
	BridgeGetter func() *Bridge
}

var (
	_ tools.ProposalHandler       = (*ClassificationProposalHandler)(nil)
	_ tools.ProposalSubmitHandler = (*ClassificationProposalHandler)(nil)
	_ tools.ProposalRejectHandler = (*ClassificationProposalHandler)(nil)
)

func (h *ClassificationProposalHandler) Kind() string {
	return ProposalKindMCPClassification
}

func (h *ClassificationProposalHandler) Allowed(ministerID string, _ tools.Permissions) bool {
	return ministerID == "rites"
}

func (h *ClassificationProposalHandler) Validate(_ context.Context, raw json.RawMessage) (string, error) {
	bridge := h.bridge()
	if bridge == nil {
		return "", fmt.Errorf("no active MCP bridge")
	}
	payload, err := parseClassificationPayload(raw)
	if err != nil {
		return "", err
	}
	return bridge.ValidateClassificationPayload(payload.ServerID, payload.Proposals)
}

func (h *ClassificationProposalHandler) OnSubmitted(_ context.Context, _ string, _ storage.EdictKey, raw json.RawMessage) {
	bridge := h.bridge()
	if bridge == nil {
		return
	}
	if payload, err := parseClassificationPayload(raw); err == nil {
		bridge.MarkProposed(payload.ServerID, payload.Proposals)
	}
}

func (h *ClassificationProposalHandler) Apply(ctx context.Context, _ storage.EdictKey, raw json.RawMessage) error {
	bridge := h.bridge()
	if bridge == nil {
		return fmt.Errorf("no active MCP bridge")
	}
	payload, err := parseClassificationPayload(raw)
	if err != nil {
		return err
	}
	return bridge.ApplySealedClassifications(ctx, payload.ServerID, payload.Proposals)
}

func (h *ClassificationProposalHandler) OnReject(_ context.Context, _ storage.EdictKey, raw json.RawMessage) {
	bridge := h.bridge()
	if bridge == nil {
		return
	}
	if payload, err := parseClassificationPayload(raw); err == nil {
		bridge.MarkRejected(payload.ServerID, payload.Proposals)
	}
}

func (h *ClassificationProposalHandler) bridge() *Bridge {
	if h == nil || h.BridgeGetter == nil {
		return nil
	}
	return h.BridgeGetter()
}

func parseClassificationPayload(raw json.RawMessage) (ClassificationProposalPayload, error) {
	var payload ClassificationProposalPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ClassificationProposalPayload{}, fmt.Errorf("invalid mcp_classification payload: %w", err)
	}
	payload.ServerID = strings.TrimSpace(payload.ServerID)
	if payload.ServerID == "" {
		return ClassificationProposalPayload{}, fmt.Errorf("server_id is required")
	}
	if len(payload.Proposals) == 0 {
		return ClassificationProposalPayload{}, fmt.Errorf("proposals must be non-empty")
	}
	return payload, nil
}

// ValidateClassificationPayload validates a multi-tool classification proposal
// against currently discovered tools on serverID and renders a human-readable
// review table for the Ruler.
func (b *Bridge) ValidateClassificationPayload(serverID string, proposals []ToolProposal) (string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	srv, exists := b.serversByID[serverID]
	if !exists {
		return "", fmt.Errorf("unknown MCP server %q", serverID)
	}
	clientName := b.serverIDToClientName[serverID]

	seen := make(map[string]bool, len(proposals))
	var rows []string
	for i, p := range proposals {
		normalized := stripClientOrServerPrefix(strings.TrimSpace(p.ToolName), clientName, serverID)
		if normalized == "" {
			return "", fmt.Errorf("proposals[%d]: tool_name is required", i)
		}
		if seen[normalized] {
			return "", fmt.Errorf("proposals[%d]: duplicate tool %q", i, normalized)
		}
		seen[normalized] = true

		key := toolKey{serverID: serverID, toolName: normalized}
		dt, ok := b.discovered[key]
		if !ok {
			return "", fmt.Errorf("proposals[%d]: tool %q is not discovered on server %q", i, normalized, serverID)
		}

		perm, err := tools.ParsePermissions(strings.TrimSpace(p.Permissions))
		if err != nil {
			return "", fmt.Errorf("proposals[%d] (%s): invalid permissions %q: %w", i, normalized, p.Permissions, err)
		}

		if strings.TrimSpace(p.SchemaHash) == "" {
			return "", fmt.Errorf("proposals[%d] (%s): schema_hash is required", i, normalized)
		}
		if p.SchemaHash != dt.schemaHash {
			return "", fmt.Errorf("proposals[%d] (%s): schema_hash %q does not match current %q", i, normalized, p.SchemaHash, dt.schemaHash)
		}

		permLabel := perm.String()
		if perm.IsEmpty() {
			permLabel = permLabel + " (SEALED DENY)"
		}
		shortHash := dt.schemaHash
		if len(shortHash) > 12 {
			shortHash = shortHash[:12]
		}
		rationale := strings.ReplaceAll(strings.TrimSpace(p.Rationale), "\n", " ")
		if rationale == "" {
			rationale = "-"
		}
		rows = append(rows, fmt.Sprintf("| `%s` | `%s` | `%s` | %s |", normalized, permLabel, shortHash, rationale))
	}

	var doc strings.Builder
	fmt.Fprintf(&doc, "## MCP Tool Classification Proposal — Server `%s`\n\n", serverID)
	fmt.Fprintf(&doc, "- **Command:** `%s`\n", strings.Join(srv.Command, " "))
	if len(srv.Env) > 0 {
		fmt.Fprintf(&doc, "- **Env:** `%s`\n", strings.Join(srv.Env, ", "))
	}
	doc.WriteString("\n| Tool | Permissions (Earth/Heaven/Intent) | Schema Hash | Rationale |\n")
	doc.WriteString("|---|---|---|---|\n")
	for _, row := range rows {
		doc.WriteString(row)
		doc.WriteByte('\n')
	}
	return doc.String(), nil
}

// MarkProposed updates in-memory tool status to ToolStateProposed for tools
// whose proposal has been queued as a Zhengming request.
func (b *Bridge) MarkProposed(serverID string, proposals []ToolProposal) {
	b.mu.Lock()
	defer b.mu.Unlock()

	clientName := b.serverIDToClientName[serverID]
	for _, p := range proposals {
		normalized := stripClientOrServerPrefix(strings.TrimSpace(p.ToolName), clientName, serverID)
		key := toolKey{serverID: serverID, toolName: normalized}
		dt, ok := b.discovered[key]
		if !ok || (p.SchemaHash != "" && p.SchemaHash != dt.schemaHash) {
			continue
		}
		permStr := strings.TrimSpace(p.Permissions)
		if run, exists := b.inFlight[key]; exists && run.schemaHash == dt.schemaHash {
			run.state = ToolStateProposed
			run.proposedPerm = permStr
		} else {
			b.inFlight[key] = &classificationRun{
				schemaHash:   dt.schemaHash,
				state:        ToolStateProposed,
				proposedPerm: permStr,
			}
		}
		b.updateToolStatusLocked(serverID, normalized, dt.schemaHash, ToolStateProposed, permStr, false)
	}
}

// ApplySealedClassifications persists Ruler-approved tool classifications with
// both schema_hash and command_hash, registers non-deny tools into ToolRegistry
// with strict subset matching, keeps "---------" tools quarantined in
// ToolStateDenied, and notifies listeners if registrations changed.
func (b *Bridge) ApplySealedClassifications(_ context.Context, serverID string, proposals []ToolProposal) error {
	b.mu.RLock()
	if b.stopping || b.manager == nil {
		b.mu.RUnlock()
		return fmt.Errorf("MCP bridge is not running")
	}
	clientName := b.serverIDToClientName[serverID]
	cmdHash := b.commandHashForServer(serverID)
	type resolvedItem struct {
		dt   discoveredTool
		perm tools.Permissions
	}
	items := make([]resolvedItem, 0, len(proposals))
	for i, p := range proposals {
		normalized := stripClientOrServerPrefix(strings.TrimSpace(p.ToolName), clientName, serverID)
		key := toolKey{serverID: serverID, toolName: normalized}
		dt, ok := b.discovered[key]
		if !ok {
			b.mu.RUnlock()
			return fmt.Errorf("tool %q is no longer discovered on server %q", normalized, serverID)
		}
		if p.SchemaHash != "" && p.SchemaHash != dt.schemaHash {
			b.mu.RUnlock()
			return fmt.Errorf("tool %q schema changed before approval", normalized)
		}
		perm, err := tools.ParsePermissions(strings.TrimSpace(p.Permissions))
		if err != nil {
			b.mu.RUnlock()
			return fmt.Errorf("proposals[%d] (%s): invalid permissions %q: %w", i, normalized, p.Permissions, err)
		}
		items = append(items, resolvedItem{dt: dt, perm: perm})
	}
	b.mu.RUnlock()

	if b.db != nil && b.username != "" {
		for _, item := range items {
			if err := storage.SaveMCPToolClassification(b.db, &storage.MCPToolClassification{
				Username:    b.username,
				Project:     b.project,
				ServerID:    serverID,
				ToolName:    item.dt.toolName,
				Permissions: item.perm.String(),
				SchemaHash:  item.dt.schemaHash,
				CommandHash: cmdHash,
				SealedBy:    b.username,
			}); err != nil {
				return fmt.Errorf("persist classification for %s/%s: %w", serverID, item.dt.toolName, err)
			}
		}
	}

	var (
		registryChanged bool
		onChanged       func()
	)
	b.mu.Lock()
	if b.stopping || b.manager == nil {
		b.mu.Unlock()
		return fmt.Errorf("MCP bridge stopped during approval")
	}
	for _, item := range items {
		key := toolKey{serverID: serverID, toolName: item.dt.toolName}
		cur, exists := b.discovered[key]
		if !exists || cur.schemaHash != item.dt.schemaHash {
			continue
		}

		delete(b.inFlight, key)
		delete(b.rejected, key)
		delete(b.failed, key)
		b.sealed[key] = sealedClassification{schemaHash: item.dt.schemaHash, perm: item.perm}

		if item.perm.IsEmpty() {
			if _, wasReg := b.registeredTools[item.dt.registeredName]; wasReg {
				if b.registry != nil {
					b.registry.Unregister(item.dt.registeredName)
				}
				delete(b.registeredTools, item.dt.registeredName)
				registryChanged = true
			}
			b.updateToolStatusLocked(serverID, item.dt.toolName, item.dt.schemaHash, ToolStateDenied, item.perm.String(), false)
			continue
		}

		registered := false
		if b.registry != nil {
			if b.canRegisterNameLocked(item.dt.registeredName) {
				adapter := b.newAdapter(item.dt, item.perm, b.manager)
				if err := b.registry.SetMCP(adapter, item.perm); err != nil {
					b.logger.Warn("skipping sealed MCP tool registration",
						"server_id", serverID, "tool", item.dt.registeredName, "error", err)
				} else {
					b.registeredTools[item.dt.registeredName] = registeredToolInfo{
						serverID:   serverID,
						toolName:   item.dt.toolName,
						schemaHash: item.dt.schemaHash,
						perm:       item.perm,
					}
					registered = true
					registryChanged = true
				}
			} else {
				b.logger.Warn("skipping sealed MCP tool due to name collision with existing registry tool",
					"server_id", serverID, "tool", item.dt.registeredName)
			}
		}
		b.updateToolStatusLocked(serverID, item.dt.toolName, item.dt.schemaHash, ToolStateSealed, item.perm.String(), registered)
	}
	onChanged = b.onRegistryChanged
	b.mu.Unlock()

	if registryChanged && onChanged != nil {
		onChanged()
	}
	return nil
}

// MarkRejected records that the Ruler rejected a pending classification
// proposal so the affected tools remain withheld without re-prompting until
// their schema or command changes.
func (b *Bridge) MarkRejected(serverID string, proposals []ToolProposal) {
	b.mu.Lock()
	defer b.mu.Unlock()

	clientName := b.serverIDToClientName[serverID]
	for _, p := range proposals {
		normalized := stripClientOrServerPrefix(strings.TrimSpace(p.ToolName), clientName, serverID)
		key := toolKey{serverID: serverID, toolName: normalized}
		dt, ok := b.discovered[key]
		if !ok || (p.SchemaHash != "" && p.SchemaHash != dt.schemaHash) {
			continue
		}
		delete(b.inFlight, key)
		delete(b.failed, key)
		b.rejected[key] = dt.schemaHash
		b.updateToolStatusLocked(serverID, normalized, dt.schemaHash, ToolStateUnclassified, "", false)
	}
}
