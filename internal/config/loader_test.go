package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipIfNotCI(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" && os.Getenv("ASIMI_TEST_CI") == "" {
		t.Skip("Skipping test that modifies HOME (set CI=1 or ASIMI_TEST_CI=1 to run)")
	}
}

func TestEscapeTOMLString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no escaping needed",
			input:    "simple string",
			expected: "simple string",
		},
		{
			name:     "escape quotes",
			input:    `string with "quotes"`,
			expected: `string with \"quotes\"`,
		},
		{
			name:     "escape backslashes",
			input:    `path\to\file`,
			expected: `path\\to\\file`,
		},
		{
			name:     "escape both quotes and backslashes",
			input:    `path\to\"file"`,
			expected: `path\\to\\\"file\"`,
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EscapeTOMLString(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetEnv(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		fallback string
		envValue string
		setEnv   bool
		expected string
	}{
		{
			name:     "environment variable exists",
			key:      "TEST_VAR_EXISTS",
			fallback: "fallback",
			envValue: "actual_value",
			setEnv:   true,
			expected: "actual_value",
		},
		{
			name:     "environment variable does not exist",
			key:      "TEST_VAR_NOT_EXISTS",
			fallback: "fallback_value",
			envValue: "",
			setEnv:   false,
			expected: "fallback_value",
		},
		{
			name:     "empty environment variable",
			key:      "TEST_VAR_EMPTY",
			fallback: "fallback",
			envValue: "",
			setEnv:   true,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up before and after
			os.Unsetenv(tt.key)
			defer os.Unsetenv(tt.key)

			if tt.setEnv {
				os.Setenv(tt.key, tt.envValue)
			}

			result := GetEnv(tt.key, tt.fallback)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSaveConfig(t *testing.T) {
	skipIfNotCI(t)
	tempDir := t.TempDir()

	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(originalDir)

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	t.Run("save config falls back to user config", func(t *testing.T) {
		originalHome := os.Getenv("HOME")
		os.Setenv("HOME", tempDir)
		defer os.Setenv("HOME", originalHome)

		cfg := &Config{
			LLM: LLMConfig{
				Model: "gpt-4",
			},
		}

		err := SaveConfig(cfg)
		require.NoError(t, err)

		_, err = os.Stat(".config/asimi")
		assert.NoError(t, err)

		_, err = os.Stat(".config/asimi/asimi.conf")
		assert.NoError(t, err)

		_, err = os.Stat(".agents")
		assert.Error(t, err)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("save config updates existing file", func(t *testing.T) {
		originalHome := os.Getenv("HOME")
		os.Setenv("HOME", tempDir)
		defer os.Setenv("HOME", originalHome)

		configDir := filepath.Join(tempDir, ".config", "asimi")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		initialContent := `[llm]
provider = "openai"
model = "gpt-3.5-turbo"
`
		err = os.WriteFile(filepath.Join(configDir, "asimi.conf"), []byte(initialContent), 0644)
		require.NoError(t, err)

		cfg := &Config{
			LLM: LLMConfig{
				Provider: "openai",
				Model:    "gpt-4",
			},
		}

		err = SaveConfig(cfg)
		require.NoError(t, err)

		loadedConfig, err := LoadProjectConfig("", true)
		require.NoError(t, err)
		assert.Equal(t, "gpt-4", loadedConfig.LLM.Model)
	})

	t.Run("save config preserves other settings", func(t *testing.T) {
		originalHome := os.Getenv("HOME")
		os.Setenv("HOME", tempDir)
		defer os.Setenv("HOME", originalHome)

		envVars := []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"}
		originalEnvs := make(map[string]string)
		for _, env := range envVars {
			originalEnvs[env] = os.Getenv(env)
			os.Unsetenv(env)
		}
		defer func() {
			for env, val := range originalEnvs {
				if val != "" {
					os.Setenv(env, val)
				}
			}
		}()

		configDir := filepath.Join(tempDir, ".config", "asimi")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		initialContent := `[llm]
provider = "openai"
model = "gpt-3.5-turbo"
api_key = "test-key"

[history]
enabled = true
max_sessions = 50
`
		err = os.WriteFile(filepath.Join(configDir, "asimi.conf"), []byte(initialContent), 0644)
		require.NoError(t, err)

		cfg := &Config{
			LLM: LLMConfig{
				Provider: "openai",
				Model:    "gpt-4",
			},
		}

		err = SaveConfig(cfg)
		require.NoError(t, err)

		loadedConfig, err := LoadProjectConfig("", true)
		require.NoError(t, err)
		assert.Equal(t, "gpt-4", loadedConfig.LLM.Model)
		assert.Equal(t, "openai", loadedConfig.LLM.Provider)
		assert.True(t, loadedConfig.History.Enabled)
		assert.Equal(t, 50, loadedConfig.History.MaxSessions)
	})
}

func TestSetProjectConfig(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("creates directory and file if not exists", func(t *testing.T) {
		err := SetProjectConfig(tempDir, "session", "agents_file", "CLAUDE.md")
		require.NoError(t, err)

		agentsDir := filepath.Join(tempDir, ".agents")
		_, err = os.Stat(agentsDir)
		assert.NoError(t, err)

		content, err := os.ReadFile(filepath.Join(tempDir, ".agents", "asimi.conf"))
		require.NoError(t, err)
		assert.Contains(t, string(content), `agents_file = "CLAUDE.md"`)
	})

	t.Run("updates existing file preserving other settings", func(t *testing.T) {
		agentsDir := filepath.Join(tempDir, "preserve_test", ".agents")
		err := os.MkdirAll(agentsDir, 0o755)
		require.NoError(t, err)

		initialContent := `[llm]
provider = "openai"
model = "gpt-4"

[session]
enabled = true
`
		confPath := filepath.Join(agentsDir, "asimi.conf")
		err = os.WriteFile(confPath, []byte(initialContent), 0o644)
		require.NoError(t, err)

		projectRoot := filepath.Join(tempDir, "preserve_test")
		err = SetProjectConfig(projectRoot, "session", "agents_file", "CLAUDE.md")
		require.NoError(t, err)

		content, err := os.ReadFile(confPath)
		require.NoError(t, err)
		contentStr := string(content)

		assert.Contains(t, contentStr, `agents_file = "CLAUDE.md"`)
		assert.Contains(t, contentStr, `provider = "openai"`)
		assert.Contains(t, contentStr, `model = "gpt-4"`)
		assert.Contains(t, contentStr, `enabled = true`)
	})

	t.Run("errors on odd number of key-value args", func(t *testing.T) {
		err := SetProjectConfig(tempDir, "session", "agents_file")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "even number")
	})
}

func TestEnsureUserConfigExists(t *testing.T) {
	t.Run("creates config file on first run", func(t *testing.T) {
		tempHome := t.TempDir()
		originalHome := os.Getenv("HOME")
		os.Setenv("HOME", tempHome)
		defer os.Setenv("HOME", originalHome)

		configPath := filepath.Join(tempHome, ".config", "asimi", "asimi.conf")
		_, err := os.Stat(configPath)
		require.True(t, os.IsNotExist(err), "Config should not exist before test")

		created, err := EnsureUserConfigExists()
		require.NoError(t, err)
		assert.True(t, created, "Should return true when config is created")

		_, err = os.Stat(configPath)
		require.NoError(t, err, "Config file should exist after EnsureUserConfigExists")

		content, err := os.ReadFile(configPath)
		require.NoError(t, err)
		assert.Contains(t, string(content), "# Asimi Default Configuration File")
		assert.Contains(t, string(content), "[llm]")
	})

	t.Run("returns false when config already exists", func(t *testing.T) {
		tempHome := t.TempDir()
		originalHome := os.Getenv("HOME")
		os.Setenv("HOME", tempHome)
		defer os.Setenv("HOME", originalHome)

		configDir := filepath.Join(tempHome, ".config", "asimi")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		configPath := filepath.Join(configDir, "asimi.conf")
		existingContent := "[llm]\nprovider = \"anthropic\"\n"
		err = os.WriteFile(configPath, []byte(existingContent), 0644)
		require.NoError(t, err)

		created, err := EnsureUserConfigExists()
		require.NoError(t, err)
		assert.False(t, created, "Should return false when config already exists")

		content, err := os.ReadFile(configPath)
		require.NoError(t, err)
		assert.Equal(t, existingContent, string(content), "Existing config should not be modified")
	})

	t.Run("creates directory if it doesn't exist", func(t *testing.T) {
		tempHome := t.TempDir()
		originalHome := os.Getenv("HOME")
		os.Setenv("HOME", tempHome)
		defer os.Setenv("HOME", originalHome)

		configDir := filepath.Join(tempHome, ".config", "asimi")
		_, err := os.Stat(configDir)
		require.True(t, os.IsNotExist(err), "Config directory should not exist before test")

		created, err := EnsureUserConfigExists()
		require.NoError(t, err)
		assert.True(t, created)

		_, err = os.Stat(configDir)
		require.NoError(t, err, "Config directory should exist after EnsureUserConfigExists")
	})
}

// =============================================================================
// LoadProjectConfig Tests
// =============================================================================

func TestLoadProjectConfig_DefaultsOnly(t *testing.T) {
	// When no user or project config exists, defaults should be returned.
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	projectDir := t.TempDir()

	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Should have the built-in defaults
	assert.True(t, cfg.Session.Enabled)
	assert.Equal(t, 300, cfg.LLM.RequestTimeoutSeconds)
	assert.Equal(t, 600, cfg.LLM.StreamIdleTimeoutSeconds)
	assert.Equal(t, 3, cfg.LLM.MaxRetries)
	assert.Equal(t, 100*time.Millisecond, cfg.UI.TickInterval)
}

// TestLoadProjectConfig_EnvOverridesModel verifies that ASIMI_MODEL and
// ASIMI_PROVIDER take precedence over config-file [llm] values.
func TestLoadProjectConfig_EnvOverridesModel(t *testing.T) {
	skipIfNotCI(t)
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// User config sets provider/model; env overrides should win.
	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
provider = "anthropic"
model = "claude-sonnet-4-20250514"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	origModel := os.Getenv("ASIMI_MODEL")
	origProvider := os.Getenv("ASIMI_PROVIDER")
	defer os.Setenv("ASIMI_MODEL", origModel)
	defer os.Setenv("ASIMI_PROVIDER", origProvider)
	os.Setenv("ASIMI_MODEL", "gpt-4o")
	os.Setenv("ASIMI_PROVIDER", "openai")

	projectDir := t.TempDir()
	cfg, err := LoadProjectConfig(projectDir, true)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "gpt-4o", cfg.LLM.Model, "ASIMI_MODEL should override config file")
	assert.Equal(t, "openai", cfg.LLM.Provider, "ASIMI_PROVIDER should override config file")
	// The overridden provider drives API-key resolution.
	assert.Equal(t, os.Getenv("OPENAI_API_KEY"), cfg.LLM.APIKey)
}

// TestLoadProjectConfig_EnvEmptyLeavesConfig verifies that an unset/empty
// ASIMI_MODEL or ASIMI_PROVIDER does not clobber config-file values.
func TestLoadProjectConfig_EnvEmptyLeavesConfig(t *testing.T) {
	skipIfNotCI(t)
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
provider = "anthropic"
model = "claude-sonnet-4-20250514"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	origModel := os.Getenv("ASIMI_MODEL")
	origProvider := os.Getenv("ASIMI_PROVIDER")
	defer os.Setenv("ASIMI_MODEL", origModel)
	defer os.Setenv("ASIMI_PROVIDER", origProvider)
	os.Unsetenv("ASIMI_MODEL")
	os.Unsetenv("ASIMI_PROVIDER")

	projectDir := t.TempDir()
	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "anthropic", cfg.LLM.Provider)
	assert.Equal(t, "claude-sonnet-4-20250514", cfg.LLM.Model)
}

// TestLoadProjectConfig_ReasoningEffort verifies that reasoning_effort is
// parsed from config and defaults to empty (provider default) when unset.
func TestLoadProjectConfig_ReasoningEffort(t *testing.T) {
	skipIfNotCI(t)
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
reasoning_effort = "low"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	origEffort := os.Getenv("ASIMI_REASONING_EFFORT")
	defer os.Setenv("ASIMI_REASONING_EFFORT", origEffort)
	os.Unsetenv("ASIMI_REASONING_EFFORT")

	projectDir := t.TempDir()
	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "low", cfg.LLM.ReasoningEffort, "config file reasoning_effort should be parsed")

	// Unset config should default to empty (provider default).
	os.Remove(filepath.Join(userConfigDir, "asimi.conf"))
	cfg, err = LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "", cfg.LLM.ReasoningEffort, "unset reasoning_effort should default to empty")
}

// TestLoadProjectConfig_EnvOverridesReasoningEffort verifies that
// ASIMI_REASONING_EFFORT takes precedence over the config file value.
func TestLoadProjectConfig_EnvOverridesReasoningEffort(t *testing.T) {
	skipIfNotCI(t)
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
reasoning_effort = "low"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	origEffort := os.Getenv("ASIMI_REASONING_EFFORT")
	defer os.Setenv("ASIMI_REASONING_EFFORT", origEffort)
	os.Setenv("ASIMI_REASONING_EFFORT", "high")

	projectDir := t.TempDir()
	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "high", cfg.LLM.ReasoningEffort, "ASIMI_REASONING_EFFORT should override config file")
}

