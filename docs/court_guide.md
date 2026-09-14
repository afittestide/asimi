# The Court — User Guide

## Background — Confucian Foundations 儒學基礎

The Court framework is built upon the bedrock of Confucian philosophy,
which emphasizes moral cultivation, social harmony, and the rectification of
names. This is not merely aesthetic metaphor—it is a deliberate encoding of
timeless principles into the craft of software development.

The Five Constant Virtues (五常, Wǔcháng):

- 仁 (Rén) — Benevolence/Humaneness: The Chancellor embodies 仁, ensuring code serves users with compassion
- 义 (Yì) — Righteousness: The Judge upholds 义, validating that implementations meet their proper purpose
- 礼 (Lǐ) — Ritual Propriety: Rituals themselves are 礼, the formal patterns that maintain order
- 智 (Zhì) — Wisdom: The War exercises 智, discerning the proper path through complexity
- 信 (Xìn) — Trustworthiness: The Tian ledger maintains 信, providing immutable accountability

As Confucius taught in the Analects (論語): 

> If names be not correct, language is not in accordance with the truth of things.
> If language be not in accordance with the truth of things,
> affairs cannot be carried on to success."
> 名不正，則言不順；言不順，則事不成。

This is the essence of Zhengming (正名).

### Main Function

The Court's purpose is to free the Ruler to hunt.
The Ruler together with the Chancellor will hunt for new rituals,
improvments and look behind corners to come up with knight moves.
The court handles the bureaucracy so the Ruler
can stay in skirmish tempo, spotting openings and shaping direction.

To free the ruler, the Court harmonizes the three realms:

- **Humanity (人, Rén)** — The Ruler. The source of intent and will, the "why" of all action. Ren flows downward through edicts and clarifications. The Ruler's humaneness (仁) is the wellspring from which all purpose emerges.

- **Heaven (天, Tiān)** — The Ledger. Events, logs, test results, and the immutable record of all that transpires. Heaven witnesses and remembers — the Tian (天) database provides accountability and the mandate that validates the Ruler's decrees.

- **Earth (地, Dì)** — Implementation. The codebase, commits, branches and worktrees. Earth receives the Ruler's intent and grounds it in material reality.

The ministers and rituals mediate between these realms — translating Ren into action, recording outcomes in Tian, shaping changes in Di. When the three realms are in harmony, intent flows naturally into code without friction. The Ruler speaks, ministers act, Heaven records, Earth changes. When harmony breaks — ambiguity in Ren, gaps in Tian, or errors in Di — Zhengming restores alignment.

> **Note on Ren:** Chinese has two homophones: 人 (humanity, the realm) and 仁 (benevolence, the virtue). The Ruler's 人 (humanity) is the source of intent;
> the ministers 仁 (benevolence) gives that intent moral weight. Both are pronounced *rén*, but they carry distinct meanings — one ontological (who acts), one ethical (how one acts).

### Guiding Principles

The Court embodies these principles:

1. **Zhengming** (正名) — Rectification of Names.
   Never guess at requirements. When ambiguity threatens, stop and ask.
   **Guessing is TREASON** — a minister that proceeds without clarity betrays the Ruler's trust.

2. **Dao** (道) — The Way.
   Follow Wu-Wei. Use rituals for repeatable frictionless workflows,
   ensuring the earth stays free of friction and bureaucracy.

3. **De** (德) — Virtue.
   The Chancellor ensures ethical behavior. Code must be beautiful.

The metaphor isn't just aesthetic — it encodes a philosophy of careful, principled software development where clarity precedes action and quality is non-negotiable.

---

## Overview

When you interact with Asimi's Court, you assume the role of the **Ruler**
(君主, Jūnzhǔ). You issue Edicts (詔令, Zhàolìng) which the Court
processes through an appropriate ritual, each handled by specialized Ministers
(大臣, Dàchén) who serve as your 君子 (Jūnzǐ)—exemplary persons who cultivate
virtue through their craft.

The Court is Asimi's autonomous agent framework, inspired by the imperial
bureaucracy of ancient China. It orchestrates multiple specialized AI ministers
to handle complex software engineering tasks from inception to deployment.

```
                        ┌─────────────────────────────┐
                        │         TUI (tui)           │
                        │  [Ruling]       [Hunting]   │
                        └─────┬───────────────┬───────┘
                              │ Prompt        │ Prompt
                        ┌─────▼─────┐   ┌────▼──────┐
                        │Chancellor │   │ Chancellor │
                        │  (宰相)    │   │  (門下侍中)│
                        └─────┬─────┘   └───────────┘
                              │              sees all (read-only)
                              │              creates edicts ──►─┐
                              │ Task / Result                   │
          ┌────────────┬──────┼───────┬────────────┐            │
          ▼            ▼      ▼       ▼            ▼            │
     War        Forge   Judge   Chancellor                 │
      (兵部)        (工部)  (刑部)  (門下侍中)                    │
                                                                │
                        ┌─────────────────┐                     │
                        │  Ritual Runner  │◄────────────────────┘
                        │  (embedded YAML)│
                        └────────┬────────┘
                                 │ dispatches Tasks
                                 ▼
                            ministers...
```

