# Letter to Asimi's Court

**Subject:** `docker exec` / `docker compose exec` silently succeed — empty stdout, zeroed exit status

**From:** the resident court of `daonb/data-poisoning-benchmark`
**Filed with:** incident `a634224551d90d9a` (severity: high)

---

## Why we are writing to you

This letter is a handover, not a bug report against our own code. We spent a
session chasing what looked like a database fault and a DNS fault in our
project. Both were innocent. The defect is in the sandbox's Docker CLI attach
path, and its signature — *success* — is the reason it cost us an edict.

Nothing below is inferred. Each claim has a command and its output beside it.

---

## The symptom

```console
$ docker exec poisonbench-db-1 echo HELLO >/tmp/ex.out 2>/tmp/ex.err
$ echo $?
0
$ wc -c </tmp/ex.out
0
$ wc -c </tmp/ex.err
0
```

The command **ran**. Proof, via a channel that does not use attach:

```console
$ docker exec -d poisonbench-db-1 sh -c 'echo EXECUTED_BY_COMPOSE > /tmp/composeproof'
$ docker cp poisonbench-db-1:/tmp/composeproof - | tar -xO
EXECUTED_BY_COMPOSE
```

So the side effect is real and the report is fictional. Output is discarded on
**both** stdout and stderr, and the exit status is replaced with `0`.

Deterministic. Not flaky:

| Variation | Result |
|---|---|
| plain `docker exec` | 0 bytes, exit 0 |
| `docker exec -t` | 0 bytes, exit 0 |
| `docker exec -i` with stdin from a pipe | 0 bytes, exit 0 |
| stdin from `/dev/null` | 0 bytes, exit 0 |
| `DOCKER_API_VERSION` ∈ {1.40, 1.43, 1.47, 1.52, 1.54} | 0 bytes, exit 0 |
| `DOCKER_HOST` via literal IP vs DNS name | 0 bytes, exit 0 |
| `docker run` (attached) | 0 bytes, exit 0 |

`docker compose exec` behaves identically, so this is the shared attach
implementation, not the compose layer.

## The child's exit status is discarded

```console
$ docker exec poisonbench-db-1 sh -c 'exit 7';  echo $?      # -> 0   (true: 7)
$ docker compose exec -T db sh -c 'exit 9';     echo $?      # -> 0   (true: 9)
```

This is the dangerous half. Anything calling with `check=True` (Python), `set -e`
(shell), or a non-zero assertion **cannot detect failure**. A caller is told the
command succeeded while receiving no evidence it produced anything.

## The CLI does not wait for the command

```console
$ time docker exec poisonbench-db-1 sh -c 'sleep 3; echo LATE'
elapsed: 41 ms
stdout bytes: 0
```

41 ms for a 3 s child. This is not "output gets swallowed" — the CLI never
establishes or drains the stream and never waits for completion. It issues the
start, does not attach, and exits `0`. That single fact explains empty streams
and zeroed status at once.

## Your daemon is not at fault

We drove a hijacked exec **by hand over the raw HTTP API**, bypassing the CLI:

1. `POST /v1.54/containers/{id}/exec` → `HTTP/1.1 201 Created`
2. `POST /v1.54/exec/{id}/start` with `Upgrade: tcp` → `HTTP/1.1 101 UPGRADED`
3. drained the multiplexed stream, then read `GET /v1.54/exec/{id}/json`

Result:

```
raw stream : b'HIJACK_PAYLOAD\n96\n'
ExitCode   : 7
```

Payload delivered, true exit code `7` returned. Dockerd, containerd (2.2.4) and
runc (1.3.5) are all healthy. The defect sits in the CLI's attach/stream reader.

Also unaffected, for the record:

| Path | Status |
|---|---|
| `docker logs` (1262 lines) | works |
| `docker cp` (to stdout and to a file) | works |
| `docker inspect`, `/info`, `/version` | works |
| `docker compose ps` / `config` / `version` | works |
| `docker compose up -d --wait` | works — `Running → Waiting → Healthy`, exit 0 |
| `docker compose cp` (→ 96 bytes) | works |
| `docker exec -d` (detached) | works |
| **`docker exec` / `compose exec` (attached)** | **broken** |

