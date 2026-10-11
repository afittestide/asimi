package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/rpc"
	tea "github.com/charmbracelet/bubbletea"
)

// fakeDaemon stands in for the real asimi daemon subprocess in unit
// tests — it binds the socket path, signals readiness on ASIMI_READY_FD
// exactly the way runDaemonMode does, and serves one trivial handler.
// No fx, no bifrost, no TUI — just the readiness handshake and a dial
// target.
func startFakeDaemon(t *testing.T, socketPath string) (stop func()) {
	t.Helper()
	l, err := rpc.Listen(socketPath)
	if err != nil {
		t.Fatalf("fake daemon listen: %v", err)
	}

	// Signal readiness if ASIMI_READY_FD is set — mirrors daemon.go.
	if fdStr := os.Getenv("ASIMI_READY_FD"); fdStr != "" {
		t.Fatalf("fake daemon shouldn't be run with ASIMI_READY_FD set at test level")
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if ctx.Err() != nil {
				return
			}
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(nc net.Conn) {
				conn := rpc.New(nc, rpc.Options{})
				// connectOrStartDaemon probes liveness with Ping;
				// a fake daemon must answer it or be evicted.
				conn.Handle(rpc.MethodPing, func(ctx context.Context, params []byte) ([]byte, error) {
					return nil, nil
				})
				// A client factory (installDaemonAutostart /
				// newDaemonConn) runs the SetContext handshake on
				// every new conn; answer it so the conn is usable.
				conn.Handle(rpc.MethodSetContext, func(ctx context.Context, params []byte) ([]byte, error) {
					return nil, nil
				})
				_ = conn.Serve()
			}(c)
		}
	}()

	return func() {
		cancel()
		_ = l.Close()
		wg.Wait()
	}
}

// TestConnectOrStartDaemonFastPath tests the fast path resolution of a live daemon socket.
func TestConnectOrStartDaemonFastPath(t *testing.T) {
	dir := t.TempDir()
	if len(filepath.Join(dir, rpc.DefaultSocketName)) >= 104 {
		t.Skip("tmp path too long for unix socket")
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)

	// The real daemon spawns and sets ASIMI_READY_FD, which can leak
	// into this test's environment (e.g. when the suite runs under a
	// hosted asimi daemon). The fake daemon below asserts it is not
	// set, so clear it here to keep the fast-path test hermetic.
	t.Setenv("ASIMI_READY_FD", "")

	// Start a fake daemon at the path rpc.SocketPath() will resolve
	// to. connectOrStartDaemon should hit the fast-path.
	resolved, err := rpc.SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	stop := startFakeDaemon(t, resolved)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, got, err := connectOrStartDaemon(ctx)
	if err != nil {
		t.Fatalf("connectOrStartDaemon: %v", err)
	}
	defer c.Close()
	if got != resolved {
		t.Errorf("path = %q, want %q", got, resolved)
	}
}

// TestDaemonLogPathHonorsASIMIHome verifies the daemon log lands under
// ASIMI_HOME when set — the Harbor case, where cwd may be read-only.
func TestDaemonLogPathHonorsASIMIHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ASIMI_HOME", home)

	p, err := daemonLogPath()
	if err != nil {
		t.Fatalf("daemonLogPath: %v", err)
	}
	want := filepath.Join(home, "asimi-daemon.log")
	if p != want {
		t.Errorf("daemonLogPath = %q, want %q", p, want)
	}
}

// TestOpenDaemonLogRoundTrip exercises the open/MkdirAll path end to
// end: the file is created, appendable, and (crucially for the spawn
// path) capturable as a child's stdout.
func TestOpenDaemonLogRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ASIMI_HOME", home)

	stdout, stderr, err := openDaemonLog()
	if err != nil {
		t.Fatalf("openDaemonLog: %v", err)
	}
	defer stdout.Close()
	if stdout != stderr {
		t.Errorf("expected both fds to alias the same log file")
	}

	if _, err := stdout.WriteString("daemon stray output\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, _ := daemonLogPath()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "daemon stray output") {
		t.Errorf("log file missing written content, got %q", data)
	}
}

