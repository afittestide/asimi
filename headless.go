package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/storage"
	"go.uber.org/fx"
)

// runHeadlessMode executes a prompt non-interactively: initializes the
// Court via fx DI, sends the prompt to the secretary, streams plain text
// to stdout, and waits for the ritual (or chat) to complete.
// Returns exit code 0 on success, 1 on failure.
func runHeadlessModeImpl() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var c *court.Court
	var cfg *Config
	var repoInfo repo.RepoInfo

	app := fx.New(
		fx.NopLogger,
		fx.Provide(
			ProvideLogger,
			ProvideRepoInfo,
			ProvideConfig,
			ProvideStorage,
			ProvideGormDB,
			ProvideScheduler,
			ProvideShellRunner,
			ProvideCourt,
		),
		fx.Populate(&c, &cfg, &repoInfo),
	)

	if err := app.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "asimi: failed to start: %v\n", err)
		return 1
	}
	defer app.Stop(context.Background())

	// Initialize the LLM client (same as TUI's Init → SetContext)
	if err := c.SetContext(ctx, headlessContextParams(cfg, &repoInfo)); err != nil {
		fmt.Fprintf(os.Stderr, "asimi: LLM init failed: %v\n", err)
		return 1
	}

	// Signal court_started so event-driven rituals and health checks fire
	c.PublishEvent(c.CourtEdictKey(), storage.EventCourtStarted, storage.JSON{
		"current_version": utils.AsimiVersion,
	})

	sink := newHeadlessSink(c)
	sink.handsoff = cli.Handsoff
	subCtx, cancelSub := context.WithCancel(ctx)
	defer cancelSub()
	events := c.Subscribe(subCtx)
	go sink.drain(events)

	// Submit the prompt to the secretary (same as TUI's SubmitPromptMsg path)
	prompt := &court.Prompt{
		Ctx:       ctx,
		Message:   cli.Prompt,
		ChannelID: "secretary",
	}
	if err := c.SubmitPrompt("secretary", prompt); err != nil {
		fmt.Fprintf(os.Stderr, "asimi: failed to submit prompt: %v\n", err)
		return 1
	}

	// Wait for completion
	exitCode := sink.wait()
	return exitCode
}

// buildSetContextParams extracts SetContextParams from config and repo info.
// Shared by both the TUI (TUIModel.setContextParams) and headless mode
// (headlessContextParams) to keep the two paths in sync.
func buildSetContextParams(cfg *Config, ri *repo.RepoInfo) types.SetContextParams {
	projectRoot := ""
	worktreePath := ""
	branch := ""
	if ri != nil {
		projectRoot = ri.ProjectRoot
		worktreePath = ri.WorktreePath
		branch = ri.Branch
	}
	// Fallback to CWD when repoInfo is unavailable or ProjectRoot is empty
	// (e.g., outside a git repo). The daemon's "." would resolve to its own
	// launch directory, so we must provide an absolute path here.
	if projectRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			projectRoot = cwd
		}
	}
	project := ""
	if cfg != nil {
		project = cfg.Court.Project
	}
	// The handshake username is resolved from the OS user, never from config:
	// a committed [court] username must not choose scope (edict 1006).
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	if username == "" {
		username = "guest"
		slog.Warn("failed to get current user, running as guest")
	}
	// Fall back to repoInfo.Slug (from git remote) when config doesn't set it.
	// This mirrors ProvideCourt (providers.go) and ensures the daemon
	// receives the correct slug for sandbox image naming.
	if project == "" && ri != nil {
		project = ri.Slug
	}
	// Determine agent name for ATIF
	atifAgentName := atifAgentName()

	return types.SetContextParams{
		Project:         project,
		Username:        username,
		ProjectRoot:     projectRoot,
		WorktreePath:    worktreePath,
		Branch:          branch,
		APIKeys:         collectAPIKeys(),
		CodexAccountID:  getCodexAccountID(),
		IsolatedHost:    cli.IsolatedHost,
		AtifAgentName:   atifAgentName,
		Provider:        cli.Provider,
		Model:           cli.Model,
		ReasoningEffort: cli.ReasoningEffort,
	}
}

