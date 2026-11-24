// Package config provides configuration loading and management for asimi.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	koanftoml "github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/file"
	koanf "github.com/knadh/koanf/v2"
)

// tomlDiagnostic is satisfied by TOML parse errors that carry
// a rich human-readable diagnostic (e.g. with line numbers).
// We use an interface assertion instead of importing the
// concrete type to avoid a second TOML parser dependency.
type tomlDiagnostic interface {
	String() string
}

//go:embed default.conf
var defaultConfContent string

// DefaultConfContent returns the embedded default configuration file content.
func DefaultConfContent() string {
	return defaultConfContent
}

// ConfigCreated tracks whether the config file was created on this run.
// TODO: find a better way and remove this global
var ConfigCreated bool

// applyMountReadOnlyDefaults sets ReadOnly=true on every additional_mounts
// entry that did not explicitly set read_only in the config. koanf exposes the
// raw array entries, so an entry that omits the key is distinguishable from one
// that sets read_only = false. The two layers (user, project) are merged by
// koanf, so this inspects the merged view.
//
// It fails CLOSED: every entry starts read-only and is only cleared when the
// merged view positively shows read_only = false for that entry. If the raw
// value ever comes back in a shape this function does not recognise, no entry
// is opened for writing — a writable bind must never be the accidental result
// of a parse surprise.
func applyMountReadOnlyDefaults(k *koanf.Koanf, cfg *Config) {
	entries, _ := k.Get("sandbox.additional_mounts").([]interface{})
	for i := range cfg.Sandbox.AdditionalMounts {
		cfg.Sandbox.AdditionalMounts[i].ReadOnly = true
		if i >= len(entries) {
			continue
		}
		entry, ok := entries[i].(map[string]interface{})
		if !ok {
			continue
		}
		if v, ok := entry["read_only"].(bool); ok {
			cfg.Sandbox.AdditionalMounts[i].ReadOnly = v
		}
	}
}

// forbiddenMCPServerKeys are raw config keys that configure URL, HTTP, SSE, or
// Streamable-HTTP MCP transports. All are rejected fail-closed due to
// GO-2026-6911 (unfixed SSRF/credential leak in mcp-go StreamableHTTP).
var forbiddenMCPServerKeys = map[string]bool{
	"url":               true,
	"endpoint":          true,
	"connection_string": true,
	"connection_type":   true,
	"base_url":          true,
	"server_url":        true,
	"sse_url":           true,
	"http_url":          true,
	"http":              true,
	"sse":               true,
	"streamable_http":   true,
	"streamable-http":   true,
}

// validateRawMCPConfig inspects the raw koanf mcp.servers entries before
// struct unmarshaling so forbidden URL/HTTP/SSE keys that do not map to struct
// fields (endpoint, connection_string, connection_type, etc.) fail closed with
// an error citing GO-2026-6911.
func validateRawMCPConfig(k *koanf.Koanf) error {
	raw := k.Get("mcp.servers")
	if raw == nil {
		return nil
	}

	var entries []map[string]interface{}
	switch v := raw.(type) {
	case []interface{}:
		entries = make([]map[string]interface{}, 0, len(v))
		for i, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				return fmt.Errorf("mcp.servers[%d]: invalid server entry", i)
			}
			entries = append(entries, m)
		}
	case []map[string]interface{}:
		entries = v
	default:
		return fmt.Errorf("mcp.servers: expected array of server tables, got %T", raw)
	}

	for i, entry := range entries {
		label := fmt.Sprintf("#%d", i)
		if id, ok := entry["id"].(string); ok && strings.TrimSpace(id) != "" {
			label = strings.TrimSpace(id)
		}
		keys := make([]string, 0, len(entry))
		for key := range entry {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			val := entry[key]
			lk := strings.ToLower(strings.TrimSpace(key))
			if forbiddenMCPServerKeys[lk] {
				if s, ok := val.(string); !ok || s != "" {
					return fmt.Errorf("mcp.servers[%s]: forbidden key %q (only stdio transport is allowed due to GO-2026-6911)", label, key)
				}
			}
			if lk == "transport" {
				s, ok := val.(string)
				if !ok {
					return fmt.Errorf("mcp.servers[%s]: invalid transport %v (only \"stdio\" transport is allowed due to GO-2026-6911)", label, val)
				}
				if s != "" && !strings.EqualFold(strings.TrimSpace(s), "stdio") {
					return fmt.Errorf("mcp.servers[%s]: transport %q is not supported (only \"stdio\" transport is allowed due to GO-2026-6911)", label, s)
				}
			}
		}
	}
	return nil
}

