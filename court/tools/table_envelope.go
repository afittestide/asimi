package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// markdownTableFormat is the generic TUI opt-in format. A tool that returns a
// markdownTableEnvelope carrying this value has its result laid out by the TUI
// as a width-aware markdown table (chat.go:formatToolResult); anything else
// falls back to the tool's plain Formatted line.
const markdownTableFormat = "markdown_table"

// tableCellMaxLen bounds a table cell so one chatty value cannot dominate the
// rendered table. Raw keeps the untruncated rows for the model.
const tableCellMaxLen = 100

// markdownTableEnvelope is the generic TUI opt-in: a tool that wants its result
// rendered as a width-aware markdown table returns this shape. Rows are plain
// strings so the TUI owns all layout. Raw carries the untruncated rows so the
// model keeps full structured fidelity (as tian_ledger carries its "events").
type markdownTableEnvelope struct {
	Format  string     `json:"format"` // always markdownTableFormat
	Title   string     `json:"title"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	Raw     any        `json:"raw,omitempty"`
}

// newMarkdownTableEnvelope builds and marshals a table envelope. raw is the
// full, untruncated slice backing the table.
func newMarkdownTableEnvelope(title string, columns []string, rows [][]string, raw any) (string, error) {
	env := markdownTableEnvelope{
		Format:  markdownTableFormat,
		Title:   title,
		Columns: columns,
		Rows:    rows,
		Raw:     raw,
	}
	encoded, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// tableCell renders a table cell: whitespace is collapsed to keep the row on
// one line, and the value is bounded to max runes so a single long value cannot
// dominate the table. Raw keeps the untruncated value.
func tableCell(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if max < 1 {
		return ""
	}
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// isMarkdownTableEnvelope reports whether result carries a populated table
// envelope. Formats use it to count rows without re-implementing the parse.
func isMarkdownTableEnvelope(result string) (markdownTableEnvelope, bool) {
	var env markdownTableEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil || env.Format != markdownTableFormat {
		return markdownTableEnvelope{}, false
	}
	return env, true
}

// formatTableList renders a converted list tool's result for the TUI. A table
// envelope reports its row count; a fail-loud scope report (or anything else)
// falls back to its own text so a JSON blob never reaches the user.
func formatTableList(verb, result string) string {
	if env, ok := isMarkdownTableEnvelope(result); ok {
		return fmt.Sprintf("%s: %d rows\n", verb, len(env.Rows))
	}
	if scopeMsg, ok := scopeReportMessage(result); ok {
		return fmt.Sprintf("%s: %s\n", verb, scopeMsg)
	}
	return fmt.Sprintf("%s: %s\n", verb, result)
}
