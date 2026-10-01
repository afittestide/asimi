# Security

## Vulnerability scanning

The release gate runs `just vuln` (see `docs/release-management.md`), which
builds the binary and runs `govulncheck -mode=binary` against it. The gate fails
the release if the scan reports any *new* reachable vulnerability.

### Accepted findings

A finding may be accepted only when it is present in `security/vuln-allowlist.txt`
with a written justification. The `just vuln` recipe subtracts the allowlist
from the scan output, so an accepted finding no longer fails the gate — but any
finding **not** on the allowlist, or any allowlist entry that stops matching,
is reported. This keeps the gate honest: it fails on the unknown, not on the
known-and-triaged.

An allowlist entry is a govulncheck vulnerability ID (e.g. `GO-2026-5932`) on
its own line; `#` starts a comment. **Do not** add an entry without a
justification in this document.

### GO-2026-5932 — `golang.org/x/crypto/openpgp` (accepted, unreachable)

- **What it is**: the `openpgp` package is unmaintained, unsafe by design, and
  has known security issues. govulncheck reports it as `Fixed in: N/A` — there
  is no upstream fix.
- **Why it cannot be fixed by a version bump**: the package is frozen upstream;
  the only remedies are removing the dependency or migrating to another
  implementation.
- **Why it is accepted**: the package is *linked* into the binary but
  *unreachable* from Asimi's own code.
  - Asimi never imports `openpgp` (nor `ocicrypt`) directly. It arrives
    transitively via the Podman client:
    `internal/runners` → `go.podman.io/podman/v6/pkg/specgen` →
    `go.podman.io/common/libimage` → `go.podman.io/image/v5/copy` →
    `github.com/containers/ocicrypt`.
  - `github.com/containers/ocicrypt` unconditionally imports
    `golang.org/x/crypto/openpgp` (in `gpgvault.go` and
    `keywrap/pgp/keywrapper_gpg.go`), which is why the symbols appear in the
    binary even though nothing calls them.
  - The `openpgp` symbols are reachable only through ocicrypt's
    image-encryption flow (`GetKeyWrapper` with a `gpg` scheme, i.e.
    `ocicrypt.Encrypt`/`Decrypt` for encrypted container images). Asimi uses
    Podman's *image copy / libimage* path, never ocicrypt-encrypted images, so
    that flow is never invoked.
  - Corroboration: `govulncheck -mode=binary -show traces` lists the `openpgp`
    symbols but produces **zero call-path traces** — no path from `main` into
    the vulnerable code exists in the binary.
- **Residual risk & follow-up**: a future code path that pulls encrypted
  container images through ocicrypt could make this reachable. If Asimi ever
  gains that capability, this acceptance must be revisited. Eliminating the
  dependency entirely (dropping or replacing the libimage path) is tracked
  separately.
