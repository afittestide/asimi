package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/afittestide/asimi/internal/runners"
)

// recordingRunner records every command it is asked to run and returns canned
// output for commands matching a key substring.
type recordingRunner struct {
	commands []string
	outputs  map[string]runners.Output
}

func (m *recordingRunner) Run(_ context.Context, input runners.Input) (runners.Output, error) {
	m.commands = append(m.commands, input.Command)
	for sub, out := range m.outputs {
		if strings.Contains(input.Command, sub) {
			return out, nil
		}
	}
	return runners.Output{ExitCode: "0"}, nil
}

func (m *recordingRunner) Restart(context.Context) error        { return nil }
func (m *recordingRunner) Close(context.Context) error          { return nil }
func (m *recordingRunner) AllowFallback(bool)                   {}
func (m *recordingRunner) RunnerType() string                   { return "mock" }
func (m *recordingRunner) GetOS() string                        { return runtime.GOOS }
func (m *recordingRunner) SetMessageChannel(chan<- runners.Msg) {}
func (m *recordingRunner) HealthCheck(context.Context) error    { return nil }

func (m *recordingRunner) contains(sub string) bool {
	for _, c := range m.commands {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func TestEnsure_CreatesWorktreeUnderProjectRoot(t *testing.T) {
	root := t.TempDir()
	mock := &recordingRunner{}

	abs, rel, branch, created, err := Ensure(context.Background(), mock, root, 42)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !created {
		t.Error("expected created=true on first Ensure")
	}
	wantAbs := filepath.Join(root, ".agents", "worktrees", "e42")
	if abs != wantAbs {
		t.Errorf("abs = %q, want %q", abs, wantAbs)
	}
	if rel != filepath.Join(".agents", "worktrees", "e42") {
		t.Errorf("rel = %q", rel)
	}
	if branch != "asimi/e42" {
		t.Errorf("branch = %q, want asimi/e42", branch)
	}
	if !mock.contains("git worktree add -B") {
		t.Errorf("expected git worktree add, got %v", mock.commands)
	}
	if !mock.contains("just install") {
		t.Errorf("expected just install, got %v", mock.commands)
	}
	if !mock.contains("git ls-files -z") {
		t.Errorf("expected CopyIgnored pipeline, got %v", mock.commands)
	}
	// The worktree must live inside the project root (the sandbox bind mount).
	if !strings.HasPrefix(abs, root) {
		t.Errorf("worktree %q must be under project root %q", abs, root)
	}
}

func TestEnsure_IdempotentReuse(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e7")
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}

	mock := &recordingRunner{}
	gotAbs, _, _, created, err := Ensure(context.Background(), mock, root, 7)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if created {
		t.Error("expected created=false when worktree already exists")
	}
	if gotAbs != abs {
		t.Errorf("abs = %q, want %q", gotAbs, abs)
	}
	if len(mock.commands) != 0 {
		t.Errorf("existing worktree must be reused without commands, got %v", mock.commands)
	}
}

func TestEnsure_EmptyRoot(t *testing.T) {
	if _, _, _, _, err := Ensure(context.Background(), &recordingRunner{}, "", 1); err == nil {
		t.Error("expected error for empty project root")
	}
}

func TestCopyIgnored(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e1")
	mock := &recordingRunner{}

	if err := CopyIgnored(context.Background(), mock, root, abs); err != nil {
		t.Fatalf("CopyIgnored: %v", err)
	}
	joined := strings.Join(mock.commands, "\n")
	for _, needle := range []string{"--others", "--ignored", "--exclude-standard", "tar --null", "tar -xf - -C"} {
		if !strings.Contains(joined, needle) {
			t.Errorf("CopyIgnored command missing %q: %s", needle, joined)
		}
	}
}

func TestSquashMergeToDefault_CommandSequence(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e3")
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"symbolic-ref": {Output: "origin/main\n", ExitCode: "0"},
	}}

	if err := SquashMergeToDefault(context.Background(), mock, root, abs, "asimi/e3", "feat: widget e3"); err != nil {
		t.Fatalf("SquashMergeToDefault: %v", err)
	}
	joined := strings.Join(mock.commands, "\n")
	if !strings.Contains(joined, "rebase main") {
		t.Errorf("expected rebase onto main, got:\n%s", joined)
	}
	if !strings.Contains(joined, "merge --squash asimi/e3") {
		t.Errorf("expected squash merge, got:\n%s", joined)
	}
	if !strings.Contains(joined, "feat: widget e3") {
		t.Errorf("expected commit message, got:\n%s", joined)
	}
}

