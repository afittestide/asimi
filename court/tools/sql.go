package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/utils"
)

// AsimiSQLTool executes SQL queries against the Court database.
type AsimiSQLTool struct {
	DBPath      string
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

	// Execute via runner.Run for consistent execution pattern
	runnerInput := runners.Input{
		Command:        "sqlite3 " + t.DBPath + " '" + strings.ReplaceAll(params.Query, "'", "'\\''") + "'",
		Description:    "Execute SQL query",
		BypassApproval: true,
	}

	runnerOutput, err := runners.HostRun(ctx, runnerInput, t.ProjectRoot)
	if err != nil {
		if runnerOutput.Output != "" {
			return "", fmt.Errorf("sqlite3 error: %s: %w", strings.TrimSpace(runnerOutput.Output), err)
		}
		return "", fmt.Errorf("sqlite3 error: %w", err)
	}

	if runnerOutput.ExitCode != "0" {
		msg := strings.TrimSpace(runnerOutput.Output)
		if msg == "" {
			return "", fmt.Errorf("sqlite3 error: exit code %s", runnerOutput.ExitCode)
		}
		return "", fmt.Errorf("sqlite3 error: %s", msg)
	}

	result := strings.TrimSpace(runnerOutput.Output)
	if result == "" {
		return `{"status":"ok"}`, nil
	}
	return result, nil
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
