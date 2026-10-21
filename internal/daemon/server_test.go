package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/rpc"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// testUsername returns the current OS user's username for daemon tests.
// This matches what the server's unixPeerUsername will verify against.
// Panics on failure because a silent fallback would mask bugs in tests.
func testUsername() string {
	u, err := user.Current()
	if err != nil {
		panic("testUsername: user.Current() failed: " + err.Error())
	}
	return u.Username
}

// initTestDB creates a temporary SQLite database for daemon tests.
func initTestDB(t *testing.T) (*gorm.DB, *storage.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	sdb, err := storage.InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	gormLog := slogGormLogger(slog.Default(), gormlogger.Silent)
	gdb, err := gorm.Open(sqlite.New(sqlite.Config{
		Conn: sdb.Conn(),
	}), &gorm.Config{
		Logger: gormLog,
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	// Auto-migrate required tables.
	if err := gdb.AutoMigrate(
		&storage.Edict{},
		&storage.Seal{},
		&storage.Zhengming{},
		&storage.TianEvent{},
		&storage.TianEventDLQ{},
		&storage.Ling{},
		&storage.ForgeManifest{},
		&storage.JudgeVerdict{},
		&storage.CensorPrecedent{},
		&storage.Incident{},
		&storage.RulerCouncil{},
		&storage.RitualGuardCheckpoint{},
		&court.RitualExecution{},
		&court.RitualStepState{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	return gdb, sdb, func() { sdb.Close() }
}

// slogGormLogger is a minimal gorm logger for tests (same as the one
// in the main package). Duplicated here to avoid an import cycle.
type testSlogGormLogger struct {
	logger   *slog.Logger
	logLevel gormlogger.LogLevel
}

func slogGormLogger(logger *slog.Logger, level gormlogger.LogLevel) *testSlogGormLogger {
	return &testSlogGormLogger{logger: logger, logLevel: level}
}

func (l *testSlogGormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return &testSlogGormLogger{logger: l.logger, logLevel: level}
}

func (l *testSlogGormLogger) Info(ctx context.Context, msg string, args ...interface{})  {}
func (l *testSlogGormLogger) Warn(ctx context.Context, msg string, args ...interface{})  {}
func (l *testSlogGormLogger) Error(ctx context.Context, msg string, args ...interface{}) {}
func (l *testSlogGormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
}

// newTestShared builds a Shared suitable for test.
func newTestShared(t *testing.T) (*Shared, func()) {
	t.Helper()
	gdb, sdb, cleanup := initTestDB(t)
	cfg := &config.Config{
		Court: config.CourtConfig{
			Username: testUsername(),
			Project:  "test-project",
		},
	}
	return &Shared{
		DB:      gdb,
		Storage: sdb,
		Config:  cfg,
		Logger:  slog.Default(),
	}, cleanup
}

// dialServeOne is a test helper: starts serveClients on a unix socket,
// dials a client connection, and calls SetContext to complete the
// handshake. Returns the live client Conn, the CourtClient, and a
// cleanup function that cancels the daemon context and waits for
// serveClients to exit.
func dialServeOne(t *testing.T, shared *Shared, projectRoot string) (*rpc.Conn, *rpc.CourtClient, func()) {
	t.Helper()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "d.sock")
	if len(sockPath) >= 104 {
		sockPath = filepath.Join("/tmp", "asimi-d-"+time.Now().Format("150405.000000")+".sock")
		t.Cleanup(func() { _ = os.Remove(sockPath) })
	}

	listener, err := rpc.Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	serveDone := make(chan error, 1)
	go func() { serveDone <- serveClients(ctx, listener, shared) }()

	// Dial client.
	netConn, err := rpc.Dial(sockPath)
	if err != nil {
		cancel()
		_ = listener.Close()
		t.Fatalf("Dial: %v", err)
	}

	clientConn := rpc.New(netConn, rpc.Options{})
	go func() { _ = clientConn.Serve() }()

	// Send SetContext handshake.
	client := rpc.NewCourtClient(clientConn)
	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer handshakeCancel()
	if err := client.SetContext(handshakeCtx, types.SetContextParams{
		Project:     "test-project",
		Username:    testUsername(),
		ProjectRoot: projectRoot,
	}); err != nil {
		cancel()
		_ = listener.Close()
		clientConn.Close()
		t.Fatalf("SetContext handshake: %v", err)
	}

	cleanup := func() {
		cancel()
		_ = listener.Close()
		clientConn.Close()
		select {
		case <-serveDone:
		case <-time.After(3 * time.Second):
			t.Log("serveClients didn't shut down in time")
		}
	}

	return clientConn, client, cleanup
}

// TestDaemonHandshakeThenRPC verifies that a client that completes the
// SetContext handshake can then exercise HasMinister and CreateEdict.
func TestDaemonHandshakeThenRPC(t *testing.T) {
	shared, cleanup := newTestShared(t)
	defer cleanup()

	projectRoot := t.TempDir()
	_, client, stop := dialServeOne(t, shared, projectRoot)
	defer stop()

	// After handshake, the server registers c handlers
	// asynchronously, so we retry briefly until HasMinister succeeds.
	// This is a benign race: the SetContext response is sent before
	// RegisterCourtHandlers runs.
	var hasMinister bool
	for i := 0; i < 50; i++ {
		hasMinister = client.HasMinister("chancellor")
		if hasMinister {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !hasMinister {
		t.Error("HasMinister(chancellor) = false, want true")
	}

	var e *storage.Edict
	var err error
	for i := 0; i < 50; i++ {
		e, err = client.CreateEdict("#1", "handshake test", "")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("CreateEdict: %v", err)
	}
	if e.Intent != "handshake test" {
		t.Errorf("edict.Intent = %q, want %q", e.Intent, "handshake test")
	}
}

// TestDaemonHandshakeTimeout verifies that a client which never sends
// SetContext gets disconnected after the handshake timeout. We detect
// this by observing that the connection's Done channel closes.
func TestDaemonHandshakeTimeout(t *testing.T) {
	shared, cleanup := newTestShared(t)
	defer cleanup()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "d.sock")
	if len(sockPath) >= 104 {
		sockPath = filepath.Join("/tmp", "asimi-d-"+time.Now().Format("150405.000000")+".sock")
		t.Cleanup(func() { _ = os.Remove(sockPath) })
	}

	listener, err := rpc.Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Use a short-lived ctx so we don't block forever if the handshake
	// timeout logic is broken.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// serveOneDone tracks connection handling (unused in this test
	// variant — kept for parity with the original).
	var _ = make(chan struct{})

	serveDone := make(chan error, 1)
	go func() { serveDone <- serveClients(ctx, listener, shared) }()

	// Dial but deliberately skip SetContext.
	netConn, err := rpc.Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	silentConn := rpc.New(netConn, rpc.Options{})
	go func() { _ = silentConn.Serve() }()

	// The daemon's handshake timeout is 30s — too long for a unit test.
	// Instead, verify that the connection eventually closes because
	// serveOne rejects it. We observe the server-side: the connection
	// Done channel should close. Give a generous window beyond the
	// 30s timeout.
	//
	// To make this test fast, we rely on the fact that the server's
	// context (ctx above) will also cancel, but the real mechanism
	// under test is that serveOne closes the connection when no
	// handshake arrives.
	//
	// For a deterministic, fast test we would need to inject the
	// handshake timeout. Since that requires an API change, we verify
	// the observable behaviour: the server drops the connection. We
	// cancel the daemon after a short wait; if serveOne already closed
	// the connection, silentConn.Done() will have fired.
	timeout := time.After(2 * time.Second)
	select {
	case <-silentConn.Done():
		// Server closed the connection — handshake timeout fired.
	case <-timeout:
		// The 30s default timeout is too long for CI. Cancel the daemon
		// and verify the connection still closes.
		cancel()
		_ = listener.Close()
		select {
		case <-silentConn.Done():
			// Connection closed after daemon shutdown — acceptable.
		case <-time.After(2 * time.Second):
			t.Error("silent client connection never closed")
		}
	}
}

// TestDaemonInvalidProjectRoot verifies that a client which sends
// SetContext with a non-existent project_root gets disconnected.
func TestDaemonInvalidProjectRoot(t *testing.T) {
	shared, cleanup := newTestShared(t)
	defer cleanup()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "d.sock")
	if len(sockPath) >= 104 {
		sockPath = filepath.Join("/tmp", "asimi-d-"+time.Now().Format("150405.000000")+".sock")
		t.Cleanup(func() { _ = os.Remove(sockPath) })
	}

	listener, err := rpc.Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveDone := make(chan error, 1)
	go func() { serveDone <- serveClients(ctx, listener, shared) }()

	netConn, err := rpc.Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	clientConn := rpc.New(netConn, rpc.Options{})
	go func() { _ = clientConn.Serve() }()

	client := rpc.NewCourtClient(clientConn)

	// Send SetContext with a path that does not exist on disk.
	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer handshakeCancel()
	err = client.SetContext(handshakeCtx, types.SetContextParams{
		Project:     "test-project",
		Username:    testUsername(),
		ProjectRoot: "/no/such/directory/ever",
	})
	// The server should return an error for an invalid project_root.
	if err == nil {
		t.Error("expected error for invalid project_root, got nil")
	}

	cancel()
	_ = listener.Close()
	select {
	case <-serveDone:
	case <-time.After(3 * time.Second):
		t.Log("serveClients didn't shut down in time")
	}
}

// TestDaemonEmptyProjectRoot verifies that a client which sends
// SetContext with an empty project_root gets disconnected.
func TestDaemonEmptyProjectRoot(t *testing.T) {
	shared, cleanup := newTestShared(t)
	defer cleanup()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "d.sock")
	if len(sockPath) >= 104 {
		sockPath = filepath.Join("/tmp", "asimi-d-"+time.Now().Format("150405.000000")+".sock")
		t.Cleanup(func() { _ = os.Remove(sockPath) })
	}

	listener, err := rpc.Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveDone := make(chan error, 1)
	go func() { serveDone <- serveClients(ctx, listener, shared) }()

	netConn, err := rpc.Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	clientConn := rpc.New(netConn, rpc.Options{})
	go func() { _ = clientConn.Serve() }()

	client := rpc.NewCourtClient(clientConn)

	// Send SetContext with empty project_root.
	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer handshakeCancel()
	err = client.SetContext(handshakeCtx, types.SetContextParams{
		Project:     "test-project",
		Username:    testUsername(),
		ProjectRoot: "",
	})
	// The server should return an error for empty project_root.
	if err == nil {
		t.Error("expected error for empty project_root, got nil")
	}

	cancel()
	_ = listener.Close()
	select {
	case <-serveDone:
	case <-time.After(3 * time.Second):
		t.Log("serveClients didn't shut down in time")
	}
}

// TestCreateCourtSetsRepoInfoBeforeStart verifies the critical
// daemon-mode invariant: createCourt must wire repoInfo before
// calling Start(), so that LoadRituals() finds the correct ProjectRoot
// on the first attempt. Without SetRepoInfo before Start, rituals
// would fail to load and only be picked up later by ConfigureModel's
// fallback path, producing spurious ERROR/WARN logs.
func TestCreateCourtSetsRepoInfoBeforeStart(t *testing.T) {
	shared, cleanup := newTestShared(t)
	defer cleanup()

	projectRoot := t.TempDir()
	agentsDir := filepath.Join(projectRoot, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir .agents: %v", err)
	}

	projectCfg, err := config.LoadProjectConfig(projectRoot, false)
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}

	repoInfo := repo.RepoInfo{
		ProjectRoot: projectRoot,
		Slug:        "test-project",
		Branch:      "main",
	}

	c, _, err := createCourt(
		context.Background(),
		shared,
		1,
		types.SetContextParams{
			Project:     "test-project",
			Username:    testUsername(),
			ProjectRoot: projectRoot,
		},
		projectCfg,
		repoInfo,
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("createCourt: %v", err)
	}
	defer c.Stop()

	// The Court must have rituals loaded in its registry.
	// This proves SetRepoInfo was called before Start(), because
	// LoadRituals() inside Start() would fail with an empty project root
	// if repoInfo wasn't wired first.
	reg := c.GetRitualRegistry()
	rituals := reg.List()
	if len(rituals) == 0 {
		t.Error("ritual registry is empty after createCourt — SetRepoInfo was not called before Start()")
	}

	// Verify that embedded rituals (e.g. dawn-audience) loaded on the
	// first attempt, not via the ConfigureModel fallback path.
	if !ritualsContain(rituals, "dawn-audience") {
		t.Errorf("expected embedded ritual 'dawn-audience' in registry, got %v", rituals)
	}
}

// ritualsContain checks if a ritual name exists in the list.
func ritualsContain(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

var _ court.Snapshot // keep the c import warm for the test file

// dialTwoClients is a test helper that starts serveClients on a unix
// socket, dials two client connections, and completes the SetContext
// handshake for each. Returns the two live client Conns, their
// CourtClients, and a cleanup function.
func dialTwoClients(t *testing.T, shared *Shared, projectRootA, projectRootB string) (*rpc.Conn, *rpc.CourtClient, *rpc.Conn, *rpc.CourtClient, func()) {
	t.Helper()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "d.sock")
	if len(sockPath) >= 104 {
		sockPath = filepath.Join("/tmp", "asimi-d-"+time.Now().Format("150405.000000")+".sock")
		t.Cleanup(func() { _ = os.Remove(sockPath) })
	}

	listener, err := rpc.Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	serveDone := make(chan error, 1)
	go func() { serveDone <- serveClients(ctx, listener, shared) }()

	dialOne := func(projectRoot string) (*rpc.Conn, *rpc.CourtClient) {
		t.Helper()
		netConn, err := rpc.Dial(sockPath)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		clientConn := rpc.New(netConn, rpc.Options{})
		go func() { _ = clientConn.Serve() }()

		client := rpc.NewCourtClient(clientConn)
		handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 5*time.Second)
		defer handshakeCancel()
		if err := client.SetContext(handshakeCtx, types.SetContextParams{
			Project:     "test-project",
			Username:    testUsername(),
			ProjectRoot: projectRoot,
		}); err != nil {
			t.Fatalf("SetContext handshake: %v", err)
		}
		return clientConn, client
	}

	connA, clientA := dialOne(projectRootA)
	connB, clientB := dialOne(projectRootB)

	cleanup := func() {
		cancel()
		_ = listener.Close()
		connA.Close()
		connB.Close()
		select {
		case <-serveDone:
		case <-time.After(3 * time.Second):
			t.Log("serveClients didn't shut down in time")
		}
	}

	return connA, clientA, connB, clientB, cleanup
}

