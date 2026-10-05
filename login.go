package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/storage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/maximhq/bifrost/core/schemas"
)

// Message types for login flow
type showOauthFailed struct{ err string }

// BaseModal represents a base modal dialog
type BaseModal struct {
	Title   string
	Content string
	Width   int
	Height  int
	Style   lipgloss.Style
}

// NewBaseModal creates a new base modal
func NewBaseModal(title, content string, width, height int) *BaseModal {
	return &BaseModal{
		Title:   title,
		Content: content,
		Width:   width,
		Height:  height,
		Style: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(globalTheme.SuccessColor).
			Width(width).
			Height(height).
			Align(lipgloss.Center, lipgloss.Center),
	}
}

// Render renders the modal
func (m *BaseModal) Render() string {
	titleStyle := lipgloss.NewStyle().
		Background(globalTheme.SuccessColor).
		Foreground(globalTheme.ToastTextColor).
		Padding(0, 1).
		Width(m.Width - 2)

	title := titleStyle.Render(m.Title)
	content := lipgloss.NewStyle().
		Width(m.Width-2).
		Height(m.Height-4).
		Align(lipgloss.Left, lipgloss.Center).
		Render(m.Content)

	body := lipgloss.JoinVertical(lipgloss.Center, title, content)
	return m.Style.Render(body)
}

// --- Codex OAuth (OpenAI) ---

const (
	codexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexAuthURL      = "https://auth.openai.com/oauth/authorize"
	codexRedirectURI  = "http://localhost:1455/auth/callback"
	codexScope        = "openid profile email offline_access model.request"
	codexCallbackPort = 1455
)

// codexTokenURL is the OAuth token endpoint. It's a var (not a const) so
// tests can override it to point at a mock server.
var codexTokenURL = "https://auth.openai.com/oauth/token"

// codexCallbackHost is the loopback address the OAuth callback server binds to.
// Configurable via ASIMI_CODEX_CALLBACK_HOST for testing.
var codexCallbackHost = func() string {
	if h := os.Getenv("ASIMI_CODEX_CALLBACK_HOST"); h != "" {
		return h
	}
	return "127.0.0.1"
}()

// codexCallbackPath is the URL path the OAuth callback server listens on.
const codexCallbackPath = "/auth/callback"

// codexTokenResponse represents the token response from the Codex OAuth flow
type codexTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

// codexOAuthCredential stores the full OAuth credential in the keyring as JSON.
type codexOAuthCredential struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // unix timestamp
	AccountID    string `json:"account_id"`
}

