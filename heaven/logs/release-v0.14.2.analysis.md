# Judge analysis — CI run 38122686702 (release v0.14.2)

**Verdict:** FAILED (rejected)
**Logs:** `heaven/logs/release-v0.14.2.log`, `heaven/logs/release-v0.14.2.failed.log`

## Provenance (tree under trial)
- Commit: `957bc4f91d6777884e239c9423184aad7906b668` — "chore: releasing 0.14.2"
- Tag: `v0.14.2` (points at HEAD); branch `main`
- Workflow: CI, event `push`, ref `refs/tags/v0.14.2`
- Runner: GitHub Actions ubuntu-24.04 (runner 2.337.0, image 20260927.320.1)
- Go: `1.26.6` (pinned in `.github/workflows/ci.yml`), build tags `containers_image_openpgp`

## What failed
- Job **Test** (`114420501083`, 6m9s) → step **Run tests** (`just test`) → exit 1
- Job **Release** skipped (`needs: test`)
- **All unit tests passed in CI** (zero `--- FAIL`).
- Failure was the CI-only **vuln gate**:

```
error: Recipe `vuln` failed with exit code 1
error: Recipe `test` failed with exit code 1
VULN GATE FAILED: unaccepted vulnerabilities: GO-2026-6599 GO-2026-6600
  GO-2026-6603 GO-2026-6605 GO-2026-6607 GO-2026-6608 GO-2026-6610
  GO-2026-6611 GO-2026-6612 GO-2026-6613 GO-2026-6617
```

## Root cause
`Justfile:64-66` runs `just vuln` after tests when `CI` is set. `just vuln`
builds the binary and runs `govulncheck -mode=binary`, then subtracts
`security/vuln-allowlist.txt` (only `GO-2026-5932` accepted).

11 findings are not allowlisted — all have upstream fixes:

| Finding | Module | Found | Fixed |
|---|---|---|---|
| GO-2026-6617 | golang.org/x/net | v0.58.0 | v0.60.0 |
| GO-2026-6612 | golang.org/x/net | v0.58.0 | v0.60.0 |
| GO-2026-6611 | golang.org/x/net | v0.58.0 | v0.60.0 |
| GO-2026-6610 | golang.org/x/net | v0.58.0 | v0.60.0 |
| GO-2026-6603 | golang.org/x/net | v0.58.0 | v0.60.0 |
| GO-2026-6613 | stdlib net/http | go1.26.6 | go1.26.9 |
| GO-2026-6608 | stdlib net/textproto, mime/multipart | go1.26.6 | go1.26.9 |
| GO-2026-6607 | stdlib crypto/tls | go1.26.6 | go1.26.9 |
| GO-2026-6605 | stdlib net/http | go1.26.6 | go1.26.9 |
| GO-2026-6600 | stdlib html/template | go1.26.6 | go1.26.9 |
| GO-2026-6599 | stdlib html/template | go1.26.6 | go1.26.9 |

This is toolchain/dependency drift since v0.14.1 (2026-10-06), **not** a defect
introduced by e915/e911/e912. It is deterministic, not flaky.

## Remediation (all fixes exist — do not allowlist)
1. Bump the Go pin in `.github/workflows/ci.yml` (and the module toolchain) to
   `>= 1.26.9` → clears the 6 stdlib findings.
2. `go get golang.org/x/net@v0.60.0` → clears the 5 x/net findings.

## Collateral anomaly (flagged, not silent-discounted)
The Ruler's earlier zhengming ("Fix the flake first", e911) referenced a flaky
`TestInstallDaemonAutostartUsesReconnectingClient` (`autostart_test.go:356`,
"waitForConnRestore never observed the reconnect"). It reproduced locally
(`test.out`, 1 run) but **did not fail this CI run**. It remains a real flake to
fix before the seal, per the Ruler's ruling — but it is *not* the cause of this
v0.14.2 CI failure. The release failure is unambiguously the vuln gate.
