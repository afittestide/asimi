package court

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/afittestide/asimi/storage"
)

// Retirement classes. Each retired row's disposition_reason names its class so
// the ledger explains itself without re-deriving the decision. Classes map to
// terminal statuses (never interchangeable, e1037):
//
//	ascended, cancelled → superseded_by_edict (the container is gone)
//	superseded          → dismissed (the work was superseded; the edict stands)
const (
	zhengmingClassAscended   = "ascended"
	zhengmingClassCancelled  = "cancelled"
	zhengmingClassSuperseded = "superseded"
)

// Class D (aged prompt whose edict row does not exist) is deleted, not
// deferred (Ruler ruling 2026-11-18): a pending request with no edicts row is
// NEVER retired by triage. Its one live instance is request 77b676770ccd79a0
// (edict_id 1043, session e6a375fe0a501678, created 2026-11-12T14:59:57
// while MAX(edicts.id)=1042) — id reservation, cross-session ownership and
// that row's repair belong to e1009 ("brewing mints its own edict at step
// 0"). Do not re-add this class as a "fix".

// zhengmingTriageCounts reports what one triage pass retired, counted from
// dispositions actually written — never from what it intended. e1043's toast
// must use these counts, not re-derive them.
type zhengmingTriageCounts struct {
	Retired    int
	Ascended   int
	Cancelled  int
	Superseded int
}

// zhengmingRetirement is one planned retirement: which request, into which
// terminal status, why, and which class the reason belongs to (so counts can
// be attributed after the fact).
type zhengmingRetirement struct {
	requestID string
	status    storage.ZhengmingStatus
	reason    string
	class     string
}

// triagePendingZhengming is the retirement half of reconnect reconciliation
// (e1037): it retires pending requests whose reason to exist has ended and
// returns the survivors. It is a filter, never a delivery path — it opens no
// dialog, renders no toast and lists nothing; e1043 owns delivery and calls
// this entry point from SetNotify, once per install, after retirement.
//
// It runs one pass per call. Every retirement is scoped to status='pending'
// by DisposeZhengming, so a repeated pass retires nothing twice; a row that
// reached a terminal state between the read and the write is a reported no-op
// (no error, no resurrection, not counted).
func (s *Court) triagePendingZhengming() ([]storage.Zhengming, zhengmingTriageCounts, bool) {
	if s == nil || s.db == nil || s.config == nil {
		return nil, zhengmingTriageCounts{}, false
	}
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}

	pending, err := s.pendingZhengming()
	if err != nil {
		logger.Warn("failed to query pending zhengming for triage", "error", err)
		return nil, zhengmingTriageCounts{}, false
	}
	if len(pending) == 0 {
		return nil, zhengmingTriageCounts{}, false
	}

	counts := zhengmingTriageCounts{}
	for _, r := range s.planZhengmingRetirements(pending) {
		// Ordering invariant (e1037): the terminal disposition is written
		// before any side effect — waiter release, ledger event — inside
		// disposeZhengming, so a failed write leaves the prompt pending and
		// the ledger never claims a retirement that did not happen.
		if err := s.DisposeZhengmingWithStatus(r.requestID, r.status, r.reason); err != nil {
			if errors.Is(err, errZhengmingNotPending) {
				// The row moved to a terminal status between the read and the
				// write (an answer raced us, or an earlier pass retired it).
				// Reported no-op: no error, no resurrection, not counted.
				logger.Info("zhengming triage no-op: request already terminal",
					"request_id", r.requestID, "class", r.class)
				continue
			}
			logger.Warn("failed to retire pending zhengming",
				"request_id", r.requestID, "class", r.class, "error", err)
			continue
		}
		counts.Retired++
		switch r.class {
		case zhengmingClassAscended:
			counts.Ascended++
		case zhengmingClassCancelled:
			counts.Cancelled++
		case zhengmingClassSuperseded:
			counts.Superseded++
		}
	}

	// Only what survived retirement may be handed to the delivery half.
	survivors, err := s.pendingZhengming()
	if err != nil {
		logger.Warn("failed to re-query pending zhengming after triage", "error", err)
		return nil, counts, false
	}
	if counts.Retired > 0 {
		logger.Info("triaged pending zhengming",
			"retired", counts.Retired,
			"ascended", counts.Ascended,
			"cancelled", counts.Cancelled,
			"superseded", counts.Superseded,
			"awaiting", len(survivors))
	}
	return survivors, counts, true
}

// pendingZhengming returns the Court's own scope's pending requests, oldest and
// most urgent first. Unbounded on purpose: a silent row cap would make "listed
// by :gates" untrue. timeout_at is deliberately not consulted — the deadline
// has no consumer (Ruler ruling 2026-11-18).
func (s *Court) pendingZhengming() ([]storage.Zhengming, error) {
	var pending []storage.Zhengming
	err := s.db.
		Where("status = ? AND username = ? AND project = ?", storage.ZhengmingPending, s.username, s.config.Project).
		Order("priority DESC, created_at ASC").
		Find(&pending).Error
	if err != nil {
		return nil, err
	}
	return pending, nil
}