// DefaultConfig returns the configuration populated with sensible defaults.
func DefaultConfig() Config {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("Failed to get user home directory", "error", err)
	}
	// Honor ASIMI_HOME: when set (containerized drivers like Harbor persist
	// it to a writable location), default the court DB to ${ASIMI_HOME}/
	// asimi.sqlite so startup works even when $HOME is read-only (e.g. under
	// Harbor's --isolated-host container). Without ASIMI_HOME we keep the
	// traditional ~/.local/share/asimi/asimi.sqlite default.
	dbPath := filepath.Join(homeDir, ".local", "share", "asimi", "asimi.sqlite")
	if home := os.Getenv("ASIMI_HOME"); home != "" {
		dbPath = filepath.Join(home, "asimi.sqlite")
	}

	return Config{
		Storage: StorageConfig{
			DatabasePath: dbPath,
		},
		LLM: LLMConfig{
			MaxToolOutput:            51200, // Default: 50KB
			RequestTimeoutSeconds:    300,
			StreamIdleTimeoutSeconds: 600,
			MaxRetries:               3,
			MaxRetryBackoffSeconds:   60,
		},
		History: HistoryConfig{
			Enabled:      true,
			MaxSessions:  50,
			MaxAgeDays:   30,
			ListLimit:    0,
			AutoSave:     false,
			SaveInterval: 300,
		},
		UI: UIConfig{
			MarkdownEnabled:      true,
			CtrlCDebounceTime:    200 * time.Millisecond, // Debounce for duplicate CTRL-C events from terminals
			CtrlCWindowTime:      2000 * time.Millisecond,
			PromptExpandedHeight: 10,
			TickInterval:         100 * time.Millisecond, // Debounce between chat content updates when streaming
			CommandTimerInterval: 3 * time.Second,        // Stopwatch refresh while a shell command runs
		},
		Session: SessionConfig{
			Enabled:      true,
			MaxSessions:  50,
			MaxAgeDays:   30,
			ListLimit:    0,
			AutoSave:     true,
			SaveInterval: 300,
		},
		Sandbox: SandboxConfig{
			RunOnHost:       []string{},
			SafeRunOnHost:   []string{},
			ApprovalTimeout: 120 * time.Second,
			IgnoreCopyGlobs: DefaultIgnoreCopyGlobs(),
			CacheVolumes:    DefaultCacheVolumes(),
			DiskMinFreeMiB:  DefaultDiskMinFreeMiB,
		},
		Court: *DefaultCourtConfig(),
		MCP:   DefaultMCPConfig(),
	}
}

// UserConfigPath returns the path to the user config directory and file.
// Returns (cfgDir, cfgPath, error).
func UserConfigPath() (string, string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	cfgDir := filepath.Join(homeDir, ".config", "asimi")
	cfgPath := filepath.Join(cfgDir, "asimi.conf")
	return cfgDir, cfgPath, nil
}

// EnsureUserConfigExists checks if the user config file exists and creates it if not.
// Returns true if the config file was created (first run), false otherwise.
func EnsureUserConfigExists() (bool, error) {
	cfgDir, cfgPath, err := UserConfigPath()
	if err != nil {
		return false, err
	}

	// Check if config file already exists
	if _, err := os.Stat(cfgPath); err == nil {
		return false, nil // Config exists, not first run
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to check config file: %w", err)
	}

	// Config doesn't exist - create it
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return false, fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := os.WriteFile(cfgPath, []byte(defaultConfContent), 0o644); err != nil {
		return false, fmt.Errorf("failed to create config file: %w", err)
	}

	slog.Info("Created user config file", "path", cfgPath)
	return true, nil
}

// resolveAPIKeys fills in provider-specific API keys from well-known
// environment variables. It is called by LoadProjectConfig when
// resolveKeys is true (e.g. TUI boot); the daemon deliberately passes
// false because it receives keys via its APIKeys mechanism.
//
// Uses convention: strings.ToUpper(provider) + "_API_KEY".
// Special cases:
//   - googleai → GEMINI_API_KEY (also checks GOOGLE_API_KEY)
func resolveAPIKeys(cfg *Config) {
	if cfg.LLM.Provider != "" && cfg.LLM.APIKey == "" {
		// Convention: PROVIDER_API_KEY
		envVar := strings.ToUpper(cfg.LLM.Provider) + "_API_KEY"
		if key := os.Getenv(envVar); key != "" {
			cfg.LLM.APIKey = key
		}
	}
}