// performCodexLogin runs the Codex-style OpenAI OAuth browser login flow.
// It starts a loopback HTTP server on localhost:1455, opens the browser,
// receives the callback with the authorization code, exchanges it for tokens,
// and stores the resulting access token as an API key in the keyring.
func (m *TUIModel) performCodexLogin() tea.Cmd {
	return func() tea.Msg {
		// PKCE
		codeVerifier := randomString(64)
		sum := sha256.Sum256([]byte(codeVerifier))
		codeChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
		state := randomString(32)

		// Build auth URL
		u, err := url.Parse(codexAuthURL)
		if err != nil {
			return showOauthFailed{fmt.Sprintf("failed to parse auth URL: %v", err)}
		}
		q := u.Query()
		q.Set("response_type", "code")
		q.Set("client_id", codexClientID)
		q.Set("redirect_uri", codexRedirectURI)
		q.Set("scope", codexScope)
		q.Set("state", state)
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
		q.Set("id_token_add_organizations", "true")
		q.Set("codex_cli_simplified_flow", "true")
		q.Set("originator", "asimi")
		u.RawQuery = q.Encode()

		// Start loopback server on port 1455
		ln, err := net.Listen("tcp", codexCallbackHost+":"+fmt.Sprint(codexCallbackPort))
		if err != nil {
			return showOauthFailed{fmt.Sprintf("failed to start callback server on port %d: %v", codexCallbackPort, err)}
		}

		// Channel to receive the authorization code
		codeCh := make(chan string, 1)
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != codexCallbackPath {
				http.NotFound(w, r)
				return
			}
			if r.URL.Query().Get("state") != state {
				http.Error(w, "state mismatch", http.StatusBadRequest)
				return
			}
			code := r.URL.Query().Get("code")
			if code == "" {
				http.Error(w, "missing code", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte("<html><body><h2>Authorization complete. You can close this window.</h2></body></html>"))
			go func() { codeCh <- code }()
		})}

		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				slog.Debug("codex oauth callback server error", "err", err)
			}
		}()

		// Open browser
		_ = openBrowser(u.String())

		// Wait for code with timeout
		var code string
		select {
		case code = <-codeCh:
		case <-time.After(5 * time.Minute):
			_ = srv.Shutdown(context.Background())
			ln.Close()
			return showOauthFailed{"authorization timed out (5 minutes)"}
		}

		// Shutdown server after receiving code
		_ = srv.Shutdown(context.Background())
		ln.Close()

		// Exchange code for token
		form := url.Values{}
		form.Set("grant_type", "authorization_code")
		form.Set("code", code)
		form.Set("redirect_uri", codexRedirectURI)
		form.Set("client_id", codexClientID)
		form.Set("code_verifier", codeVerifier)

		req, err := http.NewRequest("POST", codexTokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return showOauthFailed{fmt.Sprintf("failed to create token request: %v", err)}
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return showOauthFailed{fmt.Sprintf("token exchange failed: %v", err)}
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return showOauthFailed{fmt.Sprintf("token exchange returned status %d", resp.StatusCode)}
		}

		var tok codexTokenResponse
		if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
			return showOauthFailed{fmt.Sprintf("failed to decode token response: %v", err)}
		}

		if tok.AccessToken == "" {
			return showOauthFailed{"OAuth response did not contain an access token"}
		}

		// Extract account ID from JWT
		accountID, err := extractCodexAccountID(tok.AccessToken)
		if err != nil {
			slog.Warn("failed to extract account ID from JWT", "error", err)
		}

		// Build full OAuth credential for keyring storage
		cred := codexOAuthCredential{
			AccessToken:  tok.AccessToken,
			RefreshToken: tok.RefreshToken,
			ExpiresAt:    time.Now().Unix() + tok.ExpiresIn,
			AccountID:    accountID,
		}
		jsonCred, err := json.Marshal(cred)
		if err != nil {
			return showOauthFailed{fmt.Sprintf("failed to serialize credential: %v", err)}
		}

		// Store the full OAuth credential as JSON in keyring
		if err := SaveAPIKeyToKeyring("openai", string(jsonCred)); err != nil {
			slog.Warn("Failed to save API key to keyring, falling back to file storage", "error", err)
			// Fall back to file storage
			if err := UpdateUserLLMAuth("openai", tok.AccessToken, "codex-mini-latest"); err != nil {
				return showOauthFailed{fmt.Sprintf("failed to save credentials: %v", err)}
			}
		} else {
			// Update config file with provider and model (keyring has the key)
			if err := UpdateUserLLMAuth("openai", "", "gpt-5.5"); err != nil {
				slog.Warn("Failed to update config file", "error", err)
			}
		}

		// Update in-memory config
		m.config.LLM.Provider = "openai"
		m.config.LLM.Model = "gpt-5.5"
		m.config.LLM.APIKey = tok.AccessToken
		m.config.LLM.BaseURL = "https://chatgpt.com/backend-api"

		// Initialize LLM with new credentials
		if err := m.court.SetContext(context.Background(), m.setContextParams()); err != nil {
			return showOauthFailed{"Failed to initialize AI session: " + err.Error()}
		}

		return llmInitSuccessMsg{}
	}
}

func browserCommand(url string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url), nil
	case "linux":
		return exec.Command("xdg-open", url), nil
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url), nil
	default:
		return nil, fmt.Errorf("unsupported OS for auto-open browser")
	}
}

func openBrowser(url string) error {
	cmd, err := browserCommand(url)
	if err != nil {
		return err
	}
	return cmd.Start()
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}

// extractCodexAccountID extracts the chatgpt_account_id from the JWT
// access token's payload. The account ID is nested under the
// "https://api.openai.com/auth" claim.
func extractCodexAccountID(accessToken string) (string, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid JWT: expected 3 parts, got %d", len(parts))
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to base64-decode JWT payload: %w", err)
	}

	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("failed to parse JWT payload as JSON: %w", err)
	}

	authRaw, ok := claims["https://api.openai.com/auth"]
	if !ok {
		return "", fmt.Errorf("JWT payload missing https://api.openai.com/auth claim")
	}

	var authClaims struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	}
	if err := json.Unmarshal(authRaw, &authClaims); err != nil {
		return "", fmt.Errorf("failed to parse auth claim: %w", err)
	}

	if authClaims.ChatGPTAccountID == "" {
		return "", fmt.Errorf("JWT auth claim missing chatgpt_account_id")
	}

	return authClaims.ChatGPTAccountID, nil
}

