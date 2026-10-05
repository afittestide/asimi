# Model Selection and LLM Configuration

Asimi supports multiple AI providers and models. You can switch between them
using the :models command (alias :model) or configure them via environment
variables.

## Selecting a Model

  :models          - Open the live model picker (alias :model)
                     Enumerates models from every configured provider
  :models refresh  - Force a live refetch, bypassing the session cache

  :model <provider>/<name>   - Select a model directly, e.g. :model openrouter/anthropic/claude-3.5-sonnet
  :model <provider> <name>   - Same, space-separated
  :model <name>              - Uses the current provider

The catalog is fetched once and cached in memory for the session, so reopening
the picker is instant and makes no network calls. Use `:models refresh` (alias
`:model refresh`) when you need to see a newly pulled model or a provider that
was briefly unreachable.

The model picker displays:
  - Real models enumerated live from each provider's /v1/models
  - Useful metadata per row (context window, price)
  - The currently selected provider's models first, followed by the other
    providers in alphabetical order
  - Current active model (marked with ✓, but not hoisted to the top)
  - Provider icons (🅰️  Anthropic, 🤖 OpenAI, 🔷 Google, 🦙 Ollama)
  - A dim "unavailable:" line under the title when a provider can't be reached

Context sizes are shown as `200K ctx` when the provider reported them. A
leading `~`, e.g. `~200K ctx`, marks an approximate value filled in from
Asimi's local registry because the provider's model listing carried no context
length; it is an estimate, not an exact window.

Some providers report rich metadata (OpenRouter supplies pricing and context),
while plain OpenAI-compatible endpoints report only model ids. The picker does
not warn about the missing metadata — it simply shows fewer columns.

Navigation:
  ↓/↑              - Navigate through models
  /                - Search forward (vi-style); ? searches backward
  Enter            - Select model
  ESC              - Cancel

Typing a model name is only a last resort: the picker always ends with a
"Type a model name…" row that opens an input prompt for `provider/model`.

Unreachable providers appear as a diagnostic line (e.g.
`unavailable: openrouter (401)`) rather than a typing prompt — the picker still
shows every model from the providers that did respond.

## Supported Providers

### Anthropic (Claude)
Models: claude-3-5-sonnet-latest, claude-3-opus-latest, etc.

Authentication options:
  1. OAuth (recommended): Use :models to authenticate
  2. API Key via environment: ANTHROPIC_API_KEY=sk-ant-...
  3. API Key via keyring: Stored securely in OS keyring

Custom endpoint:
  ANTHROPIC_BASE_URL=https://custom-endpoint.com

### Vertex AI (Google Cloud)
Models: gemini-3.7-flash, gemini-2.5-pro, etc.

Authentication uses gcloud Application Default Credentials (ADC), configured
out-of-band — no API key prompt and no VERTEX_API_KEY.

Setup:
  1. gcloud auth application-default login
  2. export GOOGLE_CLOUD_PROJECT=<your-project-id>

Optionally set GOOGLE_CLOUD_REGION (defaults to "global") and
GOOGLE_APPLICATION_CREDENTIALS (path to a service-account JSON) instead of
relying on default ADC.

### Ollama (Local Models)
Models: Any model you've pulled locally (llama2, codellama, etc.)

Setup:
  1. Install Ollama: https://ollama.ai
  2. Pull a model: ollama pull llama2
  3. Asimi will auto-detect running Ollama instance

Custom host:
  OLLAMA_HOST=http://localhost:11434

## Environment Variables

### API Keys
  ANTHROPIC_API_KEY     - Anthropic API key

### Vertex AI (Google Cloud)
  GOOGLE_CLOUD_PROJECT  - GCP project ID (required)
  GOOGLE_CLOUD_REGION   - GCP region (optional, defaults to "global")
  GOOGLE_APPLICATION_CREDENTIALS - Service-account JSON path or JSON (optional)

### Base URLs (for custom endpoints)
  ANTHROPIC_BASE_URL    - Custom Anthropic endpoint
  OLLAMA_HOST           - Ollama server URL

Base URLs are the **host root** and must NOT include `/v1` — asimi passes the
URL to the provider SDK, which appends `/v1/chat/completions` itself. A `/v1`
suffix would be doubled and return a misleading `404 Not Found`. A trailing
slash and a missing `https://` scheme are normalized automatically.

### OAuth Tokens (Advanced)
  ANTHROPIC_OAUTH_TOKEN - Override stored OAuth token
  # Note: ANTHROPIC_CLIENT_ID, ANTHROPIC_CLIENT_SECRET, etc. are not yet implemented

## Configuration File

You can also configure models in ~/.config/asimi/asimi.conf:

  [llm]
  provider = "anthropic"
  model = "claude-3-5-sonnet-latest"
  api_key = "sk-ant-..."
  base_url = "https://api.anthropic.com"

## Authentication Methods

### 1. API Keys via Environment
  export ANTHROPIC_API_KEY=sk-ant-...
  asimi

### 2. API Keys via Keyring
Asimi automatically stores API keys in your OS keyring when you login
or when configured in the config file.

## Switching Providers

To switch between providers:
  1. Use :models to see all available models
  2. Select a model from a different provider
  3. Asimi will automatically switch providers

Or configure directly in ~/.config/asimi/asimi.conf:
  [llm]
  provider = "openai"

## Custom Endpoints

For custom or self-hosted LLM endpoints:

  # OpenAI-compatible endpoint
  OPENAI_BASE_URL=https://my-llm.company.com
  OPENAI_API_KEY=my-key
  asimi

  # Anthropic-compatible endpoint
  ANTHROPIC_BASE_URL=https://my-claude.company.com
  ANTHROPIC_API_KEY=my-key
  asimi

## Troubleshooting

### No models available
  - Check authentication: :login or set API keys
  - Verify network connectivity
  - Check provider status pages

### Model not found
  - Use :models to see available models
  - Check provider documentation for model names
  - Verify API key has access to the model

### Authentication errors
  - Re-authenticate: :login
  - Check API key validity
  - Verify environment variables are set correctly

### Ollama not detected
  - Ensure Ollama is running: ollama serve
  - Check OLLAMA_HOST is correct
  - Verify you've pulled at least one model

## Examples

  # Use Claude with API key
  :login
  # Select Anthropic, enter API key
  :models
  # Select claude-3-5-sonnet-latest

  # Use local Ollama
  ollama pull llama2
  asimi
  :models
  # Select llama2

  # Use Vertex AI via gcloud ADC
  gcloud auth application-default login
  export GOOGLE_CLOUD_PROJECT=my-project
  asimi
  # In asimi.conf: provider = "vertex", model = "gemini-3.7-flash"

  # Use custom endpoint
  export OPENAI_BASE_URL=https://my-llm.company.com
  export OPENAI_API_KEY=my-key
  asimi
