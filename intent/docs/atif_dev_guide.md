# ATIF — Agent Tool Interaction Format

## Developer Guide for Coding Agents

### What is ATIF?

ATIF (Agent Tool Interaction Format) is a standardized JSONL format for recording coding agent trajectories. It captures the full lifecycle of an agent session: model interactions, tool calls, token usage, and turn structure. ATIF is consumed by evaluation frameworks like Harbor for benchmarking, reward computation, and trajectory replay.

### File Location

A coding agent should write its trajectory as:

```
agent/<agent_name>.txt          # Denormalized, aggregated trajectory (JSONL)
agent/<agent_name>/sessions/    # Raw session files (JSONL, one per session)
```

The `agent/<agent_name>.txt` file is the primary artifact consumed by the evaluation framework. The raw session files under `sessions/` are supplementary.

### Configuration Fields

In the agent's config section (lock.json / result.json), include:

```json
"agent": {
    "name": "my-agent",
    "model_name": "provider/model",
    "resume_trajectory": false,
    "load_trajectory": null,
    ...
}
```

| Field | Type | Description |
|---|---|---|
| `resume_trajectory` | bool | Whether to resume from a previous trajectory |
| `load_trajectory` | string? | Path to a trajectory file to load/resume from |

### Event Schema

Every event is a JSON object on its own line (JSONL format). Events form a tree connected via `id` and `parentId`. The required fields on every event are:

| Field | Type | Description |
|---|---|---|
| `type` | string | Event type identifier |
| `id` | string | Unique event ID |
| `parentId` | string? | Parent event ID (null for root) |
| `timestamp` | string | ISO 8601 timestamp |

### Event Types

#### 1. `session`

The root event that opens a trajectory. Must be the first line.

```json
{
    "type": "session",
    "version": 3,
    "id": "019fcbc3-2342-7cee-be29-4db5aba0667c",
    "timestamp": "2026-08-04T07:53:11.235Z",
    "cwd": "/app"
}
```

| Field | Required | Description |
|---|---|---|
| `version` | yes | Schema version (currently 3) |
| `id` | yes | Session UUID |
| `timestamp` | yes | ISO 8601 |
| `cwd` | yes | Working directory inside the environment |

#### 2. `agent_start`

Marks the start of agent execution. No additional fields.

```json
{"type": "agent_start"}
```

#### 3. `turn_start`

Marks the beginning of a turn (one LLM request-response cycle).

```json
{"type": "turn_start"}
```

#### 4. `message_start`

Sent when a message begins. For user messages this is the full message. For assistant messages, it may be empty (pending) while streaming begins.

```json
{
    "type": "message_start",
    "message": {
        "role": "user",
        "content": [
            {"type": "text", "text": "User's prompt..."}
        ]
    },
    "timestamp": 1785829991417
}
```

For assistant messages, the `message` object includes API metadata:

```json
{
    "type": "message_start",
    "message": {
        "role": "assistant",
        "content": [],
        "api": "openai-completions",
        "provider": "openrouter",
        "model": "qwen/qwen3.7-flash",
        "usage": {
            "input": 0, "output": 0,
            "cacheRead": 0, "cacheWrite": 0,
            "totalTokens": 0,
            "cost": {
                "input": 0, "output": 0,
                "cacheRead": 0, "cacheWrite": 0, "total": 0
            }
        },
        "stopReason": "pending",
        "errorMessage": null
    },
    "timestamp": 1785829991587
}
```

For tool result messages:

```json
{
    "type": "message_start",
    "message": {
        "role": "toolResult",
        "toolCallId": "call_...",
        "toolName": "read",
        "content": [
            {"type": "text", "text": "..."}
        ],
        "isError": false
    },
    "timestamp": 1785829992903
}
```

#### 5. `message_end`

Sent when a message completes. Contains the final content and metadata.

For assistant messages — contains the final content, usage, and stop reason:

