package runners

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.podman.io/podman/v6/libpod/define"
	"go.podman.io/podman/v6/pkg/bindings"
	"go.podman.io/podman/v6/pkg/bindings/containers"
	"go.podman.io/podman/v6/pkg/bindings/images"

	"github.com/afittestide/asimi/internal/repo"
)

// sandboxListTimeout bounds the janitor's podman queries so a wedged daemon
// cannot stall startup reconciliation.
const sandboxListTimeout = 15 * time.Second

// janitorCtx returns a bounded context carrying the runner's podman
// connection. The podman client lives in r.conn, not the caller's context, so
// every bindings call must start from it — exactly as healthcheck and Run do.
// The connection is established on demand: startup reconciliation precedes the
// first shell command, when a lazily-initialized runner has no connection yet.
func (r *PodmanRunner) janitorCtx(ctx context.Context) (context.Context, context.CancelFunc, error) {
	conn, err := r.podmanConn(ctx)
	if err != nil {
		return nil, nil, err
	}
	opCtx, cancel := context.WithTimeout(conn, sandboxListTimeout)
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-opCtx.Done():
		}
	}()
	return opCtx, cancel, nil
}

// podmanConn returns the runner's live podman connection, establishing one when
// absent (or dead). It mirrors initialize()'s reconnect logic without touching
// container state.
func (r *PodmanRunner) podmanConn(ctx context.Context) (context.Context, error) {
	r.mu.Lock()
	conn := r.conn
	r.mu.Unlock()
	if conn != nil && conn.Err() == nil {
		return conn, nil
	}

	newConn, err := r.establishConn(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to podman: %w", err)
	}
	r.mu.Lock()
	r.conn = newConn
	r.mu.Unlock()
	return newConn, nil
}

// ListSandboxes returns this project's sandbox containers, labeled or not.
// Container listing is unfiltered on purpose: a server-side label filter would
// hide containers predating the labels, which the name-prefix fallback must
// still see. Selection is done client-side by label or name prefix.
func (r *PodmanRunner) ListSandboxes(ctx context.Context) ([]SandboxContainer, error) {
	opCtx, cancel, err := r.janitorCtx(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()

	all := true
	listed, err := containers.List(opCtx, (&containers.ListOptions{}).WithAll(all))
	if err != nil {
		return nil, fmt.Errorf("list sandbox containers: %w", err)
	}

	prefix := sandboxContainerPrefix(r.projectSlug)
	project := repo.SanitizeSegment(r.projectSlug)
	out := make([]SandboxContainer, 0)
	for _, c := range listed {
		if c.Labels[SandboxLabelProject] != project && !hasNamePrefix(c.Names, prefix) {
			continue
		}
		out = append(out, SandboxContainer{
			ID:      c.ID,
			Name:    containerDisplayName(c.Names, c.ID),
			Labels:  c.Labels,
			Created: c.Created,
		})
	}
	return out, nil
}

// hasNamePrefix reports whether any podman name matches prefix, tolerating the
// leading slash some endpoints include.
func hasNamePrefix(names []string, prefix string) bool {
	for _, name := range names {
		if len(name) > 0 && name[0] == '/' {
			name = name[1:]
		}
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// containerDisplayName picks a name for logs, falling back to the ID.
func containerDisplayName(names []string, id string) string {
	if len(names) > 0 {
		return names[0]
	}
	return id
}

// RemoveSandbox force-removes a container and its anonymous volumes.
func (r *PodmanRunner) RemoveSandbox(ctx context.Context, containerID string) error {
	opCtx, cancel, err := r.janitorCtx(ctx)
	if err != nil {
		return err
	}
	defer cancel()

	force := true
	volumes := true
	if _, err := containers.Remove(opCtx, containerID, (&containers.RemoveOptions{}).
		WithForce(force).
		WithVolumes(volumes)); err != nil {
		return fmt.Errorf("remove sandbox container %s: %w", containerID, err)
	}
	return nil
}

// SandboxFreeMiB reports free space, in MiB, on the podman store's filesystem.
// The store lives in the podman VM, so this is the VM's free space.
func (r *PodmanRunner) SandboxFreeMiB(ctx context.Context) (int64, error) {
	opCtx, cancel, err := r.janitorCtx(ctx)
	if err != nil {
		return 0, err
	}
	defer cancel()

	conn, err := bindings.GetClient(opCtx)
	if err != nil {
		return 0, fmt.Errorf("podman connection unavailable: %w", err)
	}
	resp, err := conn.DoRequest(opCtx, nil, http.MethodGet, "/info", nil, nil)
	if err != nil {
		return 0, fmt.Errorf("podman info: %w", err)
	}
	defer resp.Body.Close()

	var info define.Info
	if err := resp.Process(&info); err != nil {
		return 0, fmt.Errorf("decode podman info: %w", err)
	}
	if info.Store == nil || info.Store.GraphRootAllocated == 0 {
		return 0, fmt.Errorf("podman info has no store usage")
	}
	free := info.Store.GraphRootAllocated - info.Store.GraphRootUsed
	return int64(free / (1024 * 1024)), nil
}

// PruneSandboxArtifacts prunes reclaimable build artifacts: dangling images and
// the persistent builder cache. It leaves tagged images and running containers
// alone.
func (r *PodmanRunner) PruneSandboxArtifacts(ctx context.Context) (int, error) {
	opCtx, cancel, err := r.janitorCtx(ctx)
	if err != nil {
		return 0, err
	}
	defer cancel()

	pruned, err := images.Prune(opCtx, (&images.PruneOptions{}).WithBuildCache(true))
	if err != nil {
		return 0, fmt.Errorf("prune build artifacts: %w", err)
	}
	return len(pruned), nil
}

// ensureSandboxDiskHeadroom prunes reclaimable build artifacts when the podman
// store is below sandbox.disk_min_free_mib. 0 disables. It never blocks
// container creation.
func (r *PodmanRunner) ensureSandboxDiskHeadroom(ctx context.Context) {
	minFreeMiB := 0
	if r.config != nil {
		minFreeMiB = r.config.DiskMinFreeMiB
	}
	if minFreeMiB <= 0 {
		return
	}
	janitor := NewSandboxJanitor(r, slog.Default())
	if janitor == nil {
		return
	}
	if _, err := janitor.EnsureDiskHeadroom(ctx, minFreeMiB); err != nil {
		slog.Warn("disk-pressure guard failed; continuing with container creation", "error", err)
	}
}
