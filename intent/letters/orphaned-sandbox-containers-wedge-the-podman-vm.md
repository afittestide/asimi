# Letter to the Secretary: Orphaned Sandbox Containers Wedge the podman VM Disk

By the Ruler's ruling of 2026-10-22 (zhengming request `653554b6399f2643`):
**"Emergency cleanup NOW ... AND forge an edict for startup reconciliation so
orphans are reaped automatically"**, with a **disk-pressure guard that prunes
dangling build artifacts**.

This letter is the second half of that ruling. The emergency cleanup is already
done and verified (below); the structural remedy is what this letter asks the
Secretary to turn into an edict. I am the Forge: I have diagnosed the defect and
repaired the host this session, but forging the code change must go through an
edict, verdicts, and the seal chain — not my own pen on `main`.

Origin incident: **`edda2cb3f55e4cad`** (high) — "the build environment is out of
disk space"; e973's release gates fail at step 0.

## What was observed (verified, 2026-10-22)

- podman was unusable. `podman build` failed immediately:

  ```
  Error: mkdir /var/tmp/libpod_builder2117089511: no space left on device
  ```

- The podman VM disk was 100% full:

  ```
  Filesystem      Size  Used Avail Use% Mounted on
  /dev/vda4        40G   40G   36K 100% /
  ```

- `podman system df`:

  ```
  TYPE           TOTAL   ACTIVE  SIZE      RECLAIMABLE
  Images         63      5       10.52GB   10.52GB (100%)
  Containers     16      16      21.4GB    0B (0%)
  Local Volumes  5       8       3.524GB   1.303GB (37%)
  ```

- **16 containers were running, but only 3 belonged to the live daemon.** The
  live daemon (`./asimi --debug`, PID 55644) started at 11:09 today and owns
  exactly `asimi-shell-afittestide-asimi-cli-1`, `…-e974`, `…-e982` (started
  11:11–11:12). The other **13 were started Oct 18–21 by now-dead sessions**:

  ```
  2.7G asimi-shell-afittestide-asimi-cli-e939   started 2026-10-18 13:33
  2.6G asimi-shell-afittestide-asimi-cli-e957   started 2026-10-18 15:59
  2.2G asimi-shell-daonb-harbor-e964            started 2026-10-19 14:09
  2.2G asimi-shell-daonb-harbor-e965            started 2026-10-19 14:10
  2.6G asimi-shell-afittestide-asimi-cli-e967   started 2026-10-19 14:26
  2.8G asimi-shell-afittestide-asimi-cli-e970   started 2026-10-21 10:17
  2.7G asimi-shell-afittestide-asimi-cli-e966   started 2026-10-21 14:08
  1.1G asimi-shell-afittestide-asimi-cli-e977   started 2026-10-21 18:45
  ... plus 5 small ones (harbor-2/-8, data-poisoning-3/-7, 74305)
  ```

  Each multi-GB container held the **ephemeral** Go caches in its writable
  layer — `/root/.cache/go-build` ≈ 1.7 GB and `/go/pkg/mod` ≈ 1.1 GB. These are
  exactly the paths the sandbox Dockerfile's `--mount=type=cache` keeps *out of
  the image*, but nothing keeps them warm (or bounded) at runtime, so every
  container re-creates them and dies holding them.

## Emergency cleanup already performed (host action, not code)

During this session, to unwedge the host:

1. Evicted the Go caches from the 13 orphan layers directly in the VM (freed
   ~10 GB — podman itself could not even open its state DB until space existed:
   `saving container state: unable to open database file`).
2. `podman rm -f` the 13 orphan containers.
3. `podman image prune -f` (2 dangling images) and `podman builder prune -f`.

Verified after: `podman run` and `podman build` both succeed; VM is back to
`21G/40G` used (52%).

**This cleanup is a manual tourniquet, not a fix.** The next daemon restart will
leave another set of orphans.

## Root cause (structural)

`Court.flatlineIdleRunners` (`court/court.go:1250`) reaps only containers whose
edicts are present in the **current process's in-memory** `edictRunnerLastUsed`
map:

```go
s.edictRunnersMu.Lock()
for id, last := range s.edictRunnerLastUsed { ... }   // empty after a restart
```

On daemon restart that map is empty, so containers created by the **previous**
process are unreachable forever. They keep running `sleep infinity` and keep
their multi-GB writable layers. Disk usage therefore grows **monotonically
across sessions** until the VM wedges and every ritual fails. Nothing reconciles
on-disk podman state against the live process at startup.

This is why the incident is recurring: the same class of failure was recorded
before as `bfdc3f5bf887e784` ("Container storage is out of disk space…"), and the
"remedy" each time has been manual pruning.

## Change requested (invariant is the deliverable; the Forge chooses the mechanism)

**Startup reconciliation.** On Court/daemon startup, no
`asimi-shell-<slug>-*` container may survive **solely** because the current
process does not know about it.

- Candidate mechanism: stamp every container at create time with `specgen`
  labels — `asimi.project=<slug>`, `asimi.owner_pid=<pid>`, and `asimi.edict=<N>`
  when applicable. On startup, list this project's containers and remove those
  whose `asimi.owner_pid` is not a live process. PID is the natural owner: the
  connection/main runner already uses the client PID as its identifier
  (`commands.go:600`, `providers.go:210`, `internal/daemon/create.go:45`), and
  the court runner uses `os.Getpid()` (`court/ritual_expressions.go:1029`).
- Fallback for containers created before this lands (they carry no labels): fall
  back to name-prefix + age, but **never** remove a container whose owner may
  still be live — fail safe, not loud.

**Disk-pressure guard.** Add `[sandbox] disk_min_free_mib` (koanf; surface it in
`internal/config/types.go`, `internal/config/loader.go` `DefaultConfig().Sandbox`,
and `internal/config/default.conf` — a field with no template entry is a
bad-boy-scout violation, the e966 prior-rejection finding). When the VM's free
space drops below it, prune **reclaimable build artifacts** (dangling images,
builder cache) before creating a container. `0` disables.

## Non-goals / coordination

- **e977** (shared Go caches as named volumes at Go's default paths) is
  *complementary*: it makes a recreated container start warm. It does **not**
  reap orphans and does not bound the disk. Do not duplicate it; do not rely on
  it to fix this.
- **e978** (cgroup memory bound + OOM notify) is separate.
- `Justfile` `init-podman`'s `--disk-size`/`--memory` are intent-only on an
  existing machine; raising the VM ceiling is a host action, not code. Do not
  claim an edict fixes that.

## Verification (existing test files only — AGENTS.md)

- `internal/runners/podman_test.go` — assert containers are created with the
  owner/project labels (extend this file's existing spec-inspection style).
- `court/court_test.go` — assert startup reconciliation removes an orphan (owner
  PID dead) and never removes a container whose owner may be live; assert the
  disk-pressure guard prunes below the threshold and no-ops at `0`.
- Run `just test`, `just lint`, `just build`.

## Acceptance

1. Restarting the daemon/court leaves no orphan `asimi-shell` container from the
   previous process.
2. A container whose owning process is dead is removed at startup; a container
   whose owner is live is preserved.
3. When VM free space is below `disk_min_free_mib`, reclaimable build artifacts
   are pruned before a container is created; `0` disables.
4. `just test`, `just lint`, `just build` pass.
5. Incident `edda2cb3f55e4cad` can be closed once reconciliation is observed to
   reclaim orphans on a real restart.

## Conventions

- Commit prefix `bug:`, ending `e<N>` for the new edict's number.
- No change to the seal chain, realm model, or 礼 git-shim discipline.
