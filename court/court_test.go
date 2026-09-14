package court

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"reflect"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/mocks"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "modernc.org/sqlite"
)

func setupCourtTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "court_test")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	dbPath := filepath.Join(tmpDir, "test.db")
	sqlDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	// Enable WAL mode and busy_timeout for better concurrency — the ritual
	// guard runs concurrently and performs writes, so without these PRAGMAs
	// SQLite returns "database is locked" errors.
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "PRAGMA journal_mode = WAL")
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "PRAGMA busy_timeout = 5000")
	require.NoError(t, err)
	conn.Close()

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	err = db.AutoMigrate(
		&storage.Edict{},
		&storage.Zhengming{},
		&storage.TianEvent{},
		&storage.TianEventDLQ{},
		&storage.Ling{},
		&storage.ForgeManifest{},
		&storage.Seal{},
		&storage.JudgeVerdict{},
		&storage.CensorPrecedent{},
		&storage.Incident{},
		&storage.RulerCouncil{},
		&storage.RitualGuardCheckpoint{},
		&RitualExecution{},
		&RitualStepState{},
	)
	require.NoError(t, err)

	db.Exec(`CREATE TABLE IF NOT EXISTS ritual_guard_checkpoint (id INTEGER PRIMARY KEY, event_id INTEGER NOT NULL, updated_at DATETIME)`)
	return db
}

func TestSetContext_NilCourt(t *testing.T) {
	var s *Court
	err := s.SetContext(context.Background(), types.SetContextParams{})
	assert.EqualError(t, err, "court not initialised")
}

// TestCourt_ConnDone_NeverClosed verifies that the in-process Court's
// ConnDone returns a channel that is never closed (edict 552).
func TestCourt_ConnDone_NeverClosed(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)

	done := s.ConnDone()
	select {
	case <-done:
		t.Fatal("ConnDone should return a never-closed channel for in-process mode")
	default:
		// Good — channel is open and never closes
	}
}

// runGit runs a git command in the given directory, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

func TestSetContext_InvalidProjectRoot(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)

	err := s.SetContext(context.Background(), types.SetContextParams{
		ProjectRoot: "/nonexistent/path/that/does/not/exist",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid project_root")
}

func TestSetContext_ProjectRootIsFile(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)

	// Create a temporary file (not a directory) as ProjectRoot
	tmpFile, err := os.CreateTemp("", "notadir_*")
	require.NoError(t, err)
	tmpFile.Close()
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	err = s.SetContext(context.Background(), types.SetContextParams{
		ProjectRoot: tmpFile.Name(),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestSetContext_EmptyProjectRoot_UsesCwd(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)

	// Empty ProjectRoot defaults to "." which should resolve to current working dir.
	// This may succeed or fail depending on whether cwd has an .agents dir,
	// but it must not panic.
	err := s.SetContext(context.Background(), types.SetContextParams{
		ProjectRoot: "",
		APIKeys:     map[string]string{"openai": "sk-test"},
	})
	// We don't assert on success/failure because it depends on the test
	// environment's config files, but we verify no panic occurred.
	_ = err
}

func TestSetContext_WithAPIKeys(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)

	// Use a temp dir as project root so LoadProjectConfig can find it.
	tmpDir, err := os.MkdirTemp("", "setcontext_test")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	// Create .agents dir so LoadProjectConfig doesn't complain about stat
	agentsDir := filepath.Join(tmpDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	err = s.SetContext(context.Background(), types.SetContextParams{
		ProjectRoot: tmpDir,
		Project:     "test-project",
		Username:    "test-user",
		Branch:      "main",
		APIKeys: map[string]string{
			"openai": "sk-test-key",
		},
	})
	require.NoError(t, err)
}

// TestSetContext_ModelOverride verifies that SetContextParams.Model and
// .Provider override config-file [llm] values when reconfiguring the model.
func TestSetContext_ModelOverride(t *testing.T) {
	tempHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", origHome)

	// User config sets one provider/model; handshake overrides select another.
	userConfigDir := filepath.Join(tempHome, ".config", "asimi")
	require.NoError(t, os.MkdirAll(userConfigDir, 0o755))
	userConfig := `[llm]
provider = "anthropic"
model = "claude-sonnet-4-20250514"
`
	require.NoError(t, os.WriteFile(filepath.Join(userConfigDir, "asimi.conf"), []byte(userConfig), 0o644))

	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)

	tmpDir, err := os.MkdirTemp("", "setcontext_model_override_test")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".agents"), 0o755))

	err = s.SetContext(context.Background(), types.SetContextParams{
		ProjectRoot: tmpDir,
		Project:     "test-project",
		Username:    "test-user",
		Model:       "gpt-4o",
		Provider:    "openai",
	})
	require.NoError(t, err)

	require.NotNil(t, s.sessionCfg, "sessionCfg should be set after ConfigureModel")
	assert.Equal(t, "gpt-4o", s.sessionCfg.LLM.Model, "handshake Model should override config file")
	assert.Equal(t, "openai", s.sessionCfg.LLM.Provider, "handshake Provider should override config file")
}

func TestSetContext_Idempotent(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)

	tmpDir, err := os.MkdirTemp("", "setcontext_idempotent_test")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	agentsDir := filepath.Join(tmpDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	params := types.SetContextParams{
		ProjectRoot: tmpDir,
		Project:     "test-project",
		Username:    "test-user",
		APIKeys: map[string]string{
			"openai": "sk-test-key",
		},
	}

	// First call should succeed
	err = s.SetContext(context.Background(), params)
	require.NoError(t, err)

	// Second call with same params should also succeed (idempotent)
	err = s.SetContext(context.Background(), params)
	require.NoError(t, err)

	// Third call with different keys should also succeed (reconfigure)
	params.APIKeys = map[string]string{
		"anthropic": "sk-ant-test",
	}
	err = s.SetContext(context.Background(), params)
	require.NoError(t, err)
}

func TestSetContext_PropagatesRepoInfo(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)

	tmpDir, err := os.MkdirTemp("", "setcontext_repo_test")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	agentsDir := filepath.Join(tmpDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	err = s.SetContext(context.Background(), types.SetContextParams{
		ProjectRoot:  tmpDir,
		WorktreePath: "/tmp/worktree",
		Branch:       "feature-branch",
		Project:      "my-project",
		Username:     "dev",
		APIKeys: map[string]string{
			"openai": "sk-test",
		},
	})
	require.NoError(t, err)

	// Verify ministers were configured by checking one of them has a session.
	// ConfigureModel sets the config on all ministers; if SetContext
	// completed without error, ConfigureModel was called successfully.
	chancellor := s.GetMinister("secretary")
	assert.NotNil(t, chancellor)
}

func TestSetContext_EmptyProjectDerivesSlugFromGitRemote(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)

	// Create a temp git repo with a remote so GetRepoInfoForRoot can
	// derive a slug (owner/repo).
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	// Initialize a git repo with a remote origin URL
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "remote", "add", "origin", "https://github.com/testorg/testrepo.git")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	err := s.SetContext(context.Background(), types.SetContextParams{
		ProjectRoot: tmpDir,
		// Project is intentionally empty — slug should be derived from git remote
		Project:  "",
		Username: "test-user",
		APIKeys:  map[string]string{"openai": "sk-test"},
	})
	require.NoError(t, err)

	// The chancellor's MinisterBase should have received the repoInfo
	// with a non-empty slug derived from the git remote.
	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)
	if base, ok := chancellor.(interface{ RepoInfo() repo.RepoInfo }); ok {
		ri := base.RepoInfo()
		assert.NotEmpty(t, ri.Slug, "slug should be derived from git remote when Project is empty")
		assert.Equal(t, "testorg/testrepo", ri.Slug)
	}
}

func TestSetContext_NonEmptyProjectUsesExplicitSlug(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)

	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	err := s.SetContext(context.Background(), types.SetContextParams{
		ProjectRoot: tmpDir,
		Project:     "my-org/my-repo",
		Username:    "test-user",
		APIKeys:     map[string]string{"openai": "sk-test"},
	})
	require.NoError(t, err)

	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)
	if base, ok := chancellor.(interface{ RepoInfo() repo.RepoInfo }); ok {
		ri := base.RepoInfo()
		assert.Equal(t, "my-org/my-repo", ri.Slug)
	}
}

