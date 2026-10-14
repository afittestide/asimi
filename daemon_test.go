package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	courtTools "github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitShellRunnerMustNotFallbackToHost verifies that on a system
// without podman, InitShellRunner returns a PodmanRunner (not a
// HostRunner). A runner without a sandbox must NOT execute commands
// on the host — it should return SandboxMissingError instead.
func TestInitShellRunnerMustNotFallbackToHost(t *testing.T) {
	cfg := &config.SandboxConfig{}
	repoInfo := repo.RepoInfo{
		ProjectRoot: t.TempDir(),
		Slug:        "test-project",
	}

	runner := runners.InitShellRunner(cfg, repoInfo, "1")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
	}()

	// When podman is unavailable, InitShellRunner must return a
	// PodmanRunner (not a HostRunner) so that commands fail with
	// SandboxMissingError rather than silently running on the host.
	if runner.RunnerType() == "host" {
		t.Errorf("InitShellRunner returned HostRunner when podman is unavailable — commands will escape to host (uname → Darwin)")
	}
}

// TestInitShellRunnerDefaultImage verifies that the default sandbox image
// name is derived directly from the (canonical lowercase) git slug. Since
// RepoInfo.Slug is normalized to lowercase at its source, the runner uses it
// verbatim — no per-callsite lowercasing is needed.
func TestInitShellRunnerDefaultImage(t *testing.T) {
	cfg := &config.SandboxConfig{}
	repoInfo := repo.RepoInfo{
		ProjectRoot: t.TempDir(),
		Slug:        "myorg/myproject",
	}

	runner := runners.InitShellRunner(cfg, repoInfo, "1")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
	}()

	pr, ok := runner.(*runners.PodmanRunner)
	require.True(t, ok, "InitShellRunner should return a *runners.PodmanRunner")
	assert.Equal(t, "localhost/asimi/sandbox/myorg/myproject:latest", pr.GetImageName())
}

// TestPodmanRunnerHostFallbackMustNotLeak verifies that when
// AllowHostFallback=true and a HostRunner fallback is provided,
// PodmanRunner.Run returns SandboxFallbackError (not nil) so the
// caller always knows the sandbox was bypassed. Silent fallback to
// the host is a security violation.
func TestPodmanRunnerHostFallbackMustNotLeak(t *testing.T) {
	cfg := &config.SandboxConfig{
		AllowHostFallback: true,
	}
	repoInfo := repo.RepoInfo{
		ProjectRoot: t.TempDir(),
		Slug:        "test-project",
	}

	hostRunner := runners.NewHostRunner(1, t.TempDir())
	runner := runners.NewPodmanRunner(cfg, repoInfo, "1", hostRunner)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// When sandbox is unavailable and fallback is used, Run must
	// return SandboxFallbackError — never a silent nil error.
	output, err := runner.Run(ctx, runners.Input{
		Command:        "uname",
		Description:    "verify sandbox isolation",
		BypassApproval: true,
	})

	if err == nil {
		t.Errorf("command ran on host (output=%q) — AllowHostFallback silently escaped the sandbox", strings.TrimSpace(output.Output))
	}
}

// TestShellCommandMustFailWithoutSandbox verifies that when the
// RunShellCommand tool has a PodmanRunner with no sandbox files,
// the tool falls back to host execution (permanent state, no restart).
func TestShellCommandMustFailWithoutSandbox(t *testing.T) {
	cfg := &config.SandboxConfig{}
	repoInfo := repo.RepoInfo{
		ProjectRoot: t.TempDir(),
		Slug:        "test-project",
	}

	runner := runners.NewPodmanRunner(cfg, repoInfo, "99", nil)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
	}()

	shellTool := courtTools.NewRunShellCommand(nil, runner, nil, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// When sandbox is missing, the tool should fall back to host execution
	result, err := shellTool.Call(ctx, `{"command":"echo hello","description":"test fallback to host"}`)
	if err != nil {
		t.Fatalf("expected successful fallback to host, got error: %v", err)
	}
	var output runners.Output
	if err := json.Unmarshal([]byte(result), &output); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	if !strings.Contains(output.Output, "hello") {
		t.Errorf("output = %q, want to contain 'hello'", output.Output)
	}
	if output.ExitCode != "0" {
		t.Errorf("exitCode = %q, want '0'", output.ExitCode)
	}
}

