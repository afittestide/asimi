package court

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"al.essio.dev/pkg/shellescape"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/worktree"
	"github.com/afittestide/asimi/storage"
)

// defaultLingMinister is used when a ling omits its minister.
const defaultLingMinister = "forge"

// lingBatchSize returns the concurrent-ling bound. Lings of one edict share the
// edict's worktree (e923), so parallelism never collides on a working tree; the
// bound protects the machine (N LLM sessions plus N containers), not the tree.
// The Ministry of War remains responsible for planning file-disjoint parallel
// lings — the scheduler cannot verify that (accepted risk, ruler ruling
// 2026-10-25).
func (s *Court) lingBatchSize() int {
	if s != nil && s.config != nil && s.config.LingParallelism > 0 {
		return s.config.LingParallelism
	}
	return config.DefaultLingParallelism
}

// PublishLingCreated records a ling_created event for observability. It never
// starts work — ignition is a separate trigger.
func (s *Court) PublishLingCreated(key storage.EdictKey, lingID string) {
	if s == nil {
		return
	}
	s.PublishEvent(key, storage.EventLingCreated, storage.JSON{"ling_id": lingID})
}

// lingIgniteLock returns the per-edict mutex serializing ignition runs. It
// prevents overlapping triggers (e.g. two insert_ling calls racing) from
// double-running a ling.
func (s *Court) lingIgniteLock(edictID uint) *sync.Mutex {
	s.lingIgniteMu.Lock()
	defer s.lingIgniteMu.Unlock()
	if s.lingIgniting == nil {
		s.lingIgniting = make(map[uint]*sync.Mutex)
	}
	mu, ok := s.lingIgniting[edictID]
	if !ok {
		mu = &sync.Mutex{}
		s.lingIgniting[edictID] = mu
	}
	return mu
}

// lingCommitLock returns the per-edict mutex serializing the commit step of the
// edict's concurrent lings. Without it two lings finishing together collide on
// the shared worktree's .git/index. It serializes the fast commit, not the LLM
// work (ruler ruling R4).
func (s *Court) lingCommitLock(edictID uint) *sync.Mutex {
	s.lingCommitMu.Lock()
	defer s.lingCommitMu.Unlock()
	if s.lingCommitting == nil {
		s.lingCommitting = make(map[uint]*sync.Mutex)
	}
	mu, ok := s.lingCommitting[edictID]
	if !ok {
		mu = &sync.Mutex{}
		s.lingCommitting[edictID] = mu
	}
	return mu
}

// TriggerLingIgnition is the automatic batch-completion trigger for insert_ling.
// It returns immediately; ignition runs in the background, serialized per edict.
func (s *Court) TriggerLingIgnition(key storage.EdictKey) {
	if s == nil || s.db == nil || key.ID == 0 {
		return
	}
	// Nil-config safe: bootstrap fills config lazily, so a nil or absent
	// config must behave as "disabled" and still record the skip in the ledger.
	if s.config == nil || !s.config.LingIgnitionEnabled {
		s.logger.Info("ling ignition disabled by config", "edict_id", key.ID)
		s.PublishEvent(key, storage.EventLingIgnitionSkipped, storage.JSON{
			"reason":       "ling_ignition_disabled_by_config",
			"pending_e923": true,
		})
		return
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		mu := s.lingIgniteLock(key.ID)
		mu.Lock()
		defer mu.Unlock()
		if err := s.igniteLings(ctx, key); err != nil {
			s.logger.Warn("ling ignition failed", "edict_id", key.ID, "error", err)
		}
	}()
}

// projectRoot returns the Court's main project root, or "" when unset.
func (s *Court) projectRoot() string {
	if s == nil {
		return ""
	}
	if s.ritualGuard != nil && s.ritualGuard.repoInfo.ProjectRoot != "" {
		return s.ritualGuard.repoInfo.ProjectRoot
	}
	if s.toolCtxRepoInfo != nil {
		return s.toolCtxRepoInfo.ProjectRoot
	}
	return ""
}

