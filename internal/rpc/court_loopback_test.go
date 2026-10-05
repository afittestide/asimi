package rpc

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/courtapi"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/wire"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
)

// fakeCourt embeds courtapi.Client so any method the test
// doesn't stub panics at runtime (reaching the nil embedded interface).
// Only the methods exercised by TestCourtRPCLoopback are defined.
type fakeCourt struct {
	courtapi.Client

	mu        sync.Mutex
	hasIDs    map[string]bool
	resetLog  []string
	edicts    map[uint]*storage.Edict
	seals     map[uint][]storage.Seal
	sealNotes map[uint]string
	fallback  bool
	cancels   []string
	clears    []string
	pauses    []string
	resumes   []string
	messages  []string // "target/role/content"
	ctxFiles  []string // "target/path/content"
	rollbacks []string // "target/snapshot"
	zhengAns  []string // "reqID/answer"
	sessions  map[string]court.SessionState
	compacted []string // "target/prompt"
	shellCmds []runners.Input
	events    []struct {
		Key       storage.EdictKey
		EventType storage.CourtEvent
		Payload   storage.JSON
	}
	prompts  []string // "target/message"
	restored []string // "tabType/N"
	listed   []string // providers passed to ListModels
}

func newFakeCourt() *fakeCourt {
	return &fakeCourt{
		hasIDs:    map[string]bool{},
		edicts:    map[uint]*storage.Edict{},
		seals:     map[uint][]storage.Seal{},
		sealNotes: map[uint]string{},
		sessions:  map[string]court.SessionState{},
	}
}

func (f *fakeCourt) HasMinister(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hasIDs[id]
}

func (f *fakeCourt) ResetMinisterSession(id string, channelID ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetLog = append(f.resetLog, id)
}

func (f *fakeCourt) EdictKey(edictID uint) storage.EdictKey {
	return storage.EdictKey{ID: edictID, Username: "alice", Project: "asimi"}
}

func (f *fakeCourt) CourtEdictKey() storage.EdictKey {
	return storage.EdictKey{ID: 1, Username: "alice", Project: "asimi"}
}

func (f *fakeCourt) CreateEdict(issueRef, intent, sessionID string) (*storage.Edict, error) {
	return f.makeEdict(issueRef, intent)
}

func (f *fakeCourt) CreateEdictSilent(issueRef, intent, sessionID string) (*storage.Edict, error) {
	return f.makeEdict(issueRef, intent)
}

func (f *fakeCourt) CancelEdict(edictID uint) error {
	return nil
}

func (f *fakeCourt) AppendToIntent(edictID uint, clarification string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.edicts[edictID]; ok {
		e.Intent += "\n\n---\n**Clarification:**\n" + clarification
	}
	return nil
}

func (f *fakeCourt) SetIntent(edictID uint, intent string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.edicts[edictID]; ok {
		e.Intent = intent
	}
	return nil
}

func (f *fakeCourt) makeEdict(issueRef, intent string) (*storage.Edict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uint(len(f.edicts) + 1)
	e := &storage.Edict{ID: id, Username: "alice", Project: "asimi", IssueRef: issueRef, Intent: intent}
	f.edicts[id] = e
	return e, nil
}

func (f *fakeCourt) GetEdict(edictID uint) (*storage.Edict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.edicts[edictID]
	if !ok {
		return nil, errors.New("edict not found")
	}
	return e, nil
}

func (f *fakeCourt) GrantRulerSeal(edictID uint, notes string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.edicts[edictID]; !ok {
		return errors.New("edict not found")
	}
	f.sealNotes[edictID] = notes
	return nil
}

func (f *fakeCourt) ListActiveEdicts() ([]storage.ActiveEdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]storage.ActiveEdict, 0, len(f.edicts))
	for _, e := range f.edicts {
		out = append(out, storage.ActiveEdict{Edict: *e})
	}
	return out, nil
}

func (f *fakeCourt) HandleZhengmingResponse(ctx context.Context, requestID, answer string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zhengAns = append(f.zhengAns, requestID+"/"+answer)
	return nil
}

func (f *fakeCourt) CancelZhengming(requestID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, requestID)
}

func (f *fakeCourt) AllowRunnerFallback(allow bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallback = allow
}

