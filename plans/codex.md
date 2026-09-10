# Codex Subscription Login Flow — Low-Level Design

## 1. Overview

The Codex subscription login flow authenticates a user against OpenAI's ChatGPT OAuth infrastructure, producing an access token and refresh token that are used to call the Codex Responses API. The flow supports two login methods: **browser-based OAuth with PKCE** and **device-code OAuth** (RFC 8628). Both produce the same credential shape.

The access token is a JWT. A custom claim (`https://api.openai.com/auth.chatgpt_account_id`) inside the JWT is extracted and sent as a `chatgpt-account-id` header on every API request. The access token itself is sent as a `Bearer` token in the `Authorization` header.

## 2. OAuth Constants

| Constant | Value |
|---|---|
| Client ID | `app_EMoamEEZ73f0CkXaXp7hrann` |
| Auth base URL | `https://auth.openai.com` |
| Authorize endpoint | `https://auth.openai.com/oauth/authorize` |
| Token endpoint | `https://auth.openai.com/oauth/token` |
| Redirect URI (browser) | `http://localhost:1455/auth/callback` |
| Callback host | `127.0.0.1` (configurable via env override) |
| Device user-code endpoint | `https://auth.openai.com/api/accounts/deviceauth/usercode` |
| Device token endpoint | `https://auth.openai.com/api/accounts/deviceauth/token` |
| Device verification URI | `https://auth.openai.com/codex/device` |
| Device redirect URI | `https://auth.openai.com/deviceauth/callback` |
| Device code timeout | 15 minutes (900 seconds) |
| Scope | `openid profile email offline_access` |
| JWT claim path | `https://api.openai.com/auth` |

## 3. Login Method Selection

When login is initiated, the user is prompted to choose between:

1. **Browser login (default)** — Opens a browser, runs a local callback server, and optionally accepts a pasted redirect URL.
2. **Device code login (headless)** — Displays a user code and verification URI; the user authorizes on a separate device. No local server needed.

## 4. Browser Login Flow

### 4.1 PKCE Generation

Uses the Web Crypto API (cross-platform, no Node-specific dependencies):

- Generate 32 random bytes → base64url-encode → **code verifier**
- SHA-256 hash the verifier → base64url-encode → **code challenge**
- Challenge method: `S256`

### 4.2 State Generation

- Generate 16 random bytes → hex string → **state parameter**

### 4.3 Authorization URL Construction

Build the authorize URL with these query parameters:

| Parameter | Value |
|---|---|
| `response_type` | `code` |
| `client_id` | The client ID constant |
| `redirect_uri` | `http://localhost:1455/auth/callback` |
| `scope` | `openid profile email offline_access` |
| `code_challenge` | The PKCE challenge |
| `code_challenge_method` | `S256` |
| `state` | The random state |
| `id_token_add_organizations` | `true` |
| `codex_cli_simplified_flow` | `true` |
| `originator` | `pi` (configurable) |

### 4.4 Local Callback Server

Start an HTTP server on port 1455 at the callback host. The server handles `GET /auth/callback`:

1. Parse the URL from the request.
2. Reject non-`/auth/callback` paths with a 404 error page.
3. Validate the `state` query parameter matches the generated state. Mismatch → 400 error page.
4. Extract the `code` query parameter. Missing → 400 error page.
5. Respond with a styled success HTML page.
6. Resolve the auth code to the waiting flow.

If the server fails to start (port in use, etc.), the flow gracefully degrades — the callback promise resolves `null`, and the manual code-paste fallback takes over.

### 4.5 Browser Launch

The authorization URL is displayed to the user and the system default browser is opened:

- **macOS**: `open <url>`
- **Windows**: `rundll32 url.dll,FileProtocolHandler <url>`
- **Linux**: `xdg-open <url>`

The browser launch is best-effort (failures are silently ignored). The URL is always shown as a clickable terminal hyperlink so the user can open it manually.

### 4.6 Code Acquisition Race

When the manual-code input is available (interactive TUI mode), a race runs between:

- **Local callback server** — resolves when the browser redirect hits the server.
- **Manual code paste** — user types/pastes the authorization code or full redirect URL into the terminal.

Whichever resolves first wins. If the callback wins, the manual input prompt is aborted. If the manual input wins, its value is parsed.

