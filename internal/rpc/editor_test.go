package rpc

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/court/tools"
)

var errTestEditor = errors.New("exec: vi: not found")

func TestEditorRequestRoundTrip(t *testing.T) {
	pa, pb := net.Pipe()
	daemon := New(pa, Options{})
	tui := New(pb, Options{})

	fake := &fakeProgram{ch: make(chan any, 1)}
	RegisterEditorHandler(tui, fake)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = daemon.Serve() }()
	go func() { defer wg.Done(); _ = tui.Serve() }()
	defer func() {
		_ = daemon.Close()
		_ = tui.Close()
		wg.Wait()
	}()

	// Simulate the TUI: pretend the user edited the content.
	go func() {
		msg := <-fake.ch
		req, ok := msg.(tools.EditorRequest)
		if !ok {
			t.Errorf("unexpected msg type: %T", msg)
			return
		}
		req.ResultChan <- tools.EditorResult{Content: req.Content + " (edited)", Outcome: tools.EditorModified}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := RequestEditor(ctx, daemon, "draft edict body", "edict.md")
	if err != nil {
		t.Fatalf("RequestEditor: %v", err)
	}
	if res.Outcome != tools.EditorModified {
		t.Fatalf("Outcome = %q; wanted %q", res.Outcome, tools.EditorModified)
	}
	if res.Content != "draft edict body (edited)" {
		t.Fatalf("unexpected content roundtrip: %q", res.Content)
	}
}

// TestEditorRequestOutcomeSurvivesRPC is the regression guard for the daemon
// bridge: a non-"saved" outcome must cross the wire intact, or a
// daemon-hosted TUI reintroduces the phantom-rejection ambiguity.
func TestEditorRequestOutcomeSurvivesRPC(t *testing.T) {
	cases := []struct {
		name    string
		outcome tools.EditorOutcome
		content string
	}{
		{"unchanged", tools.EditorUnchanged, ""},
		{"discarded", tools.EditorDiscarded, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pa, pb := net.Pipe()
			daemon := New(pa, Options{})
			tui := New(pb, Options{})

			fake := &fakeProgram{ch: make(chan any, 1)}
			RegisterEditorHandler(tui, fake)

			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); _ = daemon.Serve() }()
			go func() { defer wg.Done(); _ = tui.Serve() }()
			defer func() {
				_ = daemon.Close()
				_ = tui.Close()
				wg.Wait()
			}()

			go func() {
				msg := <-fake.ch
				req := msg.(tools.EditorRequest)
				req.ResultChan <- tools.EditorResult{Content: tc.content, Outcome: tc.outcome}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			res, err := RequestEditor(ctx, daemon, "draft", "")
			if err != nil {
				t.Fatalf("RequestEditor: %v", err)
			}
			if res.Outcome != tc.outcome {
				t.Fatalf("Outcome = %q; wanted %q", res.Outcome, tc.outcome)
			}
		})
	}
}

func TestEditorRequestFailureRoundTrip(t *testing.T) {
	pa, pb := net.Pipe()
	daemon := New(pa, Options{})
	tui := New(pb, Options{})

	fake := &fakeProgram{ch: make(chan any, 1)}
	RegisterEditorHandler(tui, fake)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = daemon.Serve() }()
	go func() { defer wg.Done(); _ = tui.Serve() }()
	defer func() {
		_ = daemon.Close()
		_ = tui.Close()
		wg.Wait()
	}()

	go func() {
		msg := <-fake.ch
		req := msg.(tools.EditorRequest)
		req.ResultChan <- tools.EditorResult{Outcome: tools.EditorFailed, Err: errTestEditor}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := RequestEditor(ctx, daemon, "draft", "")
	if err != nil {
		t.Fatalf("RequestEditor: %v", err)
	}
	if res.Err == nil {
		t.Fatal("expected the editor failure to survive the RPC hop as an error")
	}
	if res.Outcome != tools.EditorFailed {
		t.Fatalf("Outcome = %q; wanted %q", res.Outcome, tools.EditorFailed)
	}
}

func TestPumpCourtEventsInterceptsEditor(t *testing.T) {
	pa, pb := net.Pipe()
	daemon := New(pa, Options{})
	tui := New(pb, Options{})

	fake := &fakeProgram{ch: make(chan any, 1)}
	RegisterEditorHandler(tui, fake)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = daemon.Serve() }()
	go func() { defer wg.Done(); _ = tui.Serve() }()
	defer func() {
		_ = daemon.Close()
		_ = tui.Close()
		wg.Wait()
	}()

	events := make(chan any, 4)
	pumpCtx, cancelPump := context.WithCancel(context.Background())
	go PumpCourtEvents(pumpCtx, daemon, events)
	defer cancelPump()

	go func() {
		msg := <-fake.ch
		req := msg.(tools.EditorRequest)
		req.ResultChan <- tools.EditorResult{Content: "modified body", Outcome: tools.EditorModified}
	}()

	respCh := make(chan tools.EditorResult, 1)
	events <- tools.EditorRequest{Content: "original body", Filename: "x.md", ResultChan: respCh}

	select {
	case got := <-respCh:
		if got.Outcome != tools.EditorModified || got.Content != "modified body" {
			t.Fatalf("unexpected result: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("response never delivered")
	}
}
