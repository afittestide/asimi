# Letter to the Forge: Repair the release-version Ritual's Verdict Gate

By the Ruler's ruling of 2026-10-01 (zhengming request `fa1293134291c9ac`):
**"Let's stop this ritual... add a letter in intent/letters to the forge
ordering him to fix the ritual in `.agents/rituals.yaml`."**

The release-version ritual for **0.13.6** was halted mid-flight. The release
artifacts themselves are sound and already committed (`98ac88f80`, "chore:
releasing Asimi 0.13.6"); this letter concerns the **ritual definition**, which
cannot currently complete on edict 1. Verify, do not rediscover — the evidence
below is exact.

## Problem

`release-version` reaches its judge step (`verify-release-readiness`), the Judge
returns **PASS**, and then the step's `then` gate fails anyway:

```
then:
  - the verdicts are passed
  - record the judge's seal
on_failure: goto
on_failure_target: prepare-changelog
```

The observed error was:

```
verdict check failed: 98 manifest(s) rejected: [storage/migrate_edict_composite_pk.go,
shogunate.test, ... CHANGELOG.md docs/roadmap.md Justfile tui.go ...]
```

Because `on_failure: goto prepare-changelog` routes a gate failure back to the
first step, the ritual **restarts forever**. This is a deterministic infinite
loop, not a flaky failure.

## Root cause

`check_verdicts_passed` (`court/ritual_expressions.go:1190-1216`) selects the
**latest manifest per `(edict_id, username, project, file_path)`** and fails if
**any** of them is `rejected`. It applies **no time, ling, or commit filter**.

`release-version` runs under **edict 1** — the permanent Court-Infrastructure
edict, reserved precisely so system operations have an audit trail. Edict 1's
manifest set therefore accumulates rejected rows across the life of the project,
and later work on a *different* `file_path` never supersedes them. Measured
state for `(edict 1, daonb, afittestide/asimi-cli)`:

- **108** latest-per-path manifests, of which **98 are `rejected`**
- Rejections span **2026-04-04 → 2026-10-01**; the newest rejected row
  (`court/ritual_test.go`) predates this release entirely
- 56 of them carry an empty `ling_id` — rows from pre-ling work, un-supersedable

Every other edict is unaffected: its rejected rows are superseded as work
continues. **Edict 1 alone fails this gate permanently.**

## Evidence

- `court/ritual_expressions.go:84` — registers `"the verdicts are passed"` →
  `check_verdicts_passed`.
- `court/ritual_expressions.go:1190-1216` — the latest-per-path query and the
  `rejected` failure. Note the code comment already anticipates reforging
  ("superseded rejected manifests should not trigger false failures") — the
  intent is right, but edict 1 never re-forges the same paths.
- `.agents/rituals.yaml:18` — `release-version`; its judge step's `then` and
  `on_failure: goto prepare-changelog`.
- `ritual_executions`: id `d57555b22632b637`, `state=running`, `edict_id=1`.
- Judge verdict `0888077ba85fa8e8` = PASS; version-bump manifest
  `2ab9aa2dc473bb63` = quenched. The gate failed on **unrelated history**.

### Secondary defect — tests pollute the live DB

Manifests with `ling_id` values `judge-test-cleanup-ritual` and
`judge-test-refactor-handsoff` — **both rejected** — exist in the **live** court
database against **edict 1**. Court tests appear to mint real `forge_manifests`
rows into the production DB instead of an isolated test database. Several of the
98 rejections may originate from test runs. Filed as incident
**`447443b12a816562`** (severity high).

## Change requested

Edit `.agents/rituals.yaml` so `release-version` can complete. Chosen direction
is yours — verify against the ritual engine and raise zhengming if ambiguous.
Candidates, in rough order of preference:

1. **Run the release under a dedicated edict** rather than shared edict 1, so
   its manifest set is not polluted by years of unrelated infrastructure work.
2. **Narrow the gate for this ritual** to the release artifacts it actually
   produces (`CHANGELOG.md`, `internal/utils/asimi_version.go`,
   `docs/roadmap.md`) instead of every latest-per-path manifest on the edict.
3. **Scope `check_verdicts_passed` to manifests created at/after the ritual's
   start** (`ritual_executions.created_at`), so historical rejections cannot
   gate a new ritual.

Whichever you choose, keep the gate's real teeth: a manifest forged by the
**current** ritual that is genuinely rejected must still fail the step.

Also address the test-pollution defect, or file it to the appropriate minister
with a clear repro.

## Acceptance

1. A `release-version` ritual on edict 1 reaches `commit-and-tag` without the
   `verdicts are passed` gate failing on pre-existing rejected manifests.
2. The gate still fails when a manifest forged by the *current* ritual is
   rejected (regression test).
3. Court tests no longer write `forge_manifests` rows into the live database,
   or the behavior is proven intentional and documented.

## Conventions

- Commit prefix `fix:` or `chore:`, ending `e{id}` with the new edict's number.
- Do **not** fold the already-committed 0.13.6 release artifacts
  (`CHANGELOG.md`, `internal/utils/asimi_version.go`) into this work — they are
  committed in `98ac88f80`.