If neither produces a code, a final fallback prompts the user to paste the code manually.

### 4.7 Manual Input Parsing

The pasted input is parsed flexibly:

1. Try parsing as a URL → extract `code` and `state` query params.
2. If not a URL, try splitting on `#` → first part is code, second is state.
3. If `code=` is present, parse as URLSearchParams.
4. Otherwise, treat the entire input as the code.

If a state is extracted, it must match the generated state or an error is thrown.

### 4.8 Token Exchange

POST to the token endpoint with `Content-Type: application/x-www-form-urlencoded`:

| Field | Value |
|---|---|
| `grant_type` | `authorization_code` |
| `client_id` | The client ID constant |
| `code` | The authorization code |
| `code_verifier` | The PKCE verifier |
| `redirect_uri` | `http://localhost:1455/auth/callback` |

The request is abortable (cancellation via AbortSignal).

### 4.9 Token Response Parsing

The response must be HTTP 200 with a JSON body containing:

- `access_token` (string, required)
- `refresh_token` (string, required)
- `expires_in` (number, required — seconds until expiry)

The expiry timestamp is computed as `Date.now() + expires_in * 1000`.

A non-200 response or missing fields throw an error with the response body for diagnostics.

### 4.10 Account ID Extraction

The access token is a JWT. To extract the ChatGPT account ID:

1. Split the token on `.` into three parts.
2. Base64-decode the middle part (payload).
3. Parse as JSON.
4. Read `["https://api.openai.com/auth"].chatgpt_account_id`.

If the account ID is missing or invalid, an error is thrown.

### 4.11 Credential Object

The final credential object produced by the browser flow:

```
{
  access: string,       // JWT access token
  refresh: string,      // Refresh token
  expires: number,      // Absolute expiry timestamp (ms since epoch)
  accountId: string,    // chatgpt_account_id from JWT
  type: "oauth"         // Added by the caller before storage
}
```

### 4.12 Cleanup

The local HTTP server is closed in a `finally` block regardless of success or failure.

## 5. Device Code Login Flow

### 5.1 Device Auth Request

POST to the device user-code endpoint with `Content-Type: application/json`:

```json
{ "client_id": "<client ID>" }
```

The response must contain:

- `device_auth_id` (string)
- `user_code` (string)
- `interval` (number or numeric string — polling interval in seconds)

A 404 response indicates device-code login is not enabled on the server.

### 5.2 User Presentation

The user is shown:
- The verification URI (`https://auth.openai.com/codex/device`) as a clickable link.
- The user code to enter at that URI.

### 5.3 Polling

Poll the device token endpoint at the specified interval using a generic device-code polling utility that implements RFC 8628:

POST to the device token endpoint with `Content-Type: application/json`:

```json
{
  "device_auth_id": "<device auth ID>",
  "user_code": "<user code>"
}
```

**Poll result handling:**

| HTTP Status | Error Code | Action |
|---|---|---|
| 200 | — | Complete: extract `authorization_code` and `code_verifier` from response |
| 403 / 404 | — | Pending: continue polling |
| Other | `deviceauth_authorization_pending` | Pending: continue polling |
| Other | `slow_down` | Increase interval by 5 seconds (per RFC 8628 §3.5), continue polling |
| Other | (any other) | Failed: throw error with response body |

The polling utility enforces:
- A minimum interval of 1 second.
- A default interval of 5 seconds if the server omits one.
- A hard deadline (15 minutes).
- AbortSignal support for cancellation.
- Slow-down accumulation: each `slow_down` response permanently increases the interval by 5 seconds.

On timeout, if any `slow_down` responses were received, a specific error message is shown suggesting clock drift in WSL/VM environments.

### 5.4 Token Exchange (Device Flow)

The device token endpoint returns `authorization_code` and `code_verifier` directly (no PKCE generation needed on the client side). Exchange the authorization code:

POST to the token endpoint with:

| Field | Value |
|---|---|
| `grant_type` | `authorization_code` |
| `client_id` | The client ID constant |
| `code` | The authorization code from device polling |
| `code_verifier` | The code verifier from device polling |
| `redirect_uri` | `https://auth.openai.com/deviceauth/callback` |

Token response parsing and account ID extraction are identical to the browser flow (§4.9, §4.10).

## 6. Token Refresh

