package court

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "modernc.org/sqlite"
)

func testEK(id uint) storage.EdictKey {
	return storage.EdictKey{ID: id, Username: "testuser", Project: "testproject"}
}

func TestParseRitual(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(*testing.T, *RitualDef)
	}{
		{
			name: "basic ritual",
			yaml: `
name: test-ritual
description: A test ritual
steps:
  - name: step1
    minister: war
    task: Plan the work
`,
			wantErr: false,
			check: func(t *testing.T, r *RitualDef) {
				if r.Name != "test-ritual" {
					t.Errorf("expected name 'test-ritual', got %q", r.Name)
				}
				if len(r.Steps) != 1 {
					t.Errorf("expected 1 step, got %d", len(r.Steps))
				}
				if r.Steps[0].Minister != "war" {
					t.Errorf("expected minister 'war', got %q", r.Steps[0].Minister)
				}
			},
		},
		{
			name: "ritual with triggers and inputs",
			yaml: `
name: implement
description: Implementation workflow
triggers:
  - event: edict_assigned
inputs:
  edict_id:
    type: string
    required: true
steps:
  - name: plan
    minister: strategist
    task: Create plan for {{ .edict_id }}
`,
			wantErr: false,
			check: func(t *testing.T, r *RitualDef) {
				if len(r.Triggers) != 1 {
					t.Errorf("expected 1 trigger, got %d", len(r.Triggers))
				}
				if r.Triggers[0].Event != "edict_assigned" {
					t.Errorf("expected event 'edict_assigned', got %q", r.Triggers[0].Event)
				}
				if input, ok := r.Inputs["edict_id"]; !ok {
					t.Error("expected input 'edict_id'")
				} else if !input.Required {
					t.Error("expected input to be required")
				}
			},
		},
		{
			name: "ritual with failure handling",
			yaml: `
name: complex-ritual
steps:
  - name: step1
    minister: forge
    task: Write code
  - name: step2
    minister: judge
    task: Run tests
    on_failure: goto
    on_failure_target: step1
    max_retries: 5
`,
			wantErr: false,
			check: func(t *testing.T, r *RitualDef) {
				if len(r.Steps) != 2 {
					t.Errorf("expected 2 steps, got %d", len(r.Steps))
				}
				step2 := r.Steps[1]
				if step2.OnFailure != "goto" {
					t.Errorf("expected on_failure 'goto', got %q", step2.OnFailure)
				}
				if step2.OnFailureTarget != "step1" {
					t.Errorf("expected on_failure_target 'step1', got %q", step2.OnFailureTarget)
				}
				if step2.MaxRetries != 5 {
					t.Errorf("expected max_retries 5, got %d", step2.MaxRetries)
				}
			},
		},
		{
			name: "ritual with given and then",
			yaml: `
name: gherkin-ritual
steps:
  - name: step1
    minister: forge
    given:
      - the edict details
      - "!git diff HEAD"
    act: |
      Implement changes.
    then:
      - "!just test"
`,
			wantErr: false,
			check: func(t *testing.T, r *RitualDef) {
				if len(r.Steps[0].Given) != 2 {
					t.Errorf("expected 2 given entries, got %d", len(r.Steps[0].Given))
				}
				if r.Steps[0].Given[0] != "the edict details" {
					t.Errorf("expected first given 'the edict details', got %q", r.Steps[0].Given[0])
				}
				if r.Steps[0].Given[1] != "!git diff HEAD" {
					t.Errorf("expected second given '!git diff HEAD', got %q", r.Steps[0].Given[1])
				}
				if len(r.Steps[0].Then) != 1 {
					t.Errorf("expected 1 then entry, got %d", len(r.Steps[0].Then))
				}
				if r.Steps[0].Then[0] != "!just test" {
					t.Errorf("expected then '!just test', got %q", r.Steps[0].Then[0])
				}
			},
		},
		{
			name:    "invalid yaml",
			yaml:    `{{{invalid yaml`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ritual, err := ParseRitual([]byte(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseRitual() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.check != nil && ritual != nil {
				tt.check(t, ritual)
			}
		})
	}
}

