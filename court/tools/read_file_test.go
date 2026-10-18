package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileTool_BasicRead(t *testing.T) {
	// Save current directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// Create a temporary directory for testing
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Create test file
	testContent := "line 1\nline 2\nline 3\nline 4\nline 5"
	if err := os.WriteFile("test.txt", []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool(tmpDir)

	t.Run("reads entire file", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != testContent {
			t.Errorf("expected %q, got %q", testContent, result)
		}
	})

	t.Run("reads file with quoted path", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "\"test.txt\""}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != testContent {
			t.Errorf("expected %q, got %q", testContent, result)
		}
	})

	t.Run("reads file with single quoted path", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "'test.txt'"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != testContent {
			t.Errorf("expected %q, got %q", testContent, result)
		}
	})
}

func TestReadFileTool_WithOffset(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	testContent := "line 1\nline 2\nline 3\nline 4\nline 5"
	if err := os.WriteFile("test.txt", []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool(tmpDir)

	t.Run("offset starts from correct line", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "offset": 3}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "line 3\nline 4\nline 5"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("offset 1 returns entire file", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "offset": 1}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != testContent {
			t.Errorf("expected %q, got %q", testContent, result)
		}
	})

	t.Run("offset beyond file returns empty", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "offset": 100}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})
}

func TestReadFileTool_WithLimit(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	testContent := "line 1\nline 2\nline 3\nline 4\nline 5"
	if err := os.WriteFile("test.txt", []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool(tmpDir)

	t.Run("limit returns correct number of lines", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "limit": 2}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "line 1\nline 2"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("limit larger than file returns entire file", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "limit": 100}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != testContent {
			t.Errorf("expected %q, got %q", testContent, result)
		}
	})

	t.Run("limit 1 returns first line", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "limit": 1}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "line 1"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})
}

func TestReadFileTool_WithOffsetAndLimit(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	testContent := "line 1\nline 2\nline 3\nline 4\nline 5"
	if err := os.WriteFile("test.txt", []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool(tmpDir)

	t.Run("offset and limit together", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "offset": 2, "limit": 2}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "line 2\nline 3"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("offset near end with limit", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "offset": 4, "limit": 10}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "line 4\nline 5"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("offset and limit at exact boundaries", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "offset": 1, "limit": 5}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != testContent {
			t.Errorf("expected %q, got %q", testContent, result)
		}
	})
}