// ensureEdictWorktree makes the edict's worktree exist before any ling runs. It
// is independent of any ritual: insert_ling may be called by any minister and no
// producing ritual need have opted in. Mutations always run on the host (the
// container bind-mounts a single worktree, so a sandboxed shell cannot add a
// sibling worktree next to itself). It returns the absolute worktree path.
func (s *Court) ensureEdictWorktree(ctx context.Context, edictID uint) (string, error) {
	root := s.projectRoot()
	if root == "" {
		return "", fmt.Errorf("ling ignition: project root not set")
	}
	abs, _, _, _, err := worktree.Ensure(ctx, s.repoMutationRunner(root), root, edictID, s.ignoreCopyGlobs())
	if err != nil {
		return "", err
	}
	return abs, nil
}

// igniteLings loads the edict's lings and runs every pending ling whose
// dependencies are all done through the shared DAG engine. Lings with unmet
// intra-batch deps are started by the engine as their deps complete.
//
// Before dispatching anything it ensures the edict's worktree. On that error
// ignition aborts: falling back to the main tree is exactly the unsafe
// condition this edict removes.
func (s *Court) igniteLings(ctx context.Context, key storage.EdictKey) error {
	var lings []storage.Ling
	if err := s.db.Where("edict_id = ? AND username = ? AND project = ?",
		key.ID, key.Username, key.Project).
		Order("created_at ASC").
		Find(&lings).Error; err != nil {
		return fmt.Errorf("load lings for ignition: %w", err)
	}

	units := make([]dagUnit, 0, len(lings))
	for _, l := range lings {
		if l.Status != storage.LingPending {
			continue // already running or done
		}
		units = append(units, dagUnit{
			ID:     l.LingID,
			DepIDs: []string(l.Dependencies),
			Index:  len(units),
			Item:   l.LingID,
		})
	}
	if len(units) == 0 {
		return nil
	}

	abs, err := s.ensureEdictWorktree(ctx, key.ID)
	if err != nil {
		s.PublishEvent(key, storage.EventLingFailed, storage.JSON{
			"error":  fmt.Sprintf("ensure edict worktree: %v", err),
			"reason": "worktree_ensure_failed",
		})
		return fmt.Errorf("ensure edict worktree for ignition: %w", err)
	}

	engine := &dagEngine{
		units:           units,
		batchSize:       s.lingBatchSize(),
		name:            "ling ignition",
		allowIncomplete: true,
		seedDone: func(done map[string]bool) {
			for _, l := range lings {
				if l.Status == storage.LingDone {
					done[l.LingID] = true
				}
			}
		},
		run: func(ctx context.Context, u dagUnit) error {
			return s.runLing(ctx, key, u, abs)
		},
	}
	_, err = engine.execute(ctx)
	return err
}