// LoadProjectConfig loads configuration for a specific project root without
// relying on the current working directory or environment-variable credentials.
//
// Layer order (later wins):
//  1. Built-in defaults (DefaultConfig)
//  2. User-level config: ~/.config/asimi/asimi.conf
//  3. Project-level config: {projectRoot}/.agents/asimi.conf (skipped if projectRoot is empty)
//
// When resolveKeys is true, resolveAPIKeys is called after unmarshaling so
// that provider-specific keys are populated from well-known environment
// variables.  The daemon typically passes false because it receives API
// keys via its APIKeys mechanism.
func LoadProjectConfig(projectRoot string, resolveKeys bool) (*Config, error) {
	k := koanf.New(".")
	userK := koanf.New(".")
	projK := koanf.New(".")

	// 1. User-level config
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home directory: %w", err)
	}
	userConfigPath := filepath.Join(homeDir, ".config", "asimi", "asimi.conf")
	if _, statErr := os.Stat(userConfigPath); statErr == nil {
		if err := userK.Load(file.Provider(userConfigPath), koanftoml.Parser()); err != nil {
			var diag tomlDiagnostic
			if errors.As(err, &diag) {
				return nil, fmt.Errorf("failed to parse %s:\n%s", userConfigPath, diag.String())
			}
			return nil, fmt.Errorf("failed to load config from %s: %w", userConfigPath, err)
		}
		if err := validateRawMCPConfig(userK); err != nil {
			return nil, err
		}
		if err := k.Merge(userK); err != nil {
			return nil, fmt.Errorf("failed to merge user config: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		slog.Warn("Unable to stat user config", "path", userConfigPath, "error", statErr)
	}

	// 2. Project-level config (skip if projectRoot is empty)
	if projectRoot != "" {
		projectConfigPath := filepath.Join(projectRoot, ".agents", "asimi.conf")
		if _, statErr := os.Stat(projectConfigPath); statErr == nil {
			if err := projK.Load(file.Provider(projectConfigPath), koanftoml.Parser()); err != nil {
				var diag tomlDiagnostic
				if errors.As(err, &diag) {
					return nil, fmt.Errorf("failed to parse %s:\n%s", projectConfigPath, diag.String())
				}
				return nil, fmt.Errorf("failed to load config from %s: %w", projectConfigPath, err)
			}
			if err := validateRawMCPConfig(projK); err != nil {
				return nil, err
			}
			if err := k.Merge(projK); err != nil {
				return nil, fmt.Errorf("failed to merge project config: %w", err)
			}
		} else if !os.IsNotExist(statErr) {
			slog.Warn("Unable to stat project config", "path", projectConfigPath, "error", statErr)
		}
	}

	// Unmarshal onto defaults so every field has a value.
	config := DefaultConfig()
	if err := k.Unmarshal("", &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// User vs. project MCP pre-classification isolation (edict 661):
	// Only user config (~/.config/asimi/asimi.conf) may pre-classify MCP tools
	// via default_permissions or [[mcp.servers.tools]] overrides without going
	// through Rites + propose. Any pre-classification defined in project config
	// (.agents/asimi.conf) is stripped.
	if userK.Exists("mcp.servers") || projK.Exists("mcp.servers") {
		var userMCP, projMCP MCPConfig
		if userK.Exists("mcp.servers") {
			if err := userK.Unmarshal("mcp", &userMCP); err != nil {
				return nil, fmt.Errorf("failed to unmarshal user mcp config: %w", err)
			}
		}
		if projK.Exists("mcp.servers") {
			if err := projK.Unmarshal("mcp", &projMCP); err != nil {
				return nil, fmt.Errorf("failed to unmarshal project mcp config: %w", err)
			}
			// Validate syntax (including strict 9-char permissions) before
			// stripping so typos still surface a clear config error.
			if err := ValidateMCPConfig(projMCP); err != nil {
				return nil, err
			}
			for i := range projMCP.Servers {
				if projMCP.Servers[i].DefaultPermissions != "" || len(projMCP.Servers[i].Tools) > 0 {
					slog.Warn("ignoring project-level MCP pre-classifications (only user config may pre-classify)",
						"server_id", projMCP.Servers[i].ID)
					projMCP.Servers[i].DefaultPermissions = ""
					projMCP.Servers[i].Tools = nil
				}
			}
		}
		merged := make([]MCPServerConfig, 0, len(userMCP.Servers)+len(projMCP.Servers))
		userIndexByID := make(map[string]int, len(userMCP.Servers))
		for _, srv := range userMCP.Servers {
			id := strings.TrimSpace(srv.ID)
			if id != "" {
				if _, already := userIndexByID[id]; !already {
					userIndexByID[id] = len(merged)
				}
			}
			merged = append(merged, srv)
		}
		for _, srv := range projMCP.Servers {
			id := strings.TrimSpace(srv.ID)
			if idx, exists := userIndexByID[id]; id != "" && exists {
				merged[idx] = srv
				delete(userIndexByID, id)
			} else {
				merged = append(merged, srv)
			}
		}
		config.MCP.Servers = merged
	}

	// Environment-variable overrides for model selection. These take
	// precedence over config-file values and exist for harness-driven runs
	// (terminal-bench) that must name the model per-run. Applied before
	// resolveAPIKeys so the API-key lookup uses the overridden provider.
	if v := os.Getenv("ASIMI_MODEL"); v != "" {
		config.LLM.Model = v
	}
	if v := os.Getenv("ASIMI_PROVIDER"); v != "" {
		config.LLM.Provider = v
	}
	if v := os.Getenv("ASIMI_REASONING_EFFORT"); v != "" {
		config.LLM.ReasoningEffort = v
	}

	// Validate reasoning_effort accepts empty (provider default), low,
	// medium, or high. Anything else is rejected to catch typos early,
	// before the value silently reaches the daemon.
	if err := ValidateReasoningEffort(config.LLM.ReasoningEffort); err != nil {
		return nil, err
	}

	// Validate MCP configuration fail-closed (stdio-only policy, GO-2026-6911;
	// unique IDs, non-empty commands, strict 9-char permission strings).
	if err := ValidateMCPConfig(config.MCP); err != nil {
		return nil, err
	}

	// Set default for session.enabled if not explicitly configured
	if !k.Exists("session.enabled") {
		config.Session.Enabled = true // Default to enabled
	}

	// Runner flatline timeout defaults to 15m when unset. An explicit 0 in the
	// config file disables the sweep, so the default must be applied only when
	// the key is absent (DefaultConfig's Court is the zero value).
	if !k.Exists("court.runner_flatline_timeout") {
		config.Court.RunnerFlatlineTimeout = DefaultRunnerFlatlineTimeout
	}

	// Backward-compat: old config files may have a [shogunate] section
	// instead of [court]. If [court] wasn't explicitly set, copy values
	// from [shogunate] so existing user configs keep working.
	if !k.Exists("court.project") && k.Exists("shogunate.project") {
		config.Court.Project = k.String("shogunate.project")
	}
	// Step idle timeout: aborts a ritual step after N seconds of silence.
	if !k.Exists("court.step_idle_timeout") && k.Exists("shogunate.step_idle_timeout") {
		config.Court.StepIdleTimeout = k.Duration("shogunate.step_idle_timeout")
	}
	// Runner flatline timeout: stops and removes idle per-edict containers
	// after N of inactivity (0 disables the sweep).
	if !k.Exists("court.runner_flatline_timeout") && k.Exists("shogunate.runner_flatline_timeout") {
		config.Court.RunnerFlatlineTimeout = k.Duration("shogunate.runner_flatline_timeout")
	}

	// Expand a leading "~" in additional mount sources so consumers only
	// ever see absolute host paths. Destination is a container path and is
	// deliberately left untouched.
	for i := range config.Sandbox.AdditionalMounts {
		source, err := ExpandHome(config.Sandbox.AdditionalMounts[i].Source)
		if err != nil {
			return nil, fmt.Errorf("failed to expand home directory in additional_mounts source %q: %w", config.Sandbox.AdditionalMounts[i].Source, err)
		}
		config.Sandbox.AdditionalMounts[i].Source = source
	}

	// Drop any additional_mounts entry whose source or destination is blank.
	// An empty table-array header leaves a zero-value entry (both fields are
	// ""), and the "empty header = active" rule means blanks reach the runner.
	// In the trust dialog a blank source renders a row with no label — the
	// Ruler is invited to tick a row that names nothing — and at the runner a
	// blank source has no host meaning at all (os.Stat("") always fails, so the
	// mount is silently skipped), so dropping it grants nothing. A blank
	// destination with a real source is unbuildable, so it goes too (edict
	// 1036).
	kept := make([]Mount, 0, len(config.Sandbox.AdditionalMounts))
	for _, m := range config.Sandbox.AdditionalMounts {
		if strings.TrimSpace(m.Source) == "" || strings.TrimSpace(m.Destination) == "" {
			slog.Warn("dropping additional mount with a blank source or destination",
				"source", m.Source, "destination", m.Destination)
			continue
		}
		kept = append(kept, m)
	}
	config.Sandbox.AdditionalMounts = kept

	// Read-only is the safe default for additional_mounts: a host path is
	// mounted with Options:["ro"] unless it explicitly asks for read_only =
	// false. The committed config may *request* a writable bind, but the Ruler
	// still has to grant it at the trust dialog (edict 1036). A bool cannot
	// distinguish "absent" from "false", so consult koanf for which entries
	// set the key.
	applyMountReadOnlyDefaults(k, &config)

	// Resolve API keys from environment variables when requested
	if resolveKeys {
		resolveAPIKeys(&config)
	}

	return &config, nil
}

// ValidateReasoningEffort validates a reasoning-effort value, accepting
// empty (provider default), "low", "medium", or "high". It is used by the
// config loader and by the CLI/handshake merge points so an invalid value
// can never reach the provider regardless of the source (config, env var,
// CLI flag, or SetContextParams handshake).
func ValidateReasoningEffort(effort string) error {
	switch effort {
	case "", "low", "medium", "high":
		return nil
	}
	return fmt.Errorf("invalid reasoning_effort %q: must be one of \"low\", \"medium\", \"high\" (or empty for provider default)", effort)
}

// ValidateConfigFile parses a config file and returns a rich diagnostic
// on TOML syntax errors. Returns nil if the file doesn't exist (missing
// config is OK) or if it parses successfully. This is intended as a
// pre-flight check before FX DI starts, so the user sees a clean error
// instead of FX stack noise.
func ValidateConfigFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read %s: %w", path, err)
	}
	_, err = koanftoml.Parser().Unmarshal(data)
	if err != nil {
		var diag tomlDiagnostic
		if errors.As(err, &diag) {
			return fmt.Errorf("failed to parse %s:\n%s", path, diag.String())
		}
		return fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return nil
}

// SaveConfig saves the current config to the user-level config file (~/.config/asimi/asimi.conf).
// It preserves all comments in the existing file.
func SaveConfig(cfg *Config) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	userConfigPath := filepath.Join(homeDir, ".config", "asimi", "asimi.conf")
	// Read existing content or start with empty
	var content string
	if data, err := os.ReadFile(userConfigPath); err == nil {
		content = string(data)
	}
	// Update provider and model using comment-preserving helpers
	content = UpdateOrInsertTOMLValue(content, "llm", "provider", cfg.LLM.Provider)
	content = UpdateOrInsertTOMLValue(content, "llm", "model", cfg.LLM.Model)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(userConfigPath), 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := os.WriteFile(userConfigPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// SetProjectConfig updates keys in the project config file under the given
// projectRoot directory. It accepts a section name followed by key-value pairs,
// similar to slog.Info().
// Example: SetProjectConfig("/path/to/project", "session", "agents_file", "CLAUDE.md")
// It preserves all comments in the existing file.
func SetProjectConfig(projectRoot, section string, keyValues ...string) error {
	if len(keyValues)%2 != 0 {
		return fmt.Errorf("SetProjectConfig requires an even number of key-value arguments")
	}

	projectConfigPath := filepath.Join(projectRoot, ".agents", "asimi.conf")
	agentsDir := filepath.Join(projectRoot, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create .agents directory: %w", err)
	}

	// Read existing content or start with empty
	var content string
	if data, err := os.ReadFile(projectConfigPath); err == nil {
		content = string(data)
	}

	// Update each key-value pair
	for i := 0; i < len(keyValues); i += 2 {
		key := keyValues[i]
		value := keyValues[i+1]
		content = UpdateOrInsertTOMLValue(content, section, key, value)
	}

	if err := os.WriteFile(projectConfigPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// GetEnv returns the environment variable value or fallback if not set.
func GetEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// EnsureProjectConfig seeds .agents/asimi.conf from the embedded default
// template if the file does not already exist. It creates .agents/ as needed.
// This lets callers modify keys in place (via config.SetProjectConfig) without
// the first write producing a stub file that overrides the full default set.
func EnsureProjectConfig(projectRoot string) error {
	path := filepath.Join(projectRoot, ".agents", "asimi.conf")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	agentsDir := filepath.Join(projectRoot, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create .agents directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(defaultConfContent), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