func (f *fakeCourt) AddSessionMessage(target, role, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, target+"/"+role+"/"+content)
	return nil
}

func (f *fakeCourt) ClearSessionHistory(target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clears = append(f.clears, target)
	return nil
}

func (f *fakeCourt) RollbackSession(target string, snap int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbacks = append(f.rollbacks, target)
	return nil
}

func (f *fakeCourt) AddSessionContextFile(target, path, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctxFiles = append(f.ctxFiles, target+"/"+path+"/"+content)
	return nil
}

func (f *fakeCourt) CompactSession(ctx context.Context, target, prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compacted = append(f.compacted, target+"/"+prompt)
	return "summary for " + target, nil
}

func (f *fakeCourt) SessionState(target string) court.SessionState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[target]
}

func (f *fakeCourt) GetEdictSeals(key storage.EdictKey) ([]storage.Seal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seals[key.ID], nil
}

func (f *fakeCourt) PublishEvent(key storage.EdictKey, et storage.CourtEvent, payload storage.JSON) uint {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, struct {
		Key       storage.EdictKey
		EventType storage.CourtEvent
		Payload   storage.JSON
	}{key, et, payload})
	return uint(len(f.events))
}

func (f *fakeCourt) RunShellCommand(ctx context.Context, in runners.Input) (runners.Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shellCmds = append(f.shellCmds, in)
	return runners.Output{Output: "ran: " + in.Command, ExitCode: "0"}, nil
}

func (f *fakeCourt) SubmitPrompt(target string, p *court.Prompt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, target+"/"+p.Message)
	return nil
}

func (f *fakeCourt) RestoreMinisterSession(tabType string, msgs []schemas.ChatMessage, channelID ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restored = append(f.restored, tabType+"/"+intString(len(msgs)))
	return nil
}

func (f *fakeCourt) TakeSnapshot() court.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return court.Snapshot{
		TakenAt: time.Unix(1700000000, 0),
		Rituals: []court.RitualEntry{{RitualName: "swift-strike", EdictID: 9, State: "running", CurrentStep: 1, TotalSteps: 3}},
	}
}

func (f *fakeCourt) ListModels(provider string) (*schemas.BifrostListModelsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed = append(f.listed, provider)
	name := provider + " display"
	ctx := 128000
	prompt := "0.005"
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{{
			ID:            provider + "-model",
			Name:          &name,
			ContextLength: &ctx,
			Pricing:       &schemas.Pricing{Prompt: &prompt},
		}},
		NextPageToken: "tok",
	}
	resp.ExtraFields.Provider = schemas.OpenAI
	resp.ExtraFields.RequestType = schemas.ListModelsRequest
	return resp, nil
}

func (f *fakeCourt) CancelTab(channelID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, "tab:"+channelID)
}

func (f *fakeCourt) ApproximateContextSize(provider, model string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if provider == "openai" && model == "gpt-4o" {
		return 128000
	}
	return 0
}

func (f *fakeCourt) PauseRitual(channelID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses = append(f.pauses, channelID)
	return true
}

func (f *fakeCourt) ResumeRitual(channelID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, channelID)
	return true
}

func (f *fakeCourt) CancellableStreamCtx(_ string) context.Context {
	return context.Background()
}

func intString(n int) string {
	switch n {
	case 0:
		return "0"
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	default:
		return "many"
	}
}