// TestLoadProjectConfig_InvalidReasoningEffort verifies that a
// reasoning_effort outside empty|low|medium|high is rejected.
func TestLoadProjectConfig_InvalidReasoningEffort(t *testing.T) {
	skipIfNotCI(t)
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
reasoning_effort = "turbo"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	projectDir := t.TempDir()
	_, err := LoadProjectConfig(projectDir, false)
	require.Error(t, err, "invalid reasoning_effort should be rejected")
	assert.Contains(t, err.Error(), "reasoning_effort")
}

// TestValidateReasoningEffort verifies the shared validator accepts the valid
// values (including empty for provider default) and rejects everything else.
// It is used by the config loader and the CLI/handshake merge points.
func TestValidateReasoningEffort(t *testing.T) {
	// Valid values
	for _, v := range []string{"", "low", "medium", "high"} {
		require.NoError(t, ValidateReasoningEffort(v), "value %q should be valid", v)
	}
	// Invalid values
	for _, v := range []string{"turbo", "Low", "HIGH", "medium ", "auto", "  "} {
		require.Error(t, ValidateReasoningEffort(v), "value %q should be rejected", v)
	}
}

func TestLoadProjectConfig_ProjectOverridesDefaults(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	projectDir := t.TempDir()
	agentsDir := filepath.Join(projectDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	projectConfig := `[llm]
provider = "openai"
model = "gpt-4o"

[session]
agents_file = "CLAUDE.md"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "asimi.conf"), []byte(projectConfig), 0o644))

	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "openai", cfg.LLM.Provider)
	assert.Equal(t, "gpt-4o", cfg.LLM.Model)
	assert.Equal(t, "CLAUDE.md", cfg.Session.AgentsFile)
	// Defaults should still be present for unoverridden fields
	assert.True(t, cfg.Session.Enabled)
}

// TestLoadProjectConfig_TickInterval verifies that [ui] tick_interval
// can be overridden from the project config and defaults otherwise.
func TestLoadProjectConfig_TickInterval(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	projectDir := t.TempDir()
	agentsDir := filepath.Join(projectDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	projectConfig := `[ui]
tick_interval = "250ms"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "asimi.conf"), []byte(projectConfig), 0o644))

	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 250*time.Millisecond, cfg.UI.TickInterval)
}