// headlessContextParams builds SetContextParams for headless mode.
func headlessContextParams(cfg *Config, ri *repo.RepoInfo) types.SetContextParams {
	return buildSetContextParams(cfg, ri)
}

// handsoffSealCourt is the narrow Court seam the handsoff seal pass needs: it
// lists the court-scoped active edicts and grants the Ruler's seal. It exists
// so the pass can be driven by a fake in tests without a live git repo or the
// full Court machinery; *court.Court satisfies it in production.
type handsoffSealCourt interface {
	ListActiveEdicts() ([]storage.ActiveEdict, error)
	GrantRulerSeal(edictID uint, notes string) error
}

// headlessSink receives court events and writes plain text to stdout.
// When handsoff is true, it auto-answers zhengming requests, auto-enacts
// swift-strike rituals, and — once the initial ritual run completes — seals
// every pending-ascension edict and waits for its merge-worktree landing.
// When handsoff is false, zhengming requests are answered interactively:
// each question is printed as a numbered menu and the user selects from stdin.
type headlessSink struct {
	court              *court.Court
	handsoff           bool
	done               chan int
	enactedSet         map[uint]bool // tracks edicts we've already auto-enacted
	pendingSuggestions int           // number of edict suggestions awaiting edict creation
	stdin              io.Reader     // for interactive zhengming prompts
	stdout             io.Writer     // for interactive zhengming menus
	streaming          bool          // true while the LLM is streaming (StreamStart→StreamComplete)
	streamingEdictID   uint          // edict bound to the active stream, 0 when none (from StreamStartMsg)

	// sealCourt is the Court seam used by the handsoff seal pass. Nil for a
	// bare sink with no court (nothing to seal).
	sealCourt handsoffSealCourt
	// landingTimeout bounds how long the handsoff seal pass waits per sealed
	// edict for its terminal landing event. Defaults to handsoffLandingTimeout;
	// overridable so tests can exercise the bound without waiting minutes.
	landingTimeout time.Duration

	// Seal-pass state (handsoff only), guarded by mu. The pass runs once when
	// the initial ritual completes: it seals every pending-ascension edict and
	// waits for their merge-worktree landings before the run exits.
	mu              sync.Mutex
	sealPassStarted bool          // the initial ritual completed and the pass was entered
	sealPassPending bool          // the pass is still discovering/sealing and must not be exited
	sealPassFailed  bool          // the pass could not enumerate pending edicts
	handsoffSealed  map[uint]bool // edicts the pass sealed, awaiting a terminal landing event
	handsoffLanded  map[uint]bool // sealed edicts whose landing reported edict_sealed
	handsoffFailed  map[uint]bool // sealed edicts whose landing terminally failed or timed out
}

func newHeadlessSink(c *court.Court) *headlessSink {
	s := &headlessSink{
		court:          c,
		handsoff:       true, // default true for backward compat; overridden in runHeadlessMode
		done:           make(chan int, 1),
		enactedSet:     make(map[uint]bool),
		handsoffSealed: make(map[uint]bool),
		handsoffLanded: make(map[uint]bool),
		handsoffFailed: make(map[uint]bool),
		landingTimeout: handsoffLandingTimeout,
		stdin:          os.Stdin,
		stdout:         os.Stdout,
	}
	if c != nil {
		s.sealCourt = c
	}
	return s
}

// drain reads events from the subscription channel and dispatches them.
func (s *headlessSink) drain(events <-chan any) {
	for msg := range events {
		s.handle(msg)
	}
}

// wait blocks until the ritual completes or fails.
func (s *headlessSink) wait() int {
	return <-s.done
}