func TestGetSandboxImageName_EmptyWhenNoRunner(t *testing.T) {
	db := setupCourtTestDB(t)
	base := NewMinisterBase(db, nil, nil, "testuser", "testproject", nil)
	rg := NewRitualGuard(RitualGuardOpts{Base: base})

	imageName := rg.getSandboxImageName()
	assert.Empty(t, imageName, "getSandboxImageName should return empty string when no PodmanRunner is available")
}

func TestConfigureModel_ReloadsRitualsWhenProjectRootBecomesAvailable(t *testing.T) {
	db := setupCourtTestDB(t)

	// Create court with nil config — no project root set
	s := NewCourt(db, nil, nil, nil)

	// Start should succeed but rituals won't load (empty project root)
	err := s.Start(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { s.Stop() })

	// Registry should be empty since LoadRituals failed
	reg := s.GetRitualRegistry()
	assert.Empty(t, reg.List(), "rituals should be empty when project root is not set")

	// ConfigureModel with a project root should load rituals
	projectRoot := t.TempDir()
	agentsDir := filepath.Join(projectRoot, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	s.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{
		ProjectRoot: projectRoot,
		Slug:        "test-project",
	})

	// Registry should now have embedded rituals (e.g. dawn-audience)
	assert.NotEmpty(t, reg.List(), "rituals should be loaded after ConfigureModel with project root")
}

func TestConfigureModel_DoesNotReloadRitualsWhenRegistryNotEmpty(t *testing.T) {
	db := setupCourtTestDB(t)

	// Create court with nil config — no project root set
	s := NewCourt(db, nil, nil, nil)

	err := s.Start(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { s.Stop() })

	// Manually register a ritual before ConfigureModel runs
	reg := s.GetRitualRegistry()
	customRitual := &RitualDef{
		Name: "custom-ritual",
		Steps: []RitualStep{
			{Minister: "secretary", Act: "do something"},
		},
	}
	require.NoError(t, reg.Register(customRitual))

	// ConfigureModel with a project root should NOT reload rituals
	// because the registry is no longer empty
	projectRoot := t.TempDir()
	agentsDir := filepath.Join(projectRoot, ".agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	s.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{
		ProjectRoot: projectRoot,
		Slug:        "test-project",
	})

	// Only the custom ritual should remain; embedded rituals should NOT be added
	names := reg.List()
	assert.Contains(t, names, "custom-ritual", "manually registered ritual should persist")
	assert.NotContains(t, names, "dawn-audience", "embedded rituals should not be reloaded when registry is not empty")
}

// ---------------------------------------------------------------------------
// Scheduler clearing tests (merged from clear_scheduler_test.go)
// ---------------------------------------------------------------------------

// msgForwardingRunner captures the msgChan (like recordingMsgChanRunner) and
// implements Run/Close/Restart so it satisfies runners.Runner.
type msgForwardingRunner struct {
	runners.Runner
	msgChan chan<- runners.Msg
}

func (r *msgForwardingRunner) SetMessageChannel(ch chan<- runners.Msg) { r.msgChan = ch }
func (r *msgForwardingRunner) Run(context.Context, runners.Input) (runners.Output, error) {
	return runners.Output{}, nil
}
func (r *msgForwardingRunner) Close(context.Context) error   { return nil }
func (r *msgForwardingRunner) Restart(context.Context) error { return nil }
func (r *msgForwardingRunner) AllowFallback(bool)            {}
func (r *msgForwardingRunner) RunnerType() string            { return "msg_forwarding" }
func (r *msgForwardingRunner) GetOS() string                 { return runtime.GOOS }

// TestClearAllSchedulers_NoSessions verifies that clearAllSchedulers returns 0
// when ministers exist but have no sessions.
func TestClearAllSchedulers_NoSessions(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	count := court.clearAllSchedulers()
	assert.Equal(t, 0, count, "clearAllSchedulers should return 0 when no sessions have schedulers")
}

// TestClearAllSchedulers_WithQueuedItems verifies that clearAllSchedulers
// iterates all ministers, finds sessions with schedulers that have queued
// items, clears them, and returns the total aborted count.
func TestClearAllSchedulers_WithQueuedItems(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	// Create blocking tools so items stay in-flight when ClearQueue() runs.
	doneA := make(chan struct{})
	doneB := make(chan struct{})
	doneC := make(chan struct{})
	defer close(doneA)
	defer close(doneB)
	defer close(doneC)

	sched1 := runners.NewCoreToolScheduler(nil)
	sched1.Schedule(context.Background(), &blockingTool{name: "tool_a", done: doneA}, `{}`)
	sched1.Schedule(context.Background(), &blockingTool{name: "tool_b", done: doneB}, `{}`)

	sched2 := runners.NewCoreToolScheduler(nil)
	sched2.Schedule(context.Background(), &blockingTool{name: "tool_c", done: doneC}, `{}`)

	mockLLM := mocks.NewLLMProvider()

	// Create sessions with these schedulers and attach to ministers
	sess1, err := NewSession(mockLLM, &SessionConfig{}, nil, sched1, nil, "test", "secretary")
	require.NoError(t, err)
	sess2, err := NewSession(mockLLM, &SessionConfig{}, nil, sched2, nil, "test", "forge")
	require.NoError(t, err)

	// Attach sessions to ministers
	chancellor := court.GetMinister("secretary")
	require.NotNil(t, chancellor)
	if base, ok := chancellor.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(sess1)
	}

	forge := court.GetMinister("forge")
	require.NotNil(t, forge)
	if base, ok := forge.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(sess2)
	}

	count := court.clearAllSchedulers()
	assert.Equal(t, 3, count, "should abort 2+1 = 3 queued items across two schedulers")

	// Verify the schedulers are now empty
	assert.Equal(t, 0, sched1.ClearQueue(), "sched1 should be empty after clear")
	assert.Equal(t, 0, sched2.ClearQueue(), "sched2 should be empty after clear")
}

// TestClearAllSchedulers_MinistersWithNilScheduler verifies that ministers
// with sessions that have nil schedulers are skipped gracefully.
func TestClearAllSchedulers_MinistersWithNilScheduler(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	mockLLM := mocks.NewLLMProvider()

	// Create a session with a scheduler then nil it out to simulate edge case
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "secretary")
	require.NoError(t, err)

	// Manually nil out the scheduler
	sess.scheduler = nil

	chancellor := court.GetMinister("secretary")
	require.NotNil(t, chancellor)
	if base, ok := chancellor.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(sess)
	}

	// Should not panic and return 0
	count := court.clearAllSchedulers()
	assert.Equal(t, 0, count)
}

// TestSubscribe_HandlesClearSchedulerMsg verifies the core fix from edict 532:
// when Subscribe() receives a ClearSchedulerMsg on the runner's message channel,
// it handles it in-process (calls clearAllSchedulers, replies on ResultChan)
// and does NOT forward it to the out channel.
func TestSubscribe_HandlesClearSchedulerMsg(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()

	runner := &msgForwardingRunner{}
	court := NewCourt(db, cfg, runner, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	// Create blocking tools so items stay in-flight for ClearSchedulerMsg to abort.
	doneX := make(chan struct{})
	doneY := make(chan struct{})
	defer close(doneX)
	defer close(doneY)

	sched := runners.NewCoreToolScheduler(nil)
	sched.Schedule(context.Background(), &blockingTool{name: "tool_x", done: doneX}, `{}`)
	sched.Schedule(context.Background(), &blockingTool{name: "tool_y", done: doneY}, `{}`)

	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, sched, nil, "test", "secretary")
	require.NoError(t, err)

	chancellor := court.GetMinister("secretary")
	require.NotNil(t, chancellor)
	if base, ok := chancellor.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(sess)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := court.Subscribe(ctx)
	require.NotNil(t, out)
	require.NotNil(t, runner.msgChan, "Subscribe should set the runner msg channel")

	// Send a ClearSchedulerMsg through the runner's msg channel
	resultChan := make(chan int, 1)
	runner.msgChan <- runners.ClearSchedulerMsg{ResultChan: resultChan}

	// Wait for the result — it must arrive promptly
	select {
	case count := <-resultChan:
		assert.Equal(t, 2, count, "should abort 2 queued items")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ClearSchedulerMsg result — Restart() would hang")
	}

	// Verify the ClearSchedulerMsg was NOT forwarded to the out channel.
	select {
	case msg := <-out:
		t.Fatalf("ClearSchedulerMsg should NOT be forwarded to out channel, got: %T %+v", msg, msg)
	case <-time.After(100 * time.Millisecond):
		// Expected: no message on out channel
	}

	// Verify scheduler queue was actually cleared
	assert.Equal(t, 0, sched.ClearQueue(), "scheduler should be empty after ClearSchedulerMsg handled")
}

