package config

import (
	"fmt"
	"os/user"
	"strings"
	"time"

	"github.com/afittestide/asimi/internal/types"
)

// Config represents the application configuration structure
type Config struct {
	Storage         StorageConfig         `koanf:"storage"`
	Logging         LoggingConfig         `koanf:"logging"`
	UI              UIConfig              `koanf:"ui"`
	LLM             LLMConfig             `koanf:"llm"`
	History         HistoryConfig         `koanf:"history"`
	Session         SessionConfig         `koanf:"session"`
	Sandbox         SandboxConfig         `koanf:"sandbox"`
	RunShellCommand RunShellCommandConfig `koanf:"run_shell_command"`
	Court           CourtConfig           `koanf:"court"`
	MCP             MCPConfig             `koanf:"mcp"`
}

// RunShellCommandConfig holds settings for the run_shell_command tool's
// host execution path.
type RunShellCommandConfig struct {
	// TimeoutMinutes is the timeout for host commands in minutes (default: 10)
	TimeoutMinutes int `koanf:"timeout_minutes"`
}

// StorageConfig holds storage configuration
type StorageConfig struct {
	DatabasePath string `koanf:"database_path"` // Path to SQLite database
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

// LLMConfig holds LLM provider settings.
// This type is shared between main, storage, and court packages.
type LLMConfig struct {
	Provider                   string `koanf:"provider"`
	Model                      string `koanf:"model"`
	ReasoningEffort            string `koanf:"reasoning_effort"` // "low"|"medium"|"high"; empty = provider default
	APIKey                     string `koanf:"api_key"`
	BaseURL                    string `koanf:"base_url"`
	MaxThinkingTokens          int    `koanf:"max_thinking_tokens"`
	MaxTurns                   int    `koanf:"max_turns"`
	DisableContextSanitization bool   `koanf:"disable_sanitization"`
	MaxToolOutput              int    `koanf:"max_tool_output"`             // Maximum tool output size in bytes (read_file, shell, etc.)
	RequestTimeoutSeconds      int    `koanf:"request_timeout_seconds"`     // Timeout for non-streaming requests (default: 60)
	StreamIdleTimeoutSeconds   int    `koanf:"stream_idle_timeout_seconds"` // Idle timeout during streaming responses (default: 120)
	MaxRetries                 int    `koanf:"max_retries"`                 // Retries on transient connect/network errors (default: 3); each retry uses bifrost's 500ms→5s exponential backoff
	MaxRetryBackoffSeconds     int    `koanf:"max_retry_backoff_seconds"`   // Cap for session-level transient-error retry backoff, 1s doubling up to this cap (default: 60)
	// AllowPrivateNetwork permits the provider SDK to connect to RFC1918
	// (10.x/172.16.x/192.168.x) and ULA endpoints. Off by default: enabling it
	// relaxes the SDK's SSRF egress guard for every provider, so base_url MUST
	// remain operator-controlled (config env/startup only) — never model- or
	// tool-settable (see the recorded precedent). Link-local/metadata
	// (169.254.0.0/16, fe80::) and unspecified addresses stay blocked.
	AllowPrivateNetwork bool `koanf:"allow_private_network"`
}

// HistoryConfig holds persistent history configuration
type HistoryConfig struct {
	Enabled      bool `koanf:"enabled"`
	MaxSessions  int  `koanf:"max_sessions"` // Used as max entries for history
	MaxAgeDays   int  `koanf:"max_age_days"`
	ListLimit    int  `koanf:"list_limit"`
	AutoSave     bool `koanf:"auto_save"`
	SaveInterval int  `koanf:"save_interval"`
}

// UIConfig holds UI-specific configuration
type UIConfig struct {
	MarkdownEnabled      bool          `koanf:"markdown_enabled"`
	CtrlCDebounceTime    time.Duration `koanf:"ctrl_c_debounce_time"`   // Quiet period for CTRL-C burst detection (handles iOS duplicate events)
	CtrlCWindowTime      time.Duration `koanf:"ctrl_c_window_time"`     // Window for second CTRL-C press to quit
	PromptExpandedHeight int           `koanf:"prompt_expanded_height"` // Height prompt grows to when multiline (default: 10)
	TickInterval         time.Duration `koanf:"tick_interval"`          // Chat render-tick debounce interval (default: 100ms)
	CommandTimerInterval time.Duration `koanf:"command_timer_interval"` // Refresh interval for the in-flight command stopwatch (default: 3s)
}

// SessionConfig holds session persistence configuration
type SessionConfig struct {
	Enabled      bool   `koanf:"enabled"`
	MaxSessions  int    `koanf:"max_sessions"`
	MaxAgeDays   int    `koanf:"max_age_days"`
	ListLimit    int    `koanf:"list_limit"`
	AutoSave     bool   `koanf:"auto_save"`
	SaveInterval int    `koanf:"save_interval"`
	AgentsFile   string `koanf:"agents_file"` // Project context file name (default: AGENTS.md, can be CLAUDE.md)
}

// Mount represents a volume mount for the sandbox container.
//
// ReadOnly is a first-class property: an additional_mounts entry is read-only
// unless its source sets read_only = false, and the Ruler's grant can pin it
// writable. cache_volumes reuse this type but ReadOnly has no meaning there
// (named volumes are the container's own storage, not host assets).
//
// The koanf tag is a plain bool so the config surface stays simple; the
// read-only default for additional_mounts is applied by the loader, which sees
// which entries explicitly set the key (edict 1036).
type Mount struct {
	Source      string `koanf:"source"`
	Destination string `koanf:"destination"`
	// ReadOnly mounts the host path with Options: ["ro"]. It defaults to true
	// for additional_mounts; set read_only = false in the config to request a
	// writable bind (which the Ruler must still grant at the trust dialog).
	ReadOnly bool `koanf:"read_only"`
}

// SandboxConfig holds configuration for the sandboxed execution environment
type SandboxConfig struct {
	// ImageName is the container image name (default: localhost/asimi/sandbox/<project>:latest)
	ImageName string `koanf:"image_name"`
	// TimeoutMinutes is the timeout for shell commands in minutes (default: 10)
	TimeoutMinutes int `koanf:"timeout_minutes"`
	// NoCleanup skips container removal on exit (for debugging)
	NoCleanup bool `koanf:"no_cleanup"`
	// AllowHostFallback falls back to host shell if container is unavailable
	AllowHostFallback bool `koanf:"allow_host_fallback"`
	// RunOnHost is a list of regex patterns for commands that should run on the host
	// instead of in the container. These commands require user approval before execution.
	RunOnHost []string `koanf:"run_on_host"`
	// ApprovalTimeout is how long to wait for a user approval decision on a
	// host command before failing the call (default: 120s)
	ApprovalTimeout time.Duration `koanf:"approval_timeout"`
	// SafeRunOnHost is a list of regex patterns for commands that can run on the host
	// without requiring user approval (e.g., read-only commands like `gh issue view`)
	SafeRunOnHost []string `koanf:"safe_run_on_host"`
	// AdditionalMounts are extra volume mounts for the container
	AdditionalMounts []Mount `koanf:"additional_mounts"`
	// PassthroughEnv is a list of host environment variable names to forward into the sandbox
	PassthroughEnv []string `koanf:"passthrough_env"`
	// PlatformOverlays are project-relative paths to isolate per-platform using named volumes
	PlatformOverlays []string `koanf:"platform_overlays"`
	// CacheVolumes are absolute in-container cache paths persisted in named
	// volumes keyed by project, so a recreated sandbox starts warm instead of
	// recompiling/re-downloading the world. The Mount overload is deliberate:
	// unlike AdditionalMounts (host path -> container path), Source here is a
	// volume-name pattern and Destination an absolute container path, and
	// Source is never stat()ed on the host. The runner expands "<slug>" in
	// Source to SanitizeSegment(repoInfo.Slug) at container-create time, so all
	// edicts of one project share a cache while different projects stay
	// separate — keyed by project, not by worktree path.
	CacheVolumes []Mount `koanf:"cache_volumes"`
	// IgnoreCopyGlobs is the allowlist of gitignore-style globs whose
	// gitignored files travel from the main tree into a new edict worktree.
	// A worktree is a fresh checkout, so local-only files are absent there;
	// this list carries the ones the worktree actually needs (local config)
	// without dragging in caches, built binaries, logs or jobs/ — which the
	// former unbounded `--ignored` copy did, multiplying secrets exposure and
	// duplicating gigabytes per edict. Empty means copy nothing.
	IgnoreCopyGlobs []string `koanf:"ignore_copy_globs"`
	// DiskMinFreeMiB is the free-space floor, in MiB, on the podman store. When
	// free space drops below it, reclaimable build artifacts (dangling images,
	// builder cache) are pruned before a container is created. 0 disables.
	DiskMinFreeMiB int `koanf:"disk_min_free_mib"`
}

// DefaultIgnoreCopyGlobs is the built-in safe allowlist for
// SandboxConfig.IgnoreCopyGlobs: common local-config names only. It must
// never include caches (.gocache), built binaries (asimi), logs (*.log),
// coverage, dist/, tmp/ or jobs/ (which .gitignore flags as possibly
// secret-bearing).
func DefaultIgnoreCopyGlobs() []string {
	return []string{".env", ".envrc", "*.local"}
}

// DefaultCacheVolumes is the built-in cache_volumes set: Go's real module
// cache path persisted in a project-keyed named volume. GOCACHE is
// deliberately absent — edict 946 already pins GOCACHE to
// <projectRoot>/.gocache, a bind-mounted directory keyed by project and
// shared across every worktree, so registering it here too would mount a
// named volume over the same path and fight that pin (recreating the
// per-worktree cache fracture ruling 3 retired). The "<slug>" placeholder is
// expanded by the runner at container-create time (see SandboxConfig).
func DefaultCacheVolumes() []Mount {
	return []Mount{
		{Source: "asimi-gomodcache-<slug>", Destination: "/go/pkg/mod"},
	}
}

// DefaultDiskMinFreeMiB is the default free-space floor (4 GiB) for the podman
// store. It is generous enough not to prune on a healthy machine but fires
// before the VM wedges; 0 disables the guard.
const DefaultDiskMinFreeMiB = 4096

// CourtConfig holds configuration for the Court.
type CourtConfig struct {
	StepIdleTimeout time.Duration `koanf:"step_idle_timeout"` // max silence before aborting a ritual step (default 5m)
	Project         string        `koanf:"project"`           // deprecated: ignored; slug is derived from the checkout (edict 1005)
	IsolatedHost    bool          `koanf:"isolated_host"`     // skip sandbox, run commands on host without approval
	OutputLimit     int           `koanf:"output_limit"`      // max chars of heavy output fields in summary mode (default 500)
	// RunnerFlatlineTimeout is how long a per-edict container may sit idle
	// before it is stopped and removed (default 15m). 0 disables the sweep.
	RunnerFlatlineTimeout time.Duration `koanf:"runner_flatline_timeout"`
	// MaxParallelLings gates and bounds automatic execution of ready lings per
	// edict. <= 0 disables automatic ignition (default 0); N > 0 runs up to N
	// ready lings concurrently in the edict's worktree.
	MaxParallelLings int `koanf:"max_parallel_lings"`
}

// DefaultOutputLimit is the fallback truncation length for heavy output
// fields when court.output_limit is unset in summary mode.
const DefaultOutputLimit = 500

// DefaultRunnerFlatlineTimeout is how long a per-edict container may sit idle
// before the Court stops and removes it.
const DefaultRunnerFlatlineTimeout = 15 * time.Minute

// DefaultCourtConfig returns the default configuration.
func DefaultCourtConfig() *CourtConfig {
	return &CourtConfig{
		StepIdleTimeout:       5 * time.Minute,
		OutputLimit:           DefaultOutputLimit,
		RunnerFlatlineTimeout: DefaultRunnerFlatlineTimeout,
	}
}

// Resolved returns a copy of c with defaults applied for unset positive-only
// fields and caller-verified project and isolatedHost authority stamped in.
// Every other [court] field flows through automatically.
func (c CourtConfig) Resolved(project string, isolatedHost bool) *CourtConfig {
	defaults := DefaultCourtConfig()
	out := c
	if out.StepIdleTimeout <= 0 {
		out.StepIdleTimeout = defaults.StepIdleTimeout
	}
	if out.OutputLimit <= 0 {
		out.OutputLimit = defaults.OutputLimit
	}
	out.Project = project
	out.IsolatedHost = isolatedHost
	return &out
}

// OSUsername returns the current OS user's username, the only identity the
// Court can verify. It falls back to "guest" when the lookup fails so callers
// always receive a non-empty scope, and is deliberately independent of any
// config-file value: a committed [court] username must never choose scope
// (edict 1006).
func OSUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "guest"
}

