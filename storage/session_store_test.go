package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// DEPRECATED: TestClearSession tests removed - ClearSession function was removed
// to preserve user history for :resume functionality.

// TestListSessions_TabTypeFilter tests filtering sessions by tabType
func TestListSessions_TabTypeFilter(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "session_test_*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	require.NoError(t, err)
	defer db.Close()

	store := NewSessionStore(db, &SessionConfig{Enabled: true})

	// Create sessions with different tab types
	sessionSpecs := []struct {
		id      string
		tabType string
	}{
		{"ruling-1", "ruling"},
		{"ruling-2", "ruling"},
		{"hunting-1", "hunting"},
		{"sandbox-1", "sandbox"},
	}

	for _, spec := range sessionSpecs {
		session := &SessionData{
			ID:          spec.id,
			FirstPrompt: "test prompt",
			Model:       "test-model",
			Provider:    "test-provider",
			WorkingDir:  "/tmp",
			TabType:     spec.tabType,
		}
		err := store.SaveSession(session, "github.com", "testuser", "testrepo", "main")
		require.NoError(t, err)
	}

	// Filter by "ruling" tabType — returns only ruling sessions
	rulingSessions, err := store.ListSessions("github.com", "testuser", "testrepo", "main", "ruling", 0)
	require.NoError(t, err)
	require.Len(t, rulingSessions, 2)
	for _, s := range rulingSessions {
		require.Equal(t, "ruling", s.TabType)
	}

	// Filter by "hunting" tabType — returns only hunting session
	huntingSessions, err := store.ListSessions("github.com", "testuser", "testrepo", "main", "hunting", 0)
	require.NoError(t, err)
	require.Len(t, huntingSessions, 1)
	require.Equal(t, "hunting", huntingSessions[0].TabType)

	// Filter by non-existent tabType — returns empty
	emptySessions, err := store.ListSessions("github.com", "testuser", "testrepo", "main", "nonexistent", 0)
	require.NoError(t, err)
	require.Len(t, emptySessions, 0)

	// Empty tabType — returns all sessions
	allSessions, err := store.ListSessions("github.com", "testuser", "testrepo", "main", "", 0)
	require.NoError(t, err)
	require.Len(t, allSessions, 4)
}

// TestListSessions_TabTypeFilterWithLimit tests that limit works correctly with tabType filter
func TestListSessions_TabTypeFilterWithLimit(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "session_test_*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	require.NoError(t, err)
	defer db.Close()

	store := NewSessionStore(db, &SessionConfig{Enabled: true})

	// Create multiple sessions with the same tab type
	for i := 0; i < 5; i++ {
		session := &SessionData{
			ID:          fmt.Sprintf("session-ruling-%d", i),
			FirstPrompt: "test prompt",
			Model:       "test-model",
			Provider:    "test-provider",
			WorkingDir:  "/tmp",
			TabType:     "ruling",
		}
		err := store.SaveSession(session, "github.com", "user", "repo", "main")
		require.NoError(t, err)
	}

	// Filter with limit
	sessions, err := store.ListSessions("github.com", "user", "repo", "main", "ruling", 3)
	require.NoError(t, err)
	require.Len(t, sessions, 3)
	for _, s := range sessions {
		require.Equal(t, "ruling", s.TabType)
	}
}

// TestListSessions_TabTypeFilterAcrossBranches tests tabType filter works correctly across branches
func TestListSessions_TabTypeFilterAcrossBranches(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "session_test_*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	require.NoError(t, err)
	defer db.Close()

	store := NewSessionStore(db, &SessionConfig{Enabled: true})

	// Create sessions on different branches with different tab types
	sessionSpecs := []struct {
		id      string
		tabType string
		branch  string
	}{
		{"ruling-main", "ruling", "main"},
		{"hunting-main", "hunting", "main"},
		{"ruling-feature", "ruling", "feature"},
		{"sandbox-feature", "sandbox", "feature"},
	}

	for _, spec := range sessionSpecs {
		session := &SessionData{
			ID:          spec.id,
			FirstPrompt: "test prompt",
			Model:       "test-model",
			Provider:    "test-provider",
			WorkingDir:  "/tmp",
			TabType:     spec.tabType,
		}
		err := store.SaveSession(session, "github.com", "user", "repo", spec.branch)
		require.NoError(t, err)
	}

	// ruling sessions on main branch only
	rulingMain, err := store.ListSessions("github.com", "user", "repo", "main", "ruling", 0)
	require.NoError(t, err)
	require.Len(t, rulingMain, 1)
	require.Equal(t, "ruling", rulingMain[0].TabType)

	// ruling sessions on feature branch only
	rulingFeature, err := store.ListSessions("github.com", "user", "repo", "feature", "ruling", 0)
	require.NoError(t, err)
	require.Len(t, rulingFeature, 1)
	require.Equal(t, "ruling", rulingFeature[0].TabType)

	// all sessions on feature branch
	allFeature, err := store.ListSessions("github.com", "user", "repo", "feature", "", 0)
	require.NoError(t, err)
	require.Len(t, allFeature, 2)
}

