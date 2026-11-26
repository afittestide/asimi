package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gologger "gorm.io/gorm/logger"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
	"github.com/alecthomas/kong"
)

// TestHeadlessSink_StreamChunkWritesText verifies that StreamChunkMsg text
// is written to stdout (captured via the sink's output).
func TestHeadlessSink_StreamChunkWritesText(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.StreamChunkMsg{Text: "hello world"})
	// No panic, no done signal — just text output
	select {
	case code := <-sink.done:
		t.Fatalf("should not signal done on StreamChunkMsg, got code %d", code)
	default:
	}
}

// TestHeadlessSink_StreamDoneNoRitual_FinishesZero verifies that StreamDoneMsg
// signals exit 0 when no ritual has been enacted (plain chat).
func TestHeadlessSink_StreamDoneNoRitual_FinishesZero(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.StreamDoneMsg{ChannelID: "secretary"})

	select {
	case code := <-sink.done:
		assert.Equal(t, 0, code)
	default:
		t.Fatal("expected done signal after StreamDoneMsg with no ritual")
	}
}

// TestHeadlessSink_StreamDoneWithRitual_DoesNotFinish verifies that
// StreamDoneMsg does NOT signal completion when a ritual is running.
func TestHeadlessSink_StreamDoneWithRitual_DoesNotFinish(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.enactedSet[42] = true
	sink.handle(court.StreamDoneMsg{ChannelID: "secretary"})

	select {
	case code := <-sink.done:
		t.Fatalf("should not signal done when ritual is running, got code %d", code)
	default:
		// correct — ritual completion will signal later
	}
}

// TestHeadlessSink_StreamError_FinishesOne verifies that StreamErrorMsg
// signals exit 1.
func TestHeadlessSink_StreamError_FinishesOne(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.StreamErrorMsg{Err: assert.AnError})

	select {
	case code := <-sink.done:
		assert.Equal(t, 1, code)
	default:
		t.Fatal("expected done signal after StreamErrorMsg")
	}
}

// TestHeadlessSink_RitualStepCompleted_FinishesZero verifies that
// RitualStepMsg with status "ritual_completed" signals exit 0.
func TestHeadlessSink_RitualStepCompleted_FinishesZero(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.RitualStepMsg{
		Status:     "ritual_completed",
		RitualName: "swift-strike",
		EdictID:    1,
		Message:    "5s",
	})

	select {
	case code := <-sink.done:
		assert.Equal(t, 0, code)
	default:
		t.Fatal("expected done signal after ritual_completed")
	}
}

// TestHeadlessSink_RitualStepFailed_FinishesOne verifies that
// RitualStepMsg with status "ritual_failed" signals exit 1.
func TestHeadlessSink_RitualStepFailed_FinishesOne(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.RitualStepMsg{
		Status:     "ritual_failed",
		RitualName: "swift-strike",
		EdictID:    1,
		Message:    "something broke",
	})

	select {
	case code := <-sink.done:
		assert.Equal(t, 1, code)
	default:
		t.Fatal("expected done signal after ritual_failed")
	}
}

// TestHeadlessSink_EventRitualCompleted_FinishesZero verifies that
// EventNotificationMsg with EventRitualCompleted signals exit 0.
func TestHeadlessSink_EventRitualCompleted_FinishesZero(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventRitualCompleted,
		EdictKey:  storage.EdictKey{ID: 7},
	})

	select {
	case code := <-sink.done:
		assert.Equal(t, 0, code)
	default:
		t.Fatal("expected done signal after EventRitualCompleted")
	}
}

// TestHeadlessSink_EventRitualFailed_FinishesOne verifies that
// EventNotificationMsg with EventRitualFailed signals exit 1.
func TestHeadlessSink_EventRitualFailed_FinishesOne(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventRitualFailed,
		EdictKey:  storage.EdictKey{ID: 7},
		Payload:   map[string]interface{}{"error": "boom"},
	})

	select {
	case code := <-sink.done:
		assert.Equal(t, 1, code)
	default:
		t.Fatal("expected done signal after EventRitualFailed")
	}
}

// TestHeadlessSink_FinishIsIdempotent verifies that calling finish
// multiple times only delivers the first code.
func TestHeadlessSink_FinishIsIdempotent(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.finish(0)
	sink.finish(1) // should be dropped

	code := <-sink.done
	assert.Equal(t, 0, code)

	select {
	case code := <-sink.done:
		t.Fatalf("should not have a second value, got %d", code)
	default:
	}
}

// TestHeadlessSink_ToolCallSuccess_PrintsResult verifies that
// ToolCallSuccessMsg with a result is handled without panic.
func TestHeadlessSink_ToolCallSuccess_PrintsResult(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(runners.ToolCallSuccessMsg{
		ToolName: "read_file",
		Result:   "file contents here",
	})
	// No done signal
	select {
	case <-sink.done:
		t.Fatal("should not signal done on ToolCallSuccessMsg")
	default:
	}
}

// TestHeadlessSink_ToolCallSuccess_MarkdownEnvelopePrintsContent verifies the
// edict-975 headless path: a markdown result envelope is printed as plain
// content rather than raw envelope JSON.
func TestHeadlessSink_ToolCallSuccess_MarkdownEnvelopePrintsContent(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	sink := newHeadlessSink(nil)
	sink.handle(runners.ToolCallSuccessMsg{
		ToolName: "edict",
		Result:   `{"format":"markdown","content":"# Title\n\nbody"}`,
	})
	require.NoError(t, w.Close())
	os.Stdout = old

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), "# Title", "markdown content must be printed")
	assert.Contains(t, string(out), "body", "markdown content must be printed")
	assert.NotContains(t, string(out), `"format"`,
		"the raw envelope JSON must not be printed verbatim")
}

// TestHeadlessSink_ToolCallError_PrintsError verifies that
// ToolCallErrorMsg is handled without panic.
func TestHeadlessSink_ToolCallError_PrintsError(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(runners.ToolCallErrorMsg{
		ToolName: "grep",
		Error:    "pattern not found",
	})
	// No done signal
	select {
	case <-sink.done:
		t.Fatal("should not signal done on ToolCallErrorMsg")
	default:
	}
}

// captureWarnHandler is a slog handler that records WARN-level records so
// tests can assert that expected messages do NOT warn.
type captureWarnHandler struct {
	mu      sync.Mutex
	warned  []string
	records []slog.Record
}

func (h *captureWarnHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}
func (h *captureWarnHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	if r.Level >= slog.LevelWarn {
		h.warned = append(h.warned, r.Message)
	}
	return nil
}
func (h *captureWarnHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *captureWarnHandler) WithGroup(name string) slog.Handler       { return h }

// newCaptureSink builds a headless sink whose slog warnings are captured.
func newCaptureSink(t *testing.T) (*headlessSink, *captureWarnHandler) {
	t.Helper()
	h := &captureWarnHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return newHeadlessSink(nil), h
}

// TestHeadlessSink_StreamAndToolProgress_NoWarnings verifies that the four
// known progress event types (StreamStartMsg, StreamCompleteMsg,
// ToolCallScheduledMsg, ToolCallExecutingMsg) are consumed intentionally —
// none of them may hit the default WARN branch, which previously flooded the
// log during swift-strike rituals.
func TestHeadlessSink_StreamAndToolProgress_NoWarnings(t *testing.T) {
	sink, h := newCaptureSink(t)

	sink.handle(court.StreamStartMsg{ChannelID: "secretary", EdictID: 3})
	require.True(t, sink.streaming, "StreamStartMsg should set streaming")
	require.Equal(t, uint(3), sink.streamingEdictID, "StreamStartMsg should capture the edict ID")

	sink.handle(runners.ToolCallScheduledMsg{ToolName: "read_file"})
	sink.handle(runners.ToolCallExecutingMsg{ToolName: "read_file"})

	// Mid-stream nothing signals done.
	select {
	case code := <-sink.done:
		t.Fatalf("should not signal done on progress events, got code %d", code)
	default:
	}

	sink.handle(court.StreamCompleteMsg{ChannelID: "secretary"})
	require.False(t, sink.streaming, "StreamCompleteMsg should clear streaming")
	require.Zero(t, sink.streamingEdictID, "StreamCompleteMsg should clear the streaming edict ID")

	// A later interrupted stream resets state the same way.
	sink.handle(court.StreamStartMsg{ChannelID: "secretary", EdictID: 4})
	require.True(t, sink.streaming, "StreamStartMsg should set streaming again")
	sink.handle(court.StreamInterruptedMsg{ChannelID: "secretary", PartialContent: "part"})
	require.False(t, sink.streaming, "StreamInterruptedMsg should clear streaming")
	require.Zero(t, sink.streamingEdictID, "StreamInterruptedMsg should clear the streaming edict ID")

	assert.Empty(t, h.warned, "known progress events must not warn; got warnings: %v", h.warned)
	select {
	case <-sink.done:
		t.Fatal("should not signal done on stream/tool progress events")
	default:
	}
}

// TestHeadlessSink_UnknownMessage_StillWarns guards the other side of the
// contract: the default branch must stay loud for genuinely unknown types
// (e.g. the old EditorRequest hang), even after the known types got cases.
func TestHeadlessSink_UnknownMessage_StillWarns(t *testing.T) {
	sink, h := newCaptureSink(t)
	sink.handle(struct{ X int }{X: 1})
	assert.Contains(t, h.warned, "headless: unhandled event",
		"genuinely unknown message types must keep warning")
}

