package config

import (
	"os/user"
	"time"
)

// Config represents the application configuration structure
type Config struct {
	Storage StorageConfig `koanf:"storage"`
	Logging LoggingConfig `koanf:"logging"`
	UI      UIConfig      `koanf:"ui"`
	LLM     LLMConfig     `koanf:"llm"`
	History HistoryConfig `koanf:"history"`
	Session SessionConfig `koanf:"session"`
	Sandbox SandboxConfig `koanf:"sandbox"`
	Court   CourtConfig   `koanf:"court"`
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
	// SafeRunOnHost is a list of regex patterns for commands that can run on the host
	// without requiring user approval (e.g., read-only commands like `gh issue view`)
	SafeRunOnHost []string `koanf:"safe_run_on_host"`
	// AdditionalMounts are extra volume mounts for the container
	AdditionalMounts []Mount `koanf:"additional_mounts"`
	// PassthroughEnv is a list of host environment variable names to forward into the sandbox
	PassthroughEnv []string `koanf:"passthrough_env"`
	// PlatformOverlays are project-relative paths to isolate per-platform using named volumes
	PlatformOverlays []string `koanf:"platform_overlays"`
}

// CourtConfig holds configuration for the Court.
type CourtConfig struct {
	RitualTimeout   time.Duration `koanf:"ritual_timeout"`    // legacy, unused
	StepIdleTimeout time.Duration `koanf:"step_idle_timeout"` // max silence before aborting a ritual step (default 5m)
	Username        string        `koanf:"username"`          // OS username for edict scoping
	Project         string        `koanf:"project"`           // project slug for edict scoping
	IsolatedHost    bool          `koanf:"isolated_host"`     // skip sandbox, run commands on host without approval
}

// DefaultCourtConfig returns the default configuration.
func DefaultCourtConfig() *CourtConfig {
	username := "guest"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	return &CourtConfig{
		StepIdleTimeout: 5 * time.Minute,
		Username:        username,
	}
}

// DefaultSessionConfig returns the default session configuration.
func DefaultSessionConfig() *SessionConfig {
	return &SessionConfig{
		MaxSessions: 50,
		MaxAgeDays:  30,
	}
}
