-- Entry point for the Asimi Neovim plugin.
-- Auto-loads when the plugin directory is in runtimepath.
-- The user calls require("asimi").setup() from their init.lua.

if vim.g.asimi_loaded then return end
vim.g.asimi_loaded = 1

-- Re-export the module for convenience
vim.g.asimi = require("asimi")
