package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/storage"
	bifrostmcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakeMCPManager implements MCPClientManager for unit tests.
type fakeMCPManager struct {
	mu            sync.RWMutex
	clients       []schemas.MCPClientState
	toolsByClient map[string][]schemas.ChatTool
	execFn        func(ctx *schemas.BifrostContext, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError)
	getToolCalls  atomic.Int32
	cleanupCalls  atomic.Int32
	cleanupErr    error
}

func (f *fakeMCPManager) GetClients() []schemas.MCPClientState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]schemas.MCPClientState, len(f.clients))
	copy(out, f.clients)
	return out
}

func (f *fakeMCPManager) GetToolPerClient(_ context.Context) map[string][]schemas.ChatTool {
	f.getToolCalls.Add(1)
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string][]schemas.ChatTool, len(f.toolsByClient))
	for k, v := range f.toolsByClient {
		cp := make([]schemas.ChatTool, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

func (f *fakeMCPManager) setTools(toolsByClient map[string][]schemas.ChatTool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toolsByClient = toolsByClient
}

func (f *fakeMCPManager) ExecuteChatTool(ctx *schemas.BifrostContext, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	if f.execFn != nil {
		return f.execFn(ctx, toolCall)
	}
	text := "ok"
	return &schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleTool,
		Content: &schemas.ChatMessageContent{ContentStr: &text},
	}, nil
}

func (f *fakeMCPManager) Cleanup() error {
	f.cleanupCalls.Add(1)
	return f.cleanupErr
}

// dummyBuiltinTool is used to test collision protection and Stop cleanup.
type dummyBuiltinTool struct {
	name string
}

func (d dummyBuiltinTool) Name() string                                 { return d.name }
func (d dummyBuiltinTool) Description() string                          { return "built-in" }
func (d dummyBuiltinTool) Call(context.Context, string) (string, error) { return "builtin", nil }
func (d dummyBuiltinTool) Format(string, string, error) string          { return "" }
func (d dummyBuiltinTool) ParameterSchema() map[string]any              { return nil }

func ctxWithPerm(t *testing.T, permStr string) context.Context {
	t.Helper()
	perm, err := tools.ParsePermissions(permStr)
	require.NoError(t, err)
	return context.WithValue(context.Background(), tools.PermissionsKey{}, perm)
}

func makeChatTool(name, desc, paramsJSON string) schemas.ChatTool {
	var params *schemas.ToolFunctionParameters
	if paramsJSON != "" {
		params = &schemas.ToolFunctionParameters{}
		if err := json.Unmarshal([]byte(paramsJSON), params); err != nil {
			panic(err)
		}
	}
	d := desc
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        name,
			Description: &d,
			Parameters:  params,
		},
	}
}

