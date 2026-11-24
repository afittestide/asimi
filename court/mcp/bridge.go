package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/google/uuid"
	bifrostmcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

const (
	// DefaultSyncInterval is how often Bridge polls GetToolPerClient to
	// reconcile added, modified, or removed MCP tools.
	DefaultSyncInterval = 30 * time.Second

	// charsPerToken is the conservative character-to-token ratio used to
	// enforce MaxOutputTokens on MCP tool output (handles both prose and
	// dense whitespace-free JSON blobs).
	charsPerToken    = 4
	truncationSuffix = "\n...[truncated]"
)

// TranslateConfig validates cfg via config.ValidateMCPConfig (rejecting any
// non-stdio transport citing GO-2026-6911) and translates it into a Bifrost
// schemas.MCPConfig configured for stdio-only execution with auto-tool-inject
// disabled so Bifrost never bypasses Asimi's tool registry or permission gate.
func TranslateConfig(cfg config.MCPConfig) (schemas.MCPConfig, error) {
	if err := config.ValidateMCPConfig(cfg); err != nil {
		return schemas.MCPConfig{}, err
	}

	toolTimeoutMS := cfg.ToolTimeout
	if toolTimeoutMS <= 0 {
		toolTimeoutMS = config.DefaultMCPToolTimeoutMS
	}
	toolTimeout := time.Duration(toolTimeoutMS) * time.Millisecond

	clients := make([]*schemas.MCPClientConfig, 0, len(cfg.Servers))
	usedNames := make(map[string]bool, len(cfg.Servers))
	for _, srv := range cfg.Servers {
		clientName := uniqueSanitizedClientName(srv.ID, usedNames)

		var args []string
		if len(srv.Command) > 1 {
			args = append([]string(nil), srv.Command[1:]...)
		}
		var envs []string
		if len(srv.Env) > 0 {
			envs = append([]string(nil), srv.Env...)
		}

		clients = append(clients, &schemas.MCPClientConfig{
			ID:             srv.ID,
			Name:           clientName,
			ConnectionType: schemas.MCPConnectionTypeSTDIO,
			AuthType:       schemas.MCPAuthTypeNone,
			ToolsToExecute: schemas.WhiteList{"*"},
			StdioConfig: &schemas.MCPStdioConfig{
				Command: srv.Command[0],
				Args:    args,
				Envs:    envs,
			},
		})
	}

	return schemas.MCPConfig{
		ClientConfigs: clients,
		ToolManagerConfig: &schemas.MCPToolManagerConfig{
			ToolExecutionTimeout:  schemas.Duration(toolTimeout),
			MaxAgentDepth:         schemas.DefaultMaxAgentDepth,
			DisableAutoToolInject: true,
		},
	}, nil
}

