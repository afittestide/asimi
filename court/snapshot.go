package court

import (
	"sort"
	"time"

	"github.com/afittestide/asimi/storage"
)

// RitualEntry represents a ritual execution (active or historical) for dashboard display.
type RitualEntry struct {
	RitualName  string        `msgpack:"ritual_name"`
	EdictID     uint          `msgpack:"edict_id,omitempty"`
	State       RitualState   `msgpack:"state"`
	CurrentStep int           `msgpack:"current_step"`
	TotalSteps  int           `msgpack:"total_steps"`
	StepName    string        `msgpack:"step_name,omitempty"`
	Age         time.Duration `msgpack:"age"`
	StartedAt   time.Time     `msgpack:"started_at"`
}

// EventEntry represents a single event from the tian_events ledger.
type EventEntry struct {
	Time      time.Time `msgpack:"time"`
	EventType string    `msgpack:"event_type"`
	Detail    string    `msgpack:"detail,omitempty"`
	EdictID   uint      `msgpack:"edict_id,omitempty"`
}

// ZhengmingEntry represents a pending clarification request for dashboard display.
type ZhengmingEntry struct {
	RequestID  string    `msgpack:"request_id"`
	EdictID    uint      `msgpack:"edict_id,omitempty"`
	MinisterID string    `msgpack:"minister_id"`
	Questions  []string  `msgpack:"questions,omitempty"`
	Priority   string    `msgpack:"priority,omitempty"`
	Status     string    `msgpack:"status,omitempty"`
	CreatedAt  time.Time `msgpack:"created_at"`
	TimeoutAt  time.Time `msgpack:"timeout_at"`
}

// Snapshot captures the current state of the court for dashboard display.
type Snapshot struct {
	Rituals   []RitualEntry    `msgpack:"rituals,omitempty"`
	Events    []EventEntry     `msgpack:"events,omitempty"`
	Zhengming []ZhengmingEntry `msgpack:"zhengming,omitempty"`
	TakenAt   time.Time        `msgpack:"taken_at"`
}

// TakeSnapshot returns a point-in-time snapshot of live rituals and recent events.
func (s *Court) TakeSnapshot() Snapshot {
	snap := Snapshot{TakenAt: time.Now()}

	// Rituals from the ritual runner (all states)
	if rr := s.GetRitualRunner(); rr != nil {
		execs, err := rr.ListExecutions(storage.EdictKey{Username: s.config.Username, Project: s.config.Project})
		if err == nil {
			for _, ex := range execs {
				age := time.Since(ex.CreatedAt)
				if ex.State != RitualStatePending && ex.State != RitualStateRunning {
					// For completed rituals, age = duration from creation to last update
					age = ex.UpdatedAt.Sub(ex.CreatedAt)
				}
				entry := RitualEntry{
					RitualName:  ex.RitualName,
					EdictID:     ex.EdictID,
					State:       ex.State,
					CurrentStep: ex.CurrentStep,
					Age:         age,
					StartedAt:   ex.CreatedAt,
				}
				// Get step info from the ritual definition
				if def := rr.registry.Get(ex.RitualName); def != nil {
					entry.TotalSteps = len(def.Steps)
					if ex.CurrentStep >= 0 && ex.CurrentStep < len(def.Steps) {
						entry.StepName = def.Steps[ex.CurrentStep].Name
					}
				}
				// Try to get step name from step states in DB
				/* TODO: why do we need this?
				var stepState RitualStepState
				if err := s.db.Where("execution_id = ? AND step_index = ?", ex.ID, ex.CurrentStep).
					First(&stepState).Error; err == nil && stepState.Name != "" {
					entry.StepName = stepState.Name
				}
				*/
				snap.Rituals = append(snap.Rituals, entry)
			}
			// Sort: active rituals first, then by age descending (most recent first)
			sort.Slice(snap.Rituals, func(i, j int) bool {
				iActive := snap.Rituals[i].State == RitualStatePending || snap.Rituals[i].State == RitualStateRunning
				jActive := snap.Rituals[j].State == RitualStatePending || snap.Rituals[j].State == RitualStateRunning
				if iActive != jActive {
					return iActive
				}
				return snap.Rituals[i].StartedAt.After(snap.Rituals[j].StartedAt)
			})
		}
	}

	// Recent events from tian_events (filtered by username/project)
	var events []storage.TianEvent
	if err := s.db.Where("username = ? AND project = ?", s.config.Username, s.config.Project).
		Order("created_at DESC").Limit(50).Find(&events).Error; err == nil {
		for _, ev := range events {
			snap.Events = append(snap.Events, EventEntry{
				Time:      ev.CreatedAt,
				EventType: string(ev.EventType),
				Detail:    extractDetail(ev),
				EdictID:   ev.EdictID,
			})
		}
	}

	// Pending zhengming (clarification requests, filtered by username/project)
	var pending []storage.Zhengming
	if err := s.db.Where("status = ? AND username = ? AND project = ?", storage.ZhengmingPending, s.config.Username, s.config.Project).
		Order("priority DESC, created_at ASC").Limit(20).Find(&pending).Error; err == nil {
		for _, z := range pending {
			var questions []string
			for _, q := range z.Questions {
				text := q.Summary
				if text == "" {
					text = q.Text
					if len(text) > 60 {
						text = text[:57] + "..."
					}
				}
				questions = append(questions, text)
			}
			snap.Zhengming = append(snap.Zhengming, ZhengmingEntry{
				RequestID:  z.RequestID,
				EdictID:    z.EdictID,
				MinisterID: z.MinisterID,
				Questions:  questions,
				Priority:   string(z.Priority),
				Status:     string(z.Status),
				CreatedAt:  z.CreatedAt,
				TimeoutAt:  z.TimeoutAt,
			})
		}
	}

	return snap
}

func extractDetail(ev storage.TianEvent) string {
	if ev.Payload == nil {
		return ""
	}
	payload := map[string]interface{}(ev.Payload)
	// Mirror the keys production publishers actually write, and mirror
	// tianEventDetail (tian_events_tool.go): detail always derives from the
	// full payload, regardless of the tool's summary/full projection.
	for _, key := range []string{"ritual", "step", "minister_id", "intent"} {
		if name, ok := payload[key].(string); ok && name != "" {
			return name
		}
	}
	return ""
}
