package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openTestMCPDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// Use a single connection for in-memory SQLite so concurrent goroutines
	// share the exact same in-memory database.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&storage.MCPToolClassification{}))
	return db
}

func findToolStatus(t *testing.T, statuses []ServerStatus, serverID, toolName string) (ToolStatus, bool) {
	t.Helper()
	for _, srv := range statuses {
		if srv.ID != serverID {
			continue
		}
		for _, ts := range srv.Tools {
			if ts.Name == toolName {
				return ts, true
			}
		}
	}
	return ToolStatus{}, false
}

func TestClassify_PreclassificationBypass(t *testing.T) {
	db := openTestMCPDB(t)
	reg := tools.NewToolRegistry()

	var proposerCalls, gateCalls, registryChanges atomic.Int32

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{
				Name:            "my_term",
				ExecutionConfig: &schemas.MCPClientConfig{ID: "my-term", Name: "my_term"},
				State:           schemas.MCPConnectionStateConnected,
			},
		},
		toolsByClient: map[string][]schemas.ChatTool{
			"my_term": {
				makeChatTool("my_term-bare_override", "Matched by bare name", `{"type":"object","properties":{}}`),
				makeChatTool("my_term-prefixed_override", "Matched by prefixed name", `{"type":"object","properties":{}}`),
				makeChatTool("my_term-defaulted", "Falls back to default_permissions", `{"type":"object","properties":{}}`),
				makeChatTool("my_term-denied", "Explicitly denied via config", `{"type":"object","properties":{}}`),
			},
		},
	}

	cfg := config.MCPConfig{
		Servers: []config.MCPServerConfig{
			{
				ID:                 "my-term",
				Command:            []string{"/usr/bin/term-mcp"},
				DefaultPermissions: "r--------",
				Tools: []config.MCPToolOverride{
					{Name: "bare_override", Permissions: "--x------"},
					{Name: "my-term-prefixed_override", Permissions: "rw-------"},
					{Name: "denied", Permissions: "---------"},
				},
			},
		},
	}

	bridge, err := NewBridge(BridgeOpts{
		Config:            cfg,
		Registry:          reg,
		DB:                db,
		Username:          "alice",
		Project:           "alice/app",
		SyncInterval:      time.Hour,
		OnRegistryChanged: func() { registryChanges.Add(1) },
		Proposer: func(context.Context, string, []ToolDescriptor) ([]ToolProposal, error) {
			proposerCalls.Add(1)
			return nil, nil
		},
		Gate: func(context.Context, string, []ToolProposal) (map[string]string, error) {
			gateCalls.Add(1)
			return nil, nil
		},
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	defer func() { require.NoError(t, bridge.Stop()) }()

	assert.Zero(t, proposerCalls.Load(), "preclassified tools must bypass RitesProposer")
	assert.Zero(t, gateCalls.Load(), "preclassified tools must bypass ZhengmingGate")
	assert.Equal(t, int32(1), registryChanges.Load(), "initial preclassified registrations must trigger OnRegistryChanged")

	tp, ok := reg.Get("my-term-bare_override")
	require.True(t, ok)
	assert.Equal(t, "--x------", tp.Permissions.String())

	tp, ok = reg.Get("my-term-prefixed_override")
	require.True(t, ok)
	assert.Equal(t, "rw-------", tp.Permissions.String())

	tp, ok = reg.Get("my-term-defaulted")
	require.True(t, ok)
	assert.Equal(t, "r--------", tp.Permissions.String())

	_, ok = reg.Get("my-term-denied")
	assert.False(t, ok, "--------- preclassified tool must stay unregistered")

	statuses := bridge.Status()
	for name, want := range map[string]struct {
		perm string
		reg  bool
	}{
		"bare_override":     {perm: "--x------", reg: true},
		"prefixed_override": {perm: "rw-------", reg: true},
		"defaulted":         {perm: "r--------", reg: true},
		"denied":            {perm: "---------", reg: false},
	} {
		ts, found := findToolStatus(t, statuses, "my-term", name)
		require.True(t, found, "expected status for %s", name)
		assert.Equal(t, ToolStatePreclassified, ts.State)
		assert.Equal(t, want.perm, ts.Permissions)
		assert.Equal(t, want.reg, ts.Registered)
	}
}

func TestClassify_SchemaHashReuseCurrentAndCrossProject(t *testing.T) {
	db := openTestMCPDB(t)
	reg := tools.NewToolRegistry()

	sameProjTool := makeChatTool("lsp-hover", "Show hover info", `{"type":"object","properties":{"file":{"type":"string"}}}`)
	sameProjHash := SchemaHash("hover", "Show hover info", sameProjTool.Function.Parameters)

	crossProjTool := makeChatTool("lsp-rename", "Rename symbol", `{"type":"object","properties":{"name":{"type":"string"}}}`)
	crossProjHash := SchemaHash("rename", "Rename symbol", crossProjTool.Function.Parameters)

	otherUserTool := makeChatTool("lsp-format", "Format file", `{"type":"object","properties":{}}`)
	otherUserHash := SchemaHash("format", "Format file", otherUserTool.Function.Parameters)

	lspCmdHash := CommandHash([]string{"/usr/bin/lsp-mcp"})
	now := time.Now().UTC()
	// Current project row takes precedence over newer row in another project.
	require.NoError(t, storage.SaveMCPToolClassification(db, &storage.MCPToolClassification{
		Username:    "alice",
		Project:     "alice/current",
		ServerID:    "lsp",
		ToolName:    "hover",
		Permissions: "r--------",
		SchemaHash:  sameProjHash,
		CommandHash: lspCmdHash,
		SealedAt:    now.Add(-2 * time.Hour),
		SealedBy:    "alice",
	}))
	require.NoError(t, storage.SaveMCPToolClassification(db, &storage.MCPToolClassification{
		Username:    "alice",
		Project:     "alice/other",
		ServerID:    "lsp",
		ToolName:    "hover",
		Permissions: "rwx------",
		SchemaHash:  sameProjHash,
		CommandHash: lspCmdHash,
		SealedAt:    now.Add(-1 * time.Minute),
		SealedBy:    "alice",
	}))

	// Cross-project reuse picks the MOST RECENTLY sealed row for alice.
	require.NoError(t, storage.SaveMCPToolClassification(db, &storage.MCPToolClassification{
		Username:    "alice",
		Project:     "alice/old-proj",
		ServerID:    "lsp",
		ToolName:    "rename",
		Permissions: "rwx------",
		SchemaHash:  crossProjHash,
		CommandHash: lspCmdHash,
		SealedAt:    now.Add(-3 * time.Hour),
		SealedBy:    "alice",
	}))
	require.NoError(t, storage.SaveMCPToolClassification(db, &storage.MCPToolClassification{
		Username:    "alice",
		Project:     "alice/recent-proj",
		ServerID:    "lsp",
		ToolName:    "rename",
		Permissions: "rw-------",
		SchemaHash:  crossProjHash,
		CommandHash: lspCmdHash,
		SealedAt:    now.Add(-5 * time.Minute),
		SealedBy:    "alice",
	}))

	// Another user's sealed classification must NOT be reused by alice.
	require.NoError(t, storage.SaveMCPToolClassification(db, &storage.MCPToolClassification{
		Username:    "bob",
		Project:     "bob/proj",
		ServerID:    "lsp",
		ToolName:    "format",
		Permissions: "rw-------",
		SchemaHash:  otherUserHash,
		CommandHash: lspCmdHash,
		SealedAt:    now,
		SealedBy:    "bob",
	}))

	var proposedNames []string
	var proposedMu sync.Mutex
	proposerDone := make(chan struct{})

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{
				Name:            "lsp",
				ExecutionConfig: &schemas.MCPClientConfig{ID: "lsp", Name: "lsp"},
				State:           schemas.MCPConnectionStateConnected,
			},
		},
		toolsByClient: map[string][]schemas.ChatTool{
			"lsp": {sameProjTool, crossProjTool, otherUserTool},
		},
	}

	bridge, err := NewBridge(BridgeOpts{
		Config: config.MCPConfig{
			Servers: []config.MCPServerConfig{{ID: "lsp", Command: []string{"/usr/bin/lsp-mcp"}}},
		},
		Registry:     reg,
		DB:           db,
		Username:     "alice",
		Project:      "alice/current",
		SyncInterval: time.Hour,
		Proposer: func(_ context.Context, _ string, discovered []ToolDescriptor) ([]ToolProposal, error) {
			proposedMu.Lock()
			for _, d := range discovered {
				proposedNames = append(proposedNames, d.Name)
			}
			proposedMu.Unlock()
			close(proposerDone)
			return []ToolProposal{{ToolName: "format", Permissions: "-w-------"}}, nil
		},
		Gate: func(_ context.Context, _ string, proposals []ToolProposal) (map[string]string, error) {
			out := make(map[string]string, len(proposals))
			for _, p := range proposals {
				out[p.ToolName] = p.Permissions
			}
			return out, nil
		},
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	defer func() { require.NoError(t, bridge.Stop()) }()

	// hover (current project) and rename (cross-project most recent) register immediately.
	tp, ok := reg.Get("lsp-hover")
	require.True(t, ok)
	assert.Equal(t, "r--------", tp.Permissions.String(), "current project row must take precedence")

	tp, ok = reg.Get("lsp-rename")
	require.True(t, ok)
	assert.Equal(t, "rw-------", tp.Permissions.String(), "most recently sealed cross-project row must be reused")

	// format (sealed only by bob) must go through Rites + Zhengming for alice.
	select {
	case <-proposerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RitesProposer on uncached tool")
	}

	proposedMu.Lock()
	assert.Equal(t, []string{"format"}, proposedNames, "only non-cached tool should invoke RitesProposer")
	proposedMu.Unlock()

	require.Eventually(t, func() bool {
		tp, ok := reg.Get("lsp-format")
		return ok && tp.Permissions.String() == "-w-------"
	}, 2*time.Second, 10*time.Millisecond)
}

func TestClassify_WithholdUntilSealed_ApproveAdjustReject(t *testing.T) {
	db := openTestMCPDB(t)
	reg := tools.NewToolRegistry()

	proposerStarted := make(chan struct{})
	releaseProposer := make(chan struct{})
	gateStarted := make(chan struct{})
	releaseGate := make(chan struct{})

	var registryChanges atomic.Int32

	approveTool := makeChatTool("term-read", "Read screen", `{"type":"object","properties":{}}`)
	adjustTool := makeChatTool("term-exec", "Run command", `{"type":"object","properties":{"cmd":{"type":"string"}}}`)
	rejectTool := makeChatTool("term-nuke", "Dangerous action", `{"type":"object","properties":{}}`)
	denyTool := makeChatTool("term-deny", "Sealed deny tool", `{"type":"object","properties":{}}`)

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{
				Name:            "term",
				ExecutionConfig: &schemas.MCPClientConfig{ID: "term", Name: "term"},
				State:           schemas.MCPConnectionStateConnected,
			},
		},
		toolsByClient: map[string][]schemas.ChatTool{
			"term": {approveTool, adjustTool, rejectTool, denyTool},
		},
	}

	bridge, err := NewBridge(BridgeOpts{
		Config: config.MCPConfig{
			Servers: []config.MCPServerConfig{{ID: "term", Command: []string{"/usr/bin/term-mcp"}}},
		},
		Registry:          reg,
		DB:                db,
		Username:          "alice",
		Project:           "alice/proj",
		SyncInterval:      time.Hour,
		OnRegistryChanged: func() { registryChanges.Add(1) },
		Proposer: func(ctx context.Context, serverID string, discovered []ToolDescriptor) ([]ToolProposal, error) {
			assert.Equal(t, "term", serverID)
			close(proposerStarted)
			select {
			case <-releaseProposer:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []ToolProposal{
				{ToolName: "read", Permissions: "r--------", Rationale: "read-only screen"},
				{ToolName: "exec", Permissions: "rwx------", Rationale: "shell access"},
				{ToolName: "nuke", Permissions: "rwxrwxrwx", Rationale: "destructive"},
				{ToolName: "deny", Permissions: "r--------", Rationale: "quarantine candidate"},
			}, nil
		},
		Gate: func(ctx context.Context, serverID string, proposals []ToolProposal) (map[string]string, error) {
			assert.Equal(t, "term", serverID)
			assert.Len(t, proposals, 4)
			close(gateStarted)
			select {
			case <-releaseGate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return map[string]string{
				"read": "r--------", // approved as proposed
				"exec": "--x------", // adjusted (narrowed) by Ruler
				"deny": "---------", // sealed deny by Ruler
				// "nuke" omitted => rejected by Ruler
			}, nil
		},
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	defer func() { require.NoError(t, bridge.Stop()) }()

	// Stage 1: While RitesProposer is running, all tools are withheld in state unclassified.
	<-proposerStarted
	for _, name := range []string{"term-read", "term-exec", "term-nuke", "term-deny"} {
		_, registered := reg.Get(name)
		assert.False(t, registered, "tool %s must be withheld while unclassified", name)
	}
	for _, bare := range []string{"read", "exec", "nuke", "deny"} {
		ts, ok := findToolStatus(t, bridge.Status(), "term", bare)
		require.True(t, ok)
		assert.Equal(t, ToolStateUnclassified, ts.State)
		assert.False(t, ts.Registered)
	}
	assert.Zero(t, registryChanges.Load())

	// Stage 2: After RitesProposer returns and while ZhengmingGate is waiting,
	// tools are in state proposed and still withheld from ToolRegistry.
	close(releaseProposer)
	<-gateStarted
	for _, name := range []string{"term-read", "term-exec", "term-nuke", "term-deny"} {
		_, registered := reg.Get(name)
		assert.False(t, registered, "tool %s must stay withheld while proposed", name)
	}
	readStatus, ok := findToolStatus(t, bridge.Status(), "term", "read")
	require.True(t, ok)
	assert.Equal(t, ToolStateProposed, readStatus.State)
	assert.Equal(t, "r--------", readStatus.Permissions)
	assert.False(t, readStatus.Registered)

	// Stage 3: Release ZhengmingGate — approved & adjusted enter ToolRegistry,
	// sealed-deny is persisted & withheld, rejected is withheld & not persisted.
	close(releaseGate)
	require.Eventually(t, func() bool {
		return registryChanges.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	tpRead, ok := reg.Get("term-read")
	require.True(t, ok)
	assert.Equal(t, "r--------", tpRead.Permissions.String())

	tpExec, ok := reg.Get("term-exec")
	require.True(t, ok)
	assert.Equal(t, "--x------", tpExec.Permissions.String(), "Ruler-adjusted permission must be registered")

	_, hasNuke := reg.Get("term-nuke")
	assert.False(t, hasNuke, "rejected tool must stay withheld")

	_, hasDeny := reg.Get("term-deny")
	assert.False(t, hasDeny, "sealed-deny (---------) tool must stay withheld")

	// Verify DB persistence under (username, project, server_id, tool_name).
	rows, err := storage.ListMCPToolClassifications(db, "alice", "alice/proj")
	require.NoError(t, err)
	byTool := make(map[string]storage.MCPToolClassification, len(rows))
	for _, r := range rows {
		byTool[r.ToolName] = r
	}
	assert.Equal(t, "r--------", byTool["read"].Permissions)
	assert.Equal(t, "--x------", byTool["exec"].Permissions)
	assert.Equal(t, "---------", byTool["deny"].Permissions)
	_, savedNuke := byTool["nuke"]
	assert.False(t, savedNuke, "rejected tool must not be persisted as sealed")

	// Verify final statuses.
	statuses := bridge.Status()
	tsRead, _ := findToolStatus(t, statuses, "term", "read")
	assert.Equal(t, ToolStateSealed, tsRead.State)
	assert.True(t, tsRead.Registered)

	tsExec, _ := findToolStatus(t, statuses, "term", "exec")
	assert.Equal(t, ToolStateSealed, tsExec.State)
	assert.Equal(t, "--x------", tsExec.Permissions)
	assert.True(t, tsExec.Registered)

	tsDeny, _ := findToolStatus(t, statuses, "term", "deny")
	assert.Equal(t, ToolStateDenied, tsDeny.State)
	assert.Equal(t, "---------", tsDeny.Permissions)
	assert.False(t, tsDeny.Registered)

	tsNuke, _ := findToolStatus(t, statuses, "term", "nuke")
	assert.Equal(t, ToolStateUnclassified, tsNuke.State)
	assert.False(t, tsNuke.Registered)
}

func TestClassify_SchemaChangeUnregistersAndNarrowsWithoutUnioning(t *testing.T) {
	db := openTestMCPDB(t)
	reg := tools.NewToolRegistry()

	v1Tool := makeChatTool("fs-edit", "Edit file v1", `{"type":"object","properties":{"path":{"type":"string"}}}`)
	v1Hash := SchemaHash("edit", "Edit file v1", v1Tool.Function.Parameters)

	fsCmdHash := CommandHash([]string{"/usr/bin/fs-mcp"})
	// Seed v1 with broad rwx------ permissions in DB.
	require.NoError(t, storage.SaveMCPToolClassification(db, &storage.MCPToolClassification{
		Username:    "alice",
		Project:     "alice/proj",
		ServerID:    "fs",
		ToolName:    "edit",
		Permissions: "rwx------",
		SchemaHash:  v1Hash,
		CommandHash: fsCmdHash,
		SealedBy:    "alice",
	}))

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{
				Name:            "fs",
				ExecutionConfig: &schemas.MCPClientConfig{ID: "fs", Name: "fs"},
				State:           schemas.MCPConnectionStateConnected,
			},
		},
		toolsByClient: map[string][]schemas.ChatTool{
			"fs": {v1Tool},
		},
	}

	gateEntered := make(chan struct{})
	releaseGate := make(chan struct{})
	var proposerCalls, registryChanges atomic.Int32

	bridge, err := NewBridge(BridgeOpts{
		Config: config.MCPConfig{
			Servers: []config.MCPServerConfig{{ID: "fs", Command: []string{"/usr/bin/fs-mcp"}}},
		},
		Registry:          reg,
		DB:                db,
		Username:          "alice",
		Project:           "alice/proj",
		SyncInterval:      time.Hour,
		OnRegistryChanged: func() { registryChanges.Add(1) },
		Proposer: func(_ context.Context, _ string, discovered []ToolDescriptor) ([]ToolProposal, error) {
			proposerCalls.Add(1)
			require.Len(t, discovered, 1)
			return []ToolProposal{{
				ToolName:    discovered[0].Name,
				Permissions: "rw-------",
				SchemaHash:  discovered[0].SchemaHash,
			}}, nil
		},
		Gate: func(ctx context.Context, _ string, proposals []ToolProposal) (map[string]string, error) {
			close(gateEntered)
			select {
			case <-releaseGate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			// Ruler narrows permissions strictly to read-only r--------.
			return map[string]string{proposals[0].ToolName: "r--------"}, nil
		},
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	defer func() { require.NoError(t, bridge.Stop()) }()

	// Initially v1 is registered with rwx------ from DB without calling Rites.
	tp, ok := reg.Get("fs-edit")
	require.True(t, ok)
	assert.Equal(t, "rwx------", tp.Permissions.String())
	assert.Zero(t, proposerCalls.Load())
	assert.Equal(t, int32(1), registryChanges.Load())

	// Mutate tool schema to v2 and trigger Reconcile.
	v2Tool := makeChatTool("fs-edit", "Edit file v2 with extra flag", `{"type":"object","properties":{"path":{"type":"string"},"dry_run":{"type":"boolean"}}}`)
	v2Hash := SchemaHash("edit", "Edit file v2 with extra flag", v2Tool.Function.Parameters)
	require.NotEqual(t, v1Hash, v2Hash)
	mgr.setTools(map[string][]schemas.ChatTool{"fs": {v2Tool}})

	bridge.Reconcile(context.Background())

	// Schema change MUST immediately unregister the tool before re-classification completes.
	_, stillReg := reg.Get("fs-edit")
	assert.False(t, stillReg, "schema-changed tool must be immediately unregistered")
	assert.Equal(t, int32(2), registryChanges.Load(), "unregistering schema-changed tool must trigger OnRegistryChanged")

	// Wait for gate to be reached; tool must still be withheld while proposed.
	<-gateEntered
	_, stillReg = reg.Get("fs-edit")
	assert.False(t, stillReg)

	// Release gate with narrowed r-------- permissions.
	close(releaseGate)
	require.Eventually(t, func() bool {
		_, ok := reg.Get("fs-edit")
		return ok
	}, 2*time.Second, 10*time.Millisecond)

	// Verify narrowed permissions replaced (never unioned with) prior rwx------.
	tp, ok = reg.Get("fs-edit")
	require.True(t, ok)
	assert.Equal(t, "r--------", tp.Permissions.String())
	assert.True(t, tp.Permissions.Earth.Read)
	assert.False(t, tp.Permissions.Earth.Write, "Write bit must not be unioned from prior rwx------")
	assert.False(t, tp.Permissions.Earth.Execute, "Execute bit must not be unioned from prior rwx------")
	assert.Equal(t, int32(3), registryChanges.Load())

	writeOnly, err := tools.ParsePermissions("-w-------")
	require.NoError(t, err)
	assert.Empty(t, reg.ForPermissions(writeOnly), "write-only minister must not see narrowed read-only tool")

	readOnly, err := tools.ParsePermissions("r--------")
	require.NoError(t, err)
	assert.Len(t, reg.ForPermissions(readOnly), 1, "read-only minister must see narrowed read-only tool")

	// Verify DB row updated to v2Hash with narrowed permissions and subsequent Reconcile bypasses Rites.
	row, err := storage.LookupMCPToolClassification(db, "alice", "alice/proj", "fs", "edit", v2Hash, fsCmdHash)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, "r--------", row.Permissions)
	assert.Equal(t, fsCmdHash, row.CommandHash)

	bridge.Reconcile(context.Background())
	assert.Equal(t, int32(1), proposerCalls.Load(), "re-sealed schema must bypass Rites on subsequent Reconcile")
}

func TestClassify_DirectNarrowingReplacesNotUnions(t *testing.T) {
	db := openTestMCPDB(t)
	reg := tools.NewToolRegistry()

	tool := makeChatTool("fs-scan", "Scan workspace", `{"type":"object","properties":{}}`)
	hash := SchemaHash("scan", "Scan workspace", tool.Function.Parameters)
	fsCmdHash := CommandHash([]string{"/usr/bin/fs-mcp"})

	require.NoError(t, storage.SaveMCPToolClassification(db, &storage.MCPToolClassification{
		Username:    "alice",
		Project:     "alice/proj",
		ServerID:    "fs",
		ToolName:    "scan",
		Permissions: "rwx------",
		SchemaHash:  hash,
		CommandHash: fsCmdHash,
		SealedBy:    "alice",
	}))

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{Name: "fs", ExecutionConfig: &schemas.MCPClientConfig{ID: "fs", Name: "fs"}, State: schemas.MCPConnectionStateConnected},
		},
		toolsByClient: map[string][]schemas.ChatTool{"fs": {tool}},
	}

	var changes atomic.Int32
	bridge, err := NewBridge(BridgeOpts{
		Config: config.MCPConfig{
			Servers: []config.MCPServerConfig{{ID: "fs", Command: []string{"/usr/bin/fs-mcp"}}},
		},
		Registry:          reg,
		DB:                db,
		Username:          "alice",
		Project:           "alice/proj",
		SyncInterval:      time.Hour,
		OnRegistryChanged: func() { changes.Add(1) },
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	defer func() { require.NoError(t, bridge.Stop()) }()

	tp, ok := reg.Get("fs-scan")
	require.True(t, ok)
	assert.Equal(t, "rwx------", tp.Permissions.String())
	assert.Equal(t, int32(1), changes.Load())

	// Narrow persisted classification for the same schema_hash to read-only.
	require.NoError(t, storage.SaveMCPToolClassification(db, &storage.MCPToolClassification{
		Username:    "alice",
		Project:     "alice/proj",
		ServerID:    "fs",
		ToolName:    "scan",
		Permissions: "r--------",
		SchemaHash:  hash,
		CommandHash: fsCmdHash,
		SealedBy:    "alice",
	}))

	bridge.Reconcile(context.Background())

	// ToolRegistry.Set must replace rwx------ with r-------- (never union).
	tp, ok = reg.Get("fs-scan")
	require.True(t, ok)
	assert.Equal(t, "r--------", tp.Permissions.String())
	assert.Equal(t, int32(2), changes.Load())

	// Another no-op Reconcile must not spuriously fire OnRegistryChanged.
	bridge.Reconcile(context.Background())
	assert.Equal(t, int32(2), changes.Load())
}

func TestClassify_RemovedToolUnregistration(t *testing.T) {
	reg := tools.NewToolRegistry()
	earthRead, err := tools.ParsePermissions("r--------")
	require.NoError(t, err)
	reg.Register(dummyBuiltinTool{name: "read_file"}, earthRead)

	t1 := makeChatTool("term-screen", "Read screen", `{"type":"object","properties":{}}`)
	t2 := makeChatTool("term-keys", "Send keys", `{"type":"object","properties":{}}`)

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{Name: "term", ExecutionConfig: &schemas.MCPClientConfig{ID: "term", Name: "term"}, State: schemas.MCPConnectionStateConnected},
		},
		toolsByClient: map[string][]schemas.ChatTool{
			"term": {t1, t2},
		},
	}

	var changes atomic.Int32
	bridge, err := NewBridge(BridgeOpts{
		Config: config.MCPConfig{
			Servers: []config.MCPServerConfig{{
				ID:                 "term",
				Command:            []string{"/usr/bin/term-mcp"},
				DefaultPermissions: "r--------",
			}},
		},
		Registry:          reg,
		SyncInterval:      time.Hour,
		OnRegistryChanged: func() { changes.Add(1) },
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	defer func() { require.NoError(t, bridge.Stop()) }()

	_, ok1 := reg.Get("term-screen")
	_, ok2 := reg.Get("term-keys")
	assert.True(t, ok1)
	assert.True(t, ok2)
	assert.Equal(t, int32(1), changes.Load())

	// Remove term-keys from server and Reconcile.
	mgr.setTools(map[string][]schemas.ChatTool{"term": {t1}})
	bridge.Reconcile(context.Background())

	_, ok1 = reg.Get("term-screen")
	_, ok2 = reg.Get("term-keys")
	_, okBuiltin := reg.Get("read_file")
	assert.True(t, ok1, "remaining MCP tool must stay registered")
	assert.False(t, ok2, "removed MCP tool must be unregistered immediately")
	assert.True(t, okBuiltin, "built-in tool must never be unregistered")
	assert.Equal(t, int32(2), changes.Load())

	_, found := findToolStatus(t, bridge.Status(), "term", "keys")
	assert.False(t, found, "removed tool must disappear from Bridge.Status")
}

func TestClassify_PeriodicSyncDeduplication(t *testing.T) {
	db := openTestMCPDB(t)
	reg := tools.NewToolRegistry()

	gateEntered := make(chan struct{})
	releaseGate := make(chan struct{})
	var proposerCalls, gateCalls atomic.Int32

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{Name: "term", ExecutionConfig: &schemas.MCPClientConfig{ID: "term", Name: "term"}, State: schemas.MCPConnectionStateConnected},
		},
		toolsByClient: map[string][]schemas.ChatTool{
			"term": {makeChatTool("term-run", "Run command", `{"type":"object","properties":{}}`)},
		},
	}

	bridge, err := NewBridge(BridgeOpts{
		Config: config.MCPConfig{
			Servers: []config.MCPServerConfig{{ID: "term", Command: []string{"/usr/bin/term-mcp"}}},
		},
		Registry:     reg,
		DB:           db,
		Username:     "alice",
		Project:      "alice/proj",
		SyncInterval: 15 * time.Millisecond,
		Proposer: func(_ context.Context, _ string, discovered []ToolDescriptor) ([]ToolProposal, error) {
			proposerCalls.Add(1)
			return []ToolProposal{{
				ToolName:    discovered[0].Name,
				Permissions: "--x------",
				SchemaHash:  discovered[0].SchemaHash,
			}}, nil
		},
		Gate: func(ctx context.Context, _ string, proposals []ToolProposal) (map[string]string, error) {
			if gateCalls.Add(1) == 1 {
				close(gateEntered)
			}
			select {
			case <-releaseGate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return map[string]string{proposals[0].ToolName: "--x------"}, nil
		},
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	defer func() { require.NoError(t, bridge.Stop()) }()

	<-gateEntered

	// Trigger multiple explicit Reconcile and RetryPendingClassifications calls
	// alongside periodic sync ticks while the first classification is still in-flight.
	for i := 0; i < 5; i++ {
		bridge.Reconcile(context.Background())
		bridge.RetryPendingClassifications(context.Background())
	}
	require.Eventually(t, func() bool {
		return mgr.getToolCalls.Load() >= 8
	}, time.Second, 10*time.Millisecond)

	assert.Equal(t, int32(1), proposerCalls.Load(), "in-flight classification must be deduplicated across periodic syncs")
	assert.Equal(t, int32(1), gateCalls.Load(), "in-flight ZhengmingGate must be deduplicated across periodic syncs")

	// Status must remain proposed (not regress to unclassified) during periodic syncs.
	ts, found := findToolStatus(t, bridge.Status(), "term", "run")
	require.True(t, found)
	assert.Equal(t, ToolStateProposed, ts.State)
	assert.Equal(t, "--x------", ts.Permissions)
	assert.False(t, ts.Registered)

	// Unblock gate and verify sealing completes cleanly.
	close(releaseGate)
	require.Eventually(t, func() bool {
		tp, ok := reg.Get("term-run")
		return ok && tp.Permissions.String() == "--x------"
	}, 2*time.Second, 10*time.Millisecond)

	// Further periodic syncs must hit DB cache and never call Proposer/Gate again.
	bridge.Reconcile(context.Background())
	assert.Equal(t, int32(1), proposerCalls.Load())
	assert.Equal(t, int32(1), gateCalls.Load())
}

func TestClassify_RetryPendingClassificationsAndRejectedWithholding(t *testing.T) {
	db := openTestMCPDB(t)
	reg := tools.NewToolRegistry()

	var proposerCalls, gateCalls atomic.Int32
	attemptDone := make(chan int32, 10)

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{Name: "srv", ExecutionConfig: &schemas.MCPClientConfig{ID: "srv", Name: "srv"}, State: schemas.MCPConnectionStateConnected},
		},
		toolsByClient: map[string][]schemas.ChatTool{
			"srv": {
				makeChatTool("srv-flaky", "Flaky classification tool", `{"type":"object","properties":{}}`),
				makeChatTool("srv-bad", "Rejected tool", `{"type":"object","properties":{}}`),
			},
		},
	}

	bridge, err := NewBridge(BridgeOpts{
		Config: config.MCPConfig{
			Servers: []config.MCPServerConfig{{ID: "srv", Command: []string{"/usr/bin/srv-mcp"}}},
		},
		Registry:     reg,
		DB:           db,
		Username:     "alice",
		Project:      "alice/proj",
		SyncInterval: time.Hour,
		Proposer: func(_ context.Context, _ string, discovered []ToolDescriptor) ([]ToolProposal, error) {
			n := proposerCalls.Add(1)
			defer func() { attemptDone <- n }()
			if n == 1 {
				return nil, errors.New("temporary rites outage")
			}
			out := make([]ToolProposal, 0, len(discovered))
			for _, d := range discovered {
				out = append(out, ToolProposal{ToolName: d.Name, Permissions: "r--------", SchemaHash: d.SchemaHash})
			}
			return out, nil
		},
		Gate: func(_ context.Context, _ string, proposals []ToolProposal) (map[string]string, error) {
			gateCalls.Add(1)
			approved := make(map[string]string)
			for _, p := range proposals {
				if p.ToolName == "flaky" {
					approved["flaky"] = "r--------"
				}
				// "bad" omitted => rejected by Ruler
			}
			return approved, nil
		},
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	defer func() { require.NoError(t, bridge.Stop()) }()

	// Attempt 1 fails in Proposer.
	select {
	case n := <-attemptDone:
		assert.Equal(t, int32(1), n)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial classification attempt")
	}

	// Wait until inFlight has cleared after attempt 1 failed.
	require.Eventually(t, func() bool {
		bridge.mu.RLock()
		defer bridge.mu.RUnlock()
		return len(bridge.inFlight) == 0 && len(bridge.failed) == 2
	}, time.Second, 10*time.Millisecond)

	// Periodic Reconcile does not retry failed tools until RetryPendingClassifications.
	bridge.Reconcile(context.Background())
	assert.Equal(t, int32(1), proposerCalls.Load())
	_, ok := reg.Get("srv-flaky")
	assert.False(t, ok)

	// Explicit RetryPendingClassifications retries pending tools and seals "flaky" while rejecting "bad".
	bridge.RetryPendingClassifications(context.Background())
	select {
	case n := <-attemptDone:
		assert.Equal(t, int32(2), n)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retry classification attempt")
	}

	require.Eventually(t, func() bool {
		tp, ok := reg.Get("srv-flaky")
		return ok && tp.Permissions.String() == "r--------"
	}, 2*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		bridge.mu.RLock()
		defer bridge.mu.RUnlock()
		return len(bridge.inFlight) == 0
	}, time.Second, 10*time.Millisecond)

	_, hasBad := reg.Get("srv-bad")
	assert.False(t, hasBad, "rejected tool must stay withheld")

	// Neither Reconcile nor RetryPendingClassifications should re-prompt for a rejected tool with unchanged schema.
	bridge.Reconcile(context.Background())
	bridge.RetryPendingClassifications(context.Background())
	assert.Equal(t, int32(2), proposerCalls.Load(), "rejected tool must not be re-prompted while schema is unchanged")
	assert.Equal(t, int32(1), gateCalls.Load())

	// Changing the rejected tool's schema triggers re-classification.
	mgr.setTools(map[string][]schemas.ChatTool{
		"srv": {
			makeChatTool("srv-flaky", "Flaky classification tool", `{"type":"object","properties":{}}`),
			makeChatTool("srv-bad", "Rejected tool v2 (safer)", `{"type":"object","properties":{}}`),
		},
	})
	bridge.Reconcile(context.Background())
	select {
	case n := <-attemptDone:
		assert.Equal(t, int32(3), n)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for schema-changed rejected tool re-classification")
	}
}

func TestClassify_InvalidPermissionsFromGateRejectedStrictly(t *testing.T) {
	db := openTestMCPDB(t)
	reg := tools.NewToolRegistry()

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{Name: "srv", ExecutionConfig: &schemas.MCPClientConfig{ID: "srv", Name: "srv"}, State: schemas.MCPConnectionStateConnected},
		},
		toolsByClient: map[string][]schemas.ChatTool{
			"srv": {
				makeChatTool("srv-good", "Good tool", `{"type":"object","properties":{}}`),
				makeChatTool("srv-badperm", "Invalid perm tool", `{"type":"object","properties":{}}`),
			},
		},
	}

	bridge, err := NewBridge(BridgeOpts{
		Config: config.MCPConfig{
			Servers: []config.MCPServerConfig{{ID: "srv", Command: []string{"/usr/bin/srv-mcp"}}},
		},
		Registry:     reg,
		DB:           db,
		Username:     "alice",
		Project:      "alice/proj",
		SyncInterval: time.Hour,
		Proposer: func(_ context.Context, _ string, discovered []ToolDescriptor) ([]ToolProposal, error) {
			out := make([]ToolProposal, 0, len(discovered))
			for _, d := range discovered {
				out = append(out, ToolProposal{ToolName: d.Name, Permissions: "r--------", SchemaHash: d.SchemaHash})
			}
			return out, nil
		},
		Gate: func(_ context.Context, _ string, _ []ToolProposal) (map[string]string, error) {
			return map[string]string{
				"good":    "r--------",
				"badperm": "rwx rwx r", // invalid 9-char string (spaces & wrong positions)
			}, nil
		},
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	defer func() { require.NoError(t, bridge.Stop()) }()

	require.Eventually(t, func() bool {
		_, ok := reg.Get("srv-good")
		return ok
	}, 2*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		bridge.mu.RLock()
		defer bridge.mu.RUnlock()
		return len(bridge.inFlight) == 0
	}, time.Second, 10*time.Millisecond)

	_, hasBad := reg.Get("srv-badperm")
	assert.False(t, hasBad, "tool with invalid 9-char permission must remain withheld")

	rows, err := storage.ListMCPToolClassifications(db, "alice", "alice/proj")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "good", rows[0].ToolName)
}

type fakeZhengmingRequester struct {
	mu        sync.Mutex
	requests  []storage.ZhengmingQuestions
	keys      []storage.EdictKey
	ministers []string
}

func (f *fakeZhengmingRequester) RequestZhengming(_ context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, _ storage.ZhengmingPriority, callerMinisterID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	f.requests = append(f.requests, questions)
	f.ministers = append(f.ministers, callerMinisterID)
	return "req-" + string(rune('0'+len(f.requests))), nil
}

func TestNewAskRulerZhengmingGate_ApproveAdjustRejectAndValidation(t *testing.T) {
	proposals := []ToolProposal{
		{ToolName: "read", Permissions: "r--------", Rationale: "reads files"},
		{ToolName: "exec", Permissions: "rwx------", Rationale: "runs commands"},
		{ToolName: "direct", Permissions: "rw-------", Rationale: "direct custom reply"},
		{ToolName: "nuke", Permissions: "rwxrwxrwx", Rationale: "dangerous"},
	}

	req := &fakeZhengmingRequester{}
	answers := []string{
		AnswerApproveClassification, // read -> r--------
		AnswerAdjustClassification,  // exec -> follow-up prompt
		"--x------",                 // exec adjusted -> --x------
		"r-----r--",                 // direct -> custom 9-char reply
		AnswerRejectClassification,  // nuke -> rejected
	}
	var idx int

	gate := NewAskRulerZhengmingGate(AskRulerGateOpts{
		Requester: req,
		WaitForZhengming: func(context.Context, string) (string, error) {
			ans := answers[idx]
			idx++
			return ans, nil
		},
		Username: "alice",
		Project:  "alice/proj",
	})

	approved, err := gate(context.Background(), "term", proposals)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"read":   "r--------",
		"exec":   "--x------",
		"direct": "r-----r--",
	}, approved)
	assert.Len(t, req.requests, 5)
	for _, k := range req.keys {
		assert.Equal(t, uint(0), k.ID, "MCP classification Zhengming must use Court key (edict id 0)")
		assert.Equal(t, "alice", k.Username)
		assert.Equal(t, "alice/proj", k.Project)
	}
	for _, m := range req.ministers {
		assert.Equal(t, "rites", m)
	}

	// Strict 9-char rwx validation rejects malformed adjusted answers.
	invalidCases := []string{
		"rwx",
		"rwxrwxrwx!",
		"r-- --- ---",
		"xwr------",
		" r--------",
	}
	for _, bad := range invalidCases {
		t.Run("invalid_"+bad, func(t *testing.T) {
			badReq := &fakeZhengmingRequester{}
			badGate := NewAskRulerZhengmingGate(AskRulerGateOpts{
				Requester: badReq,
				WaitForZhengming: func(context.Context, string) (string, error) {
					return bad, nil
				},
				Username: "alice",
				Project:  "alice/proj",
			})
			_, err := badGate(context.Background(), "term", []ToolProposal{{ToolName: "exec", Permissions: "--x------"}})
			require.Error(t, err)
		})
	}
}

func TestClassificationProposalHandler_EndToEnd(t *testing.T) {
	db := openTestMCPDB(t)
	reg := tools.NewToolRegistry()
	reg.Register(fakeBuiltInTool{name: "term-collision"}, tools.Permissions{Earth: tools.Access{Read: true}})

	editTool := makeChatTool("term-edit", "Edit file via envoy", `{"type":"object","properties":{"path":{"type":"string"}}}`)
	editHash := SchemaHash("edit", "Edit file via envoy", editTool.Function.Parameters)
	dangerTool := makeChatTool("term-nuke", "Destroy host state", `{"type":"object","properties":{}}`)
	dangerHash := SchemaHash("nuke", "Destroy host state", dangerTool.Function.Parameters)

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{
				Name:            "term",
				ExecutionConfig: &schemas.MCPClientConfig{ID: "term", Name: "term"},
				State:           schemas.MCPConnectionStateConnected,
			},
		},
		toolsByClient: map[string][]schemas.ChatTool{
			"term": {editTool, dangerTool},
		},
	}

	bridge, err := NewBridge(BridgeOpts{
		Config: config.MCPConfig{
			Servers: []config.MCPServerConfig{{ID: "term", Command: []string{"/usr/bin/term-mcp", "--stdio"}}},
		},
		Registry:     reg,
		DB:           db,
		Username:     "alice",
		Project:      "alice/proj",
		SyncInterval: time.Hour,
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))
	defer func() { require.NoError(t, bridge.Stop()) }()

	propReg := tools.NewProposalRegistry()
	handler := &ClassificationProposalHandler{BridgeGetter: func() *Bridge { return bridge }}
	require.NoError(t, propReg.Register(handler))

	// Only rites is authorized to submit mcp_classification proposals.
	assert.True(t, handler.Allowed("rites", tools.Permissions{}))
	assert.False(t, handler.Allowed("forge", tools.Permissions{}))

	req := &fakeZhengmingRequester{}
	proposeTool := tools.ProposeTool{
		Ctx:       tools.ToolContext{MinisterID: "rites", Username: "alice", Project: "alice/proj"},
		Registry:  propReg,
		Requester: req,
	}

	// Schema hash mismatch fails Validate before bothering the Ruler.
	_, err = proposeTool.Call(context.Background(), `{
		"kind":"mcp_classification",
		"summary":"Bad hash",
		"payload":{"server_id":"term","proposals":[{"tool_name":"edit","permissions":"rw-------","schema_hash":"wrong"}]}
	}`)
	require.Error(t, err)
	assert.Empty(t, req.requests)

	// Valid multi-tool proposal including sealed deny ("---------").
	payloadBytes, err := json.Marshal(map[string]any{
		"kind":    ProposalKindMCPClassification,
		"summary": "Classify term envoy tools",
		"payload": ClassificationProposalPayload{
			ServerID: "term",
			Proposals: []ToolProposal{
				{ToolName: "edit", Permissions: "rw-------", Rationale: "Reads and writes Earth", SchemaHash: editHash},
				{ToolName: "nuke", Permissions: "---------", Rationale: "Dangerous host mutation", SchemaHash: dangerHash},
			},
		},
	})
	require.NoError(t, err)

	out, err := proposeTool.Call(context.Background(), string(payloadBytes))
	require.NoError(t, err)
	assert.Contains(t, out, `"status":"proposed"`)
	require.Len(t, req.requests, 1)

	// Before Ruler approval, tools remain unregistered and in ToolStateProposed.
	_, regEdit := reg.Get("term-edit")
	assert.False(t, regEdit)
	tsEdit, _ := findToolStatus(t, bridge.Status(), "term", "edit")
	assert.Equal(t, ToolStateProposed, tsEdit.State)

	// Simulate HandleZhengmingResponse approving the proposal.
	var proposeOut struct {
		RequestID string `json:"request_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &proposeOut))
	require.NotEmpty(t, proposeOut.RequestID)
	handled, err := propReg.ApplyResponse(context.Background(), proposeOut.RequestID, tools.AnswerApproveProposal)
	require.NoError(t, err)
	assert.True(t, handled)

	// term-edit is sealed and registered with subset matching; term-nuke is denied and unregistered.
	tp, ok := reg.Get("term-edit")
	require.True(t, ok)
	assert.True(t, tp.SubsetMatch)
	assert.Equal(t, "rw-------", tp.Permissions.String())

	// Read-only Earth minister (Chancellor r--r--rwx) must NOT see or execute rw------- tool.
	chancellorPerm, _ := tools.ParsePermissions("r--r--rwx")
	for _, tl := range reg.ForPermissions(chancellorPerm) {
		assert.NotEqual(t, "term-edit", tl.Name())
	}
	chancellorCtx := context.WithValue(context.Background(), tools.PermissionsKey{}, chancellorPerm)
	_, callErr := tp.Tool.Call(chancellorCtx, `{}`)
	require.Error(t, callErr)
	assert.Contains(t, callErr.Error(), "permission denied")

	// Sealed deny tool stays unregistered with ToolStateDenied and persists with command_hash.
	_, regNuke := reg.Get("term-nuke")
	assert.False(t, regNuke)
	tsNuke, _ := findToolStatus(t, bridge.Status(), "term", "nuke")
	assert.Equal(t, ToolStateDenied, tsNuke.State)
	assert.Equal(t, "---------", tsNuke.Permissions)

	cmdHash := CommandHash([]string{"/usr/bin/term-mcp", "--stdio"})
	savedDeny, err := storage.LookupMCPToolClassification(db, "alice", "alice/proj", "term", "nuke", dangerHash, cmdHash)
	require.NoError(t, err)
	require.NotNil(t, savedDeny)
	assert.Equal(t, "---------", savedDeny.Permissions)
	assert.Equal(t, cmdHash, savedDeny.CommandHash)

	// Different command_hash for the same server_id/schema_hash must NOT reuse the seal.
	wrongCmd, err := storage.LookupMCPToolClassification(db, "alice", "alice/other-proj", "term", "edit", editHash, CommandHash([]string{"/tmp/evil"}))
	require.NoError(t, err)
	assert.Nil(t, wrongCmd)
}

type fakeBuiltInTool struct{ name string }

func (f fakeBuiltInTool) Name() string                                 { return f.name }
func (f fakeBuiltInTool) Description() string                          { return f.name }
func (f fakeBuiltInTool) Call(context.Context, string) (string, error) { return "", nil }
func (f fakeBuiltInTool) Format(string, string, error) string          { return "" }
func (f fakeBuiltInTool) ParameterSchema() map[string]any              { return nil }