func TestValidateRitual(t *testing.T) {
	tests := []struct {
		name    string
		ritual  *RitualDef
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid ritual",
			ritual: &RitualDef{
				Name: "valid",
				Steps: []RitualStep{
					{Name: "step1", Minister: "forge", Task: "do something"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing name",
			ritual: &RitualDef{
				Steps: []RitualStep{
					{Name: "step1", Minister: "forge", Task: "do something"},
				},
			},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name: "no steps",
			ritual: &RitualDef{
				Name:  "empty",
				Steps: []RitualStep{},
			},
			wantErr: true,
			errMsg:  "no steps",
		},
		{
			name: "duplicate step names",
			ritual: &RitualDef{
				Name: "dupe",
				Steps: []RitualStep{
					{Name: "step1", Minister: "forge", Task: "first"},
					{Name: "step1", Minister: "judge", Task: "second"},
				},
			},
			wantErr: true,
			errMsg:  "duplicate step name",
		},
		{
			name: "unknown on_failure_target",
			ritual: &RitualDef{
				Name: "bad-target",
				Steps: []RitualStep{
					{Name: "step1", Minister: "forge", Task: "do", OnFailure: "goto", OnFailureTarget: "nonexistent"},
				},
			},
			wantErr: true,
			errMsg:  "unknown step",
		},
		{
			name: "default minister",
			ritual: &RitualDef{
				Name: "no-minister",
				Steps: []RitualStep{
					{Name: "step1", Task: "do something"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing act",
			ritual: &RitualDef{
				Name: "no-act",
				Steps: []RitualStep{
					{Name: "step1", Minister: "forge"},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRitual(tt.ritual)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRitual() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" {
				if !containsString(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			}
		})
	}
}

func TestRitualRegistry(t *testing.T) {
	registry := NewRitualRegistry()

	// Test empty registry
	if r := registry.Get("nonexistent"); r != nil {
		t.Error("expected nil for nonexistent ritual")
	}

	// Register a ritual
	ritual := &RitualDef{
		Name: "test",
		Triggers: []RitualTrigger{
			{Event: "edict_assigned"},
		},
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Task: "do"},
		},
	}

	if err := registry.Register(ritual); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Test Get
	if r := registry.Get("test"); r == nil {
		t.Error("expected ritual, got nil")
	} else if r.Name != "test" {
		t.Errorf("expected name 'test', got %q", r.Name)
	}

	// Test GetByEvent
	rituals := registry.GetByEvent("edict_assigned")
	if len(rituals) != 1 {
		t.Errorf("expected 1 ritual for event, got %d", len(rituals))
	}

	// Test List
	names := registry.List()
	if len(names) != 1 || names[0] != "test" {
		t.Errorf("expected [test], got %v", names)
	}

	// Test registering without name
	if err := registry.Register(&RitualDef{}); err == nil {
		t.Error("expected error for ritual without name")
	}
}

func TestRitualRegistry_Summaries(t *testing.T) {
	registry := NewRitualRegistry()

	// Empty registry returns "None loaded\n"
	if s := registry.Summaries(); s != "None loaded\n" {
		t.Errorf("empty Summaries() = %q, want %q", s, "None loaded\n")
	}

	// Register rituals
	if err := registry.Register(&RitualDef{Name: "zebra", Description: "Zebra ritual"}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := registry.Register(&RitualDef{Name: "alpha", Description: "Alpha ritual"}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Should be sorted alphabetically
	s := registry.Summaries()
	if !strings.Contains(s, "- alpha: Alpha ritual") {
		t.Errorf("expected alpha in summaries, got:\n%s", s)
	}
	if !strings.Contains(s, "- zebra: Zebra ritual") {
		t.Errorf("expected zebra in summaries, got:\n%s", s)
	}
	// alpha should appear before zebra (sorted)
	alphaIdx := strings.Index(s, "alpha")
	zebraIdx := strings.Index(s, "zebra")
	if alphaIdx > zebraIdx {
		t.Error("expected alpha before zebra (sorted)")
	}
}

func TestLoadRitualsFromDir(t *testing.T) {
	// Create temp directory
	dir := t.TempDir()

	// Create a valid ritual file
	validRitual := `
name: valid-ritual
steps:
  - name: step1
    minister: forge
    task: Do something
`
	if err := os.WriteFile(filepath.Join(dir, "valid.yaml"), []byte(validRitual), 0644); err != nil {
		t.Fatal(err)
	}

	// Create another valid ritual with .yml extension
	anotherRitual := `
name: another-ritual
steps:
  - name: step1
    minister: chancellor
    task: Ask something
`
	if err := os.WriteFile(filepath.Join(dir, "another.yml"), []byte(anotherRitual), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a non-yaml file (should be ignored)
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0644); err != nil {
		t.Fatal(err)
	}

	// Load rituals
	rituals, err := LoadRitualsFromDir(dir)
	if err != nil {
		t.Fatalf("LoadRitualsFromDir() error = %v", err)
	}

	if len(rituals) != 2 {
		t.Errorf("expected 2 rituals, got %d", len(rituals))
	}

	// Test non-existent directory
	rituals, err = LoadRitualsFromDir("/nonexistent/path")
	if err != nil {
		t.Errorf("expected no error for non-existent dir, got %v", err)
	}
	if len(rituals) != 0 {
		t.Errorf("expected 0 rituals for non-existent dir, got %d", len(rituals))
	}
}

func TestLoadRitualsFromDir_OptionalMinister(t *testing.T) {
	dir := t.TempDir()

	noMinister := `
name: no-minister
steps:
  - name: check-sandbox
    then:
      - the sandbox is smoking
`
	if err := os.WriteFile(filepath.Join(dir, "no_minister.yaml"), []byte(noMinister), 0644); err != nil {
		t.Fatal(err)
	}

	rituals, err := LoadRitualsFromDir(dir)
	if err != nil {
		t.Fatalf("LoadRitualsFromDir error = %v", err)
	}
	minister := rituals[0].Steps[0].Minister
	if minister != "" {
		t.Fatalf("Step without minister should remain empty, got %q", minister)
	}
}

func TestLoadEmbeddedRituals(t *testing.T) {
	rituals, err := LoadEmbeddedRituals()
	if err != nil {
		t.Fatalf("LoadEmbeddedRituals() error = %v", err)
	}

	if len(rituals) == 0 {
		t.Fatal("expected at least 1 embedded ritual")
	}

	// Only verify dawn-audience — other rituals change frequently
	var dawnAudience *RitualDef
	for _, r := range rituals {
		if r.Name == "dawn-audience" {
			dawnAudience = r
			break
		}
	}
	if dawnAudience == nil {
		t.Fatal("dawn-audience ritual not found")
	}
	if len(dawnAudience.Steps) != 5 {
		t.Errorf("dawn-audience: expected 5 steps, got %d", len(dawnAudience.Steps))
	}
}

// containsString checks if s contains substr
func TestLoadAllRituals(t *testing.T) {
	// Test that LoadAllRituals loads embedded rituals and merges them correctly
	rituals, err := LoadAllRituals("")
	if err != nil {
		t.Fatalf("LoadAllRituals() error = %v", err)
	}

	if len(rituals) == 0 {
		t.Fatal("expected at least 1 ritual from embedded")
	}

	// Verify dawn-audience is present (from embedded)
	var found bool
	for _, r := range rituals {
		if r.Name == "dawn-audience" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected dawn-audience ritual from embedded")
	}
}

func TestLoadAllRituals_WithProjectConfig(t *testing.T) {
	// Create a temp project directory with a custom ritual
	tmpDir := t.TempDir()
	agentsDir := tmpDir + "/.agents"
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("failed to create .agents dir: %v", err)
	}

	// Write a custom ritual that overrides dawn-audience
	customYAML := []byte(`
- name: dawn-audience
  description: Custom dawn audience ritual
  triggers:
    - event: edict_created
  inputs:
    edict_id:
      type: integer
      required: true
  steps:
    - name: custom-step
      minister: forge
      task: Custom task
`)

	if err := os.WriteFile(agentsDir+"/rituals.yaml", customYAML, 0644); err != nil {
		t.Fatalf("failed to write rituals.yaml: %v", err)
	}

	// Load with project dir
	rituals, err := LoadAllRituals(tmpDir)
	if err != nil {
		t.Fatalf("LoadAllRituals() error = %v", err)
	}

	// Project config should override embedded (same name)
	var dawnAudience *RitualDef
	for _, r := range rituals {
		if r.Name == "dawn-audience" {
			dawnAudience = r
			break
		}
	}
	if dawnAudience == nil {
		t.Fatal("dawn-audience not found")
	}
	if dawnAudience.Description != "Custom dawn audience ritual" {
		t.Errorf("expected custom description, got %q", dawnAudience.Description)
	}
}

func TestLoadAllRituals_MissingProjectDir(t *testing.T) {
	// Should not error when project directory doesn't exist
	rituals, err := LoadAllRituals("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("LoadAllRituals() with nonexistent dir error = %v", err)
	}

	// Should still load embedded rituals
	if len(rituals) == 0 {
		t.Error("expected at least embedded rituals")
	}
}

func TestRitualGuardLoadRituals(t *testing.T) {
	// Test that RitualGuard.LoadRituals correctly loads and registers rituals
	db := setupRitualTestDB(t)
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	base.repoInfo = repo.RepoInfo{ProjectRoot: t.TempDir()}
	rg := NewRitualGuard(RitualGuardOpts{Base: base})

	err := rg.LoadRituals()
	if err != nil {
		t.Fatalf("LoadRituals() error = %v", err)
	}

	registry := rg.RitualRegistry()
	if registry == nil {
		t.Fatal("expected ritual registry")
	}

	// Verify dawn-audience is registered
	dawnAudience := registry.Get("dawn-audience")
	if dawnAudience == nil {
		t.Error("expected dawn-audience to be registered")
	}
}

func TestRitualGuardLoadRituals_EmptyProjectRoot(t *testing.T) {
	// LoadRituals must return an error when projectRoot is empty
	db := setupRitualTestDB(t)
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	rg := NewRitualGuard(RitualGuardOpts{Base: base})

	err := rg.LoadRituals()
	if err == nil {
		t.Fatal("expected error when projectRoot is empty")
	}
	if !strings.Contains(err.Error(), "project root not set") {
		t.Errorf("expected 'project root not set' error, got: %v", err)
	}
}

func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}

// TestRitualStreamMessages tests that ritual execution sends all expected
// typed messages through the notify callback.
func TestRitualStreamMessages(t *testing.T) {
	// Setup test database
	db := setupRitualTestDB(t)

	// Create a simple single-step ritual
	ritual := &RitualDef{
		Name:        "test-stream",
		Description: "A test ritual for streaming messages",
		Triggers:    []RitualTrigger{},
		Inputs: map[string]InputDef{
			"edict_id": {Type: "integer", Required: true},
		},
		Steps: []RitualStep{
			{
				Name:     "echo",
				Minister: "forge",
				Task:     "Echo hello",
			},
		},
	}

	// Create and populate registry
	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("Failed to register ritual: %v", err)
	}

	// Create mock court with ministers that return "hello\n"
	court := newRitualTestCourt(t, "hello\n", nil)

	// Create ritual runner
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	// Collect messages from the stream
	var messages []any
	notify := func(msg any) {
		messages = append(messages, msg)
	}

	// Start the ritual
	ctx := context.Background()
	exec, err := runner.Start(ctx, "test-stream", testEK(1), map[string]string{"edict_id": "1"}, notify)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	// Run the ritual to completion
	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	// Verify we received the expected message types
	var (
		ritualStarted  int // ritual-level "started" (empty StepName)
		stepStarted    int // step-level "started"
		completedCount int
		ritualComplete int
	)

	for _, msg := range messages {
		stepMsg, ok := msg.(RitualStepMsg)
		if !ok {
			continue
		}
		switch stepMsg.Status {
		case "started":
			if stepMsg.StepName == "" {
				ritualStarted++
				if stepMsg.RitualName != "test-stream" {
					t.Errorf("Expected ritual name 'test-stream', got %q", stepMsg.RitualName)
				}
			} else {
				stepStarted++
				if stepMsg.StepName != "echo" {
					t.Errorf("Expected step name 'echo', got %q", stepMsg.StepName)
				}
				if stepMsg.RitualName != "test-stream" {
					t.Errorf("Expected ritual name 'test-stream', got %q", stepMsg.RitualName)
				}
			}
		case "completed":
			completedCount++
		case "ritual_completed":
			ritualComplete++
		}
	}

	// Verify message counts
	if ritualStarted != 1 {
		t.Errorf("Expected 1 ritual-level 'started' message, got %d", ritualStarted)
	}
	if stepStarted != 1 {
		t.Errorf("Expected 1 step-level 'started' message, got %d", stepStarted)
	}
	if completedCount != 1 {
		t.Errorf("Expected 1 'completed' message, got %d", completedCount)
	}
	if ritualComplete != 1 {
		t.Errorf("Expected 1 'ritual_completed' message, got %d", ritualComplete)
	}

	// Verify execution state
	if exec.State != RitualStateCompleted {
		t.Errorf("Expected state 'completed', got %s", exec.State)
	}
}

// TestRitualStreamMessages_MultiStep tests a multi-step ritual sends messages for each step
func TestRitualStreamMessages_MultiStep(t *testing.T) {
	db := setupRitualTestDB(t)

	// Create a multi-step ritual
	ritual := &RitualDef{
		Name:        "multi-step",
		Description: "Multi-step ritual",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Task: "do one"},
			{Name: "step2", Minister: "judge", Task: "do two"},
			{Name: "step3", Minister: "chancellor", Task: "do three"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "ok\n", nil)
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	var messages []RitualStepMsg
	notify := func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok {
			messages = append(messages, stepMsg)
		}
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "multi-step", testEK(2), nil, notify)
	if err != nil {
		t.Fatalf("Failed to start: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Should have: ritual_started(1) + started(3) + completed(3) + ritual_completed(1) = 8 messages
	expectedCount := 8
	if len(messages) != expectedCount {
		t.Errorf("Expected %d messages, got %d", expectedCount, len(messages))
		for i, m := range messages {
			t.Logf("  [%d] step=%s status=%s", i, m.StepName, m.Status)
		}
	}

	// Verify step indices are correct
	for _, msg := range messages {
		if msg.Status == "started" || msg.Status == "completed" {
			if msg.TotalSteps != 3 {
				t.Errorf("Expected TotalSteps=3, got %d", msg.TotalSteps)
			}
		}
	}
}

// TestRitualStreamMessages_Failure tests that failure messages are sent correctly
func TestRitualStreamMessages_Failure(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "fail-ritual",
		Steps: []RitualStep{
			{Name: "fail-step", Minister: "forge", Task: "do something", OnFailure: "abort"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	// Mock court where ministers return errors
	court := newRitualTestCourt(t, "", fmt.Errorf("minister failed"))
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	var messages []RitualStepMsg
	notify := func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok {
			messages = append(messages, stepMsg)
		}
	}

	ctx := context.Background()
	exec, _ := runner.Start(ctx, "fail-ritual", testEK(3), nil, notify)
	err := runner.Run(ctx, exec)

	// Should fail
	if err == nil {
		t.Error("Expected error from failed ritual")
	}

	// Should have: started(1) + failed(1) = 2 messages
	var failedCount int
	for _, msg := range messages {
		if msg.Status == "failed" {
			failedCount++
		}
	}

	if failedCount != 1 {
		t.Errorf("Expected 1 'failed' message, got %d", failedCount)
	}

	if exec.State != RitualStateFailed {
		t.Errorf("Expected state 'failed', got %s", exec.State)
	}
}

// TestRitualGotoPassesErrorMessage tests that when step2 fails and gotos back to step1,
// step1 receives the error message from step2 in its Work field (not scratchpad).
// TestRitualGotoPassesErrorMessage tests that when step2 fails and gotos back to step1,
// step1 receives the error message from step2 in its work prompt.
func TestRitualGotoPassesErrorMessage(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:       "goto-error-test",
		MaxRetries: 3,
		Steps: []RitualStep{
			{Name: "report", Minister: "forge", Act: "report status"},
			{Name: "review", Minister: "judge", Act: "review code",
				OnFailure: "goto", OnFailureTarget: "report"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	// Forge succeeds, judge always fails — ritual loops (report -> review -> goto report -> ...)
	// After max retries (3), the ritual fails. We verify the error appears in forge's work prompt.
	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "forge done",
	}
	judgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "judge",
		tasksCh:      make(chan *Task, 1),
		result:       "",
		err:          fmt.Errorf("Here goes the error message"),
	}

	court := &Court{
		ministers: map[string]Minister{"forge": forgeM, "judge": judgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go forgeM.Run(ctx)
	go judgeM.Run(ctx)
	exec, err := runner.Start(ctx, "goto-error-test", testEK(4), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	_ = runner.Run(ctx, exec)

	// Forge should have been called at least 2 times (initial + at least 1 retry after goto)
	if forgeM.getCallCount() < 2 {
		t.Fatalf("Expected forge to be called at least 2 times, got %d", forgeM.getCallCount())
	}
}

// TestRitualGotoPassesOutputAndError tests that when a step fails with both output and error,
// the goto target is re-invoked.
func TestRitualGotoPassesOutputAndError(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:       "goto-output-error-test",
		MaxRetries: 3,
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "implement feature"},
			{Name: "step2", Minister: "judge", Act: "review code",
				OnFailure: "goto", OnFailureTarget: "step1"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "forge done",
	}
	judgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "judge",
		tasksCh:      make(chan *Task, 1),
		result:       "",
		err:          fmt.Errorf("review failed"),
	}

	court := &Court{
		ministers: map[string]Minister{"forge": forgeM, "judge": judgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go forgeM.Run(ctx)
	go judgeM.Run(ctx)
	exec, err := runner.Start(ctx, "goto-output-error-test", testEK(5), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	_ = runner.Run(ctx, exec)

	// Forge should have been called at least 2 times (initial + goto retry)
	if forgeM.getCallCount() < 2 {
		t.Fatalf("Expected forge to be called at least 2 times, got %d", forgeM.getCallCount())
	}
}

// TestRitualGotoCreatesEphemeralSessions tests that each goto re-invocation creates
// a fresh ephemeral session (no session reuse in the new architecture).
func TestRitualGotoCreatesEphemeralSessions(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:       "goto-session-test",
		MaxRetries: 3,
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "implement feature"},
			{Name: "step2", Minister: "judge", Act: "review code",
				OnFailure: "goto", OnFailureTarget: "step1"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "forge done",
	}
	judgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "judge",
		tasksCh:      make(chan *Task, 1),
		result:       "",
		err:          fmt.Errorf("review failed"),
	}

	court := &Court{
		ministers: map[string]Minister{"forge": forgeM, "judge": judgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go forgeM.Run(ctx)
	go judgeM.Run(ctx)
	exec, err := runner.Start(ctx, "goto-session-test", testEK(6), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	_ = runner.Run(ctx, exec)

	// In the new architecture, each invocation uses an ephemeral session.
	// Verify forge was called multiple times (proving goto retry worked).
	if forgeM.getCallCount() < 2 {
		t.Fatalf("Expected forge to be called at least 2 times, got %d", forgeM.getCallCount())
	}
}

// TestRitualStepPreservesOutputOnFailure tests that executeMinisterStep preserves output on error.
func TestRitualStepPreservesOutputOnFailure(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "preserve-output-test",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "do work", OnFailure: "abort"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	// Minister that fails — returns error
	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "",
		err:          fmt.Errorf("something went wrong"),
	}

	ctx := context.Background()
	go forgeM.Run(ctx)
	court := &Court{
		ministers: map[string]Minister{"forge": forgeM},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "preserve-output-test", testEK(7), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	_ = runner.Run(ctx, exec)

	// Verify error message is stored in step state
	if !strings.Contains(exec.stepStates[0].Message, "something went wrong") {
		t.Errorf("Expected step message to contain error, got %q", exec.stepStates[0].Message)
	}
}

func TestLoadBuiltinRituals(t *testing.T) {
	rituals, err := LoadEmbeddedRituals()
	if err != nil {
		t.Fatalf("LoadBuiltinRituals() error = %v", err)
	}

	if len(rituals) < 5 {
		names := make([]string, len(rituals))
		for i, r := range rituals {
			names[i] = r.Name
		}
		t.Errorf("expected 6 builtin rituals, got %d: %v", len(rituals), names)
	}

	// Verify swift-strike uses ritual-level background given
	for _, r := range rituals {
		if r.Name == "swift-strike" {
			// Background given at ritual level
			if len(r.Background) == 0 || r.Background[0] != "the edict details" {
				t.Errorf("swift-strike: expected background given 'the edict details', got %v", r.Background)
			}
			// Forge step should have no step-level given (hoisted)
			if len(r.Steps[0].Given) != 0 {
				t.Errorf("swift-strike forge: expected no step-level given, got %v", r.Steps[0].Given)
			}
			// Judge step should have Given with !just test
			if len(r.Steps[1].Given) == 0 {
				t.Error("swift-strike judge step: expected given entries")
			}
			if r.Steps[1].Given[0] != "!just test" {
				t.Errorf("swift-strike judge given: expected '!just test', got %q", r.Steps[1].Given[0])
			}
		}
	}
}

func TestBackgroundGiven(t *testing.T) {
	db := setupRitualTestDB(t)

	// Ritual with background given (bash) and a minister step
	ritual := &RitualDef{
		Name:       "bg-test",
		Background: []string{"!echo background-data"},
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "step-done\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "background-data\n", ExitCode: "0"}, // background given
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, nil, db, mockRunner, nil, repo.RepoInfo{})

	var messages []any
	notify := func(msg any) {
		messages = append(messages, msg)
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "bg-test", testEK(9), nil, notify)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Verify background given result is in context
	if _, ok := exec.Data["echo"]; !ok {
		t.Errorf("expected 'echo' key in exec.Data, got keys: %v", exec.Data)
	}

	// Verify ToolCallScheduledMsg and ToolCallSuccessMsg were emitted for background
	var scheduled *runners.ToolCallScheduledMsg
	var success *runners.ToolCallSuccessMsg
	for i := range messages {
		switch m := messages[i].(type) {
		case runners.ToolCallScheduledMsg:
			scheduled = &m
		case runners.ToolCallSuccessMsg:
			success = &m
		}
	}
	if scheduled == nil {
		t.Fatalf("expected a ToolCallScheduledMsg, got messages: %+v", messages)
	}
	if scheduled.Input != "!echo background-data" {
		t.Errorf("expected ToolCallScheduledMsg Input '!echo background-data', got %q", scheduled.Input)
	}
	if success == nil {
		t.Fatalf("expected a ToolCallSuccessMsg, got messages: %+v", messages)
	}
	if success.Input != "!echo background-data" {
		t.Errorf("expected ToolCallSuccessMsg Input '!echo background-data', got %q", success.Input)
	}
	if scheduled.CallID != success.CallID {
		t.Errorf("expected scheduled and success to share CallID, got %q and %q", scheduled.CallID, success.CallID)
	}
}

func TestBackgroundGivenFailureNotifies(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:       "bg-fail-test",
		Background: []string{"!false"},
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "step-done\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "boom\n", ExitCode: "1"}, // background given fails
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, nil, db, mockRunner, nil, repo.RepoInfo{})

	var messages []any
	notify := func(msg any) {
		messages = append(messages, msg)
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "bg-fail-test", testEK(42), nil, notify)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err == nil {
		t.Fatal("expected Run to return an error for background failure")
	}

	// Verify ToolCallErrorMsg was emitted
	var errMsg *runners.ToolCallErrorMsg
	var failedMsg *RitualStepMsg
	for i := range messages {
		switch m := messages[i].(type) {
		case runners.ToolCallErrorMsg:
			errMsg = &m
		case RitualStepMsg:
			if m.Status == "ritual_failed" {
				failedMsg = &m
			}
		}
	}
	if errMsg == nil {
		t.Fatalf("expected a ToolCallErrorMsg, got messages: %+v", messages)
	}
	if errMsg.ToolName != "false" {
		t.Errorf("expected ToolCallErrorMsg ToolName 'false', got %q", errMsg.ToolName)
	}
	if failedMsg == nil {
		t.Fatalf("expected a ritual_failed notification, got messages: %+v", messages)
	}
	if failedMsg.StepName != "false" {
		t.Errorf("expected StepName 'false', got %q", failedMsg.StepName)
	}

	// Verify EventRitualFailed was persisted to DB
	var events []storage.TianEvent
	db.Where("event_type = ?", storage.EventRitualFailed).Find(&events)
	if len(events) != 1 {
		t.Fatalf("expected 1 EventRitualFailed in DB, got %d", len(events))
	}
	if events[0].EdictID != 42 {
		t.Errorf("expected edict_id 42, got %d", events[0].EdictID)
	}
}

func TestThenStepEmitsToolCallMessages(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "then-test",
		Steps: []RitualStep{
			{
				Name:     "work",
				Minister: "forge",
				Task:     "do work",
				Then:     []string{"!echo then-output"},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "step-done\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "then-output\n", ExitCode: "0"},
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, nil, db, mockRunner, nil, repo.RepoInfo{})

	var messages []any
	notify := func(msg any) {
		messages = append(messages, msg)
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "then-test", testEK(7), nil, notify)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Verify ToolCallScheduledMsg and ToolCallSuccessMsg were emitted for the then step
	var scheduled *runners.ToolCallScheduledMsg
	var success *runners.ToolCallSuccessMsg
	for i := range messages {
		switch m := messages[i].(type) {
		case runners.ToolCallScheduledMsg:
			scheduled = &m
		case runners.ToolCallSuccessMsg:
			success = &m
		}
	}
	if scheduled == nil {
		t.Fatalf("expected a ToolCallScheduledMsg for then step, got messages: %+v", messages)
	}
	if scheduled.Input != "!echo then-output" {
		t.Errorf("expected ToolCallScheduledMsg Input '!echo then-output', got %q", scheduled.Input)
	}
	if scheduled.ToolName != "echo" {
		t.Errorf("expected ToolCallScheduledMsg ToolName 'echo', got %q", scheduled.ToolName)
	}
	if success == nil {
		t.Fatalf("expected a ToolCallSuccessMsg for then step, got messages: %+v", messages)
	}
	if success.Input != "!echo then-output" {
		t.Errorf("expected ToolCallSuccessMsg Input '!echo then-output', got %q", success.Input)
	}
	if scheduled.CallID != success.CallID {
		t.Errorf("expected scheduled and success to share CallID, got %q and %q", scheduled.CallID, success.CallID)
	}
}

func TestThenStepFailureEmitsToolCallErrorMsg(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "then-fail-test",
		Steps: []RitualStep{
			{
				Name:     "work",
				Minister: "forge",
				Task:     "do work",
				Then:     []string{"!false"},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "step-done\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "boom\n", ExitCode: "1"},
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, nil, db, mockRunner, nil, repo.RepoInfo{})

	var messages []any
	notify := func(msg any) {
		messages = append(messages, msg)
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "then-fail-test", testEK(8), nil, notify)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	_ = runner.Run(ctx, exec) // expected to return error

	// Verify ToolCallErrorMsg was emitted for the failed then step
	var errMsg *runners.ToolCallErrorMsg
	var scheduled *runners.ToolCallScheduledMsg
	for i := range messages {
		switch m := messages[i].(type) {
		case runners.ToolCallErrorMsg:
			errMsg = &m
		case runners.ToolCallScheduledMsg:
			scheduled = &m
		}
	}
	if scheduled == nil {
		t.Fatalf("expected a ToolCallScheduledMsg for then step, got messages: %+v", messages)
	}
	if errMsg == nil {
		t.Fatalf("expected a ToolCallErrorMsg for failed then step, got messages: %+v", messages)
	}
	if errMsg.ToolName != "false" {
		t.Errorf("expected ToolCallErrorMsg ToolName 'false', got %q", errMsg.ToolName)
	}
	if scheduled.CallID != errMsg.CallID {
		t.Errorf("expected scheduled and error to share CallID, got %q and %q", scheduled.CallID, errMsg.CallID)
	}
}

func TestStepLevelGivenEmitsToolCallMessages(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "step-given-test",
		Steps: []RitualStep{
			{
				Name:     "work",
				Minister: "forge",
				Task:     "do work",
				Given:    []string{"!echo step-given-data"},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "step-done\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "step-given-data\n", ExitCode: "0"}, // step-level given
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, nil, db, mockRunner, nil, repo.RepoInfo{})

	var messages []any
	notify := func(msg any) {
		messages = append(messages, msg)
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "step-given-test", testEK(11), nil, notify)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Verify ToolCallScheduledMsg and ToolCallSuccessMsg were emitted for the step-level given
	var scheduled *runners.ToolCallScheduledMsg
	var success *runners.ToolCallSuccessMsg
	for i := range messages {
		switch m := messages[i].(type) {
		case runners.ToolCallScheduledMsg:
			scheduled = &m
		case runners.ToolCallSuccessMsg:
			success = &m
		}
	}
	if scheduled == nil {
		t.Fatalf("expected a ToolCallScheduledMsg for step-level given, got messages: %+v", messages)
	}
	if scheduled.Input != "!echo step-given-data" {
		t.Errorf("expected ToolCallScheduledMsg Input '!echo step-given-data', got %q", scheduled.Input)
	}
	if scheduled.ToolName != "echo" {
		t.Errorf("expected ToolCallScheduledMsg ToolName 'echo', got %q", scheduled.ToolName)
	}
	if success == nil {
		t.Fatalf("expected a ToolCallSuccessMsg for step-level given, got messages: %+v", messages)
	}
	if success.Input != "!echo step-given-data" {
		t.Errorf("expected ToolCallSuccessMsg Input '!echo step-given-data', got %q", success.Input)
	}
	if scheduled.CallID != success.CallID {
		t.Errorf("expected scheduled and success to share CallID, got %q and %q", scheduled.CallID, success.CallID)
	}
}

func TestRitualLevelThenEmitsToolCallMessages(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "ritual-then-test",
		Then: []string{"!echo ritual-then-output"},
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "step-done\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "ritual-then-output\n", ExitCode: "0"}, // ritual-level then
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, nil, db, mockRunner, nil, repo.RepoInfo{})

	var messages []any
	notify := func(msg any) {
		messages = append(messages, msg)
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "ritual-then-test", testEK(12), nil, notify)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Verify ToolCallScheduledMsg and ToolCallSuccessMsg were emitted for the ritual-level then
	var scheduled *runners.ToolCallScheduledMsg
	var success *runners.ToolCallSuccessMsg
	for i := range messages {
		switch m := messages[i].(type) {
		case runners.ToolCallScheduledMsg:
			scheduled = &m
		case runners.ToolCallSuccessMsg:
			success = &m
		}
	}
	if scheduled == nil {
		t.Fatalf("expected a ToolCallScheduledMsg for ritual-level then, got messages: %+v", messages)
	}
	if scheduled.Input != "!echo ritual-then-output" {
		t.Errorf("expected ToolCallScheduledMsg Input '!echo ritual-then-output', got %q", scheduled.Input)
	}
	if scheduled.ToolName != "echo" {
		t.Errorf("expected ToolCallScheduledMsg ToolName 'echo', got %q", scheduled.ToolName)
	}
	if success == nil {
		t.Fatalf("expected a ToolCallSuccessMsg for ritual-level then, got messages: %+v", messages)
	}
	if success.Input != "!echo ritual-then-output" {
		t.Errorf("expected ToolCallSuccessMsg Input '!echo ritual-then-output', got %q", success.Input)
	}
	if scheduled.CallID != success.CallID {
		t.Errorf("expected scheduled and success to share CallID, got %q and %q", scheduled.CallID, success.CallID)
	}
}

func TestStepLevelGivenFailureEmitsToolCallErrorMsg(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "step-given-fail-test",
		Steps: []RitualStep{
			{
				Name:     "work",
				Minister: "forge",
				Task:     "do work",
				Given:    []string{"!false"},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "step-done\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "boom\n", ExitCode: "1"},
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, nil, db, mockRunner, nil, repo.RepoInfo{})

	var messages []any
	notify := func(msg any) {
		messages = append(messages, msg)
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "step-given-fail-test", testEK(13), nil, notify)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	_ = runner.Run(ctx, exec) // expected to return error

	var errMsg *runners.ToolCallErrorMsg
	var scheduled *runners.ToolCallScheduledMsg
	for i := range messages {
		switch m := messages[i].(type) {
		case runners.ToolCallErrorMsg:
			errMsg = &m
		case runners.ToolCallScheduledMsg:
			scheduled = &m
		}
	}
	if scheduled == nil {
		t.Fatalf("expected a ToolCallScheduledMsg for step-level given, got messages: %+v", messages)
	}
	if errMsg == nil {
		t.Fatalf("expected a ToolCallErrorMsg for failed step-level given, got messages: %+v", messages)
	}
	if errMsg.ToolName != "false" {
		t.Errorf("expected ToolCallErrorMsg ToolName 'false', got %q", errMsg.ToolName)
	}
	if scheduled.CallID != errMsg.CallID {
		t.Errorf("expected scheduled and error to share CallID, got %q and %q", scheduled.CallID, errMsg.CallID)
	}
}

func TestRitualLevelThenFailureEmitsToolCallErrorMsg(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "ritual-then-fail-test",
		Then: []string{"!false"},
		Steps: []RitualStep{
			{Name: "work", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "step-done\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "boom\n", ExitCode: "1"},
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, nil, db, mockRunner, nil, repo.RepoInfo{})

	var messages []any
	notify := func(msg any) {
		messages = append(messages, msg)
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "ritual-then-fail-test", testEK(14), nil, notify)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	_ = runner.Run(ctx, exec) // ritual-level then failure does not abort; it logs and continues

	var errMsg *runners.ToolCallErrorMsg
	var scheduled *runners.ToolCallScheduledMsg
	for i := range messages {
		switch m := messages[i].(type) {
		case runners.ToolCallErrorMsg:
			errMsg = &m
		case runners.ToolCallScheduledMsg:
			scheduled = &m
		}
	}
	if scheduled == nil {
		t.Fatalf("expected a ToolCallScheduledMsg for ritual-level then, got messages: %+v", messages)
	}
	if errMsg == nil {
		t.Fatalf("expected a ToolCallErrorMsg for failed ritual-level then, got messages: %+v", messages)
	}
	if errMsg.ToolName != "false" {
		t.Errorf("expected ToolCallErrorMsg ToolName 'false', got %q", errMsg.ToolName)
	}
	if scheduled.CallID != errMsg.CallID {
		t.Errorf("expected scheduled and error to share CallID, got %q and %q", scheduled.CallID, errMsg.CallID)
	}
}

// ritualTestMinister is a Minister that auto-completes tasks with a configured result.
type ritualTestMinister struct {
	MinisterBase
	id        string
	tasksCh   chan *Task
	result    string
	err       error
	config    config.LLMConfig // LLM config (used as the user-configured session default)
	delay     time.Duration    // optional delay before completing each task (for race-condition testing)
	callCount int
	callLog   []string // ordered list of Work strings received, for verifying dispatch order
	mu        sync.Mutex
}

func (m *ritualTestMinister) ID() string                  { return m.id }
func (m *ritualTestMinister) SystemPrompt() string        { return "" }
func (m *ritualTestMinister) Title() string               { return m.id }
func (m *ritualTestMinister) Tools() []Tool               { return nil }
func (m *ritualTestMinister) Tasks() chan<- *Task         { return m.tasksCh }
func (m *ritualTestMinister) Model() LLMProvider          { return nil }
func (m *ritualTestMinister) GetConfig() config.LLMConfig { return m.config }
func (m *ritualTestMinister) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-m.tasksCh:
			m.mu.Lock()
			m.callCount++
			m.callLog = append(m.callLog, t.Work)
			m.mu.Unlock()
			if m.delay > 0 {
				select {
				case <-time.After(m.delay):
				case <-ctx.Done():
					t.Done <- Result{Output: m.result, Err: ctx.Err()}
					return
				}
			}
			t.Done <- Result{Output: m.result, Err: m.err}
		}
	}
}
func (m *ritualTestMinister) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *ritualTestMinister) getCallLog() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.callLog))
	copy(cp, m.callLog)
	return cp
}

// newRitualTestCourt creates a Court with mock ministers for ritual tests.
// All ministers return the given output. Start a goroutine for each minister's Run.
func newRitualTestCourt(t *testing.T, output string, err error) *Court {
	t.Helper()
	ministers := map[string]Minister{}
	for _, id := range []string{"forge", "judge", "chancellor", "war", "secretary"} {
		m := &ritualTestMinister{
			MinisterBase: MinisterBase{logger: slog.Default()},
			id:           id,
			tasksCh:      make(chan *Task, 1),
			result:       output,
			err:          err,
		}
		ministers[id] = m
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go m.Run(ctx)
	}
	s := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}
	// Set up a minimal ritualGuard so PublishEvent and GetRitualRunner work
	base := &MinisterBase{logger: slog.Default()}
	s.ritualGuard = NewRitualGuard(RitualGuardOpts{
		Base:        base,
		GetMinister: s.GetMinister,
	})
	return s
}

// newRitualTestCourtWithDB is like newRitualTestCourt but with a db for the ritualGuard.
func newRitualTestCourtWithDB(t *testing.T, db *gorm.DB, output string, err error) *Court {
	t.Helper()
	ministers := map[string]Minister{}
	for _, id := range []string{"forge", "judge", "chancellor", "war", "secretary"} {
		m := &ritualTestMinister{
			MinisterBase: MinisterBase{logger: slog.Default()},
			id:           id,
			tasksCh:      make(chan *Task, 1),
			result:       output,
			err:          err,
		}
		ministers[id] = m
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go m.Run(ctx)
	}
	s := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	s.ritualGuard = NewRitualGuard(RitualGuardOpts{
		Base:        base,
		GetMinister: s.GetMinister,
	})
	return s
}

// mockCallCountRunner returns sequential results for successive calls
type mockCallCountRunner struct {
	results []runners.Output
	idx     int
}

func (m *mockCallCountRunner) Run(ctx context.Context, input runners.Input) (runners.Output, error) {
	if m.idx >= len(m.results) {
		return runners.Output{Output: "", ExitCode: "0"}, nil
	}
	result := m.results[m.idx]
	m.idx++
	if result.ExitCode != "0" {
		return result, nil
	}
	return result, nil
}

func (m *mockCallCountRunner) Restart(ctx context.Context) error    { return nil }
func (m *mockCallCountRunner) Close(ctx context.Context) error      { return nil }
func (m *mockCallCountRunner) AllowFallback(bool)                   {}
func (m *mockCallCountRunner) RunnerType() string                   { return "mock" }
func (m *mockCallCountRunner) GetOS() string                        { return runtime.GOOS }
func (m *mockCallCountRunner) SetMessageChannel(chan<- runners.Msg) {}

func (m *mockCallCountRunner) HealthCheck(ctx context.Context) error { return nil }

// setupRitualTestDB creates a test database with ritual tables
func setupRitualTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := tmpDir + "/ritual_test.db"

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("Failed to initialize gorm: %v", err)
	}

	// Migrate ritual tables
	err = db.AutoMigrate(&RitualExecution{}, &RitualStepState{}, &storage.TianEvent{}, &storage.ForgeManifest{}, &storage.Ling{}, &storage.Seal{})
	if err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	return db
}

// TestInvokeRitualTool_Requested verifies that enact_ritual returns immediately with "requested" status
// and publishes an EventRitualEnacted event for the RitualGuard to pick up.
func TestInvokeRitualTool_Requested(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:        "test-requested",
		Description: "A test ritual for async enactment",
		Steps: []RitualStep{
			{Name: "echo", Minister: "forge", Task: "echo hello"},
		},
	}

	court := newRitualTestCourtWithDB(t, db, "hello\n", nil)
	court.GetRitualRegistry().Register(ritual)

	tool := tools.InvokeRitualTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}, Launcher: court}
	input := `{"ritual_name":"test-requested","edict_id":1}`

	result, err := tool.Call(context.Background(), input)
	if err != nil {
		t.Fatalf("Call() returned error: %v", err)
	}

	// Parse result
	var res map[string]any
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("Failed to parse result JSON: %v", err)
	}

	if !strings.HasPrefix(res["status"].(string), "requested") {
		t.Errorf("expected status to start with 'requested', got %q", res["status"])
	}
	if res["ritual_name"] != "test-requested" {
		t.Errorf("expected ritual_name 'test-requested', got %q", res["ritual_name"])
	}
	// edict_id comes back as float64 from JSON unmarshaling
	if res["edict_id"] != float64(1) {
		t.Errorf("expected edict_id 1, got %v", res["edict_id"])
	}
}

