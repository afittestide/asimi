# Edicts

> *"The superior man is modest in his speech, but exceeds in his actions."*
> — Confucius, *Analects* 4.24

An **Edict** (詔令) is a work order. It is the unit of intent in Asimi: when you
want something done, you issue an edict, and the court carries it through to a
seal.

## What an edict is made of

| Field | Meaning |
|-------|---------|
| **Intent** | What you want done — the *what* and *why* |
| **Summary** | A one-line title for the docket |
| **Issue reference** | Optional link to a GitHub issue (`owner/repo#N`) |
| **Session** | The conversation the edict was born in |
| **Status** | Where it stands: `active`, `blocked`, `sealed`, or `cancelled` |

## The four statuses

The court is honest about state, and an edict has exactly four legal states:

| Status | Meaning |
|--------|---------|
| **`active`** | In flight — a ritual is running, or work waits to begin |
| **`blocked`** | Halted — waiting on *you* (a pending Zhengming), or on a gate |
| **`sealed`** | Crowned — the full seal chain was granted |
| **`cancelled`** | Abandoned before completion |

Status is **derived** from the seals and zhengming records, not stored on the
edict itself. An edict is `sealed` precisely because the seal chain is complete
— see [The Seal Chain](../court/seal-chain.md).

> **A note on "phases."** You may see the sequence
> `brewing → planning → forging → judging → reviewing` in older material. That
> is the *shape of a ritual's steps*, not an edict's status. The status you act
> on is always one of the four above.

## The seal chain, at the edict level

Every edict ascends through the same gates:

1. **Judge's Seal** — the change is sound (tests pass).
2. **Chancellor's Seal** — the change is well-formed (obeys the Imperial Code).
3. **Ruler's Seal** — *you* authorize it.
4. **頒行 (promulgation)** — the work lands, the edict seals.

A seal is recorded once and never rewritten. If you later change an edict's
intent, its existing seals are **invalidated** — the work must be re-sealed
against the new intent. This is why changing intent is a serious act.

## Issuing, inspecting, and cancelling

You issue an edict by describing your intent to the Secretary. You inspect one
by asking, or via the `:edicts` command:

```
:edicts          - Manage edicts (read, enact, seal, resume, cancel)
```

You can always cancel an edict that has not sealed:

```
Cancel edict-123
```

> **Edicts are not rewritten silently.** Replacing an edict's intent overwrites
> it and voids its seals. A **cancelled or sealed** edict is not a valid target
> for replacement — for those, issue a new edict.

## Where edicts are remembered

The event ledger (**天**) records every edict's life: creation, assignment,
phase changes, ritual steps, seals, and cancellation. To reconstruct what
happened to any edict, read its events — never rely on memory. See
[Court Status & Troubleshooting](../reference/troubleshooting.md).

Next: the sub-tasks an edict decomposes into — **lings**.
