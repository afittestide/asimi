package court

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStepDefRegistry(t *testing.T) {
	reg := NewStepDefRegistry()

	// Test matching built-in patterns
	tests := []struct {
		text    string
		wantKey string
		wantNil bool
	}{
		{"the edict details", "edict", false},
		{"the court status", "court_status", false},
		{"the manifests", "manifests", false},
		{"the verdicts", "verdicts", false},
		{"the precedents", "precedents", false},
		{"the edict lings", "lings", false},
		{"something unknown", "", true},
	}

	for _, tt := range tests {
		def, err := reg.Match(tt.text)
		if err != nil {
			t.Fatalf("Match(%q) error: %v", tt.text, err)
		}
		if tt.wantNil {
			if def != nil {
				t.Errorf("Match(%q) expected nil, got key=%q", tt.text, def.OutputKey)
			}
		} else {
			if def == nil {
				t.Errorf("Match(%q) expected key=%q, got nil", tt.text, tt.wantKey)
			} else if def.OutputKey != tt.wantKey {
				t.Errorf("Match(%q) expected key=%q, got %q", tt.text, tt.wantKey, def.OutputKey)
			}
		}
	}
}

func TestResolveStepDef(t *testing.T) {
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, nil, repo.RepoInfo{})

	// Test bash command resolution
	entry, err := runner.resolveStepDef("!just test")
	if err != nil {
		t.Fatalf("resolveStepDef('!just test') error: %v", err)
	}
	if entry.Kind != StepDefBash {
		t.Errorf("expected StepDefBash, got %d", entry.Kind)
	}
	if entry.Command != "just test" {
		t.Errorf("expected command 'just test', got %q", entry.Command)
	}

	// Test bash command with `# variable_name` suffix
	entry, err = runner.resolveStepDef("!git log -1 --format='%H %s'  # head_commit")
	if err != nil {
		t.Fatalf("resolveStepDef with comment error: %v", err)
	}
	if entry.Kind != StepDefBash {
		t.Errorf("expected StepDefBash, got %d", entry.Kind)
	}
	if entry.Key != "head_commit" {
		t.Errorf("expected key 'head_commit', got %q", entry.Key)
	}
	if entry.Command != "git log -1 --format='%H %s'" {
		t.Errorf("expected command without comment, got %q", entry.Command)
	}

	// Test that # inside arguments is not treated as a comment
	entry, err = runner.resolveStepDef("!echo 'a#b'")
	if err != nil {
		t.Fatalf("resolveStepDef('!echo a#b') error: %v", err)
	}
	if entry.Key != "echo" {
		t.Errorf("expected key 'echo', got %q", entry.Key)
	}
	if entry.Command != "echo 'a#b'" {
		t.Errorf("expected command unchanged, got %q", entry.Command)
	}

	// Test bash command without comment still works (key = first word)
	entry, err = runner.resolveStepDef("!just test")
	if err != nil {
		t.Fatalf("resolveStepDef('!just test') error: %v", err)
	}
	if entry.Key != "just" {
		t.Errorf("expected key 'just', got %q", entry.Key)
	}

	// Test builtin resolution
	entry, err = runner.resolveStepDef("the edict details")
	if err != nil {
		t.Fatalf("resolveStepDef('the edict details') error: %v", err)
	}
	if entry.Kind != StepDefBuiltin {
		t.Errorf("expected StepDefBuiltin, got %d", entry.Kind)
	}
	if entry.Command != "get_edict" {
		t.Errorf("expected handler 'get_edict', got %q", entry.Command)
	}
	if entry.Key != "edict" {
		t.Errorf("expected key 'edict', got %q", entry.Key)
	}

	// Test unknown pattern
	_, err = runner.resolveStepDef("something that does not match")
	if err == nil {
		t.Error("expected error for unknown pattern")
	}
}

func TestRunGivenStep_Bash(t *testing.T) {
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()
	mockRunner := &mockCmdRunner{output: "diff output\n", exitCode: "0"}
	runner := NewRitualRunner(registry, nil, nil, db, mockRunner, nil, repo.RepoInfo{})

	exec := &RitualExecution{
		ID:         "test-exec",
		RitualName: "test",
		EdictID:    100,
	}

	entry := StepDefEntry{
		Kind:    StepDefBash,
		Key:     "git",
		Command: "git diff HEAD",
	}

	result, err := runner.runGivenStep(context.Background(), exec, entry)
	if err != nil {
		t.Fatalf("runGivenStep error: %v", err)
	}
	if result != "diff output\n" {
		t.Errorf("expected 'diff output\\n', got %q", result)
	}
}

func TestRunGivenStep_BashFailure(t *testing.T) {
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()
	failRunner := &mockCmdRunner{output: "FAIL\n", exitCode: "1"}
	runner := NewRitualRunner(registry, nil, nil, db, failRunner, nil, repo.RepoInfo{})

	exec := &RitualExecution{
		ID:         "test-exec",
		RitualName: "test",
		EdictID:    100,
	}

	entry := StepDefEntry{
		Kind:    StepDefBash,
		Key:     "test",
		Command: "exit 1",
	}

	_, err := runner.runGivenStep(context.Background(), exec, entry)
	if err == nil {
		t.Fatal("expected error for failing bash given step")
	}
	if !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("expected exit code in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "FAIL") {
		t.Errorf("expected output in error, got: %v", err)
	}
	// Verify storeGivenResult + exec.Data["error"] are populated on failure
	if exec.Data == nil {
		t.Fatal("exec.Data should not be nil after failure")
	}
	if exec.Data["error"] != "FAIL\n" {
		t.Errorf("expected exec.Data[\"error\"] to contain full output, got %q", exec.Data["error"])
	}
	if exec.Data["test"] != "FAIL\n" {
		t.Errorf("expected exec.Data[\"test\"] to contain full output (via storeGivenResult), got %q", exec.Data["test"])
	}
}

func TestRunThenStep_Bash(t *testing.T) {
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()

	// Success case
	mockRunner := &mockCmdRunner{output: "ok\n", exitCode: "0"}
	runner := NewRitualRunner(registry, nil, nil, db, mockRunner, nil, repo.RepoInfo{})

	exec := &RitualExecution{
		ID:         "test-exec",
		RitualName: "test",
		EdictID:    100,
	}

	entry := StepDefEntry{
		Kind:    StepDefBash,
		Key:     "just",
		Command: "just test",
	}

	err := runner.runThenStep(context.Background(), exec, entry)
	if err != nil {
		t.Fatalf("runThenStep success case: %v", err)
	}

	// Failure case
	failRunner := &mockCmdRunner{output: "FAIL\n", exitCode: "1"}
	runner = NewRitualRunner(registry, nil, nil, db, failRunner, nil, repo.RepoInfo{})

	err = runner.runThenStep(context.Background(), exec, entry)
	if err == nil {
		t.Error("expected error for failing then step")
	}
	if !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("expected exit code in error, got: %v", err)
	}
}

func TestRunThenStep_Multiple(t *testing.T) {
	db := setupRitualTestDB(t)

	// Create ritual with multiple then steps, second one fails
	ritual := &RitualDef{
		Name: "multi-then",
		Steps: []RitualStep{
			{
				Name:     "build",
				Minister: "forge",
				Task:     "build something",
				Then:     []string{"!echo check1", "!exit 1"},
			},
		},
	}

	registry := NewRitualRegistry()
	registry.Register(ritual)

	court := newRitualTestCourt(t, "build\n", nil)
	mockRunner := &mockCallCountRunner{
		results: []runners.Output{
			{Output: "ok\n", ExitCode: "0"},   // first then
			{Output: "FAIL\n", ExitCode: "1"}, // second then
		},
	}
	runner := NewRitualRunner(registry, court.GetMinister, court.PublishEvent, db, mockRunner, nil, repo.RepoInfo{})

	ctx := context.Background()
	exec, err := runner.Start(ctx, "multi-then", testEK(8), nil, nil)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	err = runner.Run(ctx, exec)
	if err == nil {
		t.Error("expected error from failing then step")
	}
	if !strings.Contains(err.Error(), "then") {
		t.Errorf("expected 'then' in error message, got: %v", err)
	}
}

