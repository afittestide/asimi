# Low-Level Design: LLM Provider/Model System in Go

A Go reimplementation of the `packages/ai` subsystem in pi: a provider-agnostic
runtime that streams completions from many LLM backends behind a uniform
message/event model, with static generated model catalogs, layered auth, and a
per-model "compat" layer that adapts a small set of protocol drivers to
provider-specific quirks.

This document is implementation-oriented. It maps the existing TypeScript
design to Go idioms and specifies data structures, interfaces, concurrency
model, package layout, and the codegen contract.

---

## 1. Goals and Non-Goals

Goals:

- Stream chat completions from N providers through one uniform API.
- A small set of protocol *drivers* (one per wire protocol), not one client per
  provider. Provider-specific behavior is data, not code.
- Static, generated model catalogs plus user config overrides.
- Layered auth: stored credentials > ambient (env/files), with locked OAuth
  refresh.
- A streaming event protocol decoupled from any transport/SDK.
- Deterministic cost accounting and context-overflow detection.

Non-goals (out of scope for this doc):

- The TUI / coding-agent orchestration layer (agent loop, tool execution).
- Image generation (separate `ImagesModels` collection; same patterns).
- OAuth flow *implementations* (only the interface contract they plug into).

---

## 2. Package Layout

Go module path, e.g. `github.com/earendil-works/pi-go/ai`.

```
ai/
  types.go         # core domain types: Model, Message, Usage, Event, Context, Tool
  errors.go        # ModelsError with typed codes
  models.go        # Registry (the Models collection), cost calc, thinking-level helpers
  provider.go      # Provider interface, NewProvider factory, API dispatch
  auth/
    types.go       # ProviderAuth, ApiKeyAuth, OAuthAuth, Credential, CredentialStore, AuthContext
    resolve.go     # ResolveProviderAuth, locked OAuth refresh
    envkey.go      # EnvApiKeyAuth helper (the common api-key resolver)
    oauth.go       # LazyOAuth wrapper (interface satisfied by a loader func)
    store.go       # InMemoryCredentialStore
  api/
    driver.go      # Driver interface + registry (register/get by API name)
    stream.go      # EventStream (channel-based), LazyStream helper
    simple.go      # SimpleOptions building, AdjustMaxTokensForThinking
    transform.go   # message normalization (image downgrade, tool id normalize)
    overflow.go    # IsContextOverflow, overflow regex table
    openai_completions.go
    openai_responses.go
    openai_codex_responses.go
    azure_openai_responses.go
    anthropic_messages.go
    google_generative_ai.go
    google_vertex.go
    bedrock_converse.go
    mistral_conversations.go
  compat/
    openai_completions.go   # OpenAICompletionsCompat, DetectCompat, GetCompat (resolve+merge)
    openai_responses.go
    anthropic_messages.go
  catalog/
    generate.go     # codegen: reads provider specs -> emits catalog.gen.go
    catalog.gen.go   # generated: 35 provider constructors + model tables
    providers.go     # BuiltinProviders(), BuiltinModels() registry assembly
```

Rationale for splitting `compat` from `api`: the compat structs and detection
are pure data/logic with no HTTP dependencies, used by both the catalog codegen
and the drivers. Keeping them separate makes the drivers thinner and lets
`detectCompat` be unit-tested without a fake server.

---

## 3. Core Domain Types (`types.go`)

All types are plain structs. No methods on `Model`/`Provider` that require
state — behavior lives in interfaces implemented elsewhere.

### 3.1 APIs and provider IDs

Go has no string-literal union types. Use typed string constants plus a
`KnownApi`/`KnownProvider` set for validation, but keep the fields as `string`
so custom APIs/providers compose.

```go
type Api string

const (
    ApiOpenAICompletions        Api = "openai-completions"
    ApiOpenAIResponses          Api = "openai-responses"
    ApiOpenAICodexResponses     Api = "openai-codex-responses"
    ApiAzureOpenAIResponses     Api = "azure-openai-responses"
    ApiAnthropicMessages        Api = "anthropic-messages"
    ApiGoogleGenerativeAI       Api = "google-generative-ai"
    ApiGoogleVertex             Api = "google-vertex"
    ApiBedrockConverse          Api = "bedrock-converse-stream"
    ApiMistralConversations     Api = "mistral-conversations"
)

var KnownApis = map[Api]bool{ /* the above */ }

type ProviderID string
```

### 3.2 Content blocks and messages

```go
type TextContent struct {
    Type         string `json:"type"`         // always "text"
    Text         string `json:"text"`
    TextSignature string `json:"textSignature,omitempty"`
}

type ThinkingContent struct {
    Type             string `json:"type"`             // "thinking"
    Thinking         string `json:"thinking"`
    ThinkingSignature string `json:"thinkingSignature,omitempty"`
    Redacted         bool   `json:"redacted,omitempty"`
}

type ImageContent struct {
    Type     string `json:"type"`     // "image"
    Data     string `json:"data"`     // base64
    MIMEType string `json:"mimeType"`
}

type ToolCall struct {
    Type           string         `json:"type"` // "toolCall"
    ID             string         `json:"id"`
    Name           string         `json:"name"`
    Arguments      map[string]any `json:"arguments"`
    ThoughtSignature string      `json:"thoughtSignature,omitempty"`
}

type ContentBlock any // TextContent | ThinkingContent | ImageContent | ToolCall
```

Messages:

