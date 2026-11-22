# Your First Edict

> *"I hear and I forget. I see and I remember. I do and I understand."*
> — attributed to Confucius

An **Edict** (詔令) is a work order. You issue it; the court executes it through
a **Ritual**. This chapter walks you through issuing one and watching the seal
chain close.

## The two tabs you live in

Asimi's TUI opens with two tabs:

| Tab | You talk to | Tempo |
|-----|-------------|-------|
| **Ruling** | the **Secretary** (中書令) | Campaign — issuing and managing edicts |
| **Hunting** | the **Chancellor** (門下侍中) | Skirmish — exploring, distilling intent |

Switch tabs with `gt` (next) and `gT` (previous). Your first edict is easiest
from the **Ruling** tab, with the Secretary.

> **Ruling vs. Hunting.** The Ruling tab is the court at work: edicts flow
> through their phases. The Hunting tab is where fuzzy intent gets sharpened
> into a proper edict through conversation. If you know exactly what you want,
> start in Ruling; if you are still thinking, start in Hunting.

## Issue an edict

From the Ruling tab, simply **describe what you want** in natural language:

```
Add a --version flag that prints the build version and exits
```

The Secretary will:

1. Distill your intent into a clean **edict**.
2. Have the court **classify** its scope (S, M, L, XL).
3. Enact the appropriate **ritual**.

For a small, focused change like this, the court enacts **swift-strike** — a
tight Forge → Judge → Chancellor loop. You will see the ministers work through
the tab as their steps execute.

## Watch the seal chain close

The edict travels through phases as the ritual runs:

```
brewing → planning → forging → judging → reviewing → sealed
```

- **forging** — the Forge implements the change.
- **judging** — the Judge runs the tests and renders a verdict.
- **reviewing** — the Chancellor reviews against the Imperial Code.

When the ritual finishes, the edict **awaits your seal**. This is the final
gate, and it is yours alone (see [The Seal Chain](../court/seal-chain.md)).

## Review and grant the seal

Before you seal, look at what changed:

```
:!git diff
```

(Runs `git diff` inside the sandbox.) When you are satisfied, **grant the seal**.

Granting your seal is what **ascends** the edict: the court lands the work as a
single squash-commit on the main branch, and the change enters **現行** — the
code in force.

## Check what happened

Ask the court anything about the edict:

```
What's the status of edict-123456?
```

The Secretary answers with the phase, the lings, and the test results. To see
the raw ledger behind that answer, ask for the events:

```
What happened during that edict?
```

## When the court asks *you*

Sometimes the court will **stop and ask** rather than guess. That is
**Zhengming** (正名), and it is a feature, not a snag — see
[Zhengming](../concepts/zhengming.md). An answer is appended to the edict's
intent and work resumes.

## A larger edict

For medium work (say, 50–200 lines), the court enacts **castle-siege** instead:

```
Add a user logout endpoint
```

Here the **War** ministry first plans the work into **lings** (task orders with
dependencies), then the Forge implements each in sequence, before judging and
reviewing. See [Lings](../concepts/lings.md) and [Rituals](../concepts/rituals.md).

## The court serves you

You can always:

- **Cancel an edict** — via the `:edicts` command's actions, or by asking.
- **Take over** — fix the code yourself, commit, and ask the Chancellor to
  review; or open a fresh edict with corrected scope.

Next, we go deeper into the machinery: **edicts, lings, rituals, and zhengming**.
