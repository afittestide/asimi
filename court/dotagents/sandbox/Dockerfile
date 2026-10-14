FROM <a debian 13 "trixie" base, slim if available> 

# Install common development tools and libraries
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ripgrep \
    just && rm -rf /var/lib/apt/lists/*

COPY Justfile .
# Runs as root inside the container — bootstrap must NOT use sudo
RUN just bootstrap
# Asimi runtime — appended to /etc/bash.bashrc so the git shim and environment
# defaults are sourced by every interactive bash shell BEFORE /root/.bashrc,
# regardless of user customizations.
COPY .agents/sandbox/asimi_runtime.sh /tmp/asimi_runtime.sh
RUN cat /tmp/asimi_runtime.sh >> /etc/bash.bashrc && rm /tmp/asimi_runtime.sh

COPY .agents/sandbox/bashrc /root/.bashrc

# Default command: start shell (bashrc handles startup tasks)
CMD ["/bin/bash"]
