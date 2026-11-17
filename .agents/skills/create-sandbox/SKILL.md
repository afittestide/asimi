---
name: create-sandbox
description: "Connect the asimi sandbox to the host's container daemon (colima, Docker Desktop, or native Docker) on macOS. Use when the sandbox has no docker daemon, when `docker info` fails inside it, or when tests need the host's Compose database."
---

# create-sandbox

How to give the sandbox a reachable container daemon and host database, on
macOS, when the sandbox runs inside a podman machine VM.

**The short version:** mount the socket on Linux; on macOS you cannot, so use
TCP over `host.containers.internal`.

## Why the socket mount worked on Debian and cannot work on macOS

On Debian the sandbox and the docker daemon share one kernel, so bind-mounting
`/var/run/docker.sock` into the container works: the socket inode refers to a
live endpoint in the same kernel.

On macOS the sandbox runs inside a podman machine VM (applehv + virtiofs). A
unix socket is only a rendezvous *name*; the actual endpoint lives in the
kernel that owns it. Virtiofs carries file contents across the VM boundary but
not kernel socket state, so it cannot even describe the inode. Measured on
`/var/run/colima` (the parent directory mounts fine, the socket inside it does
not):

```
stat(colima.yaml)     -> OK size=8471
stat(docker.sock)     -> FAIL errno=95 (ENOTSUP) Operation not supported
connect(docker.sock)  -> FAIL errno=95 (ENOTSUP)
```

Mounting the **parent directory** does not help either — the per-source
`statfs` podman performs still lands on the socket and fails, and podman then
refuses to create the container:

```
Error: statfs .../docker.sock: operation not supported
```

A failed container creation leaves the court with no runner, which looks like a
crash rather than a mount problem. Keep the `additional_mounts` socket stanza
commented in `.agents/asimi.conf`.

## macOS: reach the daemon over TCP

Published ports are forwarded across the VM boundary, so a TCP endpoint works
where a socket cannot.

### 1. Enable dockerd's TCP endpoint inside the colima VM

Colima hardcodes its own systemd unit, so a drop-in is needed. Use a filename
that sorts **after** colima's `docker.conf` — colima rewrites that file on every
start and will clobber anything you put there:

```bash
colima ssh -- sudo tee /etc/systemd/system/docker.service.d/zz-tcp.conf >/dev/null <<'EOF'
[Service]
ExecStart=
ExecStart=/usr/bin/dockerd -H fd:// -H tcp://127.0.0.1:2375 --containerd=/run/containerd/containerd.sock
EOF
colima ssh -- sudo systemctl daemon-reload
colima ssh -- sudo systemctl restart docker
colima ssh -- ss -lntp | grep 2375     # expect 127.0.0.1:2375
```

Two details that are easy to get wrong:

- **The empty `ExecStart=` line is required.** Without it systemd appends to the
  inherited value and refuses the unit: `Service has more than one ExecStart=
  setting, which is only allowed for Type=oneshot services`.
- **Do not use `colima start --tcp`.** That flag does not exist. The documented
  route is the drop-in above.

Do **not** set `hosts:` in `~/.colima/default/colima.yaml` either. Colima
marshals that map into `/etc/docker/daemon.json`, and dockerd then receives
`-H` from both the config file and the command line, which it rejects:

```
unable to configure the Docker daemon with file /etc/docker/daemon.json:
the following directives are specified both as a flag and in the configuration file: hosts
```

Keep `docker: {}` in `colima.yaml` and let the drop-in own the flags.

### 2. Point the sandbox at it

In `.agents/sandbox/bashrc` (never in the host's `~/.envrc`, which would
retarget the Mac's own docker client):

```bash
export DOCKER_HOST="tcp://host.containers.internal:2375"
export BENCHMARK_DATABASE_HOST="host.containers.internal"
```

`host.containers.internal` is injected by podman into `/etc/hosts` and points at
the Mac. It is portable across machines; a literal `192.168.x.x` gateway is not.

The bashrc reaches asimi's commands because the podman runner sets
`BASH_ENV=/root/.bashrc` (`internal/runners/podman.go`), which makes
non-interactive `bash -c` source it. Verify with:

```bash
podman run --rm -e BASH_ENV=/root/.bashrc <image> bash -c 'echo $DOCKER_HOST'
```

### 3. Confirm

```bash
docker version --format 'server={{.Server.Version}}'   # inside the sandbox
```

## What works over TCP, and what does not

Measured from inside the sandbox against `tcp://host.containers.internal:2375`:

| operation | works |
|---|---|
| `docker version`, `info`, `ps`, `logs`, `logs -f` | yes |
| `docker inspect`, `stop`, `start` | yes |
| `docker compose ps` | yes |
| `docker cp` (also an HTTP hijack) | **yes** |
| `docker exec`, `docker run` | **no** — exits 0 with empty output |

The attach path silently yields nothing while still returning success, which is
a nasty failure mode: a caller using `check=True` sees a pass and gets an empty
string. The transport itself is fine — a raw API client against the same
endpoint receives the multiplexed exec frames correctly — so the fault is in the
pinned CLI's attach handling, not in gvproxy or colima.

## Reading the Compose credential without `exec`

`tests/postgres.py` needs the administrator password from the Compose volume.
`compose exec` cannot deliver it from the sandbox, but `compose ps` + `cp` can,
both being non-attach calls:

```bash
cid=$(docker compose ps -q db)
docker cp "$cid:/run/poisonbench/administrator" - | tar -xO
```

Verified to return the same credential as the host's `compose exec`.

## Docker Desktop

Same shape, different plumbing: Desktop publishes its daemon on
`~/.docker/run/docker.sock` on the Mac (a symlink; `/var/run/docker.sock`
resolves to it), and the socket mount still fails for the same virtiofs reason.
Prefer Desktop's own TCP endpoint if you enable one, and still address it as
`host.containers.internal:<port>` from the sandbox.

## Security

- Bind the VM's `tcp://127.0.0.1:2375`, not `0.0.0.0`. Port 2375 is
  **unauthenticated** — it is docker root on the Mac. Binding `0.0.0.0` also
  broke the Mac-side hijack in testing, in addition to exposing the daemon.
- Verify the Mac is not listening on its LAN address:
  `lsof -nP -iTCP:2375 -sTCP:LISTEN` should show `127.0.0.1` only.
- The drop-in lives inside the VM, so `colima delete` or a VM rebuild loses it.
  Re-run step 1 after such an operation.
- A root process in the sandbox with daemon access holds host-daemon-equivalent
  power; that is the cost of docker-outside-of-Docker and should be a deliberate
  choice, not an accident.
