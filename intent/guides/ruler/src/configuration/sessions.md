# Sessions

> *"What is past is prologue."* — Shakespeare, *The Tempest*

Asimi can save and resume your coding sessions, preserving the entire
conversation history and context.

## Starting fresh

```
:new             Start a fresh conversation
```

`:new` clears the chat history and context. Start a new session whenever you
begin a genuinely new task.

## Resuming

```
:resume          Show recent sessions; pick one to resume
```

The session list shows:

- the **first prompt** from each session,
- **time since last update**, and
- the **project**/directory.

Navigation:

| Key | Action |
|-----|--------|
| `↓`/`↑` | Move through sessions |
| `Enter` | Resume the selected session |
| `Esc` | Cancel |

## Auto-save

Sessions are saved automatically when:

- you send a message,
- you quit Asimi, or
- you start a new session.

## Session configuration

```toml
[session]
enabled = true           # Enable session persistence
auto_save = true         # Auto-save after each message
max_sessions = 50        # Maximum sessions to keep
max_age_days = 30        # Delete sessions older than this
list_limit = 20          # Number shown in :resume
```

## Where sessions live

```
~/.local/share/asimi/sessions/
```

Each session includes the full conversation history, context files, the model
and provider in use, and timestamps.

## Exporting

```
:export              Export the conversation
:export conversation Export just the conversation
:export full         Export with full context
```

The exported file opens in your `$EDITOR` for review or sharing.

Next: keeping an eye on the context window.
