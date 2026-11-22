# Lings

> *"Divide and conquer."* — attributed to Philip II of Macedon

A **Ling** (令) is a task order — a sub-task inside an edict's execution plan.
When an edict is large enough to need planning, the **War** ministry decomposes
it into lings with dependencies, and the Forge executes them.

## Why lings exist

A single edict can be a large piece of work. Rather than hand a wall of work
to one minister, the court breaks it into **atomic, ordered** pieces, each of
which can be tracked, retried, and sealed on its own.

A typical decomposition:

```
Edict: "Add user authentication"
  |
  +-- Ling 1: "Create user model"
  +-- Ling 2: "Add login endpoint"   (depends on Ling 1)
  +-- Ling 3: "Add logout endpoint"  (depends on Ling 1)
  +-- Ling 4: "Write auth tests"     (depends on Lings 2, 3)
```

The dependencies form a **DAG** — a directed graph with no cycles. The court
refuses a plan with a cycle (you cannot wait on yourself).

## The four statuses

| Status | Meaning |
|--------|---------|
| **`pending`** | Not yet started |
| **`in_progress`** | Being worked on |
| **`done`** | Completed successfully |
| **`blocked`** | Waiting on a dependency, or on Zhengming |

> **`done`, not `completed`.** The code's vocabulary is exact: a finished ling
> is **`done`**. If you see "completed" in older material, read it as `done`.

## Rules for a good ling

The War ministry lives under strict constraints when it plans:

- **All changes to one file belong in one ling.** Do not split a file's edits
  across lings — it invites conflicts and muddles the seal.
- **A ling must be atomic, clear, and testable.** A ling that cannot be
  verified is not a ling.
- **No cycles.** Dependencies must form a DAG.
- **Exact ids.** Lings are addressed by their full id (e.g.
  `74183c66ba0507ba`), never by shorthand.

## Watching lings run

In a fork-capable ritual like **castle-siege**, the implementing step **forks
over the lings** — each ling is implemented in turn (batch size 1 by default),
and completion is recorded as each finishes. You see this as the edict's work
progressing, step by step.

You can ask about an edict's lings at any time through the Secretary, or via
the event ledger (lings emit `ling_created`, `ling_started`, `ling_completed`,
and `ling_failed` events).

Next: the workflows that orchestrate the ministers — **rituals**.