// TestListAllSessions_HostOrgProjectFilter tests that host/org/project filters work
func TestListAllSessions_HostOrgProjectFilter(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "session_test_*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	require.NoError(t, err)
	defer db.Close()

	store := NewSessionStore(db, &SessionConfig{Enabled: true})

	// Create sessions across two different repos
	sessionSpecs := []struct {
		id      string
		host    string
		org     string
		project string
	}{
		{"s1", "github.com", "acme", "alpha"},
		{"s2", "github.com", "acme", "beta"},
		{"s3", "github.com", "other", "alpha"},
		{"s4", "gitlab.com", "acme", "alpha"},
	}

	for _, spec := range sessionSpecs {
		session := &SessionData{
			ID:          spec.id,
			FirstPrompt: "test",
			Model:       "m",
			Provider:    "p",
			WorkingDir:  "/tmp",
			TabType:     "ruling",
		}
		err := store.SaveSession(session, spec.host, spec.org, spec.project, "main")
		require.NoError(t, err)
	}

	// No filters — all sessions
	all, err := store.ListAllSessions(0, "", "", "")
	require.NoError(t, err)
	require.Len(t, all, 4)

	// Filter by org
	acmeOnly, err := store.ListAllSessions(0, "", "acme", "")
	require.NoError(t, err)
	require.Len(t, acmeOnly, 3)
	for _, s := range acmeOnly {
		require.Contains(t, s.ProjectSlug, "acme")
	}

	// Filter by host
	ghOnly, err := store.ListAllSessions(0, "github.com", "", "")
	require.NoError(t, err)
	require.Len(t, ghOnly, 3)

	// Filter by project
	alphaOnly, err := store.ListAllSessions(0, "", "", "alpha")
	require.NoError(t, err)
	require.Len(t, alphaOnly, 3)
	for _, s := range alphaOnly {
		require.Contains(t, s.ProjectSlug, "alpha")
	}

	// Filter by host+org+project
	specific, err := store.ListAllSessions(0, "github.com", "acme", "alpha")
	require.NoError(t, err)
	require.Len(t, specific, 1)
	require.Equal(t, "github.com/acme/alpha", specific[0].ProjectSlug)

	// With limit
	limited, err := store.ListAllSessions(2, "", "", "")
	require.NoError(t, err)
	require.Len(t, limited, 2)
}

