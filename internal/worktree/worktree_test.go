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

	abs, rel, branch, created, err := Ensure(context.Background(), mock, root, 42, []string{".env"})
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
	gotAbs, _, _, created, err := Ensure(context.Background(), mock, root, 7, nil)
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
	if _, _, _, _, err := Ensure(context.Background(), &recordingRunner{}, "", 1, nil); err == nil {
		t.Error("expected error for empty project root")
	}
}

func TestCopyIgnored(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e1")
	mock := &recordingRunner{}

	if err := CopyIgnored(context.Background(), mock, root, abs, []string{".env", ".envrc", "*.local"}); err != nil {
		t.Fatalf("CopyIgnored: %v", err)
	}
	joined := strings.Join(mock.commands, "\n")
	for _, needle := range []string{"--others", "--ignored", "--exclude-standard", "tar --null", ".agents/worktrees/*", "tar -xf - -C"} {
		if !strings.Contains(joined, needle) {
			t.Errorf("CopyIgnored command missing %q: %s", needle, joined)
		}
	}
	// The allowlist must be carried as a pathspec after `--` so git only
	// considers the declared globs. The unbounded form (no pathspec) is what
	// copied every gitignored artifact (caches, binaries, jobs/, logs).
	if !strings.Contains(joined, "-- .env .envrc") {
		t.Errorf("CopyIgnored must pass the allowlist after `--`: %s", joined)
	}
	if !strings.Contains(joined, `'*.local'`) {
		t.Errorf("CopyIgnored must quote each glob: %s", joined)
	}
	// GNU tar treats --exclude as positional: placed after -T it is
	// silently ignored, re-opening the recursive copy bug.
	exclude, list := strings.Index(joined, "--exclude="), strings.Index(joined, "-T -")
	if exclude == -1 || list == -1 || exclude > list {
		t.Errorf("--exclude must precede -T: %s", joined)
	}
}

func TestCopyIgnored_NoGlobsIsNoOp(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e1")
	mock := &recordingRunner{}

	if err := CopyIgnored(context.Background(), mock, root, abs, nil); err != nil {
		t.Fatalf("CopyIgnored: %v", err)
	}
	if len(mock.commands) != 0 {
		t.Errorf("no globs must mean no copy command, got %v", mock.commands)
	}
}

func TestSquashMergeToDefault_CommandSequence(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e3")
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"symbolic-ref":              {Output: "origin/main\n", ExitCode: "0"},
		"diff --cached --name-only": {Output: "widget.go\n", ExitCode: "0"},
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

// TestSquashMergeToDefault_EmptySquashIsNoOp: a fully-subsumed branch (its
// patch already on main under another commit) rebases to an ancestor of HEAD,
// so merge --squash stages nothing. The seal must not commit (git would exit 1
// with "no changes added to commit") and must return nil, so the caller
// proceeds to Remove and retires the worktree.
func TestSquashMergeToDefault_EmptySquashIsNoOp(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e967")
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"symbolic-ref":              {Output: "origin/main\n", ExitCode: "0"},
		"diff --cached --name-only": {Output: "", ExitCode: "0"},
	}}

	if err := SquashMergeToDefault(context.Background(), mock, root, abs, "asimi/e967", "bug: already landed e967"); err != nil {
		t.Fatalf("empty squash must be a successful no-op, got: %v", err)
	}
	joined := strings.Join(mock.commands, "\n")
	if !strings.Contains(joined, "merge --squash asimi/e967") {
		t.Errorf("expected squash merge, got:\n%s", joined)
	}
	if mock.contains("commit -m") {
		t.Errorf("must not commit when the squash staged nothing, got:\n%s", joined)
	}
}

func TestSquashMergeToDefault_DefaultMessage(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, ".agents", "worktrees", "e5")
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"symbolic-ref":              {Output: "origin/main\n", ExitCode: "0"},
		"diff --cached --name-only": {Output: "widget.go\n", ExitCode: "0"},
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
	// HEAD is detached, so root has no checked-out branch to prefer. origin/trunk
	// has no local counterpart, so the bare name would be an invalid upstream:
	// DefaultBranch must return the remote-tracking ref.
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"rev-parse --abbrev-ref HEAD": {Output: "HEAD\n", ExitCode: "0"},
		"symbolic-ref":                {Output: "origin/trunk\n", ExitCode: "0"},
		"refs/remotes/origin/trunk":   {Output: "abc123\n", ExitCode: "0"},
	}}
	if got := DefaultBranch(context.Background(), mock, t.TempDir()); got != "origin/trunk" {
		t.Errorf("DefaultBranch = %q, want origin/trunk", got)
	}
}

