package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/afittestide/asimi/internal/utils"
)

// AsimiSQLTool executes SQL queries against the Court database through the
// embedded SQLite driver (via GORM raw SQL). It has no external dependency on
// a sqlite3 CLI binary, which is absent in minimal containers.
type AsimiSQLTool struct {
	DB          *gorm.DB
	ProjectRoot string
}

func (t AsimiSQLTool) Name() string {
	return "asimisql"
}

func (t AsimiSQLTool) Description() string {
	return `Execute SQL against the Court database.

Schema reference (read before querying — do not guess tables or columns):
- storage/court_schema.go — Court tables: edicts, seals, zhengming_requests, tian_events, tian_event_dlq, lings, forge_manifests, judge_verdicts, censor_precedents, incidents, ruler_councils, ritual_guard_checkpoint
- storage/schema.go — core tables (sessions, messages, repositories, branches, ritual_executions, ritual_step_states, ...)

Most Court tables carry username and project columns; scope queries by both, e.g.
	SELECT id, intent, created_at FROM edicts WHERE username = 'daonb' AND project = 'afittestide/asimi-cli' ORDER BY created_at DESC LIMIT 5;

Note: the edicts primary key column is id (there is no edict_id column); other tables reference it via edict_id.

Status values:
- Edicts: active, blocked, sealed, cancelled (derived from seals/zhengming; use transition_edict, not direct SQL)
- Manifests: forged, live, quenched, rejected
- Verdicts: passed, failed
- Precedents: approved, rejected`
}

func (t AsimiSQLTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if params.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	if t.DB == nil {
		return "", fmt.Errorf("sqlite3 error: no database handle configured")
	}

	query := strings.TrimSpace(params.Query)
	// Single-statement guard: the previous sqlite3 CLI allowed one query per
	// invocation; keep the same contract and avoid multi-statement injection.
	if strings.Contains(strings.TrimSuffix(query, ";"), ";") {
		return "", fmt.Errorf("sqlite3 error: exactly one SQL statement is required")
	}

	// Route by statement shape: SELECT/PRAGMA/EXPLAIN return rows, everything
	// else is executed for its affected-rows effect.
	upper := strings.ToUpper(query)
	switch {
	case strings.HasPrefix(upper, "SELECT"), strings.HasPrefix(upper, "PRAGMA"),
		strings.HasPrefix(upper, "WITH"), strings.HasPrefix(upper, "EXPLAIN"):
		rows, err := t.DB.WithContext(ctx).Raw(query).Rows()
		if err != nil {
			return "", fmt.Errorf("sqlite3 error: %w", err)
		}
		defer rows.Close()
		return formatRows(rows)
	default:
		res := t.DB.WithContext(ctx).Exec(query)
		if res.Error != nil {
			return "", fmt.Errorf("sqlite3 error: %w", res.Error)
		}
		return fmt.Sprintf(`{"status":"ok","rows_affected":%d}`, res.RowsAffected), nil
	}
}

// formatRows converts sql.Rows into a JSON array of column→value objects.
func formatRows(rows *sql.Rows) (string, error) {
	cols, err := rows.Columns()
	if err != nil {
		return "", fmt.Errorf("sqlite3 error: %w", err)
	}

	results := make([]map[string]interface{}, 0, 16)
	for rows.Next() {
		values := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", fmt.Errorf("sqlite3 error: %w", err)
		}
		row := make(map[string]interface{}, len(cols))
		for i, col := range cols {
			v := values[i]
			// []byte is not JSON-serializable by encoding/json (it base64s);
			// convert to string for readability, nil stays null.
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[col] = v
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("sqlite3 error: %w", err)
	}
	if len(results) == 0 {
		return `{"status":"ok"}`, nil
	}

	out, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("sqlite3 error: %w", err)
	}
	return string(out), nil
}

func (t AsimiSQLTool) Format(input, result string, err error) string {
	var params struct {
		Query string `json:"query"`
	}
	json.Unmarshal([]byte(input), &params)

	q := params.Query
	if len(q) > 40 {
		q = q[:37] + "..."
	}

	msg := utils.NewMsgBlockBuilder("AsimiSQL")
	msg.WriteLn()

	if err != nil {
		msg.Writef("Error: %v", err)
	} else {
		msg.WriteString(q)
	}

	return msg.String() + "\n"
}

func (t AsimiSQLTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "SQL query to execute",
			},
		},
		"required": []string{"query"},
	}
}