// TestSearchMessages_HostOrgProjectFilter tests that search can be scoped
func TestSearchMessages_HostOrgProjectFilter(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "session_test_*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	require.NoError(t, err)
	defer db.Close()

	store := NewSessionStore(db, &SessionConfig{Enabled: true})

	// Save sessions with messages in two different repos.
	// Messages are stored as JSON in the content column, so we need
	// to provide actual JSON message data for SearchMessages to find them.
	alphaMessages := json.RawMessage(`[{"role":"user","content":"hello from alpha"}]`)
	betaMessages := json.RawMessage(`[{"role":"user","content":"hello from beta"}]`)

	alphaSession := &SessionData{
		ID:          "search-alpha",
		FirstPrompt: "hello from alpha",
		Model:       "m",
		Provider:    "p",
		WorkingDir:  "/tmp",
		TabType:     "ruling",
		Messages:    alphaMessages,
	}
	err = store.SaveSession(alphaSession, "github.com", "acme", "alpha", "main")
	require.NoError(t, err)

	betaSession := &SessionData{
		ID:          "search-beta",
		FirstPrompt: "hello from beta",
		Model:       "m",
		Provider:    "p",
		WorkingDir:  "/tmp",
		TabType:     "ruling",
		Messages:    betaMessages,
	}
	err = store.SaveSession(betaSession, "github.com", "acme", "beta", "main")
	require.NoError(t, err)

	// Search across all repos
	allResults, err := store.SearchMessages("hello", 10, "", "", "")
	require.NoError(t, err)
	require.True(t, len(allResults) >= 2, "expected at least 2 results, got %d", len(allResults))

	// Scope to "alpha" project only
	alphaResults, err := store.SearchMessages("hello", 10, "", "", "alpha")
	require.NoError(t, err)
	require.Len(t, alphaResults, 1)
	require.Equal(t, "alpha", alphaResults[0].Project)

	// Scope by org
	acmeResults, err := store.SearchMessages("hello", 10, "", "acme", "")
	require.NoError(t, err)
	require.Len(t, acmeResults, 2)

	// Verify Host field is populated
	for _, r := range acmeResults {
		require.Equal(t, "github.com", r.Host)
	}
}

// TestCleanupOldSessions_BranchScoping tests that cleanup can be scoped to a branch
func TestCleanupOldSessions_BranchScoping(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "session_test_*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	require.NoError(t, err)
	defer db.Close()

	// Use a large MaxSessions so the constructor auto-cleanup doesn't delete anything
	cfg := &SessionConfig{Enabled: true, MaxSessions: 100}
	store := NewSessionStore(db, cfg)

	// Create 3 sessions on "main" and 3 on "feature"
	for i := 0; i < 3; i++ {
		session := &SessionData{
			ID:          fmt.Sprintf("main-s%d", i),
			FirstPrompt: fmt.Sprintf("main prompt %d", i),
			Model:       "m",
			Provider:    "p",
			WorkingDir:  "/tmp",
			TabType:     "ruling",
		}
		err := store.SaveSession(session, "github.com", "acme", "repo", "main")
		require.NoError(t, err)

		session2 := &SessionData{
			ID:          fmt.Sprintf("feature-s%d", i),
			FirstPrompt: fmt.Sprintf("feature prompt %d", i),
			Model:       "m",
			Provider:    "p",
			WorkingDir:  "/tmp",
			TabType:     "ruling",
		}
		err = store.SaveSession(session2, "github.com", "acme", "repo", "feature")
		require.NoError(t, err)
	}

	// Look up the branch_id for "main"
	var mainBranchID int64
	err = db.conn.QueryRow(
		"SELECT b.id FROM branches b JOIN repositories r ON b.repository_id = r.id WHERE r.host = ? AND r.org = ? AND r.project = ? AND b.name = ?",
		"github.com", "acme", "repo", "main",
	).Scan(&mainBranchID)
	require.NoError(t, err)

	// Now reduce MaxSessions and cleanup scoped to main branch
	cfg.MaxSessions = 2
	err = store.CleanupOldSessions(mainBranchID)
	require.NoError(t, err)

	// Verify main has 2 sessions
	mainSessions, err := store.ListSessions("github.com", "acme", "repo", "main", "", 0)
	require.NoError(t, err)
	require.Len(t, mainSessions, 2)

	// Verify feature is untouched (still 3)
	featureSessions, err := store.ListSessions("github.com", "acme", "repo", "feature", "", 0)
	require.NoError(t, err)
	require.Len(t, featureSessions, 3)

	// Cleanup with branchID=0 — global, keeps only 2 across all branches
	cfg.MaxSessions = 2
	err = store.CleanupOldSessions(0)
	require.NoError(t, err)

	allSessions, err := store.ListAllSessions(0, "", "", "")
	require.NoError(t, err)
	require.Len(t, allSessions, 2)
}

// TestInitDB_BusyTimeout verifies that the busy_timeout PRAGMA is set after InitDB
func TestInitDB_BusyTimeout(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "busy_timeout_test_*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	require.NoError(t, err)
	defer db.Close()

	var timeout int
	err = db.conn.QueryRow("PRAGMA busy_timeout").Scan(&timeout)
	require.NoError(t, err)
	require.Equal(t, 5000, timeout, "busy_timeout should be 5000ms")
}

