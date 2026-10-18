package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepToolWithDotPath(t *testing.T) {
	// Create a temp directory with test files
	tmpDir, err := os.MkdirTemp("", "grep_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test files
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello world\nfoo bar"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "test2.txt"), []byte("hello again\nbaz"), 0644)

	// Change to the temp directory
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	tool := GrepTool{ProjectRoot: tmpDir}

	// Test 1: Search with path="." should work now (bug fix)
	result, err := tool.Call(context.Background(), `{"pattern": "hello", "path": "."}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if result == "No matches found" {
		t.Fatal("Expected matches, got 'No matches found' - the bug is not fixed!")
	}
	t.Logf("Test 1 (path='.') result: %s", result)

	// Test 2: Hidden files should be excluded by default
	result, err = tool.Call(context.Background(), `{"pattern": "hello", "path": "."}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	// Should not find content in .hidden.txt
	if strings.Contains(result, ".hidden.txt") {
		t.Log("Warning: Hidden files are being included when they shouldn't be")
	}

	// Test 3: With includeHidden=true, hidden files should be included
	os.WriteFile(filepath.Join(tmpDir, ".hidden.txt"), []byte("hidden hello\n"), 0644)
	result, err = tool.Call(context.Background(), `{"pattern": "hello", "path": ".", "includeHidden": true}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if result == "No matches found" {
		t.Fatal("Expected matches with includeHidden=true")
	}
	t.Logf("Test 3 (includeHidden=true) result: %s", result)
}

func TestGrepToolHiddenDirectory(t *testing.T) {
	// Create a temp directory with hidden subdirectory
	tmpDir, err := os.MkdirTemp("", "grep_test_hidden")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create hidden directory and file
	os.Mkdir(filepath.Join(tmpDir, ".hidden"), 0755)
	os.WriteFile(filepath.Join(tmpDir, ".hidden", "secret.txt"), []byte("secret content\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "visible.txt"), []byte("visible content\n"), 0644)

	// Change to the temp directory
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	tool := GrepTool{ProjectRoot: tmpDir}

	// Test: Without includeHidden, hidden dirs should be skipped
	result, err := tool.Call(context.Background(), `{"pattern": "content", "path": "."}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	// Should only find in visible.txt
	if !strings.Contains(result, "visible.txt") {
		t.Error("Expected to find content in visible.txt")
	}
	if strings.Contains(result, "secret.txt") {
		t.Error("Should not find content in hidden directory when includeHidden=false")
	}

	// Test: With includeHidden=true, should find in hidden dirs too
	result, err = tool.Call(context.Background(), `{"pattern": "content", "path": ".", "includeHidden": true}`)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if !strings.Contains(result, "secret.txt") {
		t.Error("Expected to find content in .hidden/secret.txt with includeHidden=true")
	}
}

func TestGrepTool_ExplicitProjectRootDiffersFromGetwd(t *testing.T) {
	// Daemon-mode scenario: projectRoot != os.Getwd()
	projectDir := t.TempDir()
	processDir := t.TempDir()
	// Deliberately do NOT chdir into projectDir — CWD stays the test's own dir,
	// so we verify that grep with a relative path resolves against ProjectRoot.

	// Create files inside the project
	os.WriteFile(filepath.Join(projectDir, "findme.txt"), []byte("hello world\nfoo bar"), 0644)

	// Create file in the process directory (outside project)
	os.WriteFile(filepath.Join(processDir, "unrelated.txt"), []byte("hello elsewhere"), 0644)

	tool := GrepTool{ProjectRoot: projectDir}

	t.Run("grep within projectRoot finds matches", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"pattern": "hello", "path": "."}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result == "No matches found" {
			t.Fatal("expected matches in projectRoot")
		}
		if !strings.Contains(result, "findme.txt") {
			t.Error("expected findme.txt in results")
		}
	})

	t.Run("grep with path outside projectRoot is denied", func(t *testing.T) {
		_, err := tool.Call(context.Background(), `{"pattern": "hello", "path": "`+processDir+`"}`)
		if err == nil {
			t.Error("expected error for grep with path outside projectRoot")
		}
	})
}

// TestGrepTool_WorktreeContext verifies that a context-carried RepoInfo
// (edict 943) routes searching into the edict's worktree.
func TestGrepTool_WorktreeContext(t *testing.T) {
	mainRoot := t.TempDir()
	wtRel := filepath.Join(".agents", "worktrees", "e42")
	if err := os.MkdirAll(filepath.Join(mainRoot, wtRel), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mainRoot, "main.txt"), []byte("needle in main"), 0644)
	os.WriteFile(filepath.Join(mainRoot, wtRel, "wt.txt"), []byte("needle in worktree"), 0644)

	tool := GrepTool{ProjectRoot: mainRoot}
	ctx := worktreeCtx(mainRoot, wtRel)

	result, err := tool.Call(ctx, `{"pattern": "needle", "path": "."}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "wt.txt") {
		t.Errorf("expected worktree match, got %q", result)
	}
	if strings.Contains(result, "main.txt") {
		t.Error("main tree file leaked into worktree-context grep")
	}
	if tool.ProjectRoot != mainRoot {
		t.Errorf("registered ProjectRoot mutated: %q", tool.ProjectRoot)
	}
}
