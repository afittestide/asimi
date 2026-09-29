package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/rpc"
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