// TestInitDB_TempDirFallback verifies InitDB's last-resort fallback to
// ${os.TempDir()}/asimi/asimi.sqlite when both the requested resolved path and
// the ${ASIMI_HOME}/asimi.sqlite location are unusable (edict 836 startup
// crash fix under Harbor --isolated-host read-only $HOME). Each bad candidate's
// parent is a regular file, so MkdirAll fails with ENOTDIR for any user (root
// included) and the tempdir candidate is the only viable path.
func TestInitDB_TempDirFallback(t *testing.T) {
	// unusable returns a DB path whose parent is a regular file: MkdirAll on
	// its directory always fails (ENOTDIR), for root and non-root alike.
	unusable := func() string {
		block := filepath.Join(t.TempDir(), "placeholder-file")
		require.NoError(t, os.WriteFile(block, []byte("x"), 0o644))
		return filepath.Join(block, "asimi.sqlite")
	}

	// Make ${ASIMI_HOME}/asimi.sqlite unwritable too, by pointing ASIMI_HOME
	// at a path inside a regular file. Then the tempdir is the only candidate.
	homeBlock := filepath.Join(t.TempDir(), "home-file")
	require.NoError(t, os.WriteFile(homeBlock, []byte("x"), 0o644))
	t.Setenv("ASIMI_HOME", filepath.Join(homeBlock, "home"))

	db, err := InitDB(unusable())
	require.NoError(t, err, "InitDB must fall back to the temp dir when every other candidate is unwritable")
	defer db.Close()

	expected := filepath.Join(os.TempDir(), "asimi", "asimi.sqlite")
	require.Equal(t, expected, db.Path(),
		"final fallback must be the tempdir path, got %q", db.Path())

	// The court DB must be usable (schema present).
	var version int
	require.NoError(t, db.conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version))
	require.GreaterOrEqual(t, version, 1)
}

// TestSaveSession_IncrementalUpsert verifies that incremental upsert only adds
// new messages and never deletes existing ones.
func TestSaveSession_IncrementalUpsert(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "incremental_test_*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	require.NoError(t, err)
	defer db.Close()

	store := NewSessionStore(db, &SessionConfig{Enabled: true})

	// Save a session with 3 messages
	msgs := json.RawMessage(`[{"role":"user","content":"msg1"},{"role":"assistant","content":"msg2"},{"role":"user","content":"msg3"}]`)
	session := &SessionData{
		ID:          "test-incremental",
		FirstPrompt: "test",
		Model:       "m",
		Provider:    "p",
		WorkingDir:  "/tmp",
		TabType:     "ruling",
		Messages:    msgs,
	}
	err = store.SaveSession(session, "github.com", "test", "repo", "main")
	require.NoError(t, err)

	// Verify 3 messages were saved
	var count int
	err = db.conn.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", session.ID).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 3, count)

	// Now save again with 5 messages (2 new ones). PersistedMsgCount should be 3.
	msgs5 := json.RawMessage(`[{"role":"user","content":"msg1"},{"role":"assistant","content":"msg2"},{"role":"user","content":"msg3"},{"role":"assistant","content":"msg4"},{"role":"user","content":"msg5"}]`)
	session.Messages = msgs5
	err = store.SaveSession(session, "github.com", "test", "repo", "main")
	require.NoError(t, err)

	// Verify 5 messages now (3 original + 2 new, no deletion)
	err = db.conn.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", session.ID).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 5, count, "should have 5 messages after incremental upsert")

	// Verify PersistedMsgCount was updated
	require.Equal(t, 5, session.PersistedMsgCount)
}

