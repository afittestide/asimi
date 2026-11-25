package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/storage"
	"github.com/maximhq/bifrost/core/schemas"
	"modernc.org/sqlite"
)

// parseProjectSlug parses a project slug into host, org and project
// Format: "org/project" (from git remote)
// We need to extract host from the Git remote URL
func parseProjectSlug(workingDir string) (host, org, project string) {
	// Try to get the Git remote URL to extract the host
	remote, err := repo.GitRemoteOriginURL(workingDir)
	if err != nil || remote == "" {
		// Fallback: no git remote
		return "local", "local", "unknown"
	}

	// Parse the remote URL to extract host
	host = "github.com" // default
	if strings.Contains(remote, "://") {
		// HTTP(S) URL format
		if u, err := url.Parse(remote); err == nil {
			host = u.Host
		}
	} else if strings.Contains(remote, ":") {
		// SSH format: git@github.com:owner/repo.git
		parts := strings.SplitN(remote, "@", 2)
		if len(parts) == 2 {
			hostPart := strings.SplitN(parts[1], ":", 2)
			if len(hostPart) >= 1 {
				host = hostPart[0]
			}
		}
	}

	// Parse org and project
	owner, repoName := repo.ParseGitRemote(remote)
	if owner == "" || repoName == "" {
		return host, "unknown", "unknown"
	}

	return host, repo.SanitizeSegment(owner), repo.SanitizeSegment(repoName)
}

// baseHistory contains common fields and logic for history stores
type baseHistory struct {
	store   *storage.HistoryStore
	host    string
	org     string
	project string
	branch  string
}

// PromptHistory handles prompt history persistence
type PromptHistory struct {
	baseHistory
}

// NewPromptHistoryStore creates a new prompt history store using SQLite
func NewPromptHistoryStore(db *storage.DB, repoInfo repo.RepoInfo) (*PromptHistory, error) {
	if db == nil {
		return nil, fmt.Errorf("storage not initialized")
	}

	host, org, project := parseProjectSlug(repoInfo.ProjectRoot)
	branch := repoInfo.BranchSlugOrDefault()

	// Create history store with config (use defaults if not available)
	histCfg := &storage.HistoryConfig{
		Enabled:     true,
		MaxSessions: 1000,
		MaxAgeDays:  90,
	}

	return &PromptHistory{
		baseHistory: baseHistory{
			store:   storage.NewHistoryStore(db, histCfg),
			host:    host,
			org:     org,
			project: project,
			branch:  branch,
		},
	}, nil
}

// Load reads the prompt history from storage
func (h *PromptHistory) Load() ([]storage.HistoryEntry, error) {
	return h.store.LoadPromptHistory(h.host, h.org, h.project, h.branch, 0)
}

// Save is a no-op for SQLite (entries are saved immediately on Append)
func (h *PromptHistory) Save(entries []storage.HistoryEntry) error {
	return nil // No-op, SQLite saves on append
}

// Append adds a new entry to the prompt history
func (h *PromptHistory) Append(prompt string) error {
	return h.store.AppendPrompt(h.host, h.org, h.project, h.branch, prompt)
}

// Clear removes all prompt history
func (h *PromptHistory) Clear() error {
	return h.store.ClearPromptHistory(h.host, h.org, h.project, h.branch)
}

// CommandHistory handles command history persistence
type CommandHistory struct {
	baseHistory
}

// NewCommandHistoryStore creates a new command history store using SQLite
func NewCommandHistoryStore(db *storage.DB, repoInfo repo.RepoInfo) (*CommandHistory, error) {
	if db == nil {
		return nil, fmt.Errorf("storage not initialized")
	}

	host, org, project := parseProjectSlug(repoInfo.ProjectRoot)
	branch := repoInfo.BranchSlugOrDefault()

	// Create history store with config (use defaults if not available)
	histCfg := &storage.HistoryConfig{
		Enabled:     true,
		MaxSessions: 1000,
		MaxAgeDays:  90,
	}

	return &CommandHistory{
		baseHistory: baseHistory{
			store:   storage.NewHistoryStore(db, histCfg),
			host:    host,
			org:     org,
			project: project,
			branch:  branch,
		},
	}, nil
}

// Load reads the command history from storage
func (h *CommandHistory) Load() ([]storage.HistoryEntry, error) {
	return h.store.LoadCommandHistory(h.host, h.org, h.project, h.branch, 0)
}

// Save is a no-op for SQLite (entries are saved immediately on Append)
func (h *CommandHistory) Save(entries []storage.HistoryEntry) error {
	return nil // No-op, SQLite saves on append
}

// Append adds a new entry to the command history
func (h *CommandHistory) Append(command string) error {
	return h.store.AppendCommand(h.host, h.org, h.project, h.branch, command)
}

