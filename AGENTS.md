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

**Edict 1** was reserved for **Court Infrastructure** operations. This
reservation is being drawn down (2026-11-18 ruling): new work should not use
edict 1; system-level invariants use the id-0 Court key (`CourtEdictKey`).

This ensures system operations have proper audit trails and prevents edict_id=0 from breaking precedent tracking.

## Common Commands

Use `just <recipe>` to manage the project:

- bootstrap                      # Install development tools
- build                          # Build the binary
- build-sandbox                  # Build the sandbox container.
- clean                          # Clean build artifacts
- clean-sandbox                  # Clean up the sandbox container
- default                        # List all available recipes
- fmt                            # Format code
- freeze-logs                    # Freeze the log files to `./heaven/logs/asimi[-deamon]-<datetime>.log`
- get-runs                       # Fetch failed CI runs since the last success, with their entire logs, to heaven/runs.
- get-vuln                      # Run vulnerability scanning, save output to ./heaven/vulns/<tool>-<datetime>.out
- hello-harbor *args             # Recipe: build the latest asimi for linux, then hand it to Harbor
- init-podman                    # Init and start the podman machine
- install                        # Install dependencies
- lint                           # Run linting
- measure                        # Measure run_shell_command tool performance
- nvim                           # Start neovim with the asimi plugin loaded (interactive dev session)
- prune-branches                 # Li 礼), so resolve those by hand.
- run                            # Run with debug logging
- serve-book *name               # Serve a guide book with live reload (mdbook serve), e.g. `just serve-book ruler`
- test                           # Run the test suite, tee-ing output to `./heaven/tests/test-<datetime>.out`
- test-atif                      # ASIMI_PROVIDER) — the test does not force a model.
- test-coverage                  # Run tests with coverage
- test-intent                    # Run intent/gherkin BDD scenarios
- test-performance               # Run performance benchmarks and guardrail tests
- test-race *packages="./court/" # Run tests under the race detector, tee-ing output to `./heaven/tests/test-race-<datetime>.out`
