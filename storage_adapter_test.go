package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestSessionStore_RoundTrip verifies that messages survive a save/load cycle
func TestSessionStore_RoundTrip(t *testing.T) {
	// Create temp DB
	tmpDir, err := os.MkdirTemp("", "adapter_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.InitDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	repoInfo := repo.RepoInfo{
		ProjectRoot: tmpDir,
		Branch:      "main",
	}

	store, err := NewSessionStore(db, repoInfo, 100, 30)
	require.NoError(t, err)
	defer store.Close()

	// Create session with various message types
	now := time.Now()
	session := &court.Session{
		ID:          "test-roundtrip-1",
		CreatedAt:   now,
		LastUpdated: now,
		FirstPrompt: "What is the meaning of life?",
		Provider:    "openai",
		Model:       "gpt-4",
		WorkingDir:  tmpDir,
		TabType:     "chancellor",
	}

	// Add diverse message types
	messages := []schemas.ChatMessage{
		textMessage(schemas.ChatMessageRoleUser, "What is the meaning of life?"),
		textMessage(schemas.ChatMessageRoleAssistant, "42"),
		textMessage(schemas.ChatMessageRoleUser, "Analyze this file: /tmp/data.csv"),
		textMessage(schemas.ChatMessageRoleTool, `{"result": "file created at /tmp/test.txt"}`),
		textMessage(schemas.ChatMessageRoleUser, "Now read the file"),
	}
	session.SetMessages(messages)

	// Save session
	err = store.SaveSessionSync(session)
	require.NoError(t, err)

	// Load session
	loaded, err := store.LoadSession(session.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	// Verify messages round-trip correctly
	loadedMessages := loaded.GetMessages()
	require.Equal(t, len(messages), len(loadedMessages), "message count should match")

	for i, expected := range messages {
		actual := loadedMessages[i]
		require.Equal(t, expected.Role, actual.Role, "message %d role should match", i)
		require.NotNil(t, actual.Content, "message %d content should not be nil", i)
		require.NotNil(t, actual.Content.ContentStr, "message %d content string should not be nil", i)
		require.Equal(t, *expected.Content.ContentStr, *actual.Content.ContentStr, "message %d text should match", i)
	}

	// Verify metadata
	require.Equal(t, session.ID, loaded.ID)
	require.Equal(t, session.FirstPrompt, loaded.FirstPrompt)
	require.Equal(t, session.Provider, loaded.Provider)
	require.Equal(t, session.Model, loaded.Model)
}

// TestSessionStore_RoundTrip_EmptyMessages verifies sessions with no messages can be saved
func TestSessionStore_RoundTrip_EmptyMessages(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "adapter_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.InitDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	repoInfo := repo.RepoInfo{
		ProjectRoot: tmpDir,
		Branch:      "main",
	}

	store, err := NewSessionStore(db, repoInfo, 100, 30)
	require.NoError(t, err)
	defer store.Close()

	// Create session with no user/AI messages (should be skipped on save)
	session := &court.Session{
		ID:          "test-empty-1",
		FirstPrompt: "empty session",
		Provider:    "openai",
		Model:       "gpt-4",
		WorkingDir:  tmpDir,
	}
	// No messages - SaveSessionSync should skip
	err = store.SaveSessionSync(session)
	require.NoError(t, err, "empty session should not error on save")

	// Session should not exist (empty sessions are skipped)
	loaded, err := store.LoadSession(session.ID)
	require.Error(t, err, "should get error for non-existent session") // Returns error for non-existent session
	require.Nil(t, loaded)
}

// TestSessionStore_RoundTrip_SystemMessages verifies system messages alone also skip save
func TestSessionStore_RoundTrip_SystemMessages(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "adapter_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.InitDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	repoInfo := repo.RepoInfo{
		ProjectRoot: tmpDir,
		Branch:      "main",
	}

	store, err := NewSessionStore(db, repoInfo, 100, 30)
	require.NoError(t, err)
	defer store.Close()

	session := &court.Session{
		ID:          "test-system-only",
		FirstPrompt: "system message only",
		Provider:    "openai",
		Model:       "gpt-4",
		WorkingDir:  tmpDir,
	}
	session.SetMessages([]schemas.ChatMessage{
		textMessage(schemas.ChatMessageRoleSystem, "You are helpful"),
	})

	// Save should skip since no human/AI messages
	err = store.SaveSessionSync(session)
	require.NoError(t, err)
}

