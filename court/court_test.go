package court

import (
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"reflect"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/mocks"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/internal/worktree"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
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

	// SQLite works best with a single connection: concurrent writers on
	// separate pool connections race file locks, and PRAGMA busy_timeout is
	// per-connection, so fresh pool connections would fail instantly with
	// "database is locked". Production (storage/db.go) caps the pool at 1 —
	// mirror that here so the DAG engine's concurrent ling runners serialize
	// through one connection instead of flaking on SQLITE_BUSY.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	// Enable WAL mode and busy_timeout for belt-and-braces robustness — the
	// ritual guard runs concurrently and performs writes, so without these
	// PRAGMAs SQLite returns "database is locked" errors.
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

// namedMsgRunner is a msgForwardingRunner with an identity so ClearSchedulerMsg
// scoping can be exercised (edict 957).
type namedMsgRunner struct {
	msgForwardingRunner
	name string
}

func (r *namedMsgRunner) RunnerName() string { return r.name }

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

// TestClearSchedulersForRunner_ScopesToRunner is the edict 957 regression
// test: a ClearSchedulerMsg carrying a runner identity must abort only the
// sessions bound to that runner, leaving other runners' in-flight calls alive.
// The bare message still aborts every session.
func TestClearSchedulersForRunner_ScopesToRunner(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()

	runnerA := &namedMsgRunner{name: "A"}
	court := NewCourt(db, cfg, runnerA, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	// Edict 42 has its own runner "B".
	court.edictRunnersMu.Lock()
	court.edictRunners[42] = &namedMsgRunner{name: "B"}
	court.edictRunnersMu.Unlock()

	doneA := make(chan struct{})
	doneB := make(chan struct{})
	defer close(doneA)
	defer close(doneB)

	schedA := runners.NewCoreToolScheduler(nil)
	resA := schedA.Schedule(context.Background(), &blockingTool{name: "tool_a", done: doneA}, `{}`)

	schedB := runners.NewCoreToolScheduler(nil)
	resB := schedB.Schedule(context.Background(), &blockingTool{name: "tool_b", done: doneB}, `{}`)

	mockLLM := mocks.NewLLMProvider()
	// Interactive session, bound to the Court-wide runner "A".
	sessA, err := NewSession(mockLLM, &SessionConfig{}, nil, schedA, nil, "", "secretary")
	require.NoError(t, err)
	// Ritual session e42, bound to edict 42's runner "B".
	sessB, err := NewSession(mockLLM, &SessionConfig{}, nil, schedB, nil, "", "e42")
	require.NoError(t, err)

	sec := court.GetMinister("secretary")
	require.NotNil(t, sec)
	if base, ok := sec.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(sessA)
	}
	forge := court.GetMinister("forge")
	require.NotNil(t, forge)
	if base, ok := forge.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(sessB, "e42")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := court.Subscribe(ctx)
	require.NotNil(t, out)
	require.NotNil(t, runnerA.msgChan, "Subscribe should set the runner msg channel")

	// Scoped clear: only B aborted.
	resultChan := make(chan int, 1)
	runnerA.msgChan <- runners.ClearSchedulerMsg{RunnerName: "B", ResultChan: resultChan}
	select {
	case count := <-resultChan:
		assert.Equal(t, 1, count, "only B's in-flight call should be aborted")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scoped ClearSchedulerMsg result")
	}
	select {
	case res := <-resB:
		assert.Error(t, res.Error, "B's call must be aborted")
	case <-time.After(time.Second):
		t.Fatal("B's in-flight call should have been aborted")
	}
	select {
	case res := <-resA:
		t.Fatalf("A's in-flight call must survive a scoped clear, got %+v", res)
	default:
	}

	// The bare message (no identity) preserves the all-sessions abort.
	resultChan = make(chan int, 1)
	runnerA.msgChan <- runners.ClearSchedulerMsg{ResultChan: resultChan}
	select {
	case count := <-resultChan:
		assert.Equal(t, 1, count, "bare clear must abort A's remaining call")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bare ClearSchedulerMsg result")
	}
	select {
	case res := <-resA:
		assert.Error(t, res.Error, "A's call must be aborted by the legacy all-sessions clear")
	case <-time.After(time.Second):
		t.Fatal("A's in-flight call should have been aborted by the bare clear")
	}
}

// TestClearSchedulersForRunner_UnknownRunnerAbortsNothing guards against a
// scoped clear silently falling back to the legacy all-sessions behavior.
func TestClearSchedulersForRunner_UnknownRunnerAbortsNothing(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()

	court := NewCourt(db, cfg, &namedMsgRunner{name: "A"}, nil)
	require.NotNil(t, court)
	court.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{})

	done := make(chan struct{})
	defer close(done)
	sched := runners.NewCoreToolScheduler(nil)
	sched.Schedule(context.Background(), &blockingTool{name: "tool_a", done: done}, `{}`)

	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, sched, nil, "", "secretary")
	require.NoError(t, err)
	sec := court.GetMinister("secretary")
	require.NotNil(t, sec)
	if base, ok := sec.(interface{ SetSession(*Session, ...string) }); ok {
		base.SetSession(sess)
	}

	assert.Equal(t, 0, court.clearSchedulersForRunner("no-such-runner"),
		"an unknown runner identity must not abort any session")
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

// TestZhengmingAnswered_EdictSuggestionWithEdictID0TypedAnswerDoesNotCreateEdict
// is the regression test for the spurious edict 893 (incident
// 75f262766657b094): the Ruler typed free text while a suggest_edict
// zhengming (options: ["Approve edict", "Reject"]) was pending with
// edict_id=0 and minister_id="". That typed answer fell through the
// key.ID==0 catch-all and created an edict. Only an explicit "Approve edict"
// may create a suggestion edict.
func TestZhengmingAnswered_EdictSuggestionWithEdictID0TypedAnswerDoesNotCreateEdict(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()

	go s.ritualGuard.Run(ctx)

	// The Podman keep-alive suggestion: no edict, no raising minister.
	req := storage.Zhengming{
		RequestID: "test-typed-suggestion",
		EdictID:   0,
		Username:  cfg.Username,
		Project:   cfg.Project,
		Questions: storage.ZhengmingQuestions{{
			Text:    "Keep the Podman machine alive between sessions",
			Summary: "podman keep-alive",
			Options: []string{tools.AnswerApproveEdict, tools.AnswerReject},
		}},
		Status:   storage.ZhengmingPending,
		Priority: storage.PriorityNormal,
	}
	require.NoError(t, db.Create(&req).Error)

	typed := "an edict needs to be more decisive about the podman machine"
	s.PublishEvent(storage.EdictKey{ID: 0, Username: cfg.Username, Project: cfg.Project},
		storage.EventZhengmingAnswered, storage.JSON{
			"request_id": "test-typed-suggestion",
			"answer":     typed,
		})

	time.Sleep(120 * time.Millisecond)

	var edicts []storage.Edict
	err := db.Where("intent = ?", typed).Find(&edicts).Error
	require.NoError(t, err)
	assert.Empty(t, edicts, "a typed answer to an edict suggestion (edict_id=0) must not create an edict")

	// Sanity: the pre-existing catch-all still works for non-suggestion
	// requests (no stored request / no Approve-edict option).
	var none []storage.Edict
	require.NoError(t, db.Where("intent = ?", "isolation-legit").Find(&none).Error)
}

// TestHandleZhengmingResponse_RoutesToRaisingMinister verifies that an answer
// is recorded against the request's own zhengming row and delivered to the
// raising minister rather than the first handler-bearing minister. Before the
// fix, HandleZhengmingResponse returned the first minister implementing the
// handler, so a ritual ask_ruler answer could bind to an unrelated request.
func TestHandleZhengmingResponse_RoutesToRaisingMinister(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	// Register a zhengming request raised by "judge".
	req := storage.Zhengming{
		RequestID:  "route-to-raiser",
		EdictID:    0,
		Username:   cfg.Username,
		Project:    cfg.Project,
		MinisterID: "judge",
		Questions: storage.ZhengmingQuestions{{
			Text:    "Proceed?",
			Options: []string{"Yes", "No"},
		}},
		Status:   storage.ZhengmingPending,
		Priority: storage.PriorityNormal,
	}
	require.NoError(t, db.Create(&req).Error)

	require.NoError(t, s.HandleZhengmingResponse(context.Background(), "route-to-raiser", "Yes"))

	// The request must be answered exactly once.
	var answered storage.Zhengming
	require.NoError(t, db.First(&answered, "request_id = ?", "route-to-raiser").Error)
	assert.Equal(t, storage.ZhengmingAnswered, answered.Status)
	assert.Equal(t, "Yes", answered.Answer)
}

// TestHandleZhengmingResponse_NotFirstHandler verifies the dispatch actually
// targets the raising minister. A fake raising minister records the answer
// while the lookup order is arranged so a *different* minister would have been
// picked by the old "first handler" loop.
func TestHandleZhengmingResponse_NotFirstHandler(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	// Swap in synthetic ministers: "aaa_first" (would win the old loop) and
	// "zzz_raiser" (actually raised the request).
	first := &recordingZhengmingMinister{id: "aaa_first"}
	raiser := &recordingZhengmingMinister{id: "zzz_raiser"}
	s.ministers = map[string]Minister{
		"aaa_first":  first,
		"zzz_raiser": raiser,
	}

	req := storage.Zhengming{
		RequestID:  "not-first-handler",
		EdictID:    7,
		Username:   cfg.Username,
		Project:    cfg.Project,
		MinisterID: "zzz_raiser",
		Questions: storage.ZhengmingQuestions{{
			Text:    "Continue?",
			Options: []string{"Yes", "No"},
		}},
		Status:   storage.ZhengmingPending,
		Priority: storage.PriorityNormal,
	}
	require.NoError(t, db.Create(&req).Error)

	require.NoError(t, s.HandleZhengmingResponse(context.Background(), "not-first-handler", "Yes"))

	assert.Equal(t, []string{"not-first-handler=Yes"}, raiser.received,
		"the answer must go to the raising minister")
	assert.Empty(t, first.received,
		"the first handler-bearing minister must not receive the answer")
}

// recordingZhengmingMinister is a Minister that records HandleZhengmingResponse calls.
type recordingZhengmingMinister struct {
	MinisterBase
	id       string
	received []string
}

func (m *recordingZhengmingMinister) ID() string           { return m.id }
func (m *recordingZhengmingMinister) SystemPrompt() string { return "" }
func (m *recordingZhengmingMinister) Tools() []Tool        { return nil }
func (m *recordingZhengmingMinister) Run(ctx context.Context) {
	<-ctx.Done()
}
func (m *recordingZhengmingMinister) HandleZhengmingResponse(_ context.Context, requestID, answer string) error {
	m.received = append(m.received, requestID+"="+answer)
	return nil
}

// TestHandleZhengmingResponse_DeliversToWaitingRitual verifies that answering a
// request raised by a ritual step unblocks the waiting WaitForZhengming and
// does not abort the step.
func TestHandleZhengmingResponse_DeliversToWaitingRitual(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	req := storage.Zhengming{
		RequestID:  "ritual-wait",
		EdictID:    42,
		Username:   cfg.Username,
		Project:    cfg.Project,
		MinisterID: "secretary",
		SessionID:  "ritual-session",
		Questions: storage.ZhengmingQuestions{{
			Text:    "Continue?",
			Options: []string{"Yes", "No"},
		}},
		Status:   storage.ZhengmingPending,
		Priority: storage.PriorityUrgent,
	}
	require.NoError(t, db.Create(&req).Error)

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = ctx, cancel
	defer cancel()
	go s.ritualGuard.Run(ctx)

	got := make(chan string, 1)
	go func() {
		answer, err := s.WaitForZhengming(ctx, "ritual-wait")
		if err != nil {
			t.Logf("WaitForZhengming error: %v", err)
		}
		got <- answer
	}()
	// Let the waiter register.
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, s.HandleZhengmingResponse(ctx, "ritual-wait", "Yes"))

	select {
	case answer := <-got:
		assert.Equal(t, "Yes", answer, "the raised request must be delivered to the blocked step")
	case <-time.After(time.Second):
		t.Fatal("WaitForZhengming should have received the answer")
	}
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
// a ritual_enacted event through the event system for a registered ritual.
func TestCourt_StartRitual_EmitsEvent(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)
	require.NoError(t, s.GetRitualRegistry().Register(&RitualDef{Name: "test-ritual"}))

	key := storage.EdictKey{ID: 42, Username: cfg.Username, Project: cfg.Project}
	err := s.StartRitual("test-ritual", key, map[string]string{"foo": "bar"})
	require.NoError(t, err)

	// Verify event was published to the Tian ledger
	var events []storage.TianEvent
	require.NoError(t, db.Find(&events, "edict_id = ? AND event_type = ?", key.ID, storage.EventRitualEnacted).Error)
	require.Len(t, events, 1, "expected one ritual_enacted event")
}