func TestSquashMergeToDefault_DefaultMessage(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e5")
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"symbolic-ref": {Output: "origin/main\n", ExitCode: "0"},
	}}

	if err := SquashMergeToDefault(context.Background(), mock, root, abs, "asimi/e5", ""); err != nil {
		t.Fatalf("SquashMergeToDefault: %v", err)
	}
	if !mock.contains("commit -m") {
		t.Errorf("expected a commit command, got %v", mock.commands)
	}
}

func TestSquashMerge_ConflictLeavesWorktreeIntact(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e9")
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"symbolic-ref": {Output: "origin/main\n", ExitCode: "0"},
		"rebase":       {Output: "CONFLICT", ExitCode: "1"},
	}}

	err := SquashMergeToDefault(context.Background(), mock, root, abs, "asimi/e9", "feat: x e9")
	if err == nil {
		t.Fatal("expected rebase conflict error")
	}
	if mock.contains("merge --squash") {
		t.Error("must not attempt squash-merge after a failed rebase")
	}
	if mock.contains("worktree remove") {
		t.Error("must not remove the worktree on conflict — forge resolves it")
	}
}

func TestRemove_CommandSequence(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e11")
	mock := &recordingRunner{}

	if err := Remove(context.Background(), mock, root, abs, "asimi/e11"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	joined := strings.Join(mock.commands, "\n")
	if !strings.Contains(joined, "worktree remove") {
		t.Errorf("expected worktree remove, got:\n%s", joined)
	}
	if strings.Contains(joined, "--force") {
		t.Errorf("removal must not use the forbidden --force flag, got:\n%s", joined)
	}
	if !strings.Contains(joined, "update-ref -d refs/heads/asimi/e11") {
		t.Errorf("expected branch delete via update-ref, got:\n%s", joined)
	}
}

func TestDefaultBranch_FallsBackToMain(t *testing.T) {
	mock := &recordingRunner{}
	if got := DefaultBranch(context.Background(), mock, t.TempDir()); got != "main" {
		t.Errorf("DefaultBranch = %q, want main", got)
	}
}

func TestDefaultBranch_StripsRemotePrefix(t *testing.T) {
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"symbolic-ref": {Output: "origin/trunk\n", ExitCode: "0"},
	}}
	if got := DefaultBranch(context.Background(), mock, t.TempDir()); got != "trunk" {
		t.Errorf("DefaultBranch = %q, want trunk", got)
	}
}

// TestEnsure_RealGit exercises Ensure against a real repository. It skips when
// git or just is unavailable, since Ensure runs `just install`.
func TestEnsure_RealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("just"); err != nil {
		t.Skip("just not available")
	}

	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("local.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(root, "Justfile"), []byte("install:\n    @true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "local.env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &realLightRunner{root: root}
	abs, _, branch, created, err := Ensure(context.Background(), runner, root, 123)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !created {
		t.Error("expected created=true")
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		t.Errorf("worktree .git missing: %v", err)
	}
	// copy-ignored carried the local-only env file.
	if _, err := os.Stat(filepath.Join(abs, "local.env")); err != nil {
		t.Errorf("ignored file not copied: %v", err)
	}

	// Commit work in the worktree and squash-merge it into main.
	if err := os.WriteFile(filepath.Join(abs, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, abs, "add", ".")
	runGit(t, abs, "commit", "-m", "feat: feature e123")

	if err := SquashMergeToDefault(context.Background(), runner, root, abs, branch, "feat: feature e123"); err != nil {
		t.Fatalf("SquashMergeToDefault: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "feature.txt")); err != nil {
		t.Errorf("squash-merge did not land feature.txt on main: %v", err)
	}
	if err := Remove(context.Background(), runner, root, abs, branch); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Errorf("worktree directory still present after Remove")
	}
}

// realLightRunner executes commands on the host via bash.
type realLightRunner struct{ root string }

func (r *realLightRunner) Run(ctx context.Context, input runners.Input) (runners.Output, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", input.Command)
	cmd.Dir = r.root
	out, err := cmd.CombinedOutput()
	exit := "0"
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = strconv.Itoa(ee.ExitCode())
		} else {
			return runners.Output{Output: string(out)}, err
		}
	}
	return runners.Output{Output: string(out), ExitCode: exit}, nil
}

func (r *realLightRunner) Restart(context.Context) error        { return nil }
func (r *realLightRunner) Close(context.Context) error          { return nil }
func (r *realLightRunner) AllowFallback(bool)                   {}
func (r *realLightRunner) RunnerType() string                   { return "host" }
func (r *realLightRunner) GetOS() string                        { return runtime.GOOS }
func (r *realLightRunner) SetMessageChannel(chan<- runners.Msg) {}
func (r *realLightRunner) HealthCheck(context.Context) error    { return nil }

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
