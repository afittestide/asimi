#!/bin/bash
# Smoke test: starts neovim headless, loads the asimi plugin, and verifies
# all :Asimi* commands are registered and the plugin sets up without errors.
#
# Usage: ./nvim/tests/run_smoke.sh

set -euo pipefail

cd "$(dirname "$0")/../.."  # project root

nvim --headless --noplugin -u NONE \
  -c "lua vim.cmd('set rtp+=' .. vim.fn.getcwd() .. '/nvim')" \
  -c "luafile nvim/tests/smoke_test.lua"