// TestHeadlessSink_MinisterInvokingAndCompleted verifies that
// MinisterInvokingMsg and MinisterCompletedMsg are handled without panic.
func TestHeadlessSink_MinisterInvokingAndCompleted(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.MinisterInvokingMsg{
		MinisterID: "forge",
		Task:       "implement the feature",
	})
	sink.handle(court.MinisterCompletedMsg{
		MinisterID: "forge",
	})
	// No done signal from these
	select {
	case <-sink.done:
		t.Fatal("should not signal done on minister messages")
	default:
	}
}

// TestHeadlessSink_MinisterCompletedWithError verifies that
// MinisterCompletedMsg with an error is handled without panic.
func TestHeadlessSink_MinisterCompletedWithError(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.MinisterCompletedMsg{
		MinisterID: "forge",
		Error:      assert.AnError,
	})
	select {
	case <-sink.done:
		t.Fatal("should not signal done on MinisterCompletedMsg")
	default:
	}
}

// TestHeadlessSink_RitualStepStarted_NoFinish verifies that intermediate
// ritual step statuses (started, completed, failed for individual steps)
// do not signal completion.
func TestHeadlessSink_RitualStepStarted_NoFinish(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.RitualStepMsg{
		Status:     "started",
		RitualName: "swift-strike",
		StepName:   "forging",
		StepIndex:  0,
		TotalSteps: 3,
	})
	sink.handle(court.RitualStepMsg{
		Status:  "completed",
		Message: "forging done",
	})
	sink.handle(court.RitualStepMsg{
		Status:   "failed",
		StepName: "judging",
	})

	select {
	case <-sink.done:
		t.Fatal("should not signal done on intermediate ritual steps")
	default:
	}
}

// TestHeadlessContextParams verifies that headlessContextParams builds
// SetContextParams from repo info, and that a committed [court] project has no
// effect: scope is derived from the checkout (edict 1005).
func TestHeadlessContextParams(t *testing.T) {
	ri := &repo.RepoInfo{
		ProjectRoot:  "/path/to/project",
		WorktreePath: "/path/to/worktree",
		Branch:       "main",
		Slug:         "daonb/derived",
	}

	params := headlessContextParams(ri)

	assert.Equal(t, "daonb/derived", params.Project, "committed [court] project must not choose scope")
	assert.NotEqual(t, "pinned-project", params.Project)
	// The username always comes from the OS user, never config (edict 1006).
	assert.Equal(t, config.OSUsername(), params.Username)
	assert.Equal(t, "/path/to/project", params.ProjectRoot)
	assert.Equal(t, "/path/to/worktree", params.WorktreePath)
	assert.Equal(t, "main", params.Branch)
}

// TestHeadlessContextParams_NilRepoInfo verifies fallback to CWD.
func TestHeadlessContextParams_NilRepoInfo(t *testing.T) {
	cfg := &Config{}
	_ = cfg
	params := headlessContextParams(nil)
	assert.NotEmpty(t, params.ProjectRoot)
}

// TestHeadlessContextParams_ProjectFromSlug verifies the project always comes
// from repoInfo.Slug, the checkout-derived slug (edict 1005).
func TestHeadlessContextParams_ProjectFromSlug(t *testing.T) {
	ri := &repo.RepoInfo{
		ProjectRoot: "/path/to/project",
		Slug:        "my-slug",
	}
	params := headlessContextParams(ri)
	assert.Equal(t, "my-slug", params.Project)
}

// TestHeadlessSink_EventEdictCreated_NoDuplicateEnactment verifies that
// the same edict ID is only enacted once.
func TestHeadlessSink_EventEdictCreated_NoDuplicateEnactment(t *testing.T) {
	sink := newHeadlessSink(nil)

	// First EventEdictCreated — should set enactedSet
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventEdictCreated,
		EdictKey:  storage.EdictKey{ID: 5},
		Payload:   map[string]interface{}{"intent": "test"},
	})
	require.True(t, sink.enactedSet[5], "edict 5 should be marked as enacted")

	// Second EventEdictCreated for same edict — should be a no-op
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventEdictCreated,
		EdictKey:  storage.EdictKey{ID: 5},
		Payload:   map[string]interface{}{"intent": "test"},
	})
	// Still only one entry
	assert.True(t, sink.enactedSet[5])
}

// TestHeadlessSink_ZhengmingPending_NilCourt_NoPanic verifies that
// receiving a ZhengmingPendingMsg when court is nil does not panic.
// autoAnswerZhengming must guard against nil court.
func TestHeadlessSink_ZhengmingPending_NilCourt_NoPanic(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.ZhengmingPendingMsg{
		RequestID:  "req-1",
		MinisterID: "secretary",
		Questions: storage.ZhengmingQuestions{
			{Text: "Which option?", Options: []string{"A", "B"}},
		},
	})
	// No panic, no done signal
	select {
	case <-sink.done:
		t.Fatal("should not signal done on ZhengmingPendingMsg")
	default:
	}
}

// TestHeadlessSink_EventEdictSealed_NoDone verifies that EventEdictSealed
// is handled without panic and does not signal completion.
func TestHeadlessSink_EventEdictSealed_NoDone(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventEdictSealed,
		EdictKey:  storage.EdictKey{ID: 9},
	})
	select {
	case <-sink.done:
		t.Fatal("should not signal done on EventEdictSealed")
	default:
	}
}

// TestHeadlessSink_EventEdictCreated_IDZero_NoEnact verifies that
// EventEdictCreated with edict ID 0 is ignored (no enactment).
func TestHeadlessSink_EventEdictCreated_IDZero_NoEnact(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventEdictCreated,
		EdictKey:  storage.EdictKey{ID: 0},
		Payload:   map[string]interface{}{"intent": "test"},
	})
	assert.False(t, sink.enactedSet[0], "edict 0 should not be enacted")
}

// TestHeadlessSink_EventRitualEnacted_PreventsStreamDoneFinish verifies that
// receiving EventRitualEnacted (from the secretary's enact_ritual tool call)
// marks the edict in enactedSet, preventing StreamDoneMsg from finishing early.
func TestHeadlessSink_EventRitualEnacted_PreventsStreamDoneFinish(t *testing.T) {
	sink := newHeadlessSink(nil)

	// Secretary enacts a ritual via the enact_ritual tool → EventRitualEnacted
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventRitualEnacted,
		EdictKey:  storage.EdictKey{ID: 42},
		Payload:   map[string]interface{}{"ritual_name": "swift-strike"},
	})
	require.True(t, sink.enactedSet[42], "edict 42 should be tracked after EventRitualEnacted")

	// StreamDoneMsg should NOT finish because a ritual is in flight
	sink.handle(court.StreamDoneMsg{ChannelID: "secretary"})
	select {
	case code := <-sink.done:
		t.Fatalf("should not signal done when ritual is in flight, got code %d", code)
	default:
	}
}

// TestHeadlessSink_EventRitualEnacted_DoesNotDuplicateTrack verifies that
// EventRitualEnacted for an edict already in enactedSet (from auto-enact)
// does not cause issues.
func TestHeadlessSink_EventRitualEnacted_DoesNotDuplicateTrack(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.enactedSet[5] = true // already tracked from EventEdictCreated auto-enact

	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventRitualEnacted,
		EdictKey:  storage.EdictKey{ID: 5},
		Payload:   map[string]interface{}{"ritual_name": "swift-strike"},
	})
	assert.True(t, sink.enactedSet[5], "edict 5 should still be tracked")
}

// TestHeadlessSink_EventRitualEnacted_IDZero_NoTrack verifies that
// EventRitualEnacted with edict ID 0 is ignored.
func TestHeadlessSink_EventRitualEnacted_IDZero_NoTrack(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventRitualEnacted,
		EdictKey:  storage.EdictKey{ID: 0},
		Payload:   map[string]interface{}{"ritual_name": "swift-strike"},
	})
	assert.False(t, sink.enactedSet[0], "edict 0 should not be tracked")
}

// TestHeadlessSink_UnknownMessage_NoPanic verifies that an unrecognized
// message type does not cause a panic.
func TestHeadlessSink_UnknownMessage_NoPanic(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handle("some random string")
	sink.handle(42)
	sink.handle(struct{ X int }{X: 1})
	select {
	case <-sink.done:
		t.Fatal("should not signal done on unknown messages")
	default:
	}
}

// TestHeadlessSink_HandsoffOff_ZhengmingNotAnswered verifies that when
// handsoff is false and court is nil, a ZhengmingPendingMsg does not panic
// or signal done (interactiveAnswerZhengming guards against nil court).
func TestHeadlessSink_HandsoffOff_ZhengmingNotAnswered(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handsoff = false
	sink.handle(court.ZhengmingPendingMsg{
		RequestID:  "req-1",
		MinisterID: "secretary",
		Questions: storage.ZhengmingQuestions{
			{Text: "Which option?", Options: []string{"A", "B"}},
		},
	})
	// No panic, no done signal
	select {
	case <-sink.done:
		t.Fatal("should not signal done on ZhengmingPendingMsg")
	default:
	}
}

// TestHeadlessSink_HandsoffOn_ZhengmingAutoAnswered verifies that when
// handsoff is true, a ZhengmingPendingMsg is auto-answered (no panic
// even with nil court — autoAnswerZhengming guards against nil).
func TestHeadlessSink_HandsoffOn_ZhengmingAutoAnswered(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handsoff = true
	sink.handle(court.ZhengmingPendingMsg{
		RequestID:  "req-2",
		MinisterID: "secretary",
		Questions: storage.ZhengmingQuestions{
			{Text: "Which option?", Options: []string{"A", "B"}},
		},
	})
	// No panic, no done signal
	select {
	case <-sink.done:
		t.Fatal("should not signal done on ZhengmingPendingMsg")
	default:
	}
}