// handle processes a single court event message.
func (s *headlessSink) handle(msg any) {
	switch m := msg.(type) {
	case court.StreamChunkMsg:
		if m.Text != "" {
			fmt.Print(m.Text)
		}

	case court.StreamDoneMsg:
		// Secretary finished streaming. If no ritual was enacted (plain
		// chat without edict creation), the session is done. If a ritual
		// is running — whether auto-enacted by the sink or enacted by
		// the secretary via enact_ritual — completion will be signaled
		// by ritual_completed/ritual_failed events.
		// If an edict suggestion is pending auto-approval (async chain:
		// auto-answer → zhengzhang answered → EdictCreated → ritual), the
		// edict creation hasn't landed yet, so wait rather than exit now.
		// maybeFinish keeps this stream-phase exit mutually exclusive with
		// the handsoff seal-pass exit.
		if len(s.enactedSet) == 0 && s.pendingSuggestions == 0 {
			s.maybeFinish()
		}

	case court.StreamErrorMsg:
		fmt.Fprintf(os.Stderr, "\nError: %v\n", m.Err)
		s.finish(1)

	case court.ZhengmingPendingMsg:
		if s.handsoff {
			// In handsoff mode an edict suggestion is auto-approved. Before
			// answering, surface the proposed edict so the ruler can see it,
			// and register it as pending so StreamDoneMsg doesn't exit before
			// the async edict-creation chain completes.
			if isEdictSuggestion(m) {
				s.printSuggestedEdict(m)
				s.pendingSuggestions++
			}
			s.autoAnswerZhengming(m)
		} else {
			s.interactiveAnswerZhengming(m)
		}

	case tools.EditorRequest:
		// An approve_doc request (e.g. from suggest_edict for payloads >500
		// chars) blocks on ResultChan. Headless has no interactive editor, so
		// always auto-approve the content unchanged. Print it to stdout so
		// the ruler still sees what was proposed.
		if !s.handsoff {
			fmt.Fprintf(s.stdout, "\n--- Document for review ---\n%s\n--- End ---\n", m.Content)
		}
		m.ResultChan <- tools.EditorResult{Content: m.Content, Saved: true}

	case court.EventNotificationMsg:
		s.handleEvent(m)

	case court.RitualStepMsg:
		s.handleRitualStep(m)

	case court.MinisterInvokingMsg:
		fmt.Printf("\n[%s] ", m.MinisterID)

	case court.MinisterCompletedMsg:
		if m.Error != nil {
			fmt.Printf(" failed: %v\n", m.Error)
		} else {
			fmt.Println(" done")
		}

	case runners.ToolCallSuccessMsg:
		if md, ok := markdownEnvelopeContent(m.Result); ok {
			if md != "" {
				fmt.Printf("\n[%s]\n%s\n", m.ToolName, md)
			}
		} else if m.Result != "" {
			fmt.Printf("\n[%s] %s\n", m.ToolName, m.Result)
		}

	case runners.ToolCallErrorMsg:
		fmt.Printf("\n[%s error] %s\n", m.ToolName, m.Error)

	case runners.ToolCallScheduledMsg:
		// Progress-only signal (the TUI shows a spinner); headless prints
		// the outcome via ToolCallSuccessMsg/ToolCallErrorMsg. Consume at
		// debug level so a swift-strike ritual doesn't flood the log with
		// unhandled-event warnings.
		slog.Debug("headless: tool call scheduled", "tool", m.ToolName)

	case runners.ToolCallExecutingMsg:
		slog.Debug("headless: tool call executing", "tool", m.ToolName)

	case court.StreamStartMsg:
		// Mirror the TUI's streaming-tab tracking: mark the stream active and
		// capture the edict ID for future use. Never warn — this is routine.
		s.streaming = true
		s.streamingEdictID = m.EdictID
		slog.Debug("headless: stream started", "edict_id", m.EdictID)

	case court.StreamCompleteMsg:
		s.streaming = false
		s.streamingEdictID = 0
		slog.Debug("headless: stream completed")

	case court.StreamInterruptedMsg:
		// The session already stopped; just reset streaming state (the
		// partial content was already printed via StreamChunkMsg).
		s.streaming = false
		s.streamingEdictID = 0
		slog.Debug("headless: stream interrupted")

	default:
		// Unhandled message types used to be silently dropped (e.g. the
		// EditorRequest hang before we added its case). Warn so a flapping
		// or missing handler is caught early rather than deadlocking tools.
		slog.Warn("headless: unhandled event", "type", fmt.Sprintf("%T", msg))
	}
}

