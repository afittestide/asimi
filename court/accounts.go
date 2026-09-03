// Package court contains the core ministers and orchestration logic.
package court

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/afittestide/asimi/internal/keyring"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// BifrostLogger wraps slog.Logger to implement schemas.Logger
type BifrostLogger struct {
	logger *slog.Logger
}

func (l *BifrostLogger) log(level slog.Level, msg string, args ...any) {
	if len(args) > 0 {
		l.logger.Log(context.Background(), level, fmt.Sprintf(msg, args...))
	} else {
		l.logger.Log(context.Background(), level, msg)
	}
}
func (l *BifrostLogger) Debug(msg string, args ...any)          { l.log(slog.LevelDebug, msg, args...) }
func (l *BifrostLogger) Info(msg string, args ...any)           { l.log(slog.LevelInfo, msg, args...) }
func (l *BifrostLogger) Warn(msg string, args ...any)           { l.log(slog.LevelWarn, msg, args...) }
func (l *BifrostLogger) Error(msg string, args ...any)          { l.log(slog.LevelError, msg, args...) }
func (l *BifrostLogger) Fatal(msg string, args ...any)          { l.log(slog.LevelError, msg, args...) }
func (l *BifrostLogger) SetLevel(schemas.LogLevel)              {}
func (l *BifrostLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l *BifrostLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// NewBifrostLogger creates a BifrostLogger wrapping the provided slog.Logger
func NewBifrostLogger(logger *slog.Logger) *BifrostLogger {
	return &BifrostLogger{logger: logger}
}

// Account implements schemas.Account using the OS keyring for credential storage.
// When apiKeys is populated (sandbox/daemon mode), keys are read from the map
// first; providers absent from the map fall through to keyring for backward
// compatibility with in-process mode.
type Account struct {
	requestTimeout    int
	streamIdleTimeout int
	maxRetries        int
	baseURL           string
	apiKeys           map[string]string
	codexAccountID    string
}

// NewAccount creates a new Account implementation backed by the OS keyring.
func NewAccount(requestTimeout, streamIdleTimeout, maxRetries int, baseURL string) schemas.Account {
	return &Account{requestTimeout: requestTimeout, streamIdleTimeout: streamIdleTimeout, maxRetries: maxRetries, baseURL: baseURL}
}

// NewAccountWithKeys creates a new Account that reads API keys from the given
// map first, falling through to keyring for providers not present in the map.
// Environment-variable reads are removed from the primary path; the caller
// (typically the daemon) is responsible for populating the map from env vars
// or any other source.
func NewAccountWithKeys(requestTimeout, streamIdleTimeout, maxRetries int, baseURL string, apiKeys map[string]string, codexAccountID string) schemas.Account {
	return &Account{requestTimeout: requestTimeout, streamIdleTimeout: streamIdleTimeout, maxRetries: maxRetries, baseURL: baseURL, apiKeys: apiKeys, codexAccountID: codexAccountID}
}

// GetConfiguredProviders returns providers that have credentials configured.
// Iterates over schemas.StandardProviders, returning providers that have
// credentials in the apiKeys map or keyring. Bedrock is included when AWS
// credential pairs are present.
func (a *Account) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	var providers []schemas.ModelProvider

	for _, sp := range schemas.StandardProviders {
		providerStr := string(sp)

		// Bedrock uses the single AWS credential pair, not single API keys
		if providerStr == "bedrock" {
			if a.hasBedrockCredentials() {
				providers = append(providers, sp)
			}
			continue
		}
		// Vertex AI authenticates via gcloud ADC, not a single API key
		if providerStr == "vertex" {
			if a.hasVertexCredentials() {
				providers = append(providers, sp)
			}
			continue
		}
		if a.hasProviderCredentials(providerStr) {
			providers = append(providers, sp)
		}
	}
	// TODO: add local llama when port available

	return providers, nil
}

// hasBedrockCredentials checks for AWS credential pairs in the apiKeys map
// or environment variables.
func (a *Account) hasBedrockCredentials() bool {
	if a.apiKeys != nil {
		return a.apiKeys["AWS_ACCESS_KEY_ID"] != "" && a.apiKeys["AWS_SECRET_ACCESS_KEY"] != ""
	}
	return hasAWSEnvCredentials()
}

// hasProviderCredentials checks if a provider has credentials in the apiKeys
// map or keyring. For daemon mode (apiKeys != nil), only the map is checked.
// For in-process mode, keyring is checked as fallback.
func (a *Account) hasProviderCredentials(providerStr string) bool {
	if a.apiKeys != nil {
		// Daemon mode: check the apiKeys map
		if apiKey, ok := a.apiKeys[providerStr]; ok && apiKey != "" {
			return true
		}
		// Provider not in map — skip keyring fallback in daemon mode
		return false
	}

	// In-process mode: check keyring
	// Resolve the keyring key name (gemini → googleai for backward compat)
	krProvider := providerStr
	apiKey, err := keyring.GetAPIKey(krProvider)
	return err == nil && apiKey != ""
}