func TestAwaitRulerSeal_StageManifestFiles(t *testing.T) {
	db := setupRitualTestDB(t)

	// Migrate edict and manifest tables
	if err := db.AutoMigrate(&storage.Edict{}, &storage.ForgeManifest{}); err != nil {
		t.Fatalf("failed to migrate edict/manifest tables: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, nil, repo.RepoInfo{})

	// Create test edict
	edict := storage.Edict{
		ID:        1,
		SessionID: "test-session",
		Intent:    "Test manifest staging",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// Create test manifests for 3 files
	manifests := []storage.ForgeManifest{
		{ManifestID: "manifest-1", EdictID: edict.ID, FilePath: "court/ritual.go", Status: storage.ManifestForged},
		{ManifestID: "manifest-2", EdictID: edict.ID, FilePath: "court/builtin_rituals.yaml", Status: storage.ManifestForged},
		{ManifestID: "manifest-3", EdictID: edict.ID, FilePath: "court/ministers.go", Status: storage.ManifestForged},
	}
	for i := range manifests {
		if err := db.Create(&manifests[i]).Error; err != nil {
			t.Fatalf("failed to create manifest %d: %v", i, err)
		}
	}

	// Mock runner to capture git add command
	var stagedFiles string
	mockRunner := &mockCmdRunner{
		output:   "",
		exitCode: "0",
		onRun: func(cmd string) {
			// Extract files from git add command
			if strings.HasPrefix(cmd, "git add ") {
				stagedFiles = strings.TrimPrefix(cmd, "git add ")
			}
		},
	}
	runner = NewRitualRunner(registry, nil, nil, db, mockRunner, nil, repo.RepoInfo{})

	// Create execution
	exec := &RitualExecution{
		ID:         "test-await-ruler-seal",
		RitualName: "swift-strike",
		EdictID:    edict.ID,
	}

	// Call await_ruler_seal handler
	err := runner.runThen(context.Background(), exec, "await_ruler_seal")
	if err != nil {
		t.Fatalf("runThen(await_ruler_seal) error: %v", err)
	}

	// Verify only manifest files were staged
	expectedFiles := "court/ritual.go court/builtin_rituals.yaml court/ministers.go"
	if stagedFiles != expectedFiles {
		t.Errorf("expected staged files %q, got %q", expectedFiles, stagedFiles)
	}
}

func TestAwaitRulerSeal_NoManifests(t *testing.T) {
	db := setupRitualTestDB(t)

	// Migrate edict and manifest tables
	if err := db.AutoMigrate(&storage.Edict{}, &storage.ForgeManifest{}); err != nil {
		t.Fatalf("failed to migrate edict/manifest tables: %v", err)
	}

	registry := NewRitualRegistry()

	// Create test edict with no manifests
	edict := storage.Edict{
		ID:        1,
		SessionID: "test-session",
		Intent:    "Test no manifests",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// Mock runner - should not be called
	mockRunner := &mockCmdRunner{
		output:   "",
		exitCode: "0",
		onRun: func(cmd string) {
			t.Errorf("git add should not be called when no manifests exist, but got: %s", cmd)
		},
	}
	runner := NewRitualRunner(registry, nil, nil, db, mockRunner, nil, repo.RepoInfo{})

	exec := &RitualExecution{
		ID:         "test-await-ruler-seal-empty",
		RitualName: "swift-strike",
		EdictID:    edict.ID,
	}

	// Call await_ruler_seal handler - should not error
	err := runner.runThen(context.Background(), exec, "await_ruler_seal")
	if err != nil {
		t.Fatalf("runThen(await_ruler_seal) error with no manifests: %v", err)
	}
}

func TestAwaitRulerSeal_CommaSeparatedFilePaths(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.Edict{}, &storage.ForgeManifest{}); err != nil {
		t.Fatalf("failed to migrate edict/manifest tables: %v", err)
	}

	registry := NewRitualRegistry()

	edict := storage.Edict{
		ID:        1,
		SessionID: "test-session",
		Intent:    "Test comma-separated paths",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	// Manifests with comma-separated file paths
	manifests := []storage.ForgeManifest{
		{ManifestID: "manifest-1", EdictID: edict.ID, FilePath: "internal/ministers/def.go, internal/ministers/load.go", Status: storage.ManifestForged},
		{ManifestID: "manifest-2", EdictID: edict.ID, FilePath: "internal/ministers/util.go", Status: storage.ManifestForged},
	}
	for i := range manifests {
		if err := db.Create(&manifests[i]).Error; err != nil {
			t.Fatalf("failed to create manifest %d: %v", i, err)
		}
	}

	var stagedFiles string
	mockRunner := &mockCmdRunner{
		output:   "",
		exitCode: "0",
		onRun: func(cmd string) {
			if strings.HasPrefix(cmd, "git add ") {
				stagedFiles = strings.TrimPrefix(cmd, "git add ")
			}
		},
	}
	runner := NewRitualRunner(registry, nil, nil, db, mockRunner, nil, repo.RepoInfo{})

	exec := &RitualExecution{
		ID:         "test-await-ruler-seal-comma",
		RitualName: "swift-strike",
		EdictID:    edict.ID,
	}

	err := runner.runThen(context.Background(), exec, "await_ruler_seal")
	if err != nil {
		t.Fatalf("runThen(await_ruler_seal) error: %v", err)
	}

	expectedFiles := "internal/ministers/def.go internal/ministers/load.go internal/ministers/util.go"
	if stagedFiles != expectedFiles {
		t.Errorf("expected staged files %q, got %q", expectedFiles, stagedFiles)
	}
}

func TestCheckVerdictsPassed_AllApproved(t *testing.T) {
	db := setupRitualTestDB(t)

	// Ensure JudgeVerdict table exists for the verdict check query
	if err := db.AutoMigrate(&storage.JudgeVerdict{}); err != nil {
		t.Fatalf("Failed to migrate JudgeVerdict: %v", err)
	}

	// Create manifests with approved status
	manifest := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "file.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "file.go",
		Status:     storage.ManifestLive,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatalf("Failed to create manifest: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_verdicts_passed")
	if err != nil {
		t.Errorf("Expected no error when all manifests approved, got: %v", err)
	}
}

// TestCheckVerdictsPassed_SomeRejected verifies the handler fails when any manifest is rejected
func TestCheckVerdictsPassed_SomeRejected(t *testing.T) {
	db := setupRitualTestDB(t)

	// Ensure JudgeVerdict table exists for the verdict check query
	if err := db.AutoMigrate(&storage.JudgeVerdict{}); err != nil {
		t.Fatalf("Failed to migrate JudgeVerdict: %v", err)
	}

	// Create one approved and one rejected manifest
	forged := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "good.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "good.go",
		Status:     storage.ManifestLive,
	}
	rejected := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "bad.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "bad.go",
		Status:     storage.ManifestRejected,
	}
	if err := db.Create(&forged).Error; err != nil {
		t.Fatalf("Failed to create forged manifest: %v", err)
	}
	if err := db.Create(&rejected).Error; err != nil {
		t.Fatalf("Failed to create rejected manifest: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_verdicts_passed")
	if err == nil {
		t.Error("Expected error when some manifests are rejected, got nil")
	}
	if !strings.Contains(err.Error(), "verdict check failed") {
		t.Errorf("Expected error to contain 'verdict check failed', got: %v", err)
	}
	if !strings.Contains(err.Error(), "bad.go") {
		t.Errorf("Expected error to mention rejected file 'bad.go', got: %v", err)
	}
}

// TestCheckVerdictsPassed_AllRejected verifies the handler fails when all manifests are rejected
func TestCheckVerdictsPassed_AllRejected(t *testing.T) {
	db := setupRitualTestDB(t)

	// Ensure JudgeVerdict table exists for the verdict check query
	if err := db.AutoMigrate(&storage.JudgeVerdict{}); err != nil {
		t.Fatalf("Failed to migrate JudgeVerdict: %v", err)
	}

	// Create multiple rejected manifests
	manifests := []storage.ForgeManifest{
		{
			ManifestID: GenerateID("manifest", "1", "test", "file1.go"),
			EdictID:    1,
			Username:   "testuser",
			Project:    "testproject",
			FilePath:   "file1.go",
			Status:     storage.ManifestRejected,
		},
		{
			ManifestID: GenerateID("manifest", "1", "test", "file2.go"),
			EdictID:    1,
			Username:   "testuser",
			Project:    "testproject",
			FilePath:   "file2.go",
			Status:     storage.ManifestRejected,
		},
	}
	for _, m := range manifests {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("Failed to create manifest: %v", err)
		}
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_verdicts_passed")
	if err == nil {
		t.Error("Expected error when all manifests are rejected, got nil")
	}
	if !strings.Contains(err.Error(), "2 manifest(s) rejected") {
		t.Errorf("Expected error to mention '2 manifest(s) rejected', got: %v", err)
	}
}

// TestCheckVerdictsPassed_NoManifests verifies the handler passes when no manifests exist
func TestCheckVerdictsPassed_NoManifests(t *testing.T) {
	db := setupRitualTestDB(t)

	// Ensure JudgeVerdict table exists for the verdict check query
	if err := db.AutoMigrate(&storage.JudgeVerdict{}); err != nil {
		t.Fatalf("Failed to migrate JudgeVerdict: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_verdicts_passed")
	if err != nil {
		t.Errorf("Expected no error when no manifests exist, got: %v", err)
	}
}

// TestCheckVerdictsPassed_FailedVerdict verifies that check_verdicts_passed returns an error
// when a JudgeVerdict has outcome='failed', even if the ForgeManifest status is not ManifestRejected.
// These are separate concepts: manifest status vs verdict outcome.
func TestCheckVerdictsPassed_FailedVerdict(t *testing.T) {
	db := setupRitualTestDB(t)

	// Ensure JudgeVerdict table exists
	if err := db.AutoMigrate(&storage.JudgeVerdict{}); err != nil {
		t.Fatalf("Failed to migrate JudgeVerdict: %v", err)
	}

	// Create a ForgeManifest with quenched status (passing manifest)
	manifest := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "file.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "file.go",
		Status:     storage.ManifestQuenched, // Passing manifest status
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatalf("Failed to create manifest: %v", err)
	}

	// Create a JudgeVerdict with failed outcome for the same manifest
	verdict := storage.JudgeVerdict{
		VerdictID:  GenerateID("verdict", "1", "test", "file.go"),
		ManifestID: manifest.ManifestID,
		TestSuite:  "unit",
		Outcome:    storage.VerdictFailed, // Failed verdict outcome
	}
	if err := db.Create(&verdict).Error; err != nil {
		t.Fatalf("Failed to create verdict: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	// Call check_verdicts_passed - should return error for failed verdict
	err := runner.runThen(context.Background(), exec, "check_verdicts_passed")
	if err == nil {
		t.Error("Expected error when verdict outcome is failed, got nil")
	}
	if !strings.Contains(err.Error(), "verdict check failed") {
		t.Errorf("Expected error to contain 'verdict check failed', got: %v", err)
	}
}

// TestCheckPrecedentApproved_AllApproved verifies the handler passes when all precedents are approved
func TestCheckPrecedentApproved_AllApproved(t *testing.T) {
	db := setupRitualTestDB(t)

	// Ensure CensorPrecedent table exists
	if err := db.AutoMigrate(&storage.CensorPrecedent{}); err != nil {
		t.Fatalf("Failed to migrate CensorPrecedent: %v", err)
	}

	// Create a manifest and an approved precedent
	manifest := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "file.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "file.go",
		Status:     storage.ManifestLive,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatalf("Failed to create manifest: %v", err)
	}

	precedent := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "1", "test", "file.go"),
		ManifestID:  manifest.ManifestID,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "style",
		Ruling:      storage.PrecedentApproved,
	}
	if err := db.Create(&precedent).Error; err != nil {
		t.Fatalf("Failed to create precedent: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_precedent_approved")
	if err != nil {
		t.Errorf("Expected no error when all precedents approved, got: %v", err)
	}
}

// TestCheckPrecedentApproved_SomeRejected verifies the handler fails when any precedent is rejected
func TestCheckPrecedentApproved_SomeRejected(t *testing.T) {
	db := setupRitualTestDB(t)

	// Ensure CensorPrecedent table exists
	if err := db.AutoMigrate(&storage.CensorPrecedent{}); err != nil {
		t.Fatalf("Failed to migrate CensorPrecedent: %v", err)
	}

	// Create two manifests — one with approved precedent, one with rejected
	goodManifest := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "good.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "good.go",
		Status:     storage.ManifestLive,
	}
	badManifest := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "bad.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "bad.go",
		Status:     storage.ManifestLive,
	}
	if err := db.Create(&goodManifest).Error; err != nil {
		t.Fatalf("Failed to create good manifest: %v", err)
	}
	if err := db.Create(&badManifest).Error; err != nil {
		t.Fatalf("Failed to create bad manifest: %v", err)
	}

	approvedPrec := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "1", "test", "good.go"),
		ManifestID:  goodManifest.ManifestID,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "style",
		Ruling:      storage.PrecedentApproved,
	}
	rejectedPrec := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "1", "test", "bad.go"),
		ManifestID:  badManifest.ManifestID,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "security",
		Ruling:      storage.PrecedentRejected,
	}
	if err := db.Create(&approvedPrec).Error; err != nil {
		t.Fatalf("Failed to create approved precedent: %v", err)
	}
	if err := db.Create(&rejectedPrec).Error; err != nil {
		t.Fatalf("Failed to create rejected precedent: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_precedent_approved")
	if err == nil {
		t.Error("Expected error when some precedents are rejected, got nil")
	}
	if !strings.Contains(err.Error(), "precedent check failed") {
		t.Errorf("Expected error to contain 'precedent check failed', got: %v", err)
	}
	if !strings.Contains(err.Error(), "1 precedent(s) rejected") {
		t.Errorf("Expected error to mention '1 precedent(s) rejected', got: %v", err)
	}
}