// TestCourt_StartRitual_UnknownRitual verifies that Court.StartRitual rejects
// an unknown ritual name and publishes no ritual_enacted event.
func TestCourt_StartRitual_UnknownRitual(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	key := storage.EdictKey{ID: 43, Username: cfg.Username, Project: cfg.Project}
	err := s.StartRitual("no-such-ritual", key, map[string]string{"foo": "bar"})
	require.Error(t, err)

	// Verify no event was published to the Tian ledger
	var events []storage.TianEvent
	require.NoError(t, db.Find(&events, "edict_id = ? AND event_type = ?", key.ID, storage.EventRitualEnacted).Error)
	require.Empty(t, events, "unknown ritual must not emit a ritual_enacted event")
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

// TestCourt_GetEdictSealsExcludesStaleSeals verifies the Court's seal-chain
// accessor mirrors GetEdictStatus: seals invalidated by an intent change must
// not surface as granted, so the UI can re-seal the edict (edict 967).
func TestCourt_GetEdictSealsExcludesStaleSeals(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	cfg.Username = "testuser"
	cfg.Project = "testproject"
	s := NewCourt(db, cfg, nil, slog.Default())
	require.NotNil(t, s)

	edict := storage.Edict{Intent: "original intent", Username: "testuser", Project: "testproject"}
	require.NoError(t, db.Create(&edict).Error)

	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}
	sealSvc := s.GetSealService()
	require.NoError(t, sealSvc.GrantSeal(key, "judge", storage.JSON{}))
	require.NoError(t, sealSvc.GrantSeal(key, "chancellor", storage.JSON{}))
	require.NoError(t, sealSvc.GrantSeal(key, "ruler", storage.JSON{}))

	// Sanity: the live chain reaches the Court before the intent changes.
	seals, err := s.GetEdictSeals(key)
	require.NoError(t, err)
	require.Len(t, seals, 3)

	require.NoError(t, s.AppendToIntent(edict.ID, "additional clarification"))

	seals, err = s.GetEdictSeals(key)
	require.NoError(t, err)
	for _, seal := range seals {
		if seal.StaleAt != nil {
			t.Errorf("Court.GetEdictSeals returned stale seal for %s", seal.MinisterID)
		}
	}
	assert.Empty(t, seals, "all seals should be stale and excluded after appendToIntent")

	// The Court and the derived status agree: the edict is active, not sealed.
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

// TestSetNotify_WiresApproveDocTool verifies the fix for the permanently-nil
// approve_doc Notify: the tool is registered as a zero-value placeholder at
// construction, and SetNotify installs the live callback in the registry and
// in any already-open session's tool catalog.
func TestSetNotify_WiresApproveDocTool(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	// Create a live session on a minister before notify is installed.
	chancellor := s.GetMinister("secretary")
	require.NotNil(t, chancellor)
	mockLLM := mocks.NewLLMProvider()
	sess, err := NewSession(mockLLM, &SessionConfig{}, nil, nil, nil, "test", "secretary")
	require.NoError(t, err)
	base, ok := chancellor.(interface{ SetSession(*Session, ...string) })
	require.True(t, ok)
	base.SetSession(sess)

	// Before SetNotify the registered placeholder has a nil callback.
	before := findApproveDoc(t, s.toolRegistry)
	require.Nil(t, before.Notify, "placeholder registered before notify must have a nil callback")

	s.SetNotify(func(any) {})

	// Registry entry now carries the live callback.
	after := findApproveDoc(t, s.toolRegistry)
	require.NotNil(t, after.Notify, "SetNotify must install the live callback in the registry")

	// The already-open session's catalog was refreshed too.
	tool, ok := sess.toolCatalog["approve_doc"]
	require.True(t, ok, "live session should expose approve_doc")
	ad, ok := tool.(tools.ApproveDocTool)
	require.True(t, ok, "session catalog entry should be an ApproveDocTool")
	require.NotNil(t, ad.Notify, "live session's approve_doc must carry the callback")
}

// findApproveDoc returns the registry's approve_doc tool.
func findApproveDoc(t *testing.T, r *tools.ToolRegistry) tools.ApproveDocTool {
	t.Helper()
	for _, tp := range r.Tools() {
		if ad, ok := tp.Tool.(tools.ApproveDocTool); ok {
			return ad
		}
	}
	t.Fatal("approve_doc not registered")
	return tools.ApproveDocTool{}
}

// TestSetNotify_DoesNotGrantApproveDocToUnauthorizedMinister pins the
// permission gate on the late-bound refresh: approve_doc is intentExec, so a
// minister without Intent Execute (forge) must not gain it in an already-open
// session just because SetNotify ran. Without the gate the refresh bypasses
// ForPermissions and the tool set depends on whether the session predates
// SetNotify.
func TestSetNotify_DoesNotGrantApproveDocToUnauthorizedMinister(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s)

	forge := s.GetMinister("forge")
	require.NotNil(t, forge)
	require.False(t, ministerHasTool(forge, "approve_doc"),
		"precondition: forge must not be entitled to approve_doc")

	// Construct the session with forge's real (permission-derived) tool set so
	// the only way approve_doc could appear is the ungated refresh loop.
	sess, err := NewSession(mocks.NewLLMProvider(), &SessionConfig{}, forge.Tools(), nil, nil, "test", "forge")
	require.NoError(t, err)
	require.False(t, sess.hasTool("approve_doc"),
		"precondition: forge session must not start with approve_doc")
	base, ok := forge.(interface{ SetSession(*Session, ...string) })
	require.True(t, ok)
	base.SetSession(sess)

	s.SetNotify(func(any) {})

	// The registry itself is repaired (permission classification preserved)...
	require.NotNil(t, findApproveDoc(t, s.toolRegistry).Notify)
	// ...but the unauthorized minister's live session must not gain the tool.
	require.False(t, sess.hasTool("approve_doc"),
		"SetNotify must not inject approve_doc into a minister that lacks Intent Execute")
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

// TestBuildToolRegistry_LazyTimeouts asserts that the shell tool's timeout
// getter is resolved lazily from session config: buildToolRegistry runs
// before ConfigureModel sets sessionCfg, so a value snapshot would capture
// zero timeouts and configured run_shell_command.timeout_minutes /
// sandbox.approval_timeout would silently fall back to runner defaults in
// daemon mode. The getter must make ConfigureModel's config effective
// without re-registration.
func TestBuildToolRegistry_LazyTimeouts(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	// A runner is required for run_shell_command registration.
	s := NewCourt(db, cfg, &msgForwardingRunner{}, nil)
	require.NotNil(t, s)

	// Grab the registered shell tool before ConfigureModel — this mirrors
	// the daemon lifecycle where buildToolRegistry runs with sessionCfg nil.
	forgePerm, _ := tools.ParsePermissions("rwxr---w-")
	ts := s.toolRegistry.ForPermissions(forgePerm)
	var shellTool *tools.RunShellCommand
	for _, tool := range ts {
		if st, ok := tool.(*tools.RunShellCommand); ok {
			shellTool = st
			break
		}
	}
	require.NotNil(t, shellTool, "run_shell_command should be registered after NewCourt")

	// timeouts is an unexported field — reach it via reflection, same as
	// the hostChecker/msgChan assertions above. reflect.New(...).Elem()
	// gives an addressable value whose fields can be read even unexported.
	shellVal := reflect.ValueOf(shellTool).Elem()
	timeoutsGetter := shellVal.FieldByName("timeouts")
	require.True(t, timeoutsGetter.IsValid(), "RunShellCommand should have timeouts field")
	require.False(t, timeoutsGetter.IsNil(), "shell tool's timeouts getter must be non-nil after NewCourt")
	getTimeouts := func() runners.HostTimeouts {
		// The unexported field can't be Interface()'d; read the func value
		// through its address directly (test-only).
		fn := *(*func() runners.HostTimeouts)(unsafe.Pointer(timeoutsGetter.UnsafeAddr()))
		return fn()
	}

	// Before ConfigureModel, the getter must resolve to zero values
	// (runner defaults apply).
	before := getTimeouts()
	assert.Zero(t, before.Command, "no session config yet — command timeout should be zero")
	assert.Zero(t, before.Approval, "no session config yet — approval timeout should be zero")

	// ConfigureModel sets sessionCfg; the same tool instance must now
	// resolve the configured deadlines without any re-registration.
	s.ConfigureModel(nil, &SessionConfig{
		RunShellCommand: config.RunShellCommandConfig{TimeoutMinutes: 7},
		Sandbox:         config.SandboxConfig{ApprovalTimeout: 3 * time.Second},
	}, repo.RepoInfo{})

	after := getTimeouts()
	assert.Equal(t, 7*time.Minute, after.Command, "configured run_shell_command.timeout_minutes should be visible through the lazy getter")
	assert.Equal(t, 3*time.Second, after.Approval, "configured sandbox.approval_timeout should be visible through the lazy getter")
}

// TestUpdateProjectRootTools_PreservesLazyTimeoutsGetter asserts the second
// production wiring site: updateProjectRootTools re-registers the shell tool
// (running before ConfigureModel in daemon mode) and must pass the lazy
// getter — not a s.hostTimeouts() value snapshot — so configured timeouts
// remain effective after re-registration.
func TestUpdateProjectRootTools_PreservesLazyTimeoutsGetter(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, &msgForwardingRunner{}, nil)
	require.NotNil(t, s)

	s.updateProjectRootTools("/tmp")

	forgePerm, _ := tools.ParsePermissions("rwxr---w-")
	ts := s.toolRegistry.ForPermissions(forgePerm)
	var shellTool *tools.RunShellCommand
	for _, tool := range ts {
		if st, ok := tool.(*tools.RunShellCommand); ok {
			shellTool = st
			break
		}
	}
	require.NotNil(t, shellTool, "run_shell_command should be registered after updateProjectRootTools")

	// Same test-only unsafe-read idiom as TestBuildToolRegistry_LazyTimeouts:
	// the unexported func field can't be reached via Interface().
	shellVal := reflect.ValueOf(shellTool).Elem()
	timeoutsGetter := shellVal.FieldByName("timeouts")
	require.True(t, timeoutsGetter.IsValid(), "RunShellCommand should have timeouts field")
	require.False(t, timeoutsGetter.IsNil(), "re-registered shell tool must carry a non-nil timeouts getter")
	fn := *(*func() runners.HostTimeouts)(unsafe.Pointer(timeoutsGetter.UnsafeAddr()))
	assert.NotNil(t, fn(), "getter must resolve to the court's hostTimeouts resolver")

	// The getter must be lazy: before any session config exists it resolves
	// to zero values (runner defaults apply), and after ConfigureModel the
	// same tool instance must see the configured deadlines.
	before := fn()
	assert.Zero(t, before.Command, "no session config yet — command timeout should be zero")
	assert.Zero(t, before.Approval, "no session config yet — approval timeout should be zero")

	s.ConfigureModel(nil, &SessionConfig{
		RunShellCommand: config.RunShellCommandConfig{TimeoutMinutes: 5},
		Sandbox:         config.SandboxConfig{ApprovalTimeout: 4 * time.Second},
	}, repo.RepoInfo{})

	after := fn()
	assert.Equal(t, 5*time.Minute, after.Command, "configured timeout_minutes must survive re-registration via the lazy getter")
	assert.Equal(t, 4*time.Second, after.Approval, "configured approval_timeout must survive re-registration via the lazy getter")
}

// worktreeAwareRunner is a shell runner that records the effective root its
// commands execute in and accepts re-targeting, so a test can prove the shell
// tool executes in the edict's worktree rather than the Court root (edict 987).
type worktreeAwareRunner struct {
	label        string
	projectRoot  string
	worktreePath string
	mu           sync.Mutex
	ranIn        []string
}

func (r *worktreeAwareRunner) Run(context.Context, runners.Input) (runners.Output, error) {
	r.mu.Lock()
	r.ranIn = append(r.ranIn, r.effectiveRoot())
	r.mu.Unlock()
	return runners.Output{Output: "ok", ExitCode: "0"}, nil
}

func (r *worktreeAwareRunner) effectiveRoot() string {
	if r.worktreePath == "" {
		return r.projectRoot
	}
	return filepath.Join(r.projectRoot, r.worktreePath)
}

func (r *worktreeAwareRunner) SetRepoInfo(ri repo.RepoInfo) {
	r.projectRoot = ri.ProjectRoot
	r.worktreePath = ri.WorktreePath
}

