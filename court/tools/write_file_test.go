package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileTool_BasicWrite(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	tool := WriteFileTool{ProjectRoot: tmpDir}

	t.Run("writes simple file", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "test.txt", "content": "hello world"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("expected success message, got %q", result)
		}

		content, err := os.ReadFile("test.txt")
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "hello world" {
			t.Errorf("expected 'hello world', got %q", string(content))
		}
	})

	t.Run("overwrites existing file", func(t *testing.T) {
		if err := os.WriteFile("overwrite.txt", []byte("old content"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Call(context.Background(), `{"path": "overwrite.txt", "content": "new content"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("expected success message, got %q", result)
		}

		content, err := os.ReadFile("overwrite.txt")
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "new content" {
			t.Errorf("expected 'new content', got %q", string(content))
		}
	})
}

func TestWriteFileTool_DirectoryCreation(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	tool := WriteFileTool{ProjectRoot: tmpDir}

	t.Run("creates nested directories", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "a/b/c/nested.txt", "content": "nested content"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("expected success message, got %q", result)
		}

		content, err := os.ReadFile("a/b/c/nested.txt")
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "nested content" {
			t.Errorf("expected 'nested content', got %q", string(content))
		}
	})

	t.Run("creates single level directory", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "dir/file.txt", "content": "content"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("expected success message, got %q", result)
		}

		info, err := os.Stat("dir")
		if err != nil {
			t.Fatalf("failed to stat directory: %v", err)
		}
		if !info.IsDir() {
			t.Error("expected 'dir' to be a directory")
		}
	})
}

func TestWriteFileTool_PathValidation(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	tool := WriteFileTool{ProjectRoot: tmpDir}

	t.Run("path outside project is denied", func(t *testing.T) {
		parentDir := filepath.Dir(tmpDir)
		outsidePath := filepath.Join(parentDir, "outside.txt")
		_, err := tool.Call(context.Background(), `{"path": "`+outsidePath+`", "content": "test"}`)
		if err == nil {
			t.Error("expected error for path outside project")
		}
	})

	t.Run("path traversal attempt is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "../escape.txt", "content": "test"}`)
		if err == nil {
			t.Error("expected error for path traversal attempt")
		}
	})

	t.Run("absolute path outside project is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "/etc/passwd", "content": "test"}`)
		if err == nil {
			t.Error("expected error for absolute path outside project")
		}
	})
}

func TestWriteFileTool_InvalidInput(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	tool := WriteFileTool{ProjectRoot: tmpDir}

	t.Run("empty JSON object", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{}`)
		if err == nil {
			t.Error("expected error for empty JSON")
		}
	})

	t.Run("missing path field", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"content": "test"}`)
		if err == nil {
			t.Error("expected error for missing path")
		}
	})

	t.Run("missing content field", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "test.txt"}`)
		if err == nil {
			t.Error("expected error for missing content")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `not json`)
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})

	t.Run("empty path", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "", "content": "test"}`)
		if err == nil {
			t.Error("expected error for empty path")
		}
	})
}

func TestWriteFileTool_QuotedPaths(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	tool := WriteFileTool{ProjectRoot: tmpDir}

	t.Run("handles double quoted path", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "\"quoted.txt\"", "content": "content"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("expected success message, got %q", result)
		}

		content, err := os.ReadFile("quoted.txt")
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "content" {
			t.Errorf("expected 'content', got %q", string(content))
		}
	})

	t.Run("handles single quoted path", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "'single.txt'", "content": "content"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("expected success message, got %q", result)
		}
	})

	t.Run("handles quoted content", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "content.txt", "content": "\"quoted content\""}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("expected success message, got %q", result)
		}

		fileContent, err := os.ReadFile("content.txt")
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(fileContent) != `"quoted content"` {
			t.Errorf("expected '\"quoted content\"', got %q", string(fileContent))
		}
	})
}