```go
type Role string

type UserMessage struct {
    Role      Role                    `json:"role"` // "user"
    Content   any                     // string | []ContentBlock
    Timestamp int64                   `json:"timestamp"` // unix ms
}

type AssistantMessage struct {
    Role          Role           `json:"role"` // "assistant"
    Content       []ContentBlock `json:"content"`
    Api           Api            `json:"api"`
    Provider      ProviderID     `json:"provider"`
    Model         string         `json:"model"`
    ResponseModel string         `json:"responseModel,omitempty"`
    ResponseID    string         `json:"responseId,omitempty"`
    Diagnostics   []Diagnostic   `json:"diagnostics,omitempty"`
    Usage         Usage          `json:"usage"`
    StopReason    StopReason     `json:"stopReason"`
    ErrorMessage  string         `json:"errorMessage,omitempty"`
    Timestamp     int64          `json:"timestamp"`
}

type ToolResultMessage struct {
    Role       Role           `json:"role"` // "toolResult"
    ToolCallID string         `json:"toolCallId"`
    ToolName   string         `json:"toolName"`
    Content    []ContentBlock `json:"content"`
    Details    any            `json:"details,omitempty"`
    IsError    bool           `json:"isError"`
    Timestamp  int64          `json:"timestamp"`
}

type Message any // UserMessage | AssistantMessage | ToolResultMessage
```

`Content` of `UserMessage` is `string | []ContentBlock`. In Go represent as
`any` with helper accessors (`UserText(msg) (string, bool)`,
`UserBlocks(msg) []ContentBlock`), or define `UserMessage.Content` as a custom
type `UserContent` with an `encoding/json` Marshaler/Unmarshaler that handles
both shapes. The custom type is preferred — it keeps call sites typed.

### 3.3 Usage and cost

```go
type Usage struct {
    Input       int64   `json:"input"`
    Output      int64   `json:"output"`
    CacheRead   int64   `json:"cacheRead"`
    CacheWrite  int64   `json:"cacheWrite"`
    CacheWrite1h int64  `json:"cacheWrite1h,omitempty"`
    TotalTokens int64   `json:"totalTokens"`
    Cost        Cost    `json:"cost"`
}

type Cost struct {
    Input      float64 `json:"input"`
    Output     float64 `json:"output"`
    CacheRead  float64 `json:"cacheRead"`
    CacheWrite float64 `json:"cacheWrite"`
    Total      float64 `json:"total"`
}

type StopReason string
const (
    StopStop    StopReason = "stop"
    StopLength  StopReason = "length"
    StopToolUse StopReason = "toolUse"
    StopError   StopReason = "error"
    StopAborted StopReason = "aborted"
)
```

### 3.4 Model

```go
type InputModality string
const ( ModText InputModality = "text"; ModImage = "image" )

type ModelCost struct {
    Input, Output, CacheRead, CacheWrite float64 // $/million tokens
}

type Model struct {
    ID              string             `json:"id"`
    Name            string             `json:"name"`
    API             Api                `json:"api"`
    Provider        ProviderID         `json:"provider"`
    BaseURL         string             `json:"baseUrl"`
    Reasoning       bool               `json:"reasoning"`
    ThinkingLevelMap ThinkingLevelMap  `json:"thinkingLevelMap,omitempty"`
    Input           []InputModality    `json:"input"`
    Cost            ModelCost          `json:"cost"`
    ContextWindow   int                `json:"contextWindow"`
    MaxTokens       int                `json:"maxTokens"`
    Headers         map[string]string  `json:"headers,omitempty"`
    Compat          Compat             `json:"compat,omitempty"` // see §8
}
```

`Compat` is an interface with three concrete implementations (see §8). The
catalog codegen emits the right concrete type per model; user-config merging
preserves it. JSON (un)marshaling uses a discriminated union on `api` to pick
the concrete compat type.

### 3.5 Context and tools

```go
type Tool struct {
    Name        string         `json:"name"`
    Description string         `json:"description"`
    Parameters  jsonschema.Raw // any JSON Schema; opaque to this layer
}

type Context struct {
    SystemPrompt string
    Messages     []Message
    Tools        []Tool
}
```

`Tool.Parameters` is a raw JSON Schema blob. pi uses TypeBox in TS; in Go use
`json.RawMessage` or a thin `JSONSchema` wrapper. This layer never interprets
parameters — drivers forward them.

### 3.6 Thinking levels

```go
type ThinkingLevel string
const (
    ThinkOff     ThinkingLevel = "off"
    ThinkMinimal ThinkingLevel = "minimal"
    ThinkLow     ThinkingLevel = "low"
    ThinkMedium  ThinkingLevel = "medium"
    ThinkHigh    ThinkingLevel = "high"
    ThinkXHigh   ThinkingLevel = "xhigh"
)

type ModelThinkingLevel = ThinkingLevel // includes "off"

// ThinkingLevelMap maps each level to a provider-specific string, or nil to
// mark it unsupported.
type ThinkingLevelMap map[ThinkingLevel]*string // nil value = unsupported
```

Use `*string` so `nil` is distinguishable from the empty string and from
"unset". A level absent from the map means "use provider default".

---

## 4. Errors (`errors.go`)

```go
type ErrorCode string
const (
    ErrModelSource     ErrorCode = "model_source"
    ErrModelValidation ErrorCode = "model_validation"
    ErrProvider        ErrorCode = "provider"
    ErrStream          ErrorCode = "stream"
    ErrAuth            ErrorCode = "auth"
    ErrOAuth           ErrorCode = "oauth"
)

type ModelsError struct {
    Code    ErrorCode
    Message string
    Cause   error
}
func (e *ModelsError) Error() string { ... }
func (e *ModelsError) Unwrap() error { return e.Cause }
```

All cross-layer failures are wrapped in `ModelsError` with a code. Drivers
emit errors via the stream protocol (see §6), not by returning `error` from
`Stream`, so `Stream` itself returns only structural errors (unknown provider,
no driver for api).

---

## 5. Provider and Registry (`provider.go`, `models.go`)

### 5.1 Provider interface

```go
type Provider struct {
    ID       ProviderID
    Name     string
    BaseURL  string
    Headers  map[string]string
    Auth     auth.ProviderAuth

    // Model list. For static providers this is immutable. For dynamic
    // providers (gateways) this is guarded by refreshState.
    models   []Model

    // Dynamic providers only. nil = static.
    refresh  func(ctx context.Context) ([]Model, error)

    // API dispatch: single driver, or map keyed by Model.API for mixed-api
    // providers (fireworks, github-copilot, opencode, ...).
    single   api.Driver
    byAPI    map[Api]api.Driver
}

func (p *Provider) Models() []Model
func (p *Provider) Refresh(ctx context.Context) error  // no-op if static
```