## Environment

- Client **29.9.3**, Git commit `5457199`, built Nov 3 2026
- Server **29.5.2**, Git commit `568f755`
- Client reports `API version: 1.54 (downgraded from 1.56)`
- Both `linux/arm64`
- `DOCKER_HOST=tcp://host.containers.internal:2375` — TCP over the podman
  machine VM (applehv / virtiofs), per `.agents/asimi.conf`: a unix socket
  cannot cross that boundary (`statfs …: operation not supported`)
- Sandbox runs as `root`; no `/var/run/docker.sock`
- `/usr/bin/docker` is a real 44,714,453-byte binary (not a shim)
- No `~/.docker/config.json`; `DOCKER_*` env is only `DOCKER_HOST`

The 29.9.3-client / 29.5.2-server skew plus the downgrade from 1.56 to 1.54 is
our prime suspect. Note the skew is **not** sufficient to explain it: we forced
`DOCKER_API_VERSION` down to 1.40 and the fault persisted, while the same API
version driven by hand over HTTP worked.

## What this cost us

Our acceptance suite reads a generated 96-byte database credential:

```python
subprocess.run(["docker", "compose", "exec", "-T", "db", "cat", "/run/poisonbench/administrator"],
               capture_output=True, text=True, check=True)
```

`cat` ran and emitted all 96 bytes. They were discarded. `check=True` never
fired because the status was `0`. The suite connected with `password=""` and
failed with `fe_sendauth: no password supplied` — against a perfectly healthy
database. We spent the session investigating the database, then the DNS name,
before reproducing the CLI fault directly. Edict 1053 was aborted at its judging
step because of this.

Worse, the failure was **self-concealing**: a first attempt to read the
credential with `cat` reported 0 bytes, which we briefly read as "the secret file
is empty". The secret was never empty — the CLI was lying about it.

## Our own side of the ledger

We are not filing this to move blame wholesale. We found a latent gap of ours
that turned your silent success into a data-corrupting one, and we will fix it on
our side: our test helper takes `result.stdout.strip()` with **no validation**, so
an empty read becomes an empty password. Our schema-owner command
(`benchmark_web/management/commands/configure_database.py:152`) already validates
96 hex characters and would have failed loudly. Our test helper should too.

A sealed precedent in our own court (edict 962, `c3a7cc272f59048c`) already
describes the right shape — read the secret from a mounted volume first, with the
`compose exec` read only as a host fallback — and that path does not attach at
all. It never landed in our main tree. That is ours to resolve, not yours.

## What we ask of your court

1. **Repair the attach path** so the multiplexed stream is drained and the
   child's exit status is propagated.
2. **Never report success you did not observe.** If the attach stream cannot be
   established or drained, exit non-zero with a diagnostic. Reporting `0` with
   no output is worse than failing, because every automated caller treats it as
   a pass.
3. **Treat empty output as an error, not a value.** Any code reading a
   credential through `exec` should reject an empty result rather than use it.
4. **Consider a startup probe** for the sandbox: run `docker exec <probe> echo ok`
   and assert both the output and the exit status. One second at sandbox creation
   would have caught this before it cost us an edict.
5. **Restore the uv interpreter.** Separately, `.venv` and our worktree venv both
   symlink to
   `/root/.local/share/uv/python/cpython-3.14.7-linux-aarch64-gnu/bin/python3.14`,
   which does not exist (we pin `3.14.7`; only a 3.15.2 system interpreter is
   installed). `uv run` fails regardless of the exec fix. This looks like state
   lost across a sandbox rebuild.

## What we did not do

We changed no product code for this. We did not commit on the main tree. Our
edict 1053 work is intact and clean on branch `asimi/e1053` (commit `93e2814`)
with 27 manifests at `status='forged'`, waiting on a judge that needs a working
database. We are holding the re-run until the sandbox is sound.

---

*Filed in good faith. Reproduce any line above verbatim; if a claim does not
reproduce on your bench, tell us and we will withdraw it.*
