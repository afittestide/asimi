package court

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newGitlessRitualRunner builds a RitualRunner pointed at a plain directory
// with no .git — the gitless ground produced by the harbor harness.
func newGitlessRitualRunner(t *testing.T) (*RitualRunner, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "hello-world")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("failed to create gitless root: %v", err)
	}
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	if err := db.AutoMigrate(&storage.ForgeManifest{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return &RitualRunner{
		db:       db,
		logger:   slog.Default(),
		repoInfo: repo.RepoInfo{ProjectRoot: root},
	}, root
}

// TestGetEarthStatus_Gitless verifies earth expressions state the gitless
// condition explicitly instead of silently returning empty strings.
func TestGetEarthStatus_Gitless(t *testing.T) {
	runner, _ := newGitlessRitualRunner(t)

	result, err := runner.getEarthStatus(context.Background())
	if err != nil {
		t.Fatalf("getEarthStatus failed: %v", err)
	}

	for _, key := range []string{"earth_capital", "earth_middle_kingdom", "earth_borderlands"} {
		got := result[key]
		if got == "" {
			t.Errorf("%s should not be silently empty on gitless ground", key)
		}
		if !containsNoGitMarker(got) {
			t.Errorf("%s = %q, want explicit no-git marker", key, got)
		}
	}
}

// TestGetBorderlands_Gitless verifies the borderlands expression reports the
// gitless state on a plain directory without running any git command.
func TestGetBorderlands_Gitless(t *testing.T) {
	runner, _ := newGitlessRitualRunner(t)

	result, err := runner.getBorderlands(context.Background())
	if err != nil {
		t.Fatalf("getBorderlands failed: %v", err)
	}

	m, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", result)
	}
	for _, key := range []string{"borderlands:changes", "borderlands:untracked"} {
		got := m[key]
		if !containsNoGitMarker(got) {
			t.Errorf("%s = %q, want explicit no-git marker", key, got)
		}
	}
}

// TestCheckCleanWorkingDirectory_Gitless verifies the cleanliness check
// reports the gitless state instead of vacuously returning clean.
func TestCheckCleanWorkingDirectory_Gitless(t *testing.T) {
	runner, _ := newGitlessRitualRunner(t)

	result, err := runner.checkCleanWorkingDirectory(context.Background())
	if err != nil {
		t.Fatalf("checkCleanWorkingDirectory should not fail on gitless ground: %v", err)
	}
	m, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", result)
	}
	if !containsNoGitMarker(m["status"]) {
		t.Errorf("status = %q, want explicit no-git marker", m["status"])
	}
}

// TestCreateBorderlandManifests_Gitless verifies the file-inventory fallback:
// on gitless ground the borderland manifests come from walking the project
// root (excluding .git/.agents/dependency/build artifacts), so the judge can
// still verdict changes through the standard pipeline.
func TestCreateBorderlandManifests_Gitless(t *testing.T) {
	runner, root := newGitlessRitualRunner(t)

	// Source files that should be inventoried
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main")
	write("README.md", "# hello world")
	write("util_test.go", "package main")
	// Excluded: VCS internals, tooling, dependencies, build artifacts
	write(".git/config", "[core]") // .git skipped even without a repo
	write(".agents/sandbox/Dockerfile", "FROM scratch")
	write("node_modules/pkg/index.js", "x")
	write("bin/asimi", "binary")
	write("main.o", "object file")

	exec := &RitualExecution{
		EdictID:  888,
		Username: "tester",
		Project:  "hello-world",
	}

	result, err := runner.createBorderlandManifests(context.Background(), exec)
	if err != nil {
		t.Fatalf("createBorderlandManifests failed: %v", err)
	}

	manifests, ok := result.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected manifests list, got %T: %v", result, result)
	}

	paths := make(map[string]bool)
	for _, m := range manifests {
		paths[m["file_path"].(string)] = true
	}
	for _, want := range []string{"main.go", "README.md", "util_test.go"} {
		if !paths[want] {
			t.Errorf("expected manifest for %s, got %v", want, paths)
		}
	}
	for _, excluded := range []string{".git/config", ".agents/sandbox/Dockerfile", "node_modules/pkg/index.js", "bin/asimi", "main.o"} {
		if paths[excluded] {
			t.Errorf("excluded path %s should not get a manifest", excluded)
		}
	}

	// Manifests must be persisted and scoped to the gitless project slug
	var count int64
	if err := runner.db.Model(&storage.ForgeManifest{}).
		Where("edict_id = ? AND username = ? AND project = ?", 888, "tester", "hello-world").
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != int64(len(manifests)) {
		t.Errorf("expected %d persisted manifests, got %d", len(manifests), count)
	}
}

func containsNoGitMarker(s string) bool {
	return strings.Contains(s, "no git repository")
}

// TestAwaitRulerSeal_GitlessSkipsGitCommands verifies the gitless guard on
// the staging step of the seal chain: on a plain directory await_ruler_seal
// succeeds without running any git command — ascension stops after the
// chancellor's seal, the edict stays sealed in the court ledger only.
func TestAwaitRulerSeal_GitlessSkipsGitCommands(t *testing.T) {
	runner, _ := newGitlessRitualRunner(t)
	var commands []string
	runner.runner = &mockCmdRunner{onRun: func(cmd string) { commands = append(commands, cmd) }}

	exec := &RitualExecution{ID: "gitless-await-seal", RitualName: "swift-strike", EdictID: 888}
	if err := runner.runThen(context.Background(), exec, "await_ruler_seal"); err != nil {
		t.Fatalf("runThen(await_ruler_seal) must succeed on gitless ground: %v", err)
	}
	if len(commands) != 0 {
		t.Errorf("no git commands may run on gitless ground, got: %v", commands)
	}
}

// TestStageInfrastructure_GitlessSkipsGitCommands verifies the gitless guard
// on stage_infrastructure: nothing is staged and no git command is attempted.
func TestStageInfrastructure_GitlessSkipsGitCommands(t *testing.T) {
	runner, _ := newGitlessRitualRunner(t)
	var commands []string
	runner.runner = &mockCmdRunner{onRun: func(cmd string) { commands = append(commands, cmd) }}

	exec := &RitualExecution{ID: "gitless-stage-infra", RitualName: "swift-strike", EdictID: 888}
	if err := runner.runThen(context.Background(), exec, "stage_infrastructure"); err != nil {
		t.Fatalf("runThen(stage_infrastructure) must succeed on gitless ground: %v", err)
	}
	if len(commands) != 0 {
		t.Errorf("no git commands may run on gitless ground, got: %v", commands)
	}
}

// TestGetHeavenSnapshot_Gitless verifies the heaven snapshot states the
// gitless condition explicitly instead of returning silent empty strings.
func TestGetHeavenSnapshot_Gitless(t *testing.T) {
	runner, _ := newGitlessRitualRunner(t)

	result, err := runner.getHeavenSnapshot(context.Background())
	if err != nil {
		t.Fatalf("getHeavenSnapshot failed on gitless ground: %v", err)
	}
	m, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", result)
	}
	if !containsNoGitMarker(m["age"]) {
		t.Errorf("age = %q, want explicit no-git marker", m["age"])
	}
	if m["branch"] != "" || m["latest_commit"] != "" {
		t.Errorf("gitless snapshot must not report a branch or commit, got %+v", m)
	}
}