// hasAWSEnvCredentials checks if standard AWS environment variables are set.
// This enables Bedrock provider support without explicit key storage.
func hasAWSEnvCredentials() bool {
	return os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != ""
}

// hasVertexCredentials checks for gcloud Application Default Credentials in the
// apiKeys map (daemon mode) or environment variables (in-process mode). Project
// is the gating dimension; region may default to "global" per bifrost.
func (a *Account) hasVertexCredentials() bool {
	if a.apiKeys != nil {
		return a.apiKeys["GOOGLE_CLOUD_PROJECT"] != ""
	}
	return os.Getenv("GOOGLE_CLOUD_PROJECT") != ""
}

// vertexEnv reads a gcloud env value from the apiKeys map (daemon mode) or
// environment variables (in-process mode), with the same dual-path discipline
// as the AWS credential reads.
func (a *Account) vertexEnv(env string) string {
	if a.apiKeys != nil {
		return a.apiKeys[env]
	}
	return os.Getenv(env)
}

// readVertexCredentials reads the service-account JSON contents for Vertex
// from GOOGLE_APPLICATION_CREDENTIALS. The env var may hold the JSON directly
// or a path to a service-account file. Returns "" so default ADC is used when
// unset or unreadable — mirroring the all-gcloud-env design.
func (a *Account) readVertexCredentials() string {
	raw := a.vertexEnv("GOOGLE_APPLICATION_CREDENTIALS")
	if raw == "" {
		return ""
	}
	// If it's already JSON (starts with '{'), pass it through directly.
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return raw
	}
	// Otherwise treat it as a path to a credentials file (standard gcloud usage).
	data, err := os.ReadFile(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// GetKeysForProvider returns API keys or OAuth tokens for a given provider
func (a *Account) GetKeysForProvider(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
	providerStr := string(provider)

	// Primary path: read from the injected apiKeys map (sandbox/daemon mode).
	if a.apiKeys != nil {
		// Special handling for Vertex: gcloud ADC, not a single API key.
		if providerStr == "vertex" {
			return a.vertexKeys(), nil
		}

		// Special handling for Bedrock: check for AWS credentials in the map
		if providerStr == "bedrock" {
			accessKey := a.apiKeys["AWS_ACCESS_KEY_ID"]
			secretKey := a.apiKeys["AWS_SECRET_ACCESS_KEY"]
			if accessKey != "" && secretKey != "" {
				enabled := true
				key := schemas.Key{
					ID:      "bedrock_aws",
					Name:    "Bedrock AWS Credentials",
					Models:  []string{"*"},
					Weight:  1.0,
					Enabled: &enabled,
					BedrockKeyConfig: &schemas.BedrockKeyConfig{
						AccessKey: schemas.EnvVar{Val: accessKey},
						SecretKey: schemas.EnvVar{Val: secretKey},
					},
				}
				if sessionToken := a.apiKeys["AWS_SESSION_TOKEN"]; sessionToken != "" {
					key.BedrockKeyConfig.SessionToken = &schemas.EnvVar{Val: sessionToken}
				}
				return []schemas.Key{key}, nil
			}
			// No AWS credentials in map — return empty, don't fall through to keyring
			return []schemas.Key{}, nil
		}

		if apiKey, ok := a.apiKeys[providerStr]; ok && apiKey != "" {
			enabled := true
			return []schemas.Key{
				{ID: providerStr + "_apikey", Name: providerStr + " API Key", Value: schemas.EnvVar{Val: apiKey}, Models: []string{"*"}, Weight: 1.0, Enabled: &enabled},
			}, nil
		}
		// Provider is in the map but empty — skip keyring fallback; the
		// caller explicitly provided no key for this provider.
		if _, exists := a.apiKeys[providerStr]; exists {
			return []schemas.Key{}, nil
		}
	}

	// Fallback: keyring-backed path for in-process mode (providers not in the map).

	// Vertex via gcloud ADC survives the fallback path too; it never falls
	// through to the single-API-key keyring path.
	if providerStr == "vertex" {
		return a.vertexKeys(), nil
	}

	// Bedrock via keyring: only if AWS keys were injected into the map or
	// discovered through keyring. Env-var-only Bedrock is the client's
	// responsibility when using NewAccountWithKeys.
	if providerStr == "bedrock" {
		// When apiKeys map is in use and "bedrock" wasn't provided, check
		// keyring as a last resort.
		apiKey, err := keyring.GetAPIKey(providerStr)
		if err == nil && apiKey != "" {
			enabled := true
			return []schemas.Key{
				{ID: providerStr + "_apikey", Name: providerStr + " API Key", Value: schemas.EnvVar{Val: apiKey}, Models: []string{}, Weight: 1.0, Enabled: &enabled},
			}, nil
		}
		return []schemas.Key{}, nil
	}

	// Resolve the Bifrost provider string to the keyring key name.
	// The keyring stores Google AI keys under "googleai", but Bifrost uses "gemini".
	krProvider := providerStr
	if providerStr == "gemini" {
		krProvider = "googleai"
	}

	apiKey, err := keyring.GetAPIKey(krProvider)
	if err == nil && apiKey != "" {
		enabled := true
		return []schemas.Key{
			{ID: providerStr + "_apikey", Name: providerStr + " API Key", Value: schemas.EnvVar{Val: apiKey}, Models: []string{}, Weight: 1.0, Enabled: &enabled},
		}, nil
	}

	return []schemas.Key{}, nil
}

// vertexKeys builds the single Vertex key when the gcloud project is
// configured using gcloud Application Default Credentials. Returns an empty
// slice when no project is configured so vertex is not presented.
func (a *Account) vertexKeys() []schemas.Key {
	project := a.vertexEnv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		return []schemas.Key{}
	}
	region := a.vertexEnv("GOOGLE_CLOUD_REGION")
	// Bifrost's primary chat paths (ChatCompletion*) hard-require a non-empty
	// region, so default an unset one to "global" — mirroring bifrost's own
	// Passthrough convention and the docs' "optional, defaults to global".
	if region == "" {
		region = "global"
	}
	enabled := true
	key := schemas.Key{
		ID:      "vertex_gcloud",
		Name:    "Vertex AI (gcloud ADC)",
		Models:  []string{"*"},
		Weight:  1.0,
		Enabled: &enabled,
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID: schemas.EnvVar{Val: project},
			Region:    schemas.EnvVar{Val: region},
			// Empty AuthCredentials relies on google.FindDefaultCredentials
			// (gcloud ADC). Only populated when GOOGLE_APPLICATION_CREDENTIALS
			// carries service-account JSON or a readable file path.
			AuthCredentials: schemas.EnvVar{Val: a.readVertexCredentials()},
		},
	}
	return []schemas.Key{key}
}

