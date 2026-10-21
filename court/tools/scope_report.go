package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// ScopeMatch is one (username, project) bucket holding rows for a natural key.
type ScopeMatch struct {
	Username string `json:"username" gorm:"column:username"`
	Project  string `json:"project" gorm:"column:project"`
	Count    int64  `json:"count" gorm:"column:count"`
}

// ScopeReport is the canonical shape returned by a scoped Court read that
// matched zero rows. It always names the queried (username, project) and, when
// rows exist for the same natural key under a different scope, lists them — so
// an empty scope can never be silently mistaken for "no rows at all" (e968).
//
// Message is the human-readable rendering, e.g.
//
//	0 rows for username="X" project="daonb/data-poisoning-benchmark";
//	10 rows exist for username="Y" project="oct7-io/data-poisoning-benchmark"
type ScopeReport struct {
	Rows     int          `json:"rows"`
	Username string       `json:"username"`
	Project  string       `json:"project"`
	Nearby   []ScopeMatch `json:"nearby,omitempty"`
	Message  string       `json:"message"`
}

// scopeMatches returns the (username, project) buckets holding rows for a
// natural key. A non-nil error means the lookup itself failed ("could not
// look"), which callers must not conflate with "matches nowhere".
func scopeMatches(db *gorm.DB, model any, naturalWhere string, args []any) ([]ScopeMatch, error) {
	if db == nil {
		return nil, fmt.Errorf("no database connection")
	}
	query := db.Model(model).Select("username, project, COUNT(*) AS count").Group("username, project")
	if naturalWhere != "" {
		query = query.Where(naturalWhere, args...)
	}
	var rows []ScopeMatch
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// scopeMissReport returns the fail-loud scope report for a natural key that
// matched zero rows in the queried scope but does match under some other scope,
// with ok=true. When the key matches nowhere (or the lookup fails), ok is false
// and the caller should surface its normal not-found error.
func scopeMissReport(db *gorm.DB, model any, naturalWhere string, args []any, username, project string) (string, bool) {
	rows, err := scopeMatches(db, model, naturalWhere, args)
	if err != nil || len(rows) == 0 {
		return "", false
	}
	return emptyScopeReport(db, model, naturalWhere, args, username, project), true
}

// emptyScopeReport renders the fail-loud shape for a scoped Court read that
// matched zero rows under (username, project). naturalWhere/args select the
// natural key (an edict id, an edict_id column, …); pass "" to report over the
// whole table. The result is JSON so every read tool returns one shape. When
// the nearby-match lookup itself fails the message says so rather than
// asserting the key matches nowhere.
func emptyScopeReport(db *gorm.DB, model any, naturalWhere string, args []any, username, project string) string {
	report := ScopeReport{Rows: 0, Username: username, Project: project}
	rows, err := scopeMatches(db, model, naturalWhere, args)
	if err != nil {
		report.Message = fmt.Sprintf("0 rows for username=%q project=%q; nearby-scope check failed: %v", username, project, err)
	} else {
		for _, r := range rows {
			if r.Username == username && r.Project == project {
				continue
			}
			report.Nearby = append(report.Nearby, r)
		}
		report.Message = renderScopeMessage(report)
	}

	encoded, err2 := json.Marshal(report)
	if err2 != nil {
		return report.Message
	}
	return string(encoded)
}

// renderScopeMessage builds the human-readable text for a ScopeReport.
func renderScopeMessage(r ScopeReport) string {
	scope := fmt.Sprintf("username=%q project=%q", r.Username, r.Project)
	if len(r.Nearby) == 0 {
		return fmt.Sprintf("0 rows for %s; no rows exist for this key under any other scope", scope)
	}
	var nearby []string
	for _, n := range r.Nearby {
		nearby = append(nearby, fmt.Sprintf("%d rows exist for username=%q project=%q", n.Count, n.Username, n.Project))
	}
	return fmt.Sprintf("0 rows for %s; %s", scope, strings.Join(nearby, "; "))
}

// scopeReportMessage reports whether result carries a human-readable Court-read
// report (a fail-loud scope report or a status-filter-miss message) rather than
// a normal data payload, returning its message for display. Formats use this so
// a user sees the explanation, not a raw JSON blob.
func scopeReportMessage(result string) (string, bool) {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil || payload.Message == "" {
		return "", false
	}
	return payload.Message, true
}