func TestTranslateConfig_StdioAndDefaults(t *testing.T) {
	cfg := config.MCPConfig{
		ToolTimeout: 45000,
		Servers: []config.MCPServerConfig{
			{
				ID:      "fs",
				Command: []string{"npx", "-y", "@modelcontextprotocol/server-filesystem", "."},
				Env:     []string{"FOO=bar", "EXISTING_VAR"},
			},
			{
				ID:        "my-term",
				Transport: "stdio",
				Command:   []string{"/usr/local/bin/term-mcp"},
			},
			{
				ID:      "my_term",
				Command: []string{"/usr/local/bin/term-mcp-2"},
			},
		},
	}

	got, err := TranslateConfig(cfg)
	require.NoError(t, err)
	require.NotNil(t, got.ToolManagerConfig)
	assert.True(t, got.ToolManagerConfig.DisableAutoToolInject)
	assert.Equal(t, schemas.Duration(45*time.Second), got.ToolManagerConfig.ToolExecutionTimeout)
	require.Len(t, got.ClientConfigs, 3)

	c0 := got.ClientConfigs[0]
	assert.Equal(t, "fs", c0.ID)
	assert.Equal(t, "fs", c0.Name)
	assert.Equal(t, schemas.MCPConnectionTypeSTDIO, c0.ConnectionType)
	assert.Equal(t, schemas.MCPAuthTypeNone, c0.AuthType)
	assert.Equal(t, schemas.WhiteList{"*"}, c0.ToolsToExecute)
	require.NotNil(t, c0.StdioConfig)
	assert.Equal(t, "npx", c0.StdioConfig.Command)
	assert.Equal(t, []string{"-y", "@modelcontextprotocol/server-filesystem", "."}, c0.StdioConfig.Args)
	assert.Equal(t, []string{"FOO=bar", "EXISTING_VAR"}, c0.StdioConfig.Envs)

	// Sanitized client names must satisfy Bifrost's client name validator and be unique.
	c1 := got.ClientConfigs[1]
	c2 := got.ClientConfigs[2]
	assert.Equal(t, "my-term", c1.ID)
	assert.Equal(t, "my_term", c1.Name)
	assert.NoError(t, bifrostmcp.ValidateMCPClientName(c1.Name))
	assert.Equal(t, "my_term", c2.ID)
	assert.NotEqual(t, c1.Name, c2.Name)
	assert.NoError(t, bifrostmcp.ValidateMCPClientName(c2.Name))

	// Zero ToolTimeout falls back to DefaultMCPToolTimeoutMS.
	defaultGot, err := TranslateConfig(config.MCPConfig{})
	require.NoError(t, err)
	assert.Equal(t, schemas.Duration(time.Duration(config.DefaultMCPToolTimeoutMS)*time.Millisecond),
		defaultGot.ToolManagerConfig.ToolExecutionTimeout)
	assert.True(t, defaultGot.ToolManagerConfig.DisableAutoToolInject)
}

func TestTranslateConfig_RejectsNonStdioCitingGO20266911(t *testing.T) {
	cases := []struct {
		name string
		srv  config.MCPServerConfig
	}{
		{
			name: "url rejected",
			srv: config.MCPServerConfig{
				ID:  "remote",
				URL: "https://example.com/mcp",
			},
		},
		{
			name: "http transport rejected",
			srv: config.MCPServerConfig{
				ID:        "remote-http",
				Transport: "http",
				Command:   []string{"echo"},
			},
		},
		{
			name: "sse transport rejected",
			srv: config.MCPServerConfig{
				ID:        "remote-sse",
				Transport: "sse",
				Command:   []string{"echo"},
			},
		},
		{
			name: "streamable-http transport rejected",
			srv: config.MCPServerConfig{
				ID:        "remote-streamable",
				Transport: "streamable-http",
				Command:   []string{"echo"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.MCPConfig{Servers: []config.MCPServerConfig{tc.srv}}

			_, err := TranslateConfig(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "GO-2026-6911")

			_, err = NewBridge(BridgeOpts{Config: cfg})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "GO-2026-6911")
		})
	}
}

func TestBridge_EmptyConfigNoOp(t *testing.T) {
	factoryCalled := false
	reg := tools.NewToolRegistry()
	bridge, err := NewBridge(BridgeOpts{
		Config:   config.DefaultMCPConfig(),
		Registry: reg,
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			factoryCalled = true
			return &fakeMCPManager{}, nil
		},
	})
	require.NoError(t, err)

	require.NoError(t, bridge.Start(context.Background()))
	assert.False(t, factoryCalled, "empty config must not start an MCPClientManager")
	assert.Empty(t, bridge.Status())
	assert.Empty(t, reg.Tools())

	// Repeated Start and Stop remain safe no-ops.
	require.NoError(t, bridge.Start(context.Background()))
	require.NoError(t, bridge.Stop())
	require.NoError(t, bridge.Stop())
}