// TestCourtRPCLoopback exercises the wire-safe subset of the
// Court RPC surface end-to-end over net.Pipe.
func TestCourtRPCLoopback(t *testing.T) {
	impl := newFakeCourt()
	impl.hasIDs["secretary"] = true
	impl.hasIDs["chancellor"] = true

	pa, pb := net.Pipe()
	server := New(pa, Options{})
	clientConn := New(pb, Options{})

	RegisterCourtHandlers(server, impl)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = server.Serve() }()
	go func() { defer wg.Done(); _ = clientConn.Serve() }()
	defer func() {
		_ = clientConn.Close()
		_ = server.Close()
		wg.Wait()
	}()

	client := NewCourtClient(clientConn)

	if !client.HasMinister("secretary") {
		t.Error("HasMinister(chancellor) = false")
	}
	if client.HasMinister("unknown") {
		t.Error("HasMinister(unknown) = true")
	}

	client.ResetMinisterSession("chancellor")
	impl.mu.Lock()
	if got := impl.resetLog; len(got) != 1 || got[0] != "chancellor" {
		t.Errorf("resetLog = %v", got)
	}
	impl.mu.Unlock()

	if k := client.EdictKey(42); k.ID != 42 || k.Username != "alice" {
		t.Errorf("EdictKey = %+v", k)
	}
	if k := client.CourtEdictKey(); k.ID != 1 {
		t.Errorf("CourtEdictKey = %+v", k)
	}

	e, err := client.CreateEdict("#7", "add feature", "")
	if err != nil {
		t.Fatalf("CreateEdict: %v", err)
	}
	if e.Intent != "add feature" || e.ID == 0 {
		t.Errorf("edict = %+v", e)
	}

	got, err := client.GetEdict(e.ID)
	if err != nil {
		t.Fatalf("GetEdict: %v", err)
	}
	if got.Intent != "add feature" {
		t.Errorf("got = %+v", got)
	}

	if _, err := client.GetEdict(99); err == nil {
		t.Error("GetEdict(99): want error")
	}

	// CancelEdict round-trip.
	if err := client.CancelEdict(e.ID); err != nil {
		t.Fatalf("CancelEdict: %v", err)
	}
	impl.mu.Lock()
	if len(impl.edicts) == 0 || impl.edicts[e.ID] == nil {
		t.Errorf("edict %d should still exist after cancel", e.ID)
	}
	impl.mu.Unlock()

	// AppendToIntent round-trip.
	if err := client.AppendToIntent(e.ID, "additional context"); err != nil {
		t.Fatalf("AppendToIntent: %v", err)
	}
	impl.mu.Lock()
	if ed := impl.edicts[e.ID]; ed != nil {
		if !strings.Contains(ed.Intent, "additional context") {
			t.Errorf("intent after AppendToIntent = %q", ed.Intent)
		}
	}
	impl.mu.Unlock()

	// SetIntent round-trip — should replace, not append.
	if err := client.SetIntent(e.ID, "brand new intent"); err != nil {
		t.Fatalf("SetIntent: %v", err)
	}
	impl.mu.Lock()
	if ed := impl.edicts[e.ID]; ed != nil {
		if ed.Intent != "brand new intent" {
			t.Errorf("intent after SetIntent = %q, want %q", ed.Intent, "brand new intent")
		}
	}
	impl.mu.Unlock()

	if err := client.GrantRulerSeal(e.ID, "looks good"); err != nil {
		t.Fatalf("GrantRulerSeal: %v", err)
	}
	impl.mu.Lock()
	if impl.sealNotes[e.ID] != "looks good" {
		t.Errorf("sealNotes[%d] = %q", e.ID, impl.sealNotes[e.ID])
	}
	impl.mu.Unlock()

	active, err := client.ListActiveEdicts()
	if err != nil {
		t.Fatalf("ListActiveEdicts: %v", err)
	}
	if len(active) != 1 || active[0].ID != e.ID {
		t.Errorf("active = %+v", active)
	}

	client.CancelZhengming("req-1")
	impl.mu.Lock()
	if len(impl.cancels) != 1 || impl.cancels[0] != "req-1" {
		t.Errorf("cancels = %v", impl.cancels)
	}
	impl.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.HandleZhengmingResponse(ctx, "req-2", "yes"); err != nil {
		t.Fatalf("HandleZhengmingResponse: %v", err)
	}
	impl.mu.Lock()
	if len(impl.zhengAns) != 1 || impl.zhengAns[0] != "req-2/yes" {
		t.Errorf("zhengAns = %v", impl.zhengAns)
	}
	impl.mu.Unlock()

	client.AllowRunnerFallback(true)
	impl.mu.Lock()
	if !impl.fallback {
		t.Error("AllowRunnerFallback not applied")
	}
	impl.mu.Unlock()

	if err := client.ClearSessionHistory("ruling"); err != nil {
		t.Fatalf("ClearSessionHistory: %v", err)
	}
	if err := client.RollbackSession("ruling", 5); err != nil {
		t.Fatalf("RollbackSession: %v", err)
	}
	if err := client.AddSessionMessage("ruling", "human", "hi"); err != nil {
		t.Fatalf("AddSessionMessage: %v", err)
	}
	impl.mu.Lock()
	if len(impl.clears) != 1 || impl.clears[0] != "ruling" {
		t.Errorf("clears = %v", impl.clears)
	}
	if len(impl.rollbacks) != 1 {
		t.Errorf("rollbacks = %v", impl.rollbacks)
	}
	if len(impl.messages) != 1 || impl.messages[0] != "ruling/human/hi" {
		t.Errorf("messages = %v", impl.messages)
	}
	impl.mu.Unlock()

	// AddSessionContextFile, CompactSession, SessionState, GetEdictSeals,
	// PublishEvent, RunShellCommand, SubmitPrompt, RestoreMinisterSession.

	if err := client.AddSessionContextFile("ruling", "a.go", "package x"); err != nil {
		t.Fatalf("AddSessionContextFile: %v", err)
	}
	impl.mu.Lock()
	if len(impl.ctxFiles) != 1 || impl.ctxFiles[0] != "ruling/a.go/package x" {
		t.Errorf("ctxFiles = %v", impl.ctxFiles)
	}
	impl.mu.Unlock()

	summary, err := client.CompactSession(ctx, "ruling", "summarise")
	if err != nil {
		t.Fatalf("CompactSession: %v", err)
	}
	if summary != "summary for ruling" {
		t.Errorf("summary = %q", summary)
	}

	impl.mu.Lock()
	impl.sessions["ruling"] = court.SessionState{
		Exists:              true,
		ChannelID:           "ruling",
		MessageCount:        3,
		MessageSnapshot:     2,
		ContextUsagePercent: 0.42,
		ContextInfo:         court.ContextInfo{Model: "claude", TotalTokens: 200000, UsedTokens: 42000},
		ContextFiles:        map[string]string{"a.go": "package x"},
	}
	impl.mu.Unlock()
	state := client.SessionState("ruling")
	if !state.Exists || state.ChannelID != "ruling" || state.MessageCount != 3 ||
		state.ContextInfo.Model != "claude" || state.ContextInfo.UsedTokens != 42000 ||
		state.ContextFiles["a.go"] != "package x" {
		t.Errorf("SessionState round-trip: %+v", state)
	}

	// PublishEvent + GetEdictSeals.
	payload := storage.JSON{"note": "shipping"}
	id := client.PublishEvent(storage.EdictKey{ID: 9, Username: "alice", Project: "asimi"}, storage.EventEdictCreated, payload)
	if id == 0 {
		t.Error("PublishEvent: zero ID")
	}
	impl.mu.Lock()
	if len(impl.events) != 1 || impl.events[0].EventType != storage.EventEdictCreated || impl.events[0].Payload["note"] != "shipping" {
		t.Errorf("events = %+v", impl.events)
	}
	impl.seals[9] = []storage.Seal{{SealID: "s1", EdictID: 9, MinisterID: "judge"}}
	impl.mu.Unlock()
	seals, err := client.GetEdictSeals(storage.EdictKey{ID: 9})
	if err != nil {
		t.Fatalf("GetEdictSeals: %v", err)
	}
	if len(seals) != 1 || seals[0].MinisterID != "judge" {
		t.Errorf("seals = %+v", seals)
	}

	// RunShellCommand.
	out, err := client.RunShellCommand(ctx, runners.Input{Command: "echo hi", Description: "test"})
	if err != nil {
		t.Fatalf("RunShellCommand: %v", err)
	}
	if out.Output != "ran: echo hi" || out.ExitCode != "0" {
		t.Errorf("RunShellCommand out = %+v", out)
	}

	// SubmitPrompt (ctx should be rebuilt server-side).
	if err := client.SubmitPrompt("secretary", &court.Prompt{
		Message:      "hello shogun",
		EdictKey:     storage.EdictKey{ID: 42},
		ChannelID:    "ruling",
		ContextFiles: map[string]string{"x.md": "hi"},
	}); err != nil {
		t.Fatalf("SubmitPrompt: %v", err)
	}
	impl.mu.Lock()
	if len(impl.prompts) != 1 || impl.prompts[0] != "secretary/hello shogun" {
		t.Errorf("prompts = %v", impl.prompts)
	}
	impl.mu.Unlock()

	// RestoreMinisterSession.
	msgs := []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}, {Role: schemas.ChatMessageRoleAssistant}}
	if err := client.RestoreMinisterSession("ruling", msgs); err != nil {
		t.Fatalf("RestoreMinisterSession: %v", err)
	}
	impl.mu.Lock()
	if len(impl.restored) != 1 || impl.restored[0] != "ruling/2" {
		t.Errorf("restored = %v", impl.restored)
	}
	impl.mu.Unlock()

	// TakeSnapshot.
	snap := client.TakeSnapshot()
	if len(snap.Rituals) != 1 || snap.Rituals[0].RitualName != "swift-strike" || snap.Rituals[0].CurrentStep != 1 {
		t.Errorf("snapshot rituals = %+v", snap.Rituals)
	}

	// CancelTab.
	client.CancelTab("ruling")
	impl.mu.Lock()
	// Previous cancels[0] was "req-1" from CancelZhengming; CancelTab appends "tab:ruling".
	sawCancelTab := false
	for _, c := range impl.cancels {
		if c == "tab:ruling" {
			sawCancelTab = true
			break
		}
	}
	if !sawCancelTab {
		t.Errorf("CancelTab not recorded: %v", impl.cancels)
	}
	impl.mu.Unlock()

	// PauseRitual / ResumeRitual round-trip.
	if !client.PauseRitual("e633") {
		t.Error("PauseRitual(e633) = false")
	}
	impl.mu.Lock()
	if len(impl.pauses) != 1 || impl.pauses[0] != "e633" {
		t.Errorf("pauses = %v", impl.pauses)
	}
	impl.mu.Unlock()

	if !client.ResumeRitual("e633") {
		t.Error("ResumeRitual(e633) = false")
	}
	impl.mu.Lock()
	if len(impl.resumes) != 1 || impl.resumes[0] != "e633" {
		t.Errorf("resumes = %v", impl.resumes)
	}
	impl.mu.Unlock()

	// ListModels round-trip: the response must cross the wire intact,
	// including the pointer fields and ExtraFields that bifrost uses in
	// practice (the edict flagged codec-carryability as the risk).
	resp, err := client.ListModels("openai")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "openai-model" {
		t.Errorf("ListModels response = %+v", resp)
	}
	if resp.Data[0].Name == nil || *resp.Data[0].Name != "openai display" {
		t.Errorf("ListModels Name = %v", resp.Data[0].Name)
	}
	if resp.Data[0].ContextLength == nil || *resp.Data[0].ContextLength != 128000 {
		t.Errorf("ListModels ContextLength = %v", resp.Data[0].ContextLength)
	}
	if resp.Data[0].Pricing == nil || resp.Data[0].Pricing.Prompt == nil || *resp.Data[0].Pricing.Prompt != "0.005" {
		t.Errorf("ListModels Pricing = %+v", resp.Data[0].Pricing)
	}
	if resp.NextPageToken != "tok" {
		t.Errorf("ListModels NextPageToken = %q", resp.NextPageToken)
	}
	if resp.ExtraFields.Provider != schemas.OpenAI || resp.ExtraFields.RequestType != schemas.ListModelsRequest {
		t.Errorf("ListModels ExtraFields = %+v", resp.ExtraFields)
	}
	impl.mu.Lock()
	if len(impl.listed) != 1 || impl.listed[0] != "openai" {
		t.Errorf("listed providers = %v", impl.listed)
	}
	impl.mu.Unlock()
}