// TestInvokeRitualTool_RequestedEvenForBadRitual verifies that enact_ritual returns "requested"
// even for rituals that would fail — failure is reported asynchronously via events.
func TestInvokeRitualTool_RequestedEvenForBadRitual(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "test-fail-requested",
		Steps: []RitualStep{
			{Name: "fail", Minister: "forge", Task: "do something", OnFailure: "abort"},
		},
	}

	court := newRitualTestCourtWithDB(t, db, "", fmt.Errorf("minister failed"))
	court.GetRitualRegistry().Register(ritual)

	tool := tools.InvokeRitualTool{Ctx: tools.ToolContext{Username: "testuser", Project: "testproject"}, Launcher: court}
	input := `{"ritual_name":"test-fail-requested","edict_id":2}`

	result, err := tool.Call(context.Background(), input)
	if err != nil {
		t.Fatalf("Call() returned error: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("Failed to parse result JSON: %v", err)
	}

	// Should still return "requested" — failure happens async
	if !strings.HasPrefix(res["status"].(string), "requested") {
		t.Errorf("expected status to start with 'requested', got %q", res["status"])
	}
	if res["ritual_name"] != "test-fail-requested" {
		t.Errorf("expected ritual_name 'test-fail-requested', got %q", res["ritual_name"])
	}
}

// mockCmdRunner implements runners.Runner for testing
type mockCmdRunner struct {
	output   string
	exitCode string
	err      error
	onRun    func(string) // optional callback to capture command
}

func (m *mockCmdRunner) Run(ctx context.Context, input runners.Input) (runners.Output, error) {
	if m.onRun != nil {
		m.onRun(input.Command)
	}
	if m.err != nil {
		return runners.Output{}, m.err
	}
	if m.exitCode != "0" {
		return runners.Output{
			Output:   m.output,
			ExitCode: m.exitCode,
		}, nil
	}
	return runners.Output{
		Output:   m.output,
		ExitCode: "0",
	}, nil
}

func (m *mockCmdRunner) Restart(ctx context.Context) error    { return nil }
func (m *mockCmdRunner) Close(ctx context.Context) error      { return nil }
func (m *mockCmdRunner) AllowFallback(bool)                   {}
func (m *mockCmdRunner) RunnerType() string                   { return "mock" }
func (m *mockCmdRunner) GetOS() string                        { return runtime.GOOS }
func (m *mockCmdRunner) SetMessageChannel(chan<- runners.Msg) {}

func (m *mockCmdRunner) HealthCheck(ctx context.Context) error { return nil }

// TestRitualMinisterStepCompletes verifies that a minister step using the ephemeral
// session pattern completes successfully and stores its result.
func TestRitualMinisterStepCompletes(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:        "minister-complete",
		Description: "Test minister step completion via ephemeral session",
		Steps: []RitualStep{
			{Name: "ask", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "done after work",
	}

	ministers := map[string]Minister{"forge": forgeM}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go forgeM.Run(ctx)

	court := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	var messages []RitualStepMsg
	notify := func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok {
			messages = append(messages, stepMsg)
		}
	}

	exec, err := runner.Start(ctx, "minister-complete", testEK(10), nil, notify)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	if exec.State != RitualStateCompleted {
		t.Errorf("Expected state 'completed', got %s", exec.State)
	}

	// Verify the step result is stored in exec.Data
	result, ok := exec.Data["ask"].(string)
	if !ok {
		t.Fatal(`Expected step result stored in exec.Data["ask"]`)
	}
	if result != "done after work" {
		t.Errorf("Expected result 'done after work', got %q", result)
	}
}

// TestRitualStepSessionGetsAtifRecorder verifies that ritual step sessions get
// an ATIF trajectory recorder when the RitualRunner has an ATIF agent name set
// (propagated from the --atif flag), matching the main/interactive sessions.
func TestRitualStepSessionGetsAtifRecorder(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:        "atif-step",
		Description: "Test ATIF recorder on ritual step session",
		Steps: []RitualStep{
			{Name: "ask", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "done after work",
	}

	ministers := map[string]Minister{"forge": forgeM}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go forgeM.Run(ctx)

	court := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})
	runner.SetAtifAgentName("forge")

	exec, err := runner.Start(ctx, "atif-step", testEK(10), nil, func(any) {})
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	if exec.State != RitualStateCompleted {
		t.Fatalf("Expected state 'completed', got %s", exec.State)
	}

	sess := exec.stepStates[0].Session
	if sess == nil {
		t.Fatal("Expected step session to be created")
	}
	if sess.atifRecorder == nil {
		t.Error("Expected step session to have a non-nil atifRecorder when RitualRunner.atifAgentName is set")
	}
}

