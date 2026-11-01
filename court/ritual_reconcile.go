package court

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// processAlive reports whether pid names a live process, and whether that
// question could be answered at all. A signal-0 probe delivers no signal but
// still performs the kernel's existence and permission check:
//
//   - nil                 → the process exists (alive=true, known=true)
//   - EPERM               → the process exists but belongs to another user
//   - ESRCH/ErrProcessDone → the process is gone (dead, known)
//   - other               → the answer is unknown; the caller must fail safe
//
// A pid <= 0 carries no ownership information and is reported as unknown.
func processAlive(pid int64) (alive bool, known bool) {
	if pid <= 0 {
		return false, false
	}
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return false, false
	}
	err = proc.Signal(syscall.Signal(0))
	switch {
	case err == nil:
		return true, true
	case errors.Is(err, syscall.ESRCH), errors.Is(err, os.ErrProcessDone):
		return false, true
	case errors.Is(err, syscall.EPERM):
		return true, true
	default:
		return false, false
	}
}

// reconcileOrphanedRituals enforces the startup invariant: no execution may
// remain `running` unless a live process owns it.
//
// A daemon that dies mid-ritual leaves its executions `running` in the DB with
// no goroutine behind them. The per-edict liveness lock is process-local, so
// after a restart it is free and the stale sweep would eventually reap the row
// by age alone — minting a phantom abort prompt for a ritual that was healthy
// and, until then, blocking re-enactment via the duplicate-enactment guard.
//
// Ownership is explicit: OwnerPID is stamped at execution start, and a row is
// orphaned only when that owner is provably dead. A live owner (another daemon,
// a test harness, a concurrently running Court) is never touched; an owner that
// cannot be established (pid <= 0, or an unanswerable probe) is left alone and
// logged rather than guessed at. Reconciled rows move to `failed`, which the
// existing recovery machinery already scans, so the ritual can be offered for
// resume and the edict stops being wedged.
//
// This is deliberately cheap and non-fatal: every failure is logged, never
// returned, so a broken reconcile cannot prevent the Court from starting.
func (rg *RitualGuard) reconcileOrphanedRituals() {
	if rg == nil || rg.MinisterBase == nil || rg.db == nil {
		return
	}

	var running []RitualExecution
	if err := rg.db.
		Where("state = ? AND username = ? AND project = ?", RitualStateRunning, rg.Username(), rg.Project()).
		Find(&running).Error; err != nil {
		rg.logger.Warn("startup reconciliation: failed to query running rituals", "error", err)
		return
	}

	for i := range running {
		exec := &running[i]
		alive, known := processAlive(exec.OwnerPID)
		if !known {
			rg.logger.Warn("startup reconciliation: execution ownership unknown, leaving it untouched",
				"execution_id", exec.ID,
				"ritual", exec.RitualName,
				"edict_id", exec.EdictID,
				"owner_pid", exec.OwnerPID)
			continue
		}
		if alive {
			rg.logger.Debug("startup reconciliation: execution owner is live, leaving it running",
				"execution_id", exec.ID,
				"ritual", exec.RitualName,
				"edict_id", exec.EdictID,
				"owner_pid", exec.OwnerPID)
			continue
		}

		rg.logger.Info("startup reconciliation: reaping orphaned ritual execution",
			"execution_id", exec.ID,
			"ritual", exec.RitualName,
			"edict_id", exec.EdictID,
			"owner_pid", exec.OwnerPID)
		if err := rg.db.Model(&RitualExecution{}).
			Where("id = ?", exec.ID).
			Updates(map[string]interface{}{
				"state":      RitualStateFailed,
				"updated_at": time.Now(),
			}).Error; err != nil {
			rg.logger.Warn("startup reconciliation: failed to mark orphaned execution failed",
				"execution_id", exec.ID,
				"error", err)
		}
	}
}