// TestDefaultBranch_LocalBranchWins: when the remote HEAD name also exists as
// a local branch, the bare name is a valid upstream and must be returned
// without the origin/ prefix (HEAD is detached here).
func TestDefaultBranch_LocalBranchWins(t *testing.T) {
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"rev-parse --abbrev-ref HEAD": {Output: "HEAD\n", ExitCode: "0"},
		"symbolic-ref":                {Output: "origin/trunk\n", ExitCode: "0"},
		"refs/heads/trunk":            {Output: "abc123\n", ExitCode: "0"},
		"refs/remotes/origin/trunk":   {Output: "abc123\n", ExitCode: "0"},
	}}
	if got := DefaultBranch(context.Background(), mock, t.TempDir()); got != "trunk" {
		t.Errorf("DefaultBranch = %q, want trunk", got)
	}
}

// TestDefaultBranch_PrefersCheckedOutBranch: the squash-merge target is root's
// checked-out branch, so the rebase base must equal it. Root's branch wins even
// when a remote HEAD names a different (stale) branch.
func TestDefaultBranch_PrefersCheckedOutBranch(t *testing.T) {
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"rev-parse --abbrev-ref HEAD": {Output: "release\n", ExitCode: "0"},
		"symbolic-ref":                {Output: "origin/trunk\n", ExitCode: "0"},
	}}
	if got := DefaultBranch(context.Background(), mock, t.TempDir()); got != "release" {
		t.Errorf("DefaultBranch = %q, want release", got)
	}
}

// TestDefaultBranch_FallsBackToMain: a detached HEAD and an origin/HEAD that
// names no valid upstream degrades to "main" rather than an invalid ref.
func TestDefaultBranch_FallsBackToMain_Detached(t *testing.T) {
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"rev-parse --abbrev-ref HEAD": {Output: "HEAD\n", ExitCode: "0"},
		"symbolic-ref":                {Output: "origin/trunk\n", ExitCode: "0"},
	}}
	if got := DefaultBranch(context.Background(), mock, t.TempDir()); got != "main" {
		t.Errorf("DefaultBranch = %q, want main", got)
	}
}

// TestDefaultBranch_QuotesRefInCommands pins the shell-quoting invariant:
// refExists interpolates a ref derived (via origin/HEAD) from the repository,
// and a git refname may legally contain shell metacharacters. Every ref must
// reach the shell quoted, mirroring the rest of the file.
func TestDefaultBranch_QuotesRefInCommands(t *testing.T) {
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"symbolic-ref": {Output: "origin/weird name\n", ExitCode: "0"},
	}}
	DefaultBranch(context.Background(), mock, t.TempDir())

	var found bool
	for _, c := range mock.commands {
		if !strings.Contains(c, "rev-parse --verify --quiet") {
			continue
		}
		found = true
		if !strings.Contains(c, "'refs/heads/weird name'") && !strings.Contains(c, "'refs/remotes/origin/weird name'") {
			t.Errorf("ref must be shell-quoted, got: %s", c)
		}
	}
	if !found {
		t.Error("expected rev-parse --verify --quiet calls, got none")
	}
}

