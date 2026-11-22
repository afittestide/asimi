# The Three Realms (三界)

> *"The Way that can be spoken of is not the constant Way."*
> — Laozi, *Tao Te Ching*, opening line (道可道，非常道)
>
> *"What I hear, I forget. What I see, I remember. What I do, I understand."*
> — attributed to Confucius

Every court is a mediator. Before anything happens in Asimi, three parties
must be in the same room: **you**, the **code**, and the **evidence**. Asimi
gives them names, and treats each as its own jurisdiction.

The idea is simple: *a realm is whom a thing lives under* — which law governs
it, not what it is made of. One filesystem, three jurisdictions.

## The three

```
人 Ren (Humanity) — Your Will
  │
  ▼
地 Di (Earth) — Code
  │
  ▼
天 Tian (Heaven) — Truth — logs & test runs
```

| Realm | What it is | In your terminal |
|-------|------------|------------------|
| **人 Ren (Humanity)** | Your intent, issues, and decisions | You, at the keyboard |
| **地 Earth (Earth)** | Source code, commits, uncommitted changes | The filesystem, git, your working tree |
| **天 Tian (Heaven)** | Tests, logs, CI verdicts, the event ledger | `/var/log`, test output, `dmesg` |

**Harmony** = your intent (人) flows through verification (天) into code (地)
without friction. When harmony breaks — ambiguity in your intent, gaps in the
evidence, or errors in the code — the court stops and repairs it before
proceeding.

## Ren (人) — the living will

Ren is *you*: your desires, requirements, and clarifications. It is the
"why" of all action, and it flows downward through **edicts** and
clarifications.

The sovereign is the source of intent and the final authority. Nothing in the
court can manufacture intent; ministers can only translate what you declare.

> Chinese has two homophones here. **人** (humanity — the realm, *who acts*) and
> **仁** (benevolence — the virtue, *how one acts*) are both pronounced *rén*.
> The Ruler's 人 is the source of intent; the ministers' 仁 gives that intent
> moral weight.

## Earth (地) — one filesystem, three jurisdictions

Earth is the ground under the court's feet, and it has its own internal
geography. Every change passes through three territories as it travels from
your editor to permanent record:

| Territory | Chinese | What it holds |
|-----------|---------|---------------|
| **The Borderlands** | — | Unstaged changes (edits not yet added) |
| **The Middle Kingdom** | — | Staged changes (added, not yet committed) |
| **The Capital** | — | Committed, unpushed changes |

Beyond the capital lies 現行 — *in force* — the released code running in
production. Harmony in Earth is the *wu-wei* flow of a change from Borderlands
through Middle Kingdom and Capital to In Force, without friction.

Earth has a vocabulary for its citizens too:

- A **民 (citizen)** is a file.
- A **邑 (settlement)** is a directory — self-similar, to any depth.
- A **州 (province)** is a worktree: a branch seated on its own ground.
- **歸宗** — "returning home" — is a merge.

## Tian (天) — the observed sky and the crowned ledger

Heaven is not the filesystem. It is a set of **sources** and a **method for
reading them**. Heaven is *authoritative, but not infallible*.

This distinction is the court's most important subtlety:

> **Evidence is not verdict.** Green CI is *testimony*, not reality. The
> runner banks testimony; only the Judge's pen files a verdict. Keep the pen
> and the bench in different hands.

Heaven has two registers:

- **史冊 — the immutable record** (`seals/`): verdicts, precedents, seals.
  This is the court's memory, and it is fixed.
- **現行 — in force**: production, the living sky. This is alive.

Heaven carries an **irreducible seed** with every verdict — the ruling, the
commit, the source, the time range, the window hash — *and* a URL. The seed is
the ground; the URL may rot. A verdict may cite a dead star and still stand.

## Why three realms and not just "the code"?

Because a change is not complete when the code compiles. It is complete when:

1. **人** — it still serves the intent that asked for it, and
2. **天** — the evidence says it is correct, and
3. **地** — the code is clean and in place.

A court that only watches Earth is a code generator. A court that watches all
three is a **government**.

Deepen this in the authoritative internal reference:
[The Three Realms](../docs/the_three_realms.md).

Next: the ritual that moves a change across all three realms and crowns it —
**the seal chain**.
