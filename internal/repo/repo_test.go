package repo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/require"
)

func TestIsMainBranch(t *testing.T) {
	tests := []struct {
		name     string
		branch   string
		expected bool
	}{
		{"main branch", "main", true},
		{"master branch", "master", true},
		{"feature branch", "feature/test", false},
		{"develop branch", "develop", false},
		{"empty branch", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isMainBranch(tt.branch)
			require.Equal(t, tt.expected, result, "isMainBranch(%q)", tt.branch)
		})
	}
}

func TestSummarizeStatus(t *testing.T) {
	cases := []struct {
		name     string
		status   gogit.Status
		expected string
	}{
		{
			name:     "empty status",
			status:   gogit.Status{},
			expected: "",
		},
		{
			name: "mixed indicators",
			status: gogit.Status{
				"modified.go": &gogit.FileStatus{
					Staging:  gogit.Modified,
					Worktree: gogit.Unmodified,
				},
				"staged_added.go": &gogit.FileStatus{
					Staging:  gogit.Added,
					Worktree: gogit.Unmodified,
				},
				"deleted.txt": &gogit.FileStatus{
					Staging:  gogit.Deleted,
					Worktree: gogit.Unmodified,
				},
				"renamed.txt": &gogit.FileStatus{
					Staging:  gogit.Renamed,
					Worktree: gogit.Unmodified,
				},
				"untracked.md": &gogit.FileStatus{
					Staging:  gogit.Untracked,
					Worktree: gogit.Untracked,
				},
				"worktree_modified.go": &gogit.FileStatus{
					Staging:  gogit.Unmodified,
					Worktree: gogit.Modified,
				},
			},
			expected: "[!+-→?]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, summarizeStatus(tc.status))
		})
	}
}

func TestDiffLines(t *testing.T) {
	tests := []struct {
		name            string
		original        []string
		current         []string
		expectedAdded   int
		expectedDeleted int
	}{
		{
			name:            "no changes",
			original:        []string{"line1", "line2", "line3"},
			current:         []string{"line1", "line2", "line3"},
			expectedAdded:   0,
			expectedDeleted: 0,
		},
		{
			name:            "only additions",
			original:        []string{"line1", "line2"},
			current:         []string{"line1", "line2", "line3", "line4"},
			expectedAdded:   2,
			expectedDeleted: 0,
		},
		{
			name:            "only deletions",
			original:        []string{"line1", "line2", "line3", "line4"},
			current:         []string{"line1", "line2"},
			expectedAdded:   0,
			expectedDeleted: 2,
		},
		{
			name:            "balanced changes (should be modifications)",
			original:        []string{"line1", "line2", "line3"},
			current:         []string{"line1", "newline2", "newline3"},
			expectedAdded:   2,
			expectedDeleted: 2,
		},
		{
			name:            "more additions than deletions",
			original:        []string{"line1", "line2"},
			current:         []string{"line1", "newline2", "line3", "line4"},
			expectedAdded:   3,
			expectedDeleted: 1,
		},
		{
			name:            "more deletions than additions",
			original:        []string{"line1", "line2", "line3", "line4"},
			current:         []string{"line1", "newline2"},
			expectedAdded:   1,
			expectedDeleted: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, deleted := diffLines(tt.original, tt.current)
			require.Equal(t, tt.expectedAdded, added, "added lines mismatch")
			require.Equal(t, tt.expectedDeleted, deleted, "deleted lines mismatch")
		})
	}
}

func TestParseGitNumstat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         string
		expectedAdded int
		expectedDel   int
	}{
		{
			name: "mixed changes",
			input: "1\t1\tmain.go\n" +
				"34\t0\ttui.go\n",
			expectedAdded: 35,
			expectedDel:   1,
		},
		{
			name:          "binary files ignored",
			input:         "-\t-\timage.png\n",
			expectedAdded: 0,
			expectedDel:   0,
		},
		{
			name:          "unbalanced changes stay separate",
			input:         "5\t2\treport.md\n",
			expectedAdded: 5,
			expectedDel:   2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			added, deleted := parseGitNumstat([]byte(tt.input))
			require.Equal(t, tt.expectedAdded, added)
			require.Equal(t, tt.expectedDel, deleted)
		})
	}
}
func TestGetRepoInfoForRoot(t *testing.T) {
	// Use the current project directory as a real git repo
	root, err := os.Getwd()
	require.NoError(t, err)

	t.Setenv("ASIMI_SKIP_GIT_STATUS", "1")

	info := GetRepoInfoForRoot(root)
	require.NotEmpty(t, info.ProjectRoot, "ProjectRoot should be set")
	require.NotEmpty(t, info.Branch, "Branch should be detected")
	require.False(t, info.IsWorktree, "project root should not be a worktree")
	require.NotEmpty(t, info.Branch, "Branch should not be empty")
	require.Equal(t, root, info.ProjectRoot, "ProjectRoot should match the provided root")
}