func TestLoadProjectConfig_UserConfigPlusProjectOverride(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// User-level config
	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
provider = "anthropic"
model = "claude-sonnet-4-20250514"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	// Project-level config overrides model only
	projectDir := t.TempDir()
	agentsDir := filepath.Join(projectDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	projectConfig := `[llm]
model = "claude-opus-4"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "asimi.conf"), []byte(projectConfig), 0o644))

	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// User config sets provider, project config overrides model
	assert.Equal(t, "anthropic", cfg.LLM.Provider)
	assert.Equal(t, "claude-opus-4", cfg.LLM.Model)
}

func TestLoadProjectConfig_NoEnvVarResolution(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// Set env var that resolveAPIKeys would pick up
	os.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	projectDir := t.TempDir()

	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// LoadProjectConfig should NOT resolve API keys from env vars
	assert.Empty(t, cfg.LLM.APIKey)
	assert.Empty(t, cfg.LLM.Provider)
}

func TestLoadProjectConfig_SandboxFromProjectConfig(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	projectDir := t.TempDir()
	agentsDir := filepath.Join(projectDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	projectConfig := `[sandbox]
run_on_host = ["^kubectl\\s"]
safe_run_on_host = ["^kubectl\\s+get"]
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "asimi.conf"), []byte(projectConfig), 0o644))

	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, []string{`^kubectl\s`}, cfg.Sandbox.RunOnHost)
	assert.Equal(t, []string{`^kubectl\s+get`}, cfg.Sandbox.SafeRunOnHost)
}