// TestDefaultBranch_RealGit_CheckedOutBranchWins reproduces the seal failure:
// the checkout is on main (786 commits ahead of a stale origin/dev) while the
// remote HEAD points at dev, which exists only as origin/dev. DefaultBranch
// must return the checked-out main — the squash-merge target — so the worktree
// is not rebased back through ~780 already-landed commits, and the merge lands
// as a single squash commit.
func TestDefaultBranch_RealGit_CheckedOutBranchWins(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "init")

	// Pin origin/dev to the first commit — a stale ancestor of main.
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	stale := strings.TrimSpace(string(out))
	if err := os.WriteFile(filepath.Join(root, "landed.txt"), []byte("landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "land later work")
	runGit(t, root, "update-ref", "refs/remotes/origin/dev", stale)
	runGit(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/dev")

	runner := &realLightRunner{root: root}
	if got := DefaultBranch(context.Background(), runner, root); got != "main" {
		t.Fatalf("DefaultBranch = %q, want main (the squash-merge target)", got)
	}

	before := countCommits(t, root)

	abs := filepath.Join(root, ".agents", "worktrees", "e955")
	runGit(t, root, "worktree", "add", "-b", "asimi/e955", abs, "main")
	if err := os.WriteFile(filepath.Join(abs, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, abs, "add", ".")
	runGit(t, abs, "commit", "-m", "bug: feature e955")

	if err := SquashMergeToDefault(context.Background(), runner, root, abs, "asimi/e955", "bug: feature e955"); err != nil {
		t.Fatalf("SquashMergeToDefault: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "feature.txt")); err != nil {
		t.Errorf("squash-merge did not land feature.txt on main: %v", err)
	}
	if after := countCommits(t, root); after != before+1 {
		t.Errorf("expected exactly one new commit (a squash), got %d", after-before)
	}
	if err := Remove(context.Background(), runner, root, abs, "asimi/e955"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

// TestSquashMergeToDefault_RealGit_SubsumedBranchIsNoOp reproduces the edict 967
// seal failure: the worktree's patch is already on main under a different
// commit (here, an identical cherry-pick). The rebase skips it, the squash
// merge stages nothing, and SquashMergeToDefault must return nil without
// creating a commit — a false "squash commit failed" left the worktree
// stranded after the edict was already sealed.
func TestSquashMergeToDefault_RealGit_SubsumedBranchIsNoOp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "init")

	abs := filepath.Join(root, ".agents", "worktrees", "e967")
	runGit(t, root, "worktree", "add", "-b", "asimi/e967", abs, "main")
	if err := os.WriteFile(filepath.Join(abs, "fix.txt"), []byte("fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, abs, "add", ".")
	runGit(t, abs, "commit", "-m", "bug: fix e967")

	// Land the identical patch on main as a different commit (hand rescue).
	patch, err := exec.Command("git", "-C", abs, "format-patch", "-1", "--stdout").Output()
	if err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(root, "e967.patch")
	if err := os.WriteFile(patchPath, patch, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "am", patchPath)

	before := countCommits(t, root)
	runner := &realLightRunner{root: root}

	if err := SquashMergeToDefault(context.Background(), runner, root, abs, "asimi/e967", "bug: fix e967"); err != nil {
		t.Fatalf("subsumed branch must be a successful no-op, got: %v", err)
	}
	if after := countCommits(t, root); after != before {
		t.Errorf("no-op squash must not create a commit: %d -> %d", before, after)
	}
	// Main already carries the fix; the no-op must not alter it.
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	mainHead, err := exec.Command("git", "-C", root, "rev-parse", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(head)) != strings.TrimSpace(string(mainHead)) {
		t.Errorf("main moved: HEAD=%s main=%s", head, mainHead)
	}

	if err := Remove(context.Background(), runner, root, abs, "asimi/e967"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Errorf("worktree directory still present after Remove")
	}
}

// TestDefaultBranch_RealGit_DetachedRemoteOnlyBranch: with a detached HEAD there
// is no checked-out branch to merge into, so the remote HEAD name is used. It
// exists only as origin/dev, so DefaultBranch returns origin/dev (a valid
// upstream) and SquashMergeToDefault rebases onto it rather than failing with
// "invalid upstream 'dev'".
func TestDefaultBranch_RealGit_DetachedRemoteOnlyBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "init")
	// origin/dev exists as a remote-tracking ref; there is no local dev.
	runGit(t, root, "update-ref", "refs/remotes/origin/dev", "HEAD")
	runGit(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/dev")
	runGit(t, root, "checkout", "--detach", "HEAD")

	runner := &realLightRunner{root: root}
	if got := DefaultBranch(context.Background(), runner, root); got != "origin/dev" {
		t.Fatalf("DefaultBranch = %q, want origin/dev", got)
	}

	abs := filepath.Join(root, ".agents", "worktrees", "e955")
	runGit(t, root, "worktree", "add", "-b", "asimi/e955", abs, "main")
	if err := os.WriteFile(filepath.Join(abs, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, abs, "add", ".")
	runGit(t, abs, "commit", "-m", "bug: feature e955")

	if err := SquashMergeToDefault(context.Background(), runner, root, abs, "asimi/e955", "bug: feature e955"); err != nil {
		t.Fatalf("SquashMergeToDefault: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "feature.txt")); err != nil {
		t.Errorf("squash-merge did not land feature.txt: %v", err)
	}
	if err := Remove(context.Background(), runner, root, abs, "asimi/e955"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

// countCommits returns the number of commits reachable from HEAD.
func countCommits(t *testing.T, dir string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-list --count: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("rev-list --count: %v", err)
	}
	return n
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
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("local.env\n.gocache/\njobs/\nasimi\n.agents/worktrees/\n"), 0o644); err != nil {
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
	// Plant ignored artifacts that must NOT travel into the new worktree:
	// a build cache, a secrets-bearing jobs/ directory, and a built binary.
	if err := os.MkdirAll(filepath.Join(root, ".gocache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gocache", "trim.txt"), []byte("cache\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "jobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "jobs", "secret"), []byte("token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "asimi"), []byte("binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant an ignored nested worktree copy that must not travel into the
	// new worktree.
	nested := filepath.Join(root, ".agents", "worktrees", "e999")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "nested.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &realLightRunner{root: root}
	abs, _, branch, created, err := Ensure(context.Background(), runner, root, 123, []string{"local.env"})
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
	// ...but the allowlist excluded caches, jobs/, and built binaries.
	for _, forbidden := range []string{".gocache", "jobs", "asimi"} {
		if _, err := os.Stat(filepath.Join(abs, forbidden)); !os.IsNotExist(err) {
			t.Errorf("CopyIgnored must not copy %q, stat err = %v", forbidden, err)
		}
	}
	// ...and worktrees are never copied into worktrees.
	if _, err := os.Stat(filepath.Join(abs, ".agents", "worktrees")); !os.IsNotExist(err) {
		t.Errorf("CopyIgnored must not copy .agents/worktrees, stat err = %v", err)
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

func TestResolve_MainAndMaster(t *testing.T) {
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"symbolic-ref": {Output: "origin/main\n", ExitCode: "0"},
	}}
	for _, target := range []string{"main", "master"} {
		got, err := Resolve(context.Background(), mock, t.TempDir(), target)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", target, err)
		}
		if got.Branch != "main" || got.WorktreeRel != "" || got.IsWorktree || got.EdictID != 0 {
			t.Errorf("Resolve(%q) = %+v, want main in-tree", target, got)
		}
	}
}

func TestResolve_EdictTarget(t *testing.T) {
	got, err := Resolve(context.Background(), &recordingRunner{}, t.TempDir(), "e123")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Branch != "asimi/e123" {
		t.Errorf("Branch = %q, want asimi/e123", got.Branch)
	}
	if got.WorktreeRel != filepath.Join(".agents", "worktrees", "e123") {
		t.Errorf("WorktreeRel = %q", got.WorktreeRel)
	}
	if !got.IsWorktree || got.EdictID != 123 {
		t.Errorf("Resolve(e123) = %+v, want worktree for edict 123", got)
	}
}

func TestResolve_PlainBranchWithNoWorktree(t *testing.T) {
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"worktree list": {Output: "worktree /tmp/repo\nbranch refs/heads/main\n", ExitCode: "0"},
	}}
	got, err := Resolve(context.Background(), mock, "/tmp/repo", "feature/x")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Branch != "feature/x" || got.WorktreeRel != "" || got.IsWorktree {
		t.Errorf("Resolve(feature/x) = %+v, want branch as-is", got)
	}
}

func TestResolve_PlainBranchMatchingLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	porcelain := "worktree " + root + "\nHEAD abc\nbranch refs/heads/main\n\n" +
		"worktree " + filepath.Join(root, ".agents", "worktrees", "e55") + "\nHEAD def\nbranch refs/heads/feature/y\n"
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"worktree list": {Output: porcelain, ExitCode: "0"},
	}}
	got, err := Resolve(context.Background(), mock, root, "feature/y")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Branch != "feature/y" {
		t.Errorf("Branch = %q, want feature/y", got.Branch)
	}
	if !got.IsWorktree || got.WorktreeRel != filepath.Join(".agents", "worktrees", "e55") {
		t.Errorf("Resolve(feature/y) = %+v, want linked worktree path", got)
	}
}

// A path outside the project root must never be accepted, even when git
// reports a branch match: the sandbox only bind-mounts ProjectRoot.
func TestResolve_RejectsWorktreeOutsideRoot(t *testing.T) {
	root := t.TempDir()
	porcelain := "worktree /somewhere/else\nHEAD abc\nbranch refs/heads/feature/z\n"
	mock := &recordingRunner{outputs: map[string]runners.Output{
		"worktree list": {Output: porcelain, ExitCode: "0"},
	}}
	got, err := Resolve(context.Background(), mock, root, "feature/z")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.IsWorktree || got.WorktreeRel != "" {
		t.Errorf("Resolve(feature/z) = %+v, want no worktree path outside root", got)
	}
}

func TestResolve_EmptyTarget(t *testing.T) {
	if _, err := Resolve(context.Background(), &recordingRunner{}, t.TempDir(), "  "); err == nil {
		t.Error("expected error for empty target")
	}
}