// runLing executes a single ling as a plain session inside the edict's
// worktree: it resolves the ling's minister (defaulting to forge), creates a
// session bound to a per-ling runner whose effective root is the worktree, and
// runs the ling's description as the prompt. On success the ling's own paths are
// committed on the edict branch under the per-edict commit mutex; failures are
// reported as ling_failed events and leave the work for inspection.
func (s *Court) runLing(ctx context.Context, key storage.EdictKey, u dagUnit, wtRoot string) error {
	lingID := u.ID
	var ling storage.Ling
	if err := s.db.First(&ling, "ling_id = ?", lingID).Error; err != nil {
		return fmt.Errorf("load ling %s: %w", lingID, err)
	}

	ministerID := ling.Minister
	if ministerID == "" {
		ministerID = defaultLingMinister
	}
	minister := s.GetMinister(ministerID)
	if minister == nil {
		err := fmt.Errorf("minister %q not found", ministerID)
		s.failLing(key, lingID, ministerID, err)
		return err
	}

	s.PublishEvent(key, storage.EventLingStarted, storage.JSON{
		"ling_id":     lingID,
		"minister":    ministerID,
		"worktree":    wtRoot,
		"is_worktree": wtRoot != "",
	})
	s.db.Model(&storage.Ling{}).
		Where("ling_id = ? AND username = ? AND project = ?", lingID, key.Username, key.Project).
		Update("status", storage.LingInProgress)

	// Snapshot the worktree's dirty paths before the ling runs. "Its own paths"
	// are the paths that were clean before the run and dirty after it. A path
	// already dirty when the ling started (another concurrent ling, or the
	// forge's own uncommitted work) is never claimed by this ling. The snapshot
	// never blocks or serializes the ling: a slow snapshot must not hold up
	// parallel work.
	before := s.dirtyPaths(ctx, wtRoot)

	// Per-ling runner + repoInfo: the shared ToolContext.RepoInfo is a pointer
	// read by every concurrent ling, and the edict runner is shared by this
	// edict's lings. Mutating either while other lings run is a data race, so
	// each ling derives its own runner aimed at the edict worktree.
	repoInfo := repo.RepoInfo{}
	if s.ritualGuard != nil {
		repoInfo = s.ritualGuard.repoInfo
	}
	root := s.projectRoot()
	repoInfo.ProjectRoot = root
	repoInfo.WorktreePath = worktree.RelPath(key.ID)
	repoInfo.IsWorktree = true
	repoInfo.Branch = worktree.Branch(key.ID)

	runner, err := s.lingRunner(ctx, key.ID, repoInfo)
	if err != nil {
		s.failLing(key, lingID, ministerID, err)
		return err
	}

	cfg := s.sessionCfg
	if cfg != nil {
		clone := *cfg
		clone.WorkingDir = wtRoot
		clone.RepoInfo = repoInfo
		clone.Runner = runner
		cfg = &clone
	}

	session, err := CreateSession(minister, minister.Model(), cfg, s.notify, ritualChannelID(key.ID), key)
	if err != nil {
		s.failLing(key, lingID, ministerID, err)
		return err
	}
	if _, err := session.AskWithStreaming(ctx, ling.Description, nil); err != nil {
		s.failLing(key, lingID, ministerID, err)
		return err
	}

	// Commit the ling's own paths on the edict branch before marking it done.
	if err := s.commitLing(ctx, key, ling, wtRoot, before); err != nil {
		s.failLing(key, lingID, ministerID, err)
		return err
	}

	// Same status update as record_ling_completed.
	if err := s.db.Model(&storage.Ling{}).
		Where("ling_id = ? AND username = ? AND project = ?", lingID, key.Username, key.Project).
		Update("status", storage.LingDone).Error; err != nil {
		s.failLing(key, lingID, ministerID, err)
		return err
	}
	s.PublishEvent(key, storage.EventLingCompleted, storage.JSON{
		"ling_id":  lingID,
		"minister": ministerID,
	})
	return nil
}

// lingRunner returns the shell runner a ling's session uses. The edict's own
// runner (e934) is preferred — it keeps the edict's container and its flatline
// bookkeeping. It is aimed at the edict worktree, which is the same value every
// ling of that edict wants, so re-aiming it is idempotent and its internal mutex
// keeps it race-free. When no per-edict runner factory is wired (tests, edict 1)
// a fresh host runner is derived rather than re-pointing the Court-wide runner,
// which other edicts share.
func (s *Court) lingRunner(ctx context.Context, edictID uint, repoInfo repo.RepoInfo) (runners.Runner, error) {
	var base runners.Runner
	if s.newEdictRunner != nil {
		r, err := s.RunnerForEdict(edictID)
		if err != nil {
			return nil, fmt.Errorf("derive ling runner: %w", err)
		}
		base = r
	} else {
		base = s.repoMutationRunner(repoInfo.EffectiveRoot())
	}
	if base == nil {
		return nil, fmt.Errorf("no runner configured for ling ignition")
	}
	if setter, ok := base.(runners.RepoInfoSetter); ok {
		setter.SetRepoInfo(repoInfo)
	}
	return base, nil
}

// dirtyPaths returns the set of paths git reports as dirty under root. A gitless
// tree, an unreadable root, or a git failure yields an empty set: path
// attribution degrades to "commit everything dirty", which is no worse than the
// pre-worktree behaviour.
func (s *Court) dirtyPaths(ctx context.Context, root string) map[string]bool {
	paths := map[string]bool{}
	if root == "" {
		return paths
	}
	out, err := s.repoMutationRunner(root).Run(ctx, runners.Input{
		Command:        fmt.Sprintf("git -C %s status --porcelain", shellescape.Quote(root)),
		Description:    "snapshot ling dirty paths",
		BypassApproval: true,
	})
	if err != nil || (out.ExitCode != "" && out.ExitCode != "0") {
		return paths
	}
	for _, line := range strings.Split(out.Output, "\n") {
		if len(line) < 4 {
			continue
		}
		// Porcelain v1: XY<space>PATH, or "XY ORIG -> PATH" for renames.
		p := line[3:]
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		p = strings.Trim(strings.TrimSpace(p), `"`)
		if p != "" {
			paths[p] = true
		}
	}
	return paths
}

