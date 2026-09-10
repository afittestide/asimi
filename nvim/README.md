# asimi.nvim

A Neovim plugin that turns Neovim into a first-class frontend for the
[Asimi](https://github.com/afittestide/asimi-cli) Court daemon. Communicates
over the existing msgpack-RPC unix socket — no Go code changes needed.

## Installation

### lazy.nvim

```lua
{
  "afittestide/asimi-cli",
  dir = "/path/to/asimi-cli/nvim",
  config = function()
    require("asimi").setup()
  end,
}
```

### Manual

Add `nvim/` to your runtimepath and call `require("asimi").setup()`.

## Setup

```lua
require("asimi").setup({
  socket = nil,           -- override socket path (default: auto-discovered)
  autoconnect = true,     -- connect on startup
  autostart = true,       -- spawn daemon if socket is dead
  log_level = "info",     -- debug | info | warn | error
  api_keys = {},          -- additional API keys (merged with env vars)
})
```

## Commands

| Command | Description |
|---|---|
| `:AsimiConnect [path]` | Connect to the daemon (or autostart) |
| `:AsimiDisconnect` | Disconnect from the daemon |
| `:AsimiHealth` | Check minister availability |
| `:AsimiTab [name]` | Switch to minister tab |
| `:AsimiCancel` | Cancel streaming for current minister |

## Chat

Each minister has its own tabpage with a `buftype=prompt` buffer:
- Type at the `> ` prompt and press `<CR>` to submit
- Streaming responses appear above the prompt line
- `<Ctrl-C>` cancels a running stream
- `G` resumes auto-scroll to follow the stream

### Icons

| Icon | Meaning |
|---|---|
| 👑 | User message |
| 🎏 | AI streaming |
| 🐉 | AI success |
| 🦐 | AI failure |
| 💭 | Reasoning/thinking |
| 🛠️ | System message |
| 🏯 | Greeting |
| ⛩️ | Ritual step |
| 🔱 | Minister invocation |
