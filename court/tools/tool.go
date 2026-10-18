package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/afittestide/asimi/internal/config"
)

// Tool defines a tool that can be invoked by ministers
type Tool interface {
	Name() string
	Description() string
	Call(ctx context.Context, input string) (string, error)
	Format(input, result string, err error) string
	ParameterSchema() map[string]any
}

// ResolvePath resolves a path in the context of a project root.
// If path is absolute, it returns filepath.Clean(path).
// If path is relative and projectRoot is non-empty, it returns
// filepath.Clean(filepath.Join(projectRoot, path)).
// If path is relative and projectRoot is empty, it returns
// filepath.Abs(path) for backward compatibility.
func ResolvePath(path, projectRoot string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if projectRoot != "" {
		return filepath.Clean(filepath.Join(projectRoot, path))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		// filepath.Abs should not fail in normal conditions,
		// but fall back to a cleaned path if it does.
		return filepath.Clean(path)
	}
	return abs
}

// resolvedRoot returns the context-propagated effective root when present, else
// the tool's registered projectRoot, preserving behavior outside rituals.
func resolvedRoot(ctx context.Context, registered string) string {
	if ri, ok := RepoInfoFromContext(ctx); ok {
		if r := ri.EffectiveRoot(); r != "" {
			return r
		}
	}
	return registered
}

// ValidatePathWithinProject checks if a file path is within the project root directory.
// It prevents path traversal attacks and ensures files are only modified within the project tree.
// Returns an error if projectRoot is empty — SetContext is the sole authority for project root.
func ValidatePathWithinProject(path, projectRoot string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}

	if projectRoot == "" {
		return fmt.Errorf("project root not set: SetContext is the sole authority for project root in daemon mode")
	}

	// Resolve the path using projectRoot context
	absPath := ResolvePath(path, projectRoot)

	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to resolve project root: %w", err)
	}

	// Clean the paths to resolve any .. or . components
	absPath = filepath.Clean(absPath)
	absRoot = filepath.Clean(absRoot)

	// Evaluate symlinks to get the real path
	// This prevents writing through symlinks to locations outside the project root
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// File (or parent dirs) may not exist yet. Walk up to find the
		// nearest existing ancestor, resolve its symlinks, then append
		// the remaining non-existent path components.
		dir := absPath
		extra := ""
		for {
			realDir, evalErr := filepath.EvalSymlinks(dir)
			if evalErr == nil {
				realPath = filepath.Join(realDir, extra)
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				// Reached filesystem root without resolving; use cleaned path
				realPath = absPath
				break
			}
			extra = filepath.Join(filepath.Base(dir), extra)
			dir = parent
		}
	}

	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		// If root symlink evaluation fails, use the cleaned path
		realRoot = absRoot
	}

	// Check if the file path is within the project root
	relPath, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return fmt.Errorf("failed to determine relative path: %w", err)
	}

	// If the relative path starts with "..", it's outside the project root
	if strings.HasPrefix(relPath, "..") {
		return fmt.Errorf("access denied: path '%s' is outside the project root '%s'", path, projectRoot)
	}

	return nil
}

// GetFileTools returns the list of file-based tools for use by ministers.
func GetFileTools(llmConfig config.LLMConfig, projectRoot string) []Tool {
	return []Tool{
		NewReadFileTool(projectRoot),
		WriteFileTool{ProjectRoot: projectRoot},
		GlobTool{ProjectRoot: projectRoot},
		ReplaceTextTool{ProjectRoot: projectRoot},
		ReadManyFilesTool{ProjectRoot: projectRoot},
		GrepTool{ProjectRoot: projectRoot},
	}
}

// GetROTools returns read-only tools for exploration and research.
func GetROTools(llmConfig config.LLMConfig, projectRoot string) []Tool {
	return []Tool{
		GlobTool{ProjectRoot: projectRoot},
		NewReadFileTool(projectRoot),
		ReadManyFilesTool{ProjectRoot: projectRoot},
		GrepTool{ProjectRoot: projectRoot},
	}
}

// GetEditTools returns read/write tools for code editing (no shell).
func GetEditTools(llmConfig config.LLMConfig, projectRoot string) []Tool {
	return []Tool{
		NewReadFileTool(projectRoot),
		WriteFileTool{ProjectRoot: projectRoot},
		ReplaceTextTool{ProjectRoot: projectRoot},
		GlobTool{ProjectRoot: projectRoot},
		ReadManyFilesTool{ProjectRoot: projectRoot},
		GrepTool{ProjectRoot: projectRoot},
	}
}
