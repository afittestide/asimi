package court

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/storage"
)

func ek(id uint) storage.EdictKey {
	return storage.EdictKey{ID: id, Username: "testuser", Project: "testproject"}
}

// TestExecuteForkStep_Parallel tests parallel fork execution
func TestExecuteForkStep_Parallel(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "fork-parallel-test",
		Steps: []RitualStep{
			{
				Name:     "prepare",
				Minister: "war",
				Task:     "prepare work units",
			},
			{
				Name: "process-all",
				Fork: &ForkDef{
					Over:      "work_units",
					BatchSize: 2,
				},
				Work: []RitualStep{
					{Name: "process", Minister: "forge", Act: "process {{ .item }}"},
				},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	strategistM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "war",
		tasksCh:      make(chan *Task, 1),
		result:       "work units prepared",
	}
	go strategistM.Run(ctx)
	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 10),
		result:       "processed",
	}
	go forgeM.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"war": strategistM, "forge": forgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "fork-parallel-test", ek(1), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	// Inject work units into context
	exec.Data = storage.JSON{
		"work_units": []interface{}{"file1.go", "file2.go", "file3.go"},
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	if exec.State != RitualStateCompleted {
		t.Errorf("Expected state 'completed', got %s", exec.State)
	}

	// Verify all items were processed via fork results
	forkData, ok := exec.Data["fork"].(map[string]interface{})
	if !ok {
		t.Fatal("expected fork data in exec.Data")
	}
	out, ok := forkData["out"].([]ForkResult)
	if !ok {
		t.Fatal("expected fork out results")
	}
	if len(out) != 3 {
		t.Errorf("Expected 3 fork results, got %d", len(out))
	}
}

// TestExecuteForkStep_Sequential tests sequential fork execution
func TestExecuteForkStep_Sequential(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "fork-sequential-test",
		Steps: []RitualStep{
			{
				Name: "process-all",
				Fork: &ForkDef{
					Over:      "items",
					BatchSize: 1,
				},
				Work: []RitualStep{
					{Name: "process", Minister: "forge", Act: "process {{ .item }}"},
				},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 10),
		result:       "done",
	}
	go forgeM.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": forgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "fork-sequential-test", ek(2), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	exec.Data = storage.JSON{
		"items": []interface{}{"first", "second", "third"},
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	// Verify all 3 items were processed via fork results
	forkData, ok := exec.Data["fork"].(map[string]interface{})
	if !ok {
		t.Fatal("expected fork data in exec.Data")
	}
	out, ok := forkData["out"].([]ForkResult)
	if !ok {
		t.Fatal("expected fork out results")
	}
	if len(out) != 3 {
		t.Errorf("Expected 3 items processed, got %d", len(out))
	}

	// Verify sequential: with BatchSize=1, items run one at a time
	// Each result should have the correct item
	items := map[string]bool{}
	for _, r := range out {
		items[fmt.Sprintf("%v", r.Item)] = true
	}
	for _, expected := range []string{"first", "second", "third"} {
		if !items[expected] {
			t.Errorf("Expected item %q in results", expected)
		}
	}
}

// TestExecuteForkStep_WithLimit tests fork execution with limit
func TestExecuteForkStep_WithLimit(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "fork-limit-test",
		Steps: []RitualStep{
			{
				Name: "process-all",
				Fork: &ForkDef{
					Over:      "items",
					BatchSize: 1,
					Limit:     "2",
				},
				Work: []RitualStep{
					{Name: "process", Minister: "forge", Act: "process {{ .item }}"},
				},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 10),
		result:       "done",
	}
	go forgeM.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": forgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "fork-limit-test", ek(3), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	exec.Data = storage.JSON{
		"items": []interface{}{"one", "two", "three", "four", "five"},
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	// Verify only 2 items were processed (limit)
	forkData, ok := exec.Data["fork"].(map[string]interface{})
	if !ok {
		t.Fatal("expected fork data in exec.Data")
	}
	out, ok := forkData["out"].([]ForkResult)
	if !ok {
		t.Fatal("expected fork out results")
	}
	if len(out) != 2 {
		t.Errorf("Expected 2 items processed (limit), got %d", len(out))
	}
}

