# TUI Architecture Documentation

This document describes the architecture of the Terminal User Interface (TUI) for **Asimi**, focusing on the component boundaries, message-passing patterns, and the multi-tab structure that mirrors the Court's minister system.

## Table of Contents

1. [Overview](#overview)
2. [Tab Manager](#tab-manager)
3. [Component Architecture](#component-architecture)
4. [Mode Management System](#mode-management-system)
5. [Command Line Component](#command-line-component)
6. [List Navigation & Selection](#list-navigation--selection)
7. [Mouse Event Handling](#mouse-event-handling)
8. [Dynamic Prompt Height](#dynamic-prompt-height)
9. [Message Flow Patterns](#message-flow-patterns)
10. [Streaming AI Responses](#streaming-ai-responses)
11. [Files Reference](#files-reference)

---

## Overview

Asimi tries to follow vi, vim & neovim as closely as possible.

### Screen Layout

```
┌─────────────────────────────────────────┐
│  TAB BAR (1 line, only if multiple tabs)│  ← TabManager.RenderTabBar()
├─────────────────────────────────────────┤
│                                         │
│         CONTENT AREA                    │  ← Mouse wheel works here
│         (Chat/Help/Models/Resume/Edict)  │
│                                         │
│  Height = screen - prompt - status -    │  ← Dynamic based on prompt height
│           cmdline - tabbar - modal      │
│                                         │
├─────────────────────────────────────────┤
│  ┌───────────────────────────────────┐  │
│  │    PROMPT AREA                    │  │  ← Mouse wheel ignored here
│  │    (2-N lines, configurable)      │  │  ← Grows when multiline
│  └───────────────────────────────────┘  │
├─────────────────────────────────────────┤
│  STATUS LINE                            │
├─────────────────────────────────────────┤
│  COMMAND LINE                           │
└─────────────────────────────────────────┘
```

**Dynamic Content Height Calculation:**
```go
commandLineHeight := 1
statusHeight := 1
promptWithBorder := promptHeight + 2
tabBarHeight := m.tabs.TabBarHeight()  // 1 if multiple tabs, 0 otherwise
modalHeight := lipgloss.Height(m.modal.Render())  // 0 if no modal
contentHeight := m.height - commandLineHeight - statusHeight - promptWithBorder - tabBarHeight - modalHeight
if contentHeight < 0 {
    contentHeight = 0
}
```

The TUI follows the [Bubbletea](https://github.com/charmbracelet/bubbletea) architecture pattern:
- **Model**: `TUIModel` holds application state
- **Update**: Message handlers update state and return commands
- **View**: Renders the current state (no logic, just rendering)

### Core Principles

1. **Single Source of Truth**: State lives in one place
2. **Message Passing**: Components communicate via messages, not direct calls
3. **Separation of Concerns**: Components handle their own input and state
4. **No Polling**: View() renders state without checking component internals
5. **Centralized Coordination**: TUI coordinates, components execute

---

## Tab Manager

The `TabManager` (`content.go`) is a core architectural component that manages multiple tabs, each representing a minister in the Court. Every tab has its own `ContentComponent` (with its own chat buffer), streaming state, and cancellable context.

### Structure

See `Tab` in `content.go`.

### Default Tabs

Default tabs are derived from `DefaultTabIDs` in `internal/ministers/constants.go`.

### Key Operations

- **SwitchTo(index)** — Switch active tab, calls `onTabSwitch` callback
- **NextTab() / PrevTab()** — Wrap-around navigation (bound to `gt`/`gT` in normal mode)
- **Add(label, type, target)** — Create a new tab and switch to it
- **Close()** — Close active tab (refuses if last tab or currently streaming)
- **StreamingChat()** — Returns the chat component of the active streaming tab (falls back to any streaming tab, then active tab)
- **StreamingChatByTab(tabID)** — Returns the chat component for a specific streaming tab
- **FlushDirtyChats()** — Calls `UpdateContent` on every dirty chat (used by the debounce tick to flush all streaming tabs)
- **CancelTabByID(tabID)** — Cancels streaming on a specific tab and creates a fresh context
- **CancelAllTabs()** — Cancels all streaming; Secretary gets a fresh context for future rituals

### Tab Greetings

Each tab is seeded with a minister-specific welcome message via `initTabGreetings()` at construction time. These are injected as `MessageTypeGreeting` into each tab's `ChatComponent`.

### Welcome Screen

Before the user presses any key, a welcome screen is shown (`renderWelcome()`). It displays version info, key shortcuts, and optional update/config notifications. Any keypress dismisses it.

---

## Component Architecture

### TUIModel (tui.go)

The main model that coordinates all components:

```go
type TUIModel struct {
    config        *Config
    width, height int
    theme         *Theme

    // UI Components
    status      StatusComponent
    prompts     map[string]*PromptComponent  // per-tab prompt components
    tabs        TabManager                    // manages all content tabs
    completions CompletionDialog
    commandLine *CommandLineComponent
    modal       *BaseModal

    // UI Flags & State
    Mode                 string  // Current UI mode for status display
    showCompletionDialog bool
    completionMode       string  // "file" or "command"
    sessionActive        bool
    rawMode              bool    // Toggle between chat and raw session view
    // ... service fields, history, approval state, etc.
}
```

**Responsibilities:**
- Coordinate component interactions
- Handle high-level key bindings
- Route messages to appropriate handlers
- Maintain global state (mode, focus, etc.)

### Components

Each component is self-contained and follows these patterns:

1. **Handles its own input** via `HandleKey()` or `Update()`
2. **Returns messages** to communicate state changes
3. **Exposes minimal API** for coordination
4. **Doesn't know about other components**

### Component Access

The prompt for the active tab is accessed via the `prompt()` method (not a field), which lazily creates a `PromptComponent` if one doesn't exist for the active tab's label:

```go
func (m *TUIModel) prompt() *PromptComponent {
    label := m.tabs.ActiveTab().Label
    p, ok := m.prompts[label]
    if !ok {
        np := NewPromptComponent(m.width, 5)
        if m.config != nil && m.config.UI.PromptExpandedHeight > 0 {
            np.SetExpandedHeight(m.config.UI.PromptExpandedHeight)
        }
        p = &np
        m.prompts[label] = p
    }
    return p
}
```

The active tab's content is accessed via `m.tabs.Content()` which returns a `*ContentComponent`.

---

## Mode Management System

### Single Source of Truth

- **`TUIModel.Mode string`** field is the only place tracking mode
- **`ChangeModeMsg{NewMode: "..."}`** is the single message for ALL mode changes
- The centralized handler in `handleCustomMessages` updates `m.Mode`, `m.status.SetMode(newMode)`, and applies mode-specific side effects

### The Message

```go
type ChangeModeMsg struct {
    NewMode string // "insert", "normal", "visual", "command", "help", "models",
                   // "select", "resume", "scroll", "yesno", "input", "search",
                   // "learning", "replace"
}
```

### Centralized Handler

```go
case ChangeModeMsg:
    oldMode := m.Mode
    newMode := msg.NewMode
    m.Mode = newMode
    m.status.SetMode(newMode)

    if newMode != "command" && newMode != "yesno" && newMode != "input" {
        m.commandLine.Blur()
    }

    // Handle scroll lock state changes
    if oldMode == "scroll" && newMode != "scroll" {
        m.tabs.Content().Chat.SetScrollLock(false)
    } else if oldMode != "scroll" && newMode == "scroll" {
        m.tabs.Content().Chat.SetScrollLock(true)
    }

    // Update prompt component based on new mode
    switch newMode {
    case "insert":
        m.prompt().EnterViInsertMode()
    case "normal":
        m.prompt().EnterViNormalMode()
    case "visual":
        m.prompt().ViCurrentMode = ViModeVisual
        m.prompt().TextArea.KeyMap = m.prompt().viNormalKeyMap
        m.prompt().TextArea.Placeholder = "Visual selection mode"
        m.prompt().Style = m.prompt().Style.BorderForeground(globalTheme.PromptOffBorder)
    case "scroll":
        m.prompt().Blur()
        m.prompt().EnterViScrollMode()
    case "command":
        m.prompt().EnterViCommandLineMode()
    case "yesno":
        m.prompt().Style = m.prompt().Style.BorderForeground(globalTheme.PromptOffBorder)
    case "input":
        m.prompt().Blur()
    case "learning":
        m.prompt().EnterViLearningMode()
    case "search":
        m.prompt().Blur()
        m.prompt().Style = m.prompt().Style.BorderForeground(globalTheme.PromptOffBorder)
    case "models":
        m.prompt().Blur()
        m.prompt().TextArea.Placeholder = "j/k navigate | /? search | n/N next match | Enter to select | ESC to abort"
        m.prompt().Style = m.prompt().Style.BorderForeground(globalTheme.PromptOffBorder)
    case "select", "resume", "help":
        m.prompt().Blur()
        m.prompt().TextArea.Placeholder = "j/k, CTRL-D/U to navigate | Enter to select | ESC to abort"
        m.prompt().Style = m.prompt().Style.BorderForeground(globalTheme.PromptOffBorder)
    }
    return m, nil
```

### Supported Modes

| Mode        | Display     | Description |
|-------------|-------------|-------------|
| `insert`    | `<INSERT>`  | Default text input mode |
| `normal`    | `<NORMAL>`  | Vi-style navigation |
| `visual`    | `<VISUAL>`  | Visual selection |
| `command`   | `<COMMAND>` | Command line input (`:`) |
| `help`      | `<HELP>`    | Help viewer |
| `models`    | `<MODELS>`   | Model selection with search support |
| `select`    | `<SELECT>`  | Unified list selection (resume, edict) |
| `scroll`    | `<SCROLL>`  | Chat history navigation |
| `yesno`     | `<YESNO>`   | Yes/no prompt |
| `input`     | `<INPUT>`   | Free text input prompt |
| `search`    | `<SEARCH>`  | Vi-style search (`/` or `?`) |
| `learning`  | `<LEARNING>` | Learning note input |
| `replace`   | `<REPLACE>` | Replace mode |

### Special Modes

- **`scroll`** — Dedicated chat-navigation mode entered with `Ctrl-B`. Locks the viewport (no auto-scroll), minimizes the prompt to 2 lines, and provides vi-style paging:
  - `Ctrl-F` / `Ctrl-B` - Page down/up
  - `Ctrl-D` / `Ctrl-U` - Half page down/up
  - `j` / `k` / `↓` / `↑` - Scroll one line down/up
  - `G` - Jump to bottom
  - `:` - Enter command mode without snapping back
  - `Esc` / `i` - Return to insert mode

  Placeholder: `"j/k to scroll | CTRL-f/b & d/u as in vi | i/:/ESC to exit"`

- **`models`** — Model selection with built-in search:
  - `j` / `k` / `↓` / `↑` - Navigate
  - `Ctrl-D` / `Ctrl-U` - Half page down/up
  - `g` / `G` - Jump to top/bottom
  - `/` / `?` - Forward/backward search
  - `n` / `N` - Next/previous search match
  - `Enter` - Select model
  - `Esc` - Return to chat

- **`select`** — Unified list selection for resume and edict views:
  - `j` / `k` / `↓` / `↑` - Navigate
  - `Ctrl-D` / `Ctrl-U` - Half page down/up
  - `g` / `G` - Jump to top/bottom
  - `Enter` - Select item
  - `Esc` - Return to chat

### Benefits

- **Single Source of Truth** — `m.Mode` is the only place tracking mode
- **Single Message** — `ChangeModeMsg` for ALL mode changes
- **Centralized Logic** — One handler updates everything
- **No Polling** — View() doesn't check component state
- **Flexible** — Easy to add modes (just strings)
- **Testable** — Verify mode changes via messages
- **Decoupled** — Components don't know about status

---

## Command Line Component

### Component Self-Management

The `CommandLineComponent` (`commandline.go`) handles its own input and communicates via messages. It supports multiple sub-modes:

```go
type CommandLineMode int

const (
    CommandLineIdle CommandLineMode = iota
    CommandLineCommand   // : commands
    CommandLineToast    // Toast notifications
    CommandLineYesNo    // Yes/no prompts
    CommandLineInput    // Free text input
    CommandLineSearch    // Vi-style search (/ or ?)
)
```

### Component Messages

```go
type (
    commandReadyMsg       struct{ command string }  // Command ready to execute
    commandCancelledMsg   struct{}                   // User pressed ESC
    commandTextChangedMsg struct{}                   // Text changed, update completions
    navigateCompletionMsg struct{ direction int }    // Navigate completion list
    acceptCompletionMsg   struct{}                   // Accept selected completion
    navigateHistoryMsg    struct{ direction int }    // Navigate history
    yesNoResponseMsg      struct{ answer bool }     // true for yes, false for no
    inputResponseMsg      struct{ text string }     // Response from free text input
    apiKeyPromptMsg       struct{ provider string } // Request API key input
    apiKeySavedMsg        struct{ provider string } // API key saved to keyring
    searchExecutedMsg     struct {
        pattern   string
        direction int
    }
    searchCancelledMsg struct{}
)
```

### HandleKey Method

```go
func (cl *CommandLineComponent) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool)
```

The method dispatches based on the current sub-mode:

1. **YesNo mode**: `y`/`Y` → yes, `n`/`N` → no, `enter` confirms, `esc` cancels
2. **Input mode**: `enter` submits text, `esc` cancels, other keys go to textinput
3. **Search mode**: `enter` executes search, `esc`/empty-backspace cancels, other keys go to textinput
4. **Command mode**: `enter` executes, `esc` cancels, `up`/`down` navigate history, `tab` accepts completion, `ctrl+n`/`ctrl+p` navigate completions

Returns `(tea.Cmd, bool)` — the command to execute and whether the key was handled.

### TUI Integration

```go
if m.commandLine.IsInCommandMode() || m.commandLine.IsInYesNoMode() ||
   m.commandLine.IsInInputMode() || m.commandLine.IsInSearchMode() {
    cmd, handled := m.commandLine.HandleKey(msg)
    if handled {
        return m, cmd
    }
    return m, nil
}
```

---

## List Navigation & Selection

### ListNavigator Interface

All selectable list views implement the `ListNavigator` interface (`select_window.go`):

```go
type ListNavigator interface {
    GetItemCount() int
    GetVisibleSlots() int
    NavNext(current int) int
    NavPrev(current int) int
    NavFirst() int
    NavLast() int
    NavNearest(index int) int // find nearest selectable to index
}
```

### SelectWindow — Generic Component

`SelectWindow[T any]` is a generic, reusable list component that implements `ListNavigator`. It supports:
- Selectable item filtering (skip non-selectable items during navigation)
- Scrolling with configurable visible slots
- Custom rendering via `RenderConfig[T]`

### Views Using List Navigation

The `ContentComponent` (`content.go`) uses `activeList ListNavigator` to delegate navigation. Each view sets up its list and `onSelect` callback:

| View         | Mode     | List Source                   | On Select |
|--------------|----------|-------------------------------|-----------|
| `ViewModels` | `models`  | `c.models.SelectWindow`       | Selects model, returns `modelSelectedMsg` |
| `ViewResume` | `select`  | `c.resume.SelectWindow`      | Loads session |
| `ViewEdict`    | `select`  | `c.edictSelect.SelectWindow`  | Returns `edictSelectedMsg` with edict ID |

### Navigation Keys (List Mode)

- `j` / `↓` — Next item
- `k` / `↑` — Previous item
- `Ctrl-D` — Half page down
- `Ctrl-U` — Half page up
- `g` / `home` — First item
- `G` / `end` — Last item
- `Enter` — Select item
- `n` / `N` — Next/previous search match (models view only)

---

## Mouse Event Handling

### Implementation

Mouse events are handled in `TUIModel.Update()`:

```go
case tea.MouseMsg:
    // Handle mouse wheel scrolling - switch to SCROLL mode when scrolling up
    if msg.Type == tea.MouseWheelUp && m.Mode != "scroll" && m.tabs.Content().GetActiveView() == ViewChat {
        // Only enter scroll mode if we're not already at the top
        if !m.tabs.Content().Chat.Viewport.AtTop() {
            contentCmd := m.tabs.UpdateContent(msg)
            enterScrollCmd := func() tea.Msg { return ChangeModeMsg{NewMode: "scroll"} }
            return m, tea.Batch(contentCmd, enterScrollCmd)
        }
    }
    contentCmd := m.tabs.UpdateContent(msg)
    return m, contentCmd
```

### Event Flow

```
Mouse Wheel Event
       ↓
   TUI Update()
       ↓
   Is MouseWheelUp AND not in scroll mode AND in chat view?
       ↓
       ├─→ YES AND viewport not at top
       │        ↓
       │   Batch: content.Update(msg) + ChangeModeMsg{NewMode: "scroll"}
       │        ↓
       │   Chat scrolls + enters scroll mode
       │
       └─→ NO (all other cases)
                ↓
           content.Update(msg)
                ↓
           Chat handles scrolling (wheel up/down, touch drag)
```

### Component Responsibilities

**TUIModel** (`tui.go`):
- Detects scroll-up over chat to auto-enter scroll mode
- Routes all mouse events to the active tab's content component
- Does NOT do position-based Y checking — delegates to content component

**ContentComponent** (`content.go`):
- Receives mouse events from TUI
- Delegates to active view (chat handles its own; help view scrolls viewport)
- Handles view-specific mouse behavior

**ChatComponent** (`chat.go`):
- Handles mouse wheel scrolling
- Updates viewport position
- Tracks user scrolling vs auto-scrolling
- Supports touch gestures (drag scrolling)

**PromptComponent** (`prompt.go`):
- Does NOT handle mouse events
- Only responds to keyboard input

---

## Dynamic Prompt Height

### Behavior

The prompt dynamically adjusts its height based on content:

- **Minimum (2 lines)**: Empty prompt, single-line content, or scroll mode
- **Expanded (`ExpandedHeight`, default 10)**: When content spans multiple lines (including wrapped text)
- **Maximum (50% of screen)**: Hard cap to ensure content area remains usable
- **Answering mode**: Sized to fit title + question + options (including "Edit"/"Chat" which are part of Options)

### Height Calculation Logic

```go
func (p *PromptComponent) CalculateDesiredHeight() int {
    // Scroll mode always minimizes to 2 lines
    if p.ViCurrentMode == ViModeScroll {
        return 2
    }

    // In answering mode, size for: title + question + options + padding
    if p.answering != nil && p.answering.Current < len(p.answering.Questions) {
        q := p.answering.Questions[p.answering.Current]
        h := 3 + len(q.Options)
        if p.MaxHeight > 0 && h > p.MaxHeight {
            return p.MaxHeight
        }
        return h
    }

    value := p.TextArea.Value()
    if value == "" {
        return 2
    }

    // Calculate visual lines (accounting for word wrap)
    textWidth := p.Width - 4
    if textWidth <= 0 {
        textWidth = 1
    }
    visualLines := 0
    for _, line := range strings.Split(value, "\n") {
        if len(line) == 0 {
            visualLines++
        } else {
            visualLines += (len(line) + textWidth - 1) / textWidth
        }
    }

    if visualLines <= 1 {
        return 2
    }

    expandedHeight := p.ExpandedHeight
    if expandedHeight <= 0 {
        expandedHeight = 10
    }
    if p.MaxHeight > 0 && expandedHeight > p.MaxHeight {
        return p.MaxHeight
    }
    return expandedHeight
}
```

### Configuration

```go
// internal/config/types.go
type UIConfig struct {
    MarkdownEnabled      bool          `koanf:"markdown_enabled"`
    CtrlCDebounceTime    time.Duration `koanf:"ctrl_c_debounce_time"`
    CtrlCWindowTime      time.Duration `koanf:"ctrl_c_window_time"`
    PromptExpandedHeight int           `koanf:"prompt_expanded_height"` // Default: 10
    TickInterval   time.Duration `koanf:"tick_interval"`   // Chat render-tick debounce, Default: 100ms
}
```

```yaml
# asimi.conf
ui:
  prompt_expanded_height: 15  # Grow to 15 lines instead of default 10
  tick_interval: 250ms  # Debounce between streaming chat content updates
```

### Integration with View()

The `View()` method recalculates prompt height before each render:

```go
func (m TUIModel) View() string {
    // ...
    m.prompt().SetScreenHeight(m.height)
    m.prompt().SetWidth(m.width - 2)
    promptHeight := m.prompt().CalculateDesiredHeight()
    m.prompt().SetHeight(promptHeight)
    promptWithBorder := promptHeight + 2

    commandLineHeight := 1
    statusHeight := 1
    tabBarHeight := m.tabs.TabBarHeight()
    contentHeight := m.height - commandLineHeight - statusHeight - promptWithBorder - tabBarHeight
    if contentHeight < 0 {
        contentHeight = 0
    }
    m.tabs.SetSize(m.width-2, contentHeight)
    // ...
}
```

### Height Transitions

| Content State | Visual Lines | Resulting Height |
|--------------|--------------|------------------|
| Empty | 0 | 2 lines |
| Single line "hello" | 1 | 2 lines |
| "line1\nline2" | 2 | ExpandedHeight (10) |
| Long wrapped text | 2+ | ExpandedHeight (10) |
| Any content in scroll mode | N/A | 2 lines |
| Multiline (small screen) | 2+ | MaxHeight (50% of screen) |
| Answering mode | N/A | 3 + options |

---

## Message Flow Patterns

### Pattern 1: Component State Change

```
User action
  ↓
Component method called
  ↓
Component updates internal state
  ↓
Returns ChangeModeMsg (or other message)
  ↓
TUI receives in handleCustomMessages
  ↓
TUI updates global state
  ↓
View() renders
```

### Pattern 2: Component Input Handling

```
User presses key
  ↓
TUI.handleKeyMsg() checks active component
  ↓
Calls Component.HandleKey(msg)
  ↓
Component processes key
  ↓
Returns (tea.Cmd, bool)
  ↓
TUI executes command (if any)
  ↓
Message flows back to handleCustomMessages
```

### Pattern 3: Batched Messages

Components can return multiple messages at once:

```go
case "esc":
    exitCmd := cl.ExitCommandMode()  // ChangeModeMsg{NewMode: "insert"}
    return tea.Batch(
        exitCmd,
        func() tea.Msg { return commandCancelledMsg{} },
    ), true
```

### Pattern 4: Command Chaining

Methods return commands that can be chained:

```go
return m, m.tabs.Content().ShowHelp(msg.topic)  // Returns ChangeModeMsg
```

---

## Streaming AI Responses

The TUI handles streaming responses from the AI through the Court's message types. Streaming chunks are routed to the correct tab based on `ChannelID`.

### Message Types

- `court.StreamStartMsg` — Streaming has started; sets the tab as streaming, captures edict ID
- `court.StreamChunkMsg` — Content chunk with optional `Text` and `Reasoning` fields
- `court.StreamCompleteMsg` — Marks the end of a streaming response

### Message Flow

```
AI starts response
       ↓
   court.StreamStartMsg
       → SetStreamingTabByTab(channelID)
       → Clear error state
       ↓
   court.StreamChunkMsg (repeated)
       → Route to correct tab via ChatByTab(channelID)
       → If msg.Reasoning != "": chat.AddThinkingChunk(reasoning)
       → If msg.Text != "": chat.AddAIChunk(text)
       → Update stream rate on status component
       → Mark chat as contentDirty
       → Schedule 50ms debounce tick if not already pending
       ↓
   chatRenderTickMsg (debounce)
       → FlushDirtyChats() — calls UpdateContent on every dirty tab
       ↓
   court.StreamCompleteMsg
       → ClearStreamingByTab(channelID)
       → FinalizeLastAIMessage() — marks as SUCCESS or FAILURE
       → Run streamCompleteCallback if set
       → Save session, refresh diff
```

### Debounced Rendering

Chat content is not re-rendered on every chunk. Instead:
1. Each chunk sets `chat.contentDirty = true`
2. A 50ms debounce tick is scheduled (only one at a time, guarded by `renderTickPending`)
3. When the tick fires, `FlushDirtyChats()` calls `UpdateContent()` on every dirty chat across all tabs

This prevents UI stalls when the AI streams content rapidly and ensures non-active tabs get flushed too.

### Rendering

The `ChatComponent.UpdateContent()` method handles rendering based on message type:

| MessageType | Rendering |
|-------------|-----------|
| `MessageTypeThinking` | 💭 prefix, dimmed thinking color, word-wrapped |
| `MessageTypeAI` | 🎏 prefix, markdown rendered (streaming, not finalized) |
| `MessageTypeAISuccess` | 🐉 prefix, markdown rendered |
| `MessageTypeAIFailure` | 🦐 prefix, markdown rendered |
| `MessageTypeUser` | 👑 prefix, prompt border color |
| `MessageTypeShell` | `$` prefix, prompt border color |
| `MessageTypeGreeting` | Greeting prefix, word-wrapped |
| `MessageTypeSystem` | System/tool call output |

`FinalizeLastAIMessage()` converts the last `MessageTypeAI` message to either `MessageTypeAISuccess` or `MessageTypeAIFailure` based on whether the response contains a failure token.

---

## Files Reference

### Core Files
- `tui.go` — Main TUI model, coordination logic, `View()`, `handleKeyMsg()`, `handleCustomMessages()`
- `content.go` — `TabManager`, `Tab`, `ContentComponent`, and all view rendering
- `commandline.go` — `CommandLineComponent` with command/yesno/input/search sub-modes
- `prompt.go` — `PromptComponent` with vi modes and dynamic height
- `status.go` — `StatusComponent` with mode display, branch info, stream rate
- `chat.go` — `ChatComponent` with message types, streaming, markdown rendering
- `select_window.go` — `ListNavigator` interface, generic `SelectWindow[T]` component
- `completion.go` — `CompletionDialog` for file/command completions
- `internal/config/types.go` — `UIConfig` including `PromptExpandedHeight`