// parseCodexOAuthCredential attempts to deserialize a keyring value as a
// codexOAuthCredential JSON blob. Returns the credential and true if the
// value is a JSON OAuth credential; returns false for plain-string API keys.
func parseCodexOAuthCredential(raw string) (codexOAuthCredential, bool) {
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return codexOAuthCredential{}, false
	}
	var cred codexOAuthCredential
	if err := json.Unmarshal([]byte(raw), &cred); err != nil {
		return codexOAuthCredential{}, false
	}
	return cred, cred.AccessToken != ""
}

// refreshCodexToken refreshes an expired Codex OAuth access token using the
// stored refresh token. It reads the credential from keyring, checks expiry,
// and if expired, POSTs a refresh request to the token endpoint. The new
// credential (with updated access token, refresh token, expiry, and account
// ID) is stored back in the keyring. Returns the new access token.
func refreshCodexToken() (string, error) {
	raw, err := GetAPIKeyFromKeyring("openai")
	if err != nil {
		return "", fmt.Errorf("failed to read OAuth credential from keyring: %w", err)
	}

	cred, ok := parseCodexOAuthCredential(raw)
	if !ok {
		return "", fmt.Errorf("no OAuth credential found in keyring for openai")
	}

	// Token still valid — no refresh needed
	if time.Now().Unix() < cred.ExpiresAt {
		return cred.AccessToken, nil
	}

	if cred.RefreshToken == "" {
		return "", fmt.Errorf("OAuth credential has no refresh token")
	}

	// POST refresh request
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", cred.RefreshToken)
	form.Set("client_id", codexClientID)

	req, err := http.NewRequest("POST", codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token refresh returned status %d", resp.StatusCode)
	}

	var tok codexTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("failed to decode refresh response: %w", err)
	}

	if tok.AccessToken == "" {
		return "", fmt.Errorf("refresh response did not contain an access token")
	}

	// Re-extract account ID from new JWT
	accountID, err := extractCodexAccountID(tok.AccessToken)
	if err != nil {
		slog.Warn("failed to extract account ID from refreshed JWT", "error", err)
		accountID = cred.AccountID // fallback to old account ID
	}

	// Build updated credential
	newCred := codexOAuthCredential{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tok.ExpiresIn,
		AccountID:    accountID,
	}

	// Fall back to old refresh token if the response omitted one
	if newCred.RefreshToken == "" {
		newCred.RefreshToken = cred.RefreshToken
	}

	jsonCred, err := json.Marshal(newCred)
	if err != nil {
		return "", fmt.Errorf("failed to serialize refreshed credential: %w", err)
	}

	if err := SaveAPIKeyToKeyring("openai", string(jsonCred)); err != nil {
		slog.Warn("failed to store refreshed credential in keyring", "error", err)
	}

	return newCred.AccessToken, nil
}

// getEffectiveAPIKey returns the effective API key for a provider, handling
// OAuth token refresh transparently. For the "openai" provider, if the
// keyring value is a JSON OAuth credential and the token is expired, it
// calls refreshCodexToken() and returns the refreshed access token.
// For non-OAuth credentials (plain strings), it returns them directly.
func getEffectiveAPIKey(provider string) string {
	// Check environment variable first
	envKey := providerEnvVar(provider)
	if key := os.Getenv(envKey); key != "" {
		return key
	}

	raw, err := GetAPIKeyFromKeyring(provider)
	if err != nil || raw == "" {
		return ""
	}

	// Check if it's a JSON OAuth credential
	if cred, ok := parseCodexOAuthCredential(raw); ok {
		// If expired, attempt refresh
		if time.Now().Unix() >= cred.ExpiresAt {
			refreshed, err := refreshCodexToken()
			if err != nil {
				slog.Warn("failed to refresh OAuth token", "provider", provider, "error", err)
				return cred.AccessToken // return stale token as last resort
			}
			return refreshed
		}
		return cred.AccessToken
	}

	// Plain string API key
	return raw
}