// TestLoopbackCourtListModelsReachesWire asserts LoopbackCourt.ListModels
// goes through the RPC surface rather than the in-process Local court.
func TestLoopbackCourtListModelsReachesWire(t *testing.T) {
	impl := newFakeCourt()

	pa, pb := net.Pipe()
	server := New(pa, Options{})
	clientConn := New(pb, Options{})
	RegisterCourtHandlers(server, impl)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = server.Serve() }()
	go func() { defer wg.Done(); _ = clientConn.Serve() }()
	defer func() {
		_ = clientConn.Close()
		_ = server.Close()
		wg.Wait()
	}()

	var localCalls []string
	local := &listModelsOnlyCourt{
		fakeCourt: newFakeCourt(),
		onList:    func(p string) { localCalls = append(localCalls, p) },
	}
	lb := NewLoopbackCourt(clientConn, local)

	resp, err := lb.ListModels("anthropic")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "anthropic-model" {
		t.Errorf("response = %+v", resp)
	}

	impl.mu.Lock()
	serverCalls := len(impl.listed)
	impl.mu.Unlock()
	if serverCalls != 1 {
		t.Errorf("server-side ListModels calls = %d, want 1", serverCalls)
	}
	if len(localCalls) != 0 {
		t.Errorf("Local.ListModels was called %v; want wire path", localCalls)
	}
}

