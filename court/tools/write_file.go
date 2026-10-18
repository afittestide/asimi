package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/afittestide/asimi/internal/utils"
)

// WriteFileInput is the input for the WriteFileTool
type WriteFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteFileTool is a tool for writing to files
type WriteFileTool struct {
	ProjectRoot string
}

func (t WriteFileTool) Name() string {
	return "write_file"
}

func (t WriteFileTool) Description() string {
	return "Writes content to a file, creating or overwriting it. The input should be a JSON object with 'path' and 'content' fields. The path must be within the current working directory."
}

func (t WriteFileTool) Call(ctx context.Context, input string) (string, error) {
	// Use RawMessage to detect missing fields
	var rawParams map[string]json.RawMessage
	err := json.Unmarshal([]byte(input), &rawParams)
	if err != nil {
		return "", fmt.Errorf("invalid input: %w. The input should be a JSON object with 'path' and 'content' fields", err)
	}

	// Check required fields are present
	rawPath, hasPath := rawParams["path"]
	rawContent, hasContent := rawParams["content"]

	if !hasPath || !hasContent {
		return "", fmt.Errorf("invalid input: missing required fields. The input should be a JSON object with 'path' and 'content' fields")
	}

	// Unmarshal the actual values
	var params WriteFileInput
	if err := json.Unmarshal(rawPath, &params.Path); err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	if err := json.Unmarshal(rawContent, &params.Content); err != nil {
		return "", fmt.Errorf("invalid content: %w", err)
	}

	// Clean up path - remove surrounding quotes if any (handles LLM adding extra quotes)
	params.Path = strings.Trim(params.Path, `"'`)

	root := resolvedRoot(ctx, t.ProjectRoot)
	// Validate that the path is within the project root
	if err := ValidatePathWithinProject(params.Path, root); err != nil {
		return "", err
	}

	resolvedPath := ResolvePath(params.Path, root)

	// Create parent directory if it doesn't exist
	dir := filepath.Dir(resolvedPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create directory: %w", err)
		}
	}

	err = os.WriteFile(resolvedPath, []byte(params.Content), 0644)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully wrote to %s", params.Path), nil
}

func (t WriteFileTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Target file path",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "File contents to write",
			},
		},
		"required": []string{"path", "content"},
	}
}

// Format formats a write_file tool call for display
func (t WriteFileTool) Format(input, result string, err error) string {
	var params WriteFileInput
	json.Unmarshal([]byte(input), &params)

	msg := utils.NewMsgBlockBuilder("Write File")
	if params.Path != "" {
		msg.Writef(" %s", params.Path)
	}
	msg.WriteLn()

	if err != nil {
		msg.Writef("Error: %v", err)
	} else {
		msg.WriteString("File written successfully")
	}

	return msg.String() + "\n"
}
