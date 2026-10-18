package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceTextTool_BasicReplace(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	if err := os.WriteFile("test.txt", []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := ReplaceTextTool{ProjectRoot: tmpDir}

	t.Run("replaces text in file", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "old_text": "hello", "new_text": "goodbye"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully") {
			t.Errorf("expected success message, got %q", result)
		}

		content, err := os.ReadFile("test.txt")
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "goodbye world" {
			t.Errorf("expected 'goodbye world', got %q", string(content))
		}
	})
}

func TestReplaceTextTool_PathValidation(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	if err := os.WriteFile("test.txt", []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := ReplaceTextTool{ProjectRoot: tmpDir}

	t.Run("path outside projectRoot is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "/etc/passwd", "old_text": "a", "new_text": "b"}`)
		if err == nil {
			t.Error("expected error for path outside projectRoot")
		}
	})

	t.Run("path traversal attempt is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "../escape.txt", "old_text": "a", "new_text": "b"}`)
		if err == nil {
			t.Error("expected error for path traversal")
		}
	})

	t.Run("empty path is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "", "old_text": "a", "new_text": "b"}`)
		if err == nil {
			t.Error("expected error for empty path")
		}
	})
}

func TestReplaceTextTool_ExplicitProjectRootDiffersFromGetwd(t *testing.T) {
	// Daemon-mode scenario: projectRoot != os.Getwd()
	projectDir := t.TempDir()
	processDir := t.TempDir()
	t.Chdir(processDir)

	// Create file inside the project
	projectFile := filepath.Join(projectDir, "code.go")
	if err := os.WriteFile(projectFile, []byte("old code"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create file in process dir (outside project)
	processFile := filepath.Join(processDir, "unrelated.txt")
	if err := os.WriteFile(processFile, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := ReplaceTextTool{ProjectRoot: projectDir}

	t.Run("replace in file inside projectRoot with absolute path succeeds", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "`+projectFile+`", "old_text": "old", "new_text": "new"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully") {
			t.Errorf("expected success message, got %q", result)
		}

		content, err := os.ReadFile(projectFile)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "new code" {
			t.Errorf("expected 'new code', got %q", string(content))
		}
	})

	t.Run("replace in file inside projectRoot with relative path succeeds", func(t *testing.T) {
		// Reset file content for this subtest
		if err := os.WriteFile(projectFile, []byte("old code"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Call(context.Background(), `{"path": "code.go", "old_text": "old", "new_text": "new"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully") {
			t.Errorf("expected success message, got %q", result)
		}

		content, err := os.ReadFile(projectFile)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "new code" {
			t.Errorf("expected 'new code', got %q", string(content))
		}
	})

	t.Run("replace in file outside projectRoot is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "`+processFile+`", "old_text": "old", "new_text": "new"}`)
		if err == nil {
			t.Error("expected error for replace in file outside projectRoot")
		}
	})
}

func TestReplaceTextTool_NoMatch(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	if err := os.WriteFile("test.txt", []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := ReplaceTextTool{ProjectRoot: tmpDir}

	t.Run("no match returns info message", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "old_text": "nonexistent", "new_text": "replacement"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "No occurrences") {
			t.Errorf("expected 'No occurrences' message, got %q", result)
		}
	})

	t.Run("identical old and new text returns info", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "old_text": "hello", "new_text": "hello"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "No changes") {
			t.Errorf("expected 'No changes' message, got %q", result)
		}
	})
}

func TestReplaceTextTool_Interface(t *testing.T) {
	tool := ReplaceTextTool{}

	t.Run("Name returns correct value", func(t *testing.T) {
		if tool.Name() != "replace_text" {
			t.Errorf("expected name 'replace_text', got %q", tool.Name())
		}
	})

	t.Run("Description is not empty", func(t *testing.T) {
		desc := tool.Description()
		if desc == "" {
			t.Error("description should not be empty")
		}
	})

	t.Run("ParameterSchema returns valid schema", func(t *testing.T) {
		schema := tool.ParameterSchema()
		if schema == nil {
			t.Fatal("schema should not be nil")
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatal("schema should have properties")
		}
		for _, key := range []string{"path", "old_text", "new_text"} {
			if _, ok := properties[key]; !ok {
				t.Errorf("schema should have '%s' property", key)
			}
		}
	})
}

// TestReplaceTextTool_WorktreeContext verifies that a context-carried RepoInfo
// (edict 943) routes replacements into the edict's worktree.
func TestReplaceTextTool_WorktreeContext(t *testing.T) {
	mainRoot := t.TempDir()
	wtRel := filepath.Join(".agents", "worktrees", "e42")
	if err := os.MkdirAll(filepath.Join(mainRoot, wtRel), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mainRoot, "file.txt"), []byte("main tree"), 0644)
	os.WriteFile(filepath.Join(mainRoot, wtRel, "file.txt"), []byte("worktree"), 0644)

	tool := ReplaceTextTool{ProjectRoot: mainRoot}
	ctx := worktreeCtx(mainRoot, wtRel)

	if _, err := tool.Call(ctx, `{"path": "file.txt", "old_text": "worktree", "new_text": "edited"}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(mainRoot, wtRel, "file.txt"))
	if string(got) != "edited" {
		t.Errorf("worktree file = %q, want edited", string(got))
	}
	main, _ := os.ReadFile(filepath.Join(mainRoot, "file.txt"))
	if string(main) != "main tree" {
		t.Errorf("main file = %q, want untouched", string(main))
	}
	if tool.ProjectRoot != mainRoot {
		t.Errorf("registered ProjectRoot mutated: %q", tool.ProjectRoot)
	}
}