// edictMinister keys the supersession groups. Never (edict_id) alone and never
// (edict_id, minister_id) as a retirement key: edict 1 + secretary legitimately
// holds seven distinct questions, so a coarse key would silently kill six real
// gates. Only an exec marker makes two prompts the same work.
type edictMinister struct {
	edictID    uint
	ministerID string
}

// planZhengmingRetirements decides, without writing, which pending requests to
// retire, into which terminal status, and why. Nothing is retired on a guess:
// each class is decidable from the ledger.
func (s *Court) planZhengmingRetirements(pending []storage.Zhengming) []zhengmingRetirement {
	superseded := supersededByExecMarker(pending)

	var out []zhengmingRetirement
	for i := range pending {
		req := &pending[i]

		// Edict 1 is Court Infrastructure and edict 0 is the unscoped
		// pseudo-edict: both are permanent, never stale.
		if req.EdictID == 0 || req.EdictID == 1 {
			continue
		}

		if class, reason, ok := s.edictRetirementReason(req); ok {
			out = append(out, zhengmingRetirement{
				requestID: req.RequestID,
				status:    storage.ZhengmingSupersededByEdict,
				reason:    reason,
				class:     class,
			})
			continue
		}
		if reason, ok := superseded[req.RequestID]; ok {
			out = append(out, zhengmingRetirement{
				requestID: req.RequestID,
				status:    storage.ZhengmingDismissed,
				reason:    reason,
				class:     zhengmingClassSuperseded,
			})
		}
		// Class D is deliberately absent — see the comment at the top of
		// this file. A pending request with no edicts row survives triage;
		// the deadline is not consulted either (no consumer, ruled
		// 2026-11-18).
	}
	return out
}

// edictRetirementReason covers the edict-level classes: the edict ascended
// (a live Ruler earth seal) or was cancelled. Both mean the container is gone,
// so both retire as superseded_by_edict. A tombstoned Ruler seal (stale_at set)
// or a ruler:intent-only seal is not a retirement reason — the edict still
// reads active, so a pending request still has work to gate. A live waiter or
// an active merge-worktree execution (which runs after ruler:earth) also keeps
// the request alive until landing finishes.
func (s *Court) edictRetirementReason(req *storage.Zhengming) (class, reason string, ok bool) {
	var edict storage.Edict
	if err := s.db.Where("id = ? AND username = ? AND project = ?",
		req.EdictID, req.Username, req.Project).First(&edict).Error; err == nil {
		if edict.CancelledAt != nil {
			return zhengmingClassCancelled, "court: edict cancelled", true
		}
	}

	// Same filter as SealService.HasSeal(key, "ruler", "earth"): only a live earth seal counts.
	var live int64
	err := s.db.Model(&storage.Seal{}).
		Where("edict_id = ? AND username = ? AND project = ? AND minister_id = ? AND realm = ? AND stale_at IS NULL",
			req.EdictID, req.Username, req.Project, "ruler", "earth").
		Count(&live).Error
	if err == nil && live > 0 {
		if s.zhengming != nil {
			s.zhengming.mu.Lock()
			_, hasWaiter := s.zhengming.pending[req.RequestID]
			s.zhengming.mu.Unlock()
			if hasWaiter {
				return "", "", false
			}
		}
		var activeMerge int64
		if err := s.db.Model(&RitualExecution{}).
			Where("edict_id = ? AND username = ? AND project = ? AND ritual_name = ? AND state IN ?",
				req.EdictID, req.Username, req.Project, mergeWorktreeRitual,
				[]string{string(RitualStatePending), string(RitualStateRunning)}).
			Count(&activeMerge).Error; err == nil && activeMerge > 0 {
			return "", "", false
		}
		return zhengmingClassAscended, "court: edict ascended (Ruler sealed)", true
	}
	return "", "", false
}

// supersededByExecMarker maps a request ID to the reason it is superseded: its
// exec marker was replaced by a newer pending request's marker for the same
// (edict_id, minister_id). Markers are matched the way promptExecID does.
// Markerless prompts are never grouped, and a lone marker is never superseded —
// that is why the same-edict clusters without markers keep their gates.
func supersededByExecMarker(pending []storage.Zhengming) map[string]string {
	groups := make(map[edictMinister][]*storage.Zhengming, len(pending))
	for i := range pending {
		req := &pending[i]
		key := edictMinister{edictID: req.EdictID, ministerID: req.MinisterID}
		groups[key] = append(groups[key], req)
	}

	out := make(map[string]string)
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			if !group[i].CreatedAt.Equal(group[j].CreatedAt) {
				return group[i].CreatedAt.Before(group[j].CreatedAt)
			}
			return group[i].RequestID < group[j].RequestID
		})
		for i := 0; i < len(group)-1; i++ {
			marker := promptExecID(group[i])
			if marker == "" {
				continue
			}
			for j := i + 1; j < len(group); j++ {
				newer := promptExecID(group[j])
				if newer == "" || newer == marker {
					continue
				}
				out[group[i].RequestID] = fmt.Sprintf("court: superseded by execution %s", newer)
				break
			}
		}
	}
	return out
}