---

## Core Concepts

### Edicts

An **Edict** is a work order issued by the Ruler. It captures intent and tracks
the entire lifecycle of a change from request to completion.

**Phases:**

```
Classifying → Planning → Forging → Judging → Reviewing → Sealed
                                                    ↑
                                              (or Cancelled)
```

- `brewing` — Chancellor determines the scope and approach
- `planning` — War breaks down the work into Lings
- `forging` — Forge implements the code changes
- `judging` — Judge runs tests and validates changes
- `reviewing` — Chancellor reviews for quality and standards
- `sealed` — Edict successfully completed (minister marks it sealed after successful completion)
- `cancelled` — Edict was cancelled

An edict can be **halted** at any phase (boolean flag), pausing work until the Ruler responds to a pending Zhengming.

**Phase Transitions:** Ministers transition the edict through phases as they complete their work. The final minister in the ritual marks the edict as `sealed` upon successful completion.

### Ministers

Each minister is a specialized AI agent with a specific role:

| Minister | Role | Core Tools | Specialized Tools |
|----------|------|------------|-------------------|
| **Secretary** | Manages court business and provides a bird's eye view of the Court | `read_file`, `glob`, `grep` | `suggest_edict`, `enact_ritual`, `consult_minister`, `asimisql` |
| **Chancellor** | Coordinates all ministers, manages edict lifecycle, interfaces with the Ruler | — | `create_edict`, `cancel_edict`, `answer_zhengming`, `get_edict_status`, `list_edicts`, `list_rituals`, `enact_ritual`, `asimisql` |
| **War** | Analyzes edicts, creates execution plans, decomposes work into Lings | `read_file`, `glob`, `grep` | `create_ling`, `update_ling`, `list_lings` |
| **Forge** | Implements code changes according to plans | `read_file`, `write_file`, `edit_file`, `glob`, `grep`, `run_shell_command` | `create_manifest`, `update_manifest`, `commit_manifest` |
| **Judge** | Writes tests and validates changes through test coverage | `read_file`, `write_file`, `edit_file`, `glob`, `run_shell_command` | `record_verdict`, `reject_manifest` |
| **Chancellor** | Sees all state read-only, helps distill intent into edicts, performs code review with precedent tracking | `read_file`, `glob`, `grep` (all tables) | `create_edict`, `record_precedent` |

**Core Tools** are the basic file system and shell tools needed for each minister's work. **Specialized Tools** are unique to each minister's role in the Court. All ministers share `ask_ruler` and `tian_ledger` as common tools — the Tian ledger is open to every minister. `consult_minister` is available only to the secretary via `extra_tools`.

### Lings

A **Ling** is a sub-task within an edict's execution plan. The War
creates Lings during the planning phase, breaking down the edict intent into
actionable work items with dependencies.

```
Edict: "Add user authentication"
  |
  +-- Ling 1: "Create user model"
  +-- Ling 2: "Add login endpoint" (depends on Ling 1)
  +-- Ling 3: "Add logout endpoint" (depends on Ling 1)
  +-- Ling 4: "Write authentication tests" (depends on Lings 2, 3)
```

### Zhengming (Clarification)

**Zhengming** (正名, "rectification of names") is the protocol for requesting
clarification when requirements are ambiguous. When a minister encounters
uncertainty that could impact the work, they invoke Zhengming to ask the Ruler
for guidance.

The edict moves to the `halted` phase until you respond. This ensures the
Court never guesses at requirements—it always seeks clarity before
proceeding.

**The Zhengming Loop:**

1. Minister calls `ask_ruler` with a question — edict moves to `halted`
2. Chancellor receives the question and attempts to answer from its wider context
3. If the Chancellor cannot answer, it escalates to the Ruler
4. Answer (from Chancellor or Ruler) is appended to edict intent, edict resumes previous phase

Zhengming is a clarification protocol, not a failure recovery mechanism. Ministers invoke it explicitly when they detect ambiguity—it operates outside the normal failure flow. **To guess is treason**—a minister that proceeds without clarity betrays the Ruler's trust.

### Rituals

**Rituals** are YAML-defined workflows that orchestrate ministers and code snippets
through a series of steps. They provide reusable patterns for common tasks.

**Built-in rituals:**

#### New (Session startup)

Invoked when starting a new session. Reviews the current state of the working tree:

```yaml
- name: new
  description: "Session startup - review current state and await commands"
  background:
    - "!echo '**The Borderlands**'"
    - "!git diff"
    - "!echo '**The middle kingdom**'"
    - "!git diff --cached"
  steps:
    - name: review
      minister: chancellor
      act: |
        Review the diff in the scratchpad:
        - If there are changes in progress, help the user resolve them
        - If there are no changes, Welcome the ruler
```

#### Swift Strike (S-size edicts)

For small, focused changes. A tight loop between Forge and Judge:

```yaml
- name: swift-strike
  max_retries: 3
  background:
    - the edict details
  steps:
    - name: forge
      minister: forge
      act: |
        Implement the changes for edict {{ .edict_id }}.
        Focus on minimal, targeted changes to fulfill the intent.
      on_failure: retry

    - name: judge
      minister: judge
      given:
        - the manifests
      act: |
        Run tests and validate the changes for edict {{ .edict_id }}.
        If tests fail, provide clear feedback for the Forge.
      then:
        - "!just test"
      depends_on: [forge]
      on_failure: goto
      on_failure_target: forge
  then:
    - the edict is sealed
```

#### Castle Siege (M-size edicts)

For medium-complexity work with planning and review:

```yaml
- name: castle-siege
  max_retries: 3
  background:
    - the edict details
  steps:
    - name: war
      minister: war
      act: |
        Analyze edict {{ .edict_id }} and produce a technical Battle Plan.
        Break down the work into clear phases with dependencies.
        Identify risks and architectural decisions.
      on_failure: zhengming

    - name: forge
      minister: forge
      given:
        - the manifests
      act: |
        Execute the Battle Plan for edict {{ .edict_id }}.
        Implement changes according to the War plan.
      depends_on: [war]
      on_failure: retry

    - name: judge
      minister: judge
      given:
        - the manifests
      act: |
        Run the Trials for edict {{ .edict_id }}.
        Execute all tests and validate the Forge's work.
      then:
        - "!just test"
      depends_on: [forge]
      on_failure: goto
      on_failure_target: forge

    - name: review
      minister: chancellor
      given:
        - the manifests
        - the verdicts
      act: |
        Review the implemented code for edict {{ .edict_id }}.
        Verify it adheres to the Imperial Code (project standards).
      depends_on: [judge]
      on_failure: goto
      on_failure_target: war
  then:
    - the edict is sealed
```

#### Grand Orchestration (XL-size edicts)

Full lifecycle orchestration with deployment:

```yaml
- name: grand-orchestration
  max_retries: 3
  background:
    - the edict details
  steps:
    - name: war
      minister: war
      act: |
        Produce a comprehensive Battle Plan for edict {{ .edict_id }}.
        Decompose into lings with dependencies.
      on_failure: zhengming

    - name: forge
      minister: forge
      given:
        - the manifests
      act: |
        Execute all lings for edict {{ .edict_id }}.
        Implement each change and stage manifests.
      depends_on: [war]
      on_failure: retry

    - name: judge
      minister: judge
      given:
        - the manifests
      act: |
        Run the full trial suite for edict {{ .edict_id }}.
      then:
        - "!just test"
      depends_on: [forge]
      on_failure: goto
      on_failure_target: forge

    - name: review
      minister: chancellor
      given:
        - the manifests
        - the verdicts
      act: |
        Full quality and standards review for edict {{ .edict_id }}.
      depends_on: [judge]
      on_failure: goto
      on_failure_target: war

    - name: deploy
      minister: marshal
      given:
        - the manifests
        - the verdicts
        - the precedents
      act: |
        Prepare deployment for edict {{ .edict_id }}.
        Verify all seals are in place before proceeding.
      depends_on: [review]
      on_failure: zhengming
  then:
    - the edict is sealed
```

#### Review (Code review)

Judge and Chancellor review changes in the working tree:

```yaml
- name: review
  background:
    - "!git diff"
    - "!git diff --cached"
    - "!just test"
  steps:
    - name: judge
      minister: judge
      act: |
        Review the code changes. Run tests and identify any issues,
        bugs, or missing test coverage.

    - name: review
      minister: chancellor
      act: |
        Review the code changes for quality and standards compliance.
        Check for style violations, security concerns, and architectural issues.
      depends_on: [judge]

    - name: report
      minister: chancellor
      act: |
        Produce a comprehensive code review report.
        Summarize the findings from the judge and chancellor.
        Highlight any blocking issues and suggest improvements.
      depends_on: [review]
```

#### Wakeup

Triggered on `court_started` event, reports court status:

```yaml
- name: wakeup
  triggers:
    - event: court_started
  background:
    - the court status
  steps:
    - name: report
      minister: chancellor
      act: |
        The Court has awoken. Report the current court status.
        List any active edicts, their phases, and any pending zhengming.
```