// UpdateUserLLMAuth updates or creates ~/.config/asimi/asimi.conf with the given LLM auth settings.
// It saves API keys securely in the keyring and only stores provider/model in the config file.
// This function preserves all comments in the existing config file.
func UpdateUserLLMAuth(provider, apiKey, model string) error {
	// Save API key securely in keyring (only if non-empty)
	if apiKey != "" {
		if err := SaveAPIKeyToKeyring(provider, apiKey); err != nil {
			slog.Warn("Failed to save API key to keyring, falling back to file storage", "error", err)
			return updateAPIKeyInFile(provider, apiKey, model)
		}
	}

	cfgDir, cfgPath, err := config.UserConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	// Read existing content or start with empty
	var content string
	if data, err := os.ReadFile(cfgPath); err == nil {
		content = string(data)
	}

	// Update values using comment-preserving helpers
	content = config.UpdateOrInsertTOMLValue(content, "llm", "provider", provider)
	content = config.UpdateOrInsertTOMLValue(content, "llm", "model", model)
	content = config.UpdateOrInsertTOMLValue(content, "llm", "auth_method", "apikey_keyring")

	// Remove plaintext API key if it exists (we're using keyring now)
	content = config.RemoveTOMLKey(content, "llm", "api_key")

	// Remove legacy OAuth tokens
	content = config.RemoveTOMLKey(content, "llm", "auth_token")
	content = config.RemoveTOMLKey(content, "llm", "refresh_token")

	return os.WriteFile(cfgPath, []byte(content), 0o600)
}

// updateAPIKeyInFile is the fallback method for storing API keys in file (less secure).
// This function preserves all comments in the existing config file.
func updateAPIKeyInFile(provider, apiKey, model string) error {
	cfgDir, cfgPath, err := config.UserConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	// Read existing content or start with empty
	var content string
	if data, err := os.ReadFile(cfgPath); err == nil {
		content = string(data)
	}

	// Update values using comment-preserving helpers
	content = config.UpdateOrInsertTOMLValue(content, "llm", "provider", provider)
	content = config.UpdateOrInsertTOMLValue(content, "llm", "model", model)
	content = config.UpdateOrInsertTOMLValue(content, "llm", "api_key", apiKey)
	content = config.UpdateOrInsertTOMLValue(content, "llm", "auth_method", "apikey_file")

	return os.WriteFile(cfgPath, []byte(content), 0o600)
}

// handleLoginCommand shows a provider selection list for authentication.
// Providers with AuthType "oauth" (OpenAI/Codex) trigger the OAuth flow.
// Providers with AuthType "apikey" prompt for a key.
// Providers with AuthType "keyless" (Ollama) need no login.
func handleLoginCommand(model *TUIModel, args []string) tea.Cmd {
	var providers []Model

	for _, sp := range schemas.StandardProviders {
		providerKey := bifrostProviderToAsimi(string(sp))
		meta := getProviderMeta(providerKey)

		switch meta.AuthType {
		case AuthTypeOAuth:
			providers = append(providers, Model{
				ID:          providerKey + "-login",
				DisplayName: "Login with " + meta.DisplayName + " (OAuth)",
				Provider:    providerKey,
				Status:      "login_required",
				OnSelect:    model.performCodexLogin(),
			})
		case AuthTypeAPIKey:
			providers = append(providers, Model{
				ID:          providerKey + "-apikey",
				DisplayName: "Set API key for " + meta.DisplayName,
				Provider:    providerKey,
				Status:      "login_required",
				OnSelect:    func() tea.Msg { return apiKeyPromptMsg{provider: providerKey} },
			})
		case AuthTypeKeyless:
			// Keyless providers (Ollama) don't need login — skip
		}
	}

	return model.tabs.Content().ShowUnifiedModels(providers, "", nil)
}

// handleLogoutCommand handles the :logout command
func handleLogoutCommand(model *TUIModel, args []string) tea.Cmd {
	return func() tea.Msg {
		provider := model.config.LLM.Provider
		if provider == "" {
			return showSystemMsg("No provider configured. Nothing to logout from.")
		}

		var errors []string

		// Delete API key from keyring
		if err := DeleteAPIKeyFromKeyring(provider); err != nil {
			slog.Warn("Failed to delete API key from keyring", "provider", provider, "error", err)
			errors = append(errors, fmt.Sprintf("API key: %v", err))
		} else {
			slog.Debug("Deleted API key from keyring", "provider", provider)
		}

		// Clear in-memory credentials
		model.config.LLM.APIKey = ""

		// Clear the session state
		model.currentEdictKey = storage.EdictKey{}
		model.sessionActive = false

		// Update status line
		model.status.SetAgent("not configured")

		// Build result message
		msg := NewChatMsgBuilder(systemPrefix)
		msg.WriteLnf("Logged out from %s", provider)

		if len(errors) > 0 {
			msg.WriteLn("")
			msg.WriteLn("Some credentials could not be removed:")
			for _, e := range errors {
				msg.WriteLnf("  • %s", e)
			}
		}

		msg.WriteLn("")
		msg.WriteLn("Use :login to authenticate.")

		return showContextMsg{content: msg.String()}
	}
}