func TestLoadProjectConfig_ResolveKeysTrue(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// User config sets provider but no API key
	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
provider = "anthropic"
model = "claude-sonnet-4-20250514"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	// Set the env var that resolveAPIKeys picks up
	os.Setenv("ANTHROPIC_API_KEY", "sk-ant-resolved")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	projectDir := t.TempDir()

	cfg, err := LoadProjectConfig(projectDir, true)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// With resolveKeys=true, the env key should be populated
	assert.Equal(t, "anthropic", cfg.LLM.Provider)
	assert.Equal(t, "sk-ant-resolved", cfg.LLM.APIKey)
}

func TestLoadProjectConfig_ResolveKeysFalse(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// User config sets provider but no API key
	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
provider = "anthropic"
model = "claude-sonnet-4-20250514"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	// Set the env var that resolveAPIKeys would pick up
	os.Setenv("ANTHROPIC_API_KEY", "sk-ant-resolved")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	projectDir := t.TempDir()

	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// With resolveKeys=false, the env key should NOT be populated
	assert.Equal(t, "anthropic", cfg.LLM.Provider)
	assert.Empty(t, cfg.LLM.APIKey)
}

func TestLoadProjectConfig_SessionEnabledDefault(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// User config has [session] section but no enabled key
	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[session]
max_sessions = 20
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	projectDir := t.TempDir()

	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// session.enabled should default to true even though [session] exists without it
	assert.True(t, cfg.Session.Enabled)
	assert.Equal(t, 20, cfg.Session.MaxSessions)
}