**Background/Given/Act/Then pattern:**

Rituals follow a Gherkin-inspired pattern. `background` at the ritual level runs once and provides shared context to all steps. At the step level, `given` gathers step-specific context before the minister acts, and `then` validates the result:

```yaml
steps:
  - name: build
    minister: forge
    scope: edict              # Context inheritance
    model: claude-sonnet-4    # Override model for this step
    temperature: 0.3          # Lower for precise work
    env:                      # Environment variables
      GOOS: linux
      GOARCH: amd64
    given:
      - the edict details          # Builtin: matched via step definitions
      - "!git stash"               # Bash: "!" prefix runs inline command
    act: Implement feature X       # LLM: minister instruction
    then:
      - "!just fmt && just lint"   # Bash: must pass or step fails
      - no lint errors             # Builtin: matched via step definitions
```

- `scope` — controls context inheritance:
  - `edict` — retain the edict's context (default)
  - `private` — start with a fresh context
  - `<step_name>` — fork from a specific step's context
- `model` — override the model for this step (e.g., use a faster model for simple tasks)
- `temperature` — LLM temperature override (0.0-1.0); lower for precise work, higher for creative exploration
- `env` — environment variables available to `given`, `act`, and `then`
- `given` — runs before the minister. Each entry is either a `!` bash command or a step definition match. Results are stored in context and available as template variables in `act`.
- `act` — the minister's instruction (LLM call).
- `then` — runs after the minister completes. Each entry is either a `!` bash command (non-zero exit fails) or a step definition match.

**Step definition resolution:** The `!` prefix denotes a bash command. Everything else is matched via cucumber-expressions step definitions (built-in Go functions + user-extensible YAML in `.agents/step_definitions.yaml`).

**Creating custom rituals:**

Place YAML file in `.agents/rituals.yaml` to define project-specific rituals:

```yaml
# .agents/rituals.yaml
- name: my-ritual
  description: "Custom workflow for my project"
  on_failure: retry
  max_retries: 2
  steps:
    - name: step-1
      minister: forge
      given:
        - the edict details
        - "!git diff HEAD"
      act: |
        Do something for the edict.
      then:
        - "!just test"
```

**Template Variables:**

Ritual `background`, `given`, and `act` fields support template expansion using `{{ .variable }}` syntax. The template data is built from three sources:

| Source | Description | Example |
|--------|-------------|---------|
| `.edict_id` | Always available — the current edict ID | `{{ .edict_id }}` |
| Ritual inputs | Keys from the `inputs` map passed to `Start()` | `{{ .edict_id }}` (when passed as input) |
| Given context | Results from `given` steps, keyed by their output key | `{{ .edict }}`, `{{ .manifests }}` |

**Builtin Step Definitions:**

Both `given` and `then` fields match entries against registered step definitions (cucumber-expressions). Built-in definitions:

| Pattern | Handler Key | Output Key | Usable In | Description |
|---------|------------|-----------|-----------|-------------|
| `the edict details` | `get_edict` | `edict` | `given` | Full edict details including intent, scope, phase |
| `the court status` | `get_court_status` | `court_status` | `given` | Current state of all active edicts |
| `the manifests` | `get_manifests` | `manifests` | `given` | List of forge manifests for the edict |
| `the verdicts` | `get_verdicts` | `verdicts` | `given` | Judge verdicts for the edict |
| `the precedents` | `get_precedents` | `precedents` | `given` | Chancellor precedents for the edict |
| `the edict is sealed` | `seal_edict` | `sealed` | `then` | Mark the edict as sealed |
| `the edict is blocked` | `block_edict` | `blocked` | `then` | Mark the edict as blocked |
| `the edict is unblocked` | `unblock_edict` | `unblocked` | `then` | Mark the edict as unblocked |

User-defined step definitions can be added in `.agents/step_definitions.yaml`:

```yaml
- pattern: "the branch diff"
  command: "git diff main...HEAD"
  key: "branch_diff"
```

**Failure handling (`on_failure`):**
- `retry` — Retry the step up to `max_retries` (default when set at ritual level)
- `zhengming` — Request clarification from the Ruler
- `goto` — Jump to a named step (requires separate `on_failure_target` field, e.g., `on_failure: goto` + `on_failure_target: forge`)
- `abort` — Stop the ritual entirely (default if unset)

`on_failure` and `max_retries` can be set at the ritual level as defaults, and overridden per step.

**When all retries are exhausted**, the Court invokes the `report_failure` ritual:

```yaml
- name: report_failure
  triggers:
    - event: ritual_failed
  background:
    - the edict details
    - the court status
  steps:
    - name: report
      minister: chancellor
      act: |
        A ritual has failed after exhausting all retries for edict {{ .edict_id }}.
        Analyze the failure and suggest next steps.
        Consider requesting zhengming if the path forward is unclear.
```