// handleEvent processes EventNotificationMsg, auto-enacting swift-strike
// when an edict is created (mirrors TUI handsoff behavior).
func (s *headlessSink) handleEvent(msg court.EventNotificationMsg) {
	switch msg.EventType {
	case storage.EventRitualEnacted:
		// Track rituals enacted by the secretary (via enact_ritual tool).
		// This prevents StreamDoneMsg from finishing early while the
		// ritual goroutine is still running. The sink's own auto-enact
		// path (EventEdictCreated → enactSwiftStrike) already adds to
		// enactedSet before publishing this event, so we only add here
		// if not already present.
		edictID := msg.EdictKey.ID
		if edictID != 0 && !s.enactedSet[edictID] {
			s.enactedSet[edictID] = true
		}

	case storage.EventEdictCreated:
		edictID := msg.EdictKey.ID
		if edictID == 0 {
			return
		}
		if s.pendingSuggestions > 0 {
			s.pendingSuggestions--
		}
		if s.enactedSet[edictID] {
			return
		}
		s.enactedSet[edictID] = true
		intent, _ := msg.Payload["intent"].(string)
		if intent == "" {
			intent = "New edict"
		}
		if len(intent) > 60 {
			intent = intent[:57] + "..."
		}
		fmt.Printf("\n[edict %d created] %s\n", edictID, intent)
		if s.handsoff {
			s.enactSwiftStrike(edictID)
		}

	case storage.EventRitualCompleted:
		fmt.Printf("\n[ritual completed for edict %d]\n", msg.EdictKey.ID)
		s.onRitualCompleted()

	case storage.EventRitualFailed:
		errMsg, _ := msg.Payload["error"].(string)
		fmt.Fprintf(os.Stderr, "\n[ritual failed for edict %d: %s]\n", msg.EdictKey.ID, errMsg)
		s.finish(1)

	case storage.EventEdictSealed:
		fmt.Printf("\n[edict %d sealed — ascended to Heaven]\n", msg.EdictKey.ID)
		s.recordLanding(msg.EdictKey.ID, true)

	case storage.EventWorktreeMergeFailed:
		s.handleMergeFailed(msg)
	}
}

// handleRitualStep processes RitualStepMsg, watching for completion/failure.
func (s *headlessSink) handleRitualStep(msg court.RitualStepMsg) {
	switch msg.Status {
	case "started":
		if msg.StepName == "" {
			fmt.Printf("\n[ritual %s started for edict %d]\n", msg.RitualName, msg.EdictID)
		} else if msg.ForkItem != "" {
			fmt.Printf("\n  %s [%s]: %s\n", msg.StepName, msg.ForkItem, msg.Message)
		} else if msg.TotalSteps > 1 {
			fmt.Printf("\n  step %d/%d: %s\n", msg.StepIndex+1, msg.TotalSteps, msg.StepName)
		}
	case "completed":
		if msg.Message != "" {
			fmt.Printf("  ✓ %s\n", msg.Message)
		}
	case "failed":
		fmt.Printf("  ✗ Failed: %s\n", msg.StepName)
	case "ritual_completed":
		fmt.Printf("\n[ritual %s completed in %s]\n", msg.RitualName, msg.Message)
		s.onRitualCompleted()
	case "ritual_failed":
		fmt.Fprintf(os.Stderr, "\n[ritual %s failed: %s]\n", msg.RitualName, msg.Message)
		s.finish(1)
	}
}

// onRitualCompleted is the single funnel for "the initial ritual run
// finished". In handsoff it enters the seal pass (seal every pending-ascension
// edict and wait for its landing) instead of exiting; otherwise it finishes as
// before. The pass runs at most once per run, so a later ritual completion
// cannot restart it.
func (s *headlessSink) onRitualCompleted() {
	if !s.handsoff {
		s.maybeFinish()
		return
	}
	s.mu.Lock()
	if s.sealPassStarted {
		s.mu.Unlock()
		s.maybeFinish()
		return
	}
	s.sealPassStarted = true
	s.sealPassPending = true
	s.mu.Unlock()
	s.sealPendingEdicts()
}

