# Config Files

> *"To know what you know and what you do not know, that is true knowledge."*
> — Confucius, *Analects* 2.17

Asimi reads configuration from files and environment variables. This chapter
covers where configuration lives and the settings you are most likely to
touch.

## Configuration files

Asimi looks for configuration in this order (later overrides earlier):

1. `.agents/asimi.conf` — **project-level**
2. `~/.config/asimi/asimi.conf` — **user-level**

After first run, your user file is loaded with all the defaults.

## The settings you will touch

```toml
[llm]
provider = "anthropic"                 # AI provider
model = "claude-sonnet-4-20250514"     # Model name
max_turns = 50                         # Max conversation turns
max_thinking_tokens = 10000            # Extended-reasoning budget
max_tool_output = 50000                # Max tool output, bytes

[session]
enabled = true                         # Persist sessions
auto_save = true                       # Save after each message
max_sessions = 50                      # Sessions to keep
max_age_days = 30                      # Delete older sessions
list_limit = 20                        # Sessions shown in :resume
```

## The court settings

Court behavior is configured under `[court]`:

```toml
[court]
poll_interval = "5s"               # Ritual Guard polling interval
step_idle_timeout = "5m"           # Max *silence* before aborting a step
runner_flatline_timeout = "15m"    # Stop/remove idle per-edict containers (0 disables)
```

Two of these deserve explanation:

- **`step_idle_timeout`** is an **idle** timer, not a wall-clock deadline. It
  resets on *every* notification from the minister — stream chunks, tool calls,
  and so on. A minister actively producing output (even slowly) never trips it;
  only a truly stuck step is aborted.
- **`runner_flatline_timeout`** extends the same idle metaphor to per-edict
  containers. Each edict gets its own runner and container; after this long with
  no activity, that container is stopped and removed (a later ritual recreates
  it against the same on-disk worktree). An edict with a running or queued
  ritual is never flatlined.

## Environment variables

### System

| Variable | Purpose |
|----------|---------|
| `EDITOR` | Editor used by `:export` |
| `SHELL` | Shell for container sessions (default `/bin/bash`) |

### API keys & authentication

| Variable | Purpose |
|----------|---------|
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `ANTHROPIC_OAUTH_TOKEN` | Anthropic OAuth token (priority over keyring) |
| `ANTHROPIC_BASE_URL` | Custom Anthropic endpoint |
| `OPENAI_API_KEY` | OpenAI API key |
| `GEMINI_API_KEY` | Google Gemini API key |
| `GOOGLE_CLOUD_PROJECT` | Vertex AI GCP project |
| `GOOGLE_CLOUD_REGION` | Vertex AI region (default `global`) |
| `GOOGLE_APPLICATION_CREDENTIALS` | Service-account JSON path or JSON |

### Development & testing

| Variable | Purpose |
|----------|---------|
| `ASIMI_KEYRING_SERVICE` | Override keyring service name (default `asimi-cli`) |
| `ASIMI_SKIP_GIT_STATUS` | Skip git status checks |
| `ASIMI_VERSION` | Override version string for testing |

> **The full catalog**, including OAuth endpoint overrides
> (`GOOGLE_CLIENT_ID`, `OPENAI_AUTH_URL`, …) and the sandbox-specific
> variables, lives in `intent/docs/` and the in-terminal `:help config`.

## Logging

Logs are rotated and stored under `~/.local/share/asimi/`. When running with
`--debug`, logs are instead written to `asimi.log` in the project root for
quick inspection.

## Applying changes

Restart Asimi for configuration changes to take effect.

Next: how sessions are saved and resumed.