// TestSubscribe_ForwardsNormalMessages verifies that non-ClearSchedulerMsg
// messages still flow through to the out channel.
func TestSubscribe_ForwardsNormalMessages(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()

	runner := &msgForwardingRunner{}
	court := NewCourt(db, cfg, runner, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := court.Subscribe(ctx)
	require.NotNil(t, out)
	require.NotNil(t, runner.msgChan)

	// Send a normal message (not ClearSchedulerMsg) — e.g., SandboxUnhealthyMsg
	normalMsg := runners.SandboxUnhealthyMsg{Message: "stale container", ContainerName: "test"}
	runner.msgChan <- normalMsg

	select {
	case msg := <-out:
		unhealthyMsg, ok := msg.(runners.SandboxUnhealthyMsg)
		assert.True(t, ok, "expected SandboxUnhealthyMsg, got %T", msg)
		assert.Equal(t, "stale container", unhealthyMsg.Message)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for normal message on out channel")
	}
}

// blockingTool is a Tool whose Call blocks until done is closed.
type blockingTool struct {
	name string
	done chan struct{}
}

func (t *blockingTool) Name() string        { return t.name }
func (t *blockingTool) Description() string { return "blocking" }
func (t *blockingTool) Call(ctx context.Context, input string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-t.done:
		return "ok", nil
	}
}
func (t *blockingTool) Format(input, result string, err error) string {
	if err != nil {
		return err.Error()
	}
	return result
}
func (t *blockingTool) ParameterSchema() map[string]any { return nil }

// ---------------------------------------------------------------------------
// Zhengming answer event-handler tests (edict 574)
// ---------------------------------------------------------------------------

// TestZhengmingAnswered_ChatDoesNotCreateEdict is the regression test for the
// bug fixed in edict 574: the "Chat" sentinel (AnswerChat) — emitted when
// the Ruler dismisses a zhengming prompt — was not recognized by the
// zhengming_answered event handler and fell through to the catch-all path
// that created a bogus edict with "Chat" as its intent.
//
// This test publishes a zhengming_answered event with AnswerChat and verifies
// no edict is created.
func TestZhengmingAnswered_ChatDoesNotCreateEdict(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	// Start the ritual guard event loop so dispatched events reach subscribers
	go s.ritualGuard.Run(ctx)

	// Publish a zhengming_answered event with AnswerChat
	s.PublishEvent(storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project},
		storage.EventZhengmingAnswered, storage.JSON{
			"request_id": "test-chat-1",
			"answer":     tools.AnswerChat,
		})

	// Give the event loop time to process
	time.Sleep(100 * time.Millisecond)

	// No edict with intent "Chat" should exist
	var edicts []storage.Edict
	err := db.Where("intent = ?", tools.AnswerChat).Find(&edicts).Error
	require.NoError(t, err)
	assert.Empty(t, edicts, "no edict should be created for Chat answer")
}

// TestZhengmingAnswered_RejectDoesNotCreateEdict verifies that AnswerReject
// is handled as a rejection, not a catch-all edict creation.
func TestZhengmingAnswered_RejectDoesNotCreateEdict(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	go s.ritualGuard.Run(ctx)

	s.PublishEvent(storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project},
		storage.EventZhengmingAnswered, storage.JSON{
			"request_id": "test-reject-1",
			"answer":     tools.AnswerReject,
		})

	time.Sleep(100 * time.Millisecond)

	var edicts []storage.Edict
	err := db.Where("intent = ?", tools.AnswerReject).Find(&edicts).Error
	require.NoError(t, err)
	assert.Empty(t, edicts, "no edict should be created for Reject answer")
}

// TestZhengmingAnswered_ApproveEdictCreatesEdict verifies that AnswerApproveEdict
// triggers edict creation from the stored zhengming suggestion.
func TestZhengmingAnswered_ApproveEdictCreatesEdict(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	go s.ritualGuard.Run(ctx)

	// Store a zhengming request with a suggestion
	req := storage.Zhengming{
		RequestID:  "test-approve-1",
		EdictID:    0,
		Username:   cfg.Username,
		Project:    cfg.Project,
		MinisterID: "chancellor",
		Questions: storage.ZhengmingQuestions{{
			Text:    "Refactor the zhengming constants",
			Summary: "refactor constants",
			Options: []string{tools.AnswerApproveEdict, tools.AnswerReject},
		}},
		Status:   storage.ZhengmingPending,
		Priority: storage.PriorityNormal,
	}
	require.NoError(t, db.Create(&req).Error)

	s.PublishEvent(storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project},
		storage.EventZhengmingAnswered, storage.JSON{
			"request_id": "test-approve-1",
			"answer":     tools.AnswerApproveEdict,
		})

	// Poll for the edict to appear (async handler: CreateEdict + db.Save for summary)
	var edict storage.Edict
	require.Eventually(t, func() bool {
		return db.Where("intent = ?", "Refactor the zhengming constants").First(&edict).Error == nil
	}, 2*time.Second, 50*time.Millisecond, "edict should be created from the approved suggestion")
	assert.Equal(t, "Refactor the zhengming constants", edict.Intent)

	// Summary is set via a separate db.Save call after CreateEdict, which may
	// race with our read. Poll for it.
	require.Eventually(t, func() bool {
		var fresh storage.Edict
		if db.First(&fresh, edict.ID).Error != nil {
			return false
		}
		return fresh.Summary == "refactor constants"
	}, 2*time.Second, 50*time.Millisecond, "edict summary should be set to the suggestion summary")
}

// TestZhengmingAnswered_AnswerConstants verifies that the constant values
// haven't drifted from their expected string sentinels. This guards against
// accidental renames that would break the TUI ↔ court contract.
func TestZhengmingAnswered_AnswerConstants(t *testing.T) {
	assert.Equal(t, "Chat", tools.AnswerChat)
	assert.Equal(t, "Approve edict", tools.AnswerApproveEdict)
	assert.Equal(t, "Reject", tools.AnswerReject)
	assert.Equal(t, "Approve and proceed", tools.AnswerApproveAndProceed)
	assert.Equal(t, "Let me clarify", tools.AnswerLetMeClarify)
}

// TestZhengmingAnswered_ApproveEdictRefinesExistingEdict verifies that when
// a zhengming request has EdictID > 0, approving it appends the suggestion
// to the existing edict's intent instead of creating a new edict.
func TestZhengmingAnswered_ApproveEdictRefinesExistingEdict(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	go s.ritualGuard.Run(ctx)

	// Create an existing edict with the right user/project scoping
	edict := storage.Edict{
		ID:       42,
		Username: cfg.Username,
		Project:  cfg.Project,
		Intent:   "Original intent",
	}
	require.NoError(t, db.Create(&edict).Error)

	// Store a zhengming request linked to the existing edict
	req := storage.Zhengming{
		RequestID:  "test-refine-1",
		EdictID:    42,
		Username:   cfg.Username,
		Project:    cfg.Project,
		MinisterID: "chancellor",
		Questions: storage.ZhengmingQuestions{{
			Text:    "Add better error handling",
			Summary: "refine error handling",
			Options: []string{tools.AnswerApproveEdict, tools.AnswerReject},
		}},
		Status:   storage.ZhengmingPending,
		Priority: storage.PriorityNormal,
	}
	require.NoError(t, db.Create(&req).Error)

	s.PublishEvent(storage.EdictKey{ID: 42, Username: cfg.Username, Project: cfg.Project},
		storage.EventZhengmingAnswered, storage.JSON{
			"request_id": "test-refine-1",
			"answer":     tools.AnswerApproveEdict,
		})

	// Poll for the intent to be updated (async handler)
	require.Eventually(t, func() bool {
		var fresh storage.Edict
		if db.First(&fresh, 42).Error != nil {
			return false
		}
		return strings.Contains(fresh.Intent, "Original intent") &&
			strings.Contains(fresh.Intent, "Add better error handling")
	}, 2*time.Second, 50*time.Millisecond, "edict intent should contain both original and appended suggestion")

	// Verify no new edict was created (edict 1 is the auto-created court infrastructure edict)
	var count int64
	db.Model(&storage.Edict{}).Where("id != ? AND id != ?", 1, 42).Count(&count)
	assert.Equal(t, int64(0), count, "no new edict should be created when refining")
}