func TestGetRepoInfoForRoot_NonExistentDir(t *testing.T) {
	t.Setenv("ASIMI_SKIP_GIT_STATUS", "1")

	info := GetRepoInfoForRoot("/nonexistent/path/that/does/not/exist")
	// When .git doesn't exist, isWorktree=false, so projectRoot=root (not a git repo)
	require.Equal(t, "/nonexistent/path/that/does/not/exist", info.ProjectRoot)
	require.Empty(t, info.Branch, "Branch should be empty for nonexistent dir")
	require.False(t, info.IsWorktree)
	require.Empty(t, info.Slug, "Slug should be empty when no git remote")
}

func TestBranchFromDetachedHead(t *testing.T) {
	t.Run("not in any state", func(t *testing.T) {
		require.Equal(t, "", branchFromDetachedHead(t.TempDir()))
	})

	t.Run("rebase-merge", func(t *testing.T) {
		tmp := t.TempDir()
		dir := filepath.Join(tmp, ".git", "rebase-merge")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "head-name"), []byte("refs/heads/main"), 0o644))
		require.Equal(t, "main", branchFromDetachedHead(tmp))
	})

	t.Run("rebase-apply", func(t *testing.T) {
		tmp := t.TempDir()
		dir := filepath.Join(tmp, ".git", "rebase-apply")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "head-name"), []byte("refs/heads/feature"), 0o644))
		require.Equal(t, "feature", branchFromDetachedHead(tmp))
	})

	t.Run("bisect", func(t *testing.T) {
		tmp := t.TempDir()
		dir := filepath.Join(tmp, ".git")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "BISECT_START"), []byte("refs/heads/develop"), 0o644))
		require.Equal(t, "develop", branchFromDetachedHead(tmp))
	})

	t.Run("rebase-merge takes precedence", func(t *testing.T) {
		tmp := t.TempDir()
		for _, p := range []string{".git/rebase-merge", ".git/rebase-apply", ".git"} {
			require.NoError(t, os.MkdirAll(filepath.Join(tmp, p), 0o755))
		}
		require.NoError(t, os.WriteFile(filepath.Join(tmp, ".git/rebase-merge/head-name"), []byte("refs/heads/main"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(tmp, ".git/rebase-apply/head-name"), []byte("refs/heads/wrong"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(tmp, ".git/BISECT_START"), []byte("refs/heads/wrong"), 0o644))
		require.Equal(t, "main", branchFromDetachedHead(tmp))
	})
}

func TestSanitizeSegment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"owner/repo slug", "owner/repo", "owner-repo"},
		{"multiple slashes", "a/b/c", "a-b-c"},
		{"empty string", "", ""},
		{"single char", "x", "x"},
		{"uppercase", "MyRepo", "myrepo"},
		{"leading slash", "/leading", "leading"},
		{"trailing slash", "trailing/", "trailing"},
		{"special chars", "hello@world!", "hello-world"},
		{"consecutive specials", "a///b", "a-b"},
		{"dots", "v1.2.3", "v1-2-3"},
		{"underscores", "my_repo", "my-repo"},
		{"spaces", "hello world", "hello-world"},
		{"numbers", "repo123", "repo123"},
		{"mixed", "Owner/Repo.v2", "owner-repo-v2"},
		{"only specials", "///", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeSegment(tt.input)
			require.Equal(t, tt.expected, result, "SanitizeSegment(%q)", tt.input)
		})
	}
}

func TestParseGitRemote(t *testing.T) {
	tests := []struct {
		name      string
		remote    string
		wantOwner string
		wantRepo  string
	}{
		{"https uppercase", "https://github.com/MyOrg/MyProject.git", "MyOrg", "MyProject"},
		{"ssh scp syntax", "git@github.com:MyOrg/MyProject.git", "MyOrg", "MyProject"},
		{"git protocol", "git://github.com/MyOrg/MyProject.git", "MyOrg", "MyProject"},
		{"mixed case", "https://github.com/myOrg/myProject", "myOrg", "myProject"},
		{"empty", "", "", ""},
		{"only owner no repo", "https://github.com/MyOrg", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo := ParseGitRemote(tt.remote)
			require.Equal(t, tt.wantOwner, owner, "owner mismatch")
			require.Equal(t, tt.wantRepo, repo, "repo mismatch")
		})
	}
}