func (r *worktreeAwareRunner) Restart(context.Context) error        { return nil }
func (r *worktreeAwareRunner) Close(context.Context) error          { return nil }
func (r *worktreeAwareRunner) AllowFallback(bool)                   {}
func (r *worktreeAwareRunner) RunnerType() string                   { return "worktree-aware" }
func (r *worktreeAwareRunner) GetOS() string                        { return runtime.GOOS }
func (r *worktreeAwareRunner) SetMessageChannel(chan<- runners.Msg) {}
func (r *worktreeAwareRunner) HealthCheck(context.Context) error    { return nil }

func (r *worktreeAwareRunner) onlyRoot(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Len(t, r.ranIn, 1, "runner should have executed exactly one command")
	return r.ranIn[0]
}

// TestRitualStepShellUsesEdictEffectiveRoot is the end-to-end regression guard
// for edict 987: an act step's session carries the execution's (per-edict)
// runner, and a run_shell_command issued through it executes in the edict's
// effective root (the worktree) — never the Court-wide runner bound to the
// project root.
func TestRitualStepShellUsesEdictEffectiveRoot(t *testing.T) {
	db := setupCourtTestDB(t)

	ritual := &RitualDef{
		Name:        "shell-root-test",
		Description: "Test that a step's shell command lands in the edict worktree",
		Steps: []RitualStep{
			{Name: "forging", Minister: "forge", Task: "do work"},
		},
	}
	registry := NewRitualRegistry()
	require.NoError(t, registry.Register(ritual))

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "done",
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go forgeM.Run(ctx)

	court := &Court{ministers: map[string]Minister{"forge": forgeM}, logger: slog.Default()}

	const root = "/canonical/project/root"
	worktreeRel := filepath.Join(".agents", "worktrees", "e42")
	courtRunner := &worktreeAwareRunner{label: "court", projectRoot: root}
	edictRunner := &worktreeAwareRunner{label: "e42"}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, courtRunner, slog.Default(), repo.RepoInfo{ProjectRoot: root})
	// Mirror Court.RunnerForEdict: system rituals (edict <= 1) use the Court
	// runner; a real edict gets its own worktree-aware runner.
	runner.SetRunnerResolver(func(edictID uint) (runners.Runner, error) {
		if edictID <= 1 {
			return courtRunner, nil
		}
		return edictRunner, nil
	}, nil)

	exec, err := runner.Start(ctx, "shell-root-test", testEK(42), nil, func(any) {})
	require.NoError(t, err)

	// Simulate prepare_worktree's routing: the execution's repoInfo gains the
	// worktree and its runner is re-targeted with it.
	exec.repoInfo.WorktreePath = worktreeRel
	exec.repoInfo.IsWorktree = true
	edictRunner.SetRepoInfo(exec.repoInfo)

	exec.State = RitualStateRunning
	_, err = runner.executeMinisterStep(ctx, exec, ritual.Steps[0])
	require.NoError(t, err)

	actSession := exec.stepStates[exec.CurrentStep].Session
	require.NotNil(t, actSession, "act session should exist for the step")

	// A shell tool registered with the Court root runner, invoked through the
	// step's session, must prefer the session's (edict) runner.
	shellTool := tools.NewRunShellCommand(nil, courtRunner, nil, root)
	actSession.executeToolCall(ctx, shellTool, "call-1", "run_shell_command",
		`{"command":"pwd","description":"test"}`)

	assert.Equal(t, filepath.Join(root, worktreeRel), edictRunner.onlyRoot(t),
		"the edict's shell runner must execute in the worktree effective root")
	assert.Empty(t, courtRunner.ranIn, "the Court-wide root runner must not execute the edict's command")
}

// TestRitualStepShellSystemEdictUsesRoot is the fail-safe half of edict 987:
// system/court rituals (edict <= 1) must keep executing at the project root.
func TestRitualStepShellSystemEdictUsesRoot(t *testing.T) {
	db := setupCourtTestDB(t)

	ritual := &RitualDef{
		Name:        "shell-root-system-test",
		Description: "Test that a system ritual's shell command stays at the root",
		Steps: []RitualStep{
			{Name: "forging", Minister: "forge", Task: "do work"},
		},
	}
	registry := NewRitualRegistry()
	require.NoError(t, registry.Register(ritual))

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "done",
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go forgeM.Run(ctx)

	court := &Court{ministers: map[string]Minister{"forge": forgeM}, logger: slog.Default()}

	const root = "/canonical/project/root"
	courtRunner := &worktreeAwareRunner{label: "court", projectRoot: root}
	edictRunner := &worktreeAwareRunner{label: "e1"}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, courtRunner, slog.Default(), repo.RepoInfo{ProjectRoot: root})
	runner.SetRunnerResolver(func(edictID uint) (runners.Runner, error) {
		if edictID <= 1 {
			return courtRunner, nil
		}
		return edictRunner, nil
	}, nil)

	exec, err := runner.Start(ctx, "shell-root-system-test", testEK(1), nil, func(any) {})
	require.NoError(t, err)
	exec.State = RitualStateRunning

	_, err = runner.executeMinisterStep(ctx, exec, ritual.Steps[0])
	require.NoError(t, err)

	actSession := exec.stepStates[exec.CurrentStep].Session
	require.NotNil(t, actSession)

	shellTool := tools.NewRunShellCommand(nil, courtRunner, nil, root)
	actSession.executeToolCall(ctx, shellTool, "call-1", "run_shell_command",
		`{"command":"pwd","description":"test"}`)

	assert.Equal(t, root, courtRunner.onlyRoot(t), "a system ritual's shell must still run at the project root")
	assert.Empty(t, edictRunner.ranIn, "a system ritual must not be routed into a per-edict runner")
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

// --- Ling ignition (edict 847) ---

// recordingProvider is an LLMProvider that records the last user prompt seen.
type recordingProvider struct {
	mu      sync.Mutex
	prompts []string
}

func (p *recordingProvider) record(req *schemas.BifrostChatRequest) {
	for _, m := range req.Input {
		if m.Role == schemas.ChatMessageRoleUser && m.Content != nil && m.Content.ContentStr != nil {
			p.mu.Lock()
			p.prompts = append(p.prompts, *m.Content.ContentStr)
			p.mu.Unlock()
		}
	}
}

// promptsSnapshot returns a copy of the recorded prompts, safe to read without
// racing the recording goroutines.
func (p *recordingProvider) promptsSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.prompts...)
}

func (p *recordingProvider) ChatCompletionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	p.record(req)
	content := "done"
	return &schemas.BifrostChatResponse{Choices: []schemas.BifrostResponseChoice{{
		ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &content}},
		FinishReason:             strPtr("stop"),
	}}}, nil
}

func (p *recordingProvider) ChatCompletionStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	p.record(req)
	ch := make(chan *schemas.BifrostStreamChunk, 1)
	stop := "stop"
	content := "done"
	ch <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{{
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &content}},
			FinishReason:             &stop,
		}},
	}}
	close(ch)
	return ch, nil
}

func (p *recordingProvider) ListModelsRequest(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{}, nil
}

func (p *recordingProvider) ListModels(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{}, nil
}

func (p *recordingProvider) ListAllModels(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{}, nil
}

// lingTestMinister is a minimal Minister for ignition tests.
type lingTestMinister struct {
	MinisterBase
	id       string
	tasksCh  chan *Task
	provider LLMProvider
}

func (m *lingTestMinister) ID() string           { return m.id }
func (m *lingTestMinister) SystemPrompt() string { return "test" }
func (m *lingTestMinister) Tools() []Tool        { return nil }
func (m *lingTestMinister) Tasks() chan<- *Task  { return m.tasksCh }
func (m *lingTestMinister) Model() LLMProvider   { return m.provider }
func (m *lingTestMinister) GetConfig() config.LLMConfig {
	return config.LLMConfig{Provider: "test", Model: "test"}
}
func (m *lingTestMinister) Run(ctx context.Context) {}

// newLingTestCourt builds a Court wired for ignition tests.
func newLingTestCourt(t *testing.T, db *gorm.DB) (*Court, *recordingProvider) {
	t.Helper()
	provider := &recordingProvider{}
	ministers := map[string]Minister{}
	for _, id := range []string{"forge", "judge"} {
		ministers[id] = &lingTestMinister{
			MinisterBase: MinisterBase{logger: slog.Default()},
			id:           id,
			tasksCh:      make(chan *Task, 1),
			provider:     provider,
		}
	}
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	// Enable ignition explicitly: these tests exercise the engine; the
	// production default is off (separate ruling, e923).
	cfg := config.DefaultCourtConfig()
	cfg.LingIgnitionEnabled = true
	s := &Court{
		db:             db,
		ministers:      ministers,
		logger:         slog.Default(),
		notify:         func(any) {},
		config:         cfg,
		lingIgniting:   map[uint]*sync.Mutex{},
		lingCommitting: map[uint]*sync.Mutex{},
	}
	s.sessionCfg = &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}
	s.ritualGuard = NewRitualGuard(RitualGuardOpts{
		Base:        base,
		GetMinister: s.GetMinister,
	})
	// Ignition now ensures the edict's worktree before dispatching (e923), so
	// the test court needs a project root and a mutation runner that stands in
	// for the host git commands. The runner records commands so a test can prove
	// Ensure ran before any ling.
	root := t.TempDir()
	s.ritualGuard.repoInfo = repo.RepoInfo{ProjectRoot: root}
	s.mutationRunner = &lingMutationRunner{}
	return s, provider
}

// lingMutationRunner is the fake host runner standing in for git in ignition
// tests. It records every command so a test can assert ordering (Ensure before
// dispatch) and scripted output for path snapshots.
type lingMutationRunner struct {
	mu       sync.Mutex
	commands []string
	root     string
	// statusOutputs scripts successive `git status --porcelain` answers; the
	// last entry repeats once the queue is exhausted. The first answer is the
	// pre-run snapshot, the second the post-run dirty set.
	statusOutputs []string
	statusCalls   int
	// onAdd, when set, is invoked when a stage command runs; it lets a test
	// detect two lings staging at once.
	onAdd func()
}

func (r *lingMutationRunner) Run(_ context.Context, in runners.Input) (runners.Output, error) {
	r.mu.Lock()
	r.commands = append(r.commands, in.Command)
	out := ""
	onAdd := r.onAdd
	switch {
	case strings.Contains(in.Command, "status --porcelain"):
		if len(r.statusOutputs) > 0 {
			i := r.statusCalls
			if i >= len(r.statusOutputs) {
				i = len(r.statusOutputs) - 1
			}
			out = r.statusOutputs[i]
			r.statusCalls++
		}
	case strings.Contains(in.Command, "diff --cached --name-only"):
		out = "changed.go"
	}
	r.mu.Unlock()
	if onAdd != nil && strings.Contains(in.Command, " add -- ") {
		onAdd()
	}
	return runners.Output{Output: out, ExitCode: "0"}, nil
}

func (r *lingMutationRunner) Restart(context.Context) error        { return nil }
func (r *lingMutationRunner) Close(context.Context) error          { return nil }
func (r *lingMutationRunner) AllowFallback(bool)                   {}
func (r *lingMutationRunner) RunnerType() string                   { return "ling-test" }
func (r *lingMutationRunner) GetOS() string                        { return runtime.GOOS }
func (r *lingMutationRunner) SetMessageChannel(chan<- runners.Msg) {}
func (r *lingMutationRunner) HealthCheck(context.Context) error    { return nil }

// SetRepoInfo records the ProjectRoot it is aimed at. Ignition must never
// re-aim the shared runner's ProjectRoot away from the main tree.
func (r *lingMutationRunner) SetRepoInfo(ri repo.RepoInfo) {
	r.mu.Lock()
	r.root = ri.ProjectRoot
	r.mu.Unlock()
}

func (r *lingMutationRunner) ran(fragment string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.commands {
		if strings.Contains(c, fragment) {
			return true
		}
	}
	return false
}

func (r *lingMutationRunner) firstIndex(fragment string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range r.commands {
		if strings.Contains(c, fragment) {
			return i
		}
	}
	return -1
}

func (r *lingMutationRunner) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.commands...)
}

