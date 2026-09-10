-- Tests for notifications.lua dispatch table.
-- Verifies all required notification methods from the RPC protocol
-- are registered with handlers.
-- Run with: lua nvim/tests/notifications_test.lua

-- Stub vim global
vim = vim or {}
vim.env = vim.env or {}
vim.loop = vim.loop or { getuid = function() return 1000 end }
vim.fn = vim.fn or {}
vim.api = vim.api or {}
vim.o = vim.o or {}
vim.defer_fn = vim.defer_fn or function() end
vim.keymap = vim.keymap or { set = function() end }

local notifications = require("asimi.notifications")

local tests = {}
local function test(name, fn) tests[#tests + 1] = { name = name, fn = fn } end

-- Required notification methods for M0+M1
local required_methods = {
  "stream.start",
  "stream.chunk",
  "stream.complete",
  "stream.done",
  "stream.interrupted",
  "stream.max_tokens",
  "stream.error",
  "event",
  "minister.invoking",
  "minister.completed",
  "ritual.step",
  "runner.container_launched",
}

test("handlers table exists", function()
  assert(type(notifications.handlers) == "table", "expected handlers table")
end)

test("all required methods are registered", function()
  for _, method in ipairs(required_methods) do
    assert(notifications.handlers[method] ~= nil,
      "missing handler for method: " .. method)
  end
end)

test("all handlers are functions", function()
  for _, method in ipairs(required_methods) do
    local h = notifications.handlers[method]
    assert(type(h) == "function",
      "expected function for " .. method .. ", got " .. type(h))
  end
end)

test("register_all registers all handlers on rpc object", function()
  local registered = {}
  local mock_rpc = {
    on_notify = function(method, fn)
      registered[method] = fn
    end
  }
  notifications.register_all(mock_rpc)
  for _, method in ipairs(required_methods) do
    assert(registered[method] ~= nil,
      "method not registered via register_all: " .. method)
  end
end)

test("register_all handler matches handler table", function()
  local registered = {}
  local mock_rpc = {
    on_notify = function(method, fn)
      registered[method] = fn
    end
  }
  notifications.register_all(mock_rpc)
  for _, method in ipairs(required_methods) do
    assert(registered[method] == notifications.handlers[method],
      "handler mismatch for " .. method)
  end
end)

-- ============================================================
-- Run tests
-- ============================================================

local passed = 0
local failed = 0
local failures = {}

for _, t in ipairs(tests) do
  local ok, err = pcall(t.fn)
  if ok then
    passed = passed + 1
  else
    failed = failed + 1
    failures[#failures + 1] = { name = t.name, err = err }
  end
end

print(string.format("\n=== notifications tests: %d passed, %d failed ===", passed, failed))
for _, f in ipairs(failures) do
  print(string.format("  FAIL: %s — %s", f.name, f.err))
end

if failed > 0 then os.exit(1) end
