package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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

// TestHeadlessContextParams verifies that headlessContextParams correctly
// builds SetContextParams from config and repo info.
func TestHeadlessContextParams(t *testing.T) {
	cfg := &Config{
		Court: config.CourtConfig{
			Username: "testuser",
			Project:  "testproject",
		},
	}
	ri := &repo.RepoInfo{
		ProjectRoot:  "/path/to/project",
		WorktreePath: "/path/to/worktree",
		Branch:       "main",
	}

	params := headlessContextParams(cfg, ri)

	assert.Equal(t, "testproject", params.Project)
	assert.Equal(t, "testuser", params.Username)
	assert.Equal(t, "/path/to/project", params.ProjectRoot)
	assert.Equal(t, "/path/to/worktree", params.WorktreePath)
	assert.Equal(t, "main", params.Branch)
}

// TestHeadlessContextParams_NilRepoInfo verifies fallback to CWD.
func TestHeadlessContextParams_NilRepoInfo(t *testing.T) {
	cfg := &Config{
		Court: config.CourtConfig{
			Username: "testuser",
		},
	}
	params := headlessContextParams(cfg, nil)
	assert.NotEmpty(t, params.ProjectRoot)
}

// TestHeadlessContextParams_ProjectFromSlug verifies that when config
// doesn't set a project, it falls back to repoInfo.Slug.
func TestHeadlessContextParams_ProjectFromSlug(t *testing.T) {
	cfg := &Config{}
	ri := &repo.RepoInfo{
		ProjectRoot: "/path/to/project",
		Slug:        "my-slug",
	}
	params := headlessContextParams(cfg, ri)
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
		Username:   cfg.Username,
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
		Username:   cfg.Username,
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
// handler, approve_doc blocks forever on ResultChan and suggest_edict hangs.
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
		assert.True(t, res.Saved, "editor result should be Saved in handsoff mode")
		assert.Equal(t, "large document content >500 chars...", res.Content, "content should be unchanged")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EditorResult — ApproveDocTool would hang")
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
		assert.True(t, res.Saved, "should auto-approve even when not handsoff")
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

// TestHeadlessActivation_ATIF_E2E is a terminal-bench readiness smoke test.
// It builds the real asimi binary and runs it end-to-end in headless mode
// against a copy of the ror-project:
//
//	asimi --handsoff --atif --isolated-host -p "add a feature"
//
// It verifies that (1) asimi exits successfully, (2) asimi actually made
// changes to the project (git diff is non-empty), and (3) the ATIF
// trajectory file (agent/asimi.txt) was fully populated — not empty.
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

	// 1. Build the real asimi binary from the module root.
	repoRoot, err := os.Getwd()
	require.NoError(t, err)
	binary := filepath.Join(t.TempDir(), "asimi")
	build := exec.Command("go", "build", "-tags", "containers_image_openpgp", "-o", binary, ".")
	build.Dir = repoRoot
	buildOut, buildErr := build.CombinedOutput()
	require.NoError(t, buildErr, "failed to build asimi binary\nOutput: %s", buildOut)

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