// getBaseURLFromEnv returns the base URL from the provider's convention-based
// environment variable: strings.ToUpper(provider) + "_BASE_URL".
// Special cases: azure uses AZURE_OPENAI_BASE_URL, gemini uses GEMINI_BASE_URL.
func getBaseURLFromEnv(provider string) string {
	envVar := strings.ToUpper(provider) + "_BASE_URL"
	return os.Getenv(envVar)
}

// GetConfigForProvider returns network configuration for a provider
func (a *Account) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	networkConfig := schemas.DefaultNetworkConfig
	if a.requestTimeout > 0 {
		networkConfig.DefaultRequestTimeoutInSeconds = a.requestTimeout
	}
	if a.streamIdleTimeout > 0 {
		networkConfig.StreamIdleTimeoutInSeconds = a.streamIdleTimeout
	}
	if a.maxRetries > 0 {
		networkConfig.MaxRetries = a.maxRetries
	}
	if a.baseURL != "" {
		networkConfig.BaseURL = a.baseURL
	} else if baseURL := getBaseURLFromEnv(string(provider)); baseURL != "" {
		networkConfig.BaseURL = baseURL
		slog.Debug("base URL from env", "provider", provider, "base_url", networkConfig.BaseURL)
	} else {
		slog.Debug("base URL using provider default", "provider", provider)
	}

	// Identify asimi to every provider. Bifrost's SetExtraHeaders only sets
	// a header when it's absent, so these can't clobber Authorization etc.
	headers := map[string]string{
		"User-Agent": "asimi/" + utils.AsimiVersion,
		"originator": "asimi",
	}
	// Codex OAuth: set the chatgpt-account-id header so the backend routes
	// requests to the correct organization.
	if provider == schemas.OpenAI && a.codexAccountID != "" {
		headers["chatgpt-account-id"] = a.codexAccountID
	}
	// OpenRouter app attribution: HTTP-Referer + X-Title surface asimi on
	// openrouter.ai/rankings and in users' dashboards.
	if provider == schemas.OpenRouter {
		headers["HTTP-Referer"] = "https://github.com/afittestide/asimi"
		headers["X-Title"] = "Asimi"
	}
	networkConfig.ExtraHeaders = headers

	config := &schemas.ProviderConfig{
		NetworkConfig: networkConfig,
		Logger:        NewBifrostLogger(slog.Default()),
	}

	// Custom provider support: if the provider is not a standard bifrost
	// provider, configure it as a custom provider with an OpenAI-compatible
	// base provider type. This allows providers like z-ai to be added via
	// configuration without code changes.
	if !isStandardProvider(provider) {
		baseURL := getBaseURLFromEnv(string(provider))
		if baseURL != "" {
			config.CustomProviderConfig = &schemas.CustomProviderConfig{
				CustomProviderKey: string(provider),
				BaseProviderType:  schemas.OpenAI,
			}
			slog.Debug("configured custom provider", "provider", provider, "base_url", baseURL)
		}
	}

	return config, nil
}

// isStandardProvider returns true if the provider is in schemas.StandardProviders.
func isStandardProvider(provider schemas.ModelProvider) bool {
	for _, sp := range schemas.StandardProviders {
		if sp == provider {
			return true
		}
	}
	return false
}
