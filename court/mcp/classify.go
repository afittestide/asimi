package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
)

// Zhengming answer options for MCP tool classification approval gates.
const (
	AnswerApproveClassification = "Approve"
	AnswerAdjustClassification  = "Adjust permissions"
	AnswerRejectClassification  = tools.AnswerReject
)

// toolKey uniquely identifies a discovered tool on a configured MCP server.
type toolKey struct {
	serverID string
	toolName string
}

// registeredToolInfo tracks a tool currently registered in ToolRegistry by this
// Bridge so Reconcile can detect permission or schema changes accurately.
type registeredToolInfo struct {
	serverID   string
	toolName   string
	schemaHash string
	perm       tools.Permissions
}

// discoveredTool captures the normalized metadata of a tool reported by
// MCPClientManager.GetToolPerClient.
type discoveredTool struct {
	serverID       string
	clientName     string
	toolName       string // bare tool name (without client/server prefix)
	bifrostName    string // raw Function.Name returned by Bifrost
	registeredName string // namespaced name in ToolRegistry (<serverID>-<toolName>)
	description    string
	schemaHash     string
	chatTool       schemas.ChatTool
}

// classificationRun tracks an active async classification attempt for a tool
// so periodic sync deduplicates in-flight work and preserves ToolStateProposed.
type classificationRun struct {
	schemaHash   string
	state        ToolState
	proposedPerm string
	attemptID    uint64
	cancel       context.CancelFunc
}

// sealedClassification caches a recently sealed classification in memory to
// close the TOCTOU window between DB lookups outside b.mu and applying state
// under b.mu.
type sealedClassification struct {
	schemaHash string
	perm       tools.Permissions
}

type preparedBatch struct {
	serverID  string
	attemptID uint64
	ctx       context.Context
	cancel    context.CancelFunc
	items     []discoveredTool
}

