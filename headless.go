package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"strconv"
	"strings"

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
func runHeadlessMode() int {
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
	username := ""
	if cfg != nil {
		project = cfg.Court.Project
		username = cfg.Court.Username
	}
	if username == "" {
		if u, err := user.Current(); err == nil {
			username = u.Username
		}
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

// headlessSink receives court events and writes plain text to stdout.
// When handsoff is true, it auto-answers zhengming requests and auto-enacts
// swift-strike rituals, mirroring the TUI's handsoff mode behavior.
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
}

func newHeadlessSink(c *court.Court) *headlessSink {
	return &headlessSink{
		court:      c,
		handsoff:   true, // default true for backward compat; overridden in runHeadlessMode
		done:       make(chan int, 1),
		enactedSet: make(map[uint]bool),
		stdin:      os.Stdin,
		stdout:     os.Stdout,
	}
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
		if len(s.enactedSet) == 0 && s.pendingSuggestions == 0 {
			s.finish(0)
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
		if m.Result != "" {
			fmt.Printf("\n[%s] %s\n", m.ToolName, m.Result)
		}

	case runners.ToolCallErrorMsg:
		fmt.Printf("\n[%s error] %s\n", m.ToolName, m.Error)

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
		s.finish(0)

	case storage.EventRitualFailed:
		errMsg, _ := msg.Payload["error"].(string)
		fmt.Fprintf(os.Stderr, "\n[ritual failed for edict %d: %s]\n", msg.EdictKey.ID, errMsg)
		s.finish(1)

	case storage.EventEdictSealed:
		fmt.Printf("\n[edict %d sealed — ascended to Heaven]\n", msg.EdictKey.ID)
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
		s.finish(0)
	case "ritual_failed":
		fmt.Fprintf(os.Stderr, "\n[ritual %s failed: %s]\n", msg.RitualName, msg.Message)
		s.finish(1)
	}
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