Make `Provider` a concrete struct, not an interface. The TS version uses an
interface because factories return closures; in Go a struct with function
fields (or a small interface) is cleaner. Prefer the struct: fewer allocations,
clearer zero-value handling, and the "factory" is just `NewProvider(opts)`.

`Models()` must never panic; the registry treats a panicking/erroneous provider
as having no models. In Go, recover in a wrapper if providers are third-party;
for first-party providers a panic is a bug — but the registry still defers a
recover to be safe.

### 5.2 Provider factory

```go
type ProviderOptions struct {
    ID           ProviderID
    Name         string
    BaseURL      string
    Headers      map[string]string
    Auth         auth.ProviderAuth
    Models       []Model
    Refresh      func(ctx context.Context) ([]Model, error) // optional
    API          any // api.Driver OR map[Api]api.Driver
}

func NewProvider(opts ProviderOptions) *Provider
```

Refresh deduplication: a `sync.Mutex` plus a `*future` field. Concurrent
`Refresh` calls share one in-flight fetch. On error the stored list is left
unchanged; the error propagates to the caller wrapped as `ErrModelSource`.

```go
type refreshFuture struct {
    done chan struct{}
    err  error
}
```

### 5.3 Registry (`Models`)

```go
type Registry struct {
    mu          sync.RWMutex
    providers   map[ProviderID]*Provider
    credentials auth.CredentialStore
    authCtx     auth.AuthContext
}

func NewRegistry(opts ...Option) *Registry

func (r *Registry) SetProvider(p *Provider)
func (r *Registry) DeleteProvider(id ProviderID)
func (r *Registry) Providers() []*Provider
func (r *Registry) Provider(id ProviderID) *Provider
func (r *Registry) Models(provider ...ProviderID) []Model
func (r *Registry) Model(provider ProviderID, id string) (Model, bool)
func (r *Registry) Refresh(ctx context.Context, provider ...ProviderID) error
func (r *Registry) Auth(model Model) (auth.AuthResult, error) // nil result = unconfigured
func (r *Registry) Stream(ctx context.Context, model Model, c Context, opts StreamOptions) *EventStream
func (r *Registry) Complete(ctx context.Context, model Model, c Context, opts StreamOptions) (AssistantMessage, error)
func (r *Registry) StreamSimple(ctx context.Context, model Model, c Context, opts SimpleOptions) *EventStream
func (r *Registry) CompleteSimple(...) (AssistantMessage, error)
```

`Models(provider)` aggregates across providers, best-effort: a provider whose
`Models()` panics yields nothing. `Model(provider, id)` is the common lookup.

`Stream` flow (matches TS `ModelsImpl.stream` + `lazyStream`):

1. Look up the provider; if missing, return an `EventStream` that emits a
   single `error` event (`ErrProvider`).
2. Resolve auth (`r.Auth(model)`). If auth resolution fails with `ErrOAuth` /
   `ErrAuth`, emit an error event (do **not** return `error` from `Stream`).
3. Apply auth to the model/options: override `BaseURL` from auth if set, merge
   `apiKey` (caller > auth), merge headers (auth < caller).
4. Dispatch to the provider's driver for `model.API`. If the provider has no
   driver for that API, emit `ErrStream`.
5. Forward the driver's event stream to the outer stream.

`Stream` returns synchronously with a live `*EventStream`; all async work
(auth, HTTP) happens in a goroutine feeding the stream. This mirrors
`lazyStream` and is essential for the TUI/consumer that wants to start reading
events immediately.

### 5.4 Cost and thinking helpers

```go
func CalculateCost(m Model, u *Usage) Cost
func SupportedThinkingLevels(m Model) []ThinkingLevel
func ClampThinkingLevel(m Model, want ThinkingLevel) ThinkingLevel
func ModelsEqual(a, b *Model) bool
```

`CalculateCost`: Anthropic charges 2x base input for 1h cache writes:
`cacheWrite cost = (cost.CacheWrite * shortWrite + cost.Input * 2 * longWrite) / 1e6`.
Mutates `u.Cost` and returns it.

`SupportedThinkingLevels`: if `!m.Reasoning` -> `["off"]`. Otherwise filter the
ordered levels `[off, minimal, low, medium, high, xhigh]` by the
`ThinkingLevelMap`: a level is supported unless its map value is `nil`.
`xhigh` requires the map value to be non-nil (i.e., explicitly enabled).

`ClampThinkingLevel`: if `want` is supported, return it. Otherwise walk *up*
the ordered list, then *down*, returning the first supported level.

---

## 6. Streaming Protocol (`api/stream.go`)

### 6.1 Event types

```go
type EventType string
const (
    EvStart         EventType = "start"
    EvTextStart     EventType = "text_start"
    EvTextDelta     EventType = "text_delta"
    EvTextEnd       EventType = "text_end"
    EvThinkingStart EventType = "thinking_start"
    EvThinkingDelta EventType = "thinking_delta"
    EvThinkingEnd   EventType = "thinking_end"
    EvToolCallStart EventType = "toolcall_start"
    EvToolCallDelta EventType = "toolcall_delta"
    EvToolCallEnd   EventType = "toolcall_end"
    EvDone          EventType = "done"
    EvError         EventType = "error"
)

type Event struct {
    Type         EventType
    ContentIndex int
    Delta        string         // for *_delta
    Content      string         // for text_end / thinking_end
    ToolCall     *ToolCall      // for toolcall_end
    Partial      *AssistantMessage // always present (the running message)
    Message      *AssistantMessage // for done
    Error        *AssistantMessage // for error
    Reason       StopReason     // for done/error
}
```

A single `Event` struct with optional fields is more Go-idiomatic than the TS
discriminated union and avoids a type switch at every consumer. Provide
constructors (`EvTextDelta(i, delta, partial)`) so callers don't set fields by
hand. Consumers still switch on `e.Type`.

