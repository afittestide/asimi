-- Smoke test: starts neovim headless, loads the asimi plugin, calls setup(),
-- and verifies all :Asimi* commands are registered.
--
-- Run with:
--   nvim --headless --noplugin -u NONE \
--     -c "lua vim.cmd('set rtp+=' .. vim.fn.getcwd() .. '/nvim')" \
--     -c "luafile nvim/tests/smoke_test.lua"
--
-- Or via the wrapper script:
--   ./nvim/tests/run_smoke.sh

local passed = 0
local failed = 0
local failures = {}

local function check(name, fn)
  local ok, err = pcall(fn)
  if ok then
    passed = passed + 1
    print("  ✓ " .. name)
  else
    failed = failed + 1
    failures[#failures + 1] = { name = name, err = err }
    print("  ✗ " .. name .. " — " .. tostring(err))
  end
end

print("=== asimi.nvim smoke test ===")
print("")

-- ── 1. Plugin loads without error ──────────────────────────────────
print("[1] Plugin loads")

local asimi_ok, asimi_err
check("require('asimi') succeeds", function()
  asimi_ok, asimi_err = pcall(require, "asimi")
  assert(asimi_ok, "require('asimi') failed: " .. tostring(asimi_err))
end)

check("module has version", function()
  assert(type(asimi_ok) == "boolean" and asimi_ok, "asimi not loaded")
  local asimi = require("asimi")
  assert(type(asimi.version) == "string", "version is not a string")
  assert(#asimi.version > 0, "version is empty")
  print("    version = " .. asimi.version)
end)

-- ── 2. setup() runs cleanly ─────────────────────────────────────────
print("[2] setup()")

check("setup({autoconnect=false}) completes", function()
  local asimi = require("asimi")
  asimi.setup({ autoconnect = false })
end)

check("setup() is idempotent (calling twice)", function()
  local asimi = require("asimi")
  asimi.setup({ autoconnect = false })
end)

-- ── 3. All submodules load ──────────────────────────────────────────
print("[3] Submodules")

check("require('asimi.config') loads", function()
  local config = require("asimi.config")
  assert(type(config.get) == "function")
end)

check("require('asimi.rpc') loads", function()
  local rpc = require("asimi.rpc")
  assert(type(rpc.connect) == "function")
  assert(type(rpc.connected) == "function")
end)

check("require('asimi.log') loads", function()
  local log = require("asimi.log")
  assert(type(log.info) == "function")
end)

check("require('asimi.highlights') loads", function()
  local hl = require("asimi.highlights")
  assert(type(hl.icon) == "table")
  assert(type(hl.hl_group) == "table")
end)

check("require('asimi.chat') loads", function()
  local chat = require("asimi.chat")
  assert(type(chat.create_buffer) == "function")
end)

check("require('asimi.tab') loads", function()
  local tab = require("asimi.tab")
  assert(type(tab.ministers) == "function")
  assert(#tab.ministers() == 4, "expected 4 ministers")
end)

check("require('asimi.commands') loads", function()
  local commands = require("asimi.commands")
  assert(type(commands.setup) == "function")
end)

check("require('asimi.notifications') loads", function()
  local notifications = require("asimi.notifications")
  assert(type(notifications.register_all) == "function")
  assert(notifications.handlers["stream.chunk"] ~= nil, "stream.chunk handler missing")
end)

-- ── 4. All :Asimi* commands are registered ──────────────────────────
print("[4] Commands")

local expected_commands = {
  "AsimiConnect",
  "AsimiDisconnect",
  "AsimiHealth",
  "AsimiTab",
  "AsimiCancel",
}

for _, cmd in ipairs(expected_commands) do
  check(":" .. cmd .. " is registered", function()
    local exists = vim.fn.exists(":" .. cmd)
    assert(exists == 2, ":" .. cmd .. " not found (exists() = " .. tostring(exists) .. ")")
  end)
end

-- ── 5. Public API exports ───────────────────────────────────────────
print("[5] Public API")

check("asimi.connect is a function", function()
  local asimi = require("asimi")
  assert(type(asimi.connect) == "function")
end)

check("asimi.disconnect is a function", function()
  local asimi = require("asimi")
  assert(type(asimi.disconnect) == "function")
end)

check("asimi.health is a function", function()
  local asimi = require("asimi")
  assert(type(asimi.health) == "function")
end)

-- ── 6. RPC state is disconnected (no daemon) ────────────────────────
print("[6] RPC state")

check("rpc.connected() returns false (no daemon)", function()
  local rpc = require("asimi.rpc")
  assert(rpc.connected() == false, "expected connected() == false")
  assert(rpc.state() == "disconnected", "expected state == 'disconnected', got: " .. tostring(rpc.state()))
end)

-- ── 7. AsimiConnect command can be invoked ─────────────────────────
print("[7] AsimiConnect")

check(":AsimiConnect runs without error (no-op when no daemon)", function()
  -- We setup with autoconnect=false to keep state clean for this test.
  -- Calling :AsimiConnect should attempt to connect. Since there's no
  -- daemon and the default config has autostart=true, it will try to
  -- spawn one. In a headless test environment without the asimi binary
  -- the autostart will fail gracefully, leaving state as "disconnected".
  -- That's fine — we just verify the Lua command dispatches cleanly.
  local rpc = require("asimi.rpc")
  rpc.disconnect() -- ensure we start disconnected

  vim.cmd("AsimiConnect")
  -- After the command, the state should at least be clean (no crash).
  -- It may be "connecting" briefly but we validate no error was thrown.
  local ok = pcall(rpc.state)
  assert(ok, "rpc.state() panicked after AsimiConnect")
end)

-- ── 8. Chat buffer can be created ──────────────────────────────────
print("[8] Chat buffer")

check("chat.create_buffer('forge') returns a valid buffer", function()
  local chat = require("asimi.chat")
  local buf = chat.create_buffer("forge")
  assert(type(buf) == "number", "expected buffer number, got " .. type(buf))
  assert(vim.api.nvim_buf_is_valid(buf), "buffer is not valid")
  local bt = vim.api.nvim_buf_get_option(buf, "buftype")
  assert(bt == "prompt", "expected buftype=prompt, got: " .. tostring(bt))
  local name = vim.api.nvim_buf_get_name(buf)
  assert(name == "asimi://chat/forge", "expected buffer name 'asimi://chat/forge', got: " .. name)
end)

-- ── Summary ─────────────────────────────────────────────────────────
print("")
print(string.format("=== smoke test: %d passed, %d failed ===", passed, failed))
if failed > 0 then
  for _, f in ipairs(failures) do
    print("  FAIL: " .. f.name .. " — " .. f.err)
  end
  os.exit(1)
end
print("All smoke tests passed ✓")
