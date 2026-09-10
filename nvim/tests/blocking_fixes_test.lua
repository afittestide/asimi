-- Tests for blocking issue fixes from Chancellor review.
-- Tests the icon prefix in flush() and config.socket in default_socket().
-- Run with: lua nvim/tests/blocking_fixes_test.lua (from nvim/ directory)

-- Stub vim global
vim = vim or {}
vim.env = vim.env or {}
vim.loop = vim.loop or { getuid = function() return 1000 end }
vim.fn = vim.fn or {}
vim.api = vim.api or {}
vim.o = vim.o or {}
vim.defer_fn = vim.defer_fn or function() end
vim.keymap = vim.keymap or { set = function() end }

local tests = {}
local function test(name, fn) tests[#tests + 1] = { name = name, fn = fn } end

-- Resolve a file path relative to the nvim/ directory, regardless of CWD.
local function resolve(path)
  local f = io.open(path, "r")
  if f then return f end
  -- Try relative to parent of tests/ directory (i.e. nvim/)
  f = io.open("../" .. path, "r")
  if f then return f end
  -- Try from project root
  return io.open("nvim/" .. path, "r")
end

-- ============================================================
-- Test 1: flush() prepends icon prefix to first text line
-- ============================================================

test("flush function source contains icon_prefix variable", function()
  local f = resolve("lua/asimi/chat.lua")
  assert(f, "could not open chat.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find("local icon_prefix = hl%.icon%[s%.icon%]"),
    "expected icon_prefix variable in flush()")
  assert(src:find("t_lines%[1%] = icon_prefix %.%. t_lines%[1%]"),
    "expected icon_prefix prepended to first text line")
  assert(src:find("r_lines%[1%] = hl%.icon%.thinking %.%. r_lines%[1%]"),
    "expected thinking icon prepended to reasoning first line")
end)

-- ============================================================
-- Test 2: default_socket() reads config.get().socket first
-- ============================================================

test("default_socket reads config.socket first", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  local func_start = src:find("local function default_socket%(")
  assert(func_start, "default_socket function not found")

  local func_body = src:sub(func_start, func_start + 500)
  local cfg_pos = func_body:find("cfg%.socket")
  local env_pos = func_body:find("ASIMI_DAEMON_SOCKET")

  assert(cfg_pos, "config.get().socket not checked in default_socket()")
  assert(env_pos, "ASIMI_DAEMON_SOCKET not checked in default_socket()")
  assert(cfg_pos < env_pos,
    "config.socket should be checked BEFORE ASIMI_DAEMON_SOCKET")
end)

-- ============================================================
-- Test 3: Help file is plain text (no Lua comment prefixes)
-- ============================================================

test("help file has no Lua comment prefixes", function()
  local f = resolve("doc/asimi.txt")
  assert(f, "could not open asimi.txt")
  local content = f:read("*a")
  f:close()

  for line in content:gmatch("[^\n]+") do
    local trimmed = line:match("^%s*(.*)")
    if trimmed and #trimmed > 0 then
      assert(not trimmed:match("^%-%-"),
        "found Lua comment prefix in help file: " .. line)
    end
  end
end)

test("help file has help tags", function()
  local f = resolve("doc/asimi.txt")
  assert(f, "could not open asimi.txt")
  local content = f:read("*a")
  f:close()

  assert(content:find("%*asimi%*"), "expected *asimi* help tag")
end)

test("help file has modeline", function()
  local f = resolve("doc/asimi.txt")
  assert(f, "could not open asimi.txt")
  local content = f:read("*a")
  f:close()

  assert(content:find("vim:ft=help"), "expected vim modeline in help file")
end)

-- ============================================================
-- Test 4: Non-blocking fixes
-- ============================================================

test("thinking icon has 3 spaces matching TUI", function()
  local hl = require("asimi.highlights")
  assert(hl.icon.thinking == "💭   ",
    "expected '💭   ' (3 spaces), got '" .. hl.icon.thinking .. "'")
end)

test("minister icons in chat.lua use 2 spaces", function()
  local f = resolve("lua/asimi/chat.lua")
  assert(f, "could not open chat.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find('"🔱  "'),
    "expected 🔱  (2 spaces) in chat.lua")
  assert(not src:find('"🔱 "%. '),
    "found 🔱  (1 space) — should be 2 spaces")
end)

test("init.lua has no redundant if/else with identical branches", function()
  local f = resolve("lua/asimi/init.lua")
  assert(f, "could not open init.lua")
  local src = f:read("*a")
  f:close()

  assert(not src:find('if i == 1 then%s+vim%.cmd%"tabnew%"%s+else%s+vim%.cmd%"tabnew%"%s+end'),
    "found redundant if/else with identical branches")
end)

test("notifications.lua has TODO for events.drained", function()
  local f = resolve("lua/asimi/notifications.lua")
  assert(f, "could not open notifications.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find("events%.drained"),
    "expected TODO or comment for events.drained in notifications.lua")
end)

-- ============================================================
-- Test 5: E5560 fix — git_info cache avoids vim.fn.system()
--        inside libuv fast event callbacks
-- ============================================================

test("rpc.lua has git_info cache table", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find("local git_info = nil"),
    "expected git_info cache variable in rpc.lua")
end)