// TestSaveSession_FailedIntermediatePreservesState verifies that a failed
// save doesn't lose previously saved messages. We simulate this by saving
// with a reduced message list after a successful save — the existing
// messages should not be deleted.
func TestSaveSession_FailedIntermediatePreservesState(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "failed_intermediate_*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	require.NoError(t, err)
	defer db.Close()

	store := NewSessionStore(db, &SessionConfig{Enabled: true})

	// Save a session with 5 messages
	msgs5 := json.RawMessage(`[{"role":"user","content":"msg1"},{"role":"assistant","content":"msg2"},{"role":"user","content":"msg3"},{"role":"assistant","content":"msg4"},{"role":"user","content":"msg5"}]`)
	session := &SessionData{
		ID:          "test-failed-intermediate",
		FirstPrompt: "test",
		Model:       "m",
		Provider:    "p",
		WorkingDir:  "/tmp",
		TabType:     "ruling",
		Messages:    msgs5,
	}
	err = store.SaveSession(session, "github.com", "test", "repo", "main")
	require.NoError(t, err)
	require.Equal(t, 5, session.PersistedMsgCount)

	// Simulate a scenario where the in-memory history was reduced (e.g., context compaction)
	// and a save is attempted. With the old DELETE-then-INSERT, this would overwrite
	// the full history. With incremental upsert, the 5 original messages are preserved.
	msgs3 := json.RawMessage(`[{"role":"user","content":"msg1"},{"role":"assistant","content":"msg2"},{"role":"user","content":"msg3"}]`)
	session.Messages = msgs3
	// PersistedMsgCount is still 5, so no new messages to insert
	err = store.SaveSession(session, "github.com", "test", "repo", "main")
	require.NoError(t, err)

	// Verify all 5 original messages are still in the DB
	var count int
	err = db.conn.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", session.ID).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 5, count, "all 5 original messages should be preserved")

	// Verify the messages are correct by loading the session
	loaded, _, _, _, _, err := store.LoadSession(session.ID)
	require.NoError(t, err)

	var loadedMsgs []interface{}
	err = json.Unmarshal(loaded.Messages, &loadedMsgs)
	require.NoError(t, err)
	require.Len(t, loadedMsgs, 5)
}

// TestSchemaMigration_V3toV4 verifies that a v3 database is migrated to v4
// with the unique index on messages(session_id, sequence).
func TestSchemaMigration_V3toV4(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "migration_v4_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	// Create a fresh DB (which will be at SchemaVersion 4)
	db, err := InitDB(dbPath)
	require.NoError(t, err)

	// Verify schema version is 4
	version, err := db.getSchemaVersion()
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, version)
	require.Equal(t, SchemaVersion, version)

	// Verify the unique index exists
	var idxCount int
	err = db.conn.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_messages_session_seq'",
	).Scan(&idxCount)
	require.NoError(t, err)
	require.Equal(t, 1, idxCount, "unique index idx_messages_session_seq should exist")

	// Verify the old non-unique index was dropped
	var oldIdxCount int
	err = db.conn.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_messages_session'",
	).Scan(&oldIdxCount)
	require.NoError(t, err)
	require.Equal(t, 0, oldIdxCount, "old non-unique index idx_messages_session should not exist")

	db.Close()

	// Now test migration from a v3 database by manually downgrading
	dbPath2 := filepath.Join(tmpDir, "test_v3.db")
	db2, err := InitDB(dbPath2)
	require.NoError(t, err)

	// Downgrade to v3 by removing the v4 version record and dropping the unique index
	_, err = db2.conn.Exec("DELETE FROM schema_version WHERE version >= 4")
	require.NoError(t, err)
	_, err = db2.conn.Exec("DROP INDEX IF EXISTS idx_messages_session_seq")
	require.NoError(t, err)
	// Recreate the old non-unique index to simulate v3 state
	_, err = db2.conn.Exec("CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, sequence)")
	require.NoError(t, err)
	db2.Close()

	// Reopen — should trigger migration from v3 to current version
	db2, err = InitDB(dbPath2)
	require.NoError(t, err)
	defer db2.Close()

	version, err = db2.getSchemaVersion()
	require.NoError(t, err)
	require.Equal(t, SchemaVersion, version, "should have migrated to current schema version")

	// Verify unique index exists after migration
	err = db2.conn.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_messages_session_seq'",
	).Scan(&idxCount)
	require.NoError(t, err)
	require.Equal(t, 1, idxCount, "unique index should exist after migration")

	// Verify old index was dropped during migration
	err = db2.conn.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_messages_session'",
	).Scan(&oldIdxCount)
	require.NoError(t, err)
	require.Equal(t, 0, oldIdxCount, "old non-unique index should be dropped after migration")
}