// TestCheckPrecedentApproved_NoPrecedents verifies the handler passes when no precedents exist
func TestCheckPrecedentApproved_NoPrecedents(t *testing.T) {
	db := setupRitualTestDB(t)

	// Ensure CensorPrecedent table exists
	if err := db.AutoMigrate(&storage.CensorPrecedent{}); err != nil {
		t.Fatalf("Failed to migrate CensorPrecedent: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_precedent_approved")
	if err != nil {
		t.Errorf("Expected no error when no precedents exist, got: %v", err)
	}
}

// TestCheckPrecedentApproved_RejectedThenApproved verifies latest-wins: an approved
// precedent after a rejected one should pass.
func TestCheckPrecedentApproved_RejectedThenApproved(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.CensorPrecedent{}); err != nil {
		t.Fatalf("Failed to migrate CensorPrecedent: %v", err)
	}

	manifest := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "file.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "file.go",
		Status:     storage.ManifestLive,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatalf("Failed to create manifest: %v", err)
	}

	// Old rejected precedent
	rejectedPrec := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "1", "test", "file.go"),
		ManifestID:  manifest.ManifestID,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "style",
		Ruling:      storage.PrecedentRejected,
	}
	if err := db.Create(&rejectedPrec).Error; err != nil {
		t.Fatalf("Failed to create rejected precedent: %v", err)
	}

	// Newer approved precedent — manually set created_at to be later
	approvedPrec := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "2", "test", "file.go"),
		ManifestID:  manifest.ManifestID,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "style",
		Ruling:      storage.PrecedentApproved,
	}
	if err := db.Create(&approvedPrec).Error; err != nil {
		t.Fatalf("Failed to create approved precedent: %v", err)
	}
	// Ensure approvedPrec has a later created_at
	approvedPrec.CreatedAt = rejectedPrec.CreatedAt.Add(time.Minute)
	db.Model(&storage.CensorPrecedent{}).Where("precedent_id = ?", approvedPrec.PrecedentID).Update("created_at", approvedPrec.CreatedAt)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_precedent_approved")
	if err != nil {
		t.Errorf("Expected no error when latest precedent is approved, got: %v", err)
	}
}

// TestCheckPrecedentApproved_RejectedIsLatest verifies that when the latest precedent
// is rejected, the check fails even if there's an older approved one.
func TestCheckPrecedentApproved_RejectedIsLatest(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.CensorPrecedent{}); err != nil {
		t.Fatalf("Failed to migrate CensorPrecedent: %v", err)
	}

	manifest := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "file.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "file.go",
		Status:     storage.ManifestLive,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatalf("Failed to create manifest: %v", err)
	}

	// Old approved precedent
	approvedPrec := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "1", "test", "file.go"),
		ManifestID:  manifest.ManifestID,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "style",
		Ruling:      storage.PrecedentApproved,
	}
	if err := db.Create(&approvedPrec).Error; err != nil {
		t.Fatalf("Failed to create approved precedent: %v", err)
	}

	// Newer rejected precedent
	rejectedPrec := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "2", "test", "file.go"),
		ManifestID:  manifest.ManifestID,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "style",
		Ruling:      storage.PrecedentRejected,
	}
	if err := db.Create(&rejectedPrec).Error; err != nil {
		t.Fatalf("Failed to create rejected precedent: %v", err)
	}
	rejectedPrec.CreatedAt = approvedPrec.CreatedAt.Add(time.Minute)
	db.Model(&storage.CensorPrecedent{}).Where("precedent_id = ?", rejectedPrec.PrecedentID).Update("created_at", rejectedPrec.CreatedAt)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_precedent_approved")
	if err == nil {
		t.Error("Expected error when latest precedent is rejected, got nil")
	}
	if !strings.Contains(err.Error(), "precedent check failed") {
		t.Errorf("Expected error to contain 'precedent check failed', got: %v", err)
	}
}

