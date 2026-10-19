// Package worktree manages deterministic per-edict git worktrees.
//
// Each edict gets one worktree at .agents/worktrees/e{ID} on branch
// asimi/e{ID}, reused by every ritual of that edict. All git and shell
// work is delegated to a runners.Runner so the same code path works on
// the host and inside the podman sandbox. The package is deliberately
// small: create/enter, carry ignored files, squash-merge on seal, remove.
package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"al.essio.dev/pkg/shellescape"

	"github.com/afittestide/asimi/internal/runners"
)

// Branch returns the deterministic branch name for an edict.
func Branch(edictID uint) string {
	return fmt.Sprintf("asimi/e%d", edictID)
}

// RelPath returns the worktree path relative to the project root.
func RelPath(edictID uint) string {
	return filepath.Join(".agents", "worktrees", fmt.Sprintf("e%d", edictID))
}

// Ensure idempotently creates the edict's branch and worktree, installs
// dependencies inside it, and carries over gitignored files. It returns the
// absolute path, the project-root-relative path, the branch name, and whether
// the worktree was created (false when an existing one was reused).
//
// Creating depends on the runner supporting shell pipelines: dependency
// install and ignored-file copy are single shell invocations.
func Ensure(ctx context.Context, r runners.Runner, root string, edictID uint) (abs, rel, branch string, created bool, err error) {
	if root == "" {
		return "", "", "", false, fmt.Errorf("worktree: empty project root")
	}
	if r == nil {
		return "", "", "", false, fmt.Errorf("worktree: no runner configured")
	}

	rel = RelPath(edictID)
	abs = filepath.Join(root, rel)
	branch = Branch(edictID)

	// Idempotent reuse: all rituals of an edict share one worktree.
	if _, statErr := os.Stat(abs); statErr == nil {
		return abs, rel, branch, false, nil
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", "", "", false, fmt.Errorf("mkdir worktrees: %w", err)
	}

	addCmd := fmt.Sprintf("git worktree add -B %s %s", shellescape.Quote(branch), shellescape.Quote(abs))
	if _, err := run(ctx, r, addCmd, "create secluded worktree for edict"); err != nil {
		return "", "", "", false, err
	}

	installCmd := fmt.Sprintf("cd %s && just install", shellescape.Quote(abs))
	if _, err := run(ctx, r, installCmd, "install dependencies in worktree"); err != nil {
		return "", "", "", false, err
	}

	if err := CopyIgnored(ctx, r, root, abs); err != nil {
		return "", "", "", false, err
	}

	return abs, rel, branch, true, nil
}

// CopyIgnored copies untracked-ignored files from root into the worktree at
// abs, so local-only files (env files, caches) travel with the worktree.
func CopyIgnored(ctx context.Context, r runners.Runner, root, abs string) error {
	if r == nil {
		return fmt.Errorf("worktree: no runner configured")
	}
	// .agents/worktrees is itself gitignored, so without the tar --exclude a
	// new worktree would copy every other worktree — and any nested copies
	// they carry — into itself, growing the tree on every edict. --exclude
	// must precede -T: GNU tar treats it as positional and silently drops it
	// when it follows a file list.
	cmd := fmt.Sprintf("cd %s && git ls-files -z --others --ignored --exclude-standard | tar --null --exclude=%s -T - -cf - | tar -xf - -C %s",
		shellescape.Quote(root), shellescape.Quote(".agents/worktrees/*"), shellescape.Quote(abs))
	if _, err := run(ctx, r, cmd, "copy gitignored files into worktree"); err != nil {
		return err
	}
	return nil
}

// SquashMergeToDefault rebases the branch at abs onto the repository's default
// branch, then squash-merges it into root as a single commit with the given
// message. On a rebase conflict it returns an error and leaves the worktree
// intact so the forge (工部) can resolve the conflict before the ruler reviews.
//
// The caller owns the commit message shape (AGENTS.md: feat:/bug:/chore: … e<N>).
func SquashMergeToDefault(ctx context.Context, r runners.Runner, root, abs, branch, summary string) error {
	if r == nil {
		return fmt.Errorf("worktree: no runner configured")
	}
	if root == "" || abs == "" || branch == "" {
		return fmt.Errorf("worktree: missing root, path or branch")
	}

	defaultBranch := DefaultBranch(ctx, r, root)

	rebaseCmd := fmt.Sprintf("git -C %s rebase %s", shellescape.Quote(abs), shellescape.Quote(defaultBranch))
	if _, err := run(ctx, r, rebaseCmd, "rebase edict worktree onto default branch"); err != nil {
		return fmt.Errorf("rebase onto %s failed (worktree left intact): %w", defaultBranch, err)
	}

	mergeCmd := fmt.Sprintf("git -C %s merge --squash %s", shellescape.Quote(root), shellescape.Quote(branch))
	if _, err := run(ctx, r, mergeCmd, "squash-merge edict worktree"); err != nil {
		return fmt.Errorf("squash-merge failed: %w", err)
	}

	msg := strings.TrimSpace(summary)
	if msg == "" {
		msg = "chore: merge edict worktree"
	}
	commitCmd := fmt.Sprintf("git -C %s commit -m %s", shellescape.Quote(root), shellescape.Quote(msg))
	if _, err := run(ctx, r, commitCmd, "commit squashed edict worktree"); err != nil {
		return fmt.Errorf("squash commit failed: %w", err)
	}

	return nil
}

