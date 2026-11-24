package storage

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MalformedMarkerKey is the single JSON key used to represent a database value
// that failed to parse. Scanning such a row degrades to a bounded marker
// instead of returning an error, which would otherwise make GORM's Find abort
// the whole result set on the first bad row (see edict 857).
const MalformedMarkerKey = "__malformed__"

// malformedMarkerLimit bounds how much of a malformed raw value is retained in
// the marker, keeping it small and out of log lines.
const malformedMarkerLimit = 200

// JSON is a custom type for storing JSON data in the database
type JSON map[string]interface{}

// Value implements driver.Valuer for JSON
func (j JSON) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

// Scan implements sql.Scanner for JSON. Malformed input is tolerated: the
// value becomes a bounded marker so a single poisoned row cannot abort a
// multi-row query. The failure is logged by key/type only, never by raw value.
func (j *JSON) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	raw, ok := scanBytes(value)
	if !ok {
		*j = nil
		slog.Warn("JSON scan: unsupported value type", "type", fmt.Sprintf("%T", value))
		return nil
	}
	if err := json.Unmarshal(raw, j); err != nil {
		*j = JSON{MalformedMarkerKey: trimRawValue(raw)}
		slog.Warn("JSON scan: malformed JSON value", "error", err)
		return nil
	}
	return nil
}

// StringArray is a custom type for storing string arrays in the database
type StringArray []string

// Value implements driver.Valuer for StringArray
func (s StringArray) Value() (driver.Value, error) {
	if s == nil {
		return nil, nil
	}
	return json.Marshal(s)
}

// Scan implements sql.Scanner for StringArray. Like JSON.Scan, malformed input
// is tolerated and logged without embedding the raw value.
func (s *StringArray) Scan(value interface{}) error {
	if value == nil {
		*s = nil
		return nil
	}
	raw, ok := scanBytes(value)
	if !ok {
		raw = nil
		slog.Warn("StringArray scan: unsupported value type", "type", fmt.Sprintf("%T", value))
		return nil
	}
	if err := json.Unmarshal(raw, s); err != nil {
		*s = nil
		slog.Warn("StringArray scan: malformed JSON value", "error", err)
		return nil
	}
	return nil
}

// scanBytes normalises a driver value to bytes; drivers hand back either
// []byte or a string.
func scanBytes(value interface{}) ([]byte, bool) {
	switch v := value.(type) {
	case []byte:
		return v, true
	case string:
		return []byte(v), true
	default:
		return nil, false
	}
}

// trimRawValue bounds a raw value to malformedMarkerLimit runes, appending an
// ellipsis when truncated.
func trimRawValue(raw []byte) string {
	runes := []rune(string(raw))
	if len(runes) <= malformedMarkerLimit {
		return string(runes)
	}
	return string(runes[:malformedMarkerLimit]) + "…"
}

// EdictKey is the composite primary key for an edict (id, username, project).
type EdictKey struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Project  string `json:"project"`
}

// EdictStatus represents the current status of an edict
// This is derived from seals and zhengming tables, not stored in edicts table
type EdictStatus string

const (
	// EdictDraft is an edict whose intent has not yet been approved by the Ruler
	// (missing a live (ruler, intent) seal). Derived, never stored.
	//
	// Invariant: draft ⇒ not blocked. The draft check must precede the
	// pending-zhengming derivation in GetEdictStatus so a draft with a pending
	// zhengming derives as EdictDraft, not EdictBlocked.
	EdictDraft          EdictStatus = "draft"
	EdictIntentApproved EdictStatus = "intent-approved"
	EdictActive         EdictStatus = "active"
	EdictBlocked        EdictStatus = "blocked"
	EdictSealed         EdictStatus = "sealed"
	EdictCancelled      EdictStatus = "cancelled"
)