// TestCheckVerdictsPassed_FailedThenPassed verifies latest-wins: a passed
// verdict after a failed one should pass.
func TestCheckVerdictsPassed_FailedThenPassed(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.JudgeVerdict{}); err != nil {
		t.Fatalf("Failed to migrate JudgeVerdict: %v", err)
	}

	manifest := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "file.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "file.go",
		Status:     storage.ManifestQuenched,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatalf("Failed to create manifest: %v", err)
	}

	// Old failed verdict
	failedVerdict := storage.JudgeVerdict{
		VerdictID:  GenerateID("verdict", "1", "test", "file.go"),
		ManifestID: manifest.ManifestID,
		TestSuite:  "unit",
		Outcome:    storage.VerdictFailed,
	}
	if err := db.Create(&failedVerdict).Error; err != nil {
		t.Fatalf("Failed to create failed verdict: %v", err)
	}

	// Newer passed verdict
	passedVerdict := storage.JudgeVerdict{
		VerdictID:  GenerateID("verdict", "2", "test", "file.go"),
		ManifestID: manifest.ManifestID,
		TestSuite:  "unit",
		Outcome:    storage.VerdictPassed,
	}
	if err := db.Create(&passedVerdict).Error; err != nil {
		t.Fatalf("Failed to create passed verdict: %v", err)
	}
	passedVerdict.CreatedAt = failedVerdict.CreatedAt.Add(time.Minute)
	db.Model(&storage.JudgeVerdict{}).Where("verdict_id = ?", passedVerdict.VerdictID).Update("created_at", passedVerdict.CreatedAt)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_verdicts_passed")
	if err != nil {
		t.Errorf("Expected no error when latest verdict is passed, got: %v", err)
	}
}

func TestRitualRunner_RepoInfoStoredAndUsed(t *testing.T) {
	// Verify that the repoInfo passed to NewRitualRunner is stored in r.repoInfo
	// and used by expressions, not re-fetched via GetRepoInfo().
	t.Run("repoInfo is stored from constructor argument", func(t *testing.T) {
		info := repo.RepoInfo{
			ProjectRoot:  "/explicit/daemon/root",
			Branch:       "custom-branch",
			IsWorktree:   true,
			WorktreePath: "worktrees/custom",
			Slug:         "owner/repo",
		}
		registry := NewRitualRegistry()
		runner := NewRitualRunner(registry, nil, nil, nil, nil, nil, info)

		// The runner's repoInfo field should match what was passed in
		assert.Equal(t, info, runner.repoInfo, "RitualRunner should store the repoInfo passed to constructor")
	})

	t.Run("repoInfo with empty ProjectRoot", func(t *testing.T) {
		info := repo.RepoInfo{} // all zero values
		registry := NewRitualRegistry()
		runner := NewRitualRunner(registry, nil, nil, nil, nil, nil, info)

		assert.Equal(t, "", runner.repoInfo.ProjectRoot, "empty ProjectRoot should be stored as-is")
		assert.Equal(t, "", runner.repoInfo.Branch, "empty Branch should be stored as-is")
	})

	t.Run("repoInfo differs from os.Getwd", func(t *testing.T) {
		// In daemon mode, repoInfo.ProjectRoot may differ from the process cwd.
		// The RitualRunner should use the provided repoInfo, not call GetRepoInfo().
		daemonRoot := "/daemon/project/root"
		info := repo.RepoInfo{
			ProjectRoot: daemonRoot,
			Branch:      "main",
		}
		registry := NewRitualRegistry()
		runner := NewRitualRunner(registry, nil, nil, nil, nil, nil, info)

		// Verify the runner stores the daemon root, not os.Getwd()
		assert.Equal(t, daemonRoot, runner.repoInfo.ProjectRoot)
	})

	t.Run("SetConfig updates repoInfo when ProjectRoot is non-empty", func(t *testing.T) {
		// This is the core daemon-mode bug: the RitualRunner is created at
		// daemon startup with empty repoInfo. SetContext/ConfigureModel later
		// supplies the real root, but the runner never sees it unless
		// SetConfig propagates repoInfo.
		registry := NewRitualRegistry()
		runner := NewRitualRunner(registry, nil, nil, nil, nil, nil, repo.RepoInfo{})

		// Before SetConfig: empty ProjectRoot
		assert.Equal(t, "", runner.repoInfo.ProjectRoot)

		// Simulate what ConfigureModel does after the first handshake
		updatedInfo := repo.RepoInfo{ProjectRoot: "/client/project", Branch: "main", Slug: "org/repo"}
		runner.SetConfig(nil, "org/repo", updatedInfo, nil)

		assert.Equal(t, "/client/project", runner.repoInfo.ProjectRoot)
		assert.Equal(t, "main", runner.repoInfo.Branch)
	})

	t.Run("SetConfig does not overwrite repoInfo with empty ProjectRoot", func(t *testing.T) {
		registry := NewRitualRegistry()
		runner := NewRitualRunner(registry, nil, nil, nil, nil, nil, repo.RepoInfo{
			ProjectRoot: "/existing/root",
		})

		// SetConfig with empty repoInfo should preserve the existing root
		runner.SetConfig(nil, "", repo.RepoInfo{}, nil)
		assert.Equal(t, "/existing/root", runner.repoInfo.ProjectRoot, "empty repoInfo should not overwrite existing ProjectRoot")
	})
}

func TestGetInfrastructureTemplates_ResolvesAgainstProjectRoot(t *testing.T) {
	// Create a temp dir to serve as the "project root" — distinct from CWD.
	projectRoot := t.TempDir()

	// Verify CWD is NOT the project root (the split-brain scenario).
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NotEqual(t, projectRoot, cwd, "project root must differ from CWD for a meaningful test")

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, nil, nil, nil, repo.RepoInfo{
		ProjectRoot: projectRoot,
		Slug:        "test/project",
	})

	result, err := runner.getInfrastructureTemplates(context.Background())
	require.NoError(t, err)

	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok, "result should be a map")

	// Verify template_files contains absolute paths under projectRoot
	templateFiles, ok := resultMap["template_files"].([]string)
	require.True(t, ok, "template_files should be []string")
	require.NotEmpty(t, templateFiles, "all files are new, so template_files should not be empty")

	for _, f := range templateFiles {
		assert.True(t, filepath.IsAbs(f), "template_file path should be absolute: %s", f)
		assert.Contains(t, f, projectRoot, "template_file path should be under project root: %s", f)
	}

	// Verify directories are absolute and under projectRoot
	dirs, ok := resultMap["directories"].([]string)
	require.True(t, ok, "directories should be []string")
	for _, d := range dirs {
		assert.True(t, filepath.IsAbs(d), "directory path should be absolute: %s", d)
		assert.Contains(t, d, projectRoot, "directory path should be under project root: %s", d)
	}

	// Verify files were actually written to disk under projectRoot
	expectedFiles := []string{
		filepath.Join(projectRoot, "Justfile"),
		filepath.Join(projectRoot, ".agents/asimi.conf"),
		filepath.Join(projectRoot, ".agents/sandbox/Dockerfile"),
		filepath.Join(projectRoot, ".agents/sandbox/bashrc"),
		filepath.Join(projectRoot, ".agents/sandbox/asimi_runtime.sh"),
	}
	for _, f := range expectedFiles {
		info, err := os.Stat(f)
		require.NoError(t, err, "file should exist at %s", f)
		assert.NotZero(t, info.Size(), "file should not be empty: %s", f)
	}

	// Verify directory was created with os.MkdirAll (not HostRun)
	dirInfo, err := os.Stat(filepath.Join(projectRoot, ".agents", "sandbox"))
	require.NoError(t, err, ".agents/sandbox directory should exist")
	assert.True(t, dirInfo.IsDir(), ".agents/sandbox should be a directory")
}

