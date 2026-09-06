PROJECT_NAME := `git config --get remote.origin.url | sed -E 's/.*[:\/]([^\/]+)\/([^\/]+)\.git$/\1\/\2/' | tr '[A-Z]' '[a-z]'`

# List all available recipes
default:
    @just --list

# Install dependencies
install:
    go mod download
    go mod vendor

# Build the binary
build:
    go build -tags containers_image_openpgp -o asimi .

# Run with debug logging
run:
    pkill -9 asimi || true
    rm -f asimi.log asimi-daemon.log
    go build -tags containers_image_openpgp .
    ./asimi --debug

# Run all tests (CI mode when CI env var is set)
test:
    #!/usr/bin/env bash
    set -o pipefail
    export GOTOOLCHAIN=auto
    if [ -n "$CI" ]; then
        go test -tags containers_image_openpgp -timeout 5m -v ./...
        just vuln
    else
        go test -tags containers_image_openpgp -timeout 1m ./... | tee test.out
    fi

# Run performance benchmarks and guardrail tests
test-performance:
    go test -tags containers_image_openpgp -run TestSchedulerConcurrencyGuardrail -timeout 30s ./internal/runners/
    go test -tags containers_image_openpgp -bench BenchmarkScheduler -benchmem -run '^$' -timeout 5m ./internal/runners/

# Run tests with coverage
test-coverage:
    go test -tags containers_image_openpgp -v -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html

# Run intent/gherkin BDD scenarios
test-intent:
    go test -mod=mod -run TestIntentGherkin -timeout 30s .

# Run the terminal-bench activation E2E test with a real LLM.
# Requires CI=true, LLM_E2E=1, and a provider API key in the environment
# (ANTHROPIC_API_KEY, OPENROUTER_API_KEY, or OPENAI_API_KEY).
# Uses whatever model/provider the caller has configured (config, ASIMI_MODEL,
# ASIMI_PROVIDER) — the test does not force a model.
test-atif:
    CI=1 go test -tags containers_image_openpgp -run TestActivation_ATIF_E2E -timeout 20m -v .

# Run linting
lint:
    go vet ./...

# Format code
fmt:
    go fmt ./...
    goimports -w .

# Freeze the log files to asimi.<suffix>.log and asimi-daemon.<suffix>.log
freeze-log suffix:
    cp asimi.log asimi.{{suffix}}.log
    cp asimi-daemon.log asimi-daemon.{{suffix}}.log

# Start neovim with the asimi plugin loaded (interactive dev session)
nvim:
    nvim -u nvim/tests/dev_init.lua

# Clean build artifacts
clean:
    rm -f asimi
    rm -f coverage.out coverage.html
    rm -f asimi.log
    rm -rf test_tmp
    rm -rf profiles

# Install development tools
bootstrap:
    go install golang.org/x/tools/cmd/goimports@latest
    go install golang.org/x/vuln/cmd/govulncheck@latest

# Run vulnerability scanning (fails in CI if vulnerabilities found)
vuln:
    #!/usr/bin/env bash
    set -o pipefail
    if ! command -v govulncheck > /dev/null 2>&1; then
        echo "ERROR: govulncheck is not installed. Run 'just bootstrap' first."
        exit 1
    fi
    go build -tags containers_image_openpgp -o /tmp/asimi-vuln .
    govulncheck -mode=binary /tmp/asimi-vuln
    rm -f /tmp/asimi-vuln

# Init and start the podman machine 
init-podman:
    podman machine init --disk-size 30 >/dev/null 2>&1 || true
    podman machine start >/dev/null 2>&1 || true
# Build the sandbox container
build-sandbox: init-podman
    podman build -t localhost/asimi/sandbox/{{PROJECT_NAME}}:latest -f .agents/sandbox/Dockerfile .

# Clean up the sandbox container
clean-sandbox:
    podman rmi localhost/asimi/sandbox/{{PROJECT_NAME}}:latest

# Measure run_shell_command tool performance
measure:
    @echo "=== Measuring run_shell_command Tool Performance ==="
    @echo ""
    @echo "Sending performance test prompt to asimi..."
    @echo ""
    go run -tags containers_image_openpgp . -p 'Performance test: measure the run_shell_command tool overhead by executiing exactly 12 run_shell_command commands in a SINGLE function_calls block (all at once, not sequentially): 1. First command: date +%s%N, 2-11. Ten commands: : (colon command, does nothing), 12. Last command: date +%s%N. After receiving both the timestamps, calculates the per call overhead'
