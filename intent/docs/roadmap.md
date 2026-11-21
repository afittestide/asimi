# Roadmap

### v0.18.0

### Full Parallelism (rolled forward from 0.17.0 — unbuilt, not abandoned)
- Ling ignition on by default — the engine already runs each ling in the edict's
  worktree (e923); flipping `court.ling_ignition_enabled` on is the remaining ruling
- Drafting ritual — mints its own edict at step 0, long-lived per-minister sessions,
  serialized lings (e1009; the Ruler's mechanism and its four open forks are recorded
  in `intent/letters/e940.md`, Letter 4)

### Carry-over test debt (standing since the 0.16.0 stock-take)
- The tilde-expansion tests from e992 and the 13 `EnsureEdictWorktree` / `:switch`
  test funcs from e952 exist only as uncommitted worktree diffs, not in `main`

### v0.19.0

### the nvim base
The plugin era begins. `asimi.nvim` talks to the daemon over msgpack-RPC. It is
distributed two ways: `lazy-asimi` (a drop-in lazy.nvim config that auto-installs
`asimi.nvim` and pre-configures it) and as a reusable plugin for a user's own nvim.
The Bubbletea TUI still ships.

- `asimi.nvim` core — msgpack-RPC client against the daemon
- `lazy-asimi` start config (install + preconfigure `asimi.nvim`, `~/.config/nvim`)

### v0.20.0

### nvim parity with the TUI court surface
- Ritual tabs w/ step folding, sticky active step, 5-row thinking scroll,
  replace-file view, native modes, CTRL-C resume on a ritual tab (e681)
- Non-blocking rendering (debounce/tick) on nvim buffers

### v0.21.0

### Letters as the court's message — `send_letter` replaces `consult_minister`
A conversation that outgrows its minister no longer needs the Ruler as courier,
and a minister needing another's counsel no longer blocks on a synchronous
consult. Ministers write letters with the existing YAML front matter
(`from`, `to`, `edict`, `subject`, `severity`, `requested_remedies`) and send
them with `send_letter`: the letter is persisted to `intent/letters/` and
routed by its address — another court leaves it in the outbox as today; a
minister of this court opens a new letter-keyed tab with the letter as its
opening prompt, quietly, the sender's session continuing untouched. The
system stamps each letter with its originating session as metadata, so no
reply field is needed: a responding letter is injected into that conversation
while it is still active, and opens a new tab only when it is not.
`consult_minister` retires in the same motion, with its recursion guards and
ExcludeTools plumbing — nothing depends on it.

### Court Alignment
- Realign ministers to full 三省 (https://github.com/afittestide/asimi/issues/154)
- Sharpen roles → 文言文 (issues/156), tools (issues/155), DB schema (issues/152)

## V1.0 — the polished court

The Bubbletea TUI is retired; its legacy features (vim clone, sticky messages, step
folding, thinking scroll, replace view, mode sharpening) live on as nvim-plugin
features, delivered by the 0.19/0.20 parity line. Nothing new is added — 1.0 is the
release that certifies the 0.x polish line complete: nvim is the only frontend, the
court is aligned, and the old UI is gone.
Full documentation is available

## V1.1

### MCP Support (e661 — leading)

e661 adds the Minister of Rites (禮部) to ingest MCP servers as foreign envoys:
it classifies each offered tool into the 三界 permission model
(地/天/人, read/write/execute) for the Ruler to seal; only sealed tools enter
the registry, gated by minister realm permissions.

- Bifrost-native MCP client (connection, discovery, sync)
- Per-server `default_permissions` + per-tool overrides in config
- `mcp_tool_classifications` table; cached, re-classified only on schema change
- After e661, a headless terminal emulator (for nvim + terminal-bench drives)
  is added via config alone.

Ref: https://github.com/afittestide/asimi/issues/158

### integrate diffView plugin
- Seal edict command to have review/comment/approval UI

## V1.2

- Realm-aligned minister permissions: the repo is earth + intent; tests/docs are
  intent content (https://github.com/afittestide/asimi/issues/160)
- Update swift-strike to BDD

## V2.0

A `mandate_url` config points to a Mandate server bridging the external world
and the Court. It serves an OpenAPI spec of the three realms; Earth is git; the
rest is a ritual-updated cache fed by webhooks/cron (CI, issues, TODOs, brand,
analytics). The Court is a process engine (seal chain, ritual DAGs, permissions)
reading the spec at startup and pulling realm data as ritual context. A second
mandate server — for digital marketers — proves the abstraction.