// TestMigrateV4toV5_ShogunateToCourtEventRename verifies that the v4→v5
// migration renames stored event string values from the old
// "shogunate_started"/"shogunate_ready" to "court_started"/"court_ready"
// in both tian_events and tian_event_dlq tables.
func TestMigrateV4toV5_ShogunateToCourtEventRename(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "migration_v4v5_test.db")

	// Manually create a database at schema v4 with old shogunate event values
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer conn.Close()

	// Create the schema_version table at v4
	_, err = conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER)`)
	require.NoError(t, err)
	_, err = conn.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (4, unixepoch())`)
	require.NoError(t, err)

	// Create tian_events and tian_event_dlq with old event values
	_, err = conn.Exec(`CREATE TABLE tian_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_type TEXT NOT NULL,
		edict_id INTEGER,
		payload TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	require.NoError(t, err)
	_, err = conn.Exec(`CREATE TABLE tian_event_dlq (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_type TEXT NOT NULL,
		edict_id INTEGER,
		payload TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	require.NoError(t, err)

	// Insert old-format events
	_, err = conn.Exec(`INSERT INTO tian_events (event_type, edict_id, payload) VALUES
		('shogunate_started', 1, '{}'),
		('shogunate_ready', 1, '{}'),
		('court_started', 2, '{}'),
		('other_event', 3, '{}')`)
	require.NoError(t, err)
	_, err = conn.Exec(`INSERT INTO tian_event_dlq (event_type, edict_id, payload) VALUES
		('shogunate_started', 1, '{}'),
		('shogunate_ready', 1, '{}')`)
	require.NoError(t, err)

	conn.Close()

	// Now open with InitDB — this should detect v4 < v5 and run migration
	db, err := InitDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Verify tian_events: old values renamed, other values untouched
	rows, err := db.conn.Query(`SELECT event_type FROM tian_events WHERE event_type LIKE '%started%' OR event_type LIKE '%ready%' OR event_type = 'other_event' ORDER BY id`)
	require.NoError(t, err)
	var eventTypes []string
	for rows.Next() {
		var et string
		require.NoError(t, rows.Scan(&et))
		eventTypes = append(eventTypes, et)
	}
	rows.Close()
	require.Contains(t, eventTypes, "court_started", "shogunate_started should be renamed to court_started")
	require.Contains(t, eventTypes, "court_ready", "shogunate_ready should be renamed to court_ready")
	require.Contains(t, eventTypes, "other_event", "unrelated events should be untouched")
	for _, et := range eventTypes {
		require.NotContains(t, et, "shogunate", "no event should contain 'shogunate' after migration")
	}

	// Verify tian_event_dlq
	rows, err = db.conn.Query(`SELECT event_type FROM tian_event_dlq ORDER BY id`)
	require.NoError(t, err)
	var dlqTypes []string
	for rows.Next() {
		var et string
		require.NoError(t, rows.Scan(&et))
		dlqTypes = append(dlqTypes, et)
	}
	rows.Close()
	require.NotEmpty(t, dlqTypes)
	for _, et := range dlqTypes {
		require.NotContains(t, et, "shogunate", "no DLQ event should contain 'shogunate' after migration")
	}

	// Verify schema version is now 7 (v4→v5, v5→v6, v6→v7 all run)
	var version int
	err = db.conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	require.NoError(t, err)
	require.Equal(t, 7, version, "schema should be at version 7 after migration")
}

// TestMigrateV5toV6_SageToChancellorSealRename verifies that the v5→v6
// migration renames stale minister_id='sage' values to 'chancellor' in
// the seals table. This ensures existing users don't lose their seal
// chain after the 三省 alignment rename.
func TestMigrateV5toV6_SageToChancellorSealRename(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "migration_v5v6_test.db")

	// Manually create a database at schema v5 with old sage seals
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	_, err = conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER)`)
	require.NoError(t, err)
	_, err = conn.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (5, unixepoch())`)
	require.NoError(t, err)

	// Create a minimal seals table matching the GORM model
	_, err = conn.Exec(`CREATE TABLE seals (
		seal_id TEXT PRIMARY KEY,
		edict_id INTEGER NOT NULL,
		username TEXT NOT NULL DEFAULT '',
		project TEXT NOT NULL DEFAULT '',
		minister_id TEXT NOT NULL,
		sealed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		metadata TEXT
	)`)
	require.NoError(t, err)

	// Insert seals with old and new minister_ids
	_, err = conn.Exec(`INSERT INTO seals (seal_id, edict_id, minister_id, username, project) VALUES
		('s1', 1, 'judge',     'u', 'p'),
		('s2', 1, 'sage',      'u', 'p'),
		('s3', 2, 'judge',     'u', 'p'),
		('s4', 2, 'chancellor','u', 'p'),
		('s5', 3, 'ruler',     'u', 'p')`)
	require.NoError(t, err)

	conn.Close()

	// Open with InitDB — should detect v5 < v6 and run migration
	db, err := InitDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Verify: all 'sage' seals should now be 'chancellor'
	rows, err := db.Conn().Query(`SELECT minister_id FROM seals ORDER BY seal_id`)
	require.NoError(t, err)
	var ministerIDs []string
	for rows.Next() {
		var mid string
		require.NoError(t, rows.Scan(&mid))
		ministerIDs = append(ministerIDs, mid)
	}
	rows.Close()

	require.Equal(t, []string{"judge", "chancellor", "judge", "chancellor", "ruler"}, ministerIDs,
		"sage seals should be renamed to chancellor; others untouched")

	// No seals should have minister_id='sage' anymore
	var sageCount int
	err = db.Conn().QueryRow("SELECT COUNT(*) FROM seals WHERE minister_id = 'sage'").Scan(&sageCount)
	require.NoError(t, err)
	require.Equal(t, 0, sageCount, "no seals should have minister_id='sage' after migration")

	// Verify schema version is now 6
	var version int
	err = db.conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	require.NoError(t, err)
	require.Equal(t, 7, version, "schema should be at version 7 after migration")
}

