package court

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ChatPathFragment is the route bifrost appends to the effective base URL
// for chat completions on OpenAI-compatible providers. Keep it in sync with
// the debug log in Account.GetConfigForProvider.
const ChatPathFragment = "/v1/chat/completions"

// ProviderRequestContext captures the non-secret identity of a provider
// request — who we called and where — so failures can be rendered with
// their target instead of surfacing as bare "Not Found"s. It must never
// carry API keys, Authorization material, or request payloads.
type ProviderRequestContext struct {
	Provider string
	BaseURL  string // effective, normalized; "" means provider default
}

// ChatRequestContext builds the context for a chat-completion request.
// configuredBaseURL is the raw value from config (llm.base_url); the same
// resolution order as Account.GetConfigForProvider applies: configured
// value, then the provider's env convention, then "".
func ChatRequestContext(provider string, configuredBaseURL string) ProviderRequestContext {
	return ProviderRequestContext{
		Provider: provider,
		BaseURL:  effectiveBaseURL(schemas.ModelProvider(provider), configuredBaseURL),
	}
}

// Wrap renders a provider failure with its request context, e.g.:
//
//	provider API error (openai @ https://zro.moonmath.ai, POST /v1/chat/completions, status 404): Not Found
//
// status may be nil when the failure never reached an HTTP response. The
// underlying message is redacted so leaked credentials can never reach
// the user through an error string.
func (c ProviderRequestContext) Wrap(method string, status *int, cause error) error {
	host := c.BaseURL
	if host == "" {
		host = "provider-default endpoint"
	}
	provider := c.Provider
	if provider == "" {
		provider = "unknown-provider"
	}
	var statusFragment string
	if status != nil {
		statusFragment = fmt.Sprintf(", status %d", *status)
	}
	return fmt.Errorf("provider API error (%s @ %s, %s %s%s): %s",
		provider, host, method, ChatPathFragment, statusFragment,
		RedactProviderErrorMessage(cause.Error()))
}

// credentialExhaustionType is bifrost's synthetic error type, emitted when
// every configured key returned a permanent per-key failure (401/402/403).
const credentialExhaustionType = "upstream_credentials_exhausted"

// WrapBifrost renders a bifrost failure with its request context. When
// bifrost collapsed per-key 401/402/403 failures into its synthetic 502
// (upstream_credentials_exhausted), a hint is appended because gateways also
// return 403 for model-access denials: the configured model may be the
// rejected party, not the key.
func (c ProviderRequestContext) WrapBifrost(method string, be *schemas.BifrostError) error {
	cause := bifrostErrorToGoError(be)
	if cause == nil {
		cause = errors.New("bifrost returned no error detail")
	}
	err := c.Wrap(method, bifrostStatusCode(be), cause)
	if !isCredentialExhaustion(be) {
		return err
	}
	return fmt.Errorf("%w — if the API key is valid, check that the configured model is available to it; some gateways reject unknown models with 403", err)
}

// isCredentialExhaustion reports whether bifrost replaced the provider's
// per-key 401/402/403 failures with its synthetic upstream_credentials_exhausted
// error. Bifrost populates both the top-level and ErrorField type; check both.
func isCredentialExhaustion(be *schemas.BifrostError) bool {
	if be == nil {
		return false
	}
	if be.Type != nil && *be.Type == credentialExhaustionType {
		return true
	}
	return be.Error != nil && be.Error.Type != nil && *be.Error.Type == credentialExhaustionType
}

// errSecretPatterns match the shapes credentials take when a provider (or a
// proxy in the middle) echoes request material back inside an error body.
var errSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+\S+`),
	regexp.MustCompile(`(?i)(api[_-]?key|apikey|authorization|access[_-]?token)\s*[:=]\s*\S+`),
	regexp.MustCompile(`sk-\S+`), // OpenAI-style key prefix
	regexp.MustCompile(`(?i)x-api-key:\s*\S+`),
}

// RedactProviderErrorMessage strips credential-shaped material from a
// provider error body before it is shown to the user. Provider payloads
// sometimes parrot back request headers; nothing user-secret may survive.
func RedactProviderErrorMessage(msg string) string {
	out := msg
	for _, re := range errSecretPatterns {
		out = re.ReplaceAllString(out, "[redacted]")
	}
	return strings.TrimSpace(out)
}