// sanitizeClientName maps an arbitrary Asimi server ID into a Bifrost-valid
// client name (ASCII-only, no spaces or hyphens, cannot start with a digit).
func sanitizeClientName(id string) string {
	if bifrostmcp.ValidateMCPClientName(id) == nil {
		return id
	}
	var b strings.Builder
	for _, r := range id {
		if r > 127 {
			continue
		}
		if r == '-' || r == ' ' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	name := b.String()
	if name == "" {
		name = "mcp_server"
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "mcp_" + name
	}
	return name
}

func uniqueSanitizedClientName(id string, used map[string]bool) string {
	base := sanitizeClientName(id)
	if !used[base] {
		used[base] = true
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s_%d", base, i)
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

// BridgeOpts configures a Bridge instance.
type BridgeOpts struct {
	Config            config.MCPConfig
	Registry          *tools.ToolRegistry
	DB                *gorm.DB
	Username          string
	Project           string
	Logger            *slog.Logger
	SyncInterval      time.Duration
	Proposer          RitesProposer
	Gate              ZhengmingGate
	OnRegistryChanged func()

	// ManagerFactory optionally overrides creation of the underlying
	// MCPClientManager (primarily for testing). When nil, Start constructs a
	// *bifrostmcp.MCPManager via bifrostmcp.NewMCPManager.
	ManagerFactory func(ctx context.Context, cfg schemas.MCPConfig) (MCPClientManager, error)
}

// Bridge manages Bifrost MCP client connections, periodic tool discovery,
// classification lookup, ToolRegistry registration, and clean teardown.
type Bridge struct {
	cfg               config.MCPConfig
	bifrostCfg        schemas.MCPConfig
	registry          *tools.ToolRegistry
	db                *gorm.DB
	username          string
	project           string
	logger            *slog.Logger
	syncInterval      time.Duration
	toolTimeout       time.Duration
	maxOutputTokens   int
	proposer          RitesProposer
	gate              ZhengmingGate
	onRegistryChanged func()
	managerFactory    func(ctx context.Context, cfg schemas.MCPConfig) (MCPClientManager, error)

	serversByID          map[string]config.MCPServerConfig
	commandHashByServer  map[string]string
	clientNameToServerID map[string]string
	serverIDToClientName map[string]string

	mu              sync.RWMutex
	started         bool
	stopping        bool
	manager         MCPClientManager
	cancel          context.CancelFunc
	servers         map[string]*ServerStatus
	registeredTools map[string]registeredToolInfo // registered tool names owned by this bridge
	discovered      map[toolKey]discoveredTool
	inFlight        map[toolKey]*classificationRun
	sealed          map[toolKey]sealedClassification
	rejected        map[toolKey]string // toolKey -> rejected schemaHash
	failed          map[toolKey]string // toolKey -> failed schemaHash
	nextAttemptID   uint64
	wg              sync.WaitGroup
}

// NewBridge validates opts.Config (fail-closed) and initializes a Bridge.
func NewBridge(opts BridgeOpts) (*Bridge, error) {
	bifrostCfg, err := TranslateConfig(opts.Config)
	if err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	syncInterval := opts.SyncInterval
	if syncInterval <= 0 {
		syncInterval = DefaultSyncInterval
	}
	toolTimeoutMS := opts.Config.ToolTimeout
	if toolTimeoutMS <= 0 {
		toolTimeoutMS = config.DefaultMCPToolTimeoutMS
	}
	maxOutputTokens := opts.Config.MaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = config.DefaultMCPMaxOutputTokens
	}

	serversByID := make(map[string]config.MCPServerConfig, len(opts.Config.Servers))
	commandHashByServer := make(map[string]string, len(opts.Config.Servers))
	clientNameToServerID := make(map[string]string, len(bifrostCfg.ClientConfigs))
	serverIDToClientName := make(map[string]string, len(bifrostCfg.ClientConfigs))
	servers := make(map[string]*ServerStatus, len(opts.Config.Servers))

	for i, srv := range opts.Config.Servers {
		serversByID[srv.ID] = srv
		commandHashByServer[srv.ID] = CommandHash(srv.Command)
		clientName := bifrostCfg.ClientConfigs[i].Name
		clientNameToServerID[clientName] = srv.ID
		serverIDToClientName[srv.ID] = clientName
		servers[srv.ID] = &ServerStatus{
			ID:      srv.ID,
			Name:    clientName,
			State:   string(schemas.MCPConnectionStateDisconnected),
			Command: append([]string(nil), srv.Command...),
		}
	}

	return &Bridge{
		cfg:                  opts.Config,
		bifrostCfg:           bifrostCfg,
		registry:             opts.Registry,
		db:                   opts.DB,
		username:             opts.Username,
		project:              opts.Project,
		logger:               logger,
		syncInterval:         syncInterval,
		toolTimeout:          time.Duration(toolTimeoutMS) * time.Millisecond,
		maxOutputTokens:      maxOutputTokens,
		proposer:             opts.Proposer,
		gate:                 opts.Gate,
		onRegistryChanged:    opts.OnRegistryChanged,
		managerFactory:       opts.ManagerFactory,
		serversByID:          serversByID,
		commandHashByServer:  commandHashByServer,
		clientNameToServerID: clientNameToServerID,
		serverIDToClientName: serverIDToClientName,
		servers:              servers,
		registeredTools:      make(map[string]registeredToolInfo),
		discovered:           make(map[toolKey]discoveredTool),
		inFlight:             make(map[toolKey]*classificationRun),
		sealed:               make(map[toolKey]sealedClassification),
		rejected:             make(map[toolKey]string),
		failed:               make(map[toolKey]string),
	}, nil
}

// Start connects configured stdio MCP servers via Bifrost and launches the
// periodic tool sync loop. When no servers are configured, Start is a clean
// no-op.
func (b *Bridge) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return nil
	}
	if len(b.cfg.Servers) == 0 {
		b.started = true
		b.mu.Unlock()
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	bridgeCtx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	b.started = true
	b.stopping = false
	factory := b.managerFactory
	bifrostCfg := b.bifrostCfg
	logger := b.logger
	b.mu.Unlock()

	var (
		mgr MCPClientManager
		err error
	)
	if factory != nil {
		mgr, err = factory(bridgeCtx, bifrostCfg)
	} else {
		mgr = bifrostmcp.NewMCPManager(bridgeCtx, bifrostCfg, nil, newSlogBifrostLogger(logger), nil)
	}
	if err != nil {
		cancel()
		b.mu.Lock()
		b.started = false
		b.cancel = nil
		b.mu.Unlock()
		return fmt.Errorf("start mcp manager: %w", err)
	}

	b.mu.Lock()
	// Guard against a concurrent Stop() while the manager was initializing.
	if !b.started {
		b.mu.Unlock()
		_ = mgr.Cleanup()
		return nil
	}
	b.manager = mgr
	b.wg.Add(1)
	b.mu.Unlock()

	b.Reconcile(bridgeCtx)

	go b.pollLoop(bridgeCtx)
	return nil
}

func (b *Bridge) pollLoop(ctx context.Context) {
	defer b.wg.Done()
	ticker := time.NewTicker(b.syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.Reconcile(ctx)
		}
	}
}