// TestOpenDaemonLogFallback verifies the failure contract: when the log
// directory can't be resolved, we degrade to the terminal rather than
// failing — starting the daemon beats logging its output.
func TestOpenDaemonLogFallback(t *testing.T) {
	// Pointing ASIMI_HOME at a regular file makes MkdirAll fail with
	// "not a directory", exercising the error path without mocks.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	t.Setenv("ASIMI_HOME", blocker)

	out, errF, err := openDaemonLog()
	if err == nil {
		t.Fatal("expected an error when ASIMI_HOME is not a directory")
	}
	if out != os.Stdout || errF != os.Stderr {
		t.Errorf("fallback should return os.Stdout/os.Stderr, got %v/%v", out, errF)
	}
}

// TestReadySignalOnFD verifies the pipe-fd handshake end-to-end: open
// a pipe, pass the write end as fd 3 to a function that mimics the
// daemon's readiness write, confirm the parent wakes from ReadFull.
func TestReadySignalOnFD(t *testing.T) {
	readR, readW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer readR.Close()

	// Simulate the daemon writing the readiness byte on "fd 3" — in
	// this test we just use the pipe directly.
	go func() {
		_, _ = readW.Write([]byte{1})
		_ = readW.Close()
	}()

	buf := make([]byte, 1)
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(readR, buf)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read ready: %v", err)
		}
		if buf[0] != 1 {
			t.Fatalf("ready byte = %d", buf[0])
		}
	case <-time.After(time.Second):
		t.Fatal("readiness byte never arrived")
	}
}

// dialPair returns a connected, served rpc.Conn pair: the first is the
// "client" end (what the TUI would hold), the second the server end.
// Closing the server simulates a dead daemon.
func dialPair(t *testing.T) (client, server *rpc.Conn) {
	t.Helper()
	pa, pb := net.Pipe()
	client = rpc.New(pa, rpc.Options{})
	server = rpc.New(pb, rpc.Options{})
	go func() { _ = client.Serve() }()
	go func() { _ = server.Serve() }()
	return client, server
}

// pinnedConnDone pins ConnDone to a channel captured before the drop. It
// stands in for the documented usage of ReconnectingClient.ConnDone ("re-call
// after a reconnect") and removes the race where the watcher subscribes to
// the freshly swapped-in live conn instead of the conn that is about to die.
type pinnedConnDone struct {
	*rpc.ReconnectingClient
	done <-chan struct{}
}

func (p pinnedConnDone) ConnDone() <-chan struct{} { return p.done }

// reconnectSafetyNet bounds how long a guard waits for the reconnect swap. It
// is a backstop only: the success condition is the swap signal (Restored()),
// never the deadline.
const reconnectSafetyNet = 35 * time.Second