// Clear removes all command history
func (h *CommandHistory) Clear() error {
	return h.store.ClearCommandHistory(h.host, h.org, h.project, h.branch)
}

// SessionStore adapter wraps the SQLite session store with the old interface
type SessionStore struct {
	store       *storage.SessionStore
	Host        string
	Org         string
	Project     string
	Branch      string
	ProjectRoot string
	saveChan    chan *court.Session
	stopChan    chan struct{}
	closeOnce   sync.Once
	wg          sync.WaitGroup // Track in-flight saves

	// saveHook, if non-nil, replaces saveSessionSync in saveWithRetry.
	// Used only in tests to simulate transient SQLITE_BUSY errors.
	saveHook func(*court.Session) error
}

// NewSessionStore creates a new session store using SQLite
func NewSessionStore(db *storage.DB, repoInfo repo.RepoInfo, maxSessions, maxAgeDays int) (*SessionStore, error) {
	if db == nil {
		return nil, fmt.Errorf("storage not initialized")
	}

	host, org, project := parseProjectSlug(repoInfo.ProjectRoot)
	branch := repoInfo.BranchSlugOrDefault()

	sessCfg := &storage.SessionConfig{
		Enabled:     true,
		MaxSessions: maxSessions,
		MaxAgeDays:  maxAgeDays,
	}

	store := &SessionStore{
		store:       storage.NewSessionStore(db, sessCfg),
		Host:        host,
		Org:         org,
		Project:     project,
		Branch:      branch,
		ProjectRoot: repoInfo.ProjectRoot,
		saveChan:    make(chan *court.Session, 100),
		stopChan:    make(chan struct{}),
	}

	// Start async save worker
	go store.saveWorker()

	// Cleanup old sessions
	if err := store.CleanupOldSessions(); err != nil {
		slog.Warn("failed to cleanup old sessions", "error", err)
	}

	return store, nil
}

func (s *SessionStore) saveWorker() {
	for {
		select {
		case session := <-s.saveChan:
			s.saveWithRetry(session)
			s.wg.Done() // Mark this save as complete
		case <-s.stopChan:
			// Drain remaining saves
			for len(s.saveChan) > 0 {
				session := <-s.saveChan
				s.saveWithRetry(session)
				s.wg.Done()
			}
			return
		}
	}
}

// saveWithRetry calls saveSessionSync with exponential backoff on SQLITE_BUSY.
// Backoff schedule: 50ms, 200ms, 500ms (3 retries).
func (s *SessionStore) saveWithRetry(session *court.Session) {
	backoffs := []time.Duration{50 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond}

	saveFn := s.saveSessionSync
	if s.saveHook != nil {
		saveFn = s.saveHook
	}

	var lastErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		err := saveFn(session)
		if err == nil {
			return
		}
		lastErr = err
		if !isSQLiteBusy(err) {
			break
		}
		if attempt < len(backoffs) {
			slog.Warn("session save busy, retrying",
				"error", err, "attempt", attempt+1, "backoff", backoffs[attempt])
			time.Sleep(backoffs[attempt])
		}
	}
	slog.Warn("failed to save session after retries", "error", lastErr)
}

// isSQLiteBusy returns true if the error is a SQLITE_BUSY or SQLITE_LOCKED error.
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	if se, ok := err.(*sqlite.Error); ok {
		code := se.Code()
		// SQLITE_BUSY = 5, SQLITE_LOCKED = 6
		return code == 5 || code == 6
	}
	// Fallback: check error string for drivers that wrap the error
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "SQLITE_LOCKED")
}

// SaveSession saves a session asynchronously
func (s *SessionStore) SaveSession(session *court.Session) {
	if session != nil {
		s.wg.Add(1) // Track this save
		select {
		case s.saveChan <- session:
		default:
			s.wg.Done() // Don't track if we couldn't queue it
			slog.Warn("save channel full, skipping save")
		}
	}
}

// SaveSessionSync saves a session synchronously
func (s *SessionStore) SaveSessionSync(session *court.Session) error {
	return s.saveSessionSync(session)
}

