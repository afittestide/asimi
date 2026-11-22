# Context

> *"The eye is not satisfied with seeing, nor the ear filled with hearing."*
> — Ecclesiastes 1:8

Every model has a limited **context window** — the amount of text it can hold in
mind at once. Asimi tracks token usage so you stay within that limit and manage
the conversation deliberately.

## Viewing context

```
:context         Show detailed context information
```

The context view shows token usage, the maximum limit, the percentage used,
the number of messages, and the files currently loaded.

## Where the window comes from

The window is **provider-reported when available** — Asimi asks the provider for
the model's real context length and treats that as the source of truth. When
the provider reports nothing, Asimi falls back to a local registry *estimate*,
always rendered with a leading `~` (e.g. `~128.0k`) so an estimate is never
shown as exact. See [Selecting a Model](../models/selection.md).

## Automatic overflow recovery

If a request is rejected because the prompt exceeds the model's window, Asimi
does **not** halt. It:

1. learns the true window from the provider's error message,
2. compacts the conversation with a bounded summary (older and large messages
   are trimmed as needed), and
3. re-sends your message once.

If recovery still cannot fit, the session stays alive and you get an actionable
hint: `context window N exceeded — use :compact or :new`.

## Managing context

When context grows large:

1. **Compact** the conversation: `:compact`
2. **Start fresh**: `:new`
3. **Export** before you do either: `:export`
4. **Reference only essential files** with `@`
5. **Be concise** in your requests

## Context files

Files referenced with `@` are loaded into context. `:context` lists them.

## What Asimi counts

Tokens are counted for your prompts, the AI's responses, system messages, file
contents, and tool calls and results.

## Best practices

1. Watch usage with `:context`
2. Start a fresh session for each new task
3. Reference files only when needed
4. Export important conversations before starting new ones

Next: the sandbox.
