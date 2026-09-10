-- Dev init: load the asimi plugin for interactive testing.
--
-- Usage:
--   nvim -u nvim/tests/dev_init.lua
--
-- Starts neovim with the asimi plugin on the runtimepath and setup() called.
-- Autoconnect is disabled so you can manually :AsimiConnect when ready.

-- Add the plugin directory to runtimepath (lua-native, no vimscript)
local root = vim.fn.getcwd()
vim.opt.rtp:prepend(root .. "/nvim")

-- Load and configure the plugin
require("asimi").setup({
  autoconnect = false,
  log_level = "debug",
})
