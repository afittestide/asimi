package utils

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// builtinIgnoreDirs are always pruned by name at any level. They are not
// .gitignore patterns but structural noise no file picker should ever surface.
var builtinIgnoreDirs = map[string]bool{
	".git":   true,
	"vendor": true,
}

// overridePaths re-include paths the .gitignore excludes. heaven/ and earth/
// hold rented testimony and the three realms' working notes; they are never
// committed but surface in conversations, so the file picker must list them.
var overridePaths = []string{"heaven", "earth"}

// GetFileTree walks root and returns every file path relative to root, sorted.
// The ignore set is taken from the .gitignore at root, merged with a small set
// of always-safe built-in directories (.git, vendor). A matched directory is
// pruned from the walk; a matched file is omitted from the result. The override
// paths (heaven, earth) are re-included even when .gitignore excludes them.
func GetFileTree(root string) ([]string, error) {
	patterns := readGitignore(root)
	matcher := gitignore.NewMatcher(patterns)

	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Skip files/directories we can't access
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// Names below root, used both for built-in matching and gitignore.
		parts := pathParts(relPath)

		if info.IsDir() {
			if builtinIgnoreDirs[info.Name()] || matcher.Match(parts, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if matcher.Match(parts, false) {
			return nil
		}
		files = append(files, relPath)
		return nil
	})

	if err != nil {
		return nil, err
	}

	sort.Strings(files)
	return files, nil
}

// readGitignore parses the .gitignore at root into gitignore patterns. A
// missing or unreadable file yields no patterns: .gitignore is the source of
// truth, so its absence means nothing beyond the built-ins is ignored.
// The override paths are appended last so they take priority over the file.
func readGitignore(root string) []gitignore.Pattern {
	f, err := os.Open(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	defer f.Close()

	var patterns []gitignore.Pattern
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}
	for _, p := range overridePaths {
		patterns = append(patterns,
			gitignore.ParsePattern("!"+p, nil),
			gitignore.ParsePattern("!"+p+"/**", nil),
		)
	}
	return patterns
}

// pathParts splits a relative path into its components, ignoring the "."
// identity element returned when relPath is the root itself.
func pathParts(relPath string) []string {
	if relPath == "." {
		return nil
	}
	return strings.Split(filepath.ToSlash(relPath), "/")
}