// newLingTestCourtPerMinister is like newLingTestCourt but gives each minister
// its own recording provider, so a test can assert which minister ran which
// ling. It returns the court and a map of minister ID → provider.
func newLingTestCourtPerMinister(t *testing.T, db *gorm.DB) (*Court, map[string]*recordingProvider) {
	t.Helper()
	providers := map[string]*recordingProvider{}
	ministers := map[string]Minister{}
	for _, id := range []string{"forge", "judge"} {
		provider := &recordingProvider{}
		providers[id] = provider
		ministers[id] = &lingTestMinister{
			MinisterBase: MinisterBase{logger: slog.Default()},
			id:           id,
			tasksCh:      make(chan *Task, 1),
			provider:     provider,
		}
	}
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	// Enable ignition explicitly: these tests exercise the engine; the
	// production default is off (flip is a separate ruling, e923).
	cfg := config.DefaultCourtConfig()
	cfg.LingIgnitionEnabled = true
	s := &Court{
		db:        db,
		ministers: ministers,
		logger:    slog.Default(),
		notify:    func(any) {},
		config:    cfg,
	}
	s.sessionCfg = &SessionConfig{LLM: config.LLMConfig{Provider: "test", Model: "test"}}
	s.ritualGuard = NewRitualGuard(RitualGuardOpts{
		Base:        base,
		GetMinister: s.GetMinister,
	})
	root := t.TempDir()
	s.ritualGuard.repoInfo = repo.RepoInfo{ProjectRoot: root}
	s.mutationRunner = &lingMutationRunner{}
	return s, providers
}

// TestIgniteLings_DependencyOrder verifies that lings authored in a batch with
// intra-batch deps ignite in order: the dependent ling runs only after its
// dependency completes, and both end up done.
func TestIgniteLings_DependencyOrder(t *testing.T) {
	db := setupCourtTestDB(t)
	s, provider := newLingTestCourt(t, db)

	key := storage.EdictKey{ID: 42, Username: "testuser", Project: "testproject"}
	base := storage.Ling{EdictID: 42, Username: "testuser", Project: "testproject", Status: storage.LingPending}
	a := base
	a.LingID = "ling-a"
	a.Description = "task A"
	b := base
	b.LingID = "ling-b"
	b.Description = "task B"
	b.Dependencies = storage.StringArray{"ling-a"}
	require.NoError(t, db.Create(&a).Error)
	require.NoError(t, db.Create(&b).Error)

	require.NoError(t, s.igniteLings(context.Background(), key))

	provider.mu.Lock()
	prompts := append([]string{}, provider.prompts...)
	provider.mu.Unlock()
	require.Len(t, prompts, 2, "both lings should have run")
	assert.Contains(t, prompts[0], "task A", "ling a must run first")
	assert.Contains(t, prompts[1], "task B", "ling b must run after a")

	var lings []storage.Ling
	require.NoError(t, db.Where("edict_id = ?", 42).Find(&lings).Error)
	for _, l := range lings {
		assert.Equal(t, storage.LingDone, l.Status, "ling %s should be done", l.LingID)
	}

	// Events: one created is not published here (only insert_ling does that);
	// started/completed should each appear per ling.
	var events []storage.TianEvent
	require.NoError(t, db.Where("edict_id = ?", 42).Find(&events).Error)
	counts := map[storage.CourtEvent]int{}
	for _, e := range events {
		counts[e.EventType]++
	}
	assert.Equal(t, 2, counts[storage.EventLingStarted])
	assert.Equal(t, 2, counts[storage.EventLingCompleted])
	assert.Zero(t, counts[storage.EventLingFailed])
}

// TestIgniteLings_LeavesPendingWithoutDeps verifies lings whose dependencies
// are not met stay pending without error.
func TestIgniteLings_LeavesPendingWithoutDeps(t *testing.T) {
	db := setupCourtTestDB(t)
	s, provider := newLingTestCourt(t, db)

	key := storage.EdictKey{ID: 43, Username: "testuser", Project: "testproject"}
	blocked := storage.Ling{
		LingID: "ling-blocked", EdictID: 43, Username: "testuser", Project: "testproject",
		Description: "blocked task", Dependencies: storage.StringArray{"never-created"},
		Status: storage.LingPending,
	}
	require.NoError(t, db.Create(&blocked).Error)

	require.NoError(t, s.igniteLings(context.Background(), key))

	provider.mu.Lock()
	n := len(provider.prompts)
	provider.mu.Unlock()
	assert.Zero(t, n, "blocked ling must not run")

	var got storage.Ling
	require.NoError(t, db.First(&got, "ling_id = ?", "ling-blocked").Error)
	assert.Equal(t, storage.LingPending, got.Status)
}

// TestRunLing_ResolvesLingMinister verifies that a ling's own `minister` field
// is honoured: each ling is dispatched to the minister it names, and a ling
// with no minister falls back to forge. This is the routing contract of
// runLing / igniteLings (edict 847, change G).
func TestRunLing_ResolvesLingMinister(t *testing.T) {
	db := setupCourtTestDB(t)
	s, providers := newLingTestCourtPerMinister(t, db)

	// ling-forge omits the minister → must default to forge.
	// ling-judge names judge explicitly.
	lings := []storage.Ling{
		{LingID: "ling-forge", EdictID: 44, Username: "testuser", Project: "testproject",
			Description: "forge task", Status: storage.LingPending},
		{LingID: "ling-judge", EdictID: 44, Username: "testuser", Project: "testproject",
			Description: "judge task", Minister: "judge", Status: storage.LingPending},
	}
	for i := range lings {
		require.NoError(t, db.Create(&lings[i]).Error)
	}

	key := storage.EdictKey{ID: 44, Username: "testuser", Project: "testproject"}
	require.NoError(t, s.igniteLings(context.Background(), key))

	forgePrompts := providers["forge"].promptsSnapshot()
	judgePrompts := providers["judge"].promptsSnapshot()
	require.Len(t, forgePrompts, 1, "forge should run exactly the unspecified-minister ling")
	require.Len(t, judgePrompts, 1, "judge should run exactly the ling that named it")
	assert.Contains(t, forgePrompts[0], "forge task")
	assert.Contains(t, judgePrompts[0], "judge task")

	var judgeLing storage.Ling
	require.NoError(t, db.First(&judgeLing, "ling_id = ?", "ling-judge").Error)
	assert.Equal(t, storage.LingDone, judgeLing.Status)
}

// TestRunLing_FailingMinisterPublishesLingFailed verifies that a ling whose
// minister is not registered is not silently dropped: it stays pending (so a
// later trigger can retry) and a ling_failed event is published (edict 847).
func TestRunLing_FailingMinisterPublishesLingFailed(t *testing.T) {
	db := setupCourtTestDB(t)
	s, _ := newLingTestCourt(t, db)

	ling := storage.Ling{
		LingID: "ling-orphan", EdictID: 45, Username: "testuser", Project: "testproject",
		Description: "orphan task", Minister: "nonexistent", Status: storage.LingPending,
	}
	require.NoError(t, db.Create(&ling).Error)

	key := storage.EdictKey{ID: 45, Username: "testuser", Project: "testproject"}
	// allowIncomplete=true means execute() itself returns nil; the failure is
	// surfaced via the ling_failed event and the ling remaining pending.
	require.NoError(t, s.igniteLings(context.Background(), key))

	var events []storage.TianEvent
	require.NoError(t, db.Where("edict_id = ?", 45).Find(&events).Error)
	counts := map[storage.CourtEvent]int{}
	for _, e := range events {
		counts[e.EventType]++
	}
	assert.Equal(t, 1, counts[storage.EventLingFailed], "missing minister must publish ling_failed")

	var got storage.Ling
	require.NoError(t, db.First(&got, "ling_id = ?", "ling-orphan").Error)
	assert.Equal(t, storage.LingPending, got.Status, "failed ling stays pending for retry")
}

// TestTriggerLingIgnition_GateOffSkipsAndStaysPending verifies the off state
// of the gate: a trigger with LingIgnitionEnabled=false must (a) spawn no
// minister session (no ling_started event), (b) record a ling_ignition_skipped
// event carrying the reason, and (c) leave the ling pending and listable
// (e923 gate; the recorded blocker used to name e531, which is sealed).
func TestTriggerLingIgnition_GateOffSkipsAndStaysPending(t *testing.T) {
	db := setupCourtTestDB(t)
	s, provider := newLingTestCourt(t, db)
	s.config.LingIgnitionEnabled = false

	key := storage.EdictKey{ID: 46, Username: "testuser", Project: "testproject"}
	require.NoError(t, db.Create(&storage.Ling{
		LingID: "ling-gated", EdictID: 46, Username: "testuser", Project: "testproject",
		Description: "gated task", Status: storage.LingPending,
	}).Error)

	s.TriggerLingIgnition(key)

	provider.mu.Lock()
	n := len(provider.prompts)
	provider.mu.Unlock()
	assert.Zero(t, n, "no minister session may start while the gate is off")

	var events []storage.TianEvent
	require.NoError(t, db.Where("edict_id = ?", 46).Find(&events).Error)
	counts := map[storage.CourtEvent]int{}
	var skipPayload storage.JSON
	for _, e := range events {
		counts[e.EventType]++
		if e.EventType == storage.EventLingIgnitionSkipped {
			skipPayload = e.Payload
		}
	}
	assert.Zero(t, counts[storage.EventLingStarted], "no ling may start while the gate is off")
	assert.Equal(t, 1, counts[storage.EventLingIgnitionSkipped], "skip must be visible in the ledger")
	require.NotNil(t, skipPayload)
	assert.Equal(t, "ling_ignition_disabled_by_config", skipPayload["reason"])
	assert.Equal(t, true, skipPayload["pending_e923"], "the recorded blocker is this edict, not the sealed e531")
	assert.Nil(t, skipPayload["pending_e531"], "the stale e531 reason must be gone")

	var got storage.Ling
	require.NoError(t, db.First(&got, "ling_id = ?", "ling-gated").Error)
	assert.Equal(t, storage.LingPending, got.Status, "gated ling stays pending and listable")
}

// TestTriggerLingIgnition_GateOnIgnites verifies the on state of the gate:
// with LingIgnitionEnabled=true the trigger processes pending lings.
func TestTriggerLingIgnition_GateOnIgnites(t *testing.T) {
	db := setupCourtTestDB(t)
	s, provider := newLingTestCourt(t, db)

	key := storage.EdictKey{ID: 47, Username: "testuser", Project: "testproject"}
	require.NoError(t, db.Create(&storage.Ling{
		LingID: "ling-active", EdictID: 47, Username: "testuser", Project: "testproject",
		Description: "active task", Status: storage.LingPending,
	}).Error)

	s.TriggerLingIgnition(key)
	// TriggerLingIgnition runs in a background goroutine; poll for completion.
	require.Eventually(t, func() bool {
		var got storage.Ling
		if err := db.First(&got, "ling_id = ?", "ling-active").Error; err != nil {
			return false
		}
		return got.Status == storage.LingDone
	}, 5*time.Second, 10*time.Millisecond, "gated-on trigger must ignite the ling")

	provider.mu.Lock()
	n := len(provider.prompts)
	provider.mu.Unlock()
	assert.Equal(t, 1, n, "exactly one minister session runs")
}

// newLingWorktreeCourt extends newLingTestCourt for worktree tests: the edict's
// worktree directory pre-exists (so Ensure reuses it rather than shelling out
// `git worktree add`), and the mutation runner is returned so tests can inspect
// the git commands ignition issued.
func newLingWorktreeCourt(t *testing.T, db *gorm.DB, edictID uint) (*Court, *recordingProvider, *lingMutationRunner) {
	t.Helper()
	s, provider := newLingTestCourt(t, db)
	runner := s.mutationRunner.(*lingMutationRunner)
	require.NoError(t, os.MkdirAll(
		filepath.Join(s.projectRoot(), worktree.RelPath(edictID)), 0o755))
	return s, provider, runner
}

// TestIgniteLings_EnsuresEdictWorktree verifies the edict worktree is ensured
// before any ling is dispatched (e923): the worktree directory exists after
// ignition, and each ling's session is bound to it (WorkingDir = worktree,
// IsWorktree true) — never the main tree.
func TestIgniteLings_EnsuresEdictWorktree(t *testing.T) {
	db := setupCourtTestDB(t)
	s, provider, _ := newLingWorktreeCourt(t, db, 48)

	ling := storage.Ling{
		LingID: "ling-wt", EdictID: 48, Username: "testuser", Project: "testproject",
		Description: "worktree task", Status: storage.LingPending,
	}
	require.NoError(t, db.Create(&ling).Error)

	key := storage.EdictKey{ID: 48, Username: "testuser", Project: "testproject"}
	require.NoError(t, s.igniteLings(context.Background(), key))

	wtRoot := filepath.Join(s.projectRoot(), worktree.RelPath(48))
	_, err := os.Stat(wtRoot)
	require.NoError(t, err, "the edict worktree must exist after ignition")

	require.Len(t, provider.promptsSnapshot(), 1, "the ling must run")
	var started storage.JSON
	var events []storage.TianEvent
	require.NoError(t, db.Where("edict_id = ?", 48).Find(&events).Error)
	for _, e := range events {
		if e.EventType == storage.EventLingStarted {
			started = e.Payload
		}
	}
	require.NotNil(t, started, "the ling_started event must carry the routing")
	assert.Equal(t, wtRoot, started["worktree"], "the ling must be routed into the edict worktree")
	assert.Equal(t, true, started["is_worktree"], "the ling's session must be marked as worktree-bound")
	assert.Equal(t, s.projectRoot(), s.mutationRunner.(*lingMutationRunner).repoInfoRoot(),
		"ProjectRoot must stay the main tree")
}

