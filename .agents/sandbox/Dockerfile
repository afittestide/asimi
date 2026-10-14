FROM golang:1.26.9-bookworm

# The golang base image sets GOTOOLCHAIN=local, which makes the toolchain
# refuse to fetch a newer Go when go.mod requires one (e.g. after a dependency
# bump). Override it so the sandbox can self-heal to the go.mod toolchain
# instead of failing with "go.mod requires go >= X (running go Y)".
ENV GOTOOLCHAIN=auto

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
RUN just bootstrap
COPY .agents/sandbox/bashrc /root/.bashrc
CMD ["just install && /bin/bash"]