// TestSessionStore_ListSessions verifies list sessions work without messages
func TestSessionStore_ListSessions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "adapter_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.InitDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	repoInfo := repo.RepoInfo{
		ProjectRoot: tmpDir,
		Branch:      "main",
	}

	store, err := NewSessionStore(db, repoInfo, 100, 30)
	require.NoError(t, err)
	defer store.Close()

	// Save a session
	session := &court.Session{
		ID:          "test-list-1",
		FirstPrompt: "List test",
		Provider:    "openai",
		Model:       "gpt-4",
		WorkingDir:  tmpDir,
	}
	messages := []schemas.ChatMessage{
		textMessage(schemas.ChatMessageRoleUser, "Hello"),
		textMessage(schemas.ChatMessageRoleAssistant, "Hi there!"),
	}
	session.SetMessages(messages)

	err = store.SaveSessionSync(session)
	require.NoError(t, err)

	// List sessions
	sessions, err := store.ListSessions(10, "")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(sessions), 1, "should have at least one session")

	// Find our session
	var found *court.Session
	for i := range sessions {
		if sessions[i].ID == session.ID {
			found = &sessions[i]
			break
		}
	}
	require.NotNil(t, found, "session should be in list")
	require.Equal(t, session.FirstPrompt, found.FirstPrompt)
	require.Equal(t, 2, found.MessageCount, "list view should have message count")
}

// TestIsSQLiteBusy verifies that isSQLiteBusy correctly identifies SQLITE_BUSY errors
func TestIsSQLiteBusy(t *testing.T) {
	// Test with nil
	require.False(t, isSQLiteBusy(nil))

	// Test with a non-SQLite error
	require.False(t, isSQLiteBusy(fmt.Errorf("some other error")))

	// Test with a SQLITE_BUSY error from the modernc.org/sqlite driver
	busyErr := fmt.Errorf("The database file is locked (SQLITE_BUSY) (5) (SQLITE_BUSY)")
	require.True(t, isSQLiteBusy(busyErr))

	// Test with a SQLITE_LOCKED error string
	lockedErr := fmt.Errorf("A table in the database is locked (SQLITE_LOCKED) (6)")
	require.True(t, isSQLiteBusy(lockedErr))

	// Test with a wrapped error containing SQLITE_BUSY
	wrappedErr := fmt.Errorf("failed to save: %w", busyErr)
	require.True(t, isSQLiteBusy(wrappedErr))
}

// TestSessionStore_IncrementalUpsert_PreservesHistory verifies that when a
// session is saved multiple times (as happens during a conversation), the
// incremental upsert strategy correctly preserves all messages and only
// adds new ones.
func TestSessionStore_IncrementalUpsert_PreservesHistory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "incremental_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.InitDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	repoInfo := repo.RepoInfo{
		ProjectRoot: tmpDir,
		Branch:      "main",
	}

	store, err := NewSessionStore(db, repoInfo, 100, 30)
	require.NoError(t, err)
	defer store.Close()

	session := &court.Session{
		ID:          "test-incremental-adapter",
		FirstPrompt: "Hello",
		Provider:    "openai",
		Model:       "gpt-4",
		WorkingDir:  tmpDir,
		TabType:     "chancellor",
	}

	// First save: 2 messages
	session.SetMessages([]schemas.ChatMessage{
		textMessage(schemas.ChatMessageRoleUser, "Hello"),
		textMessage(schemas.ChatMessageRoleAssistant, "Hi there!"),
	})
	err = store.SaveSessionSync(session)
	require.NoError(t, err)
	require.Equal(t, 2, session.PersistedMsgCount)

	// Load and verify
	loaded, err := store.LoadSession(session.ID)
	require.NoError(t, err)
	require.Equal(t, 2, len(loaded.GetMessages()))
	require.Equal(t, 2, loaded.PersistedMsgCount)

	// Second save: add 2 more messages (4 total)
	session.SetMessages([]schemas.ChatMessage{
		textMessage(schemas.ChatMessageRoleUser, "Hello"),
		textMessage(schemas.ChatMessageRoleAssistant, "Hi there!"),
		textMessage(schemas.ChatMessageRoleUser, "How are you?"),
		textMessage(schemas.ChatMessageRoleAssistant, "I'm doing well!"),
	})
	err = store.SaveSessionSync(session)
	require.NoError(t, err)
	require.Equal(t, 4, session.PersistedMsgCount)

	// Load and verify all 4 messages are present
	loaded, err = store.LoadSession(session.ID)
	require.NoError(t, err)
	require.Equal(t, 4, len(loaded.GetMessages()), "all 4 messages should be present")
	require.Equal(t, 4, loaded.PersistedMsgCount)

	// Verify message contents
	msgs := loaded.GetMessages()
	require.Equal(t, "Hello", *msgs[0].Content.ContentStr)
	require.Equal(t, "Hi there!", *msgs[1].Content.ContentStr)
	require.Equal(t, "How are you?", *msgs[2].Content.ContentStr)
	require.Equal(t, "I'm doing well!", *msgs[3].Content.ContentStr)
}