func TestSchemaHash_DeterministicCanonicalJSON(t *testing.T) {
	p1 := &schemas.ToolFunctionParameters{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"type": "object",
		"properties": {
			"z": {"type": "string", "description": "last"},
			"a": {"description": "first", "type": "integer"}
		},
		"required": ["a"]
	}`), p1))

	p2 := &schemas.ToolFunctionParameters{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"required": ["a"],
		"properties": {
			"a": {"type": "integer", "description": "first"},
			"z": {"description": "last", "type": "string"}
		},
		"type": "object"
	}`), p2))

	h1 := SchemaHash("read_file", "Reads a file", p1)
	h2 := SchemaHash("read_file", "Reads a file", p2)
	assert.Len(t, h1, 64, "SchemaHash should be hex-encoded SHA-256")
	assert.Equal(t, h1, h2, "semantically identical schemas in different key orders must hash identically")

	// Changing name, description, or parameters must change the hash.
	assert.NotEqual(t, h1, SchemaHash("write_file", "Reads a file", p1))
	assert.NotEqual(t, h1, SchemaHash("read_file", "Reads a file differently", p1))
	assert.NotEqual(t, h1, SchemaHash("read_file", "Reads a file", nil))
	assert.Equal(t, SchemaHash("noop", "desc", nil), SchemaHash("noop", "desc", nil))
}

func TestStatusStructs_WireRoundTrip(t *testing.T) {
	in := ServerStatus{
		ID:      "fs",
		Name:    "fs",
		State:   "connected",
		Command: []string{"npx", "server"},
		Tools: []ToolStatus{
			{
				Name:           "read_file",
				RegisteredName: "fs-read_file",
				Description:    "Reads a file",
				State:          ToolStateSealed,
				Permissions:    "r--------",
				SchemaHash:     "deadbeef",
				Registered:     true,
			},
		},
	}

	mpBytes, err := msgpack.Marshal(in)
	require.NoError(t, err)
	var mpOut ServerStatus
	require.NoError(t, msgpack.Unmarshal(mpBytes, &mpOut))
	assert.Equal(t, in, mpOut)

	jsonBytes, err := json.Marshal(in)
	require.NoError(t, err)
	var jsonOut ServerStatus
	require.NoError(t, json.Unmarshal(jsonBytes, &jsonOut))
	assert.Equal(t, in, jsonOut)
}

