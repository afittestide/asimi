# Security

## Vulnerability scanning

The release gate runs `just get-vuln` (see `intent/docs/release-management.md`), which
builds the binary and runs `govulncheck -mode=binary` against it. The gate fails
the release if the scan reports any *new* reachable vulnerability.

Every run archives its full output under `heaven/vulns/` as
`<tool>-<datetime>.out` (e.g. `govulncheck-20261109T142655.out`). Reports are
append-only evidence — each run writes a new timestamped file and never
overwrites one — and `heaven/` is gitignored. Failed CI runs are a different
archive, fetched on demand by `just get-runs` into `heaven/runs/`.

### Accepted findings

A finding may be accepted only when it is present in `security/vuln-allowlist.txt`
with a written justification. The `just get-vuln` recipe subtracts the allowlist
from the scan output, so an accepted finding no longer fails the gate — but any
finding **not** on the allowlist, or any allowlist entry that stops matching,
is reported. This keeps the gate honest: it fails on the unknown, not on the
known-and-triaged.

An allowlist entry is a govulncheck vulnerability ID (e.g. `GO-2026-6911`) on
its own line; `#` starts a comment. **Do not** add an entry without a
justification in this document.

### GO-2026-6911 — `github.com/mark3labs/mcp-go` (accepted, not exercised)

- **What it is**: SSRF and credential leak via an unvalidated `Location`
  header in mcp-go's Streamable-HTTP transport. govulncheck reports it as
  `Fixed in: N/A` — mcp-go has **no fixed release** at this version, so no
  version bump can clear it.
- **Why it cannot be fixed by a version bump**: there is no upstream fix. The
  only remedies are dropping the dependency or waiting for one; the module
  arrives transitively through `github.com/maximhq/bifrost/core/schemas`, which
  Asimi does not control.
- **Where it comes from**: the module arrives transitively via Bifrost
  (`github.com/maximhq/bifrost/core/mcp` and `core/schemas`; `go mod why -m`
  confirms the chain). This finding is *reachable* in the binary: govulncheck
  lists the `transport.StreamableHTTP` symbols (`NewStreamableHTTP`, `Close`,
  `SendNotification`, `SendRequest`, `SetRequestHandler`), which bifrost's
  `core/mcp/clientmanager.go` calls on the HTTP/Streamable-HTTP connection path.
- **Why it is accepted**: the vulnerable `transport.StreamableHTTP` code path in
  `mcp-go` is **not exercised by Asimi**. Asimi leaves
  `schemas.BifrostConfig.MCPConfig` nil in `court/llm_init.go` so Bifrost never
  auto-initializes an internal MCP client or bypasses Asimi's seal gate. Instead,
  Asimi enables Bifrost's `MCPManager` directly via `court/mcp` **strictly for
  `stdio` transport** (`schemas.MCPConnectionTypeSTDIO`). Any HTTP, SSE,
  Streamable-HTTP, or URL-based MCP server configuration is explicitly rejected
  both at config load and at bridge initialization in `court/mcp`, ensuring
  `transport.StreamableHTTP` is never instantiated or invoked. The honest basis
  for the acceptance is therefore "linked and reachable in the call graph, but
  never invoked because Asimi restricts MCP strictly to `stdio`", **not**
  "unreachable".
- **Residual risk & follow-up**: this acceptance must be **re-reviewed when
  bifrost next bumps mcp-go** — if the bump pulls in a fixed `mcp-go`, remove
  the allowlist entry. Until upstream fixes `GO-2026-6911`, `stdio` remains the
  only permitted MCP transport: enabling HTTP, SSE, Streamable-HTTP, or
  URL-based MCP servers (or wiring `schemas.BifrostConfig.MCPConfig`) would make
  the vulnerable `transport.StreamableHTTP` code path genuinely reachable and
  invalidate this acceptance. Bumping bifrost itself is tracked separately.

### GO-2026-5932 — retired

`GO-2026-5932` (`golang.org/x/crypto/openpgp`, accepted as unreachable) stopped
being reported by the scan at this dependency version and was removed from
`security/vuln-allowlist.txt`; a stale entry means the acceptance should be
re-reviewed, so it was retired rather than silently left in place.
