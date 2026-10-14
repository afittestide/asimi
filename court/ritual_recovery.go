package court

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/afittestide/asimi/storage"
)

// findFirstIncompleteStep returns the 0-based index of the first incomplete step,
// or -1 if all steps are complete. A step is considered incomplete if:
//   - its Status is not "completed" (never executed or failed)
//   - it has a retry count > 0 (failed and retried)
//   - its message contains error patterns: "context canceled", "timeout", "aborted"
func findFirstIncompleteStep(stepStates []RitualStepState) int {
	for i, ss := range stepStates {
		if ss.Status != "completed" || ss.RetryCount > 0 {
			return i
		}
		if strings.Contains(ss.Message, "context canceled") ||
			strings.Contains(ss.Message, "timeout") ||
			strings.Contains(ss.Message, "aborted") {
			return i
		}
	}
	return -1
}

// recoveryResult captures the outcome of recovery detection and confirmation.
type recoveryResult struct {
	previousExecutionID string
	fromStep            int
}

// recoverFromPreviousExec checks for an aborted/paused ritual execution for the
// given edict, confirms recovery via zhengming if needed, and applies recovery
// state to the provided exec. Returns a recoveryResult when recovery was
// applied, or an error if the user dismissed or zhengming failed.
func (r *RitualRunner) recoverFromPreviousExec(ctx context.Context, ritualName string, key storage.EdictKey, def *RitualDef, exec *RitualExecution) (recoveryResult, error) {
	// Look up previous aborted/paused execution for this ritual
	var previousExec *RitualExecution
	if err := r.db.Where("edict_id = ? AND username = ? AND project = ? AND ritual_name = ? AND state IN (?, ?, ?, ?, ?)", key.ID, key.Username, key.Project, ritualName, RitualStateAborted, RitualStateStopped, RitualStateFailed, RitualStateRecovering, RitualStateDismissed).
		Order("updated_at DESC").
		First(&previousExec).Error; err != nil {
		// No previous execution found — start fresh
		return recoveryResult{}, nil
	}

	// Found previous execution — handle based on state
	r.logger.Info("found previous ritual execution",
		"ritual", ritualName,
		"edict_id", key.ID,
		"previous_execution_id", previousExec.ID,
		"state", previousExec.State)

	// If dismissed, user explicitly chose to skip — abort this invocation
	if previousExec.State == RitualStateDismissed {
		r.logger.Info("ritual dismissed by user, starting fresh",
			"ritual", ritualName,
			"previous_execution_id", previousExec.ID)
		return recoveryResult{}, nil
	}

	// If state is "recovering", user already approved — apply recovery data directly
	if previousExec.State == RitualStateRecovering {
		var stepStates []RitualStepState
		if err := r.db.Where("execution_id = ?", previousExec.ID).Order("step_index").Find(&stepStates).Error; err == nil {
			firstIncompleteStep := findFirstIncompleteStep(stepStates)

			// Mark the previous "recovering" execution as completed to prevent
			// zombie state, regardless of which step we resume from.
			previousExec.State = RitualStateCompleted
			r.db.Save(previousExec)

			if firstIncompleteStep > 0 && firstIncompleteStep < len(def.Steps) {
				r.logger.Info("resuming ritual from incomplete step",
					"ritual", ritualName,
					"from_step", firstIncompleteStep,
					"previous_execution_id", previousExec.ID)

				exec.RecoveryMode = true
				exec.PreviousExecutionID = previousExec.ID
				exec.CurrentStep = firstIncompleteStep
				exec.Data = previousExec.Data

				r.logger.Info("marked previous recovering execution as completed",
					"previous_execution_id", previousExec.ID)

				return recoveryResult{
					previousExecutionID: previousExec.ID,
					fromStep:            firstIncompleteStep,
				}, nil
			}

			// firstIncompleteStep == 0 (or -1): start fresh from step 0, but
			// the previous recovering execution was already marked completed above.
			r.logger.Info("previous recovering execution has no steps to preserve, starting fresh",
				"ritual", ritualName,
				"previous_execution_id", previousExec.ID)
		}
	}

	// For aborted/stopped/failed states: start fresh (user was already
	// prompted at startup via promptForAbortedRituals, or this is a new
	// invocation — the zhengming prompt now lives there, not here).
	return recoveryResult{}, nil
}

