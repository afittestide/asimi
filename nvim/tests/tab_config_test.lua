-- Tests for tab.lua and config.lua pure-logic functions.
-- Run with: lua nvim/tests/tab_config_test.lua

-- Stub vim global for modules that reference it at require time
vim = vim or {}
vim.env = vim.env or {}
vim.loop = vim.loop or { getuid = function() return 1000 end }
vim.fn = vim.fn or {}
vim.api = vim.api or {}
vim.o = vim.o or {}

local tab = require("asimi.tab")
local config = require("asimi.config")

local tests = {}
local function test(name, fn) tests[#tests + 1] = { name = name, fn = fn } end

-- ============================================================
-- tab.lua tests
-- ============================================================

test("ministers returns 4 ministers", function()
  local mins = tab.ministers()
  assert(#mins == 4, "expected 4 ministers, got " .. #mins)
end)

test("minister_ids returns correct order", function()
  local ids = tab.minister_ids()
  assert(ids[1] == "forge", "expected forge first, got " .. tostring(ids[1]))
  assert(ids[2] == "secretary", "expected secretary second, got " .. tostring(ids[2]))
  assert(ids[3] == "judge", "expected judge third, got " .. tostring(ids[3]))
  assert(ids[4] == "chancellor", "expected chancellor fourth, got " .. tostring(ids[4]))
end)

test("each minister has id and title", function()
  for _, m in ipairs(tab.ministers()) do
    assert(m.id and #m.id > 0, "minister missing id")
    assert(m.title and #m.title > 0, "minister missing title")
  end
end)

test("greeting for forge", function()
  local g = tab.greeting("forge")
  assert(g and #g > 0, "expected non-empty greeting for forge")
  assert(g:find("Forge"), "expected 'Forge' in greeting, got: " .. g)
  assert(g:find("工部"), "expected '工部' in forge greeting")
end)

test("greeting for secretary", function()
  local g = tab.greeting("secretary")
  assert(g and #g > 0, "expected non-empty greeting for secretary")
  assert(g:find("Secretary"), "expected 'Secretary' in greeting")
end)

test("greeting for judge", function()
  local g = tab.greeting("judge")
  assert(g and #g > 0, "expected non-empty greeting for judge")
  assert(g:find("tribunal"), "expected 'tribunal' in judge greeting")
  assert(g:find("Tests"), "expected 'Tests' in judge greeting")
end)

test("greeting for chancellor", function()
  local g = tab.greeting("chancellor")
  assert(g and #g > 0, "expected non-empty greeting for chancellor")
  assert(g:find("Chancellery"), "expected 'Chancellery' in chancellor greeting")
  assert(g:find("封駁"), "expected '封駁' in chancellor greeting")
end)

test("greeting for unknown minister returns empty string", function()
  local g = tab.greeting("nonexistent")
  assert(g == "", "expected empty string for unknown minister, got: " .. tostring(g))
end)

test("all greetings are non-empty", function()
  for _, m in ipairs(tab.ministers()) do
    local g = tab.greeting(m.id)
    assert(g and #g > 0, "expected non-empty greeting for " .. m.id)
  end
end)

-- ============================================================
-- config.lua tests
-- ============================================================

test("config.setup returns config table", function()
  local cfg = config.setup({})
  assert(type(cfg) == "table", "expected table")
  assert(cfg.socket == nil, "expected nil socket by default")
  assert(cfg.autoconnect == true, "expected autoconnect true by default")
  assert(cfg.autostart == true, "expected autostart true by default")
  assert(cfg.log_level == "info", "expected info log level by default")
end)

test("config.setup respects socket override", function()
  local cfg = config.setup({ socket = "/tmp/custom.sock" })
  assert(cfg.socket == "/tmp/custom.sock", "expected custom socket path")
end)

test("config.setup respects autoconnect=false", function()
  local cfg = config.setup({ autoconnect = false })
  assert(cfg.autoconnect == false, "expected autoconnect false")
end)

test("config.setup respects autostart=false", function()
  local cfg = config.setup({ autostart = false })
  assert(cfg.autostart == false, "expected autostart false")
end)

test("config.setup respects log_level override", function()
  local cfg = config.setup({ log_level = "debug" })
  assert(cfg.log_level == "debug", "expected debug log level")
end)

test("config.setup respects api_keys", function()
  local cfg = config.setup({ api_keys = { anthropic = "sk-test" } })
  assert(cfg.api_keys.anthropic == "sk-test", "expected api key")
end)

test("config.get returns current config", function()
  config.setup({ log_level = "warn" })
  local cfg = config.get()
  assert(cfg.log_level == "warn", "expected warn log level")
end)

test("config.log_level returns current level", function()
  config.setup({ log_level = "error" })
  assert(config.log_level() == "error", "expected error log level")
end)

test("config.setup with nil opts uses defaults", function()
  local cfg = config.setup(nil)
  assert(cfg.autoconnect == true, "expected autoconnect true")
  assert(cfg.log_level == "info", "expected info log level")
  assert(cfg.api_keys ~= nil, "expected api_keys table")
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

print(string.format("\n=== tab/config tests: %d passed, %d failed ===", passed, failed))
for _, f in ipairs(failures) do
  print(string.format("  FAIL: %s — %s", f.name, f.err))
end

if failed > 0 then os.exit(1) end
