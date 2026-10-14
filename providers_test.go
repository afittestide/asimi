package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
)

// TestProvideConfig_ReturnsErrorOnMalformedConfig verifies that ProvideConfig
// propagates errors from LoadProjectConfig instead of silently falling back to
// a hardcoded default config. This is the core behavioral change of edict 523:
// the dead os.ErrNotExist fallback (which had contradictory openai/gpt-3.5-turbo
// defaults) was removed.
func TestProvideConfig_ReturnsErrorOnMalformedConfig(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// Create a malformed user config (duplicate TOML key)
	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	malformed := `[llm]
provider = "openai"
provider = "anthropic"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(malformed), 0o644))

	logger := slog.Default()
	ri := repo.RepoInfo{ProjectRoot: ""}

	cfg, err := ProvideConfig(logger, ri)
	require.Error(t, err, "malformed config must return an error, not silently fall back")
	assert.Nil(t, cfg, "config should be nil on error")
	assert.Contains(t, err.Error(), "failed to load configuration")
}

// TestProvideConfig_SucceedsWithDefaults verifies that ProvideConfig succeeds
// and returns valid defaults when no config file exists (the normal first-run
// path). This confirms the dead fallback removal didn't break the happy path.
func TestProvideConfig_SucceedsWithDefaults(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// EnsureUserConfigExists will create the config file from the embedded template
	_, err := config.EnsureUserConfigExists()
	require.NoError(t, err)

	logger := slog.Default()
	ri := repo.RepoInfo{ProjectRoot: ""}

	cfg, err := ProvideConfig(logger, ri)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Provider should be empty (matching DefaultConfig), NOT "openai"
	// (the old dead fallback hardcoded "openai")
	assert.Empty(t, cfg.LLM.Provider, "provider should default to empty, not a hardcoded value")
}

// TestProvideConfig_MaxTurnsOverride verifies that the --max-turns CLI flag
// overrides the LLMConfig.MaxTurns value from the config file.
func TestProvideConfig_MaxTurnsOverride(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	_, err := config.EnsureUserConfigExists()
	require.NoError(t, err)

	originalMaxTurns := cli.MaxTurns
	cli.MaxTurns = 42
	defer func() { cli.MaxTurns = originalMaxTurns }()

	logger := slog.Default()
	ri := repo.RepoInfo{ProjectRoot: ""}

	cfg, err := ProvideConfig(logger, ri)
	require.NoError(t, err)
	assert.Equal(t, 42, cfg.LLM.MaxTurns, "MaxTurns should be overridden by --max-turns CLI flag")
}

// TestProvideConfig_ModelOverride verifies that the --model and --provider
// CLI flags override the LLM config values.
func TestProvideConfig_ModelOverride(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	_, err := config.EnsureUserConfigExists()
	require.NoError(t, err)

	originalModel := cli.Model
	originalProvider := cli.Provider
	cli.Model = "gpt-4o"
	cli.Provider = "openai"
	defer func() {
		cli.Model = originalModel
		cli.Provider = originalProvider
	}()

	logger := slog.Default()
	ri := repo.RepoInfo{ProjectRoot: ""}

	cfg, err := ProvideConfig(logger, ri)
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o", cfg.LLM.Model, "Model should be overridden by --model CLI flag")
	assert.Equal(t, "openai", cfg.LLM.Provider, "Provider should be overridden by --provider CLI flag")
}

// TestProvideConfig_ReasoningEffortPrecedence verifies the precedence
// chain for reasoning effort: CLI flag > env var > config file.
// The env var is folded into the config by LoadProjectConfig, and the
// CLI flag is applied last in ProvideConfig so it wins.
func TestProvideConfig_ReasoningEffortPrecedence(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// Config file sets "low".
	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userCfg := `[llm]
reasoning_effort = "low"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userCfg), 0o644))

	// Env var sets "medium".
	origEnv := os.Getenv("ASIMI_REASONING_EFFORT")
	defer os.Setenv("ASIMI_REASONING_EFFORT", origEnv)
	os.Setenv("ASIMI_REASONING_EFFORT", "medium")

	// CLI flag sets "high" — should win.
	origCli := cli.ReasoningEffort
	cli.ReasoningEffort = "high"
	defer func() { cli.ReasoningEffort = origCli }()

	logger := slog.Default()
	ri := repo.RepoInfo{ProjectRoot: ""}

	cfg, err := ProvideConfig(logger, ri)
	require.NoError(t, err)
	assert.Equal(t, "high", cfg.LLM.ReasoningEffort, "CLI flag should take precedence over env var and config")

	// Clear the CLI flag: env var should now win over the config file.
	cli.ReasoningEffort = ""
	cfg, err = ProvideConfig(logger, ri)
	require.NoError(t, err)
	assert.Equal(t, "medium", cfg.LLM.ReasoningEffort, "env var should take precedence over config file")

	// Clear the env var too: config file should win.
	os.Unsetenv("ASIMI_REASONING_EFFORT")
	cfg, err = ProvideConfig(logger, ri)
	require.NoError(t, err)
	assert.Equal(t, "low", cfg.LLM.ReasoningEffort, "config file should take precedence over default")
}

// TestProvideConfig_ReasoningEffortInvalidCLI verifies that ProvideConfig
// rejects an invalid --reasoning-effort value even though the CLI flag is
// merged after LoadProjectConfig's validation (C1 from code review): the CLI
// merge point applies the shared validator too.
func TestProvideConfig_ReasoningEffortInvalidCLI(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	_, err := config.EnsureUserConfigExists()
	require.NoError(t, err)

	origCli := cli.ReasoningEffort
	cli.ReasoningEffort = "turbo"
	defer func() { cli.ReasoningEffort = origCli }()

	logger := slog.Default()
	ri := repo.RepoInfo{ProjectRoot: ""}

	cfg, err := ProvideConfig(logger, ri)
	require.Error(t, err, "invalid --reasoning-effort should be rejected at the CLI merge point")
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "reasoning_effort")
}

// TestNoGlobalRunnerSingleton verifies the package-global runner is fully
// retired: no production code references runners.GetRunner or runners.SetRunner
// (both of which no longer exist, so any reference would also fail to compile).
func TestNoGlobalRunnerSingleton(t *testing.T) {
	root, err := os.Getwd()
	require.NoError(t, err)

	banned := []string{"runners.GetRunner", "runners.SetRunner"}
	var offenders []string

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" || name == ".agents" || name == "tmp" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, b := range banned {
			if strings.Contains(string(data), b) {
				offenders = append(offenders, path+": "+b)
			}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, offenders, "package-global runner singleton must be fully retired")
}