// Reconcile polls live MCP tools from the underlying MCPClientManager and
// reconciles them against config pre-classifications, persisted DB seals,
// ToolRegistry, and deduplicated async Rites + Zhengming classification:
//
//  1. Config pre-classifications (per-tool override matching bare or prefixed
//     name, then server default_permissions) register directly via
//     ToolRegistry.Set with state preclassified without invoking Rites.
//  2. Otherwise, persisted classifications are looked up via
//     storage.LookupMCPToolClassification (current project first, falling back
//     to the most recently sealed matching schema_hash across projects for the
//     same username) and registered immediately with state sealed when matched.
//  3. Removed tools and schema-changed tools are unregistered immediately via
//     ToolRegistry.Unregister.
//  4. New or schema-changed unclassified tools are withheld from ToolRegistry
//     (state unclassified) and dispatched to deduplicated async classification
//     via RitesProposer -> state proposed -> ZhengmingGate ->
//     storage.SaveMCPToolClassification -> ToolRegistry.Set -> OnRegistryChanged.
func (b *Bridge) Reconcile(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	b.mu.RLock()
	mgr := b.manager
	stopping := b.stopping
	b.mu.RUnlock()
	if mgr == nil || stopping {
		return
	}

	clients := mgr.GetClients()
	toolsByClient := mgr.GetToolPerClient(ctx)

	clientStates := make(map[string]schemas.MCPConnectionState, len(clients))
	for _, c := range clients {
		serverID := b.resolveServerID(c)
		clientStates[serverID] = c.State
	}

	type resolvedEntry struct {
		dt      discoveredTool
		state   ToolState
		perm    tools.Permissions
		hasPerm bool
	}

	resolvedByServer := make(map[string][]resolvedEntry, len(toolsByClient))
	for clientName, chatTools := range toolsByClient {
		serverID := b.serverIDForClientName(clientName)
		if _, ok := clientStates[serverID]; !ok {
			clientStates[serverID] = schemas.MCPConnectionStateConnected
		}
		srvCfg := b.serversByID[serverID]

		entries := make([]resolvedEntry, 0, len(chatTools))
		for _, ct := range chatTools {
			if ct.Function == nil || ct.Function.Name == "" {
				continue
			}

			bifrostName := ct.Function.Name
			unprefixed := stripClientOrServerPrefix(bifrostName, clientName, serverID)
			registeredName := namespaceToolName(serverID, unprefixed)
			desc := ""
			if ct.Function.Description != nil {
				desc = *ct.Function.Description
			}
			hash := SchemaHash(unprefixed, desc, ct.Function.Parameters)

			state, perm, hasPerm := b.resolveToolClassification(
				srvCfg, serverID, unprefixed, bifrostName, registeredName, hash,
			)

			entries = append(entries, resolvedEntry{
				dt: discoveredTool{
					serverID:       serverID,
					clientName:     clientName,
					toolName:       unprefixed,
					bifrostName:    bifrostName,
					registeredName: registeredName,
					description:    desc,
					schemaHash:     hash,
					chatTool:       ct,
				},
				state:   state,
				perm:    perm,
				hasPerm: hasPerm,
			})
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].dt.toolName < entries[j].dt.toolName
		})
		resolvedByServer[serverID] = entries
	}

	var (
		staleCancels    []context.CancelFunc
		batches         []preparedBatch
		registryChanged bool
		onChanged       func()
	)

	b.mu.Lock()
	if b.stopping || b.manager == nil {
		b.mu.Unlock()
		return
	}

	newDiscovered := make(map[toolKey]discoveredTool)
	for _, entries := range resolvedByServer {
		for _, e := range entries {
			key := toolKey{serverID: e.dt.serverID, toolName: e.dt.toolName}
			newDiscovered[key] = e.dt
		}
	}

	// Clean up state for tools that disappeared or whose schema hash changed.
	for key, run := range b.inFlight {
		dt, exists := newDiscovered[key]
		if !exists || dt.schemaHash != run.schemaHash {
			if run.cancel != nil {
				staleCancels = append(staleCancels, run.cancel)
			}
			delete(b.inFlight, key)
		}
	}
	for key, sc := range b.sealed {
		dt, exists := newDiscovered[key]
		if !exists || dt.schemaHash != sc.schemaHash {
			delete(b.sealed, key)
		}
	}
	for key, rejHash := range b.rejected {
		dt, exists := newDiscovered[key]
		if !exists || dt.schemaHash != rejHash {
			delete(b.rejected, key)
		}
	}
	for key, failHash := range b.failed {
		dt, exists := newDiscovered[key]
		if !exists || dt.schemaHash != failHash {
			delete(b.failed, key)
		}
	}

	seenRegistered := make(map[string]registeredToolInfo)
	desiredToolsByServer := make(map[string][]ToolStatus, len(resolvedByServer))
	toClassifyByServer := make(map[string][]discoveredTool)

	for serverID, entries := range resolvedByServer {
		statuses := make([]ToolStatus, 0, len(entries))
		for _, e := range entries {
			dt := e.dt
			key := toolKey{serverID: dt.serverID, toolName: dt.toolName}
			state := e.state
			perm := e.perm
			hasPerm := e.hasPerm

			// Close TOCTOU window if an async classification sealed this exact
			// schemaHash after our DB lookup outside b.mu.
			if state == ToolStateUnclassified {
				if sc, ok := b.sealed[key]; ok && sc.schemaHash == dt.schemaHash {
					if sc.perm.IsEmpty() {
						state = ToolStateDenied
					} else {
						state = ToolStateSealed
					}
					perm = sc.perm
					hasPerm = true
				}
			}

			ts := ToolStatus{
				Name:           dt.toolName,
				RegisteredName: dt.registeredName,
				Description:    dt.description,
				State:          state,
				SchemaHash:     dt.schemaHash,
			}
			if hasPerm {
				ts.Permissions = perm.String()
			}

			switch state {
			case ToolStatePreclassified, ToolStateSealed, ToolStateDenied:
				// Cancel any redundant in-flight attempt if tool became classified.
				if run, ok := b.inFlight[key]; ok {
					if run.cancel != nil {
						staleCancels = append(staleCancels, run.cancel)
					}
					delete(b.inFlight, key)
				}
				delete(b.rejected, key)
				delete(b.failed, key)

				// Zero permissions ("---------") act as sealed deny and stay unregistered.
				if hasPerm && perm.IsEmpty() {
					if state == ToolStateSealed {
						ts.State = ToolStateDenied
					}
				} else if b.registry != nil && hasPerm {
					if b.canRegisterNameLocked(dt.registeredName) {
						adapter := b.newAdapter(dt, perm, mgr)
						if err := b.registry.SetMCP(adapter, perm); err != nil {
							b.logger.Warn("skipping MCP tool registration",
								"server_id", dt.serverID, "tool", dt.registeredName, "error", err)
						} else {
							info := registeredToolInfo{
								serverID:   dt.serverID,
								toolName:   dt.toolName,
								schemaHash: dt.schemaHash,
								perm:       perm,
							}
							prev, existed := b.registeredTools[dt.registeredName]
							if !existed || prev.schemaHash != info.schemaHash || prev.perm != info.perm {
								registryChanged = true
							}
							seenRegistered[dt.registeredName] = info
							ts.Registered = true
						}
					} else {
						b.logger.Warn("skipping MCP tool due to name collision with existing registry tool",
							"server_id", dt.serverID, "tool", dt.registeredName)
					}
				}

			case ToolStateUnclassified:
				// Withhold from ToolRegistry; deduplicate in-flight, rejected, and failed tools.
				if run, ok := b.inFlight[key]; ok && run.schemaHash == dt.schemaHash {
					ts.State = run.state
					ts.Permissions = run.proposedPerm
				} else if b.rejected[key] == dt.schemaHash {
					// Rejected by Ruler for this schemaHash; keep withheld without re-prompting.
				} else if b.failed[key] == dt.schemaHash {
					// Failed previously for this schemaHash; wait for RetryPendingClassifications.
				} else if b.proposer != nil {
					toClassifyByServer[dt.serverID] = append(toClassifyByServer[dt.serverID], dt)
				}
			}

			statuses = append(statuses, ts)
		}
		desiredToolsByServer[serverID] = statuses
	}

	// Immediately unregister removed tools, schema-changed tools that became
	// unclassified, and tools that transitioned to sealed deny.
	for name := range b.registeredTools {
		if _, keep := seenRegistered[name]; !keep {
			if b.registry != nil {
				b.registry.Unregister(name)
			}
			registryChanged = true
		}
	}
	b.registeredTools = seenRegistered
	b.discovered = newDiscovered

	for serverID, st := range b.servers {
		if state, ok := clientStates[serverID]; ok && state != "" {
			st.State = string(state)
		}
		st.Tools = desiredToolsByServer[serverID]
	}
	for serverID, statuses := range desiredToolsByServer {
		if _, exists := b.servers[serverID]; !exists {
			state := string(schemas.MCPConnectionStateConnected)
			if cs, ok := clientStates[serverID]; ok && cs != "" {
				state = string(cs)
			}
			b.servers[serverID] = &ServerStatus{
				ID:    serverID,
				Name:  serverID,
				State: state,
				Tools: statuses,
			}
		}
	}

	if len(toClassifyByServer) > 0 {
		batches = b.prepareBatchesLocked(ctx, toClassifyByServer)
	}
	onChanged = b.onRegistryChanged
	b.mu.Unlock()

	for _, c := range staleCancels {
		c()
	}
	for _, batch := range batches {
		go b.classifyBatch(batch)
	}
	if registryChanged && onChanged != nil {
		onChanged()
	}
}