func TestMCPToolAdapter_SchemaExecutionPermissionsTimeoutTruncation(t *testing.T) {
	earthRead, err := tools.ParsePermissions("r--------")
	require.NoError(t, err)

	chatTool := makeChatTool(
		"fs-read_file",
		"Read a file from the envoy",
		`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`,
	)

	t.Run("parameter schema and metadata", func(t *testing.T) {
		adapter := NewMCPToolAdapter(MCPToolAdapterOpts{
			ServerID:    "fs",
			Tool:        chatTool,
			Permissions: earthRead,
		})
		assert.Equal(t, "fs-read_file", adapter.Name())
		assert.Equal(t, "Read a file from the envoy", adapter.Description())

		schema := adapter.ParameterSchema()
		assert.Equal(t, "object", schema["type"])
		props, ok := schema["properties"].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, props, "path")

		// Nil params fall back to an empty object schema.
		nilParamsAdapter := NewMCPToolAdapter(MCPToolAdapterOpts{
			Tool:        makeChatTool("fs-ping", "Ping", ""),
			Permissions: earthRead,
		})
		nilSchema := nilParamsAdapter.ParameterSchema()
		assert.Equal(t, "object", nilSchema["type"])
		assert.NotNil(t, nilSchema["properties"])

		// Format covers success, empty result, and error.
		assert.Contains(t, adapter.Format(`{"path":"a.go"}`, "hello", nil), "fs-read_file")
		assert.Contains(t, adapter.Format(`{}`, "", nil), "Completed")
		assert.Contains(t, adapter.Format(`{}`, "", errors.New("boom")), "boom")
	})

	t.Run("execution and argument forwarding", func(t *testing.T) {
		var capturedName, capturedArgs string
		mgr := &fakeMCPManager{
			execFn: func(_ *schemas.BifrostContext, tc *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
				require.NotNil(t, tc.Function.Name)
				capturedName = *tc.Function.Name
				capturedArgs = tc.Function.Arguments
				out := "file-contents"
				return &schemas.ChatMessage{
					Role:    schemas.ChatMessageRoleTool,
					Content: &schemas.ChatMessageContent{ContentStr: &out},
				}, nil
			},
		}

		adapter := NewMCPToolAdapter(MCPToolAdapterOpts{
			ServerID:    "fs",
			Name:        "fs-read_file",
			Tool:        chatTool,
			Permissions: earthRead,
			Manager:     mgr,
		})

		got, callErr := adapter.Call(ctxWithPerm(t, "rwx------"), `{"path":"README.md"}`)
		require.NoError(t, callErr)
		assert.Equal(t, "file-contents", got)
		assert.Equal(t, "fs-read_file", capturedName)
		assert.Equal(t, `{"path":"README.md"}`, capturedArgs)

		// Empty input defaults to "{}".
		_, callErr = adapter.Call(ctxWithPerm(t, "r--------"), "   ")
		require.NoError(t, callErr)
		assert.Equal(t, "{}", capturedArgs)

		// ContentBlocks fallback.
		p1, p2 := "line1", "line2"
		mgr.execFn = func(_ *schemas.BifrostContext, _ *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
			return &schemas.ChatMessage{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{Type: schemas.ChatContentBlockTypeText, Text: &p1},
						{Type: schemas.ChatContentBlockTypeText, Text: &p2},
					},
				},
			}, nil
		}
		got, callErr = adapter.Call(ctxWithPerm(t, "r--------"), `{}`)
		require.NoError(t, callErr)
		assert.Equal(t, "line1\nline2", got)

		// BifrostError is surfaced cleanly.
		mgr.execFn = func(_ *schemas.BifrostContext, _ *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream failure"}}
		}
		_, callErr = adapter.Call(ctxWithPerm(t, "r--------"), `{}`)
		require.Error(t, callErr)
		assert.Contains(t, callErr.Error(), "upstream failure")
	})

	t.Run("execution-time permission gating and live registry check", func(t *testing.T) {
		executed := false
		mgr := &fakeMCPManager{
			execFn: func(_ *schemas.BifrostContext, _ *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
				executed = true
				out := "ok"
				return &schemas.ChatMessage{
					Role:    schemas.ChatMessageRoleTool,
					Content: &schemas.ChatMessageContent{ContentStr: &out},
				}, nil
			},
		}

		reg := tools.NewToolRegistry()
		adapter := NewMCPToolAdapter(MCPToolAdapterOpts{
			ServerID:    "fs",
			Name:        "fs-read_file",
			Tool:        chatTool,
			Permissions: earthRead,
			Manager:     mgr,
			Registry:    reg,
		})
		reg.Set(adapter, earthRead)

		// Missing permissions in context fails closed.
		_, callErr := adapter.Call(context.Background(), `{}`)
		require.Error(t, callErr)
		assert.Contains(t, callErr.Error(), "permission denied")
		assert.False(t, executed)

		// Non-matching realm permissions fail closed.
		_, callErr = adapter.Call(ctxWithPerm(t, "---rwx---"), `{}`)
		require.Error(t, callErr)
		assert.Contains(t, callErr.Error(), "permission denied")
		assert.False(t, executed)

		// Matching permissions succeed.
		_, callErr = adapter.Call(ctxWithPerm(t, "r--------"), `{}`)
		require.NoError(t, callErr)
		assert.True(t, executed)

		// Narrowing permissions in registry takes effect immediately on live adapter.
		executed = false
		earthExec, parseErr := tools.ParsePermissions("--x------")
		require.NoError(t, parseErr)
		reg.Set(adapter, earthExec)
		_, callErr = adapter.Call(ctxWithPerm(t, "r--------"), `{}`)
		require.Error(t, callErr)
		assert.Contains(t, callErr.Error(), "permission denied")
		assert.False(t, executed)

		// Unregistering tool denies further calls immediately.
		reg.Unregister("fs-read_file")
		_, callErr = adapter.Call(ctxWithPerm(t, "rwxrwxrwx"), `{}`)
		require.Error(t, callErr)
		assert.Contains(t, callErr.Error(), "not registered")
		assert.False(t, executed)
	})

	t.Run("tool timeout enforcement", func(t *testing.T) {
		mgr := &fakeMCPManager{
			execFn: func(ctx *schemas.BifrostContext, _ *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
				<-ctx.Done()
				return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: ctx.Err().Error()}}
			},
		}

		adapter := NewMCPToolAdapter(MCPToolAdapterOpts{
			ServerID:    "fs",
			Name:        "fs-slow",
			Tool:        chatTool,
			Permissions: earthRead,
			Manager:     mgr,
			ToolTimeout: 30 * time.Millisecond,
		})

		start := time.Now()
		_, callErr := adapter.Call(ctxWithPerm(t, "r--------"), `{}`)
		require.Error(t, callErr)
		assert.ErrorIs(t, callErr, context.DeadlineExceeded)
		assert.Less(t, time.Since(start), time.Second)
	})

	t.Run("max output tokens truncation", func(t *testing.T) {
		largeOutput := strings.Repeat("abcdefghij", 50) // 500 runes
		mgr := &fakeMCPManager{
			execFn: func(_ *schemas.BifrostContext, _ *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
				return &schemas.ChatMessage{
					Role:    schemas.ChatMessageRoleTool,
					Content: &schemas.ChatMessageContent{ContentStr: &largeOutput},
				}, nil
			},
		}

		adapter := NewMCPToolAdapter(MCPToolAdapterOpts{
			ServerID:        "fs",
			Name:            "fs-big",
			Tool:            chatTool,
			Permissions:     earthRead,
			Manager:         mgr,
			MaxOutputTokens: 20, // 20 * 4 = 80 runes max
		})

		got, callErr := adapter.Call(ctxWithPerm(t, "r--------"), `{}`)
		require.NoError(t, callErr)
		assert.LessOrEqual(t, utf8.RuneCountInString(got), 20*charsPerToken)
		assert.True(t, strings.HasSuffix(got, truncationSuffix))
		assert.True(t, utf8.ValidString(got))

		// Multi-byte UTF-8 output stays valid UTF-8 after truncation.
		utf8Output := strings.Repeat("é", 200)
		mgr.execFn = func(_ *schemas.BifrostContext, _ *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
			return &schemas.ChatMessage{
				Role:    schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{ContentStr: &utf8Output},
			}, nil
		}
		got, callErr = adapter.Call(ctxWithPerm(t, "r--------"), `{}`)
		require.NoError(t, callErr)
		assert.LessOrEqual(t, utf8.RuneCountInString(got), 20*charsPerToken)
		assert.True(t, utf8.ValidString(got))

		// Short output is not modified.
		shortOutput := "short"
		mgr.execFn = func(_ *schemas.BifrostContext, _ *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
			return &schemas.ChatMessage{
				Role:    schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{ContentStr: &shortOutput},
			}, nil
		}
		got, callErr = adapter.Call(ctxWithPerm(t, "r--------"), `{}`)
		require.NoError(t, callErr)
		assert.Equal(t, "short", got)
	})
}

