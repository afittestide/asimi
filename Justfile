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


# Recipe: build the latest asimi for linux, then hand it to Harbor
# (cross-compiles a static linux/amd64 binary, then runs a hello-world trial)
hello-harbor *args:
    #!/usr/bin/env bash
    set -euo pipefail
    CGO_ENABLED=0 GOOS=linux go build -tags containers_image_openpgp -o asimi_linux .
    ROOT="$(pwd)"
    BIN="${ROOT}/asimi_linux"
    echo "==> linux binary: ${BIN}"
    file "${BIN}"
    echo "==> running harbor hello-world trial against ${BIN}"
    cd "${HARBOR_DIR:-../harbor}"
    echo "==> harbor: $(uv run which harbor) ($(uv run harbor --version))"
    uv run harbor run \
        -p examples/tasks/hello-world \
        -a asimi \
        -o "${ROOT}/jobs" \
        --model "openai/deepseek-v4.1-flash" \
        --ak "local_binary=${BIN}" \
        --ak "OPENAI_API_KEY=${OPENAI_API_KEY}" \
        --ak "OPENAI_BASE_URL=${OPENAI_BASE_URL}" \
        {{args}} 2>&1
    echo "<== results written to ${ROOT}/jobs"

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

# Run vulnerability scanning (fails on any finding not in security/vuln-allowlist.txt)
vuln:
    #!/usr/bin/env bash
    set -o pipefail
    if ! command -v govulncheck > /dev/null 2>&1; then
        echo "ERROR: govulncheck is not installed. Run 'just bootstrap' first."
        exit 1
    fi
    allowlist="security/vuln-allowlist.txt"
    go build -tags containers_image_openpgp -o /tmp/asimi-vuln .
    out=$(govulncheck -mode=binary /tmp/asimi-vuln 2>&1)
    status=$?
    rm -f /tmp/asimi-vuln
    if [ "$status" -eq 0 ]; then
        echo "$out"
        exit 0
    fi
    # Collect accepted IDs (one per line, '#' comments ignored).
    accepted=""
    if [ -f "$allowlist" ]; then
        accepted=$(grep -v '^[[:space:]]*#' "$allowlist" | grep -v '^[[:space:]]*$' || true)
    fi
    # Find the vulnerability IDs the scan actually reported.
    found=$(echo "$out" | grep -oE 'GO-[0-9]{4}-[0-9]+' | sort -u)
    unaccepted=""
    for id in $found; do
        if ! echo "$accepted" | grep -qx "$id"; then
            unaccepted="$unaccepted $id"
        fi
    done
    # A stale allowlist entry (no longer reported) is also a failure: it means
    # the acceptance should be re-reviewed. But it does not block the release.
    stale=""
    for id in $accepted; do
        if ! echo "$found" | grep -qx "$id"; then
            stale="$stale $id"
        fi
    done
    echo "$out"
    echo
    if [ -n "$unaccepted" ]; then
        echo "VULN GATE FAILED: unaccepted vulnerabilities:$unaccepted"
        echo "Fix them, or add a justified entry to $allowlist (see docs/security.md)."
        exit 1
    fi
    if [ -n "$stale" ]; then
        echo "NOTE: allowlist entries no longer reported (re-review and remove):$stale"
    fi
    echo "VULN GATE PASSED: all reported findings are accepted in $allowlist."

# Init and start the podman machine 
init-podman:
    podman machine init --disk-size 30 --memory 4096 >/dev/null 2>&1 || true
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
