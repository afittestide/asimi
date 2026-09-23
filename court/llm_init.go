package court

import (
	"context"
	"log/slog"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// InitBifrost creates a Bifrost client using the court Account
// (keyring-backed credentials) and slog logger. Formerly lived in
// main.go; moved here so the daemon can initialise its own client
// without the TUI having to ship one across the wire.
func InitBifrost(ctx context.Context, requestTimeout, streamIdleTimeout, maxRetries int, baseURL string, apiKeys map[string]string, codexAccountID string) (*bifrost.Bifrost, error) {
	baseURL = normalizeBaseURL(baseURL)
	slog.Debug("init bifrost", "base_url", baseURL)
	return bifrost.Init(ctx, schemas.BifrostConfig{
		Account: NewAccountWithKeys(requestTimeout, streamIdleTimeout, maxRetries, baseURL, apiKeys, codexAccountID),
		Logger:  NewBifrostLogger(slog.Default()),
	})
}
