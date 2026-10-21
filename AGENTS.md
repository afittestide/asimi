## Language & Philosophy

**Language**: Go

**Write idiomatic Go** - Keep it simple, flat, and direct.

- **Model friendly file structure**: Keep file names specific and module names meaningfull
- **No wrappers**: Avoid unnecessary abstractions
- **No build tags**: Keep builds simple
- **Short, meaningful names**: Follow Go conventions
- **Inline comments**: Only for non-trivial code
- **Commit message **: start with "fix:", "feat:" or "chore:", follow by short description and end with "e123" for edict 123
- **Few testfiles**: don't start a new test file if the there's an exisiting file that can host your tests

## Reserved Edict IDs

**Edict 1** is reserved for **Court Infrastructure** operations:
- System-level operations (init, bootstrap, project setup)
- Infrastructure decisions and precedents

This ensures system operations have proper audit trails and prevents edict_id=0 from breaking precedent tracking.

## Common Commands

```bash
# Development
just run              # Run with debug logging (writes to ./asimi.log)
just build            # Build binary
just test             # Run all tests
just test-coverage    # Run tests with coverage report
just lint             # Run go vet
just fmt              # Format code

# Infrastructure
just bootstrap        # Install dev tools (goimports, govulncheck)
just build-sandbox    # Build container image for sandboxed execution
just clean-sandbox    # Clean up container image

# Performance analysis
just measure          # Profile startup performance

# Run single test
go test -v -run TestName ./...
```

## Architecture Overview

Asimi is a vi-inspired, terminal-based AI coding agent with containerized shell execution.
