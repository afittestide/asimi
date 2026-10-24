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
// dependencies inside it, and carries over the allowlisted gitignored files
// (copyGlobs). It returns the absolute path, the project-root-relative path,
// the branch name, and whether the worktree was created (false when an
// existing one was reused).
//
// Creating depends on the runner supporting shell pipelines: dependency
// install and ignored-file copy are single shell invocations.
func Ensure(ctx context.Context, r runners.Runner, root string, edictID uint, copyGlobs []string) (abs, rel, branch string, created bool, err error) {
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

	if err := CopyIgnored(ctx, r, root, abs, copyGlobs); err != nil {
		return "", "", "", false, err
	}

	return abs, rel, branch, true, nil
}

// CopyIgnored copies the gitignored files matching copyGlobs from root into
// the worktree at abs, so local-only config travels with the worktree. The
// globs are an opt-in allowlist: only what the caller declares is copied.
// With no globs there is nothing to copy and the call is a no-op, matching
// the gitless path.
func CopyIgnored(ctx context.Context, r runners.Runner, root, abs string, copyGlobs []string) error {
	if r == nil {
		return fmt.Errorf("worktree: no runner configured")
	}
	if len(copyGlobs) == 0 {
		return nil
	}
	// Build the pathspec: `-- <glob>...` restricts git to the declared
	// allowlist, so an unbounded `--ignored` sweep can never happen. Quoted
	// globs survive the shell, but git expands them as pathspecs (shell
	// globbing does not apply to an unexpanded literal in a non-matching
	// directory).
	quoted := make([]string, 0, len(copyGlobs))
	for _, g := range copyGlobs {
		quoted = append(quoted, shellescape.Quote(g))
	}
	// .agents/worktrees is itself gitignored, so without the tar --exclude a
	// new worktree would copy every other worktree — and any nested copies
	// they carry — into itself, growing the tree on every edict. --exclude
	// must precede -T: GNU tar treats it as positional and silently drops it
	// when it follows a file list.
	cmd := fmt.Sprintf("cd %s && git ls-files -z --others --ignored --exclude-standard -- %s | tar --null --exclude=%s -T - -cf - | tar -xf - -C %s",
		shellescape.Quote(root), strings.Join(quoted, " "), shellescape.Quote(".agents/worktrees/*"), shellescape.Quote(abs))
	if _, err := run(ctx, r, cmd, "copy allowlisted gitignored files into worktree"); err != nil {
		return err
	}
	return nil
}

// fsyncLooseObjects is git's per-invocation durability switch: it makes git
// fsync each loose object it writes. The default does not, so a just-written
// object can be a torn read for a concurrent process on a stacked filesystem
// (incident 9f8b980c998a6c32: "loose object … is corrupt" after git re-read a
// tree it had itself just written, the object later proving byte-valid).
const fsyncLooseObjects = "-c core.fsync=loose-object"

// LooseObjectCorrupt reports whether err is git's "loose object … is corrupt".
// That failure is a transient torn read of a freshly written object rather than
// disk rot, so the caller may retry the operation once before giving up.
func LooseObjectCorrupt(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "loose object") && strings.Contains(msg, "is corrupt")
}

