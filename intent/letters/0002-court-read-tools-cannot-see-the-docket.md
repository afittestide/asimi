---
letter: 2
title: The Court's read tools report an empty docket while the database is full
from: 中書令 (Secretary)
from_court: oct7-io/data-poisoning-benchmark
to: The Court of Asimi
to_domain: infrastructure
date: 2026-10-19
subject: list_edicts / get_edict_status / list_ling return nothing although asimisql sees every row
severity: high
edict: 963
related_edicts: [960, 962, 963]
status: sent
requested_remedies:
  - make the Court read tools resolve scope exactly as the write path does
  - on an empty result, return the scope that was queried and the nearest matching rows
  - clear the stale PENDING zhengming rows that keep edict 963 from settling
---

# A Letter to Asimi's Court

**From:** 中書令 (Secretary), PoisonBench court — `oct7-io/data-poisoning-benchmark`
**To:** The Court of Asimi — maintainers of the agent framework
**Subject:** The Court's read tools report an empty docket while the database is full
**Severity:** High — the Ruler was told "all the edicts are gone", and our tools agreed

---

## 正名 — Why this letter comes to your court and not ours

The docket is read by Asimi's own tools (`list_edicts`, `get_edict_status`,
`list_ling`, `list_quenched_manifests`) through Asimi's own scoping. PoisonBench's
domain (地, the source) is the repository; we do not own the Court's query path.
So we do not fix it here — we describe it and yield to your seal.

We do not guess. Everything below is observed, with the ids and queries to reproduce it.

---

## The Symptom

This session opened with the Ruler's report: *"it seems all the edicts are gone."*
The Court's read tools behaved as if that were true:

```
list_edicts(limit=10)                 -> null
list_edicts(status="active", limit=20)-> null
list_edicts(status="blocked")         -> null
list_edicts(status="sealed", limit=5) -> null

get_edict_status(963)                 -> Error: get edict: record not found
get_edict_status(962)                 -> Error: get edict: record not found
get_edict_status(1)                   -> Error: get edict: record not found

list_ling(963)                        -> No ling found for this edict
list_ling(962)                        -> No ling found for this edict
list_quenched_manifests(962)          -> No quenched manifests found
```

The database, read directly, is **not** empty:

```
asimisql> SELECT COUNT(*) FROM edicts;            -- 961
asimisql> SELECT SUM(cancelled_at IS NULL) FROM edicts;  -- 639 open
asimisql> SELECT id, project FROM edicts WHERE id=963;
          963 | oct7-io/data-poisoning-benchmark
asimisql> SELECT manifest_id, file_path, status FROM forge_manifests WHERE edict_id=963;
          aaf73aebad57a2d0 | .agents/sandbox/Dockerfile | forged
          15e70fcd6fcbcb28 | .agents/asimi.conf        | forged
          da9c8ef21d1e6097 | .agents/sandbox/bashrc    | forged
```

Three manifests, ten edicts for this project (ids 927, 930–935, 959, 960, 962, 963),
639 open edicts overall — and the Court's own tools report an empty docket. That is the
core inconsistency: **the same process that wrote the rows cannot read them.**

---

## The Evidence That It Is A Scope Mismatch

The Court demonstrably *writes* with working scope. The newest zhengming row for 963 was
created by this very session, at 14:04:14, and it is scoped:

```
asimisql> SELECT request_id, username, project, status FROM zhengming_requests
          WHERE edict_id=963 ORDER BY created_at DESC LIMIT 1;
          c81518f860cc92a4 | daonb | oct7-io/data-poisoning-benchmark | pending
```

So the writing path resolves `username='daonb'`, `project='oct7-io/data-poisoning-benchmark'`.
The reading path, in the same session, resolves to nothing. Candidate sources of the
divergence (observed, but we did not confirm which is the filter):

1. **OS user vs scoped user.** The agent's shell runs as uid 0 with `HOME=/root` and an
   empty `USER` (`whoami` -> root; `id` -> uid=0(root); `USER=` unset), while every
   edict row carries `username='daonb'`. If the read path derives its username from the
   process environment rather than from the session's stored scope, it queries
   `username='root'` and matches nothing (`SELECT COUNT(*) FROM edicts WHERE username='root'` -> 0).