// TestZhengmingAnswered_NonSentinelAnswerWithEdictID0CreatesEdict verifies that
// a system-ritual answer (edict_id=0, non-sentinel answer) still creates an
// edict — this is the legitimate catch-all path that should NOT be broken
// by the bug fix.
func TestZhengmingAnswered_NonSentinelAnswerWithEdictID0CreatesEdict(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	go s.ritualGuard.Run(ctx)

	customAnswer := "Let's build a new feature"
	s.PublishEvent(storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project},
		storage.EventZhengmingAnswered, storage.JSON{
			"request_id": "test-custom-1",
			"answer":     customAnswer,
		})

	time.Sleep(100 * time.Millisecond)

	var edicts []storage.Edict
	err := db.Where("intent = ?", customAnswer).Find(&edicts).Error
	require.NoError(t, err)
	assert.Len(t, edicts, 1, "a non-sentinel answer with edict_id=0 should create an edict")
}

// TestZhengmingAnswered_ChatAndNonSentinelIsolation verifies that when a
// Chat answer and a legitimate answer are both processed, only the
// legitimate answer creates an edict. Events are published sequentially to
// avoid SQLite write-lock contention.
func TestZhengmingAnswered_ChatAndNonSentinelIsolation(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	go s.ritualGuard.Run(ctx)

	// Publish Chat first
	s.PublishEvent(storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project},
		storage.EventZhengmingAnswered, storage.JSON{
			"request_id": "isolation-chat",
			"answer":     tools.AnswerChat,
		})
	time.Sleep(100 * time.Millisecond)

	// Then publish a legitimate answer
	legitAnswer := "Build the database migration tool"
	s.PublishEvent(storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project},
		storage.EventZhengmingAnswered, storage.JSON{
			"request_id": "isolation-legit",
			"answer":     legitAnswer,
		})
	time.Sleep(150 * time.Millisecond)

	// Chat should NOT have created an edict
	var chatEdicts []storage.Edict
	err := db.Where("intent = ?", tools.AnswerChat).Find(&chatEdicts).Error
	require.NoError(t, err)
	assert.Empty(t, chatEdicts, "no edict should be created for Chat answer")

	// The legitimate answer SHOULD have created an edict
	var legitEdicts []storage.Edict
	err = db.Where("intent = ?", legitAnswer).Find(&legitEdicts).Error
	require.NoError(t, err)
	assert.Len(t, legitEdicts, 1, "one edict should be created from the legitimate answer")
}

// ---------------------------------------------------------------------------
// HostChecker wiring tests (edict 631)
// ---------------------------------------------------------------------------

// TestBuildToolRegistry_WiresHostChecker verifies that buildToolRegistry
// extracts CheckHostCommand from the chancellor's MinisterBase and stores
// it as s.hostChecker so the shell tool can honor run_on_host /
// safe_run_on_host config patterns.
func TestBuildToolRegistry_WiresHostChecker(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	// ConfigureModel with a SessionConfig that has RunOnHost patterns
	s.ConfigureModel(nil, &SessionConfig{
		Sandbox: config.SandboxConfig{
			RunOnHost: []string{"^gh "},
		},
	}, repo.RepoInfo{})

	// hostChecker should be non-nil after buildToolRegistry
	require.NotNil(t, s.hostChecker, "hostChecker should be wired from chancellor's CheckHostCommand")

	// Verify it actually matches patterns from the config
	runOnHost, needsApproval := s.hostChecker("gh issue list")
	assert.True(t, runOnHost, "command matching RunOnHost should return runOnHost=true")
	assert.True(t, needsApproval, "RunOnHost patterns require approval")

	runOnHost, needsApproval = s.hostChecker("ls -la")
	assert.False(t, runOnHost, "non-matching command should return runOnHost=false")
}

// TestCourt_ImplementsRuntimeDispatchInterfaces verifies the Court directly
// implements the three runtime-dispatch interfaces that were previously
// extracted from the chancellor via type assertion.
func TestCourt_ImplementsRuntimeDispatchInterfaces(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	var _ tools.ZhengmingRequester = s
	var _ tools.MinisterConsultant = s
	var _ tools.RitualLauncher = s
}

// TestCourt_StartRitual_EmitsEvent verifies that Court.StartRitual publishes
// a ritual_enacted event through the event system.
func TestCourt_StartRitual_EmitsEvent(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	key := storage.EdictKey{ID: 42, Username: cfg.Username, Project: cfg.Project}
	err := s.StartRitual("test-ritual", key, map[string]string{"foo": "bar"})
	require.NoError(t, err)

	// Verify event was published to the Tian ledger
	var events []storage.TianEvent
	require.NoError(t, db.Find(&events, "edict_id = ? AND event_type = ?", key.ID, storage.EventRitualEnacted).Error)
	require.Len(t, events, 1, "expected one ritual_enacted event")
}

// TestCourt_CheckHostCommand_NoConfig verifies that CheckHostCommand returns
// (false, false) when no session config is set (before ConfigureModel).
func TestCourt_CheckHostCommand_NoConfig(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	runOnHost, needsApproval := s.CheckHostCommand("gh issue list")
	assert.False(t, runOnHost)
	assert.False(t, needsApproval)
}

// TestCourt_CheckHostCommand_IsolatedHost verifies that in isolated-host
// mode, every command runs on host without approval.
func TestCourt_CheckHostCommand_IsolatedHost(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	cfg.IsolatedHost = true
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	runOnHost, needsApproval := s.CheckHostCommand("git status")
	assert.True(t, runOnHost, "isolated-host should route to host")
	assert.False(t, needsApproval, "isolated-host should not require approval")
}

// TestCourt_AppendToIntent_InvalidatesSeals verifies that appending to an
// edict's intent invalidates all existing seals, since they were earned
// against the old intent (edict 683 seal-invalidation feature).
func TestCourt_AppendToIntent_InvalidatesSeals(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	cfg.Username = "testuser"
	cfg.Project = "testproject"
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	// Create an edict and grant judge + chancellor seals
	edict := storage.Edict{Intent: "original intent", Username: "testuser", Project: "testproject"}
	require.NoError(t, db.Create(&edict).Error)

	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}
	sealSvc := s.GetSealService()
	require.NoError(t, sealSvc.GrantSeal(key, "judge", storage.JSON{}))
	require.NoError(t, sealSvc.GrantSeal(key, "chancellor", storage.JSON{}))

	// Confirm seals exist
	hasJudge, err := sealSvc.HasSeal(key, "judge")
	require.NoError(t, err)
	require.True(t, hasJudge, "judge seal should exist before appendToIntent")

	// AppendToIntent should invalidate all seals
	require.NoError(t, s.AppendToIntent(edict.ID, "additional clarification"))

	// Seals should now be stale
	hasJudge, err = sealSvc.HasSeal(key, "judge")
	require.NoError(t, err)
	require.False(t, hasJudge, "judge seal should be stale after appendToIntent")

	hasChancellor, err := sealSvc.HasSeal(key, "chancellor")
	require.NoError(t, err)
	require.False(t, hasChancellor, "chancellor seal should be stale after appendToIntent")

	// Edict status should be active again (no valid seals)
	status, err := sealSvc.GetEdictStatus(key)
	require.NoError(t, err)
	assert.Equal(t, storage.EdictActive, status, "edict should be active after seal invalidation")
}