// repoInfoRoot reports the last ProjectRoot the fake mutation runner was aimed at.
func (r *lingMutationRunner) repoInfoRoot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.root
}

// TestRunLing_UsesEdictWorktree pins runLing's binding contract directly: the
// ling's session resolves to the edict worktree (WorkingDir + runner) while the
// shared ToolContext.RepoInfo/registry are left untouched (e923 §2).
func TestRunLing_UsesEdictWorktree(t *testing.T) {
	db := setupCourtTestDB(t)
	s, _, mut := newLingWorktreeCourt(t, db, 55)
	s.toolCtxRepoInfo = &repo.RepoInfo{ProjectRoot: "/sentinel"}
	before := *s.toolCtxRepoInfo

	var ling storage.Ling
	ling = storage.Ling{
		LingID: "ling-route", EdictID: 55, Username: "testuser", Project: "testproject",
		Description: "route me", Status: storage.LingPending,
	}
	require.NoError(t, db.Create(&ling).Error)

	key := storage.EdictKey{ID: 55, Username: "testuser", Project: "testproject"}
	wtRoot := filepath.Join(s.projectRoot(), worktree.RelPath(55))
	require.NoError(t, s.runLing(context.Background(), key,
		dagUnit{ID: "ling-route", Index: 0, Item: "ling-route"}, wtRoot))

	// The shared pointer must not have been re-pointed at the worktree.
	assert.Equal(t, before, *s.toolCtxRepoInfo, "the shared RepoInfo must stay untouched")
	assert.Equal(t, "/sentinel", s.toolCtxRepoInfo.ProjectRoot)
	// The ling's git commands must all have run inside the worktree.
	for _, c := range mut.snapshot() {
		if strings.Contains(c, "git -C") {
			assert.Contains(t, c, wtRoot, "ling commands must execute in the edict worktree")
		}
	}
}

// aborts ignition: no ling runs, a failure event is recorded, and the main tree
// is never used as a fallback (e923).
// TestIgniteLings_EnsureFailureAborts verifies that a worktree Ensure failure
// aborts ignition: no ling runs, a failure event is recorded, and the main tree
// is never used as a fallback (e923).
func TestIgniteLings_EnsureFailureAborts(t *testing.T) {
	db := setupCourtTestDB(t)
	s, provider := newLingTestCourt(t, db)

	ling := storage.Ling{
		LingID: "ling-nofail", EdictID: 49, Username: "testuser", Project: "testproject",
		Description: "should not run", Status: storage.LingPending,
	}
	require.NoError(t, db.Create(&ling).Error)

	// A runner whose git commands fail makes Ensure (git worktree add) fail.
	s.mutationRunner = &failingLingRunner{}

	key := storage.EdictKey{ID: 49, Username: "testuser", Project: "testproject"}
	err := s.igniteLings(context.Background(), key)
	require.Error(t, err, "ignition must fail when the worktree cannot be ensured")

	assert.Empty(t, provider.promptsSnapshot(), "no ling may run when Ensure fails")

	var got storage.Ling
	require.NoError(t, db.First(&got, "ling_id = ?", "ling-nofail").Error)
	assert.Equal(t, storage.LingPending, got.Status, "the ling must stay pending for a later retry")

	var events []storage.TianEvent
	require.NoError(t, db.Where("edict_id = ?", 49).Find(&events).Error)
	var failed storage.JSON
	for _, e := range events {
		if e.EventType == storage.EventLingFailed {
			failed = e.Payload
		}
	}
	require.NotNil(t, failed, "the Ensure failure must be visible in the ledger")
	assert.Equal(t, "worktree_ensure_failed", failed["reason"])
}

// failingLingRunner fails every command, so worktree.Ensure aborts.
type failingLingRunner struct{}

func (r *failingLingRunner) Run(context.Context, runners.Input) (runners.Output, error) {
	return runners.Output{Output: "boom", ExitCode: "1"}, nil
}
func (r *failingLingRunner) Restart(context.Context) error        { return nil }
func (r *failingLingRunner) Close(context.Context) error          { return nil }
func (r *failingLingRunner) AllowFallback(bool)                   {}
func (r *failingLingRunner) RunnerType() string                   { return "ling-fail" }
func (r *failingLingRunner) GetOS() string                        { return runtime.GOOS }
func (r *failingLingRunner) SetMessageChannel(chan<- runners.Msg) {}
func (r *failingLingRunner) HealthCheck(context.Context) error    { return nil }
func (r *failingLingRunner) SetRepoInfo(repo.RepoInfo)            {}

// TestIgniteLings_ParallelNoSharedStateRace verifies that independent lings run
// concurrently, each in the edict's worktree, without mutating the shared
// ToolContext.RepoInfo. Run under -race this is the guard for e923 §5.
func TestIgniteLings_ParallelNoSharedStateRace(t *testing.T) {
	db := setupCourtTestDB(t)
	s, provider, _ := newLingWorktreeCourt(t, db, 50)

	// Seed the shared pointer with a sentinel so any mutation is detectable.
	if s.toolCtxRepoInfo == nil {
		s.toolCtxRepoInfo = &repo.RepoInfo{ProjectRoot: "/sentinel"}
	}
	before := *s.toolCtxRepoInfo

	for _, id := range []string{"ling-p1", "ling-p2", "ling-p3"} {
		require.NoError(t, db.Create(&storage.Ling{
			LingID: id, EdictID: 50, Username: "testuser", Project: "testproject",
			Description: "parallel " + id, Status: storage.LingPending,
		}).Error)
	}

	key := storage.EdictKey{ID: 50, Username: "testuser", Project: "testproject"}
	require.NoError(t, s.igniteLings(context.Background(), key))

	assert.Len(t, provider.promptsSnapshot(), 3, "all independent lings must run")
	assert.Equal(t, before, *s.toolCtxRepoInfo, "the shared RepoInfo must never be mutated")

	// Every ling must have been routed into the edict worktree (asserted from
	// its own ledger event, since concurrent sessions share the minister's map).
	wtRoot := filepath.Join(s.projectRoot(), worktree.RelPath(50))
	var started int
	var events []storage.TianEvent
	require.NoError(t, db.Where("edict_id = ?", 50).Find(&events).Error)
	for _, e := range events {
		if e.EventType != storage.EventLingStarted {
			continue
		}
		started++
		assert.Equal(t, wtRoot, e.Payload["worktree"], "every ling session must be bound to the edict worktree")
		assert.Equal(t, true, e.Payload["is_worktree"])
	}
	assert.Equal(t, 3, started, "every ling must report its routing")
}

// TestIgniteLings_CommitsOnSuccess verifies that a completed ling commits its
// own paths on the edict branch with the ling id in the subject, under the
// per-edict commit mutex (e923, ruler ruling R4).
func TestIgniteLings_CommitsOnSuccess(t *testing.T) {
	db := setupCourtTestDB(t)
	s, _, mut := newLingWorktreeCourt(t, db, 51)
	mut.statusOutputs = []string{"", " M changed.go\n"}

	require.NoError(t, db.Create(&storage.Ling{
		LingID: "ling-commit", EdictID: 51, Username: "testuser", Project: "testproject",
		Description: "commit me", Status: storage.LingPending,
	}).Error)

	key := storage.EdictKey{ID: 51, Username: "testuser", Project: "testproject"}
	require.NoError(t, s.igniteLings(context.Background(), key))

	// The fake git reports a dirty path at completion that was clean at start,
	// so the ling must stage exactly that path and commit.
	wtRoot := filepath.Join(s.projectRoot(), worktree.RelPath(51))
	assert.True(t, mut.ran("git -C "+wtRoot+" add --"),
		"the ling must stage its own paths: %v", mut.snapshot())

	subjects := mut.subjectsContaining("ling-commit")
	require.Len(t, subjects, 1, "the ling must produce one commit: %v", mut.snapshot())
	assert.Contains(t, subjects[0], "ling-commit", "the commit subject must name the ling")
	assert.Contains(t, subjects[0], "e51", "the commit subject must carry the edict suffix")

	var got storage.Ling
	require.NoError(t, db.First(&got, "ling_id = ?", "ling-commit").Error)
	assert.Equal(t, storage.LingDone, got.Status)
}

// subjectsContaining returns the commit -m arguments of recorded commands that
// mention fragment.
func (r *lingMutationRunner) subjectsContaining(fragment string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.commands {
		if strings.Contains(c, " commit -m ") && strings.Contains(c, fragment) {
			out = append(out, c)
		}
	}
	return out
}

// TestIgniteLings_ParallelCommitsSerialized verifies the per-edict commit mutex
// keeps two lings' stage/commit windows from interleaving on the shared
// worktree's .git/index: while one ling holds the commit lock no other ling may
// enter its stage step (e923 §4).
func TestIgniteLings_ParallelCommitsSerialized(t *testing.T) {
	db := setupCourtTestDB(t)
	s, _, mut := newLingWorktreeCourt(t, db, 52)

	// Two lings commit concurrently; the fake reports a dirty path for each.
	var wg sync.WaitGroup
	var mu sync.Mutex
	inStage := false
	var overlap bool
	mut.onAdd = func() {
		mu.Lock()
		if inStage {
			overlap = true
		}
		inStage = true
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		inStage = false
		mu.Unlock()
	}
	mut.statusOutputs = []string{" M own.go\n"}

	key := storage.EdictKey{ID: 52, Username: "testuser", Project: "testproject"}
	wtRoot := filepath.Join(s.projectRoot(), worktree.RelPath(52))
	for _, id := range []string{"ling-c1", "ling-c2"} {
		ling := storage.Ling{LingID: id, EdictID: 52, Username: "testuser", Project: "testproject"}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.commitLing(context.Background(), key, ling, wtRoot, nil)
		}()
	}
	wg.Wait()

	assert.False(t, overlap, "two lings must never stage at the same time")
	assert.Len(t, mut.subjectsContaining("e52"), 2, "both commits must have happened: %v", mut.snapshot())
}

// TestIgniteLings_BatchSizeBound verifies the configured parallelism bound is
// honoured: with LingParallelism=1 two independent lings never run at the same
// time (e923 §3).
func TestIgniteLings_BatchSizeBound(t *testing.T) {
	db := setupCourtTestDB(t)
	s, provider, _ := newLingWorktreeCourt(t, db, 53)
	s.config.LingParallelism = 1
	assert.Equal(t, 1, s.lingBatchSize())

	for _, id := range []string{"ling-b1", "ling-b2"} {
		require.NoError(t, db.Create(&storage.Ling{
			LingID: id, EdictID: 53, Username: "testuser", Project: "testproject",
			Description: "bounded " + id, Status: storage.LingPending,
		}).Error)
	}

	key := storage.EdictKey{ID: 53, Username: "testuser", Project: "testproject"}
	require.NoError(t, s.igniteLings(context.Background(), key))

	prompts := provider.promptsSnapshot()
	require.Len(t, prompts, 2, "both lings must still run")
	assert.Contains(t, prompts[0], "bounded ling-b1", "with a bound of 1 the lings run one after the other")
	assert.Contains(t, prompts[1], "bounded ling-b2")
}

