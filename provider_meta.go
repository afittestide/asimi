// Package main — provider_meta.go holds the provider metadata table and
// convention-based helpers (env var names, auth checks) that replace
// scattered switch statements across the codebase.

package main

import (
	"os"
	"strings"

	"github.com/afittestide/asimi/internal/keyring"
	"github.com/afittestide/asimi/internal/utils"
)

// AuthType classifies how a provider authenticates.
type AuthType string

const (
	AuthTypeAPIKey  AuthType = "apikey"
	AuthTypeOAuth   AuthType = "oauth"
	AuthTypeKeyless AuthType = "keyless"
	AuthTypeGCloud  AuthType = "gcloud"
)

// ProviderMeta holds asimi-specific metadata that cannot be derived by
// convention. Everything else (env var names, base URL env vars) is
// convention-based: strings.ToUpper(provider) + "_API_KEY", etc.
type ProviderMeta struct {
	DisplayName string   // Human-readable name for the UI
	Icon        string   // Emoji icon for the model selection UI
	AuthType    AuthType // How this provider authenticates
}

// providerMeta is the single metadata table replacing 6 scattered switch
// statements. Only fields that cannot be derived by convention live here.
//
// Provider keys use asimi's naming convention (e.g., "googleai" for Google
// AI / Gemini). The convention-based helpers translate between asimi keys
// and bifrost provider keys where they differ.
var providerMeta = map[string]ProviderMeta{
	"anthropic":  {DisplayName: "Anthropic", Icon: "🅰️ ", AuthType: AuthTypeAPIKey},
	"openai":     {DisplayName: "OpenAI", Icon: "🤖", AuthType: AuthTypeOAuth},
	"googleai":   {DisplayName: "Google AI", Icon: "🔷", AuthType: AuthTypeAPIKey},
	"openrouter": {DisplayName: "OpenRouter", Icon: "🔀", AuthType: AuthTypeAPIKey},
	"ollama":     {DisplayName: "Ollama", Icon: "🦙", AuthType: AuthTypeKeyless},
	"bedrock":    {DisplayName: "AWS Bedrock", Icon: "☁️ ", AuthType: AuthTypeAPIKey},
	"vertex":     {DisplayName: "Vertex AI", Icon: "🗻", AuthType: AuthTypeGCloud},
	"azure":      {DisplayName: "Azure OpenAI", Icon: "🅱️ ", AuthType: AuthTypeAPIKey},
	"cohere":     {DisplayName: "Cohere", Icon: "🔗", AuthType: AuthTypeAPIKey},
	"mistral":    {DisplayName: "Mistral AI", Icon: "🌬️", AuthType: AuthTypeAPIKey},
	"groq":       {DisplayName: "Groq", Icon: "⚡", AuthType: AuthTypeAPIKey},
	"perplexity": {DisplayName: "Perplexity", Icon: "🔍", AuthType: AuthTypeAPIKey},
	"xai":        {DisplayName: "xAI", Icon: "✖️ ", AuthType: AuthTypeAPIKey},
	"deepinfra":  {DisplayName: "DeepInfra", Icon: "🏗️ ", AuthType: AuthTypeAPIKey},
	"fireworks":  {DisplayName: "Fireworks AI", Icon: "🎆", AuthType: AuthTypeAPIKey},
	"together":   {DisplayName: "Together AI", Icon: "🤝", AuthType: AuthTypeAPIKey},
}

// getProviderMeta returns metadata for a provider, with sensible defaults
// for providers not explicitly listed.
func getProviderMeta(provider string) ProviderMeta {
	if meta, ok := providerMeta[provider]; ok {
		return meta
	}
	return ProviderMeta{
		DisplayName: provider,
		Icon:        "  ",
		AuthType:    AuthTypeAPIKey,
	}
}

// providerDisplayName returns a human-readable provider name from the metadata table.
func providerDisplayName(provider string) string {
	return getProviderMeta(provider).DisplayName
}

// getProviderIcon returns an icon for the provider from the metadata table.
func getProviderIcon(provider string) string {
	return getProviderMeta(provider).Icon
}

// providerAuthType returns the auth type for a provider from the metadata table.
func providerAuthType(provider string) AuthType {
	return getProviderMeta(provider).AuthType
}

// providerEnvVar returns the primary environment variable name for a provider's
// API key, using convention: strings.ToUpper(provider) + "_API_KEY".
// Special cases:
//   - googleai → GEMINI_API_KEY (historical convention; also checks GOOGLE_API_KEY)
//   - bedrock → AWS_ACCESS_KEY_ID (Bedrock uses AWS credential pairs, not a single key)
func providerEnvVar(provider string) string {
	switch provider {
	case "googleai":
		return "GEMINI_API_KEY"
	case "bedrock":
		return "AWS_ACCESS_KEY_ID"
	case "vertex":
		// Vertex authenticates via gcloud ADC, not a single API key.
		return "GOOGLE_CLOUD_PROJECT"
	default:
		return strings.ToUpper(provider) + "_API_KEY"
	}
}

// providerBaseURLEnvVar returns the environment variable name for a provider's
// base URL, using convention: strings.ToUpper(provider) + "_BASE_URL".
func providerBaseURLEnvVar(provider string) string {
	switch provider {
	case "googleai":
		return "GEMINI_BASE_URL"
	case "azure":
		return "AZURE_OPENAI_BASE_URL"
	default:
		return strings.ToUpper(provider) + "_BASE_URL"
	}
}

// providerBaseURLFromEnv returns the base URL from the provider's environment variable.
func providerBaseURLFromEnv(provider string) string {
	return os.Getenv(providerBaseURLEnvVar(provider))
}

// checkProviderAuth checks if a provider has credentials configured (env var or keyring).
// Uses convention-based env var resolution with the metadata table for auth type.
func checkProviderAuth(provider string) bool {
	// Keyless providers (e.g., Ollama) are always "authenticated"
	if providerAuthType(provider) == AuthTypeKeyless {
		return true
	}

	// gcloud ADC providers (Vertex AI) are authenticated when the gcloud
	// project is set; credentials are configured out-of-band.
	if providerAuthType(provider) == AuthTypeGCloud {
		return utils.GCloudProject() != ""
	}

	// Bedrock uses a credential pair
	if provider == "bedrock" {
		return os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != ""
	}

	// Check convention-based env var
	envVar := providerEnvVar(provider)
	if os.Getenv(envVar) != "" {
		return true
	}
	// Google AI also checks GOOGLE_API_KEY
	if provider == "googleai" && os.Getenv("GOOGLE_API_KEY") != "" {
		return true
	}

	// Check keyring
	apiKey, err := keyring.GetAPIKey(provider)
	if err != nil || apiKey == "" {
		return false
	}

	// For openai, the keyring value may be a JSON OAuth credential or a plain API key.
	// A value that is neither (e.g. JSON with an empty access token) must not
	// count as authenticated, otherwise model fetching will attempt API calls
	// with invalid credentials.
	if provider == "openai" {
		if _, ok := parseCodexOAuthCredential(apiKey); ok {
			return true // valid OAuth credential
		}
		// If it looks like JSON but failed to parse as a valid credential, reject
		if strings.HasPrefix(strings.TrimSpace(apiKey), "{") {
			return false
		}
		// Non-JSON, non-empty → treat as a plain API key
		return true
	}

	return true
}