### 6.2 EventStream

The TS version is a hand-rolled async iterator with a queue and a final-result
promise. In Go this is a channel plus a separate result channel/future.

```go
type EventStream struct {
    ch     chan Event        // buffered small (e.g. 16) or unbuffered
    result chan AssistantMessage
    once   sync.Once
}

func (s *EventStream) Events() <-chan Event      // for ranging
func (s *EventStream) Result() <-chan AssistantMessage // resolves on done/error
func (s *EventStream) Push(e Event)              // driver-side; no-op after close
func (s *EventStream) End(msg AssistantMessage)  // closes channels
```

Design notes:

- `Push` is called only by the producing goroutine. After the terminal event
  (`done`/`error`), `Push` is a no-op (guarded by `once` / a `done` bool under
  a mutex) so buggy drivers can't double-close.
- The terminal event must be pushed *before* `End` so consumers ranging over
  `Events()` see it; `Result()` resolves from the terminal event payload.
- `context.Context` cancellation: the *caller* cancels via the `ctx` passed to
  `Stream`. The driver watches `ctx.Done()` and emits an `error` event with
  `Reason: "aborted"`. Do not close `Events()` on ctx cancel without emitting
  an event — the consumer relies on the terminal event for the final message.

### 6.3 LazyStream

`Stream` must return immediately. Auth resolution and driver setup are async.
`LazyStream(model, setup)` returns an `EventStream` whose producing goroutine
runs `setup()` (which returns an inner `EventStream` or `error`) and forwards
all events. Setup failure emits one `error` event with a synthesized
`AssistantMessage` (empty content, `StopReason: "error"`, `ErrorMessage`).

```go
func LazyStream(m Model, setup func(context.Context) (*EventStream, error)) *EventStream
```

Forwarding = a goroutine ranging over the inner `Events()` and pushing to the
outer stream, then propagating `Result()`.

---

## 7. Auth (`auth/`)

### 7.1 Types

```go
type ModelAuth struct {
    APIKey  string
    Headers map[string]string
    BaseURL string
}

type AuthResult struct {
    Auth   ModelAuth
    Source string // for status UI: "ANTHROPIC_API_KEY", "OAuth", "~/.aws/credentials"
}

type Credential interface{ isCredential() }
type ApiKeyCredential struct {
    Type     string            // "api-key"
    Key      string
    Metadata map[string]string
}
type OAuthCredential struct {
    Type    string // "oauth"
    Access  string
    Refresh string
    Expires int64  // unix ms
    // plus any provider extras (copilot base url, scopes, etc.)
}
```

`CredentialStore` is the persistence seam. One credential per provider id.

```go
type CredentialStore interface {
    Read(ctx context.Context, providerID string) (Credential, error) // nil, nil = missing
    Modify(ctx context.Context, providerID string,
        fn func(ctx context.Context, current Credential) (Credential, error)) (Credential, error)
    Delete(ctx context.Context, providerID string) error
}
```

`Modify` is the *only* write path and must serialize per provider id
(cross-process where the backing store supports it, e.g. a file lock). The
OAuth refresh runs *inside* `Modify` so concurrent requests can't double-refresh
a rotated token.

`AuthContext` is injectable env/filesystem access:

```go
type AuthContext interface {
    Env(name string) (string, bool)
    FileExists(path string) bool
}
```

Use `os.Getenv` / `os.Stat` defaults; tests inject fakes. (No `error` returns —
env/file lookups are best-effort.)

### 7.2 ProviderAuth

```go
type ProviderAuth struct {
    APIKey ApiKeyAuth   // at least one of APIKey/OAuth must be set
    OAuth  OAuthAuth
}

type ApiKeyAuth interface {
    Name() string
    Login(ctx context.Context, cb LoginCallbacks) (ApiKeyCredential, error) // nil = ambient-only
    Resolve(ctx context.Context, in ApiKeyResolveInput) (*AuthResult, error) // nil = unconfigured
}

type ApiKeyResolveInput struct {
    Model      Model
    Ctx        AuthContext
    Credential *ApiKeyCredential
}

type OAuthAuth interface {
    Name() string
    Login(ctx context.Context, cb LoginCallbacks) (OAuthCredential, error)
    Refresh(ctx context.Context, c OAuthCredential) (OAuthCredential, error) // network; may fail
    ToAuth(ctx context.Context, c OAuthCredential) (ModelAuth, error)        // side-effect-free
}
```

`LoginCallbacks` drives interactive prompts (text/secret/select/manual_code),
device-code events, and an abort `context.Context`. Defined in `auth/types.go`;
implementations are app-owned (the TUI provides them).

### 7.3 EnvApiKeyAuth helper (`auth/envkey.go`)

The common case: stored key wins, else first set env var.

```go
func EnvApiKeyAuth(name string, envVars ...string) ApiKeyAuth
```

Returns an `ApiKeyAuth` whose `Resolve` returns
`{Auth: {APIKey: cred.Key}, Source: "stored credential"}` if a stored key
exists, else iterates `envVars` and returns the first set one with
`Source: envVar`, else `nil`. `Login` prompts for a secret.

### 7.4 LazyOAuth (`auth/oauth.go`)

```go
func LazyOAuth(name string, load func() (OAuthAuth, error)) OAuthAuth
```

Wraps a loader so provider definitions can advertise OAuth without importing
the flow code. `load()` runs once on first `Login`/`Refresh`/`ToAuth` call
(`sync.Once`).

### 7.5 Resolution with locked refresh (`auth/resolve.go`)

```go
func ResolveProviderAuth(
    ctx context.Context,
    provider ProviderID,
    pauth ProviderAuth,
    model Model,
    store CredentialStore,
    actx AuthContext,
) (*AuthResult, error)
```

Algorithm (matches `resolve.ts`):

1. `cred, _ := store.Read(ctx, provider)`.
2. If `cred` is an OAuth credential and `pauth.OAuth` != nil -> resolve OAuth
   (below).