func TestProjectSlugLowercasesRemoteSegments(t *testing.T) {
	// projectSlug uses git config --get remote.origin.url, so set up a real repo.
	dir := t.TempDir()
	_, err := runGitCommand(dir, "init")
	require.NoError(t, err)
	_, err = runGitCommand(dir, "remote", "add", "origin", "https://github.com/MyOrg/MyProject.git")
	require.NoError(t, err)
	t.Setenv("ASIMI_SKIP_GIT_STATUS", "1")

	slug := projectSlug(dir)
	require.Equal(t, "myorg/myproject", slug)
}

// newTestRepo creates a RepoInfo backed by a real git repo in a temp dir.
// The repo has one committed file and one uncommitted modification so that
// RefreshDiff produces non-zero added/deleted counts.
func newTestRepo(t *testing.T) (*RepoInfo, string) {
	t.Helper()
	dir := t.TempDir()

	_, err := runGitCommand(dir, "init")
	require.NoError(t, err)
	// Configure identity for commit
	_, err = runGitCommand(dir, "config", "user.email", "test@example.com")
	require.NoError(t, err)
	_, err = runGitCommand(dir, "config", "user.name", "Test")
	require.NoError(t, err)

	// Committed file
	committed := filepath.Join(dir, "committed.txt")
	require.NoError(t, os.WriteFile(committed, []byte("line1\nline2\n"), 0o644))
	_, err = runGitCommand(dir, "add", "committed.txt")
	require.NoError(t, err)
	_, err = runGitCommand(dir, "commit", "-m", "initial")
	require.NoError(t, err)

	// Working tree modification
	modified := filepath.Join(dir, "committed.txt")
	require.NoError(t, os.WriteFile(modified, []byte("line1\nline2\nline3\n"), 0o644))

	repo, err := gogit.PlainOpenWithOptions(dir, &gogit.PlainOpenOptions{DetectDotGit: true})
	require.NoError(t, err)

	ri := &RepoInfo{
		ProjectRoot: dir,
		repo:        repo,
	}
	ri.RefreshDiff()
	return ri, dir
}

func TestRefreshDiffWithTTL_DoesNotRefreshWithinTTL(t *testing.T) {
	ri, _ := newTestRepo(t)

	// Record the diff state after first refresh
	require.True(t, ri.diffInitialized, "newTestRepo should have initialized the diff")

	// Manually set known state to verify TTL prevents re-computation.
	ri.LinesAdded = 100
	ri.LinesDeleted = 50
	ri.lastDiffRefresh = time.Now()

	// Called immediately after lastDiffRefresh → should be skipped within TTL
	ri.RefreshDiffWithTTL(1 * time.Second)

	// Values must be unchanged because refresh should have been skipped
	require.Equal(t, 100, ri.LinesAdded, "RefreshDiffWithTTL within TTL must skip recomputation")
	require.Equal(t, 50, ri.LinesDeleted, "RefreshDiffWithTTL within TTL must skip recomputation")
}

func TestRefreshDiffWithTTL_RefreshesAfterTTLExpiry(t *testing.T) {
	ri, dir := newTestRepo(t)

	// Force a refresh with a very old lastDiffRefresh to force TTL expiry
	ri.lastDiffRefresh = time.Now().Add(-10 * time.Second)
	ri.diffInitialized = true

	// Set known stale values to verify recomputation
	ri.LinesAdded = 99
	ri.LinesDeleted = 88

	ri.RefreshDiffWithTTL(100 * time.Millisecond)

	// After TTL, the diff must be recomputed from the real repo state
	// committed.txt has 2 lines committed and 3 in worktree → 1 added, 0 deleted
	require.Equal(t, 1, ri.LinesAdded,
		"after TTL expiry, RefreshDiffWithTTL must recompute added lines")
	require.Equal(t, 0, ri.LinesDeleted,
		"after TTL expiry, RefreshDiffWithTTL must recompute deleted lines")
	_ = dir
}