// TestCourt_DeliverZhengmingAnswer verifies the Court-owned zhengming dispatch:
// WaitForZhengming blocks until DeliverZhengmingAnswer is called.
func TestCourt_DeliverZhengmingAnswer(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start waiting in a goroutine
	done := make(chan string, 1)
	go func() {
		answer, err := s.WaitForZhengming(ctx, "test-request-1")
		if err != nil {
			done <- ""
			return
		}
		done <- answer
	}()

	// Give the goroutine time to register
	time.Sleep(50 * time.Millisecond)

	// Deliver the answer
	delivered := s.DeliverZhengmingAnswer(ZhengmingAnswer{
		RequestID: "test-request-1",
		Answer:    "yes",
	})
	assert.True(t, delivered, "answer should be delivered to waiting caller")

	select {
	case answer := <-done:
		assert.Equal(t, "yes", answer)
	case <-time.After(time.Second):
		t.Fatal("WaitForZhengming should have returned")
	}
}

// TestCourt_DeliverZhengmingAnswer_NoWaiter verifies that DeliverZhengmingAnswer
// returns false when no one is waiting for the answer.
func TestCourt_DeliverZhengmingAnswer_NoWaiter(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	delivered := s.DeliverZhengmingAnswer(ZhengmingAnswer{
		RequestID: "no-waiter",
		Answer:    "yes",
	})
	assert.False(t, delivered, "should return false when no one is waiting")
}

// TestCourt_WaitForZhengming_Cancel verifies that WaitForZhengming returns
// an error when the context is cancelled.
func TestCourt_WaitForZhengming_Cancel(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.WaitForZhengming(ctx, "test-request-cancel")
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.Error(t, err, "should return error on context cancel")
	case <-time.After(time.Second):
		t.Fatal("WaitForZhengming should have returned after cancel")
	}
}

// updateProjectRootTools re-registers the shell tool with the stored
// hostChecker (and msgChan) rather than nil values.
func TestUpdateProjectRootTools_PreservesHostChecker(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()

	runner := &msgForwardingRunner{}
	s := NewCourt(db, cfg, runner, nil)
	require.NotNil(t, s)

	// Set up a msgChan via Subscribe (which calls SetRunnerMessageChannel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = s.Subscribe(ctx)
	require.NotNil(t, s.msgChan, "msgChan should be set after Subscribe")

	// ConfigureModel to wire hostChecker
	s.ConfigureModel(nil, &SessionConfig{
		Sandbox: config.SandboxConfig{
			RunOnHost: []string{"^docker "},
		},
	}, repo.RepoInfo{})
	require.NotNil(t, s.hostChecker)

	// Call updateProjectRootTools and verify the shell tool has the hostChecker
	s.updateProjectRootTools("/tmp")

	// Get the registered shell tool from the registry
	forgePerm, _ := tools.ParsePermissions("rwxr---w-")
	ts := s.toolRegistry.ForPermissions(forgePerm)
	var shellTool *tools.RunShellCommand
	for _, t := range ts {
		if st, ok := t.(*tools.RunShellCommand); ok {
			shellTool = st
			break
		}
	}
	require.NotNil(t, shellTool, "run_shell_command should be registered after updateProjectRootTools")

	// Verify the shell tool's internal shouldRunOnHost field is non-nil.
	// This is the critical check: updateProjectRootTools must pass s.hostChecker
	// (not nil) when constructing the RunShellCommand. We use reflection because
	// shouldRunOnHost is an unexported field on tools.RunShellCommand.
	shellVal := reflect.ValueOf(shellTool).Elem()
	shouldRunOnHost := shellVal.FieldByName("shouldRunOnHost")
	require.True(t, shouldRunOnHost.IsValid(), "RunShellCommand should have shouldRunOnHost field")
	assert.False(t, shouldRunOnHost.IsNil(), "shell tool's shouldRunOnHost must be non-nil after updateProjectRootTools")

	// Verify the shell tool's internal msgChan pointer is non-nil and
	// dereferences to a non-nil channel. With the pointer approach,
	// updateProjectRootTools passes &s.msgChan — after Subscribe sets
	// s.msgChan, the tool should see the non-nil channel.
	msgChanField := shellVal.FieldByName("msgChan")
	require.True(t, msgChanField.IsValid(), "RunShellCommand should have msgChan field")
	assert.False(t, msgChanField.IsNil(), "shell tool's msgChan pointer must be non-nil after updateProjectRootTools")
	derefChan := msgChanField.Elem()
	assert.False(t, derefChan.IsNil(), "shell tool's *msgChan must point to a non-nil channel after Subscribe")
}

// TestRequestZhengming_SetsSessionID verifies that RequestZhengming records
// the minister's current session ID on the zhengming record. This is the root
// fix for edict 667: every zhengming record should carry the session of the
// minister that created it.
func TestRequestZhengming_SetsSessionID(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	// Wire minister lookup so RequestZhengming can resolve callerMinisterID
	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface{ SetMinisterLookup(func(string) Minister) }); ok {
			base.SetMinisterLookup(s.GetMinister)
		}
	}

	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)

	// Create and attach a session to the chancellor
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "secretary")
	require.NoError(t, err)
	if base, ok := chancellor.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(sess)
	}

	// Call RequestZhengming via the Court (implements ZhengmingRequester)
	var zr tools.ZhengmingRequester = s

	key := storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Should we proceed?",
		Summary: "proceed check",
		Options: []string{tools.AnswerApproveEdict, tools.AnswerReject},
	}}
	requestID, err := zr.RequestZhengming(context.Background(), key, questions, storage.PriorityNormal, "secretary")
	require.NoError(t, err)

	// Verify the zhengming record has the session ID
	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Equal(t, sess.ID, req.SessionID, "zhengming record should carry the minister's session ID")
}

// TestRequestZhengming_NilSessionLeavesSessionIDEmpty verifies that when no
// session is active, the zhengming record gets an empty session ID (not a
// panic or error).
func TestRequestZhengming_NilSessionLeavesSessionIDEmpty(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)

	// No session attached — Session() returns nil

	var zr tools.ZhengmingRequester = s

	key := storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Should we proceed?",
		Summary: "proceed check",
		Options: []string{tools.AnswerApproveEdict, tools.AnswerReject},
	}}
	requestID, err := zr.RequestZhengming(context.Background(), key, questions, storage.PriorityNormal, "secretary")
	require.NoError(t, err)

	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Empty(t, req.SessionID, "zhengming record should have empty session ID when no session is active")
}

// TestRequestZhengming_UsesCallingMinisterSessionID verifies that when a non-chancellor
// minister's session is active and the chancellor calls RequestZhengming with that
// minister's ID as callerMinisterID, the zhengming record carries the calling minister's
// session ID, not the chancellor's. (edict 674)
func TestRequestZhengming_UsesCallingMinisterSessionID(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	// Wire minister lookup so RequestZhengming can resolve callerMinisterID
	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface{ SetMinisterLookup(func(string) Minister) }); ok {
			base.SetMinisterLookup(s.GetMinister)
		}
	}

	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)

	sage := s.GetMinister("chancellor")
	require.NotNil(t, sage)

	// Attach a session to the chancellor
	mockLLM := mocks.NewLLMProvider()
	chancellorSess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "secretary")
	require.NoError(t, err)
	if base, ok := chancellor.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(chancellorSess)
	}

	// Attach a DIFFERENT session to the sage
	sageSess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "chancellor")
	require.NoError(t, err)
	if base, ok := sage.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(sageSess)
	}

	// The chancellor calls RequestZhengming on behalf of "chancellor"
	var zr tools.ZhengmingRequester = s

	key := storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Should we proceed?",
		Summary: "proceed check",
		Options: []string{tools.AnswerApproveEdict, tools.AnswerReject},
	}}
	requestID, err := zr.RequestZhengming(context.Background(), key, questions, storage.PriorityNormal, "chancellor")
	require.NoError(t, err)

	// The zhengming record should carry the SAGE's session ID, not the chancellor's
	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Equal(t, sageSess.ID, req.SessionID,
		"zhengming record should carry the calling minister's (sage) session ID, not the chancellor's")
	assert.NotEqual(t, chancellorSess.ID, req.SessionID,
		"zhengming record must NOT carry the chancellor's session ID")
}