// setupHeadlessCourtDB opens an in-memory sqlite DB migrated with the storage
// models needed by a real Court. It returns the gorm DB and a file path for
// the sqlite file (so WAL/locking behaves like production).
func setupHeadlessCourtDB(t *testing.T) *gorm.DB {
	t.Helper()
	dir, err := os.MkdirTemp("", "headless_court_test")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	dbPath := filepath.Join(dir, "court.db")

	sqlDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	// Mirror production InitDB (storage/db.go) so the harness contends the
	// same way real SQLite does. Without these, concurrent zhengming writes
	// fail instantly with SQLITE_BUSY (database is locked) instead of waiting.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	_, err = sqlDB.Exec("PRAGMA journal_mode = WAL")
	require.NoError(t, err)
	_, err = sqlDB.Exec("PRAGMA busy_timeout = 5000")
	require.NoError(t, err)

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: gologger.Default.LogMode(gologger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(
		&storage.Edict{},
		&storage.Zhengming{},
		&storage.TianEvent{},
		&storage.Ling{},
		&storage.ForgeManifest{},
		&storage.Seal{},
		&storage.JudgeVerdict{},
		&storage.CensorPrecedent{},
	))
	return db
}

// TestHeadlessSink_HandsoffZhengmingJoinsAnswers verifies that the headless
// auto-answer (autoAnswerZhengming) answers a multi-question zhengming using
// the recommended option[0] of each question, joined with "; " — the shared
// handsoff behavior originally asserted by the TUI handsoff zhengming tests.
// It exercises the real Court path (a pending zhengming is stored and the
// answer is recorded back via HandleZhengmingResponse).
func TestHeadlessSink_HandsoffZhengmingJoinsAnswers(t *testing.T) {
	db := setupHeadlessCourtDB(t)
	cfg := config.DefaultCourtConfig()
	s := court.NewCourt(db, cfg, nil, slog.New(slog.DiscardHandler))

	// Register a pending multi-question zhengming.
	req := storage.Zhengming{
		RequestID:  "headless-auto-1",
		EdictID:    0,
		Username:   config.OSUsername(),
		Project:    cfg.Project,
		MinisterID: "secretary",
		Questions: storage.ZhengmingQuestions{
			{Text: "Q1?", Options: []string{"A", "B"}},
			{Text: "Q2?", Options: []string{"X", "Y"}},
		},
		Status:   storage.ZhengmingPending,
		Priority: storage.PriorityNormal,
	}
	require.NoError(t, db.Create(&req).Error)

	sink := newHeadlessSink(s)
	sink.handsoff = true
	sink.handle(court.ZhengmingPendingMsg{
		RequestID:  "headless-auto-1",
		MinisterID: "secretary",
		Questions: storage.ZhengmingQuestions{
			{Text: "Q1?", Options: []string{"A", "B"}},
			{Text: "Q2?", Options: []string{"X", "Y"}},
		},
	})

	// The auto-answer runs in a goroutine; poll for the zhengming to flip
	// to answered with the joined recommended options.
	require.Eventually(t, func() bool {
		var stored storage.Zhengming
		if err := db.First(&stored, "request_id = ?", "headless-auto-1").Error; err != nil {
			return false
		}
		return stored.Status == storage.ZhengmingAnswered && stored.Answer == "A; X"
	}, 3*time.Second, 20*time.Millisecond, "auto-answer should join options as 'A; X'")
}

// TestHeadlessSink_HandsoffZhengmingSingleAnswer verifies that a single
// question is auto-answered with option[0], preserving the coverage removed
// with the TUI handsoff zhengming tests.
func TestHeadlessSink_HandsoffZhengmingSingleAnswer(t *testing.T) {
	db := setupHeadlessCourtDB(t)
	cfg := config.DefaultCourtConfig()
	s := court.NewCourt(db, cfg, nil, slog.New(slog.DiscardHandler))

	req := storage.Zhengming{
		RequestID:  "headless-auto-single",
		EdictID:    0,
		Username:   config.OSUsername(),
		Project:    cfg.Project,
		MinisterID: "secretary",
		Questions: storage.ZhengmingQuestions{
			{Text: "Which approach?", Options: []string{"Option A", "Option B"}},
		},
		Status:   storage.ZhengmingPending,
		Priority: storage.PriorityNormal,
	}
	require.NoError(t, db.Create(&req).Error)

	sink := newHeadlessSink(s)
	sink.handsoff = true
	sink.handle(court.ZhengmingPendingMsg{
		RequestID:  "headless-auto-single",
		MinisterID: "secretary",
		Questions: storage.ZhengmingQuestions{
			{Text: "Which approach?", Options: []string{"Option A", "Option B"}},
		},
	})

	require.Eventually(t, func() bool {
		var stored storage.Zhengming
		if err := db.First(&stored, "request_id = ?", "headless-auto-single").Error; err != nil {
			return false
		}
		return stored.Status == storage.ZhengmingAnswered && stored.Answer == "Option A"
	}, 3*time.Second, 20*time.Millisecond, "auto-answer should pick option[0]")
}

// TestHeadlessSink_StreamDoneWithPendingSuggestion_DoesNotFinish verifies
// that StreamDoneMsg does NOT signal completion when an edict suggestion is
// pending auto-approval — even if the edict hasn't been created yet
// (enactedSet is empty). This prevents the headless process from exiting
// before the async auto-answer chain creates the edict and runs the ritual.
func TestHeadlessSink_StreamDoneWithPendingSuggestion_DoesNotFinish(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handsoff = true
	sink.handle(court.ZhengmingPendingMsg{
		RequestID:  "req-suggest",
		MinisterID: "secretary",
		EdictKey:   storage.EdictKey{ID: 0},
		Questions: storage.ZhengmingQuestions{
			{Text: "Fix the suggest_edict flow", Summary: "Fix suggest_edict flow", Options: []string{tools.AnswerApproveEdict, tools.AnswerReject}},
		},
	})
	require.Equal(t, 1, sink.pendingSuggestions, "suggestion should be tracked as pending")

	// Even with no enacted edict, StreamDone must not finish while a
	// suggestion is pending.
	sink.handle(court.StreamDoneMsg{ChannelID: "secretary"})
	select {
	case code := <-sink.done:
		t.Fatalf("should not signal done while suggestion pending, got code %d", code)
	default:
		// correct — edict creation/ritual completion will signal later
	}

	// Once the edict is created, the pending count decrements and the ritual
	// is enacted, so completion is now driven by the ritual events.
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventEdictCreated,
		EdictKey:  storage.EdictKey{ID: 42},
		Payload:   map[string]interface{}{"intent": "Fix suggest_edict flow"},
	})
	require.Equal(t, 0, sink.pendingSuggestions, "pending suggestion should be cleared after edict creation")
	require.True(t, sink.enactedSet[42], "edict 42 should be tracked after creation")

	// Ritual completion signals exit 0.
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventRitualCompleted,
		EdictKey:  storage.EdictKey{ID: 42},
	})
	select {
	case code := <-sink.done:
		assert.Equal(t, 0, code)
	default:
		t.Fatal("expected done signal after ritual completed")
	}
}

// TestHeadlessSink_SuggestedEdictPrints verifies that in handsoff mode an
// edict suggestion is printed to stdout (before auto-answering) so the ruler
// can see the proposed edict.
func TestHeadlessSink_SuggestedEdictPrints(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handsoff = true
	var out strings.Builder
	sink.stdout = &out

	sink.handle(court.ZhengmingPendingMsg{
		RequestID:  "req-suggest-print",
		MinisterID: "secretary",
		EdictKey:   storage.EdictKey{ID: 0},
		Questions: storage.ZhengmingQuestions{
			{Text: "Add a login page and wire it to the auth service", Summary: "Add login page", Options: []string{tools.AnswerApproveEdict, tools.AnswerReject}},
		},
	})

	outStr := out.String()
	assert.Contains(t, outStr, "📜 Suggested New Edict")
	assert.Contains(t, outStr, "Add login page")
	assert.Contains(t, outStr, "Add a login page and wire it to the auth service")
	assert.Contains(t, outStr, "[handsoff] Approving")

	// A non-suggestion zhengming must not print the suggestion block.
	out.Reset()
	sink.handle(court.ZhengmingPendingMsg{
		RequestID:  "req-other",
		MinisterID: "secretary",
		EdictKey:   storage.EdictKey{ID: 0},
		Questions: storage.ZhengmingQuestions{
			{Text: "Which option?", Options: []string{"A", "B"}},
		},
	})
	assert.NotContains(t, out.String(), "📜 Suggested New Edict")
}

// TestHeadlessSink_EditorRequest_HandsoffAutoApproves verifies that the
// headless sink handles tools.EditorRequest by auto-approving the content
// unchanged. In handsoff mode, it must NOT print the document. Without this
// handler, the edict-suggestion editor review blocks forever on ResultChan.
func TestHeadlessSink_EditorRequest_HandsoffAutoApproves(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handsoff = true
	var out strings.Builder
	sink.stdout = &out

	resultChan := make(chan tools.EditorResult, 1)
	sink.handle(tools.EditorRequest{
		Content:    "large document content >500 chars...",
		Filename:   "suggested_edict.md",
		ResultChan: resultChan,
	})

	select {
	case res := <-resultChan:
		assert.Equal(t, tools.EditorUnchanged, res.Outcome, "handsoff must report the content reviewed unchanged")
		assert.Equal(t, "large document content >500 chars...", res.Content, "content should be unchanged")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EditorResult — the review helper would hang")
	}

	// In handsoff mode the document is not printed to stdout.
	assert.NotContains(t, out.String(), "Document for review")
}

