package repo

import (
	"bufio"
	"bytes"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
)

// RepoInfo contains information about the git repository and worktree.
// This type is shared between main and court packages.
type RepoInfo struct {
	ProjectRoot     string
	WorktreePath    string
	Branch          string
	IsWorktree      bool
	IsMain          bool
	Slug            string // Project slug (e.g., "owner/repo")
	LinesAdded      int    // Lines added in working directory
	LinesDeleted    int    // Lines deleted in working directory
	status          string
	repo            *gogit.Repository
	lastDiffRefresh time.Time
	diffInitialized bool
}

// GetRepoInfo returns information about the current git repository and worktree
func GetRepoInfo() RepoInfo {

	cwd, err := os.Getwd()
	if err != nil {
		return RepoInfo{}
	}

	// Detect if we're in a worktree by checking if .git is a file vs directory
	gitPath := filepath.Join(cwd, ".git")
	info, err := os.Stat(gitPath)
	isWorktree := err == nil && !info.IsDir()

	// Find project root - for worktrees, find the main repo root
	var projectRoot string
	var worktreePath string

	if isWorktree {
		// Read .git file to find the main repository
		mainRepoRoot, err := findMainRepoRoot(cwd)
		if err == nil && mainRepoRoot != "" {
			projectRoot = mainRepoRoot
			// Calculate worktree path relative to main repo
			relPath, err := filepath.Rel(projectRoot, cwd)
			if err == nil && relPath != "." {
				worktreePath = relPath
			}
		} else {
			// Fallback to current directory if we can't find main repo
			projectRoot = cwd
		}
	} else {
		// Not a worktree, use current directory
		projectRoot = cwd
	}

	// Get current branch and status using go-git
	branch := ""
	status := ""
	repo, err := gogit.PlainOpenWithOptions(cwd, &gogit.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err == nil {
		ref, err := repo.Head()
		if err == nil {
			if ref.Name().IsBranch() {
				branch = ref.Name().Short()
			} else {
				// Detached HEAD — check if we're in a rebase, which preserves
				// the original branch name in .git/rebase-merge/head-name.
				branch = branchFromDetachedHead(projectRoot)
				if branch == "" {
					branch = ref.Hash().String()[:7]
				}
			}
		} else if isWorktree {
			// go-git doesn't fully support worktrees, try reading HEAD directly
			branch = readBranchFromWorktree()
		}
		// Get git status - only read once at startup
		// Skip in tests to avoid slow git operations
		if os.Getenv("ASIMI_SKIP_GIT_STATUS") == "" {
			status = readShortStatus(repo)
		}
	} else if isWorktree {
		// go-git failed, try reading branch directly from worktree
		branch = readBranchFromWorktree()
	}

	// Detect if branch is a main branch
	isMain := isMainBranch(branch)

	repoInfo := RepoInfo{
		ProjectRoot:  projectRoot,
		WorktreePath: worktreePath,
		Branch:       branch,
		IsWorktree:   isWorktree,
		IsMain:       isMain,
		Slug:         projectSlug(projectRoot),
		status:       status,
		repo:         repo,
	}

	// Calculate diff stats if we have a repo and not skipping git status
	if repo != nil && os.Getenv("ASIMI_SKIP_GIT_STATUS") == "" {
		// TODO: this should run in the background and update the status when done
		repoInfo.RefreshDiff()
	}

	return repoInfo
}

// GetRepoInfoForRoot returns information about the git repository at the given root directory.
// It is identical to GetRepoInfo but takes an explicit root instead of calling os.Getwd().
func GetRepoInfoForRoot(root string) RepoInfo {
	// Detect if we're in a worktree by checking if .git is a file vs directory
	gitPath := filepath.Join(root, ".git")
	info, err := os.Stat(gitPath)
	isWorktree := err == nil && !info.IsDir()

	// Find project root - for worktrees, find the main repo root
	var projectRoot string
	var worktreePath string

	if isWorktree {
		// Read .git file to find the main repository
		mainRepoRoot, err := findMainRepoRoot(root)
		if err == nil && mainRepoRoot != "" {
			projectRoot = mainRepoRoot
			// Calculate worktree path relative to main repo
			relPath, err := filepath.Rel(projectRoot, root)
			if err == nil && relPath != "." {
				worktreePath = relPath
			}
		} else {
			// Fallback to current directory if we can't find main repo
			projectRoot = root
		}
	} else {
		// Not a worktree, use given root
		projectRoot = root
	}

	// Get current branch and status using go-git
	branch := ""
	status := ""
	repo, err := gogit.PlainOpenWithOptions(root, &gogit.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err == nil {
		ref, err := repo.Head()
		if err == nil {
			if ref.Name().IsBranch() {
				branch = ref.Name().Short()
			} else {
				// Detached HEAD — check if we're in a rebase, which preserves
				// the original branch name in .git/rebase-merge/head-name.
				branch = branchFromDetachedHead(projectRoot)
				if branch == "" {
					branch = ref.Hash().String()[:7]
				}
			}
		} else if isWorktree {
			// go-git doesn't fully support worktrees, try reading HEAD directly
			branch = readBranchFromWorktreeAt(root)
		}
		// Get git status - only read once at startup
		// Skip in tests to avoid slow git operations
		if os.Getenv("ASIMI_SKIP_GIT_STATUS") == "" {
			status = readShortStatus(repo)
		}
	} else if isWorktree {
		// go-git failed, try reading branch directly from worktree
		branch = readBranchFromWorktreeAt(root)
	}

	// Detect if branch is a main branch
	isMain := isMainBranch(branch)

	repoInfo := RepoInfo{
		ProjectRoot:  projectRoot,
		WorktreePath: worktreePath,
		Branch:       branch,
		IsWorktree:   isWorktree,
		IsMain:       isMain,
		Slug:         projectSlug(projectRoot),
		status:       status,
		repo:         repo,
	}

	// Calculate diff stats if we have a repo and not skipping git status
	if repo != nil && os.Getenv("ASIMI_SKIP_GIT_STATUS") == "" {
		// TODO: this should run in the background and update the status when done
		repoInfo.RefreshDiff()
	}

	return repoInfo
}

// GetStatus returns a short git status string (e.g., "[!+]")
// This is cached at the time RepoInfo is created and does not update dynamically
func (r *RepoInfo) GetStatus() string {
	return r.status
}

// IsClean returns true if the working tree has no changes
func (r *RepoInfo) IsClean() bool {
	if r == nil {
		return true
	}
	if !r.diffInitialized {
		r.RefreshDiff()
		if r.repo != nil {
			r.status = readShortStatus(r.repo)
		}
	}
	return r.LinesAdded == 0 && r.LinesDeleted == 0
}

// RefreshDiff recalculates diff statistics using gogit
func (r *RepoInfo) RefreshDiff() {
	if r == nil || r.repo == nil {
		return
	}

	r.lastDiffRefresh = time.Now()
	r.diffInitialized = true

	// TODO: add support for non-worktree branches
	worktree, err := r.repo.Worktree()
	if err != nil {
		return
	}

	repoPath := worktree.Filesystem.Root()
	if repoPath == "" {
		repoPath = r.ProjectRoot
	}
	if repoPath == "" {
		return
	}

	headExists := true
	if _, err := r.repo.Head(); err != nil {
		headExists = false
	}

	added := 0
	deleted := 0

	if headExists {
		added, deleted = collectDiffFromGit(repoPath, []string{"--numstat", "HEAD"})
	} else {
		slog.Debug("Getting diff for initial commit")
		a, d := collectDiffFromGit(repoPath, []string{"--numstat", "--cached"})
		added += a
		deleted += d

		a, d = collectDiffFromGit(repoPath, []string{"--numstat"})
		added += a
		deleted += d
	}

	r.LinesAdded = added
	r.LinesDeleted = deleted
	slog.Debug("Refreshed git diff", "+", added, "-", deleted)
}

// RefreshDiffWithTTL recalculates diff statistics only if more than ttl has elapsed
// since the last refresh.
func (r *RepoInfo) RefreshDiffWithTTL(ttl time.Duration) {
	if r == nil || r.repo == nil {
		return
	}
	if r.diffInitialized && time.Since(r.lastDiffRefresh) < ttl {
		return
	}
	r.RefreshDiff()
}

func (r *RepoInfo) BranchSlugOrDefault() string {
	slug := SanitizeSegment(r.Branch)
	// TODO: pick a better default branch for cases when working outside repo,
	//       to avoid a collision make it illegal in git.
	if slug == "" {
		return "main"
	}

	return slug
}

// SanitizeSegment normalizes a string for use as a URL/storage segment.
func SanitizeSegment(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	prevHyphen := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen {
			b.WriteRune('-')
			prevHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// findMainRepoRoot returns the nearest ancestor directory (including start)
// that contains a project marker like .git or go.mod. Falls back to start.
// findMainRepoRoot finds the main repository root when in a worktree
// by reading the .git file and extracting the main repo path from the gitdir
func findMainRepoRoot(worktreeDir string) (string, error) {
	gitPath := filepath.Join(worktreeDir, ".git")

	// Read the .git file
	content, err := os.ReadFile(gitPath)
	if err != nil {
		return "", err
	}

	// Parse gitdir: path
	// Example: gitdir: /Users/daonb/src/asimi-cli/.git/worktrees/GH33-fix-worktrees
	gitdirLine := strings.TrimSpace(string(content))
	if !strings.HasPrefix(gitdirLine, "gitdir: ") {
		return "", fmt.Errorf("invalid .git file format")
	}

	gitdir := strings.TrimPrefix(gitdirLine, "gitdir: ")

	// The gitdir points to: <main-repo>/.git/worktrees/<worktree-name>
	// We need to extract <main-repo> from this path
	// Split by "/.git/worktrees/" to get the main repo path
	parts := strings.Split(gitdir, "/.git/worktrees/")
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected gitdir format: %s", gitdir)
	}

	mainRepoRoot := parts[0]
	return mainRepoRoot, nil
}

// readBranchFromWorktree reads the branch name directly from a git worktree
func readBranchFromWorktree() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return readBranchFromWorktreeAt(cwd)
}

// readBranchFromWorktreeAt reads the branch name directly from a git worktree at dir
func readBranchFromWorktreeAt(dir string) string {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}

	// If .git is a directory, not a worktree
	if info.IsDir() {
		return ""
	}

	// Read the .git file to get the actual git directory
	content, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}

	// Parse gitdir: path
	gitdirLine := strings.TrimSpace(string(content))
	if !strings.HasPrefix(gitdirLine, "gitdir: ") {
		return ""
	}

	gitdir := strings.TrimPrefix(gitdirLine, "gitdir: ")

	// Read HEAD from the worktree git directory
	headPath := filepath.Join(gitdir, "HEAD")
	headContent, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}

	// Parse ref: refs/heads/branch
	headLine := strings.TrimSpace(string(headContent))
	if strings.HasPrefix(headLine, "ref: ") {
		ref := strings.TrimPrefix(headLine, "ref: ")
		if strings.HasPrefix(ref, "refs/heads/") {
			return strings.TrimPrefix(ref, "refs/heads/")
		}
	}

	// If HEAD is detached, return the short hash
	if len(headLine) >= 7 {
		return headLine[:7]
	}

	return ""
}

