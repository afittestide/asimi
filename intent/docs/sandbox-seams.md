# Sandbox seams

Inventory of every seam through which host assets reach an asimi sandbox
session. Written for edict 1036 (gh #180, "Start with a trust dialog").

A *seam* is any path by which authority over a host asset crosses into a
session. The invariant this inventory serves:

> Authority to touch host assets must be granted by the Ruler to a specific
> session, and must not outlive the client process that holds it.

A committed file may **request** a seam; it may never grant one.

## The seams

| # | Seam | Code | Exposes | Consent today |
|---|------|------|---------|---------------|
| 1 | Working-tree bind | `internal/runners/podman.go` (createContainer, project-root bind) | project root, **rw** | implicit, always |
| 2 | Additional mounts | `internal/runners/podman.go` (`SandboxConfig.AdditionalMounts`) | host paths (gh, gcloud), **ro by default** | repository config *requests*; Ruler grants at the trust dialog |
| 3 | Passthrough env | `internal/runners/podman.go` (`SandboxConfig.PassthroughEnv`) | named host env vars (ADC, project, region) | repository config *requests*; Ruler grants at the trust dialog |
| 4 | Platform overlays | `internal/runners/podman.go` (`SandboxConfig.PlatformOverlays`) | named volumes over project paths (dirs **and** files) | repository config only |
| 5 | Cache volumes | `internal/runners/podman.go` (`namedCacheVolumes`) | slug-keyed named volumes | repository config only |
| 6 | **Host network** | `internal/runners/podman.go` — `s.NetNS = {NSMode: Host}` | **entire host network namespace**; localhost services reachable | implicit, unconditional — **NOT GATED** |
| 7 | Host runner / `run_on_host` | `court/tool_dispatch.go`, `court/tools/shell.go` | commands run **on the host**, unsandboxed | **GATED**: a `run_on_host`/`safe_run_on_host` pattern is only a *request*; execution requires the mandate's explicit `SeamHostExec` grant, checked in `Court.CheckHostCommand`. No grant (the no-dialog case) → typed refusal |
| 8 | `--isolated-host` | `court/court.go`, `headless.go` | bypasses the sandbox entirely | CLI flag — **NOT GATED, deliberately**: an operator's explicit command-line choice, outside the mandate's reach. No seam row corresponds to it and the dialog never implies it is covered |
| 9 | MCP servers (`[[mcp.servers]]`) | `court/mcp/bridge.go`, `internal/config/mandate.go` | `stdio` subprocesses spawned **on the host**, unsandboxed, with host env access | **GATED**: `[[mcp.servers]]` is only a *request*; spawning requires an explicit `SeamMCPServer` grant (`Selections.MCPServers[id]`). Off by default, excluded from "Allow all", stripped by `BuildMandate` when ungranted, fail-closed (zero servers) under `RestrictiveMandate` |

## What edict 1036 scoped

- **Seams 2 and 3**: now presented as *requests* in the launch-time trust
dialog and stripped into the session **mandate** by the Ruler's answers.
Additional mounts default to **read-only** (`read_only = false` in the config
only *requests* a writable bind; the Ruler must still open it in the dialog).
- **Seam 1** (working-tree bind): structural — the session cannot execute at all
  without the project root. Not grantable; recorded here.
- **Seams 4 and 5**: platform overlay/cache volumes derived from the repository
  config. Narrowed to the mandate's granted config (they are read from the
  mandate), but not individually choosable in the dialog yet.

## Recorded gaps (follow-up lings)

- **Seam 6 — host network namespace** (`NetNS: Host`): unconditional, no consent
  at all, and it makes every localhost service on the host reachable from inside
  the sandbox. **Deferred by the Ruler's ruling:** defaulting the host network
  OFF may break sandboxes that reach host services, so this seam needs its own
  ruling rather than a default flip. It stays `NOT GATED` pending that ruling;
  the follow-up ling is deliberately left open. Do not narrow it as a side
  effect of another change.
- **Seam 7 — host runner / `run_on_host`**: **gated.** The inventory's reading
  was confirmed against the code: the only thing separating `run_on_host` from
  `safe_run_on_host` was that one asked and the other did not — both reached the
  host, and a repository file decided which. Since host execution is strictly
  more authority than the sandbox, both now need an affirmative Ruler grant.
  The mechanism:
  - **vocabulary** — `config.SeamHostExec` / `config.SeamHostRunner` is the
    grant, and `Selections.HostExec` is the only field that sets it. The
    requested config is deliberately not consulted: `run_on_host` is a request,
    and a request is not a grant.
  - **gate** — `Court.CheckHostCommand` matches the pattern (which commands
    *belong* on the host) and then consults `Mandate.GrantFor(SeamHostExec,
    SeamHostRunner)` (whether they *may* run there). No grant →
    `court.HostExecRefusedError`, a typed refusal naming the reason.
  - **fail closed** — with no dialog the mandate is `RestrictiveMandate`, which
    records `Granted: false`, so headless `-p` and daemon-with-no-TUI sessions
    refuse host execution instead of running it silently.
  - **no blanket grant** — "Allow all" grants requested assets inside the
    sandbox and never ticks the host row; only that row by name grants it. The
    dialog now presents it as `host run_on_host (commands run OFF the sandbox,
    on this machine)`.
  - **no fallback escape** — a refusal is returned before any runner runs, so
    it cannot be caught by the sandbox-missing fallback and re-run on the host.
    The same check now covers that fallback: an unavailable sandbox is not
    consent to execute unsandboxed, and the old approval bypass inferred from a
    nil approval channel (`bypassApproval := msgChan == nil`) is gone — it
    required approval instead, failing closed when nobody can be asked.

  `run_on_host` keeps its per-command approval on top of the grant; that
  difference between the two pattern lists is the one thing that was ever
  legitimately theirs.
