# Export Command Specification

## Overview
The `/export` command serializes the current conversation context to a text file and opens it in the user's `$EDITOR`.

## User Story
As a user, I want to export my current conversation to a text file so that I can:
- Review the conversation history in my preferred editor
- Save important conversations for later reference
- Share conversation transcripts with others
- Edit and annotate conversations

## Command Syntax
```
/export
```

## Behavior

### 1. Context Serialization
The command should serialize the following information:
- Session metadata (ID, created time, model, provider)
- System prompt (first message)
- All conversation messages in chronological order:
  - User messages
  - Assistant messages
  - Tool calls and their results
- Context files (AGENTS.md and any dynamically added files)

### 2. Export Format
The exported file should be in Markdown format with the following structure:

```markdown
# Asimi Conversation Export

**Session ID:** {session_id}
**Created:** {created_at}
**Last Updated:** {last_updated}
**Model:** {provider}/{model}
**Working Directory:** {working_dir}

---

## System Prompt

{system_prompt_content}

---

## Context Files

### {filename1}
```
{file_content}
```

### {filename2}
```
{file_content}
```

---

## Conversation

### User ({timestamp})
{user_message}

### Assistant ({timestamp})
{assistant_message}

### Tool Call: {tool_name} ({timestamp})
**Input:**
```json
{tool_input}
```

**Output:**
```
{tool_output}
```

...
```

### 3. File Handling
- Create a temporary file with a meaningful name: `asimi-export-{session_id}.md`
- Place the file in a temporary directory (use `os.TempDir()`)
- Open the file in `$EDITOR` (fallback to `vi` if not set)
- Wait for the editor to close
- Optionally prompt user to save the file permanently

### 4. Editor Integration
- Use `$EDITOR` environment variable
- Fallback to `vi` if `$EDITOR` is not set
- Execute the editor as a subprocess and wait for it to complete
- Handle editor errors gracefully

### 5. Error Handling
- No active session: Show error message "No active session to export"
- Failed to create temp file: Show error with details
- Failed to launch editor: Show error with details
- Editor exits with error: Show warning but don't fail

## Implementation Notes

### File Location
- Use `os.TempDir()` for temporary storage
- Filename format: `asimi-export-{session_id}-{timestamp}.md`

### Editor Execution
```go
editor := os.Getenv("EDITOR")
if editor == "" {
    editor = "vi"
}
cmd := exec.Command(editor, filepath)
cmd.Stdin = os.Stdin
cmd.Stdout = os.Stdout
cmd.Stderr = os.Stderr
err := cmd.Run()
```

### Message Formatting
- Extract text content from `llms.MessageContent` parts
- Format tool calls with JSON-formatted input
- Include timestamps for each message (if available)
- Handle different message types (text, tool calls, tool responses)

## Testing Considerations
- Test with no active session
- Test with empty session (only system prompt)
- Test with conversation history
- Test with context files
- Test with tool calls
- Test with missing `$EDITOR`
- Test with invalid `$EDITOR`

## Future Enhancements
- Allow user to specify export location
- Support different export formats (JSON, plain text, HTML)
- Option to export only selected messages
- Auto-save exports to a configured directory
- Export history browsing
