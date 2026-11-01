package storage

import (
	"encoding/json"
	"time"

	"github.com/afittestide/asimi/internal/config"
)

// Schema version
const SchemaVersion = 8

// Type aliases - use types from internal/config as the single source of truth
type (
	SessionConfig = config.SessionConfig
	HistoryConfig = config.HistoryConfig
)

// DBSession maps directly to the sessions table with db tags
// This is used for database operations only
type DBSession struct {
	ID          string    `db:"id"`
	BranchID    int64     `db:"branch_id"`
	CreatedAt   time.Time `db:"created_at"`
	LastUpdated time.Time `db:"last_updated"`
	FirstPrompt string    `db:"first_prompt"`
	Provider    string    `db:"provider"`
	Model       string    `db:"model"`
	WorkingDir  string    `db:"working_dir"`
	TabType     string    `db:"tab_type"`
}

// SessionData contains the persistable session fields
// Note: The main Session type in the main package includes runtime fields
// like llm, toolCatalog, etc. that are not persisted
type SessionData struct {
	ID          string
	CreatedAt   time.Time
	LastUpdated time.Time
	FirstPrompt string
	Provider    string
	Model       string
	WorkingDir  string
	ProjectSlug string
	TabType     string
	// Messages: JSON-encoded message array, agnostic to type
	Messages     json.RawMessage
	ContextFiles map[string]string
	MessageCount int // Number of messages (for list views, avoids loading full messages)

	// PersistedMsgCount tracks how many messages have been successfully
	// persisted to the DB. Only messages with index >= this value are
	// inserted on the next SaveSession call, preventing the DELETE-then-
	// INSERT clobber that loses history when a save fails mid-way.
	PersistedMsgCount int `json:"-"`
}

// Repository represents a Git repository (host/org/project)
type Repository struct {
	ID      int64  `db:"id"`      // Auto-increment primary key
	Host    string `db:"host"`    // e.g., "github.com", "gitlab.com", "bitbucket.org"
	Org     string `db:"org"`     // e.g., "afittestide"
	Project string `db:"project"` // e.g., "asimi-cli"
}

// Branch represents a Git branch within a repository
type Branch struct {
	ID           int64  `db:"id"`            // Auto-increment primary key
	RepositoryID int64  `db:"repository_id"` // Foreign key to repositories.id
	Name         string `db:"name"`          // e.g., "main", "feature/sqlite"
}

// Message represents a single message in a conversation
type Message struct {
	ID        int64     `db:"id"`         // Auto-increment primary key
	SessionID string    `db:"session_id"` // Foreign key to sessions.id
	Sequence  int       `db:"sequence"`   // Message order in conversation
	Role      string    `db:"role"`       // "human", "ai", "system", "tool"
	Content   string    `db:"content"`    // JSON-encoded MessageContent.Parts
	CreatedAt time.Time `db:"created_at"` // Stored as Unix timestamp
}

// PromptHistory represents a user prompt for autocomplete
type PromptHistory struct {
	ID        int64     `db:"id"`        // Auto-increment primary key
	BranchID  int64     `db:"branch_id"` // Foreign key to branches.id
	Prompt    string    `db:"prompt"`    // User's prompt text
	Timestamp time.Time `db:"timestamp"` // Stored as Unix timestamp
}

// CommandHistory represents a slash command for history
type CommandHistory struct {
	ID        int64     `db:"id"`        // Auto-increment primary key
	BranchID  int64     `db:"branch_id"` // Foreign key to branches.id
	Command   string    `db:"command"`   // Command text
	Timestamp time.Time `db:"timestamp"` // Stored as Unix timestamp
}

// SchemaVersionRecord tracks schema migrations
type SchemaVersionRecord struct {
	Version   int       `db:"version"`
	AppliedAt time.Time `db:"applied_at"`
}

