package utils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetFileTreeRespectsGitignore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.log\nbuild/\n")
	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, "app.log", "noise")
	writeFile(t, root, "build/out.go", "package out")

	files, err := GetFileTree(root)
	require.NoError(t, err)

	assert.Contains(t, files, "main.go")
	assert.NotContains(t, files, "app.log")
	assert.NotContains(t, files, "build/out.go")
}

func TestGetFileTreeAlwaysSkipsBuiltinDirs(t *testing.T) {
	root := t.TempDir()
	// No .gitignore at all: the built-ins must still apply.
	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, ".git/config", "[core]")
	writeFile(t, root, "vendor/dep/dep.go", "package dep")

	files, err := GetFileTree(root)
	require.NoError(t, err)

	assert.Equal(t, []string{"main.go"}, files)
}

func TestGetFileTreeIgnoresNestedNameMatch(t *testing.T) {
	root := t.TempDir()
	// A pattern without a slash matches at any depth.
	writeFile(t, root, ".gitignore", "node_modules/\n")
	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, "pkg/node_modules/dep.js", "module.exports = {}")

	files, err := GetFileTree(root)
	require.NoError(t, err)

	// The .gitignore itself is a tracked file and is listed; the nested
	// node_modules directory is pruned because the pattern has no slash.
	assert.Equal(t, []string{".gitignore", "main.go"}, files)
}

func TestGetFileTreeWithoutGitignoreIgnoresNothingExtra(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, "app.log", "kept")

	files, err := GetFileTree(root)
	require.NoError(t, err)

	assert.Equal(t, []string{"app.log", "main.go"}, files)
}

func TestGetFileTreeOverrideReincludesHeavenAndEarth(t *testing.T) {
	root := t.TempDir()
	// .gitignore excludes both dirs; the override must re-include them.
	writeFile(t, root, ".gitignore", "heaven/\nearth/\n*.log\n")
	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, "heaven/probe.txt", "testimony")
	writeFile(t, root, "heaven/deep/run.log", "ci run")
	writeFile(t, root, "earth/hr.go", "package earth")
	writeFile(t, root, "noise.log", "ignored")

	files, err := GetFileTree(root)
	require.NoError(t, err)

	assert.Contains(t, files, "heaven/probe.txt")
	assert.Contains(t, files, "heaven/deep/run.log")
	assert.Contains(t, files, "earth/hr.go")
	assert.Contains(t, files, "main.go")
	// A non-overridden gitignore match is still ignored.
	assert.NotContains(t, files, "noise.log")
}

func TestGetFileTreeOverrideAppliesWithoutGitignore(t *testing.T) {
	root := t.TempDir()
	// No .gitignore: override paths are simply walked as normal.
	writeFile(t, root, "heaven/note.md", "hello")
	writeFile(t, root, "main.go", "package main")

	files, err := GetFileTree(root)
	require.NoError(t, err)

	assert.Equal(t, []string{"heaven/note.md", "main.go"}, files)
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
