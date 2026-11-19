package court

import (
	"errors"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
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

// TestProviderRequestContext_WrapBifrost_CredentialExhaustionAddsModelHint
// covers bifrost's synthetic 502: when every key is collapsed into
// upstream_credentials_exhausted, the message must keep the model name on the
// suspect list, since gateways also 403 on model-access denials.
func TestProviderRequestContext_WrapBifrost_CredentialExhaustionAddsModelHint(t *testing.T) {
	ctx := ChatRequestContext("openai", "https://zro.moonmath.ai")
	status := 502
	errType := "upstream_credentials_exhausted"
	msg := "all configured keys returned permanent per-key errors (401/402/403)"
	be := &schemas.BifrostError{
		Type:       &errType,
		StatusCode: &status,
		Error:      &schemas.ErrorField{Type: &errType, Message: msg},
	}
	err := ctx.WrapBifrost("POST", be)
	assert.Contains(t, err.Error(), msg)
	assert.Contains(t, err.Error(), "check that the configured model is available")
}

// TestProviderRequestContext_WrapBifrost_ErrorFieldTypeOnly covers the same
// hint when only ErrorField.Type carries the sentinel.
func TestProviderRequestContext_WrapBifrost_ErrorFieldTypeOnly(t *testing.T) {
	ctx := ChatRequestContext("openai", "https://zro.moonmath.ai")
	status := 502
	errType := "upstream_credentials_exhausted"
	be := &schemas.BifrostError{
		StatusCode: &status,
		Error:      &schemas.ErrorField{Type: &errType, Message: "all configured keys returned permanent per-key errors"},
	}
	assert.True(t, isCredentialExhaustion(be))
	assert.Contains(t, ctx.WrapBifrost("POST", be).Error(), "check that the configured model is available")
}

// TestProviderRequestContext_WrapBifrost_NoHintForOrdinaryErrors keeps the
// model hint out of unrelated provider failures.
func TestProviderRequestContext_WrapBifrost_NoHintForOrdinaryErrors(t *testing.T) {
	ctx := ChatRequestContext("openai", "https://api.openai.com")
	status := 404
	be := &schemas.BifrostError{
		StatusCode: &status,
		Error:      &schemas.ErrorField{Message: "Not Found"},
	}
	err := ctx.WrapBifrost("POST", be)
	assert.Contains(t, err.Error(), "status 404")
	assert.NotContains(t, err.Error(), "check that the configured model")
}

// TestProviderRequestContext_WrapBifrost_HintGatedOnSentinelNotStatus pins the
// gate itself: the hint must key off the upstream_credentials_exhausted
// sentinel and never off a bare 401/402/403 status. Without this, an
// implementation that guessed from the status code would keep every other
// test green while re-mislabelling a genuine credential rejection as a model
// problem. Also checks the hint cannot smuggle provider-echoed credentials
// past Wrap's redaction.
func TestProviderRequestContext_WrapBifrost_HintGatedOnSentinelNotStatus(t *testing.T) {
	ctx := ChatRequestContext("openai", "https://zro.moonmath.ai")
	for _, code := range []int{401, 402, 403, 404} {
		status := code
		be := &schemas.BifrostError{
			StatusCode: &status,
			Error:      &schemas.ErrorField{Message: "per-key rejection"},
		}
		msg := ctx.WrapBifrost("POST", be).Error()
		assert.False(t, isCredentialExhaustion(be), "status %d is not the sentinel", code)
		assert.NotContains(t, msg, "check that the configured model", "status %d grew the hint", code)
		assert.Contains(t, msg, fmt.Sprintf("status %d", code))
	}

	sentinel := credentialExhaustionType
	be := &schemas.BifrostError{
		Type:  &sentinel,
		Error: &schemas.ErrorField{Type: &sentinel, Message: "keys rejected: Bearer sk-judge-secret"},
	}
	msg := ctx.WrapBifrost("POST", be).Error()
	assert.Contains(t, msg, "check that the configured model")
	assert.NotContains(t, msg, "sk-judge-secret")
}

// TestProviderRequestContext_WrapBifrost_NilError covers the nil failure: it
// must not panic and must not claim credential exhaustion.
func TestProviderRequestContext_WrapBifrost_NilError(t *testing.T) {
	ctx := ChatRequestContext("openai", "https://api.openai.com")
	err := ctx.WrapBifrost("POST", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "check that the configured model")
	assert.False(t, isCredentialExhaustion(nil))
}

// TestIsTransientProviderError pins the transient-error classifier: 5xx and
// 408/429 retry, network-failure shapes with no status retry, and 4xx /
// credential exhaustion never do.
func TestIsTransientProviderError(t *testing.T) {
	ctx := ChatRequestContext("openai", "https://api.example.com")
	tests := []struct {
		name      string
		err       error
		transient bool
	}{
		{name: "500", err: ctx.Wrap("POST", intPtr(500), errors.New("internal error")), transient: true},
		{name: "503", err: ctx.Wrap("POST", intPtr(503), errors.New("service unavailable")), transient: true},
		{name: "502 bad gateway", err: ctx.Wrap("POST", intPtr(502), errors.New("bad gateway")), transient: true},
		{name: "408 request timeout", err: ctx.Wrap("POST", intPtr(408), errors.New("request timeout")), transient: true},
		{name: "429 rate limited", err: ctx.Wrap("POST", intPtr(429), errors.New("rate limited")), transient: true},
		{name: "404 not found", err: ctx.Wrap("POST", intPtr(404), errors.New("Not Found")), transient: false},
		{name: "400 bad request", err: ctx.Wrap("POST", intPtr(400), errors.New("bad request")), transient: false},
		{name: "401 unauthorized", err: ctx.Wrap("POST", intPtr(401), errors.New("unauthorized")), transient: false},
		{name: "connection reset no status", err: ctx.Wrap("POST", nil, errors.New("read tcp 1.2.3.4:443->5.6.7.8:999: read: connection reset by peer")), transient: true},
		{name: "connection refused no status", err: ctx.Wrap("POST", nil, errors.New("dial tcp: connection refused")), transient: true},
		{name: "broken pipe no status", err: ctx.Wrap("POST", nil, errors.New("write: broken pipe")), transient: true},
		{name: "EOF no status", err: ctx.Wrap("POST", nil, errors.New("unexpected EOF")), transient: true},
		{name: "io timeout no status", err: ctx.Wrap("POST", nil, errors.New("read: i/o timeout")), transient: true},
		{name: "stream idle timeout", err: ctx.Wrap("POST", nil, errors.New("stream idle timeout exceeded")), transient: true},
		{name: "TLS handshake timeout", err: ctx.Wrap("POST", nil, errors.New("net/http: TLS handshake timeout")), transient: true},
		{name: "plain message no status", err: ctx.Wrap("POST", nil, errors.New("model overloaded")), transient: false},
		{
			name: "credential exhaustion sentinel",
			err: fmt.Errorf("provider API error (openai @ x, POST /v1/chat/completions, status 502): %s: all configured keys returned permanent per-key errors",
				credentialExhaustionType),
			transient: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.transient, isTransientProviderError(tt.err), "%q", tt.err)
		})
	}
	assert.False(t, isTransientProviderError(nil))
}

func intPtr(n int) *int { return &n }

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
