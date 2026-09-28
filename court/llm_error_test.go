package court

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactProviderErrorMessage covers the redaction helper: provider error
// bodies must never surface API keys, bearer tokens, or Authorization
// material to the user, even when the provider echoes request headers back.
func TestRedactProviderErrorMessage(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		banned []string
	}{
		{
			name:   "bearer token",
			in:     `upstream 401: invalid Bearer sk-live-abc123DEF456`,
			banned: []string{"sk-live-abc123DEF456"},
		},
		{
			name:   "authorization header echoed",
			in:     "Failed to fetch, Authorization: Bearer tok_secret_999",
			banned: []string{"Bearer tok_secret_999", "tok_secret_999"},
		},
		{
			name:   "api_key assignment",
			in:     `bad request: api_key="sk-9f8e7d6c5b4a" not allowed`,
			banned: []string{"sk-9f8e7d6c5b4a"},
		},
		{
			name:   "sk-prefixed key",
			in:     "request rejected, key sk-proj-XXXXYYYYZZZZ revoked",
			banned: []string{"sk-proj-XXXXYYYYZZZZ"},
		},
		{
			name:   "access token equals form",
			in:     "proxy error: access_token = ya29.a0AfH6SMBx",
			banned: []string{"ya29.a0AfH6SMBx"},
		},
		{
			name:   "x-api-key header style",
			in:     "401 from gateway (x-api-key: zro-secret-key-01)",
			banned: []string{"zro-secret-key-01"},
		},
		{
			name:   "plain message untouched",
			in:     "model not found for this account",
			banned: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := RedactProviderErrorMessage(tt.in)
			for _, b := range tt.banned {
				assert.NotContains(t, out, b)
			}
			if tt.banned == nil {
				assert.Equal(t, tt.in, out)
			} else {
				assert.Contains(t, out, "[redacted]")
			}
		})
	}
}

// TestRedactProviderErrorMessage_InsensitiveToCase checks case variants.
func TestRedactProviderErrorMessage_InsensitiveToCase(t *testing.T) {
	out := RedactProviderErrorMessage("APIKEY=deadbeef123 API-Key: cafebad00")
	assert.NotContains(t, out, "deadbeef123")
	assert.NotContains(t, out, "cafebad00")
}

// TestProviderRequestContext_Wrap verifies the user-facing error carries
// provider, effective base URL, method/path, and status — the edict's
// requested output shape — while carrying no secret material.
func TestProviderRequestContext_Wrap(t *testing.T) {
	ctx := ChatRequestContext("openai", "https://zro.moonmath.ai/v2/status/")
	status := 404
	err := ctx.Wrap("POST", &status, fmt.Errorf("Not Found"))
	msg := err.Error()
	assert.Equal(t,
		"provider API error (openai @ https://zro.moonmath.ai/v2/status, POST /v1/chat/completions, status 404): Not Found",
		msg)
}

// TestProviderRequestContext_Wrap_DefaultEndpoint covers the no-config case.
func TestProviderRequestContext_Wrap_DefaultEndpoint(t *testing.T) {
	ctx := ChatRequestContext("anthropic", "")
	status := 500
	err := ctx.Wrap("POST", &status, fmt.Errorf("internal error"))
	assert.Contains(t, err.Error(), "anthropic @ provider-default endpoint")
}

// TestProviderRequestContext_Wrap_NoStatus covers pre-HTTP failures where
// no status code exists (DNS errors, timeouts).
func TestProviderRequestContext_Wrap_NoStatus(t *testing.T) {
	ctx := ChatRequestContext("openai", "https://api.example.com")
	err := ctx.Wrap("POST", nil, fmt.Errorf("dial tcp: no such host"))
	assert.Contains(t, err.Error(), "POST /v1/chat/completions")
	assert.NotContains(t, err.Error(), "status")
}

// TestProviderRequestContext_Wrap_NeverLeaksSecrets asserts the wrapped
// error scrubs credentials embedded in the underlying provider message.
func TestProviderRequestContext_Wrap_NeverLeaksSecrets(t *testing.T) {
	ctx := ChatRequestContext("openai", "https://api.openai.com")
	status := 401
	err := ctx.Wrap("POST", &status,
		fmt.Errorf(`unauthorized: Bearer sk-real-secret-key-0001 rejected`))
	msg := err.Error()
	require.NotContains(t, msg, "sk-real-secret-key-0001")
	require.Contains(t, msg, "status 401")
}

// TestChatRequestContext_UsesEnvConvention checks the env-var fallback path
// matches Account.GetConfigForProvider's resolution order.
func TestChatRequestContext_UsesEnvConvention(t *testing.T) {
	t.Setenv("GEMINI_BASE_URL", "https://zro.moonmath.ai")
	ctx := ChatRequestContext("gemini", "")
	assert.Equal(t, "https://zro.moonmath.ai", ctx.BaseURL)

	// Configured base URL wins over env; normalization applies (e863).
	t.Setenv("OPENAI_BASE_URL", "https://env.example.com")
	ctx = ChatRequestContext("openai", "https://cfg.example.com/v1")
	assert.Equal(t, "https://cfg.example.com", ctx.BaseURL)

	// Neither set → empty (provider default).
	ctx = ChatRequestContext("mistral", "")
	assert.Equal(t, "", ctx.BaseURL)
}
