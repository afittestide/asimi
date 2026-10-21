# Judge analysis — CI run 38145942622 (release v0.14.4)

**Verdict:** FAILED (rejected) — a scheduling race in a test, on the `test-race` gate, not a product defect.
**Logs:** `heaven/logs/release-v0.14.4.full.log` (6821 lines),
`heaven/logs/release-v0.14.4.failed.log` (6448 lines).

## Provenance (tree under trial)
- Run: `38145942622` — workflow **CI**, event `push`, ref `refs/tags/v0.14.4`
- Commit: `e913736ed8f0f0bea38b524f7c4391475fd183a2` — "chore: releasing 0.14.4"
- Tag: `v0.14.4` → HEAD; branch `main`; working tree clean at analysis time.
- Parent commits: `e9298b5b5` ("fix: flaky reconnect e917"), `39e6b2e7b` (v0.14.3).
- Job **Test** (`114489186773`, 14:08:03Z→~14:14Z) → step **Run tests** (`just test`) → exit 1
- Job **Release** (`114490378163`) skipped (`needs: test`)
- Runner: GitHub Actions `ubuntu-latest`; Go `1.26.9` (pinned in `.github/workflows/ci.yml:23`)
- Court manifests: **none** reference this commit (`get_manifest_by_commit` → not found); this run
  is not a judgment of a forged manifest, so no manifest was transitioned.

## What failed
`just test` (`Justfile:56-69`) runs the plain suite, then `just test-race`, then `just vuln`.
**The plain suite passed** (all packages, incl. `court` → lines 4887-4888 `ok .../court 17.541s`).
The failure is in the **`test-race`** gate (`Justfile:76-80`, scoped to `./court/`):

```
2026/10/11 14:13:16 WARN private-network provider egress enabled provider=openai base_url=http://10.0.0.5
--- FAIL: TestCoreToolScheduler_Schedule_Error (0.00s)
    session_test.go:1027: Error: Should be true
FAIL	github.com/afittestide/asimi/court	34.872s
error: Recipe `test-race` failed with exit code 1
error: Recipe `test` failed with exit code 1
```

**No `WARNING: DATA RACE`** appears anywhere in the run (0 occurrences). This is a plain
assertion failure, surfaced only under `-race` because the added synchronization/goroutine
timing changes the interleaving.

## Root cause — result-channel delivery races the notify dispatch

`TestCoreToolScheduler_Schedule_Error` (`court/session_test.go:1009-1028`) does:

```go
var errorNotified atomic.Bool
notify := func(msg any) { ...Store(true) }          // :1014-1018
scheduler := runners.NewCoreToolScheduler(notify)   // :1020
ch := scheduler.Schedule(ctx, tool, "{}")           // :1023
result := <-ch                                      // :1024
assert.Error(t, result.Error)                       // :1026
assert.True(t, errorNotified.Load())                // :1027  <- observed failure
```

The mock returns `assert.AnError` (`session_test.go:987-989`). In
`executeCall` (`internal/runners/scheduler.go:165-219`) the error branch does, **while holding
`s.mu`**:

1. `notifications = append(..., ToolCallErrorMsg{...})` — :178-186 (buffered, not yet sent)
2. `resultChan <- ToolCallResult{Error: err}` — :189  ← **releases the test's `<-ch`**
3. `close(resultChan)` / deletes — :209-213
4. `s.mu.Unlock()` — :217
5. `s.sendNotifications(notifications)` — :219 ← **actually calls `notify`**

So the test is unblocked at step 2, but `notify` is not invoked until step 5, *after* the
mutex is released. There is **nothing ordering step 2 against step 5**. The scheduler never
promised that the notification callback has run by the time the result is readable. When the
test goroutine wins the race (a `t.Parallel()` run under `-race` on a loaded CI box), it reads
`errorNotified.Load()` as `false` → `:1027` fails.