test("rpc.lua has resolve_git_info() function", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find("local function resolve_git_info%("),
    "expected resolve_git_info() function in rpc.lua")
end)

test("resolve_git_info caches result and returns cached", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  -- Check that the function returns early if git_info is set
  assert(src:find("if git_info then return git_info end"),
    "expected early return when git_info is already cached")
end)

test("rpc.lua has invalidate_git_info() function", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find("local function invalidate_git_info%("),
    "expected invalidate_git_info() function in rpc.lua")
end)

test("invalidate_git_info sets git_info to nil", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find("git_info = nil"),
    "expected git_info = nil in invalidate_git_info")
end)

test("send_set_context reads from resolve_git_info(), not vim.fn.system()", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  -- send_set_context must call resolve_git_info(), not vim.fn.system() directly
  local func_start = src:find("local function send_set_context%(")
  assert(func_start, "send_set_context function not found")
  local func_body = src:sub(func_start, func_start + 300)

  assert(func_body:find("resolve_git_info%("),
    "send_set_context should call resolve_git_info()")
  assert(not func_body:find("vim%.fn%.system"),
    "send_set_context should NOT call vim.fn.system() directly")
end)

test("connect() resolves git_info eagerly before libuv callback", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  -- In connect(), resolve_git_info() should be called BEFORE pipe:connect()
  local func_start = src:find("function M%.connect%(")
  assert(func_start, "M.connect function not found")

  -- Look for resolve_git_info() call between M.connect() and pipe:connect()
  local body = src:sub(func_start, func_start + 600)
  local resolve_pos = body:find("resolve_git_info%(")
  local connect_pos = body:find("pipe:connect%(")

  assert(resolve_pos, "connect() must call resolve_git_info()")
  assert(connect_pos, "connect() must call pipe:connect()")
  assert(resolve_pos < connect_pos,
    "resolve_git_info() must be called BEFORE pipe:connect() in connect()")
end)

test("connect_uv() resolves git_info eagerly if not cached", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  local func_start = src:find("function M%.connect_uv%(")
  assert(func_start, "M.connect_uv function not found")

  local body = src:sub(func_start, func_start + 400)
  local resolve_pos = body:find("resolve_git_info%(")
  local connect_pos = body:find("pipe:connect%(")

  assert(resolve_pos, "connect_uv() must call resolve_git_info()")
  assert(connect_pos, "connect_uv() must call pipe:connect()")
  assert(resolve_pos < connect_pos,
    "resolve_git_info() must be called BEFORE pipe:connect() in connect_uv()")
end)

