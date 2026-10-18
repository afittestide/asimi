package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/afittestide/asimi/internal/utils"
)

// ReadFileInput is the input for the ReadFileTool
type ReadFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// readFileInputRaw is used to handle string values for numeric fields (workaround for Claude Code CLI)
type readFileInputRaw struct {
	Path   string `json:"path"`
	Offset any    `json:"offset,omitempty"`
	Limit  any    `json:"limit,omitempty"`
}

// ReadFileTool is a tool for reading files
type ReadFileTool struct {
	ProjectRoot string
}

// NewReadFileTool creates a new ReadFileTool
func NewReadFileTool(projectRoot string) *ReadFileTool {
	return &ReadFileTool{ProjectRoot: projectRoot}
}

func (t ReadFileTool) Name() string {
	return "read_file"
}

func (t ReadFileTool) Description() string {
	return "Reads a file and returns its content. The input should be a JSON object with a 'path' field. Optionally specify 'offset' (line number to start from, 1-based) and 'limit' (number of lines to read)."
}

func (t ReadFileTool) Call(ctx context.Context, input string) (string, error) {
	var params ReadFileInput
	err := json.Unmarshal([]byte(input), &params)
	if err != nil {
		// If unmarshalling fails, assume the input is a raw path
		params.Path = input
	}

	// Workaround for Claude Code CLI bug: numeric params come as strings
	// If offset/limit are zero but the input contains them, try flexible parsing
	if (params.Offset == 0 && strings.Contains(input, `"offset"`)) ||
		(params.Limit == 0 && strings.Contains(input, `"limit"`)) {
		var rawParams readFileInputRaw
		if json.Unmarshal([]byte(input), &rawParams) == nil {
			params.Path = rawParams.Path
			if s, ok := rawParams.Offset.(string); ok && s != "" {
				fmt.Sscanf(s, "%d", &params.Offset)
			}
			if s, ok := rawParams.Limit.(string); ok && s != "" {
				fmt.Sscanf(s, "%d", &params.Limit)
			}
		}
	}

	// Clean up the path to remove any surrounding quotes
	params.Path = strings.Trim(params.Path, `"'`)

	root := resolvedRoot(ctx, t.ProjectRoot)
	if err := ValidatePathWithinProject(params.Path, root); err != nil {
		return "", err
	}

	resolvedPath := ResolvePath(params.Path, root)

	// Read file content
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", err
	}

	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")
	totalLines := len(lines)

	// Handle offset (1-based, convert to 0-based)
	startLine := 0
	if params.Offset > 0 {
		startLine = params.Offset - 1
		if startLine >= totalLines {
			return "", nil // Offset beyond file end
		}
	}

	// Handle limit
	endLine := totalLines
	if params.Limit > 0 {
		endLine = startLine + params.Limit
		if endLine > totalLines {
			endLine = totalLines
		}
	}

	selectedLines := lines[startLine:endLine]
	return strings.Join(selectedLines, "\n"), nil
}

func (t ReadFileTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the file",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Line number to start reading from (1-based)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Number of lines to read",
			},
		},
		"required": []string{"path"},
	}
}

// Format formats a read_file tool call for display
func (t ReadFileTool) Format(input, result string, err error) string {
	var params ReadFileInput
	json.Unmarshal([]byte(input), &params)

	msg := utils.NewMsgBlockBuilder("Read File")
	if params.Path != "" {
		msg.Writef(" %s", params.Path)
	}
	msg.WriteLn()

	if err != nil {
		msg.Writef("Error: %v", err)
	} else {
		lines := strings.Count(result, "\n") + 1
		if result == "" {
			lines = 0
		}
		msg.Writef("Read %d lines", lines)
	}

	return msg.String() + "\n"
}