// RetryPendingClassifications clears failed classification markers and retries
// deduplicated async classification for any currently discovered tools that
// remain unclassified (skipping tools already registered, in-flight, or
// explicitly rejected for their current schema_hash).
func (b *Bridge) RetryPendingClassifications(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	b.mu.Lock()
	if b.stopping || b.manager == nil {
		b.mu.Unlock()
		return
	}
	mgr := b.manager
	// Clear prior transient errors so failed unclassified tools can be retried.
	b.failed = make(map[toolKey]string)

	if len(b.discovered) == 0 {
		b.mu.Unlock()
		b.Reconcile(ctx)
		return
	}

	candidates := make([]discoveredTool, 0, len(b.discovered))
	for key, dt := range b.discovered {
		if reg, ok := b.registeredTools[dt.registeredName]; ok && reg.schemaHash == dt.schemaHash {
			continue
		}
		if sc, ok := b.sealed[key]; ok && sc.schemaHash == dt.schemaHash {
			continue
		}
		if run, ok := b.inFlight[key]; ok && run.schemaHash == dt.schemaHash {
			continue
		}
		if b.rejected[key] == dt.schemaHash {
			continue
		}
		candidates = append(candidates, dt)
	}
	b.mu.Unlock()

	if len(candidates) == 0 {
		return
	}

	type resolvedCandidate struct {
		dt      discoveredTool
		state   ToolState
		perm    tools.Permissions
		hasPerm bool
	}

	resolved := make([]resolvedCandidate, 0, len(candidates))
	for _, dt := range candidates {
		srvCfg := b.serversByID[dt.serverID]
		state, perm, hasPerm := b.resolveToolClassification(
			srvCfg, dt.serverID, dt.toolName, dt.bifrostName, dt.registeredName, dt.schemaHash,
		)
		resolved = append(resolved, resolvedCandidate{
			dt:      dt,
			state:   state,
			perm:    perm,
			hasPerm: hasPerm,
		})
	}

	var (
		batches         []preparedBatch
		registryChanged bool
		onChanged       func()
	)

	b.mu.Lock()
	if b.stopping || b.manager == nil {
		b.mu.Unlock()
		return
	}

	toClassifyByServer := make(map[string][]discoveredTool)
	for _, rc := range resolved {
		dt := rc.dt
		key := toolKey{serverID: dt.serverID, toolName: dt.toolName}

		cur, exists := b.discovered[key]
		if !exists || cur.schemaHash != dt.schemaHash {
			continue
		}

		switch rc.state {
		case ToolStatePreclassified, ToolStateSealed, ToolStateDenied:
			if (rc.state == ToolStateSealed || rc.state == ToolStateDenied) && rc.hasPerm {
				b.sealed[key] = sealedClassification{schemaHash: dt.schemaHash, perm: rc.perm}
			}
			registered := false
			if b.registry != nil && rc.hasPerm && !rc.perm.IsEmpty() {
				if b.canRegisterNameLocked(dt.registeredName) {
					adapter := b.newAdapter(dt, rc.perm, mgr)
					if err := b.registry.SetMCP(adapter, rc.perm); err != nil {
						b.logger.Warn("skipping MCP tool registration on retry",
							"server_id", dt.serverID, "tool", dt.registeredName, "error", err)
					} else {
						info := registeredToolInfo{
							serverID:   dt.serverID,
							toolName:   dt.toolName,
							schemaHash: dt.schemaHash,
							perm:       rc.perm,
						}
						prev, existed := b.registeredTools[dt.registeredName]
						if !existed || prev.schemaHash != info.schemaHash || prev.perm != info.perm {
							registryChanged = true
						}
						b.registeredTools[dt.registeredName] = info
						registered = true
					}
				}
			}
			permStr := ""
			if rc.hasPerm {
				permStr = rc.perm.String()
			}
			b.updateToolStatusLocked(dt.serverID, dt.toolName, dt.schemaHash, rc.state, permStr, registered)

		case ToolStateUnclassified:
			if run, ok := b.inFlight[key]; ok && run.schemaHash == dt.schemaHash {
				continue
			}
			if b.rejected[key] == dt.schemaHash {
				continue
			}
			if b.proposer != nil {
				toClassifyByServer[dt.serverID] = append(toClassifyByServer[dt.serverID], dt)
			}
		}
	}

	if len(toClassifyByServer) > 0 {
		batches = b.prepareBatchesLocked(ctx, toClassifyByServer)
	}
	onChanged = b.onRegistryChanged
	b.mu.Unlock()

	for _, batch := range batches {
		go b.classifyBatch(batch)
	}
	if registryChanged && onChanged != nil {
		onChanged()
	}
}

