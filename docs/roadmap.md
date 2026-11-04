# Roadmap

### v0.16.0

### Full Parallelism
- Ling ignition on by default — the engine already runs each ling in the edict's worktree (e923) ; flipping `court.ling_ignition_enabled` on is the remaining ruling
- Per-ling output attribution — each ling's stream is attributable and its transcript durable (e994)

### Court and docs
- Brewing ritual — mints its own edict at step 0, long-lived per-minister sessions, serialized lings (e1009)
- 禮部 (Minister of Rites) owns all documentation; the docs tree moves to `intent/docs/` (e1017)

## V1.0

### neovim base TUI

The Go Bubbletea terminal UI is retired. Neovim is the first-class frontend via
the in-repo `nvim/` plugin (`asimi.nvim`), which talks to the daemon over
msgpack-RPC. It is distributed two ways: `lazy-asimi` (a drop-in lazy.nvim
config that auto-installs `asimi.nvim` and pre-configures it) and as a reusable
plugin for a user's own nvim.

Legacy TUI UI features (vim clone, sticky messages, step folding, thinking
scroll, replace view, mode sharpening) are re-cast as nvim-plugin features.

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

### Court Alignment

- Realign ministers to full 三省 (https://github.com/afittestide/asimi/issues/154)
- Sharpen roles → 文言文 (issues/156), tools (issues/155), DB schema (issues/152)

### The nvim co-path

- **`lazy-asimi`** start config (install + preconfigure `asimi.nvim`, `~/.config/nvim`)
- Plugin parity with the retired TUI's Court surface: ritual tabs w/ step folding,
  sticky active step, 5-row thinking scroll, replace-file view, native modes,
  CTRL-C resume on a ritual tab (e681)
- Non-blocking rendering (debounce/tick) on nvim buffers

## V1.1

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