// TestRequestZhengming_EmptySessionIDWhenMinisterLookupNil verifies that when
// getMinister is nil (lookup not wired), SessionID is empty string rather
// than falling back to the requester's (chancellor's) session. (edict 674)
func TestRequestZhengming_EmptySessionIDWhenMinisterLookupNil(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)

	// Attach a session to the chancellor — this is the "wrong" session
	// that the old code would have used as fallback
	mockLLM := mocks.NewLLMProvider()
	chancellorSess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "secretary")
	require.NoError(t, err)
	if base, ok := chancellor.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(chancellorSess)
	}

	// Note: SetMinisterLookup is NOT called — getMinister is nil

	var zr tools.ZhengmingRequester = s

	key := storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Should we proceed?",
		Summary: "proceed check",
		Options: []string{tools.AnswerApproveEdict, tools.AnswerReject},
	}}
	// Caller is "chancellor" but getMinister is nil, so lookup fails
	requestID, err := zr.RequestZhengming(context.Background(), key, questions, storage.PriorityNormal, "chancellor")
	require.NoError(t, err)

	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Empty(t, req.SessionID,
		"zhengming SessionID should be empty when minister lookup is nil, not the chancellor's session")
	assert.NotEqual(t, chancellorSess.ID, req.SessionID,
		"zhengming SessionID must NOT fall back to the chancellor's session")
}

// TestRequestZhengming_EmptySessionIDWhenMinisterNotFound verifies that when
// getMinister returns nil for the callerMinisterID (minister not registered),
// SessionID is empty string. (edict 674)
func TestRequestZhengming_EmptySessionIDWhenMinisterNotFound(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	// Wire minister lookup — but "nonexistent" minister doesn't exist
	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface{ SetMinisterLookup(func(string) Minister) }); ok {
			base.SetMinisterLookup(s.GetMinister)
		}
	}

	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)

	// Chancellor has a session, but the caller ("nonexistent") does not
	mockLLM := mocks.NewLLMProvider()
	chancellorSess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "secretary")
	require.NoError(t, err)
	if base, ok := chancellor.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(chancellorSess)
	}

	var zr tools.ZhengmingRequester = s

	key := storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Should we proceed?",
		Summary: "proceed check",
		Options: []string{tools.AnswerApproveEdict, tools.AnswerReject},
	}}
	requestID, err := zr.RequestZhengming(context.Background(), key, questions, storage.PriorityNormal, "nonexistent")
	require.NoError(t, err)

	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Empty(t, req.SessionID,
		"zhengming SessionID should be empty when calling minister is not found")
}

// TestRequestZhengming_EmptySessionIDWhenMinisterHasNoSession verifies that
// when the calling minister exists but has no active session, SessionID is
// empty string. (edict 674)
func TestRequestZhengming_EmptySessionIDWhenMinisterHasNoSession(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	// Wire minister lookup
	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface{ SetMinisterLookup(func(string) Minister) }); ok {
			base.SetMinisterLookup(s.GetMinister)
		}
	}

	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)

	sage := s.GetMinister("chancellor")
	require.NotNil(t, sage)

	// Chancellor has a session, sage does NOT
	mockLLM := mocks.NewLLMProvider()
	chancellorSess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "secretary")
	require.NoError(t, err)
	if base, ok := chancellor.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(chancellorSess)
	}

	var zr tools.ZhengmingRequester = s

	key := storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Should we proceed?",
		Summary: "proceed check",
		Options: []string{tools.AnswerApproveEdict, tools.AnswerReject},
	}}
	// "chancellor" exists but has no session → SessionID should be empty
	requestID, err := zr.RequestZhengming(context.Background(), key, questions, storage.PriorityNormal, "chancellor")
	require.NoError(t, err)

	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Empty(t, req.SessionID,
		"zhengming SessionID should be empty when calling minister has no session")
	assert.NotEqual(t, chancellorSess.ID, req.SessionID,
		"zhengming SessionID must NOT fall back to the chancellor's session")
}

// TestZhengmingAnswered_NonSentinelCreatesEdictWithSessionID verifies that
// the system path (key.ID == 0, non-approve answer) creates an edict linked
// to the zhengming's session_id, not hardcoded "".
func TestZhengmingAnswered_NonSentinelCreatesEdictWithSessionID(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	go s.ritualGuard.Run(ctx)

	// Store a zhengming request with a known session_id
	sessionID := "test-session-667"
	req := storage.Zhengming{
		RequestID:  "test-session-edict-1",
		EdictID:    0,
		Username:   cfg.Username,
		Project:    cfg.Project,
		MinisterID: "secretary",
		SessionID:  sessionID,
		Questions:  storage.ZhengmingQuestions{{Text: "?", Summary: "?", Options: []string{"A", "B"}}},
		Status:     storage.ZhengmingPending,
		Priority:   storage.PriorityNormal,
	}
	require.NoError(t, db.Create(&req).Error)

	customAnswer := "Build something new"
	s.PublishEvent(storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project},
		storage.EventZhengmingAnswered, storage.JSON{
			"request_id": "test-session-edict-1",
			"answer":     customAnswer,
		})

	// Poll for the edict to appear (async handler)
	var edict storage.Edict
	require.Eventually(t, func() bool {
		return db.Where("intent = ?", customAnswer).First(&edict).Error == nil
	}, 2*time.Second, 50*time.Millisecond, "edict should be created from the zhengming answer")

	assert.Equal(t, sessionID, edict.SessionID, "edict should carry the zhengming's session_id")
}

// ---------------------------------------------------------------------------
// Per-channel session tests (edict 676)
// ---------------------------------------------------------------------------

// TestSessionForTab_RitualTabFindsSessionOnMinister verifies that sessionForTab
// resolves a ritual/edict tab target (e.g. "e633") by scanning all ministers
// for a session keyed by that channel — the core fix for the per-channel
// session isolation bug in edict 676.
func TestSessionForTab_RitualTabFindsSessionOnMinister(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	mockLLM := mocks.NewLLMProvider()

	// Create a session on the sage minister under channel "e633"
	ritualSess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "", "e633")
	require.NoError(t, err)

	sage := court.GetMinister("chancellor")
	require.NotNil(t, sage)
	if base, ok := sage.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(ritualSess, "e633")
	}

	// sessionForTab("e633") should find the session on the sage minister
	sess := court.sessionForTab("e633")
	require.NotNil(t, sess, "sessionForTab should find ritual session by scanning all ministers")
	assert.Equal(t, ritualSess.ID, sess.ID)

	// Also verify SessionState routes through sessionForTab
	state := court.SessionState("e633")
	assert.True(t, state.Exists, "SessionState should find the ritual session")
	assert.Equal(t, "e633", state.ChannelID)
}

// TestSessionForTab_InteractiveTabUsesDirectLookup verifies that for
// interactive tabs (e.g. "chancellor"), sessionForTab uses the direct minister
// lookup path and returns that minister's session keyed by its own ID.
func TestSessionForTab_InteractiveTabUsesDirectLookup(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	mockLLM := mocks.NewLLMProvider()
	interactiveSess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "", "chancellor")
	require.NoError(t, err)

	sage := court.GetMinister("chancellor")
	require.NotNil(t, sage)
	if base, ok := sage.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(interactiveSess)
	}

	sess := court.sessionForTab("chancellor")
	require.NotNil(t, sess)
	assert.Equal(t, interactiveSess.ID, sess.ID)
}

// TestSessionForTab_RitualTabDoesNotFindInteractiveSession verifies that
// a ritual tab target does not accidentally return the minister's
// interactive session (stored under the minister's own ID key).
func TestSessionForTab_RitualTabDoesNotFindInteractiveSession(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	mockLLM := mocks.NewLLMProvider()
	interactiveSess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "", "chancellor")
	require.NoError(t, err)

	sage := court.GetMinister("chancellor")
	require.NotNil(t, sage)
	if base, ok := sage.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(interactiveSess) // stored under "chancellor" key
	}

	// "e633" is a ritual tab — should return nil since no session is keyed "e633"
	sess := court.sessionForTab("e633")
	assert.Nil(t, sess, "ritual tab should not find the interactive session")
}