func TestLoadProjectConfig_EmptyProjectRoot(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// User config sets some values
	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
provider = "anthropic"
model = "claude-sonnet-4-20250514"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	// Empty projectRoot — should skip project layer without error
	cfg, err := LoadProjectConfig("", false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// User-level config should still be loaded
	assert.Equal(t, "anthropic", cfg.LLM.Provider)
	assert.Equal(t, "claude-sonnet-4-20250514", cfg.LLM.Model)
}

// =============================================================================
// TOML Comment Preservation Tests
// =============================================================================

func TestFindTOMLSectionBounds(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		section     string
		expectStart int
		expectEnd   int
		expectFound bool
	}{
		{
			name: "find section in middle",
			content: `[storage]
path = "/data"

[llm]
provider = "openai"
model = "gpt-4"

[history]
enabled = true`,
			section:     "llm",
			expectStart: 3,
			expectEnd:   7,
			expectFound: true,
		},
		{
			name: "find first section",
			content: `[llm]
provider = "openai"

[history]
enabled = true`,
			section:     "llm",
			expectStart: 0,
			expectEnd:   3,
			expectFound: true,
		},
		{
			name: "find last section",
			content: `[llm]
provider = "openai"

[history]
enabled = true`,
			section:     "history",
			expectStart: 3,
			expectEnd:   5,
			expectFound: true,
		},
		{
			name: "section not found",
			content: `[llm]
provider = "openai"`,
			section:     "history",
			expectStart: -1,
			expectEnd:   2,
			expectFound: false,
		},
		{
			name:        "empty content",
			content:     "",
			section:     "llm",
			expectStart: -1,
			expectEnd:   1,
			expectFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := strings.Split(tt.content, "\n")
			start, end, found := FindTOMLSectionBounds(lines, tt.section)
			assert.Equal(t, tt.expectFound, found, "found mismatch")
			if found {
				assert.Equal(t, tt.expectStart, start, "start mismatch")
				assert.Equal(t, tt.expectEnd, end, "end mismatch")
			}
		})
	}
}

func TestUpdateTOMLValue(t *testing.T) {
	tests := []struct {
		name              string
		content           string
		section           string
		key               string
		newValue          string
		expectFound       bool
		expectContains    []string
		expectNotContains []string
	}{
		{
			name: "update existing value",
			content: `[llm]
provider = "openai"
model = "gpt-3.5"`,
			section:           "llm",
			key:               "model",
			newValue:          "gpt-4",
			expectFound:       true,
			expectContains:    []string{`model = "gpt-4"`},
			expectNotContains: []string{`model = "gpt-3.5"`},
		},
		{
			name: "preserve inline comment",
			content: `[llm]
provider = "openai" # the provider
model = "gpt-3.5"`,
			section:        "llm",
			key:            "provider",
			newValue:       "anthropic",
			expectFound:    true,
			expectContains: []string{`provider = "anthropic" # the provider`},
		},
		{
			name: "preserve full-line comments",
			content: `[llm]
# This is the provider setting
provider = "openai"
# This is the model setting
model = "gpt-3.5"`,
			section:     "llm",
			key:         "provider",
			newValue:    "anthropic",
			expectFound: true,
			expectContains: []string{
				"# This is the provider setting",
				`provider = "anthropic"`,
				"# This is the model setting",
			},
		},
		{
			name: "key not found in section",
			content: `[llm]
provider = "openai"`,
			section:     "llm",
			key:         "model",
			newValue:    "gpt-4",
			expectFound: false,
		},
		{
			name: "section not found",
			content: `[storage]
path = "/data"`,
			section:     "llm",
			key:         "provider",
			newValue:    "openai",
			expectFound: false,
		},
		{
			name: "update value with special characters",
			content: `[llm]
api_key = "old-key"`,
			section:        "llm",
			key:            "api_key",
			newValue:       `new-key-with-"quotes"`,
			expectFound:    true,
			expectContains: []string{`api_key = "new-key-with-\"quotes\""`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, found := UpdateTOMLValue(tt.content, tt.section, tt.key, tt.newValue)
			assert.Equal(t, tt.expectFound, found)
			for _, s := range tt.expectContains {
				assert.Contains(t, result, s)
			}
			for _, s := range tt.expectNotContains {
				assert.NotContains(t, result, s)
			}
		})
	}
}

func TestInsertTOMLValue(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		section        string
		key            string
		value          string
		expectContains []string
	}{
		{
			name: "insert into existing section",
			content: `[llm]
provider = "openai"`,
			section: "llm",
			key:     "model",
			value:   "gpt-4",
			expectContains: []string{
				`provider = "openai"`,
				`model = "gpt-4"`,
			},
		},
		{
			name: "insert preserves comments",
			content: `[llm]
# Provider setting
provider = "openai"

[history]
enabled = true`,
			section: "llm",
			key:     "model",
			value:   "gpt-4",
			expectContains: []string{
				"# Provider setting",
				`provider = "openai"`,
				`model = "gpt-4"`,
				"[history]",
			},
		},
		{
			name: "section not found returns unchanged",
			content: `[storage]
path = "/data"`,
			section: "llm",
			key:     "provider",
			value:   "openai",
			expectContains: []string{
				"[storage]",
				`path = "/data"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := InsertTOMLValue(tt.content, tt.section, tt.key, tt.value)
			for _, s := range tt.expectContains {
				assert.Contains(t, result, s)
			}
		})
	}
}

func TestRemoveTOMLKey(t *testing.T) {
	tests := []struct {
		name              string
		content           string
		section           string
		key               string
		expectContains    []string
		expectNotContains []string
	}{
		{
			name: "remove existing key",
			content: `[llm]
provider = "openai"
api_key = "secret"
model = "gpt-4"`,
			section: "llm",
			key:     "api_key",
			expectContains: []string{
				`provider = "openai"`,
				`model = "gpt-4"`,
			},
			expectNotContains: []string{
				"api_key",
				"secret",
			},
		},
		{
			name: "remove preserves surrounding comments",
			content: `[llm]
# Provider setting
provider = "openai"
# API key (to be removed)
api_key = "secret"
# Model setting
model = "gpt-4"`,
			section: "llm",
			key:     "api_key",
			expectContains: []string{
				"# Provider setting",
				`provider = "openai"`,
				"# API key (to be removed)",
				"# Model setting",
				`model = "gpt-4"`,
			},
			expectNotContains: []string{
				`api_key = "secret"`,
			},
		},
		{
			name: "key not found returns unchanged",
			content: `[llm]
provider = "openai"`,
			section: "llm",
			key:     "api_key",
			expectContains: []string{
				`provider = "openai"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RemoveTOMLKey(tt.content, tt.section, tt.key)
			for _, s := range tt.expectContains {
				assert.Contains(t, result, s)
			}
			for _, s := range tt.expectNotContains {
				assert.NotContains(t, result, s)
			}
		})
	}
}

