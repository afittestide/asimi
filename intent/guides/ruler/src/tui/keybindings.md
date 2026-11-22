# Keybindings

> *"He who moves not forward, goes backward."* — Goethe

Asimi's keys are vi's keys. This chapter collects the ones you will use most,
grouped by what you are doing.

## Moving the cursor (NORMAL mode)

| Key | Action |
|-----|--------|
| `h` `j` `k` `l` | Left, down, up, right |
| `←` `↓` `↑` `→` | Arrow keys work too |
| `w` `b` `e` | Word forward / back / end of word |
| `0` `^` `$` | Line start / first non-blank / line end |
| `gg` `G` | Document start / end |

## History

| Key | Action |
|-----|--------|
| `↑` | Previous prompt (on the first line) |
| `↓` | Next prompt (on the last line) |
| `k` / `j` | Previous / next prompt (NORMAL mode, at the line's edge) |

## Tabs

| Key | Action |
|-----|--------|
| `gt` / `gT` | Next / previous tab |
| `1gt`, `2gt` | Jump to a numbered tab |
| `Tab` / `Shift+Tab` | Cycle tabs |

## Editing the prompt (NORMAL mode)

| Key | Action |
|-----|--------|
| `i` `I` `a` `A` | Enter INSERT (before, line-start, after, line-end) |
| `o` `O` | Open a new line below / above and insert |
| `x` `X` | Delete character under / before the cursor |
| `dw` `dd` `D` | Delete word / line / to end of line |
| `p` `P` | Paste after / before |
| `u` `Ctrl+r` | Undo / redo |

## Searching

| Key | Action |
|-----|--------|
| `/pattern` | Search forward |
| `?pattern` | Search backward |
| `n` / `N` | Next / previous match |

## Submitting and newlines (INSERT mode)

| Key | Action |
|-----|--------|
| `Enter` | Submit the prompt |
| `Alt+Enter` (`Option+Enter` on macOS) | Insert a newline without submitting |

## Special keys

| Key | Action |
|-----|--------|
| `@` | Reference a file (opens completion) |
| `#` | Comment a line (and in NORMAL mode, enter LEARNING mode) |
| `?` | Quick help (NORMAL mode) |
| `Ctrl+C` twice | Quit |
| `Ctrl+Z` | Background Asimi |
| `Ctrl+O` | Toggle the raw session view |

## Referencing files with `@`

Type `@` to open the file-completion dialog and load a file into context:

```
Can you review @main.go and suggest improvements?
```

Type to filter (`@mai`, `@src/`), navigate with `↓`/`↑`, select with `Enter`,
cancel with `Esc`. Referenced files count against your context; see
[Context](../configuration/context.md).

## A vi-fluent cheat line

```
Esc    normal      i a o   insert      :   command
@file  reference   #note   teach court  ?   quick help
gt gT  tabs        / n N   search      u   undo
```

Next: the full command list.