3. Else if `cred` is an api-key credential and `pauth.APIKey` != nil ->
   `pauth.APIKey.Resolve(ctx, {model, actx, cred})`.
4. Else (no stored credential, or type mismatch with no handler) -> ambient:
   if `pauth.APIKey` != nil, `Resolve` with `cred=nil`; else `nil, nil`.
5. Wrap resolution failures in `ModelsError{Code: ErrAuth}` (store/resolve) or
   `ErrOAuth` (refresh/ToAuth).

OAuth locked refresh (double-checked locking):

1. If `now < cred.Expires`, return `ToAuth(cred)` directly (no lock).
2. Else `store.Modify(ctx, provider, func(current Credential) (Credential, error) {
       if current is not oauth -> return nil, nil  // logged out meanwhile
       if now < current.Expires -> return nil, nil  // another caller refreshed
       refreshed, err := oauth.Refresh(ctx, *current)
       if err != nil -> return nil, &ModelsError{Code: ErrOAuth, Cause: err}
       return &refreshed, nil
   })`.
3. If `Modify` returns a non-oauth credential (or nil), return `nil` (logged
   out).
4. `ToAuth(refreshed)`; wrap failure as `ErrOAuth`.

`InMemoryCredentialStore` (`auth/store.go`) implements `Modify` with a
per-provider promise chain (a `map[string]chan struct{}` of mutexes, or a
`map[string]*sync.Mutex`). Sufficient for tests and single-process use; the
coding-agent provides a file-backed store with `flock`.

---

## 8. Compat Layer (`compat/`)

This is the heart of "one driver, many providers." Each protocol driver has a
compat struct describing how a specific model deviates from the protocol's
canonical shape. Unset fields default to URL/provider auto-detection.

### 8.1 OpenAI Completions compat

```go
type MaxTokensField string
const ( MaxCompletionTokens MaxTokensField = "max_completion_tokens"; MaxTokens = "max_tokens" )

type ThinkingFormat string
const (
    TFOpenAI          ThinkingFormat = "openai"
    TFOpenRouter      ThinkingFormat = "openrouter"
    TFDeepSeek        ThinkingFormat = "deepseek"
    TFTogether        ThinkingFormat = "together"
    TFZai             ThinkingFormat = "zai"
    TFQwen            ThinkingFormat = "qwen"
    TFQwenChatTpl     ThinkingFormat = "qwen-chat-template"
    TFChatTemplate    ThinkingFormat = "chat-template"
    TFStringThinking  ThinkingFormat = "string-thinking"
    TFAntLing         ThinkingFormat = "ant-ling"
)

type OpenAICompletionsCompat struct {
    SupportsStore                         *bool
    SupportsDeveloperRole                 *bool
    SupportsReasoningEffort               *bool
    SupportsUsageInStreaming              *bool
    MaxTokensField                        *MaxTokensField
    RequiresToolResultName                *bool
    RequiresAssistantAfterToolResult      *bool
    RequiresThinkingAsText                *bool
    RequiresReasoningContentOnAssistant   *bool
    ThinkingFormat                        *ThinkingFormat
    ChatTemplateKwargs                    map[string]ChatTemplateKwargValue
    OpenRouterRouting                     *OpenRouterRouting
    VercelGatewayRouting                  *VercelGatewayRouting
    ZaiToolStream                         *bool
    SupportsStrictMode                    *bool
    CacheControlFormat                    *string // "anthropic"
    SendSessionAffinityHeaders            *bool
    SupportsLongCacheRetention            *bool
}
```

Use `*T` for every optional field so "unset" (auto-detect) is distinguishable
from "set to zero value." Booleans especially: `*bool` = explicit true/false,
`nil` = auto.

`ChatTemplateKwargValue`: `string | number | bool | nil | {$var, omitWhenOff}`.
In Go:

```go
type ChatTemplateKwargValue struct {
    Scalar       any // string|float64|bool|nil, when non-variable
    Var          string  // "thinking.enabled"|"thinking.effort", when variable
    OmitWhenOff  bool
}
```

With a custom JSON marshaler that emits either the scalar or `{"$var":...}`.

`OpenRouterRouting` / `VercelGatewayRouting` are plain structs mirroring the TS
interfaces (allow_fallbacks, order, only, ignore, quantizations, sort,
max_price, percentile cutoffs, etc.). Translate verbatim.

### 8.2 OpenAI Responses / Anthropic compat

Smaller structs (`OpenAIResponsesCompat`, `AnthropicMessagesCompat`) per §8.1
of the TS types: `SupportsDeveloperRole`, `SendSessionIdHeader`,
`SupportsLongCacheRetention`; and Anthropic's `SupportsEagerToolInputStreaming`,
`SupportsLongCacheRetention`, `SendSessionAffinityHeaders`,
`SupportsCacheControlOnTools`, `SupportsTemperature`, `ForceAdaptiveThinking`,
`AllowEmptySignature`. Same `*T` optional pattern.

### 8.3 Detection and resolution (`compat/openai_completions.go`)

```go
func DetectCompat(m Model) OpenAICompletionsCompat
func GetCompat(m Model) OpenAICompletionsCompat // merge model.compat over detected
```

`DetectCompat` inspects `m.Provider` and `m.BaseURL` to classify (isZai,
isTogether, isMoonshot, isOpenRouter, isCloudflare*, isNvidia, isAntLing,
isGrok, isDeepSeek, isOpenRouterDeveloperRoleModel) and produces a fully
populated compat. This is a direct port of `detectCompat` in
`openai-completions.ts`; keep the classification predicates as named helpers
so they're testable and the matrix is readable.

`GetCompat` returns `DetectCompat` if `m.Compat` is nil, otherwise merges:
each field takes `model.compat.X` if non-nil, else `detected.X`. Routing maps
default to empty (not nil) when unset on the model — preserve that so callers
can always range over them.

### 8.4 The Compat interface on Model