// branchFromDetachedHead recovers the original branch name when HEAD is detached
// during git operations that preserve it (rebase, bisect). Returns "" if the
// branch can't be recovered.
func branchFromDetachedHead(projectRoot string) string {
	for _, candidate := range []struct {
		subdir, file string
	}{
		{".git/rebase-merge", "head-name"}, // rebase --merge (default)
		{".git/rebase-apply", "head-name"}, // rebase --apply (legacy)
		{".git", "BISECT_START"},           // git bisect
	} {
		path := filepath.Join(projectRoot, candidate.subdir, candidate.file)
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		ref := strings.TrimSpace(string(content))
		if strings.HasPrefix(ref, "refs/heads/") {
			return strings.TrimPrefix(ref, "refs/heads/")
		}
	}
	return ""
}

// isMainBranch checks if the given branch name is considered a main branch.
// Currently checks for "main" and "master".
func isMainBranch(branch string) bool {
	return branch == "main" || branch == "master"
}

func readShortStatus(repo *gogit.Repository) string {
	if repo == nil {
		return ""
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return ""
	}

	status, err := worktree.Status()
	if err != nil {
		return ""
	}

	return summarizeStatus(status)
}

// projectSlug returns the project slug (e.g., "owner/repo") from the git remote origin URL.
// Returns an empty string if the project root is not a git repository or has no remote.
func projectSlug(projectRoot string) string {
	if projectRoot == "" {
		return ""
	}

	remoteURL, err := GitRemoteOriginURL(projectRoot)
	if err != nil || remoteURL == "" {
		return ""
	}

	owner, repo := ParseGitRemote(remoteURL)
	if owner == "" || repo == "" {
		return ""
	}
	// Canonical slug is always lowercase; Git remotes may carry uppercase.
	// Preserve the "/" separator (required by podman image names).
	return strings.ToLower(owner) + "/" + strings.ToLower(repo)
}