**Loop exhaustion recovery:**

When a step exhausts all `max_retries` attempts, the Court invokes the `report_failure` ritual. This ritual summarizes the work done, identifies the failure point, and distills a Zhengming question for the Ruler to decide the path forward.

---

## Tools Reference

**Common Tools** (available to all ministers):
- `ask_ruler(question)` — Request a clarification or decision from the Ruler
- `tian_ledger(edict_id?, event_type?, limit?, offset?, since_id?, detail?)` — Query the Tian event ledger for a bird's eye view. `detail=summary` (default) tail-truncates `last_step_output` to the court-configured length; `detail=full` returns every payload key untruncated; `since_id` returns only events newer than the given id

### Secretary Tools

**Edict Management:**
- `suggest_edict(intent)` — Distill the Ruler's intent into a new edict
- `enact_ritual(ritual_name, edict_id)` — Start a ritual for an edict
- `consult_minister(minister_id, task)` — Dispatch ad-hoc work to another minister

**Observability:**
- `asimisql(query)` — Execute raw SQL for advanced queries

> The Secretary manages court business and provides a bird's eye view of the Court. For questions about what happened during an edict's lifecycle, use `tian_ledger`.

### Chancellor Tools

**Edict Management:**
- `create_edict(intent, scope?)` — Create a new edict from the Ruler's intent
- `cancel_edict(edict_id)` — Cancel an edict, moving it to `cancelled` phase
- `get_edict_status(edict_id)` — Get current status, phase, and Lings for an edict
- `list_edicts(status?)` — List edicts, optionally filtered by status

**Zhengming (Clarification):**
- `answer_zhengming(edict_id, answer)` — Provide Ruler's answer and resume the edict

**Ritual Management:**
- `list_rituals()` — List available rituals from `.agents/rituals/`
- `enact_ritual(ritual_name, edict_id)` — Start a ritual for an edict

**Observability:**
- `asimisql(query)` — Execute raw SQL for advanced queries

> The Chancellor orchestrates ministers through rituals, not direct invocation. For ad-hoc minister work, use `enact_ritual` with a custom ritual.

### War Tools

- `create_ling(edict_id, description, depends_on?)` — Create a new Ling (sub-task) for an edict
- `update_ling(ling_id, status?, description?)` — Update a Ling's status or description
- `list_lings(edict_id)` — List all Lings for an edict with their dependencies

### Forge Tools

- `create_manifest(edict_id, file_path, change_type)` — Record a new file change (create/modify/delete)
- `update_manifest(manifest_id, status)` — Update manifest status (staged → live → quenched)
- `commit_manifest(manifest_id, commit_sha)` — Mark manifest as committed with git SHA

### Judge Tools

- `record_verdict(edict_id, ling_id?, passed, details?)` — Record test results for an edict or specific Ling
- `reject_manifest(manifest_id, reason)` — Mark a manifest as rejected with reasoning

### Chancellor Tools

- `record_precedent(edict_id, approved, reasoning)` — Record code review outcome with reasoning

---

## Data Model

### Forge Manifests

When the Forge implements changes, it creates **Manifests** tracking each file modification:

- `staged` — Change created but not committed
- `live` — Committed to the repository
- `quenched` — Validated by the Judge
- `rejected` — Failed review (set by Judge or Chancellor via `reject_manifest`)

### Ling Status

Lings track their progress through the execution plan:

- `pending` — Not yet started
- `in_progress` — Currently being worked on
- `completed` — Successfully finished
- `blocked` — Waiting on dependency or Zhengming

### Judge Verdicts

The Judge creates **Verdicts** after running tests:
- `passed` — Tests succeeded
- `failed` — Tests failed

### Chancellor Precedents

The Chancellor records **Precedents** from code reviews:
- `approved` — Code meets standards
- `rejected` — Code violates principles

### Tian Events

The **Tian** (天, Heaven) ledger records all events in the edict lifecycle for auditing and debugging. **All events are logged to the session logs** for complete traceability. Every minister reads the ledger with the `tian_ledger` tool, which returns recent events filtered by edict, event type, and paginated with limit/offset — the bird's eye view of what transpired in the Court.

Because `limit` bounds *rows*, not bytes, a single heavy event (e.g. a `ritual_completed` carrying a full review report in `payload.last_step_output`) can overflow the transport at the default `limit`. The tool therefore bounds its default surface:

