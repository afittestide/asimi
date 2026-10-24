FROM golang:1.26.10-bookworm

# The golang base image sets GOTOOLCHAIN=local, which makes the toolchain
# refuse to fetch a newer Go when go.mod requires one (e.g. after a dependency
# bump). Override it so the sandbox can self-heal to the go.mod toolchain
# instead of failing with "go.mod requires go >= X (running go Y)".
ENV GOTOOLCHAIN=auto

# Bound Go build parallelism and runtime memory so concurrent edict rituals
# cannot OOM-kill the compiler in the ~2 GiB sandbox VM. Without these, cold
# parallel compilation of modernc.org/sqlite/libc produces `signal: killed`
# => `[build failed]`, which the swift-strike judge gate reads as a test failure.
ENV GOFLAGS=-p=1 \
    GOMAXPROCS=2 \
    GOMEMLIMIT=1500MiB

# Install common development tools and build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ripgrep \
    libgpgme-dev \
    libbtrfs-dev \
    libdevmapper-dev \
    pkg-config \
    sqlite3 \
    && rm -rf /var/lib/apt/lists/*

# Install neovim (latest from GitHub, architecture-aware)
RUN ARCH=$(uname -m | sed 's/aarch64/arm64/; s/x86_64/x86_64/') && \
    curl -sL "https://github.com/neovim/neovim/releases/download/v0.12.4/nvim-linux-${ARCH}.tar.gz" \
    | tar xz -C /usr/local --strip-components=1

# Install just
RUN curl --proto '=https' --tlsv1.2 -sSf https://just.systems/install.sh | bash -s -- --to /usr/local/bin

COPY Justfile .
# Cache mounts keep the Go module and build caches OUT of the committed image
# layer. Without them `just bootstrap` bakes ~180 MB into that layer, and on a
# podman VM whose disk is nearly full the layer commit fails with:
#   unpacking failed ... /root/.cache/go-build/...: no space left on device
# The caches persist across builds (so downloads are reused) yet never ship in
# the image, shrinking the layer to just the installed binaries.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    just bootstrap
COPY .agents/sandbox/bashrc /root/.bashrc
CMD ["just install && /bin/bash"]
