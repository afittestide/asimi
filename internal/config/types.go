package config

import (
	"os/user"
	"time"
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

// Mount represents a volume mount for the sandbox container
type Mount struct {
	Source      string `koanf:"source"`
	Destination string `koanf:"destination"`
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

// CourtConfig holds configuration for the Court.
type CourtConfig struct {
	RitualTimeout   time.Duration `koanf:"ritual_timeout"`    // legacy, unused
	StepIdleTimeout time.Duration `koanf:"step_idle_timeout"` // max silence before aborting a ritual step (default 5m)
	Username        string        `koanf:"username"`          // OS username for edict scoping
	Project         string        `koanf:"project"`           // project slug for edict scoping
	IsolatedHost    bool          `koanf:"isolated_host"`     // skip sandbox, run commands on host without approval
	OutputLimit     int           `koanf:"output_limit"`      // max chars of heavy output fields in summary mode (default 500)
	// RunnerFlatlineTimeout is how long a per-edict container may sit idle
	// before it is stopped and removed (default 15m). 0 disables the sweep.
	RunnerFlatlineTimeout time.Duration `koanf:"runner_flatline_timeout"`
	// LingIgnitionEnabled gates automatic execution of lings. It defaults to
	// false (zero value). Ling ignition itself is safe — lings run in the
	// edict's worktree (e923) — but flipping the default is a separate ruling.
	LingIgnitionEnabled bool `koanf:"ling_ignition_enabled"`
	// LingParallelism bounds how many lings of one edict execute concurrently
	// during ignition. Lings share the edict's worktree and are only disjoint
	// if the Ministry of War planned them so, hence a bounded value rather than
	// unbounded fan-out. 0 falls back to DefaultLingParallelism.
	LingParallelism int `koanf:"ling_parallelism"`
}

// DefaultLingParallelism is the fallback concurrent-ling bound when
// court.ling_parallelism is unset (or non-positive).
const DefaultLingParallelism = 5

// DefaultOutputLimit is the fallback truncation length for heavy output
// fields when court.output_limit is unset in summary mode.
const DefaultOutputLimit = 500

// DefaultRunnerFlatlineTimeout is how long a per-edict container may sit idle
// before the Court stops and removes it.
const DefaultRunnerFlatlineTimeout = 15 * time.Minute

// DefaultCourtConfig returns the default configuration.
func DefaultCourtConfig() *CourtConfig {
	username := "guest"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	return &CourtConfig{
		StepIdleTimeout:       5 * time.Minute,
		Username:              username,
		OutputLimit:           DefaultOutputLimit,
		RunnerFlatlineTimeout: DefaultRunnerFlatlineTimeout,
	}
}

// DefaultSessionConfig returns the default session configuration.
func DefaultSessionConfig() *SessionConfig {
	return &SessionConfig{
		MaxSessions: 50,
		MaxAgeDays:  30,
	}
}
