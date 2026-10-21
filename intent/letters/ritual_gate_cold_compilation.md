# Letter to the Secretary: The Ritual Gate's Bottleneck Is Cold Compilation

By the Ruler's ruling of 2026-10-21 (zhengming request `b78090859cf300dd`):
**"Diagnosis only — report to the Court, file no edict"**, with the direction
that both levers below be recommended, and that the secondary defect found along
the way be filed as **its own incident**. This letter is that report. No edict was
filed; nothing was written to 地, and no seal, verdict, or manifest was recorded.

The complaint was that test running time has become the bottleneck slowing the
rituals. The measurements say the complaint is correct, but the cause is not the
tests. **It is compilation**, and each edict pays for it in full, in a container
that starts with an empty Go build cache.

## Provenance of the tree under trial

- **Host:** Linux aarch64, **4 cores** (`nproc`=4), **1941 MB RAM, no swap**,
  `go1.26.9 linux/arm64`.
- **Repository:** `afittestide/asimi`.
- **Commits:** measurements span two trees. The bulk were taken at `88d1a6a10`
  with a dirty borderlands (`court/tools/approve_doc.go`,
  `approve_doc_test.go`, `builtin.go`, `builtin_test.go`, `shell.go` modified;
  `internal/ministers/userconf/ministers.yaml` staged). While this investigation
  ran, three commits landed — `23e259394` (e971), `4ab1f1744` (letters),
  `9094ff1e2` (release-gate letter) — moving `HEAD` to `9094ff1e2`. The
  structural conclusion (the gate is compile-bound and cache-driven) does not
  depend on which of the two trees is in front of you.
- **Container:** per-edict podman containers, `asimi-shell-<slug>-e<N>`
  (`court/court.go:1136-1160`, `:715`).

## What the gate actually costs

`just test` is the Judge's gate in `swift-strike` (`given: "!just test"`,
`court/builtin_rituals.yaml:28`). It runs **two** Go invocations: the full suite
and then `test-race`.

| Phase | cold cache | warm cache |
|---|---|---|
| `go test -tags containers_image_openpgp ./...` | **77 s** | 23 s |
| `go test -race -parallel 1 ./court/` | **82 s** | 31 s |
| **`just test` (both of the above, as the gate runs them)** | **217 s**, **353 s** | **24 s** |

The spread on the cold figure is real, not noise in the report: 217 s and 353 s
are two separate cold `just test` runs on the same host.

The Go build cache is what changes: **20 K when empty → 818 MB** after the plain
suite → **1.6 GB** after the race run.

Meanwhile the tests themselves are cheap. Summed per-test elapsed time:

- root package: **949 tests, 12.7 s**
- court package: **809 tests, 13.5 s**

The two slowest bodies are small and already known:
`TestInstallDaemonAutostartUsesReconnectingClient` (4.9 s — its success path is
fast; `reconnectSafetyNet` is 35 s and is only a backstop,
`autostart_test.go:239`), and `TestHarborIntegration_ASIMIHomeDBFallback`
(7.8–12.9 s in the release logs).

So of a ~217–353 s gate, roughly **150–330 s is building**. Optimizing test
bodies cannot reach the problem the Ruler is feeling.

## Root cause: every edict compiles from zero

Per-edict runners exist so concurrent edicts never share a container
(`Court.RunnerForEdict` → `NewPodmanRunner(..., fmt.Sprintf("e%d", edictID))`).
Container creation (`internal/runners/podman.go:375` onward) mounts only:

- the project root (bind),
- `[container.additional_mounts]`,
- platform overlays.

