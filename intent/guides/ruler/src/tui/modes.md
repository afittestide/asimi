# Modes

> *"Everything has beauty, but not everyone sees it."* — attributed to Confucius

Asimi speaks vi. If you have ever used vim or neovim, you are already at home:
the same **modal** editing, the same muscle memory. Asimi extends the set with
two modes of its own.

Each mode has a **status line** and a **colour**, so you always know where you
are without thinking about it.

## INSERT mode — default

Where you compose messages.

| | |
|---|---|
| **Status** | `-- INSERT --` |
| **Border** | Green |
| **Enter it** | `i`, `I`, `a`, `A`, `o`, `O` |
| **Leave it** | `Esc` |

Press `Enter` to submit; press `Alt+Enter` (Option+Enter on macOS) to insert a
newline without sending.

## NORMAL mode

Where you navigate and issue commands.

| | |
|---|---|
| **Status** | `-- NORMAL --` |
| **Border** | Yellow |
| **Enter it** | `Esc` |

From NORMAL you can navigate with `h j k l`, enter commands with `:`, return to
INSERT with `i`/`a`/`o`, and show quick help with `?`.

## COMMAND-LINE mode

Where you run Asimi commands.

| | |
|---|---|
| **Status** | `:` |
| **Border** | Magenta |
| **Enter it** | `:` from NORMAL |

Type a command and press `Enter`. `Esc` cancels. `Tab` completes.

## SCROLL mode

Where you read the conversation without disturbing the prompt.

| | |
|---|---|
| **Status** | `-- SCROLL --` |
| **Border** | Cyan |
| **Enter it** | `Ctrl+b`, or scroll the mouse wheel up |

While scrolling: `j`/`k` line by line, `Ctrl+d`/`Ctrl+u` half page, `Ctrl+f`/
`Ctrl+b` full page, `G` to the bottom. `:` re-enters COMMAND-LINE while staying
in history; `Esc` or `i` returns you to INSERT.

## LEARNING mode — Asimi's own

A mode for teaching the court about your project, in place.

| | |
|---|---|
| **Status** | `-- LEARNING --` |
| **Border** | Purple |
| **Enter it** | `#` from NORMAL |

Type a note and press `Enter` to **append it to `AGENTS.md`**. This is how you
feed the court a convention without leaving the terminal:

```
# We use snake_case for function names in this project
```

Every minister reads `AGENTS.md` as standing law, so what you write here
becomes a rule the whole court follows. `Esc` cancels.

## Select mode — Asimi's own

Some actions open a **selection list** rather than a prompt: choosing a session
to resume, a model to connect to, a file to reference. In a selection list:

| Key | Action |
|-----|--------|
| `j`/`k` or `↓`/`↑` | Move through the list |
| `/` | Search forward (`?` backward) |
| `Enter` | Select |
| `Esc` / `q` | Cancel |

## The modes at a glance

| Mode | Status | Border | How to enter |
|------|--------|--------|--------------|
| INSERT | `-- INSERT --` | Green | `i a o` … (default) |
| NORMAL | `-- NORMAL --` | Yellow | `Esc` |
| COMMAND-LINE | `:` | Magenta | `:` |
| SCROLL | `-- SCROLL --` | Cyan | `Ctrl+b` |
| LEARNING | `-- LEARNING --` | Purple | `#` |

Next: the keys themselves.