// TestMigrateV5toV6_SealsTableNotExist verifies the migration handles
// databases where the seals table doesn't exist yet (e.g., a DB created
// by a tool that only ran raw SQL migrations without GORM AutoMigrate).
// The migration should succeed without error and record version 7.
func TestMigrateV5toV6_SealsTableNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "migration_v5v6_no_seals.db")

	// Manually create a database at schema v5 WITHOUT a seals table
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	_, err = conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER)`)
	require.NoError(t, err)
	_, err = conn.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (5, unixepoch())`)
	require.NoError(t, err)

	conn.Close()

	// Open with InitDB — should detect v5 < v6 and run migration
	// The migration must not fail even though seals table doesn't exist
	db, err := InitDB(dbPath)
	require.NoError(t, err, "migration should succeed when seals table does not exist")
	defer db.Close()

	// Verify schema version is now 6
	var version int
	err = db.conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	require.NoError(t, err)
	require.Equal(t, 7, version, "schema should be at version 7 after migration")

	// Verify seals table still doesn't exist (migration shouldn't create it)
	var tableExists bool
	err = db.conn.QueryRow(
		"SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='seals'",
	).Scan(&tableExists)
	require.NoError(t, err)
	require.False(t, tableExists, "migration should not create the seals table")
}

// TestPostGormMigrate_RenamesSageSeals verifies that PostGormMigrate
// renames all seals with minister_id='sage' to minister_id='chancellor'.
func TestPostGormMigrate_RenamesSageSeals(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, gormDB.AutoMigrate(&Seal{}, &CensorPrecedent{}, &ForgeManifest{}))

	// Insert seals with mixed minister_ids
	seals := []Seal{
		{SealID: "seal-1", EdictID: 1, MinisterID: "sage"},
		{SealID: "seal-2", EdictID: 1, MinisterID: "judge"},
		{SealID: "seal-3", EdictID: 2, MinisterID: "sage"},
		{SealID: "seal-4", EdictID: 3, MinisterID: "ruler"},
		{SealID: "seal-5", EdictID: 3, MinisterID: "chancellor"},
	}
	for i := range seals {
		require.NoError(t, gormDB.Create(&seals[i]).Error)
	}

	// Call PostGormMigrate — should rename sage → chancellor
	require.NoError(t, PostGormMigrate(gormDB))

	// Verify no seals remain with minister_id='sage'
	var sageCount int64
	require.NoError(t, gormDB.Model(&Seal{}).Where("minister_id = 'sage'").Count(&sageCount).Error)
	require.Equal(t, int64(0), sageCount, "no seals should have minister_id='sage' after migration")

	// Verify exactly 3 seals now have minister_id='chancellor' (2 renamed + 1 original)
	var chancellorCount int64
	require.NoError(t, gormDB.Model(&Seal{}).Where("minister_id = 'chancellor'").Count(&chancellorCount).Error)
	require.Equal(t, int64(3), chancellorCount, "exactly 3 seals should have minister_id='chancellor' after migration")

	// Verify other minister_ids are untouched
	var judgeCount, rulerCount int64
	require.NoError(t, gormDB.Model(&Seal{}).Where("minister_id = 'judge'").Count(&judgeCount).Error)
	require.Equal(t, int64(1), judgeCount, "judge seals should be untouched")
	require.NoError(t, gormDB.Model(&Seal{}).Where("minister_id = 'ruler'").Count(&rulerCount).Error)
	require.Equal(t, int64(1), rulerCount, "ruler seals should be untouched")

	// Verify idempotency: running again should be a no-op
	require.NoError(t, PostGormMigrate(gormDB))
	require.NoError(t, gormDB.Model(&Seal{}).Where("minister_id = 'sage'").Count(&sageCount).Error)
	require.Equal(t, int64(0), sageCount, "idempotent: still no sage seals")
	require.NoError(t, gormDB.Model(&Seal{}).Where("minister_id = 'chancellor'").Count(&chancellorCount).Error)
	require.Equal(t, int64(3), chancellorCount, "idempotent: still 3 chancellor seals")
}

