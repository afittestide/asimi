# Rituals

> *"Ritual is the way of Heaven, the righteousness of Earth, and the conduct of
> the people."* — paraphrase of the *Book of Rites* (禮記)

A **Ritual** is a workflow: a named sequence of steps that ministers execute to
accomplish a task. Where an edict is *what* you want done, a ritual is *how* the
court does it — a reusable pattern that weaves together prompts, setup, and
guardrails.

Rituals are how the court keeps its **礼** (propriety): the same careful shape,
onevery time, no matter who runs it.

## Why rituals

A coding task is rarely a single action. Fixing a bug means implementing it,
*then* testing it, *then* reviewing it — in that order, with a gate at each
step. A ritual makes that order explicit and repeatable. It is the court's
answer to *"but did you remember to run the tests?"* — the ritual does.

The full reference for authoring rituals lives in
[`intent/docs/rituals.md`](../docs/rituals.md); this chapter is the user's view.

## What a ritual is made of

Each ritual is a YAML document with ordered **steps**. Each step names:

- the **minister** who executes it,
- the **act** — the instruction the minister performs,
- optional **given** context to hand the minister, and
- optional **then** outcomes the step must produce to pass.

Here is the shape of **swift-strike**, the small-change ritual:

```yaml
- name: swift-strike
  steps:
    - name: forging
      minister: forge
      act: Implement the changes for the edict.
      on_failure: retry

    - name: judging
      minister: judge
      act: Run the tests; render a verdict.
      then:
        - the verdicts are passed
      on_failure: goto
      on_failure_target: forging

    - name: reviewing
      minister: chancellor
      act: Review against the Imperial Code; record precedent.
  then:
    - the edict awaits ruler's seal
```

Read it top to bottom: Forge implements → Judge tests → Chancellor reviews →
the edict **awaits your seal**. If judging fails, control **goes back** to
forging (`goto`). The ritual drives the seal chain; it never short-circuits it.

## The context pattern: background / given / act / then

Rituals follow a Gherkin-inspired rhythm, borrowed from behaviour-driven
development:

| Part | Runs | Purpose |
|------|------|---------|
| **background** | once, before steps | Shared context every step needs |
| **given** | before a step's act | Gather context for this step |
| **act** | the step | The minister's instruction |
| **then** | after a step | Validate the outcome |

A `given` or `then` entry starting with `!` is a shell command; anything else is
matched against a library of step definitions (e.g. *"the manifests"*,
*"the edict details"*).

## Failure handling

Every step declares what to do when it fails:

| `on_failure` | Effect |
|--------------|--------|
| **`retry`** | Try the same step again (up to `max_retries`) |
| **`goto`** | Jump to a named step (`on_failure_target`) |
| **`zhengming`** | Stop and ask you (see [Zhengming](zhengming.md)) |

When all retries are exhausted, the court invokes a **report_failure** ritual
to summarise what went wrong and distill a question for you. The court never
silently gives up.

## The built-in rituals

Asimi ships a set of rituals. These are the ones you will meet first:

| Ritual | For | Steps |
|--------|-----|-------|
| **swift-strike** | Small, focused changes | forging → judging → reviewing |
| **castle-siege** | Medium work needing a plan | strategizing → implementing (fork over lings) → judging → reviewing |
| **merge-worktree** | Landing a sealed edict | landing |
| **dawn-audience** | Court startup briefing | ping → sandbox check → version check → summarize → next |
| **clean-slate** | Splitting uncommitted work into commits | survey → commit |
| **fix-lint** | Repairing linter errors in parallel | lint → fix-all-files (fork) → commit-all |
| **project-init** | Setting up a project for Asimi | establish-infrastructure → sandbox-ready → chancellor-reviews |
| **review-contribution** | Reviewing a foreign contributor's GitHub PR | judging → censoring → posting |

### The two you choose between most

- **swift-strike** — a tight Forge/Judge/Chancellor loop. Good for a bug fix or
  a small feature. Fast, with a gate at every step.
- **castle-siege** — adds a **War** planning phase up front, which records the
  work as **lings**; the implementing step then forks over those lings. Good
  when the work has parts that must happen in order.

You rarely call a ritual by name. The court selects one from your edict's scope.

## Custom rituals

You can extend the court with your own rituals in `.agents/rituals.yaml`. The
full grammar — triggers, inputs, the fork/join pattern, system steps, every
context variable — is documented in
[`intent/docs/rituals.md`](../docs/rituals.md).

## Rituals and the seal chain

A ritual does not seal an edict by itself. It drives the work *to* the gates
and produces the evidence each gate weighs — then the edict **awaits the
ruler's seal**. The ritual serves the seal chain; it cannot replace it.

Next: how the court stops and asks when it is unsure — **Zhengming**.