// Stop cancels the periodic sync loop and any in-flight classification work,
// unregisters all MCP tools added to the ToolRegistry by this bridge, and calls
// Cleanup on the underlying manager. Stop is idempotent and safe to call even
// if Start was never called or was a no-op.
func (b *Bridge) Stop() error {
	b.mu.Lock()
	if !b.started && b.manager == nil {
		b.mu.Unlock()
		return nil
	}
	cancel := b.cancel
	mgr := b.manager
	b.started = false
	b.stopping = true
	b.cancel = nil
	b.manager = nil
	inFlightCancels := make([]context.CancelFunc, 0, len(b.inFlight))
	for _, run := range b.inFlight {
		if run != nil && run.cancel != nil {
			inFlightCancels = append(inFlightCancels, run.cancel)
		}
	}
	b.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, c := range inFlightCancels {
		c()
	}
	b.wg.Wait()

	b.mu.Lock()
	toUnregister := make([]string, 0, len(b.registeredTools))
	for name := range b.registeredTools {
		toUnregister = append(toUnregister, name)
	}
	if b.registry != nil {
		for _, name := range toUnregister {
			b.registry.Unregister(name)
		}
	}
	b.registeredTools = make(map[string]registeredToolInfo)
	b.discovered = make(map[toolKey]discoveredTool)
	b.inFlight = make(map[toolKey]*classificationRun)
	b.sealed = make(map[toolKey]sealedClassification)
	b.rejected = make(map[toolKey]string)
	b.failed = make(map[toolKey]string)
	for _, st := range b.servers {
		st.State = string(schemas.MCPConnectionStateDisconnected)
		for i := range st.Tools {
			st.Tools[i].Registered = false
		}
	}
	b.stopping = false
	onChanged := b.onRegistryChanged
	b.mu.Unlock()

	if len(toUnregister) > 0 && b.registry != nil && onChanged != nil {
		onChanged()
	}

	if mgr != nil {
		return mgr.Cleanup()
	}
	return nil
}

// Status returns a wire-safe snapshot of all configured MCP servers and their
// discovered tools, sorted deterministically by server ID and tool name.
func (b *Bridge) Status() []ServerStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(b.servers) == 0 {
		return nil
	}

	ids := make([]string, 0, len(b.servers))
	for id := range b.servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]ServerStatus, 0, len(ids))
	for _, id := range ids {
		st := b.servers[id]
		cp := ServerStatus{
			ID:      st.ID,
			Name:    st.Name,
			State:   st.State,
			Command: append([]string(nil), st.Command...),
			Error:   st.Error,
		}
		if len(st.Tools) > 0 {
			cp.Tools = make([]ToolStatus, len(st.Tools))
			copy(cp.Tools, st.Tools)
			sort.Slice(cp.Tools, func(i, j int) bool {
				return cp.Tools[i].Name < cp.Tools[j].Name
			})
		}
		out = append(out, cp)
	}
	return out
}

