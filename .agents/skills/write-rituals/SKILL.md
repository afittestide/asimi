---
name: write-rituals
description: Author a new Asimi ritual or edit an existing one in .agents/rituals.yaml. Covers structure, triggers, inputs, steps, context variables, fork/join, and failure handling.
ministers: [secretary, strategist]
---

# write-rituals

Author or edit ritual YAML that Asimi executes as a workflow of ministerial
steps. Rituals live in `.agents/rituals.yaml`; the shipped reference
implementation is [`court/builtin_rituals.yaml`](../../../court/builtin_rituals.yaml).

The authoritative field-by-field reference is [`docs/rituals.md`](../../../docs/rituals.md).
Read it when a detail is not covered here and defer to it if this guide drifts.

## When to use

Use this skill to:

- create a new custom ritual in `.agents/rituals.yaml`
- edit the triggers, steps, inputs, or failure handling of an existing ritual
- diagnose a ritual that fails validation or loops unexpectedly

Do not restate or duplicate `docs/rituals.md`; link to it instead.

## Ritual structure

Each ritual is one YAML object in a top-level list:

| Field | Type | Purpose |
|-------|------|---------|
| `name` | string | Unique identifier |
| `description` | string | Human-readable summary |
| `triggers` | array | What activates the ritual (omitted = manual) |
| `inputs` | object | Typed parameters (`string`, `boolean`, `integer`) |
| `max_retries` | integer | Retry attempts on failure (default 1) |
| `background` | array | Context pre-loaded before any step runs |
| `then` | array | Outcomes the ritual as a whole produces |
| `steps` | array | Ordered steps to execute |

```yaml
- name: my-ritual
  description: What this ritual does
  triggers:
    - event: court_started
  inputs:
    edict_id:
      type: string
      required: true
  background:
    - the edict details
  steps:
    - name: doing-the-thing
      minister: forge
      act: |
        Perform the action for edict {{ .edict_id }}.
  then:
    - the edict awaits ruler's seal
```

Inputs become template variables: `{{ .edict_id }}`, `{{ .verbose }}`.

## Ministers

Each step names the minister that executes it:

| Minister | Role |
|----------|------|
| `chancellor` | Coordinates the court, reports status, requests guidance |
| `strategist` | Analyzes requirements, produces technical plans |
| `forge` | Implements changes, writes code |
| `judge` | Runs tests, verifies correctness |
| `sage` | Reviews code quality, records precedents |

A step with **no `minister`** is a **system step** handled by the runtime
(e.g. sandbox health, version checks) — use it for built-in checks, not work.

## Steps

```yaml
steps:
  - name: forging
    minister: forge
    given:
      - the manifests
      - "!just test"
    act: |
      Implement the edict. {{ .edict }}
    then:
      - the verdicts are passed
    out: |
      {{ .result }}
    on_failure: goto
    on_failure_target: forging
    effort: high
```

| Field | Purpose |
|-------|---------|
| `name` | Unique identifier within the ritual (gerund form) |
| `minister` | Executing minister; omit for a system step |
| `act` | The prompt/action to execute |
| `given` | Context variables passed to the step |
| `then` | Outcomes this step produces |
| `out` | Output format template |
| `on_failure` | `retry`, `goto`, or `zhengming` |
| `on_failure_target` | Step name to jump to for `goto` |
| `effort` | Reasoning effort: `low`, `medium`, `high`; omit to inherit the default |

## Context variables

Reference built-ins with `{{ .variable }}`. Key variables:

| Variable | Content |
|----------|---------|
| `the edict` / `the edict details` | Current edict |
| `the manifests` | File changes |
| `the borderlands` / `the middle kingdom` / `the capital` | Unstaged / staged / committed changes |
| `the verdicts` | Test results from the judge |
| `the earth status` | Full git status across all three parts |
| `manifests for the borderlands` | File-level diffs for unstaged changes |
| `the court status` | Court and minister status |
| `the unsealed edicts` | Edicts awaiting approval |
| `a heaven's snapshot` | Heaven's current state |
| `the project metadata` | Project language and structure |
| `the infrastructure templates` | Project-init templates |
| `Asimi's versions` | Current and latest version info |

See `docs/rituals.md` for the full list.

**Shell injection** — prefix a `given` entry with `!` to run a command and
inject its output as context:

```yaml
given:
  - the manifests
  - "!just test"
```

**Step chaining** — a step's `act` result is published under its `name` and is
reachable from later steps as `{{ .stepName }}`. Use this to feed a heavy
summarization step into a short chancellor step:

```yaml
steps:
  - name: summarize
    minister: strategist
    given:
      - the earth status
    act: |
      Condense the data into a short briefing.
      {{ .earth_borderlands }}
      {{ .earth_capital }}
  - name: next
    minister: chancellor
    act: |
      Briefing:
      {{ .summarize }}
```

## Fork/Join

Run work steps over a list of items in parallel:

```yaml
steps:
  - name: lint
    minister: forge
    act: |
      Return a JSON array: [{"file": "path.go", "errors": ["..."]}]
  - name: fix-all-files
    fork:
      over: lint        # iterate the "lint" step's results
      batch_size: 5     # parallel items per batch
    work:
      - name: fix-file
        minister: forge
        act: |
          Fix errors in {{ .item.file }}: {{ .item.errors }}
      - name: verify-file
        minister: judge
        act: |
          Run tests for {{ .item.file }}
        on_failure: retry
  - name: committing-all
    minister: forge
    act: |
      Stage and commit all changes.
```

Inside `work:` steps, `{{ .item }}` is the current item.

## Failure handling

- `on_failure: retry` — retry the same step
- `on_failure: goto` with `on_failure_target: <step>` — jump to a named step
- `on_failure: zhengming` — ask the ruler for clarification

## Style rules

- **Gerund step names** — `forging`, `judging`, `reviewing`, `committing-all`.
- **One clear task per `act`** — split unrelated work into separate steps.
- **Ritual-level `max_retries`** — set the retry budget on the ritual, not per step.
- **Keep chancellor Acts short** — see session semantics below.
- **Validation checklist** before committing a ritual:
  - the file is valid YAML
  - every `on_failure_target` names a real step in the same ritual
  - step names are unique within the ritual

## Minister session semantics

Acts dispatched to a minister run in a **throwaway session** created per step;
their history is discarded when the step finishes, so large `given:` payloads
(diffs, dumps) do not leak into user-facing chat.

`chancellor` is the exception: its Acts run inside the **persistent
interactive session**, so their prompts and outputs become part of the user's
chat history and consume the user-facing context window. Keep chancellor Acts
short — pre-digest heavy data in a `strategist` (or `sage`) step and pass the
summary in via `{{ .priorStep }}`.

## See also

- [`docs/rituals.md`](../../../docs/rituals.md) — authoritative reference
- [`court/builtin_rituals.yaml`](../../../court/builtin_rituals.yaml) — reference implementations
