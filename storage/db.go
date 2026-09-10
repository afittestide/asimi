package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gorm.io/gorm"
	_ "modernc.org/sqlite" // SQLite driver
)

// DBPath extracts the database file path from a gorm.DB using PRAGMA database_list.
func DBPath(db *gorm.DB) string {
	if db == nil {
		return ""
	}
	var file string
	row := db.Raw("PRAGMA database_list").Row()
	var seq int
	var name string
	if err := row.Scan(&seq, &name, &file); err != nil {
		slog.Warn("failed to get database path", "error", err)
		return ""
	}
	return file
}

// DB wraps the database connection with additional functionality
type DB struct {
	conn *sql.DB
	path string
}

// InitDB initializes the SQLite database and creates tables if needed
func InitDB(dbPath string) (*DB, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open database connection
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool
	conn.SetMaxOpenConns(1) // SQLite works best with single connection
	conn.SetMaxIdleConns(1)

	// Enable foreign keys (SQLite requires this per connection)
	if _, err := conn.Exec("PRAGMA foreign_keys = ON"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := conn.Exec("PRAGMA journal_mode = WAL"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Set busy_timeout so SQLite waits for locks instead of failing instantly
	if _, err := conn.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to set busy_timeout: %w", err)
	}

	db := &DB{
		conn: conn,
		path: dbPath,
	}

	// Check current schema version and migrate if needed
	currentVersion, err := db.getSchemaVersion()
	if err != nil {
		// Schema version table doesn't exist, create fresh schema
		if _, err := conn.Exec(Schema); err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to create schema: %w", err)
		}
		// Mark all migrations as applied — the fresh Schema SQL already
		// creates tables with all current columns.
		for v := 2; v <= SchemaVersion; v++ {
			if _, err := conn.Exec(
				"INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (?, unixepoch())",
				v,
			); err != nil {
				conn.Close()
				return nil, fmt.Errorf("failed to record schema version %d: %w", v, err)
			}
		}
		slog.Debug("SQLite database initialized with fresh schema", "path", dbPath, "version", SchemaVersion)
	} else if currentVersion < SchemaVersion {
		// Run migrations
		if err := db.runMigrations(currentVersion); err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to run migrations: %w", err)
		}
		slog.Debug("SQLite database migrated", "path", dbPath, "from_version", currentVersion, "to_version", SchemaVersion)
	} else {
		slog.Debug("SQLite database initialized", "path", dbPath, "version", currentVersion)
	}

	// Register custom REGEXP function
	if err := db.registerRegexpFunction(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to register regexp function: %w", err)
	}

	return db, nil
}

// getSchemaVersion returns the current schema version from the database
func (db *DB) getSchemaVersion() (int, error) {
	var version int
	err := db.conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	if err != nil {
		return 0, err
	}
	return version, nil
}

// runMigrations runs all necessary migrations from currentVersion to SchemaVersion
func (db *DB) runMigrations(currentVersion int) error {
	for v := currentVersion + 1; v <= SchemaVersion; v++ {
		switch v {
		case 2:
			if err := db.migrateV1toV2(); err != nil {
				return fmt.Errorf("migration v1→v2 failed: %w", err)
			}
		case 3:
			if err := db.migrateV2toV3(); err != nil {
				return fmt.Errorf("migration v2→v3 failed: %w", err)
			}
		case 4:
			if err := db.migrateV3toV4(); err != nil {
				return fmt.Errorf("migration v3→v4 failed: %w", err)
			}
		case 5:
			if err := db.migrateV4toV5(); err != nil {
				return fmt.Errorf("migration v4→v5 failed: %w", err)
			}
		case 6:
			if err := db.migrateV5toV6(); err != nil {
				return fmt.Errorf("migration v5→v6 failed: %w", err)
			}
		case 7:
			if err := db.migrateV6toV7(); err != nil {
				return fmt.Errorf("migration v6→v7 failed: %w", err)
			}
		default:
			return fmt.Errorf("unknown migration version: %d", v)
		}
	}
	return nil
}

// migrateV1toV2 adds username and project columns to ritual tables
func (db *DB) migrateV1toV2() error {
	migrations := []string{
		`ALTER TABLE ritual_executions ADD COLUMN username TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE ritual_executions ADD COLUMN project TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE ritual_step_states ADD COLUMN username TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE ritual_step_states ADD COLUMN project TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_ritual_executions_user_project ON ritual_executions(username, project)`,
		`CREATE INDEX IF NOT EXISTS idx_ritual_step_states_user_project ON ritual_step_states(username, project)`,
	}
	for _, m := range migrations {
		if _, err := db.conn.Exec(m); err != nil {
			return fmt.Errorf("exec %q: %w", m, err)
		}
	}
	if _, err := db.conn.Exec(
		"INSERT INTO schema_version (version, applied_at) VALUES (?, unixepoch())",
		2,
	); err != nil {
		return fmt.Errorf("record version 2: %w", err)
	}
	slog.Debug("migrated schema v1→v2: added username/project to ritual tables")
	return nil
}

