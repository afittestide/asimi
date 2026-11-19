package court

import (
	"fmt"
	"strings"

	"github.com/afittestide/asimi/storage"
)

// ZhengmingTriageMsg is the one summary notification of a reconnect triage
// pass (e1043). Its counts are the dispositions e1037's triage actually
// wrote (the zhengmingTriageCounts it returned), never what triage intended
// and never re-derived by re-querying here.
type ZhengmingTriageMsg struct {
	Retired    int    `msgpack:"retired"`
	Ascended   int    `msgpack:"ascended"`
	Cancelled  int    `msgpack:"cancelled"`
	Superseded int    `msgpack:"superseded"`
	Awaiting   int    `msgpack:"awaiting"`
	Text       string `msgpack:"text"`
}

// triageToastText renders the single dated line. Only classes that can still
// be retired appear: orphan (class D) is not in the vocabulary, so a counter
// that can only ever read x0 must not appear — that would be a claim about a
// disposition this pass never makes.
func triageToastText(counts zhengmingTriageCounts, awaiting int) string {
	parts := []string{
		fmt.Sprintf("edict ascended x%d", counts.Ascended),
		fmt.Sprintf("cancelled x%d", counts.Cancelled),
		fmt.Sprintf("superseded x%d", counts.Superseded),
	}
	return fmt.Sprintf("Zhengming triage: %d retired — %s · %d awaiting an answer (:gates)",
		counts.Retired, strings.Join(parts, ", "), awaiting)
}

// deliverPendingZhengming is the delivery half of reconnect reconciliation
// (e1043). The retirement half is e1037's triagePendingZhengming, called just
// before this; delivery only consumes what it returned.
//
// Ordering: triage retires → re-query survivors → deliver, one pass per
// SetNotify. Delivery is stateless — it caches and replays nothing, so a
// repeated install delivers only what is still pending at that moment and
// never delivers the same gate twice.
func (s *Court) deliverPendingZhengming(notify func(msg any), survivors []storage.Zhengming, counts zhengmingTriageCounts) {
	if notify == nil {
		return
	}
	awaiting := 0
	for i := range survivors {
		req := &survivors[i]
		if s.zhengmingBlocksAscent(req) {
			notify(ZhengmingPendingMsg{
				RequestID:  req.RequestID,
				EdictKey:   storage.EdictKey{ID: req.EdictID, Username: req.Username, Project: req.Project},
				MinisterID: req.MinisterID,
				ChannelID:  req.SessionID,
				Questions:  req.Questions,
				Priority:   req.Priority,
			})
			continue
		}
		awaiting++
	}
	// Silence: no retirements and no survivors → emit nothing at all. A
	// quiet boot must be quiet. Awaiting counts only the survivors that
	// opened no dialog — the dialogled ones are already on the screen.
	if counts.Retired == 0 && awaiting == 0 {
		return
	}
	notify(ZhengmingTriageMsg{
		Retired:    counts.Retired,
		Ascended:   counts.Ascended,
		Cancelled:  counts.Cancelled,
		Superseded: counts.Superseded,
		Awaiting:   awaiting,
		Text:       triageToastText(counts, awaiting),
	})
}

// zhengmingBlocksAscent reports whether a triage survivor still blocks ascent,
// using exactly two authoritative signals:
//   - a live waiter in the Court's zhengming dispatch map (a ritual is parked
//     in WaitForZhengming right now), or
//   - the request's edict has a ritual_executions row in state pending/running.
//
// A survivor with neither signal is a real gate but nobody is waiting on it —
// it must not steal the screen; it stays listed in :gates. Never widened to
// RitualStateStopped: a stopped ritual is parked, not live.
func (s *Court) zhengmingBlocksAscent(req *storage.Zhengming) bool {
	s.zhengming.mu.Lock()
	_, hasWaiter := s.zhengming.pending[req.RequestID]
	s.zhengming.mu.Unlock()
	if hasWaiter {
		return true
	}
	var live int64
	err := s.db.Model(&RitualExecution{}).
		Where("edict_id = ? AND username = ? AND project = ? AND state IN ?",
			req.EdictID, req.Username, req.Project,
			[]string{string(RitualStatePending), string(RitualStateRunning)}).
		Count(&live).Error
	return err == nil && live > 0
}