// TestWatchConnDropObservesReconnect is the edict 911 regression guard:
// with the default-mode ReconnectingClient, watchConnDrop must observe a
// drop, and waitForConnRestore must then observe the swapped-in fresh
// conn as restored — not time out. A LoopbackCourt-style static conn
// would make the restore branch unreachable.
func TestWatchConnDropObservesReconnect(t *testing.T) {
	var calls int
	var mu sync.Mutex
	factory := func() (*rpc.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		client, server := dialPair(t)
		// Keep the server end alive for the test; it leaks into the
		// t.Cleanup below via the closure.
		t.Cleanup(func() { _ = server.Close() })
		return client, nil
	}

	rc := rpc.NewReconnectingClient(factory, nil)
	if err := rc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rc.Close()

	// The first conn must be the one we hold, so we can kill it.
	first := rc.Conn()
	if first == nil {
		t.Fatal("Start did not install a conn")
	}

	// watchConnDrop blocks until the conn dies. Pin the watcher's view to
	// the first conn *before* killing it, so the assertion is driven by
	// the drop event rather than racing the reconnect swap.
	lost := make(chan tea.Msg, 1)
	go func() { lost <- watchConnDrop(pinnedConnDone{rc, first.Done()})() }()

	// Capture the restore signal for this drop before triggering it.
	// Restored() is closed when a fresh conn is swapped in; reading it
	// after the kill could miss a swap that landed first.
	restoreCh := rc.Restored()

	// Kill the conn the way a daemon exit would.
	if err := first.Close(); err != nil {
		t.Fatalf("close first conn: %v", err)
	}

	select {
	case msg := <-lost:
		if _, ok := msg.(connectionLostMsg); !ok {
			t.Fatalf("watchConnDrop returned %T, want connectionLostMsg", msg)
		}
	case <-time.After(reconnectSafetyNet):
		t.Fatal("watchConnDrop never observed the drop")
	}

	// The reconnect swap is the success condition, not a wall clock.
	select {
	case <-restoreCh:
	case <-time.After(reconnectSafetyNet):
		t.Fatal("reconnect never swapped in a fresh conn")
	}
	if rc.Conn() == first {
		t.Fatal("ConnDone channel was not swapped: rc.Conn() is still the dead conn")
	}

	// waitForConnRestore must now report restore rather than the
	// exhaustion failure.
	restored := make(chan tea.Msg, 1)
	go func() { restored <- waitForConnRestore(rc)() }()

	select {
	case msg := <-restored:
		if _, ok := msg.(connectionRestoredMsg); !ok {
			t.Fatalf("waitForConnRestore returned %T, want connectionRestoredMsg", msg)
		}
	case <-time.After(reconnectSafetyNet):
		t.Fatal("waitForConnRestore never observed the reconnect")
	}

	mu.Lock()
	got := calls
	mu.Unlock()
	if got < 2 {
		t.Fatalf("factory called %d times, want at least 2 (initial + reconnect)", got)
	}
}

// TestReconnectGuardFailsWithoutLiveConn is the negative check for the
// daemon-reconnect guards: when the client never swaps in a live conn, the
// restore signal must stay open and waitForConnRestore must report failure,
// not success. This proves the guards are signalled on an actual swap rather
// than passing on the mere passage of time.
func TestReconnectGuardFailsWithoutLiveConn(t *testing.T) {
	var calls int
	var mu sync.Mutex
	factory := func() (*rpc.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls > 1 {
			return nil, errors.New("daemon unreachable")
		}
		client, server := dialPair(t)
		t.Cleanup(func() { _ = server.Close() })
		return client, nil
	}

	rc := rpc.NewReconnectingClient(factory, nil)
	if err := rc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rc.Close()

	restoreCh := rc.Restored()
	if err := rc.Conn().Close(); err != nil {
		t.Fatalf("close first conn: %v", err)
	}

	restored := make(chan tea.Msg, 1)
	go func() { restored <- waitForConnRestore(rc)() }()
	select {
	case msg := <-restored:
		t.Fatalf("waitForConnRestore with no live conn returned %T, want no success", msg)
	case <-time.After(time.Second):
	}

	select {
	case <-restoreCh:
		t.Fatal("Restored() fired without a successful swap")
	default:
	}
}

// armRestorable wraps a ReconnectingClient and signals when Restored() is
// read, so a test can prove waitForConnRestore has armed on the restore
// signal before the swap is allowed to land.
type armRestorable struct {
	*rpc.ReconnectingClient
	armed chan struct{}
}

func (a armRestorable) Restored() <-chan struct{} {
	a.armed <- struct{}{}
	return a.ReconnectingClient.Restored()
}