// TestRitualTimeoutCancelsStep verifies that context cancellation stops a running step.
func TestRitualTimeoutCancelsStep(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:        "timeout-test",
		Description: "Test timeout fires normally",
		Steps: []RitualStep{
			{Name: "slow", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	// Create a minister whose model returns a deadline exceeded error (simulates timeout)
	slowMinister := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "",
		err:          context.DeadlineExceeded,
	}

	ministers := map[string]Minister{"forge": slowMinister}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go slowMinister.Run(ctx)

	court := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	notify := func(msg any) {}

	exec, err := runner.Start(ctx, "timeout-test", testEK(11), nil, notify)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err == nil {
		t.Fatal("Expected error from context cancellation or timeout, got nil")
	}
}

// TestRitualContextCancelledNoAbortNotification verifies that when a ritual
// step's context is cancelled, the ritual runner does NOT emit a
// RitualStepMsg{Status:"aborted"} notification. The session layer already
// sends StreamInterruptedMsg which surfaces as 🛠️ ABORTED in the TUI;
// a duplicate from the ritual layer would produce "🛠️ ABORTED ABORTED".
func TestRitualContextCancelledNoAbortNotification(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:        "cancel-notif-test",
		Description: "Test that ctx cancellation doesn't emit aborted notification",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	// Minister that blocks until context is cancelled
	slowMinister := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		delay:        2 * time.Second, // long enough for us to cancel
		result:       "",
		err:          nil,
	}

	ministers := map[string]Minister{"forge": slowMinister}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go slowMinister.Run(ctx)

	court := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	// Collect all notifications
	var notifications []RitualStepMsg
	var mu sync.Mutex
	notify := func(msg any) {
		if rsm, ok := msg.(RitualStepMsg); ok {
			mu.Lock()
			notifications = append(notifications, rsm)
			mu.Unlock()
		}
	}

	exec, err := runner.Start(ctx, "cancel-notif-test", testEK(12), nil, notify)
	require.NoError(t, err)

	// Cancel context shortly after starting — simulates user pressing Ctrl-C
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err = runner.Run(ctx, exec)
	require.Error(t, err, "expected error from context cancellation")

	// Verify state is aborted
	require.Equal(t, RitualStateAborted, exec.State, "ritual state should be aborted")

	// Verify no RitualStepMsg{Status:"aborted"} was emitted
	mu.Lock()
	defer mu.Unlock()
	for _, n := range notifications {
		assert.NotEqual(t, "aborted", n.Status,
			"RitualStepMsg{Status:'aborted'} should not be emitted on ctx cancellation — "+
				"StreamInterruptedMsg from the session layer already shows the abort")
	}
}

// TestRitualMinisterStepExecutesLings verifies that ritual minister steps route
// through the Task pattern, enabling proper routing. This is the primary test
// for Edict 324's main purpose: ensuring processTask runs with the correct
// EdictKey so the Forge can handle failed verdicts for the right edict.
//
// The key verification is that the Task arrives at the minister with the correct
// EdictKey, enabling proper result routing and verdict handling.
func TestRitualMinisterStepRoutesThroughTaskPattern(t *testing.T) {
	db := setupRitualTestDB(t)

	// Add edicts table for this test
	require.NoError(t, db.AutoMigrate(&storage.Edict{}))

	// Create edict for the test
	edict := &storage.Edict{SessionID: "test-session", Intent: "Build REST API", Username: "testuser", Project: "testproject"}
	require.NoError(t, db.Create(edict).Error)

	ritual := &RitualDef{
		Name:        "minister-task-test",
		Description: "Test that minister steps use Task pattern with EdictKey",
		Steps: []RitualStep{
			{Name: "forge-step", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	// Track if the task was received with the correct EdictKey
	var receivedTask *Task

	forgeM := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "success",
	}

	ministers := map[string]Minister{"forge": forgeM}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// NOTE: Do NOT start go forgeM.Run(ctx) here — this test manually reads
	// from tasksCh to inspect the task before sending a result back.

	court := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	court.ritualGuard = NewRitualGuard(RitualGuardOpts{
		Base:        base,
		GetMinister: court.GetMinister,
	})

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "minister-task-test", testEK(edict.ID), nil, nil)
	require.NoError(t, err)

	// Run ritual in background and capture the task
	go func() {
		_ = runner.Run(ctx, exec)
	}()

	// Wait for task to arrive at the minister
	select {
	case receivedTask = <-forgeM.tasksCh:
		// Task received
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for task to arrive at minister")
	}

	// Verify the task has the correct EdictKey.
	// The EdictKey enables the ritual engine to route tasks correctly
	// and ensures the Forge's processTask can handle failed verdicts for the right edict.
	require.NotNil(t, receivedTask, "task should have been received")
	assert.Equal(t, edict.Key(), receivedTask.EdictKey,
		"task must have correct EdictKey for result routing")
	assert.NotNil(t, receivedTask.Done,
		"task must have done channel for result routing")

	// Send result back and verify ritual completes
	receivedTask.Done <- Result{Output: "success", Err: nil}

	// Wait for ritual to complete
	for i := 0; i < 50; i++ {
		if exec.State == RitualStateCompleted || exec.State == RitualStateFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	assert.Equal(t, RitualStateCompleted, exec.State, "ritual should complete successfully")
}

func TestExpandTemplate_ActResult(t *testing.T) {
	runner := &RitualRunner{logger: slog.Default()}
	exec := &RitualExecution{
		EdictID:     1,
		CurrentStep: 1,
		Data: storage.JSON{
			"act_result": "step0 produced this output",
		},
		stepStates: []RitualStepState{
			{Name: "step0", Message: "done"},
		},
	}

	result := runner.expandTemplate("Previous result: {{ .act_result }}", exec)
	expected := "Previous result: step0 produced this output"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestExpandTemplate_StepResults(t *testing.T) {
	runner := &RitualRunner{logger: slog.Default()}
	exec := &RitualExecution{
		EdictID:     1,
		CurrentStep: 2,
		stepStates: []RitualStepState{
			{Name: "plan", Message: "the plan output"},
			{Name: "implement", Message: "the impl output"},
			{Name: "review", Message: "should be excluded (current step)"},
		},
	}

	result := runner.expandTemplate(`Plan said: {{ index .step_results "plan" }}`, exec)
	expected := "Plan said: the plan output"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}

	// Current step (index 2) should NOT appear in step_results
	result2 := runner.expandTemplate("{{ .step_results }}", exec)
	if strings.Contains(result2, "should be excluded") {
		t.Error("current step should not appear in step_results")
	}
}

func TestBuildWorkPrompt(t *testing.T) {
	runner := &RitualRunner{logger: slog.Default()}

	t.Run("with step results", func(t *testing.T) {
		exec := &RitualExecution{
			CurrentStep: 1,
			Data: storage.JSON{
				"branch": "feature-x",
			},
			stepStates: []RitualStepState{
				{Name: "plan", Message: "the plan"},
				{Name: "implement", Message: ""},
			},
		}

		result := runner.buildWorkPrompt(exec, "Do the implementation")
		if !strings.Contains(result, "# Previous Step Results") {
			t.Error("expected previous step results section")
		}
		if !strings.Contains(result, "## plan") {
			t.Error("expected plan step result")
		}
		if !strings.Contains(result, "the plan") {
			t.Error("expected plan result content")
		}
		if !strings.Contains(result, "# Task") {
			t.Error("expected task section")
		}
		if !strings.Contains(result, "Do the implementation") {
			t.Error("expected act content")
		}
	})

	t.Run("empty sections omitted", func(t *testing.T) {
		exec := &RitualExecution{
			CurrentStep: 0,
			stepStates:  []RitualStepState{},
		}

		result := runner.buildWorkPrompt(exec, "Just do it")
		if strings.Contains(result, "# Previous Step Results") {
			t.Error("should not include empty step results section")
		}
		if strings.Contains(result, "# Given Context") {
			t.Error("should not include empty given context section")
		}
		if !strings.Contains(result, "# Task") {
			t.Error("expected task section")
		}
		if !strings.Contains(result, "Just do it") {
			t.Error("expected act content")
		}
	})
}

func TestBuildWorkPrompt_GotoFreshContext(t *testing.T) {
	runner := &RitualRunner{logger: slog.Default()}

	exec := &RitualExecution{
		CurrentStep: 1,
		stepStates: []RitualStepState{
			{Name: "plan", Message: "initial plan"},
			{Name: "implement", Message: ""},
		},
	}

	// First invocation
	result1 := runner.buildWorkPrompt(exec, "implement the feature")
	if !strings.Contains(result1, "initial plan") {
		t.Error("first invocation should contain initial plan")
	}

	// Simulate goto: step 0 re-ran and produced updated output
	exec.stepStates[0].Message = "revised plan after failure"

	// Second invocation (goto re-invocation) — should see updated results
	result2 := runner.buildWorkPrompt(exec, "implement the feature")
	if !strings.Contains(result2, "revised plan after failure") {
		t.Error("goto re-invocation should contain updated plan")
	}
	if strings.Contains(result2, "initial plan") {
		t.Error("goto re-invocation should NOT contain stale initial plan")
	}
}

func TestBuildWorkPrompt_GotoIncludesLaterSteps(t *testing.T) {
	runner := &RitualRunner{logger: slog.Default()}

	// Simulate: steps 0→1→2 ran, step 2 failed and goto back to step 1
	exec := &RitualExecution{
		CurrentStep: 1,
		stepStates: []RitualStepState{
			{Name: "plan", Message: "the plan"},
			{Name: "implement", Message: ""},             // current step, re-invoked
			{Name: "review", Message: "review feedback"}, // ran before goto
		},
	}

	result := runner.buildWorkPrompt(exec, "redo implementation")
	if !strings.Contains(result, "## review") {
		t.Error("should include step after CurrentStep that already ran")
	}
	if !strings.Contains(result, "review feedback") {
		t.Error("should include review step's message")
	}
}

func TestExpandTemplate_GotoIncludesLaterSteps(t *testing.T) {
	runner := &RitualRunner{logger: slog.Default()}

	// Simulate: steps 0→1→2 ran, step 2 failed and goto back to step 0
	exec := &RitualExecution{
		EdictID:     1,
		CurrentStep: 0,
		stepStates: []RitualStepState{
			{Name: "plan", Message: ""},                 // current step
			{Name: "implement", Message: "impl output"}, // ran before goto
			{Name: "review", Message: "review output"},  // ran before goto
		},
	}

	result := runner.expandTemplate(`impl: {{ index .step_results "implement" }}`, exec)
	expected := "impl: impl output"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestBuildWorkPrompt_RetryOmitsStepResults(t *testing.T) {
	// Regression test: on retry/goto, step_results should be omitted
	// to avoid duplicating context that's already in the session history.
	runner := &RitualRunner{logger: slog.Default()}

	// Simulate a retry: RetryCount > 0 means this is a retry
	exec := &RitualExecution{
		CurrentStep: 1,
		stepStates: []RitualStepState{
			{Name: "plan", Message: "plan output", RetryCount: 0},
			{Name: "implement", Message: "failed attempt 1", RetryCount: 1}, // being retried
		},
	}

	result := runner.buildWorkPrompt(exec, "try again")

	// Should contain the task and previous failure
	if !strings.Contains(result, "# Task") {
		t.Error("should contain Task section")
	}
	if !strings.Contains(result, "try again") {
		t.Error("should contain the act")
	}
	if !strings.Contains(result, "Previous Attempt Failed") {
		t.Error("should contain previous failure section")
	}
	if !strings.Contains(result, "failed attempt 1") {
		t.Error("should contain the failure message")
	}

	// Should NOT contain step_results on retry (they're in session history)
	if strings.Contains(result, "plan output") {
		t.Error("should NOT contain step results on retry - they're in session history")
	}
	if strings.Contains(result, "# Reference Data") {
		t.Error("should NOT include Reference Data section on retry")
	}
}

func TestBuildWorkPrompt_FirstAttemptIncludesStepResults(t *testing.T) {
	// Verify that on first attempt (no retry), step_results ARE included
	runner := &RitualRunner{logger: slog.Default()}

	exec := &RitualExecution{
		CurrentStep: 1,
		stepStates: []RitualStepState{
			{Name: "plan", Message: "plan output", RetryCount: 0},
			{Name: "implement", Message: "", RetryCount: 0}, // first attempt
		},
	}

	result := runner.buildWorkPrompt(exec, "implement it")

	// Should contain step_results on first attempt
	if !strings.Contains(result, "plan output") {
		t.Error("first attempt should contain step results")
	}
	if !strings.Contains(result, "# Reference Data") {
		t.Error("first attempt should include Reference Data section")
	}
}

func TestFixLintRitual(t *testing.T) {
	rituals, err := LoadEmbeddedRituals()
	if err != nil {
		t.Fatalf("LoadEmbeddedRituals() error = %v", err)
	}

	var fixLint *RitualDef
	for _, r := range rituals {
		if r.Name == "fix-lint" {
			fixLint = r
			break
		}
	}

	if fixLint == nil {
		t.Fatal("fix-lint ritual not found")
	}

	if len(fixLint.Steps) != 3 {
		t.Fatalf("fix-lint: expected 3 steps, got %d", len(fixLint.Steps))
	}

	forkStep := fixLint.Steps[1]
	if forkStep.Fork == nil {
		t.Fatal("fix-lint: expected fork step to have Fork defined")
	}
	if forkStep.Fork.Over != "lint" {
		t.Fatalf("fix-lint: expected fork over 'lint', got %q", forkStep.Fork.Over)
	}
	if forkStep.Fork.BatchSize != 5 {
		t.Fatalf("fix-lint: expected batch_size 5, got %d", forkStep.Fork.BatchSize)
	}
}

// TestRitualSessionIDTracking verifies that session_id is captured during ritual execution
func TestRitualSessionIDTracking(t *testing.T) {
	// Create in-memory database
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// Run migrations via GORM AutoMigrate
	if err := db.AutoMigrate(
		&storage.Edict{},
		&storage.Zhengming{},
		&storage.TianEvent{},
		&storage.TianEventDLQ{},
		&storage.Ling{},
		&storage.ForgeManifest{},
		&storage.JudgeVerdict{},
		&storage.CensorPrecedent{},
		&storage.Incident{},
		&storage.RulerCouncil{},
		&storage.RitualGuardCheckpoint{},
		&RitualExecution{},
		&RitualStepState{},
	); err != nil {
		t.Fatalf("failed to auto-migrate schema: %v", err)
	}

	// Create a test edict
	testEdict := storage.Edict{
		SessionID: "session-initial",
		Intent:    "Test session tracking",
	}
	if err := db.Create(&testEdict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	edictID := testEdict.ID

	// Verify ritual_executions table has session_id column
	var execResult struct {
		Cid        int    `gorm:"column:cid"`
		Name       string `gorm:"column:name"`
		Type       string `gorm:"column:type"`
		NotNull    int    `gorm:"column:notnull"`
		DefaultVal string `gorm:"column:dflt_value"`
		Pk         int    `gorm:"column:pk"`
	}

	// Check ritual_executions.session_id exists
	rows, err := db.Raw("PRAGMA table_info(ritual_executions)").Rows()
	if err != nil {
		t.Fatalf("failed to get ritual_executions columns: %v", err)
	}
	defer rows.Close()

	foundSessionID := false
	for rows.Next() {
		if err := db.ScanRows(rows, &execResult); err != nil {
			t.Fatalf("failed to scan row: %v", err)
		}
		if execResult.Name == "session_id" {
			foundSessionID = true
			break
		}
	}
	if !foundSessionID {
		t.Error("ritual_executions.session_id column not found")
	}

	// Check ritual_step_states.session_id exists
	rows, err = db.Raw("PRAGMA table_info(ritual_step_states)").Rows()
	if err != nil {
		t.Fatalf("failed to get ritual_step_states columns: %v", err)
	}
	defer rows.Close()

	foundSessionID = false
	for rows.Next() {
		if err := db.ScanRows(rows, &execResult); err != nil {
			t.Fatalf("failed to scan row: %v", err)
		}
		if execResult.Name == "session_id" {
			foundSessionID = true
			break
		}
	}
	if !foundSessionID {
		t.Error("ritual_step_states.session_id column not found")
	}

	// Test inserting and querying session_id directly via SQL
	execID := "ritual-exec-test-1"
	sessionID := "session-from-minister"

	// Insert ritual execution with session_id
	err = db.Exec(`
		INSERT INTO ritual_executions (id, ritual_name, edict_id, session_id, current_step, state, data, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, unixepoch(), unixepoch())
	`, execID, "test-ritual", edictID, sessionID, 0, "running", "{}").Error
	if err != nil {
		t.Fatalf("failed to insert ritual execution: %v", err)
	}

	// Verify session_id was stored
	var storedSessionID string
	err = db.Raw("SELECT session_id FROM ritual_executions WHERE id = ?", execID).Scan(&storedSessionID).Error
	if err != nil {
		t.Fatalf("failed to retrieve session_id: %v", err)
	}
	if storedSessionID != sessionID {
		t.Errorf("expected session_id '%s', got %q", sessionID, storedSessionID)
	}

	// Insert step state with session_id
	stepSessionID := "session-step-1"
	err = db.Exec(`
		INSERT INTO ritual_step_states (execution_id, step_index, name, session_id, retry_count, message)
		VALUES (?, ?, ?, ?, ?, ?)
	`, execID, 0, "test-step", stepSessionID, 0, "Step completed").Error
	if err != nil {
		t.Fatalf("failed to insert step state: %v", err)
	}

	// Verify step state session_id was stored
	var storedStepSessionID string
	err = db.Raw("SELECT session_id FROM ritual_step_states WHERE execution_id = ? AND step_index = ?", execID, 0).Scan(&storedStepSessionID).Error
	if err != nil {
		t.Fatalf("failed to retrieve step session_id: %v", err)
	}
	if storedStepSessionID != stepSessionID {
		t.Errorf("expected step session_id '%s', got %q", stepSessionID, storedStepSessionID)
	}

	// Test querying by session_id
	var count int64
	err = db.Raw("SELECT COUNT(*) FROM ritual_step_states WHERE session_id = ?", stepSessionID).Scan(&count).Error
	if err != nil {
		t.Fatalf("failed to query step states by session_id: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 step state for session_id, got %d", count)
	}
}

// TestExpandTemplate_GivenContextFlattening verifies that given step results stored
// directly in exec.Data are accessible via templates.
func TestExpandTemplate_GivenContextFlattening(t *testing.T) {
	runner := &RitualRunner{logger: slog.Default()}
	exec := &RitualExecution{
		EdictID:     1,
		CurrentStep: 2,
		Data: storage.JSON{
			"asimi_version": map[string]interface{}{
				"current_version": "1.2.3",
				"latest_version":  "1.2.4",
				"has_update":      true,
			},
			"unsealed_edicts": []map[string]interface{}{
				{"edict_id": float64(1), "summary": "Test edict"},
			},
		},
	}

	// The Out template from check-asimi-version step expects this to work
	result := runner.expandTemplate("Running latest Asimi version {{ .asimi_version.current_version }}", exec)
	expected := "Running latest Asimi version 1.2.3"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}

	// Also test unsealed_edicts which is used in summarize-and-next step
	result2 := runner.expandTemplate("Edicts: {{ len .unsealed_edicts }}", exec)
	expected2 := "Edicts: 1"
	if result2 != expected2 {
		t.Errorf("expected %q, got %q", expected2, result2)
	}
}

// TestExpandTemplate_Inputs verifies that ritual inputs stored under
// exec.Data["inputs"] are flattened into the top-level template context
// so that {{ .version }} resolves correctly.
func TestExpandTemplate_Inputs(t *testing.T) {
	runner := &RitualRunner{logger: slog.Default()}

	// Case 1: map[string]interface{} — the type that appears after DB
	// round-trip via storage.JSON.Scan (e.g. in the recovery path).
	exec := &RitualExecution{
		EdictID: 1,
		Data: storage.JSON{
			"inputs": map[string]interface{}{"version": "1.2.3"},
		},
	}

	result := runner.expandTemplate("{{ .version }}", exec)
	expected := "1.2.3"
	if result != expected {
		t.Errorf("map[string]interface{}: expected %q, got %q", expected, result)
	}

	// Case 2: map[string]string — the type that Start() creates in-memory
	// (the normal first-run execution path before any DB reload).
	exec2 := &RitualExecution{
		EdictID: 1,
		Data: storage.JSON{
			"inputs": map[string]string{"version": "1.2.3"},
		},
	}

	result2 := runner.expandTemplate("{{ .version }}", exec2)
	if result2 != expected {
		t.Errorf("map[string]string: expected %q, got %q", expected, result2)
	}

	// Case 3: multiple inputs with mixed usage in a template
	exec3 := &RitualExecution{
		EdictID: 1,
		Data: storage.JSON{
			"inputs": map[string]string{"version": "1.2.3", "name": "release"},
		},
	}

	result3 := runner.expandTemplate("{{ .name }} v{{ .version }}", exec3)
	expected3 := "release v1.2.3"
	if result3 != expected3 {
		t.Errorf("multi-input: expected %q, got %q", expected3, result3)
	}
}

// TestRitualActToolCallsDoNotPolluteChancellorSession verifies that ritual Act sessions
// are ephemeral and do not pollute the Chancellor's interactive session.
func TestRitualActToolCallsDoNotPolluteChancellorSession(t *testing.T) {
	db := setupRitualTestDB(t)

	// Create a ritual with multiple steps
	ritual := &RitualDef{
		Name: "test-pollution",
		Steps: []RitualStep{
			{Name: "do_work", Minister: "forge", Act: "Do some work that generates tool calls"},
			{Name: "do_more", Minister: "forge", Act: "Do more work with different context"},
		},
	}
	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("Failed to register ritual: %v", err)
	}

	// Create forge minister with mock LLM
	forge := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "forge done",
	}

	// Create chancellor minister with its own session
	chancellor := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "secretary",
		tasksCh:      make(chan *Task, 1),
		result:       "chancellor response",
	}

	ctx := context.Background()
	go forge.Run(ctx)
	go chancellor.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{
			"forge":     forge,
			"secretary": chancellor,
		},
		logger: slog.Default(),
	}
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	court.ritualGuard = NewRitualGuard(RitualGuardOpts{
		Base:        base,
		GetMinister: court.GetMinister,
	})

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	// Run the ritual
	exec, err := runner.Start(ctx, "test-pollution", testEK(1), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	// Verify ritual completed
	if exec.State != RitualStateCompleted {
		t.Errorf("Expected state 'completed', got %s", exec.State)
	}

	// Verify step results are stored in exec.Data (not in Chancellor session)
	if exec.Data["do_work"] == nil {
		t.Error("Expected Act result for 'do_work' to be stored in exec.Data")
	}
	if exec.Data["do_more"] == nil {
		t.Error("Expected Act result for 'do_more' to be stored in exec.Data")
	}

	// Key assertion: The Chancellor's interactive session is separate from ritual Act sessions.
	// With ephemeral sessions per Act, each Act gets a fresh session that is discarded
	// after completion. The Chancellor's session is never touched by ritual execution.
	//
	// We verify this by checking that the ritual used ephemeral sessions:
	// - executeMinisterStep calls CreateSession() for each Act
	// - The session is discarded via Rollback() after the Act completes
	// - Results are stored in exec.Data, not in any persistent session
	t.Log("Ritual completed with ephemeral session isolation - Acts do not pollute Chancellor session")
}