```go
type Compat interface{ isCompat() }
func (OpenAICompletionsCompat) isCompat() {}
func (OpenAIResponsesCompat) isCompat()   {}
func (AnthropicMessagesCompat) isCompat() {}
```

The catalog codegen attaches the right concrete type based on `Model.API`.
Drivers type-assert: `c := m.Compat.(compat.OpenAICompletionsCompat)` (with a
guard). For user-defined models with the wrong compat type, the driver emits a
stream error.

---

## 9. API Drivers (`api/`)

### 9.1 Driver interface and registry

```go
type Driver interface {
    Stream(ctx context.Context, m Model, c Context, opts StreamOptions) *EventStream
    StreamSimple(ctx context.Context, m Model, c Context, opts SimpleOptions) *EventStream
}

func RegisterDriver(api Api, d Driver)
func DriverFor(api Api) (Driver, bool)
```

Each protocol module's `init()` registers its driver. The provider factory
wires `Driver`/`map[Api]Driver` into the provider. Dispatch: a provider with a
single driver uses it for all models; a provider with a map dispatches on
`m.API`; a model whose API has no entry produces a stream error event
(`ErrStream: "no API implementation for <api>"`).

### 9.2 Driver contract

- `Stream` returns immediately with a live `EventStream`. All network work runs
  in a goroutine.
- Once invoked, request/model/runtime failures are encoded in the stream as an
  `error` event with a synthesized `AssistantMessage` (`StopReason: "error"` or
  `"aborted"`, `ErrorMessage` set). `Stream` itself returns no `error` for
  request failures — only for structural problems (e.g. nil model, though these
  should be guarded upstream).
- Must emit `start` first, then content events, then a terminal `done` or
  `error`. `start` carries an empty `AssistantMessage` (zero usage, empty
  content) that subsequent events mutate via `Partial`.
- Respect `opts.Context` for cancellation -> `error` event with `aborted`.
- Apply `opts.OnPayload` (may replace payload) and `opts.OnResponse` (post-response
  hook) when provided.

### 9.3 StreamOptions

```go
type StreamOptions struct {
    Context                  context.Context
    Temperature              *float64
    MaxTokens                *int
    APIKey                   string
    Headers                  map[string]string
    Transport                Transport // sse|websocket|websocket-cached|auto
    CacheRetention           CacheRetention // none|short|long
    SessionID                string
    TimeoutMs                int
    WebsocketConnectTimeoutMs int
    MaxRetries               int
    MaxRetryDelayMs          int
    Metadata                 map[string]any
    Env                      map[string]string // provider-scoped env overrides
    OnPayload                func(payload any, m Model) any
    OnResponse               func(resp ProviderResponse, m Model)
}

type SimpleOptions struct {
    StreamOptions
    Reasoning       ThinkingLevel
    ThinkingBudgets ThinkingBudgets
}
```

Per-API option types (e.g. `OpenAICompletionsOptions` adding `ToolChoice`,
`ReasoningEffort`) extend `StreamOptions` as separate structs passed to typed
driver methods, OR — to keep the `Driver` interface uniform — as extra fields on
`StreamOptions` accessed via type-asserted option bags. The TS design uses
generic typed options per API; in Go, prefer a uniform `Driver` interface with
an `any` options bag the driver type-asserts. Concretely:

```go
type StreamOptions struct { ... common fields ...; Extra any } // per-API options
```

This loses some compile-time safety but keeps dispatch uniform. If per-API
typing is desired, expose typed helper constructors on each driver package
(`openaicompletions.Stream(ctx, m, c, opts)`) that the registry can call after
narrowing `m.API`. Recommendation: uniform interface + `Extra` bag for v1.

### 9.4 SimpleOptions building (`api/simple.go`)

`buildBaseOptions` strips `Reasoning`/`ThinkingBudgets` from `SimpleOptions`
into a plain `StreamOptions` (after applying `apiKey`).

`AdjustMaxTokensForThinking(base, modelMax, level, budgets)`:
- Default budgets: minimal=1024, low=2048, medium=8192, high=16384.
- `maxTokens = base==nil ? modelMax : min(base+budget, modelMax)`.
- Clamp `reasoning="xhigh"` to `"high"` for budget selection.
- If `maxTokens <= budget`, shrink budget to `max(0, maxTokens-1024)`.
- Returns `{maxTokens, thinkingBudget}`.

### 9.5 Message transformation (`api/transform.go`)

Pre-stream normalization shared by all drivers:

- **Image downgrade**: if `!model.Input` includes `"image"`, replace image
  blocks in user/toolResult content with a text placeholder (collapsing
  consecutive placeholders). `"(image omitted: model does not support images)"`
  for user, `"(tool image omitted: ...)"` for tool results.
- **Tool-call ID normalization**: normalize IDs across providers for replay
  consistency (port `normalizeToolCallId`).
- Driver-specific message conversion (TS `convertMessages`) lives inside each
  driver, not here — it depends on compat and protocol shape.

### 9.6 Overflow detection (`api/overflow.go`)

```go
func IsContextOverflow(msg AssistantMessage, contextWindow int) bool
```

Two signals (port `overflow.ts`):
1. `stopReason == "stop"` and `usage.input > contextWindow` (z.ai silent
   overflow).
2. `stopReason == "length"` and `usage.output == 0` and
   `usage.input >= contextWindow*0.99` (Xiaomi MiMo truncates to fit, then no
   room to generate).

Additionally a regex table (`OVERFLOW_PATTERNS`) matches provider error
messages — used by callers that surface `errorMessage` from error events. Keep
the table as a slice of `*regexp.Regexp` compiled at init.

### 9.7 Driver: OpenAI Completions (`api/openai_completions.go`)

Largest driver (~1300 lines TS). Outline:

1. Resolve compat via `compat.GetCompat(m)`.
2. Build an HTTP client (not the `openai` npm SDK — Go has no equivalent; use
   `net/http` + `encoding/json` + an SSE reader). Base URL from `m.BaseURL`
   (Cloudflare gateway URL resolution is a special case). Default headers from
   `m.Headers`; GitHub Copilot dynamic headers when `provider == "github-copilot"`;
   session-affinity headers (`session_id`, `x-client-request-id`,
   `x-session-affinity`) when `compat.SendSessionAffinityHeaders` and
   `SessionID` set; Cloudflare AI Gateway moves `Authorization` to
   `cf-aig-authorization`.
3. Build params: `model`, `messages` (via `convertMessages` honoring
   `compat.SupportsDeveloperRole`, `RequiresToolResultName`,
   `RequiresAssistantAfterToolResult`, `RequiresThinkingAsText`,
   `RequiresReasoningContentOnAssistantMessages`), `stream: true`,
   `stream_options: {include_usage: true}` when supported,
   `prompt_cache_key`/`prompt_cache_retention` per cache retention + compat,
   `tools`/`tool_choice` when context has tools, max-tokens field per
   `compat.MaxTokensField`, reasoning params per `compat.ThinkingFormat`
   (openai `reasoning_effort`; openrouter `reasoning:{effort}`; deepseek
   `thinking:{type}`+`reasoning_effort`; together `reasoning:{enabled}`+effort;
   zai `thinking:{type}`; qwen `enable_thinking`; chat-template
   `chat_template_kwargs`; etc.), OpenRouter/Vercel routing fields,
   `cache_control` markers when `compat.CacheControlFormat == "anthropic"`.
4. Apply `OnPayload`.
5. POST `/chat/completions`, read SSE stream.
6. For each chunk: capture `responseId`/`responseModel`; parse `usage` (with
   per-provider fallbacks like Moonshot); the first `choice.delta` drives
   text/thinking/tool-call block state. Maintain block-by-index and
   block-by-id maps for streaming tool calls; parse arguments with
   `parseStreamingJson` (incremental JSON repair). Emit `*_start`/`*_delta`/
   `*_end` events. On `finish_reason`, finalize blocks and compute `stopReason`.
7. Terminal: `done` (stop/length/toolUse) or `error`. Apply `CalculateCost`.

Implement an SSE reader (`bufio.Scanner` with a long max-token size, or a
custom line reader) that yields `data: ` payloads; handle `[DONE]`. Reuse it
across all SSE-based drivers.

### 9.8 Other drivers

Each is a port of the matching TS file. Wire-protocol specifics differ but the
skeleton is identical: build params from `Context` + compat, POST, read SSE (or
WebSocket for providers that support it — `Transport` option), map chunks to
the event protocol, compute usage/cost, terminate. The Anthropic driver uses
`AnthropicMessagesCompat` for `cache_control` on tools, eager tool input
streaming, adaptive thinking, temperature support. The Google drivers use
`generateContent`/`streamGenerateContent` (GenAI) and the Vertex token-exchange
endpoint. Bedrock uses SigV4 + the ConverseStream API. Mistral uses its
conversations API.

HTTP/SSE plumbing, retry/backoff, and the `OnResponse` hook should be shared
helpers in `api/http.go` to avoid duplication.

---

## 10. Catalog Codegen (`catalog/`)

### 10.1 Generator

`catalog/generate.go` (a `//go:generate` target or standalone command) reads a
provider-spec source and emits `catalog.gen.go`. Two viable source formats:

1. **Author the catalog in the generator itself** (TS does this —
   `scripts/generate-models.ts` holds the model tables and emits TS). The Go
   generator would hold Go structs and emit Go.
2. **Author in a data format** (YAML/JSON) and emit Go.

Recommendation: author as Go data in a separate input file
(`catalog/specs.go`), and have the generator emit `catalog.gen.go` containing
the provider constructors and a `BuiltinProviders()` slice. This keeps the
authoring experience type-checked (the spec file compiles) while the generated
file is the single source for the registry. Per the repo rule, never hand-edit
`catalog.gen.go`; edit `specs.go` (or the generator) and regenerate.