When the access token expires, it is refreshed using the refresh token:

POST to the token endpoint with `Content-Type: application/x-www-form-urlencoded`:

| Field | Value |
|---|---|
| `grant_type` | `refresh_token` |
| `refresh_token` | The stored refresh token |
| `client_id` | The client ID constant |

The response is parsed identically to §4.9. The new account ID is re-extracted from the new access token's JWT.

The refresh is NOT abortable (no AbortSignal) — it always attempts to complete.

## 7. Credential Storage

### 7.1 File Location

Credentials are persisted to `auth.json` in the agent directory (`~/.pi/agent/auth.json` by default). The file has permission mode `0o600` (owner read/write only).

### 7.2 Storage Shape

The file is a JSON object keyed by provider ID:

```json
{
  "openai-codex": {
    "type": "oauth",
    "access": "<JWT>",
    "refresh": "<refresh token>",
    "expires": 1234567890000,
    "accountId": "<chatgpt account id>"
  }
}
```

One credential per provider. The `type` field discriminates between `"oauth"` and `"api_key"`.

### 7.3 File Locking

Credential writes use file locking (`proper-lockfile`) to prevent race conditions when multiple processes try to refresh or write simultaneously:

- **Sync path**: Acquires a lock with up to 10 retries (20ms busy-wait between attempts).
- **Async path**: Acquires a lock with up to 10 retries (exponential backoff, 100ms–10s, randomized), 30-second stale timeout, and compromised-lock detection.

All writes are serialized: read current content → merge the change → write back. This preserves unrelated credentials that may have been added by other processes.

### 7.4 Write Strategy

Writes use a read-merge-write pattern under the lock:
1. Read the current file content under the lock.
2. Parse as JSON.
3. Set or delete the target provider's entry.
4. Serialize and write back.
5. Release the lock.

This ensures concurrent edits by multiple processes don't clobber each other.

## 8. Auth Resolution at Request Time

### 8.1 Priority Order

1. **Runtime override** (CLI `--api-key` flag) — highest priority, not persisted.
2. **Stored credential** in `auth.json` — either API key or OAuth token.
3. **Environment variable** — fallback for API-key providers (not OAuth).

For OAuth credentials, ambient/env fallback is never used. A stored OAuth credential "owns" the provider.

### 8.2 OAuth Token Refresh (Double-Checked Locking)

When a stored OAuth credential is expired:

1. **Optimistic check** (no lock): Is `Date.now() >= expires`? If not, use the token directly.
2. **Under the credential-store lock**: Re-check expiry (another process may have refreshed already).
3. If still expired: call `refresh()` to exchange the refresh token.
4. Persist the new credential under the lock.
5. Return the new auth.

This ensures concurrent requests and processes cannot double-refresh a rotated token.

### 8.3 Token Derivation for API Requests

The OAuth credential is converted to request auth by returning `{ apiKey: credential.access }`. The access token serves as the API key.

### 8.4 HTTP Headers for Codex API Requests

The Codex Responses API requires these headers on every request:

| Header | Value |
|---|---|
| `Authorization` | `Bearer <access_token>` |
| `chatgpt-account-id` | The account ID extracted from the JWT |
| `originator` | `pi` |
| `User-Agent` | `pi (<platform> <release>; <arch>)` |
| `OpenAI-Beta` | `responses=experimental` (SSE streaming only) |
| `session-id` | A UUID (optional, for session continuity) |

The account ID is re-extracted from the JWT on every request (not cached from login time).

## 9. Login UI Flow (Interactive Mode)

### 9.1 Entry Point

User types `/login` → provider selector appears with searchable list of all registered providers (OAuth and API-key). Each provider shows a status indicator:

- `✓ configured` — credential already stored.
- `✓ env: <VAR>` — environment variable detected.
- `• unconfigured` — no auth found.

### 9.2 OAuth Login Dialog

When an OAuth provider is selected, the login dialog replaces the editor area. The dialog:

1. Calls the provider's `login()` method with callback handlers.
2. On `onAuth`: displays the authorization URL as a clickable hyperlink, opens the browser, and (for callback-server providers) shows a manual paste input.
3. On `onDeviceCode`: displays the verification URI and user code, then shows a waiting message.
4. On `onPrompt`: shows a text input field for manual code entry.
5. On `onSelect`: shows a sub-selector for method selection (browser vs device code).
6. On `onProgress`: appends a status message.