// prepareBatchesLocked registers in-flight classification entries and increments
// b.wg while holding b.mu so concurrent Reconcile/RetryPendingClassifications
// deduplicate cleanly and Stop() can safely cancel and wait on all goroutines.
func (b *Bridge) prepareBatchesLocked(parentCtx context.Context, byServer map[string][]discoveredTool) []preparedBatch {
	serverIDs := make([]string, 0, len(byServer))
	for id := range byServer {
		serverIDs = append(serverIDs, id)
	}
	sort.Strings(serverIDs)

	batches := make([]preparedBatch, 0, len(serverIDs))
	for _, serverID := range serverIDs {
		items := byServer[serverID]
		if len(items) == 0 {
			continue
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].toolName < items[j].toolName
		})

		b.nextAttemptID++
		attemptID := b.nextAttemptID
		attemptCtx, attemptCancel := context.WithCancel(parentCtx)

		for _, dt := range items {
			key := toolKey{serverID: dt.serverID, toolName: dt.toolName}
			b.inFlight[key] = &classificationRun{
				schemaHash: dt.schemaHash,
				state:      ToolStateUnclassified,
				attemptID:  attemptID,
				cancel:     attemptCancel,
			}
		}

		b.wg.Add(1)
		batches = append(batches, preparedBatch{
			serverID:  serverID,
			attemptID: attemptID,
			ctx:       attemptCtx,
			cancel:    attemptCancel,
			items:     items,
		})
	}
	return batches
}

