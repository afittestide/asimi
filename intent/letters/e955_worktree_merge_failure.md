# Letter to the Forge: Make the Worktree-Seal Failure Land Without Panicking

By the Judge's ruling of 2026-10-18 (verdict `c8d7b109a3527fe0`, and its
per-manifest siblings). Edict 955's manifest set is **rejected**. Verify, do not
rediscover — the evidence below is exact and the tree in question is still in
front of you.

The three defects the edict set out to fix (A: `DefaultBranch` resolving to a
stale `origin/HEAD`; B: silent merge failure; C: `transition_edict` sealing
without publishing) are all addressed in the uncommitted borderlands of
`.agents/worktrees/e955`. The work is close. But it cannot ascend: **its own
acceptance gate, `just test`, fails.**

## What the Judge ran, and what happened

Tree under trial: worktree `.agents/worktrees/e955`, branch `asimi/e955`,
`HEAD dcf3ec618`. Its committed tree is **identical to `main` a2d5f42fb**
(`git diff --name-only main asimi/e955` → empty); the change lives entirely in
**9 modified, unstaged files**:

```
court/court.go            court/ritual_manager.go     court/tools/builtin.go
court/tools/edict.go      court/tools/tool_context.go internal/worktree/worktree.go
internal/worktree/worktree_test.go storage/court_schema.go  tui.go
```

```
$ cd .agents/worktrees/e955 && go test -run TestMergeEdictWorktree_ConflictLeavesWorktreeIntact ./court/
--- FAIL: TestMergeEdictWorktree_ConflictLeavesWorktreeIntact (0.00s)
panic: runtime error: invalid memory address or nil pointer dereference [SIGSEGV]
  log/slog.(*Logger).Warn
  court.(*RitualGuard).PublishEvent   court/ritual_manager.go:174
  court.(*Court).PublishEvent         court/court.go:996
  court.(*Court).mergeEdictWorktree   court/court.go:1587
  court.(*Court).handleSealGranted    court/court.go:1554
  court.TestMergeEdictWorktree_ConflictLeavesWorktreeIntact  court/ritual_expressions_test.go:3082
FAIL github.com/afittestide/asimi/court
```

`go test ./...` reports **exactly this one failure** (court); every other package
is green, and `go vet ./...` is clean. On `main` the same test passes. This is a
deterministic panic, not a flaky assertion.

## Root cause

The new code publishes on failure:

```go
s.PublishEvent(s.EdictKey(edictID), storage.EventWorktreeMergeFailed, storage.JSON{...})
```

which routes into `RitualGuard.PublishEvent` (`court/ritual_manager.go:174`):

```go
select {
case rg.eventCh <- Event{...}:
default:
    rg.logger.Warn("event channel full, persisted to DB only", "type", eventType)  // ← :174
}
```

The test builds a **bare guard literal**:

```go
s.ritualGuard = &RitualGuard{MinisterBase: &MinisterBase{repoInfo: repo.RepoInfo{ProjectRoot: root}}}
```

That guard has a **nil `eventCh` and a nil `logger`**. A nil channel is never
sendable, so the `select` always takes the `default` branch and dereferences the
nil logger → SIGSEGV. On `main`, `mergeEdictWorktree` returned void and published
nothing, so the fixture's missing logger went unnoticed. Publishing exposes it.

Fix direction is yours; two viable shapes, and the choice is a design question,
not a guess:

1. **Harden the guard** so a nil logger (and, arguably, a nil `eventCh`) degrades
   safely — this is the same "resolution never fails" contract `DefaultBranch`
   already follows, and it protects every future publisher, not just this one.
2. **Give the fixture a real guard** — either `slog.New(slog.NewTextHandler(io.Discard, nil))`
   plus a buffered `eventCh`, or construct it via `NewRitualGuard(RitualGuardOpts{Base: ...})`
   the way `court/ritual_manager_test.go` and `court/court_test.go` do.

Whichever you pick, the test must actually assert the new behavior.

## Unmet acceptance criteria

The approved plan (`edicts.intent` for 955, section 4) requires test additions
in **existing** files. `grep` confirms **none of these assertions exist** — only
`internal/worktree/worktree_test.go` was touched:

- `court/ritual_expressions_test.go` — a `SquashMergeToDefault` failure publishes
  `EventWorktreeMergeFailed` **with a non-empty reason**, and the seal is still
  recorded. The current test only asserts the *absence* of `merge --squash` and
  `worktree remove`; it does not inspect the event, and it panics before reaching
  either assertion.
- `court/ritual_manager_test.go` — `EventWorktreeMergeFailed` yields the
  `"Edict N sealed but work NOT merged: <reason>"` notification (and the
  merged/cleanup variant when `merged: true`).
- `tui_test.go` — the failure renders a warning/error and is **not** reported as
  plain success.

The new event wiring is therefore effectively untested outside the path that
panics. Add the assertions the plan names; do not start a new test file
(AGENTS.md: few test files).

## Provenance notes for your reforging

- All 20 manifests for this change are marked `rejected` (`forge_manifests`,
  `edict_id=955`); only the two earlier-round `quenched` rows remain. They are
  immutable — reforging is the way forward.
- The manifests **predate the tree under trial**: e.g. manifest
  `46c200ff5a883d27` records `court/court.go` sha `673c4e73…`, but the live file
  hashes `55b0277f…`, and the change is uncommitted so no commit-hash provenance
  exists. Re-`create_manifest` after the fixes so the forged SHAs match what the
  Judge will actually test.

## Acceptance

1. `just test`, `just lint`, `just build` all pass in the e955 worktree.
2. `TestMergeEdictWorktree_ConflictLeavesWorktreeIntact` completes without a
   panic; a rebase conflict leaves the worktree intact (no `merge --squash`, no
   `worktree remove`) **and** publishes `EventWorktreeMergeFailed` with a
   non-empty `reason`.
3. The `ritual_manager` notification and the `tui` rendering assertions from the
   plan exist and pass.
4. A failed seal, on every path (event and `transition_edict`), is never a silent
   no-op and never panics on a nil dependency.

## Conventions

- Commit prefix `bug:`, ending `e955`.
- Return the work through judgment when green; do not seal it yourself — the seal
  path is exactly what this edict is repairing.