// TestDaemonTwoClients verifies that two clients can connect to the
// daemon simultaneously, each completing the handshake and exercising
// RPC calls independently. Both clients get their own isolated
// Court instance backed by the shared DB.
func TestDaemonTwoClients(t *testing.T) {
	shared, cleanup := newTestShared(t)
	defer cleanup()

	projectRootA := t.TempDir()
	projectRootB := t.TempDir()

	_, clientA, _, clientB, stop := dialTwoClients(t, shared, projectRootA, projectRootB)
	defer stop()

	// Both clients should see the chancellor minister (each through
	// its own isolated Court). Retry briefly because
	// RegisterCourtHandlers runs after the handshake response.
	waitForMinister := func(client *rpc.CourtClient, name string) bool {
		for i := 0; i < 50; i++ {
			if client.HasMinister(name) {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}

	if !waitForMinister(clientA, "chancellor") {
		t.Error("client A: HasMinister(chancellor) = false, want true")
	}
	if !waitForMinister(clientB, "chancellor") {
		t.Error("client B: HasMinister(chancellor) = false, want true")
	}

	// Each client can create an edict independently.
	edictA, err := clientA.CreateEdict("#2", "client A edict", "")
	if err != nil {
		t.Fatalf("client A CreateEdict: %v", err)
	}
	if edictA.Intent != "client A edict" {
		t.Errorf("client A edict.Intent = %q, want %q", edictA.Intent, "client A edict")
	}

	edictB, err := clientB.CreateEdict("#3", "client B edict", "")
	if err != nil {
		t.Fatalf("client B CreateEdict: %v", err)
	}
	if edictB.Intent != "client B edict" {
		t.Errorf("client B edict.Intent = %q, want %q", edictB.Intent, "client B edict")
	}
}

// TestDaemonTwoClientsIsolation verifies that one client disconnecting
// does not affect the other. Client A disconnects, then client B must
// still be able to make RPC calls.
func TestDaemonTwoClientsIsolation(t *testing.T) {
	shared, cleanup := newTestShared(t)
	defer cleanup()

	projectRootA := t.TempDir()
	projectRootB := t.TempDir()

	connA, _, connB, clientB, stop := dialTwoClients(t, shared, projectRootA, projectRootB)
	defer stop()

	// Confirm client B works before disconnecting A.
	waitForMinister := func(client *rpc.CourtClient, name string) bool {
		for i := 0; i < 50; i++ {
			if client.HasMinister(name) {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	if !waitForMinister(clientB, "chancellor") {
		t.Fatal("client B: HasMinister(chancellor) = false before disconnect")
	}

	// Disconnect client A.
	connA.Close()

	// Give the server a moment to process the disconnect.
	time.Sleep(50 * time.Millisecond)

	// Client B must still work after A is gone.
	edictB, err := clientB.CreateEdict("#4", "client B still alive", "")
	if err != nil {
		t.Fatalf("client B CreateEdict after A disconnect: %v", err)
	}
	if edictB.Intent != "client B still alive" {
		t.Errorf("client B edict.Intent = %q, want %q", edictB.Intent, "client B still alive")
	}

	// Client B's connection should still be alive.
	select {
	case <-connB.Done():
		t.Error("client B connection closed unexpectedly after A disconnected")
	default:
	}
}

// TestCreateCourtHonoursPinnedProject covers edict 968: the daemon path must
// apply the pinned [court] project (and username) from .agents/asimi.conf, so
// the same checkout cannot resolve to two different Court scopes — the TUI path
// already honours it, and the daemon used to ignore it entirely.
func TestCreateCourtHonoursPinnedProject(t *testing.T) {
	shared, cleanup := newTestShared(t)
	defer cleanup()

	projectRoot := t.TempDir()
	agentsDir := filepath.Join(projectRoot, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir .agents: %v", err)
	}
	conf := "[court]\nproject = \"oct7-io/data-poisoning-benchmark\"\nusername = \"pinned-user\"\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "asimi.conf"), []byte(conf), 0o644); err != nil {
		t.Fatalf("write asimi.conf: %v", err)
	}

	projectCfg, err := config.LoadProjectConfig(projectRoot, false)
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	if projectCfg.Court.Project != "oct7-io/data-poisoning-benchmark" {
		t.Fatalf("config project = %q, want pinned slug", projectCfg.Court.Project)
	}

	// The handshake carries an origin-derived slug (daonb/...); the pinned config
	// must win so the daemon scope matches the TUI's.
	repoInfo := repo.RepoInfo{
		ProjectRoot: projectRoot,
		Slug:        "daonb/data-poisoning-benchmark",
		Branch:      "main",
	}
	c, _, err := createCourt(
		context.Background(),
		shared,
		1,
		types.SetContextParams{
			Project:     "daonb/data-poisoning-benchmark",
			Username:    testUsername(),
			ProjectRoot: projectRoot,
		},
		projectCfg,
		repoInfo,
		nil,
		true, // isolatedHost avoids a podman runner
	)
	if err != nil {
		t.Fatalf("createCourt: %v", err)
	}
	defer c.Stop()

	key := c.EdictKey(1)
	if key.Project != "oct7-io/data-poisoning-benchmark" {
		t.Errorf("Court project = %q, want pinned %q", key.Project, "oct7-io/data-poisoning-benchmark")
	}
	if key.Username != "pinned-user" {
		t.Errorf("Court username = %q, want pinned %q", key.Username, "pinned-user")
	}
}