// DefaultSessionConfig returns the default session configuration.
func DefaultSessionConfig() *SessionConfig {
	return &SessionConfig{
		MaxSessions: 50,
		MaxAgeDays:  30,
	}
}

const (
	// DefaultMCPTimeoutMS is the default MCP server connection/startup timeout in milliseconds.
	DefaultMCPTimeoutMS = 10000
	// DefaultMCPToolTimeoutMS is the default per-tool MCP execution timeout in milliseconds.
	DefaultMCPToolTimeoutMS = 30000
	// DefaultMCPMaxOutputTokens is the default cap on MCP tool output tokens.
	DefaultMCPMaxOutputTokens = 10000
)

// MCPConfig holds configuration for external MCP servers (foreign envoys).
// Until upstream fixes GO-2026-6911 in github.com/mark3labs/mcp-go, only stdio
// transport is permitted; URL, HTTP, SSE, and Streamable-HTTP configs are
// rejected fail-closed.
type MCPConfig struct {
	Timeout         int               `koanf:"timeout"`
	ToolTimeout     int               `koanf:"tool_timeout"`
	MaxOutputTokens int               `koanf:"max_output_tokens"`
	Servers         []MCPServerConfig `koanf:"servers"`
}

// MCPServerConfig configures a single stdio MCP server.
// URL and Transport are present for fail-closed validation so non-stdio
// configurations are caught and rejected citing GO-2026-6911.
type MCPServerConfig struct {
	ID                 string            `koanf:"id"`
	Command            []string          `koanf:"command"`
	Env                []string          `koanf:"env"`
	DefaultPermissions string            `koanf:"default_permissions"`
	Tools              []MCPToolOverride `koanf:"tools"`
	URL                string            `koanf:"url"`
	Transport          string            `koanf:"transport"`
}

