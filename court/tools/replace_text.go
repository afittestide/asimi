package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/afittestide/asimi/internal/utils"
)

// ReplaceTextInput is the input for the ReplaceTextTool
type ReplaceTextInput struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// ReplaceTextTool is a tool for replacing text in a file
// TODO: add a glob pattern fieldto limit the tools reach. i,e. "*.md"
type ReplaceTextTool struct {
	ProjectRoot string
}

func (t ReplaceTextTool) Name() string {
	return "replace_text"
}

func (t ReplaceTextTool) Description() string {
	return "Replaces all occurrences of a string in a file with another string. The input should be a JSON object with 'path', 'old_text', and 'new_text' fields. The path must be within the current working directory."
}

func (t ReplaceTextTool) Call(ctx context.Context, input string) (string, error) {
	var params ReplaceTextInput
	err := json.Unmarshal([]byte(input), &params)
	if err != nil {
		return "", fmt.Errorf("invalid input: %w. The input should be a JSON object with 'path', 'old_text', and 'new_text' fields", err)
	}

	root := resolvedRoot(ctx, t.ProjectRoot)
	// Validate that the path is within the project root
	if err := ValidatePathWithinProject(params.Path, root); err != nil {
		return "", err
	}

	resolvedPath := ResolvePath(params.Path, root)

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", err
	}

	oldContent := string(content)

	// Check if old_string and new_string are identical
	if params.OldText == params.NewText {
		return fmt.Sprintf("No changes to apply. The old_string and new_string are identical in file: %s", params.Path), nil
	}

	newContent := strings.ReplaceAll(oldContent, params.OldText, params.NewText)

	// Count how many replacements were made
	occurrences := strings.Count(oldContent, params.OldText)

	if occurrences == 0 {
		return fmt.Sprintf("No occurrences of '%s' found in %s", params.OldText, params.Path), nil
	}

	err = os.WriteFile(resolvedPath, []byte(newContent), 0644)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Successfully modified file: %s (%d replacements)", params.Path, occurrences), nil
}

func (t ReplaceTextTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path",
			},
			"old_text": map[string]any{
				"type":        "string",
				"description": "Text to replace",
			},
			"new_text": map[string]any{
				"type":        "string",
				"description": "Replacement text",
			},
		},
		"required": []string{"path", "old_text", "new_text"},
	}
}

// Format formats a replace_text tool call for display
func (t ReplaceTextTool) Format(input, result string, err error) string {
	var params ReplaceTextInput
	json.Unmarshal([]byte(input), &params)

	msg := utils.NewMsgBlockBuilder("Replace Text")
	if params.Path != "" {
		msg.Writef(" %s", params.Path)
	}
	msg.WriteLn()

	if err != nil {
		msg.Writef("Error: %v", err)
	} else if strings.Contains(result, "No occurrences") {
		msg.WriteString("No matches found")
	} else if strings.Contains(result, "No changes") {
		msg.WriteString("No changes needed")
	} else {
		msg.WriteString("Text replaced successfully")
	}

	return msg.String()
}