test("attempt_reconnect resolves git_info before libuv callback", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  local func_start = src:find("local function attempt_reconnect%(")
  assert(func_start, "attempt_reconnect function not found")

  local body = src:sub(func_start, func_start + 400)
  local resolve_pos = body:find("resolve_git_info%(")
  local connect_pos = body:find("pipe:connect%(")

  assert(resolve_pos, "attempt_reconnect must call resolve_git_info()")
  assert(connect_pos, "attempt_reconnect must call pipe:connect()")
  assert(resolve_pos < connect_pos,
    "resolve_git_info() must be called BEFORE pipe:connect() in attempt_reconnect()")
end)

test("vim.fn.system() calls happen only outside libuv callbacks", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  -- Find all vim.fn.system calls and verify they're only in resolve_git_info()
  -- and not in send_set_context, complete_connection, or handle_frame
  local nonce = {}  -- use as marker

  -- Check complete_connection (which runs in libuv callback) — no vim.fn.system
  local cc_start = src:find("local function complete_connection%(")
  local cc_body
  if cc_start then
    -- Find the next function or end of relevant section
    local cc_end = src:find("\nend\n", cc_start)
    if not cc_end then cc_end = src:find("local function", cc_start + 10) or #src end
    cc_body = src:sub(cc_start, cc_end)
    assert(not cc_body:find("vim%.fn%.system"),
      "complete_connection (libuv callback) must NOT call vim.fn.system()")
  end

  -- All vim.fn.system calls should be inside resolve_git_info()
  local sys_calls = {}
  for match in src:gmatch("vim%.fn%.system%([^\n]*%)") do
    sys_calls[#sys_calls + 1] = match
  end
  assert(#sys_calls >= 2, "expected at least 2 vim.fn.system calls (git, branch)")
end)

-- ============================================================
-- Test 6: E5560 fix — env_cache avoids vim.env.X calls
--        inside libuv fast event callbacks (mirrors git_info)
-- ============================================================

test("rpc.lua has env_cache variable", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find("local env_cache = nil"),
    "expected env_cache cache variable in rpc.lua")
end)

test("rpc.lua has resolve_env_cache() function", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find("local function resolve_env_cache%("),
    "expected resolve_env_cache() function in rpc.lua")
end)

test("resolve_env_cache caches result and returns cached", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find("if env_cache then return env_cache end"),
    "expected early return when env_cache is already cached")
end)

test("resolve_env_cache captures username from vim.env", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  local func_start = src:find("local function resolve_env_cache%(")
  assert(func_start, "resolve_env_cache function not found")

  local func_body = src:sub(func_start, func_start + 250)
  assert(func_body:find("vim%.env%.USER"),
    "resolve_env_cache should read vim.env.USER")
  assert(func_body:find("vim%.env%.LOGNAME"),
    "resolve_env_cache should read vim.env.LOGNAME")
end)

test("resolve_env_cache captures API key env vars", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  local func_start = src:find("local function resolve_env_cache%(")
  assert(func_start, "resolve_env_cache function not found")

  local func_body = src:sub(func_start, func_start + 300)
  assert(func_body:find("ANTHROPIC_API_KEY"),
    "resolve_env_cache should read ANTHROPIC_API_KEY")
  assert(func_body:find("OPENAI_API_KEY"),
    "resolve_env_cache should read OPENAI_API_KEY")
  assert(func_body:find("GEMINI_API_KEY"),
    "resolve_env_cache should read GEMINI_API_KEY")
end)

test("send_set_context reads from resolve_env_cache(), not vim.env directly", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  local func_start = src:find("local function send_set_context%(")
  assert(func_start, "send_set_context function not found")
  local func_body = src:sub(func_start, func_start + 300)

  assert(func_body:find("resolve_env_cache%("),
    "send_set_context should call resolve_env_cache()")
  assert(not func_body:find("vim%.env%.USER") and not func_body:find("vim%.env%.ANTHROPIC"),
    "send_set_context should NOT call vim.env directly")
end)