```json
{
    "type": "message_end",
    "message": {
        "role": "assistant",
        "content": [
            {"type": "thinking", "thinking": "...", "thinkingSignature": "reasoning"},
            {"type": "text", "text": "Response text..."},
            {
                "type": "toolCall",
                "id": "call_...",
                "name": "read",
                "arguments": {"path": "/app/file.py"}
            }
        ],
        "api": "openai-completions",
        "provider": "openrouter",
        "model": "qwen/qwen3.7-flash",
        "usage": {
            "input": 1666, "output": 74,
            "cacheRead": 0, "cacheWrite": 0,
            "reasoning": 17,
            "totalTokens": 1740,
            "cost": {
                "input": 0.00004998, "output": 0.00000962,
                "cacheRead": 0, "cacheWrite": 0, "total": 0.00005960
            }
        },
        "stopReason": "toolUse",
        "responseId": "gen-...",
        "rawStopReason": "tool_calls"
    },
    "timestamp": 1785829991587
}
```

| Field | Type | Description |
|---|---|---|
| `message.role` | string | `"user"`, `"assistant"`, or `"toolResult"` |
| `message.content` | array | Content blocks (text, thinking, toolCall, toolResult) |
| `message.api` | string? | API format used |
| `message.provider` | string? | Provider name |
| `message.model` | string? | Model name |
| `message.usage` | object? | Token usage breakdown |
| `message.usage.input` | int | Input tokens |
| `message.usage.output` | int | Output tokens (excluding reasoning) |
| `message.usage.cacheRead` | int | Cached input tokens read |
| `message.usage.cacheWrite` | int | Cached input tokens written |
| `message.usage.reasoning` | int | Reasoning tokens |
| `message.usage.totalTokens` | int | Total tokens |
| `message.usage.cost` | object | Cost breakdown per component |
| `message.usage.cost.total` | float | Total cost in USD |
| `message.stopReason` | string | Why generation stopped |
| `message.responseId` | string? | Response identifier from API |
| `message.rawStopReason` | string? | Raw stop reason |

**Valid `stopReason` values:** `"toolUse"`, `"stop"`, `"maxTokens"`, `"error"`, `"pending"`

**Valid `message.role` values:** `"user"`, `"assistant"`, `"toolResult"`

#### 6. `tool_execution_start`

Marks when a tool call begins executing.

```json
{
    "type": "tool_execution_start",
    "toolCallId": "call_...",
    "toolName": "read",
    "args": {"path": "/app/filter.py"}
}
```

| Field | Required | Description |
|---|---|---|
| `toolCallId` | yes | Matches the toolCall id from the assistant message |
| `toolName` | yes | Name of the tool |
| `args` | yes | Arguments passed to the tool |

#### 7. `tool_execution_update`

Sent when a tool produces partial/streaming output.

```json
{
    "type": "tool_execution_update",
    "toolCallId": "call_...",
    "toolName": "bash",
    "args": {"command": "ls /app/"},
    "partialResult": {
        "content": [
            {"type": "text", "text": "filter.py\ntest_outputs.py\n"}
        ],
        "details": {}
    }
}
```

| Field | Required | Description |
|---|---|---|
| `partialResult` | yes | Partial content from the tool |
| `partialResult.content` | yes | Array of content blocks |
| `partialResult.details` | no | Optional metadata |

#### 8. `tool_execution_end`

Marks the completion of a tool call.

```json
{
    "type": "tool_execution_end",
    "toolCallId": "call_...",
    "toolName": "read",
    "result": {
        "content": [
            {"type": "text", "text": "..."}
        ]
    },
    "isError": false
}
```

| Field | Required | Description |
|---|---|---|
| `result` | yes | Final result object |
| `result.content` | yes | Array of content blocks |
| `isError` | yes | Boolean: whether the tool returned an error |

#### 9. `turn_end`

Marks the end of a turn. Contains the full assistant message with all tool results.

