FROM <a debian 13 "trixie" base, slim if available> 

# Bound Go build parallelism and runtime memory so concurrent edict rituals
# cannot OOM-kill the compiler in the ~2 GiB sandbox VM. Without these, cold
# parallel compilation of modernc.org/sqlite/libc produces `signal: killed`
# => `[build failed]`, which the swift-strike judge gate reads as a test failure.
ENV GOFLAGS=-p=1 \
    GOMAXPROCS=2 \
    GOMEMLIMIT=1500MiB

# Install common development tools and libraries
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ripgrep \
    just && rm -rf /var/lib/apt/lists/*

COPY Justfile .
# Runs as root inside the container — bootstrap must NOT use sudo.
# Cache mounts keep the Go module and build caches OUT of the committed image
# layer: without them bootstrap bakes ~180 MB into the layer and a build on a
# nearly-full podman VM fails committing it with
#   unpacking failed ... /root/.cache/go-build/...: no space left on device
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    just bootstrap
# Asimi runtime — appended to /etc/bash.bashrc so the git shim and environment
# defaults are sourced by every interactive bash shell BEFORE /root/.bashrc,
# regardless of user customizations.
COPY .agents/sandbox/asimi_runtime.sh /tmp/asimi_runtime.sh
RUN cat /tmp/asimi_runtime.sh >> /etc/bash.bashrc && rm /tmp/asimi_runtime.sh

COPY .agents/sandbox/bashrc /root/.bashrc

# Default command: start shell (bashrc handles startup tasks)
CMD ["/bin/bash"]
