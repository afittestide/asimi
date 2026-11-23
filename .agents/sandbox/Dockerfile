FROM golang:1-trixie

# The golang base image sets GOTOOLCHAIN=local, which makes the toolchain
# refuse to fetch a newer Go when go.mod requires one (e.g. after a dependency
# bump). Override it so the sandbox can self-heal to the go.mod toolchain
# instead of failing with "go.mod requires go >= X (running go Y)".
ENV GOTOOLCHAIN=auto
ENV CLOUDSDK_CORE_DISABLE_PROMPTS=1

# add gcloud source
RUN curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg \
       | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" \
       > /etc/apt/sources.list.d/google-cloud-sdk.list
# add gh source
RUN mkdir -p -m 755 /etc/apt/keyrings \
	&& out=$(mktemp) && wget -nv -O$out https://cli.github.com/packages/githubcli-archive-keyring.gpg \
	&& cat $out | tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null \
	&& chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
	&& mkdir -p -m 755 /etc/apt/sources.list.d \
	&& echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | tee /etc/apt/sources.list.d/github-cli.list > /dev/null

# Install common development tools and build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    apt-transport-https \
    ca-certificates \
    curl \
    gh \
    git \
    gnupg \
    google-cloud-cli \
    just \
    libbtrfs-dev \
    libdevmapper-dev \
    libgpgme-dev \
    pkg-config \
    ripgrep \
    sqlite3 \
    wget \
    && rm -rf /var/lib/apt/lists/*

COPY Justfile .
# Cache mounts keep the Go module and build caches shared among sandboxes
# The caches persist across builds (so downloads are reused) yet never ship in
# the image, shrinking the layer to just the installed binaries.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    just bootstrap
COPY .agents/sandbox/bashrc /root/.bashrc
CMD ["just install && /bin/bash"]
