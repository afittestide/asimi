package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/afittestide/asimi/internal/utils"
	"github.com/yargevad/filepathx"
)

// ReadManyFilesInput is the input for the ReadManyFilesTool.
type ReadManyFilesInput struct {
	Paths []string `json:"paths"`
}

// ReadManyFilesTool is a tool for reading multiple files using glob patterns.
type ReadManyFilesTool struct {
	ProjectRoot string
}

func (t ReadManyFilesTool) Name() string {
	return "read_many_files"
}

func (t ReadManyFilesTool) Description() string {
	return "Reads content from multiple files specified by wildcard paths. The input should be a JSON object with a 'paths' field, which is an array of strings."
}

func (t ReadManyFilesTool) Call(ctx context.Context, input string) (string, error) {
	var params ReadManyFilesInput
	err := json.Unmarshal([]byte(input), &params)
	if err != nil {
		return "", fmt.Errorf("invalid input: %w. The input should be a JSON object with a 'paths' field", err)
	}

	root := resolvedRoot(ctx, t.ProjectRoot)
	var contentBuilder strings.Builder
	var allMatches []string

	for _, pattern := range params.Paths {
		// If ProjectRoot is set and pattern is not absolute, join with ProjectRoot
		if root != "" && !filepath.IsAbs(pattern) {
			pattern = filepath.Join(root, pattern)
		}
		matches, err := filepathx.Glob(pattern)
		if err != nil {
			// Silently ignore glob errors for now
			continue
		}
		allMatches = append(allMatches, matches...)
	}

	// Create a map to track unique matches
	uniqueMatchesMap := make(map[string]bool)
	var uniqueMatches []string
	for _, match := range allMatches {
		if !uniqueMatchesMap[match] {
			uniqueMatchesMap[match] = true
			uniqueMatches = append(uniqueMatches, match)
		}
	}

	for _, path := range uniqueMatches {
		if err := ValidatePathWithinProject(path, root); err != nil {
			// Skip files outside the project directory
			continue
		}

		content, err := os.ReadFile(path)
		if err != nil {
			// If we can't read a file, we can skip it and continue.
			continue
		}
		contentBuilder.WriteString(fmt.Sprintf("---\t%s---\n", path))
		contentBuilder.Write(content)
		contentBuilder.WriteString("\n")
	}

	return contentBuilder.String(), nil
}

func (t ReadManyFilesTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paths": map[string]any{
				"type":        "array",
				"description": "Array of file paths or glob patterns to read",
				"items": map[string]any{
					"type":        "string",
					"description": "A file path or glob pattern",
				},
			},
		},
		"required": []string{"paths"},
	}
}

// Format formats a read_many_files tool call for display
func (t ReadManyFilesTool) Format(input, result string, err error) string {
	var params ReadManyFilesInput
	json.Unmarshal([]byte(input), &params)

	msg := utils.NewMsgBlockBuilder("Read Many Files")
	if len(params.Paths) == 1 {
		msg.Writef("(%v)", params.Paths[0])
	} else if len(params.Paths) > 1 {
		msg.Writef("(%d files)", len(params.Paths))
	}
	msg.WriteLn()

	if err != nil {
		msg.Writef("Error: %v", err)
	} else {
		// Count files by counting "---\t" markers
		fileCount := strings.Count(result, "---\t")
		msg.Writef("Read %d files", fileCount)
	}

	return msg.String() + "\n"
}
