package tools

import "encoding/json"

// markdownFormat is the TUI result-envelope opt-in for a bare markdown block
// (chat.go:markdownEnvelopeContent). Duplicated here because the TUI and the
// daemon share no constants package — same reason as markdownTableFormat.
const markdownFormat = "markdown"

// markdownEnvelope is the TUI opt-in for a single record rendered as markdown.
// Content holds the rendered block; Raw carries the untouched record so the
// model keeps full structured fidelity (as the table envelope's Raw does).
type markdownEnvelope struct {
	Format  string `json:"format"`        // always markdownFormat
	Content string `json:"content"`       // rendered markdown
	Raw     any    `json:"raw,omitempty"` // untouched record for the model
}

// newMarkdownEnvelope builds and marshals a markdown envelope. raw is the
// untouched record backing the rendered content.
func newMarkdownEnvelope(content string, raw any) (string, error) {
	env := markdownEnvelope{
		Format:  markdownFormat,
		Content: content,
		Raw:     raw,
	}
	encoded, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// isMarkdownEnvelope reports whether result carries a populated markdown
// envelope. Formats use it to render the record without re-implementing the
// parse; a markdown_table envelope is not a markdown envelope.
func isMarkdownEnvelope(result string) (markdownEnvelope, bool) {
	var env markdownEnvelope
	if err := json.Unmarshal([]byte(result), &env); err != nil || env.Format != markdownFormat {
		return markdownEnvelope{}, false
	}
	return env, true
}