// Edict represents a high-level task/issue being processed by the Court
// Status is derived from seals and zhengming tables - see EdictStatus type
// Primary key is composite: (id, username, project)
type Edict struct {
	ID          uint       `gorm:"primaryKey;autoIncrement"`
	Username    string     `gorm:"primaryKey;column:username"`
	Project     string     `gorm:"primaryKey;column:project"`
	SessionID   string     `gorm:"column:session_id;index"`
	IssueRef    string     `gorm:"column:issue_ref"`
	Summary     string     `gorm:"column:summary"`
	Intent      string     `gorm:"column:intent"`
	CancelledAt *time.Time `gorm:"column:cancelled_at"` // NULL = not cancelled, timestamp = cancelled at this time
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// Key returns the composite key for this edict.
func (e *Edict) Key() EdictKey {
	return EdictKey{ID: e.ID, Username: e.Username, Project: e.Project}
}

// TableName returns the table name for Edict
func (Edict) TableName() string {
	return "edicts"
}

// Seal represents a minister's seal on an edict
type Seal struct {
	SealID     string     `gorm:"primaryKey;column:seal_id"`
	EdictID    uint       `gorm:"column:edict_id;index"`
	Username   string     `gorm:"column:username"`
	Project    string     `gorm:"column:project"`
	MinisterID string     `gorm:"column:minister_id"` // "judge", "chancellor", "ruler"
	Realm      string     `gorm:"column:realm"`       // "intent" or "earth"
	SealedAt   time.Time  `gorm:"column:sealed_at;autoCreateTime"`
	Metadata   JSON       `gorm:"column:metadata;type:json"` // Optional: verdict_id, precedent_id, etc.
	StaleAt    *time.Time `gorm:"column:stale_at"`           // Non-null when invalidated by intent change
}

// TableName returns the table name for Seal
func (Seal) TableName() string {
	return "seals"
}

// ZhengmingQuestion represents a single question with predefined answer options
type ZhengmingQuestion struct {
	Text    string   `json:"text"`
	Summary string   `json:"summary,omitempty"` // Short display text for the UI; Text is used if empty
	Options []string `json:"options"`
}

// ZhengmingQuestions is a slice of ZhengmingQuestion stored as JSON in the database
type ZhengmingQuestions []ZhengmingQuestion

// Value implements driver.Valuer for ZhengmingQuestions
func (q ZhengmingQuestions) Value() (driver.Value, error) {
	if q == nil {
		return nil, nil
	}
	return json.Marshal(q)
}

// Scan implements sql.Scanner for ZhengmingQuestions
func (q *ZhengmingQuestions) Scan(value interface{}) error {
	if value == nil {
		*q = nil
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("failed to unmarshal ZhengmingQuestions value: %v", value)
	}
	return json.Unmarshal(bytes, q)
}

// ZhengmingPriority represents the priority of a clarification request
type ZhengmingPriority string

const (
	PriorityNormal ZhengmingPriority = "normal"
	PriorityUrgent ZhengmingPriority = "urgent"
)

// ZhengmingStatus represents the status of a clarification request.
type ZhengmingStatus string

// Every writer must route through these constants. The live database already
// holds rows written by paths that bypassed the typed set (how 'dismissed' and
// 'cancelled' entered the data), so the vocabulary is closed here for future
// writers (e1010).
const (
	ZhengmingPending  ZhengmingStatus = "pending"
	ZhengmingAnswered ZhengmingStatus = "answered"
	ZhengmingExpired  ZhengmingStatus = "expired"
	// ZhengmingDismissed marks a prompt the Court retired because a newer
	// execution of the same work superseded it (or a sanctioned manual
	// dismissal). The edict still stands and may be gated again. It is NOT
	// 'answered': the record must not claim the Ruler spoke when the Court
	// retired the prompt. The reason is recorded in DispositionReason.
	ZhengmingDismissed ZhengmingStatus = "dismissed"
	// ZhengmingSupersededByEdict marks a prompt whose target edict ended —
	// Ruler-sealed or cancelled — so the question no longer applies. Distinct
	// from ZhengmingExpired (the clock ran out) and ZhengmingDismissed (the
	// work was superseded while the edict stands); the three must never be
	// used interchangeably (e1037).
	ZhengmingSupersededByEdict ZhengmingStatus = "superseded_by_edict"
)

// TerminalZhengmingStatus reports whether status is one of the three terminal
// dispositions a Court-initiated retirement may write: dismissed (a newer
// execution superseded the work), expired (the deadline elapsed and nobody
// answered), or superseded_by_edict (the target edict ended — sealed or
// cancelled). The set is closed: a caller cannot admit a fourth terminal
// status by accident (e1037).
func TerminalZhengmingStatus(status ZhengmingStatus) bool {
	switch status {
	case ZhengmingDismissed, ZhengmingExpired, ZhengmingSupersededByEdict:
		return true
	}
	return false
}

// Legacy zhengming vocabulary note (e1037): the live database holds terminal
// rows written before the typed set above existed — 'cancelled' (15 rows) and
// 'resolved' (8 rows), alongside the typed 'dismissed' (1120) and 'expired'
// (244). Those legacy values predate the typed set and are left untouched;
// history is not rewritten and no second spelling of any typed status is
// minted. New writers must use the typed constants only.

// Zhengming represents a clarification request from a minister
type Zhengming struct {
	RequestID  string             `gorm:"primaryKey;column:request_id"`
	EdictID    uint               `gorm:"column:edict_id;index"`
	Username   string             `gorm:"column:username"`
	Project    string             `gorm:"column:project"`
	MinisterID string             `gorm:"column:minister_id"`
	SessionID  string             `gorm:"column:session_id"`
	Questions  ZhengmingQuestions `gorm:"column:question;type:text"`
	Answer     string             `gorm:"column:answer"`
	// DispositionReason records why the Court retired a prompt nobody
	// answered (a superseded abort prompt, or a sanctioned manual dismissal).
	// It is deliberately distinct from Answer, which must keep meaning "the
	// Ruler replied" — a Court-authored retirement must not masquerade as the
	// Ruler speaking (e1010).
	DispositionReason string            `gorm:"column:disposition_reason"`
	Priority          ZhengmingPriority `gorm:"column:priority"`
	Status            ZhengmingStatus   `gorm:"column:status"`
	TimeoutAt         time.Time         `gorm:"column:timeout_at"`
	AnsweredAt        *time.Time        `gorm:"column:answered_at"`
	CreatedAt         time.Time         `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt         time.Time         `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName returns the table name for Zhengming
func (Zhengming) TableName() string {
	return "zhengming_requests"
}

// CourtEvent represents an event type emitted by the Court lifecycle.
type CourtEvent string

const (
	EventCourtStarted      CourtEvent = "court_started"
	EventCourtReady        CourtEvent = "court_ready"
	EventEdictAssigned     CourtEvent = "edict_assigned"
	EventEdictCreated      CourtEvent = "edict_created"
	EventForgeCommitted    CourtEvent = "forge_committed"
	EventManifestCommitted CourtEvent = "manifest_committed"
	EventManifestRejected  CourtEvent = "manifest_rejected"
	EventRitualEnacted     CourtEvent = "ritual_enacted"
	EventRitualStarted     CourtEvent = "ritual_started"
	EventRitualCompleted   CourtEvent = "ritual_completed"
	EventRitualFailed      CourtEvent = "ritual_failed"
	EventRitualAborted     CourtEvent = "ritual_aborted"
	EventStepStarted       CourtEvent = "step_started"
	EventStepCompleted     CourtEvent = "step_completed"
	EventStepFailed        CourtEvent = "step_failed"
	EventLingCreated       CourtEvent = "ling_created"
	EventLingStarted       CourtEvent = "ling_started"
	EventLingCompleted     CourtEvent = "ling_completed"
	EventLingFailed        CourtEvent = "ling_failed"
	// EventLingIgnitionSkipped records that ignition was suppressed by config
	// (court.max_parallel_lings<=0); the ling itself is untouched and
	// remains pending.
	EventLingIgnitionSkipped CourtEvent = "ling_ignition_skipped"
	EventZhengmingNeeded     CourtEvent = "zhengming_needed"
	EventZhengmingAnswered   CourtEvent = "zhengming_answered"
	// EventZhengmingDismissed records that the Court retired a pending
	// zhengming request (superseded abort prompt, or sanctioned manual
	// disposition) without the Ruler answering it. Distinct from
	// EventZhengmingAnswered: the ledger must show the Court closed the
	// prompt, not that the Ruler spoke (e1010).
	EventZhengmingDismissed CourtEvent = "zhengming_dismissed"
	EventEdictCancelled     CourtEvent = "edict_cancelled"
	EventSealGranted        CourtEvent = "seal_granted"
	EventEdictSealed        CourtEvent = "edict_sealed"
	// EventWorktreeMergeFailed records that a ruler seal could not land the
	// edict's worktree on the merge target (or remove it afterwards). The seal
	// is already recorded, so without this event the seal looks like an
	// unqualified success while the work never lands.
	EventWorktreeMergeFailed CourtEvent = "worktree_merge_failed"
	EventWebhookReceived     CourtEvent = "webhook_received"
)

// TianEvent represents an event in the Tian ledger
type TianEvent struct {
	ID        uint       `gorm:"primaryKey;autoIncrement"`
	EdictID   uint       `gorm:"column:edict_id;index"`
	Username  string     `gorm:"column:username"`
	Project   string     `gorm:"column:project"`
	EventType CourtEvent `gorm:"column:event_type"`
	Payload   JSON       `gorm:"column:payload;type:json"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName returns the table name for TianEvent
func (TianEvent) TableName() string {
	return "tian_events"
}

// TianEventDLQ represents a dead-letter queue entry for failed event processing
type TianEventDLQ struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	OriginalID   uint       `gorm:"column:original_id"`
	EdictID      uint       `gorm:"column:edict_id;index"`
	Username     string     `gorm:"column:username"`
	Project      string     `gorm:"column:project"`
	EventType    CourtEvent `gorm:"column:event_type"`
	Payload      JSON       `gorm:"column:payload;type:json"`
	ErrorMessage string     `gorm:"column:error_message"`
	RetryCount   int        `gorm:"column:retry_count"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName returns the table name for TianEventDLQ
func (TianEventDLQ) TableName() string {
	return "tian_event_dlq"
}

// LingStatus represents the status of a ling (sub-task)
type LingStatus string

const (
	LingPending    LingStatus = "pending"
	LingInProgress LingStatus = "in_progress"
	LingDone       LingStatus = "done"
	LingBlocked    LingStatus = "blocked"
)

// Ling represents a sub-task or step in an edict's execution plan
type Ling struct {
	LingID       string      `gorm:"primaryKey;column:ling_id"`
	EdictID      uint        `gorm:"column:edict_id;index"`
	Username     string      `gorm:"column:username"`
	Project      string      `gorm:"column:project"`
	Description  string      `gorm:"column:description"`
	Dependencies StringArray `gorm:"column:dependencies;type:json"`
	Status       LingStatus  `gorm:"column:status"`
	Minister     string      `gorm:"column:minister"`
	CreatedAt    time.Time   `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time   `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName returns the table name for Ling
func (Ling) TableName() string {
	return "lings"
}

// ManifestStatus represents the status of a forge manifest
type ManifestStatus string

const (
	ManifestForged   ManifestStatus = "forged"
	ManifestLive     ManifestStatus = "live"
	ManifestQuenched ManifestStatus = "quenched"
	ManifestRejected ManifestStatus = "rejected"
)

// ForgeManifest represents a code change manifest from the Forge
type ForgeManifest struct {
	ManifestID string         `gorm:"primaryKey;column:manifest_id"`
	EdictID    uint           `gorm:"column:edict_id;index"`
	Username   string         `gorm:"column:username"`
	Project    string         `gorm:"column:project"`
	LingID     string         `gorm:"column:ling_id"`
	FilePath   string         `gorm:"column:file_path"`
	FuncName   string         `gorm:"column:func_name"`
	ContentSHA string         `gorm:"column:content_sha"`
	CommitHash string         `gorm:"column:commit_hash"`
	Status     ManifestStatus `gorm:"column:status"`
	VerdictID  string         `gorm:"column:verdict_id"`
	CreatedAt  time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time      `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName returns the table name for ForgeManifest
func (ForgeManifest) TableName() string {
	return "forge_manifests"
}

// VerdictOutcome represents the outcome of a judge's verdict
type VerdictOutcome string

const (
	VerdictPassed VerdictOutcome = "passed"
	VerdictFailed VerdictOutcome = "failed"
)

// JudgeVerdict.EdictID contract (正名) — mirrors CensorPrecedent.EdictID:
//
//	EdictID == 0 means the verdict has no owning edict (legacy rows, or
//	  deliberately account-level records). Edict-level verdicts with
//	  EdictID == 0 are gate-irrelevant: they must not block any edict.
//	EdictID > 0 is the id of the owning edict; an edict-level verdict
//	  (manifest_id == '', test_suite == 'edict') only affects that edict.
//
// No backfill: pre-existing rows have EdictID == 0 and become account
// history, never gate-relevant.
type JudgeVerdict struct {
	VerdictID  string         `gorm:"primaryKey;column:verdict_id"`
	ManifestID string         `gorm:"column:manifest_id;index"`
	EdictID    uint           `gorm:"column:edict_id;index"` // See the JudgeVerdict.EdictID contract above.
	Username   string         `gorm:"column:username"`
	Project    string         `gorm:"column:project"`
	TestSuite  string         `gorm:"column:test_suite"`
	Outcome    VerdictOutcome `gorm:"column:outcome"`
	Evidence   JSON           `gorm:"column:evidence;type:json"`
	CreatedAt  time.Time      `gorm:"column:created_at;autoCreateTime"`
}

// TableName returns the table name for JudgeVerdict
func (JudgeVerdict) TableName() string {
	return "judge_verdicts"
}

// PrecedentRuling represents the ruling of a censor precedent
type PrecedentRuling string

const (
	PrecedentApproved PrecedentRuling = "approved"
	PrecedentRejected PrecedentRuling = "rejected"
)

// CensorPrecedent.EdictID contract (正名):
//
//	EdictID == 0 means the precedent has no owning edict. Two distinct cases
//	  collapse onto this single sentinel:
//	    1. Unknown/legacy: rows created before the edict_id column existed.
//	    2. Deliberately edict-level: rulings that apply to the codebase as a
//	       whole (e.g. manifest_id == "") rather than to one edict.
//	EdictID > 0 is the id of the owning edict.
//
// NULL is not part of this contract; use HasEdict() to test ownership and
// never compare the column directly against NULL.

// CensorPrecedent represents an ethics review precedent from the Censor
type CensorPrecedent struct {
	PrecedentID   string          `gorm:"primaryKey;column:precedent_id"`
	ManifestID    string          `gorm:"column:manifest_id;index"`
	EdictID       uint            `gorm:"column:edict_id;index"` // See the CensorPrecedent.EdictID contract above; use HasEdict().
	Username      string          `gorm:"column:username;not null;default:''"`
	Project       string          `gorm:"column:project;not null;default:''"`
	Principle     string          `gorm:"column:principle"`
	Ruling        PrecedentRuling `gorm:"column:ruling"`
	Justification string          `gorm:"column:justification"`
	CreatedAt     time.Time       `gorm:"column:created_at;autoCreateTime"`
}

// HasEdict reports whether the precedent is owned by an edict. It is the
// sanctioned way to test ownership under the EdictID contract above.
func (p CensorPrecedent) HasEdict() bool {
	return p.EdictID != 0
}

// TableName returns the table name for CensorPrecedent
func (CensorPrecedent) TableName() string {
	return "censor_precedents"
}

// Incident represents a production incident tracked independently of any minister
type Incident struct {
	IncidentID  string    `gorm:"primaryKey;column:incident_id"`
	Description string    `gorm:"column:description"`
	Severity    string    `gorm:"column:severity"`
	Status      string    `gorm:"column:status;default:open"`
	RootCause   string    `gorm:"column:root_cause"`
	Resolution  string    `gorm:"column:resolution"`
	EdictID     uint      `gorm:"column:edict_id;index"`
	CommitHash  string    `gorm:"column:commit_hash;index"`
	Username    string    `gorm:"column:username"`
	Project     string    `gorm:"column:project"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName returns the table name for Incident
func (Incident) TableName() string {
	return "incidents"
}

// RulerCouncil represents a high-stakes decision requiring human approval
type RulerCouncil struct {
	CouncilID  string    `gorm:"primaryKey;column:council_id"`
	EdictID    uint      `gorm:"column:edict_id;index"`
	Username   string    `gorm:"column:username"`
	Project    string    `gorm:"column:project"`
	Decision   string    `gorm:"column:decision"`
	Approved   bool      `gorm:"column:approved"`
	ApprovedBy string    `gorm:"column:approved_by"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName returns the table name for RulerCouncil
func (RulerCouncil) TableName() string {
	return "ruler_councils"
}

// RitualGuardCheckpoint represents a checkpoint for the Ritual Guard's event processing
type RitualGuardCheckpoint struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	EventID   uint      `gorm:"column:event_id"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName returns the table name for RitualGuardCheckpoint
func (RitualGuardCheckpoint) TableName() string {
	return "ritual_guard_checkpoint"
}

// MCPToolClassification persists a Ruler-sealed Three Realms classification
// for an MCP tool. Primary key is composite: (username, project, server_id, tool_name),
// allowing per-project overrides while supporting user-wide cross-project reuse
// on matching schema_hash and command_hash.
type MCPToolClassification struct {
	Username    string    `gorm:"primaryKey;column:username;index:idx_mcp_tool_class_reuse,priority:1"`
	Project     string    `gorm:"primaryKey;column:project"`
	ServerID    string    `gorm:"primaryKey;column:server_id;index:idx_mcp_tool_class_reuse,priority:2"`
	ToolName    string    `gorm:"primaryKey;column:tool_name;index:idx_mcp_tool_class_reuse,priority:3"`
	Permissions string    `gorm:"column:permissions"` // 9-char rwx Three Realms string (--------- = sealed deny)
	SchemaHash  string    `gorm:"column:schema_hash;index:idx_mcp_tool_class_reuse,priority:4"`
	CommandHash string    `gorm:"column:command_hash;index:idx_mcp_tool_class_reuse,priority:5"`
	SealedAt    time.Time `gorm:"column:sealed_at;autoCreateTime"`
	SealedBy    string    `gorm:"column:sealed_by"`
}

// TableName returns the table name for MCPToolClassification
func (MCPToolClassification) TableName() string {
	return "mcp_tool_classifications"
}

// LookupMCPToolClassification finds a sealed classification matching both
// schemaHash and commandHash strictly within username (never crossing username
// boundaries). It checks (username, project, server_id, tool_name) first so
// per-project rows take precedence; if no row exists for the current project,
// it falls back to the most recently sealed row for (username, server_id,
// tool_name) with matching schema_hash and command_hash across the same user's
// other projects.
//
// Returns (nil, nil) when no matching classification is found (or when the
// current project's row has a different schema_hash or command_hash and must
// be re-classified).
func LookupMCPToolClassification(db *gorm.DB, username, project, serverID, toolName, schemaHash, commandHash string) (*MCPToolClassification, error) {
	if db == nil {
		return nil, fmt.Errorf("lookup mcp tool classification: nil database")
	}
	if username == "" {
		return nil, fmt.Errorf("lookup mcp tool classification: username is required")
	}
	if serverID == "" || toolName == "" {
		return nil, fmt.Errorf("lookup mcp tool classification: server_id and tool_name are required")
	}
	if schemaHash == "" || commandHash == "" {
		return nil, nil
	}

	var current []MCPToolClassification
	if err := db.Where(
		"username = ? AND project = ? AND server_id = ? AND tool_name = ?",
		username, project, serverID, toolName,
	).Limit(1).Find(&current).Error; err != nil {
		return nil, fmt.Errorf("lookup mcp tool classification (project): %w", err)
	}
	if len(current) > 0 {
		if current[0].SchemaHash == schemaHash && current[0].CommandHash == commandHash {
			return &current[0], nil
		}
		// Current project has an explicit row whose schema or command changed;
		// do not return a stale classification or override it from another project.
		return nil, nil
	}

	var fallback []MCPToolClassification
	if err := db.Where(
		"username = ? AND server_id = ? AND tool_name = ? AND schema_hash = ? AND command_hash = ?",
		username, serverID, toolName, schemaHash, commandHash,
	).Order("sealed_at DESC").Limit(1).Find(&fallback).Error; err != nil {
		return nil, fmt.Errorf("lookup mcp tool classification (fallback): %w", err)
	}
	if len(fallback) == 0 {
		return nil, nil
	}
	return &fallback[0], nil
}

// SaveMCPToolClassification upserts a sealed MCP tool classification under its
// composite primary key (username, project, server_id, tool_name).
func SaveMCPToolClassification(db *gorm.DB, c *MCPToolClassification) error {
	if db == nil {
		return fmt.Errorf("save mcp tool classification: nil database")
	}
	if c == nil {
		return fmt.Errorf("save mcp tool classification: nil classification")
	}
	if c.Username == "" {
		return fmt.Errorf("save mcp tool classification: username is required")
	}
	if c.ServerID == "" || c.ToolName == "" {
		return fmt.Errorf("save mcp tool classification: server_id and tool_name are required")
	}
	if c.SealedAt.IsZero() {
		c.SealedAt = time.Now()
	}

	err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "username"},
			{Name: "project"},
			{Name: "server_id"},
			{Name: "tool_name"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"permissions", "schema_hash", "command_hash", "sealed_at", "sealed_by"}),
	}).Create(c).Error
	if err != nil {
		return fmt.Errorf("save mcp tool classification: %w", err)
	}
	return nil
}

// ListMCPToolClassifications returns sealed MCP tool classifications strictly
// bounded to username. When project is non-empty, results are filtered to that
// project; when project is empty, all projects for username are returned.
func ListMCPToolClassifications(db *gorm.DB, username, project string) ([]MCPToolClassification, error) {
	if db == nil {
		return nil, fmt.Errorf("list mcp tool classifications: nil database")
	}
	if username == "" {
		return nil, fmt.Errorf("list mcp tool classifications: username is required")
	}

	q := db.Where("username = ?", username)
	if project != "" {
		q = q.Where("project = ?", project)
	}
	var rows []MCPToolClassification
	if err := q.Order("project ASC, server_id ASC, tool_name ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list mcp tool classifications: %w", err)
	}
	return rows, nil
}