func (b *Bridge) resolveServerID(c schemas.MCPClientState) string {
	if c.ExecutionConfig != nil && c.ExecutionConfig.ID != "" {
		return c.ExecutionConfig.ID
	}
	if c.ExecutionConfig != nil && c.ExecutionConfig.Name != "" {
		return b.serverIDForClientName(c.ExecutionConfig.Name)
	}
	return b.serverIDForClientName(c.Name)
}

func (b *Bridge) serverIDForClientName(clientName string) string {
	if id, ok := b.clientNameToServerID[clientName]; ok {
		return id
	}
	return clientName
}

func (b *Bridge) commandHashForServer(serverID string) string {
	return b.commandHashByServer[serverID]
}

// MCPToolAdapterOpts configures an MCPToolAdapter.
type MCPToolAdapterOpts struct {
	ServerID        string
	Name            string // Registry tool name; defaults to Tool.Function.Name when empty.
	Tool            schemas.ChatTool
	Permissions     tools.Permissions
	Manager         MCPClientManager
	Registry        *tools.ToolRegistry // Optional; when set, Call verifies live registration & permissions.
	ToolTimeout     time.Duration
	MaxOutputTokens int
}

// MCPToolAdapter adapts a Bifrost MCP tool to Asimi's tools.Tool interface,
// enforcing execution-time permission gating, per-call timeout, and output
// token bounds.
type MCPToolAdapter struct {
	serverID        string
	name            string
	bifrostName     string
	description     string
	params          *schemas.ToolFunctionParameters
	permissions     tools.Permissions
	manager         MCPClientManager
	registry        *tools.ToolRegistry
	toolTimeout     time.Duration
	maxOutputTokens int
}

// Compile-time check that *MCPToolAdapter implements tools.Tool.
var _ tools.Tool = (*MCPToolAdapter)(nil)

// NewMCPToolAdapter constructs an MCPToolAdapter with defaults applied for
// unset timeout and output token bounds.
func NewMCPToolAdapter(opts MCPToolAdapterOpts) *MCPToolAdapter {
	var (
		bifrostName string
		description string
		params      *schemas.ToolFunctionParameters
	)
	if opts.Tool.Function != nil {
		bifrostName = opts.Tool.Function.Name
		if opts.Tool.Function.Description != nil {
			description = *opts.Tool.Function.Description
		}
		params = opts.Tool.Function.Parameters
	}
	name := opts.Name
	if name == "" {
		name = bifrostName
	}
	toolTimeout := opts.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = time.Duration(config.DefaultMCPToolTimeoutMS) * time.Millisecond
	}
	maxOutputTokens := opts.MaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = config.DefaultMCPMaxOutputTokens
	}

	return &MCPToolAdapter{
		serverID:        opts.ServerID,
		name:            name,
		bifrostName:     bifrostName,
		description:     description,
		params:          params,
		permissions:     opts.Permissions,
		manager:         opts.Manager,
		registry:        opts.Registry,
		toolTimeout:     toolTimeout,
		maxOutputTokens: maxOutputTokens,
	}
}

func (a *MCPToolAdapter) Name() string {
	return a.name
}

func (a *MCPToolAdapter) Description() string {
	return a.description
}

func (a *MCPToolAdapter) ParameterSchema() map[string]any {
	if a.params == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	raw, err := json.Marshal(a.params)
	if err != nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil || schema == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
	}
	if _, ok := schema["properties"]; !ok {
		schema["properties"] = map[string]any{}
	}
	return schema
}