func (b *Bridge) classifyBatch(batch preparedBatch) {
	defer b.wg.Done()
	defer batch.cancel()
	keepProposedInFlight := false
	defer func() {
		if keepProposedInFlight {
			b.finishNonProposedInFlight(batch)
			return
		}
		b.finishInFlight(batch)
	}()

	proposer := b.proposer
	gate := b.gate
	if proposer == nil {
		return
	}

	byToolName := make(map[string]discoveredTool, len(batch.items))
	descriptors := make([]ToolDescriptor, 0, len(batch.items))
	for _, item := range batch.items {
		byToolName[item.toolName] = item
		var params *schemas.ToolFunctionParameters
		if item.chatTool.Function != nil {
			params = item.chatTool.Function.Parameters
		}
		descriptors = append(descriptors, ToolDescriptor{
			ServerID:    batch.serverID,
			Name:        item.toolName,
			Description: item.description,
			Parameters:  params,
			SchemaHash:  item.schemaHash,
		})
	}

	proposals, err := proposer(batch.ctx, batch.serverID, descriptors)
	if err != nil {
		b.logger.Warn("MCP tool classification proposer failed",
			"server_id", batch.serverID, "error", err)
		b.markBatchFailed(batch, batch.items)
		return
	}

	// When no explicit Gate is configured, the proposer (Minister of Rites)
	// submits proposals directly through the general `propose` tool, whose
	// OnSubmitted hook marks tools ToolStateProposed and whose Apply/OnReject
	// hooks resolve them when HandleZhengmingResponse runs.
	if gate == nil {
		keepProposedInFlight = true
		var unsubmitted []discoveredTool
		b.mu.RLock()
		for _, item := range batch.items {
			key := toolKey{serverID: batch.serverID, toolName: item.toolName}
			if sc, ok := b.sealed[key]; ok && sc.schemaHash == item.schemaHash {
				continue
			}
			if b.rejected[key] == item.schemaHash {
				continue
			}
			if run, ok := b.inFlight[key]; ok && run.schemaHash == item.schemaHash && run.state == ToolStateProposed {
				continue
			}
			unsubmitted = append(unsubmitted, item)
		}
		b.mu.RUnlock()
		if len(unsubmitted) > 0 {
			b.markBatchFailed(batch, unsubmitted)
		}
		return
	}

	// Validate proposals from Rites strictly before presenting to the Ruler.
	validProposals := make([]ToolProposal, 0, len(proposals))
	proposedTools := make(map[string]bool, len(proposals))
	clientName := b.serverIDToClientName[batch.serverID]

	for _, p := range proposals {
		normalized := stripClientOrServerPrefix(p.ToolName, clientName, batch.serverID)
		item, ok := byToolName[normalized]
		if !ok {
			b.logger.Warn("ignoring proposal for unknown MCP tool",
				"server_id", batch.serverID, "tool", p.ToolName)
			continue
		}
		if proposedTools[item.toolName] {
			continue
		}
		perm, parseErr := tools.ParsePermissions(p.Permissions)
		if parseErr != nil {
			b.logger.Warn("ignoring proposal with invalid 9-char permissions",
				"server_id", batch.serverID, "tool", item.toolName, "permissions", p.Permissions, "error", parseErr)
			continue
		}
		if p.SchemaHash != "" && p.SchemaHash != item.schemaHash {
			b.logger.Warn("ignoring proposal with mismatched schema hash",
				"server_id", batch.serverID, "tool", item.toolName)
			continue
		}
		proposedTools[item.toolName] = true
		validProposals = append(validProposals, ToolProposal{
			ToolName:    item.toolName,
			Permissions: perm.String(),
			Rationale:   p.Rationale,
			SchemaHash:  item.schemaHash,
		})
	}

	// Any tool in the batch that did not receive a valid proposal stays
	// unclassified and is marked failed so RetryPendingClassifications can retry.
	var unproposed []discoveredTool
	for _, item := range batch.items {
		if !proposedTools[item.toolName] {
			unproposed = append(unproposed, item)
		}
	}
	if len(unproposed) > 0 {
		b.markBatchFailed(batch, unproposed)
	}
	if len(validProposals) == 0 {
		return
	}

	// Transition still-current tools to ToolStateProposed before invoking ZhengmingGate.
	activeProposals := make([]ToolProposal, 0, len(validProposals))
	b.mu.Lock()
	if b.stopping || b.manager == nil {
		b.mu.Unlock()
		return
	}
	for _, p := range validProposals {
		key := toolKey{serverID: batch.serverID, toolName: p.ToolName}
		if !b.isAttemptCurrentLocked(key, batch.attemptID, p.SchemaHash) {
			continue
		}
		if run, ok := b.inFlight[key]; ok {
			run.state = ToolStateProposed
			run.proposedPerm = p.Permissions
		}
		b.updateToolStatusLocked(batch.serverID, p.ToolName, p.SchemaHash, ToolStateProposed, p.Permissions, false)
		activeProposals = append(activeProposals, p)
	}
	b.mu.Unlock()

	if len(activeProposals) == 0 {
		return
	}

	approved, err := gate(batch.ctx, batch.serverID, activeProposals)
	if err != nil {
		b.logger.Warn("MCP tool classification Zhengming gate failed",
			"server_id", batch.serverID, "error", err)
		failedItems := make([]discoveredTool, 0, len(activeProposals))
		for _, p := range activeProposals {
			if item, ok := byToolName[p.ToolName]; ok {
				failedItems = append(failedItems, item)
			}
		}
		b.markBatchFailed(batch, failedItems)
		return
	}

	var (
		registryChanged bool
		onChanged       func()
	)

	for _, p := range activeProposals {
		item := byToolName[p.ToolName]
		key := toolKey{serverID: batch.serverID, toolName: p.ToolName}

		permStr, ok := lookupApprovedPermission(approved, item)
		if !ok || permStr == "" {
			// Ruler rejected this tool; keep it withheld for this schemaHash.
			b.mu.Lock()
			if b.isAttemptCurrentLocked(key, batch.attemptID, p.SchemaHash) {
				b.rejected[key] = p.SchemaHash
				delete(b.failed, key)
				b.updateToolStatusLocked(batch.serverID, p.ToolName, p.SchemaHash, ToolStateUnclassified, "", false)
			}
			b.mu.Unlock()
			continue
		}

		// Strict 9-char rwx validation on Ruler-approved/adjusted permissions.
		perm, parseErr := tools.ParsePermissions(permStr)
		if parseErr != nil {
			b.logger.Warn("rejecting invalid 9-char permissions returned by ZhengmingGate",
				"server_id", batch.serverID, "tool", p.ToolName, "permissions", permStr, "error", parseErr)
			b.markBatchFailed(batch, []discoveredTool{item})
			continue
		}

		// Check staleness before persisting to DB.
		b.mu.RLock()
		stillCurrent := !b.stopping && b.manager != nil && b.isAttemptCurrentLocked(key, batch.attemptID, p.SchemaHash)
		b.mu.RUnlock()
		if !stillCurrent {
			continue
		}

		if b.db != nil && b.username != "" {
			saveErr := storage.SaveMCPToolClassification(b.db, &storage.MCPToolClassification{
				Username:    b.username,
				Project:     b.project,
				ServerID:    batch.serverID,
				ToolName:    p.ToolName,
				Permissions: perm.String(),
				SchemaHash:  p.SchemaHash,
				CommandHash: b.commandHashForServer(batch.serverID),
				SealedBy:    b.username,
			})
			if saveErr != nil {
				b.logger.Error("failed to persist sealed MCP tool classification",
					"server_id", batch.serverID, "tool", p.ToolName, "error", saveErr)
				b.markBatchFailed(batch, []discoveredTool{item})
				continue
			}
		}

		b.mu.Lock()
		if b.stopping || b.manager == nil || !b.isAttemptCurrentLocked(key, batch.attemptID, p.SchemaHash) {
			b.mu.Unlock()
			continue
		}

		b.sealed[key] = sealedClassification{schemaHash: p.SchemaHash, perm: perm}
		delete(b.rejected, key)
		delete(b.failed, key)

		if perm.IsEmpty() {
			// Sealed deny ("---------"): persist as denied and keep withheld from ToolRegistry.
			if _, wasReg := b.registeredTools[item.registeredName]; wasReg {
				if b.registry != nil {
					b.registry.Unregister(item.registeredName)
				}
				delete(b.registeredTools, item.registeredName)
				registryChanged = true
			}
			b.updateToolStatusLocked(batch.serverID, p.ToolName, p.SchemaHash, ToolStateDenied, perm.String(), false)
		} else {
			registered := false
			if b.registry != nil {
				if b.canRegisterNameLocked(item.registeredName) {
					adapter := b.newAdapter(item, perm, b.manager)
					if err := b.registry.SetMCP(adapter, perm); err != nil {
						b.logger.Warn("skipping sealed MCP tool registration",
							"server_id", item.serverID, "tool", item.registeredName, "error", err)
					} else {
						b.registeredTools[item.registeredName] = registeredToolInfo{
							serverID:   item.serverID,
							toolName:   item.toolName,
							schemaHash: item.schemaHash,
							perm:       perm,
						}
						registered = true
						registryChanged = true
					}
				} else {
					b.logger.Warn("skipping sealed MCP tool due to name collision with existing registry tool",
						"server_id", item.serverID, "tool", item.registeredName)
				}
			}
			b.updateToolStatusLocked(batch.serverID, p.ToolName, p.SchemaHash, ToolStateSealed, perm.String(), registered)
		}
		onChanged = b.onRegistryChanged
		b.mu.Unlock()
	}

	if registryChanged && onChanged != nil {
		onChanged()
	}
}

