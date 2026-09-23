package rpc

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
)

// TestNotificationPipeline sends a handful of notifications from the
// "server" side of a net.Pipe, expects the client to decode and deliver
// them on its Subscribe channel in order.
func TestNotificationPipeline(t *testing.T) {
	pa, pb := net.Pipe()
	server := New(pa, Options{})
	clientConn := New(pb, Options{})

	serverEvents := make(chan any, 16)
	clientOut := make(chan any, 16)
	SubscribeAll(clientConn, clientOut)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = server.Serve() }()
	go func() { defer wg.Done(); _ = clientConn.Serve() }()

	pumpCtx, cancelPump := context.WithCancel(context.Background())
	go PumpNotifications(pumpCtx, server, serverEvents)

	defer func() {
		cancelPump()
		close(serverEvents)
		_ = clientConn.Close()
		_ = server.Close()
		wg.Wait()
	}()

	// Feed a mix of notification types.
	want := []any{
		court.StreamStartMsg{ChannelID: "ruling", EdictID: 9},
		court.StreamChunkMsg{ChannelID: "ruling", Text: "hello "},
		court.StreamChunkMsg{ChannelID: "ruling", Text: "world"},
		court.StreamChunkMsg{ChannelID: "chancellor", Text: "thinking"},
		court.StreamCompleteMsg{ChannelID: "ruling"},
		court.StreamErrorMsg{ChannelID: "forge", Err: errors.New("boom")},
		court.EventsDrainedMsg{Events: []court.DrainedEvent{
			{EventType: storage.EventEdictCreated, EdictKey: storage.EdictKey{ID: 9}},
		}},
		court.MinisterInvokingMsg{ChannelID: "ruling", MinisterID: "forge", EdictKey: storage.EdictKey{ID: 9}, Task: "write code"},
		court.MinisterCompletedMsg{ChannelID: "ruling", MinisterID: "forge", EdictKey: storage.EdictKey{ID: 9}, Output: "done", Sealed: true},
		court.EventNotificationMsg{ChannelID: "ruling", EventType: storage.EventSealGranted, EdictKey: storage.EdictKey{ID: 9}, Message: "sealed"},
		court.ZhengmingPendingMsg{RequestID: "z-1", MinisterID: "chancellor", ChannelID: "e42", EdictKey: storage.EdictKey{ID: 9}},
		court.ZhengmingAnsweredMsg{RequestID: "z-1", Answer: "yes"},
		court.RitualStepMsg{ChannelID: "ruling", RitualName: "swift-strike", StepIndex: 1, TotalSteps: 3, Status: "ok"},
		runners.ContainerLaunchedMsg{Message: "up", ContainerID: "abc123"},
		court.StreamDoneMsg{ChannelID: "ruling"},
		runners.ToolCallScheduledMsg{ChannelID: "ruling", CallID: "tc1", ToolName: "read_file", Input: "test.go", Status: "scheduled", Formatted: "read_file: test.go"},
		runners.ToolCallExecutingMsg{ChannelID: "ruling", CallID: "tc1", ToolName: "read_file", Input: "test.go", Status: "executing", Formatted: "read_file: test.go"},
		runners.ToolCallSuccessMsg{ChannelID: "ruling", CallID: "tc1", ToolName: "read_file", Input: "test.go", Status: "success", Result: "ok", Formatted: "read_file: ok"},
		runners.ToolCallErrorMsg{ChannelID: "ruling", CallID: "tc2", ToolName: "bad_tool", Input: "x", Status: "error", Error: "boom", Formatted: "bad_tool: boom"},
		runners.ToolCallAbortedMsg{ChannelID: "ruling", CallID: "tc3", ToolName: "slow_tool", Input: "y", Status: "aborted", Reason: "restart", Formatted: "slow_tool: restart"},
	}

	for _, m := range want {
		serverEvents <- m
	}

	timeout := time.After(3 * time.Second)
	got := make([]any, 0, len(want))
	for len(got) < len(want) {
		select {
		case v := <-clientOut:
			got = append(got, v)
		case <-timeout:
			t.Fatalf("only received %d/%d notifications: %+v", len(got), len(want), got)
		}
	}

	for i := range want {
		if !matchesNotification(want[i], got[i]) {
			t.Errorf("msg[%d]: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// matchesNotification compares notification values with the StreamErrorMsg
// and MinisterCompletedMsg custom-marshal rules baked in: those types
// reconstruct their Error field via errors.New so only Error().Error()
// is compared.
func matchesNotification(want, got any) bool {
	switch w := want.(type) {
	case court.StreamErrorMsg:
		g, ok := got.(court.StreamErrorMsg)
		if !ok {
			return false
		}
		if g.ChannelID != w.ChannelID {
			return false
		}
		if (g.Err == nil) != (w.Err == nil) {
			return false
		}
		if g.Err != nil && g.Err.Error() != w.Err.Error() {
			return false
		}
		return true
	case court.MinisterCompletedMsg:
		g, ok := got.(court.MinisterCompletedMsg)
		if !ok {
			return false
		}
		if g.ChannelID != w.ChannelID || g.MinisterID != w.MinisterID || g.Output != w.Output || g.Sealed != w.Sealed {
			return false
		}
		if (g.Error == nil) != (w.Error == nil) {
			return false
		}
		if g.Error != nil && g.Error.Error() != w.Error.Error() {
			return false
		}
		return true
	case court.EventsDrainedMsg:
		g, ok := got.(court.EventsDrainedMsg)
		if !ok || len(g.Events) != len(w.Events) {
			return false
		}
		for i := range w.Events {
			if g.Events[i].EventType != w.Events[i].EventType || g.Events[i].EdictKey != w.Events[i].EdictKey {
				return false
			}
		}
		return true
	}
	// Fallback: shallow equality via fmt-style comparison is too lossy for
	// pointer-bearing values, but every other notification in this test is
	// comparable by ==.
	return comparableEqual(want, got)
}

func comparableEqual(a, b any) bool {
	switch at := a.(type) {
	case court.StreamStartMsg:
		bt, ok := b.(court.StreamStartMsg)
		return ok && at == bt
	case court.StreamChunkMsg:
		bt, ok := b.(court.StreamChunkMsg)
		return ok && at == bt
	case court.StreamCompleteMsg:
		bt, ok := b.(court.StreamCompleteMsg)
		return ok && at == bt
	case court.StreamDoneMsg:
		bt, ok := b.(court.StreamDoneMsg)
		return ok && at == bt
	case court.StreamInterruptedMsg:
		bt, ok := b.(court.StreamInterruptedMsg)
		return ok && at == bt
	case court.StreamMaxTokensReachedMsg:
		bt, ok := b.(court.StreamMaxTokensReachedMsg)
		return ok && at == bt
	case court.MinisterInvokingMsg:
		bt, ok := b.(court.MinisterInvokingMsg)
		return ok && at == bt
	case court.EventNotificationMsg:
		bt, ok := b.(court.EventNotificationMsg)
		return ok && at.ChannelID == bt.ChannelID && at.EventType == bt.EventType && at.EdictKey == bt.EdictKey && at.Message == bt.Message
	case court.ZhengmingPendingMsg:
		bt, ok := b.(court.ZhengmingPendingMsg)
		return ok && at.RequestID == bt.RequestID && at.MinisterID == bt.MinisterID && at.ChannelID == bt.ChannelID && at.EdictKey == bt.EdictKey
	case court.ZhengmingAnsweredMsg:
		bt, ok := b.(court.ZhengmingAnsweredMsg)
		return ok && at == bt
	case court.RitualStepMsg:
		bt, ok := b.(court.RitualStepMsg)
		return ok && at == bt
	case runners.ContainerLaunchedMsg:
		bt, ok := b.(runners.ContainerLaunchedMsg)
		return ok && at == bt
	case runners.ToolCallScheduledMsg:
		bt, ok := b.(runners.ToolCallScheduledMsg)
		return ok && at == bt
	case runners.ToolCallExecutingMsg:
		bt, ok := b.(runners.ToolCallExecutingMsg)
		return ok && at == bt
	case runners.ToolCallSuccessMsg:
		bt, ok := b.(runners.ToolCallSuccessMsg)
		return ok && at == bt
	case runners.ToolCallErrorMsg:
		bt, ok := b.(runners.ToolCallErrorMsg)
		return ok && at == bt
	case runners.ToolCallAbortedMsg:
		bt, ok := b.(runners.ToolCallAbortedMsg)
		return ok && at == bt
	}
	return false
}