// maybeFinish is the only place the run exits. It refuses while the handsoff
// seal pass is still sealing or while any sealed edict still owes a terminal
// landing event; otherwise it emits the exit code once, derived from the state
// accumulated at that moment (0 unless a landing failed). Being the sole
// sender keeps the stream-phase exit and the seal-pass exit mutually exclusive,
// and a late event cannot downgrade a code already sent: finish keeps the first
// value, and the code is decided from accumulated state at the moment of the
// (only) send.
func (s *headlessSink) maybeFinish() {
	s.mu.Lock()
	pending := s.sealPassPending || s.awaitingLandingLocked() > 0
	code := 0
	if s.sealPassFailed || len(s.handsoffFailed) > 0 {
		code = 1
	}
	s.mu.Unlock()
	if pending {
		return
	}
	s.finish(code)
}

// awaitingLandingLocked counts sealed edicts that have not yet reported a
// terminal landing. Caller holds s.mu.
func (s *headlessSink) awaitingLandingLocked() int {
	return len(s.handsoffSealed) - len(s.handsoffLanded) - len(s.handsoffFailed)
}

// handsoffLandingTimeout is the default per-edict budget for the seal pass to
// wait for a landing. Landings retry through merge-worktree (max_retries: 3)
// and can stall on a live git operation, so an unbounded wait would hang an
// autonomous run forever. Matching the generous host timeouts elsewhere in the
// court, the budget is a few minutes per sealed edict (scaled by the number of
// edicts); on expiry the pass reports the outstanding edicts and exits
// non-zero rather than exiting silently green.
const handsoffLandingTimeout = 5 * time.Minute

// sealPendingEdicts is the handsoff seal pass. It runs once when the initial
// ritual run completes, sweeping *all* edicts in the court's scope that carry
// both the Judge's and the Chancellor's seals and no Ruler's seal (pending
// ascension). Each is granted the Ruler's seal, which publishes
// seal_granted{minister_id: ruler} and triggers the merge-worktree landing;
// the run then waits (bounded by landingTimeout per edict) for those landings
// before exiting, with code 0 only when every selected edict reached
// edict_sealed. ListActiveEdicts already excludes ruler-sealed and cancelled
// edicts, so re-running never double-seals or re-lands anything.
//
// Sealing is synchronous (fast, bounded by the number of pending edicts) and
// registers each edict as awaiting its landing BEFORE granting the seal, so a
// terminal event that races ahead of the bookkeeping is never dropped. Only the
// timeout watchdog runs in a goroutine, so the drain loop keeps handling the
// landing events the pass is waiting on.
func (s *headlessSink) sealPendingEdicts() {
	sealed, err := s.sealableEdicts()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[handsoff] seal pass failed: %v\n", err)
		s.mu.Lock()
		s.sealPassFailed = true
		s.mu.Unlock()
	}

	for _, edictID := range sealed {
		fmt.Printf("[handsoff] sealing edict %d (judge+chancellor present)\n", edictID)
	}

	s.mu.Lock()
	s.sealPassPending = false
	outstanding := s.awaitingLandingLocked() > 0
	s.mu.Unlock()

	// maybeFinish both handles the no-edicts case and releases an edict whose
	// landing event raced ahead of sealing (it is already terminal).
	s.maybeFinish()
	if outstanding {
		go s.watchLandingTimeout()
	}
}

// watchLandingTimeout fails the pass if its sealed edicts never report a
// terminal landing within landingTimeout each (scaled by the edict count).
// Outstanding edicts are reported and counted as failures so an autonomous run
// that left work unlanded exits non-zero.
func (s *headlessSink) watchLandingTimeout() {
	s.mu.Lock()
	timeout := s.landingTimeout
	count := len(s.handsoffSealed)
	s.mu.Unlock()
	if timeout <= 0 {
		timeout = handsoffLandingTimeout
	}
	if count < 1 {
		count = 1
	}
	time.Sleep(timeout * time.Duration(count))

	s.mu.Lock()
	var outstanding []uint
	for id := range s.handsoffSealed {
		if !s.handsoffLanded[id] && !s.handsoffFailed[id] {
			s.handsoffFailed[id] = true
			outstanding = append(outstanding, id)
		}
	}
	s.mu.Unlock()
	if len(outstanding) == 0 {
		return
	}
	sort.Slice(outstanding, func(i, j int) bool { return outstanding[i] < outstanding[j] })
	fmt.Fprintf(os.Stderr, "[handsoff] landing timed out for edicts %v\n", outstanding)
	s.maybeFinish()
}