// Remove detaches and deletes the worktree at abs and deletes its branch. root
// is the main working tree the commands run against.
//
// The force flags (`git worktree remove --force`, `git branch -D`) are
// forbidden by the sandbox git shim (Li 礼), so removal requires a clean
// worktree (the forge commits its work before the ruler's seal) and the branch
// is deleted via the update-ref plumbing instead. Removal failures are
// surfaced to the caller, which leaves the worktree intact for inspection.
func Remove(ctx context.Context, r runners.Runner, root, abs, branch string) error {
	if r == nil {
		return fmt.Errorf("worktree: no runner configured")
	}
	removeCmd := fmt.Sprintf("git -C %s worktree remove %s", shellescape.Quote(root), shellescape.Quote(abs))
	if _, err := run(ctx, r, removeCmd, "remove sealed worktree"); err != nil {
		return err
	}
	pruneCmd := fmt.Sprintf("git -C %s worktree prune", shellescape.Quote(root))
	if _, err := run(ctx, r, pruneCmd, "prune worktree metadata"); err != nil {
		return err
	}
	branchCmd := fmt.Sprintf("git -C %s update-ref -d refs/heads/%s", shellescape.Quote(root), shellescape.Quote(branch))
	if _, err := run(ctx, r, branchCmd, "delete sealed worktree branch"); err != nil {
		return err
	}
	return nil
}

// DefaultBranch resolves the branch edict worktrees are rebased onto and
// squash-merged into. SquashMergeToDefault commits into root's checked-out
// branch, so that branch is the merge target and the rebase base must equal
// it — otherwise the worktree is replayed through history the target already
// carries. Root's checked-out branch is therefore preferred, then origin/HEAD
// (when it is a locally valid upstream), then "main". Resolution never fails:
// an unreadable answer degrades to the next candidate.
//
// The remote HEAD name is only used when it is a locally valid upstream: a
// local branch b, else the remote-tracking ref origin/b. Returning the bare b
// when only origin/b exists makes git rebase/merge fail with "invalid upstream
// 'b'" (see edict 955).
func DefaultBranch(ctx context.Context, r runners.Runner, root string) string {
	if r == nil || root == "" {
		return "main"
	}
	head := fmt.Sprintf("git -C %s rev-parse --abbrev-ref HEAD", shellescape.Quote(root))
	if out, err := run(ctx, r, head, "resolve default branch"); err == nil {
		// "HEAD" means detached — no checked-out branch to merge into.
		if b := strings.TrimSpace(out.Output); b != "" && b != "HEAD" {
			return b
		}
	}
	symRef := fmt.Sprintf("git -C %s symbolic-ref --short refs/remotes/origin/HEAD", shellescape.Quote(root))
	if out, err := run(ctx, r, symRef, "resolve default branch"); err == nil {
		if b := strings.TrimSpace(out.Output); b != "" {
			b = strings.TrimPrefix(b, "origin/")
			if refExists(ctx, r, root, "refs/heads/"+b) {
				return b
			}
			if refExists(ctx, r, root, "refs/remotes/origin/"+b) {
				return "origin/" + b
			}
		}
	}
	return "main"
}

// refExists reports whether ref resolves in root.
func refExists(ctx context.Context, r runners.Runner, root, ref string) bool {
	cmd := fmt.Sprintf("git -C %s rev-parse --verify --quiet %s", shellescape.Quote(root), shellescape.Quote(ref))
	out, err := run(ctx, r, cmd, "check ref "+ref)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out.Output) != ""
}

// run executes a command with approval bypassed and treats a non-zero exit
// code as an error. Runners report command failures through ExitCode rather
// than the returned error, so both must be checked.
func run(ctx context.Context, r runners.Runner, command, description string) (runners.Output, error) {
	out, err := r.Run(ctx, runners.Input{
		Command:        command,
		Description:    description,
		BypassApproval: true,
	})
	if err != nil {
		return out, fmt.Errorf("%s: %w", description, err)
	}
	if out.ExitCode != "" && out.ExitCode != "0" {
		return out, fmt.Errorf("%s: exit %s: %s", description, out.ExitCode, strings.TrimSpace(out.Output))
	}
	return out, nil
}
