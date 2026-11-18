package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnixPeerUsername_NonUnixConn verifies that unixPeerUsername
// returns "" for a non-Unix connection (e.g. net.Pipe).
func TestUnixPeerUsername_NonUnixConn(t *testing.T) {
	client, _ := net.Pipe()
	defer client.Close()

	if got := unixPeerUsername(client); got != "" {
		t.Errorf("unixPeerUsername(net.Pipe) = %q, want %q", got, "")
	}
}

// TestUnixPeerUsername_RealSocket verifies that unixPeerUsername
// returns the current OS user when connecting over a real Unix socket.
func TestUnixPeerUsername_RealSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "s.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serverConn, err := listener.Accept()
		if err != nil {
			t.Logf("Accept: %v", err)
			return
		}
		defer serverConn.Close()

		got := unixPeerUsername(serverConn)
		currentUser, err := user.Current()
		if err != nil {
			t.Logf("user.Current: %v", err)
			// Just check it's non-empty when we can't verify the exact value.
			if got == "" {
				t.Error("unixPeerUsername returned empty on real socket")
			}
			return
		}
		if got != currentUser.Username {
			t.Errorf("unixPeerUsername = %q, want %q", got, currentUser.Username)
		}
	}()

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	<-done
}

// TestUnixPeerUsername_ClosedSocket verifies graceful handling when
// File() fails (e.g. connection already closed).
func TestUnixPeerUsername_ClosedSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "s.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serverConn, err := listener.Accept()
		if err != nil {
			t.Logf("Accept: %v", err)
			return
		}
		// Close immediately so File() fails on the server side.
		serverConn.Close()

		got := unixPeerUsername(serverConn)
		if got != "" {
			t.Errorf("unixPeerUsername on closed conn = %q, want %q", got, "")
		}
	}()

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	client.Close()

	<-done
}

// TestUnixPeerPID_RealSocket verifies the mandate owner is resolved from the
// socket peer, not from the listener's own PID: in daemon mode the Court lives
// in the daemon, so os.Getpid() would name the wrong process and the mandate
// would outlive the client that granted it (edict 1036).
func TestUnixPeerPID_RealSocket(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("peer PID is only resolvable on linux and darwin")
	}

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "pid.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	got := make(chan int, 1)
	go func() {
		serverConn, err := listener.Accept()
		if err != nil {
			got <- -1
			return
		}
		defer serverConn.Close()
		got <- unixPeerPID(serverConn)
	}()

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	peerPID := <-got
	assert.Equal(t, os.Getpid(), peerPID,
		"the peer PID must be the client process that dialed, not the listener")
}

// TestUnixPeerPID_NonUnixConn verifies the fallback: a non-Unix connection has
// no peer credential, so the owner is unknown (0) rather than guessed.
func TestUnixPeerPID_NonUnixConn(t *testing.T) {
	client, _ := net.Pipe()
	defer client.Close()

	assert.Equal(t, 0, unixPeerPID(client))
}

// TestUnixPeerCredential_KeepsConnDeadlinedAndPollable pins the reason the
// credential read borrows the fd via SyscallConn instead of calling File():
// File dups the fd and switches the dup to blocking mode, which takes the
// connection off the netpoller. After a peer-cred read the connection must
// still be under the netpoller and must still honour SetDeadline (edict 1036).
func TestUnixPeerCredential_KeepsConnDeadlinedAndPollable(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("peer credentials are only readable on linux and darwin")
	}

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "cred.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	checked := make(chan error, 1)
	go func() {
		serverConn, err := listener.Accept()
		if err != nil {
			checked <- err
			return
		}
		defer serverConn.Close()
		uc := serverConn.(*net.UnixConn)

		if pid, _, ok := unixPeerCredential(uc); !ok || pid != os.Getpid() {
			checked <- fmt.Errorf("peer credential = (%d, ok=%v), want the dialer's pid", pid, ok)
			return
		}

		// The same fd must still accept a deadline. A blocking dup cannot be
		// deadline-driven by the runtime, so a read would hang instead of
		// returning a timeout.
		if err := uc.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			checked <- fmt.Errorf("SetReadDeadline after peer-cred read: %w", err)
			return
		}
		buf := make([]byte, 1)
		if _, err := uc.Read(buf); err == nil {
			checked <- fmt.Errorf("read with an expired deadline succeeded; the conn is no longer deadline-driven")
			return
		} else if !errors.Is(err, os.ErrDeadlineExceeded) {
			checked <- fmt.Errorf("read error = %v, want a deadline error", err)
			return
		}

		// Clear the deadline and prove the connection is still usable.
		if err := uc.SetReadDeadline(time.Time{}); err != nil {
			checked <- fmt.Errorf("clearing deadline after timeout: %w", err)
			return
		}
		checked <- nil
	}()

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	select {
	case err := <-checked:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("peer-cred+deadline probe hung: the connection is not under the netpoller")
	}
}
