package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/afittestide/asimi/storage"
)

// reviewWith runs reviewInEditor with a fake notify that replies with res.
func reviewWith(t *testing.T, content string, res EditorResult) (editorReview, error) {
	t.Helper()
	notify := func(msg any) {
		req, ok := msg.(EditorRequest)
		if !ok {
			return
		}
		req.ResultChan <- res
	}
	return reviewInEditor(context.Background(), notify, content, "doc.md")
}

func TestReviewInEditor(t *testing.T) {
	t.Run("unchanged content is reviewed, not rejected", func(t *testing.T) {
		// Regression guard for incident bf31225394b5997b: an editor that exits
		// without writing (lost tty, non-interactive $EDITOR, :q!) leaves the
		// bytes untouched. That is not a decision the Ruler made.
		review, err := reviewWith(t, "untouched", EditorResult{Outcome: EditorUnchanged})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review.Status != reviewApproved {
			t.Fatalf("expected status %q, got %q", reviewApproved, review.Status)
		}
	})

	t.Run("modified content returns the diff", func(t *testing.T) {
		original := "alpha\nbeta\ngamma\n"
		edited := "alpha\nBETA\ngamma\n"
		review, err := reviewWith(t, original, EditorResult{Content: edited, Outcome: EditorModified})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review.Status != reviewModified {
			t.Fatalf("expected status %q, got %q", reviewModified, review.Status)
		}
		if review.Content != edited {
			t.Fatalf("expected modified content roundtrip, got: %q", review.Content)
		}
		if !strings.Contains(review.Diff, "BETA") {
			t.Fatalf("expected diff to contain BETA, got: %s", review.Diff)
		}
	})

	t.Run("modified outcome with identical bytes is approved", func(t *testing.T) {
		// An editor that rewrites the file byte-for-byte changed nothing.
		review, err := reviewWith(t, "same", EditorResult{Content: "same", Outcome: EditorModified})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review.Status != reviewApproved {
			t.Fatalf("expected status %q, got %q", reviewApproved, review.Status)
		}
	})

	t.Run("deliberate discard is a rejection", func(t *testing.T) {
		review, err := reviewWith(t, "untouched", EditorResult{Outcome: EditorDiscarded})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if review.Status != reviewRejected {
			t.Fatalf("expected status %q, got %q", reviewRejected, review.Status)
		}
	})

	t.Run("editor launch failure is an error, not a status", func(t *testing.T) {
		_, err := reviewWith(t, "x", EditorResult{Outcome: EditorFailed, Err: assertError("exec: vi: not found")})
		if err == nil {
			t.Fatal("expected error when the editor fails to launch")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected the launch failure to survive, got: %v", err)
		}
	})

	t.Run("no outcome is an error, never an approval", func(t *testing.T) {
		if _, err := reviewWith(t, "x", EditorResult{}); err == nil {
			t.Fatal("expected an error when the host reports no outcome")
		}
	})

	t.Run("requires notify", func(t *testing.T) {
		if _, err := reviewInEditor(context.Background(), nil, "test", ""); err == nil {
			t.Fatal("expected error when notify is nil")
		}
	})

	t.Run("context cancellation aborts the review", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := reviewInEditor(ctx, func(any) {}, "test", "")
		if err == nil {
			t.Fatal("expected ctx cancellation error")
		}
	})
}

// reviewRequester records the zhengming requests raised after the review pass.
type reviewRequester struct {
	asked []storage.ZhengmingQuestions
}

func (r *reviewRequester) RequestZhengming(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
	r.asked = append(r.asked, questions)
	return "req-1", nil
}