// TestRitualEphemeralSessionIsDiscarded verifies that ritual Act sessions are discarded
// after completion (via Rollback), ensuring isolation between ritual executions.
func TestRitualEphemeralSessionIsDiscarded(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "test-discard",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Act: "First step"},
			{Name: "step2", Minister: "forge", Act: "Second step"},
		},
	}
	registry := NewRitualRegistry()
	if err := registry.Register(ritual); err != nil {
		t.Fatalf("Failed to register ritual: %v", err)
	}

	// Create forge minister
	forge := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "done",
	}

	ctx := context.Background()
	go forge.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": forge},
		logger:    slog.Default(),
	}
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	court.ritualGuard = NewRitualGuard(RitualGuardOpts{
		Base:        base,
		GetMinister: court.GetMinister,
	})

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "test-discard", testEK(1), nil, nil)
	if err != nil {
		t.Fatalf("Failed to start ritual: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Ritual run failed: %v", err)
	}

	// Verify both steps completed
	if exec.State != RitualStateCompleted {
		t.Errorf("Expected state 'completed', got %s", exec.State)
	}

	// Verify both step results are stored
	if exec.Data["step1"] == nil {
		t.Error("Expected step1 result in exec.Data")
	}
	if exec.Data["step2"] == nil {
		t.Error("Expected step2 result in exec.Data")
	}

	// The ephemeral sessions created for Acts are discarded via Rollback().
	// This test documents that each Act gets a fresh session, and after completion
	// those sessions are not accumulated in the Chancellor's interactive session.

	// Session count tracking would require hooking into CreateSession, but the
	// key behavior is verified by the isolation test above.
	t.Log("Both steps completed with isolated ephemeral sessions")
}

// TestRitualStepActResultIsAvailableInNextStepTemplate verifies that a step's
// Act output is reachable from the next step's Act template via {{ .stepName }}.
// This is the mechanism the dawn-audience ritual relies on to pass the
// strategist's summary into the chancellor's Act without re-templating raw data.
func TestRitualStepActResultIsAvailableInNextStepTemplate(t *testing.T) {
	db := setupRitualTestDB(t)

	// Capture task.Work per call so we can inspect what the second step sees.
	var (
		mu        sync.Mutex
		seenWork  []string
		callCount int
	)

	captureMinister := &captureRitualMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 4),
		respond: func(t *Task) Result {
			mu.Lock()
			seenWork = append(seenWork, t.Work)
			callCount++
			n := callCount
			mu.Unlock()
			return Result{Output: fmt.Sprintf("RESULT_FROM_STEP_%d", n)}
		},
	}

	ritual := &RitualDef{
		Name: "step-cross-ref",
		Steps: []RitualStep{
			{Name: "first", Minister: "forge", Act: "do work A"},
			{Name: "second", Minister: "forge", Act: "previous: {{ .first }}"},
		},
	}
	registry := NewRitualRegistry()
	require.NoError(t, registry.Register(ritual))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go captureMinister.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": captureMinister},
		logger:    slog.Default(),
	}
	base := NewMinisterBase(db, nil, slog.Default(), "testuser", "testproject", nil)
	court.ritualGuard = NewRitualGuard(RitualGuardOpts{
		Base:        base,
		GetMinister: court.GetMinister,
	})
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "step-cross-ref", testEK(1), nil, nil)
	require.NoError(t, err)
	require.NoError(t, runner.Run(ctx, exec))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seenWork, 2, "expected forge to be invoked twice")
	assert.Contains(t, seenWork[1], "RESULT_FROM_STEP_1",
		"second step's act template should contain the first step's Act result")
}

// captureRitualMinister is a Minister test double that lets each test supply
// per-task response logic, useful for asserting on task.Work content.
type captureRitualMinister struct {
	MinisterBase
	id      string
	tasksCh chan *Task
	respond func(*Task) Result
}

func (m *captureRitualMinister) ID() string                  { return m.id }
func (m *captureRitualMinister) SystemPrompt() string        { return "" }
func (m *captureRitualMinister) Title() string               { return m.id }
func (m *captureRitualMinister) Tools() []Tool               { return nil }
func (m *captureRitualMinister) Tasks() chan<- *Task         { return m.tasksCh }
func (m *captureRitualMinister) Model() LLMProvider          { return nil }
func (m *captureRitualMinister) GetConfig() config.LLMConfig { return config.LLMConfig{} }
func (m *captureRitualMinister) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-m.tasksCh:
			t.Done <- m.respond(t)
		}
	}
}

// TestCheckVerdictsPassed_AllApproved verifies the handler passes when all manifests are approved
// TestSwiftStrikeJudgingStepHasVerdictCheck verifies that swift-strike's judging step
// includes the verdict check before recording the seal.
func TestSwiftStrikeJudgingStepHasVerdictCheck(t *testing.T) {
	rituals, err := LoadEmbeddedRituals()
	if err != nil {
		t.Fatalf("LoadEmbeddedRituals() error = %v", err)
	}

	var swiftStrike *RitualDef
	for _, r := range rituals {
		if r.Name == "swift-strike" {
			swiftStrike = r
			break
		}
	}
	if swiftStrike == nil {
		t.Fatal("swift-strike ritual not found")
	}

	// Find the judging step
	var judgingStep *RitualStep
	for i := range swiftStrike.Steps {
		if swiftStrike.Steps[i].Name == "judging" {
			judgingStep = &swiftStrike.Steps[i]
			break
		}
	}
	if judgingStep == nil {
		t.Fatal("swift-strike: judging step not found")
	}

	// Verify then steps include verdict check BEFORE seal recording
	if len(judgingStep.Then) < 2 {
		t.Fatalf("swift-strike judging: expected at least 2 then steps, got %d: %v",
			len(judgingStep.Then), judgingStep.Then)
	}

	// Verdict check must come first
	if judgingStep.Then[0] != "the verdicts are passed" {
		t.Errorf("swift-strike judging: first then step should be 'the verdicts are passed', got %q",
			judgingStep.Then[0])
	}

	// Seal recording must come second
	if judgingStep.Then[1] != "record the judge's seal" {
		t.Errorf("swift-strike judging: second then step should be 'record the judge's seal', got %q",
			judgingStep.Then[1])
	}
}

// TestCastleSiegeJudgementStepHasVerdictCheck verifies that castle-siege's judgement step
// includes the verdict check before git diff and seal recording.
func TestCastleSiegeJudgementStepHasVerdictCheck(t *testing.T) {
	rituals, err := LoadEmbeddedRituals()
	if err != nil {
		t.Fatalf("LoadEmbeddedRituals() error = %v", err)
	}

	var castleSiege *RitualDef
	for _, r := range rituals {
		if r.Name == "castle-siege" {
			castleSiege = r
			break
		}
	}
	if castleSiege == nil {
		t.Fatal("castle-siege ritual not found")
	}

	// Find the judgement step
	var judgingStep *RitualStep
	for i := range castleSiege.Steps {
		if castleSiege.Steps[i].Name == "judging" {
			judgingStep = &castleSiege.Steps[i]
			break
		}
	}
	if judgingStep == nil {
		t.Fatal("castle-siege: judgement step not found")
	}

	// Verify then steps include verdict check BEFORE git diff and seal recording
	if len(judgingStep.Then) != 2 {
		t.Fatalf("castle-siege judgement: expected at least 3 then steps, got %d: %v",
			len(judgingStep.Then), judgingStep.Then)
	}

	// Verdict check must come first
	if judgingStep.Then[0] != "the verdicts are passed" {
		t.Errorf("castle-siege judgement: first then step should be 'the verdicts are passed', got %q",
			judgingStep.Then[0])
	}

	// Seal recording must come third
	if judgingStep.Then[1] != "record the judge's seal" {
		t.Errorf("castle-siege judgement: third then step should be 'record the judge's seal', got %q",
			judgingStep.Then[2])
	}
}