func TestBridge_SyncPollingDBCacheAndStopCleanup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&storage.MCPToolClassification{}))

	// Pre-seed a sealed classification in DB for server "term" tool "screen".
	screenTool := makeChatTool("term-screen", "Read terminal screen", `{"type":"object","properties":{}}`)
	screenHash := SchemaHash("screen", "Read terminal screen", screenTool.Function.Parameters)
	require.NoError(t, storage.SaveMCPToolClassification(db, &storage.MCPToolClassification{
		Username:    "alice",
		Project:     "acme/app",
		ServerID:    "term",
		ToolName:    "screen",
		Permissions: "r--------",
		SchemaHash:  screenHash,
		CommandHash: CommandHash([]string{"/usr/bin/term-mcp"}),
		SealedBy:    "alice",
	}))

	reg := tools.NewToolRegistry()
	earthRead, err := tools.ParsePermissions("r--------")
	require.NoError(t, err)
	// Built-in tool that must survive Stop() untouched.
	reg.Register(dummyBuiltinTool{name: "read_file"}, earthRead)

	mgr := &fakeMCPManager{
		clients: []schemas.MCPClientState{
			{
				Name:            "term",
				ExecutionConfig: &schemas.MCPClientConfig{ID: "term", Name: "term"},
				State:           schemas.MCPConnectionStateConnected,
			},
		},
		toolsByClient: map[string][]schemas.ChatTool{
			"term": {
				screenTool,
				makeChatTool("term-send_keys", "Send keystrokes", `{"type":"object","properties":{"keys":{"type":"string"}}}`),
				makeChatTool("term-blocked", "Blocked tool", `{"type":"object","properties":{}}`),
				makeChatTool("term-unknown", "Unclassified tool", `{"type":"object","properties":{}}`),
			},
		},
	}

	cfg := config.MCPConfig{
		ToolTimeout:     5000,
		MaxOutputTokens: 1000,
		Servers: []config.MCPServerConfig{
			{
				ID:      "term",
				Command: []string{"/usr/bin/term-mcp"},
				Tools: []config.MCPToolOverride{
					{Name: "send_keys", Permissions: "--x------"},
					{Name: "blocked", Permissions: "---------"},
				},
			},
		},
	}

	bridge, err := NewBridge(BridgeOpts{
		Config:       cfg,
		Registry:     reg,
		DB:           db,
		Username:     "alice",
		Project:      "acme/app",
		SyncInterval: 20 * time.Millisecond,
		ManagerFactory: func(context.Context, schemas.MCPConfig) (MCPClientManager, error) {
			return mgr, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, bridge.Start(context.Background()))

	// Initial sync registered sealed ("term-screen") and preclassified ("term-send_keys"),
	// while withholding denied ("term-blocked") and unclassified ("term-unknown").
	_, hasScreen := reg.Get("term-screen")
	_, hasSendKeys := reg.Get("term-send_keys")
	_, hasBlocked := reg.Get("term-blocked")
	_, hasUnknown := reg.Get("term-unknown")
	assert.True(t, hasScreen, "sealed tool with matching hash must be registered")
	assert.True(t, hasSendKeys, "preclassified tool must be registered")
	assert.False(t, hasBlocked, "sealed-deny (---------) tool must not be registered")
	assert.False(t, hasUnknown, "unclassified tool must not be registered")

	statuses := bridge.Status()
	require.Len(t, statuses, 1)
	assert.Equal(t, "term", statuses[0].ID)
	assert.Equal(t, "connected", statuses[0].State)
	require.Len(t, statuses[0].Tools, 4)

	byName := make(map[string]ToolStatus, len(statuses[0].Tools))
	for _, ts := range statuses[0].Tools {
		byName[ts.Name] = ts
	}
	assert.Equal(t, ToolStateSealed, byName["screen"].State)
	assert.True(t, byName["screen"].Registered)
	assert.Equal(t, ToolStatePreclassified, byName["send_keys"].State)
	assert.True(t, byName["send_keys"].Registered)
	assert.Equal(t, ToolStatePreclassified, byName["blocked"].State)
	assert.False(t, byName["blocked"].Registered)
	assert.Equal(t, ToolStateUnclassified, byName["unknown"].State)
	assert.False(t, byName["unknown"].Registered)

	// Periodic sync polling detects removed tools and unregisters them.
	mgr.setTools(map[string][]schemas.ChatTool{
		"term": {screenTool},
	})
	require.Eventually(t, func() bool {
		_, stillThere := reg.Get("term-send_keys")
		return !stillThere && mgr.getToolCalls.Load() >= 2
	}, time.Second, 10*time.Millisecond, "periodic sync should poll GetToolPerClient and unregister removed tools")

	// Stop tears down polling, unregisters all remaining MCP tools, leaves built-in tools intact, and calls Cleanup().
	require.NoError(t, bridge.Stop())

	_, hasScreen = reg.Get("term-screen")
	_, hasBuiltin := reg.Get("read_file")
	assert.False(t, hasScreen, "Stop must unregister MCP tools")
	assert.True(t, hasBuiltin, "Stop must not unregister non-MCP built-in tools")
	assert.Equal(t, int32(1), mgr.cleanupCalls.Load(), "Stop must call manager.Cleanup() once")

	stoppedStatus := bridge.Status()
	require.Len(t, stoppedStatus, 1)
	assert.Equal(t, "disconnected", stoppedStatus[0].State)
	for _, ts := range stoppedStatus[0].Tools {
		assert.False(t, ts.Registered)
	}

	// Idempotent second Stop does not call Cleanup again.
	require.NoError(t, bridge.Stop())
	assert.Equal(t, int32(1), mgr.cleanupCalls.Load())
}