- **`detail=summary`** (default) — every payload key is returned verbatim **except** `last_step_output`, which is tail-truncated: only the **last N characters** survive (the end of a review carries its verdict), marked with a leading `…` and a `…(truncated)` suffix. The derived `detail` field is always computed from the *full* payload.
- **`detail=full`** — every payload key is returned untruncated, bypassing the bound. Use this when you need the whole body.
- **`since_id`** — an optional cursor. When `> 0`, only events with `id > since_id` are returned; poll with the highest id you have seen to fetch what's new without re-walking history. Still ordered `created_at DESC` and still honouring `limit`/`offset`.

The truncation length N is a **court-level setting** (`court.output_limit` in the config file, default `500` chars); it is fixed for the whole court and is not a per-call tool input.

Events are classified into two categories:

#### Domain Events (Trigger Actions)

These events trigger minister actions via subscriptions. Ministers subscribe to specific event types and receive them as tasks:

| Event | Emitted by | Subscribers | Action Triggered |
|-------|-----------|-------------|------------------|
| `edict_created` | Chancellor | War | Begin planning |
| `manifest_committed` | Forge | Judge | Run tests |
| `manifest_rejected` | Judge, Chancellor | Forge | Reforge changes |
| `zhengming_answered` | Chancellor | originating minister | Resume halted work |
| `court_started` | Court | — | Triggers `wakeup` ritual |

#### Audit Events (Ledger Only)

These events are recorded for traceability and debugging but do not trigger subscriptions:

| Event | Purpose |
|-------|---------|
| `edict_assigned` | Records which minister received an edict |
| `phase_changed` | Tracks edict phase transitions |
| `forge_committed` | Records Forge commit actions |
| `ritual_started` | Marks ritual invocation |
| `ritual_completed` | Marks successful ritual completion |
| `ritual_failed` | Marks ritual failure after retries |
| `ritual_step_started` | Tracks step execution start |
| `ritual_step_completed` | Tracks step execution success |
| `ritual_step_failed` | Tracks step execution failure |
| `zhengming_needed` | Records clarification request |
| `edict_cancelled` | Records edict cancellation |

