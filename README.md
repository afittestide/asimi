# Asimi CLI

[![Tests](https://img.shields.io/github/actions/workflow/status/afittestide/asimi/ci.yml?branch=main&label=tests)](https://github.com/afittestide/asimi/actions/workflows/ci.yml?query=branch%3Amain)

> A safe, fast, imperial court to help you rule your project

Asimi is an opinionated multi-model coding agent with a new type of interface - a government.
With Asimi you're the ruler of your project and your intent is formulated and implemented by
a small government of 4 ministers.

For the fittest government system we've adopted and simplified the court of the three departments and six ministries system from Imperial China (三省六部). It's a system that's been used for over a millennium and its principles are still relevant.
Here are those that are key for Asimi:
- Wu-Wei as the goal - Always seeking the simplest solution
- Rectification of names - Naming is hard and your ministers grok that
- Separation of powers - No minister is fully trusted e.g., if the forge fakes a test, the judge will catch it.
- Rituals - Asimi replaces the artistic rituals with workflows that weave prompts, setup and guardrails.
- The ruler can be wrong

> **New to Asimi?** Start with the [Ruler's Guide](docs/RULER_GUIDE.md) — a 10-minute walkthrough from installation to your first edict.

![Asimi Demo](demo.gif)

## ✨ Features

- 📦 **Integrated Podman Sandboxes** - Agent's shell runs in its own container
- 🎨 **vi mimicry** - Asimi is based on the fittest dev tool and its reincarnations vim and neovim
- 🤖 **Multiple AI Providers** - Support for Ollama, Claude Pro/Max and OpenAI API v1 compatible services
- 🧹 **Less Clutter** - Asimi's special files are under `.agents` directory and TOML is used for .conf
- **Long Term Memory** - Asimi uses sqlite to store the court records
- 📊 **Context Awareness** - token counting and context visualization
- 🎯 **Session Management** - Save and resume your coding sessions
- **Rituals** - workflows defined in yaml for hardcoded guardrails

## 🚀 Quick Start

First, there are two great tools required on your system:

- [Podman](https://podman.io/docs/installation) for the sandbox: like `docker` but safer
- [Just](https://github.com/casey/just) to collect all the scripts in a Justfile


Then, choose your installer flavor:

### One-liner (Recommended)

The script below downloads the right binary for your system and copies it.
If you prefer to do it yourself, see below.

```bash
curl -fsSL https://asimi.dev/installer | bash
```

**Options:**
- `ASIMI_INSTALL_DIR=/path/to/dir` - Custom installation directory
- `ASIMI_VERSION=v0.1.0` - Install a specific version

### brew

```bash
brew tap afittestide/tap
brew install asimi
```

### Go

```bash
go install github.com/afittestide/asimi@latest
```

### Binaries

Download the binary from your platform from [latest releases](https://github.com/afittestide/asimi/releases/latest) and copy to your favorite bin directory e.g, `/usr/local/bin`.

### First Steps

To start Asimi in interactive mode, type `asimi`.

1. **Initialize your repo:**
    `:init` - Creates `AGENTS.md` and `Justfile` if missing, and prepares the sandbox image

2. **Check your container:**
    `:!uname -a` - Runs shell commands in a persistent, containerized bash
    `:!pwd` - Should be the same path as on your host

## 🏝️ The Sandbox

Asimi uses podman to run the agent's shell in its own container.
podman is compatible with docker so there's no need to learn new commands.
Asimi uses it instead of docker because it's more secure - on linux it doesn't require a daemon.

The sandbox is based on `.agents/sandbox/Dockerfile` which is created by the `init` command. 
To build the image run `just build-sandbox` for an image named as in:
`localhost/asimi/sandbox/afittestide/asimi:latest`.
Asimi will launch a container based on this image the first time the model calls the shell tool.
The container will stay up as long as the program is running. 
Once the program exits, the container is shutdown and removed.

Some commands, like gh, can't run in the sandbox.
For these commands you can add a special exception in the config file.

To run commands in the container use `:!<shell command>`.

### Configuration Options

The sandbox can be configured in `.agents/asimi.conf` (project-level) or `~/.config/asimi/asimi.conf` (user-level).


```toml
[sandbox]
# Regex patterns for commands to run on the host (requires user approval)
run_on_host = ['^gh\s', '^podman\s']
# Regex patterns for commands to run on the host WITHOUT approval (safe, read-only)
safe_run_on_host = ['^gh\s+(issue|pr)\s+(view|list)', '^git\s+status']

# Custom container image name (default: localhost/asimi/sandbox/<project-name>:latest)
image_name = "localhost/my-custom-sandbox:latest"

# Additional mount points for the container
# Useful for mounting secrets, caches, or other project directories
[[sandbox.additional_mounts]]
source = "/path/to/host/dir"
destination = "/path/in/container"

[[sandbox.additional_mounts]]
source = "/another/host/path"
destination = "/another/container/path"
```


## ⌨️ vi FTW

Asimi mimics the vi/vim/neovim interface and extends the traditional modes:

### Traditional Modes

- **Insert Mode** For typing your prompts
- **Normal Mode** Navigation and editing the prompt
- **Command Mode** Entering agents commands
   - `:help` - Show help 
   - `:context` - View token usage and context
   - `:new` - Start a new conversation
   - `:resume` - resume an old session

### New Modes

- **Scroll Mode** Use CTRL-B to enter the mode and scroll with your keys
- **Select Mode** For choosing a session to resume, a model to connect to, etc.

## 🛠️ Development

Asimi's roadmap can be found in ./docs/roadmap.md .

### Contributor Installation

To develop Asimi itself:

1. **Fork and clone:**
   ```bash
   gh repo fork afittestide/asimi --clone
   cd asimi
   ```

2. **Install dependencies:**
   ```bash
   just bootstrap    # Installs goimports, govulncheck
   ```

3. **Build and run:**
   ```bash
   just build        # Build binary
   just run          # Run with debug logging to ./asimi.log
   ```

4. **Initialize sandbox (first run):**
   ```bash
   just build-sandbox
   ```

5. **Run tests:**
   ```bash
   just test
   ```

### Loopback Mode (Testing)

By default, Asimi autostarts a daemon process for RPC communication between
the TUI and the court. In rare cases — debugging RPC issues, running in
environments where spawning a daemon is undesirable — contributors can set
`ASIMI_LOOPBACK=1` to use an in-process `net.Pipe` loopback instead of a
real daemon socket. This routes all wire-safe calls and notifications
through the MessagePack RPC codec in-process — same code path, no external
process.

```bash
ASIMI_LOOPBACK=1 just run
```

> **Note:** Loopback mode is a testing escape-hatch, not a production mode.

## Principles

- Before the 🦙 comes the dev
- Mimicking is better than innovation
- ex/vi/vim/neovim are the best TUI ever made
- User's host is sacred and 🦙 access should be as restricted
- Integrations are great, let's have more of these

### Prerequisites

- Go 1.25 or higher
- `just bootstrap`

### Common Tasks

We're using a `Justfile` to collect all our scripts.
If you need a new script please add a recipe in the Justfile.

```bash
# List recipes
just

# run Asimi
just run

# Run tests
just test

# measure shell's  performance
just measure
```

### Project Structure

Flat. Please refrain from adding directories and files.

## 📦 Libraries

- **[LangChainGo](https://github.com/afittestide/langchaingo)** - Using our own fork for model communications
- **[Bubble Tea](https://github.com/charmbracelet/bubbletea)** - Terminal UI framework
- **[Koanf](https://github.com/knadh/koanf)** - Configuration management
- **[Kong](https://github.com/alecthomas/kong)** - CLI argument parser
- **[Glamour](https://github.com/charmbracelet/glamour)** - Markdown rendering

## 🔒 Security

Asimi takes security seriously:

- API keys are stored securely in your system keyring
- No data is sent to third parties except your chosen AI provider
- Shell commands are executed in a containerized sandbox

## 🤝 Contributing

We welcome contributions! Here are some ways you can help:

1. **Report bugs** - Open an issue with details
2. **Suggest features** - Share your ideas
3. **Submit PRs** - Fix bugs or add features

### Commit Message Style

We use present progressive tense for commit messages:

```bash
# Good
git commit -m "feat: adding markdown support"
git commit -m "bug: fixing context overflow bug"

# Avoid
git commit -m "added markdown support"
git commit -m "fixed context overflow bug"
```

## 📝 Configuration

Asimi stores its configuration in `~/.config/asimi/asimi.conf` (user-level) or `.agents/asimi.conf` (project-level).
After first run the user's file is loaded with all the defaults.


### Environment Variables

#### System Variables

- **`EDITOR`** - Preferred text editor for `:export` commands (default: system default)
- **`SHELL`** - Shell to use in container sessions (default: `/bin/bash`)

#### API Keys & Authentication

- **`ANTHROPIC_API_KEY`** - API key for Anthropic Claude models (alternative to OAuth)
- **`ANTHROPIC_OAUTH_TOKEN`** - OAuth token for Anthropic API (takes priority over keyring). Supports three formats:
  - Raw access token: `sk-ant-...`
  - JSON format: `{"access_token":"...", "refresh_token":"...", "expiry":"...", "provider":"anthropic"}`
  - Base64-encoded JSON (useful if copying from keychain)
- **`ANTHROPIC_BASE_URL`** - Custom base URL for Anthropic API (e.g., for proxy or custom endpoint)
- **`OPENAI_API_KEY`** - API key for OpenAI GPT models
- **`GEMINI_API_KEY`** - API key for Google Gemini models

**Vertex AI (Google Cloud) — via gcloud ADC:**
- `GOOGLE_CLOUD_PROJECT` - GCP project ID (required for Vertex AI)
- `GOOGLE_CLOUD_REGION` - GCP region (optional, defaults to `global`)
- `GOOGLE_APPLICATION_CREDENTIALS` - Path to a service-account JSON, or the JSON itself (optional)
- Auth: `gcloud auth application-default login` (Application Default Credentials). No `VERTEX_API_KEY` is used.

#### OAuth Configuration (Advanced)

For custom OAuth setups, you can override the default OAuth endpoints:

**Google/Gemini:**
- `GOOGLE_CLIENT_ID` - OAuth client ID
- `GOOGLE_CLIENT_SECRET` - OAuth client secret
- `GOOGLE_AUTH_URL` - Authorization URL (optional, default: `https://accounts.google.com/o/oauth2/v2/auth`)
- `GOOGLE_TOKEN_URL` - Token URL (optional, default: `https://oauth2.googleapis.com/token`)
- `GOOGLE_OAUTH_SCOPES` - Comma-separated list of scopes (optional, default: generative-language scope)

**OpenAI:**
- `OPENAI_CLIENT_ID` - OAuth client ID
- `OPENAI_CLIENT_SECRET` - OAuth client secret
- `OPENAI_AUTH_URL` - Authorization URL
- `OPENAI_TOKEN_URL` - Token URL
- `OPENAI_OAUTH_SCOPES` - Comma-separated list of scopes (optional)

**Anthropic:** Not yet implemented. Anthropic OAuth env vars are not currently supported.

#### Development & Testing

- **`ASIMI_KEYRING_SERVICE`** - Override keyring service name (default: `asimi-cli`)
- **`ASIMI_SKIP_GIT_STATUS`** - Skip git status checks (set to any value to enable)
- **`ASIMI_VERSION`** - Override version string for testing


Logs are rotated and stored in `~/.local/share/asimi/`. When running with `--debug`, logs are instead written to `asimi.log` in the project root for quick inspection.


## 🙏 Acknowledgments

- Built with ❤️ using Go
- Inspired by vi and his great grandchildren - the coding agents
- Special thanks to the Bubble Tea and LangChainGo communities