// commitLing stages the ling's own paths (the dirty set now minus paths already
// dirty before it ran) and commits them on the edict branch. The per-edict commit
// mutex serializes the fast stage+commit so two lings finishing together never
// collide on .git/index; the LLM work runs unlocked. A clean ling (nothing new of
// its own) is not an error — it simply commits nothing.
func (s *Court) commitLing(ctx context.Context, key storage.EdictKey, ling storage.Ling, wtRoot string, before map[string]bool) error {
	if wtRoot == "" {
		return nil
	}
	mu := s.lingCommitLock(key.ID)
	mu.Lock()
	defer mu.Unlock()

	now := s.dirtyPaths(ctx, wtRoot)
	var paths []string
	for p := range now {
		if !before[p] {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil
	}

	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, shellescape.Quote(p))
	}
	addCmd := fmt.Sprintf("git -C %s add -- %s", shellescape.Quote(wtRoot), strings.Join(quoted, " "))
	if err := runGitSerialized(ctx, s.repoMutationRunner(wtRoot), addCmd, "stage ling paths"); err != nil {
		return err
	}

	// Only commit when something is actually staged: a path that vanished
	// between the snapshot and the stage leaves nothing to commit.
	staged, _ := s.repoMutationRunner(wtRoot).Run(ctx, runners.Input{
		Command:        fmt.Sprintf("git -C %s diff --cached --name-only", shellescape.Quote(wtRoot)),
		Description:    "check staged ling paths",
		BypassApproval: true,
	})
	if strings.TrimSpace(staged.Output) == "" {
		return nil
	}

	subject := fmt.Sprintf("feat: %s (ling %s) e%d", shortDescription(ling.Description), ling.LingID, key.ID)
	commitCmd := fmt.Sprintf("git -C %s commit -m %s", shellescape.Quote(wtRoot), shellescape.Quote(subject))
	return runGitSerialized(ctx, s.repoMutationRunner(wtRoot), commitCmd, "commit ling paths")
}

// shortDescription collapses a ling description to a single commit-subject line.
func shortDescription(desc string) string {
	desc = strings.Join(strings.Fields(desc), " ")
	if len(desc) > 60 {
		desc = strings.TrimSpace(desc[:60])
	}
	if desc == "" {
		return "ling work"
	}
	return desc
}

// runGitSerialized runs a git mutation, treating a non-zero exit as an error.
func runGitSerialized(ctx context.Context, r runners.Runner, command, description string) error {
	out, err := r.Run(ctx, runners.Input{
		Command:        command,
		Description:    description,
		BypassApproval: true,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", description, err)
	}
	if out.ExitCode != "" && out.ExitCode != "0" {
		return fmt.Errorf("%s: exit %s: %s", description, out.ExitCode, strings.TrimSpace(out.Output))
	}
	return nil
}

// sweepLingWorktrees removes any legacy per-ling worktrees nested under the
// edict's worktree before the edict worktree itself is removed (a parent with
// live children cannot be removed reliably). Per-ling worktrees are superseded
// by e923 — lings share the edict worktree — so this is a migration sweep for
// trees left behind by earlier shapes; it is safe when none exist.
func (s *Court) sweepLingWorktrees(ctx context.Context, edictID uint) {
	root := s.projectRoot()
	if root == "" {
		return
	}
	parent := filepath.Join(root, worktree.RelPath(edictID))
	if _, err := os.Stat(parent); err != nil {
		return
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	r := s.repoMutationRunner(root)
	for _, e := range entries {
		// A linked worktree carries a .git file (not directory); anything else
		// is not ours to remove.
		if !e.IsDir() || len(e.Name()) < 2 || e.Name()[0] != 'l' {
			continue
		}
		child := filepath.Join(parent, e.Name())
		if info, err := os.Stat(filepath.Join(child, ".git")); err != nil || info.IsDir() {
			continue
		}
		branch := fmt.Sprintf("%s/%s", worktree.Branch(edictID), e.Name())
		if err := worktree.Remove(ctx, r, root, child, branch); err != nil {
			s.logger.Warn("could not sweep nested ling worktree", "edict", edictID, "path", e.Name(), "error", err)
		}
	}
}

func (s *Court) failLing(key storage.EdictKey, lingID, ministerID string, err error) {
	s.PublishEvent(key, storage.EventLingFailed, storage.JSON{
		"ling_id":  lingID,
		"minister": ministerID,
		"error":    err.Error(),
	})
}