func TestGetInfrastructureTemplates_PreservesExistingFiles(t *testing.T) {
	projectRoot := t.TempDir()

	// Pre-create a Dockerfile with custom content
	sandboxDir := filepath.Join(projectRoot, ".agents", "sandbox")
	require.NoError(t, os.MkdirAll(sandboxDir, 0o755))

	customDockerfile := "FROM custom-base:latest\n"
	require.NoError(t, os.WriteFile(filepath.Join(sandboxDir, "Dockerfile"), []byte(customDockerfile), 0o644))

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, nil, nil, nil, repo.RepoInfo{
		ProjectRoot: projectRoot,
		Slug:        "test/project",
	})

	result, err := runner.getInfrastructureTemplates(context.Background())
	require.NoError(t, err)

	resultMap := result.(map[string]interface{})
	templateFiles := resultMap["template_files"].([]string)

	// Dockerfile should NOT appear in createdFiles since it already existed
	for _, f := range templateFiles {
		assert.NotContains(t, f, "Dockerfile", "existing Dockerfile should not be overwritten")
	}

	// The pre-existing Dockerfile should be unchanged
	content, err := os.ReadFile(filepath.Join(sandboxDir, "Dockerfile"))
	require.NoError(t, err)
	assert.Equal(t, customDockerfile, string(content), "existing Dockerfile should be preserved")
}

func TestGetInfrastructureTemplates_SlugSubstitution(t *testing.T) {
	projectRoot := t.TempDir()

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, nil, nil, nil, repo.RepoInfo{
		ProjectRoot: projectRoot,
		Slug:        "myorg/myrepo",
	})

	_, err := runner.getInfrastructureTemplates(context.Background())
	require.NoError(t, err)

	// Check Justfile has the slug substituted
	justfileContent, err := os.ReadFile(filepath.Join(projectRoot, "Justfile"))
	require.NoError(t, err)
	assert.Contains(t, string(justfileContent), "PROJECT_NAME := `git")
	// Edict 775: the generated Justfile's PROJECT_NAME must lowercase the
	// org/repo slug (via tr) so build-sandbox tags and the runner's lookup
	// agree on a lowercase image tag.
	assert.Contains(t, string(justfileContent), `tr '[A-Z]' '[a-z]'`)

	// Check asimi.conf has slug-based image name
	confContent, err := os.ReadFile(filepath.Join(projectRoot, ".agents", "asimi.conf"))
	require.NoError(t, err)
	assert.Contains(t, string(confContent), `image_name = "localhost/asimi/sandbox/myorg/myrepo:latest"`)
	assert.Contains(t, string(confContent), `project = "myorg/myrepo"`)
}

func TestGetInfrastructureTemplates_SlugSubstitutionLowercasesImage(t *testing.T) {
	projectRoot := t.TempDir()

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, nil, nil, nil, repo.RepoInfo{
		ProjectRoot: projectRoot,
		Slug:        "myorg/myproject",
	})

	_, err := runner.getInfrastructureTemplates(context.Background())
	require.NoError(t, err)

	// The slug is canonical lowercase at its source, so the runner uses it
	// verbatim for both the image_name and project without any ToLower.
	confContent, err := os.ReadFile(filepath.Join(projectRoot, ".agents", "asimi.conf"))
	require.NoError(t, err)
	assert.Contains(t, string(confContent), `image_name = "localhost/asimi/sandbox/myorg/myproject:latest"`)
	assert.Contains(t, string(confContent), `project = "myorg/myproject"`)
}

func TestGetInfrastructureTemplates_TemplateFilesListSorted(t *testing.T) {
	// Ensure createdFiles list is deterministic (map iteration order is random in Go).
	// This matters because the LLM receives template_files in the prompt.
	projectRoot := t.TempDir()

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, nil, nil, nil, repo.RepoInfo{
		ProjectRoot: projectRoot,
	})

	result, err := runner.getInfrastructureTemplates(context.Background())
	require.NoError(t, err)

	resultMap := result.(map[string]interface{})
	templateFiles := resultMap["template_files"].([]string)

	// Verify sorted for determinism
	sorted := make([]string, len(templateFiles))
	copy(sorted, templateFiles)
	sort.Strings(sorted)
	assert.Equal(t, sorted, templateFiles, "template_files should be sorted for determinism")
}

func TestGetLings(t *testing.T) {
	db := setupRitualTestDB(t)

	// Migrate Ling table
	require.NoError(t, db.AutoMigrate(&storage.Ling{}, &storage.Edict{}))

	// Create edict
	edict := &storage.Edict{SessionID: "test-session", Intent: "Test lings", Username: "testuser", Project: "testproject"}
	require.NoError(t, db.Create(edict).Error)

	// Create lings for the edict
	lings := []storage.Ling{
		{LingID: "ling-1", EdictID: edict.ID, Username: "testuser", Project: "testproject", Description: "Phase 1", Status: storage.LingPending},
		{LingID: "ling-2", EdictID: edict.ID, Username: "testuser", Project: "testproject", Description: "Phase 2", Dependencies: storage.StringArray{"ling-1"}, Status: storage.LingPending},
	}
	for i := range lings {
		require.NoError(t, db.Create(&lings[i]).Error)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	// Call getLings
	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}
	result, err := runner.getLings(key)
	require.NoError(t, err)

	resultList, ok := result.([]map[string]interface{})
	require.True(t, ok, "result should be []map[string]interface{}")
	require.Len(t, resultList, 2)

	// Verify first ling
	assert.Equal(t, "ling-1", resultList[0]["ling_id"])
	assert.Equal(t, "Phase 1", resultList[0]["description"])
	assert.Equal(t, "pending", resultList[0]["status"])

	// Verify second ling has dependencies
	assert.Equal(t, "ling-2", resultList[1]["ling_id"])
	assert.Equal(t, "Phase 2", resultList[1]["description"])
	deps, ok := resultList[1]["dependencies"].(storage.StringArray)
	assert.True(t, ok)
	assert.Contains(t, deps, "ling-1")
}

func TestGetLings_NoLings(t *testing.T) {
	db := setupRitualTestDB(t)
	require.NoError(t, db.AutoMigrate(&storage.Ling{}, &storage.Edict{}))

	edict := &storage.Edict{SessionID: "test-session", Intent: "No lings", Username: "testuser", Project: "testproject"}
	require.NoError(t, db.Create(edict).Error)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	key := storage.EdictKey{ID: edict.ID, Username: "testuser", Project: "testproject"}
	_, err := runner.getLings(key)
	assert.Error(t, err, "should error when no lings found")
	assert.Contains(t, err.Error(), "no lings found")
}

func TestRecordLingCompleted(t *testing.T) {
	db := setupRitualTestDB(t)
	require.NoError(t, db.AutoMigrate(&storage.Ling{}, &storage.Edict{}))

	// Create edict
	edict := &storage.Edict{SessionID: "test-session", Intent: "Test ling completion", Username: "testuser", Project: "testproject"}
	require.NoError(t, db.Create(edict).Error)

	// Create pending ling
	ling := &storage.Ling{
		LingID:   "ling-complete-1",
		EdictID:  edict.ID,
		Username: "testuser",
		Project:  "testproject",
		Status:   storage.LingPending,
	}
	require.NoError(t, db.Create(ling).Error)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	// Create execution with item containing ling_id
	exec := &RitualExecution{
		EdictID:  edict.ID,
		Username: "testuser",
		Project:  "testproject",
		Data: storage.JSON{
			"item": map[string]interface{}{
				"ling_id":     "ling-complete-1",
				"description": "Phase 1 work",
			},
		},
	}

	// Call record_ling_completed handler
	err := runner.runThen(context.Background(), exec, "record_ling_completed")
	require.NoError(t, err)

	// Verify ling status was updated
	var updated storage.Ling
	require.NoError(t, db.First(&updated, "ling_id = ?", "ling-complete-1").Error)
	assert.Equal(t, storage.LingDone, updated.Status)
}

func TestRecordLingCompleted_NoLingID(t *testing.T) {
	db := setupRitualTestDB(t)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	// Create execution without item
	exec := &RitualExecution{
		EdictID: 1,
		Data:    storage.JSON{},
	}

	// Should not error when no item in context
	err := runner.runThen(context.Background(), exec, "record_ling_completed")
	assert.NoError(t, err)
}

func TestRecordLingCompleted_LingNotFound(t *testing.T) {
	db := setupRitualTestDB(t)
	require.NoError(t, db.AutoMigrate(&storage.Ling{}, &storage.Edict{}))

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	// Create execution with item containing a ling_id that doesn't exist in DB
	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
		Data: storage.JSON{
			"item": map[string]interface{}{
				"ling_id":     "nonexistent-ling",
				"description": "Phase 1 work",
			},
		},
	}

	// Should error when ling not found in database
	err := runner.runThen(context.Background(), exec, "record_ling_completed")
	assert.Error(t, err, "should error when ling_id not found in DB")
	assert.Contains(t, err.Error(), "ling not found")
}

