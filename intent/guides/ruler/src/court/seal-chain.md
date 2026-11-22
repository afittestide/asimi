# The Seal Chain

> *"If you want to know the road ahead, ask those coming back."*
> — Chinese proverb

A change is not finished when it is written. It is finished when it has been
**weighed, reviewed, and authorized** — and the record of that authorization
is a **seal**.

The **seal chain** is the path an edict travels from *proposal* to *law*. At each
gate a different minister — never the author — examines the work and either
applies its seal or refuses. Only when the chain is complete does the change
*ascend* into the land of the living (現行).

## The gates

```
    Judge's Seal  ──►  Chancellor's Seal  ──►  Ruler's Seal  ──►  頒行
    "It is sound"     "It is well-formed"     "It is my will"     (promulgation)
       天                  人/法典                人                現行
```

| Gate | Who | What it certifies | Realm |
|------|-----|-------------------|-------|
| **Judge's Seal** | Judge (刑部) | The change is *sound* — tests pass, coverage holds | 天 |
| **Chancellor's Seal** | Chancellor (門下侍中) | The change is *well-formed* — it obeys the Imperial Code | 人 / 法典 |
| **Ruler's Seal** | **You** | The change is *willed* — you authorize it | 人 |
| **頒行 (promulgation)** | the court | The change is *promulgated* into 現行 | 地 → 現行 |

Each seal is a distinct act, made by a distinct hand. No minister can grant
another's seal. The Judge cannot declare the code beautiful; the Chancellor
cannot declare the tests green; and **nobody** can grant your seal but you.

## Why the order is fixed

The chain is not arbitrary. Each gate depends on the last, because each
*reasons about a different thing*:

- Testing a change that the code review will later reject is wasted effort — so
  **soundness comes first**.
- Reviewing a change whose tests don't yet pass would mean reviewing a moving
  target — so **you review what is proven**.
- Authorizing a change nobody verified would be guesswork — and **guessing is
  treason**.

> **The pen and the bench live in different hands.** The Judge's verdict and
> the evidence it cites are separate: the runner banks testimony, only the
> Judge's pen files. This is why the seal chain has *gates* and not a single
> stamp.

## What a seal is, technically

Every seal is recorded. The `seals` table in the court's ledger is the **only
immutable history** — once a seal is granted it is never rewritten. This is
what makes the chain auditable: for any change in the repository, you can ask
*who sealed it, when, and against what evidence.*

The record is not a convenience. It is the point. A court without a fixed
memory of its own rulings cannot accumulate law — and law, built case by case,
is how the Imperial Code grows.

## Guards on the chain

The chain is protected against the ways a naive pipeline fails:

- **A failed landing is never recorded as success.** If the final merge into
  the blessed branch fails, the edict does *not* seal — it stays open and the
  failure is surfaced to you.
- **A sealed edict never silently strands.** If a landing exhausts its retries,
  the court raises a **zhengming** to you rather than leaving the work
  unmerged and forgotten.
- **Your seal is not automatic.** The court can *propose* that an edict awaits
  your seal, but only your authorization crowns it.

## Ascension

When the chain is complete, the edict **ascends**. Its sealed worktree is
landed — squash-merged into one commit on the blessed branch. The change has
left the realm of drafts and entered **現行**, the law in force.

> The word is deliberate. A citizen crossing a realm border is not merely
> *carried*; it is **transfigured**. Ascension is not migration.

Now that you know the path, meet the travellers: **the ministers**.