// migrateV2toV3 adds username and project columns to censor_precedents
func (db *DB) migrateV2toV3() error {
	// censor_precedents is a GORM-managed table; it may not exist
	// in databases that don't use GORM (e.g., the local SQLite store)
	var tableExists bool
	err := db.conn.QueryRow(
		"SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='censor_precedents'",
	).Scan(&tableExists)
	if err != nil {
		return fmt.Errorf("check censor_precedents existence: %w", err)
	}

	if tableExists {
		migrations := []string{
			`ALTER TABLE censor_precedents ADD COLUMN username TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE censor_precedents ADD COLUMN project TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS idx_censor_precedents_user_project ON censor_precedents(username, project)`,
		}
		for _, m := range migrations {
			if _, err := db.conn.Exec(m); err != nil {
				return fmt.Errorf("exec %q: %w", m, err)
			}
		}
	}
	if _, err := db.conn.Exec(
		"INSERT INTO schema_version (version, applied_at) VALUES (?, unixepoch())",
		3,
	); err != nil {
		return fmt.Errorf("record version 3: %w", err)
	}
	slog.Debug("migrated schema v2→v3: added username/project to censor_precedents")
	return nil
}

// migrateV3toV4 adds a unique index on messages(session_id, sequence) so
// that INSERT OR IGNORE works correctly for incremental upsert saves.
// Also drops the redundant non-unique index on the same columns.
func (db *DB) migrateV3toV4() error {
	migrations := []string{
		`DROP INDEX IF EXISTS idx_messages_session`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, sequence)`,
	}
	for _, m := range migrations {
		if _, err := db.conn.Exec(m); err != nil {
			return fmt.Errorf("exec %q: %w", m, err)
		}
	}
	if _, err := db.conn.Exec(
		"INSERT INTO schema_version (version, applied_at) VALUES (?, unixepoch())",
		4,
	); err != nil {
		return fmt.Errorf("record version 4: %w", err)
	}
	slog.Debug("migrated schema v3→v4: unique index on messages(session_id, sequence)")
	return nil
}

// migrateV4toV5 renames stored event string values from the old
// "shogunate_started"/"shogunate_ready" to "court_started"/"court_ready"
// in both tian_events and tian_event_dlq tables. These tables are created
// by GORM AutoMigrate, not by the raw Schema SQL, so they may not exist
// when the migration runs on a DB that hasn't been opened by the app yet.
func (db *DB) migrateV4toV5() error {
	migrations := []string{
		`UPDATE tian_events SET event_type = 'court_started' WHERE event_type = 'shogunate_started'`,
		`UPDATE tian_events SET event_type = 'court_ready' WHERE event_type = 'shogunate_ready'`,
		`UPDATE tian_event_dlq SET event_type = 'court_started' WHERE event_type = 'shogunate_started'`,
		`UPDATE tian_event_dlq SET event_type = 'court_ready' WHERE event_type = 'shogunate_ready'`,
	}
	for _, m := range migrations {
		if _, err := db.conn.Exec(m); err != nil {
			// Tables may not exist yet (created by GORM AutoMigrate).
			// Skip gracefully — the rename will apply on the next app
			// startup if old values are present.
			continue
		}
	}
	if _, err := db.conn.Exec(
		"INSERT INTO schema_version (version, applied_at) VALUES (?, unixepoch())",
		5,
	); err != nil {
		return fmt.Errorf("record version 5: %w", err)
	}
	slog.Debug("migrated schema v4→v5: renamed shogunate_* event values to court_*")
	return nil
}

// migrateV5toV6 renames stale minister_id values in the seals table.
// The 三省 alignment (edict 629) renamed minister roles:
//   - "sage" → "chancellor" (the second seal in the chain)
//
// Existing databases still have seals with minister_id='sage' from before
// the rename. This migration updates them so the seal chain code (which now
// checks for 'chancellor') recognizes old seals.
func (db *DB) migrateV5toV6() error {
	// The seals table is created by GORM AutoMigrate, so it may not exist
	// when the migration runs on a DB that hasn't been opened by the app yet.
	var tableExists bool
	err := db.conn.QueryRow(
		"SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='seals'",
	).Scan(&tableExists)
	if err != nil {
		return fmt.Errorf("check seals table existence: %w", err)
	}

	if tableExists {
		_, err := db.conn.Exec(
			`UPDATE seals SET minister_id = 'chancellor' WHERE minister_id = 'sage'`,
		)
		if err != nil {
			return fmt.Errorf("rename sage seals to chancellor: %w", err)
		}
	}

	if _, err := db.conn.Exec(
		"INSERT INTO schema_version (version, applied_at) VALUES (?, unixepoch())",
		6,
	); err != nil {
		return fmt.Errorf("record version 6: %w", err)
	}
	slog.Debug("migrated schema v5→v6: renamed sage seals to chancellor")
	return nil
}

// migrateV6toV7 adds the stale_at column to the seals table.
// This column tracks seal invalidation when an edict's intent changes.
func (db *DB) migrateV6toV7() error {
	var tableExists bool
	err := db.conn.QueryRow(
		"SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='seals'",
	).Scan(&tableExists)
	if err != nil {
		return fmt.Errorf("check seals table existence: %w", err)
	}

	if tableExists {
		// Check if the column already exists (idempotent)
		var colExists bool
		err := db.conn.QueryRow(
			"SELECT COUNT(*) > 0 FROM pragma_table_info('seals') WHERE name = 'stale_at'",
		).Scan(&colExists)
		if err != nil {
			return fmt.Errorf("check stale_at column existence: %w", err)
		}
		if !colExists {
			_, err := db.conn.Exec(`ALTER TABLE seals ADD COLUMN stale_at DATETIME DEFAULT NULL`)
			if err != nil {
				return fmt.Errorf("add stale_at column: %w", err)
			}
		}
	}

	if _, err := db.conn.Exec(
		"INSERT INTO schema_version (version, applied_at) VALUES (?, unixepoch())",
		7,
	); err != nil {
		return fmt.Errorf("record version 7: %w", err)
	}
	slog.Debug("migrated schema v6→v7: added stale_at column to seals")
	return nil
}

// registerRegexpFunction adds REGEXP support to SQLite
func (db *DB) registerRegexpFunction() error {
	// Note: The sqlite driver from modernc.org/sqlite doesn't support
	// sql.Conn.Raw() for custom functions. We'll implement REGEXP
	// filtering in Go code after querying instead.
	// This is a placeholder - we'll handle regex in application layer.

	return nil
}

// PostGormMigrate runs data migrations that depend on GORM-managed tables
// existing. It must be called after db.AutoMigrate has run so that tables
// like `seals` are guaranteed to exist. This is idempotent and safe to run
// on every startup.
func PostGormMigrate(db *gorm.DB) error {
	result := db.Exec(`UPDATE seals SET minister_id = 'chancellor' WHERE minister_id = 'sage'`)
	if result.Error != nil {
		return fmt.Errorf("rename sage seals to chancellor: %w", result.Error)
	}
	slog.Debug("post-GORM migration: renamed sage seals to chancellor",
		"rows_affected", result.RowsAffected)

	// Backfill censor_precedents.edict_id for per-manifest precedents from the
	// owning forge_manifests row. Edict-level rows (manifest_id = '') are left
	// at 0 (unknown owner). Idempotent: it only fills rows where edict_id = 0.
	backfill := db.Exec(`
		UPDATE censor_precedents SET edict_id = (
			SELECT fm.edict_id FROM forge_manifests fm
			WHERE fm.manifest_id = censor_precedents.manifest_id
		)
		WHERE censor_precedents.edict_id = 0
		  AND censor_precedents.manifest_id != ''
		  AND EXISTS (
		    SELECT 1 FROM forge_manifests fm
		    WHERE fm.manifest_id = censor_precedents.manifest_id
		  )`)
	if backfill.Error != nil {
		return fmt.Errorf("backfill censor_precedents.edict_id: %w", backfill.Error)
	}
	slog.Debug("post-GORM migration: backfilled censor_precedents.edict_id",
		"rows_affected", backfill.RowsAffected)
	return nil
}

// Close closes the database connection
func (db *DB) Close() error {
	if db.conn != nil {
		return db.conn.Close()
	}
	return nil
}

// Conn returns the underlying database connection
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// Path returns the database file path
func (db *DB) Path() string {
	return db.path
}

// Vacuum optimizes the database file
func (db *DB) Vacuum() error {
	_, err := db.conn.Exec("VACUUM")
	return err
}

// Stats returns database statistics
func (db *DB) Stats() (map[string]int64, error) {
	stats := make(map[string]int64)

	// Count repositories
	var repoCount int64
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM repositories").Scan(&repoCount); err != nil {
		return nil, err
	}
	stats["repositories"] = repoCount

	// Count branches
	var branchCount int64
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM branches").Scan(&branchCount); err != nil {
		return nil, err
	}
	stats["branches"] = branchCount

	// Count sessions
	var sessionCount int64
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessionCount); err != nil {
		return nil, err
	}
	stats["sessions"] = sessionCount

	// Count messages
	var messageCount int64
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM messages").Scan(&messageCount); err != nil {
		return nil, err
	}
	stats["messages"] = messageCount

	// Count prompt history
	var promptCount int64
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM prompt_history").Scan(&promptCount); err != nil {
		return nil, err
	}
	stats["prompt_history"] = promptCount

	// Count command history
	var commandCount int64
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM command_history").Scan(&commandCount); err != nil {
		return nil, err
	}
	stats["command_history"] = commandCount

	// Count workflows (if table exists)
	var workflowCount int64
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM workflows").Scan(&workflowCount); err == nil {
		stats["workflows"] = workflowCount
	}

	// Count workflow steps (if table exists)
	var stepCount int64
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM workflow_steps").Scan(&stepCount); err == nil {
		stats["workflow_steps"] = stepCount
	}

	return stats, nil
}