func TestRecordLingCompleted_EmptyLingID(t *testing.T) {
	db := setupRitualTestDB(t)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	// Create execution with item containing empty ling_id
	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
		Data: storage.JSON{
			"item": map[string]interface{}{
				"ling_id":     "",
				"description": "Phase 1 work",
			},
		},
	}

	// Should not error when ling_id is empty — skip silently
	err := runner.runThen(context.Background(), exec, "record_ling_completed")
	assert.NoError(t, err, "empty ling_id should be skipped silently")
}

func TestForkWithGiven_LoadsLingsBeforeFork(t *testing.T) {
	db := setupRitualTestDB(t)
	require.NoError(t, db.AutoMigrate(&storage.Ling{}, &storage.Edict{}))

	// Create edict
	edict := &storage.Edict{SessionID: "test-session", Intent: "Fork with lings test", Username: "testuser", Project: "testproject"}
	require.NoError(t, db.Create(edict).Error)

	// Create lings
	require.NoError(t, db.Create(&storage.Ling{
		LingID: "ling-f1", EdictID: edict.ID, Username: "testuser", Project: "testproject",
		Description: "Fix file A", Status: storage.LingPending,
	}).Error)
	require.NoError(t, db.Create(&storage.Ling{
		LingID: "ling-f2", EdictID: edict.ID, Username: "testuser", Project: "testproject",
		Description: "Fix file B", Status: storage.LingPending,
	}).Error)

	ritual := &RitualDef{
		Name: "fork-lings-test",
		Steps: []RitualStep{
			{
				Name:  "implementing",
				Given: []string{"the edict lings"},
				Fork: &ForkDef{
					Over:      "lings",
					BatchSize: 1,
				},
				Work: []RitualStep{
					{Name: "forge-phase", Minister: "forge", Act: "Implement: {{ .item.description }}"},
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

	exec, err := runner.Start(ctx, "fork-lings-test", ek(edict.ID), nil, nil)
	require.NoError(t, err)

	err = runner.Run(ctx, exec)
	require.NoError(t, err)

	// Verify fork results contain both lings
	forkData, ok := exec.Data["fork"].(map[string]interface{})
	require.True(t, ok, "expected fork data in exec.Data")

	out, ok := forkData["out"].([]ForkResult)
	require.True(t, ok, "expected fork out results")
	assert.Len(t, out, 2, "both lings should be processed")
}

// closeTrackingRunner wraps a mockCmdRunner and records whether Close was called.
type closeTrackingRunner struct {
	mockCmdRunner
	closeCalled bool
	closeErr    error
}

func (r *closeTrackingRunner) Close(ctx context.Context) error {
	r.closeCalled = true
	return r.closeErr
}

func TestBuildSandbox_ClosesOldRunner(t *testing.T) {
	projectRoot := t.TempDir()

	// Create a Justfile with a no-op build-sandbox so `just build-sandbox` succeeds.
	justfile := `build-sandbox:
	@echo "ok"`
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "Justfile"), []byte(justfile), 0o644))

	registry := NewRitualRegistry()
	mockRunner := &closeTrackingRunner{}
	runner := NewRitualRunner(registry, nil, nil, nil, mockRunner, slog.Default(), repo.RepoInfo{
		ProjectRoot: projectRoot,
	})

	result, err := runner.buildSandbox(context.Background())
	require.NoError(t, err)

	resultMap := result.(map[string]string)
	assert.Equal(t, "built", resultMap["status"])

	// The old runner must be closed after a successful build so that
	// verifySandboxReady creates a fresh container from the new image.
	assert.True(t, mockRunner.closeCalled, "buildSandbox should call Close on the old runner after successful build")
}

func TestBuildSandbox_ClosesOldRunnerEvenOnCloseError(t *testing.T) {
	projectRoot := t.TempDir()

	justfile := `build-sandbox:
	@echo "ok"`
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "Justfile"), []byte(justfile), 0o644))

	registry := NewRitualRegistry()
	mockRunner := &closeTrackingRunner{
		closeErr: fmt.Errorf("container stop failed"),
	}
	runner := NewRitualRunner(registry, nil, nil, nil, mockRunner, slog.Default(), repo.RepoInfo{
		ProjectRoot: projectRoot,
	})

	result, err := runner.buildSandbox(context.Background())
	require.NoError(t, err, "buildSandbox should not fail when Close returns an error")

	resultMap := result.(map[string]string)
	assert.Equal(t, "built", resultMap["status"])
	assert.True(t, mockRunner.closeCalled, "Close should still be called even if it errors")
}

func TestBuildSandbox_NilRunner(t *testing.T) {
	projectRoot := t.TempDir()

	justfile := `build-sandbox:
	@echo "ok"`
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "Justfile"), []byte(justfile), 0o644))

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, nil, nil, slog.Default(), repo.RepoInfo{
		ProjectRoot: projectRoot,
	})

	// Should not panic when r.runner is nil
	result, err := runner.buildSandbox(context.Background())
	require.NoError(t, err)

	resultMap := result.(map[string]string)
	assert.Equal(t, "built", resultMap["status"])
}

// TestBuildSandbox_IsolatedHost verifies that buildSandbox is a no-op
// when isolatedHost is set.
func TestBuildSandbox_IsolatedHost(t *testing.T) {
	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, nil, nil, slog.Default(), repo.RepoInfo{})
	runner.isolatedHost = true

	result, err := runner.buildSandbox(context.Background())
	require.NoError(t, err)

	resultMap := result.(map[string]string)
	assert.Equal(t, "skipped", resultMap["status"])
}

// TestVerifySandboxUp_IsolatedHost verifies that verifySandboxUp
// succeeds immediately in isolated-host mode.
func TestVerifySandboxUp_IsolatedHost(t *testing.T) {
	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, nil, nil, slog.Default(), repo.RepoInfo{})
	runner.isolatedHost = true

	result, err := runner.verifySandboxUp(context.Background())
	require.NoError(t, err)

	resultMap := result.(map[string]string)
	assert.Equal(t, "ready", resultMap["status"])
}

// TestVerifySandboxReady_IsolatedHost verifies that verifySandboxReady
// succeeds immediately in isolated-host mode.
func TestVerifySandboxReady_IsolatedHost(t *testing.T) {
	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, nil, nil, slog.Default(), repo.RepoInfo{})
	runner.isolatedHost = true

	result, err := runner.verifySandboxReady(context.Background())
	require.NoError(t, err)

	resultMap := result.(map[string]string)
	assert.Equal(t, "ready", resultMap["status"])
}

// TestCheckVerdictsPassed_PassedThenFailed verifies latest-wins: a failed
// verdict after a passed one should fail.
func TestCheckVerdictsPassed_EdictLevelFailed(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.JudgeVerdict{}); err != nil {
		t.Fatalf("Failed to migrate JudgeVerdict: %v", err)
	}

	// Create a failed edict-level verdict (manifest_id = '')
	verdict := storage.JudgeVerdict{
		VerdictID:  GenerateID("verdict", "1", "edict", "fail"),
		ManifestID: "",
		Username:   "testuser",
		Project:    "testproject",
		TestSuite:  "edict",
		Outcome:    storage.VerdictFailed,
	}
	if err := db.Create(&verdict).Error; err != nil {
		t.Fatalf("Failed to create edict-level verdict: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_verdicts_passed")
	if err == nil {
		t.Error("Expected error when edict-level verdict is failed, got nil")
	}
	if !strings.Contains(err.Error(), "edict-level verdict") {
		t.Errorf("Expected error to mention 'edict-level verdict', got: %v", err)
	}
}

// TestCheckVerdictsPassed_EdictLevelPassed verifies that a passed edict-level
// verdict does not trigger a failure.
func TestCheckVerdictsPassed_EdictLevelPassed(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.JudgeVerdict{}); err != nil {
		t.Fatalf("Failed to migrate JudgeVerdict: %v", err)
	}

	verdict := storage.JudgeVerdict{
		VerdictID:  GenerateID("verdict", "1", "edict", "pass"),
		ManifestID: "",
		Username:   "testuser",
		Project:    "testproject",
		TestSuite:  "edict",
		Outcome:    storage.VerdictPassed,
	}
	if err := db.Create(&verdict).Error; err != nil {
		t.Fatalf("Failed to create edict-level verdict: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_verdicts_passed")
	if err != nil {
		t.Errorf("Expected no error when edict-level verdict is passed, got: %v", err)
	}
}

