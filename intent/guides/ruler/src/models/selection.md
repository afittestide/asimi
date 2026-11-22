# Selecting a Model

> *"The right tool for the right task."*

Asimi can talk to many providers, each offering many models. This chapter is
about choosing one — and about how Asimi learns each model's true size.

## The model picker

```
:models          Open the live model picker (alias :model)
:models refresh  Force a live refetch, bypassing the session cache
```

The picker enumerates models **live** from each configured provider, shows
useful metadata per row (context window, price), and lists the current
provider's models first.

Navigation:

| Key | Action |
|-----|--------|
| `↓`/`↑` | Move through models |
| `/` | Search forward (`?` backward) |
| `Enter` | Select |
| `Esc` | Cancel |

## Selecting directly

```
:model openrouter/anthropic/claude-3.5-sonnet   provider/name
:model openai gpt-4o                             provider and name, space-separated
:model gpt-4o                                    a name, using the current provider
```

## Reading the picker

- A leading **`~`** marks an **estimate**: `~200K ctx` means the provider did not
  report a context length, so Asimi filled one in from its local registry. A
  plain `200K ctx` is provider-reported and exact.
- Unreachable providers appear as a diagnostic line, e.g.
  `unavailable: openrouter (401)`, rather than disappearing — the picker still
  shows every model from the providers that did respond.
- Plain OpenAI-compatible endpoints report only model ids; the picker shows
  fewer columns rather than warning.
- The picker always ends with a **"Type a model name…"** row, as a last resort.

## The cache

The catalog is fetched once and cached **in memory for the session**, so
reopening the picker is instant and makes no network calls. Use
`:models refresh` after a provider adds models, or after a provider that was
briefly unreachable recovers.

## How the context window is learned

Asimi prefers **provider-reported** context windows and treats them as the
source of truth. When a provider reports nothing, Asimi falls back to a local
registry *estimate*, always rendered with a leading `~` so an estimate is never
shown as exact. See [Context](../configuration/context.md).

## Switching providers

The provider follows the model. Selecting a model from a different provider
switches the court to it. You can also set it in config:

```toml
[llm]
provider = "openai"
```

## Troubleshooting

- **No models available** — check `:login` or your API keys, and network access.
- **Model not found** — use `:models` to see what is actually available; check
  the provider's naming; confirm your key has access to that model.

Next: configuration.