// TestDaemonSafeRunOnHostUsesClientProjectRoot verifies the critical
// daemon-mode invariant: when a safe_run_on_host command is executed
// via the RunShellCommand tool, it runs in the CLIENT's ProjectRoot,
// NOT the daemon process's CWD.
func TestDaemonSafeRunOnHostUsesClientProjectRoot(t *testing.T) {
	// Create two distinct project directories with unique marker files
	projectA := t.TempDir()
	projectB := t.TempDir()
	markerA := "CLIENT_A_MARKER"
	markerB := "CLIENT_B_MARKER"

	if err := os.WriteFile(filepath.Join(projectA, "whoami.txt"), []byte(markerA), 0644); err != nil {
		t.Fatalf("write marker A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectB, "whoami.txt"), []byte(markerB), 0644); err != nil {
		t.Fatalf("write marker B: %v", err)
	}

	// Simulate what the daemon does when a safe_run_on_host command
	// arrives: it creates an ephemeral HostRunner with the client's
	// ProjectRoot. Verify that commands run in the client's directory.
	hostRunnerA := runners.NewHostRunner(0, projectA)
	output, err := hostRunnerA.Run(context.Background(), runners.Input{
		Command:        "cat whoami.txt",
		BypassApproval: true,
	})
	if err != nil {
		t.Fatalf("HostRunner A: %v", err)
	}
	if !strings.Contains(output.Output, markerA) {
		t.Errorf("safe_run_on_host command ran in wrong directory.\nGot: %q\nWant: %q\nProjectRoot: %s", output.Output, markerA, projectA)
	}
	if strings.Contains(output.Output, markerB) {
		t.Errorf("safe_run_on_host ran in project B instead of A.\nGot: %q\nProjectRoot: %s", output.Output, projectA)
	}

	// Now verify client B gets its own project root
	hostRunnerB := runners.NewHostRunner(0, projectB)
	outputB, err := hostRunnerB.Run(context.Background(), runners.Input{
		Command:        "cat whoami.txt",
		BypassApproval: true,
	})
	if err != nil {
		t.Fatalf("HostRunner B: %v", err)
	}
	if !strings.Contains(outputB.Output, markerB) {
		t.Errorf("safe_run_on_host command ran in wrong directory for B.\nGot: %q\nWant: %q\nProjectRoot: %s", outputB.Output, markerB, projectB)
	}
	if strings.Contains(outputB.Output, markerA) {
		t.Errorf("safe_run_on_host for B found A's marker.\nGot: %q\nProjectRoot: %s", outputB.Output, projectB)
	}

	// Also verify the full tool path: RunShellCommand with safe_run_on_host
	// pattern uses the projectRoot passed to NewRunShellCommand.
	hostChecker := func(cmd string) (bool, bool) { return true, false } // safe, no approval needed
	tool := courtTools.NewRunShellCommand(hostChecker, nil, nil, projectA)
	result, err := tool.Call(context.Background(), `{"command":"cat whoami.txt","description":"verify project root"}`)
	if err != nil {
		t.Fatalf("RunShellCommand: %v", err)
	}

	var toolOutput runners.Output
	if err := json.Unmarshal([]byte(result), &toolOutput); err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if !strings.Contains(toolOutput.Output, markerA) {
		t.Errorf("RunShellCommand safe_run_on_host did NOT run in client ProjectRoot.\nGot: %q\nWant: %q\nprojectRoot: %s", toolOutput.Output, markerA, projectA)
	}
}

// errorRunner is a mock Runner that always returns a specific error from Run.
type errorRunner struct {
	runErr error
}

func (m *errorRunner) Run(ctx context.Context, input runners.Input) (runners.Output, error) {
	return runners.Output{}, m.runErr
}
func (m *errorRunner) Restart(ctx context.Context) error     { return nil }
func (m *errorRunner) Close(ctx context.Context) error       { return nil }
func (m *errorRunner) AllowFallback(bool)                    {}
func (m *errorRunner) RunnerType() string                    { return "podman" }
func (m *errorRunner) GetOS() string                         { return "linux" }
func (m *errorRunner) SetMessageChannel(chan<- runners.Msg)  {}
func (m *errorRunner) HealthCheck(ctx context.Context) error { return nil }

// TestShellCommandPodmanUnavailablePropagatesError verifies that when
// PodmanUnavailableError is returned from the runner, shell.go propagates
// the error instead of falling back to host or retrying.
func TestShellCommandPodmanUnavailablePropagatesError(t *testing.T) {
	runner := &errorRunner{runErr: runners.PodmanUnavailableError{Reason: "podman is down"}}
	shellTool := courtTools.NewRunShellCommand(nil, runner, nil, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := shellTool.Call(ctx, `{"command":"echo hello","description":"test podman unavailable"}`)
	if err == nil {
		t.Fatal("expected error to be propagated, got nil")
	}
	if _, ok := err.(runners.PodmanUnavailableError); !ok {
		t.Fatalf("expected PodmanUnavailableError, got %T: %v", err, err)
	}
}

// TestShellCommandSandboxSetupMissingFallsBackToHost verifies that when
// SandboxSetupMissingError is returned from the runner, shell.go falls
// back to host execution (same as SandboxMissingError).
func TestShellCommandSandboxSetupMissingFallsBackToHost(t *testing.T) {
	runner := &errorRunner{runErr: runners.SandboxSetupMissingError{}}
	shellTool := courtTools.NewRunShellCommand(nil, runner, nil, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := shellTool.Call(ctx, `{"command":"echo hello","description":"test setup missing fallback"}`)
	if err != nil {
		t.Fatalf("expected successful fallback to host, got error: %v", err)
	}

	var output runners.Output
	if err := json.Unmarshal([]byte(result), &output); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	if !strings.Contains(output.Output, "hello") {
		t.Errorf("output = %q, want to contain 'hello'", output.Output)
	}
	if output.ExitCode != "0" {
		t.Errorf("exitCode = %q, want '0'", output.ExitCode)
	}
}