// TestSuggestEdictLargePayloadUnchangedFallsThrough verifies that an unchanged
// review still reaches the Ruler as a zhengming prompt: no phantom rejection,
// no skipped review, and suggest_edict's three-way contract is unchanged.
func TestSuggestEdictLargePayloadUnchangedFallsThrough(t *testing.T) {
	body := strings.Repeat("evidence line\n", 50) // >500 chars
	requester := &reviewRequester{}
	tool := SuggestEdictTool{
		Ctx:       testCtx(),
		Requester: requester,
		NotifyFn: func() func(any) {
			return func(msg any) {
				req, ok := msg.(EditorRequest)
				if !ok {
					return
				}
				req.ResultChan <- EditorResult{Outcome: EditorUnchanged}
			}
		},
	}

	input, _ := json.Marshal(map[string]any{"suggestion": body, "summary": "large suggestion"})
	result, err := tool.Call(context.Background(), string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if res.Status != "suggested" {
		t.Fatalf("expected unchanged review to fall through to zhengming, got %q", res.Status)
	}
	if len(requester.asked) != 1 {
		t.Fatalf("expected exactly one zhengming request, got %d", len(requester.asked))
	}
}

// TestSuggestEdictLargePayloadEditorFailure verifies a real editor failure is
// surfaced as an error rather than being reported as a rejection.
func TestSuggestEdictLargePayloadEditorFailure(t *testing.T) {
	body := strings.Repeat("evidence line\n", 50)
	requester := &reviewRequester{}
	tool := SuggestEdictTool{
		Ctx:       testCtx(),
		Requester: requester,
		NotifyFn: func() func(any) {
			return func(msg any) {
				req, ok := msg.(EditorRequest)
				if !ok {
					return
				}
				req.ResultChan <- EditorResult{Outcome: EditorFailed, Err: assertError("no tty")}
			}
		},
	}

	input, _ := json.Marshal(map[string]any{"suggestion": body, "summary": "large suggestion"})
	if _, err := tool.Call(context.Background(), string(input)); err == nil {
		t.Fatal("expected editor failure to surface as an error")
	}
	if len(requester.asked) != 0 {
		t.Fatalf("a failed review must not raise a zhengming request")
	}
}

// TestSuggestEdictLargePayloadModifiedBouncesBack verifies the ruler_modified
// contract survives the refactor: an edited suggestion returns the diff and
// never reaches zhengming.
func TestSuggestEdictLargePayloadModifiedBouncesBack(t *testing.T) {
	original := strings.Repeat("original line\n", 50)
	edited := strings.Repeat("edited line\n", 50)
	requester := &reviewRequester{}
	tool := SuggestEdictTool{
		Ctx:       testCtx(),
		Requester: requester,
		NotifyFn: func() func(any) {
			return func(msg any) {
				req, ok := msg.(EditorRequest)
				if !ok {
					return
				}
				req.ResultChan <- EditorResult{Content: edited, Outcome: EditorModified}
			}
		},
	}

	input, _ := json.Marshal(map[string]any{"suggestion": original, "summary": "large suggestion"})
	result, err := tool.Call(context.Background(), string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res struct {
		Status   string `json:"status"`
		Modified string `json:"modified"`
		Diff     string `json:"diff"`
	}
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if res.Status != "ruler_modified" {
		t.Fatalf("expected status 'ruler_modified', got %q", res.Status)
	}
	if res.Modified != edited {
		t.Fatal("modified content must round-trip")
	}
	if !strings.Contains(res.Diff, "edited line") {
		t.Fatalf("expected the diff to carry the edit, got: %s", res.Diff)
	}
	if len(requester.asked) != 0 {
		t.Fatalf("a modified suggestion must bounce back to the Sage, not reach zhengming")
	}
}

// TestSuggestEdictLargePayloadDiscardStillReachesZhengming verifies a
// deliberate discard keeps its meaning: the review reports a rejection and
// suggest_edict's three-way branch is unchanged (it still falls through to
// zhengming with the original text, and the Court records the Ruler's answer).
func TestSuggestEdictLargePayloadDiscardStillReachesZhengming(t *testing.T) {
	body := strings.Repeat("evidence line\n", 50)
	requester := &reviewRequester{}
	tool := SuggestEdictTool{
		Ctx:       testCtx(),
		Requester: requester,
		NotifyFn: func() func(any) {
			return func(msg any) {
				req, ok := msg.(EditorRequest)
				if !ok {
					return
				}
				req.ResultChan <- EditorResult{Outcome: EditorDiscarded}
			}
		},
	}

	input, _ := json.Marshal(map[string]any{"suggestion": body, "summary": "large suggestion"})
	result, err := tool.Call(context.Background(), string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if res.Status != "suggested" {
		t.Fatalf("discard must fall through to zhengming, got %q", res.Status)
	}
	if len(requester.asked) != 1 || requester.asked[0][0].Text != body {
		t.Fatal("zhengming must carry the original suggestion text")
	}
}

// assertError is a helper for tests
type assertError string

func (e assertError) Error() string {
	return string(e)
}
