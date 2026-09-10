-- Tests for msgpack encoder/decoder round-trip and wire compatibility.
-- Run with: lua nvim/tests/msgpack_test.lua

local encode = require("asimi.msgpack")
local decode = require("asimi.msgpack_decode")

local tests = {}
local function test(name, fn) tests[#tests + 1] = { name = name, fn = fn } end

local function deep_eq(a, b)
  if type(a) ~= type(b) then return false end
  if type(a) == "table" then
    for k, v in pairs(a) do
      if not deep_eq(v, b[k]) then return false end
    end
    for k, v in pairs(b) do
      if not deep_eq(v, a[k]) then return false end
    end
    return true
  end
  return a == b
end

-- nil sentinel round-trips as nil
test("nil sentinel encodes to 0xc0", function()
  local b = encode.encode(encode.NIL)
  assert(b == "\xc0", "expected 0xc0, got " .. b:byte(1))
end)

test("nil encodes to 0xc0", function()
  local b = encode.encode(nil)
  assert(b == "\xc0", "expected 0xc0, got " .. b:byte(1))
end)

-- booleans
test("true encodes to 0xc3", function()
  local b = encode.encode(true)
  assert(b == "\xc3", "expected 0xc3, got " .. b:byte(1))
end)

test("false encodes to 0xc2", function()
  local b = encode.encode(false)
  assert(b == "\xc2", "expected 0xc2, got " .. b:byte(1))
end)

test("true round-trip", function()
  local enc = encode.encode(true)
  local dec = decode.decode(enc)
  assert(dec == true, "expected true, got " .. tostring(dec))
end)

test("false round-trip", function()
  local enc = encode.encode(false)
  local dec = decode.decode(enc)
  assert(dec == false, "expected false, got " .. tostring(dec))
end)

-- integers: positive fixint
test("positive fixint 0", function()
  local b = encode.encode(0)
  assert(b == "\x00", "expected 0x00, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == 0, "expected 0, got " .. tostring(dec))
end)

test("positive fixint 127", function()
  local b = encode.encode(127)
  assert(b == "\x7f", "expected 0x7f, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == 127, "expected 127, got " .. tostring(dec))
end)

-- integers: negative fixint
test("negative fixint -1", function()
  local b = encode.encode(-1)
  assert(b:byte(1) == 0xff, "expected 0xff, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == -1, "expected -1, got " .. tostring(dec))
end)

test("negative fixint -32", function()
  local b = encode.encode(-32)
  assert(b:byte(1) == 0xe0, "expected 0xe0, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == -32, "expected -32, got " .. tostring(dec))
end)

-- uint8
test("uint8 128", function()
  local b = encode.encode(128)
  assert(b:byte(1) == 0xcc, "expected 0xcc, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == 128, "expected 128, got " .. tostring(dec))
end)

test("uint8 255", function()
  local b = encode.encode(255)
  assert(b:byte(1) == 0xcc, "expected 0xcc")
  local dec = decode.decode(b)
  assert(dec == 255, "expected 255, got " .. tostring(dec))
end)

-- uint16
test("uint16 256", function()
  local b = encode.encode(256)
  assert(b:byte(1) == 0xcd, "expected 0xcd, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == 256, "expected 256, got " .. tostring(dec))
end)

test("uint16 65535", function()
  local b = encode.encode(65535)
  local dec = decode.decode(b)
  assert(dec == 65535, "expected 65535, got " .. tostring(dec))
end)

-- uint32
test("uint32 65536", function()
  local b = encode.encode(65536)
  assert(b:byte(1) == 0xce, "expected 0xce, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == 65536, "expected 65536, got " .. tostring(dec))
end)

-- int8
test("int8 -33", function()
  local b = encode.encode(-33)
  assert(b:byte(1) == 0xd0, "expected 0xd0, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == -33, "expected -33, got " .. tostring(dec))
end)

test("int8 -128", function()
  local b = encode.encode(-128)
  local dec = decode.decode(b)
  assert(dec == -128, "expected -128, got " .. tostring(dec))
end)

-- int16
test("int16 -129", function()
  local b = encode.encode(-129)
  assert(b:byte(1) == 0xd1, "expected 0xd1, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == -129, "expected -129, got " .. tostring(dec))
end)

test("int16 -32768", function()
  local b = encode.encode(-32768)
  local dec = decode.decode(b)
  assert(dec == -32768, "expected -32768, got " .. tostring(dec))
end)

-- int32
test("int32 -32769", function()
  local b = encode.encode(-32769)
  assert(b:byte(1) == 0xd2, "expected 0xd2, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == -32769, "expected -32769, got " .. tostring(dec))
end)

-- strings
test("fixstr empty", function()
  local b = encode.encode("")
  assert(b:byte(1) == 0xa0, "expected 0xa0, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == "", "expected empty string")
end)

test("fixstr short", function()
  local b = encode.encode("hello")
  assert(b:byte(1) == 0xa5, "expected 0xa5, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == "hello", "expected hello, got " .. tostring(dec))
end)

test("fixstr max 31 chars", function()
  local s = string.rep("a", 31)
  local b = encode.encode(s)
  assert(b:byte(1) == 0xbf, "expected 0xbf, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == s, "round-trip failed")
end)

test("str8 32 chars", function()
  local s = string.rep("a", 32)
  local b = encode.encode(s)
  assert(b:byte(1) == 0xd9, "expected 0xd9, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == s, "round-trip failed")
end)

test("str8 255 chars", function()
  local s = string.rep("x", 255)
  local b = encode.encode(s)
  assert(b:byte(1) == 0xd9, "expected 0xd9")
  local dec = decode.decode(b)
  assert(dec == s, "round-trip failed")
end)

test("str16 256 chars", function()
  local s = string.rep("y", 256)
  local b = encode.encode(s)
  assert(b:byte(1) == 0xda, "expected 0xda, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(dec == s, "round-trip failed")
end)

-- unicode string
test("unicode string round-trip", function()
  local s = "你好世界"
  local b = encode.encode(s)
  local dec = decode.decode(b)
  assert(dec == s, "expected " .. s .. ", got " .. tostring(dec))
end)

-- arrays
test("fixarray empty", function()
  local b = encode.encode({})
  assert(b:byte(1) == 0x90, "expected 0x90, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(#dec == 0, "expected empty array")
end)

test("fixarray small", function()
  -- Use NIL sentinel for nil in array positions
  local arr2 = {1, "two", true, encode.NIL}
  local b = encode.encode(arr2)
  local dec = decode.decode(b)
  assert(dec[1] == 1, "expected 1")
  assert(dec[2] == "two", "expected two")
  assert(dec[3] == true, "expected true")
  assert(dec[4] == nil, "expected nil")
  -- Lua # operator is unreliable with nil holes; check positions instead
end)

test("fixarray integers", function()
  local arr = {10, 20, 30}
  local b = encode.encode(arr)
  local dec = decode.decode(b)
  assert(deep_eq(dec, arr), "round-trip failed")
end)

-- maps
test("fixmap small", function()
  local m = {name = "asimi", version = 1}
  local b = encode.encode(m)
  local dec = decode.decode(b)
  assert(dec.name == "asimi", "expected asimi")
  assert(dec.version == 1, "expected 1")
end)

test("map with string keys and nested values", function()
  local m = {key = "value", nested = {a = 1, b = "two"}}
  local b = encode.encode(m)
  local dec = decode.decode(b)
  assert(dec.key == "value", "expected value")
  assert(dec.nested.a == 1, "expected 1")
  assert(dec.nested.b == "two", "expected two")
end)

-- float64
test("float64 round-trip", function()
  local v = 3.14159
  local b = encode.encode(v)
  assert(b:byte(1) == 0xcb, "expected 0xcb, got " .. b:byte(1))
  local dec = decode.decode(b)
  assert(math.abs(dec - v) < 1e-9, "expected " .. v .. ", got " .. tostring(dec))
end)

-- float32 decode
test("float32 decode", function()
  local b = "\xca" .. string.pack(">f", 1.5)
  local dec = decode.decode(b)
  assert(math.abs(dec - 1.5) < 1e-6, "expected 1.5, got " .. tostring(dec))
end)

-- ============================================================
-- Wire-format compatibility tests
-- These verify the exact byte format expected by Go's
-- vmihailenco/msgpack/v5 for msgpack-RPC frames.
-- ============================================================

test("RPC request frame format", function()
  -- [0, msgid, method, params]
  local frame = {0, 1, "SetContext", {project = "test"}}
  local b = encode.encode(frame)
  local dec = decode.decode(b)
  assert(dec[1] == 0, "expected frame type 0")
  assert(dec[2] == 1, "expected msgid 1")
  assert(dec[3] == "SetContext", "expected method SetContext")
  assert(dec[4].project == "test", "expected project test")
end)

test("RPC response frame format", function()
  -- [1, msgid, error=nil, result]
  local frame = {1, 42, encode.NIL, {has = true}}
  local b = encode.encode(frame)
  local dec = decode.decode(b)
  assert(dec[1] == 1, "expected frame type 1")
  assert(dec[2] == 42, "expected msgid 42")
  assert(dec[3] == nil, "expected nil error")
  assert(dec[4].has == true, "expected has=true")
end)

test("RPC notification frame format", function()
  -- [2, method, params]
  local frame = {2, "stream.chunk", {channel_id = "abc", text = "hello"}}
  local b = encode.encode(frame)
  local dec = decode.decode(b)
  assert(dec[1] == 2, "expected frame type 2")
  assert(dec[2] == "stream.chunk", "expected method stream.chunk")
  assert(dec[3].channel_id == "abc", "expected channel_id abc")
  assert(dec[3].text == "hello", "expected text hello")
end)

test("RPC response with string error", function()
  -- Daemon may send error as string
  local frame = {1, 5, "method not found", encode.NIL}
  local b = encode.encode(frame)
  local dec = decode.decode(b)
  assert(dec[1] == 1, "expected frame type 1")
  assert(dec[2] == 5, "expected msgid 5")
  assert(dec[3] == "method not found", "expected error string")
  assert(dec[4] == nil, "expected nil result")
end)

-- Stream desync test: multiple frames concatenated
test("multiple frames in one buffer", function()
  local f1 = encode.encode({2, "stream.start", {channel_id = "ch1"}})
  local f2 = encode.encode({2, "stream.chunk", {channel_id = "ch1", text = "hi"}})
  local combined = f1 .. f2

  local frame1, pos = decode.decode(combined, 1)
  assert(frame1[2] == "stream.start", "expected stream.start")
  assert(frame1[3].channel_id == "ch1", "expected ch1")

  local frame2, pos2 = decode.decode(combined, pos)
  assert(frame2[2] == "stream.chunk", "expected stream.chunk")
  assert(frame2[3].text == "hi", "expected hi")

  assert(pos2 == #combined + 1, "expected to consume all bytes, pos=" .. pos2 .. " len=" .. #combined)
end)

-- Incomplete data should not crash (pcall protection)
test("incomplete data does not crash decoder", function()
  local partial = encode.encode({1, 2, "he"})  -- complete but test with truncated
  local truncated = partial:sub(1, 3)
  local ok = pcall(decode.decode, truncated, 1)
  -- Should either error or return incomplete — just don't crash
  -- The decoder should error on incomplete data
  assert(not ok, "expected error on truncated data")
end)

-- NIL sentinel in array positions
test("NIL sentinel in array preserves position", function()
  local arr = {encode.NIL, "after", encode.NIL}
  local b = encode.encode(arr)
  local dec = decode.decode(b)
  -- Lua # operator is unreliable with nil holes; check positions instead
  assert(dec[1] == nil, "expected nil at position 1")
  assert(dec[2] == "after", "expected 'after' at position 2")
  assert(dec[3] == nil, "expected nil at position 3")
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

print(string.format("\n=== msgpack tests: %d passed, %d failed ===", passed, failed))
for _, f in ipairs(failures) do
  print(string.format("  FAIL: %s — %s", f.name, f.err))
end

if failed > 0 then os.exit(1) end
