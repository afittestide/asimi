# Configuration

Asimi can be configured through configuration files and environment variables.

## Configuration Files

Asimi looks for configuration in this order:
  1. .agents/asimi.conf        (project-level)
  2. ~/.config/asimi/asimi.conf (user-level)

## Basic Configuration

[llm]
provider = "anthropic"           # AI provider
model = "claude-sonnet-4-20250514"  # Model name
max_turns = 50                   # Max conversation turns
max_thinking_tokens = 10000      # Max thinking tokens for extended reasoning
max_tool_output = 50000          # Max tool output size in bytes

[session]
enabled = true                   # Enable session persistence
auto_save = true                 # Auto-save sessions
max_sessions = 50                # Max sessions to keep
max_age_days = 30                # Delete old sessions
list_limit = 20                  # Sessions shown in :resume

## Providers

Supported providers:
  - anthropic      (Claude models)
  - openai         (GPT models)
  - googleai       (Gemini models)
  - ollama         (Local models)
  - azure          (Azure OpenAI)
  - gemini         (Google Gemini)
  - openrouter     (OpenRouter)
  - bedrock        (AWS Bedrock)
  - vertex         (Google Vertex AI via gcloud ADC)
  - custom         (Custom OpenAI-compatible endpoint)

## Environment Variables

### System
  EDITOR                    - Text editor for :export
  SHELL                     - Shell for container sessions

### API Keys & Authentication
  ANTHROPIC_API_KEY         - Anthropic API key
  ANTHROPIC_OAUTH_TOKEN     - Anthropic OAuth token (priority over keyring)
  ANTHROPIC_BASE_URL        - Custom Anthropic endpoint
  OPENAI_API_KEY            - OpenAI API key
  GEMINI_API_KEY            - Google Gemini API key

### Vertex AI (Google Cloud)
  No VERTEX_API_KEY is used.
  Auth for Vertex uses gcloud Application Default Credentials (ADC):
   gcloud auth application-default login
  GOOGLE_CLOUD_PROJECT       - GCP project ID for Vertex AI (optional when gcloud config has a project)
  GOOGLE_CLOUD_REGION        - GCP region for Vertex AI (optional, defaults to "global")
  GOOGLE_APPLICATION_CREDENTIALS - Path to a service-account JSON, or the JSON itself (optional)

### OAuth Configuration (Advanced)
  GOOGLE_CLIENT_ID          - Google OAuth client ID
  GOOGLE_CLIENT_SECRET      - Google OAuth client secret
  GOOGLE_AUTH_URL           - Google OAuth authorization URL (optional)
  GOOGLE_TOKEN_URL          - Google OAuth token URL (optional)
  GOOGLE_OAUTH_SCOPES       - Google OAuth scopes (optional)
  
  OPENAI_CLIENT_ID          - OpenAI OAuth client ID
  OPENAI_CLIENT_SECRET      - OpenAI OAuth client secret
  OPENAI_AUTH_URL           - OpenAI OAuth authorization URL
  OPENAI_TOKEN_URL          - OpenAI OAuth token URL
  OPENAI_OAUTH_SCOPES       - OpenAI OAuth scopes (optional)

  # Note: Anthropic OAuth env vars (ANTHROPIC_CLIENT_ID, etc.) are not yet implemented

### Development
  ASIMI_KEYRING_SERVICE     - Override keyring service name
  ASIMI_SKIP_GIT_STATUS     - Skip git status checks
  ASIMI_VERSION             - Override version string

## Logging

Logs are stored in:
  ~/.local/share/asimi/asimi.log

Logs are automatically rotated.

## Example Configuration

[llm]
provider = "anthropic"
model = "claude-sonnet-4-20250514"
max_turns = 100
max_thinking_tokens = 10000
reasoning_effort = "medium"
max_tool_output = 50000

[session]
enabled = true
auto_save = true
max_sessions = 100
max_age_days = 60
list_limit = 30

## Changing Configuration

After editing configuration restart Asimi for changes to take effect.