func TestEnsureTOMLSection(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		section        string
		expectContains []string
	}{
		{
			name: "section already exists",
			content: `[llm]
provider = "openai"`,
			section: "llm",
			expectContains: []string{
				"[llm]",
				`provider = "openai"`,
			},
		},
		{
			name:    "add new section to empty content",
			content: "",
			section: "llm",
			expectContains: []string{
				"[llm]",
			},
		},
		{
			name: "add new section to existing content",
			content: `[storage]
path = "/data"`,
			section: "llm",
			expectContains: []string{
				"[storage]",
				`path = "/data"`,
				"[llm]",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EnsureTOMLSection(tt.content, tt.section)
			for _, s := range tt.expectContains {
				assert.Contains(t, result, s)
			}
		})
	}
}

func TestUpdateOrInsertTOMLValue(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		section        string
		key            string
		value          string
		expectContains []string
	}{
		{
			name: "update existing key",
			content: `[llm]
provider = "openai"`,
			section: "llm",
			key:     "provider",
			value:   "anthropic",
			expectContains: []string{
				`provider = "anthropic"`,
			},
		},
		{
			name: "insert new key in existing section",
			content: `[llm]
provider = "openai"`,
			section: "llm",
			key:     "model",
			value:   "gpt-4",
			expectContains: []string{
				`provider = "openai"`,
				`model = "gpt-4"`,
			},
		},
		{
			name:    "create section and insert key",
			content: "",
			section: "llm",
			key:     "provider",
			value:   "openai",
			expectContains: []string{
				"[llm]",
				`provider = "openai"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UpdateOrInsertTOMLValue(tt.content, tt.section, tt.key, tt.value)
			for _, s := range tt.expectContains {
				assert.Contains(t, result, s)
			}
		})
	}
}

func TestSaveConfigPreservesComments(t *testing.T) {
	skipIfNotCI(t)
	tempDir := t.TempDir()

	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(originalDir)

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	t.Run("preserves comments when updating", func(t *testing.T) {
		originalHome := os.Getenv("HOME")
		os.Setenv("HOME", tempDir)
		defer os.Setenv("HOME", originalHome)

		configDir := filepath.Join(tempDir, ".config", "asimi")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		initialContent := `# Project configuration
# This file configures the LLM settings

[llm]
# The LLM provider to use
provider = "openai"
# The model name
model = "gpt-3.5-turbo" # default model

[history]
# Enable history tracking
enabled = true
`
		err = os.WriteFile(filepath.Join(configDir, "asimi.conf"), []byte(initialContent), 0644)
		require.NoError(t, err)

		cfg := &Config{
			LLM: LLMConfig{
				Provider: "anthropic",
				Model:    "claude-3-opus",
			},
		}

		err = SaveConfig(cfg)
		require.NoError(t, err)

		content, err := os.ReadFile(filepath.Join(configDir, "asimi.conf"))
		require.NoError(t, err)
		contentStr := string(content)

		assert.Contains(t, contentStr, "# Project configuration")
		assert.Contains(t, contentStr, "# This file configures the LLM settings")
		assert.Contains(t, contentStr, "# The LLM provider to use")
		assert.Contains(t, contentStr, "# The model name")
		assert.Contains(t, contentStr, "# Enable history tracking")

		assert.Contains(t, contentStr, `provider = "anthropic"`)
		assert.Contains(t, contentStr, `model = "claude-3-opus"`)

		assert.Contains(t, contentStr, "[history]")
		assert.Contains(t, contentStr, "enabled = true")
	})

	t.Run("preserves inline comments", func(t *testing.T) {
		originalHome := os.Getenv("HOME")
		os.Setenv("HOME", tempDir)
		defer os.Setenv("HOME", originalHome)

		configDir := filepath.Join(tempDir, ".config", "asimi")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		initialContent := `[llm]
provider = "openai" # cloud provider
model = "gpt-3.5" # fast model
`
		err = os.WriteFile(filepath.Join(configDir, "asimi.conf"), []byte(initialContent), 0644)
		require.NoError(t, err)

		cfg := &Config{
			LLM: LLMConfig{
				Provider: "anthropic",
				Model:    "claude-3",
			},
		}

		err = SaveConfig(cfg)
		require.NoError(t, err)

		content, err := os.ReadFile(filepath.Join(configDir, "asimi.conf"))
		require.NoError(t, err)
		contentStr := string(content)

		assert.Contains(t, contentStr, `provider = "anthropic" # cloud provider`)
		assert.Contains(t, contentStr, `model = "claude-3" # fast model`)
	})
}