// listModelsOnlyCourt records calls to Local.ListModels so the test can
// detect an accidental in-process fallback.
type listModelsOnlyCourt struct {
	*fakeCourt
	onList func(string)
}

func (c *listModelsOnlyCourt) ListModels(provider string) (*schemas.BifrostListModelsResponse, error) {
	c.onList(provider)
	return c.fakeCourt.ListModels(provider)
}

// TestLoopbackCourtListModelsLocalFallback pins the retained fallback:
// a LoopbackCourt with no CourtClient (nil embed) must serve through
// Local, and with neither must error rather than panic.
func TestLoopbackCourtListModelsLocalFallback(t *testing.T) {
	var localCalls []string
	local := &listModelsOnlyCourt{
		fakeCourt: newFakeCourt(),
		onList:    func(p string) { localCalls = append(localCalls, p) },
	}
	lb := &LoopbackCourt{Local: local} // no CourtClient

	resp, err := lb.ListModels("ollama")
	if err != nil {
		t.Fatalf("ListModels (local fallback): %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "ollama-model" {
		t.Errorf("response = %+v", resp)
	}
	if len(localCalls) != 1 || localCalls[0] != "ollama" {
		t.Errorf("localCalls = %v", localCalls)
	}

	empty := &LoopbackCourt{}
	if _, err := empty.ListModels("openai"); err == nil {
		t.Error("ListModels with no client and no local: want error, got nil")
	}
}

// TestCourtClientListModelsServerErrorPropagates asserts a server-side
// failure reaches the caller intact, rather than being swallowed by the
// wire layer.
func TestCourtClientListModelsServerErrorPropagates(t *testing.T) {
	impl := &erroringListModelsCourt{fakeCourt: newFakeCourt()}

	pa, pb := net.Pipe()
	server := New(pa, Options{})
	clientConn := New(pb, Options{})
	RegisterCourtHandlers(server, impl)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = server.Serve() }()
	go func() { defer wg.Done(); _ = clientConn.Serve() }()
	defer func() {
		_ = clientConn.Close()
		_ = server.Close()
		wg.Wait()
	}()

	client := NewCourtClient(clientConn)
	if _, err := client.ListModels("openai"); err == nil {
		t.Fatal("ListModels: want server error, got nil")
	} else if !strings.Contains(err.Error(), "LLM client not initialized") {
		t.Errorf("ListModels error = %v, want wrapped server error", err)
	}
}

