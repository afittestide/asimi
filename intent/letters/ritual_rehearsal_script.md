# Letter to the Secretary: A Dedicated Edict for the Ritual Rehearsal Script

Your Majesty's ruling of this day (zhengming request `8cd7fcfbd9cc9e58`):
**"New dedicated edict for the ritual rehearsal script fix."**

Propose this edict via `suggest_edict` and proceed through swift-strike. The
reference implementation already sits in the borderlands (`court/ritual.go`,
`court/ritual_test.go`) — the forge should verify, not rediscover.

## Problem

In harbor trial `jobs/2026-09-30__15-29-56/hello-world__neG2YAP`, swift-strike's
forging step received only `Implement the changes for the edict: {{ .edict }}`.
In a gitless trial container there is no AGENTS.md, hence no scratchpad, no
skill list, no schema notes. The forge minister did not know what the judging
step would run against its work — so it spent ~100 seconds and ~40 tool calls
reverse-engineering its own 60MB binary (`strings`, `grep -a -b -o`, `dd` at
byte offsets) to recover the ritual definition embedded in it, hunting
specifically for `given: "!just test"` — the one fact that would tell it what
shape its scaffolding must take. The 120s `AgentTimeoutError` killed it
mid-`dd`, one or two probes from a working plan. The verifier still scored
reward 1.0: the task itself took the agent 15 seconds.

IRL ritual participants rehearse for hours, observing the other participants'
motions. Our ministers receive one line of the script and must guess the
ceremony. This is not a model problem — it is a context problem, and it will
reproduce in every trial, every gitless ground, out of the box.

## Change

Every minister performing a ritual step receives the full ritual choreography
in its context, alongside the existing court-history scratchpad:

- All steps in order: name, minister, `given` commands **verbatim** (this is
  the load-bearing part — the forge must see `!just test`), act summary,
  `then` conditions, `on_failure` routing (`goto → target`).
- The step being performed is marked (▶), with a closing line telling the
  minister that steps after it will consume its output.
- Must cover fork `work:` steps (castle-siege, fix-lint) — they flow through
  `executeStep` → `executeMinisterStep`, so one injection point covers all.
- Must work in gitless ground: injection via the one-shot session scratchpad
  (`SetScratchpad`), not via files on 地.

## Reference implementation (already in borderlands, uncommitted)

- `court/ritual.go`:
  - `renderRitualScript(def *RitualDef, currentStepName string) string` —
    renders the choreography from the parsed `*RitualDef` (already on
    `exec.def`; no new plumbing).
  - `composeStepScratchpad(parts ...string) string` — joins script + court
    history, skipping empties.
  - `executeMinisterStep` now calls `composeStepScratchpad(
    renderRitualScript(exec.def, step.Name), r.buildStepScratchpad(exec, step))`
    inside the existing `exec.EdictID > 1` scratchpad block.
- `court/ritual_test.go`: `TestRenderRitualScript` (ordering, marking, given
  verbatim, act summary, then/on_failure routing), `TestRenderRitualScript_NilDef`,
  `TestRenderRitualScript_TaskAlias`, `TestComposeStepScratchpad`,
  `TestRenderRitualScript_ForkStep`.
- Status: full `go test ./...` green, `gofmt` clean, `just lint` clean.

## Acceptance

1. Any minister, any ritual step, any trial: reading its task, it knows who
   comes before and after it, what commands the judge will run, and where
   failures route — without mining the binary or guessing.
2. A hello-world harbor trial completes swift-strike (or project-init)
   well within the default 120s agent timeout.
3. Fork work steps receive the script of their parent ritual.
4. No behavior change when the scratchpad has neither script nor history
   (empty composition injects nothing).

## Conventions

- Commit prefix `feat:`, ending `e{id}` with the new edict's number (e.g. `e891`).
- The `CHANGELOG.md` date bump already staged in the Middle Kingdom belongs to
  other work — do not fold it into this edict's commits.
