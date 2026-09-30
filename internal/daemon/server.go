package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/rpc"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/internal/wire"
)

// InitFunc is called by Run to populate shared resources (DB, config,
// logger) via fx. The main package passes its own provider list.
type InitFunc func() (*Shared, error)

// Run starts a foreground daemon process: wires shared infrastructure
// (logger, config, DB) via initFn, listens on the socket returned by
// rpc.SocketPath, and serves clients. Each connection gets its own
// Court and Runner created from the shared resources.
//
// Shutdown: SIGINT or SIGTERM drains in-flight work with a 10s grace
// and tears the socket down.
func Run(initFn InitFunc) error {
	shared, err := initFn()
	if err != nil {
		return err
	}
	if shared == nil {
		return fmt.Errorf("daemon: shared resources not initialised")
	}

	path, err := rpc.SocketPath()
	if err != nil {
		return fmt.Errorf("daemon: resolve socket path: %w", err)
	}
	listener, err := rpc.Listen(path)
	if err != nil {
		return fmt.Errorf("daemon: listen %s: %w", path, err)
	}
	defer listener.Close()

	slog.Info("daemon listening", "socket", path)
	fmt.Fprintf(os.Stderr, "asimi daemon ready at %s\n", path)

	// Readiness signal to a parent process (TUI autostart). The parent
	// hands us a pipe fd and blocks on it until we signal that the
	// listener is bound.
	if fdStr := os.Getenv("ASIMI_READY_FD"); fdStr != "" {
		if fd, err := strconv.Atoi(fdStr); err == nil && fd > 2 {
			f := os.NewFile(uintptr(fd), "ready")
			if f != nil {
				_, _ = f.Write([]byte{1})
				_ = f.Close()
			}
		}
	}

	pidPath := path + ".pid"
	if err := writePidFile(pidPath); err != nil {
		slog.Warn("daemon: write pid file", "path", pidPath, "err", err)
	}
	defer os.Remove(pidPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal handling.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		slog.Info("daemon: shutdown signal received")
		cancel()
		_ = listener.Close()
	}()

	return serveClients(ctx, listener, shared)
}

// serveClients accepts connections on listener and services each one
// until ctx cancels or Accept fails. Each connection gets its own
// Court created from the shared resources. connID is assigned
// atomically so every connection has a unique identifier.
func serveClients(ctx context.Context, listener net.Listener, shared *Shared) error {
	var connID atomic.Uint64
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("daemon: accept: %w", err)
		}
		id := connID.Add(1)
		wg.Add(1)
		go func(nc net.Conn, cid uint64) {
			defer wg.Done()
			serveOne(ctx, nc, shared, cid)
		}(conn, id)
	}
}

// serveOne runs the RPC server loop for a single client connection.
// It performs a handshake (SetContext), creates per-connection
// Runner and Court, registers handlers, pumps server→client
// notifications, and blocks until the client disconnects or ctx
// cancels. connID uniquely identifies this connection within the
// daemon process.
//
// SetContext is idempotent: the first call creates the Court and
// Runner; every call (re)configures the model from the latest params.
func serveOne(ctx context.Context, conn net.Conn, shared *Shared, connID uint64) {
	defer conn.Close()

	rpcConn := rpc.New(conn, rpc.Options{})
	rpcConn.Handle(rpc.MethodPing, func(ctx context.Context, _ []byte) ([]byte, error) {
		return wire.Encode(rpc.PingResult{Ok: true})
	})

	var ct *court.Court
	var runner runners.Runner
	firstHandshakeCh := make(chan struct{})
	var firstHandshakeOnce sync.Once

	rpcConn.Handle(rpc.MethodSetContext, func(_ context.Context, params []byte) ([]byte, error) {
		var p types.SetContextParams
		if err := wire.Decode(params, &p); err != nil {
			return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
		}
		if p.ProjectRoot == "" {
			return nil, wire.NewError(0, "empty project_root")
		}
		if _, err := os.Stat(p.ProjectRoot); err != nil {
			return nil, wire.NewError(0, "invalid project_root")
		}

		// Verify the client-supplied Username against the peer credential
		// from the Unix socket. This prevents any process with socket
		// access from impersonating another user.
		if verified := unixPeerUsername(conn); verified != "" && verified != p.Username {
			msg := fmt.Sprintf("username mismatch: client claims %q, socket peer is %q", p.Username, verified)
			slog.Info(msg)
			return nil, wire.NewError(0, msg)
		}

		if ct == nil {
			projectCfg, err := config.LoadProjectConfig(p.ProjectRoot, false)
			if err != nil {
				return nil, wire.NewError(0, err.Error())
			}
			// HasVCS is the single authoritative gitless-ground flag and
			// SetContextParams does not carry it over the wire — re-derive it
			// so the RepoInfo never treats a real git repository as gitless.
			var hasVCS bool
			if p.ProjectRoot != "" {
				hasVCS = repo.GetRepoInfoForRoot(p.ProjectRoot).HasVCS
			}
			repoInfo := repo.RepoInfo{ProjectRoot: p.ProjectRoot, WorktreePath: p.WorktreePath, Branch: p.Branch, HasVCS: hasVCS, Slug: p.Project}
			ct, runner, err = createCourt(ctx, shared, connID, p, projectCfg, repoInfo, shared.NewSessionStore, p.IsolatedHost)
			if err != nil {
				return nil, wire.NewError(0, err.Error())
			}
		}
		if err := reconfigureModel(ctx, ct, p); err != nil {
			return nil, wire.NewError(0, err.Error())
		}
		firstHandshakeOnce.Do(func() { close(firstHandshakeCh) })
		return wire.Encode(struct{}{})
	})

	go func() {
		if err := rpcConn.Serve(); err != nil {
			shared.Logger.Debug("daemon: conn.Serve exited", "conn_id", connID, "err", err)
		}
	}()

	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer handshakeCancel()
	select {
	case <-firstHandshakeCh:
	case <-handshakeCtx.Done():
		shared.Logger.Warn("daemon: handshake timeout", "conn_id", connID)
		rpcConn.Close()
		return
	}

	rpc.RegisterCourtHandlers(rpcConn, ct)

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()
	go rpc.PumpCourtEvents(connCtx, rpcConn, ct.Subscribe(connCtx))

	defer func() {
		if runner != nil {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			_ = runner.Close(closeCtx)
		}
	}()

	shared.Logger.Info("daemon: client connected", "conn_id", connID)
	<-rpcConn.Done()
	shared.Logger.Info("daemon: client disconnected", "conn_id", connID)
}

func writePidFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
}
