package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afittestide/asimi/internal/repo"
)

// TestResolvedRoot covers worktree-aware root resolution (edict 943): a context
// RepoInfoKey carrying an edict worktree wins, the registered field is the
// fallback, and the registered field is never mutated.
func TestResolvedRoot(t *testing.T) {
	t.Run("no context repo info falls back to registered root", func(t *testing.T) {
		if got := resolvedRoot(context.Background(), "/main"); got != "/main" {
			t.Errorf("resolvedRoot = %q, want /main", got)
		}
	})

	t.Run("context repo info with worktree wins", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), RepoInfoKey{}, repo.RepoInfo{
			ProjectRoot:  "/main",
			WorktreePath: ".agents/worktrees/e42",
			IsWorktree:   true,
		})
		if got := resolvedRoot(ctx, "/main"); got != "/main/.agents/worktrees/e42" {
			t.Errorf("resolvedRoot = %q, want /main/.agents/worktrees/e42", got)
		}
	})

	t.Run("empty effective root falls back to registered root", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), RepoInfoKey{}, repo.RepoInfo{})
		if got := resolvedRoot(ctx, "/main"); got != "/main" {
			t.Errorf("resolvedRoot = %q, want /main", got)
		}
	})
}

// worktreeCtx builds a context carrying an edict's worktree-aware RepoInfo, as
// Session.executeToolCall does at dispatch. edict 943.
func worktreeCtx(mainRoot, worktree string) context.Context {
	return context.WithValue(context.Background(), RepoInfoKey{}, repo.RepoInfo{
		ProjectRoot:  mainRoot,
		WorktreePath: worktree,
		IsWorktree:   true,
	})
}

func TestResolvePath(t *testing.T) {
	t.Run("absolute path is cleaned", func(t *testing.T) {
		got := ResolvePath("/foo/bar/../baz", "/project")
		want := "/foo/baz"
		if got != want {
			t.Errorf("ResolvePath(%q, %q) = %q, want %q", "/foo/bar/../baz", "/project", got, want)
		}
	})

	t.Run("relative path with projectRoot joins and cleans", func(t *testing.T) {
		got := ResolvePath("src/main.go", "/project")
		want := "/project/src/main.go"
		if got != want {
			t.Errorf("ResolvePath(%q, %q) = %q, want %q", "src/main.go", "/project", got, want)
		}
	})

	t.Run("relative path with dot in projectRoot is cleaned", func(t *testing.T) {
		got := ResolvePath("sub/file.txt", "/project/./root")
		want := "/project/root/sub/file.txt"
		if got != want {
			t.Errorf("ResolvePath(%q, %q) = %q, want %q", "sub/file.txt", "/project/./root", got, want)
		}
	})

	t.Run("relative path with empty projectRoot uses filepath.Abs", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		got := ResolvePath("local.txt", "")
		want := filepath.Join(tmpDir, "local.txt")
		if got != want {
			t.Errorf("ResolvePath(%q, %q) = %q, want %q", "local.txt", "", got, want)
		}
	})

	t.Run("absolute path ignores projectRoot", func(t *testing.T) {
		got := ResolvePath("/absolute/path", "/different/root")
		want := "/absolute/path"
		if got != want {
			t.Errorf("ResolvePath(%q, %q) = %q, want %q", "/absolute/path", "/different/root", got, want)
		}
	})
}