// TestCleanup_CascadesLingWorktrees verifies the seal-time sweep removes a
// legacy nested per-ling worktree before the edict worktree is removed (a
// parent with live children cannot be removed reliably) — e923 §4.
func TestCleanup_CascadesLingWorktrees(t *testing.T) {
	db := setupCourtTestDB(t)
	s, _, mut := newLingWorktreeCourt(t, db, 54)

	parent := filepath.Join(s.projectRoot(), worktree.RelPath(54))
	child := filepath.Join(parent, "ling-legacy")
	require.NoError(t, os.MkdirAll(child, 0o755))
	// A linked worktree is marked by a .git file; a plain directory is not ours.
	require.NoError(t, os.WriteFile(filepath.Join(child, ".git"), []byte("gitdir: x\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "later"), 0o755))

	s.sweepLingWorktrees(context.Background(), 54)

	cmds := mut.snapshot()
	childRemove := -1
	parentRemove := -1
	for i, c := range cmds {
		if strings.Contains(c, "worktree remove") && strings.Contains(c, child) {
			childRemove = i
		}
		if strings.Contains(c, "worktree remove") && strings.Contains(c, parent) && !strings.Contains(c, child) {
			parentRemove = i
		}
	}
	require.NotEqual(t, -1, childRemove, "the nested ling worktree must be swept: %v", cmds)
	if parentRemove != -1 {
		assert.Less(t, childRemove, parentRemove, "the child must be removed before the parent")
	}
	assert.True(t, mut.ran("/ling-legacy"), "the nested branch must be deleted too: %v", cmds)
	assert.False(t, mut.ran("later"), "a plain directory is not a ling worktree and must be left alone: %v", cmds)
}

// TestIgniteLings_EnsureRunsBeforeDispatch proves the ordering the edict
// demands on the path TestIgniteLings_EnsuresEdictWorktree skips by
// pre-creating the directory: when the edict's worktree does not yet exist,
// ignition must create it (worktree.Ensure shells `git worktree add`) *before*
// it snapshots that worktree for the first ling (e923 §1).
func TestIgniteLings_EnsureRunsBeforeDispatch(t *testing.T) {
	db := setupCourtTestDB(t)
	// Deliberately no pre-created worktree, unlike newLingWorktreeCourt.
	s, _ := newLingTestCourt(t, db)
	mut := s.mutationRunner.(*lingMutationRunner)

	require.NoError(t, db.Create(&storage.Ling{
		LingID: "ling-order", EdictID: 56, Username: "testuser", Project: "testproject",
		Description: "order me", Status: storage.LingPending,
	}).Error)

	key := storage.EdictKey{ID: 56, Username: "testuser", Project: "testproject"}
	require.NoError(t, s.igniteLings(context.Background(), key))

	addIdx := mut.firstIndex("worktree add")
	require.NotEqual(t, -1, addIdx, "ignition must ensure the worktree: %v", mut.snapshot())
	snapIdx := mut.firstIndex("status --porcelain")
	require.NotEqual(t, -1, snapIdx, "the ling must snapshot its worktree: %v", mut.snapshot())
	assert.Less(t, addIdx, snapIdx, "Ensure must precede the ling's worktree snapshot")
}

// TestDirtyPaths_ParsesPorcelain pins the path attribution that decides which
// paths a ling commits (e923 R4): modified, renamed, and quoted porcelain lines
// must all resolve to the correct path — a mis-parse would let one ling claim
// another's work, or miss its own.
func TestDirtyPaths_ParsesPorcelain(t *testing.T) {
	db := setupCourtTestDB(t)
	s, _ := newLingTestCourt(t, db)
	mut := s.mutationRunner.(*lingMutationRunner)
	mut.statusOutputs = []string{" M changed.go\nR  old.go -> new.go\n?? \"sp ace.go\"\n"}

	got := s.dirtyPaths(context.Background(), s.projectRoot())
	assert.True(t, got["changed.go"], "a modified path must be reported")
	assert.True(t, got["new.go"], "a rename must attribute the new path")
	assert.False(t, got["old.go"], "a rename must not attribute the old path")
	assert.True(t, got["sp ace.go"], "a quoted path must be unquoted")
}

// TestDirtyPaths_GitFailureYieldsEmptySet pins the documented degradation: a
// gitless tree or git failure yields an empty set, never a partial one, so path
// attribution falls back to "commit everything dirty" rather than mis-claiming.
func TestDirtyPaths_GitFailureYieldsEmptySet(t *testing.T) {
	db := setupCourtTestDB(t)
	s, _ := newLingTestCourt(t, db)
	s.mutationRunner = &failingLingRunner{}
	assert.Empty(t, s.dirtyPaths(context.Background(), "/any"))
}

// TestCommitLing_NothingNewOfItsOwnCommitsNothing proves the R4 guard that a
// path already dirty before the ling ran (another concurrent ling, or the
// forge's uncommitted work) is never claimed: the ling stages and commits
// nothing.
func TestCommitLing_NothingNewOfItsOwnCommitsNothing(t *testing.T) {
	db := setupCourtTestDB(t)
	s, _ := newLingTestCourt(t, db)
	mut := s.mutationRunner.(*lingMutationRunner)
	mut.statusOutputs = []string{" M shared.go\n"}

	key := storage.EdictKey{ID: 57, Username: "testuser", Project: "testproject"}
	ling := storage.Ling{LingID: "ling-clean", EdictID: 57}
	before := map[string]bool{"shared.go": true}

	require.NoError(t, s.commitLing(context.Background(), key, ling, s.projectRoot(), before))
	assert.False(t, mut.ran(" add -- "), "a ling with no new paths must not stage: %v", mut.snapshot())
	assert.False(t, mut.ran(" commit -m "), "a ling with no new paths must not commit")
}

// TestRunGitSerialized_NonZeroExitIsError pins that a failed git mutation is
// surfaced, never swallowed — a silent stage/commit failure would mark a ling
// done with its work uncommitted.
func TestRunGitSerialized_NonZeroExitIsError(t *testing.T) {
	err := runGitSerialized(context.Background(), &failingLingRunner{}, "git commit", "commit ling paths")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 1")
}

// TestShortDescription pins the commit-subject normalizer: whitespace collapsed
// to one line, capped at 60 characters, and never empty (the subject always
// carries the ling id and edict suffix set by commitLing).
func TestShortDescription(t *testing.T) {
	assert.Equal(t, "ling work", shortDescription(""))
	assert.Equal(t, "a b c", shortDescription("  a   b \n c  "))
	got := shortDescription(strings.Repeat("x", 80) + " tail")
	assert.Equal(t, strings.Repeat("x", 60), got)
}

// TestSetContext_PreservesHasVCSDetection pins edict 888's single-authoritative
// HasVCS detection through the SetContext path. SetContextParams carries no
// HasVCS field, so SetContext must re-derive it (GetRepoInfoForRoot) instead
// of rebuilding a RepoInfo literal with the flag zeroed — ConfigureModel
// pushes that literal into the RitualRunner, and gitless ground skips
// staging/commit steps and inventories the whole tree for borderland
// manifests. A false gitless detection would silently break the seal chain
// on ordinary git repositories.
func TestSetContext_PreservesHasVCSDetection(t *testing.T) {
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	s := NewCourt(db, cfg, nil, nil)
	require.NotNil(t, s.ritualGuard)
	require.NotNil(t, s.ritualGuard.RitualRunner())

	// Ground truth: a real git repository.
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	groundTruth := repo.GetRepoInfoForRoot(root)
	require.True(t, groundTruth.HasVCS, "precondition: git repository detected")

	err := s.SetContext(context.Background(), types.SetContextParams{
		ProjectRoot: root,
		Branch:      groundTruth.Branch,
		Project:     groundTruth.Slug,
		APIKeys:     map[string]string{"anthropic": "sk-test-dummy"},
	})
	require.NoError(t, err)

	runnerInfo := s.ritualGuard.RitualRunner().repoInfo
	assert.True(t, runnerInfo.HasVCS,
		"SetContext must preserve HasVCS for a real git repository")
	assert.False(t, runnerInfo.Gitless(),
		"ritual runner must not treat a git repository as gitless ground")
}

// repoInfoRecordingRunner records the RepoInfo it is re-targeted with, so a
// test can prove the worktree lever reaches the shell execution layer.
type repoInfoRecordingRunner struct {
	runners.Runner
	last repo.RepoInfo
	set  bool
}

func (r *repoInfoRecordingRunner) SetRepoInfo(ri repo.RepoInfo) { r.last = ri; r.set = true }
func (r *repoInfoRecordingRunner) Run(context.Context, runners.Input) (runners.Output, error) {
	return runners.Output{}, nil
}
func (r *repoInfoRecordingRunner) Close(context.Context) error   { return nil }
func (r *repoInfoRecordingRunner) Restart(context.Context) error { return nil }
func (r *repoInfoRecordingRunner) AllowFallback(bool)            {}
func (r *repoInfoRecordingRunner) RunnerType() string            { return "recording" }
func (r *repoInfoRecordingRunner) GetOS() string                 { return runtime.GOOS }

// TestSetRepoInfo_RoutesWorktreeToRunnerAndTools proves that entering a
// worktree (RepoInfo.WorktreePath set) reaches the execution layers: the runner
// is re-targeted and the file tools resolve against the worktree root.
func TestSetRepoInfo_RoutesWorktreeToRunnerAndTools(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	runner := &repoInfoRecordingRunner{}
	s := NewCourt(db, cfg, runner, nil)
	require.NotNil(t, s)

	s.ConfigureModel(nil, &SessionConfig{}, repo.RepoInfo{ProjectRoot: "/proj"})

	// Enter the edict's worktree.
	s.SetRepoInfo(repo.RepoInfo{
		ProjectRoot:  "/proj",
		WorktreePath: filepath.Join(".agents", "worktrees", "e531"),
		IsWorktree:   true,
	})

	require.True(t, runner.set, "runner must be re-targeted on worktree entry")
	assert.Equal(t, filepath.Join("/proj", ".agents", "worktrees", "e531"), runner.last.EffectiveRoot())
	assert.Equal(t, "/proj", runner.last.ProjectRoot, "ProjectRoot must stay the main tree")

	// The registered file tools must resolve against the worktree.
	want := filepath.Join("/proj", ".agents", "worktrees", "e531")
	found := false
	for _, tp := range s.toolRegistry.Tools() {
		switch rf := tp.Tool.(type) {
		case *tools.ReadFileTool:
			found = true
			assert.Equal(t, want, rf.ProjectRoot, "read_file must resolve against the worktree")
		case tools.ReadFileTool:
			found = true
			assert.Equal(t, want, rf.ProjectRoot, "read_file must resolve against the worktree")
		}
	}
	require.True(t, found, "read_file tool must be registered")
}

// TestSetRepoInfo_NoWorktreeEndsAtProjectRoot guards the base case: without a
// worktree, the runner and tools stay on the main tree.
func TestSetRepoInfo_NoWorktreeEndsAtProjectRoot(t *testing.T) {
	db := setupMinisterTestDB(t)
	cfg := config.DefaultCourtConfig()
	runner := &repoInfoRecordingRunner{}
	s := NewCourt(db, cfg, runner, nil)

	s.SetRepoInfo(repo.RepoInfo{ProjectRoot: "/proj"})

	require.True(t, runner.set)
	assert.Equal(t, "/proj", runner.last.EffectiveRoot())
	assert.Equal(t, "/proj", runner.last.ProjectRoot)
}

// ---------------------------------------------------------------------------
// Per-edict runners (edict 934)
// ---------------------------------------------------------------------------

// trackingRunner records Close calls and exposes a distinct label so a test can
// assert that distinct edicts get distinct runners.
type trackingRunner struct {
	label       string
	closeCalled bool
}

func (r *trackingRunner) Run(context.Context, runners.Input) (runners.Output, error) {
	return runners.Output{}, nil
}
func (r *trackingRunner) Restart(context.Context) error        { return nil }
func (r *trackingRunner) Close(context.Context) error          { r.closeCalled = true; return nil }
func (r *trackingRunner) AllowFallback(bool)                   {}
func (r *trackingRunner) RunnerType() string                   { return "tracking" }
func (r *trackingRunner) GetOS() string                        { return runtime.GOOS }
func (r *trackingRunner) SetMessageChannel(chan<- runners.Msg) {}
func (r *trackingRunner) HealthCheck(context.Context) error    { return nil }

// newEdictRunnerTestCourt builds a Court whose per-edict factory hands out
// tracking runners with unique labels.
func newEdictRunnerTestCourt(t *testing.T) (*Court, *[]*trackingRunner) {
	t.Helper()
	db := setupCourtTestDB(t)
	cfg := config.DefaultCourtConfig()
	// Avoid the connection runner so RunnerForEdict creates real edict runners.
	s := NewCourt(db, cfg, nil, slog.Default())

	var created []*trackingRunner
	s.newEdictRunner = func(edictID uint, _ repo.RepoInfo) (runners.Runner, error) {
		r := &trackingRunner{label: fmt.Sprintf("e%d", edictID)}
		created = append(created, r)
		return r, nil
	}
	// Wire the guard's resolver so rituals use it too.
	if s.ritualGuard != nil && s.ritualGuard.RitualRunner() != nil {
		s.ritualGuard.RitualRunner().SetRunnerResolver(s.RunnerForEdict, s.TouchEdictRunner)
	}
	return s, &created
}

func TestRunnerForEdict_DistinctRunnersPerEdict(t *testing.T) {
	s, created := newEdictRunnerTestCourt(t)

	r1, err := s.RunnerForEdict(42)
	require.NoError(t, err)
	require.NotNil(t, r1)

	r1again, err := s.RunnerForEdict(42)
	require.NoError(t, err)
	assert.Same(t, r1, r1again, "the same edict must reuse its cached runner")

	r2, err := s.RunnerForEdict(43)
	require.NoError(t, err)
	require.NotNil(t, r2)

	assert.NotSame(t, r1, r2, "distinct edicts must get distinct runners")
	assert.Equal(t, "e42", r1.(*trackingRunner).label)
	assert.Equal(t, "e43", r2.(*trackingRunner).label)
	assert.Len(t, *created, 2, "one runner created per edict")
}

func TestRunnerForEdict_SystemEdictUsesCourtRunner(t *testing.T) {
	db := setupCourtTestDB(t)
	courtRunner := &trackingRunner{label: "court"}
	s := NewCourt(db, config.DefaultCourtConfig(), courtRunner, slog.Default())
	s.newEdictRunner = func(uint, repo.RepoInfo) (runners.Runner, error) {
		t.Fatal("system edict must not create a per-edict runner")
		return nil, nil
	}

	for _, id := range []uint{0, 1} {
		r, err := s.RunnerForEdict(id)
		require.NoError(t, err)
		assert.Same(t, courtRunner, r, "edict %d must fall back to the Court runner", id)
	}
}

func TestCloseEdictRunner_ClosesAndForgets(t *testing.T) {
	s, _ := newEdictRunnerTestCourt(t)

	r, err := s.RunnerForEdict(42)
	require.NoError(t, err)
	tr := r.(*trackingRunner)

	s.CloseEdictRunner(context.Background(), 42)
	assert.True(t, tr.closeCalled, "Close must be called on the edict runner")

	s.edictRunnersMu.Lock()
	_, stillCached := s.edictRunners[42]
	_, stillUsed := s.edictRunnerLastUsed[42]
	s.edictRunnersMu.Unlock()
	assert.False(t, stillCached, "closed runner must be removed from the map")
	assert.False(t, stillUsed, "closed runner's last-used entry must be removed")

	// Closing again is a no-op.
	s.CloseEdictRunner(context.Background(), 42)

	// A new acquisition recreates the runner.
	r2, err := s.RunnerForEdict(42)
	require.NoError(t, err)
	assert.NotSame(t, r, r2, "a flatlined edict's runner is recreated on next use")
}

func TestFlatlineIdleRunners_ClosesIdle(t *testing.T) {
	s, _ := newEdictRunnerTestCourt(t)
	s.runnerFlatlineTimeout = time.Minute

	r, err := s.RunnerForEdict(42)
	require.NoError(t, err)
	tr := r.(*trackingRunner)

	// Backdate the last-used stamp past the timeout.
	s.edictRunnersMu.Lock()
	s.edictRunnerLastUsed[42] = time.Now().Add(-2 * time.Minute)
	s.edictRunnersMu.Unlock()

	s.flatlineIdleRunners(context.Background())
	assert.True(t, tr.closeCalled, "an idle edict container must be stopped and removed")
}

func TestFlatlineIdleRunners_SkipsActiveRitual(t *testing.T) {
	s, _ := newEdictRunnerTestCourt(t)
	s.runnerFlatlineTimeout = time.Minute

	r, err := s.RunnerForEdict(42)
	require.NoError(t, err)
	tr := r.(*trackingRunner)

	s.edictRunnersMu.Lock()
	s.edictRunnerLastUsed[42] = time.Now().Add(-2 * time.Minute)
	s.edictRunnersMu.Unlock()

	// Hold the edict's ritual lock to simulate a running ritual.
	lock := s.ritualGuard.edictRitualLock(42)
	lock.Lock()
	defer lock.Unlock()

	s.flatlineIdleRunners(context.Background())
	assert.False(t, tr.closeCalled, "a running ritual's container must never be flatlined")
}

func TestFlatlineIdleRunners_DisabledWhenZero(t *testing.T) {
	s, _ := newEdictRunnerTestCourt(t)
	s.runnerFlatlineTimeout = 0

	r, err := s.RunnerForEdict(42)
	require.NoError(t, err)
	tr := r.(*trackingRunner)

	s.edictRunnersMu.Lock()
	s.edictRunnerLastUsed[42] = time.Now().Add(-24 * time.Hour)
	s.edictRunnersMu.Unlock()

	s.flatlineIdleRunners(context.Background())
	assert.False(t, tr.closeCalled, "a zero timeout disables the flatline sweep")
}

// TestTouchEdictRunner_StampsLastUsed verifies the terminal-state stamp refreshes
// an existing edict runner's last-used time (so a just-finished ritual is not
// immediately flatlined) and is a no-op for system edicts / unknown edicts.
func TestTouchEdictRunner_StampsLastUsed(t *testing.T) {
	s, _ := newEdictRunnerTestCourt(t)

	_, err := s.RunnerForEdict(42)
	require.NoError(t, err)

	// Backdate, then touch, and confirm it moved forward.
	s.edictRunnersMu.Lock()
	s.edictRunnerLastUsed[42] = time.Now().Add(-time.Hour)
	s.edictRunnersMu.Unlock()

	s.TouchEdictRunner(42)

	s.edictRunnersMu.Lock()
	stamped := s.edictRunnerLastUsed[42]
	s.edictRunnersMu.Unlock()
	assert.WithinDuration(t, time.Now(), stamped, time.Minute,
		"TouchEdictRunner must refresh the edict's last-used time")

	// System edicts and unknown edicts must not create/stamp entries.
	s.TouchEdictRunner(1)
	s.TouchEdictRunner(999)
	s.edictRunnersMu.Lock()
	_, unknown := s.edictRunnerLastUsed[999]
	s.edictRunnersMu.Unlock()
	assert.False(t, unknown, "touching an edict without a runner must be a no-op")
}

// TestFlatlineIdleRunners_SkipsRunningRitualFromDB verifies the flatline sweep
// consults the DB, not just the in-memory lock: a ritual row left in the running
// state (e.g. recovered after a restart) protects its edict's container.
func TestFlatlineIdleRunners_SkipsRunningRitualFromDB(t *testing.T) {
	s, _ := newEdictRunnerTestCourt(t)
	s.runnerFlatlineTimeout = time.Minute

	r, err := s.RunnerForEdict(42)
	require.NoError(t, err)
	tr := r.(*trackingRunner)

	require.NotNil(t, s.ritualGuard)
	require.NoError(t, s.ritualGuard.db.Create(&RitualExecution{
		ID:         "db-running-42",
		RitualName: "db-running",
		EdictID:    42,
		Username:   s.ritualGuard.Username(),
		Project:    s.ritualGuard.Project(),
		State:      RitualStateRunning,
	}).Error)

	s.edictRunnersMu.Lock()
	s.edictRunnerLastUsed[42] = time.Now().Add(-2 * time.Minute)
	s.edictRunnersMu.Unlock()

	s.flatlineIdleRunners(context.Background())
	assert.False(t, tr.closeCalled,
		"an edict with a ritual running in the DB must never be flatlined")
}

// TestStop_ClosesEdictRunners verifies Court.Stop tears down every per-edict
// runner (and thus its container) so none is leaked on shutdown.
func TestStop_ClosesEdictRunners(t *testing.T) {
	s, created := newEdictRunnerTestCourt(t)

	_, err := s.RunnerForEdict(42)
	require.NoError(t, err)
	_, err = s.RunnerForEdict(43)
	require.NoError(t, err)
	require.Len(t, *created, 2)

	require.NoError(t, s.Stop())

	for _, r := range *created {
		assert.True(t, r.closeCalled, "every edict runner must be closed on Stop")
	}
}

// Court.ignoreCopyGlobs must fall back to the built-in safe allowlist when no
// session config is injected, and prefer the configured globs once it is. The
// fallback is what guarantees a worktree never regresses to an unbounded
// ignored-file sweep.
func TestCourt_ignoreCopyGlobs_FallbackAndConfigured(t *testing.T) {
	s := &Court{}
	assert.Equal(t, config.DefaultIgnoreCopyGlobs(), s.ignoreCopyGlobs(),
		"no session config → built-in safe allowlist")

	want := []string{"custom.env", "config/*.local"}
	s.sessionCfg = &SessionConfig{Sandbox: config.SandboxConfig{IgnoreCopyGlobs: want}}
	assert.Equal(t, want, s.ignoreCopyGlobs(), "configured globs win")

	// An explicitly empty (non-nil) allowlist is honored as "copy nothing",
	// not silently replaced by the default.
	s.sessionCfg = &SessionConfig{Sandbox: config.SandboxConfig{IgnoreCopyGlobs: []string{}}}
	assert.Empty(t, s.ignoreCopyGlobs(), "explicit empty allowlist means copy nothing")
}

// flakyMergeRunner fails the first `fails` commands containing failOn, then
// delegates to worktreeTestRunner. It models a transient torn read of a
// just-written loose object: the seal must retry once instead of aborting.
type flakyMergeRunner struct {
	worktreeTestRunner
	failOn string
	fails  int
	output string
}

func (m *flakyMergeRunner) Run(ctx context.Context, input runners.Input) (runners.Output, error) {
	// The abort only ever follows a failed rebase; never let it consume a
	// failure, or a test could count it as the retry's rebase attempt.
	if m.fails > 0 && strings.Contains(input.Command, m.failOn) && !strings.Contains(input.Command, "rebase --abort") {
		m.fails--
		m.commands = append(m.commands, input.Command)
		return runners.Output{Output: m.output, ExitCode: "1"}, nil
	}
	return m.worktreeTestRunner.Run(ctx, input)
}

// mergeTestRunnerOpts builds the canned outputs the un-failed stages of the
// merge need (stable default branch, a non-empty squash index).
func mergeTestRunnerOutputs() map[string]runners.Output {
	return map[string]runners.Output{
		"symbolic-ref":              {Output: "origin/main\n", ExitCode: "0"},
		"diff --cached --name-only": {Output: "feature.txt\n", ExitCode: "0"},
	}
}

// TestMergeEdictWorktree_RunsOnHostMutationRunnerNotCourtRunner is the
// consistency repair of incident 9f8b980c998a6c32: EnsureEdictWorktree already
// mutates the repo through a host runner, and the seal-time merge must do the
// same. The Court-wide (podman) runner is asserted to receive nothing.
func TestMergeEdictWorktree_RunsOnHostMutationRunnerNotCourtRunner(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "worktrees", "e531"), 0o755))

	courtRunner := &worktreeTestRunner{}
	mutation := &worktreeTestRunner{outputs: mergeTestRunnerOutputs()}
	s := &Court{
		logger:         slog.Default(),
		runner:         courtRunner,
		mutationRunner: mutation,
		config:         &config.CourtConfig{Username: "u", Project: "p"},
	}
	s.ritualGuard = &RitualGuard{MinisterBase: &MinisterBase{repoInfo: repo.RepoInfo{ProjectRoot: root}}}

	s.mergeEdictWorktree(context.Background(), 531)

	assert.Contains(t, mutation.joined(), "rebase", "the mutation goes through the host mutation runner")
	assert.Contains(t, mutation.joined(), "worktree remove", "the post-merge removal uses the same runner")
	assert.Empty(t, courtRunner.commands, "no repo mutation may go through the Court-wide runner")
}