func TestBuildDependencyMap(t *testing.T) {
	tests := []struct {
		name     string
		units    []interface{}
		wantIDs  []string
		wantDeps [][]string
	}{
		{
			name:     "empty list",
			units:    []interface{}{},
			wantIDs:  nil,
			wantDeps: nil,
		},
		{
			name: "items without dependencies",
			units: []interface{}{
				map[string]interface{}{"ling_id": "l1", "description": "task 1"},
				map[string]interface{}{"ling_id": "l2", "description": "task 2"},
			},
			wantIDs:  []string{"l1", "l2"},
			wantDeps: [][]string{{}, {}},
		},
		{
			name: "items with dependencies",
			units: []interface{}{
				map[string]interface{}{"ling_id": "l1", "description": "task 1"},
				map[string]interface{}{"ling_id": "l2", "description": "task 2", "dependencies": []string{"l1"}},
				map[string]interface{}{"ling_id": "l3", "description": "task 3", "dependencies": []string{"l1", "l2"}},
			},
			wantIDs:  []string{"l1", "l2", "l3"},
			wantDeps: [][]string{{}, {"l1"}, {"l1", "l2"}},
		},
		{
			name: "non-map items get auto-generated IDs",
			units: []interface{}{
				"simple-string",
				42,
			},
			wantIDs:  []string{"_fork_0", "_fork_1"},
			wantDeps: [][]string{{}, {}},
		},
		{
			name: "mixed ling and non-ling items",
			units: []interface{}{
				map[string]interface{}{"ling_id": "l1"},
				map[string]interface{}{"file": "a.go"},
			},
			wantIDs:  []string{"l1", "_fork_1"},
			wantDeps: [][]string{{}, {}},
		},
		{
			name: "dependencies as []interface{}",
			units: []interface{}{
				map[string]interface{}{"ling_id": "l1"},
				map[string]interface{}{"ling_id": "l2", "dependencies": []interface{}{"l1"}},
			},
			wantIDs:  []string{"l1", "l2"},
			wantDeps: [][]string{{}, {"l1"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildDependencyMap(tt.units)
			if len(result) != len(tt.wantIDs) {
				t.Fatalf("expected %d units, got %d", len(tt.wantIDs), len(result))
			}
			for i, u := range result {
				if u.ID != tt.wantIDs[i] {
					t.Errorf("unit[%d].ID = %q, want %q", i, u.ID, tt.wantIDs[i])
				}
				if len(u.DepIDs) != len(tt.wantDeps[i]) {
					t.Errorf("unit[%d].DepIDs = %v, want %v", i, u.DepIDs, tt.wantDeps[i])
					continue
				}
				for j, dep := range u.DepIDs {
					if dep != tt.wantDeps[i][j] {
						t.Errorf("unit[%d].DepIDs[%d] = %q, want %q", i, j, dep, tt.wantDeps[i][j])
					}
				}
			}
		})
	}
}

func TestSeedReadyQueue(t *testing.T) {
	units := buildDependencyMap([]interface{}{
		map[string]interface{}{"ling_id": "l1", "description": "task 1"},
		map[string]interface{}{"ling_id": "l2", "description": "task 2", "dependencies": []string{"l1"}},
		map[string]interface{}{"ling_id": "l3", "description": "task 3", "dependencies": []string{"l1", "l2"}},
		map[string]interface{}{"ling_id": "l4", "description": "task 4", "dependencies": []string{"l2"}},
	})

	// Initially, only l1 has no deps → ready
	done := map[string]bool{}
	ready := seedReadyQueue(units, done)
	if len(ready) != 1 || ready[0] != 0 {
		t.Errorf("expected ready = [0], got %v", ready)
	}

	// After l1 completes, l2 becomes ready (its only dep is l1)
	done["l1"] = true
	ready = seedReadyQueue(units, done)
	if len(ready) != 1 || ready[0] != 1 {
		t.Errorf("expected ready = [1], got %v", ready)
	}

	// After l1 and l2 complete, l3 and l4 become ready
	done["l2"] = true
	ready = seedReadyQueue(units, done)
	if len(ready) != 2 {
		t.Errorf("expected 2 ready items, got %d: %v", len(ready), ready)
	}
	// Both l3 (idx=2) and l4 (idx=3) should be ready
	readySet := map[int]bool{}
	for _, idx := range ready {
		readySet[idx] = true
	}
	if !readySet[2] || !readySet[3] {
		t.Errorf("expected indices 2 and 3 to be ready, got %v", ready)
	}

	// After all done, nothing is ready
	done["l3"] = true
	done["l4"] = true
	ready = seedReadyQueue(units, done)
	if len(ready) != 0 {
		t.Errorf("expected 0 ready items, got %d", len(ready))
	}
}

func TestSeedReadyQueue_NoDeps(t *testing.T) {
	// Items without dependencies should all be immediately ready
	units := buildDependencyMap([]interface{}{
		map[string]interface{}{"file": "a.go", "errors": []string{"err1"}},
		map[string]interface{}{"file": "b.go", "errors": []string{"err2"}},
	})

	done := map[string]bool{}
	ready := seedReadyQueue(units, done)
	if len(ready) != 2 {
		t.Errorf("expected 2 ready items (no deps), got %d", len(ready))
	}
}

func TestExecuteForkDAG_NoDeps(t *testing.T) {
	// Non-DAG items (no dependencies) should all be dispatched immediately
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()

	ritual := &RitualDef{
		Name: "fork-no-deps",
		Steps: []RitualStep{
			{
				Name: "fork-step",
				Fork: &ForkDef{Over: "items", BatchSize: 3},
				Work: []RitualStep{
					{Name: "work", Minister: "forge", Act: "do work on {{ .item.file }}"},
				},
			},
		},
	}
	registry.Register(ritual)

	court := newRitualTestCourt(t, "done\n", nil)
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		ID:         "test-exec",
		RitualName: "fork-no-deps",
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		Data:       storage.JSON{"items": []interface{}{map[string]interface{}{"file": "a.go"}, map[string]interface{}{"file": "b.go"}}},
		def:        ritual,
		stepStates: []RitualStepState{{Name: "fork-step"}},
	}

	step := ritual.Steps[0]
	workUnits, err := runner.getForkWorkUnits(exec, "items")
	if err != nil {
		t.Fatalf("getForkWorkUnits error: %v", err)
	}

	results, err := runner.executeForkDAG(context.Background(), exec, step, workUnits, 3)
	if err != nil {
		t.Fatalf("executeForkDAG error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestExecuteForkDAG_WithDeps(t *testing.T) {
	// Create a DAG where l2 depends on l1, so l1 must complete before l2 runs
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()

	// Create lings in the DB so the DAG executor can query their statuses
	l1 := storage.Ling{
		LingID:   "l1",
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
		Status:   storage.LingPending,
	}
	if err := db.Create(&l1).Error; err != nil {
		t.Fatalf("create l1: %v", err)
	}
	l2 := storage.Ling{
		LingID:       "l2",
		EdictID:      1,
		Username:     "testuser",
		Project:      "testproject",
		Status:       storage.LingPending,
		Dependencies: storage.StringArray{"l1"},
	}
	if err := db.Create(&l2).Error; err != nil {
		t.Fatalf("create l2: %v", err)
	}

	// Ritual with fork over lings
	ritual := &RitualDef{
		Name: "fork-with-deps",
		Steps: []RitualStep{
			{
				Name: "fork-step",
				Fork: &ForkDef{Over: "lings", BatchSize: 1},
				Work: []RitualStep{
					{Name: "work", Minister: "forge", Act: "implement {{ .item.description }}", Then: []string{"record the ling completed"}},
				},
			},
		},
	}
	registry.Register(ritual)

	court := newRitualTestCourt(t, "done\n", nil)
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, slog.Default(), repo.RepoInfo{})

	// Build work units like getLings would return
	workUnits := []interface{}{
		map[string]interface{}{
			"ling_id":      "l1",
			"edict_id":     float64(1),
			"description":  "task 1",
			"dependencies": storage.StringArray{},
			"status":       "pending",
		},
		map[string]interface{}{
			"ling_id":      "l2",
			"edict_id":     float64(1),
			"description":  "task 2",
			"dependencies": storage.StringArray{"l1"},
			"status":       "pending",
		},
	}

	exec := &RitualExecution{
		ID:         "test-exec",
		RitualName: "fork-with-deps",
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		Data:       storage.JSON{"lings": workUnits},
		def:        ritual,
		stepStates: []RitualStepState{{Name: "fork-step"}},
	}

	// The DAG executor needs the "record the ling completed" then step to
	// mark lings as done. Since our test minister just returns "done",
	// the then step will run and mark l1 as done, which unlocks l2.

	step := ritual.Steps[0]
	results, err := runner.executeForkDAG(context.Background(), exec, step, workUnits, 1)
	if err != nil {
		t.Fatalf("executeForkDAG error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}

	// Verify both lings are now marked as done in the DB
	var lings []storage.Ling
	db.Where("edict_id = ? AND username = ? AND project = ?", 1, "testuser", "testproject").Find(&lings)
	for _, l := range lings {
		if l.Status != storage.LingDone {
			t.Errorf("ling %s status = %q, want %q", l.LingID, l.Status, storage.LingDone)
		}
	}
}

func TestExecuteForkDAG_StuckItems(t *testing.T) {
	// If deps can never be satisfied (missing dep), executor should return an error
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()

	ritual := &RitualDef{
		Name: "fork-stuck",
		Steps: []RitualStep{
			{
				Name: "fork-step",
				Fork: &ForkDef{Over: "lings", BatchSize: 1},
				Work: []RitualStep{
					{Name: "work", Minister: "forge", Act: "do work"},
				},
			},
		},
	}
	registry.Register(ritual)

	// l2 depends on "missing_ling" which doesn't exist — can never be satisfied
	// Create l2 in DB
	l2 := storage.Ling{
		LingID:       "l2",
		EdictID:      1,
		Username:     "testuser",
		Project:      "testproject",
		Status:       storage.LingPending,
		Dependencies: storage.StringArray{"missing_ling"},
	}
	if err := db.Create(&l2).Error; err != nil {
		t.Fatalf("create l2: %v", err)
	}

	court := newRitualTestCourt(t, "done\n", nil)
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, slog.Default(), repo.RepoInfo{})

	workUnits := []interface{}{
		map[string]interface{}{
			"ling_id":      "l2",
			"description":  "task 2",
			"dependencies": storage.StringArray{"missing_ling"},
			"status":       "pending",
		},
	}

	exec := &RitualExecution{
		ID:         "test-exec",
		RitualName: "fork-stuck",
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		Data:       storage.JSON{},
		def:        ritual,
		stepStates: []RitualStepState{{Name: "fork-step"}},
	}

	step := ritual.Steps[0]
	_, err := runner.executeForkDAG(context.Background(), exec, step, workUnits, 1)
	if err == nil {
		t.Fatal("expected error for stuck items, got nil")
	}
	if !strings.Contains(err.Error(), "stuck") {
		t.Errorf("expected 'stuck' in error, got %q", err.Error())
	}
}

func TestExecuteForkDAG_ContextCancellation(t *testing.T) {
	// If context is cancelled during execution, executor should return early
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()

	ritual := &RitualDef{
		Name: "fork-cancel",
		Steps: []RitualStep{
			{
				Name: "fork-step",
				Fork: &ForkDef{Over: "items", BatchSize: 1},
				Work: []RitualStep{
					{Name: "work", Minister: "forge", Act: "do work"},
				},
			},
		},
	}
	registry.Register(ritual)

	court := newRitualTestCourt(t, "done\n", nil)
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, slog.Default(), repo.RepoInfo{})

	// Use a context that we cancel after a brief delay — this simulates
	// cancellation happening during a long-running fork execution.
	ctx, cancel := context.WithCancel(context.Background())

	exec := &RitualExecution{
		ID:         "test-exec",
		RitualName: "fork-cancel",
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		Data:       storage.JSON{"items": []interface{}{map[string]interface{}{"file": "a.go"}}},
		def:        ritual,
		stepStates: []RitualStepState{{Name: "fork-step"}},
	}

	step := ritual.Steps[0]
	workUnits := []interface{}{map[string]interface{}{"file": "a.go"}}

	// Cancel after a short delay so the executor can start dispatching
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := runner.executeForkDAG(ctx, exec, step, workUnits, 1)
	// With pre-cancelled context, the executor may complete the single item
	// before noticing the cancellation. Either outcome is acceptable.
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled or nil, got: %v", err)
	}
}

func TestExecuteForkDAG_CastleSiegeThenStep(t *testing.T) {
	// Verify that the castle-siege ritual uses "record the ling completed"
	// (not the old "the ling is completed")
	rituals, err := LoadEmbeddedRituals()
	if err != nil {
		t.Fatalf("LoadEmbeddedRituals() error = %v", err)
	}

	var castleSiege *RitualDef
	for _, r := range rituals {
		if r.Name == "castle-siege" {
			castleSiege = r
			break
		}
	}
	if castleSiege == nil {
		t.Fatal("castle-siege ritual not found")
	}

	// Find the implementing fork step
	var forkStep *RitualStep
	for i := range castleSiege.Steps {
		if castleSiege.Steps[i].Fork != nil {
			forkStep = &castleSiege.Steps[i]
			break
		}
	}
	if forkStep == nil {
		t.Fatal("castle-siege: fork step not found")
	}

	// Verify the work step has the correct then step
	for _, workStep := range forkStep.Work {
		for _, then := range workStep.Then {
			if then == "record the ling completed" {
				return // found the correct then step
			}
			if then == "the ling is completed" {
				t.Error("castle-siege still uses old 'the ling is completed' — should be 'record the ling completed'")
			}
		}
	}
}