`Schedule` has the same shape: it returns `resultChan` to the caller and *then* calls
`s.sendNotifications` (:132-134), so `ToolCallScheduledMsg`/`ToolCallExecutingMsg` are also
dispatched after the handle is live.

## Reproduction (local)
`GOTOOLCHAIN=auto go test -race -tags containers_image_openpgp -run 'TestCoreToolScheduler_Schedule_Error$' -count=200 ./court/`
→ **3 failures / 200** (~1.5%). The race window is real and deterministic; CI presents it
intermittently. This is the same failure the v0.14.4 run reported.

## Product vs test verdict
This is a defect in the **test's expectation**, not in the product:
- `executeToolCall` (`court/session.go:2101-2117`) consumes **only** the result channel; the TUI
  consumes the **notifications**, and there is no production ordering guarantee that a
  notification has fired before its result is readable. Production code does not depend on the
  test's assumed ordering.
- The fix is to make the assertion synchronize on the notification itself (e.g. a channel that
  `notify` closes on `ToolCallErrorMsg`, received with a bounded timeout—not a fixed sleep),
  or to have `executeCall` send notifications *before* writing the result channel so the
  observable order matches. The first is the smaller, contract-preserving change.

## Adversarial cross-checks (not silent-discounted)
- **Not the v0.14.3 flake.** v0.14.3 failed in package root on `TestWatchConnDropObservesReconnect`
  (reconnect race). e917 (`e9298b5b5`) fixed the reconnect guards, and this run's plain suite
  **passes** package root — so the reconnect guard is green here. This is a *different* flake and
  a *different* package (`court` vs root).
- **Not the v0.14.2 vuln gate.** Go is pinned `1.26.9` and the run reached `test-race`; the vuln
  step (`Justfile:67-69`) was never reached. Already remediated by the v0.14.3 release work.
- **`-race` is the trigger.** Plain `just test` passed; `just test-race` failed. The race recipe
  is the permanent gate by design (`Justfile:76-77`) — this is exactly the class of flake it was
  added to catch, except here it is the *test* racing, not the product.
- **Flaky or deterministic?** The race window is deterministic; it manifests intermittently
  (~1.5% locally under `-race -count`). Not a false alarm.

## Remediation (fix the test; keep it a real guard)
1. In `TestCoreToolScheduler_Schedule_Error` (`court/session_test.go:1009-1028`), replace the
   `atomic.Bool` + post-hoc read with a signal that `notify` closes when it sees
   `ToolCallErrorMsg` (e.g. `done := make(chan struct{})`; `close(done)` in the error case),
   then `select { case <-done: ...; case <-time.After(N): t.Fatal(...) }`. No fixed sleep for
   success.
2. Alternatively (product-side, if the ordering is intended to be observable), move
   `s.sendNotifications(notifications)` in `executeCall` (`internal/runners/scheduler.go:219`)
   to run **before** the `resultChan <- ...` send. Prefer the test fix unless review wants the
   stronger contract.
3. Audit sibling tests for the same assumption (e.g. `court/session_test.go:363`,
   `court_test.go:496-500`) — they read results directly, and `ritual_test.go` consumes
   notifications from a channel, so they are less exposed. Keep it a real guard: the test must
   still fail if `executeCall` never emits `ToolCallErrorMsg` on error.

## Collateral anomaly (flagged, not silent-discounted)
`.github/workflows/ci.yml` pins **two different Go versions**: `1.26.9` for the `test` job
(:23) but `1.26.6` for the `goreleaser` job (:60). The release job therefore builds/tests on a
toolchain the test gate did not vet, and 1.26.6 carries the stdlib findings from the v0.14.2
analysis. Not the cause of this failure (the release job was skipped) — but it should be
harmonized.

## Verdict
Heaven is binary: `just test` failed on `e913736ed` (v0.14.4) at the `test-race` gate, so the
tag's build is **rejected**. The cause is a scheduling race in
`TestCoreToolScheduler_Schedule_Error` — a test that reads a notification flag before the
scheduler has dispatched the notification — not a production regression.
