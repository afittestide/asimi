package court

import (
	"context"
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
