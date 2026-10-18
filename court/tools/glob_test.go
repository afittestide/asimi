package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobTool(t *testing.T) {
	// Save current directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	// Create a temporary directory structure for testing
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Create test files
	testFiles := []string{
		"root.go",
		filepath.Join("sub1", "file1.go"),
		filepath.Join("sub1", "file2.go"),
		filepath.Join("sub1", "nested", "deep.go"),
		filepath.Join("sub2", "file3.go"),
	}

	for _, f := range testFiles {
		dir := filepath.Dir(f)
		if dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(f, []byte("package test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("recursive pattern matches root and nested files", func(t *testing.T) {
		tool := GlobTool{ProjectRoot: tmpDir}
		result, err := tool.Call(context.Background(), `{"pattern": "**/*.go"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should find all .go files including root.go
		if result == "No matches found" {
			t.Fatal("expected matches, got none")
		}

		// Check that root.go is included
		if !containsLine(result, "root.go") {
			t.Error("expected root.go to be included in results")
		}

		// Check that nested files are included
		if !containsLine(result, filepath.Join("sub1", "file1.go")) {
			t.Error("expected sub1/file1.go to be included")
		}
		if !containsLine(result, filepath.Join("sub1", "nested", "deep.go")) {
			t.Error("expected sub1/nested/deep.go to be included")
		}
	})

	t.Run("regular pattern works", func(t *testing.T) {
		tool := GlobTool{ProjectRoot: tmpDir}
		result, err := tool.Call(context.Background(), `{"pattern": "*.go"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !containsLine(result, "root.go") {
			t.Error("expected root.go to be included")
		}
	})
}

func containsLine(result, expected string) bool {
	for _, line := range splitLines(result) {
		line = strings.TrimSpace(line)
		if line == expected || filepath.Base(line) == expected || strings.HasSuffix(line, string(filepath.Separator)+expected) {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	for _, line := range []byte(s) {
		if line == '\n' {
			lines = append(lines, "")
		} else {
			if len(lines) == 0 {
				lines = append(lines, "")
			}
			lines[len(lines)-1] += string(line)
		}
	}
	return lines
}

func TestGlobTool_ExplicitProjectRootDiffersFromGetwd(t *testing.T) {
	// Daemon-mode scenario: projectRoot != os.Getwd()
	projectDir := t.TempDir()
	processDir := t.TempDir()
	t.Chdir(processDir)

	// Create files inside the project
	for _, f := range []string{
		filepath.Join(projectDir, "root.go"),
		filepath.Join(projectDir, "sub", "file1.go"),
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("package test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create file in the process directory (outside project)
	if err := os.WriteFile(filepath.Join(processDir, "unrelated.go"), []byte("package unrelated"), 0644); err != nil {
		t.Fatal(err)
	}

	// Change into project directory for glob to find files
	tool := GlobTool{ProjectRoot: projectDir}

	t.Run("glob finds files inside projectRoot", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"pattern": "**/*.go"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result == "No matches found" {
			t.Fatal("expected matches, got none")
		}
		if !containsLine(result, filepath.Join(projectDir, "root.go")) {
			t.Error("expected root.go in results")
		}
		if !containsLine(result, filepath.Join(projectDir, "sub", "file1.go")) {
			t.Error("expected sub/file1.go in results")
		}
		// Verify the unrelated file outside projectRoot is NOT in results
		if containsLine(result, filepath.Join(processDir, "unrelated.go")) {
			t.Error("did not expect unrelated.go in results")
		}
	})
}

// TestGlobTool_WorktreeContext verifies that a context-carried RepoInfo
// (edict 943) routes globbing into the edict's worktree.
func TestGlobTool_WorktreeContext(t *testing.T) {
	mainRoot := t.TempDir()
	wtRel := filepath.Join(".agents", "worktrees", "e42")
	if err := os.MkdirAll(filepath.Join(mainRoot, wtRel), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mainRoot, "main_only.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(mainRoot, wtRel, "wt_only.go"), []byte("package main"), 0644)

	tool := GlobTool{ProjectRoot: mainRoot}
	ctx := worktreeCtx(mainRoot, wtRel)

	result, err := tool.Call(ctx, `{"pattern": "**/*.go"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsLine(result, filepath.Join(mainRoot, wtRel, "wt_only.go")) {
		t.Errorf("expected worktree file in results, got %q", result)
	}
	if containsLine(result, filepath.Join(mainRoot, "main_only.go")) {
		t.Error("main tree file leaked into worktree-context glob")
	}
	if tool.ProjectRoot != mainRoot {
		t.Errorf("registered ProjectRoot mutated: %q", tool.ProjectRoot)
	}
}