type erroringListModelsCourt struct {
	*fakeCourt
}

func (c *erroringListModelsCourt) ListModels(string) (*schemas.BifrostListModelsResponse, error) {
	return nil, errors.New("LLM client not initialized")
}

// TestMethodListModelsRegistered guards against a refactor silently
// dropping the method from the RPC surface.
func TestMethodListModelsRegistered(t *testing.T) {
	impl := newFakeCourt()

	pa, pb := net.Pipe()
	server := New(pa, Options{})
	clientConn := New(pb, Options{})
	RegisterCourtHandlers(server, impl)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = server.Serve() }()
	go func() { defer wg.Done(); _ = clientConn.Serve() }()
	defer func() {
		_ = clientConn.Close()
		_ = server.Close()
		wg.Wait()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := clientConn.Call(ctx, MethodListModels, ListModelsParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("MethodListModels not served: %v", err)
	}
	var r ListModelsResult
	if err := wire.Decode(raw, &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Response == nil || len(r.Response.Data) != 1 {
		t.Errorf("result = %+v", r)
	}
}

// TestApproximateContextSizeRoundTrip verifies the registry helper crosses the
// wire intact, mirroring the MethodListModels test.
func TestApproximateContextSizeRoundTrip(t *testing.T) {
	impl := newFakeCourt()

	pa, pb := net.Pipe()
	server := New(pa, Options{})
	clientConn := New(pb, Options{})
	RegisterCourtHandlers(server, impl)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = server.Serve() }()
	go func() { defer wg.Done(); _ = clientConn.Serve() }()
	defer func() {
		_ = clientConn.Close()
		_ = server.Close()
		wg.Wait()
	}()

	client := NewCourtClient(clientConn)
	if got := client.ApproximateContextSize("openai", "gpt-4o"); got != 128000 {
		t.Errorf("ApproximateContextSize = %d, want 128000", got)
	}
	if got := client.ApproximateContextSize("openai", "unknown"); got != 0 {
		t.Errorf("ApproximateContextSize(unknown) = %d, want 0", got)
	}
}