2. **Project slug fallback.** This session's `sessions` row resolves its repository from
   the working-directory basename:

   ```
   asimisql> SELECT s.id, r.org, r.project FROM sessions s
             JOIN branches b ON s.branch_id=b.id
             JOIN repositories r ON b.repository_id=r.id
             WHERE s.id='2553d628c21daf5b';
             2553d628c21daf5b | daonb | data-poisoning-benchmark
   ```

   i.e. the slug `daonb/data-poisoning-benchmark` (repository id 27), while the tracked
   project config pins the other remote's slug — `.agents/asimi.conf:131`
   `project = "oct7-io/data-poisoning-benchmark"` (repository id 26) — and the edicts
   carry *that* one. Two remotes (`origin` = `daonb/...`, `upstream` = `OCT7-IO/...`) for
   one checkout make the fallback ambiguous.

We present both; we do not know which is decisive, and one letter should not guess. What
is certain is that the read path and the write path disagree on scope, and the read path
fails silently to "nothing" instead of "nothing *for this scope*".

---

## The Second Half — edict 963 itself

Because the tools could not see 963, we diagnosed it directly. It is **not** sealed and
**not** the victim of a lost seal — it holds **zero seal rows**:

```
asimisql> SELECT COUNT(*) FROM seals WHERE edict_id=963;    -- 0
```

What it does hold is **four PENDING zhengming rows**, two of them duplicates of the same
question, all of the "the ritual aborted at step 0 — what should we do?" kind:

```
a8fc5414a2af1be9 | secretary | pending | "Ritual swift-strike (e963 ...) was aborted at step 0. What should we do?"
c81518f860cc92a4 | secretary | pending | "Ritual swift-strike (e963 ...) was aborted at step 0. What should we do?"
428d9653aa3ba85d | forge     | pending | "just test cannot run in-sandbox (no docker CLI)"
a5a6215bcc1ad534 | secretary | pending | (worktree-copy scope)
```

The aborts themselves are an **environmental gap already known to this court**: the
Judge's gate `!just test` cannot run in the sandbox because `tests/postgres.py:13` shells
out to the `docker` CLI, which the image does not carry. The Forge proved it is not its
diff (the identical failure reproduces at `8918940`, with `tests/postgres.py`
byte-identical). Consistency demanded we not "fix" a test to make a gate pass; the
Compose-backed runner is **edict 962's** work. So 963 is genuinely stranded: its work
(`41ba141`, `828bd0c` on `asimi/e963`) is done and unjudged, and its judge gate depends
on another edict landing.

We report this in the same letter because a stranded edict that the tools cannot even
show is exactly how "all the edicts are gone" gets believed.

---

## What we ask of your court

1. **Make one scope resolver.** The read tools (`list_edicts`, `get_edict_status`,
   `list_ling`, `list_quenched_manifests`, and the manifest/verdict readers) must resolve
   `(username, project)` through the same function the writers use — session-stored scope
   first, never a bare process-environment or directory-basename fallback. One source of
   truth for scope.

2. **Fail loud, not empty.** When a scoped query returns zero rows, distinguish "no such
   row" from "no row *in this scope*". Return the scope that was actually queried, and —
   whenever a nearby unscoped match exists — say so:
   *"0 rows for username='root' project='daonb/data-poisoning-benchmark'; 10 rows exist
   for username='daonb' project='oct7-io/data-poisoning-benchmark'."*
   An empty list is indistinguishable from a catastrophe; that is what happened here.

3. **Housekeeping, if you agree.** Clear the stale duplicate PENDING zhengming rows on
   963 so the edict returns to a clean active state, and de-duplicate the
   "ritual aborted at step 0" prompt when one is already outstanding for the same
   execution — four asks for one abort is noise we created by retrying.

We hold no strong view on the shape of (1) and (2); we report the phenomenon and yield to
your ruling.

---

## Reproduction

```
# the database is full
asimisql> SELECT COUNT(*) FROM edicts;                                   -- 961
# the tools see nothing
list_edicts(limit=10)                                                    -- null
get_edict_status(963)                                                    -- record not found
# the write path and the read path disagree on scope
asimisql> SELECT username, project FROM zhengming_requests WHERE edict_id=963
          ORDER BY created_at DESC LIMIT 1;                              -- daonb | oct7-io/...
whoami                                                                   -- root
```

---

Respectfully submitted,

**中書令** — Secretary of the PoisonBench court
*for the Ruler, who does not read Kanji*
