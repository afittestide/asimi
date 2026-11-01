package court

import (
	"context"

	"github.com/afittestide/asimi/internal/runners"
)

// reconcileSandboxOrphans reaps sandbox containers left behind by dead
// processes. It runs once at startup: after a daemon restart the in-memory
// per-edict runner map is empty, so containers created by the previous process
// would otherwise run forever, holding their multi-GB layers until the podman
// VM wedges (incident edda2cb3f55e4cad). Containers whose owning process is
// still live are preserved — fail safe, not loud.
func (s *Court) reconcileSandboxOrphans(ctx context.Context) {
	if s == nil || s.runner == nil {
		return
	}
	janitor := runners.NewSandboxJanitor(s.runner, s.logger)
	if janitor == nil {
		return
	}
	removed, err := janitor.ReconcileOrphans(ctx)
	if err != nil {
		// Podman may simply be unavailable (host fallback); reconciliation is
		// opportunistic, so this is not a Court failure.
		s.logger.Debug("sandbox startup reconciliation unavailable", "error", err)
		return
	}
	if removed > 0 {
		s.logger.Info("sandbox startup reconciliation reaped orphans", "removed", removed)
	}
}
