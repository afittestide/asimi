package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// This file holds the $EDITOR review pass used by new_edict and replace_edict
// for large documents. It is deliberately not a Tool: nothing ever dispatched
// it as one, and registering it cost a bespoke repair path (a placeholder in
// the registry plus a SetNotify re-registration of live sessions) for a hook
// no minister could reach.

// EditorOutcome is what the host established about the review. It replaces the
// old Saved bool, which collapsed two different facts: "the bytes on disk were
// not rewritten" and "the Ruler declined". An absence of evidence is not a
// decision, so the outcome is explicit and the caller maps it honestly.
type EditorOutcome string

const (
	// EditorUnchanged means the editor exited and the document's bytes are
	// exactly what the caller sent: the document was reviewed, nothing changed.
	//
	// A deliberate `:q!` is NOT distinguishable from an editor that never
	// really ran (no tty after a sandbox/daemon restart, a non-interactive
	// $EDITOR, a `vi` fallback that cannot attach a terminal, or `:wq` on an
	// unmodified buffer): every one of them leaves the bytes untouched. A host
	// that cannot establish a discard must report unchanged rather than invent
	// a rejection the Ruler never made.
	EditorUnchanged EditorOutcome = "unchanged"
	// EditorModified means the Ruler edited and saved; Content carries the new
	// bytes.
	EditorModified EditorOutcome = "modified"
	// EditorDiscarded means the host has positive evidence that the Ruler quit
	// without saving. Only a host that can establish that may report it.
	EditorDiscarded EditorOutcome = "discarded"
	// EditorFailed means the editor never produced a usable answer. It always
	// reaches the caller as an error, never as a status.
	EditorFailed EditorOutcome = "failed"
)

// EditorRequest is sent via Notify to ask the host (TUI or RPC client) to open
// content in $EDITOR. The receiver owns the temp-file lifecycle: it writes the
// content, runs the editor, and returns the outcome on ResultChan.
//
// This shape is intentionally serialization-friendly (no *exec.Cmd) so a daemon
// can bridge it to a remote TUI over RPC.
type EditorRequest struct {
	Content    string
	Filename   string // hint for extension/syntax highlighting (optional)
	ResultChan chan EditorResult
}

// EditorResult carries the post-edit content and the outcome back to the
// caller. Content is only meaningful for EditorModified.
type EditorResult struct {
	Content string
	Outcome EditorOutcome
	Err     error
}

// Review statuses. approved means the document was reviewed and left as it
// was; modified means the Ruler edited and saved; rejected means the Ruler
// deliberately discarded it.
const (
	reviewApproved = "approved"
	reviewModified = "modified"
	reviewRejected = "rejected"
)

// editorReview is the outcome of reviewInEditor.
type editorReview struct {
	Status  string
	Diff    string
	Content string
}

// reviewInEditor opens content in $EDITOR for the Ruler to review. The
// returned status is reviewApproved (bytes unchanged), reviewModified (edited,
// with a Diff) or reviewRejected (a deliberate discard). Editor failures — and
// outcomes the host could not establish — are returned as errors, never as a
// status the Ruler did not make.
func reviewInEditor(ctx context.Context, notify func(any), content, filename string) (editorReview, error) {
	if notify == nil {
		return editorReview{}, fmt.Errorf("editor review requires a notify callback")
	}

	resultChan := make(chan EditorResult, 1)
	notify(EditorRequest{
		Content:    content,
		Filename:   filename,
		ResultChan: resultChan,
	})

	var res EditorResult
	select {
	case <-ctx.Done():
		return editorReview{}, ctx.Err()
	case res = <-resultChan:
	}

	if res.Err != nil {
		return editorReview{}, fmt.Errorf("editor failed: %w", res.Err)
	}

	switch res.Outcome {
	case EditorUnchanged:
		return editorReview{Status: reviewApproved}, nil
	case EditorDiscarded:
		return editorReview{Status: reviewRejected}, nil
	case EditorModified:
		if trimmedEqual(res.Content, content) {
			return editorReview{Status: reviewApproved}, nil
		}
		diff, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A:        difflib.SplitLines(content),
			B:        difflib.SplitLines(res.Content),
			FromFile: "original",
			ToFile:   "modified",
			Context:  3,
		})
		return editorReview{Status: reviewModified, Diff: diff, Content: res.Content}, nil
	case EditorFailed:
		return editorReview{}, fmt.Errorf("editor reported a failure with no error")
	default:
		// No outcome is neither an approval nor a rejection: refuse to invent a
		// decision the Ruler never made.
		return editorReview{}, fmt.Errorf("editor reported no outcome")
	}
}

// trimmedEqual reports whether two documents are equal ignoring trailing
// whitespace, so an editor that only normalizes the final newline does not
// register as an edit.
func trimmedEqual(a, b string) bool {
	return strings.TrimRight(a, " \t\n\r") == strings.TrimRight(b, " \t\n\r")
}
