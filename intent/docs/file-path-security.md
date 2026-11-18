# File Path Security - Issue #51

## Overview

This document describes the implementation of file path security restrictions for the `write_file` and `replace_text` tools, addressing GitHub issue #51.

## Problem Statement

Previously, the AI model could potentially write or modify files anywhere on the filesystem, including sensitive system files or files outside the project directory. This posed a security risk as malicious or erroneous prompts could lead to unintended file modifications.

## Solution

We implemented path validation that restricts file write operations to only files within the project's root directory. This prevents:

1. **Path traversal attacks** - Using `..` to access parent directories
2. **Absolute path access** - Writing to arbitrary locations like `/etc/passwd`
3. **Symlink attacks** - Following symlinks that point outside the project
4. **Complex path manipulation** - Combinations of the above techniques

## Implementation Details

### Core Function: `validatePathWithinProject`

Located in `tools.go`, this function:

1. Determines the project root using `GetRepoInfo().ProjectRoot`
2. Converts both the target path and project root to absolute paths
3. Resolves symlinks using `filepath.EvalSymlinks()` to prevent symlink-based attacks
4. Calculates the relative path from project root to target
5. Rejects any path that would escape the project root (starts with `..`)

### Modified Tools

#### WriteFileTool
- **Before**: Could write to any path on the filesystem
- **After**: Validates path before writing, rejects paths outside project root
- **Additional improvement**: Creates parent directories automatically if they don't exist

#### ReplaceTextTool
- **Before**: Could modify any file on the filesystem
- **After**: Validates path before reading/modifying, rejects paths outside project root

### Error Messages

When a path is rejected, users receive a clear error message:
```
access denied: path '../outside.txt' is outside the project root '/path/to/project'
```

## Security Features

### 1. Path Traversal Prevention
```go
// Rejected:
"../../../etc/passwd"
"subdir/../../outside.txt"
```

### 2. Absolute Path Validation
```go
// Rejected:
"/etc/passwd"
"/tmp/malicious.txt"

// Allowed (if within project):
"/full/path/to/project/file.txt"
```

### 3. Symlink Resolution
```go
// If project/symlink -> /outside/directory
// Then: project/symlink/file.txt is rejected
```

### 4. Empty Path Rejection
```go
// Rejected:
""
```

## Testing

Comprehensive test coverage in `tools_path_validation_test.go`:

### Test Cases

1. **TestValidatePathWithinProject**
   - Valid relative paths
   - Valid nested paths
   - Path traversal attempts
   - Absolute paths (both inside and outside project)
   - Empty paths

2. **TestWriteFileToolPathValidation**
   - Writing files within project
   - Writing to subdirectories
   - Rejecting path traversal
   - Rejecting absolute paths outside project
   - Rejecting complex path traversal

3. **TestReplaceTextToolPathValidation**
   - Replacing text in project files
   - Rejecting path traversal
   - Rejecting absolute paths outside project

4. **TestPathValidationWithSymlinks**
   - Preventing writes through symlinks to outside locations

### Running Tests

```bash
# Run all path validation tests
go test -v -run "TestValidatePathWithinProject|TestWriteFileToolPathValidation|TestReplaceTextToolPathValidation|TestPathValidationWithSymlinks"

# Run specific test
go test -v -run TestWriteFileToolPathValidation
```

## Edge Cases Handled

1. **Non-existent parent directories**: The `WriteFileTool` now creates parent directories automatically
2. **Symlinks in path**: Resolved to real paths before validation
3. **Relative vs absolute paths**: Both are converted to absolute and validated
4. **Project root detection**: Falls back to current directory if project root cannot be determined
5. **Worktree support**: Works correctly with git worktrees using `GetRepoInfo()`

## Backward Compatibility

This change is **breaking** for any workflows that relied on writing files outside the project directory. However, this is intentional for security reasons. Users who need to write files outside the project should:

1. Use the `run_shell_command` tool to execute commands that write files
2. Manually copy files after AI operations complete
3. Adjust their project structure to include necessary files

## Future Enhancements

Potential improvements for future consideration:

1. **Configurable allowed paths**: Allow users to whitelist specific directories outside the project
2. **Read-only mode**: Option to disable all write operations
3. **Audit logging**: Log all file write attempts for security monitoring
4. **Dry-run mode**: Preview file changes without actually writing

## Related Files

- `tools.go` - Core implementation
- `tools_path_validation_test.go` - Test suite
- `utils.go` - `GetRepoInfo()` function for project root detection

## References

- GitHub Issue: #51
- Related security best practices: OWASP Path Traversal