// Schema is the SQL DDL for creating all tables
const Schema = `
-- Repositories table (host + org + project)
CREATE TABLE IF NOT EXISTS repositories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    host TEXT NOT NULL,
    org TEXT NOT NULL,
    project TEXT NOT NULL,
    UNIQUE(host, org, project)
);

CREATE INDEX IF NOT EXISTS idx_repositories_lookup ON repositories(host, org, project);

-- Branches table
CREATE TABLE IF NOT EXISTS branches (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    repository_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    UNIQUE(repository_id, name),
    FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_branches_repo ON branches(repository_id, name);

-- Sessions table
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    branch_id INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    last_updated INTEGER NOT NULL,
    first_prompt TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    working_dir TEXT NOT NULL,
    tab_type TEXT NOT NULL,
    FOREIGN KEY (branch_id) REFERENCES branches(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sessions_branch ON sessions(branch_id, last_updated DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(last_updated DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at DESC);

-- Messages table
CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, sequence);
CREATE INDEX IF NOT EXISTS idx_messages_created ON messages(created_at DESC);

-- Prompt history table
CREATE TABLE IF NOT EXISTS prompt_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    branch_id INTEGER NOT NULL,
    prompt TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    FOREIGN KEY (branch_id) REFERENCES branches(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_prompt_history_branch ON prompt_history(branch_id, timestamp DESC);

-- Command history table
CREATE TABLE IF NOT EXISTS command_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    branch_id INTEGER NOT NULL,
    command TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    FOREIGN KEY (branch_id) REFERENCES branches(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_command_history_branch ON command_history(branch_id, timestamp DESC);

-- Workflows table (added in schema version 2)
CREATE TABLE IF NOT EXISTS workflows (
    id TEXT PRIMARY KEY,
    branch_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    current_step INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'pending',
    max_retries INTEGER NOT NULL DEFAULT 3,
    data TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (branch_id) REFERENCES branches(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_workflows_branch ON workflows(branch_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_workflows_state ON workflows(state);

-- Workflow steps table (added in schema version 2)
CREATE TABLE IF NOT EXISTS workflow_steps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workflow_id TEXT NOT NULL,
    step_index INTEGER NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    retry_count INTEGER NOT NULL DEFAULT 0,
    message TEXT NOT NULL DEFAULT '',
    prompt_template TEXT NOT NULL DEFAULT '',
    prepare_data TEXT NOT NULL DEFAULT '{}',
    UNIQUE(workflow_id, step_index),
    FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_workflow_steps_workflow ON workflow_steps(workflow_id, step_index);

-- Ritual executions table (added in schema version 3)
CREATE TABLE IF NOT EXISTS ritual_executions (
    id TEXT PRIMARY KEY,
    ritual_name TEXT NOT NULL,
    edict_id INTEGER NOT NULL,
    username TEXT NOT NULL DEFAULT '',
    project TEXT NOT NULL DEFAULT '',
    session_id TEXT,
    current_step INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'pending',
    data TEXT NOT NULL DEFAULT '{}',
    owner_pid INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX IF NOT EXISTS idx_ritual_executions_edict ON ritual_executions(edict_id);
CREATE INDEX IF NOT EXISTS idx_ritual_executions_state ON ritual_executions(state);
CREATE INDEX IF NOT EXISTS idx_ritual_executions_session ON ritual_executions(session_id);
CREATE INDEX IF NOT EXISTS idx_ritual_executions_user_project ON ritual_executions(username, project);

-- Ritual step states table (added in schema version 3)
CREATE TABLE IF NOT EXISTS ritual_step_states (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id TEXT NOT NULL,
    step_index INTEGER NOT NULL,
    name TEXT NOT NULL,
    username TEXT NOT NULL DEFAULT '',
    project TEXT NOT NULL DEFAULT '',
    session_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    retry_count INTEGER NOT NULL DEFAULT 0,
    message TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_ritual_step_states_execution ON ritual_step_states(execution_id);
CREATE INDEX IF NOT EXISTS idx_ritual_step_states_session ON ritual_step_states(session_id);
CREATE INDEX IF NOT EXISTS idx_ritual_step_states_user_project ON ritual_step_states(username, project);

-- Schema version table
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (1, unixepoch());
INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (2, unixepoch());
INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (3, unixepoch());
INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (4, unixepoch());
`