The dialog supports cancellation via Escape/Ctrl+C at any point, which aborts the flow via an `AbortController`.

### 9.3 Post-Login

On success:
1. The dialog is dismissed and the editor is restored.
2. The model registry is refreshed to pick up the new credentials.
3. If the user was on an "unknown model" (no auth), the provider's default model is auto-selected.
4. A status message confirms login and shows the credential file path.

On failure:
1. The dialog is dismissed.
2. An error message is shown (unless the error is "Login cancelled").

### 9.4 Logout

User types `/logout` → provider selector shows only providers with stored credentials. Selecting a provider removes its entry from `auth.json`. Environment variables and models.json config are not affected.

## 10. CLI Login Flow

The AI package CLI (`pi-ai login`) provides a terminal-only login path:

1. List all registered OAuth providers or accept a provider ID as argument.
2. Use `readline` for stdin/stdout interaction.
3. Same callback interface but text-based:
   - `onAuth`: prints the URL.
   - `onDeviceCode`: prints the verification URI and user code.
   - `onPrompt`: prompts via readline.
   - `onSelect`: prints numbered options and reads a number.
4. On success, saves credentials to `auth.json` in the current directory.

## 11. Provider Registration

The OpenAI Codex provider is registered in the built-in provider list with:

- **Provider ID**: `openai-codex`
- **Display name**: `OpenAI Codex`
- **Base URL**: `https://chatgpt.com/backend-api`
- **Auth**: OAuth only (no API-key auth path)
- **OAuth implementation**: Lazy-loaded via a bundler-opaque dynamic import to keep Node-only code (local HTTP server, crypto) out of browser bundles.

The OAuth implementation object exposes three operations:
- `login(callbacks)` → runs the interactive flow, returns credentials
- `refresh(credential)` → exchanges refresh token, returns new credentials
- `toAuth(credential)` → returns `{ apiKey: credential.access }` for request auth

## 12. Credential Type System

Two credential types exist in `auth.json`, discriminated by the `type` field:

| Type | Fields | Usage |
|---|---|---|
| `oauth` | `access`, `refresh`, `expires`, `accountId` | OAuth providers (Codex, Anthropic, Copilot) |
| `api_key` | `key`, optional `env` | API-key providers, with optional provider-scoped env overrides |

The credential store is keyed by provider ID — one credential per provider. A stored credential "owns" the provider: ambient env vars are only consulted when nothing is stored.

## 13. Error Handling Summary

| Scenario | Behavior |
|---|---|
| Token exchange fails (non-200) | Throw with HTTP status and response body |
| Token response missing fields | Throw with serialized JSON for diagnostics |
| JWT decode fails or account ID missing | Throw "Failed to extract accountId from token" |
| State mismatch (callback or manual) | Throw "State mismatch" |
| Local server port in use | Degrade to manual-paste-only flow |
| Device code endpoint returns 404 | Throw with guidance to use browser login |
| Device code polling times out | Throw timeout error (with WSL/VM clock-drift hint if slow_down was received) |
| Refresh fails | Credential preserved for retry; model discovery skips the provider; user can `/login` to re-authenticate |
| Login cancelled (Escape/Ctrl+C) | Abort signal fires; "Login cancelled" error thrown; no credential changes |

## 14. Security Considerations

- **PKCE**: The browser flow uses S256 PKCE to prevent authorization code interception.
- **State parameter**: Random 16-byte hex state prevents CSRF on the callback.
- **File permissions**: `auth.json` is created with mode `0o600` and parent directory with `0o700`.
- **File locking**: Prevents race conditions and credential corruption from concurrent processes.
- **No shell invocation**: Browser launch uses `spawn` without shell to prevent URL injection.
- **Token storage**: Tokens are stored in plaintext in `auth.json` (protected by file permissions). No encryption at rest.
- **Abort support**: Browser flow and device flow both support cancellation via AbortSignal.

---

The document covers the complete flow: two login methods (browser PKCE + device code), token exchange, JWT account ID extraction, credential persistence with file locking, double-checked-locking refresh, request-time auth resolution, the interactive TUI dialog lifecycle, and the CLI login path. Ready for the chancellor.

