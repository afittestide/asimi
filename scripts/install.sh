#!/bin/bash
#
# Asimi CLI Installer
#
# Usage:
#   curl -fsSL https://asimi.dev/installer | bash
#   or
#   wget -qO- https://asimi.dev/installer | bash
#
# Options (via environment variables):
#   ASIMI_INSTALL_DIR  - Installation directory (default: /usr/local/bin or ~/.local/bin)
#   ASIMI_VERSION      - Specific version to install (default: latest)
#   ASIMI_NO_MODIFY_PATH - Set to 1 to skip PATH modification
#

set -euo pipefail

# Configuration
GITHUB_REPO="afittestide/asimi"
BINARY_NAME="asimi"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Print functions
info() {
    printf "${BLUE}==>${NC} %s\n" "$1"
}

success() {
    printf "${GREEN}==>${NC} %s\n" "$1"
}

warn() {
    printf "${YELLOW}Warning:${NC} %s\n" "$1"
}

error() {
    printf "${RED}Error:${NC} %s\n" "$1" >&2
}

die() {
    error "$1"
    exit 1
}

# Detect OS
detect_os() {
    local os
    os="$(uname -s)"
    case "$os" in
        Linux*)  echo "linux" ;;
        Darwin*) echo "darwin" ;;
        *)       die "Unsupported operating system: $os" ;;
    esac
}

# Detect architecture
detect_arch() {
    local arch
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64)  echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *)             die "Unsupported architecture: $arch" ;;
    esac
}

# Get latest version from GitHub
get_latest_version() {
    local url="https://api.github.com/repos/${GITHUB_REPO}/releases/latest"
    local version
    
    if command -v curl &>/dev/null; then
        version=$(curl -fsSL "$url" | grep '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
    elif command -v wget &>/dev/null; then
        version=$(wget -qO- "$url" | grep '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
    else
        die "Neither curl nor wget found. Please install one of them."
    fi
    
    if [[ -z "$version" ]]; then
        die "Failed to fetch latest version from GitHub"
    fi
    
    echo "$version"
}

# Download file
download() {
    local url="$1"
    local output="$2"
    
    info "Downloading from $url"
    
    if command -v curl &>/dev/null; then
        curl -fsSL "$url" -o "$output"
    elif command -v wget &>/dev/null; then
        wget -qO "$output" "$url"
    else
        die "Neither curl nor wget found. Please install one of them."
    fi
}

# Verify checksum
verify_checksum() {
    local file="$1"
    local checksums_file="$2"
    local filename
    filename=$(basename "$file")
    
    local expected_sum
    expected_sum=$(grep "$filename" "$checksums_file" | awk '{print $1}')
    
    if [[ -z "$expected_sum" ]]; then
        warn "Checksum not found for $filename, skipping verification"
        return 0
    fi
    
    local actual_sum
    if command -v sha256sum &>/dev/null; then
        actual_sum=$(sha256sum "$file" | awk '{print $1}')
    elif command -v shasum &>/dev/null; then
        actual_sum=$(shasum -a 256 "$file" | awk '{print $1}')
    else
        warn "sha256sum/shasum not found, skipping checksum verification"
        return 0
    fi
    
    if [[ "$expected_sum" != "$actual_sum" ]]; then
        die "Checksum verification failed!\nExpected: $expected_sum\nActual:   $actual_sum"
    fi
    
    success "Checksum verified"
}

# Determine installation directory
get_install_dir() {
    # Check if user specified a directory
    if [[ -n "${ASIMI_INSTALL_DIR:-}" ]]; then
        echo "$ASIMI_INSTALL_DIR"
        return
    fi
    
    # Try /usr/local/bin if writable
    if [[ -w "/usr/local/bin" ]]; then
        echo "/usr/local/bin"
        return
    fi
    
    # Fall back to ~/.local/bin
    echo "${HOME}/.local/bin"
}

# Add directory to PATH in shell config
add_to_path() {
    local dir="$1"
    local shell_config=""
    
    # Determine shell config file
    case "${SHELL:-}" in
        */bash)
            if [[ -f "$HOME/.bashrc" ]]; then
                shell_config="$HOME/.bashrc"
            elif [[ -f "$HOME/.bash_profile" ]]; then
                shell_config="$HOME/.bash_profile"
            fi
            ;;
        */zsh)
            shell_config="$HOME/.zshrc"
            ;;
        */fish)
            # Fish uses a different syntax
            if [[ -d "$HOME/.config/fish" ]]; then
                shell_config="$HOME/.config/fish/config.fish"
            fi
            ;;
    esac
    
    if [[ -z "$shell_config" ]]; then
        warn "Could not detect shell config file. Add $dir to your PATH manually."
        return
    fi
    
    # Check if already in PATH configuration
    if grep -q "$dir" "$shell_config" 2>/dev/null; then
        return
    fi
    
    info "Adding $dir to PATH in $shell_config"
    
    if [[ "${SHELL:-}" == */fish ]]; then
        echo "fish_add_path $dir" >> "$shell_config"
    else
        echo "" >> "$shell_config"
        echo "# Added by Asimi installer" >> "$shell_config"
        echo "export PATH=\"$dir:\$PATH\"" >> "$shell_config"
    fi
    
    warn "Restart your shell or run: source $shell_config"
}