func TestEnsureProjectConfig(t *testing.T) {
	t.Run("creates .agents/asimi.conf when not present", func(t *testing.T) {
		projectRoot := t.TempDir()

		err := EnsureProjectConfig(projectRoot)
		require.NoError(t, err)

		confPath := filepath.Join(projectRoot, ".agents", "asimi.conf")
		data, err := os.ReadFile(confPath)
		require.NoError(t, err)
		assert.NotEmpty(t, data, "config file should have content from embedded template")
	})

	t.Run("does not overwrite existing file", func(t *testing.T) {
		projectRoot := t.TempDir()
		agentsDir := filepath.Join(projectRoot, ".agents")
		require.NoError(t, os.MkdirAll(agentsDir, 0o755))

		existing := []byte("existing = true\n")
		confPath := filepath.Join(agentsDir, "asimi.conf")
		require.NoError(t, os.WriteFile(confPath, existing, 0o644))

		err := EnsureProjectConfig(projectRoot)
		require.NoError(t, err)

		data, err := os.ReadFile(confPath)
		require.NoError(t, err)
		assert.Equal(t, existing, data, "should not overwrite existing config")
	})

	t.Run("creates .agents directory if missing", func(t *testing.T) {
		projectRoot := t.TempDir()

		err := EnsureProjectConfig(projectRoot)
		require.NoError(t, err)

		info, err := os.Stat(filepath.Join(projectRoot, ".agents"))
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})
}

func TestDefaultConfContent_ReferencesCorrectPaths(t *testing.T) {
	content := DefaultConfContent()

	// The embedded default.conf must reference the actual config file paths
	// used by LoadProjectConfig — not stale names.
	assert.Contains(t, content, "~/.config/asimi/asimi.conf",
		"comment must reference user config path used by LoadProjectConfig")
	assert.Contains(t, content, ".agents/asimi.conf",
		"comment must reference project config path used by LoadProjectConfig")

	// The ASIMI_* env var loading layer was removed; the comment must not claim it exists.
	assert.NotContains(t, content, "ASIMI_*",
		"comment must not reference removed ASIMI_* env var layer")

	// Stale file extensions must not appear.
	assert.NotContains(t, content, "conf.toml",
		"comment must not reference stale conf.toml filename")
	assert.NotContains(t, content, "asimi.toml",
		"comment must not reference stale asimi.toml filename")
}

// =============================================================================
// LoadProjectConfig: missing vs malformed config
// =============================================================================

func TestLoadProjectConfig_MissingUserConfig_UsesDefaults(t *testing.T) {
	// No user config file exists — should return defaults without error.
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	cfg, err := LoadProjectConfig("", false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Defaults should be present
	assert.True(t, cfg.Session.Enabled)
	assert.Equal(t, 300, cfg.LLM.RequestTimeoutSeconds)
}

func TestLoadProjectConfig_MalformedUserConfig_ReturnsError(t *testing.T) {
	// A broken user config (duplicate TOML key) must return an error,
	// not silently fall back to defaults.
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	malformed := `[llm]
provider = "openai"
provider = "anthropic"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(malformed), 0o644))

	_, err := LoadProjectConfig("", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "asimi.conf")
}

func TestLoadProjectConfig_MalformedProjectConfig_ReturnsError(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	projectDir := t.TempDir()
	agentsDir := filepath.Join(projectDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	malformed := `[llm]
provider = "openai"
provider = "anthropic"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "asimi.conf"), []byte(malformed), 0o644))

	_, err := LoadProjectConfig(projectDir, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "asimi.conf")
}

// TestDefaultConf_ProviderIsEmpty verifies that the embedded default.conf
// template has provider commented out as empty string, matching DefaultConfig()
// which leaves Provider as "". This prevents user confusion: the old template
// had #provider = "anthropic" which didn't match the runtime default.
func TestDefaultConf_ProviderIsEmpty(t *testing.T) {
	content := DefaultConfContent()

	// The provider line should be commented out with an empty value
	assert.Contains(t, content, `# provider = `,
		"default.conf must have provider = \"\" (matching DefaultConfig), not a hardcoded provider name")

	// Must NOT contain the old contradictory default
	assert.NotContains(t, content, `#provider = "anthropic"`,
		"default.conf must not default provider to anthropic — DefaultConfig leaves it empty")
}