func TestReadFileTool_EdgeCases(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool(tmpDir)

	t.Run("empty file", func(t *testing.T) {
		if err := os.WriteFile("empty.txt", []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
		result, err := tool.Call(context.Background(), `{"path": "empty.txt"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})

	t.Run("single line file without newline", func(t *testing.T) {
		if err := os.WriteFile("single.txt", []byte("just one line"), 0644); err != nil {
			t.Fatal(err)
		}
		result, err := tool.Call(context.Background(), `{"path": "single.txt"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "just one line" {
			t.Errorf("expected %q, got %q", "just one line", result)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "nonexistent.txt"}`)
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})

	t.Run("empty path", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": ""}`)
		if err == nil {
			t.Error("expected error for empty path")
		}
	})

	t.Run("raw path without JSON", func(t *testing.T) {
		testContent := "raw content"
		if err := os.WriteFile("raw.txt", []byte(testContent), 0644); err != nil {
			t.Fatal(err)
		}
		result, err := tool.Call(context.Background(), "raw.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != testContent {
			t.Errorf("expected %q, got %q", testContent, result)
		}
	})
}

func TestReadFileTool_PathValidation(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Create a file inside the project
	if err := os.WriteFile("inside.txt", []byte("inside content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a file outside the project
	parentDir := filepath.Dir(tmpDir)
	outsidePath := filepath.Join(parentDir, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside content"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool(tmpDir)

	t.Run("path outside project is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "`+outsidePath+`"}`)
		if err == nil {
			t.Error("expected error for path outside project")
		}
		if !strings.Contains(err.Error(), "outside") && !strings.Contains(err.Error(), "denied") {
			t.Errorf("expected error message about path outside project, got: %v", err)
		}
	})

	t.Run("path traversal attempt is denied", func(t *testing.T) {
		// Try to escape using ..
		_, err := tool.Call(context.Background(), `{"path": "../outside.txt"}`)
		if err == nil {
			t.Error("expected error for path traversal attempt")
		}
	})
}

func TestReadFileTool_ClaudeCodeCLIWorkaround(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	testContent := "line 1\nline 2\nline 3\nline 4\nline 5"
	if err := os.WriteFile("test.txt", []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool(tmpDir)

	t.Run("offset as string works", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "offset": "3"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "line 3\nline 4\nline 5"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("limit as string works", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "limit": "2"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "line 1\nline 2"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("both offset and limit as strings work", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "offset": "2", "limit": "2"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "line 2\nline 3"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})
}

func TestReadFileTool_Interface(t *testing.T) {
	tool := NewReadFileTool("")

	t.Run("Name returns correct value", func(t *testing.T) {
		if tool.Name() != "read_file" {
			t.Errorf("expected name 'read_file', got %q", tool.Name())
		}
	})

	t.Run("Description is not empty", func(t *testing.T) {
		desc := tool.Description()
		if desc == "" {
			t.Error("description should not be empty")
		}
		if !strings.Contains(desc, "path") {
			t.Error("description should mention 'path'")
		}
	})

	t.Run("ParameterSchema returns valid schema", func(t *testing.T) {
		schema := tool.ParameterSchema()
		if schema == nil {
			t.Fatal("schema should not be nil")
		}

		schemaType, ok := schema["type"].(string)
		if !ok || schemaType != "object" {
			t.Errorf("expected type 'object', got %v", schema["type"])
		}

		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatal("schema should have properties")
		}

		if _, ok := properties["path"]; !ok {
			t.Error("schema should have 'path' property")
		}

		required, ok := schema["required"].([]string)
		if !ok {
			t.Fatal("schema should have required array")
		}

		found := false
		for _, r := range required {
			if r == "path" {
				found = true
				break
			}
		}
		if !found {
			t.Error("'path' should be in required array")
		}
	})
}

func TestReadFileTool_Format(t *testing.T) {
	tool := NewReadFileTool("")

	t.Run("format with result", func(t *testing.T) {
		result := tool.Format(`{"path": "test.txt"}`, "line 1\nline 2", nil)
		if !strings.Contains(result, "test.txt") {
			t.Error("format should include path")
		}
		if !strings.Contains(result, "2 lines") {
			t.Error("format should show line count")
		}
	})

	t.Run("format with error", func(t *testing.T) {
		result := tool.Format(`{"path": "test.txt"}`, "", os.ErrNotExist)
		if !strings.Contains(result, "Error") {
			t.Error("format should show error")
		}
	})

	t.Run("format with empty result", func(t *testing.T) {
		result := tool.Format(`{"path": "test.txt"}`, "", nil)
		if !strings.Contains(result, "0 lines") {
			t.Error("format should show 0 lines for empty result")
		}
	})
}

func TestReadFileTool_ExplicitProjectRootDiffersFromGetwd(t *testing.T) {
	// Daemon-mode scenario: projectRoot != os.Getwd()
	projectDir := t.TempDir()
	processDir := t.TempDir()
	t.Chdir(processDir)

	// Create file inside the project
	projectFile := filepath.Join(projectDir, "src", "code.go")
	if err := os.MkdirAll(filepath.Dir(projectFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectFile, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create file in the process directory (outside project)
	processFile := filepath.Join(processDir, "unrelated.txt")
	if err := os.WriteFile(processFile, []byte("unrelated"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool(projectDir)

	t.Run("read file inside projectRoot with absolute path succeeds", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "`+projectFile+`"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "package main" {
			t.Errorf("expected 'package main', got %q", result)
		}
	})

	t.Run("read file inside projectRoot with relative path succeeds", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "src/code.go"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "package main" {
			t.Errorf("expected 'package main', got %q", result)
		}
	})

	t.Run("read file in cwd outside projectRoot is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "`+processFile+`"}`)
		if err == nil {
			t.Error("expected error for read of file in cwd but outside projectRoot")
		}
	})

	t.Run("path traversal beyond projectRoot is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "`+filepath.Join(projectDir, "..", "escape.txt")+`"}`)
		if err == nil {
			t.Error("expected error for path traversal beyond projectRoot")
		}
	})
}

// TestReadFileTool_WorktreeContext verifies that a context-carried RepoInfo
// (edict 943) routes reads into the edict's worktree, not the main tree.
func TestReadFileTool_WorktreeContext(t *testing.T) {
	mainRoot := t.TempDir()
	wtRel := filepath.Join(".agents", "worktrees", "e42")
	if err := os.MkdirAll(filepath.Join(mainRoot, wtRel), 0755); err != nil {
		t.Fatal(err)
	}
	// Same relative path in both trees, different content.
	os.WriteFile(filepath.Join(mainRoot, "file.txt"), []byte("main tree"), 0644)
	os.WriteFile(filepath.Join(mainRoot, wtRel, "file.txt"), []byte("worktree"), 0644)

	tool := NewReadFileTool(mainRoot)
	ctx := worktreeCtx(mainRoot, wtRel)

	result, err := tool.Call(ctx, `{"path": "file.txt"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "worktree" {
		t.Errorf("read %q, want worktree content", result)
	}
	if tool.ProjectRoot != mainRoot {
		t.Errorf("registered ProjectRoot mutated: %q", tool.ProjectRoot)
	}

	// Without the context value the tool resolves against ProjectRoot as before.
	result, err = tool.Call(context.Background(), `{"path": "file.txt"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "main tree" {
		t.Errorf("read %q, want main tree content", result)
	}
}
