# Changelog

All [Semantic Versions](https://semver.org/spec/v2.0.0.html) of this project and their notable changes are documented in the file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), with
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Per-edict git worktrees** — each edict now gets its own worktree at `.agents/worktrees/e{ID}` on branch `asimi/e{ID}`, created on first use and reused by every ritual of that edict, so concurrent edicts no longer mutate each other's working tree. The file tools (`read_file`, `write_file`, …) and `run_shell_command` run inside the edict's worktree instead of the project root, and the worktree is removed when the edict seals (e531, e943, e987)
- **`:switch <target>`** — move the whole Court between branches and edict worktrees in place, without exiting Asimi and starting a new instance: `:switch e123` enters (creating it when missing) edict 123's worktree on `asimi/e123`, `:switch main` returns to the main working tree, and `:switch <branch>` resolves a named branch to its linked worktree via `git worktree list`. The switch re-targets every minister, the RitualRunner, and the shell runner through the existing `SetContext` path, then resets all tabs to the default minister tabs; it refuses while any tab is streaming (e952)
- **`ignore_copy_globs`** — a new `[sandbox]` config option bounding which gitignored files travel from the main tree into a new edict worktree; the worktree is a fresh checkout, so this allowlist (common local-config names by default) carries the local-only files a worktree needs without copying build artifacts (e946)
- **Width-aware `tian_ledger` table** — the human-facing Heaven ledger now renders as a markdown table (`edict_id`, `event`, `time`, `detail`) whose columns are sized to the chat component, via a generic JSON→markdown-table opt-in any daemon-side tool may emit; the raw event summaries still ride along so the model keeps full fidelity (e939)
- **Edict details as a markdown block** — the swift-strike `given: "the edict details"` step now prints the edict's full, untruncated Intent as a rendered markdown block instead of the bare step name, so the Ruler can see what a ritual is about (e975)

### Changed

- **Sandbox resource ceilings** — the sandbox image now sets `GOFLAGS=-p=1`, `GOMAXPROCS=2`, and `GOMEMLIMIT=1500MiB` to bound Go build parallelism and runtime memory, so concurrent edict rituals can no longer OOM-kill the compiler in the ~2 GiB podman VM (e948)
- **Single shared Go build cache** — every runner (host, podman, and each edict worktree) now points `GOCACHE` at one `<projectRoot>/.gocache`, so `go build`/`go test` reuse the same content-addressed cache instead of rebuilding the module graph per worktree; any inherited `GOCACHE` is dropped first (e946)
- **One project-slug resolver; fail-loud scoping** — project slug resolution is now shared and honours a pinned config, and Court read tools (`list_edicts`, `get_edict_status`, `list_ling`, …) that match zero rows in the queried scope now report where rows actually exist (naming the nearby `(username, project)` buckets) instead of letting an empty scope be mistaken for "no rows at all" (e968)
- **Edict list previews** — the `:edict` selector now strips leading whitespace and `#` from each edict's intent preview, so a `# Goal: …` markdown heading no longer shows its hash (666df1530)
- **Tab-close messaging** — closing a tab now toasts "Tab closed; `:qa` to close all" instead of the ambiguous bare "Tab closed" (59753408f)
- **Toolchain security floors** — bumped to Go 1.26.10 and podman v6.1.3 (with a vendor refresh) to remediate the 2026-10-22 govulncheck findings (e986)

### Fixed

- **Sealing a worktree edict** — sealing now rebases the edict branch onto the resolved default branch, commits any work still staged in the worktree, squash-merges it into the default branch as one commit, and removes the worktree and branch. Failed merges are surfaced (not silently swallowed), a fully-subsumed squash is treated as a no-op rather than a failure, and the rebase/merge runs through a host runner that retries once on an ephemeral corrupt loose object (e945, e955, e974, e984)
- **`run_shell_command` ignored the edict worktree** — shell steps that omitted an explicit `cd` executed in the project root and mutated the main tree; they now run inside the edict's worktree (e987)
- **File tools ignored the edict worktree** — `read_file`/`write_file` and friends now resolve paths against the edict's worktree root, closing the e531 isolation leak into the main tree (e943)
- **Ritual abort on tab close** — closing a tab (`:q`) after a CTRL-C now aborts the running ritual instead of leaving it stuck and queueing every later ritual until restart (e921)
- **Cancelling a ritual** — cancelling now context-cancels in-flight work and resolves the worktree branch even when the project root is nested inside another worktree (e928)
- **Stale seal tombstones** — the seal UI/decision path now counts only live seals (`stale_at IS NULL`), so an edict whose intent was clarified can be re-sealed instead of being blocked by invalidated tombstones (e967)
- **Runner restart aborting other sessions** — `PodmanRunner.Restart` now scopes its `ClearSchedulerMsg` abort to the restarted runner's own sessions instead of context-cancelling tool calls belonging to every other session (e957)
- **`approve_doc` with a nil Notify** — the tool is no longer registered with a zero-value struct and a permanently-nil callback; it uses a lazy `NotifyFn` getter, fixing "approve_doc requires TUI notify to open editor" (e970)
- **OOM from duplicated git/Go caches** — added a `.dockerignore` and bounded the builder context (pruning before build) so edict worktrees, `vendor/`, `.git/`, and build logs are no longer shipped into the podman VM, fixing "no space left on device" (b1bde5b4c)

## [0.14.5] - 2026-10-11

### Added

- **Private-network provider egress opt-in** — a new `llm.allow_private_network` config option lets the provider SDK reach RFC1918 (`10.x`/`172.16.x`/`192.168.x`) and ULA endpoints for self-hosted/on-prem models. Off by default because enabling it relaxes the SDK's SSRF egress guard for every provider; link-local/metadata addresses (`169.254.0.0/16`, `fe80::`) remain blocked regardless (e912)

### Fixed

- **Daemon disconnect recovery** — the TUI reconnects automatically when the daemon connection drops: the court client is wrapped in a reconnecting client that respawns a dead daemon and re-runs the SetContext handshake on every new connection, drop detection re-arms after each drop, and if reconnect does not complete within the poll window the user is told "Daemon unreachable — still trying to reconnect…" instead of being stranded on "Reconnecting…" (e911)
- **Judge evidence files no longer clutter the project root** — the Judge no longer writes `judge_evidence_*.json` files into the working tree; its treason clause now requires a verdict to include its evidence rather than render it to a JSON file

## [0.14.1] - 2026-10-06

### Added

- **Live model picker** — `:models` (alias `:model`) now enumerates models live from every configured provider's `/v1/models` and caches the catalog for the session (reopening is instant and makes no network calls); `:models refresh` forces a refetch. Rows carry per-model metadata (context window, price), list the current provider's models first followed by the others alphabetically, show provider icons, and render a dim `unavailable: <provider>` line when a provider cannot be reached (e903, e906, e910)
- **Direct model selection** — `:model <provider>/<name>`, `:model <provider> <name>`, and `:model <name>` (current provider) select a model without opening the picker; the picker also ends with a "Type a model name…" row for `provider/model` input (e903, e906, e910)

### Changed

- **Context window reporting** — the context window is now provider-reported when available, falling back to a local registry estimate rendered with a leading `~` (e.g. `~128.0k`) so an estimate is never shown as exact (e908)
- **Model rows drop the modality token** — model picker rows no longer show a `text->text`-style modality token (e910)

### Fixed

- **Automatic context overflow recovery** — a request rejected for exceeding the model's context window no longer halts the session: Asimi learns the true window from the provider's error, compacts the conversation to fit, and re-sends once; if it still cannot fit, the session stays alive with an actionable hint (`context window N exceeded — use :compact or :new`) (e908)
- **Malformed tool-call arguments** — a tool call whose `function.arguments` is not valid JSON is normalized to `{}` in place and reported back to the model as an error, instead of poisoning history and wedging the session on every later request (e854)
- **Unknown rituals** — `enact_ritual` now rejects an unknown ritual name with a clear error listing the available rituals, instead of emitting an event that can never run (e841)
- **Precedent verification on reforged manifests** — `check_precedent_approved` now evaluates only current manifests, so a rejected precedent left on a superseded manifest no longer blocks an edict from ascending (sibling of the verdict-gate bug)
- **Fork race condition** — removed a data race in fork/join ritual execution (e900)

## [0.13.7] - 2026-10-01

### Chaged

- vendored package where updated based on security requirments

## [0.13.6] - 2026-10-01

### Added

- **Gitless ground as a first-class mode** — `RepoInfo` now carries `HasVCS` as the single authoritative VCS-detection flag, so Asimi runs cleanly in a plain directory with no git repository: the project slug falls back to the directory basename, branch slugs stay empty, earth expressions and heaven snapshots report the condition explicitly, borderland manifests are built from a file inventory, and the honest "no VCS detected" log line replaces the previous false "git repository detected" (e888)

### Changed

- **Host runner timeout discipline** — commands run outside the sandbox (`--isolated-host` / host runner) now run under a command deadline (default 10m) and bounded approval waits (default 2m), matching the Podman runner. Both are configurable via `[run_shell_command].timeout_minutes` and the sandbox `approval_timeout`, and are plumbed through `SessionConfig` into every ephemeral `HostRunner` construction path (e886, e889)
- **Manifest permissions merge** — `create_manifest` is dual-registered under intent `Write` so the forge can record its own manifests; `registry.Register` now merges permissions instead of panicking on re-registration (e888)
- **`asimisql` uses the embedded driver** — court queries now go through the embedded driver (`ToolContext` DB) instead of shelling out to a `sqlite3` CLI binary (e888)

### Fixed

- **`ask_ruler` inside a ritual** — answers submitted on a ritual tab are now delivered as the zhengming answer instead of being treated as a ritual interjection (which paused or aborted the step); the TUI tracks pending zhengming per routing channel and the Court refuses to mint interactive sessions for ritual channels (e894)
- **Headless log noise** — headless mode (`-p`) now consumes tool-scheduling and stream-lifecycle events at debug level, so a headless run no longer floods its log with unhandled-event warnings (598d32e64)
- **`bash` step commands that are not found** — a given step failing with exit 127 (`command not found`) now produces a note for the minister instead of failing the ritual step, leaving the verdict gate as the single retry mechanism and ending the harbor timeout spiral (e888)

## [0.13.5] - 2026-09-29

### Added

- **Ling execution engine (built, default off)** — lings (task orders) can now execute over a shared DAG engine that honors declared dependencies; lings carry an owning minister and batches of `insert_ling` calls with satisfied dependencies can be ignited automatically. **Ignition is disabled by default** (`ling_ignition_enabled = false`) until per-ling worktree isolation lands (e531) — concurrent lings on one working tree are unsafe without it (e847)
- **Kimi K3** — added Moonshot's Kimi K3 to the model roster/config defaults

### Changed

- **Provider error reporting** — provider errors now carry provider name and HTTP status context, improving operator diagnosis (e879)

### Fixed

- **Release tarball wrapping** — release tarballs no longer double-wrap the `asimi` binary under an `./asimi` directory, fixing the checksum/installer layout (e864)
- **`OPENAI_BASE_URL` compatibility** — `openai`-compatible providers honor custom base URLs again (e863)
- **ask_ruler routing** — ruler questions are routed to the correct tab (e866)
- **`--isolated-host` log output** — headless-mode log output restored under `--isolated-host` (e881)
- **Precedent tools** — `record_precedent` / `query_precedents` fixes (e874, e875, e876)
- **Duplicate seal crash** — crash on duplicate seal grant; seal granting is now idempotent (e882)

## [0.13.4] - 2026-09-17

### Fixed

- Heaven's ledger
- support for clit prompts starting with `-` as in `-p "- <prompt>`
- ask_ruler chat option send the users prompt as the reply

## [0.13.3] - 2026-09-14

### Added

- **Minister communication over the Tian ledger** — ministers can now communicate through the Tian ledger

### Fixed

- **Harbor integration** — honor `ASIMI_HOME` for the court DB path and degrade gracefully when `$HOME` is unwritable, fixing the read-only-`$HOME` startup abort under `--isolated-host`; `initLogger` no longer panics when its log directory cannot be created (e836)
- **Init-project ritual TODO comments** — the ritual now uses TODO comments correctly
- **`suggest_edict` description** — improved the tool description for smoother activation

## [0.13.2] - 2026-09-08

### Fixed

- Aborted and failed ritual no longer blocks the rituals queue

### Changed

- Default context size set to 256K for unknown models

## [0.13.1] - 2026-09-07

### Added

- **`--reasoning-effort` flag** — new `--reasoning-effort` CLI flag (plus `ASIMI_REASONING_EFFORT` env, subcommand, and `[llm]` config plumbing) for setting the reasoning effort / model thinking level on supported providers, useful for harness-driven and headless runs (e798)

### Changed

- **Repo rename completed** — docs, `scripts/install.sh`, `.goreleaser.yml`, and version strings updated to finish the repository name change
- **TUI update speed (e780)** — chat content re-renders are now debounced while streaming via a new `[ui] tick_interval` config (default `100ms`), letting the TUI batch streaming updates for smoother, faster rendering
- **Edict selector seals use emojis** — the judge and chancellor seal glyphs in the `:edict` list are now rendered as emojis (👩‍⚖️ / ☯️) instead of kanji (刑 / 門)
- **Git diff TTL caching** — repo status/diff statistics (`internal/repo`) are now cached and refreshed with a TTL instead of being recomputed on every call, and tool-call messages are batched as dirty content updates instead of forcing full re-renders; both reduce unnecessary TUI work

### Fixed

- **ATIF trajectory completeness ** — fixes to the `--atif` recorder so recorded trajectories are well-formed
- **Slowdown** - getting context size from the provider in a loop led to sluginsh off the ui

## [0.13.0] - 2026-09-01

### Added

- **`--model` & `--provider` CLI flags** — new `--model` and `--provider` flags plus
  `ASIMI_MODEL` / `ASIMI_PROVIDER` environment-variable overrides, so the model can be
  selected from the command line or environment for harness-driven runs (terminal-bench).
  Precedence: CLI flag > env var > config `[llm]` section > defaults (e785)
- **Vertex AI provider** — new `vertex` provider that talks to Google Vertex AI via gcloud
  application-default credentials (ADC), with all gcloud-specific values read from
  environment variables (`GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_REGION`, optional
  `GOOGLE_APPLICATION_CREDENTIALS`); no `VERTEX_API_KEY` needed (e786)

## [0.12.0] - 2026-08-28

### Added

- **ATIF recording** — new `--atif` flag and an ATIF session recorder for capturing sessions (e746)

### Changed

- **`request_zhengming` renamed to `ask_ruler`** — the clarification tool is now called `ask_ruler` (e770)
- **Progressive session resume** — the chat rebuild is now fed through a batched render tick for smoother progressive resume (e771)
- **Per-model context sizes** — context sizes are now computed per model instead of assumed (e767)
- **Host runner note** — Asimi now notes when a session executes without a sandbox (host runner)
- **Rituals error handling** — improved error handling across rituals (e758)

### Fixed

- **Podman socket on macOS** — find the podman socket on macOS applehv machines (#165)
- **Sandbox image slugs with capitals** — lowercase the sandbox image slug so organizations with capitals in their names work (e775)
- **System prompt wording** — corrected spelling and improved the description in the system prompt (#163)
- **macOS peercred tests** — shorten the Unix socket path in peercred tests for macOS (#164)

## [0.11.0] - 2026-08-09

### Added

- **Skills discovery** — added support for the Agent Skills open standard (https://agentskills.io), a lightweight file-based format for giving AI agents specialized knowledge and workflows. Skills live in `.agents/skills/<skill-name>/SKILL.md` with optional supporting files and optional per-minister targeting (e662)

### Fixed

- **SelfUpdate checksum verification** — `SelfUpdate` now computes the SHA256 checksum against the tarball bytes (before extraction), not the extracted binary. This fixes a guaranteed mismatch because `checksums.txt` contains hashes of the `.tar.gz` archives, not the extracted binary (e749)

## [0.10.1] - 2026-08-05

### Added

- **Transient scratchpad** — court history (precedents, manifests, verdicts, seals, lings) is now loaded before each ritual step and injected into the next user message, then cleared. This makes court history visible to the minister on every turn, including during tool calls, without baking it into the immutable system prompt (e723)
- **Vi-like quit commands** — `:quit` now closes the current tab (or quits the application if it's the last tab). `:qa` and `:quitall` always quit the application, matching Vim conventions (e744)
- **Messages as toasts** — transient informational messages are now displayed as toast notifications instead of inline messages
- **Stale container cleanup** — podman container creation now handles 409 conflicts by removing stale containers and retrying, preventing stuck states after ungraceful shutdowns (e734)

### Fixed

- **Edict Chat action** — restored the "Chat" action on the edict dashboard, which creates an edict tab and restores the edict's birth session there for direct conversation within an edict context
- **Edict session resume** — fixed pending prompt display when restoring a session on a ritual tab, ensuring the Ruler's input is visible before the AI response arrives (e729)
- **Client identity validation** — daemon now validates the client identity against Unix socket peer credentials (SO_PEERCRED), preventing impersonation by other processes with socket access (e739)
- **Consulted minister output routing** — `consult_minister` now routes streaming output to the correct tab by using a channelID from context, rather than defaulting to the caller's minister ID (e719)
- **English follow-up text** — fixed English language follow-up text in prompts (#161)
- **Continue on non-paused ritual** — `:continue` now enacts a fresh swift-strike ritual when no ritual is paused, instead of showing "Ritual is not paused" (e740)
- **Isolated host per-connection** — `--isolated-host` is now per-connection rather than daemon-wide, fixing the issue where the TUI reusing an existing daemon would inherit the daemon's mode instead of its own intent (e742)
- **CTRL-C between ritual steps** — pressing CTRL-C between ritual steps now pauses the ritual for interjection instead of hard-aborting it (e732)
- **Sandbox creation race** — fixed a race condition where concurrent container creation attempts caused infinite error loops due to mismatched error string matching (e736)
- **Status bar tab switching** — the status bar's middle section (bucket utilization, stopwatch) now correctly updates when switching between tabs

## [0.10.0] - 2026-07-29

### Added

- **Classic ministers** — renamed ministers to align with the 三省六部 (Three Departments and Six Ministries) system: Sage → Chancellor (門下侍中), Strategist → War (兵部), Chancellor → Secretary (中書令). Seal chain is now Judge → Chancellor → Ruler (e629)
- **Zhengming as common tool** — `request_zhengming` is now a shared tool available to all ministers, not restricted to specific roles
- **CLI Zhengming support** — zhengming (clarification) requests can now be triggered from the CLI (4eb0ec760)
- **Stale seals on edict change** — seals are now invalidated when an edict's intent changes, ensuring re-review after modifications (e714)
- **Headless Prompt Mode** — `-p` flag for non-interactive execution with stdout output and exit codes, `--max-turns` for benchmark control (e684)
- **Isolated Host Mode** — `--isolated-host` flag bypasses podman sandbox and approval gates for already-isolated environments like terminal-bench and CI (e683)

### Changed

- **consult_minister scope** — `consult_minister` is now restricted to the Secretary only, concentrating cross-minister coordination through the secretary role (e706)
- **Removed update_edict tool** — the `update_edict` tool has been removed; edict updates are now handled through `suggest_edict` for refinement (e701)
- **Minister reordering** — reordered ministers to align with the 三省六部 hierarchy (772267426)
- **Messages converted to toasts** — transient messages are now displayed as toast notifications (ed38fefe3)

### Fixed

- **CI dependencies** — restored `libgpgme-dev` in CI dependencies (e1)
- **consult_minister self-call** — `consult_minister` can no longer call itself, preventing circular consultations (e688)
- **Earth-wide ritual edict** — earth-wide rituals now correctly use court edict 1 instead of edict 0 (e687)
- **DB seal migration** — fixed database seal migration for existing sessions
- **Zhengming routing** — zhengming prompts now route to the active tab; fixed hardcoded caller in `suggest_edict` (e704)
- **Zhengming↔Tab link** — fixed the link between zhengming prompts and tab routing (809463139)
- **tabnew default tab label** — fixed the default tab label when creating a new tab (242605fce)
- **SQLite3 error output** — surfaced SQLite3 error output in asimisql errors (e709)
- **TUI garbage characters** — fixed rendering artifacts (`bg:1e1e/...` codes) in the content renderer (e697)
- **Ritual input flattening** — `expandTemplate` now handles `map[string]string` inputs correctly (e685)

### Postponed

- **Handsoff Mode** — `--handsoff` flag auto-answers zhengming with the recommended option, enabling fully autonomous runs without human intervention (e682)
- **Realm-Aware File Permissions** — `RealmGuard` system for earth/intent file access control; proved more complex than expected, work stored on `realm-aware-permissions` branch for a future version (e695, e696, e698, e699, e702)


## [0.9.2] - 2026-07-21

### Added

- **Minister consultation** — all ministers can now consult another minister, enabling cross-minister collaboration (e653)
- **Chat action on edicts** — added a Chat action when interacting with an edict, allowing direct conversation within an edict context (e672)

### Changed

- **Documentation links** — added missing documentation links

### Fixed

- **Self-update asset name mismatch** — asset names in GitHub releases don't include the "v" prefix (e.g. `asimi_0.9.1_darwin_arm64.tar.gz`), but the code was looking for `asimi_v0.9.1_darwin_arm64.tar.gz`. The "v" is now stripped from the tag name before matching, fixing the "no matching asset found" error on `asimi update`
- **Prompting on edict tab** — fixed prompting behavior on the edict tab (e668)
- **Session ID storage for edicts** — fixed storing the session ID that mothered an edict (e674)
- **Multiple minister tabs** — `tabnew` now creates unique channel IDs so new tabs get independent streams (e680)
- **Template expansion with map inputs** — `expandTemplate` now handles `map[string]string` inputs correctly (e685)
- **consult_minister output routing** — minister responses now route to the correct tab
- **Chat on an edict** — fixed conversation flow when chatting within an edict context

## [0.9.1] - 2026-07-19

### Added
- **Parallel shell command execution** — sandbox shell now executes commands concurrently for faster builds (e657)
- **20+ LLM provider support** — expanded from 5 to 20+ supported providers (e658)
- **Clean-slate ritual** — review uncommitted changes, split them into logical commits, and link them to edicts
- **Glamour-based help rendering** — consolidated help topics into a single tutorial with rich formatting (e650)
- **YAML-based minister definitions** — ministers are now configured via YAML for easier customization (e625-7)
- **Parallel minister execution** — ministers can now perform tasks in parallel for faster rituals
- **Ritual tab routing** — ritual prompt routing with pause/resume interjection (e647)
- **Ritual queue reporting** — clear feedback when a ritual is queued
- **Sandbox OS detection** — `GetOS` on Runner interface for accurate sandbox OS reporting
- **suggest_edict intent replacement** — `suggest_edict` can now replace the edict intent (e634)
- **Unify ritual channel IDs** — ritual channels now use the `e<N>` convention consistently

### Changed
- **Ministers Order** - easier for the uninitiated, starting with Forge
- **Shogunate → Court rename** — renamed `shogunate/` package to `court/` (type `Shogunate` → `Court`, `NewShogunate` → `NewCourt`), config section `[shogunate]` → `[court]` (backward-compatible), event strings `shogunate_started` → `court_started`, `shogunate_ready` → `court_ready`, RPC types `ShogunateClient` → `CourtClient`, `internal/shogunateapi/` → `internal/courtapi/`
- **RPC protocol renamed** — "Shogunate RPC Protocol" → "Asimi's RPC Protocol"
- **Daemon extraction** — extracted daemon server code from root `daemon.go` into `internal/daemon/` package
- **DB schema migration v4→v5** — updates old event string values in `tian_events` and `tian_event_dlq`
- **Direct GitHub API for updates** — replaced `go-github-selfupdate` with direct GitHub API calls
- **Minister tool configuration** — replaced private tools with `extra_tools` in minister definitions
- **Removed the marshal role** (e624)
- **Simplified minister architecture** — streamlined minister definitions and removed dead code
- **Improved welcome screen** — updated with tutorial and quit commands
- **Improved ritual notifications** — better messaging and progress reporting (e664, e651)

### Fixed
- **Ritual extra params** — properly handling rituals extra parameters
- **run_on_host approval** — fixed approval request for `run_on_host` commands (e666)
- **Shell progress display** — fixed showing shell progress in rituals
- **Linux build** — fixed build on Linux (e663)
- **Detached HEAD project name** — fixed getting the right project name when on a detached HEAD
- **`:edicts` Edit flow** — fixed Edit action to exit answering mode and use `SetIntent` (e655)
- **Update notification** — fixed the update notification logic (e646)
- **Prompt height with word-wrapped options** — fixed prompt height calculation accounting for word-wrapped options
- **Ritual message routing** — fixed routing of ritual messages to per-edict channels with auto-created tabs (e640)
- **Zhengming minister blocking** — zhengming no longer stops all minister avatars (e642)
- **Container ID in status bar** — fixed container ID display
- **Post swift-strike staging** — fixed staging of changes after swift-strike (e637)
- **Bubbletea wordLeft infinite loop** — fixed infinite loop in word navigation (e635)
- **wire run_on_host** — fixed wiring of `run_on_host` (e631)
- **Podman sandbox errors** — clarified podman sandbox error messages (e131)

## [0.8.1] - 2026-07-09

### Changed

- podman is now v6 for safety & speed

## [0.8.0] - 2026-07-09

### Added

- **Context-aware onboarding** — onboarding flow now detects what's actually missing and provides targeted guidance (e617)

### Changed

- **`:edict` command** — replaced `:seal` with a unified `:edict` command for edict management: read dashboard, enact ritual, seal, resume session, cancel edict with action menu (e595)
- **Improved help text **

### Fixed

- **Edit action in edict menu** — fixed the "Edit" action in `:edict <id>` (e613)
- **Manifest-less ritual path** — edict-level verdicts and precedents are now visible to then-checks (e611)
- **Default menu options** — removed hardcoded "Edit" and "Chat" options, moved them into per-question Options slice (e609)
- **Superseded manifest checks** — `check_verdicts_passed` and related queries now respect superseded manifests, preventing false retries (e603)
- **PodmanRunner connection context** — fixed sandbox disconnection caused by context derived from streaming context (e601)
- **Lost sandbox handling** — improved recovery when sandboxes are lost (e589)
- **Empty forging handling** — fixed handling of empty forge results

### Known Bugs

- Codex OAuth login fails

## [0.7.0] - 2026-06-30

### Added

- **API keyring storage** — API keys are now stored securely in the OS keyring (e518)
- **Welcome screen** — dismissible on any key press, with restored home page
- **Ritual aborted message** — clear feedback when a ritual is aborted (e517)
- **Improved models command** — better model selection UX

### Changed

- **Swift-strike ritual routing** — replaced LLM-based ritual routing with a direct YESNO swift-strike prompt for edict enactment (e503)
- **Theme-based colors** — TUI now uses the theme system for consistent coloring
- **Aborted message styling** — improved messaging for aborted operations (e499)

### Fixed

- **Shell runner hangs** — resolved multiple shell runner deadlock issues (e528)
- **Config error reporting** — clearer error messages for configuration problems
- **Dismissed ritual handling** — dismissed rituals no longer cause issues
- **Double ABORTED message** — removed duplicate aborted notifications
- **In-ritual zhengming timeout** — removed premature timeout during zhengming flow (e502)
- **Tests opening browser** — tests no longer trigger browser launches
- **Welcome screen rendering** — fixed visual issues on the welcome screen

## [0.6.0] - 2026-06-17

### Added

- **Daemon mode** — `asimi daemon` subcommand serves the Shogunate over a Unix socket with msgpack-RPC, enabling persistent sessions, multi-project support, and TUI reconnection
- **TUI ↔ Daemon bridge** — bidirectional RPC with `ASIMI_DAEMON=1` auto-start and `ASIMI_DAEMON_SOCKET` for connecting a running TUI to an existing daemon
- **Shogunate (幕府)** — the six-minister governance system with strategist, forge, judge, censor, sage, and marshal roles, orchestrated by the chancellor
- **Rituals** — structured workflows (swift-strike, castle-siege, dawn-audience, review-borderlands, fix-lint) with step-level execution, recovery, and zhengming checkpoints
- **Edict system** — intent-driven work orders with seal chain ascension (judge → sage → ruler), zhengming (正名) clarification flow, and active/blocked/sealed/cancelled status
- **Fork/Join DAG execution** — parallel minister orchestration with dependency resolution
- **Tool Registry & Permission System Phase 1** — governable tool access for ministers
- **Bifrost migration** — replaced langchaingo with Bifrost for LLM provider routing, supporting OpenAI, Anthropic, Bedrock, and Minimax via a unified interface
- **Platform overlay volumes** — mount host directories into the sandbox with proper isolation
- **Sandbox isolation hardening** — ping liveness probe, host network support, and improved container lifecycle
- Tab and Shift+Tab keyboard shortcuts for navigating between TUI tabs (e498)
- **Per-tab streaming** — origin-based stream routing with per-tab CTRL-C context cancellation
- **Tab system** — `gt`/`gT` navigation across chancellor, forge, judge, and shogunate tabs
- **`:edict` command** — unified edict management (replaces `:seal`): read dashboard, enact ritual, seal, resume session, cancel edict with action menu
- **`:export` over the wire** — session export via `GetSessionExport` RPC
- **SetContext handshake** — multi-project daemon support with per-client project context
- **Ritual recovery** — resume interrupted rituals with zhengming confirmation
- **Welcome message** — onboarding greeting on startup
- **Ruler guide** — documentation for the ruler (user) role in the Shogunate
- **RPC protocol reference** — docs for the msgpack-RPC wire protocol
- **Li enforcement** — block destructive git flags (`--no-verify`, `--overwrite-ignore`, `git clean`) in sandbox
- **Asimi attribution headers** — send identification headers to LLM providers
- **Environment variable base URLs** — configure LLM provider endpoints via env vars

### Changed

- **Wire protocol migrated to msgpack-RPC** — standard msgpack-RPC for Neovim compatibility, replacing custom streaming
- **Persistent minister sessions** — ministers reuse conversation context across ritual steps with per-step effort control
- **Sage tools refactor** — consolidated censor into sage, reducing ministers from 6 to 5
- **Init workflow** — converted to ritual-native `project-init` with `:init clear` support
- **Message serialization** — extracted from storage layer to adapter pattern
- **Shogunate branding** — court-themed chat messages and tab styling
- **Config loader** — cleaned up and simplified
- **Stream chunk handling** — folded `StreamReasoningChunkMsg` into `StreamChunkMsg` for unified streaming
- **Podman bumped to 5.8.2**

### Fixed

- Daemon correctly receives working directory and environment variables from client
- RPC connection dropping and automatic reconnection
- Per-client runner isolation — each client uses its own sandbox runner
- Ritual step output rendering and completion handling
- Zhengming routing carries caller MinisterID correctly
- Provider status indicator reflects current state
- Resuming works on all tabs, not just the active one
- Bifrost log messages formatting
- Edict ID assignment on creation
- Bedrock connection initialization
- Court status limited to the current project
- Tool output truncation at proper boundaries
- Context sanitization and partial tool call cleanup
- Daemon liveness probing and eviction of wedged daemons
- Ritual step retry without reloading context
- Multi-project and multi-user session isolation
- Read file size limiting for safety
- Grep tool output correctness
- Tab name color and stream routing to correct tab
- `IsClean()` refreshes status cache before checking
- ANTHROPIC_API_KEY no longer leaks between tests
- Context canceled errors replaced with user-friendly 'aborted by user' message

## [0.5.0] - 2025-01-20

### Added

- Supporting `r` key in normal mode to replace character under cursor (#76)
- Adding tool execution and streaming support to Shogunate Session

### Changed

- Merging `[container]` and `[run_shell_command]` config sections into unified `[sandbox]` section
- Moving config loading functions to `internal/config` package for better code organization
- Replacing Chancellor's `Send()` method with `AskWithStreaming()` for proper tool execution

### Fixed

- Init workflow pre-checks now abort instead of retry when uncommitted changes are detected
- Skiping gitlinks and symlinks when checking for file changes
- Fixing :compact showing duplicate "Compacting..." messages (#121)
- Fixing arrow keys not working in normal mode with empty prompt (#120)
- Ignoring ghost CTRL-C, fix #115

## [0.4.2] - 2026-01-06

### Fixed

- Simplifying Justfile creation on init
- Using the latest langchaingo for OpenAI thoughts
- Retrying in workflow and error handling improvements
- Improving shell runner initialization
- Skipping inaccessible dirs instead of erroring
- Turning on live scroll at bottom
- Preventing file overwrite (#93)

### Changed

- Shortening sandbox id on the status line

## [0.4.1] - 2025-12-29

### Fixed

- .agents/asimi.conf is now properly created, even when there's an exiting CLAUDE.md

## [0.4.0] - 2025-12-28

### Added
- A one char gutter with top & bottom indicators for hidden lines
- One-liner installer script (`curl -fsSL https://asimi.dev/installer | bash`)
- Prompt now grows to `ui.prompt_expanded_height` (default:10) lines when user input is more than one line (including wrapped long text), and returns to 2-line height when in scroll mode or cleared (#31)

### Fixed
- Starting a new session with `:new` now picks up newly built sandbox images without requiring program restart
- Renewing OAuth token on 403 errors
- Clearing message queue on `:init` reinitialize (#107)
- Mouse scroll now properly enters SCROLL mode (#103)
- Background color removed from tool output for cleaner display
- Thinking output now styled consistently with tool output
- Tool denial output formatting improved

## [0.3.2] - 2025-12-15

### Fixed
- CTRL-C debounce time - default shortened to 100ms, added to conf

## [0.3.1] - 2025-12-14

### Added
- Alt+Enter (Option+Enter on Mac) now inserts a newline in the prompt instead of submitting (#101)
- `:logout` command to sign out from the current provider

### Fixed
- Login URL can now be copied
- Status line now fills the entire terminal width when sandbox is showing (#100)
- Resumed sessions now display tool calls exactly as they appeared during the original session (#102)

## [0.3.0] - 2025-01-27

### Changed
- Markdown renderer is on by default
- When no sandbox, use host as fallback
- Command approval now prints the command

### Fixed

- OAuth token refresh now works when server rejects token with 401 error before local expiry time
- Command line prompt now properly dismissed when switching modes

## [0.2.1] - 2025-01-15

### Fixed

- Closing instructions of :init
- Unified tool output
- Init verfication output format and strings

## [0.2.0] - 2025-12-04

### Added

- `:update` command to check for and install updates from GitHub releases
- Automatic update checking on startup (non-blocking, 5-second timeout)
- Self-update capability using go-github-selfupdate
- Support for `ANTHROPIC_OAUTH_TOKEN` environment variable to bypass keyring authentication
  - Accepts raw access token format
  - Accepts full JSON format with refresh token and expiry
  - Accepts base64-encoded JSON (useful when copying from macOS Keychain)
- Configuration option `run_shell_command.timeout_minutes` to set shell command timeout (default: 10 minutes)
- `:!` command prefix to run shell commands in the container (e.g., `:!uname -a`)
- `:resume` command to resume previous sessions
- `:init` command to analyze project and generate infrastructure files (AGENTS.md, Justfile, .agents/asimi.conf, Dockerfile)
  - Automatic verification with retry logic (up to 5 attempts)
  - `:init clear` to regenerate all files from scratch
- Per-branch prompt & command history
- `ui.markdown_enabled` configuration toggle to re-enable Glamour-based markdown rendering (defaults to off for faster resizing) (#53)
- Ctrl-B SCROLL mode for the chat viewport with vi-style paging and `:1` to jump to the first message without re-pinning
- Toast notification when container is launched (#77)

### Changed
- `:init` command now automatically retries with AI-generated fixes when verification fails

### Fixed
- OAuth token now automatically refreshes during chat sessions to prevent 401 errors when token expires mid-conversation
- Context validation error when interrupting tool execution (issue #37)
- Shell command timeouts now properly reported (exit code 124)
- Container connection failures now trigger automatic restart and retry
- Enter now submits prompts directly from vi normal mode when the prompt is non-empty (#32)
- ESC in NORMAL mode now switches to INSERT mode (#70)
- Prompt placeholder now shows helpful navigation hints in RESUME & MODEL modes (#69)
- Conversation history is now automatically compacted when context usage exceeds 90% (#54)
- Model thinking/reasoning messages are now displayed in the chat (e.g., Claude extended thinking) (#38)
- Status line now shows error emoji (❌) when model errors occur mid-conversation (#65)
- Current prompt text is now preserved when navigating history with up/down arrows (#71)

### Removed

- non-vi mode is no longer supported - vi FTW!
- `/` is just a slash. Use `:` to enter command mode



## [0.1.0] - 2025/11/1

A development snapshort made for a friend. Not production ready at all.
inking) (#38)
- Status line now shows error emoji (❌) when model errors occur mid-conversation (#65)
- Current prompt text is now preserved when navigating history with up/down arrows (#71)

### Removed

- non-vi mode is no longer supported - vi FTW!
- `/` is just a slash. Use `:` to enter command mode



[0.10.2]: https://github.com/daonb/asimi/compare/v0.10.1...v0.10.2
[0.10.1]: https://github.com/daonb/asimi/compare/v0.10.0...v0.10.1
[0.10.0]: https://github.com/daonb/asimi/compare/v0.9.2...v0.10.0

## [0.1.0] - 2025/11/1

A development snapshort made for a friend. Not production ready at all.