func collectDiffFromGit(repoPath string, opts []string) (int, int) {

	args := []string{"diff"}
	args = append(args, opts...)
	output, err := runGitCommand(repoPath, args...)
	if err != nil {
		slog.Debug("git diff failed", "args", strings.Join(args, " "), "err", err, "output", strings.TrimSpace(string(output)))
		return 0, 0
	}

	return parseGitNumstat(output)
}

func runGitCommand(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
func summarizeStatus(status gogit.Status) string {
	if len(status) == 0 {
		return ""
	}

	var modified, added, deleted, untracked, renamed int
	for _, entry := range status {
		switch entry.Staging {
		case gogit.Modified, gogit.UpdatedButUnmerged:
			modified++
		case gogit.Added, gogit.Copied:
			added++
		case gogit.Deleted:
			deleted++
		case gogit.Renamed:
			renamed++
		case gogit.Untracked:
			untracked++
		}
		switch entry.Worktree {
		case gogit.Modified, gogit.UpdatedButUnmerged:
			modified++
		case gogit.Added, gogit.Copied:
			added++
		case gogit.Deleted:
			deleted++
		case gogit.Renamed:
			renamed++
		case gogit.Untracked:
			untracked++
		}
	}

	var builder strings.Builder
	builder.WriteString("[")
	if modified > 0 {
		builder.WriteString("!")
	}
	if added > 0 {
		builder.WriteString("+")
	}
	if deleted > 0 {
		builder.WriteString("-")
	}
	if renamed > 0 {
		builder.WriteString("→")
	}
	if untracked > 0 {
		builder.WriteString("?")
	}
	builder.WriteString("]")

	result := builder.String()
	if result == "[]" {
		return ""
	}
	return result
}

// GitRemoteOriginURL returns the remote origin URL for the given working directory.
func GitRemoteOriginURL(workingDir string) (string, error) {
	cmd := exec.Command("git", "-C", workingDir, "config", "--get", "remote.origin.url")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// ParseGitRemote extracts owner and repo name from a git remote URL.
func ParseGitRemote(remote string) (owner, repo string) {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimSuffix(remote, ".git")
	if remote == "" {
		return "", ""
	}

	if strings.Contains(remote, "://") {
		if u, err := url.Parse(remote); err == nil {
			segments := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(segments) >= 2 {
				owner = segments[len(segments)-2]
				repo = segments[len(segments)-1]
			}
			return owner, repo
		}
	}

	if strings.Contains(remote, ":") {
		parts := strings.SplitN(remote, ":", 2)
		if len(parts) == 2 {
			path := strings.Trim(parts[1], "/")
			segments := strings.Split(path, "/")
			if len(segments) >= 2 {
				owner = segments[len(segments)-2]
				repo = segments[len(segments)-1]
			}
		}
	}

	return owner, repo
}

func parseGitNumstat(data []byte) (int, int) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	buf := make([]byte, 1024)
	scanner.Buffer(buf, 1024*1024)

	added := 0
	deleted := 0

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}

		added += parseNumStatValue(parts[0])
		deleted += parseNumStatValue(parts[1])
	}

	if err := scanner.Err(); err != nil {
		slog.Debug("failed to parse git numstat output", "err", err)
	}

	return added, deleted
}

func parseNumStatValue(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" {
		return 0
	}
	val, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return val
}

// diffLines performs a simple line-based diff
func diffLines(original, current []string) (added int, deleted int) {
	// Create maps for quick lookup
	origMap := make(map[string]int)
	currMap := make(map[string]int)

	for _, line := range original {
		origMap[line]++
	}

	for _, line := range current {
		currMap[line]++
	}

	// Count added lines (in current but not in original)
	for line, count := range currMap {
		origCount := origMap[line]
		if count > origCount {
			added += count - origCount
		}
	}

	// Count deleted lines (in original but not in current)
	for line, count := range origMap {
		currCount := currMap[line]
		if count > currCount {
			deleted += count - currCount
		}
	}

	return added, deleted
}