# Main installation function
main() {
    echo ""
    echo "  ▄▀█ █▀ █ █▀▄▀█ █"
    echo "  █▀█ ▄█ █ █ ▀ █ █"
    echo ""
    echo "  A safe, opinionated coding agent"
    echo ""
    
    # Detect platform
    local os arch
    os=$(detect_os)
    arch=$(detect_arch)
    info "Detected platform: ${os}/${arch}"
    
    # Get version
    local version="${ASIMI_VERSION:-}"
    if [[ -z "$version" ]]; then
        info "Fetching latest version..."
        version=$(get_latest_version)
    fi
    info "Installing version: $version"
    
    # Version without 'v' prefix for archive name
    local version_num="${version#v}"
    
    # Construct download URLs
    local archive_name="asimi_${version_num}_${os}_${arch}.tar.gz"
    local base_url="https://github.com/${GITHUB_REPO}/releases/download/${version}"
    local archive_url="${base_url}/${archive_name}"
    local checksums_url="${base_url}/checksums.txt"
    
    # Create temporary directory
    local tmp_dir
    tmp_dir=$(mktemp -d)
    trap 'rm -rf "$tmp_dir"' EXIT
    
    # Download files
    local archive_path="${tmp_dir}/${archive_name}"
    local checksums_path="${tmp_dir}/checksums.txt"
    
    download "$archive_url" "$archive_path"
    download "$checksums_url" "$checksums_path"
    
    # Verify checksum
    verify_checksum "$archive_path" "$checksums_path"
    
    # Extract archive
    info "Extracting archive..."
    tar -xzf "$archive_path" -C "$tmp_dir"
    
    # Get installation directory
    local install_dir
    install_dir=$(get_install_dir)
    
    # Create directory if needed
    if [[ ! -d "$install_dir" ]]; then
        info "Creating directory: $install_dir"
        mkdir -p "$install_dir"
    fi
    
    # Install binary
    local binary_path="${install_dir}/${BINARY_NAME}"
    info "Installing to: $binary_path"
    
    if [[ -w "$install_dir" ]]; then
        cp "${tmp_dir}/${BINARY_NAME}" "$binary_path"
        chmod +x "$binary_path"
    else
        info "Requesting sudo access to install to $install_dir"
        sudo cp "${tmp_dir}/${BINARY_NAME}" "$binary_path"
        sudo chmod +x "$binary_path"
    fi
    
    # Check if install dir is in PATH
    if [[ ":$PATH:" != *":$install_dir:"* ]]; then
        if [[ -z "${ASIMI_NO_MODIFY_PATH:-}" ]]; then
            add_to_path "$install_dir"
        else
            warn "$install_dir is not in PATH. Add it manually to use asimi."
        fi
    fi
    
    # Verify installation
    if command -v asimi &>/dev/null; then
        success "Asimi ${version} installed successfully!"
    else
        success "Asimi ${version} installed to $binary_path"
        warn "You may need to restart your shell or add $install_dir to your PATH"
    fi
    
    echo ""
    echo "  Next steps:"
    echo "    1. Run 'asimi' to start"
    echo "    2. Use ':models' To pick a model"
    echo "    2. Use ':init' to set up your project"
    echo "    3. Use ':help' for more commands"
    echo ""
    echo "  Dependencies:"
    echo "    - Podman (for sandboxed shell): https://podman.io/docs/installation"
    echo "    - Just (for scripts): https://github.com/casey/just"
    echo ""
    echo "  Documentation: https://github.com/${GITHUB_REPO}"
    echo ""
}

# Run main
main "$@"