// TestSaveWithRetry_EventuallySucceeds verifies that saveWithRetry retries
// on SQLITE_BUSY errors and eventually succeeds when the lock is released.
func TestSaveWithRetry_EventuallySucceeds(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "retry_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.InitDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	repoInfo := repo.RepoInfo{
		ProjectRoot: tmpDir,
		Branch:      "main",
	}

	store, err := NewSessionStore(db, repoInfo, 100, 30)
	require.NoError(t, err)
	defer store.Close()

	session := &court.Session{
		ID:          "test-retry",
		FirstPrompt: "retry test",
		Provider:    "openai",
		Model:       "gpt-4",
		WorkingDir:  tmpDir,
		TabType:     "chancellor",
	}
	session.SetMessages([]schemas.ChatMessage{
		textMessage(schemas.ChatMessageRoleUser, "Hello"),
		textMessage(schemas.ChatMessageRoleAssistant, "World"),
	})

	// Simulate a transient SQLITE_BUSY: fail the first 2 attempts, succeed on the 3rd.
	var callCount int
	store.saveHook = func(s *court.Session) error {
		callCount++
		if callCount < 3 {
			return fmt.Errorf("The database file is locked (SQLITE_BUSY) (5) (SQLITE_BUSY)")
		}
		// Delegate to the real save on the 3rd attempt
		return store.saveSessionSync(s)
	}

	store.saveWithRetry(session)
	require.Equal(t, 3, callCount, "should have retried twice before succeeding")

	// Verify the session was actually saved
	loaded, err := store.LoadSession(session.ID)
	require.NoError(t, err)
	require.Equal(t, 2, len(loaded.GetMessages()))
}

// TestSaveWithRetry_ExhaustsRetries verifies that saveWithRetry gives up
// after all retries are exhausted when SQLITE_BUSY persists.
func TestSaveWithRetry_ExhaustsRetries(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "retry_exhaust_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.InitDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	repoInfo := repo.RepoInfo{
		ProjectRoot: tmpDir,
		Branch:      "main",
	}

	store, err := NewSessionStore(db, repoInfo, 100, 30)
	require.NoError(t, err)
	defer store.Close()

	session := &court.Session{
		ID:          "test-retry-exhaust",
		FirstPrompt: "retry exhaust test",
		Provider:    "openai",
		Model:       "gpt-4",
		WorkingDir:  tmpDir,
		TabType:     "chancellor",
	}
	session.SetMessages([]schemas.ChatMessage{
		textMessage(schemas.ChatMessageRoleUser, "Hello"),
	})

	// Always fail with SQLITE_BUSY
	var callCount int
	store.saveHook = func(s *court.Session) error {
		callCount++
		return fmt.Errorf("The database file is locked (SQLITE_BUSY) (5) (SQLITE_BUSY)")
	}

	store.saveWithRetry(session)
	// 1 initial attempt + 3 retries = 4 total
	require.Equal(t, 4, callCount, "should have attempted 4 times (1 + 3 retries)")
}

