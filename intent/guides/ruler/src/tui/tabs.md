# Tabs

> *"There is a time for everything, and a season for every activity."*
> — Ecclesiastes 3:1

Asimi's TUI (terminal user interface) is built around **tabs**, in the spirit of
vim's own. A tab is a workspace: it has its own chat history and its own input
buffer, so switching tabs never loses your place.

## The two tabs you start with

Asimi opens with two tabs:

| Tab | Counterpart | Tempo | Purpose |
|-----|-------------|-------|---------|
| **Ruling** | the **Secretary** (中書令) | Campaign | Issue and manage edicts |
| **Hunting** | the **Chancellor** (門下侍中) | Skirmish | Explore, and distill intent |

The names are the whole idea:

- **Ruling** is the court at deliberate work. Edicts flow through their steps,
  rituals orchestrate the ministers. This is where you *govern*.
- **Hunting** is agility — knight moves, L-shaped thinking. You talk with the
  Chancellor, who sits outside the daily hierarchy and sees everything
  read-only. This is where you *spot openings*.

The image is Cao Cao's *«running the empire is like hunting»* — the ruler
sweeps the hunting grounds, the court handles the bureaucracy so the ruler can
stay in skirmish tempo.

## Moving between tabs

| Key / command | Action |
|---------------|--------|
| `gt` | Next tab |
| `gT` | Previous tab |
| `1gt`, `2gt` | Jump to tab 1, 2, … |
| `Tab` / `Shift+Tab` | Cycle tabs |
| `:tabnew <minister>` | Open a read-only view of a minister's work log |
| `:tabclose` | Close the current tab |
| `:q` | Close the current tab (quit the app if it was the last) |

## Minister tabs — observation, not conversation

`:tabnew <minister>` opens a **read-only** tab showing that minister's work log.
It is for watching, not for talking:

- `:tabnew forge` — file changes, manifests, commit history
- `:tabnew judge` — test runs and verdicts
- `:tabnew war` — battle plans and lings
- `:tabnew chancellor` — review outcomes and precedents

This is how you monitor what a minister is doing without interrupting the
court.

## Moving the court between grounds

A tab can be re-pointed at a different branch or worktree with `:switch`:

```
:switch e123    -> edict 123's worktree (branch asimi/e123); created if missing
:switch main    -> the main working tree
:switch <branch> -> a named branch, resolved to its worktree when one exists
```

`:switch` is refused while a tab is streaming, and resets tabs to their
defaults. See [Commands](commands.md).

Next: the vi modes you already know, and the two Asimi adds.