// sealableEdicts grants the Ruler's seal to every edict the pass must seal and
// returns their IDs: rows from the court's scoped active list that carry both
// the Judge's and the Chancellor's seals. Each edict is registered as awaiting
// its landing before the seal is granted, so an event cannot race ahead of the
// bookkeeping. Rows lacking either seal are half-sealed and left to the normal
// flow; edicts 0/1 (system and court infrastructure) are never sealed. A nil
// court (bare test sink) has nothing to seal.
func (s *headlessSink) sealableEdicts() ([]uint, error) {
	if s.sealCourt == nil {
		return nil, nil
	}
	active, err := s.sealCourt.ListActiveEdicts()
	if err != nil {
		return nil, err
	}
	sealed := make([]uint, 0, len(active))
	for _, e := range active {
		if !e.HasJudgeSeal || !e.HasChancellorSeal {
			continue
		}
		if e.ID <= 1 {
			continue // reserved infrastructure edict
		}
		s.mu.Lock()
		s.handsoffSealed[e.ID] = true
		s.mu.Unlock()
		if err := s.sealCourt.GrantRulerSeal(e.ID, "handsoff auto-seal"); err != nil {
			s.mu.Lock()
			delete(s.handsoffSealed, e.ID)
			s.handsoffFailed[e.ID] = true
			s.mu.Unlock()
			fmt.Fprintf(os.Stderr, "[handsoff] failed to seal edict %d: %v\n", e.ID, err)
			continue
		}
		sealed = append(sealed, e.ID)
	}
	return sealed, nil
}

// recordLanding records a terminal landing event for a sealed edict: ok true
// for edict_sealed, false for a terminal failure. An event for an edict the
// pass did not seal, or a second terminal event for an edict that already has
// one, is ignored idempotently.
func (s *headlessSink) recordLanding(edictID uint, ok bool) {
	s.mu.Lock()
	if !s.handsoffSealed[edictID] || s.handsoffLanded[edictID] || s.handsoffFailed[edictID] {
		s.mu.Unlock()
		return
	}
	if ok {
		s.handsoffLanded[edictID] = true
	} else {
		s.handsoffFailed[edictID] = true
	}
	s.mu.Unlock()
	s.maybeFinish()
}

// handleMergeFailed surfaces a terminal landing failure for an edict the
// handsoff pass sealed, so the run exits non-zero instead of hiding the red.
// The payload's `merged` flag distinguishes a merge that succeeded but whose
// worktree cleanup failed from one that never landed. Events for edicts the
// pass did not seal — including every merge failure when handsoff is off — are
// ignored, so the print never leaks into a non-handsoff run and a second
// terminal event for an already-terminal edict does not print twice.
func (s *headlessSink) handleMergeFailed(msg court.EventNotificationMsg) {
	edictID := msg.EdictKey.ID
	if !s.handsoffAwaitingLanding(edictID) {
		return
	}
	reason, _ := msg.Payload["reason"].(string)
	merged, _ := msg.Payload["merged"].(bool)
	if merged {
		fmt.Fprintf(os.Stderr, "[handsoff] edict %d land failed: %s (landed but cleanup failed)\n", edictID, reason)
	} else {
		fmt.Fprintf(os.Stderr, "[handsoff] edict %d land failed: %s (NOT merged)\n", edictID, reason)
	}
	s.recordLanding(edictID, false)
}

// handsoffAwaitingLanding reports whether the pass sealed edictID and has not
// yet recorded its terminal landing event. Guarded by s.mu.
func (s *headlessSink) handsoffAwaitingLanding(edictID uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handsoffSealed[edictID] && !s.handsoffLanded[edictID] && !s.handsoffFailed[edictID]
}

// isEdictSuggestion reports whether a zhengming request is an edict
// suggestion (as created by the suggest_edict tool): a single question whose
// first option approves edict creation and that targets a brand-new edict.
func isEdictSuggestion(msg court.ZhengmingPendingMsg) bool {
	return len(msg.Questions) > 0 &&
		len(msg.Questions[0].Options) > 0 &&
		msg.Questions[0].Options[0] == tools.AnswerApproveEdict &&
		msg.EdictKey.ID == 0
}

