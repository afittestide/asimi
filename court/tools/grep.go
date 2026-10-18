package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/afittestide/asimi/internal/utils"
)

// GrepInput is the input for the GrepTool
type GrepInput struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path,omitempty"`
	IncludeHidden bool   `json:"includeHidden,omitempty"`
}

// GrepTool searches for patterns in files
type GrepTool struct {
	ProjectRoot string
}

func (t GrepTool) Name() string {
	return "grep"
}

func (t GrepTool) Description() string {
	return "Searches for a pattern in files. The input should be a JSON object with 'pattern' (regex) and optional 'path' (file or directory, defaults to '.'). Returns matching lines with file:line: prefix."
}

func (t GrepTool) Call(ctx context.Context, input string) (string, error) {
	var params GrepInput
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if params.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if params.Path == "" {
		params.Path = "."
	}
	root := resolvedRoot(ctx, t.ProjectRoot)
	if root != "" && !filepath.IsAbs(params.Path) {
		params.Path = filepath.Join(root, params.Path)
	}

	if err := ValidatePathWithinProject(params.Path, root); err != nil {
		return "", err
	}

	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	var results []string
	maxResults := 100 // Limit results to avoid overwhelming output

	err = filepath.Walk(params.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}
		name := info.Name()
		isHidden := name != "." && strings.HasPrefix(name, ".")

		if info.IsDir() {
			if isHidden && !params.IncludeHidden {
				return filepath.SkipDir
			}
			if name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if isHidden && !params.IncludeHidden {
			return nil
		}
		if info.Size() > 1<<20 { // Skip files > 1MB
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil // Skip files we can't read
		}

		lines := strings.Split(string(content), "\n")
		for lineNum, line := range lines {
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d:%s", path, lineNum+1, line))
				if len(results) >= maxResults {
					return fmt.Errorf("max results reached")
				}
			}
		}
		return nil
	})

	if err != nil && err.Error() != "max results reached" {
		return "", err
	}

	if len(results) == 0 {
		return "No matches found", nil
	}

	output := strings.Join(results, "\n")
	if len(results) >= maxResults {
		output += fmt.Sprintf("\n... (truncated at %d results)", maxResults)
	}
	return output, nil
}

func (t GrepTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regular expression pattern to search for",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "File or directory to search in (defaults to '.')",
			},
			"includeHidden": map[string]any{
				"type":        "boolean",
				"description": "Include hidden files and directories in search (similar to ripgrep's --hidden flag)",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t GrepTool) Format(input, result string, err error) string {
	var params GrepInput
	json.Unmarshal([]byte(input), &params)

	path := params.Path
	if path == "" {
		path = "."
	}

	msg := utils.NewMsgBlockBuilder("Grep")
	msg.Writef(" /%s/ in %s", params.Pattern, path)
	msg.WriteLn()

	if err != nil {
		msg.Writef("Error: %v", err)
	} else if result == "No matches found" {
		msg.WriteString("No matches found")
	} else {
		matches := strings.Count(result, "\n") + 1
		if strings.Contains(result, "truncated") {
			matches = 100
		}
		msg.Writef("Found %d matches", matches)
	}

	return msg.String() + "\n"
}
