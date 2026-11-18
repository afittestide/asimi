# Rituals

Rituals are the operational workflows of Asimi. Each ritual defines a sequence of steps that ministers execute to accomplish a task. Users can define custom rituals in `.agents/rituals.yaml` to extend Asimi's capabilities.

## Table of Contents

- [Quick Start](#quick-start)
- [Ritual Structure](#ritual-structure)
- [Triggers](#triggers)
- [Inputs](#inputs)
- [Steps](#steps)
- [Context Variables](#context-variables)
- [The Fork/Join Pattern](#the-forkjoin-pattern)
- [Failure Handling](#failure-handling)
- [Full Example](#full-example)
- [Built-in Rituals](#built-in-rituals)

---

## Quick Start

Create `.agents/rituals.yaml` with your custom ritual:

```yaml
- name: my-ritual
  description: What this ritual does
  steps:
    - name: my-step
      minister: forge
      act: |
        Perform the action here.
```

---

## Ritual Structure

Each ritual is a YAML object with these fields:

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Unique identifier for the ritual |
| `description` | string | Human-readable description |
| `triggers` | array | What activates this ritual |
| `inputs` | object | Input parameters (optional) |
| `max_retries` | integer | Max retry attempts on failure (default: 1) |
| `background` | array | Context pre-loaded before steps (see [Background](#background)) |
| `then` | array | Outcomes the ritual as a whole produces |
| `steps` | array | Ordered sequence of steps to execute |

### Background

The `background` field pre-loads context variables before any steps run. This is useful when multiple steps need the same context:

```yaml
background:
  - the edict details
  - the earth status
```

### Ritual-level `then`

A ritual can declare outcomes at the top level, separate from step-level `then`. These describe what the ritual as a whole produces once all steps complete:

```yaml
- name: swift-strike
  steps:
    # ...
  then:
    - the edict awaits ruler's seal
```

---

## Triggers

Triggers define when a ritual activates. If omitted, the ritual is manual by default.

### Event Trigger

```yaml
triggers:
  - event: court_started
```

The ritual activates when a specific event occurs.

---

## Inputs

Define parameters the ritual accepts:

```yaml
inputs:
  edict_id:
    type: string
    required: true
  verbose:
    type: boolean
    required: false
    default: false
```

| Field | Description |
|-------|-------------|
| `type` | Parameter type (string, boolean, integer) |
| `required` | Whether the parameter must be provided |
| `default` | Default value if not required |

Inputs become available as context variables, prefixed with `.`:

```
{{ .edict_id }}
{{ .verbose }}
```

---

## Steps

Steps are the core of a ritual. Each step specifies:

```yaml
steps:
  - name: step-name           # Unique identifier
    minister: forge           # Which ministry executes this
    act: |                    # The action/prompt to execute
      Your prompt here.
    given:                    # Context to provide (optional)
      - the manifests
      - the verdicts
    then:                     # Outcomes produced (optional)
      - the verdicts are passed
    out: |                    # Output format template (optional)
      {{ .result }}
    on_failure: retry        # Failure handling (optional)
```

### Ministers

Each minister has a specific role:

| Minister | Role |
|----------|------|
| `chancellor` | Coordinates the court, reports status, requests guidance |
| `strategist` | Analyzes requirements, produces technical plans |
| `forge` | Implements changes, writes code |
| `judge` | Runs tests, verifies correctness |
| `sage` | Reviews code quality, records precedents |

### Step Fields

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Step identifier (unique within ritual) |
| `minister` | string | Ministry that executes this step |
| `act` | string | The prompt/action to execute |
| `given` | array | Context variables to provide |
| `then` | array | Outcomes this step produces |
| `out` | string | Output format template |
| `on_failure` | string | Action on failure: `retry`, `goto`, `zhengming` |
| `on_failure_target` | string | Target step name for `goto` |
| `effort` | string | Reasoning effort for this step: `low`, `medium`, or `high`. Only set when the step declares an `effort:` key; a step with no `effort:` key inherits the user-configured default (from `[llm] reasoning_effort` / `--reasoning-effort` / `ASIMI_REASONING_EFFORT`, or the provider default if unset). Forwarded as `reasoning_effort` to providers that support it (e.g. Fireworks Kimi K2). |

---

## Context Variables

Context variables provide information to steps. They are referenced in templates using `{{ .variable }}`.

### Built-in Context Variables

| Variable | Description |
|----------|-------------|
| `the edict` / `the edict details` | The current edict being processed |
| `the manifests` | List of file changes (manifests) |
| `the borderlands` | Unstaged changes (git status) |
| `the middle kingdom` | Staged changes |
| `the capital` | Committed changes |
| `the verdicts` | Test results from the judge |
| `the earth status` | Full git status (borderlands, middle kingdom, capital) |
| `manifests for the borderlands` | File-level diffs for unstaged changes |
| `the court status` | Status of the court and ministers |
| `the unsealed edicts` | Edicts awaiting approval |
| `a heaven's snapshot` | Heaven's current state |
| `the project metadata` | Project language, structure info |
| `the infrastructure templates` | Template files for project init |
| `Asimi's versions` | Current and latest version info |

### Providing Context with `given`

Use `given` to explicitly pass context to a step:

```yaml
steps:
  - name: review-code
    minister: judge
    given:
      - the manifests
      - the verdicts
    act: |
      Review these changes:
      {{ .manifests }}
```

Prefix an entry with `!` to run a shell command and inject its output as context:

```yaml
given:
  - the manifests
  - "!just test"
```

### System Steps (No Minister)

A step without a `minister` field is a system step — it is handled by the runtime rather than dispatched to a minister. System steps are useful for built-in checks like sandbox health or version checks:

```yaml
- name: check-sandbox
  then:
    - the sandbox is healthy
- name: check-asimi-version
  given:
    - Asimi's versions
  out: |
    {{- if .asimi_version.has_update }}
    Update available: {{ .asimi_version.latest_version }} (current {{ .asimi_version.current_version }})
    {{- else }}
    Running latest Asimi version {{ .asimi_version.current_version }}
    {{- end }}
```

### Producing Context with `then`

Use `then` to declare what a step produces:

```yaml
steps:
  - name: run-tests
    minister: judge
    act: |
      Run the test suite.
    then:
      - the verdicts are passed
```

### Referencing a Prior Step's Output

A step's Act result is automatically published under its name and is reachable from any later step's templates as `{{ .stepName }}`. Use this to chain a heavy summarization step into a chancellor-facing step without re-templating the raw inputs:

```yaml
steps:
  - name: summarize
    minister: strategist
    given:
      - the earth status
    act: |
      Condense the data below into a short briefing.
      {{ .earth_borderlands }}
      {{ .earth_middle_kingdom }}
      {{ .earth_capital }}

  - name: next
    minister: chancellor
    act: |
      Court briefing:
      {{ .summarize }}
      Decide the next move.
```

### Minister Session Semantics

Ritual Acts dispatched to a minister run in a throwaway session created per step — their conversation history is discarded once the step completes, so large `given:` payloads (diffs, DB dumps) do not leak into any user-facing chat.

`chancellor` is intentionally different: Acts targeting the chancellor run inside its persistent interactive session, so their prompts and outputs become part of the user's chat history. This lets rituals weave context into the ongoing conversation, but it also means anything you put in a chancellor Act will burn user-facing context window. Keep chancellor Acts short — pre-digest heavy data in a `strategist` (or `sage`) step and pass the summary in via `{{ .priorStep }}`, as in the example above.

---

## The Fork/Join Pattern

For parallel execution over a list of items, use `fork`:

```yaml
steps:
  - name: lint
    minister: forge
    act: |
      Run the linter and return a JSON array of files with errors.
      Format: [{"file": "path.go", "errors": ["error1"]}]
    then:
      - the edict is blocked

  - name: fix-all-files
    fork:
      over: lint          # Iterate over the "lint" step's results
      batch_size: 5       # Process 5 items in parallel
    work:
      - name: fix-file
        minister: forge
        act: |
          Fix all linter errors in file {{ .item.file }}.
          The errors to fix are: {{ .item.errors }}.
      - name: verify-file
        minister: judge
        act: |
          Run tests for file {{ .item.file }}.
        on_failure: retry
        on_failure_target: fix-file

  - name: commit-all
    minister: forge
    act: |
      Stage and commit all changes.
```

Within fork work steps, use `{{ .item }}` to access the current item being processed.

---

## Failure Handling

### Retry

Retry the same step:

```yaml
on_failure: retry
```

### Goto

Jump to a specific step:

```yaml
on_failure: goto
on_failure_target: forging
```

### Zhengming

Request clarification from the ruler:

```yaml
on_failure: zhengming
```

This is used when requirements are unclear and the minister needs guidance to proceed.

---

## Full Example

Here's `swift-strike`, one of the built-in rituals — a tight forge/judge/sage loop for small changes:

```yaml
- name: swift-strike
  description: A tight loop for enacting small changes in 地
  inputs:
    edict_id:
      type: string
      required: true
  max_retries: 3
  background:
    - the edict details
  steps:
    - name: forging
      minister: forge
      act: |
        Implement the changes for the edict below.
        {{ .edict }}
        Focus on minimal, targeted changes to fulfill the intent.
      on_failure: retry

    - name: judging
      minister: judge
      given:
        - the manifests
        - "!just test"
      act: |
        If any tests were changed, verify they were not weakened.
        {{ .manifests }}
        If tests fail, provide clear feedback for the Forge.
      then:
        - the verdicts are passed
        - record the judge's seal
      on_failure: goto
      on_failure_target: forging

    - name: reviewing
      minister: sage
      given:
        - the manifests
        - the verdicts
      act: |
        Review the code changes for the edict.
        Check for style violations, security concerns, and architectural issues.
        Record your insights using record_precedent.
      on_failure: goto
      on_failure_target: forging
  then:
    - the edict awaits ruler's seal
```

---

## Built-in Rituals

Asimi ships with seven built-in rituals in [`builtin_rituals.yaml`](../../court/builtin_rituals.yaml).

### swift-strike

A tight forge/judge/sage loop for small, targeted changes. The forge implements the edict, the judge runs tests and verifies coverage, and the sage reviews for style and security. On failure, control loops back to forging.

**Steps:** forging → judging → reviewing

### castle-siege

A medium-complexity workflow that adds a strategist planning phase before forging. The strategist analyzes the edict and produces a Battle Plan with phases and dependencies. The forge then executes the plan, followed by judging and reviewing.

**Steps:** strategizing → forging → judging → reviewing

### dawn-audience

The startup ritual, triggered automatically by the `court_started` event. It pings the model, checks sandbox health, checks for Asimi updates, then the strategist condenses the state of the three realms into a short briefing, and finally the chancellor presents that briefing and requests zhengming for the next step. The summarize step runs in a throwaway session so the raw diffs/edicts don't pollute the chancellor's interactive history; only the condensed briefing reaches the user-facing session. Contains system steps (no minister) for sandbox and version checks.

**Steps:** ping-model → check-sandbox → check-asimi-version → summarize → next

### review-borderland

A code review ritual for unstaged changes. The judge verifies test coverage for the borderlands, then the sage reviews for style, security, and architecture. Neither step stages or commits — the ruler decides what to do after review.

**Steps:** judging → reviewing

### clean-slate

Reviews all uncommitted changes (staged and unstaged), splits them into logical commits, determines if any should amend HEAD, and links commits to edicts for the seal chain. The strategist surveys the changes and produces a commit plan. The chancellor executes the commits, then links edict-tagged commits to their edicts by creating manifests.

**Steps:** survey → commit → link-edicts

### project-init

Initializes a project for Asimi. The forge customizes infrastructure templates (Justfile, Dockerfile, bashrc, asimi.conf, AGENTS.md) for the detected language. The judge verifies the sandbox is operational (must return Linux from `uname -a`). The sage reviews the infrastructure files for quality. Accepts an optional `clearMode` boolean and `agentsFile` string input.

**Steps:** establish-infrastructure → sandbox-ready → sage-reviews

---

## See Also

- [builtin_rituals.yaml](../../court/builtin_rituals.yaml) — Reference implementation of all built-in rituals
- [Edicts](./court_guide.md#edicts) — How edicts flow through the seal chain
- [The Three Realms](./the_three_realms.md) — Understanding 天, 地, and 人
