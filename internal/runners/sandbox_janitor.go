package runners

import (
	"context"
	"log/slog"
	"strconv"
	"time"
)

// SandboxContainer is the minimal view of a podman container the janitor needs
// to decide whether it is an orphan.
type SandboxContainer struct {
	ID      string
	Name    string
	Labels  map[string]string
	Created time.Time
}

// SandboxContainerBackend lists and removes this project's sandbox containers.
type SandboxContainerBackend interface {
	ListSandboxes(ctx context.Context) ([]SandboxContainer, error)
	RemoveSandbox(ctx context.Context, containerID string) error
}

// SandboxDiskBackend reports free space and prunes reclaimable build artifacts.
type SandboxDiskBackend interface {
	// SandboxFreeMiB returns free space in MiB on the podman store filesystem.
	SandboxFreeMiB(ctx context.Context) (int64, error)
	// PruneSandboxArtifacts prunes dangling images and the builder cache,
	// returning the number of pruned entries.
	PruneSandboxArtifacts(ctx context.Context) (int, error)
}

// SandboxJanitor reconciles on-disk podman state with live processes. The
// invariant it enforces: no sandbox container survives solely because the
// current process does not know about it. It also keeps the podman store from
// filling up by pruning reclaimable build artifacts under disk pressure.
type SandboxJanitor struct {
	// Containers and Disk are the podman-facing backends. The container
	// backend scopes itself to its project — the janitor never needs a slug.
	Containers SandboxContainerBackend
	Disk       SandboxDiskBackend

	// OrphanMaxAge is how old an unlabeled container must be before it may be
	// reaped. Labels are the precise signal; this is the fallback for
	// containers created before labels existed, where an owner may still be
	// live. Zero disables reaping of unlabeled containers entirely.
	OrphanMaxAge time.Duration

	// ProcessAlive reports whether a PID is a live process. Defaults to
	// processAlive (signal 0).
	ProcessAlive func(pid int) bool

	// Now is the clock used for the age fallback; defaults to time.Now.
	Now func() time.Time

	Logger *slog.Logger
}

// NewSandboxJanitor builds a janitor for one project from a podman-facing
// runner. It returns nil when the runner cannot reconcile podman state (e.g. a
// host runner), which keeps callers' "nil means no reconciliation" check honest.
func NewSandboxJanitor(r Runner, logger *slog.Logger) *SandboxJanitor {
	containers, ok := r.(SandboxContainerBackend)
	if !ok {
		return nil
	}
	disk, _ := r.(SandboxDiskBackend)
	return &SandboxJanitor{
		Containers:   containers,
		Disk:         disk,
		OrphanMaxAge: DefaultOrphanMaxAge,
		ProcessAlive: processAlive,
		Now:          time.Now,
		Logger:       logger,
	}
}

// DefaultOrphanMaxAge is how old an unlabeled sandbox container must be before
// the fallback reaps it. Generous: a live daemon's runner reuses its container
// by name, so only containers from a truly dead process reach this age unused.
const DefaultOrphanMaxAge = 6 * time.Hour

// ReconcileOrphans removes every listed sandbox container whose owner is known
// to be dead, and returns the number removed. Unlabeled containers are reaped
// only once older than OrphanMaxAge; anything the janitor cannot prove dead is
// preserved (fail safe, not loud).
func (j *SandboxJanitor) ReconcileOrphans(ctx context.Context) (int, error) {
	if j == nil || j.Containers == nil {
		return 0, nil
	}
	containers, err := j.Containers.ListSandboxes(ctx)
	if err != nil {
		return 0, err
	}
	now := j.now()
	removed := 0
	for _, c := range containers {
		if !j.orphan(c, now) {
			continue
		}
		if err := j.Containers.RemoveSandbox(ctx, c.ID); err != nil {
			j.logger().Warn("failed to remove orphan sandbox container",
				"container", c.Name, "id", c.ID, "error", err)
			continue
		}
		removed++
		j.logger().Info("reaped orphan sandbox container", "container", c.Name, "id", c.ID)
	}
	return removed, nil
}

// EnsureDiskHeadroom prunes reclaimable build artifacts when free space on the
// podman store is below minFreeMiB. It returns whether it pruned. minFreeMiB 0
// (or less) disables the guard. A missing disk backend or an unreadable free
// space is a no-op: the guard must never block container creation.
func (j *SandboxJanitor) EnsureDiskHeadroom(ctx context.Context, minFreeMiB int) (bool, error) {
	if j == nil || j.Disk == nil || minFreeMiB <= 0 {
		return false, nil
	}
	freeMiB, err := j.Disk.SandboxFreeMiB(ctx)
	if err != nil {
		j.logger().Warn("disk-pressure guard skipped: free space unavailable", "error", err)
		return false, nil
	}
	if freeMiB >= int64(minFreeMiB) {
		return false, nil
	}
	pruned, err := j.Disk.PruneSandboxArtifacts(ctx)
	if err != nil {
		j.logger().Warn("disk-pressure prune failed",
			"free_mib", freeMiB, "min_free_mib", minFreeMiB, "error", err)
		return false, err
	}
	j.logger().Info("disk pressure: pruned reclaimable build artifacts",
		"free_mib", freeMiB, "min_free_mib", minFreeMiB, "pruned", pruned)
	return true, nil
}

// orphan reports whether c may be reaped: its owner PID is provably dead, or —
// for a container predating labels — it is older than OrphanMaxAge.
func (j *SandboxJanitor) orphan(c SandboxContainer, now time.Time) bool {
	if pidRaw, ok := c.Labels[SandboxLabelOwnerPID]; ok {
		pid, err := strconv.Atoi(pidRaw)
		if err != nil {
			return false
		}
		return !j.processAlive()(pid)
	}
	// Legacy container: no owner label. Only age is available, and an owner may
	// still be live, so reap conservatively.
	if j.OrphanMaxAge <= 0 || c.Created.IsZero() {
		return false
	}
	return now.Sub(c.Created) > j.OrphanMaxAge
}

func (j *SandboxJanitor) processAlive() func(int) bool {
	if j.ProcessAlive != nil {
		return j.ProcessAlive
	}
	return processAlive
}

func (j *SandboxJanitor) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *SandboxJanitor) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}
