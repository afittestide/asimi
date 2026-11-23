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
- **Where it comes from**: all three of today's findings arrive indirectly via
  `github.com/maximhq/bifrost/core/schemas` (`go mod why -m` confirms the chain).
  This one is *reachable* in the binary: govulncheck lists the
  `transport.StreamableHTTP` symbols
  (`NewStreamableHTTP`, `Close`, `SendNotification`, `SendRequest`,
  `SetRequestHandler`), which bifrost's `core/mcp/clientmanager.go` calls.
- **Why it is accepted**: the vulnerable code is **not exercised by Asimi's
  configuration**. Those symbols are reached only when bifrost builds its MCP
  client, which happens when a `MCPConfig` is supplied. Asimi constructs
  `schemas.BifrostConfig` with only `Account` and `Logger`
  (`court/llm_init.go`), leaving `MCPConfig` nil, so bifrost never creates an
  `MCPManager` and never opens a Streamable-HTTP transport. There is also no
  `[mcp]` key in `internal/config/default.conf`, and Asimi's own code — outside
  the vendored tree — never mentions MCP at all. The honest basis for the
  acceptance is therefore "linked and reachable in the call graph, but never
  invoked by Asimi's configuration", **not** "unreachable".
- **Residual risk & follow-up**: this acceptance must be **re-reviewed when
  bifrost next bumps mcp-go**. If the bump pulls in a fixed mcp-go, the entry is
  removed; if Asimi ever gains MCP support (a configured `MCPConfig`), the
  vulnerable transport becomes genuinely reachable and this acceptance no longer
  holds. Bumping bifrost itself is tracked separately.

### GO-2026-5932 — retired

`GO-2026-5932` (`golang.org/x/crypto/openpgp`, accepted as unreachable) stopped
being reported by the scan at this dependency version and was removed from
`security/vuln-allowlist.txt`; a stale entry means the acceptance should be
re-reviewed, so it was retired rather than silently left in place.