// TestHeadlessSink_EditorRequest_NonHandsoffPrints verifies that when NOT in
// handsoff mode, an EditorRequest prints the content to stdout (so the user
// still sees it) and still auto-approves since headless has no editor.
func TestHeadlessSink_EditorRequest_NonHandsoffPrints(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handsoff = false
	var out strings.Builder
	sink.stdout = &out

	resultCh := make(chan tools.EditorResult, 1)
	sink.handle(tools.EditorRequest{
		Content:    "Some document body",
		Filename:   "doc.md",
		ResultChan: resultCh,
	})

	assert.Contains(t, out.String(), "Document for review")
	assert.Contains(t, out.String(), "Some document body")

	select {
	case res := <-resultCh:
		assert.Equal(t, tools.EditorUnchanged, res.Outcome, "headless must report the content reviewed unchanged")
		assert.Equal(t, "Some document body", res.Content)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EditorResult")
	}
}

// TestHeadlessSink_HandsoffOff_NoAutoEnact verifies that when handsoff is
// false, EventEdictCreated does NOT auto-enact swift-strike (edict is
// tracked in enactedSet but no ritual is published).
func TestHeadlessSink_HandsoffOff_NoAutoEnact(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handsoff = false
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventEdictCreated,
		EdictKey:  storage.EdictKey{ID: 10},
		Payload:   map[string]interface{}{"intent": "test"},
	})
	// Edict should be tracked (so StreamDone doesn't finish early if
	// the secretary enacts a ritual via the tool), but no auto-enact.
	assert.True(t, sink.enactedSet[10], "edict 10 should be tracked")
}

// TestHeadlessSink_HandsoffOn_AutoEnacts verifies that when handsoff is
// true, EventEdictCreated triggers auto-enact (edict tracked in enactedSet).
// With nil court, enactSwiftStrike is a no-op but enactedSet is set before
// the call.
func TestHeadlessSink_HandsoffOn_AutoEnacts(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handsoff = true
	sink.handle(court.EventNotificationMsg{
		EventType: storage.EventEdictCreated,
		EdictKey:  storage.EdictKey{ID: 11},
		Payload:   map[string]interface{}{"intent": "test"},
	})
	assert.True(t, sink.enactedSet[11], "edict 11 should be tracked")
}

// TestHeadlessSink_InteractiveZhengming_SingleQuestion verifies that
// interactiveAnswerZhengming reads a selection from stdin and builds
// the correct answer. Uses a mock stdin/stdout and nil court (which
// causes interactiveAnswerZhengming to return early, but promptQuestion
// is tested directly).
func TestHeadlessSink_InteractiveZhengming_SingleQuestion(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.stdin = strings.NewReader("2\n")
	var out strings.Builder
	sink.stdout = &out

	answer := sink.promptQuestion(storage.ZhengmingQuestion{
		Text:    "Which option?",
		Options: []string{"A", "B", "C"},
	})
	assert.Equal(t, "B", answer)
	assert.Contains(t, out.String(), "Which option?")
	assert.Contains(t, out.String(), "1) A")
	assert.Contains(t, out.String(), "2) B")
	assert.Contains(t, out.String(), "3) C")
}

// TestHeadlessSink_InteractiveZhengming_UsesSummary verifies that
// promptQuestion uses the summary field when available.
func TestHeadlessSink_InteractiveZhengming_UsesSummary(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.stdin = strings.NewReader("1\n")
	var out strings.Builder
	sink.stdout = &out

	answer := sink.promptQuestion(storage.ZhengmingQuestion{
		Text:    "Which approach do you prefer for this particular situation?",
		Summary: "Approach?",
		Options: []string{"Option A", "Option B"},
	})
	assert.Equal(t, "Option A", answer)
	assert.Contains(t, out.String(), "Approach?")
	assert.NotContains(t, out.String(), "Which approach do you prefer")
}

// TestHeadlessSink_InteractiveZhengming_InvalidInput_Reprompts verifies
// that invalid input re-prompts the same question until a valid
// selection is made.
func TestHeadlessSink_InteractiveZhengming_InvalidInput_Reprompts(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.stdin = strings.NewReader("abc\n0\n5\n2\n")
	var out strings.Builder
	sink.stdout = &out

	answer := sink.promptQuestion(storage.ZhengmingQuestion{
		Text:    "Pick one",
		Options: []string{"A", "B", "C"},
	})
	assert.Equal(t, "B", answer)
	assert.Contains(t, out.String(), "Invalid selection")
}

// TestHeadlessSink_InteractiveZhengming_NilCourt_NoPanic verifies that
// interactiveAnswerZhengming with nil court returns early without panic.
func TestHeadlessSink_InteractiveZhengming_NilCourt_NoPanic(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handsoff = false
	sink.stdin = strings.NewReader("1\n")
	var out strings.Builder
	sink.stdout = &out

	// Should not panic, should not signal done
	sink.handle(court.ZhengmingPendingMsg{
		RequestID: "req-1",
		Questions: storage.ZhengmingQuestions{
			{Text: "Pick one", Options: []string{"A", "B"}},
		},
	})
	select {
	case <-sink.done:
		t.Fatal("should not signal done on ZhengmingPendingMsg")
	default:
	}
}

// TestHeadlessNoLLMStartup_DBHonorsASIMIHome verifies that DefaultConfig
// defaults the court DB path to ${ASIMI_HOME}/asimi.sqlite (not the read-only
// ~/.local/share/asimi) when ASIMI_HOME is set — mirroring how the ATIF writer
// honors ASIMI_HOME. This is the config-side fix for the Harbor --isolated-host
// startup crash (SQLite readonly error 1544) and needs no LLM to run.
func TestHeadlessNoLLMStartup_DBHonorsASIMIHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ASIMI_HOME", home)

	cfg := config.DefaultConfig()
	require.Equal(t, filepath.Join(home, "asimi.sqlite"), cfg.Storage.DatabasePath,
		"DefaultConfig must default DB path to ${ASIMI_HOME}/asimi.sqlite when ASIMI_HOME is set")

	// Without ASIMI_HOME, the classic ~/.local/share path is retained.
	t.Setenv("ASIMI_HOME", "")
	cfg = config.DefaultConfig()
	require.NotEqual(t, "", cfg.Storage.DatabasePath)
	require.True(t, strings.HasSuffix(cfg.Storage.DatabasePath, filepath.Join(".local", "share", "asimi", "asimi.sqlite")),
		"DB path must fall back to the classic location when ASIMI_HOME is unset, got %q", cfg.Storage.DatabasePath)
}

// TestHeadlessNoLLMStartup_DBWritableFallback verifies that storage.InitDB
// does not abort startup when the requested (resolved) DB path is unusable:
// it falls back to ${ASIMI_HOME}/asimi.sqlite, then os.TempDir(), only erroring
// if every candidate fails. The primary path is forced to fail by placing a
// regular file as its parent (works regardless of whether the test runs as
// root, where read-only directory permissions would not block writes). This is
// the storage-side fix for the Harbor --isolated-host read-only $HOME crash.
func TestHeadlessNoLLMStartup_DBWritableFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ASIMI_HOME", home)

	// Primary path made unusable: its parent is a regular file, so MkdirAll
	// fails with ENOTDIR for any user (root included) and forces the fallback.
	block := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(block, []byte("x"), 0o644))
	primary := filepath.Join(block, "asimi.sqlite")

	db, err := storage.InitDB(primary)
	require.NoError(t, err, "InitDB must fall back to a writable path when the resolved path is unusable")
	defer db.Close()

	// The DB should have landed under ASIMI_HOME, not the unusable primary path.
	require.Equal(t, filepath.Join(home, "asimi.sqlite"), db.Path())

	// The court DB must be usable (schema present).
	var version int
	require.NoError(t, db.Conn().QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version))
	require.GreaterOrEqual(t, version, 1)
}

// TestLogDir_HonorsASIMIHome verifies that the production log directory is
// routed through ASIMI_HOME when it is set (so a read-only $HOME cannot abort
// startup with a log-dir panic), and falls back to the classic
// ~/.local/share/asimi location otherwise. This is the logger-side half of the
// Harbor --isolated-host read-only $HOME fix.
func TestLogDir_HonorsASIMIHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ASIMI_HOME", home)
	t.Setenv("HOME", t.TempDir())

	origDebug := cli.Debug
	cli.Debug = false
	t.Cleanup(func() { cli.Debug = origDebug })

	require.Equal(t, home, logDir(),
		"logDir must point at ASIMI_HOME when it is set")

	t.Setenv("ASIMI_HOME", "")
	require.True(t, strings.HasSuffix(logDir(), filepath.Join(".local", "share", "asimi")),
		"logDir must fall back to ~/.local/share/asimi without ASIMI_HOME, got %q", logDir())

	// $ASIMI_HOME takes precedence over --debug: it is the environment's
	// signal that cwd is not writable (Harbor/read-only cwd).
	t.Setenv("ASIMI_HOME", home)
	cli.Debug = true
	require.Equal(t, home, logDir(),
		"logDir must point at ASIMI_HOME even in debug mode")

	// With no ASIMI_HOME, --debug falls back to the current directory.
	t.Setenv("ASIMI_HOME", "")
	require.Equal(t, ".", logDir())
}

