package worktree

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"al.essio.dev/pkg/shellescape"

	"github.com/afittestide/asimi/internal/runners"
)

// ResolvedTarget is a `:switch` target mapped onto the repository layout.
type ResolvedTarget struct {
	// Branch is the git branch the Court should be re-based onto.
	Branch string
	// WorktreeRel is the project-root-relative worktree path, or "" when the
	// target lives in the main working tree.
	WorktreeRel string
	// IsWorktree reports whether WorktreeRel names a linked worktree.
	IsWorktree bool
	// EdictID is non-zero when the target is an edict ("e<N>"); Ensure
	// creates the worktree when it is missing.
	EdictID uint
}

var edictTargetPattern = regexp.MustCompile(`^e([0-9]+)$`)

// Resolve maps a `:switch` target to its branch and worktree:
//   - "main"/"master" → the repository's default branch in the main tree.
//   - "e<N>"          → the edict's branch asimi/e<N> and its worktree; the
//     caller must Ensure the worktree when it does not exist yet.
//   - anything else   → the branch as-is, unless `git worktree list` shows a
//     linked worktree on that branch, in which case its path is returned.
//
// A worktree path outside root is never accepted: the answer degrades to the
// branch string with no worktree path, mirroring RepoInfo.EffectiveRoot()'s
// project-root join semantics.
func Resolve(ctx context.Context, r runners.Runner, root, target string) (ResolvedTarget, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return ResolvedTarget{}, fmt.Errorf("worktree: empty switch target")
	}
	if root == "" {
		return ResolvedTarget{}, fmt.Errorf("worktree: empty project root")
	}

	switch target {
	case "main", "master":
		return ResolvedTarget{Branch: DefaultBranch(ctx, r, root)}, nil
	}

	if m := edictTargetPattern.FindStringSubmatch(target); m != nil {
		n, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			return ResolvedTarget{}, fmt.Errorf("worktree: invalid edict target %q: %w", target, err)
		}
		id := uint(n)
		return ResolvedTarget{
			Branch:      Branch(id),
			WorktreeRel: RelPath(id),
			IsWorktree:  true,
			EdictID:     id,
		}, nil
	}

	// A plain branch: a linked worktree of the same branch wins, otherwise
	// the branch string is used as-is with no worktree path.
	if rel, ok := LinkedWorktreePath(ctx, r, root, target); ok {
		return ResolvedTarget{Branch: target, WorktreeRel: rel, IsWorktree: true}, nil
	}
	return ResolvedTarget{Branch: target}, nil
}

// LinkedWorktreePath returns the project-root-relative path of the linked
// worktree checked out on branch. ok is false when branch has no linked
// worktree, when the path is the main tree itself, or when git cannot be read.
func LinkedWorktreePath(ctx context.Context, r runners.Runner, root, branch string) (string, bool) {
	if r == nil || root == "" || branch == "" {
		return "", false
	}
	cmd := fmt.Sprintf("git -C %s worktree list --porcelain", shellescape.Quote(root))
	out, err := r.Run(ctx, runners.Input{
		Command:        cmd,
		Description:    "list git worktrees",
		BypassApproval: true,
	})
	if err != nil || (out.ExitCode != "" && out.ExitCode != "0") {
		return "", false
	}

	want := "refs/heads/" + branch
	path := ""
	for _, line := range strings.Split(out.Output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			if strings.TrimPrefix(line, "branch ") != want {
				path = ""
				continue
			}
			if rel, ok := relativeWorktree(root, path); ok {
				return rel, true
			}
			path = ""
		}
	}
	return "", false
}

// relativeWorktree converts a worktree listed by git into a project-root
// relative path, rejecting the main tree and any path escaping root.
func relativeWorktree(root, abs string) (string, bool) {
	if abs == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == "" {
		return "", false
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}