// TestCheckPrecedentApproved_EdictLevelRejected verifies that a rejected
// edict-level precedent triggers a failure.
func TestCheckPrecedentApproved_EdictLevelRejected(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.CensorPrecedent{}); err != nil {
		t.Fatalf("Failed to migrate CensorPrecedent: %v", err)
	}

	precedent := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "1", "edict", "reject"),
		ManifestID:  "",
		EdictID:     1,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "ethics_review",
		Ruling:      storage.PrecedentRejected,
	}
	if err := db.Create(&precedent).Error; err != nil {
		t.Fatalf("Failed to create edict-level precedent: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_precedent_approved")
	if err == nil {
		t.Error("Expected error when edict-level precedent is rejected, got nil")
	}
	if !strings.Contains(err.Error(), "edict-level precedent") {
		t.Errorf("Expected error to mention 'edict-level precedent', got: %v", err)
	}
}

// TestCheckPrecedentApproved_EdictLevelApproved verifies that an approved
// edict-level precedent does not trigger a failure.
func TestCheckPrecedentApproved_EdictLevelApproved(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.CensorPrecedent{}); err != nil {
		t.Fatalf("Failed to migrate CensorPrecedent: %v", err)
	}

	precedent := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "1", "edict", "approve"),
		ManifestID:  "",
		EdictID:     1,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "ethics_review",
		Ruling:      storage.PrecedentApproved,
	}
	if err := db.Create(&precedent).Error; err != nil {
		t.Fatalf("Failed to create edict-level precedent: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_precedent_approved")
	if err != nil {
		t.Errorf("Expected no error when edict-level precedent is approved, got: %v", err)
	}
}

// TestCheckPrecedentApproved_CrossEdictRejectionDoesNotBlock verifies the
// cross-edict scope fix: an edict-level REJECTED precedent belonging to
// edict A must NOT block edict B's ascension gate.
func TestCheckPrecedentApproved_CrossEdictRejectionDoesNotBlock(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.CensorPrecedent{}); err != nil {
		t.Fatalf("Failed to migrate CensorPrecedent: %v", err)
	}

	// Edict A owns a rejected edict-level precedent.
	precedentA := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "A", "edict", "reject"),
		ManifestID:  "",
		EdictID:     1,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "ethics_review",
		Ruling:      storage.PrecedentRejected,
	}
	if err := db.Create(&precedentA).Error; err != nil {
		t.Fatalf("Failed to create edict-level precedent for A: %v", err)
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	// Edict B (different edict) must NOT be blocked by A's rejection.
	execB := &RitualExecution{
		EdictID:  2,
		Username: "testuser",
		Project:  "testproject",
	}
	err := runner.runThen(context.Background(), execB, "check_precedent_approved")
	if err != nil {
		t.Errorf("Expected edict B to pass despite edict A's rejection, got: %v", err)
	}

	// Edict A itself IS blocked.
	execA := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}
	err = runner.runThen(context.Background(), execA, "check_precedent_approved")
	if err == nil {
		t.Error("Expected edict A to be blocked by its own rejection, got nil")
	}
}

// TestCheckPrecedentApproved_EdictLevelLatestWinsPerEdict verifies that the
// edict-level check uses latest-wins scoped to the CURRENT edict: an older
// rejection for this edict followed by a newer approval for this edict passes,
// but the same-shaped data scoped to another edict does not interfere.
func TestCheckPrecedentApproved_EdictLevelLatestWinsPerEdict(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.CensorPrecedent{}); err != nil {
		t.Fatalf("Failed to migrate CensorPrecedent: %v", err)
	}

	// Edict 1: old rejection then newer approval → check passes.
	rejected := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "1", "edict", "reject"),
		ManifestID:  "",
		EdictID:     1,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "ethics_review",
		Ruling:      storage.PrecedentRejected,
	}
	if err := db.Create(&rejected).Error; err != nil {
		t.Fatalf("Failed to create rejection: %v", err)
	}
	approved := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "1", "edict", "approve"),
		ManifestID:  "",
		EdictID:     1,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "ethics_review",
		Ruling:      storage.PrecedentApproved,
	}
	if err := db.Create(&approved).Error; err != nil {
		t.Fatalf("Failed to create approval: %v", err)
	}
	approved.CreatedAt = rejected.CreatedAt.Add(time.Minute)
	db.Model(&storage.CensorPrecedent{}).Where("precedent_id = ?", approved.PrecedentID).Update("created_at", approved.CreatedAt)

	// Edict 2: latest edict-level precedent is rejected (newer than edict 1's).
	rejected2 := storage.CensorPrecedent{
		PrecedentID: GenerateID("precedent", "2", "edict", "reject"),
		ManifestID:  "",
		EdictID:     2,
		Username:    "testuser",
		Project:     "testproject",
		Principle:   "ethics_review",
		Ruling:      storage.PrecedentRejected,
	}
	if err := db.Create(&rejected2).Error; err != nil {
		t.Fatalf("Failed to create edict 2 rejection: %v", err)
	}
	// Make edict 1's approval later than edict 2's rejection to prove scoping.
	approved.CreatedAt = rejected2.CreatedAt.Add(time.Minute)
	db.Model(&storage.CensorPrecedent{}).Where("precedent_id = ?", approved.PrecedentID).Update("created_at", approved.CreatedAt)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	// Edict 1's latest edict-level record is its own approval → passes.
	exec1 := &RitualExecution{EdictID: 1, Username: "testuser", Project: "testproject"}
	if err := runner.runThen(context.Background(), exec1, "check_precedent_approved"); err != nil {
		t.Errorf("Expected edict 1 to pass with its own latest approval, got: %v", err)
	}

	// Edict 2's latest edict-level record is its own rejection → fails.
	exec2 := &RitualExecution{EdictID: 2, Username: "testuser", Project: "testproject"}
	if err := runner.runThen(context.Background(), exec2, "check_precedent_approved"); err == nil {
		t.Error("Expected edict 2 to fail due to its own latest rejection, got nil")
	}
}

// TestCheckVerdictsPassed_PassedThenFailed verifies latest-wins: a failed
// verdict after a passed one should fail.
func TestCheckVerdictsPassed_PassedThenFailed(t *testing.T) {
	db := setupRitualTestDB(t)

	if err := db.AutoMigrate(&storage.JudgeVerdict{}); err != nil {
		t.Fatalf("Failed to migrate JudgeVerdict: %v", err)
	}

	manifest := storage.ForgeManifest{
		ManifestID: GenerateID("manifest", "1", "test", "file.go"),
		EdictID:    1,
		Username:   "testuser",
		Project:    "testproject",
		FilePath:   "file.go",
		Status:     storage.ManifestQuenched,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatalf("Failed to create manifest: %v", err)
	}

	// Old passed verdict
	passedVerdict := storage.JudgeVerdict{
		VerdictID:  GenerateID("verdict", "1", "test", "file.go"),
		ManifestID: manifest.ManifestID,
		TestSuite:  "unit",
		Outcome:    storage.VerdictPassed,
	}
	if err := db.Create(&passedVerdict).Error; err != nil {
		t.Fatalf("Failed to create passed verdict: %v", err)
	}

	// Newer failed verdict
	failedVerdict := storage.JudgeVerdict{
		VerdictID:  GenerateID("verdict", "2", "test", "file.go"),
		ManifestID: manifest.ManifestID,
		TestSuite:  "unit",
		Outcome:    storage.VerdictFailed,
	}
	if err := db.Create(&failedVerdict).Error; err != nil {
		t.Fatalf("Failed to create failed verdict: %v", err)
	}
	failedVerdict.CreatedAt = passedVerdict.CreatedAt.Add(time.Minute)
	db.Model(&storage.JudgeVerdict{}).Where("verdict_id = ?", failedVerdict.VerdictID).Update("created_at", failedVerdict.CreatedAt)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_verdicts_passed")
	if err == nil {
		t.Error("Expected error when latest verdict is failed, got nil")
	}
	if !strings.Contains(err.Error(), "verdict check failed") {
		t.Errorf("Expected error to contain 'verdict check failed', got: %v", err)
	}
}

// TestCheckLingDAG_NoLings verifies that check_ling_dag passes when no lings exist.
func TestCheckLingDAG_NoLings(t *testing.T) {
	db := setupRitualTestDB(t)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_ling_dag")
	assert.NoError(t, err, "Expected no error when no lings exist")
}

