package court

import (
	"context"
	"fmt"
	"sync"

	"github.com/afittestide/asimi/storage"
)

// dagUnit is a single schedulable unit in a dependency graph: an ID, the IDs
// it depends on, and an item to run. It is the ritual-free shape shared by the
// fork runner and the ling ignition trigger.
type dagUnit struct {
	ID     string
	DepIDs []string
	Index  int
	Item   interface{}
}

// dagResult records the outcome of one dispatched unit.
type dagResult struct {
	Unit  dagUnit
	Error error
}

// dagEngine runs dependency-ordered units with bounded concurrency. It is the
// single scheduler: the fork path and the ling ignition trigger both drive it.
type dagEngine struct {
	units     []dagUnit
	batchSize int
	name      string

	// allowIncomplete leaves units with unsatisfied dependencies pending
	// instead of failing the run. Fork steps require every unit to complete;
	// ling ignition does not, because a ling's dependency may be authored
	// (or running) in a later insert_ling call and is picked up then.
	allowIncomplete bool

	// seedDone marks units already completed before dispatch (e.g. read from
	// the DB).
	seedDone func(done map[string]bool)

	// refresh re-reads external completion state after every collected result
	// (e.g. a ritual step marked other lings done out-of-band).
	refresh func(done map[string]bool)

	// run executes one unit. A returned error is recorded but does not stop
	// scheduling; the unit still counts as done so its dependents proceed.
	run func(ctx context.Context, u dagUnit) error
}

// execute dispatches ready units (respecting batchSize) and marks them done as
// they complete, starting newly-unblocked units in the same run.
func (e *dagEngine) execute(ctx context.Context) ([]dagResult, error) {
	units := e.units
	if len(units) == 0 {
		return nil, nil
	}

	batchSize := e.batchSize
	if batchSize <= 0 {
		batchSize = 1
	}

	done := make(map[string]bool, len(units))
	if e.seedDone != nil {
		e.seedDone(done)
	}

	dispatched := make(map[int]bool, len(units))
	running := 0

	var results []dagResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, batchSize)
	resultCh := make(chan dagResult, len(units))

	collect := func(res dagResult) {
		mu.Lock()
		results = append(results, res)
		mu.Unlock()
		if res.Unit.Index >= 0 && res.Unit.Index < len(units) {
			done[units[res.Unit.Index].ID] = true
		}
		running--
		if e.refresh != nil {
			e.refresh(done)
		}
	}

	// acquire takes a worker slot, collecting one finished unit first if needed.
	acquire := func() bool {
		select {
		case sem <- struct{}{}:
			return true
		case res := <-resultCh:
			collect(res)
			select {
			case sem <- struct{}{}:
				return true
			case <-ctx.Done():
				return false
			}
		case <-ctx.Done():
			return false
		}
	}

	// readyIndices returns not-yet-dispatched units whose deps are all done.
	readyIndices := func() []int {
		var ready []int
		for _, idx := range seedReadyQueue(units, done) {
			if !dispatched[idx] {
				ready = append(ready, idx)
			}
		}
		return ready
	}

	for {
		for _, idx := range readyIndices() {
			if !acquire() {
				wg.Wait()
				return results, ctx.Err()
			}
			dispatched[idx] = true
			running++
			u := units[idx]
			wg.Add(1)
			go func(u dagUnit) {
				defer wg.Done()
				defer func() { <-sem }()
				resultCh <- dagResult{Unit: u, Error: e.run(ctx, u)}
			}(u)
		}

		if running == 0 {
			var blocked []string
			for _, u := range units {
				if !done[u.ID] {
					blocked = append(blocked, u.ID)
				}
			}
			if len(blocked) > 0 && !e.allowIncomplete {
				return results, fmt.Errorf("%s stuck: %d item(s) with unsatisfied dependencies: %v", e.name, len(blocked), blocked)
			}
			break
		}

		select {
		case res := <-resultCh:
			collect(res)
		case <-ctx.Done():
			wg.Wait()
			return results, ctx.Err()
		}
	}

	wg.Wait()
	for {
		select {
		case res := <-resultCh:
			collect(res)
		default:
			return results, nil
		}
	}
}

// buildDependencyMap extracts IDs and dependency lists from work units.
// Items without a "ling_id" field get auto-generated IDs.
// Items without a "dependencies" field have empty deps → immediately ready.
func buildDependencyMap(workUnits []interface{}) []dagUnit {
	units := make([]dagUnit, len(workUnits))
	for i, item := range workUnits {
		units[i] = dagUnit{Index: i, Item: item}

		m, ok := item.(map[string]interface{})
		if !ok {
			units[i].ID = fmt.Sprintf("_fork_%d", i)
			continue
		}

		if id, ok := m["ling_id"].(string); ok && id != "" {
			units[i].ID = id
		} else {
			units[i].ID = fmt.Sprintf("_fork_%d", i)
		}

		switch d := m["dependencies"].(type) {
		case []string:
			units[i].DepIDs = d
		case storage.StringArray:
			units[i].DepIDs = []string(d)
		case []interface{}:
			for _, v := range d {
				if s, ok := v.(string); ok {
					units[i].DepIDs = append(units[i].DepIDs, s)
				}
			}
		}
	}
	return units
}

// seedReadyQueue returns the indices of units whose dependencies are all
// satisfied. A unit with no dependencies is immediately ready.
func seedReadyQueue(units []dagUnit, done map[string]bool) []int {
	var ready []int
	for i, u := range units {
		if done[u.ID] {
			continue // already completed
		}
		allSatisfied := true
		for _, dep := range u.DepIDs {
			if !done[dep] {
				allSatisfied = false
				break
			}
		}
		if allSatisfied {
			ready = append(ready, i)
		}
	}
	return ready
}