// AbortRebase clears leftover rebase state in the worktree at abs so a retry can
// start. A rebase writes a tree for every replayed commit, so a torn read at any
// pick other than the last leaves .git/rebase-merge behind (or rebase-apply for
// the apply backend); git then refuses a fresh `git rebase` with "It seems that
// there is already a rebase-merge directory". Without this the seal's single
// retry is a no-op that wedges the worktree (incident 9f8b980c998a6c32).
//
// The command is unconditional — git's "No rebase in progress?" answer is the
// common case and is treated as success (abort has nothing to undo), so the
// caller never needs to probe the git dir. A real abort failure is returned: the
// retry must not proceed on stale in-progress rebase state.
func AbortRebase(ctx context.Context, r runners.Runner, abs string) error {
	if r == nil {
		return fmt.Errorf("worktree: no runner configured")
	}
	if abs == "" {
		return fmt.Errorf("worktree: missing path")
	}
	const description = "abort stale rebase before retry"
	abortCmd := fmt.Sprintf("git -C %s rebase --abort", shellescape.Quote(abs))
	out, err := r.Run(ctx, runners.Input{
		Command:        abortCmd,
		Description:    description,
		BypassApproval: true,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", description, err)
	}
	if out.ExitCode != "" && out.ExitCode != "0" {
		// git exits 128 with "No rebase in progress?" when there is nothing to
		// abort; that is the clean case, not a failure.
		if strings.Contains(out.Output, "No rebase in progress") {
			return nil
		}
		return fmt.Errorf("%s: exit %s: %s", description, out.ExitCode, strings.TrimSpace(out.Output))
	}
	return nil
}

// SquashMergeToDefault rebases the branch at abs onto the repository's default
// branch, then squash-merges it into root as a single commit with the given
// message. On a rebase conflict it returns an error and leaves the worktree
// intact so the forge (工部) can resolve the conflict before the ruler reviews.
//
// Loose-object writes are fsynced (core.fsync=loose-object) rather than left to
// git's default: the object writer and the reader that rejected it can be the
// same process, so an unsynced object is a retryable torn read, not corruption.
//
// A fully-subsumed branch is a successful no-op, not an error: when the
// branch's patch is already on the target under a different commit (hand
// rescue, cherry-pick, a sibling pane), the rebase skips it and the squash
// merge stages nothing — git reports "Already up to date" and exits 0. The
// commit is then skipped and nil is returned, so the caller proceeds to Remove
// and the edict ascends with its worktree retired.
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

	rebaseCmd := fmt.Sprintf("git -C %s %s rebase %s", shellescape.Quote(abs), fsyncLooseObjects, shellescape.Quote(defaultBranch))
	if _, err := run(ctx, r, rebaseCmd, "rebase edict worktree onto default branch"); err != nil {
		return fmt.Errorf("rebase onto %s failed (worktree left intact): %w", defaultBranch, err)
	}

	mergeCmd := fmt.Sprintf("git -C %s %s merge --squash %s", shellescape.Quote(root), fsyncLooseObjects, shellescape.Quote(branch))
	if _, err := run(ctx, r, mergeCmd, "squash-merge edict worktree"); err != nil {
		return fmt.Errorf("squash-merge failed: %w", err)
	}

	// A branch whose patch is already on the target rebases to an ancestor of
	// HEAD, so merge --squash succeeds having staged nothing. Committing then
	// fails with "no changes added to commit" and the seal would report a false
	// failure; an empty index means the work is already landed.
	stagedCmd := fmt.Sprintf("git -C %s diff --cached --name-only", shellescape.Quote(root))
	staged, err := run(ctx, r, stagedCmd, "check squash-merge staged changes")
	if err != nil {
		return fmt.Errorf("inspect staged changes: %w", err)
	}
	if strings.TrimSpace(staged.Output) == "" {
		return nil
	}

	msg := strings.TrimSpace(summary)
	if msg == "" {
		msg = "chore: merge edict worktree"
	}
	commitCmd := fmt.Sprintf("git -C %s %s commit -m %s", shellescape.Quote(root), fsyncLooseObjects, shellescape.Quote(msg))
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
	removeCmd := fmt.Sprintf("git -C %s %s worktree remove %s", shellescape.Quote(root), fsyncLooseObjects, shellescape.Quote(abs))
	if _, err := run(ctx, r, removeCmd, "remove sealed worktree"); err != nil {
		return err
	}
	pruneCmd := fmt.Sprintf("git -C %s %s worktree prune", shellescape.Quote(root), fsyncLooseObjects)
	if _, err := run(ctx, r, pruneCmd, "prune worktree metadata"); err != nil {
		return err
	}
	branchCmd := fmt.Sprintf("git -C %s %s update-ref -d refs/heads/%s", shellescape.Quote(root), fsyncLooseObjects, shellescape.Quote(branch))
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
