# Provider Authentication

Use the :login command to authenticate with an AI provider. This is separate
from :models, which only shows models you can already access.

## Using :login

  :login           - Open provider selection list

The login list shows:
  - Login with OpenAI (Codex OAuth) — browser-based OAuth flow
  - Set API key for Anthropic — prompts for an API key
  - Set API key for Google AI — prompts for an API key
  - Set API key for OpenRouter — prompts for an API key

Navigation:
  ↓/↑              - Navigate through providers
  Enter            - Select provider and authenticate
  ESC              - Cancel

## OpenAI (Codex OAuth)

Selecting "Login with OpenAI (Codex OAuth)" opens a browser window to complete
the OAuth flow. A local callback server on port 1455 receives the authorization
code. After successful login, your credentials are stored securely in the
OS keyring and the models list is refreshed.

Requirements:
  - A browser available on your machine
  - Port 1455 accessible on localhost

## API Key Providers

For Anthropic, Google AI, and OpenRouter, selecting the entry prompts you to
enter an API key. The key is stored in the OS keyring (not in plaintext config)
and the models list is refreshed.

## Vertex AI (gcloud ADC)

Vertex AI requires no login within Asimi. It authenticates using gcloud
Application Default Credentials (ADC), configured out-of-band:

  gcloud auth application-default login
  gcloud config set project <your-project-id>

The project is read from GOOGLE_CLOUD_PROJECT when set, otherwise from
`gcloud config get-value project`.

Optionally set GOOGLE_CLOUD_REGION (defaults to "global") and
GOOGLE_APPLICATION_CREDENTIALS (service-account JSON path or JSON). No
VERTEX_API_KEY is used.

## Environment Variables

You can also set API keys via environment variables instead of :login:

  ANTHROPIC_API_KEY   - Anthropic API key
  OPENAI_API_KEY      - OpenAI API key (plain key, not OAuth)
  GEMINI_API_KEY      - Google Gemini API key
  OPENROUTER_API_KEY  - OpenRouter API key
  GOOGLE_CLOUD_PROJECT - GCP project ID for Vertex AI (gcloud ADC)

## Logging Out

Use :logout to clear stored credentials for the current provider:

  :logout

## After Login

Once authenticated, use :models to select and switch between available models:

  :models

## Troubleshooting

### OAuth flow times out
  - Ensure port 1455 is not in use
  - Check your browser opens correctly
  - Complete the flow within 5 minutes

### API key not accepted
  - Verify the key is valid and active
  - Check for trailing whitespace or newlines
  - Try logging out first with :logout, then :login again
