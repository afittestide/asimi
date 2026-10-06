# Context and Token Usage

Asimi tracks token usage to help you stay within model limits and manage
conversation context effectively.

## Viewing Context

  :context         - Show detailed context information

The context view shows:
  - Current token usage
  - Maximum token limit
  - Percentage used
  - Number of messages
  - Loaded files and their sizes

## Token Limits

Different models have different context limits:
  - Claude Sonnet: 200K tokens
  - GPT-4: 128K tokens
  - Gemini Pro: 1M tokens

The window is **provider-reported when available** — Asimi asks the provider
for the model's real context length and treats that as the source of truth.
When the provider reports nothing, Asimi falls back to a local registry
estimate; an estimate is rendered with a leading `~` (e.g. `~128.0k`) so it
is never shown as exact.

Asimi automatically tracks usage and warns when approaching limits.

## Automatic Overflow Recovery

If a request is rejected because the prompt exceeds the model's context
window, Asimi does not halt. It:

1. Learns the true window from the provider's error message.
2. Compacts the conversation with a summary request that is bounded to fit
   within that window (older and large messages are trimmed as needed).
3. Re-sends your message once.

If recovery still cannot fit, the session stays alive and you get an
actionable hint: `context window N exceeded — use :compact or :new`.

## Managing Context

When context gets too large:

1. Compact the conversation:
   :compact

2. Start a new session:
   :new

3. Export current conversation:
   :export

4. Reference only essential files:
   Use @ selectively

5. Use shorter prompts:
   Be concise in your requests

## Context Files

Files referenced with @ are loaded into context:
  @main.go         - Adds main.go to context

View loaded files:
  :context         - Shows all files in context

## Token Counting

Asimi counts tokens for:
  - Your prompts
  - AI responses
  - System messages
  - File contents
  - Tool calls and results

## Best Practices

1. Monitor context usage regularly with :context
2. Start fresh sessions for new tasks
3. Reference files only when needed
4. Export important conversations before starting new sessions