// TestCheckLingDAG_ValidDAG verifies that check_ling_dag passes for a valid DAG.
func TestCheckLingDAG_ValidDAG(t *testing.T) {
	db := setupRitualTestDB(t)

	lings := []storage.Ling{
		{LingID: "a", EdictID: 1, Username: "testuser", Project: "testproject", Dependencies: storage.StringArray{}},
		{LingID: "b", EdictID: 1, Username: "testuser", Project: "testproject", Dependencies: storage.StringArray{"a"}},
		{LingID: "c", EdictID: 1, Username: "testuser", Project: "testproject", Dependencies: storage.StringArray{"a", "b"}},
	}
	for _, l := range lings {
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("Failed to create ling: %v", err)
		}
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_ling_dag")
	assert.NoError(t, err, "Expected no error for valid DAG")
}

// TestCheckLingDAG_CircularDependency verifies that check_ling_dag fails when
// lings form a circular dependency.
func TestCheckLingDAG_CircularDependency(t *testing.T) {
	db := setupRitualTestDB(t)

	lings := []storage.Ling{
		{LingID: "a", EdictID: 1, Username: "testuser", Project: "testproject", Dependencies: storage.StringArray{"b"}},
		{LingID: "b", EdictID: 1, Username: "testuser", Project: "testproject", Dependencies: storage.StringArray{"a"}},
	}
	for _, l := range lings {
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("Failed to create ling: %v", err)
		}
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_ling_dag")
	assert.Error(t, err, "Expected error for circular dependency")
	assert.Contains(t, err.Error(), "circular dependency")
}

// TestCheckLingDAG_IsolatesByEdictKey verifies that check_ling_dag only considers
// lings for the specific edict (not other edicts' lings).
func TestCheckLingDAG_IsolatesByEdictKey(t *testing.T) {
	db := setupRitualTestDB(t)

	// Edict 1: valid DAG
	lings1 := []storage.Ling{
		{LingID: "x", EdictID: 1, Username: "testuser", Project: "testproject", Dependencies: storage.StringArray{}},
		{LingID: "y", EdictID: 1, Username: "testuser", Project: "testproject", Dependencies: storage.StringArray{"x"}},
	}
	// Edict 2: circular dependency (should not affect edict 1)
	lings2 := []storage.Ling{
		{LingID: "a", EdictID: 2, Username: "testuser", Project: "testproject", Dependencies: storage.StringArray{"b"}},
		{LingID: "b", EdictID: 2, Username: "testuser", Project: "testproject", Dependencies: storage.StringArray{"a"}},
	}
	for _, l := range append(lings1, lings2...) {
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("Failed to create ling: %v", err)
		}
	}

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	exec := &RitualExecution{
		EdictID:  1,
		Username: "testuser",
		Project:  "testproject",
	}

	err := runner.runThen(context.Background(), exec, "check_ling_dag")
	assert.NoError(t, err, "Expected no error for edict 1 (valid DAG), despite edict 2 having a cycle")
}

// TestGetCourtStatus_ChancellorSealChecksChancellorMinisterID verifies that getCourtStatus
// checks for minister_id = 'chancellor' (not the old 'sage'). An edict with a
// chancellor seal should show status 'active' via the SQL CASE expression.
func TestGetCourtStatus_ChancellorSealChecksChancellorMinisterID(t *testing.T) {
	db := setupRitualTestDB(t)

	// setupRitualTestDB doesn't migrate Edict/Seal tables — create them here.
	require.NoError(t, db.AutoMigrate(&storage.Edict{}, &storage.Seal{}, &storage.Zhengming{}))

	// Create an edict.
	edict := storage.Edict{
		ID: 1, Username: "testuser", Project: "testproject", Intent: "test",
	}
	require.NoError(t, db.Create(&edict).Error)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	key := storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"}

	// Before any seal: status should be 'active'.
	result, err := runner.getCourtStatus(key)
	require.NoError(t, err)
	rows, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, rows, 1)
	assert.Equal(t, "active", rows[0]["status"])

	// Grant a chancellor seal — the SQL must match minister_id = 'chancellor'.
	sealSvc := storage.NewSealService(db)
	require.NoError(t, sealSvc.GrantSeal(key, "chancellor", storage.JSON{}))

	result, err = runner.getCourtStatus(key)
	require.NoError(t, err)
	rows, ok = result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, rows, 1)
	// With a chancellor seal (but no ruler seal), the edict is still 'active'.
	assert.Equal(t, "active", rows[0]["status"])

	// Grant a ruler seal — the edict should disappear from the active list.
	require.NoError(t, sealSvc.GrantSeal(key, "ruler", storage.JSON{}))
	result, err = runner.getCourtStatus(key)
	require.NoError(t, err)
	rows, ok = result.([]map[string]interface{})
	require.True(t, ok)
	assert.Empty(t, rows, "edict with ruler seal should not appear in court status")
}

func TestRunGivenStep_BashFailure_TruncatesOutput(t *testing.T) {
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()
	longOutput := strings.Repeat("x", 2000)
	failRunner := &mockCmdRunner{output: longOutput, exitCode: "1"}
	runner := NewRitualRunner(registry, nil, nil, db, failRunner, nil, repo.RepoInfo{})

	exec := &RitualExecution{
		ID:         "test-exec",
		RitualName: "test",
		EdictID:    100,
	}

	entry := StepDefEntry{
		Kind:    StepDefBash,
		Key:     "test",
		Command: "exit 1",
	}

	_, err := runner.runGivenStep(context.Background(), exec, entry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 1")
	assert.Contains(t, err.Error(), "…")
	assert.Less(t, len(err.Error()), 600,
		"error message should be truncated, not contain the full 2000-char output")
	// Verify exec.Data["error"] contains the FULL untruncated output
	require.NotNil(t, exec.Data, "exec.Data should be set on failure")
	assert.Equal(t, longOutput, exec.Data["error"],
		"exec.Data[\"error\"] should contain the full output, not truncated")
	assert.Equal(t, longOutput, exec.Data["test"],
		"exec.Data[\"test\"] (via storeGivenResult) should contain the full output, not truncated")
}

func TestRunThenStep_BashFailure_TruncatesOutput(t *testing.T) {
	db := setupRitualTestDB(t)
	registry := NewRitualRegistry()
	longOutput := strings.Repeat("y", 2000)
	failRunner := &mockCmdRunner{output: longOutput, exitCode: "1"}
	runner := NewRitualRunner(registry, nil, nil, db, failRunner, nil, repo.RepoInfo{})

	exec := &RitualExecution{
		ID:         "test-exec",
		RitualName: "test",
		EdictID:    100,
	}

	entry := StepDefEntry{
		Kind:    StepDefBash,
		Key:     "just",
		Command: "just test",
	}

	err := runner.runThenStep(context.Background(), exec, entry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 1")
	assert.Contains(t, err.Error(), "…")
	assert.Less(t, len(err.Error()), 600,
		"error message should be truncated, not contain the full 2000-char output")
}

// TestGetCourtStatus_StaleRulerSealShowsActive verifies that getCourtStatus
// filters stale seals — an edict whose ruler seal was invalidated by
// appendToIntent must reappear as 'active' (edict 714).
func TestGetCourtStatus_StaleRulerSealShowsActive(t *testing.T) {
	db := setupRitualTestDB(t)
	require.NoError(t, db.AutoMigrate(&storage.Edict{}, &storage.Seal{}, &storage.Zhengming{}))

	edict := storage.Edict{
		ID: 1, Username: "testuser", Project: "testproject", Intent: "test",
	}
	require.NoError(t, db.Create(&edict).Error)

	registry := NewRitualRegistry()
	runner := NewRitualRunner(registry, nil, nil, db, nil, slog.Default(), repo.RepoInfo{})

	key := storage.EdictKey{ID: 1, Username: "testuser", Project: "testproject"}
	sealSvc := storage.NewSealService(db)

	// Seal with all three — edict is sealed
	for _, m := range []string{"judge", "chancellor", "ruler"} {
		require.NoError(t, sealSvc.GrantSeal(key, m, storage.JSON{}))
	}
	result, err := runner.getCourtStatus(key)
	require.NoError(t, err)
	rows, _ := result.([]map[string]interface{})
	assert.Empty(t, rows, "sealed edict should not appear in court status")

	// Invalidate seals (intent changed) — edict should reappear as 'active'
	require.NoError(t, sealSvc.InvalidateSeals(key))
	result, err = runner.getCourtStatus(key)
	require.NoError(t, err)
	rows, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, rows, 1, "edict with stale ruler seal should reappear as active")
	assert.Equal(t, "active", rows[0]["status"])
}