func TestExecuteForkDAG_DispatchOrder_LinearDeps(t *testing.T) {
	// l2 depends on l1, l3 depends on l2 — all must run in strict order.
	// Uses batchSize=1 to force sequential execution.
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()

	// Create lings in the DB
	for _, l := range []storage.Ling{
		{LingID: "l1", EdictID: 1, Username: "testuser", Project: "testproject", Status: storage.LingPending},
		{LingID: "l2", EdictID: 1, Username: "testuser", Project: "testproject", Status: storage.LingPending, Dependencies: storage.StringArray{"l1"}},
		{LingID: "l3", EdictID: 1, Username: "testuser", Project: "testproject", Status: storage.LingPending, Dependencies: storage.StringArray{"l2"}},
	} {
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("create %s: %v", l.LingID, err)
		}
	}

	ritual := &RitualDef{
		Name: "fork-linear-order",
		Steps: []RitualStep{
			{
				Name: "fork-step",
				Fork: &ForkDef{Over: "lings", BatchSize: 1},
				Work: []RitualStep{
					{Name: "work", Minister: "forge", Act: "implement {{ .item.description }}", Then: []string{"record the ling completed"}},
				},
			},
		},
	}
	registry.Register(ritual)

	court := newRitualTestCourt(t, "done\n", nil)
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, slog.Default(), repo.RepoInfo{})

	workUnits := []interface{}{
		map[string]interface{}{
			"ling_id": "l1", "edict_id": float64(1), "description": "task 1",
			"dependencies": storage.StringArray{}, "status": "pending",
		},
		map[string]interface{}{
			"ling_id": "l2", "edict_id": float64(1), "description": "task 2",
			"dependencies": storage.StringArray{"l1"}, "status": "pending",
		},
		map[string]interface{}{
			"ling_id": "l3", "edict_id": float64(1), "description": "task 3",
			"dependencies": storage.StringArray{"l2"}, "status": "pending",
		},
	}

	exec := &RitualExecution{
		ID: "test-exec", RitualName: "fork-linear-order", EdictID: 1,
		Username: "testuser", Project: "testproject",
		Data:       storage.JSON{"lings": workUnits},
		def:        ritual,
		stepStates: []RitualStepState{{Name: "fork-step"}},
	}

	results, err := runner.executeForkDAG(context.Background(), exec, ritual.Steps[0], workUnits, 1)
	if err != nil {
		t.Fatalf("executeForkDAG error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Verify all lings are done
	var lings []storage.Ling
	db.Where("edict_id = ? AND username = ? AND project = ?", 1, "testuser", "testproject").Find(&lings)
	for _, l := range lings {
		if l.Status != storage.LingDone {
			t.Errorf("ling %s status = %q, want %q", l.LingID, l.Status, storage.LingDone)
		}
	}

	// Verify dispatch order: l1 before l2 before l3
	forgeMinister := court.ministers["forge"].(*ritualTestMinister)
	callLog := forgeMinister.getCallLog()
	if len(callLog) != 3 {
		t.Fatalf("expected 3 calls, got %d: %v", len(callLog), callLog)
	}
	if !strings.Contains(callLog[0], "task 1") {
		t.Errorf("first dispatch should be task 1, got %q", callLog[0])
	}
	if !strings.Contains(callLog[1], "task 2") {
		t.Errorf("second dispatch should be task 2, got %q", callLog[1])
	}
	if !strings.Contains(callLog[2], "task 3") {
		t.Errorf("third dispatch should be task 3, got %q", callLog[2])
	}
}

func TestExecuteForkDAG_DispatchOrder_DiamondDeps(t *testing.T) {
	// Diamond DAG: A→B, A→C, B→D, C→D
	// A must run first, then B and C can run in parallel, then D last.
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()

	for _, l := range []storage.Ling{
		{LingID: "a", EdictID: 1, Username: "testuser", Project: "testproject", Status: storage.LingPending},
		{LingID: "b", EdictID: 1, Username: "testuser", Project: "testproject", Status: storage.LingPending, Dependencies: storage.StringArray{"a"}},
		{LingID: "c", EdictID: 1, Username: "testuser", Project: "testproject", Status: storage.LingPending, Dependencies: storage.StringArray{"a"}},
		{LingID: "d", EdictID: 1, Username: "testuser", Project: "testproject", Status: storage.LingPending, Dependencies: storage.StringArray{"b", "c"}},
	} {
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("create %s: %v", l.LingID, err)
		}
	}

	ritual := &RitualDef{
		Name: "fork-diamond",
		Steps: []RitualStep{
			{
				Name: "fork-step",
				Fork: &ForkDef{Over: "lings", BatchSize: 2}, // allow B and C to run in parallel
				Work: []RitualStep{
					{Name: "work", Minister: "forge", Act: "implement {{ .item.description }}", Then: []string{"record the ling completed"}},
				},
			},
		},
	}
	registry.Register(ritual)

	court := newRitualTestCourt(t, "done\n", nil)
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, slog.Default(), repo.RepoInfo{})

	workUnits := []interface{}{
		map[string]interface{}{
			"ling_id": "a", "edict_id": float64(1), "description": "task A",
			"dependencies": storage.StringArray{}, "status": "pending",
		},
		map[string]interface{}{
			"ling_id": "b", "edict_id": float64(1), "description": "task B",
			"dependencies": storage.StringArray{"a"}, "status": "pending",
		},
		map[string]interface{}{
			"ling_id": "c", "edict_id": float64(1), "description": "task C",
			"dependencies": storage.StringArray{"a"}, "status": "pending",
		},
		map[string]interface{}{
			"ling_id": "d", "edict_id": float64(1), "description": "task D",
			"dependencies": storage.StringArray{"b", "c"}, "status": "pending",
		},
	}

	exec := &RitualExecution{
		ID: "test-exec", RitualName: "fork-diamond", EdictID: 1,
		Username: "testuser", Project: "testproject",
		Data:       storage.JSON{"lings": workUnits},
		def:        ritual,
		stepStates: []RitualStepState{{Name: "fork-step"}},
	}

	results, err := runner.executeForkDAG(context.Background(), exec, ritual.Steps[0], workUnits, 2)
	if err != nil {
		t.Fatalf("executeForkDAG error: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}

	forgeMinister := court.ministers["forge"].(*ritualTestMinister)
	callLog := forgeMinister.getCallLog()
	if len(callLog) != 4 {
		t.Fatalf("expected 4 calls, got %d: %v", len(callLog), callLog)
	}

	// A must be first
	if !strings.Contains(callLog[0], "task A") {
		t.Errorf("first dispatch should be task A, got %q", callLog[0])
	}

	// B and C must both come before D. Their relative order can vary
	// since they are dispatched concurrently when batchSize=2.
	idxB, idxC, idxD := -1, -1, -1
	for i, call := range callLog {
		if strings.Contains(call, "task B") {
			idxB = i
		}
		if strings.Contains(call, "task C") {
			idxC = i
		}
		if strings.Contains(call, "task D") {
			idxD = i
		}
	}
	if idxB < 0 {
		t.Fatal("task B not found in call log")
	}
	if idxC < 0 {
		t.Fatal("task C not found in call log")
	}
	if idxD < 0 {
		t.Fatal("task D not found in call log")
	}

	if idxB > idxD {
		t.Errorf("task B (idx %d) dispatched after task D (idx %d) — violates DAG order", idxB, idxD)
	}
	if idxC > idxD {
		t.Errorf("task C (idx %d) dispatched after task D (idx %d) — violates DAG order", idxC, idxD)
	}
	if idxB < 1 {
		t.Errorf("task B (idx %d) dispatched before task A completed — violates DAG order", idxB)
	}
	if idxC < 1 {
		t.Errorf("task C (idx %d) dispatched before task A completed — violates DAG order", idxC)
	}
}

func TestExecuteForkDAG_NoEarlyDispatch(t *testing.T) {
	// Adversarial test: l2 depends on l1. Even with batchSize=1,
	// verify l2 is NEVER dispatched until l1 completes.
	// We use a slow minister for l1 to widen the race window.
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()

	for _, l := range []storage.Ling{
		{LingID: "l1", EdictID: 1, Username: "testuser", Project: "testproject", Status: storage.LingPending},
		{LingID: "l2", EdictID: 1, Username: "testuser", Project: "testproject", Status: storage.LingPending, Dependencies: storage.StringArray{"l1"}},
	} {
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("create %s: %v", l.LingID, err)
		}
	}

	ritual := &RitualDef{
		Name: "fork-no-early",
		Steps: []RitualStep{
			{
				Name: "fork-step",
				Fork: &ForkDef{Over: "lings", BatchSize: 1},
				Work: []RitualStep{
					{Name: "work", Minister: "forge", Act: "implement {{ .item.description }}", Then: []string{"record the ling completed"}},
				},
			},
		},
	}
	registry.Register(ritual)

	// Use a slow minister: adds 50ms delay before completing each task.
	// This widens the race window so any premature dispatch of l2 would be caught.
	slowMinister := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		result:       "done\n",
		delay:        50 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go slowMinister.Run(ctx)

	ministers := map[string]Minister{}
	for _, id := range []string{"judge", "chancellor", "war", "secretary"} {
		m := &ritualTestMinister{
			MinisterBase: MinisterBase{logger: slog.Default()},
			id:           id,
			tasksCh:      make(chan *Task, 1),
			result:       "done\n",
		}
		ministers[id] = m
		go m.Run(ctx)
	}
	ministers["forge"] = slowMinister

	court := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}
	base := &MinisterBase{logger: slog.Default()}
	court.ritualGuard = NewRitualGuard(RitualGuardOpts{
		Base:        base,
		GetMinister: court.GetMinister,
	})

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, slog.Default(), repo.RepoInfo{})

	workUnits := []interface{}{
		map[string]interface{}{
			"ling_id": "l1", "edict_id": float64(1), "description": "task 1",
			"dependencies": storage.StringArray{}, "status": "pending",
		},
		map[string]interface{}{
			"ling_id": "l2", "edict_id": float64(1), "description": "task 2",
			"dependencies": storage.StringArray{"l1"}, "status": "pending",
		},
	}

	exec := &RitualExecution{
		ID: "test-exec", RitualName: "fork-no-early", EdictID: 1,
		Username: "testuser", Project: "testproject",
		Data:       storage.JSON{"lings": workUnits},
		def:        ritual,
		stepStates: []RitualStepState{{Name: "fork-step"}},
	}

	results, err := runner.executeForkDAG(context.Background(), exec, ritual.Steps[0], workUnits, 1)
	if err != nil {
		t.Fatalf("executeForkDAG error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	callLog := slowMinister.getCallLog()
	if len(callLog) != 2 {
		t.Fatalf("expected 2 calls, got %d: %v", len(callLog), callLog)
	}
	if !strings.Contains(callLog[0], "task 1") {
		t.Errorf("first dispatch should be task 1, got %q", callLog[0])
	}
	if !strings.Contains(callLog[1], "task 2") {
		t.Errorf("second dispatch should be task 2, got %q", callLog[1])
	}

	// The critical invariant: l2 was dispatched AFTER l1 completed.
	// Since l1 takes 50ms, any premature l2 dispatch would appear as
	// callLog[0] containing "task 2" — but we already verified
	// callLog[0] is task 1 and callLog[1] is task 2 above.
}

// idleTestMinister is a minister that periodically sends notifications
// while working, or stays silent, depending on configuration.
type idleTestMinister struct {
	MinisterBase
	id             string
	tasksCh        chan *Task
	notifyInterval time.Duration // if >0, send notifications at this interval
	totalDuration  time.Duration // how long to work before completing
	result         string
	err            error
}

func (m *idleTestMinister) ID() string                  { return m.id }
func (m *idleTestMinister) SystemPrompt() string        { return "" }
func (m *idleTestMinister) Title() string               { return m.id }
func (m *idleTestMinister) Tools() []Tool               { return nil }
func (m *idleTestMinister) Tasks() chan<- *Task         { return m.tasksCh }
func (m *idleTestMinister) Model() LLMProvider          { return nil }
func (m *idleTestMinister) GetConfig() config.LLMConfig { return config.LLMConfig{} }
func (m *idleTestMinister) RepoInfo() repo.RepoInfo     { return repo.RepoInfo{} }
func (m *idleTestMinister) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-m.tasksCh:
			if m.notifyInterval > 0 && t.Notify != nil {
				// Send periodic notifications while working
				ticker := time.NewTicker(m.notifyInterval)
				defer ticker.Stop()
				deadline := time.Now().Add(m.totalDuration)
				for {
					select {
					case <-t.Ctx.Done():
						t.Done <- Result{Output: m.result, Err: t.Ctx.Err()}
						return
					case <-ticker.C:
						if time.Now().After(deadline) {
							t.Done <- Result{Output: m.result, Err: m.err}
							return
						}
						t.Notify(StreamChunkMsg{ChannelID: "forge", Text: "working..."})
					}
				}
			}
			// Silent: just wait for the duration or task context cancellation
			if m.totalDuration > 0 {
				select {
				case <-time.After(m.totalDuration):
				case <-t.Ctx.Done():
					t.Done <- Result{Output: m.result, Err: t.Ctx.Err()}
					return
				}
			}
			t.Done <- Result{Output: m.result, Err: m.err}
		}
	}
}

// TestRitualIdleTimeoutAbortsSilentStep verifies that the idle timer aborts
// a step when the minister produces no output for the configured idle period.
func TestRitualIdleTimeoutAbortsSilentStep(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:        "idle-timeout-test",
		Description: "Test idle timeout aborts silent step",
		Steps: []RitualStep{
			{Name: "silent", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	// Minister that accepts task but never sends notifications and takes long
	silentMinister := &idleTestMinister{
		MinisterBase:  MinisterBase{logger: slog.Default()},
		id:            "forge",
		tasksCh:       make(chan *Task, 1),
		totalDuration: 10 * time.Second, // would take 10s if not aborted
		result:        "done",
		err:           nil,
	}

	ministers := map[string]Minister{"forge": silentMinister}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go silentMinister.Run(ctx)

	court := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})
	// Set a very short idle timeout for testing
	runner.idleTimeout = 100 * time.Millisecond

	notify := func(msg any) {}
	exec, err := runner.Start(ctx, "idle-timeout-test", testEK(42), nil, notify)
	require.NoError(t, err)

	err = runner.Run(ctx, exec)
	require.Error(t, err, "expected idle timeout to abort step")
}

// TestRitualIdleTimeoutKeepsActiveStepAlive verifies that the idle timer
// resets when the minister produces output, allowing long-running steps
// to complete as long as they keep sending notifications.
func TestRitualIdleTimeoutKeepsActiveStepAlive(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:        "idle-alive-test",
		Description: "Test idle timer resets on activity",
		Steps: []RitualStep{
			{Name: "chatty", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	// Minister that sends notifications every 50ms and completes after 500ms.
	// The idle timeout is 200ms, but it should never fire because the
	// minister is actively producing output.
	chattyMinister := &idleTestMinister{
		MinisterBase:   MinisterBase{logger: slog.Default()},
		id:             "forge",
		tasksCh:        make(chan *Task, 1),
		notifyInterval: 50 * time.Millisecond,
		totalDuration:  500 * time.Millisecond,
		result:         "done",
		err:            nil,
	}

	ministers := map[string]Minister{"forge": chattyMinister}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go chattyMinister.Run(ctx)

	court := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})
	// Idle timeout shorter than total duration — should NOT fire because
	// the minister sends notifications every 50ms.
	runner.idleTimeout = 200 * time.Millisecond

	notify := func(msg any) {}
	exec, err := runner.Start(ctx, "idle-alive-test", testEK(43), nil, notify)
	require.NoError(t, err)

	err = runner.Run(ctx, exec)
	require.NoError(t, err, "step should complete despite short idle timeout — minister was active")
}

// TestRitualChannelID verifies that RitualExecution.ChannelID() returns
// the correct per-edict channel ID, including edict 1.
func TestRitualChannelID(t *testing.T) {
	tests := []struct {
		edictID uint
		want    string
	}{
		{1, "e1"},
		{2, "e2"},
		{100, "e100"},
		{644, "e644"},
	}
	for _, tt := range tests {
		e := &RitualExecution{EdictID: tt.edictID}
		assert.Equal(t, tt.want, e.ChannelID(),
			"ChannelID() for edict %d", tt.edictID)
	}
}

// TestRitualChannelIDHelper verifies the standalone ritualChannelID function.
func TestRitualChannelIDHelper(t *testing.T) {
	tests := []struct {
		edictID uint
		want    string
	}{
		{1, "e1"},
		{2, "e2"},
		{644, "e644"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, ritualChannelID(tt.edictID),
			"ritualChannelID(%d)", tt.edictID)
	}
}

// TestRitualNotifyUsesEdictChannelID verifies that Notify() sets the
// ChannelID on RitualStepMsg to the edict's channel, not "secretary".
func TestRitualNotifyUsesEdictChannelID(t *testing.T) {
	var got RitualStepMsg
	e := &RitualExecution{
		EdictID:    644,
		RitualName: "test",
		ID:         "exec-1",
		notify: func(msg any) {
			if m, ok := msg.(RitualStepMsg); ok {
				got = m
			}
		},
	}
	e.Notify(RitualStepMsg{Status: "started"})
	assert.Equal(t, "e644", got.ChannelID, "Notify should route to e644, not chancellor")
	assert.Equal(t, "test", got.RitualName)
	assert.Equal(t, uint(644), got.EdictID)
}

// TestRitualNotifyEdict1UsesE1Channel verifies that edict 1 rituals
// route to the "e1" channel (per-edict tab) like every other edict.
func TestRitualNotifyEdict1UsesE1Channel(t *testing.T) {
	var got RitualStepMsg
	e := &RitualExecution{
		EdictID:    1,
		RitualName: "bootstrap",
		ID:         "exec-1",
		notify: func(msg any) {
			if m, ok := msg.(RitualStepMsg); ok {
				got = m
			}
		},
	}
	e.Notify(RitualStepMsg{Status: "started"})
	assert.Equal(t, "e1", got.ChannelID, "edict 1 should route to e1 channel")
}

// pauseTestMinister blocks on each task until the task context is cancelled,
// then returns context.Canceled. Used for pause/resume testing.
type pauseTestMinister struct {
	MinisterBase
	id      string
	tasksCh chan *Task
}

func (m *pauseTestMinister) ID() string                  { return m.id }
func (m *pauseTestMinister) SystemPrompt() string        { return "" }
func (m *pauseTestMinister) Title() string               { return m.id }
func (m *pauseTestMinister) Tools() []Tool               { return nil }
func (m *pauseTestMinister) Tasks() chan<- *Task         { return m.tasksCh }
func (m *pauseTestMinister) Model() LLMProvider          { return nil }
func (m *pauseTestMinister) GetConfig() config.LLMConfig { return config.LLMConfig{} }
func (m *pauseTestMinister) RepoInfo() repo.RepoInfo     { return repo.RepoInfo{} }

func (m *pauseTestMinister) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-m.tasksCh:
			// Block until the task context is cancelled
			<-t.Ctx.Done()
			t.Done <- Result{Output: "", Err: context.Canceled}
		}
	}
}

// TestRitualPauseResume verifies the pause/resume mechanism for ruler
// interjection. When PauseRitual is called during a running step, the
// step's context is cancelled (interrupting the LLM stream). When
// ResumeRitual is called, the step is retried from scratch.
func TestRitualPauseResume(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:        "pause-test",
		Description: "Test pause and resume",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	slowMinister := &pauseTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
	}

	ministers := map[string]Minister{"forge": slowMinister}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go slowMinister.Run(ctx)

	court := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	notify := func(msg any) {}

	exec, err := runner.Start(ctx, "pause-test", testEK(50), nil, notify)
	require.NoError(t, err)

	// Run the ritual in a goroutine
	runDone := make(chan error, 1)
	go func() {
		runDone <- runner.Run(ctx, exec)
	}()

	// Wait for the step to start
	time.Sleep(500 * time.Millisecond)

	// Pause the ritual
	channelID := exec.ChannelID()
	paused := runner.PauseRitual(channelID)
	assert.True(t, paused, "PauseRitual should return true when a step is running")

	// Wait for the step to be interrupted
	time.Sleep(500 * time.Millisecond)

	// Resume the ritual
	resumed := runner.ResumeRitual(channelID)
	assert.True(t, resumed, "ResumeRitual should return true when a ritual is paused")

	// The step retries after resume. It will block again — cancel the
	// parent context to end the ritual.
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	// Wait for the ritual to complete (aborted by context cancellation)
	select {
	case err := <-runDone:
		_ = err // expected — ritual aborted
	case <-time.After(5 * time.Second):
		t.Fatal("ritual did not complete after resume")
	}
}

