package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// SessionStore handles session persistence
type SessionStore struct {
	db  *DB
	cfg *SessionConfig
}

// NewSessionStore creates a new session store
func NewSessionStore(db *DB, cfg *SessionConfig) *SessionStore {
	return &SessionStore{
		db:  db,
		cfg: cfg,
	}
}

// SaveSession saves or updates a session with all its messages.
// Uses incremental upsert: only messages with index >= PersistedMsgCount
// are inserted, and existing messages are never deleted. This prevents
// data loss when a save fails (SQLITE_BUSY) and a later save with fewer
// in-memory messages succeeds.
func (s *SessionStore) SaveSession(session *SessionData, host, org, project, branch string) error {
	// Start transaction
	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Get or create repository
	repoID, err := s.getOrCreateRepositoryTx(tx, host, org, project)
	if err != nil {
		return err
	}

	// Get or create branch
	branchID, err := s.getOrCreateBranchTx(tx, repoID, branch)
	if err != nil {
		return err
	}

	// Update last updated timestamp
	session.LastUpdated = time.Now()

	// Insert or update session metadata.
	// Use ON CONFLICT DO UPDATE instead of INSERT OR REPLACE to avoid
	// deleting the session row (which would cascade-delete all messages).
	_, err = tx.Exec(`
		INSERT INTO sessions
		(id, branch_id, created_at, last_updated, first_prompt, provider, model, working_dir, tab_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			branch_id = excluded.branch_id,
			created_at = excluded.created_at,
			last_updated = excluded.last_updated,
			first_prompt = excluded.first_prompt,
			provider = excluded.provider,
			model = excluded.model,
			working_dir = excluded.working_dir,
			tab_type = excluded.tab_type`,
		session.ID,
		branchID,
		session.CreatedAt.Unix(),
		session.LastUpdated.Unix(),
		session.FirstPrompt,
		session.Provider,
		session.Model,
		session.WorkingDir,
		session.TabType,
	)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	// Serialize messages to JSON (json.RawMessage.Marshal returns itself)
	messagesJSON, err := json.Marshal(session.Messages)
	if err != nil {
		return fmt.Errorf("failed to marshal messages: %w", err)
	}
	session.Messages = messagesJSON

	// Incremental upsert: only insert messages with index >= PersistedMsgCount.
	// Use INSERT OR IGNORE to skip any that might already exist (e.g., from
	// a partially committed prior save).
	var rawMsgs []interface{}
	msgCount := session.PersistedMsgCount
	if err := json.Unmarshal(messagesJSON, &rawMsgs); err == nil && rawMsgs != nil {
		for i, msgData := range rawMsgs {
			if i < session.PersistedMsgCount {
				continue // already persisted
			}
			msgJSON, err := json.Marshal(msgData)
			if err != nil {
				return fmt.Errorf("failed to marshal message %d: %w", i, err)
			}
			_, err = tx.Exec(`
				INSERT OR IGNORE INTO messages (session_id, sequence, role, content, created_at)
				VALUES (?, ?, ?, ?, ?)`,
				session.ID, i, "", string(msgJSON), time.Now().Unix(),
			)
			if err != nil {
				return fmt.Errorf("failed to insert message %d: %w", i, err)
			}
		}
		msgCount = len(rawMsgs)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Update PersistedMsgCount only after successful commit
	session.PersistedMsgCount = msgCount

	slog.Debug("Session saved", "id", session.ID, "messages", len(session.Messages))
	return nil
}

// LoadSession loads a session by ID with all its messages
// Returns: (session, host, org, project, branch, error)
func (s *SessionStore) LoadSession(sessionID string) (*SessionData, string, string, string, string, error) {
	// Query session metadata with repository and branch info
	var session SessionData
	var host, org, project, branch string
	var createdAt, lastUpdated int64

	err := s.db.conn.QueryRow(`
		SELECT s.id, s.created_at, s.last_updated, s.first_prompt,
		       s.provider, s.model, s.working_dir, s.tab_type,
		       r.host, r.org, r.project, b.name
		FROM sessions s
		JOIN branches b ON s.branch_id = b.id
		JOIN repositories r ON b.repository_id = r.id
		WHERE s.id = ?`,
		sessionID,
	).Scan(
		&session.ID,
		&createdAt,
		&lastUpdated,
		&session.FirstPrompt,
		&session.Provider,
		&session.Model,
		&session.WorkingDir,
		&session.TabType,
		&host,
		&org,
		&project,
		&branch,
	)

	if err == sql.ErrNoRows {
		return nil, "", "", "", "", fmt.Errorf("session not found: %s", sessionID)
	}
	if err != nil {
		return nil, "", "", "", "", fmt.Errorf("failed to load session: %w", err)
	}

	// Convert Unix timestamps to time.Time
	session.CreatedAt = time.Unix(createdAt, 0)
	session.LastUpdated = time.Unix(lastUpdated, 0)
	session.ProjectSlug = fmt.Sprintf("%s/%s/%s", host, org, project)
	session.Messages = json.RawMessage{}           // Initialize empty raw JSON
	session.ContextFiles = make(map[string]string) // Initialize empty map

	// Load messages from the messages table
	rows, err := s.db.conn.Query(`
		SELECT content
		FROM messages
		WHERE session_id = ?
		ORDER BY sequence`,
		sessionID,
	)
	if err != nil {
		return nil, "", "", "", "", fmt.Errorf("failed to load messages: %w", err)
	}
	defer rows.Close()

	// Collect message JSON strings
	var msgJSONs []string
	for rows.Next() {
		var msgJSON string
		if err := rows.Scan(&msgJSON); err != nil {
			return nil, "", "", "", "", fmt.Errorf("failed to scan message: %w", err)
		}
		msgJSONs = append(msgJSONs, msgJSON)
	}

	if err := rows.Err(); err != nil {
		return nil, "", "", "", "", fmt.Errorf("error iterating messages: %w", err)
	}

	// Combine into a JSON array - storage passes bytes through
	// Adapter will deserialize this when constructing the Session
	if len(msgJSONs) > 0 {
		session.Messages = json.RawMessage("[" + strings.Join(msgJSONs, ",") + "]")
	}

	slog.Debug("Session loaded", "id", sessionID, "message_bytes", len(session.Messages))
	return &session, host, org, project, branch, nil
}

// ListSessions lists sessions for a given host/org/project/branch
func (s *SessionStore) ListSessions(host, org, project, branch, tabType string, limit int) ([]SessionData, error) {
	query := `
		SELECT s.id, s.created_at, s.last_updated, s.first_prompt,
		       s.provider, s.model, s.working_dir, s.tab_type,
		       COUNT(m.id) as message_count
		FROM sessions s
		JOIN branches b ON s.branch_id = b.id
		JOIN repositories r ON b.repository_id = r.id
		LEFT JOIN messages m ON s.id = m.session_id
		WHERE r.host = ? AND r.org = ? AND r.project = ? AND b.name = ?`
	args := []interface{}{host, org, project, branch}

	if tabType != "" {
		query += " AND s.tab_type = ?"
		args = append(args, tabType)
	}

	query += `
		GROUP BY s.id, s.created_at, s.last_updated, s.first_prompt,
		         s.provider, s.model, s.working_dir, s.tab_type
		ORDER BY s.last_updated DESC`

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []SessionData
	for rows.Next() {
		var session SessionData
		var createdAt, lastUpdated int64
		var messageCount int

		err := rows.Scan(
			&session.ID,
			&createdAt,
			&lastUpdated,
			&session.FirstPrompt,
			&session.Provider,
			&session.Model,
			&session.WorkingDir,
			&session.TabType,
			&messageCount,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}

		session.CreatedAt = time.Unix(createdAt, 0)
		session.LastUpdated = time.Unix(lastUpdated, 0)
		session.ProjectSlug = fmt.Sprintf("%s/%s/%s", host, org, project)
		session.MessageCount = messageCount
		session.Messages = json.RawMessage{} // Empty for list view
		session.ContextFiles = make(map[string]string)

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	return sessions, nil
}

// ListAllSessions lists sessions, optionally filtered by host/org/project.
// When all filters are empty, returns all sessions (backward compat).
func (s *SessionStore) ListAllSessions(limit int, host, org, project string) ([]SessionData, error) {
	query := `
		SELECT s.id, s.created_at, s.last_updated, s.first_prompt,
		       s.provider, s.model, s.working_dir, s.tab_type, r.host, r.org, r.project,
		       COUNT(m.id) as message_count
		FROM sessions s
		JOIN branches b ON s.branch_id = b.id
		JOIN repositories r ON b.repository_id = r.id
		LEFT JOIN messages m ON s.id = m.session_id`

	var args []interface{}
	if host != "" || org != "" || project != "" {
		conditions := " WHERE "
		var preds []string
		if host != "" {
			preds = append(preds, "r.host = ?")
			args = append(args, host)
		}
		if org != "" {
			preds = append(preds, "r.org = ?")
			args = append(args, org)
		}
		if project != "" {
			preds = append(preds, "r.project = ?")
			args = append(args, project)
		}
		for i, p := range preds {
			if i > 0 {
				conditions += " AND "
			}
			conditions += p
		}
		query += conditions
	}

	query += `
		GROUP BY s.id, s.created_at, s.last_updated, s.first_prompt,
		         s.provider, s.model, s.working_dir, s.tab_type, r.host, r.org, r.project
		ORDER BY s.last_updated DESC`

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list all sessions: %w", err)
	}
	defer rows.Close()

	var sessions []SessionData
	for rows.Next() {
		var session SessionData
		var createdAt, lastUpdated int64
		var rowHost, rowOrg, rowProject string
		var messageCount int

		err := rows.Scan(
			&session.ID,
			&createdAt,
			&lastUpdated,
			&session.FirstPrompt,
			&session.Provider,
			&session.Model,
			&session.WorkingDir,
			&session.TabType,
			&rowHost,
			&rowOrg,
			&rowProject,
			&messageCount,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}

		session.CreatedAt = time.Unix(createdAt, 0)
		session.LastUpdated = time.Unix(lastUpdated, 0)
		session.ProjectSlug = fmt.Sprintf("%s/%s/%s", rowHost, rowOrg, rowProject)
		session.MessageCount = messageCount
		session.Messages = json.RawMessage{} // Empty for list view
		session.ContextFiles = make(map[string]string)

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	return sessions, nil
}

// DeleteSession deletes a session (messages are cascade deleted by DB)
func (s *SessionStore) DeleteSession(sessionID string) error {
	result, err := s.db.conn.Exec("DELETE FROM sessions WHERE id = ?", sessionID)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	return nil
}

// CleanupOldSessions deletes sessions older than maxAgeDays or exceeding maxSessions count.
// When branchID is 0, cleans up across all branches (backward compat).
func (s *SessionStore) CleanupOldSessions(branchID int64) error {
	if s.cfg == nil {
		return nil
	}

	// Delete sessions older than maxAgeDays
	if s.cfg.MaxAgeDays > 0 {
		cutoffTime := time.Now().AddDate(0, 0, -s.cfg.MaxAgeDays).Unix()
		query := "DELETE FROM sessions WHERE last_updated < ?"
		args := []interface{}{cutoffTime}
		if branchID > 0 {
			query += " AND branch_id = ?"
			args = append(args, branchID)
		}
		result, err := s.db.conn.Exec(query, args...)
		if err != nil {
			return fmt.Errorf("failed to delete old sessions: %w", err)
		}

		if deleted, _ := result.RowsAffected(); deleted > 0 {
			slog.Info("Deleted old sessions", "count", deleted, "max_age_days", s.cfg.MaxAgeDays, "branch_id", branchID)
		}
	}

	// Keep only the most recent maxSessions
	if s.cfg.MaxSessions > 0 {
		if branchID > 0 {
			// Scoped to a specific branch: only delete sessions on that branch
			// that fall outside the most recent maxSessions
			_, err := s.db.conn.Exec(
				"DELETE FROM sessions WHERE branch_id = ? AND id NOT IN (SELECT id FROM sessions WHERE branch_id = ? ORDER BY last_updated DESC LIMIT ?)",
				branchID, branchID, s.cfg.MaxSessions,
			)
			if err != nil {
				return fmt.Errorf("failed to limit session count: %w", err)
			}
		} else {
			// Global: keep only the most recent maxSessions across all branches
			_, err := s.db.conn.Exec(
				"DELETE FROM sessions WHERE id NOT IN (SELECT id FROM sessions ORDER BY last_updated DESC LIMIT ?)",
				s.cfg.MaxSessions,
			)
			if err != nil {
				return fmt.Errorf("failed to limit session count: %w", err)
			}
		}
	}

	return nil
}

// CleanupProjectSessions deletes sessions belonging to the specified repository
// (across all of its branches) that are older than MaxAgeDays or exceed
// MaxSessions. It is a no-op if the repository does not exist yet.
func (s *SessionStore) CleanupProjectSessions(host, org, project string) error {
	if s.cfg == nil {
		return nil
	}

	var repoID int64
	err := s.db.conn.QueryRow(
		"SELECT id FROM repositories WHERE host = ? AND org = ? AND project = ?",
		host, org, project,
	).Scan(&repoID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to look up repository: %w", err)
	}

	if s.cfg.MaxAgeDays > 0 {
		cutoffTime := time.Now().AddDate(0, 0, -s.cfg.MaxAgeDays).Unix()
		result, err := s.db.conn.Exec(
			`DELETE FROM sessions
			 WHERE last_updated < ?
			   AND branch_id IN (SELECT id FROM branches WHERE repository_id = ?)`,
			cutoffTime, repoID,
		)
		if err != nil {
			return fmt.Errorf("failed to delete old project sessions: %w", err)
		}

		if deleted, _ := result.RowsAffected(); deleted > 0 {
			slog.Info("Deleted old sessions", "count", deleted, "max_age_days", s.cfg.MaxAgeDays, "host", host, "org", org, "project", project)
		}
	}

	if s.cfg.MaxSessions > 0 {
		_, err := s.db.conn.Exec(
			`DELETE FROM sessions
			 WHERE branch_id IN (SELECT id FROM branches WHERE repository_id = ?)
			   AND id NOT IN (
			       SELECT id FROM sessions
			       WHERE branch_id IN (SELECT id FROM branches WHERE repository_id = ?)
			       ORDER BY last_updated DESC
			       LIMIT ?
			   )`,
			repoID, repoID, s.cfg.MaxSessions,
		)
		if err != nil {
			return fmt.Errorf("failed to limit project session count: %w", err)
		}
	}

	return nil
}

// SearchMessages searches for messages matching a regex pattern.
// host/org/project scope the query; when all empty, searches all sessions.
func (s *SessionStore) SearchMessages(pattern string, limit int, host, org, project string) ([]SearchResult, error) {
	// Compile regex
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	// Query messages with optional host/org/project scope
	query := `
		SELECT m.session_id, m.sequence, m.role, m.content,
		       s.first_prompt, s.working_dir,
		       r.host, r.org, r.project, b.name
		FROM messages m
		JOIN sessions s ON m.session_id = s.id
		JOIN branches b ON s.branch_id = b.id
		JOIN repositories r ON b.repository_id = r.id`

	var args []interface{}
	if host != "" || org != "" || project != "" {
		conditions := " WHERE "
		var preds []string
		if host != "" {
			preds = append(preds, "r.host = ?")
			args = append(args, host)
		}
		if org != "" {
			preds = append(preds, "r.org = ?")
			args = append(args, org)
		}
		if project != "" {
			preds = append(preds, "r.project = ?")
			args = append(args, project)
		}
		for i, p := range preds {
			if i > 0 {
				conditions += " AND "
			}
			conditions += p
		}
		query += conditions
	}

	query += " ORDER BY m.created_at DESC"

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit*10) // Get more, filter, then limit
	}

	rows, err := s.db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search messages: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var sessionID, role, contentJSON, firstPrompt, workingDir, resultHost, resultOrg, resultProject, branch string
		var sequence int

		err := rows.Scan(
			&sessionID, &sequence, &role, &contentJSON,
			&firstPrompt, &workingDir,
			&resultHost, &resultOrg, &resultProject, &branch,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		// Check if content matches regex
		if re.MatchString(contentJSON) {
			// Extract matched text snippet
			matches := re.FindStringSubmatch(contentJSON)
			snippet := contentJSON
			if len(matches) > 0 {
				// Get context around match
				idx := strings.Index(contentJSON, matches[0])
				start := max(0, idx-100)
				end := min(len(contentJSON), idx+len(matches[0])+100)
				snippet = contentJSON[start:end]
			}

			results = append(results, SearchResult{
				SessionID:   sessionID,
				Sequence:    sequence,
				Role:        role,
				Snippet:     snippet,
				FirstPrompt: firstPrompt,
				WorkingDir:  workingDir,
				Host:        resultHost,
				Org:         resultOrg,
				Project:     resultProject,
				Branch:      branch,
			})

			if limit > 0 && len(results) >= limit {
				break
			}
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating messages: %w", err)
	}

	return results, nil
}

// SearchResult represents a search result
type SearchResult struct {
	SessionID   string
	Sequence    int
	Role        string
	Snippet     string
	FirstPrompt string
	WorkingDir  string
	Host        string
	Org         string
	Project     string
	Branch      string
}

// Helper functions for transactions

func (s *SessionStore) getOrCreateRepositoryTx(tx *sql.Tx, host, org, project string) (int64, error) {
	var id int64
	err := tx.QueryRow(
		"SELECT id FROM repositories WHERE host = ? AND org = ? AND project = ?",
		host, org, project,
	).Scan(&id)

	if err == nil {
		return id, nil
	}

	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("failed to query repository: %w", err)
	}

	result, err := tx.Exec(
		"INSERT INTO repositories (host, org, project) VALUES (?, ?, ?)",
		host, org, project,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create repository: %w", err)
	}

	return result.LastInsertId()
}

func (s *SessionStore) getOrCreateBranchTx(tx *sql.Tx, repositoryID int64, name string) (int64, error) {
	var id int64
	err := tx.QueryRow(
		"SELECT id FROM branches WHERE repository_id = ? AND name = ?",
		repositoryID, name,
	).Scan(&id)

	if err == nil {
		return id, nil
	}

	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("failed to query branch: %w", err)
	}

	result, err := tx.Exec(
		"INSERT INTO branches (repository_id, name) VALUES (?, ?)",
		repositoryID, name,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create branch: %w", err)
	}

	return result.LastInsertId()
}

// Helper function for min/max (Go 1.21+)
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