// TestGetForkWorkUnits tests retrieving work units from context
func TestGetForkWorkUnits(t *testing.T) {
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, nil, repo.RepoInfo{})

	tests := []struct {
		name      string
		execData  storage.JSON
		over      string
		wantCount int
		wantErr   bool
	}{
		{
			name: "from exec.Data",
			execData: storage.JSON{
				"files": []interface{}{"a.go", "b.go"},
			},
			over:      "files",
			wantCount: 2,
			wantErr:   false,
		},
		{
			name: "from step_results",
			execData: storage.JSON{
				"step_results": map[string]interface{}{
					"output": []interface{}{"x", "y", "z"},
				},
			},
			over:      "output",
			wantCount: 3,
			wantErr:   false,
		},
		{
			name:      "key not found",
			execData:  storage.JSON{},
			over:      "missing",
			wantCount: 0,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &RitualExecution{
				ID:         "test-exec",
				RitualName: "test",
				Data:       tt.execData,
			}

			units, err := runner.getForkWorkUnits(exec, tt.over)
			if (err != nil) != tt.wantErr {
				t.Errorf("getForkWorkUnits() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(units) != tt.wantCount {
				t.Errorf("expected %d work units, got %d", tt.wantCount, len(units))
			}
		})
	}
}

// TestToInterfaceSlice tests conversion of various slice types
func TestToInterfaceSlice(t *testing.T) {
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, nil, repo.RepoInfo{})

	tests := []struct {
		name    string
		input   interface{}
		wantLen int
		wantErr bool
	}{
		{
			name:    "interface slice",
			input:   []interface{}{"a", "b", "c"},
			wantLen: 3,
			wantErr: false,
		},
		{
			name:    "map slice",
			input:   []map[string]interface{}{{"key": "val1"}, {"key": "val2"}},
			wantLen: 2,
			wantErr: false,
		},
		{
			name:    "string slice via json",
			input:   []string{"x", "y"},
			wantLen: 2,
			wantErr: false,
		},
		{
			name:    "int slice via json",
			input:   []int{1, 2, 3, 4},
			wantLen: 4,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.toInterfaceSlice(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("toInterfaceSlice() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(result) != tt.wantLen {
				t.Errorf("expected %d items, got %d", tt.wantLen, len(result))
			}
		})
	}
}

// TestExecuteForkItem tests single fork item execution with multi-step work
func TestExecuteForkItem(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "fork-item-test",
		Steps: []RitualStep{
			{
				Name: "process",
				Fork: &ForkDef{
					Over:      "items",
					BatchSize: 1,
				},
				Work: []RitualStep{
					{Name: "step1", Minister: "forge", Act: "work on {{ .item }}"},
					{Name: "step2", Minister: "judge", Act: "verify {{ .item }}"},
				},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "forge done",
	}
	go forgeM.Run(ctx)
	judgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "judge",
		tasksCh:      make(chan *Task, 1),
		result:       "judge done",
	}
	go judgeM.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": forgeM, "judge": judgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "fork-item-test", ek(4), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	exec.Data = storage.JSON{
		"items": []interface{}{"item1"},
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	// Verify fork results stored
	forkData, ok := exec.Data["fork"].(map[string]interface{})
	if !ok {
		t.Fatal("expected fork data in exec.Data")
	}

	out, ok := forkData["out"].([]ForkResult)
	if !ok {
		t.Fatal("expected fork out results")
	}

	if len(out) != 1 {
		t.Errorf("expected 1 successful fork result, got %d", len(out))
	}
}