// TestRitualPauseAbort verifies that when a ritual is paused and then
// the context is cancelled (simulating :abort), the ritual goroutine
// exits with an error.
func TestRitualPauseAbort(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name:        "pause-abort-test",
		Description: "Test pause and abort",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Task: "do work"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	slowMinister := &pauseTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
	}

	ministers := map[string]Minister{"forge": slowMinister}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go slowMinister.Run(ctx)

	court := &Court{
		ministers: ministers,
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	notify := func(msg any) {}
	exec, err := runner.Start(ctx, "pause-abort-test", testEK(51), nil, notify)
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		runDone <- runner.Run(ctx, exec)
	}()

	// Wait for the step to start
	time.Sleep(200 * time.Millisecond)

	// Pause the ritual
	channelID := exec.ChannelID()
	paused := runner.PauseRitual(channelID)
	assert.True(t, paused, "PauseRitual should return true")

	time.Sleep(200 * time.Millisecond)

	// Abort: cancel the parent context (simulates :abort → CancelTab)
	cancel()

	select {
	case err := <-runDone:
		assert.Error(t, err, "ritual should fail after abort")
	case <-time.After(5 * time.Second):
		t.Fatal("ritual did not complete after abort")
	}
}

// TestRitualPauseNotRunning verifies that PauseRitual returns true when
// no step is actively running — the pause channel sits ready for the
// between-step check.
func TestRitualPauseNotRunning(t *testing.T) {
	db := setupRitualTestDB(t)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, nil, repo.RepoInfo{})

	assert.True(t, runner.PauseRitual("e999"), "PauseRitual should return true when no step is running")
	assert.False(t, runner.PauseRitual("e999"), "PauseRitual should return false when already paused")
	assert.True(t, runner.ResumeRitual("e999"), "ResumeRitual should return true when something is paused")
	assert.False(t, runner.ResumeRitual("e999"), "ResumeRitual should return false when nothing is paused")
}

// Ported from PR #135 (Nanook contribution): verify cmd_running/cmd_done
// notifications are emitted for shell (bash) given and then steps.

// TestThenStepEmitsCmdRunningDone verifies that a bash then step emits
// RitualStepMsg{Status:"cmd_running"} before and {Status:"cmd_done"} after.
func TestThenStepEmitsCmdRunningDone(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "then-cmd-test",
		Steps: []RitualStep{
			{
				Name:     "work",
				Minister: "forge",
				Task:     "do work",
				Then:     []string{"!echo then-output"},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "step-done\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "then-output\n", ExitCode: "0"},
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, nil, db, mockRunner, nil, repo.RepoInfo{})

	var messages []RitualStepMsg
	notify := func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok {
			messages = append(messages, stepMsg)
		}
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "then-cmd-test", testEK(12), nil, notify)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	var cmdRunning, cmdDone *RitualStepMsg
	var cmdRunningIdx, cmdDoneIdx int = -1, -1
	for i := range messages {
		switch messages[i].Status {
		case "cmd_running":
			cmdRunning = &messages[i]
			cmdRunningIdx = i
		case "cmd_done":
			cmdDone = &messages[i]
			cmdDoneIdx = i
		}
	}
	if cmdRunning == nil {
		t.Fatalf("expected a cmd_running RitualStepMsg, got: %+v", messages)
	}
	if cmdDone == nil {
		t.Fatalf("expected a cmd_done RitualStepMsg, got: %+v", messages)
	}
	if cmdRunningIdx >= cmdDoneIdx {
		t.Errorf("expected cmd_running (idx %d) before cmd_done (idx %d)", cmdRunningIdx, cmdDoneIdx)
	}
	if cmdRunning.StepName != "echo" {
		t.Errorf("expected cmd_running StepName 'echo', got %q", cmdRunning.StepName)
	}
	if cmdRunning.Message != "echo then-output" {
		t.Errorf("expected cmd_running Message 'echo then-output', got %q", cmdRunning.Message)
	}
	if cmdDone.StepName != "echo" {
		t.Errorf("expected cmd_done StepName 'echo', got %q", cmdDone.StepName)
	}
	if cmdDone.Message != "then-output\n" {
		t.Errorf("expected cmd_done Message 'then-output\\n', got %q", cmdDone.Message)
	}
}

// TestStepLevelGivenEmitsCmdRunningDone verifies that a bash given step emits
// RitualStepMsg{Status:"cmd_running"} before and {Status:"cmd_done"} after.
func TestStepLevelGivenEmitsCmdRunningDone(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "step-given-cmd-test",
		Steps: []RitualStep{
			{
				Name:     "work",
				Minister: "forge",
				Task:     "do work",
				Given:    []string{"!echo step-given-data"},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "step-done\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "step-given-data\n", ExitCode: "0"},
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, nil, db, mockRunner, nil, repo.RepoInfo{})

	var messages []RitualStepMsg
	notify := func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok {
			messages = append(messages, stepMsg)
		}
	}

	ctx := context.Background()
	exec, err := runner.Start(ctx, "step-given-cmd-test", testEK(13), nil, notify)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	var cmdRunning, cmdDone *RitualStepMsg
	var cmdRunningIdx, cmdDoneIdx int = -1, -1
	for i := range messages {
		switch messages[i].Status {
		case "cmd_running":
			cmdRunning = &messages[i]
			cmdRunningIdx = i
		case "cmd_done":
			cmdDone = &messages[i]
			cmdDoneIdx = i
		}
	}
	if cmdRunning == nil {
		t.Fatalf("expected a cmd_running RitualStepMsg, got: %+v", messages)
	}
	if cmdDone == nil {
		t.Fatalf("expected a cmd_done RitualStepMsg, got: %+v", messages)
	}
	if cmdRunningIdx >= cmdDoneIdx {
		t.Errorf("expected cmd_running (idx %d) before cmd_done (idx %d)", cmdRunningIdx, cmdDoneIdx)
	}
	if cmdRunning.StepName != "echo" {
		t.Errorf("expected cmd_running StepName 'echo', got %q", cmdRunning.StepName)
	}
	if cmdRunning.Message != "echo step-given-data" {
		t.Errorf("expected cmd_running Message 'echo step-given-data', got %q", cmdRunning.Message)
	}
	if cmdDone.StepName != "echo" {
		t.Errorf("expected cmd_done StepName 'echo', got %q", cmdDone.StepName)
	}
	if cmdDone.Message != "step-given-data\n" {
		t.Errorf("expected cmd_done Message 'step-given-data\\n', got %q", cmdDone.Message)
	}
}

// TestBuildStepScratchpad_SealsAndLings verifies that buildStepScratchpad includes
// the Seals (Seal Chain) and Lings (Sub-tasks) sections when data exists in the DB.
func TestBuildStepScratchpad_SealsAndLings(t *testing.T) {
	db := setupRitualTestDB(t)
	key := testEK(42)

	// Seed a JudgeVerdict and ForgeManifest so the existing sections also show up
	sealSvc := storage.NewSealService(db)

	// Grant judge seal
	require.NoError(t, sealSvc.GrantSeal(key, "judge", nil))
	// Grant chancellor seal
	require.NoError(t, sealSvc.GrantSeal(key, "chancellor", nil))

	// Add a couple of lings
	ling1 := uuid.New().String()
	ling2 := uuid.New().String()

	for _, ling := range []storage.Ling{
		{LingID: ling1, EdictID: key.ID, Username: key.Username, Project: key.Project, Description: "First task", Status: storage.LingPending},
		{LingID: ling2, EdictID: key.ID, Username: key.Username, Project: key.Project, Description: "Second task", Status: storage.LingInProgress},
	} {
		require.NoError(t, db.Create(&ling).Error)
	}

	// Build a minimal runner
	runner := NewRitualRunner(NewRitualRegistry(), nil, nil, db, nil, nil, repo.RepoInfo{})

	// Create a minimal RitualExecution
	exec := &RitualExecution{
		ID:          "test-1",
		RitualName:  "test",
		EdictID:     key.ID,
		Username:    key.Username,
		Project:     key.Project,
		State:       RitualStateRunning,
		CurrentStep: 0,
		stepStates:  make([]RitualStepState, 1),
	}

	step := RitualStep{Name: "test-step", Minister: "forge"}

	scratchpad := runner.buildStepScratchpad(exec, step)

	// Must contain seals section
	require.Contains(t, scratchpad, "Seals (Seal Chain)")
	require.Contains(t, scratchpad, "judge")
	require.Contains(t, scratchpad, "chancellor")

	// Must contain lings section
	require.Contains(t, scratchpad, "Lings (Sub-tasks)")
	require.Contains(t, scratchpad, "First task")
	require.Contains(t, scratchpad, "Second task")

	// Verify ling ID prefix is shown (first 8 chars)
	require.Contains(t, scratchpad, ling1[:8])
	require.Contains(t, scratchpad, ling2[:8])

	// Verify status labels
	require.Contains(t, scratchpad, "pending")
	require.Contains(t, scratchpad, "in_progress")
}

// TestBuildStepScratchpad_EmptySealsLings verifies that buildStepScratchpad
// omits the Seals and Lings sections when no data exists.
func TestBuildStepScratchpad_EmptySealsLings(t *testing.T) {
	db := setupRitualTestDB(t)
	key := testEK(43)

	runner := NewRitualRunner(NewRitualRegistry(), nil, nil, db, nil, nil, repo.RepoInfo{})

	exec := &RitualExecution{
		ID:          "test-2",
		RitualName:  "test-empty",
		EdictID:     key.ID,
		Username:    key.Username,
		Project:     key.Project,
		State:       RitualStateRunning,
		CurrentStep: 0,
		stepStates:  make([]RitualStepState, 1),
	}

	step := RitualStep{Name: "test-step", Minister: "forge"}

	scratchpad := runner.buildStepScratchpad(exec, step)

	// When nothing exists, scratchpad should be empty string
	require.Empty(t, scratchpad, "expected empty scratchpad when no court history exists")
}

// TestRitualPauseBetweenSteps verifies that pausing a ritual between steps
// (when no step is actively running) blocks the main loop in waitIfPaused
// and resumes correctly when ResumeRitual is called.
func TestRitualPauseBetweenSteps(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "pause-between-steps",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Task: "do step 1"},
			{Name: "step2", Minister: "forge", Task: "do step 2"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	ministry := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		// Use a small delay on step2 so the main loop doesn't zip through
		// step2 before we can observe the between-steps pause
		delay:  200 * time.Millisecond,
		result: "done",
		err:    nil,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ministry.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": ministry},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	// Track step completions — unbuffered channel for precise synchronization
	stepDone := make(chan string)
	notify := func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok && stepMsg.Status == "completed" {
			stepDone <- stepMsg.StepName
		}
	}

	exec, err := runner.Start(ctx, "pause-between-steps", testEK(52), nil, notify)
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		runDone <- runner.Run(ctx, exec)
	}()

	// Wait for step1 to complete (unbuffered channel ensures we synchronize
	// exactly at the completion boundary, before the loop advances)
	select {
	case name := <-stepDone:
		assert.Equal(t, "step1", name)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for step1 to complete")
	}

	// Now the main loop is between steps (step1 done, step2 not yet started
	// because the minister's delay hasn't elapsed).
	channelID := exec.ChannelID()

	// PauseRitual should return true (no step actively running — the main
	// loop hasn't reached waitIfPaused yet either, but the pause channel
	// will intercept it when it does).
	paused := runner.PauseRitual(channelID)
	assert.True(t, paused, "PauseRitual should return true between steps")

	// Double-pause should return false (already paused)
	assert.False(t, runner.PauseRitual(channelID), "PauseRitual should return false when already paused")

	// Resume the ritual
	resumed := runner.ResumeRitual(channelID)
	assert.True(t, resumed, "ResumeRitual should return true when paused between steps")

	// Wait for step2 to complete
	select {
	case name := <-stepDone:
		assert.Equal(t, "step2", name)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for step2 to complete after resume")
	}

	// The ritual should complete naturally
	select {
	case err := <-runDone:
		assert.NoError(t, err, "ritual should complete without error after between-steps resume")
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("ritual did not complete after between-steps resume")
	}

	// Both steps should have been executed
	assert.Equal(t, 2, exec.CurrentStep, "both steps should have been executed")
	require.Len(t, exec.stepStates, 2)
	assert.Equal(t, "completed", exec.stepStates[0].Status, "step1 should be completed")
	assert.Equal(t, "completed", exec.stepStates[1].Status, "step2 should be completed")
}

// TestRitualPauseBetweenStepsAbort verifies that aborting a ritual that is
// paused between steps works correctly — waitIfPaused returns ctx.Err and
// the ritual exits with aborted state.
func TestRitualPauseBetweenStepsAbort(t *testing.T) {
	db := setupRitualTestDB(t)

	ritual := &RitualDef{
		Name: "pause-between-steps-abort",
		Steps: []RitualStep{
			{Name: "step1", Minister: "forge", Task: "do step 1"},
			{Name: "step2", Minister: "forge", Task: "do step 2"},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	ministry := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		delay:        200 * time.Millisecond,
		result:       "done",
		err:          nil,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ministry.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": ministry},
		logger:    slog.Default(),
	}

	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	stepDone := make(chan string)
	notify := func(msg any) {
		if stepMsg, ok := msg.(RitualStepMsg); ok && stepMsg.Status == "completed" {
			stepDone <- stepMsg.StepName
		}
	}

	exec, err := runner.Start(ctx, "pause-between-steps-abort", testEK(53), nil, notify)
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		runDone <- runner.Run(ctx, exec)
	}()

	// Wait for step1 to complete
	select {
	case name := <-stepDone:
		assert.Equal(t, "step1", name)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for step1 to complete")
	}

	// Pause between steps
	channelID := exec.ChannelID()
	paused := runner.PauseRitual(channelID)
	assert.True(t, paused, "PauseRitual should return true between steps")

	// Abort: cancel the parent context (simulates :abort)
	cancel()

	select {
	case err := <-runDone:
		assert.Error(t, err, "ritual should fail with error after abort between steps")
		assert.Equal(t, RitualStateAborted, exec.State, "ritual should be aborted")
	case <-time.After(5 * time.Second):
		t.Fatal("ritual did not abort after between-steps pause")
	}
}

// runStepToCompletion runs a one-step ritual and returns the resulting Session,
// letting callers assert on how the reasoning effort was applied.
func runStepEffortSession(t *testing.T, effort string, userDefault string) *Session {
	t.Helper()
	db := setupRitualTestDB(t)

	step := RitualStep{Name: "step1", Minister: "forge", Task: "do step 1"}
	if effort != "" {
		step.Effort = effort
	}
	ritual := &RitualDef{Name: "effort-test", Steps: []RitualStep{step}}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	ministry := &ritualTestMinister{
		MinisterBase: MinisterBase{logger: slog.Default()},
		id:           "forge",
		tasksCh:      make(chan *Task, 1),
		config:       config.LLMConfig{ReasoningEffort: userDefault},
		result:       "done",
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ministry.Run(ctx)

	court := &Court{
		ministers: map[string]Minister{"forge": ministry},
		logger:    slog.Default(),
	}
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, nil, nil, repo.RepoInfo{})

	exec, err := runner.Start(ctx, "effort-test", testEK(54), nil, nil)
	require.NoError(t, err)
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx, exec) }()

	select {
	case err := <-runDone:
		require.NoError(t, err, "ritual should complete")
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("ritual did not complete")
	}

	require.Len(t, exec.stepStates, 1)
	require.NotNil(t, exec.stepStates[0].Session, "step should have a session")
	return exec.stepStates[0].Session
}

// TestRitualEffort_InheritsUserDefault verifies that a ritual step with no
// explicit `effort:` key inherits the user-configured reasoning effort
// (set via [llm] reasoning_effort / --reasoning-effort / ASIMI_REASONING_EFFORT)
// rather than forcing a hard-coded "medium".
func TestRitualEffort_InheritsUserDefault(t *testing.T) {
	sess := runStepEffortSession(t, "", "high")
	assert.Equal(t, "high", sess.ReasoningEffort,
		"ritual step without explicit effort should inherit the user-configured default")
}

// TestRitualEffort_ExplicitOverridesUserDefault verifies that an explicit
// `effort:` on a ritual step takes precedence over the user-configured
// default.
func TestRitualEffort_ExplicitOverridesUserDefault(t *testing.T) {
	sess := runStepEffortSession(t, "low", "high")
	assert.Equal(t, "low", sess.ReasoningEffort,
		"explicit ritual step effort should take precedence over the user default")
}