func (a *MCPToolAdapter) Call(ctx context.Context, input string) (string, error) {
	// Execution-time permission gating (fail-closed).
	callerPerm, ok := tools.PermissionsFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("permission denied: missing caller permissions for MCP tool %q", a.name)
	}

	effectivePerm := a.permissions
	if a.registry != nil {
		tp, registered := a.registry.Get(a.name)
		if !registered {
			return "", fmt.Errorf("permission denied: MCP tool %q is not registered", a.name)
		}
		effectivePerm = tp.Permissions
	}

	if !callerPerm.Allows(effectivePerm) {
		return "", fmt.Errorf("permission denied: caller permissions %s do not satisfy MCP tool %q (%s)",
			callerPerm.String(), a.name, effectivePerm.String())
	}

	if a.manager == nil {
		return "", fmt.Errorf("mcp tool %q: client manager is not initialized", a.name)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	var (
		bifrostCtx *schemas.BifrostContext
		cancel     context.CancelFunc
	)
	if a.toolTimeout > 0 {
		bifrostCtx, cancel = schemas.NewBifrostContextWithTimeout(ctx, a.toolTimeout)
	} else {
		bifrostCtx, cancel = schemas.NewBifrostContextWithCancel(ctx)
	}
	defer cancel()

	toolType := string(schemas.ChatToolTypeFunction)
	callID := uuid.NewString()
	callName := a.bifrostName
	if callName == "" {
		callName = a.name
	}
	args := input
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}

	toolCall := &schemas.ChatAssistantMessageToolCall{
		Type: &toolType,
		ID:   &callID,
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &callName,
			Arguments: args,
		},
	}

	msg, bErr := a.manager.ExecuteChatTool(bifrostCtx, toolCall)
	if bErr != nil {
		if errors.Is(bifrostCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("mcp tool %q timed out after %s: %w", a.name, a.toolTimeout, context.DeadlineExceeded)
		}
		errMsg := "tool execution failed"
		if bErr.Error != nil && bErr.Error.Message != "" {
			errMsg = bErr.Error.Message
		}
		return "", fmt.Errorf("mcp tool %q failed: %s", a.name, errMsg)
	}
	if errors.Is(bifrostCtx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("mcp tool %q timed out after %s: %w", a.name, a.toolTimeout, context.DeadlineExceeded)
	}

	output := extractChatMessageText(msg)
	return truncateOutputToTokens(output, a.maxOutputTokens), nil
}

func (a *MCPToolAdapter) Format(input, result string, err error) string {
	msg := utils.NewMsgBlockBuilder(a.name)
	msg.WriteLn()
	if err != nil {
		msg.Writef("Error: %v", err)
	} else if result == "" {
		msg.Writef("Completed")
	} else {
		msg.Writef("%s", utils.TruncateMiddle(result, 200))
	}
	return msg.String() + "\n"
}

func extractChatMessageText(msg *schemas.ChatMessage) string {
	if msg == nil || msg.Content == nil {
		return ""
	}
	if msg.Content.ContentStr != nil {
		return *msg.Content.ContentStr
	}
	if len(msg.Content.ContentBlocks) == 0 {
		return ""
	}
	var parts []string
	for _, block := range msg.Content.ContentBlocks {
		if block.Text != nil {
			parts = append(parts, *block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// truncateOutputToTokens bounds output to at most maxTokens estimated tokens
// (using charsPerToken runes per token), appending truncationSuffix when room
// permits and preserving valid UTF-8 boundaries.
func truncateOutputToTokens(output string, maxTokens int) string {
	if maxTokens <= 0 {
		return output
	}
	maxRunes := maxTokens * charsPerToken
	if utf8.RuneCountInString(output) <= maxRunes {
		return output
	}
	runes := []rune(output)
	suffixRunes := utf8.RuneCountInString(truncationSuffix)
	if maxRunes <= suffixRunes {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-suffixRunes]) + truncationSuffix
}

// slogBifrostLogger adapts *slog.Logger to Bifrost's schemas.Logger interface.
type slogBifrostLogger struct {
	logger *slog.Logger
}

func newSlogBifrostLogger(logger *slog.Logger) *slogBifrostLogger {
	if logger == nil {
		logger = slog.Default()
	}
	return &slogBifrostLogger{logger: logger}
}

func (l *slogBifrostLogger) log(level slog.Level, msg string, args ...any) {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	l.logger.Log(context.Background(), level, msg)
}

func (l *slogBifrostLogger) Debug(msg string, args ...any)          { l.log(slog.LevelDebug, msg, args...) }
func (l *slogBifrostLogger) Info(msg string, args ...any)           { l.log(slog.LevelInfo, msg, args...) }
func (l *slogBifrostLogger) Warn(msg string, args ...any)           { l.log(slog.LevelWarn, msg, args...) }
func (l *slogBifrostLogger) Error(msg string, args ...any)          { l.log(slog.LevelError, msg, args...) }
func (l *slogBifrostLogger) Fatal(msg string, args ...any)          { l.log(slog.LevelError, msg, args...) }
func (l *slogBifrostLogger) SetLevel(schemas.LogLevel)              {}
func (l *slogBifrostLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l *slogBifrostLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}
