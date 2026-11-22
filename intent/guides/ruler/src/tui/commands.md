# Commands

> *"Better a diamond with a flaw than a pebble without."* — attributed to Confucius

Commands are entered from **COMMAND-LINE mode**: press `:` in NORMAL mode,
type the command, and press `Enter`.

This is the authoritative command surface — the same list Asimi shows for
`:help commands`.

## Court

| Command | Action |
|---------|--------|
| `:edicts` | Manage edicts (read, enact, seal, resume, cancel) |
| `:continue` | Resume a paused ritual on the current tab |
| `:abort` | Abort a paused ritual on the current tab |

## Navigation

| Command | Action |
|---------|--------|
| `:switch <target>` | Move the Court to a branch or edict worktree in place |

`:switch` targets:

```
:switch e123     -> edict 123's worktree (branch asimi/e123); created if missing
:switch main     -> the main working tree
:switch <branch> -> a named branch, resolved to its worktree when one exists
```

It is refused while a tab is streaming, and resets tabs to their defaults.

## Session & information

| Command | Action |
|---------|--------|
| `:new` | Start a new conversation |
| `:resume` | Resume a previous session |
| `:help [topic]` | Show help, optionally for a specific topic |
| `:context` | Show context usage and token information |
| `:export [type]` | Export the conversation to a file and open it in `$EDITOR` |

`:export` types: `conversation` (default), `full`.

## Tabs

| Command | Action |
|---------|--------|
| `:tabnew <minister>` | Open a read-only view of a minister's work log |
| `:tabclose` | Close the current tab |
| `:quit` / `:q` | Close the current tab (quit the app if last) |
| `:qa` / `:quitall` | Quit the application (all tabs) |

## Configuration

| Command | Action |
|---------|--------|
| `:init [clean]` | Initialize the project: creates `AGENTS.md`, `Justfile`, `.agents/sandbox/` |
| `:models` | Select an AI model (alias `:model`) |
| `:login` | Authenticate with an AI provider |
| `:update` | Check for and install updates |

> **`:models` is not `:login`.** `:models` opens the model picker; `:login`
> authenticates a provider. Older material may conflate the two — it is wrong.
> See [Providers & Login](../models/providers.md).

## Examples

```
:help modes      Show help about vi modes
:new             Start a fresh conversation
:export          Export and edit the conversation
:context         Check token usage
:tabnew forge    Watch the Forge at work
:switch main     Return the court to the main tree
:edicts          Open the edict manager
```

Next: connecting Asimi to a model.