// TestPostGormMigrate_BackfillsCensorPrecedentEdictID verifies the migration
// backfills censor_precedents.edict_id for per-manifest rows from their
// owning forge_manifest, while leaving edict-level rows at 0.
func TestPostGormMigrate_BackfillsCensorPrecedentEdictID(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, gormDB.AutoMigrate(&Seal{}, &CensorPrecedent{}, &ForgeManifest{}))

	// Two manifests owned by different edicts.
	require.NoError(t, gormDB.Create(&ForgeManifest{
		ManifestID: "man-a", EdictID: 1, Username: "u", Project: "p", Status: ManifestLive,
	}).Error)
	require.NoError(t, gormDB.Create(&ForgeManifest{
		ManifestID: "man-b", EdictID: 2, Username: "u", Project: "p", Status: ManifestLive,
	}).Error)

	// Per-manifest precedents (edict_id left 0 to simulate pre-migration rows)
	// plus one edict-level precedent.
	require.NoError(t, gormDB.Create(&CensorPrecedent{
		PrecedentID: "p-a", ManifestID: "man-a", Principle: "ethics_review",
		Ruling: PrecedentApproved,
	}).Error)
	require.NoError(t, gormDB.Create(&CensorPrecedent{
		PrecedentID: "p-b", ManifestID: "man-b", Principle: "ethics_review",
		Ruling: PrecedentApproved,
	}).Error)
	require.NoError(t, gormDB.Create(&CensorPrecedent{
		PrecedentID: "p-edict", ManifestID: "", Principle: "ethics_review",
		Ruling: PrecedentApproved,
	}).Error)

	require.NoError(t, PostGormMigrate(gormDB))

	var precA, precB, precEdict CensorPrecedent
	require.NoError(t, gormDB.Where("precedent_id = ?", "p-a").First(&precA).Error)
	require.NoError(t, gormDB.Where("precedent_id = ?", "p-b").First(&precB).Error)
	require.NoError(t, gormDB.Where("precedent_id = ?", "p-edict").First(&precEdict).Error)
	require.Equal(t, uint(1), precA.EdictID, "per-manifest precedent should inherit edict_id 1")
	require.Equal(t, uint(2), precB.EdictID, "per-manifest precedent should inherit edict_id 2")
	require.Equal(t, uint(0), precEdict.EdictID, "edict-level precedent should stay 0 (unknown owner)")
}