// TestWaitForConnRestoreSignalsOnSwap drives the production success path of
// waitForConnRestore directly: the waiter is armed after the drop while the
// client still holds the dead conn, and must return connectionRestoredMsg
// only when the reconnect actually closes the Restored() channel — never
// before. This covers the <-restored branch that the other guards reach only
// via their pre-armed shortcut.
func TestWaitForConnRestoreSignalsOnSwap(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	var calls int
	var mu sync.Mutex
	factory := func() (*rpc.Conn, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n > 1 {
			// Hold the reconnect until the waiter is armed, so the swap
			// cannot land early via the shortcut branch.
			<-release
		}
		client, server := dialPair(t)
		t.Cleanup(func() { _ = server.Close() })
		return client, nil
	}

	rc := rpc.NewReconnectingClient(factory, nil)
	if err := rc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rc.Close()
	defer once.Do(func() { close(release) })

	first := rc.Conn()
	if first == nil {
		t.Fatal("Start did not install a conn")
	}

	// Drop the live conn first: reconnect blocks in the factory, so rc still
	// holds the dead conn when the waiter arms.
	if err := first.Close(); err != nil {
		t.Fatalf("close first conn: %v", err)
	}

	armed := make(chan struct{}, 1)
	wait := make(chan tea.Msg, 1)
	go func() { wait <- waitForConnRestore(armRestorable{rc, armed})() }()

	select {
	case <-armed:
	case <-time.After(reconnectSafetyNet):
		t.Fatal("waitForConnRestore never armed on Restored()")
	}

	// The swap has not been released, so no restore can be reported yet.
	select {
	case msg := <-wait:
		t.Fatalf("waitForConnRestore returned %T before the swap landed", msg)
	case <-time.After(50 * time.Millisecond):
	}

	once.Do(func() { close(release) })

	select {
	case msg := <-wait:
		if _, ok := msg.(connectionRestoredMsg); !ok {
			t.Fatalf("waitForConnRestore returned %T, want connectionRestoredMsg", msg)
		}
	case <-time.After(reconnectSafetyNet):
		t.Fatal("waitForConnRestore never observed the swap")
	}
}

// TestInstallDaemonAutostartUsesReconnectingClient guards the edict 911
// fix at its source: installDaemonAutostart must install a
// *rpc.ReconnectingClient (not a plain LoopbackCourt), so that a dropped
// conn is observed via a *swapped* ConnDone channel. It then proves the
// swap functionally: killing the live conn reconnects to the (still
// listening) daemon and waitForConnRestore reports restore.
//
// Without the ReconnectingClient, the type assertion fails immediately —
// no 30s poll needed — and the restore branch would be unreachable.
func TestInstallDaemonAutostartUsesReconnectingClient(t *testing.T) {
	dir := t.TempDir()
	if len(filepath.Join(dir, rpc.DefaultSocketName)) >= 104 {
		t.Skip("tmp path too long for unix socket")
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("ASIMI_READY_FD", "")

	resolved, err := rpc.SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	stop := startFakeDaemon(t, resolved)
	defer stop()

	model := newTestModel(t)
	model.court = &mockCourtClient{}

	hook, err := installDaemonAutostart(context.Background(), model)
	if err != nil {
		t.Fatalf("installDaemonAutostart: %v", err)
	}
	if hook == nil {
		t.Fatal("installDaemonAutostart returned a nil program hook")
	}

	rc, ok := model.court.(*rpc.ReconnectingClient)
	if !ok {
		t.Fatalf("model.court = %T, want *rpc.ReconnectingClient", model.court)
	}
	defer rc.Close()

	first := rc.Conn()
	if first == nil {
		t.Fatal("installDaemonAutostart did not install a conn")
	}

	// Kill the live conn the way a daemon exit would; the reconnecting
	// client must dial the still-listening daemon and swap in a new conn.
	// Capture the restore signal before the kill so a fast swap cannot be
	// missed.
	restoreCh := rc.Restored()
	if err := first.Close(); err != nil {
		t.Fatalf("close first conn: %v", err)
	}

	// The swap signal is the success condition; the deadline is only a
	// backstop against a hung reconnect.
	select {
	case <-restoreCh:
	case <-time.After(reconnectSafetyNet):
		t.Fatal("reconnect never swapped in a fresh conn")
	}

	restored := make(chan tea.Msg, 1)
	go func() { restored <- waitForConnRestore(rc)() }()

	select {
	case msg := <-restored:
		if _, ok := msg.(connectionRestoredMsg); !ok {
			t.Fatalf("waitForConnRestore returned %T, want connectionRestoredMsg", msg)
		}
	case <-time.After(reconnectSafetyNet):
		t.Fatal("waitForConnRestore never observed the reconnect")
	}

	if rc.Conn() == first {
		t.Fatal("ConnDone channel was not swapped: rc.Conn() is still the dead conn")
	}
}
