-- Tests for highlights.lua data consistency with TUI chat.go prefixes.
-- Run with: lua nvim/tests/highlights_test.lua

-- Stub vim global
vim = vim or {}
vim.env = vim.env or {}
vim.loop = vim.loop or { getuid = function() return 1000 end }
vim.fn = vim.fn or {}
vim.api = vim.api or {}
vim.o = vim.o or {}

local hl = require("asimi.highlights")

local tests = {}
local function test(name, fn) tests[#tests + 1] = { name = name, fn = fn } end

-- Icon prefix tests — must match TUI chat.go constants exactly.
-- TUI values from chat.go:
--   userPrefix            = "👑  "
--   asimiPrefix           = "🎏  "
--   completeSuccessPrefix = "🐉  "
--   completeFailurePrefix = "🦐  "
--   systemPrefix          = "🛠️  "
--   greetingPrefix        = "🏯  "

test("user icon matches TUI", function()
  assert(hl.icon.user == "👑  ", "expected '👑  ', got '" .. hl.icon.user .. "'")
end)

test("ai icon matches TUI asimiPrefix", function()
  assert(hl.icon.ai == "🎏  ", "expected '🎏  ', got '" .. hl.icon.ai .. "'")
end)

test("success icon matches TUI completeSuccessPrefix", function()
  assert(hl.icon.success == "🐉  ", "expected '🐉  ', got '" .. hl.icon.success .. "'")
end)

test("failure icon matches TUI completeFailurePrefix", function()
  assert(hl.icon.failure == "🦐  ", "expected '🦐  ', got '" .. hl.icon.failure .. "'")
end)

test("system icon matches TUI systemPrefix", function()
  assert(hl.icon.system == "🛠️  ", "expected '🛠️  ', got '" .. hl.icon.system .. "'")
end)

test("greeting icon matches TUI greetingPrefix", function()
  assert(hl.icon.greeting == "🏯  ", "expected '🏯  ', got '" .. hl.icon.greeting .. "'")
end)

test("thinking icon matches TUI reasoning prefix", function()
  -- TUI uses "💭   " (3 spaces) in NewChatMsgBuilder
  assert(hl.icon.thinking == "💭   ", "expected '💭   ', got '" .. hl.icon.thinking .. "'")
end)

-- All message types have icons
test("all types have icons", function()
  for type_name, _ in pairs(hl.types) do
    assert(hl.icon[type_name] ~= nil, "missing icon for type: " .. type_name)
    assert(#hl.icon[type_name] > 0, "empty icon for type: " .. type_name)
  end
end)

-- All message types have hl_group mappings
test("all types have hl_group", function()
  for type_name, _ in pairs(hl.types) do
    assert(hl.hl_group[type_name] ~= nil, "missing hl_group for type: " .. type_name)
    assert(#hl.hl_group[type_name] > 0, "empty hl_group for type: " .. type_name)
  end
end)

-- All hl_groups are prefixed with "Asimi"
test("all hl_groups start with Asimi", function()
  for _, group in pairs(hl.hl_group) do
    assert(group:match("^Asimi"), "expected hl_group to start with 'Asimi', got: " .. group)
  end
end)

-- Types match expected names
test("type names match expected values", function()
  assert(hl.types.user == "user")
  assert(hl.types.ai == "ai")
  assert(hl.types.success == "success")
  assert(hl.types.failure == "failure")
  assert(hl.types.thinking == "thinking")
  assert(hl.types.shell == "shell")
  assert(hl.types.system == "system")
  assert(hl.types.greeting == "greeting")
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

print(string.format("\n=== highlights tests: %d passed, %d failed ===", passed, failed))
for _, f in ipairs(failures) do
  print(string.format("  FAIL: %s — %s", f.name, f.err))
end

if failed > 0 then os.exit(1) end