func (s *SessionStore) saveSessionSync(session *court.Session) error {
	if session == nil {
		return fmt.Errorf("cannot save nil session")
	}

	// Don't save empty sessions (only system messages)
	if !session.HasUserContent() {
		return nil // Skip saving empty sessions
	}

	// Get messages for serialization
	messages := session.GetMessages()

	// Generate ID and timestamps for new sessions
	if session.ID == "" {
		session.ID = court.GenerateSessionID()
	}
	now := time.Now()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	session.LastUpdated = now

	// Set FirstPrompt if not set
	if session.FirstPrompt == "" {
		session.FirstPrompt = session.ExtractFirstPrompt()
	}

	// Serialize messages to JSON (storage stores raw JSON for type agnosticism)
	messagesJSON, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("failed to marshal messages: %w", err)
	}

	// Convert main.Session to storage.SessionData
	storageSession := &storage.SessionData{
		ID:                session.ID,
		CreatedAt:         session.CreatedAt,
		LastUpdated:       session.LastUpdated,
		FirstPrompt:       session.FirstPrompt,
		Provider:          session.Provider,
		Model:             session.Model,
		WorkingDir:        session.WorkingDir,
		ProjectSlug:       session.ProjectSlug,
		TabType:           session.TabType,
		Messages:          messagesJSON,
		ContextFiles:      session.ContextFiles,
		PersistedMsgCount: session.PersistedMsgCount,
	}

	if err := s.store.SaveSession(storageSession, s.Host, s.Org, s.Project, s.Branch); err != nil {
		return err
	}

	// Update the in-memory session's persisted count on success
	session.PersistedMsgCount = storageSession.PersistedMsgCount
	return nil
}

// LoadSession loads a session by ID
func (s *SessionStore) LoadSession(id string) (*court.Session, error) {
	storageSession, host, org, project, branch, err := s.store.LoadSession(id)
	if err != nil {
		return nil, err
	}

	// Verify it's from the same repo/branch (optional check)
	_ = host
	_ = org
	_ = project
	_ = branch

	// Deserialize JSON messages to []schemas.ChatMessage
	var messages []schemas.ChatMessage
	if len(storageSession.Messages) > 0 {
		if err := json.Unmarshal(storageSession.Messages, &messages); err != nil {
			return nil, fmt.Errorf("failed to unmarshal messages: %w", err)
		}
	}

	// Convert storage.SessionData to court.Session
	session := &court.Session{
		ID:                storageSession.ID,
		CreatedAt:         storageSession.CreatedAt,
		LastUpdated:       storageSession.LastUpdated,
		FirstPrompt:       storageSession.FirstPrompt,
		Provider:          storageSession.Provider,
		Model:             storageSession.Model,
		WorkingDir:        storageSession.WorkingDir,
		ProjectSlug:       storageSession.ProjectSlug,
		TabType:           storageSession.TabType,
		ContextFiles:      storageSession.ContextFiles,
		PersistedMsgCount: len(messages),
	}
	session.SetMessages(messages)

	return session, nil
}

// ListSessions lists sessions for the current branch, optionally filtered by tab type
func (s *SessionStore) ListSessions(limit int, tabType string) ([]court.Session, error) {
	storageSessions, err := s.store.ListSessions(s.Host, s.Org, s.Project, s.Branch, tabType, limit)
	if err != nil {
		return nil, err
	}

	sessions := make([]court.Session, len(storageSessions))
	for i, ss := range storageSessions {
		// Deserialize JSON messages (may be empty for list view)
		var messages []schemas.ChatMessage
		if len(ss.Messages) > 0 {
			if err := json.Unmarshal(ss.Messages, &messages); err != nil {
				return nil, fmt.Errorf("failed to unmarshal messages for session %s: %w", ss.ID, err)
			}
		}

		sessions[i] = court.Session{
			ID:           ss.ID,
			CreatedAt:    ss.CreatedAt,
			LastUpdated:  ss.LastUpdated,
			FirstPrompt:  ss.FirstPrompt,
			Provider:     ss.Provider,
			Model:        ss.Model,
			WorkingDir:   ss.WorkingDir,
			ProjectSlug:  ss.ProjectSlug,
			TabType:      ss.TabType,
			ContextFiles: ss.ContextFiles,
			MessageCount: ss.MessageCount,
		}
		sessions[i].SetMessages(messages)
	}

	return sessions, nil
}

// CleanupOldSessions removes old sessions for the current project across all its branches.
func (s *SessionStore) CleanupOldSessions() error {
	return s.store.CleanupProjectSessions(s.Host, s.Org, s.Project)
}

// DeleteSession deletes a session and all its messages from the store
func (s *SessionStore) DeleteSession(sessionID string) error {
	return s.store.DeleteSession(sessionID)
}

// DEPRECATED: ClearSession removed - use DeleteSession or reset in memory
// ClearSession was removed to preserve user history for :resume functionality

// Close closes the session store gracefully, waiting for pending saves to complete
func (s *SessionStore) Close() {
	s.closeOnce.Do(func() {
		close(s.stopChan)
		if waitWithTimeout(&s.wg, 2*time.Second) {
			slog.Debug("session store closed gracefully")
		} else {
			slog.Warn("session store close timed out, some saves may be lost")
		}
	})
}

// waitWithTimeout waits for a WaitGroup with a timeout, returning true if completed
func waitWithTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Flush waits for all pending saves to complete
func (s *SessionStore) Flush() {
	s.wg.Wait()
}
