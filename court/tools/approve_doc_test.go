package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestApproveDocTool(t *testing.T) {
	t.Run("validates content is required", func(t *testing.T) {
		tool := ApproveDocTool{Notify: func(any) {}}
		_, err := tool.Call(context.Background(), `{}`)
		if err == nil {
			t.Fatal("expected error when content is missing")
		}
		if err.Error() != "content is required" {
			t.Fatalf("expected 'content is required' error, got: %v", err)
		}
	})

	t.Run("rejects when user quits without saving", func(t *testing.T) {
		tool := ApproveDocTool{Notify: func(msg any) {
			req, ok := msg.(EditorRequest)
			if !ok {
				return
			}
			req.ResultChan <- EditorResult{Saved: false}
		}}

		input := map[string]any{"content": "untouched", "description": "Test review"}
		inputJSON, _ := json.Marshal(input)
		result, err := tool.Call(context.Background(), string(inputJSON))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var res struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(result), &res); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}
		if res.Status != "rejected" {
			t.Fatalf("expected status 'rejected', got: %s", res.Status)
		}
	})

	t.Run("approves when saved without changes", func(t *testing.T) {
		original := "This is test content that will not be modified"
		tool := ApproveDocTool{Notify: func(msg any) {
			req, ok := msg.(EditorRequest)
			if !ok {
				return
			}
			req.ResultChan <- EditorResult{Content: req.Content, Saved: true}
		}}

		input := map[string]any{"content": original, "description": "Test review"}
		inputJSON, _ := json.Marshal(input)
		result, err := tool.Call(context.Background(), string(inputJSON))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var res struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(result), &res); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}
		if res.Status != "approved" {
			t.Fatalf("expected status 'approved', got: %s", res.Status)
		}
	})

	t.Run("returns modified content and diff when edited", func(t *testing.T) {
		original := "alpha\nbeta\ngamma\n"
		edited := "alpha\nBETA\ngamma\n"
		tool := ApproveDocTool{Notify: func(msg any) {
			req, ok := msg.(EditorRequest)
			if !ok {
				return
			}
			req.ResultChan <- EditorResult{Content: edited, Saved: true}
		}}

		input := map[string]any{"content": original}
		inputJSON, _ := json.Marshal(input)
		result, err := tool.Call(context.Background(), string(inputJSON))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var res struct {
			Status  string `json:"status"`
			Diff    string `json:"diff"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(result), &res); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}
		if res.Status != "modified" {
			t.Fatalf("expected status 'modified', got: %s", res.Status)
		}
		if res.Content != edited {
			t.Fatalf("expected modified content roundtrip, got: %q", res.Content)
		}
		if !strings.Contains(res.Diff, "BETA") {
			t.Fatalf("expected diff to contain BETA, got: %s", res.Diff)
		}
	})

	t.Run("requires notify", func(t *testing.T) {
		noNotify := ApproveDocTool{}
		_, err := noNotify.Call(context.Background(), `{"content":"test"}`)
		if err == nil {
			t.Fatal("expected error when notify is nil")
		}
	})

	t.Run("works when notify is installed after construction", func(t *testing.T) {
		// Mirrors the Court's late binding: the tool is constructed as a
		// zero-value placeholder, then the live callback is set once known.
		tool := ApproveDocTool{}
		tool.Notify = func(msg any) {
			req, ok := msg.(EditorRequest)
			if !ok {
				return
			}
			req.ResultChan <- EditorResult{Content: req.Content, Saved: true}
		}

		input := map[string]any{"content": "late-bound notify"}
		inputJSON, _ := json.Marshal(input)
		result, err := tool.Call(context.Background(), string(inputJSON))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var res struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(result), &res); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}
		if res.Status != "approved" {
			t.Fatalf("expected status 'approved', got: %s", res.Status)
		}
	})

	t.Run("parameter schema is valid", func(t *testing.T) {
		tool := ApproveDocTool{Notify: func(any) {}}
		schema := tool.ParameterSchema()
		if schema == nil {
			t.Fatal("expected non-nil schema")
		}

		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatal("expected properties to be a map")
		}

		if _, ok := props["content"]; !ok {
			t.Error("expected 'content' property in schema")
		}
		if _, ok := props["filename"]; !ok {
			t.Error("expected 'filename' property in schema")
		}
		if _, ok := props["description"]; !ok {
			t.Error("expected 'description' property in schema")
		}

		required, ok := schema["required"].([]string)
		if !ok {
			t.Fatal("expected required to be a slice of strings")
		}
		if len(required) != 1 || required[0] != "content" {
			t.Fatalf("expected ['content'] as required, got: %v", required)
		}
	})
}

func TestApproveDocToolFormat(t *testing.T) {
	tool := ApproveDocTool{}

	t.Run("formats approved result", func(t *testing.T) {
		input := `{"content": "test", "description": "Test review"}`
		result := `{"status": "approved"}`

		output := tool.Format(input, result, nil)
		if output == "" {
			t.Fatal("expected non-empty output")
		}
	})

	t.Run("formats rejected result", func(t *testing.T) {
		input := `{"content": "test", "description": "Test review"}`
		result := `{"status": "rejected"}`

		output := tool.Format(input, result, nil)
		if output == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(output, "Rejected") {
			t.Fatalf("expected 'Rejected' in output, got: %s", output)
		}
	})

	t.Run("formats error result", func(t *testing.T) {
		input := `{"content": "test"}`
		output := tool.Format(input, "", assertError("test error"))

		if output == "" {
			t.Fatal("expected non-empty output")
		}
	})
}

// assertError is a helper for tests
type assertError string

func (e assertError) Error() string {
	return string(e)
}
