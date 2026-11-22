# Providers & Login

> *"The wise man does not lay up his own treasures."* — Laozi

Asimi is multi-model: it can talk to several AI providers, and you can switch
between them per session. This chapter covers connecting to one. Selecting a
specific model is the next chapter.

## The two commands

| Command | What it does |
|---------|--------------|
| `:login` | Authenticate with a provider (store credentials) |
| `:models` | Pick from the models you can already access |

They are **separate**. `:login` establishes access; `:models` chooses a model
among the providers you have access to.

## Supported providers

| Provider | Auth |
|----------|------|
| **Anthropic** (Claude) | OAuth, API key (env or keyring) |
| **OpenAI** (GPT) | Codex OAuth, or `OPENAI_API_KEY` |
| **Google AI / Gemini** | API key |
| **Vertex AI** (Google Cloud) | gcloud Application Default Credentials |
| **Ollama** (local) | none — auto-detected |
| **OpenRouter**, **Bedrock**, **Azure**, **custom** | provider-specific |

## Logging in

Run `:login` to open the provider list:

```
:login
```

- **Login with OpenAI (Codex OAuth)** — opens a browser; a local callback server
  on port `1455` receives the authorization. Requires a browser and port 1455
  to be free.
- **Set API key for Anthropic / Google AI / OpenRouter** — prompts for a key,
  which is stored in your **OS keyring** (never in plaintext config).

After a successful login, the model list is refreshed.

## Logging out

```
:logout
```

Clears the stored credentials for the current provider.

## Vertex AI — no login inside Asimi

Vertex authenticates **out-of-band** using gcloud Application Default
Credentials. There is no `:login` step and no `VERTEX_API_KEY`:

```bash
gcloud auth application-default login
gcloud config set project <your-project-id>
```

The project is read from `GOOGLE_CLOUD_PROJECT` when set, otherwise from
`gcloud config get-value project`. Optionally set `GOOGLE_CLOUD_REGION`
(default `global`) and `GOOGLE_APPLICATION_CREDENTIALS`.

## Environment variables as an alternative

Instead of `:login`, you may set API keys in the environment:

| Variable | Provider |
|----------|----------|
| `ANTHROPIC_API_KEY` | Anthropic |
| `ANTHROPIC_OAUTH_TOKEN` | Anthropic (priority over keyring) |
| `OPENAI_API_KEY` | OpenAI |
| `GEMINI_API_KEY` | Google Gemini |
| `OPENROUTER_API_KEY` | OpenRouter |
| `GOOGLE_CLOUD_PROJECT` | Vertex AI (gcloud ADC) |

> **Full env-var catalog:** the environment variable list also includes
> system variables (`EDITOR`, `SHELL`), OAuth overrides, and development
> switches. See [Config Files](../configuration/config.md).

## Troubleshooting

- **OAuth times out** — ensure port `1455` is free and the browser opened.
- **API key not accepted** — check for trailing whitespace; try `:logout` then
  `:login` again.
- **Ollama not detected** — ensure `ollama serve` is running and you have pulled
  at least one model; check `OLLAMA_HOST`.

Next: choosing the model itself.
