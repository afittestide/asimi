# Judge analysis — CI run 38125435933 (release v0.14.3)

**Verdict:** FAILED (rejected) — deterministic scheduling race in a test, not a product defect.
**Logs:** `heaven/logs/release-v0.14.3.full.log` (full run, 6806 lines),
`heaven/logs/release-v0.14.3.failed.log` (failed-step only).

## Provenance (tree under trial)
- Run: `38125435933` — workflow **CI**, event `push`, ref `refs/tags/v0.14.3`
- Commit: `39e6b2e7b888ce2324e53523f59ba9eef92eb251` — "chore: releasing 0.14.3"
- Tag: `v0.14.3` → HEAD; branch `main`; working tree **clean** (no staged/unstaged tracked changes at analysis time)
- Job **Test** (`114428709454`, 08:37:05Z→08:39:58Z) → step **Run tests** (`just test`) → exit 1
- Job **Release** (`114429217714`) skipped (`needs: test`)
- Runner: GitHub Actions `ubuntu-latest`; Go `1.26.9` (pinned in `.github/workflows/ci.yml`)
- Court manifests: **none** reference this commit (`get_manifest_by_commit` → not found); this
  run is not a judgment of a forged manifest, so no manifest was transitioned.

## What failed
`just test` failed in **package root** (`github.com/afittestide/asimi`), one test:

```
=== RUN   TestWatchConnDropObservesReconnect
2026/10/11 08:39:32 INFO reconnecting: reconnected successfully
    autostart_test.go:270: watchConnDrop never observed the drop
--- FAIL: TestWatchConnDropObservesReconnect (2.00s)
FAIL
FAIL	github.com/afittestide/asimi	20.874s
```

All other packages passed (`internal/...`, `storage`). Zero other `--- FAIL`.

## Root cause — a race between first-conn subscription and the reconnect swap

The test (`autostart_test.go:229-279`) asserts that `watchConnDrop` observes the drop of
the *first* connection:

```go
first := rc.Conn()                                   // :248  the initial conn
go func() { lost <- watchConnDrop(rc)() }()          // :257  subscribes ASYNCHRONOUSLY
if err := first.Close(); err != nil { ... }          // :261  kill the first conn
select {
case msg := <-lost: ...                              // :263
case <-time.After(2 * time.Second):
    t.Fatal("watchConnDrop never observed the drop") // :270  <- observed failure
}
```

`watchConnDrop` blocks on `courtClient.ConnDone()` (`tui.go:1682-1691`), and
`ReconnectingClient.ConnDone` (`internal/rpc/reconnecting.go:666-673`) reads the
**current** `rc.conn` under `RLock` and returns *that* conn's `Done()` channel.

Nothing synchronises step :257 (the watcher must subscribe to `first.Done()`) against
`watchConnection`/`reconnect` (`internal/rpc/reconnecting.go:91-114`) swapping `rc.conn`
to the freshly dialled conn. When the swap wins the race:

1. `first.Close()` closes `first.Done()`; `watchConnection` wakes and reconnects, installing
   the second conn — logged `INFO reconnecting: reconnected successfully`.
2. The `watchConnDrop` goroutine finally runs and calls `ConnDone()`, which now reads
   `rc.conn` = **second** (a live, open conn) and returns its **open** `Done()` channel.
3. `watchConnDrop` blocks on a channel that will never close → the 2s test deadline expires
   at `autostart_test.go:270`.

The failing log confirms the order: the `reconnected successfully` line is printed
**before** the `never observed the drop` fatal — i.e. the reconnect won the race. This is a
defect in the **test**, not in the product: `ConnDone`'s contract explicitly documents
"Callers should re-call ConnDone after a reconnect" (`reconnecting.go:662-665`), and the
product path (`handleConnectionLost` → `waitForConnRestore` → `handleConnectionRestored`)
re-arms the watcher after a drop. The test instead arms the watcher once, concurrently with
the kill, and never pins it to the first conn.

## Adversarial cross-checks (not silent-discounted)
- **Is this the old e911 flake?** No. `TestInstallDaemonAutostartUsesReconnectingClient`
  is now **SKIP**ped (`autostart_test.go:309`, "flaky daemon-reconnect guard; rewritten by
  follow-up flake-fix edict"). The failing test, `TestWatchConnDropObservesReconnect`, is
  its replacement guard — and it reintroduces the same *class* of race. The regression guard
  for e911 is still not deterministic.
- **Is the vuln gate (the v0.14.2 failure) still the cause?** No. `.github/workflows/ci.yml`
  now pins Go `1.26.9`, and this run reached the unit-test phase and failed there. The
  v0.14.2 vuln findings are remediated; this is a different, independent failure. (The
  cached `release-v0.14.2.analysis.md` in this directory does **not** describe the latest run.)
- **Flaky or deterministic?** The race is real and reproducible under scheduling jitter; in
  CI it presents intermittently (a genuine flake), but the race window itself is
  deterministic — it is not a false alarm.

## Remediation (fix the test; do not weaken the guard)
1. Remove the race: subscribe the watcher to the targeted conn's `Done()` **before** killing
   it, e.g. capture `done := rc.ConnDone()` (or call `watchConnDrop` against a client pinned
   to `first`) and only then `first.Close()`. Synchronise on conn state, do not sleep/poll.
2. Or assert drop directly on `first.Done()` and assert restore on the swapped conn, matching
   the product's re-arm sequence in `handleConnectionRestored` (`tui.go:4130-4141`).
3. Keep it a real guard: it must still fail if the reconnect never swaps in a live conn.
4. `just test` and `just lint` must pass; commit prefix `fix:`, ending `e{edict}`.

## Verdict
Heaven is binary: `just test` failed on `39e6b2e7b` (v0.14.3), so the tag's build is
**rejected**. The cause is a scheduling race in `TestWatchConnDropObservesReconnect`
(the e911 regression guard), not a production regression.