// TestMethodApproximateContextSizeRegistered guards against a refactor silently
// dropping the method from the RPC surface.
func TestMethodApproximateContextSizeRegistered(t *testing.T) {
	impl := newFakeCourt()

	pa, pb := net.Pipe()
	server := New(pa, Options{})
	clientConn := New(pb, Options{})
	RegisterCourtHandlers(server, impl)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = server.Serve() }()
	go func() { defer wg.Done(); _ = clientConn.Serve() }()
	defer func() {
		_ = clientConn.Close()
		_ = server.Close()
		wg.Wait()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := clientConn.Call(ctx, MethodApproximateContextSize, ApproximateContextSizeParams{Provider: "openai", Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("MethodApproximateContextSize not served: %v", err)
	}
	var r ApproximateContextSizeResult
	if err := wire.Decode(raw, &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Size != 128000 {
		t.Errorf("result = %+v", r)
	}
}

// TestLoopbackCourtApproximateContextSizeReachesWire asserts LoopbackCourt's
// registry lookup goes through the RPC surface rather than the in-process
// Local court, so the TUI never links the registry directly.
func TestLoopbackCourtApproximateContextSizeReachesWire(t *testing.T) {
	impl := newFakeCourt()

	pa, pb := net.Pipe()
	server := New(pa, Options{})
	clientConn := New(pb, Options{})
	RegisterCourtHandlers(server, impl)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = server.Serve() }()
	go func() { defer wg.Done(); _ = clientConn.Serve() }()
	defer func() {
		_ = clientConn.Close()
		_ = server.Close()
		wg.Wait()
	}()

	// A local court that would return a different value: reaching it would
	// prove the wire path was bypassed.
	local := &approxTrackingCourt{fakeCourt: newFakeCourt(), size: 4242}
	lb := NewLoopbackCourt(clientConn, local)

	if got := lb.ApproximateContextSize("openai", "gpt-4o"); got != 128000 {
		t.Errorf("ApproximateContextSize = %d, want 128000 (wire path)", got)
	}
	if len(local.calls) != 0 {
		t.Errorf("Local.ApproximateContextSize was called %v; want wire path", local.calls)
	}
}

// approxTrackingCourt records calls to Local.ApproximateContextSize so a test
// can detect an accidental in-process fallback.
type approxTrackingCourt struct {
	*fakeCourt
	size  int
	calls []string
}

func (c *approxTrackingCourt) ApproximateContextSize(provider, model string) int {
	c.calls = append(c.calls, provider+":"+model)
	return c.size
}

// TestLoopbackCourtApproximateContextSizeLocalFallback pins the retained
// fallback: a LoopbackCourt with no CourtClient (nil embed) must serve through
// Local, and with neither must return 0 rather than panic.
func TestLoopbackCourtApproximateContextSizeLocalFallback(t *testing.T) {
	local := &approxTrackingCourt{fakeCourt: newFakeCourt(), size: 4242}
	lb := &LoopbackCourt{Local: local} // no CourtClient

	if got := lb.ApproximateContextSize("openai", "gpt-4o"); got != 4242 {
		t.Errorf("ApproximateContextSize (local fallback) = %d, want 4242", got)
	}
	if len(local.calls) != 1 || local.calls[0] != "openai:gpt-4o" {
		t.Errorf("local.calls = %v", local.calls)
	}

	empty := &LoopbackCourt{}
	if got := empty.ApproximateContextSize("openai", "gpt-4o"); got != 0 {
		t.Errorf("ApproximateContextSize with no client and no local = %d, want 0", got)
	}
}