Each provider spec contains: id, name, base URL, headers, auth (env var names
or OAuth loader ref), models (with all `Model` fields incl. `Compat`), and an
optional refresh function for dynamic providers (openrouter, github-copilot,
opencode, opencode-go, cloudflare-ai-gateway). For dynamic providers the
generated constructor wires a `Refresh` that fetches the provider's
`/models`-style endpoint and maps results into `Model` structs (using the
provider's compat defaults).

### 10.2 Generated file shape

```go
// code generated; do not edit.

func amazonBedrockProvider() *Provider { ... }
func anthropicProvider() *Provider { ... }
// ... 35 providers

func BuiltinProviders() []*Provider {
    return []*Provider{ amazonBedrockProvider(), anthropicProvider(), /* ... */ }
}

func BuiltinModels(opts ...Option) *Registry {
    r := NewRegistry(opts...)
    for _, p := range BuiltinProviders() { r.SetProvider(p) }
    return r
}
```

The 35 built-in providers and their api assignments (from §1 analysis):
- openai-completions only (18): ant-ling, cerebras, cloudflare-workers-ai,
  deepseek, groq, huggingface, moonshotai, moonshotai-cn, nvidia, openrouter,
  together, xai, xiaomi, xiaomi-token-plan-ams/cn/sgp, zai, zai-coding-cn.
- openai-responses only (3): openai, openai-codex, azure-openai-responses.
- anthropic-messages only (4): anthropic, kimi-coding, minimax, minimax-cn.
- Other single-api: google (generative-ai), google-vertex, amazon-bedrock
  (bedrock-converse), mistral (mistral-conversations).
- Multi-api gateways (5): cloudflare-ai-gateway, fireworks, github-copilot,
  opencode, opencode-go (each a `map[Api]Driver`).

---

## 11. User-Defined Models and Overrides

The coding-agent loads a `models.json` (or equivalent config) that can: define
new providers, override built-in provider baseUrl/headers/auth, and override
individual model fields (including `contextWindow`, `maxTokens`, `cost`,
`compat`). This is a layer above `ai/`, but the `ai` package must support the
merge semantics:

- **Model override** = deep merge: top-level scalar fields replace; `cost` is
  field-wise merged; `thinkingLevelMap` is keyed-merged; `compat` is field-wise
  merged with nested routing/kwarg maps deep-merged (see `mergeCompat` in
  `model-registry.ts`). The merged `Compat` retains the concrete type of the
  base (don't down-grade to a generic map).
- **Custom model** = a full `Model` with defaults filled
  (`contextWindow: 128000`, `maxTokens: 16384`, `input: ["text"]`,
  `cost: {0,0,0,0}`, `api` from the provider's api).
- **Provider override** = replace `BaseURL`/`Headers`/`Auth` on a built-in
  provider; applied at registry assembly time.

Expose a helper `ApplyModelOverride(base Model, ov ModelOverride) Model` in
`models.go` so the coding-agent layer doesn't reimplement the merge. Validate
`contextWindow > 0` when provided.

---

## 12. Concurrency Model (Go-specific)

- **Registry**: `sync.RWMutex`. Reads (`Providers`, `Models`, `Model`) take R;
  `SetProvider`/`DeleteProvider` take W. `Models()` snapshots a slice under R
  lock and returns it; don't hold the lock while callers iterate a provider's
  models (copy the slice).
- **Provider refresh**: `sync.Mutex` + single-flight future; see §5.2.
- **CredentialStore.Modify**: per-provider serialization; see §7.1.
- **OAuth refresh**: double-checked locking inside `Modify`; see §7.5.
- **EventStream**: single producer goroutine, single or multiple consumers of
  `Events()` (fan-out is a consumer concern; the stream itself is single-consumer
  in practice — document it as such, and if fan-out is needed, provide a
  `Tee()` helper). `Result()` is safe to read concurrently with ranging.
- **context.Context** is threaded through `Stream`/`Complete`/`Auth`/`Refresh`.
  Cancellation produces an `aborted` error event, never a panic.
- **No goroutine leaks**: every producing goroutine must either reach a
  terminal event or observe `ctx.Done()` and emit `aborted` then return.
  `EventStream.End` closes channels; `Push` after end is a no-op.

---

## 13. JSON and Persistence

- `Model`, `Message`, `AssistantMessage`, `Usage`, `Credential` are JSON-tagged
  for persistence (auth.json, session transcripts, models.json). Field names
  match the TS camelCase JSON to stay wire-compatible with existing artifacts.
- `Model.Compat` uses a discriminated-union JSON adapter keyed on `Model.API`
  (or an embedded `_kind` field) to unmarshal into the right concrete compat
  type.
- `UserMessage.Content` (`string | []block`) uses a custom `UserContent` type
  with `MarshalJSON`/`UnmarshalJSON`.
- `ChatTemplateKwargValue` uses a custom marshaler for the scalar vs `$var`
  shapes.

---

## 14. Testing Strategy

- **Unit**: `compat.DetectCompat` over a table of `(provider, baseUrl, modelID)`
  -> expected compat; `CalculateCost` incl. Anthropic 2x long-write;
  `ClampThinkingLevel` / `SupportedThinkingLevels` over reasoning + level-map
  matrices; `IsContextOverflow` for both signals; `ApplyModelOverride` deep
  merge.
- **Drivers**: each driver tested against a fake HTTP/SSE server
  (`httptest.Server` returning canned SSE chunks) covering text, thinking,
  tool-call streaming, usage parsing, finish reasons, and error events. No real
  provider APIs or keys.
- **Auth**: `ResolveProviderAuth` with a fake `CredentialStore` and
  `AuthContext` — covers stored-key, env fallback, OAuth happy path, expired
  refresh under contention (two concurrent resolves share one refresh; assert
  `Refresh` called once), and refresh failure (`ErrOAuth`).
- **Registry**: `Stream` emits a terminal error event for unknown provider /
  unconfigured auth / no driver for api (assert via `Result()` and ranging
  `Events()`).
- A faux provider (like `packages/ai/src/providers/faux.ts`) for the
  coding-agent test harness: a `Driver` that echoes canned events from a
  script.

---

## 15. Migration / Parity Checklist

Port in this order to stay testable at each step:

1. `types.go`, `errors.go` — pure types.
2. `api/stream.go` (`EventStream`, `LazyStream`) — test with a fake producer.
3. `compat/*` — `DetectCompat`/`GetCompat` and merge logic; table-testable.
4. `auth/*` — `EnvApiKeyAuth`, `ResolveProviderAuth`, in-memory store; fake
   OAuth.
5. `provider.go`, `models.go` — `Registry`, `NewProvider`, cost/thinking
   helpers; wire a faux driver.
6. `api/openai_completions.go` + SSE reader — first real driver; fake-server
   tests.
7. `catalog/` codegen + the 35 providers.
8. Remaining drivers (anthropic, google*, bedrock, mistral, responses, codex,
   azure).
9. `api/overflow.go`, `api/transform.go`, `api/simple.go` — exercised via
   drivers.

Each step has a self-contained test target and no dependency on later steps.

---

## 16. Open Questions

1. **HTTP client**: build on `net/http` + a shared SSE reader, or adopt a
   vendor SDK per protocol (e.g. `github.com/anthropics/anthropic-sdk-go`)?
   Recommendation: `net/http` + shared SSE for uniformity, fewer deps, and
   parity with the SDK-agnostic event protocol. Vendor SDKs would re-introduce
   the per-provider code path the compat layer is designed to eliminate.
2. **WebSocket transports** (`Transport: websocket`): needed by a subset of
   providers. Implement behind the same `Driver` interface with a transport
   switch; defer until a provider actually requires it.
3. **Typed per-API options**: uniform `Driver` + `Extra` bag (§9.3) vs typed
   driver constructors. Revisit after the first driver lands.
4. **Image generation** (`ImagesModels`): out of scope here, but the same
   `Registry`/`Provider`/`Driver`/`Compat` shapes generalize; design when
   needed.