// TestClearAllSchedulers_MultipleSessionsPerMinister verifies that
// clearAllSchedulers iterates all sessions across all channels on a
// single minister, not just the default interactive session.
func TestClearAllSchedulers_MultipleSessionsPerMinister(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	court := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	doneA := make(chan struct{})
	doneB := make(chan struct{})
	defer close(doneA)
	defer close(doneB)

	sched1 := runners.NewCoreToolScheduler(nil)
	sched1.Schedule(context.Background(), &blockingTool{name: "tool_a", done: doneA}, `{}`)
	sched2 := runners.NewCoreToolScheduler(nil)
	sched2.Schedule(context.Background(), &blockingTool{name: "tool_b", done: doneB}, `{}`)

	mockLLM := mocks.NewLLMProvider()

	// Create two sessions on the SAME minister (chancellor) under different channel IDs
	sess1, err := NewSession(mockLLM, &SessionConfig{}, nil, sched1, nil, "", "secretary")
	require.NoError(t, err)
	sess2, err := NewSession(mockLLM, &SessionConfig{}, nil, sched2, nil, "", "e633")
	require.NoError(t, err)

	chancellor := court.GetMinister("secretary")
	require.NotNil(t, chancellor)
	if base, ok := chancellor.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(sess1)         // interactive session under "secretary" key
		base.SetSession(sess2, "e633") // ritual session under "e633" key
	}

	count := court.clearAllSchedulers()
	assert.Equal(t, 2, count, "should abort 1+1 = 2 queued items across two sessions on the same minister")
}

// ---------------------------------------------------------------------------
// Court-owned runtime-dispatch tests (edict 678)
// ---------------------------------------------------------------------------

// TestCourt_RequestZhengming_CreatesDBRecord verifies that Court.RequestZhengming
// (the new implementation in tool_dispatch.go) creates a zhengming record in
// the DB with the correct fields.
func TestCourt_RequestZhengming_CreatesDBRecord(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	key := storage.EdictKey{ID: 42, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Which approach?",
		Summary: "approach check",
		Options: []string{"Option A", "Option B"},
	}}

	requestID, err := s.RequestZhengming(context.Background(), key, questions, storage.PriorityNormal, "chancellor")
	require.NoError(t, err)
	assert.NotEmpty(t, requestID)

	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Equal(t, uint(42), req.EdictID)
	assert.Equal(t, cfg.Username, req.Username)
	assert.Equal(t, cfg.Project, req.Project)
	assert.Equal(t, "chancellor", req.MinisterID)
	assert.Equal(t, storage.ZhengmingPending, req.Status)
	assert.Equal(t, storage.PriorityNormal, req.Priority)
}

// TestCourt_RequestZhengming_UrgentPrioritySetsShorterTimeout verifies that
// urgent priority sets a 1-hour timeout instead of the default 24-hour.
func TestCourt_RequestZhengming_UrgentPrioritySetsShorterTimeout(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	key := storage.EdictKey{ID: 42, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Urgent question?",
		Summary: "urgent",
		Options: []string{"A", "B"},
	}}

	requestID, err := s.RequestZhengming(context.Background(), key, questions, storage.PriorityUrgent, "chancellor")
	require.NoError(t, err)

	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Equal(t, storage.PriorityUrgent, req.Priority)

	// Urgent timeout should be ~1 hour, not ~24 hours
	remaining := time.Until(req.TimeoutAt)
	assert.Less(t, remaining, 2*time.Hour, "urgent timeout should be ~1 hour")
	assert.Greater(t, remaining, 30*time.Minute, "urgent timeout should not be too short")
}

// TestCourt_RequestZhengming_EmitsEvent verifies that Court.RequestZhengming
// publishes a zhengming_requested event to the Tian ledger.
func TestCourt_RequestZhengming_EmitsEvent(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	key := storage.EdictKey{ID: 42, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Which approach?",
		Summary: "approach check",
		Options: []string{"Option A", "Option B"},
	}}

	requestID, err := s.RequestZhengming(context.Background(), key, questions, storage.PriorityNormal, "chancellor")
	require.NoError(t, err)

	var events []storage.TianEvent
	require.NoError(t, db.Find(&events, "edict_id = ? AND event_type = ?", key.ID, "zhengming_requested").Error)
	require.Len(t, events, 1, "expected one zhengming_requested event")

	// Verify event payload contains the request_id and minister_id
	assert.Equal(t, requestID, events[0].Payload["request_id"])
	assert.Equal(t, "chancellor", events[0].Payload["minister_id"])
}

// TestCourt_RequestZhengming_FiresCallbackOnCaller verifies that
// Court.RequestZhengming fires the onZhengmingRaised callback on the
// CALLING minister (not the chancellor).
func TestCourt_RequestZhengming_FiresCallbackOnCaller(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	sageRaised := false
	sage := s.GetMinister("chancellor")
	require.NotNil(t, sage)

	if base, ok := sage.(interface{ SetOnZhengmingRaised(func()) }); ok {
		base.SetOnZhengmingRaised(func() { sageRaised = true })
	}

	chancellorRaised := false
	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)
	if base, ok := chancellor.(interface{ SetOnZhengmingRaised(func()) }); ok {
		base.SetOnZhengmingRaised(func() { chancellorRaised = true })
	}

	key := storage.EdictKey{ID: 42, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Which approach?",
		Summary: "approach check",
		Options: []string{"Option A", "Option B"},
	}}

	_, err := s.RequestZhengming(context.Background(), key, questions, storage.PriorityNormal, "chancellor")
	require.NoError(t, err)

	assert.True(t, sageRaised, "onZhengmingRaised should fire on the calling minister (sage)")
	assert.False(t, chancellorRaised, "onZhengmingRaised should NOT fire on the chancellor")
}

// TestRequestZhengming_ContextSessionIDOverridesMinisterLookup verifies that
// when the context carries a session ID, RequestZhengming uses it even when
// callerMinisterID is empty or points to a minister with no interactive
// session. This is the core fix for edict 717: the session ID from context
// (the session actually executing the tool) takes priority over the
// minister-lookup path.
func TestRequestZhengming_ContextSessionIDOverridesMinisterLookup(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	// Wire minister lookup so the old fallback path *could* find a minister
	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface{ SetMinisterLookup(func(string) Minister) }); ok {
			base.SetMinisterLookup(s.GetMinister)
		}
	}

	// Attach a session to the secretary — this is the "wrong" session the
	// old fallback would return if callerMinisterID were "secretary".
	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)
	mockLLM := mocks.NewLLMProvider()
	chancellorSess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "secretary")
	require.NoError(t, err)
	if base, ok := chancellor.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(chancellorSess)
	}

	// Inject a DIFFERENT session ID via context — the one actually executing
	// the tool (e.g. a ritual session "e717").
	ritualSessionID := "ritual-session-e717"
	ctxWithSession := context.WithValue(ctx, tools.SessionIDKey{}, ritualSessionID)

	key := storage.EdictKey{ID: 42, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{
		Text:    "Approve?",
		Summary: "approval check",
		Options: []string{"Yes", "No"},
	}}

	// callerMinisterID is "" — no minister to look up, but context has the ID
	requestID, err := s.RequestZhengming(ctxWithSession, key, questions, storage.PriorityNormal, "")
	require.NoError(t, err)

	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Equal(t, ritualSessionID, req.SessionID,
		"zhengming should use the session ID from context, not the minister lookup")
	assert.NotEqual(t, chancellorSess.ID, req.SessionID,
		"zhengming must NOT use the chancellor's session")
}