```json
{
    "type": "turn_end",
    "message": {
        "role": "assistant",
        "content": [...],
        "api": "openai-completions",
        "provider": "openrouter",
        "model": "qwen/qwen3.7-flash",
        "usage": {...},
        "stopReason": "toolUse"
    },
    "toolResults": [
        {
            "role": "toolResult",
            "toolCallId": "call_...",
            "toolName": "read",
            "content": [{"type": "text", "text": "..."}],
            "isError": false,
            "timestamp": 1785829992903
        }
    ]
}
```

| Field | Required | Description |
|---|---|---|
| `toolResults` | yes | Array of all tool results from this turn |

#### 10. `model_change`

Records a change in model or provider.

```json
{
    "type": "model_change",
    "id": "0b7760c5",
    "parentId": null,
    "timestamp": "2026-08-04T07:53:11.394Z",
    "provider": "openrouter",
    "modelId": "qwen/qwen3.7-flash"
}
```

#### 11. `thinking_level_change`

Records a change in thinking level.

```json
{
    "type": "thinking_level_change",
    "id": "057bce8c",
    "parentId": "0b7760c5",
    "timestamp": "2026-08-04T07:53:11.394Z",
    "thinkingLevel": "medium"
}
```

### Turn Lifecycle

The typical turn lifecycle in the trajectory:

```
turn_start
  → message_start (user message)
  → message_end   (user message)
  → message_start (assistant message, empty/pending)
  → message_end   (assistant message with tool calls)
  → tool_execution_start (tool 1)
  → tool_execution_update (tool 1, streaming)
  → tool_execution_end   (tool 1)
  → tool_execution_start (tool 2)
  → tool_execution_update (tool 2, streaming)
  → tool_execution_end   (tool 2)
  → message_start (toolResult 1)
  → message_end   (toolResult 1)
  → message_start (toolResult 2)
  → message_end   (toolResult 2)
turn_end
```

### Multiple Turns

Agents can have multiple turns. Each turn follows the same lifecycle pattern. The last turn in the sequence is the final answer.

### Error Handling

When an API error occurs, the assistant message includes an `errorMessage`:

```json
{
    "type": "message_end",
    "message": {
        "role": "assistant",
        "content": [],
        "stopReason": "error",
        "errorMessage": "429: Provider returned error..."
    }
}
```

### Content Block Types

Within `message.content`, each block has a `type`:

| Type | Description |
|---|---|
| `"text"` | Plain text content |
| `"thinking"` | Model reasoning/thinking (has `thinking` and `thinkingSignature` fields) |
| `"toolCall"` | A tool invocation request (has `id`, `name`, `arguments` fields) |
| `"toolResult"` | A tool result (has `toolCallId`, `toolName`, `content` fields) |

### Example: Minimal Agent Trajectory

```jsonl
{"type":"session","version":3,"id":"sess-001","timestamp":"2026-08-04T07:53:11.235Z","cwd":"/app"}
{"type":"agent_start"}
{"type":"turn_start"}
{"type":"message_start","message":{"role":"user","content":[{"type":"text","text":"Hello"}],"timestamp":1000}}
{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"Hello"}],"timestamp":1000}}
{"type":"message_start","message":{"role":"assistant","content":[],"usage":{"input":10,"output":0,"totalTokens":10},"stopReason":"pending","timestamp":1100}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hi there!"}],"usage":{"input":10,"output":5,"totalTokens":15},"stopReason":"stop","timestamp":1200}}
{"type":"turn_end","message":{...},"toolResults":[]}
```

### Schema Version History

| Version | Notes |
|---|---|
| 1 | Initial format |
| 2 | Added tool_execution_start/end events |
| 3 | Added turn_start/turn_end, message_start/message_end, tool_execution_update, cost tracking, thinking blocks |

---

*For questions, consult the Harbor evaluation framework source or file an issue.*