// TestLogLevel verifies the ruled resolution contract: --debug wins, empty
// --log-level defaults to info, valid levels map through, and an unrecognized
// level is a hard error rather than a silent info fallback.
func TestLogLevel(t *testing.T) {
	origDebug, origLevel := cli.Debug, cli.LogLevel
	t.Cleanup(func() { cli.Debug, cli.LogLevel = origDebug, origLevel })

	cases := []struct {
		name     string
		debug    bool
		logLevel string
		want     slog.Level
		wantErr  bool
	}{
		{name: "empty defaults to info", logLevel: "", want: slog.LevelInfo},
		{name: "debug level", logLevel: "debug", want: slog.LevelDebug},
		{name: "info level", logLevel: "info", want: slog.LevelInfo},
		{name: "warn level", logLevel: "warn", want: slog.LevelWarn},
		{name: "error level", logLevel: "error", want: slog.LevelError},
		{name: "unknown level errors", logLevel: "trace", wantErr: true},
		{name: "debug flag wins over empty level", debug: true, logLevel: "", want: slog.LevelDebug},
		{name: "debug flag wins over warn", debug: true, logLevel: "warn", want: slog.LevelDebug},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli.Debug, cli.LogLevel = tc.debug, tc.logLevel
			got, err := logLevel()
			if tc.wantErr {
				require.Error(t, err, "invalid level must be rejected, not silently defaulted")
				require.Contains(t, err.Error(), "--log-level")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestLogDir_LogLevelDebug verifies that --log-level=debug mirrors --debug's
// cwd location effect (ruling 4) while other levels and ASIMI_HOME keep their
// existing behavior.
func TestLogDir_LogLevelDebug(t *testing.T) {
	origDebug, origLevel := cli.Debug, cli.LogLevel
	t.Cleanup(func() { cli.Debug, cli.LogLevel = origDebug, origLevel })
	cli.Debug = false

	t.Setenv("HOME", t.TempDir())

	// No ASIMI_HOME: level debug sends logs to cwd.
	t.Setenv("ASIMI_HOME", "")
	cli.LogLevel = "debug"
	require.Equal(t, ".", logDir(), "--log-level=debug must mirror --debug's cwd fallback")

	// Other levels keep the classic location.
	cli.LogLevel = "info"
	require.True(t, strings.HasSuffix(logDir(), filepath.Join(".local", "share", "asimi")),
		"--log-level=info must use the default location, got %q", logDir())

	// ASIMI_HOME still wins over --log-level=debug.
	home := t.TempDir()
	t.Setenv("ASIMI_HOME", home)
	cli.LogLevel = "debug"
	require.Equal(t, home, logDir(), "ASIMI_HOME must win over --log-level=debug")
}

// TestLogLevelRegression_NoFlags verifies the regression bar: with no debug
// flag and no level set, the effective level is info, the log dir is the
// default, and no announcement is printed.
func TestLogLevelRegression_NoFlags(t *testing.T) {
	origDebug, origLevel := cli.Debug, cli.LogLevel
	origMode := runHeadlessMode
	t.Cleanup(func() { cli.Debug, cli.LogLevel = origDebug, origLevel; runHeadlessMode = origMode })

	cli.Debug, cli.LogLevel = false, ""
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ASIMI_HOME", "")

	level, err := logLevel()
	require.NoError(t, err)
	require.Equal(t, slog.LevelInfo, level)
	require.True(t, strings.HasSuffix(logDir(), filepath.Join(".local", "share", "asimi")),
		"default log dir must be unchanged, got %q", logDir())

	runHeadlessMode = func() int { return 0 }
	var buf bytes.Buffer
	runHeadless(&buf, time.Now())
	require.Empty(t, buf.String(), "no announcement without --debug")
}

func TestHeadlessSink_HandleRitualStepInfo(t *testing.T) {
	sink := newHeadlessSink(nil)
	var out bytes.Buffer
	sink.stdout = &out

	sink.handleRitualStep(court.RitualStepMsg{
		StepName: "echo",
		Status:   "info",
		Message:  "hello from out step",
	})

	require.Equal(t, "hello from out step\n", out.String())
}

// runEventHeadless runs the shared headless path with cli.Event set to raw,
// capturing stdout and stderr. Sink and event prints target os.Stdout by
// default, so both are swapped for pipes around the run.
func runEventHeadless(t *testing.T, raw string) (code int, stdout, stderr string) {
	t.Helper()

	or, ow, err := os.Pipe()
	require.NoError(t, err)
	er, ew, err := os.Pipe()
	require.NoError(t, err)

	origStdout, origStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = ow, ew
	origEvent := cli.Event
	cli.Event = raw
	t.Cleanup(func() {
		os.Stdout, os.Stderr = origStdout, origStderr
		cli.Event = origEvent
	})

	done := make(chan int, 1)
	go func() { done <- runHeadlessModeImpl() }()

	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(&outBuf, or) }()
	go func() { defer wg.Done(); io.Copy(&errBuf, er) }()

	select {
	case code = <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("event headless run timed out")
	}
	ow.Close()
	ew.Close()
	wg.Wait()
	return code, outBuf.String(), errBuf.String()
}

func TestHeadlessEventMode_MalformedEvent(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "invalid json", raw: "{not-json"},
		{name: "json array", raw: `["a","b"]`},
		{name: "json null", raw: "null"},
		{name: "missing event_type", raw: `{"provider":"github","delivery_id":"d","payload":{}}`},
		{name: "empty event_type", raw: `{"provider":"github","event_type":"","payload":{}}`},
		{name: "missing payload", raw: `{"provider":"github","event_type":"push"}`},
		{name: "payload not an object", raw: `{"provider":"github","event_type":"push","payload":"not-json"}`},
		{name: "payload null", raw: `{"provider":"github","event_type":"push","payload":null}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupIsolatedEventEnv(t)
			code, stdout, stderr := runEventHeadless(t, tc.raw)
			require.Equal(t, 1, code)
			require.NotEmpty(t, stderr)
			require.Empty(t, stdout)
		})
	}
}

// TestParseWebhookEvent_ServiceFormat verifies the --event delivery format
// normalizes into the storage.JSON trigger payload exactly as specified:
// event_json verbatim, github_event from event_type, github_delivery from
// delivery_id, provider and received_at passed through, and body the payload
// re-serialized as a compact JSON string.
func TestParseWebhookEvent_ServiceFormat(t *testing.T) {
	raw := `{"provider":"github","event_type":"push","delivery_id":"d67eccb4-e8e3-11f1-90fa-86ac38dcf04d","received_at":"2026-11-25T15:21:09Z","payload":{"ref":"refs/heads/main","forced":false}}`

	payload, err := parseWebhookEvent(raw)
	require.NoError(t, err)
	require.Equal(t, raw, payload["event_json"])
	require.Equal(t, "push", payload["github_event"])
	require.Equal(t, "d67eccb4-e8e3-11f1-90fa-86ac38dcf04d", payload["github_delivery"])
	require.Equal(t, "github", payload["provider"])
	require.Equal(t, "2026-11-25T15:21:09Z", payload["received_at"])
	require.JSONEq(t, `{"forced":false,"ref":"refs/heads/main"}`, payload["body"].(string))
}

// TestParseWebhookEvent_RejectsMalformed verifies strict validation: any
// malformed delivery is an error, never a silent no-match.
func TestParseWebhookEvent_RejectsMalformed(t *testing.T) {
	cases := []string{
		"{not-json",
		`["a","b"]`,
		"null",
		`{"payload":{}}`,
		`{"event_type":"","payload":{}}`,
		`{"event_type":"push"}`,
		`{"event_type":"push","payload":null}`,
		`{"event_type":"push","payload":"str"}`,
	}
	for _, raw := range cases {
		payload, err := parseWebhookEvent(raw)
		require.Error(t, err, "raw: %s", raw)
		require.Nil(t, payload, "raw: %s", raw)
	}
}

func setupIsolatedEventEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ASIMI_HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	origIsolated := cli.IsolatedHost
	origHandsoff := cli.Handsoff
	cli.IsolatedHost = true
	cli.Handsoff = true
	t.Cleanup(func() {
		cli.IsolatedHost = origIsolated
		cli.Handsoff = origHandsoff
	})
}

func TestHeadlessEventMode_PingTriggersHello(t *testing.T) {
	setupIsolatedEventEnv(t)

	raw := `{"provider":"github","event_type":"ping","delivery_id":"delivery-abc","received_at":"2026-11-25T15:21:09Z","payload":{"zen":"keep it simple"}}`
	code, stdout, stderr := runEventHeadless(t, raw)

	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Contains(t, stdout, "event=ping")
	require.Contains(t, stdout, "delivery=delivery-abc")
	require.Contains(t, stdout, raw)
}

// TestHeadlessEventMode_PushDeliveryNoMatch is the real-world smoke case:
// the wh-event.json delivery shape (event_type "push") parses cleanly and
// routes with no matching ritual, printing the no-match line and exit 0.
func TestHeadlessEventMode_PushDeliveryNoMatch(t *testing.T) {
	setupIsolatedEventEnv(t)

	raw := `{"provider":"github","event_type":"push","delivery_id":"d67eccb4-e8e3-11f1-90fa-86ac38dcf04d","received_at":"2026-11-25T15:21:09Z","payload":{"ref":"refs/heads/main","before":"7eae20c9","after":"36591d19"}}`
	code, stdout, stderr := runEventHeadless(t, raw)

	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Contains(t, stdout, "no ritual matched webhook_received")
	require.Contains(t, stdout, `"push"`)
	require.Contains(t, stdout, `"d67eccb4-e8e3-11f1-90fa-86ac38dcf04d"`)
}

// TestHeadlessEventMode_SingleCourtStarted pins the Tian ledger to exactly
// one court_started row per headless run. Regression guard for the merge
// artifact that published the event twice: duplicated ledger rows and, per
// ritual_manager.go, every court_started-triggered ritual firing twice per
// run — invisible to the suite until this assertion existed.
func TestHeadlessEventMode_SingleCourtStarted(t *testing.T) {
	setupIsolatedEventEnv(t)

	raw := `{"provider":"github","event_type":"push","delivery_id":"d67eccb4-e8e3-11f1-90fa-86ac38dcf04d","received_at":"2026-11-25T15:21:09Z","payload":{"ref":"refs/heads/main"}}`
	code, _, stderr := runEventHeadless(t, raw)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	dbPath := filepath.Join(os.Getenv("ASIMI_HOME"), "asimi.sqlite")
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer conn.Close()

	// The run's own connection may still be closing as fx unwinds; wait out
	// the WAL lock instead of racing it.
	_, err = conn.Exec("PRAGMA busy_timeout = 5000")
	require.NoError(t, err)

	var count int
	require.NoError(t, conn.QueryRow(
		"SELECT COUNT(*) FROM tian_events WHERE event_type = 'court_started'",
	).Scan(&count))
	require.Equal(t, 1, count,
		"a headless run must persist exactly one court_started row, got %d", count)
}

func TestHeadlessEventMode_UnmatchedEventExitsZero(t *testing.T) {
	setupIsolatedEventEnv(t)

	raw := `{"provider":"github","event_type":"unknown_test_event","delivery_id":"delivery-999","received_at":"2026-11-25T15:21:09Z","payload":{}}`
	code, stdout, stderr := runEventHeadless(t, raw)

	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Contains(t, stdout, "unknown_test_event")
	require.Contains(t, stdout, "delivery-999")
}

func TestHeadlessSink_EventModeIgnoresStreamDone(t *testing.T) {
	sink := newHeadlessSink(nil)
	var out bytes.Buffer
	sink.stdout = &out
	sink.eventMode = true

	sink.handle(court.StreamDoneMsg{ChannelID: "e0"})

	select {
	case code := <-sink.done:
		t.Fatalf("eventMode sink exited prematurely with code %d on StreamDoneMsg", code)
	default:
	}
}

func TestHeadlessSink_EventModeIgnoresUnrelatedCompletionBeforeArm(t *testing.T) {
	sink := newHeadlessSink(nil)
	var out bytes.Buffer
	sink.stdout = &out
	sink.eventMode = true

	sink.handleRitualStep(court.RitualStepMsg{
		RitualName: "dawn-audience",
		EdictID:    0,
		Status:     "ritual_completed",
		Message:    "1ms",
	})

	select {
	case code := <-sink.done:
		t.Fatalf("eventMode sink exited prematurely with code %d on unrelated ritual_completed before matched rituals were armed", code)
	default:
	}
}

func TestHeadlessSink_EventModeRitualFailureExitsOne(t *testing.T) {
	sink := newHeadlessSink(nil)
	var out, errBuf bytes.Buffer
	sink.stdout = &out
	sink.stderr = &errBuf
	sink.eventMode = true
	sink.armEventRituals([]string{"hello"})

	sink.handleRitualStep(court.RitualStepMsg{
		RitualName: "hello",
		EdictID:    0,
		Status:     "ritual_failed",
		Message:    "boom",
	})

	select {
	case code := <-sink.done:
		require.Equal(t, 1, code)
	default:
		t.Fatal("expected eventMode sink to finish with exit code 1")
	}
	require.Contains(t, errBuf.String(), "boom")
}

func TestHeadlessSink_EventModeMultipleRituals(t *testing.T) {
	t.Run("all succeed exits 0", func(t *testing.T) {
		sink := newHeadlessSink(nil)
		var out, errBuf bytes.Buffer
		sink.stdout = &out
		sink.stderr = &errBuf
		sink.eventMode = true
		sink.armEventRituals([]string{"hello", "audit"})

		sink.handleRitualStep(court.RitualStepMsg{
			RitualName: "hello",
			EdictID:    0,
			Status:     "ritual_completed",
			Message:    "1ms",
		})
		select {
		case code := <-sink.done:
			t.Fatalf("exited early with code %d while second ritual still pending", code)
		default:
		}

		sink.handleRitualStep(court.RitualStepMsg{
			RitualName: "audit",
			EdictID:    0,
			Status:     "ritual_completed",
			Message:    "2ms",
		})
		select {
		case code := <-sink.done:
			require.Equal(t, 0, code)
		default:
			t.Fatal("expected exit 0 after all matched rituals succeeded")
		}
	})

	t.Run("one succeeds one fails exits 1", func(t *testing.T) {
		sink := newHeadlessSink(nil)
		var out, errBuf bytes.Buffer
		sink.stdout = &out
		sink.stderr = &errBuf
		sink.eventMode = true
		sink.armEventRituals([]string{"hello", "audit"})

		sink.handleRitualStep(court.RitualStepMsg{
			RitualName: "hello",
			EdictID:    0,
			Status:     "ritual_completed",
			Message:    "1ms",
		})
		select {
		case code := <-sink.done:
			t.Fatalf("exited early with code %d while second ritual still pending", code)
		default:
		}

		sink.handleRitualStep(court.RitualStepMsg{
			RitualName: "audit",
			EdictID:    0,
			Status:     "ritual_failed",
			Message:    "failed step",
		})
		select {
		case code := <-sink.done:
			require.Equal(t, 1, code)
		default:
			t.Fatal("expected exit 1 after one matched ritual failed")
		}
	})
}

func TestHeadlessSink_EventModeIgnoresUnrelatedWhilePending(t *testing.T) {
	sink := newHeadlessSink(nil)
	var out, errBuf bytes.Buffer
	sink.stdout = &out
	sink.stderr = &errBuf
	sink.eventMode = true
	sink.armEventRituals([]string{"hello"})

	// Different ritual name on edict 0.
	sink.handleRitualStep(court.RitualStepMsg{
		RitualName: "dawn-audience",
		EdictID:    0,
		Status:     "ritual_completed",
		Message:    "1ms",
	})
	// Same ritual name but non-zero EdictID.
	sink.handleRitualStep(court.RitualStepMsg{
		RitualName: "hello",
		EdictID:    42,
		Status:     "ritual_completed",
		Message:    "1ms",
	})
	// StreamDoneMsg and StreamErrorMsg while ritual is still pending.
	sink.handle(court.StreamDoneMsg{ChannelID: "e0"})
	sink.handle(court.StreamErrorMsg{ChannelID: "e0", Err: io.EOF})

	select {
	case code := <-sink.done:
		t.Fatalf("exited early with code %d on unrelated completions", code)
	default:
	}

	sink.handleRitualStep(court.RitualStepMsg{
		RitualName: "hello",
		EdictID:    0,
		Status:     "ritual_completed",
		Message:    "3ms",
	})
	select {
	case code := <-sink.done:
		require.Equal(t, 0, code)
	default:
		t.Fatal("expected exit 0 once matched ritual completed")
	}
}

func TestSelectEntryMode_EventFlagDoesNotBlockOnOpenStdin(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	// --event with non-TTY open stdin pipe must not block on stdin.
	mode, arg := selectEntryMode("", `{"provider":"github","event_type":"push","payload":{}}`, false, pr)
	require.Equal(t, entryHeadlessEvent, mode)
	require.Equal(t, `{"provider":"github","event_type":"push","payload":{}}`, arg)

	// Explicit -p prompt still takes precedence over --event.
	mode, arg = selectEntryMode("do something", `{"provider":"github","event_type":"push","payload":{}}`, false, pr)
	require.Equal(t, entryHeadlessPrompt, mode)
	require.Equal(t, "do something", arg)

	// Piped stdin without -p or --event reads prompt.
	mode, arg = selectEntryMode("", "", false, strings.NewReader("  piped prompt \n"))
	require.Equal(t, entryHeadlessPrompt, mode)
	require.Equal(t, "piped prompt", arg)

	// Empty closed stdin without -p or --event falls back to interactive.
	mode, arg = selectEntryMode("", "", false, strings.NewReader(""))
	require.Equal(t, entryInteractive, mode)
	require.Equal(t, "", arg)
}

// TestEventFlagParsesViaKong asserts the --event CLI flag parses via kong
// and populates cli.Event, which drives the shared headless run path.
func TestEventFlagParsesViaKong(t *testing.T) {
	orig := cli.Event
	t.Cleanup(func() { cli.Event = orig })

	raw := `{"provider":"github","event_type":"ping","payload":{}}`
	parser := kong.Must(&cli, cliOptions...)
	_, err := parser.Parse([]string{"--event", raw})
	require.NoError(t, err)
	require.Equal(t, raw, cli.Event)
}

// TestDebugLogAnnouncement verifies the harness-visible line printed in
// headless --debug runs, table-testing both path-resolution success and
// failure cases.
func TestDebugLogAnnouncement(t *testing.T) {
	origDebug := cli.Debug
	t.Cleanup(func() { cli.Debug = origDebug })

	home := t.TempDir()

	cases := []struct {
		name      string
		asimiHome string
		home      string
		debug     bool
		want      string
	}{
		{
			name:      "resolved via ASIMI_HOME",
			asimiHome: home,
			debug:     true,
			want:      "Running in debug mode, log file at " + filepath.Join(home, "asimi.log"),
		},
		{
			name:      "resolved via cwd in debug without home",
			asimiHome: "",
			debug:     true,
			want:      "Running in debug mode, log file at " + mustAbs(t, "asimi.log"),
		},
		{
			name:      "unresolved without home or debug cwd fallback",
			asimiHome: "",
			home:      "", // os.UserHomeDir fails
			debug:     false,
			want:      "Running in debug mode, log file at (unresolved; logging to stderr)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ASIMI_HOME", tc.asimiHome)
			t.Setenv("HOME", tc.home)
			cli.Debug = tc.debug
			require.Equal(t, tc.want, debugLogAnnouncement())
		})
	}

	// Restore for the emit-path test below.
	cli.Debug = true
	t.Setenv("ASIMI_HOME", home)
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	require.NoError(t, err)
	return abs
}

// TestPrintDebugAnnouncementEmit covers the emit path itself (writer seam +
// trailing newline + harness-visible prefix), guarding the production call
// site: if the call site in main() stops passing os.Stdout, this test still
// passes — but it fails if printDebugAnnouncement changes its format or loses
// the newline, and its existence keeps the seam exercised.
func TestPrintDebugAnnouncementEmit(t *testing.T) {
	origDebug := cli.Debug
	cli.Debug = true
	t.Cleanup(func() { cli.Debug = origDebug })

	home := t.TempDir()
	t.Setenv("ASIMI_HOME", home)

	var buf bytes.Buffer
	printDebugAnnouncement(&buf)

	want := "Running in debug mode, log file at " + filepath.Join(home, "asimi.log") + "\n"
	require.Equal(t, want, buf.String())
}

// TestRunHeadless verifies the wiring itself: in a debug headless run the
// announcement is written to stdout before the headless mode runs. Uses a
// stub for runHeadlessMode so no real headless session is booted.
func TestRunHeadless(t *testing.T) {
	origDebug := cli.Debug
	origMode := runHeadlessMode
	t.Cleanup(func() { cli.Debug = origDebug; runHeadlessMode = origMode })

	clicks := []string{}
	runHeadlessMode = func() int { clicks = append(clicks, "run"); return 7 }
	cli.Debug = true
	t.Setenv("ASIMI_HOME", t.TempDir())

	var buf bytes.Buffer
	code := runHeadless(&buf, time.Now())

	require.Equal(t, 7, code, "runHeadless must return the headless run's exit code")
	require.Equal(t, []string{"run"}, clicks, "runHeadlessMode must be called exactly once")
	require.True(t, strings.HasPrefix(buf.String(), "Running in debug mode, log file at "),
		"debug headless run must announce the log path first, got %q", buf.String())

	// Without --debug, nothing is announced (stdout stays clean for piped output).
	buf.Reset()
	clicks = nil
	cli.Debug = false
	runHeadless(&buf, time.Now())
	require.Empty(t, buf.String(), "announcement must not print outside debug mode")
	require.Equal(t, []string{"run"}, clicks)
}

// TestPromptFlagAcceptsLeadingDash guards Harbor Defect A: a benchmark task
// prompt that begins with '-' must be consumed as the -p/--prompt value rather
// than mistaken for a flag. Before cliOptions enabled hyphen-prefixed
// parameters, `asimi -p '- do X'` aborted with
// `--prompt: expected string value but got "- do X" (short flag)` and exit
// code 80 — asimi never started.
func TestPromptFlagAcceptsLeadingDash(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"short flag with dash value", []string{"-p", "- You are given a PyTorch state dict"}, "- You are given a PyTorch state dict"},
		{"long flag with dash value", []string{"--prompt", "- hello"}, "- hello"},
		{"equals form with dash value", []string{"--prompt=- hello"}, "- hello"},
		{"plain value unchanged", []string{"-p", "hello"}, "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := cli
			t.Cleanup(func() { cli = orig })

			parser, err := kong.New(&cli, cliOptions...)
			require.NoError(t, err)
			_, err = parser.Parse(tc.args)
			require.NoError(t, err, "leading-dash prompt must parse as a value")
			assert.Equal(t, tc.want, cli.Prompt)
		})
	}
}

// resolveE2EBinary returns the path to a prebuilt asimi binary for the
// real-binary E2E tests. The binary is NOT built here: an in-suite `go build`
// of the whole module OOM-kills the linker on memory-constrained runners
// (proven 2026-11-20: `link: signal: killed` at ~1.9GB with no swap; runs
// #293/#294). CI builds the binary once in its own step (see .github/workflows
// /ci.yml `Build e2e binary`) and exports ASIMI_E2E_BINARY. When the variable
// is unset the test skips rather than building inline, so `just test` never
// compiles the module from inside the suite.
func resolveE2EBinary(t *testing.T) string {
	t.Helper()
	path := os.Getenv("ASIMI_E2E_BINARY")
	if path == "" {
		t.Skip("skipping real-binary E2E (set ASIMI_E2E_BINARY to a prebuilt asimi binary)")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ASIMI_E2E_BINARY=%s is not usable: %v", path, err)
	}
	return path
}

// NOTE: TestHeadless_BogusProviderFailsFast (the e1075 bogus-provider
// regression) was removed by the ruler's decree: tests that build the binary
// from inside the suite wedge CI's 5m budget (run #293). The regression
// coverage will return as a post-build test against the released binary.

// TestActivation_ATIF_E2E is a terminal-bench readiness smoke test.
// It runs a PREBUILT asimi binary (ASIMI_E2E_BINARY, exported by CI) end-to-end
// in headless mode against a copy of the ror-project:
//
//	asimi --handsoff --atif --isolated-host -p "add a feature"
//
// It verifies that (1) asimi exits successfully, (2) asimi actually made
// changes to the project (git diff is non-empty), and (3) the ATIF
// trajectory file (agent/asimi.txt) was fully populated — not empty.
//
// The binary is never built in-suite: an inline `go build` of the whole module
// OOM-kills the linker on memory-constrained runners (proven 2026-11-20, runs
// #293/#294). Without ASIMI_E2E_BINARY set the test skips.
//
// The model/provider used is whatever the caller is actually configured to
// run: this test deliberately does NOT force a model. Any ASIMI_MODEL /
// ASIMI_PROVIDER (or backend config) the user has in effect is inherited by
// the subprocess, so the readiness check exercises the production setup.
//
// Requires a real LLM, so it is gated the same way as
// TestInitRitualWithLLM_E2E: it skips unless CI=true and LLM_E2E=1 are both
// set and a provider API key is present in the environment.
func TestActivation_ATIF_E2E(t *testing.T) {
	skipIfNotCI(t)
	if os.Getenv("LLM_E2E") == "" {
		t.Skip("skipping real-LLM E2E (set LLM_E2E=1 to run)")
	}
	// Detect any configured provider purely as a gate — we inherit (and
	// exercise) whatever model the caller actually runs, not a hardcoded one.
	provider, _, _ := detectLLMProvider()
	if provider == "" {
		t.Skip("no LLM API key found (set ANTHROPIC_API_KEY, OPENROUTER_API_KEY, or OPENAI_API_KEY)")
	}
	t.Logf("provider key detected: %s (model comes from user config/env)", provider)

	// 1. Use the prebuilt asimi binary exported by CI (never build in-suite).
	binary := resolveE2EBinary(t)
	repoRoot, err := os.Getwd()
	require.NoError(t, err)

	// 2. Set up a clean git repo from the ror-project demo so the agent has
	// a real project to modify.
	tmpDir := t.TempDir()
	srcDir := filepath.Join(repoRoot, "testdata", "ror-project")
	cpCmd := exec.Command("cp", "-r", srcDir+"/.", tmpDir)
	require.NoError(t, cpCmd.Run(), "failed to copy testdata/ror-project")

	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { os.Chdir(repoRoot) })

	initTestGitRepo(t, tmpDir)
	// Give the repo a remote so project_slug is populated (mirrors the
	// project-init E2E test).
	runTestGitCommand(t, tmpDir, "remote", "add", "origin", "https://github.com/testorg/ror-demo.git")

	// Baseline: no working-tree changes yet.
	assert.Empty(t, gitStatus(t, tmpDir), "expected clean tree before activation")

	// 3. Activate asimi headlessly against the ror-project with the real LLM,
	// turning on ATIF trajectory recording (--atif), handsoff auto-approval,
	// and isolated-host (terminal-bench runs in an already-isolated
	// environment, so no podman sandbox or approval gates).
	// The subprocess inherits the caller's environment verbatim, so whatever
	// model/provider the user has configured (config file, ASIMI_MODEL,
	// ASIMI_PROVIDER, or the provider API key) is exercised — we do not force
	// a model here, keeping this a true readiness check.
	prompt := "add a feature: list all posts with titles"
	cmd := exec.Command(binary, "--handsoff", "--atif", "--isolated-host", "-p", prompt)
	cmd.Dir = tmpDir
	cmd.Env = os.Environ()
	cmdOutput := &lockBuffer{}
	cmd.Stdout = cmdOutput
	cmd.Stderr = cmdOutput

	runCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	err = cmd.Run()
	if ctxErr := runCtx.Err(); ctxErr == context.DeadlineExceeded {
		t.Fatalf("asimi activation timed out\nOutput:\n%s", cmdOutput.String())
	}
	require.NoError(t, err, "asimi activation failed\nOutput:\n%s", cmdOutput.String())

	// 4. Assert the activation made changes to the project.
	status := gitStatus(t, tmpDir)
	require.NotEmpty(t, status, "asimi should have made code changes\nOutput:\n%s", cmdOutput.String())
	t.Logf("changes made by asimi:\n%s", status)

	// 5. Assert the ATIF trajectory was fully recorded. With --atif the agent
	// name is "asimi", so the trajectory lives at agent/asimi.txt and must be
	// non-empty with only valid JSONL events.
	atifFile := filepath.Join(tmpDir, "agent", "asimi.txt")
	data, atifErr := os.ReadFile(atifFile)
	require.NoError(t, atifErr, "expected ATIF file at agent/asimi.txt")
	require.NotEmpty(t, strings.TrimSpace(string(data)), "ATIF file should be fully populated (non-empty)")
	lineCount := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		lineCount++
		require.True(t, json.Valid([]byte(line)), "ATIF line %d is not valid JSON: %s", lineCount, line)
	}
	t.Logf("ATIF file %s fully populated with %d JSONL events", atifFile, lineCount)
}

// lockBuffer is a minimal concurrency-safe buffer for capturing a command's
// stdout/stderr so diagnostics can be printed on test failure.
type lockBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// gitStatus returns the porcelain status for a directory (non-empty when the
// working tree has uncommitted changes).
func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git status failed: %s", out)
	return strings.TrimSpace(string(out))
}

// fakeSealCourt is a handsoffSealCourt seam for driving the seal pass without
// a live court, git repo, or ritual machinery.
type fakeSealCourt struct {
	mu      sync.Mutex
	active  []storage.ActiveEdict
	err     error
	granted []uint
	onGrant func(id uint)
}

func (f *fakeSealCourt) ListActiveEdicts() ([]storage.ActiveEdict, error) {
	return f.active, f.err
}

func (f *fakeSealCourt) GrantRulerSeal(edictID uint, notes string) error {
	f.mu.Lock()
	f.granted = append(f.granted, edictID)
	hook := f.onGrant
	f.mu.Unlock()
	if hook != nil {
		hook(edictID)
	}
	return nil
}

func (f *fakeSealCourt) grantedIDs() []uint {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint(nil), f.granted...)
}

// TestHeadlessSink_SealPassSelectsPendingAscension verifies the handsoff sweep
// seals exactly the edicts carrying both the Judge's and the Chancellor's
// seals (and never the reserved edict 1 or system edict 0).
func TestHeadlessSink_SealPassSelectsPendingAscension(t *testing.T) {
	fc := &fakeSealCourt{active: []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 11}, HasJudgeSeal: true, HasChancellorSeal: true},
		{Edict: storage.Edict{ID: 12}, HasJudgeSeal: true},
		{Edict: storage.Edict{ID: 1}, HasJudgeSeal: true, HasChancellorSeal: true},
	}}
	sink := newHeadlessSink(nil)
	sink.handsoff = true
	sink.sealCourt = fc

	sealed, err := sink.sealableEdicts()
	require.NoError(t, err)
	assert.Equal(t, []uint{11}, sealed, "only judge+chancellor rows are sealable")
	assert.Equal(t, []uint{11}, fc.grantedIDs(), "exactly the pending-ascension row is sealed")
}

// TestHeadlessSink_SealPassWaitsForLanding verifies that a completed ritual in
// handsoff does not finish while a sealed edict awaits its landing, and that
// edict_sealed releases it with exit 0.
func TestHeadlessSink_SealPassWaitsForLanding(t *testing.T) {
	fc := &fakeSealCourt{active: []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 11}, HasJudgeSeal: true, HasChancellorSeal: true},
	}}
	sink := newHeadlessSink(nil)
	sink.handsoff = true
	sink.sealCourt = fc
	sink.landingTimeout = time.Minute

	sink.handleEvent(court.EventNotificationMsg{
		EventType: storage.EventRitualCompleted,
		EdictKey:  storage.EdictKey{ID: 11},
	})

	select {
	case code := <-sink.done:
		t.Fatalf("must not finish while a sealed edict awaits landing (got %d)", code)
	default:
	}

	sink.handleEvent(court.EventNotificationMsg{
		EventType: storage.EventEdictSealed,
		EdictKey:  storage.EdictKey{ID: 11},
	})

	select {
	case code := <-sink.done:
		assert.Equal(t, 0, code)
	default:
		t.Fatal("expected finish(0) after the landing lands")
	}
}

// TestHeadlessSink_SealPassMergeFailedExitsOne verifies a terminal landing
// failure is surfaced and the run exits 1.
func TestHeadlessSink_SealPassMergeFailedExitsOne(t *testing.T) {
	fc := &fakeSealCourt{active: []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 11}, HasJudgeSeal: true, HasChancellorSeal: true},
	}}
	sink := newHeadlessSink(nil)
	sink.handsoff = true
	sink.sealCourt = fc
	sink.landingTimeout = time.Minute

	sink.handleEvent(court.EventNotificationMsg{
		EventType: storage.EventRitualCompleted,
		EdictKey:  storage.EdictKey{ID: 11},
	})
	sink.handleEvent(court.EventNotificationMsg{
		EventType: storage.EventWorktreeMergeFailed,
		EdictKey:  storage.EdictKey{ID: 11},
		Payload:   map[string]interface{}{"reason": "rebase conflict", "merged": false},
	})

	select {
	case code := <-sink.done:
		assert.Equal(t, 1, code)
	default:
		t.Fatal("expected finish(1) after a terminal landing failure")
	}
}

// TestHeadlessSink_SealPassNonHandsoffUnchanged verifies handsoff=false keeps
// the historical semantics: ritual completion finishes 0 and no seals are granted.
func TestHeadlessSink_SealPassNonHandsoffUnchanged(t *testing.T) {
	fc := &fakeSealCourt{active: []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 11}, HasJudgeSeal: true, HasChancellorSeal: true},
	}}
	sink := newHeadlessSink(nil)
	sink.handsoff = false
	sink.sealCourt = fc

	sink.handleEvent(court.EventNotificationMsg{
		EventType: storage.EventRitualCompleted,
		EdictKey:  storage.EdictKey{ID: 11},
	})

	select {
	case code := <-sink.done:
		assert.Equal(t, 0, code)
	default:
		t.Fatal("non-handsoff ritual completion must finish as before")
	}
	assert.Empty(t, fc.grantedIDs(), "non-handsoff must grant no seals")
}

// TestHeadlessSink_SealPassEarlyTerminalEvent verifies an edict_sealed that
// races ahead of the seal bookkeeping is not dropped (no hang).
func TestHeadlessSink_SealPassEarlyTerminalEvent(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.handsoff = true
	sink.landingTimeout = time.Minute
	fc := &fakeSealCourt{
		active: []storage.ActiveEdict{
			{Edict: storage.Edict{ID: 11}, HasJudgeSeal: true, HasChancellorSeal: true},
		},
	}
	// The landing event arrives (on another goroutine in production) while the
	// seal is being granted.
	fc.onGrant = func(id uint) {
		sink.handleEvent(court.EventNotificationMsg{
			EventType: storage.EventEdictSealed,
			EdictKey:  storage.EdictKey{ID: id},
		})
	}
	sink.sealCourt = fc

	sink.handleEvent(court.EventNotificationMsg{
		EventType: storage.EventRitualCompleted,
		EdictKey:  storage.EdictKey{ID: 11},
	})

	select {
	case code := <-sink.done:
		assert.Equal(t, 0, code)
	default:
		t.Fatal("expected finish(0): an early landing event must not be dropped")
	}
}

// TestHeadlessSink_RecordLandingIgnoresUnknownAndRepeats verifies idempotence:
// events for unsealed edicts are ignored and a second terminal event cannot
// downgrade an already-sent 0.
func TestHeadlessSink_RecordLandingIgnoresUnknownAndRepeats(t *testing.T) {
	sink := newHeadlessSink(nil)
	sink.mu.Lock()
	sink.handsoffSealed[11] = true
	sink.mu.Unlock()

	sink.recordLanding(99, true)
	select {
	case code := <-sink.done:
		t.Fatalf("unknown landing event must be ignored (got %d)", code)
	default:
	}

	sink.recordLanding(11, true)
	assert.Equal(t, 0, <-sink.done, "first terminal event sends 0")

	sink.recordLanding(11, false)
	select {
	case code := <-sink.done:
		t.Fatalf("a repeat terminal event must not send a second code (got %d)", code)
	default:
	}
}

// TestHeadlessSink_SealPassTimeout verifies an unlanded seal pass is bounded
// and exits non-zero instead of hanging.
func TestHeadlessSink_SealPassTimeout(t *testing.T) {
	fc := &fakeSealCourt{active: []storage.ActiveEdict{
		{Edict: storage.Edict{ID: 11}, HasJudgeSeal: true, HasChancellorSeal: true},
	}}
	sink := newHeadlessSink(nil)
	sink.handsoff = true
	sink.sealCourt = fc
	sink.landingTimeout = 20 * time.Millisecond

	sink.handleEvent(court.EventNotificationMsg{
		EventType: storage.EventRitualCompleted,
		EdictKey:  storage.EdictKey{ID: 11},
	})

	select {
	case code := <-sink.done:
		assert.Equal(t, 1, code, "a stalled landing must exit non-zero")
	case <-time.After(3 * time.Second):
		t.Fatal("seal pass did not honor its landing timeout")
	}
}

// TestHeadlessSink_MergeFailedIgnoresUnsealedEdict verifies a
// worktree_merge_failed event neither prints nor affects the exit code when it
// is not for an edict the seal pass sealed — the non-handsoff run and events
// for other flows must stay byte-for-byte unchanged.
func TestHeadlessSink_MergeFailedIgnoresUnsealedEdict(t *testing.T) {
	for _, handsoff := range []bool{false, true} {
		sink := newHeadlessSink(nil)
		sink.handsoff = handsoff

		sink.handleEvent(court.EventNotificationMsg{
			EventType: storage.EventWorktreeMergeFailed,
			EdictKey:  storage.EdictKey{ID: 11},
			Payload:   map[string]interface{}{"reason": "rebase conflict", "merged": false},
		})

		select {
		case code := <-sink.done:
			t.Fatalf("handsoff=%v: an unsealed merge failure must not finish (got %d)", handsoff, code)
		default:
		}
	}
}
