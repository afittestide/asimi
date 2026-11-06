package court

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/storage"
)

func TestCancelTabCancelsRegisteredContext(t *testing.T) {
	s := &Court{}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()

	ctx := s.CancellableStreamCtx("ruling")
	select {
	case <-ctx.Done():
		t.Fatal("ctx cancelled before CancelTab")
	default:
	}

	s.CancelTab("ruling")
	select {
	case <-ctx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("ctx not cancelled after CancelTab")
	}
}

func TestCancelTabUnknownChannelIsNoOp(t *testing.T) {
	s := &Court{}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()
	// Should not panic.
	s.CancelTab("unknown")
}

func TestCancellableStreamCtxReplacesPriorCancel(t *testing.T) {
	s := &Court{}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()

	first := s.CancellableStreamCtx("ruling")
	// Register a second for same channel: should cancel the first.
	second := s.CancellableStreamCtx("ruling")

	select {
	case <-first.Done():
		// expected — first was replaced
	case <-time.After(time.Second):
		t.Fatal("first ctx not cancelled when replaced")
	}
	select {
	case <-second.Done():
		t.Fatal("second ctx cancelled prematurely")
	default:
	}

	s.CancelTab("ruling")
	select {
	case <-second.Done():
	case <-time.After(time.Second):
		t.Fatal("second ctx not cancelled")
	}
}

func TestCancellableStreamCtxChildOfRootCtx(t *testing.T) {
	// When the court's root ctx cancels, stream ctxes cancel too.
	s := &Court{}
	s.ctx, s.cancel = context.WithCancel(context.Background())

	ctx := s.CancellableStreamCtx("ruling")
	s.cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream ctx not cancelled when root cancelled")
	}
}

// TestCancelEdictCancelsEdictChannel verifies that CancelEdict cancels the
// per-edict streaming context (e.g. "e644"), not the chancellor channel.
func TestCancelEdictCancelsEdictChannel(t *testing.T) {
	db := setupEventTestDB(t)
	err := db.AutoMigrate(&storage.Edict{})
	if err != nil {
		t.Fatalf("Failed to migrate edicts: %v", err)
	}

	// Insert a test edict
	edict := storage.Edict{ID: 644, Username: "testuser", Project: "testproject"}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("Failed to create edict: %v", err)
	}

	s := &Court{
		db:     db,
		config: &config.CourtConfig{Project: "testproject"}, username: "testuser",
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()

	// Register streaming contexts for both the edict channel and chancellor
	edictCtx := s.CancellableStreamCtx("e644")
	chancellorCtx := s.CancellableStreamCtx("secretary")

	// Both should be active before CancelEdict
	select {
	case <-edictCtx.Done():
		t.Fatal("edict ctx cancelled before CancelEdict")
	default:
	}
	select {
	case <-chancellorCtx.Done():
		t.Fatal("chancellor ctx cancelled before CancelEdict")
	default:
	}

	// Cancel the edict
	if err := s.CancelEdict(644); err != nil {
		t.Fatalf("CancelEdict failed: %v", err)
	}

	// The edict's streaming context should be cancelled
	select {
	case <-edictCtx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("edict ctx not cancelled after CancelEdict")
	}

	// The chancellor ctx should NOT be cancelled
	select {
	case <-chancellorCtx.Done():
		t.Fatal("chancellor ctx should not be cancelled when cancelling a different edict")
	default:
	}
}

// TestCancelTabCauseAttributesCancellation locks the e1024 contract at the
// Court boundary: the ritual guard's cancelRitual hook (wired to CancelTabCause)
// must attach the *supplied* cause to the cancelled context, so the unwinding
// ritual reports a flatline reap or an edict-state abort honestly instead of
// falling back to "cancelled by ruler". CancelTab, the ruler path, must attach
// errRulerInterrupt; a nil cause must delegate to that same ruler path.
func TestCancelTabCauseAttributesCancellation(t *testing.T) {
	newCourt := func(t *testing.T) *Court {
		t.Helper()
		s := &Court{}
		s.ctx, s.cancel = context.WithCancel(context.Background())
		t.Cleanup(s.cancel)
		return s
	}

	t.Run("CancelTab attaches the ruler-interrupt cause", func(t *testing.T) {
		s := newCourt(t)
		ctx := s.CancellableStreamCtx("ruling")
		s.CancelTab("ruling")
		<-ctx.Done()
		if got := context.Cause(ctx); !errors.Is(got, errRulerInterrupt) {
			t.Fatalf("CancelTab must cancel with errRulerInterrupt, got %v", got)
		}
	})

	t.Run("CancelTabCause attaches the supplied court-abort cause", func(t *testing.T) {
		s := newCourt(t)
		ctx := s.CancellableStreamCtx("e1024")
		s.CancelTabCause("e1024", errFlatlineReaped)
		<-ctx.Done()
		got := context.Cause(ctx)
		if !errors.Is(got, errFlatlineReaped) {
			t.Fatalf("CancelTabCause must attach the supplied cause, got %v", got)
		}
		if isRulerInterrupt(ctx.Err(), ctx) {
			t.Fatal("a court abort must never be classified as a ruler interrupt")
		}
		if !isCourtAbortCause(got) {
			t.Fatal("a flatline reap must be recognized as a court abort cause")
		}
	})

	t.Run("a nil cause delegates to the ruler path", func(t *testing.T) {
		s := newCourt(t)
		ctx := s.CancellableStreamCtx("e1024")
		s.CancelTabCause("e1024", nil)
		<-ctx.Done()
		if got := context.Cause(ctx); !errors.Is(got, errRulerInterrupt) {
			t.Fatalf("nil cause must delegate to CancelTab (errRulerInterrupt), got %v", got)
		}
	})

	t.Run("a cause-less cancel is never attributed to the ruler", func(t *testing.T) {
		s := newCourt(t)
		ctx := s.CancellableStreamCtx("e1024")
		s.cancelTab("e1024", nil)
		<-ctx.Done()
		if isRulerInterrupt(ctx.Err(), ctx) {
			t.Fatal("a bare context.Canceled must not read as a ruler interrupt")
		}
		if got := describeFailure(ctx.Err(), ctx); got != "cancelled (cause unknown): context canceled" {
			t.Fatalf("a cause-less cancel must be recorded as unknown, got %q", got)
		}
	})
}