// TestRequestZhengming_ContextSessionIDWithMinisterSession verifies that
// when both context and minister lookup would provide a session ID, the
// context value wins. (edict 717)
func TestRequestZhengming_ContextSessionIDWithMinisterSession(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface{ SetMinisterLookup(func(string) Minister) }); ok {
			base.SetMinisterLookup(s.GetMinister)
		}
	}

	// Attach a session to secretary
	secretary := s.GetMinister("secretary")
	require.NotNil(t, secretary)
	mockLLM := mocks.NewLLMProvider()
	secretarySess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "secretary")
	require.NoError(t, err)
	if base, ok := secretary.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(secretarySess)
	}

	// Context carries a different session ID
	ctxSessionID := "ctx-session-xyz"
	ctxWithSession := context.WithValue(ctx, tools.SessionIDKey{}, ctxSessionID)

	key := storage.EdictKey{ID: 1, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{Text: "OK?", Options: []string{"A", "B"}}}

	requestID, err := s.RequestZhengming(ctxWithSession, key, questions, storage.PriorityNormal, "secretary")
	require.NoError(t, err)

	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Equal(t, ctxSessionID, req.SessionID,
		"context session ID should take priority over minister lookup")
}

// TestRequestZhengming_FallbackToMinisterSessionWhenNoContextSession verifies
// that when context has no session ID, the old minister-lookup fallback is
// used. This ensures backward compatibility. (edict 717)
func TestRequestZhengming_FallbackToMinisterSessionWhenNoContextSession(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	for _, minister := range s.Ministers() {
		if base, ok := minister.(interface{ SetMinisterLookup(func(string) Minister) }); ok {
			base.SetMinisterLookup(s.GetMinister)
		}
	}

	secretary := s.GetMinister("secretary")
	require.NotNil(t, secretary)
	mockLLM := mocks.NewLLMProvider()
	secretarySess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "secretary")
	require.NoError(t, err)
	if base, ok := secretary.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(secretarySess)
	}

	// No session ID in context
	key := storage.EdictKey{ID: 1, Username: cfg.Username, Project: cfg.Project}
	questions := storage.ZhengmingQuestions{{Text: "OK?", Options: []string{"A", "B"}}}

	requestID, err := s.RequestZhengming(context.Background(), key, questions, storage.PriorityNormal, "secretary")
	require.NoError(t, err)

	var req storage.Zhengming
	require.NoError(t, db.First(&req, "request_id = ?", requestID).Error)
	assert.Equal(t, secretarySess.ID, req.SessionID,
		"should fall back to minister's session when context has no session ID")
}

// TestCourt_ConsultMinister_NotFound verifies that Court.ConsultMinister
// returns an error when the requested minister doesn't exist.
func TestCourt_ConsultMinister_NotFound(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	key := storage.EdictKey{ID: 1, Username: cfg.Username, Project: cfg.Project}
	_, err := s.ConsultMinister(context.Background(), "secretary", "nonexistent", "secretary", key, "do something")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minister not found")
}

// TestCourt_ConsultMinister_SelfConsultationRejected verifies that
// Court.ConsultMinister rejects self-consultation as a defense-in-depth guard.
func TestCourt_ConsultMinister_SelfConsultationRejected(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	key := storage.EdictKey{ID: 1, Username: cfg.Username, Project: cfg.Project}
	_, err := s.ConsultMinister(context.Background(), "forge", "forge", "forge", key, "do work")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot consult itself")
}

// TestCourt_CheckHostCommand_SafeRunOnHost verifies that a command matching
// SafeRunOnHost returns (true, false) — run on host without approval.
func TestCourt_CheckHostCommand_SafeRunOnHost(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	s.ConfigureModel(nil, &SessionConfig{
		Sandbox: config.SandboxConfig{
			RunOnHost:     []string{"^docker "},
			SafeRunOnHost: []string{"^gh "},
		},
	}, repo.RepoInfo{})

	// SafeRunOnHost match — no approval needed
	runOnHost, needsApproval := s.CheckHostCommand("gh issue list")
	assert.True(t, runOnHost, "SafeRunOnHost match should return runOnHost=true")
	assert.False(t, needsApproval, "SafeRunOnHost match should return needsApproval=false")

	// RunOnHost match — approval needed
	runOnHost, needsApproval = s.CheckHostCommand("docker build .")
	assert.True(t, runOnHost, "RunOnHost match should return runOnHost=true")
	assert.True(t, needsApproval, "RunOnHost match should return needsApproval=true")

	// No match — sandbox
	runOnHost, needsApproval = s.CheckHostCommand("ls -la")
	assert.False(t, runOnHost, "non-matching command should return runOnHost=false")
	assert.False(t, needsApproval, "non-matching command should return needsApproval=false")
}

// TestCourt_CancelZhengmingDispatch verifies that CancelZhengmingDispatch
// removes a pending wait, causing a blocked WaitForZhengming to return
// without receiving an answer.
func TestCourt_CancelZhengmingDispatch(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := s.WaitForZhengming(ctx, "cancel-test")
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)

	// Cancel the dispatch — the goroutine should NOT return yet because
	// the channel is deleted but the select is still waiting on ctx.Done().
	// Only DeliverZhengmingAnswer or ctx cancel unblocks it.
	s.CancelZhengmingDispatch("cancel-test")

	// DeliverZhengmingAnswer should now return false (no waiter)
	delivered := s.DeliverZhengmingAnswer(ZhengmingAnswer{
		RequestID: "cancel-test",
		Answer:    "too late",
	})
	assert.False(t, delivered, "should return false after CancelZhengmingDispatch")

	// Cancel context to unblock the goroutine
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("WaitForZhengming should have returned after cancel")
	}
}

// TestCourt_TianLedgerToolWiresOutputLimit guards the court-level config
// path for edict 844: the truncation length set on CourtConfig must reach
// the tian_ledger tool's ToolContext. Without this, the tool would
// silently fall back to the default and a mis-wired court would ship.
func TestCourt_TianLedgerToolWiresOutputLimit(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	cfg.OutputLimit = 1234
	s := NewCourt(db, cfg, nil, nil)

	registry := s.GetToolRegistry()
	require.NotNil(t, registry, "court should build a tool registry")

	extras := registry.ExtraTools("", []string{"tian_ledger"})
	require.Len(t, extras, 1, "tian_ledger must be registered as an extra tool")

	tool, ok := extras[0].(tools.TianLedgerTool)
	require.True(t, ok, "tian_ledger must be a TianLedgerTool, got %T", extras[0])
	assert.Equal(t, 1234, tool.Ctx.OutputLimit,
		"tian_ledger must receive the court-level truncation limit")

	// The documented default must be non-zero, otherwise summary mode would
	// return an unbounded body and reintroduce the transport overflow.
	assert.Equal(t, config.DefaultOutputLimit, config.DefaultCourtConfig().OutputLimit)
	assert.NotZero(t, config.DefaultOutputLimit)
}

// TestExtractDetail_MirrorsProductionKeys verifies the snapshot's twin
// projection derives detail from the keys production publishers actually
// write (ritual → step → minister_id → intent), and returns empty for
// unrecognised payloads rather than guessing.
func TestExtractDetail_MirrorsProductionKeys(t *testing.T) {
	cases := []struct {
		name    string
		payload storage.JSON
		want    string
	}{
		{"ritual key", storage.JSON{"ritual": "swift-strike", "execution_id": 1}, "swift-strike"},
		{"step key", storage.JSON{"ritual": "", "step": "forge", "step_index": 0}, "forge"},
		{"minister_id key", storage.JSON{"minister_id": "judge", "notes": "ok"}, "judge"},
		{"intent key", storage.JSON{"intent": "fix it", "id": 5}, "fix it"},
		{"ritual wins over step", storage.JSON{"ritual": "swift-strike", "step": "forge"}, "swift-strike"},
		{"unknown keys yield empty", storage.JSON{"unrelated": "value"}, ""},
		{"nil payload yields empty", nil, ""},
		{"empty string is skipped", storage.JSON{"ritual": "", "step": "forge"}, "forge"},
		{"huge trailing body does not affect detail", storage.JSON{"intent": "fix it", "last_step_output": strings.Repeat("X", 5000)}, "fix it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := storage.TianEvent{Payload: tc.payload}
			if got := extractDetail(ev); got != tc.want {
				t.Errorf("extractDetail() = %q, want %q", got, tc.want)
			}
		})
	}
}
