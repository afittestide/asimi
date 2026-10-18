package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadManyFilesTool_RelativePatternWithProjectRoot(t *testing.T) {
	// Scenario: CWD != ProjectRoot (daemon mode)
	// Relative glob patterns must resolve against ProjectRoot, not CWD.
	projectDir := t.TempDir()
	processDir := t.TempDir()

	// Set CWD to processDir (different from projectDir)
	t.Chdir(processDir)

	// Create files inside the project directory
	projectFiles := map[string]string{
		filepath.Join(projectDir, "a.txt"):        "alpha",
		filepath.Join(projectDir, "sub", "b.txt"): "beta",
		filepath.Join(projectDir, "sub", "c.go"):  "package sub",
	}
	for path, content := range projectFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create a file in processDir that must NOT be found
	if err := os.WriteFile(filepath.Join(processDir, "decoy.txt"), []byte("decoy"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := ReadManyFilesTool{ProjectRoot: projectDir}

	t.Run("relative glob resolves against ProjectRoot", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"paths": ["*.txt"]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "alpha") {
			t.Error("expected projectDir/a.txt content in result")
		}
		if strings.Contains(result, "decoy") {
			t.Error("did not expect processDir/decoy.txt content in result")
		}
	})

	t.Run("relative glob with subdirectory pattern", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"paths": ["sub/*.txt"]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "beta") {
			t.Error("expected projectDir/sub/b.txt content in result")
		}
		if strings.Contains(result, "package sub") {
			t.Error("did not expect .go file content (pattern was *.txt)")
		}
	})

	t.Run("relative glob with recursive star pattern", func(t *testing.T) {
		result, err := tool.Call(context.Background(), `{"paths": ["**/*.txt"]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "alpha") {
			t.Error("expected projectDir/a.txt content in result")
		}
		if !strings.Contains(result, "beta") {
			t.Error("expected projectDir/sub/b.txt content in result")
		}
		if strings.Contains(result, "decoy") {
			t.Error("did not expect processDir/decoy.txt content in result")
		}
	})

	t.Run("absolute pattern is not joined with ProjectRoot", func(t *testing.T) {
		// An absolute path should be used as-is, not prefixed with ProjectRoot
		absPath := filepath.Join(projectDir, "a.txt")
		result, err := tool.Call(context.Background(), `{"paths": ["`+absPath+`"]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "alpha") {
			t.Error("expected a.txt content via absolute path")
		}
	})
}

func TestReadManyFilesTool_NoProjectRoot(t *testing.T) {
	// Without ProjectRoot, relative globs resolve against CWD
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "hello.txt"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := ReadManyFilesTool{ProjectRoot: tmpDir}
	result, err := tool.Call(context.Background(), `{"paths": ["*.txt"]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "world") {
		t.Error("expected hello.txt content when ProjectRoot is set")
	}
}

// TestReadManyFilesTool_WorktreeContext verifies that a context-carried RepoInfo
// (edict 943) routes globbed reads into the edict's worktree.
func TestReadManyFilesTool_WorktreeContext(t *testing.T) {
	mainRoot := t.TempDir()
	wtRel := filepath.Join(".agents", "worktrees", "e42")
	if err := os.MkdirAll(filepath.Join(mainRoot, wtRel), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mainRoot, "hello.txt"), []byte("main world"), 0644)
	os.WriteFile(filepath.Join(mainRoot, wtRel, "hello.txt"), []byte("worktree world"), 0644)

	tool := ReadManyFilesTool{ProjectRoot: mainRoot}
	ctx := worktreeCtx(mainRoot, wtRel)

	result, err := tool.Call(ctx, `{"paths": ["*.txt"]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "worktree world") {
		t.Errorf("expected worktree content, got %q", result)
	}
	if strings.Contains(result, "main world") {
		t.Error("main tree file leaked into worktree-context read")
	}
	if tool.ProjectRoot != mainRoot {
		t.Errorf("registered ProjectRoot mutated: %q", tool.ProjectRoot)
	}
}