// =============================================================================
// ValidateConfigFile Tests
// =============================================================================

func TestValidateConfigFile_ValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "valid.conf")
	require.NoError(t, os.WriteFile(path, []byte(`[llm]
provider = "openai"
model = "gpt-4"
`), 0o644))

	err := ValidateConfigFile(path)
	assert.NoError(t, err)
}

func TestValidateConfigFile_FileDoesNotExist(t *testing.T) {
	err := ValidateConfigFile("/nonexistent/path/to/config.conf")
	assert.NoError(t, err, "missing config file should not be an error")
}

func TestValidateConfigFile_SyntaxError_RichDiagnostic(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.conf")

	// Unclosed quoted string — a classic TOML syntax error
	malformed := `[llm]
provider = "anthropic
model = "claude-sonnet-4"
`
	require.NoError(t, os.WriteFile(path, []byte(malformed), 0o644))

	err := ValidateConfigFile(path)
	require.Error(t, err)

	errStr := err.Error()
	// Must include the file path
	assert.Contains(t, errStr, path)
	// Must include context from the rich diagnostic (pelletier's DecodeError.String()
	// includes line numbers with pipe characters, e.g. "1| [llm]" or "2| provider")
	assert.Contains(t, errStr, "|")
	// Must NOT be the terse message alone (no line numbers / context)
	assert.NotEqual(t, errStr, "failed to parse "+path+": toml: basic strings cannot have new lines")
}

func TestValidateConfigFile_DuplicateKey(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "dup.conf")
	malformed := `[llm]
provider = "openai"
provider = "anthropic"
`
	require.NoError(t, os.WriteFile(path, []byte(malformed), 0o644))

	err := ValidateConfigFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), path)
	// pelletier's go-toml catches duplicate keys
	assert.Contains(t, err.Error(), "provider")
}

func TestValidateConfigFile_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "empty.conf")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o644))

	err := ValidateConfigFile(path)
	assert.NoError(t, err, "empty config file should be valid")
}

// =============================================================================
// Backward-compat: [shogunate] section reading (Edict 638, Phase 3)
// =============================================================================

// TestLoadProjectConfig_ShogunateBackwardCompat verifies that a config file
// with an old [shogunate] section is read as [court] when [court] is absent.
func TestLoadProjectConfig_ShogunateBackwardCompat(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	projectDir := t.TempDir()
	agentsDir := filepath.Join(projectDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	// Old-format config with [shogunate] instead of [court]
	projectConfig := `[shogunate]
username = "legacy-user"
project = "legacy-project"
ritual_timeout = "10m"
step_idle_timeout = "3m"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "asimi.conf"), []byte(projectConfig), 0o644))

	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "legacy-user", cfg.Court.Username)
	assert.Equal(t, "legacy-project", cfg.Court.Project)
	assert.Equal(t, 10*time.Minute, cfg.Court.RitualTimeout)
	assert.Equal(t, 3*time.Minute, cfg.Court.StepIdleTimeout)
}

// TestLoadProjectConfig_CourtOverridesShogunate verifies that [court] takes
// precedence over [shogunate] when both sections are present.
func TestLoadProjectConfig_CourtOverridesShogunate(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	projectDir := t.TempDir()
	agentsDir := filepath.Join(projectDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	projectConfig := `[shogunate]
username = "old-user"
project = "old-project"

[court]
username = "new-user"
project = "new-project"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "asimi.conf"), []byte(projectConfig), 0o644))

	cfg, err := LoadProjectConfig(projectDir, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "new-user", cfg.Court.Username)
	assert.Equal(t, "new-project", cfg.Court.Project)
}

// TestResolveAPIKeys_Convention verifies that convention-based env var resolution
// works for providers that were not in the old hardcoded switch (e.g. cohere).
func TestResolveAPIKeys_Convention(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
provider = "cohere"
model = "command-r-plus"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	t.Setenv("COHERE_API_KEY", "cohere-test-key")

	projectDir := t.TempDir()
	cfg, err := LoadProjectConfig(projectDir, true)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "cohere", cfg.LLM.Provider)
	assert.Equal(t, "cohere-test-key", cfg.LLM.APIKey)
}

// TestResolveAPIKeys_ConventionMistral verifies another non-hardcoded provider
func TestResolveAPIKeys_ConventionMistral(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
provider = "mistral"
model = "mistral-large"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	t.Setenv("MISTRAL_API_KEY", "mistral-test-key")

	projectDir := t.TempDir()
	cfg, err := LoadProjectConfig(projectDir, true)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "mistral", cfg.LLM.Provider)
	assert.Equal(t, "mistral-test-key", cfg.LLM.APIKey)
}