func (b *Bridge) finishInFlight(batch preparedBatch) {
	var cancel context.CancelFunc
	b.mu.Lock()
	for _, item := range batch.items {
		key := toolKey{serverID: item.serverID, toolName: item.toolName}
		if run, ok := b.inFlight[key]; ok && run.attemptID == batch.attemptID {
			if cancel == nil {
				cancel = run.cancel
			}
			delete(b.inFlight, key)
		}
	}
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// finishNonProposedInFlight clears inFlight entries for a propose-driven batch
// except those awaiting Ruler review in ToolStateProposed.
func (b *Bridge) finishNonProposedInFlight(batch preparedBatch) {
	b.mu.Lock()
	for _, item := range batch.items {
		key := toolKey{serverID: item.serverID, toolName: item.toolName}
		if run, ok := b.inFlight[key]; ok && run.attemptID == batch.attemptID && run.state != ToolStateProposed {
			delete(b.inFlight, key)
		}
	}
	b.mu.Unlock()
}

func (b *Bridge) markBatchFailed(batch preparedBatch, items []discoveredTool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, item := range items {
		key := toolKey{serverID: item.serverID, toolName: item.toolName}
		if !b.isAttemptCurrentLocked(key, batch.attemptID, item.schemaHash) {
			continue
		}
		b.failed[key] = item.schemaHash
		b.updateToolStatusLocked(item.serverID, item.toolName, item.schemaHash, ToolStateUnclassified, "", false)
	}
}

func (b *Bridge) isAttemptCurrentLocked(key toolKey, attemptID uint64, schemaHash string) bool {
	run, ok := b.inFlight[key]
	if !ok || run.attemptID != attemptID || run.schemaHash != schemaHash {
		return false
	}
	dt, exists := b.discovered[key]
	return exists && dt.schemaHash == schemaHash
}

func (b *Bridge) updateToolStatusLocked(serverID, toolName, schemaHash string, state ToolState, permissions string, registered bool) {
	st, ok := b.servers[serverID]
	if !ok {
		return
	}
	for i := range st.Tools {
		if st.Tools[i].Name == toolName && st.Tools[i].SchemaHash == schemaHash {
			st.Tools[i].State = state
			st.Tools[i].Permissions = permissions
			st.Tools[i].Registered = registered
			return
		}
	}
}

func (b *Bridge) canRegisterNameLocked(name string) bool {
	if b.registry == nil {
		return false
	}
	if b.registry.HasNonMCPTool(name) {
		return false
	}
	if _, owned := b.registeredTools[name]; owned {
		return true
	}
	_, exists := b.registry.Get(name)
	return !exists
}

func (b *Bridge) newAdapter(dt discoveredTool, perm tools.Permissions, mgr MCPClientManager) *MCPToolAdapter {
	return NewMCPToolAdapter(MCPToolAdapterOpts{
		ServerID:        dt.serverID,
		Name:            dt.registeredName,
		Tool:            dt.chatTool,
		Permissions:     perm,
		Manager:         mgr,
		Registry:        b.registry,
		ToolTimeout:     b.toolTimeout,
		MaxOutputTokens: b.maxOutputTokens,
	})
}

func (b *Bridge) resolveToolClassification(
	srv config.MCPServerConfig,
	serverID, toolName, bifrostName, registeredName, schemaHash string,
) (ToolState, tools.Permissions, bool) {
	// 1. Per-tool override from config takes highest precedence (matches bare,
	// Bifrost-prefixed, or server-ID-prefixed tool name).
	for _, override := range srv.Tools {
		if override.Name == toolName || override.Name == bifrostName || override.Name == registeredName {
			if perm, err := tools.ParsePermissions(override.Permissions); err == nil {
				return ToolStatePreclassified, perm, true
			}
		}
	}

	// 2. Server-level default_permissions from config.
	if srv.DefaultPermissions != "" {
		if perm, err := tools.ParsePermissions(srv.DefaultPermissions); err == nil {
			return ToolStatePreclassified, perm, true
		}
	}

	// 3. Persisted Ruler-sealed classification in DB matching both schemaHash
	// and commandHash (current project first, falling back to most recently
	// sealed matching schema_hash + command_hash across projects for the same
	// username).
	if b.db != nil && b.username != "" {
		cmdHash := b.commandHashForServer(serverID)
		row, err := storage.LookupMCPToolClassification(b.db, b.username, b.project, serverID, toolName, schemaHash, cmdHash)
		if err != nil {
			b.logger.Warn("failed to look up MCP tool classification",
				"server_id", serverID, "tool", toolName, "error", err)
		} else if row != nil {
			if perm, err := tools.ParsePermissions(row.Permissions); err == nil {
				if perm.IsEmpty() {
					return ToolStateDenied, perm, true
				}
				return ToolStateSealed, perm, true
			}
		}
	}

	return ToolStateUnclassified, tools.Permissions{}, false
}

func lookupApprovedPermission(approved map[string]string, item discoveredTool) (string, bool) {
	if approved == nil {
		return "", false
	}
	if perm, ok := approved[item.toolName]; ok {
		return perm, true
	}
	if perm, ok := approved[item.registeredName]; ok {
		return perm, true
	}
	if perm, ok := approved[item.bifrostName]; ok {
		return perm, true
	}
	return "", false
}

func stripClientOrServerPrefix(toolName, clientName, serverID string) string {
	if clientName != "" {
		prefix := clientName + "-"
		if strings.HasPrefix(toolName, prefix) {
			return strings.TrimPrefix(toolName, prefix)
		}
	}
	if serverID != "" {
		prefix := serverID + "-"
		if strings.HasPrefix(toolName, prefix) {
			return strings.TrimPrefix(toolName, prefix)
		}
	}
	return toolName
}

func namespaceToolName(serverID, unprefixed string) string {
	if serverID == "" {
		return unprefixed
	}
	prefix := serverID + "-"
	if strings.HasPrefix(unprefixed, prefix) {
		return unprefixed
	}
	return prefix + unprefixed
}

// AskRulerGateOpts configures a ZhengmingGate backed by Asimi's ask_ruler
// Zhengming flow (RequestZhengming + WaitForZhengming).
type AskRulerGateOpts struct {
	MinisterID       string // defaults to "rites"
	Requester        tools.ZhengmingRequester
	WaitForZhengming func(ctx context.Context, requestID string) (string, error)
	Username         string
	Project          string
}

// NewAskRulerZhengmingGate builds a ZhengmingGate that presents each proposed
// MCP tool classification to the Ruler via Zhengming (ask_ruler) under the
// Court key (edict id 0), supporting approve, adjust (with strict 9-char rwx
// validation), and reject.
func NewAskRulerZhengmingGate(opts AskRulerGateOpts) ZhengmingGate {
	ministerID := opts.MinisterID
	if ministerID == "" {
		ministerID = "rites"
	}

	return func(ctx context.Context, serverID string, proposals []ToolProposal) (map[string]string, error) {
		if opts.Requester == nil || opts.WaitForZhengming == nil {
			return nil, fmt.Errorf("ask_ruler zhengming gate requires Requester and WaitForZhengming")
		}

		key := storage.EdictKey{ID: 0, Username: opts.Username, Project: opts.Project}
		approved := make(map[string]string, len(proposals))

		for _, p := range proposals {
			proposedPerm, err := tools.ParsePermissions(p.Permissions)
			if err != nil {
				return nil, fmt.Errorf("invalid proposed permissions %q for %s/%s: %w", p.Permissions, serverID, p.ToolName, err)
			}

			var text strings.Builder
			fmt.Fprintf(&text, "Approve Three Realms classification for MCP tool %s/%s?\n\n", serverID, p.ToolName)
			fmt.Fprintf(&text, "Proposed permissions: %s (earth/heaven/intent)\n", proposedPerm.String())
			if p.Rationale != "" {
				fmt.Fprintf(&text, "Rationale: %s\n", p.Rationale)
			}
			text.WriteString("\nSelect Approve, Adjust permissions (or reply directly with a strict 9-char rwx string such as r-------- or ---------), or Reject.")

			questions := storage.ZhengmingQuestions{{
				Summary: fmt.Sprintf("Classify MCP tool %s/%s (%s)", serverID, p.ToolName, proposedPerm.String()),
				Text:    text.String(),
				Options: []string{
					AnswerApproveClassification,
					AnswerAdjustClassification,
					AnswerRejectClassification,
				},
			}}

			reqID, err := opts.Requester.RequestZhengming(ctx, key, questions, storage.PriorityNormal, ministerID)
			if err != nil {
				return nil, fmt.Errorf("request zhengming for %s/%s: %w", serverID, p.ToolName, err)
			}

			answer, err := opts.WaitForZhengming(ctx, reqID)
			if err != nil {
				return nil, fmt.Errorf("wait for zhengming for %s/%s: %w", serverID, p.ToolName, err)
			}

			switch {
			case answer == AnswerApproveClassification || strings.EqualFold(strings.TrimSpace(answer), "approve"):
				approved[p.ToolName] = proposedPerm.String()

			case answer == AnswerRejectClassification || answer == tools.AnswerChat || strings.EqualFold(strings.TrimSpace(answer), "reject"):
				// Rejected: omit from approved map so the tool stays withheld.

			case answer == AnswerAdjustClassification || strings.EqualFold(strings.TrimSpace(answer), "adjust"):
				adjQuestions := storage.ZhengmingQuestions{{
					Summary: fmt.Sprintf("Enter 9-char rwx permissions for %s/%s", serverID, p.ToolName),
					Text: fmt.Sprintf(
						"Enter a strict 9-character Three Realms permission string (earth/heaven/intent, e.g. r-------- or ---------) for MCP tool %s/%s (proposed: %s).",
						serverID, p.ToolName, proposedPerm.String(),
					),
					Options: []string{
						proposedPerm.String(),
						"---------",
						AnswerRejectClassification,
					},
				}}
				adjReqID, err := opts.Requester.RequestZhengming(ctx, key, adjQuestions, storage.PriorityNormal, ministerID)
				if err != nil {
					return nil, fmt.Errorf("request adjusted permissions for %s/%s: %w", serverID, p.ToolName, err)
				}
				adjAnswer, err := opts.WaitForZhengming(ctx, adjReqID)
				if err != nil {
					return nil, fmt.Errorf("wait for adjusted permissions for %s/%s: %w", serverID, p.ToolName, err)
				}
				if adjAnswer == AnswerRejectClassification || adjAnswer == tools.AnswerChat || strings.EqualFold(strings.TrimSpace(adjAnswer), "reject") {
					continue
				}
				adjPerm, err := tools.ParsePermissions(adjAnswer)
				if err != nil {
					return nil, fmt.Errorf("invalid adjusted permissions %q for %s/%s: %w", adjAnswer, serverID, p.ToolName, err)
				}
				approved[p.ToolName] = adjPerm.String()

			default:
				// Treat any other reply as a direct adjusted 9-char rwx string.
				adjPerm, err := tools.ParsePermissions(answer)
				if err != nil {
					return nil, fmt.Errorf("invalid permissions %q for %s/%s: %w", answer, serverID, p.ToolName, err)
				}
				approved[p.ToolName] = adjPerm.String()
			}
		}

		return approved, nil
	}
}