> **Note:** The Event Registry (see [Architecture](#architecture)) routes Domain Events to subscribed ministers. Audit Events flow through the registry but have no subscribers — they exist solely for the Tian ledger's immutable record.

---

## Using the Court

### Starting the Court

The Court starts automatically when Asimi initializes. Ministers begin their processing loops and await tasks.

### Issuing Edicts

Simply describe what you want in natural language. The Chancellor will:

1. Create an edict capturing your intent
2. Classify the scope (S, M, L, XL)
3. Enact an appropriate ritual

When a ritual is enacted, the Ritual Runner takes over and executes the workflow.

```
You: Add a logout button to the header that clears the session and redirects to /login

Chancellor: [Creates edict, invokes swift-strike ritual]
  Forge: [Implements the button]
  Judge: [Runs tests]
  -> Edict sealed
```

### Responding to Zhengming

When a minister needs clarification, you'll be prompted:

```
Zhengming from Chancellor:
"Should the logout confirmation use a modal dialog or inline confirmation?"

> Use a modal dialog with "Cancel" and "Logout" buttons
```

Your response is appended to the edict's intent and processing continues.

### Checking Status

Ask about edict status:

```
You: What's the status of the logout feature?

Chancellor: [Uses get_edict_status tool]
  Edict issue-123: phase=judging, all tests passing
```

### Cancelling Edicts

```
You: Cancel the logout feature edict

Chancellor: [Uses cancel_edict tool]
  Edict issue-123 cancelled
```

### TUI — Tabs and Two Tempos

The TUI uses **vi-style tabs** with a top tab-bar. Asimi starts with two tabs: **Ruling** and **Hunting**. Each tab has its own chat history and input buffer — switching tabs preserves your place.

**Navigation**: `gt` / `gT` (next/prev tab), `1gt` / `2gt` (jump by number).

**Opening tabs**: `:tabnew <minister>` opens a read-only view of a minister's work log.

```
┌──────────────────────────────────────────────┐
│ [Ruling] [Hunting] [Forge]                   │
│──────────────────────────────────────────────│
│ [FORGE] Work Log                             │
│ ─────────────────────────────────────        │
│ [edict-a3f8] Implementing auth changes...    │
│  wrote: src/auth/handler.go                  │
│  wrote: src/auth/middleware.go               │
│  manifest: staged (2 files)                  │
│                                              │
│ [edict-b7c2] Adding logout endpoint...       │
│  wrote: src/auth/logout.go                   │
│  manifest: live (1 file, sha:e4f1a2)         │
│                                              │
└──────────────────────────────────────────────┘
```

#### Ruling Tab — Campaign Tempo

The Ruling tab is the court. You talk to the **Chancellor**, edicts flow through phases (classify → plan → forge → judge → review), rituals orchestrate ministers. This is deliberate, structured work.

#### Hunting Tab — Skirmish Tempo

The Hunting tab is where agility lives — knight moves, L-shaped lateral thinking. You talk to **Chancellor** (門下侍中), who sits outside the court hierarchy.

**Chancellor** sees everything: edicts, ling, manifests, precedents, code — full read-only access across the entire system. Its job is to distill the Ruler's *ren* (仁) — to help fuzzy intent crystallize into sharp edicts.

```
Hunting tab                          Ruling tab
───────────                          ──────────
You + Chancellor                      Chancellor + Court
    │                                     │
    ├─ "this auth is brittle"             │
    ├─ Chancellor probes, questions        │
    ├─ intent crystallizes                │
    ├─ creates edict ────────────────────►│
    │   [edict-a3f8b2c1](ruling://...)    ├─ Chancellor classifies
    │                                     ├─ ritual kicks off
    │                                     ▼
```

When Chancellor creates an edict, the Hunting tab shows a clickable link. Clicking it switches to the Ruling tab, scrolled to where that edict's conversation begins.

#### Minister Tabs — Observation

`:tabnew <minister>` opens a read-only tab showing that minister's work log.
Useful for monitoring what a minister is doing without interrupting the court.

Examples:
- `:tabnew forge` — file changes, manifests, commit history
- `:tabnew judge` — test runs, verdicts
- `:tabnew chancellor` — review outcomes, precedents
- `:tabnew war` — battle plans, lings

| Aspect | Ruling | Hunting | Minister |
|--------|--------|---------|----------|
| **Counterpart** | Chancellor | Chancellor | — (scroll-only log) |
| **Tempo** | Campaign (castle-siege) | Skirmish (swift-strike) | Observation |
| **Purpose** | Execute edicts | Spot openings, distill intent | Monitor minister activity |
| **Edict creation** | Ruler dictates directly | Chancellor distills from conversation | — |
| **Awareness** | Court ministers only | Full read-only across all state | Single minister's history |

---

## Architecture

### Core Types

```go
// Minister is the shared interface for all Court ministers
type Minister interface {
    ID() string
    Role() string
    Scratchpad() string
    Tools() []Tool
    Tasks() chan<- *Task
    Events() chan<- *Event
    Run(ctx context.Context)
}

// Prompt carries the Ruler's message to the Chancellor
type Prompt struct {
    Message      string
    SessionID    string            // empty = new session
    ContextFiles map[string]string // files loaded via @ references
}

// Task carries work from Chancellor to a Minister
type Task struct {
    EdictID     uint
    Work        string
    Done        chan<- Result
}

// Result signals a Minister has completed a Task
type Result struct {
    MinisterID string
    Sealed     bool
    Output     string
    Err        error
}
```

Every minister embeds `MinisterBase`, which provides database access, session creation, event emission, and zhengming requests.

### Bootstrap Sequence

1. **`NewCourt(db, cfg, runner, logger)`** — creates all six ministers with a shared `MinisterBase` (db, runner, logger). No LLM client yet.
2. **`ConfigureModel(model, sessionConfig, repoInfo)`** — injects the LLM client and config into every minister via `SetMinisterConfig()`.
3. **`Start(ctx)`** — loads rituals from embedded YAML + `.agents/rituals/`, launches each minister's `Run()` goroutine, starts the ritual guard polling loop, and invokes the `wakeup` ritual.
4. **TUI sends a `Prompt`** to `chancellor.Prompts` channel.
5. **Chancellor** creates an edict (`PhaseBrewing`), creates a `Session`, and streams the LLM response back to the TUI via notify callbacks.
6. **LLM tool calls** (`enact_ritual`) dispatch work to ministers through ritual workflows.

### Threading Strategy

The Court uses a **channel-based actor pattern** built on Go's CSP primitives.

**Minister goroutines** (persistent): Each minister's `Run()` method blocks on a `select` over its task channel and the context's Done channel:

```go
func (m *SomeMinister) Run(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case task := <-m.tasks:
            m.processTask(ctx, task)
        case event := <-m.events:
            m.handleEvent(ctx, event)
        }
    }
}
```

**Task dispatch** (synchronous blocking): A ritual step sends a `Task` on the target minister's channel, then blocks on the per-call `Done` channel waiting for a `Result`. The ritual's **idle timer** prevents indefinite blocking — if the minister produces no output for `step_idle_timeout` (default 5m), the step context is cancelled.

```
Ritual Runner / Chancellor       Minister goroutine
       │                              │
       ├── Task ──────────────────►   │
       │   (blocks on Done chan)       ├── processTask()
       │                              │
       │   ◄───────── Result ─────────┤
       │                              │
```

**Ritual runner**: Runs in a separate goroutine spawned by `StartRitual()`. Dispatches `Task` messages to ministers sequentially according to the ritual's step order.

**Ritual guard**: A polling loop (`runRitualGuard()`) that ticks at a configurable interval and runs the `RitualGuardRunner.Run()` method with a per-cycle timeout (default 30s). The guard processes events from the event registry.

**Cancellation**: `Court.Stop()` cancels the root context, which propagates to all minister goroutines and the ritual guard.

### Event Registry

The Court maintains a central event registry. Ministers subscribe to event types they care about and receive them as tasks. Events flow through the registry, not directly between ministers.

| Event | Emitted by | Subscribers |
|-------|-----------|-------------|
| `edict_created` | Chancellor | War |
| `phase_changed` | any minister | Chancellor |
| `zhengming_raised` | any minister | Chancellor |
| `zhengming_answered` | Chancellor | originating minister |
| `manifest_committed` | Forge | Judge |
| `manifest_rejected` | Judge, Chancellor | Forge |
| `verdict_delivered` | Judge | Chancellor, Chancellor |
| `precedent_recorded` | Chancellor | Chancellor |
| `ritual_step_started` | Ritual Runner | — |
| `ritual_step_completed` | Ritual Runner | — |
| `ritual_step_failed` | Ritual Runner | — |

### Tool-Level Isolation

Ministers are isolated by **tool catalogs** — each minister receives a different set of tools when its `Session` is created.

| Minister | File Tools | Shell | DB Tables | Orchestration Tools |
|----------|-----------|-------|-----------|-------------------|
| **Chancellor** | read-only (list, read, read_many, grep) | no | edicts, zhengming, forge_manifests, ling | create_edict, enact_ritual, asimi_sql |
| **War** | read-only | no | ling | create_ling, list_ling, update_ling |
| **Forge** | read-write (read, write, replace, list, read_many, grep) | yes | forge_manifests | create_manifest, update_manifest, commit_manifest |
| **Judge** | edit (read, write, replace, list, read_many, grep) | yes | verdicts, forge_manifests | record_verdict, reject_manifest |
| **Chancellor** | read-only (all tables) | no | edicts, ling, forge_manifests, verdicts, censor_precedents | create_edict, record_precedent |

Key constraints:
- **War cannot write code** — it only plans (ling) and reads.
- **Chancellor cannot write files** — it orchestrates, never implements.
- **Only Forge and Judge have shell access**.
- **Chancellor sees all but changes nothing** — full read-only across every table; can only create edicts and record precedents.
- All ministers share `ask_ruler` and `tian_ledger` as common tools — the Tian ledger is open to every minister. `consult_minister` is available only to the secretary via `extra_tools`.

The `Session` also enforces **write protection** — a file must be read via `read_file` before it can be written via `write_file`. This is tracked per-session in `filesRead`.

### Constitutional Summary

| Concern | Prevention Mechanism |
|---------|---------------------|
| **Minister writes code it shouldn't** | Tool catalogs: only Forge and Judge get write/edit tools |
| **Minister runs shell commands it shouldn't** | Shell tool only granted to Forge, Judge |
| **Intent drift** | Chancellor classifies edicts; War plans before Forge implements |
| **Ambiguity propagation** | Zhengming: any minister can halt and request clarification |
| **Runaway minister** | Idle timer aborts silent steps after `step_idle_timeout` (default 5m); context cancellation propagates |
| **Tool call loops** | Session detects repeated identical tool calls after 3 attempts; invokes `report_failure` ritual |
| **Unauthorized file writes** | Write protection: file must be `read_file`'d before `write_file` or `replace_text` |
| **Ritual failure** | Step-level retry, goto, abort; Chancellor can regress to War |
| **Precedent violation** | Chancellor logs all rulings to `censor_precedents` table as searchable case law |

---

## Configuration

### Court Settings

Configured in `internal/config/types.go` under `CourtConfig`, set via `.agents/asimi.toml`:

```toml
[court]
poll_interval = "5s"        # Ritual Guard polling interval (default: 5s)
step_idle_timeout = "5m"    # Max silence before aborting a step (default: 5m)
```

`step_idle_timeout` is an **idle** timer, not a wall-clock deadline: it resets
on every notification from the minister (stream chunks, tool calls, etc.).
A minister that is actively producing output — even if slowly — will never
hit this limit. Only truly stuck steps (no output at all) are aborted.
```

### Project Rituals

Create custom rituals in `.agents/rituals/*.yaml` to define project-specific workflows.

---

## Troubleshooting

### "LLM not configured"

The model hasn't connected yet. Wait for authentication to complete.

### Edict stuck in a phase

Check for pending Zhengming requests — the Court may be waiting for your input.

### Ritual failures

Check the Tian events to see what happened:

```
You: What happened with the logout feature?

Secretary: [Uses tian_ledger tool for edict issue-123]
  Events show: ritual_step_failed at step "build" - compilation error
```

For advanced queries, use `asimisql` with raw SQL.