// TestExecuteForkStep_FailureHandling tests fork execution with failures
func TestExecuteForkStep_FailureHandling(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "fork-failure-test",
		Steps: []RitualStep{
			{
				Name: "process-all",
				Fork: &ForkDef{
					Over:      "items",
					BatchSize: 1,
				},
				Work: []RitualStep{
					{Name: "process", Minister: "forge", Act: "process {{ .item }}"},
				},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	// Minister that fails with an error
	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 10),
		result:       "success",
		err:          fmt.Errorf("processing failed"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	go forgeM.Run(ctx)
	defer cancel()

	court := &Court{
		ministers: map[string]Minister{"forge": forgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "fork-failure-test", ek(5), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	exec.Data = storage.JSON{
		"items": []interface{}{"item1", "item2", "item3"},
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	// Verify fork results include failures (all items fail because the model returns an error)
	forkData, ok := exec.Data["fork"].(map[string]interface{})
	if !ok {
		t.Fatal("expected fork data in exec.Data")
	}

	errs, ok := forkData["err"].([]ForkResult)
	if !ok {
		t.Fatal("expected fork err results")
	}

	if len(errs) != 3 {
		t.Errorf("expected 3 failed results, got %d", len(errs))
	}
}

// TestExecuteForkStep_Notification tests that fork steps send proper notifications
func TestExecuteForkStep_Notification(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "fork-notify-test",
		Steps: []RitualStep{
			{
				Name: "process-all",
				Fork: &ForkDef{
					Over:      "items",
					BatchSize: 1,
				},
				Work: []RitualStep{
					{Name: "process", Minister: "forge", Act: "process {{ .item }}"},
				},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 10),
		result:       "done",
	}
	go forgeM.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": forgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	var messages []RitualStepMsg
	notify := func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok {
			messages = append(messages, stepMsg)
		}
	}

	exec, err := runner.Start(ctx, "fork-notify-test", ek(6), nil, notify)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	exec.Data = storage.JSON{
		"items": []interface{}{"item1", "item2"},
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	// Verify fork notifications
	var forkStarted, forkCompleted int
	var forkItemMsgs []RitualStepMsg
	for _, msg := range messages {
		if msg.StepName == "process-all" {
			switch msg.Status {
			case "started":
				forkStarted++
				if !strings.Contains(msg.Message, "fork over") {
					t.Error("expected fork started message to mention 'fork over'")
				}
			case "completed":
				forkCompleted++
				if !strings.Contains(msg.Message, "Fork completed") {
					t.Error("expected fork completed message to contain summary")
				}
			}
		}
		if msg.ForkItem != "" {
			forkItemMsgs = append(forkItemMsgs, msg)
		}
	}

	if forkStarted != 1 {
		t.Errorf("expected 1 fork started notification, got %d", forkStarted)
	}
	// Allow for 1 or 2 completed notifications (one per step execution context)
	if forkCompleted < 1 {
		t.Errorf("expected at least 1 fork completed notification, got %d", forkCompleted)
	}

	// Verify ForkItem is populated on fork work step messages and that
	// StepIndex/TotalSteps reflect the work step's position within the fork.
	if len(forkItemMsgs) == 0 {
		t.Error("expected at least one notification with ForkItem set")
	}
	for _, msg := range forkItemMsgs {
		if msg.ForkItem != "1/2" && msg.ForkItem != "2/2" {
			t.Errorf("expected ForkItem to be '1/2' or '2/2', got %q", msg.ForkItem)
		}
		if msg.TotalSteps != 1 {
			t.Errorf("expected TotalSteps to be 1 within single-work-step fork, got %d", msg.TotalSteps)
		}
		if msg.StepIndex != 0 {
			t.Errorf("expected StepIndex to be 0 within single-work-step fork, got %d", msg.StepIndex)
		}
	}
	// Verify all expected ForkItem values are present
	seen := map[string]bool{}
	for _, msg := range forkItemMsgs {
		seen[msg.ForkItem] = true
	}
	if !seen["1/2"] {
		t.Error("expected ForkItem '1/2' in notifications")
	}
	if !seen["2/2"] {
		t.Error("expected ForkItem '2/2' in notifications")
	}
}

// TestExecuteForkStep_TemplateExpansion tests template expansion in fork work steps
func TestExecuteForkStep_TemplateExpansion(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "fork-template-test",
		Steps: []RitualStep{
			{
				Name: "process-all",
				Fork: &ForkDef{
					Over:      "files",
					BatchSize: 1,
				},
				Work: []RitualStep{
					{Name: "fix", Minister: "forge", Act: "Fix {{ .item }}"},
				},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 10),
		result:       "fixed",
	}
	go forgeM.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": forgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "fork-template-test", ek(7), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	exec.Data = storage.JSON{
		"files": []interface{}{"main.go", "utils.go"},
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	// Verify all items processed
	forkData, ok := exec.Data["fork"].(map[string]interface{})
	if !ok {
		t.Fatal("expected fork data in exec.Data")
	}
	out, ok := forkData["out"].([]ForkResult)
	if !ok {
		t.Fatal("expected fork out results")
	}

	if len(out) != 2 {
		t.Errorf("expected 2 work items, got %d", len(out))
	}

	// Verify each item was processed (items are in the fork results)
	items := map[string]bool{}
	for _, r := range out {
		items[fmt.Sprintf("%v", r.Item)] = true
	}
	if !items["main.go"] {
		t.Error("expected template expansion for main.go")
	}
	if !items["utils.go"] {
		t.Error("expected template expansion for utils.go")
	}
}

// TestExecuteForkStep_Cancelation tests fork execution cancellation
func TestExecuteForkStep_Cancelation(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "fork-cancel-test",
		Steps: []RitualStep{
			{
				Name: "process-all",
				Fork: &ForkDef{
					Over:      "items",
					BatchSize: 1,
				},
				Work: []RitualStep{
					{Name: "process", Minister: "forge", Act: "process {{ .item }}"},
				},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	ctx, cancel := context.WithCancel(context.Background())

	// Minister that returns context.Canceled error
	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 10),
		result:       "done",
		err:          context.Canceled,
	}
	go forgeM.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": forgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "fork-cancel-test", ek(8), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	exec.Data = storage.JSON{
		"items": []interface{}{"item1", "item2", "item3", "item4", "item5"},
	}

	// Cancel immediately
	cancel()

	// Run will be cancelled
	_ = runner.Run(ctx, exec)

	// Verify fork results show errors (cancelled)
	forkData, ok := exec.Data["fork"].(map[string]interface{})
	if !ok {
		// Fork may not have started at all if cancelled fast enough
		return
	}
	out, _ := forkData["out"].([]ForkResult)
	if len(out) >= 5 {
		t.Errorf("expected cancellation to stop processing, but all %d items succeeded", len(out))
	}
}

// TestForkStepStates_IsolatesItemState is the invariant test for edict 900:
// a fork item's step states must not alias the parent's backing array, and
// each item's Session must be reset so it cannot be shared across concurrent
// goroutines.
func TestForkStepStates_IsolatesItemState(t *testing.T) {
	parent := []RitualStepState{
		{Name: "step1", Message: "parent-1", Session: &Session{ID: "shared"}},
		{Name: "step2", Message: "parent-2", Session: &Session{ID: "shared"}},
	}

	item := forkStepStates(parent)

	// The copy must not alias the parent backing array.
	if &item[0] == &parent[0] {
		t.Fatal("forkStepStates returned a slice aliasing the parent backing array")
	}

	// Mutating the copy must leave the parent untouched.
	item[0].Message = "item-mutated"
	item[0].Status = "completed"
	if parent[0].Message != "parent-1" || parent[0].Status != "" {
		t.Errorf("mutating fork state leaked into parent: %+v", parent[0])
	}

	// Sessions must never be shared with concurrent items.
	for i := range item {
		if item[i].Session != nil {
			t.Errorf("item[%d].Session = %p, want nil (sessions must be item-private)", i, item[i].Session)
		}
	}

	// Non-session fields are copied verbatim.
	if item[1].Name != "step2" || item[1].Message != "parent-2" {
		t.Errorf("forkStepStates did not preserve non-session fields: %+v", item[1])
	}
}

// TestForkStepStates_NilSafe verifies forkStepStates handles an empty parent
// (a ritual step with no prior states) without panicking.
func TestForkStepStates_NilSafe(t *testing.T) {
	if got := forkStepStates(nil); len(got) != 0 {
		t.Errorf("forkStepStates(nil) = %v, want empty", got)
	}
	if got := forkStepStates([]RitualStepState{}); len(got) != 0 {
		t.Errorf("forkStepStates(empty) = %v, want empty", got)
	}
}

// TestSaveExecution_ForkItemNoOp verifies the invariant that a fork item never
// persists: saveExecution is a no-op whenever forkItem is set, so sibling
// goroutines cannot db.Save shared rows concurrently.
func TestSaveExecution_ForkItemNoOp(t *testing.T) {
	db := setupRitualTestDB(t)

	exec := &RitualExecution{
		ID:         "fork-exec-1",
		RitualName: "fork-noop",
		EdictID:    900,
		Username:   "testuser",
		Project:    "testproject",
		forkItem:   true,
		stepStates: []RitualStepState{{Name: "step1"}},
	}

	runner := NewRitualRunner(NewRitualRegistry(), nil, nil, db, nil, slog.Default(), repo.RepoInfo{})
	runner.saveExecution(exec)

	var count int64
	if err := db.Model(&RitualExecution{}).Where("id = ?", exec.ID).Count(&count).Error; err != nil {
		t.Fatalf("count executions: %v", err)
	}
	if count != 0 {
		t.Errorf("fork item persisted %d execution row(s); want 0", count)
	}

	var stateCount int64
	if err := db.Model(&RitualStepState{}).Where("execution_id = ?", exec.ID).Count(&stateCount).Error; err != nil {
		t.Fatalf("count step states: %v", err)
	}
	if stateCount != 0 {
		t.Errorf("fork item persisted %d step state row(s); want 0", stateCount)
	}
}

// TestSaveExecution_SnapshotsLiveState verifies that persistence does not write
// gorm's generated columns back into the live execution/step-state structs.
// Those writes would race with concurrent readers (edict 900).
func TestSaveExecution_SnapshotsLiveState(t *testing.T) {
	db := setupRitualTestDB(t)

	exec := &RitualExecution{
		ID:         "snapshot-exec-1",
		RitualName: "snapshot",
		EdictID:    900,
		Username:   "testuser",
		Project:    "testproject",
		stepStates: []RitualStepState{{ExecutionID: "snapshot-exec-1", Name: "step1"}},
	}

	runner := NewRitualRunner(NewRitualRegistry(), nil, nil, db, nil, slog.Default(), repo.RepoInfo{})
	runner.saveExecution(exec)

	// A fresh row gets an auto-increment id from the DB. That id must live on
	// the persisted row, not be written into the live struct by Save.
	if exec.stepStates[0].ID != 0 {
		t.Errorf("saveExecution wrote generated id %d into live step state", exec.stepStates[0].ID)
	}

	var stored RitualStepState
	if err := db.Where("execution_id = ?", exec.ID).First(&stored).Error; err != nil {
		t.Fatalf("persisted step state not found: %v", err)
	}
	if stored.ID == 0 {
		t.Error("persisted step state has no generated id")
	}
}

// TestRaceGate_FailsClosed guards the permanent gate wired by edict 900. It is
// a static check, not a test of the race detector itself: it asserts the
// justfile wiring is such that `just test` propagates a `just test-race`
// failure. Without `set -e`, test-race is a non-final command and its non-zero
// exit would be swallowed — the exact silent regression this edict prevents.
func TestRaceGate_FailsClosed(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "Justfile"))
	if err != nil {
		t.Fatalf("read Justfile: %v", err)
	}
	content := string(data)

	// The test recipe must invoke the race recipe...
	if !strings.Contains(content, "just test-race") {
		t.Fatal("Justfile `test` recipe does not invoke `just test-race`")
	}

	// ...and the test recipe must run with errexit so a failing race step
	// aborts it (`set -e` or the combined `set -euo pipefail`).
	testRecipe := recipeBody(content, "test:")
	if testRecipe == "" {
		t.Fatal("could not extract `test:` recipe body from Justfile")
	}
	if !strings.Contains(testRecipe, "set -e") {
		t.Fatal("`test:` recipe lacks `set -e`; a failing `just test-race` would be swallowed and `just test` would exit 0")
	}

	// The race recipe must actually enable the race detector.
	raceRecipe := recipeBody(content, "test-race")
	if raceRecipe == "" {
		t.Fatal("could not extract `test-race` recipe body from Justfile")
	}
	if !strings.Contains(raceRecipe, "-race") {
		t.Fatal("`test-race` recipe does not pass -race to go test")
	}
}

// recipeBody returns the indented body of the recipe whose header line starts
// with header (e.g. "test:" or "test-race"). It stops at the next unindented,
// non-blank line.
func recipeBody(justfile, header string) string {
	lines := strings.Split(justfile, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, header) && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return ""
	}
	var body []string
	for _, line := range lines[start:] {
		if line == "" {
			body = append(body, line)
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		body = append(body, line)
	}
	return strings.Join(body, "\n")
}

// mockCallCountRunner implements runners.Runner for fork tests