// TestRepoMutationRunner_IsHost guards the production default: with no runner
// injected, repo mutations resolve to a host runner rooted at the project.
func TestRepoMutationRunner_IsHost(t *testing.T) {
	s := &Court{logger: slog.Default(), runner: &worktreeTestRunner{}}

	mutation := s.repoMutationRunner(t.TempDir())
	assert.Equal(t, "host", mutation.RunnerType(), "repo mutations must always run on the host")
}

// TestEnsureEdictWorktree_UsesMutationRunner covers the creation path: it takes
// the same host mutation runner as the seal-time merge, via the shared seam.
func TestEnsureEdictWorktree_UsesMutationRunner(t *testing.T) {
	root := t.TempDir()
	mutation := &worktreeTestRunner{}
	courtRunner := &worktreeTestRunner{}
	s := &Court{
		logger:         slog.Default(),
		runner:         courtRunner,
		mutationRunner: mutation,
		config:         &config.CourtConfig{Username: "u", Project: "p"},
	}
	s.ritualGuard = &RitualGuard{MinisterBase: &MinisterBase{repoInfo: repo.RepoInfo{ProjectRoot: root}}}

	rel, err := s.EnsureEdictWorktree(context.Background(), 531)
	require.NoError(t, err)
	assert.Equal(t, worktree.RelPath(531), rel)
	assert.Contains(t, mutation.joined(), "worktree add -B", "creation mutates via the host mutation runner")
	assert.Empty(t, courtRunner.commands, "creation must not touch the Court-wide runner")
}

