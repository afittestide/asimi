## Edict: Save all tab sessions on shutdown (fix :qa session loss)

### Problem

`TUIModel.shutdown()` (tui.go:634) calls `saveSession()` (tui.go:606), which persists ONLY the active tab's session — it reads `m.tabs.ActiveTab()`. When the ruler runs `:qa` / `:quitall` (handleQuitAllCommand, commands.go:236) with N tabs open, N-1 sessions are silently dropped: those ministers' conversations cannot be resumed via `:resume`.

This also affects the pre-existing double-CTRL-C quit path (tui.go:1329) and the old single-tab `:quit`. The gap predates edict 744, but 744 made `:qa` a first-class "quit everything" command, so the loss is now the primary path.

### Proposed change

1. Add a `saveAllSessions()` method on TUIModel that iterates every tab, resolves each tab's minister session (via `m.court.GetMinister(string(tab.Type))` → `GetSession(tab.Target)`), and calls `m.sessionStore.SaveSession(session)` for each non-nil session.
2. In `shutdown()`, call `saveAllSessions()` instead of `saveSession()`. Keep `saveSession()` for the per-tab auto-save path on stream completion (tui.go:2153), which is correct as-is.
3. Optionally: in `handleQuitAllCommand`, call `m.stopStreaming()` (CancelAllTabs) before `shutdown()` so in-flight streams are cancelled and daemon-side contexts torn down — matching the double-CTRL-C path (tui.go:1329-1330) — so `:qa` while streaming is clean.

### Tests

Add a test with multiple tabs (each with a court minister session) verifying `shutdown()`/`saveAllSessions()` persists sessions for ALL tabs, not just the active one.

Evidence: commands.go:236 (handleQuitAllCommand → shutdown); tui.go:606-631 (saveSession reads only ActiveTab); tui.go:634-640 (shutdown calls saveSession); tui.go:1329-1330 (double-CTRL-C calls stopStreaming before shutdown; :qa does not); commands.go:213-219 (handleQuitCommand single-tab path)
