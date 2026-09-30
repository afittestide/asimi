package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupSQLTestDB opens an in-memory SQLite database via the same embedded
// driver the app uses (no sqlite3 CLI binary required).
func setupSQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)").Error)
	require.NoError(t, db.Exec("INSERT INTO t (name) VALUES ('alpha'), ('beta')").Error)
	return db
}

// TestAsimiSQLTool_SelectViaGORM verifies reads work through the embedded
// driver with the JSON rows contract preserved.
func TestAsimiSQLTool_SelectViaGORM(t *testing.T) {
	db := setupSQLTestDB(t)
	tool := AsimiSQLTool{DB: db, ProjectRoot: t.TempDir()}

	result, err := tool.Call(context.Background(), `{"query":"SELECT id, name FROM t ORDER BY id"}`)
	require.NoError(t, err)
	assert.Contains(t, result, `"name":"alpha"`)
	assert.Contains(t, result, `"name":"beta"`)
}

// TestAsimiSQLTool_WriteViaGORM verifies writes execute and report affected rows.
func TestAsimiSQLTool_WriteViaGORM(t *testing.T) {
	db := setupSQLTestDB(t)
	tool := AsimiSQLTool{DB: db, ProjectRoot: t.TempDir()}

	result, err := tool.Call(context.Background(), `{"query":"DELETE FROM t WHERE name = 'alpha'"}`)
	require.NoError(t, err)
	assert.Contains(t, result, `"status":"ok"`)
	assert.Contains(t, result, `"rows_affected":1`)

	var count int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM t").Scan(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestAsimiSQLTool_EmptySelectReturnsOk verifies the empty-result contract.
func TestAsimiSQLTool_EmptySelectReturnsOk(t *testing.T) {
	db := setupSQLTestDB(t)
	tool := AsimiSQLTool{DB: db, ProjectRoot: t.TempDir()}

	result, err := tool.Call(context.Background(), `{"query":"SELECT id FROM t WHERE name = 'missing'"}`)
	require.NoError(t, err)
	assert.Equal(t, `{"status":"ok"}`, result)
}

// TestAsimiSQLTool_SQLErrorMessage verifies error messages keep the
// "sqlite3 error:" prefix the callers rely on.
func TestAsimiSQLTool_SQLErrorMessage(t *testing.T) {
	db := setupSQLTestDB(t)
	tool := AsimiSQLTool{DB: db, ProjectRoot: t.TempDir()}

	_, err := tool.Call(context.Background(), `{"query":"SELECT nonexistent_column FROM t"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sqlite3 error:")
	assert.Contains(t, err.Error(), "no such column")
}

// TestAsimiSQLTool_SingleStatement verifies the one-statement contract.
func TestAsimiSQLTool_SingleStatement(t *testing.T) {
	db := setupSQLTestDB(t)
	tool := AsimiSQLTool{DB: db, ProjectRoot: t.TempDir()}

	_, err := tool.Call(context.Background(), `{"query":"SELECT 1; DROP TABLE t"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one SQL statement")
}

// TestAsimiSQLTool_NilDB verifies a clear error when no handle is configured.
func TestAsimiSQLTool_NilDB(t *testing.T) {
	tool := AsimiSQLTool{ProjectRoot: t.TempDir()}
	_, err := tool.Call(context.Background(), `{"query":"SELECT 1"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no database handle")
}

// TestAsimiSQLTool_RequiredQuery verifies empty-query rejection.
func TestAsimiSQLTool_RequiredQuery(t *testing.T) {
	db := setupSQLTestDB(t)
	tool := AsimiSQLTool{DB: db, ProjectRoot: t.TempDir()}

	_, err := tool.Call(context.Background(), `{"query":""}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query is required")
}

// TestAsimiSQLTool_Format verifies the Format method works correctly
func TestAsimiSQLTool_Format(t *testing.T) {
	tool := AsimiSQLTool{ProjectRoot: t.TempDir()}

	// Test with short query
	result := tool.Format(`{"query":"SELECT * FROM edicts"}`, "output", nil)
	assert.Contains(t, result, "AsimiSQL")
	assert.Contains(t, result, "SELECT * FROM edicts")

	// Test with long query (should be truncated)
	longQuery := "SELECT edict_id, intent, status, created_at, updated_at, notes, metadata FROM edicts WHERE status = 'active' ORDER BY created_at DESC"
	result = tool.Format(`{"query":"`+longQuery+`"}`, "output", nil)
	assert.Contains(t, result, "...")
	assert.LessOrEqual(t, len(result), 100)

	// Test with error
	result = tool.Format(`{"query":"bad"}`, "", assert.AnError)
	assert.Contains(t, result, "Error:")
}

// TestAsimiSQLTool_ParameterSchema verifies the tool's parameter schema
func TestAsimiSQLTool_ParameterSchema(t *testing.T) {
	tool := AsimiSQLTool{ProjectRoot: t.TempDir()}

	schema := tool.ParameterSchema()
	assert.NotNil(t, schema)

	props, ok := schema["properties"].(map[string]any)
	assert.True(t, ok)

	query, ok := props["query"]
	assert.True(t, ok)

	queryDef := query.(map[string]any)
	assert.Equal(t, "string", queryDef["type"])
	assert.Contains(t, queryDef["description"], "SQL")
}

// TestAsimiSQLTool_NameAndDescription verifies tool metadata
func TestAsimiSQLTool_NameAndDescription(t *testing.T) {
	tool := AsimiSQLTool{}

	assert.Equal(t, "asimisql", tool.Name())

	desc := tool.Description()
	assert.Contains(t, desc, "Execute SQL")
	assert.Contains(t, desc, "Court database")
}

// TestAsimiSQLTool_RelativeDBPath verifies the DB handle is used as-is (the
// daemon resolves the database path; the tool no longer shells out with a
// working directory).
func TestAsimiSQLTool_RelativeDBPath(t *testing.T) {
	db := setupSQLTestDB(t)
	tool := AsimiSQLTool{DB: db, ProjectRoot: filepath.Join(t.TempDir(), "data")}

	result, err := tool.Call(context.Background(), `{"query":"SELECT name FROM t LIMIT 1"}`)
	require.NoError(t, err)
	assert.Contains(t, result, `"name":"alpha"`)
}

// TestAsimiSQLTool_InvalidJSON pins the invalid-input rejection the GORM
// rewrite must preserve: malformed tool input fails with the "invalid input"
// prefix before any database access.
func TestAsimiSQLTool_InvalidJSON(t *testing.T) {
	db := setupSQLTestDB(t)
	tool := AsimiSQLTool{DB: db, ProjectRoot: t.TempDir()}

	_, err := tool.Call(context.Background(), "not json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid input")
}