// printSuggestedEdict prints a formatted block showing the proposed edict to
// stdout before the handsoff auto-answer approves it.
func (s *headlessSink) printSuggestedEdict(msg court.ZhengmingPendingMsg) {
	q := msg.Questions[0]
	fmt.Fprintf(s.stdout, "\n📜 Suggested New Edict\n")
	if q.Summary != "" {
		fmt.Fprintf(s.stdout, "%s\n", q.Summary)
	}
	fmt.Fprintf(s.stdout, "%s\n", q.Text)
	fmt.Fprintf(s.stdout, "[handsoff] Approving...\n")
}

// autoAnswerZhengming answers with the recommended option (option[0]) for
// each question, mirroring the TUI's handsoff mode.
func (s *headlessSink) autoAnswerZhengming(msg court.ZhengmingPendingMsg) {
	if s.court == nil {
		return
	}
	answers := make([]string, len(msg.Questions))
	for i, q := range msg.Questions {
		if len(q.Options) > 0 {
			answers[i] = q.Options[0]
		}
	}
	answer := strings.Join(answers, "; ")
	slog.Info("headless: auto-answering zhengming", "request_id", msg.RequestID, "answer", answer)
	go func() {
		if err := s.court.HandleZhengmingResponse(context.Background(), msg.RequestID, answer); err != nil {
			slog.Error("headless: failed to answer zhengming", "error", err)
		}
	}()
}

// interactiveAnswerZhengming prints each question as a numbered menu to
// stdout, reads the user's selection from stdin, and submits the answers
// via HandleZhengmingResponse. Invalid input re-prompts the same question.
func (s *headlessSink) interactiveAnswerZhengming(msg court.ZhengmingPendingMsg) {
	if s.court == nil {
		return
	}
	answers := make([]string, len(msg.Questions))
	for i, q := range msg.Questions {
		answers[i] = s.promptQuestion(q)
	}
	answer := strings.Join(answers, "; ")
	slog.Info("headless: interactive zhengming answered", "request_id", msg.RequestID, "answer", answer)
	go func() {
		if err := s.court.HandleZhengmingResponse(context.Background(), msg.RequestID, answer); err != nil {
			slog.Error("headless: failed to answer zhengming", "error", err)
		}
	}()
}

// promptQuestion prints a numbered menu for a single question and reads
// the user's selection from stdin, re-prompting on invalid input.
func (s *headlessSink) promptQuestion(q storage.ZhengmingQuestion) string {
	label := q.Text
	if q.Summary != "" {
		label = q.Summary
	}
	fmt.Fprintf(s.stdout, "\n%s\n", label)
	for {
		for j, opt := range q.Options {
			if j > 0 {
				fmt.Fprint(s.stdout, "  ")
			}
			fmt.Fprintf(s.stdout, "%d) %s", j+1, opt)
		}
		fmt.Fprint(s.stdout, "\n> ")

		var input string
		fmt.Fscanln(s.stdin, &input)

		n, err := strconv.Atoi(input)
		if err != nil || n < 1 || n > len(q.Options) {
			fmt.Fprintf(s.stdout, "Invalid selection. Please enter a number 1-%d.\n", len(q.Options))
			continue
		}
		return q.Options[n-1]
	}
}

// enactSwiftStrike publishes EventRitualEnacted to trigger the swift-strike
// ritual for the given edict, mirroring the TUI's enactRitualForEdict.
func (s *headlessSink) enactSwiftStrike(edictID uint) {
	if s.court == nil {
		return
	}
	key := s.court.EdictKey(edictID)
	payload := storage.JSON{
		"ritual_name": "swift-strike",
		"edict_id":    edictID,
		"inputs": map[string]interface{}{
			"edict_id": fmt.Sprintf("%d", edictID),
		},
	}
	fmt.Printf("[enacting swift-strike for edict %d]\n", edictID)
	s.court.PublishEvent(key, storage.EventRitualEnacted, payload)
}

// finish sends the exit code to the done channel (once).
func (s *headlessSink) finish(code int) {
	select {
	case s.done <- code:
	default:
	}
}
