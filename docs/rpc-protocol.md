# Court RPC Protocol

This is the wire protocol between the `asimi` TUI and the `asimi daemon`
when the two run as separate processes. It is **standard msgpack-RPC**
compatible with Neovim's built-in `rpcrequest` / `rpcnotify` and any
other msgpack-RPC client (Python, etc.). No JSON-RPC, no gRPC, no
generated stubs. Everything you need to read, extend, or re-implement
the protocol is in one directory: `internal/wire` (frame + codec) and
`internal/rpc` (dispatch).

## 1. Transport

**Wire format**: a stream of [MessagePack](https://msgpack.org/)
arrays. Each value on the wire is exactly one msgpack-RPC envelope —
a 3- or 4-element array. The stream is decoded with
[`vmihailenco/msgpack/v5`](https://github.com/vmihailenco/msgpack).

* No length prefix, no framing headers. Just back-to-back msgpack
  values.
* A single value larger than **16 MiB** (`wire.MaxFrameSize`) is
  rejected with `wire.ErrFrameTooLarge`. Peers should close the
  connection.

**Physical transport**: a single unix domain socket. The default path
(`rpc.SocketPath`) prefers `$XDG_RUNTIME_DIR/asimi.sock` on Linux,
falling back to `/tmp/asimi-$UID.sock` on macOS, honouring the 104-byte
`sun_path` cap.

Socket permissions: directory `0700`, socket `0600` — user-scoped v1.
(Multi-user daemon deployments would change these; see § 7.)

## 2. Frame envelope

Standard msgpack-RPC array envelopes:

| Kind         | Array                                    |
| ------------ | ---------------------------------------- |
| Request      | `[0, msgid, method, params]`               |
| Response     | `[1, msgid, error, result]`              |
| Notification | `[2, method, params]`                    |

Where:

* `msgid` (`uint64`) correlates a request with its response.
* `method` is the Go method name string (e.g. `"SubmitPrompt"`).
* `params` is a single msgpack value (usually a tagged struct or map).
  It is carried as raw bytes so the dispatcher can route to the right
  handler before the handler decodes its typed payload.
* `error` is `nil` on success, or an `*wire.Error` struct on failure.
  Simple string errors are accepted from third-party clients and are
  normalised to `code=0`.
* `result` is the raw msgpack return value, present only when `error`
  is `nil`.

```go
type Error struct {
    Code    int32  `msgpack:"c"`
    Message string `msgpack:"m"`
}
```

**The envelope is symmetric** — either peer may issue a request. The
daemon uses this to ask the TUI for host-command approval; the TUI uses
it for everything else. Every side-specific convention lives in the
method catalog, not in the transport.

### Error codes (transport)

`internal/wire` defines a small handler-agnostic set; application-level
errors use codes above 1000 to avoid collision.

| Code | Name                        | Meaning                            |
| ---- | --------------------------- | ---------------------------------- |
| 0    | *(unset)*                   | non-wire error; look at `Message`  |
| 1    | `CodeUnknownMethod`         | peer sent a method we don't serve  |
| 2    | `CodePeerDisconnected`      | connection closed mid-call         |
| 3    | `CodeFrameTooLarge`         | value exceeded 16 MiB              |
| 4    | `CodeDecodeFailed`          | params didn't unmarshal            |
| 5    | `CodeNotReady`              | reserved for handshake futures     |

## 3. Connection lifecycle

1. **Dial** — client dials the socket with `net.Dial("unix", path)`,
   wraps the conn in `rpc.New(conn, opts)`.
2. **Serve** — both peers call `conn.Serve()` in a goroutine. Serve
   starts three inner goroutines: reader, writer, and a notification
   dispatcher (notifications handlers run serially in arrival order; a
   slow handler blocks subsequent notifications on that conn).
3. **Register handlers** before Serve is called:
   `conn.Handle(method, fn)` for request handlers;
   `conn.HandleNotify(method, fn)` for one-way messages.
4. **Call** — `Call(ctx, method, params)` blocks until a response frame
   arrives or `ctx` cancels. Returns `ErrPeerDisconnected` if the conn
   closes mid-call. The raw result is unmarshalled by the caller.
5. **Notify** — `Notify(method, params)` enqueues a fire-and-forget
   frame. Backpressure: the outbound chan is bounded (`WriteBuffer`,
   default 256); oversubscription drops with a warning log. Tune via
   `rpc.Options{WriteBuffer: N}`.
6. **Close** — `Close()` is idempotent. Any in-flight Call on that conn
   fails with `ErrPeerDisconnected`.

There is **no dedicated handshake frame**. Instead, the TUI calls
`SetContext` immediately after the connection is established, sending
project metadata, API keys, and OAuth tokens to the daemon. The daemon
uses this to initialise (or re-initialise) its Bifrost LLM client.
`SetContext` is **idempotent** — it can be called at any time to push
runtime configuration updates (model switch, OAuth token refresh, etc.)
without reconnecting. Protocol versioning is a planned addition;
currently both peers assume "same build" compatibility.

## 4. Method catalog — TUI → daemon

Every method name is a Go constant in
`internal/rpc/court_methods.go`. Params/results are Go structs in
`internal/rpc/court_types.go`. Most are 1:1 with
`courtapi.Client` methods.

| Method                     | Params                                              | Result                          |
| -------------------------- | --------------------------------------------------- | ------------------------------- |
| `HasMinister`              | `{id}`                                              | `{has bool}`                    |
| `ResetMinisterSession`     | `{id}`                                              | —                               |
| `EdictKey`                 | `{edict_id uint}`                                   | `{key EdictKey}`                |
| `CourtEdictKey`            | —                                                   | `{key EdictKey}`                |
| `CreateEdict`              | `{issue_ref, intent}`                               | `{edict *Edict}`                |
| `CreateEdictSilent`        | `{issue_ref, intent}`                               | `{edict *Edict}`                |
| `GetEdict`                 | `{edict_id uint}`                                   | `{edict *Edict}`                |
| `ListActiveEdicts`         | —                                                   | `{edicts []ActiveEdict}`        |
| `GrantRulerSeal`           | `{edict_id uint, notes}`                            | —                               |
| `GetEdictSeals`            | `{key EdictKey}`                                    | `{seals []Seal}`                |
| `PublishEvent`             | `{key EdictKey, event_type, payload JSON}`          | `{event_id uint}`               |
| `SubmitPrompt`             | `{target_id, message, edict_key, channel_id, context_files}` | —                      |
| `RestoreMinisterSession`   | `{tab_type, messages []ChatMessage, channel_id?}`   | —                               |
| `HandleZhengmingResponse`  | `{request_id, answer}`                              | —                               |
| `CancelZhengming`          | `{request_id}`                                      | —                               |
| `AllowRunnerFallback`      | `{allow bool}`                                      | —                               |
| `RunShellCommand`          | `{input runners.Input}`                             | `{output runners.Output}`       |
| `SessionState`             | `{tab_target}`                                      | `{state SessionState}`          |
| `AddSessionContextFile`    | `{tab_target, path, content}`                       | —                               |
| `AddSessionMessage`        | `{tab_target, role, content}`                       | —                               |
| `ClearSessionHistory`      | `{tab_target}`                                      | —                               |
| `RollbackSession`          | `{tab_target, snapshot int}`                        | —                               |
| `CompactSession`           | `{tab_target, prompt}`                              | `{summary}`                     |
| `GetSessionExport`         | `{tab_target}`                                      | `{export *SessionExport}`       |
| `TakeSnapshot`             | —                                                   | `{snapshot Snapshot}`           |
| `CancelTab`                | `{channel_id}`                                      | —                               |
| `SetContext`               | `{project, username, project_root, worktree_path, branch, api_keys?, auth_token?, refresh_token?}` | — |

The `court.Prompt` type has a `context.Context` field that doesn't
cross the wire. `SubmitPromptParams` carries the same fields minus
`Ctx`; the server rebuilds `ctx` from the handler's own ctx.

**`SetContext` params in detail** — the `api_keys` field is a
`map[string]string` keyed by provider name (e.g. `"anthropic"`,
`"openai"`, `"google"`). It is optional (`omitempty`); when omitted or
empty the daemon falls back to environment variables or its own
credential store. Each call **replaces** the entire key set — callers
must send the full map on every invocation, not a delta. The
`auth_token` and `refresh_token` fields carry OAuth credentials for
providers that use browser-based auth; they follow the same
replace semantics.

## 5. Notification catalog — daemon → TUI

Fire-and-forget messages keyed on method name. The type registry lives
in `internal/rpc/notifications.go`.

| Method                      | Payload shape                                                                    |
| --------------------------- | -------------------------------------------------------------------------------- |
| `stream.start`              | `{channel_id, edict_id}`                                                         |
| `stream.chunk`              | `{channel_id, text, reasoning?, percent_context_used?, ling_id?, minister_id?}`  |
| `stream.reasoning`          | `{channel_id, text}`                                                             |
| `stream.complete`           | `{channel_id, ling_id?}`                                                         |
| `stream.done`               | `{channel_id}`                                                                   |
| `stream.interrupted`        | `{channel_id, partial_content, ling_id?}`                                        |
| `stream.max_tokens`         | `{channel_id, content, ling_id?}`                                                |
| `stream.context_overflow`   | `{channel_id, window, ling_id?}`                                                 |
| `stream.error`              | `{channel_id, err string, ling_id?}` — `error` value is serialised as a string   |
| `events.drained`            | `{channel_id, events []DrainedEvent}` — crash-recovery replay                    |
| `minister.invoking`         | `{channel_id, minister_id, edict_key, task}`                                     |
| `minister.completed`        | `{channel_id, minister_id, edict_key, output, sealed, err string}`               |
| `event`                     | `{channel_id, event_type, edict_key, message, payload}`                          |
| `zhengming.pending`         | `{request_id, edict_key, minister_id, questions, priority}`                      |
| `zhengming.answered`        | `{request_id, answer}`                                                           |
| `ritual.step`               | `{channel_id, ritual_name, execution_id, edict_id, step_name, step_index, total_steps, status, message}` |
| `runner.container_launched` | `{message, container_id}`                                                        |

**Ling attribution rider.** `ling_id` / `minister_id` are set on the stream
notifications of a *ling* session (an edict's work item, run under a minister).
Several lings of one edict stream into the same `e<N>` channel, so these fields
are what let the TUI tell their output apart. They are `omitempty` and empty for
every other stream (interactive minister tabs, ritual steps, single-ling
edicts), which keeps old decoders working and unattributed rendering unchanged.
`stream.chunk` is the only notification that carries `minister_id`; the terminal
notifications carry `ling_id` alone.

**Delivery guarantees**: in arrival order per connection (notifications
run on a single dispatcher goroutine). Drop on slow consumer: if the
outbound chan is full, the writer logs `notification queue full,
dropping` and discards the frame. No replay on reconnect in v1.

## 6. Bidirectional: the approval round-trip

Host-command approval is the only daemon-initiated request today. It
uses the same `Call` / `Handle` machinery as the rest:

```
Daemon                                         TUI
------                                         ---
                                               RegisterApprovalHandler(conn, program)
...user submits prompt...
shell tool wants to run `rm file`
↓
rpc.RequestApproval(ctx, conn, "rm file")
  → Call(ctx, "tui.request_approval", {Command: "rm file"})
                                               Handler fires
                                               Sends runners.ApprovalRequestMsg
                                                 {Command, ResponseChan} to *tea.Program
                                               Renders "Allow rm file? [y/N]"
                                               User presses 'y'
                                               TUI writes true to ResponseChan
                                               Handler returns {Approved: true}
  ← Response {Approved: true}
Runner proceeds
```

Safety net: if the TUI dies mid-approval, the daemon's `Call` fails
with `ErrPeerDisconnected` and the tool call errors cleanly instead of
hanging forever. A live-but-stuck TUI is bounded by the ctx the daemon
passes to `RequestApproval` — wrap it in `context.WithTimeout` at the
call site for a hard ceiling.

## 7. Neovim compatibility

Because the protocol is standard msgpack-RPC, a Neovim Lua script can
talk to the daemon directly over the unix socket:

```lua
local chan = vim.fn.sockconnect('unix', '/tmp/asimi-501.sock', {rpc = true})
local ok, err = pcall(vim.fn.rpcrequest, chan, 'HasMinister', {id = 'chancellor'})
```

Notes for Neovim callers:

* The socket path defaults to `rpc.SocketPath()` — use
  `$XDG_RUNTIME_DIR/asimi.sock` on Linux or `/tmp/asimi-$UID.sock` on
  macOS.
* `rpcrequest` returns the `result` slot when `error` is `nil`. If the
  server returns an `*wire.Error`, Neovim surfaces it as a Lua error.
* `rpcnotify` sends a notification envelope (`[2, method, params]`).
* Params and results are plain msgpack maps — tagged Go structs and Lua
tables are fully compatible.

## 8. Environment variables

The TUI picks its transport mode from env vars; precedence in
`runInteractiveMode`. The default is now autostart daemon:

| Var                    | Value    | Effect                                                                  |
| ---------------------- | -------- | ----------------------------------------------------------------------- |
| `ASIMI_DAEMON_SOCKET`  | `/path`  | Connect to this specific live daemon. Fails fast if socket is dead.     |
| `ASIMI_LOOPBACK`       | `1`      | In-process net.Pipe loopback — every call through the codec, no socket. Testing escape-hatch. |
| *(none)*               | —        | Autostart daemon: dial default socket, spawn `asimi daemon` on miss, wait ≤10s. |

The daemon subprocess honours **`ASIMI_READY_FD`** (set by the TUI
autostart path): an fd number the daemon writes `0x01` to once its
listener is bound. Parents block on reading that byte to avoid socket-
polling races.

The autostart timeout is `autostartReadyTimeout = 10s` (in
`autostart.go`). Raise it if you're seeing spurious timeouts on cold
macOS machines with `podman machine start`.

## 9. Cookbook: add a new method

Wire a new `Client` method in five files:

1. **Interface** — add the method signature to
   `internal/courtapi/client.go`. Wire-safe params/return types
   only (primitives, tagged structs, no closures, no `context.Context`
   as a param, no channels). Kept in-process only? See § 10.
2. **Server implementation** — implement on `*court.Court` (or
   whichever type satisfies `Client`). Keep it in
   `court/court.go` alongside siblings, or add a new file if
   the surface grows.
3. **Method constant** — add `MethodFoo = "Foo"` to
   `internal/rpc/court_methods.go`.
4. **Wire DTOs** — add `FooParams` / `FooResult` structs with
   `msgpack:"..."` tags to `internal/rpc/court_types.go`.
5. **Server handler** — in `internal/rpc/court_server.go` inside
   `RegisterCourtHandlers`, add:

   ```go
   c.Handle(MethodFoo, func(ctx context.Context, params []byte) ([]byte, error) {
       var p FooParams
       if err := wire.Decode(params, &p); err != nil {
           return nil, wire.NewError(wire.CodeDecodeFailed, err.Error())
       }
       out, err := impl.Foo(p.X, p.Y)
       if err != nil {
           return nil, err
       }
       return wire.Encode(FooResult{Out: out})
   })
   ```
6. **Client method** — in `internal/rpc/court_client.go`:

   ```go
   func (c *CourtClient) Foo(x, y string) (string, error) {
       raw, err := c.conn.Call(context.Background(), MethodFoo, FooParams{X: x, Y: y})
       if err != nil { return "", err }
       var r FooResult
       if err := wire.Decode(raw, &r); err != nil { return "", err }
       return r.Out, nil
   }
   ```
7. **Test** — extend the `fakeCourt` in
   `internal/rpc/court_loopback_test.go` and exercise Foo through
   the codec.

### Adding a new notification

1. **Tag the struct** with `msgpack:"..."` — if it has an `error` or
   channel field, either mark it `msgpack:"-"` and write a custom
   `MarshalMsgpack`/`UnmarshalMsgpack` (see `StreamErrorMsg` for the
   pattern), or use a wire-twin struct.
2. **Add a method constant** near the others in
   `internal/rpc/notifications.go`: `NotifyFoo = "foo.bar"`.
3. **Register both sides** in the `typeToMethod` and `methodToDecoder`
   maps in the same file.
4. **Emit** from the server by sending the struct down the Subscribe
   chan that `PumpCourtEvents` consumes.
5. **Consume** on the client: the `SubscribeAll` handler will decode
   and deliver it on the shared `<-chan any`.

## 10. The non-wire-safe holdouts

One method on `courtapi.Client` is deliberately in-process only:

| Method           | Why                                                                                       |
| ---------------- | ----------------------------------------------------------------------------------------- |
| `GetMinister`    | Returns a `Minister` interface whose methods include I/O. Used only by `saveSession()`.   |

`ConfigureModel` was previously listed here because it takes a live
`bifrost.LLMProvider` pointer. It is now deprecated: the daemon calls
`ConfigureModel` internally after every `SetContext`, so the TUI never
needs to invoke it directly. Once `GetMinister` is migrated to a
wire-safe shape, `LoopbackCourt` collapses into the plain
`*CourtClient` and the TUI becomes truly stateless.

## 11. Files at a glance

```
internal/wire/
  frame.go        Error struct, Error codes, Frame constants
  codec.go        ReadFrame / WriteFrame / Encode / Decode
internal/rpc/
  conn.go         Bidirectional Conn: Call / Notify / Handle / HandleNotify
  court_methods.go  MethodFoo constants
  court_types.go    Params / Result DTOs
  court_server.go   RegisterCourtHandlers + ServeCourtNotifications
  court_client.go   Typed CourtClient
  court_loopback.go LoopbackCourt hybrid (wire + local passthrough)
  notifications.go      typeToMethod + methodToDecoder registries,
                        PumpNotifications / SubscribeAll
  approval.go           Bidirectional approval Call (daemon → TUI)
  unix.go               SocketPath / Listen / Dial
internal/courtapi/
  client.go       The Client interface (single source of truth)
daemon.go         `asimi daemon` subcommand
autostart.go      autostart
loopback.go       ASIMI_LOOPBACK / ASIMI_DAEMON_SOCKET wiring
```