func TestRefreshDiffWithTTL_InitializesWhenNotYetLoaded(t *testing.T) {
	dir := t.TempDir()
	_, err := runGitCommand(dir, "init")
	require.NoError(t, err)
	_, err = runGitCommand(dir, "config", "user.email", "test@example.com")
	require.NoError(t, err)
	_, err = runGitCommand(dir, "config", "user.name", "Test")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o644))
	_, err = runGitCommand(dir, "add", "file.txt")
	require.NoError(t, err)
	_, err = runGitCommand(dir, "commit", "-m", "init")
	require.NoError(t, err)

	repo, err := gogit.PlainOpenWithOptions(dir, &gogit.PlainOpenOptions{DetectDotGit: true})
	require.NoError(t, err)

	ri := &RepoInfo{repo: repo}
	require.False(t, ri.diffInitialized,
		"newly created RepoInfo must not have diff initialized")

	// Clean repo, no working tree changes
	ri.RefreshDiffWithTTL(time.Hour)
	require.True(t, ri.diffInitialized, "RefreshDiffWithTTL should initialize the diff")
	require.Equal(t, 0, ri.LinesAdded, "clean repo should have no additions")
	require.Equal(t, 0, ri.LinesDeleted, "clean repo should have no deletions")
}

func TestIsClean_UsesCachedDiffAfterInitialization(t *testing.T) {
	ri, _ := newTestRepo(t)

	// Contents of committed.txt in worktree is 3 lines vs 2 committed → dirty
	require.False(t, ri.IsClean(), "working tree has an added line, so IsClean must be false")

	// Now simulate a clean state (revert the modification)
	worktree, err := ri.repo.Worktree()
	require.NoError(t, err)
	_, err = runGitCommand(ri.ProjectRoot, "checkout", "--", "committed.txt")
	require.NoError(t, err)
	require.NoError(t, worktree.Reset(&gogit.ResetOptions{Mode: gogit.HardReset}))

	// Manually update the diff to reflect clean state
	ri.LinesAdded = 0
	ri.LinesDeleted = 0
	ri.diffInitialized = true

	// IsClean should use cached values, not re-run git
	require.True(t, ri.IsClean(), "IsClean should report clean after diff is updated")
	require.True(t, ri.diffInitialized, "IsClean should not reset diffInitialized")

	// Change the underlying worktree again — cache is now stale, which is
	// expected because RefreshDiff is not re-run on every IsClean call.
	require.NoError(t, os.WriteFile(filepath.Join(ri.ProjectRoot, "committed.txt"), []byte("one\n"), 0o644))

	// Still returns stale cached value — this is the intended behaviour:
	// IsClean avoids subprocess per call, and higher-level code calls
	// RefreshDiff at logical points (submit prompt, tool call success).
	require.True(t, ri.IsClean(),
		"IsClean should use cached values without triggering RefreshDiff")
}

func TestIsClean_InitializesDiffOnFirstCall(t *testing.T) {
	dir := t.TempDir()
	_, err := runGitCommand(dir, "init")
	require.NoError(t, err)
	_, err = runGitCommand(dir, "config", "user.email", "test@example.com")
	require.NoError(t, err)
	_, err = runGitCommand(dir, "config", "user.name", "Test")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\n"), 0o644))
	_, err = runGitCommand(dir, "add", "file.txt")
	require.NoError(t, err)
	_, err = runGitCommand(dir, "commit", "-m", "init")
	require.NoError(t, err)

	repo, err := gogit.PlainOpenWithOptions(dir, &gogit.PlainOpenOptions{DetectDotGit: true})
	require.NoError(t, err)

	ri := &RepoInfo{repo: repo}
	require.False(t, ri.diffInitialized, "before RefreshDiff/IsClean, diff must not be initialized")

	// Clean repo → true
	require.True(t, ri.IsClean(), "clean repo should be reported as clean")
	require.True(t, ri.diffInitialized, "IsClean should trigger diff initialization")

	// Now make it dirty and call IsClean again
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\ntwo\n"), 0o644))

	// diffInitialized is already true → it will return the CACHED clean result.
	// This is the optimization — to get current state, caller must RefreshDiff.
	require.True(t, ri.IsClean(),
		"after init, IsClean must use cached values (caller must Refresh for fresh state)")

	// But after refreshing, IsClean reflects the dirty state
	ri.RefreshDiff()
	require.False(t, ri.IsClean(), "after RefreshDiff, IsClean must report dirty")
}
