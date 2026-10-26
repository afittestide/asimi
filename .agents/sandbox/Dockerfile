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
    wget \
    libbtrfs-dev \
    libdevmapper-dev \
    pkg-config \
    sqlite3 \
    && rm -rf /var/lib/apt/lists/*

# install gh
RUN mkdir -p -m 755 /etc/apt/keyrings \
	&& out=$(mktemp) && wget -nv -O$out https://cli.github.com/packages/githubcli-archive-keyring.gpg \
	&& cat $out | tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null \
	&& chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
	&& mkdir -p -m 755 /etc/apt/sources.list.d \
	&& echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | tee /etc/apt/sources.list.d/github-cli.list > /dev/null \
	&& apt update \
	&& apt install gh -y


# Install just
RUN curl --proto '=https' --tlsv1.2 -sSf https://just.systems/install.sh | bash -s -- --to /usr/local/bin

COPY Justfile .
# Cache mounts keep the Go module and build caches shared among sandboxes
# The caches persist across builds (so downloads are reused) yet never ship in
# the image, shrinking the layer to just the installed binaries.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    just bootstrap
COPY .agents/sandbox/bashrc /root/.bashrc
CMD ["just install && /bin/bash"]