func TestWriteFileTool_Interface(t *testing.T) {
	tool := WriteFileTool{}

	t.Run("Name returns correct value", func(t *testing.T) {
		if tool.Name() != "write_file" {
			t.Errorf("expected name 'write_file', got %q", tool.Name())
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
		if !strings.Contains(desc, "content") {
			t.Error("description should mention 'content'")
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
		if _, ok := properties["content"]; !ok {
			t.Error("schema should have 'content' property")
		}

		required, ok := schema["required"].([]string)
		if !ok {
			t.Fatal("schema should have required array")
		}

		requiredFields := map[string]bool{"path": false, "content": false}
		for _, r := range required {
			if _, exists := requiredFields[r]; exists {
				requiredFields[r] = true
			}
		}
		for field, found := range requiredFields {
			if !found {
				t.Errorf("'%s' should be in required array", field)
			}
		}
	})
}

func TestWriteFileTool_Format(t *testing.T) {
	tool := WriteFileTool{}

	t.Run("format with success result", func(t *testing.T) {
		result := tool.Format(`{"path": "test.txt"}`, "Successfully wrote to test.txt", nil)
		if !strings.Contains(result, "Write File") {
			t.Error("format should include tool name")
		}
		if !strings.Contains(result, "test.txt") {
			t.Error("format should include path")
		}
		if !strings.Contains(result, "successfully") {
			t.Error("format should indicate success")
		}
	})

	t.Run("format with error", func(t *testing.T) {
		testErr := os.ErrPermission
		result := tool.Format(`{"path": "test.txt"}`, "", testErr)
		if !strings.Contains(result, "Error") {
			t.Error("format should show error")
		}
	})

	t.Run("format with empty path", func(t *testing.T) {
		result := tool.Format(`{"path": ""}`, "Successfully wrote", nil)
		if !strings.Contains(result, "Write File") {
			t.Error("format should still include tool name")
		}
	})
}

func TestWriteFileTool_ExplicitProjectRootDiffersFromGetwd(t *testing.T) {
	// Daemon-mode scenario: projectRoot != os.Getwd()
	projectDir := t.TempDir()
	processDir := t.TempDir()
	t.Chdir(processDir)

	// Create a subdirectory in the project
	if err := os.MkdirAll(filepath.Join(projectDir, "src"), 0755); err != nil {
		t.Fatal(err)
	}

	tool := WriteFileTool{ProjectRoot: projectDir}

	t.Run("write to file inside projectRoot with absolute path succeeds", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "`+filepath.Join(projectDir, "src", "main.go")+`", "content": "package main"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("expected success message, got %q", result)
		}

		content, err := os.ReadFile(filepath.Join(projectDir, "src", "main.go"))
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "package main" {
			t.Errorf("expected 'package main', got %q", string(content))
		}
	})

	t.Run("write to file inside projectRoot with relative path succeeds", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "src/util.go", "content": "package util"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("expected success message, got %q", result)
		}

		content, err := os.ReadFile(filepath.Join(projectDir, "src", "util.go"))
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "package util" {
			t.Errorf("expected 'package util', got %q", string(content))
		}
	})

	t.Run("write to path in cwd (outside projectRoot) is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "`+filepath.Join(processDir, "outside.txt")+`", "content": "bad"}`)
		if err == nil {
			t.Error("expected error for write to cwd outside projectRoot")
		}
	})

	t.Run("path traversal beyond projectRoot is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"path": "`+filepath.Join(projectDir, "..", "escape.txt")+`", "content": "bad"}`)
		if err == nil {
			t.Error("expected error for path traversal beyond projectRoot")
		}
	})

	t.Run("write to new file inside projectRoot with relative path succeeds", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"path": "newdir/new.txt", "content": "new"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("expected success message, got %q", result)
		}
	})
}

// TestWriteFileTool_WorktreeContext verifies that a context-carried RepoInfo
// (edict 943) routes writes into the edict's worktree, leaving main untouched.
func TestWriteFileTool_WorktreeContext(t *testing.T) {
	mainRoot := t.TempDir()
	wtRel := filepath.Join(".agents", "worktrees", "e42")
	if err := os.MkdirAll(filepath.Join(mainRoot, wtRel), 0755); err != nil {
		t.Fatal(err)
	}

	tool := WriteFileTool{ProjectRoot: mainRoot}
	ctx := worktreeCtx(mainRoot, wtRel)

	if _, err := tool.Call(ctx, `{"path": "out.txt", "content": "hello"}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(mainRoot, wtRel, "out.txt")); err != nil {
		t.Errorf("expected file in worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainRoot, "out.txt")); err == nil {
		t.Error("main tree was polluted by a worktree-context write")
	}
	if tool.ProjectRoot != mainRoot {
		t.Errorf("registered ProjectRoot mutated: %q", tool.ProjectRoot)
	}
}
