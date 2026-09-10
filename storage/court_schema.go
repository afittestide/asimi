package storage

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// JSON is a custom type for storing JSON data in the database
type JSON map[string]interface{}

// Value implements driver.Valuer for JSON
func (j JSON) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

// Scan implements sql.Scanner for JSON
func (j *JSON) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("failed to unmarshal JSON value: %v", value)
	}
	return json.Unmarshal(bytes, j)
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

// Scan implements sql.Scanner for StringArray
func (s *StringArray) Scan(value interface{}) error {
	if value == nil {
		*s = nil
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("failed to unmarshal StringArray value: %v", value)
	}
	return json.Unmarshal(bytes, s)
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
	EdictActive    EdictStatus = "active"
	EdictBlocked   EdictStatus = "blocked"
	EdictSealed    EdictStatus = "sealed"
	EdictCancelled EdictStatus = "cancelled"
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

// ZhengmingStatus represents the status of a clarification request
type ZhengmingStatus string

const (
	ZhengmingPending  ZhengmingStatus = "pending"
	ZhengmingAnswered ZhengmingStatus = "answered"
	ZhengmingExpired  ZhengmingStatus = "expired"
)

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
	Priority   ZhengmingPriority  `gorm:"column:priority"`
	Status     ZhengmingStatus    `gorm:"column:status"`
	TimeoutAt  time.Time          `gorm:"column:timeout_at"`
	AnsweredAt *time.Time         `gorm:"column:answered_at"`
	CreatedAt  time.Time          `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time          `gorm:"column:updated_at;autoUpdateTime"`
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
	EventZhengmingNeeded   CourtEvent = "zhengming_needed"
	EventZhengmingAnswered CourtEvent = "zhengming_answered"
	EventEdictCancelled    CourtEvent = "edict_cancelled"
	EventSealGranted       CourtEvent = "seal_granted"
	EventEdictSealed       CourtEvent = "edict_sealed"
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

// JudgeVerdict represents a test/CI verdict from the Judge
type JudgeVerdict struct {
	VerdictID  string         `gorm:"primaryKey;column:verdict_id"`
	ManifestID string         `gorm:"column:manifest_id;index"`
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

// CensorPrecedent represents an ethics review precedent from the Censor
type CensorPrecedent struct {
	PrecedentID   string          `gorm:"primaryKey;column:precedent_id"`
	ManifestID    string          `gorm:"column:manifest_id;index"`
	EdictID       uint            `gorm:"column:edict_id;index"`
	Username      string          `gorm:"column:username;not null;default:''"`
	Project       string          `gorm:"column:project;not null;default:''"`
	Principle     string          `gorm:"column:principle"`
	Ruling        PrecedentRuling `gorm:"column:ruling"`
	Justification string          `gorm:"column:justification"`
	CreatedAt     time.Time       `gorm:"column:created_at;autoCreateTime"`
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