// TestMergeEdictWorktree_RetriesTransientCorruptLooseObject: a rebase that
// fails once with "loose object … is corrupt" is a torn read, not disk rot, so
// the seal retries the whole operation once and lands the work — without
// publishing a failure the ruler would read as "sealed but not merged".
func TestMergeEdictWorktree_RetriesTransientCorruptLooseObject(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e531")
	require.NoError(t, os.MkdirAll(abs, 0o755))

	mutation := &flakyMergeRunner{
		worktreeTestRunner: worktreeTestRunner{outputs: mergeTestRunnerOutputs()},
		failOn:             "rebase",
		fails:              1,
		output:             "fatal: loose object 585ccf60a5ba79b40f0b63e0f0f0d0e6b1f2c3d4 is corrupt",
	}
	s := &Court{
		logger:         slog.Default(),
		runner:         &worktreeTestRunner{},
		mutationRunner: mutation,
		config:         &config.CourtConfig{Username: "u", Project: "p"},
	}
	s.ritualGuard = &RitualGuard{
		MinisterBase: &MinisterBase{repoInfo: repo.RepoInfo{ProjectRoot: root}},
		eventCh:      make(chan Event, 4),
	}

	s.mergeEdictWorktree(context.Background(), 531)

	assert.Contains(t, mutation.joined(), "merge --squash asimi/e531", "the retry lands the work")
	assert.Contains(t, mutation.joined(), "worktree remove", "the retry proceeds to retire the worktree")
	select {
	case ev := <-s.ritualGuard.eventCh:
		t.Fatalf("a transient corruption must not publish a failure, got %s", ev.Type)
	default:
	}
}

// TestMergeEdictWorktree_PersistentCorruptionStillFails: when the object is
// genuinely unreadable the retry is exhausted, the failure is published, and
// the worktree stays intact — exactly today's failure-and-leave-intact
// behaviour, with no retry loop.
func TestMergeEdictWorktree_PersistentCorruptionStillFails(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e531")
	require.NoError(t, os.MkdirAll(abs, 0o755))

	mutation := &flakyMergeRunner{
		worktreeTestRunner: worktreeTestRunner{outputs: mergeTestRunnerOutputs()},
		failOn:             "rebase",
		fails:              2,
		output:             "fatal: loose object 585ccf60a5ba79b40f0b63e0f0f0d0e6b1f2c3d4 is corrupt",
	}
	s := &Court{
		logger:         slog.Default(),
		runner:         &worktreeTestRunner{},
		mutationRunner: mutation,
		config:         &config.CourtConfig{Username: "u", Project: "p"},
	}
	s.ritualGuard = &RitualGuard{
		MinisterBase: &MinisterBase{repoInfo: repo.RepoInfo{ProjectRoot: root}},
		eventCh:      make(chan Event, 4),
	}

	s.mergeEdictWorktree(context.Background(), 531)

	rebaseAttempts := 0
	for _, cmd := range mutation.commands {
		if strings.Contains(cmd, " rebase ") && !strings.Contains(cmd, "rebase --abort") {
			rebaseAttempts++
		}
	}
	assert.Equal(t, 2, rebaseAttempts, "the operation is retried exactly once, never looped")
	assert.NotContains(t, mutation.joined(), "merge --squash", "no merge after a failed retry")
	assert.NotContains(t, mutation.joined(), "worktree remove", "worktree is left intact on failure")
	_, err := os.Stat(abs)
	assert.NoError(t, err, "persistent corruption leaves the worktree for the forge")

	select {
	case ev := <-s.ritualGuard.eventCh:
		assert.Equal(t, storage.EventWorktreeMergeFailed, ev.Type, "a second failure is announced")
		reason, _ := ev.Payload["reason"].(string)
		assert.Contains(t, reason, "is corrupt", "the reason carries the git error")
	default:
		t.Fatal("expected EventWorktreeMergeFailed after a failed retry")
	}
}

// healingRunner runs real git through a host runner and, the first time a
// command reports a torn loose object, restores the object's bytes — modelling
// the incident's ephemeral torn read (byte-valid on disk, unreadable on the
// first read, readable afterwards).
type healingRunner struct {
	runners.Runner
	heal   func()
	healed bool
}

func (h *healingRunner) Run(ctx context.Context, in runners.Input) (runners.Output, error) {
	out, err := h.Runner.Run(ctx, in)
	if !h.healed && strings.Contains(out.Output, "is corrupt") {
		h.healed = true
		h.heal()
	}
	return out, err
}

// TestMergeEdictWorktree_RetriesAfterMidReplayCorruptLooseObject is the real-git
// regression the mock-based retry tests cannot see. The incident's torn read is
// not limited to the last object a rebase writes: git writes a tree for every
// replayed commit, and a rebase that hits a corrupt object mid-replay leaves
// .git/rebase-merge behind. The seal's retry must therefore not be a bare re-run
// of `git rebase` — git refuses to start a rebase while another is in progress,
// so the second attempt is a no-op that returns the same error and wedges the
// worktree. With the corrupt object healed before the retry (a transient read),
// the seal must still land the work.
func TestMergeEdictWorktree_RetriesAfterMidReplayCorruptLooseObject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".agents/worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "base"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "init")

	// The forge's worktree carries several commits, so the rebase replays more
	// than one pick and reads an intermediate tree.
	abs := filepath.Join(root, worktree.RelPath(531))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	runGit(t, root, "worktree", "add", "-B", worktree.Branch(531), abs)
	for i, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(abs, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, abs, "add", ".")
		runGit(t, abs, "commit", "-m", fmt.Sprintf("c%d", i+1))
	}
	// main diverges so the branch must be replayed.
	if err := os.WriteFile(filepath.Join(root, "mainonly"), []byte("m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "m1")

	// A torn read of an intermediate tree: the first replayed commit's tree.
	treeSHA := strings.TrimSpace(gitOutput(t, abs, "rev-parse", worktree.Branch(531)+"~2^{tree}"))
	// Obtain canonical bytes through git so this works whether the object is
	// loose or packed (the GitHub runner packs it, where a raw path read fails),
	// then encode a proper loose object ourselves: the on-disk format is
	// zlib-compressed "<type> <size>\0<content>". A loose object shadows a
	// packed one, so corrupting this path tears the read on any git.
	content := []byte(gitOutput(t, abs, "cat-file", "tree", treeSHA))
	var packed bytes.Buffer
	zw := zlib.NewWriter(&packed)
	fmt.Fprintf(zw, "tree %d\x00", len(content))
	zw.Write(content)
	require.NoError(t, zw.Close())
	good := packed.Bytes()

	objDir := strings.TrimSpace(gitOutput(t, abs, "rev-parse", "--git-path", "objects"))
	if !filepath.IsAbs(objDir) {
		objDir = filepath.Join(abs, objDir)
	}
	objPath := filepath.Join(objDir, treeSHA[:2], treeSHA[2:])
	require.NoError(t, os.MkdirAll(filepath.Dir(objPath), 0o755))
	require.NoError(t, os.WriteFile(objPath, good, 0o644))
	require.NoError(t, os.WriteFile(objPath, []byte("garbage"), 0o644))

	mutation := &healingRunner{
		Runner: runners.NewHostRunner(0, root),
		heal:   func() { require.NoError(t, os.WriteFile(objPath, good, 0o644)) },
	}
	s := &Court{
		logger:         slog.Default(),
		runner:         &worktreeTestRunner{},
		mutationRunner: mutation,
		config:         &config.CourtConfig{Username: "u", Project: "p"},
	}
	s.ritualGuard = &RitualGuard{
		MinisterBase: &MinisterBase{repoInfo: repo.RepoInfo{ProjectRoot: root}},
		eventCh:      make(chan Event, 4),
	}

	require.NoError(t, s.mergeEdictWorktree(context.Background(), 531),
		"a transient torn read must not abort the seal")

	select {
	case ev := <-s.ritualGuard.eventCh:
		t.Fatalf("a transient torn read must not publish %s: %v", ev.Type, ev.Payload)
	default:
	}

	// The branch's work landed on main and the worktree was retired.
	for _, name := range []string{"a", "b", "c"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("branch work %q did not land on main after the retry: %v", name, err)
		}
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Errorf("worktree must be removed after a successful retry, stat err = %v", err)
	}
}

// TestMergeEdictWorktree_AbortsRebaseStateBeforeRetry pins the ordering the
// mid-replay regression depends on: a rebase that dies on a torn loose object
// may have left .git/rebase-merge behind (git writes a tree per replayed
// commit), and git refuses a fresh rebase while that state exists. The retry
// must therefore clear the state with `git rebase --abort` BEFORE re-running the
// rebase — a bare re-run is a refused no-op that wedges the worktree.
func TestMergeEdictWorktree_AbortsRebaseStateBeforeRetry(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "worktrees", "e531"), 0o755))

	mutation := &flakyMergeRunner{
		worktreeTestRunner: worktreeTestRunner{outputs: mergeTestRunnerOutputs()},
		failOn:             "rebase",
		fails:              1,
		output:             "fatal: loose object 585ccf60 is corrupt",
	}
	s := &Court{
		logger:         slog.Default(),
		runner:         &worktreeTestRunner{},
		mutationRunner: mutation,
		config:         &config.CourtConfig{Username: "u", Project: "p"},
	}
	s.ritualGuard = &RitualGuard{
		MinisterBase: &MinisterBase{repoInfo: repo.RepoInfo{ProjectRoot: root}},
		eventCh:      make(chan Event, 4),
	}

	s.mergeEdictWorktree(context.Background(), 531)

	abortIdx, rebaseIdxs := -1, []int{}
	for i, cmd := range mutation.commands {
		if strings.Contains(cmd, "rebase --abort") {
			abortIdx = i
			continue
		}
		if strings.Contains(cmd, " rebase ") {
			rebaseIdxs = append(rebaseIdxs, i)
		}
	}
	require.Len(t, rebaseIdxs, 2, "exactly one retry: %v", mutation.commands)
	require.NotEqual(t, -1, abortIdx, "stale rebase state must be aborted before the retry")
	assert.Less(t, abortIdx, rebaseIdxs[1],
		"--abort must precede the second rebase, commands:\n%s", mutation.joined())
	assert.Less(t, rebaseIdxs[0], abortIdx,
		"the abort is only needed after a failed rebase")
}

// TestMergeEdictWorktree_AbortFailureDoesNotWedgeTheSeal: if the stale rebase
// state cannot be cleared, the seal must not pretend the retry happened — it
// skips the second attempt and falls through to the normal failure-and-leave-
// intact path, with the worktree still present for the forge.
func TestMergeEdictWorktree_AbortFailureDoesNotWedgeTheSeal(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e531")
	require.NoError(t, os.MkdirAll(abs, 0o755))

	mutation := &flakyMergeRunner{
		worktreeTestRunner: worktreeTestRunner{outputs: map[string]runners.Output{
			"symbolic-ref":   {Output: "origin/main\n", ExitCode: "0"},
			"rebase --abort": {Output: "error: cannot abort", ExitCode: "1"},
		}},
		failOn: "rebase main",
		fails:  1,
		output: "fatal: loose object 585ccf60 is corrupt",
	}
	s := &Court{
		logger:         slog.Default(),
		runner:         &worktreeTestRunner{},
		mutationRunner: mutation,
		config:         &config.CourtConfig{Username: "u", Project: "p"},
	}
	s.ritualGuard = &RitualGuard{
		MinisterBase: &MinisterBase{repoInfo: repo.RepoInfo{ProjectRoot: root}},
		eventCh:      make(chan Event, 4),
	}

	s.mergeEdictWorktree(context.Background(), 531)

	rebaseIdxs := 0
	for _, cmd := range mutation.commands {
		if strings.Contains(cmd, " rebase ") && !strings.Contains(cmd, "rebase --abort") {
			rebaseIdxs++
		}
	}
	assert.Equal(t, 1, rebaseIdxs, "a failed abort must not attempt the rebase again")

	select {
	case ev := <-s.ritualGuard.eventCh:
		assert.Equal(t, storage.EventWorktreeMergeFailed, ev.Type)
	default:
		t.Fatal("expected EventWorktreeMergeFailed when the stale state cannot be cleared")
	}
	_, err := os.Stat(abs)
	assert.NoError(t, err, "worktree stays intact for the forge")
}