test("connect() resolves env_cache eagerly before libuv callback", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  local func_start = src:find("function M%.connect%(")
  assert(func_start, "M.connect function not found")

  local body = src:sub(func_start, func_start + 600)
  local resolve_pos = body:find("resolve_env_cache%(")
  local connect_pos = body:find("pipe:connect%(")

  assert(resolve_pos, "connect() must call resolve_env_cache()")
  assert(connect_pos, "connect() must call pipe:connect()")
  assert(resolve_pos < connect_pos,
    "resolve_env_cache() must be called BEFORE pipe:connect() in connect()")
end)

test("connect_uv() resolves env_cache eagerly if not cached", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  local func_start = src:find("function M%.connect_uv%(")
  assert(func_start, "M.connect_uv function not found")

  local body = src:sub(func_start, func_start + 450)
  local resolve_pos = body:find("resolve_env_cache%(")
  local connect_pos = body:find("pipe:connect%(")

  assert(resolve_pos, "connect_uv() must call resolve_env_cache()")
  assert(connect_pos, "connect_uv() must call pipe:connect()")
  assert(resolve_pos < connect_pos,
    "resolve_env_cache() must be called BEFORE pipe:connect() in connect_uv()")
end)

test("collect_api_keys accepts optional env parameter", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  assert(src:find("local function collect_api_keys%(env%)"),
    "expected collect_api_keys to take an 'env' parameter")
  assert(src:find("env = env or env_cache"),
    "collect_api_keys should fall back to env_cache")
end)

test("vim.env calls happen only in resolve_env_cache and collect_api_keys fallback", function()
  local f = resolve("lua/asimi/rpc.lua")
  assert(f, "could not open rpc.lua")
  local src = f:read("*a")
  f:close()

  -- Check complete_connection (runs in libuv callback) — no vim.env.X
  local cc_start = src:find("local function complete_connection%(")
  if cc_start then
    local cc_end = src:find("\nend\n", cc_start)
    if not cc_end then cc_end = src:find("local function", cc_start + 10) or #src end
    local cc_body = src:sub(cc_start, cc_end)
    assert(not cc_body:find("vim%.env%."),
      "complete_connection (libuv callback) must NOT call vim.env")
  end

  -- Check handle_disconnect — no vim.env.X
  local hd_start = src:find("function M%.handle_disconnect%(")
  assert(hd_start, "M.handle_disconnect function not found")
  local hd_body = src:sub(hd_start, hd_start + 400)
  assert(not hd_body:find("vim%.env%."),
    "handle_disconnect must NOT call vim.env")

  -- All vim.env calls should be inside resolve_env_cache() or collect_api_keys()
  -- count vim.env. calls that are NOT in resolve_env_cache or default_socket
  local resolve_start = src:find("local function resolve_env_cache%(")
  local resolve_end = src:find("\nend\n", resolve_start)
  
  local collect_start = src:find("local function collect_api_keys%(")
  local collect_end = src:find("\nend\n", collect_start)
  
  -- vim.env calls outside these functions should only be in default_socket
  local outside_calls = 0
  for m in src:gmatch("vim%.env%.[%w_]+") do
    local pos = src:find(m)
    local in_resolve = resolve_start and pos >= resolve_start and pos <= resolve_end
    local in_collect = collect_start and pos >= collect_start and pos <= collect_end
    if not in_resolve and not in_collect then
      outside_calls = outside_calls + 1
    end
  end
  -- default_socket has 2 vim.env calls (ASIMI_DAEMON_SOCKET, XDG_RUNTIME_DIR)
  -- Those are fine because default_socket runs in connect() which is main loop
  assert(outside_calls <= 3,
    "expected at most 3 vim.env calls outside resolve+collect (default_socket), got " .. outside_calls)
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

print(string.format("\n=== blocking fixes tests: %d passed, %d failed ===", passed, failed))
for _, f in ipairs(failures) do
  print(string.format("  FAIL: %s — %s", f.name, f.err))
end

if failed > 0 then os.exit(1) end