// promptForAbortedRituals scans for aborted/stopped/failed ritual executions
// and prompts the user (via zhengming) to recover, mark as completed, or pass.
// It runs at startup and sets recoveryComplete when done.
func (rg *RitualGuard) promptForAbortedRituals(ctx context.Context) {
	defer func() {
		rg.recoveryMu.Lock()
		rg.recoveryComplete = true
		rg.recoveryMu.Unlock()
	}()

	if rg.db == nil {
		rg.logger.Debug("no db available, skipping aborted ritual recovery")
		return
	}

	if rg.getMinister == nil {
		rg.logger.Debug("no getMinister function, skipping aborted ritual recovery")
		return
	}

	// Set recoveryComplete before the per-ritual loop so event-driven rituals
	// are not blocked. The per-edict ritual locks serialize ritual execution, so
	// a recovering ritual cannot conflict with itself.
	rg.recoveryMu.Lock()
	rg.recoveryComplete = true
	rg.recoveryMu.Unlock()

	// Query aborted/stopped/failed rituals, excluding edict_id=0
	var abortedExecs []RitualExecution
	err := rg.db.Where("edict_id != 0 AND username = ? AND project = ? AND state IN (?, ?, ?, ?, ?)",
		rg.Username(), rg.Project(), RitualStateAborted, RitualStateStopped, RitualStateFailed, RitualStateRecovering, RitualStateDismissed).
		Order("updated_at DESC").
		Limit(5).
		Find(&abortedExecs).Error
	if err != nil {
		rg.logger.Warn("failed to query aborted rituals", "error", err)
		return
	}

	if len(abortedExecs) == 0 {
		rg.logger.Debug("no aborted rituals found")
		return
	}

	// First pass: auto-complete aborted rituals for sealed/cancelled edicts
	for i := range abortedExecs {
		exec := &abortedExecs[i]
		var edict storage.Edict
		if err := rg.db.First(&edict, "id = ? AND username = ? AND project = ?", exec.EdictID, exec.Username, exec.Project).Error; err != nil {
			rg.logger.Warn("failed to find edict for aborted ritual", "edict_id", exec.EdictID, "error", err)
			continue
		}
		var sealCount int64
		rg.db.Model(&storage.Seal{}).Where("edict_id = ? AND username = ? AND project = ? AND stale_at IS NULL", exec.EdictID, exec.Username, exec.Project).Count(&sealCount)
		if sealCount > 0 || edict.CancelledAt != nil {
			exec.State = RitualStateCompleted
			rg.db.Save(exec)
			rg.logger.Info("auto-completed aborted ritual for sealed/cancelled edict",
				"execution_id", exec.ID,
				"edict_id", exec.EdictID)
		}
	}

	// Get chancellor for zhengming
	if rg.waitForZhengming == nil || rg.requestZhengming == nil {
		rg.logger.Warn("zhengming functions not wired, skipping recovery prompts")
		return
	}

	for _, exec := range abortedExecs {
		// Check context before each iteration
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Skip already completed or dismissed rituals (from first pass or prior runs)
		if exec.State == RitualStateCompleted || exec.State == RitualStateDismissed {
			continue
		}

		// Check if the edict is sealed or cancelled
		var edict storage.Edict
		if err := rg.db.First(&edict, "id = ? AND username = ? AND project = ?", exec.EdictID, exec.Username, exec.Project).Error; err != nil {
			rg.logger.Warn("failed to find edict for aborted ritual", "edict_id", exec.EdictID, "error", err)
			continue
		}

		// Determine first incomplete step
		var stepStates []RitualStepState
		rg.db.Where("execution_id = ?", exec.ID).Order("step_index").Find(&stepStates)
		incompleteStep := findFirstIncompleteStep(stepStates)

		// Build description text, preferring summary over intent
		description := edict.Summary
		if description == "" {
			description = edict.Intent
		}
		// Truncate long descriptions
		if len(description) > 60 {
			description = description[:57] + "..."
		}

		// Build zhengming question
		var options []string
		if incompleteStep >= 0 {
			options = []string{
				fmt.Sprintf("Recover from step %d", incompleteStep),
				"Mark as completed",
				"Pass",
			}
		} else {
			options = []string{
				"Mark as completed",
				"Pass",
			}
		}

		questions := storage.ZhengmingQuestions{
			{
				Text:    fmt.Sprintf("Ritual %q (e%d: %s) was aborted at step %d. What should we do?", exec.RitualName, exec.EdictID, description, incompleteStep),
				Options: options,
			},
		}

		key := storage.EdictKey{ID: exec.EdictID, Username: exec.Username, Project: exec.Project}
		requestID, err := rg.requestZhengming(ctx, key, questions, storage.PriorityUrgent, "secretary")
		if err != nil {
			rg.logger.Warn("failed to request zhengming for aborted ritual", "execution_id", exec.ID, "error", err)
			continue
		}

		answer, err := rg.waitForZhengming(ctx, requestID)
		if err != nil {
			rg.logger.Warn("failed waiting for zhengming answer", "execution_id", exec.ID, "error", err)
			continue
		}

		switch {
		case strings.HasPrefix(answer, "Recover from step "):
			stepStr := strings.TrimPrefix(answer, "Recover from step ")
			stepIdx, err := strconv.Atoi(stepStr)
			if err != nil {
				rg.logger.Warn("invalid recovery step in answer", "answer", answer, "error", err)
				continue
			}
			exec.State = RitualStateRecovering
			exec.CurrentStep = stepIdx
			rg.db.Save(&exec)
			rg.logger.Info("set ritual to recovering",
				"execution_id", exec.ID,
				"from_step", stepIdx)

			// Re-trigger the ritual so recoverFromPreviousExec picks up the
			// "recovering" state and resumes execution.
			inputs := map[string]string{}
			if rawInputs, ok := exec.Data["inputs"]; ok {
				if m, ok := rawInputs.(map[string]interface{}); ok {
					for k, v := range m {
						inputs[k] = fmt.Sprintf("%v", v)
					}
				}
			}
			rg.startRitual(exec.RitualName, exec.EdictKey(), inputs)

		case answer == "Mark as completed":
			exec.State = RitualStateCompleted
			rg.db.Save(&exec)
			rg.logger.Info("marked aborted ritual as completed",
				"execution_id", exec.ID)

		case answer == "Pass":
			exec.State = RitualStateDismissed
			rg.db.Save(&exec)
			rg.logger.Info("user dismissed aborted ritual",
				"execution_id", exec.ID)

		default:
			rg.logger.Warn("unknown zhengming answer for aborted ritual", "answer", answer)
		}
	}
}
