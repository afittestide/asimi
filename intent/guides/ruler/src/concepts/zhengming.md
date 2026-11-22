# Zhengming

> *"If names be not correct, language is not in accordance with the truth of
> things. If language be not in accordance with the truth of things, affairs
> cannot be carried on to success."*
> — Confucius, *Analects* 13.3

**Zhengming** (正名) — "rectification of names" — is the protocol by which the
court stops and asks you when something is unclear. It is not an error handler
or a failure mode. It is a way of working.

## The principle

From the court's founding:

> **Guessing is treason.** A minister that proceeds without clarity betrays the
> Ruler's trust.

This is the opposite of how most tools behave. A conventional assistant, faced
with an ambiguous request, quietly picks an interpretation and proceeds. Asimi
refuses. When a name is not correct — when an instruction could mean two things
— the court **halts** and requests clarification.

## How a Zhengming works

1. A minister detects genuine ambiguity and calls **`ask_ruler`** with a
   question and a small set of concrete options.
2. The edict moves to **`blocked`** — it will not proceed until answered.
3. The **Secretary** (or Chancellor, depending on the path) receives the
   question and may answer from wider context.
4. If it cannot, the question **escalates to you**.
5. **Your answer** is appended to the edict's intent, and the edict resumes.

You are presented with options (typically two to four), so answering is fast and
precise rather than open-ended.

## Why the court asks

The cost of asking is a moment of your attention. The cost of guessing is
*days* — a change built on the wrong assumption, tested, reviewed, and sealed
before anyone notices it is the wrong thing. The court front-loads the question
to avoid the rework.

This is also why the court is designed to keep the *number* of questions low:
the Secretary's job is to **free you to hunt** — to distill intent so sharply
that the questions that reach you are the few that genuinely matter.

> **Healthy tension.** There is a deliberate pull between *never guessing* and
> *not bothering the Ruler*. That tension keeps the Secretary on his toes.

## The life of a Zhengming

A clarification request has a status, just as an edict does. It is usually
**`pending`** until answered, but the court may also retire one:

| Status | Meaning |
|--------|---------|
| **`pending`** | Awaiting an answer |
| **`answered`** | You replied; the edict resumed |
| **`expired`** | The deadline elapsed unanswered |
| **`dismissed`** | The court retired it — superseded by newer work |
| **`superseded_by_edict`** | The target edict ended (sealed or cancelled) |

By design, a court-authored retirement is **never recorded as `answered`** —
the ledger must never claim you spoke when you did not.

## Where questions surface

The court's event ledger (**天**) records every request
(`zhengming_needed`) and every answer (`zhengming_answered`). Pending questions
are visible through the Secretary. When you see an edict sitting in `blocked`,
the first thing to check is whether it is waiting on **you**.

See [Court Status & Troubleshooting](../reference/troubleshooting.md) for the
practical commands that surface pending questions.

Next, we leave the machinery and meet the interface: **the TUI**.