**There is no persistent Go build cache among those mounts.** The
`--mount=type=cache,target=/root/.cache/go-build` and `/go/pkg/mod` entries in
`.agents/sandbox/Dockerfile:44-45` apply to the **image build** only — they keep
the cache out of the committed layer (commit `b1bde5b4c`, "fix: OOM due to git &
go cache duplication"). They do nothing at run time.

The consequence: each edict's first `just test` reconstructs up to ~1.6 GB of
compilation artifacts, and because the gate covers the whole `./...` tree, that
per-edict cost is not amortized across edicts. This is the same shape as the
finding already recorded in incident `ad64399312711c74`, which names the cold
build as the thing that gets OOM-killed.

## Lever 1 — give per-edict containers a persistent shared Go cache

Recommended, structural. Provision a named volume for `GOCACHE` (and
`GOMODCACHE`) as a platform overlay, written once on image build and shared
read-mostly across edict containers. This converts a per-edict cold cost into a
one-time cost — the difference between ~160–350 s and ~25 s for each ritual's
judge step.

## Lever 2 — re-validate the baked-in `-p=1` throttle before removing it

Also recommended, but **only after re-validation**, for the reason in the caveat.

`.agents/sandbox/Dockerfile:13-15` bakes in:

```
ENV GOFLAGS=-p=1 GOMAXPROCS=2 GOMEMLIMIT=1500MiB
```

Cold-cache totals, same host, same tree:

| parallelism | plain | race | total | compiler kills |
|---|---|---|---|---|
| Go default | 78 s | 82 s | **160 s** | 0 |
| `-p=1` / `GOMAXPROCS=2` | 174 s | 173 s | **347 s** | 0 |

That is **2.2×** paid for no benefit observed here. The per-setting detail:

| setting | cold `go build` | peak RSS of build |
|---|---|---|
| `-p=1`, `GOMAXPROCS=1` | 157 s | 1047 MB |
| `-p=2`, `GOMAXPROCS=2` | 68 s | 1032 MB |
| `-p=4`, `GOMAXPROCS=4` | 58 s | 1235 MB |

**Caveat that must not be skipped.** Incident `ad64399312711c74` (2026-10-17,
"the sandbox container has no memory bound and Go compiles with default full
parallelism… exceed the ~2GB podman VM and get OOM-killed") is **still
`status=open`**, and it is the justification for the throttle. The runs above had
`GOFLAGS` and `GOMAXPROCS` **unset**, and reported **0** `signal: killed`.
A `go test ./...` cold run at default parallelism with `GOMEMLIMIT` also unset
completed in **69 s with 16/16 packages ok and 0 killed**.

Those runs are **not** environment-matched proof that the edict-948 hazard is
gone. They were taken on the host, not inside the runner's sandbox, and with the
runner's exec env not pinned. Removing the throttle without clearing
`ad64399312711c74` would be acquitting against an open heavenly failure record.
**Re-validation must be run in the sandbox, with the container exec env pinned,
before the throttle is lifted.**

## Secondary defect — the result channel can precede the terminal notification

Found by accident, and filed separately per the ruling (no edict here — its own
incident).

While taking the cold default-parallelism measurement, the plain suite returned
**rc=1** on an unrelated test:

```
--- FAIL: TestCoreToolScheduler (0.01s)
    scheduler_test.go:70: Error: "2" is not greater than or equal to "3"
panic: runtime error: index out of range [2] with length 2
    internal/runners/scheduler_test.go:75
FAIL github.com/afittestide/asimi/internal/runners
```

The cause is **ordering, not load**. In `executeCall`
(`internal/runners/scheduler.go:165`; the success arm at `:194-219`) the success
message is appended to
`notifications` *and* the result is delivered on `resultChan` (`:206`) — **both
while `s.mu` is still held**. Both are dispatched only after `s.mu.Unlock()` →
`sendNotifications` (`scheduler.go:90-97`). A consumer that waits on
`resultChan` can therefore observe the result **before** `ToolCallSuccessMsg` has
been emitted, so it sees fewer than the three terminal messages the test expects
(`scheduled`, `executing`, `success`).

Honest limitation: I could **not** reproduce it standalone. 200 reps at
`GOMAXPROCS=1` and 100 reps in one binary all passed. It has only been observed
once, under real full-suite load. That is consistent with a genuine ordering race
whose window is wide only when the machine is saturated — the same saturation the
gate itself creates.

## Containment and collateral

- **Nothing was written.** No files, manifests, verdicts, seals, commits, or
  incidents were created by this investigation. `git status --porcelain` is empty.
- **Collateral that is worth knowing:** the work required repeated `go clean
  -cache` runs and full cold builds of a very heavy dependency tree on a 1.9 GB
  machine. This **restarted the shared sandbox several times**, aborting this
  session's own in-flight tool calls. Combined with the open cross-session
  cancellation incident `b3644ac4e364f3ec` — where one container's health-check
  churn guillotines *other* edicts' in-flight tool calls — this means that
  hammering the gate is itself a resource hazard while rituals are live. Any
  re-validation under Lever 2 should be run deliberately, not alongside running
  edicts.

## Disposition requested of the Secretary

1. Record this report to the Court; **no edict** (per the ruling).
2. Carry both levers as the recommendation: a **shared persistent Go build
   cache** for per-edict containers, and **re-validation of the baked-in `-p=1`
   throttle**, the latter gated on clearing or amending `ad64399312711c74` with
   sandbox-side evidence.
3. Open the `executeCall` ordering defect as **its own incident**, with the
   repro caveat above stated plainly rather than overstated.
