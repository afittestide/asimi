# Installation

> *"A journey of a thousand miles begins with a single step."*
> — Laozi, *Tao Te Ching* (千里之行，始於足下)

Asimi is a single binary plus two supporting tools. This chapter gets you from
nothing to a running court.

## What you need

Two tools are required on your system before Asimi will work well:

- **[Podman](https://podman.io/docs/installation)** — for the sandbox. Asimi
  runs the agent's shell commands in its own container. Podman is used instead
  of Docker because it is more secure (on Linux it needs no daemon).
- **[Just](https://github.com/casey/just)** — a command runner, used by your
  project's `Justfile` to collect its scripts.

You will also need access to at least one AI provider — locally hosted
(Ollama), or a hosted API (Anthropic, OpenAI, Google, and others). See
[Providers & Login](../models/providers.md).

## Installing Asimi

Choose one of these paths.

### One-liner (recommended)

The installer downloads the right binary for your system and copies it into
place:

```bash
curl -fsSL https://asimi.dev/installer | bash
```

Options:

- `ASIMI_INSTALL_DIR=/path/to/dir` — custom installation directory
- `ASIMI_VERSION=v0.1.0` — install a specific version

### Homebrew

```bash
brew tap afittestide/tap
brew install asimi
```

### Go

```bash
go install github.com/afittestide/asimi@latest
```

### Prebuilt binary

Download the binary for your platform from the
[latest releases](https://github.com/afittestide/asimi/releases/latest) and
copy it to a directory on your `PATH` (e.g. `/usr/local/bin`).

## First run

Start Asimi in interactive mode by typing:

```bash
asimi
```

You will land in the **Ruling** tab, in INSERT mode, facing the Secretary.
(If this is your very first launch, the court boots with a **dawn-audience** —
a short briefing on the state of the three realms. See
[Rituals](../concepts/rituals.md).)

Two commands orient you before any real work:

1. **Initialize your repo**

   ```
   :init
   ```

   Creates `AGENTS.md` and a `Justfile` if missing, and prepares the sandbox
   image definition under `.agents/`.

2. **Check your container**

   ```
   :!uname -a
   :!pwd
   ```

   The `:!` prefix runs a shell command **inside the sandbox**. `uname -a`
   should report `Linux` (the sandbox is Linux, even on macOS), and `pwd`
   should match your host project path.

You are now ready. The next chapter issues your first edict.

## Where Asimi keeps things

| Path | What lives there |
|------|------------------|
| `.agents/` | Asimi's project files: sandbox definition, rituals, config |
| `.agents/asimi.conf` | Project-level configuration |
| `~/.config/asimi/asimi.conf` | User-level configuration |
| `~/.local/share/asimi/` | Logs and saved sessions |
| `AGENTS.md` | Your project's conventions, taught to the ministers |

> **Note on `AGENTS.md`.** This file is how you teach the ministers your
> project's conventions — how to build, how to test, the coding style. When you
> write conventions here, every minister reads them as standing law.