// MCPToolOverride sets Three Realms permissions for a specific tool on an MCP server.
type MCPToolOverride struct {
	Name        string `koanf:"name"`
	Permissions string `koanf:"permissions"`
}

// DefaultMCPConfig returns the default MCP configuration.
func DefaultMCPConfig() MCPConfig {
	return MCPConfig{
		Timeout:         DefaultMCPTimeoutMS,
		ToolTimeout:     DefaultMCPToolTimeoutMS,
		MaxOutputTokens: DefaultMCPMaxOutputTokens,
	}
}

// ValidateMCPConfig validates MCP configuration fail-closed:
//   - rejects negative timeouts or token limits
//   - rejects any URL or non-stdio Transport citing GO-2026-6911
//   - requires non-empty unique server IDs and non-empty stdio Command
//   - validates DefaultPermissions (when set) and every tool override's
//     Permissions via types.ParsePermissions (strict 9-char rwx, no spaces)
func ValidateMCPConfig(cfg MCPConfig) error {
	if cfg.Timeout < 0 {
		return fmt.Errorf("mcp.timeout must be non-negative, got %d", cfg.Timeout)
	}
	if cfg.ToolTimeout < 0 {
		return fmt.Errorf("mcp.tool_timeout must be non-negative, got %d", cfg.ToolTimeout)
	}
	if cfg.MaxOutputTokens < 0 {
		return fmt.Errorf("mcp.max_output_tokens must be non-negative, got %d", cfg.MaxOutputTokens)
	}

	seenIDs := make(map[string]bool, len(cfg.Servers))
	for i, srv := range cfg.Servers {
		label := strings.TrimSpace(srv.ID)
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}

		// Fail closed on any URL or non-stdio transport before checking Command,
		// so URL/HTTP/SSE/Streamable-HTTP configs always report GO-2026-6911.
		if srv.URL != "" {
			return fmt.Errorf("mcp.servers[%s]: url is not supported (only stdio transport is allowed due to GO-2026-6911)", label)
		}
		if srv.Transport != "" && !strings.EqualFold(strings.TrimSpace(srv.Transport), "stdio") {
			return fmt.Errorf("mcp.servers[%s]: transport %q is not supported (only \"stdio\" transport is allowed due to GO-2026-6911)", label, srv.Transport)
		}

		id := strings.TrimSpace(srv.ID)
		if id == "" {
			return fmt.Errorf("mcp.servers[%d]: id is required", i)
		}
		if seenIDs[id] {
			return fmt.Errorf("mcp.servers[%s]: duplicate server id %q", id, id)
		}
		seenIDs[id] = true

		if len(srv.Command) == 0 || strings.TrimSpace(srv.Command[0]) == "" {
			return fmt.Errorf("mcp.servers[%s]: stdio command is required", id)
		}

		if srv.DefaultPermissions != "" {
			if _, err := types.ParsePermissions(srv.DefaultPermissions); err != nil {
				return fmt.Errorf("mcp.servers[%s]: invalid default_permissions: %w", id, err)
			}
		}

		seenTools := make(map[string]bool, len(srv.Tools))
		for j, tool := range srv.Tools {
			toolName := strings.TrimSpace(tool.Name)
			if toolName == "" {
				return fmt.Errorf("mcp.servers[%s].tools[%d]: tool name is required", id, j)
			}
			if seenTools[toolName] {
				return fmt.Errorf("mcp.servers[%s].tools[%s]: duplicate tool override %q", id, toolName, toolName)
			}
			seenTools[toolName] = true
			if _, err := types.ParsePermissions(tool.Permissions); err != nil {
				return fmt.Errorf("mcp.servers[%s].tools[%s]: invalid permissions: %w", id, toolName, err)
			}
		}
	}

	return nil
}