- **Seam 8 — `--isolated-host`**: bypasses the sandbox entirely. **Ruled: not
  gated, deliberately.** This is an explicit operator flag, not a
  repository-requested seam: it is not in any committed file, so there is
  nothing for the Ruler to grant or deny, and it is outside the mandate's
  reach by construction. The trust dialog does not present it and must never
  imply it is covered. Two things make that a guarantee rather than a
  comment:
  - **no seam row can correspond to it** — `config.SessionSeams` derives rows
    from `AdditionalMounts` and `PassthroughEnv` only, and no `SeamKind`
    represents a flag, so no `Selections` key can grant or deny it;
  - **the rendered text names it** — the footer states that running with
    `--isolated-host` is the operator's command-line choice and that no dialog
    answer turns it on or off, and the dialog never asserts a sandbox is in
    force. `trust_dialog.go` exposes `TrustDialog.Covers`, and
    `TestCourt_IsolatedHostIsNotADialogSeam` (`court/court_test.go`) plus
    `TestTrustDialog_DoesNotClaimToCoverIsolatedHost` (`tui_test.go`) assert
    both halves.

  Changing the flag's behaviour is explicitly out of scope: it stays a
  deliberate operator act.
- **Seam 9 — MCP servers (`[[mcp.servers]]`)**: **gated (edict 661).** Bifrost's
  `stdio` transport spawns each configured command directly on the host outside
  the podman sandbox and can forward host environment variables (`env`). Under
  edict 1036's invariant (*"A committed file may request a seam; it may never
  grant one"*):
  - **vocabulary** — `config.SeamMCPServer` (`"mcp_server"`) enumerates each
    configured `[[mcp.servers]]` entry in `config.SessionSeams(cfg)` showing
    its `id`, `command`, and `env`.
  - **no blanket grant** — like `SeamHostExec`, MCP servers start **off by
    default** and are **excluded from "Allow all"**. Only an explicit
    `Selections.MCPServers[id] = true` grant admits a server.
  - **stripping & fail closed** — `BuildMandate` strips every ungranted server
    from `Mandate.Config.MCP.Servers` and records auditable `Grant` entries.
    `RestrictiveMandate` (no-dialog / headless) grants zero servers and spawns
    no MCP subprocesses.
  - **user-vs-project pre-classification isolation** — repo-level
    `default_permissions` and `[[mcp.servers.tools]]` overrides in
    `.agents/asimi.conf` are stripped at config load. Only user config
    (`~/.config/asimi/asimi.conf`) may pre-classify MCP tools without going
    through the Minister of Rites + `propose` + Zhengming.
  - **stdio-only transport (`GO-2026-6911`)** — HTTP, SSE, Streamable-HTTP, and
    URL-based MCP server configs are rejected fail-closed at load and bridge
    initialization.

## The mandate

The Ruler's dialog answers strip the repository-requested config to the granted
set. That stripped config *is* the mandate (`config.Mandate`):

- owned by the **client process's PID** — the process that established the
  session, i.e. the same PID stamped on the sandbox container. In-process
  (TUI loopback / headless) that is `os.Getpid()`; in daemon mode the Court
  lives *inside the daemon*, so the owner is the socket peer's PID
  (`unixPeerPID`, `SO_PEERCRED` / `LOCAL_PEERPID`) recorded by
  `Court.SetClientPID`, never the daemon's own PID. A mandate owned by the
  daemon would outlive the client that granted it;
- holding a **reference** to the granted config, not a copy;
- carrying the explicit **grant record** (`Grants`), which any future
  re-widening check must consult rather than the effective config;
- **void once the owner PID is gone** — no new container may be created under
  it (see `MandateVoidError` and the sandbox janitor).

A mandate is **read-only by default**. The only widening path is an explicit
Ruler grant. Reach *into* the sandbox (mounts, env — seams 2-3) is separate
from unsandboxed host execution (host runner — seam 7, `Selections.HostExec`;
and MCP `stdio` servers — seam 9, `Selections.MCPServers`). "Allow all" covers
in-sandbox assets and never grants host execution or MCP servers.

## The no-dialog default

When no trust dialog is presented (headless `-p`, a daemon with no TUI,
handsoff mode), the mandate is the **most restrictive** reading: no additional
host mounts, no passthrough env vars, **no host execution**, and **zero MCP
servers**. The committed config still records the *request*, but a request is
not a grant — so a repository that asks for `run_on_host` gets a typed refusal
(`court.HostExecRefusedError`), and requested `[[mcp.servers]]` are stripped
and never spawned.

## e838 reconciliation

There is **one** mandate concept. `internal/mandate.Client` (the e838 shape —
the Court's contract with a credential-holding server) is retained as a grant
**strategy** (brokering), not a rival definition: where a credential can be
brokered, the sandbox never receives the raw secret and the mandate need not
carry a mount for it at all. No `internal/mandate` package exists in this tree;
nothing is duplicated. If e838's client is later revived, it must fulfil a
`Grant` here rather than mint a second `Mandate` type.
