package court

import (
	"context"
	"fmt"
	"sync"

	"github.com/afittestide/asimi/storage"
)

// defaultLingMinister is used when a ling omits its minister.
const defaultLingMinister = "forge"

// lingBatchSize bounds how many lings execute concurrently during ignition.
const lingBatchSize = 5

// PublishLingCreated records a ling_created event for observability. It never
// starts work — ignition is a separate trigger.
func (s *Court) PublishLingCreated(key storage.EdictKey, lingID string) {
	if s == nil {
		return
	}
	s.PublishEvent(key, storage.EventLingCreated, storage.JSON{"ling_id": lingID})
}

// lingIgniteLock returns the per-edict mutex serializing ignition runs. It
// prevents overlapping triggers (e.g. two insert_ling calls racing) from
// double-running a ling.
func (s *Court) lingIgniteLock(edictID uint) *sync.Mutex {
	s.lingIgniteMu.Lock()
	defer s.lingIgniteMu.Unlock()
	if s.lingIgniting == nil {
		s.lingIgniting = make(map[uint]*sync.Mutex)
	}
	mu, ok := s.lingIgniting[edictID]
	if !ok {
		mu = &sync.Mutex{}
		s.lingIgniting[edictID] = mu
	}
	return mu
}

// TriggerLingIgnition is the automatic batch-completion trigger for insert_ling.
// It returns immediately; ignition runs in the background, serialized per edict.
func (s *Court) TriggerLingIgnition(key storage.EdictKey) {
	if s == nil || s.db == nil || key.ID == 0 {
		return
	}
	// Nil-config safe: bootstrap fills config lazily, so a nil or absent
	// config must behave as "disabled" and still record the skip in the ledger.
	if s.config == nil || !s.config.LingIgnitionEnabled {
		s.logger.Info("ling ignition disabled by config", "edict_id", key.ID)
		s.PublishEvent(key, storage.EventLingIgnitionSkipped, storage.JSON{
			"reason":       "ling_ignition_disabled_by_config",
			"pending_e531": true,
		})
		return
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		mu := s.lingIgniteLock(key.ID)
		mu.Lock()
		defer mu.Unlock()
		if err := s.igniteLings(ctx, key); err != nil {
			s.logger.Warn("ling ignition failed", "edict_id", key.ID, "error", err)
		}
	}()
}

// igniteLings loads the edict's lings and runs every pending ling whose
// dependencies are all done through the shared DAG engine. Lings with unmet
// intra-batch deps are started by the engine as their deps complete.
func (s *Court) igniteLings(ctx context.Context, key storage.EdictKey) error {
	var lings []storage.Ling
	if err := s.db.Where("edict_id = ? AND username = ? AND project = ?",
		key.ID, key.Username, key.Project).
		Order("created_at ASC").
		Find(&lings).Error; err != nil {
		return fmt.Errorf("load lings for ignition: %w", err)
	}

	units := make([]dagUnit, 0, len(lings))
	for _, l := range lings {
		if l.Status != storage.LingPending {
			continue // already running or done
		}
		units = append(units, dagUnit{
			ID:     l.LingID,
			DepIDs: []string(l.Dependencies),
			Index:  len(units),
			Item:   l.LingID,
		})
	}
	if len(units) == 0 {
		return nil
	}

	engine := &dagEngine{
		units:           units,
		batchSize:       lingBatchSize,
		name:            "ling ignition",
		allowIncomplete: true,
		seedDone: func(done map[string]bool) {
			for _, l := range lings {
				if l.Status == storage.LingDone {
					done[l.LingID] = true
				}
			}
		},
		run: func(ctx context.Context, u dagUnit) error {
			return s.runLing(ctx, key, u.ID)
		},
	}
	_, err := engine.execute(ctx)
	return err
}

// runLing executes a single ling as a plain session: it resolves the ling's
// minister (defaulting to forge), creates a session and runs the ling's
// description as the prompt. On success the ling is marked done; failures are
// reported as ling_failed events.
func (s *Court) runLing(ctx context.Context, key storage.EdictKey, lingID string) error {
	var ling storage.Ling
	if err := s.db.First(&ling, "ling_id = ?", lingID).Error; err != nil {
		return fmt.Errorf("load ling %s: %w", lingID, err)
	}

	ministerID := ling.Minister
	if ministerID == "" {
		ministerID = defaultLingMinister
	}
	minister := s.GetMinister(ministerID)
	if minister == nil {
		err := fmt.Errorf("minister %q not found", ministerID)
		s.failLing(key, lingID, ministerID, err)
		return err
	}

	s.PublishEvent(key, storage.EventLingStarted, storage.JSON{
		"ling_id":  lingID,
		"minister": ministerID,
	})
	s.db.Model(&storage.Ling{}).
		Where("ling_id = ? AND username = ? AND project = ?", lingID, key.Username, key.Project).
		Update("status", storage.LingInProgress)

	session, err := CreateSession(minister, minister.Model(), s.sessionCfg, s.notify, ritualChannelID(key.ID), key)
	if err != nil {
		s.failLing(key, lingID, ministerID, err)
		return err
	}
	if _, err := session.AskWithStreaming(ctx, ling.Description, nil); err != nil {
		s.failLing(key, lingID, ministerID, err)
		return err
	}

	// Same status update as record_ling_completed.
	if err := s.db.Model(&storage.Ling{}).
		Where("ling_id = ? AND username = ? AND project = ?", lingID, key.Username, key.Project).
		Update("status", storage.LingDone).Error; err != nil {
		s.failLing(key, lingID, ministerID, err)
		return err
	}
	s.PublishEvent(key, storage.EventLingCompleted, storage.JSON{
		"ling_id":  lingID,
		"minister": ministerID,
	})
	return nil
}

func (s *Court) failLing(key storage.EdictKey, lingID, ministerID string, err error) {
	s.PublishEvent(key, storage.EventLingFailed, storage.JSON{
		"ling_id":  lingID,
		"minister": ministerID,
		"error":    err.Error(),
	})
}