func TestValidatePathWithinProject_ExplicitProjectRoot(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir() // separate temp dir acts as "outside"

	// Create a file inside the project
	insideFile := filepath.Join(projectDir, "inside.txt")
	if err := os.WriteFile(insideFile, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a file outside the project
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("path inside explicit project root is allowed", func(t *testing.T) {
		err := ValidatePathWithinProject(insideFile, projectDir)
		if err != nil {
			t.Errorf("expected no error for path inside project root, got: %v", err)
		}
	})

	t.Run("relative path inside explicit project root is allowed", func(t *testing.T) {
		// No chdir needed — ResolvePath uses projectRoot for relative paths
		err := ValidatePathWithinProject("inside.txt", projectDir)
		if err != nil {
			t.Errorf("expected no error for relative path inside project root, got: %v", err)
		}
	})

	t.Run("path outside explicit project root is denied", func(t *testing.T) {
		err := ValidatePathWithinProject(outsideFile, projectDir)
		if err == nil {
			t.Error("expected error for path outside project root")
		}
	})

	t.Run("path traversal beyond explicit project root is denied", func(t *testing.T) {
		err := ValidatePathWithinProject(filepath.Join(projectDir, "..", "escape.txt"), projectDir)
		if err == nil {
			t.Error("expected error for path traversal beyond project root")
		}
	})

	t.Run("absolute path to /etc/passwd denied when root is tmpdir", func(t *testing.T) {
		err := ValidatePathWithinProject("/etc/passwd", projectDir)
		if err == nil {
			t.Error("expected error for /etc/passwd")
		}
	})

	t.Run("empty path always denied", func(t *testing.T) {
		err := ValidatePathWithinProject("", projectDir)
		if err == nil {
			t.Error("expected error for empty path")
		}
	})
}

func TestValidatePathWithinProject_EmptyProjectRoot_ReturnsError(t *testing.T) {
	// When projectRoot is empty, ValidatePathWithinProject returns an error
	// because SetContext is the sole authority for project root in daemon mode.
	err := ValidatePathWithinProject("local.txt", "")
	if err == nil {
		t.Error("expected error for empty projectRoot")
	}
	if !strings.Contains(err.Error(), "project root not set") {
		t.Errorf("expected 'project root not set' error, got: %v", err)
	}
}

func TestValidatePathWithinProject_ProjectRootDiffersFromGetwd(t *testing.T) {
	// This is the key daemon-mode test: the process cwd may differ from projectRoot.
	// Create two directories: one is the "project root" (configured), the other is
	// where the process actually runs (os.Getwd).
	projectDir := t.TempDir()
	processDir := t.TempDir()
	t.Chdir(processDir) // process runs from a different directory

	// Create files in each directory
	projectFile := filepath.Join(projectDir, "src", "main.go")
	if err := os.MkdirAll(filepath.Join(projectDir, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectFile, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}
	processFile := filepath.Join(processDir, "unrelated.txt")
	if err := os.WriteFile(processFile, []byte("unrelated"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("projectRoot is used, not os.Getwd()", func(t *testing.T) {
		// Absolute path inside projectRoot should be allowed even though cwd is different
		err := ValidatePathWithinProject(projectFile, projectDir)
		if err != nil {
			t.Errorf("expected no error for file in projectRoot (not cwd), got: %v", err)
		}
	})

	t.Run("path inside cwd but outside projectRoot is denied", func(t *testing.T) {
		// A file in the process directory should be denied when projectRoot is different
		err := ValidatePathWithinProject(processFile, projectDir)
		if err == nil {
			t.Error("expected error for file in cwd but outside projectRoot")
		}
	})

	t.Run("relative path resolves against projectRoot, not cwd", func(t *testing.T) {
		// A relative path like "unrelated.txt" now resolves against projectRoot,
		// not cwd. It becomes <projectDir>/unrelated.txt which is inside
		// projectRoot — so it is allowed.
		err := ValidatePathWithinProject("unrelated.txt", projectDir)
		if err != nil {
			t.Errorf("expected no error for relative path resolved against projectRoot, got: %v", err)
		}
	})

	t.Run("relative path inside projectRoot resolves correctly", func(t *testing.T) {
		// No chdir needed — ResolvePath uses projectRoot for relative paths
		err := ValidatePathWithinProject("src/main.go", projectDir)
		if err != nil {
			t.Errorf("expected no error for relative path inside projectRoot, got: %v", err)
		}
	})

	t.Run("relative path resolves against projectRoot when CWD is different", func(t *testing.T) {
		// CWD is processDir (set above), but the relative path should resolve
		// against projectRoot, not CWD.
		err := ValidatePathWithinProject("src/main.go", projectDir)
		if err != nil {
			t.Errorf("expected no error for relative path resolved against projectRoot, got: %v", err)
		}
	})
}

func TestValidatePathWithinProject_SymlinkHandling(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()

	// Create a file outside the project
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside the project pointing outside
	linkPath := filepath.Join(projectDir, "link.txt")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	t.Run("symlink inside project pointing outside is denied", func(t *testing.T) {
		err := ValidatePathWithinProject(linkPath, projectDir)
		if err == nil {
			t.Error("expected error for symlink pointing outside project root")
		}
	})

	t.Run("regular file inside project is allowed", func(t *testing.T) {
		regularFile := filepath.Join(projectDir, "regular.txt")
		if err := os.WriteFile(regularFile, []byte("ok"), 0644); err != nil {
			t.Fatal(err)
		}
		err := ValidatePathWithinProject(regularFile, projectDir)
		if err != nil {
			t.Errorf("expected no error for regular file inside project, got: %v", err)
		}
	})
}

func TestValidatePathWithinProject_NestedDirectories(t *testing.T) {
	projectDir := t.TempDir()

	// Create deeply nested file
	nestedPath := filepath.Join(projectDir, "a", "b", "c", "deep.txt")
	if err := os.MkdirAll(filepath.Dir(nestedPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nestedPath, []byte("deep"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("deeply nested path inside project is allowed", func(t *testing.T) {
		err := ValidatePathWithinProject(nestedPath, projectDir)
		if err != nil {
			t.Errorf("expected no error for deeply nested path, got: %v", err)
		}
	})

	t.Run("project root itself is allowed", func(t *testing.T) {
		err := ValidatePathWithinProject(projectDir, projectDir)
		if err != nil {
			t.Errorf("expected no error for project root itself, got: %v", err)
		}
	})
}

func TestValidatePathWithinProject_NewFileCreation(t *testing.T) {
	projectDir := t.TempDir()

	// File doesn't exist yet but its parent directory does
	newFilePath := filepath.Join(projectDir, "newfile.txt")

	t.Run("non-existent file inside project is allowed", func(t *testing.T) {
		err := ValidatePathWithinProject(newFilePath, projectDir)
		if err != nil {
			t.Errorf("expected no error for new file inside project, got: %v", err)
		}
	})

	t.Run("non-existent file outside project is denied", func(t *testing.T) {
		err := ValidatePathWithinProject("/etc/newfile.txt", projectDir)
		if err == nil {
			t.Error("expected error for new file outside project")
		}
	})

	t.Run("non-existent file in non-existent directory inside project is allowed", func(t *testing.T) {
		// Neither the directory nor the file exists yet — common when creating new files
		newDirFile := filepath.Join(projectDir, "newdir", "new.txt")
		err := ValidatePathWithinProject(newDirFile, projectDir)
		if err != nil {
			t.Errorf("expected no error for new file in new dir inside project, got: %v", err)
		}
	})
}
