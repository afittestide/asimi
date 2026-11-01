package runners

import (
	"strconv"

	"github.com/afittestide/asimi/internal/repo"
)

// Sandbox container labels. They let a later process reconcile on-disk podman
// state against live processes: a container whose asimi.owner_pid is not a
// live process is an orphan from a dead session and can be reaped.
const (
	// SandboxLabelProject scopes a container to a project slug.
	SandboxLabelProject = "asimi.project"
	// SandboxLabelOwnerPID records the PID that created the container.
	SandboxLabelOwnerPID = "asimi.owner_pid"
	// SandboxLabelEdict records the edict a per-edict container serves. It is
	// absent on the connection runner, which serves no single edict.
	SandboxLabelEdict = "asimi.edict"
)

// SandboxLabels returns the labels stamped on every sandbox container this
// process creates. edictID 0 (the connection runner) omits the edict label.
func SandboxLabels(slug string, ownerPID int, edictID uint) map[string]string {
	labels := map[string]string{
		SandboxLabelProject:  repo.SanitizeSegment(slug),
		SandboxLabelOwnerPID: strconv.Itoa(ownerPID),
	}
	if edictID > 0 {
		labels[SandboxLabelEdict] = strconv.FormatUint(uint64(edictID), 10)
	}
	return labels
}

// sandboxContainerPrefix is the name prefix shared by every container this
// project's sandboxes ever had, labeled or not. The startup janitor matches it
// so containers created before labels existed are still visible.
func sandboxContainerPrefix(slug string) string {
	return "asimi-shell-" + repo.SanitizeSegment(slug) + "-"
}