// TestSaveWithRetry_NonBusyErrorNoRetry verifies that non-BUSY errors
// don't trigger retries.
func TestSaveWithRetry_NonBusyErrorNoRetry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "retry_nonbusy_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.InitDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	repoInfo := repo.RepoInfo{
		ProjectRoot: tmpDir,
		Branch:      "main",
	}

	store, err := NewSessionStore(db, repoInfo, 100, 30)
	require.NoError(t, err)
	defer store.Close()

	session := &court.Session{
		ID:          "test-retry-nonbusy",
		FirstPrompt: "non-busy test",
		Provider:    "openai",
		Model:       "gpt-4",
		WorkingDir:  tmpDir,
		TabType:     "chancellor",
	}
	session.SetMessages([]schemas.ChatMessage{
		textMessage(schemas.ChatMessageRoleUser, "Hello"),
	})

	// Fail with a non-BUSY error
	var callCount int
	store.saveHook = func(s *court.Session) error {
		callCount++
		return fmt.Errorf("disk I/O error")
	}

	store.saveWithRetry(session)
	require.Equal(t, 1, callCount, "non-BUSY error should not trigger retries")
}

// TestSessionStore_CleanupProjectScopedAndZeroDisables verifies that the root
// SessionStore adapter scopes CleanupOldSessions to the current project across
// all of its branches without deleting sessions from other projects, and that
// maxSessions=0 / maxAgeDays=0 disables the corresponding pruning rules.
func TestSessionStore_CleanupProjectScopedAndZeroDisables(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.InitDB(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer db.Close()

	projA := filepath.Join(tmpDir, "proj-a")
	projB := filepath.Join(tmpDir, "proj-b")
	require.NoError(t, os.MkdirAll(projA, 0o755))
	require.NoError(t, os.MkdirAll(projB, 0o755))

	runProviderGit(t, projA, "init")
	runProviderGit(t, projA, "remote", "add", "origin", "https://github.com/acme/project-a.git")
	runProviderGit(t, projB, "init")
	runProviderGit(t, projB, "remote", "add", "origin", "https://github.com/acme/project-b.git")

	rawStore := storage.NewSessionStore(db, &storage.SessionConfig{Enabled: true})
	for _, s := range []struct {
		id, project, branch string
		ageDays             int
		offsetSec           int64
	}{
		{"a-main-old", "project-a", "main", 45, 0},
		{"a-feat-mid", "project-a", "feature", 0, -20},
		{"a-main-new", "project-a", "main", 0, -5},
		{"b-main-old", "project-b", "main", 45, 0},
		{"b-main-new", "project-b", "main", 0, -5},
	} {
		require.NoError(t, rawStore.SaveSession(&storage.SessionData{
			ID:          s.id,
			FirstPrompt: "p",
			Model:       "m",
			Provider:    "p",
			WorkingDir:  tmpDir,
			TabType:     "ruling",
		}, "github.com", "acme", s.project, s.branch))
		ts := time.Now().AddDate(0, 0, -s.ageDays).Unix() + s.offsetSec
		_, err := db.Conn().Exec("UPDATE sessions SET last_updated = ? WHERE id = ?", ts, s.id)
		require.NoError(t, err)
	}

	// Opening with (0, 0) disables both count and age pruning.
	storeZero, err := NewSessionStore(db, repo.RepoInfo{ProjectRoot: projA, Branch: "main"}, 0, 0)
	require.NoError(t, err)
	storeZero.Close()

	aSessions, err := rawStore.ListAllSessions(0, "github.com", "acme", "project-a")
	require.NoError(t, err)
	require.Len(t, aSessions, 3)

	// Opening with (1, 30) prunes project-a across both branches down to 1 newest session,
	// leaving project-b (including its 45-day-old session) completely untouched.
	storePrune, err := NewSessionStore(db, repo.RepoInfo{ProjectRoot: projA, Branch: "main"}, 1, 30)
	require.NoError(t, err)
	storePrune.Close()

	aSessions, err = rawStore.ListAllSessions(0, "github.com", "acme", "project-a")
	require.NoError(t, err)
	require.Len(t, aSessions, 1)
	require.Equal(t, "a-main-new", aSessions[0].ID)

	bSessions, err := rawStore.ListAllSessions(0, "github.com", "acme", "project-b")
	require.NoError(t, err)
	require.Len(t, bSessions, 2)
}
