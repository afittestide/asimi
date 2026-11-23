package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
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

// --- Edict 1005: ProvideCourt scope derivation ---

// fakeCourtRunner is the minimal runners.Runner ProvideCourt needs to build a
// Court. It never runs anything; these tests exercise scope resolution, not
// shell execution.
type fakeCourtRunner struct{ runners.Runner }

func (fakeCourtRunner) Run(context.Context, runners.Input) (runners.Output, error) {
	return runners.Output{}, nil
}
func (fakeCourtRunner) Restart(context.Context) error        { return nil }
func (fakeCourtRunner) Close(context.Context) error          { return nil }
func (fakeCourtRunner) AllowFallback(bool)                   {}
func (fakeCourtRunner) RunnerType() string                   { return "fake" }
func (fakeCourtRunner) GetOS() string                        { return "linux" }
func (fakeCourtRunner) SetMessageChannel(chan<- runners.Msg) {}
func (fakeCourtRunner) HealthCheck(context.Context) error    { return nil }

// provideCourtParams builds the fx params ProvideCourt takes, pointed at a
// fixture checkout.
func provideCourtParams(t *testing.T, projectRoot string, hasVCS bool) CourtParams {
	t.Helper()
	db := setupTestGormDB(t)
	return CourtParams{
		Lifecycle: nopLifecycle{},
		GormDB:    db,
		Config:    &Config{},
		RepoInfo: repo.RepoInfo{
			ProjectRoot: projectRoot,
			HasVCS:      hasVCS,
		},
		Runner: fakeCourtRunner{},
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

// TestProvideCourtRefusesOnOriginlessAmbiguity pins edict 1005 acceptance: the
// boot path (TUI/headless, via fx) must refuse when the checkout has no origin
// remote and its remotes name different repos, rather than silently picking a
// scope that can receive writes. Falsifiable: pass a committed [court] project
// through the config and the refusal must still happen — the pin is not a
// source of scope.
func TestProvideCourtRefusesOnOriginlessAmbiguity(t *testing.T) {
	dir := t.TempDir()
	runProviderGit(t, dir, "init")
	// Two remotes naming different repos, and deliberately no origin.
	runProviderGit(t, dir, "remote", "add", "fork", "https://github.com/daonb/data-poisoning-benchmark.git")
	runProviderGit(t, dir, "remote", "add", "upstream", "https://github.com/OCT7-IO/other-repo.git")
	t.Setenv("ASIMI_SKIP_GIT_STATUS", "1")

	params := provideCourtParams(t, dir, true)
	// A committed pin must not bypass the refusal.
	params.Config.Court.Project = "pinned/project"

	c, err := ProvideCourt(params)
	require.Error(t, err, "origin-less ambiguous checkout must refuse to boot")
	assert.Nil(t, c)
	assert.Contains(t, err.Error(), "add an origin remote",
		"the error must tell the user how to fix the checkout")
	assert.NotContains(t, err.Error(), "pinned/project",
		"a committed [court] project must not rescue the boot or leak into scope")
}

// TestProvideCourtDerivesScopeFromCheckout verifies the success path: with an
// origin present the Court boots and its scope is <verified user>/<origin repo
// basename>, never the committed pin.
func TestProvideCourtDerivesScopeFromCheckout(t *testing.T) {
	dir := t.TempDir()
	runProviderGit(t, dir, "init")
	runProviderGit(t, dir, "remote", "add", "origin", "https://github.com/afittestide/asimi-cli.git")
	t.Setenv("ASIMI_SKIP_GIT_STATUS", "1")

	params := provideCourtParams(t, dir, true)
	params.Config.Court.Project = "pinned/project"

	c, err := ProvideCourt(params)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Stop()

	want := repo.SanitizeSegment(config.OSUsername()) + "/asimi-cli"
	if got := c.EdictKey(1).Project; got != want {
		t.Errorf("Court project = %q, want checkout-derived %q (committed pin must be ignored)", got, want)
	}
}

// TestProvideCourtForwardsCourtConfig verifies that [court] fields such as
// MaxParallelLings and StepIdleTimeout reach the constructed Court while
// Project stays checkout-derived.
func TestProvideCourtForwardsCourtConfig(t *testing.T) {
	dir := t.TempDir()
	runProviderGit(t, dir, "init")
	runProviderGit(t, dir, "remote", "add", "origin", "https://github.com/afittestide/asimi-cli.git")
	t.Setenv("ASIMI_SKIP_GIT_STATUS", "1")

	params := provideCourtParams(t, dir, true)
	params.Config.Court.Project = "pinned/project"
	params.Config.Court.MaxParallelLings = 4
	params.Config.Court.StepIdleTimeout = 2 * time.Minute

	c, err := ProvideCourt(params)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Stop()

	got := c.Config()
	wantProject := repo.SanitizeSegment(config.OSUsername()) + "/asimi-cli"
	assert.Equal(t, wantProject, got.Project)
	assert.Equal(t, 4, got.MaxParallelLings)
	assert.Equal(t, 2*time.Minute, got.StepIdleTimeout)
}

// nopLifecycle is a do-nothing fx.Lifecycle for tests that never start the app.
type nopLifecycle struct{}

func (nopLifecycle) Append(fx.Hook) {}

// runProviderGit runs a git command in dir, failing the test on error.
func runProviderGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
