package runners

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
)

var (
	globalRunner Runner
	runnerMu     sync.RWMutex
)

// SetRunner sets the global runner (thread-safe)
func SetRunner(r Runner) {
	runnerMu.Lock()
	globalRunner = r
	runnerMu.Unlock()
}

// GetRunner returns the global runner (thread-safe)
func GetRunner() Runner {
	runnerMu.RLock()
	r := globalRunner
	runnerMu.RUnlock()
	return r
}

// DefaultMaxOutputSize is the fallback when no config is provided.
const DefaultMaxOutputSize = 51200 // 50KB

// Input is the input for shell command execution
type Input struct {
	Command        string `json:"command"`
	Description    string `json:"description"`
	BypassApproval bool   `json:"-"` // Internal field, not included in JSON
}

// Output is the output of shell command execution
type Output struct {
	Output   string `json:"stdout"`
	ExitCode string `json:"exitCode"`
}

// Runner is the interface for shell command execution backends
type Runner interface {
	Run(ctx context.Context, input Input) (Output, error)
	Restart(ctx context.Context) error
	Close(ctx context.Context) error
	AllowFallback(bool)
	RunnerType() string // Returns "podman" or "host"
	GetOS() string      // Returns the OS where commands execute ("linux" for sandbox, runtime.GOOS for host)
	SetMessageChannel(msgChan chan<- Msg)
	HealthCheck(ctx context.Context) error
}

// Type aliases - use types from internal/config as the single source of truth
type (
	Config = config.SandboxConfig
	Mount  = config.Mount
)

// Messages sent by runners (bubbletea pattern)

// Msg is the interface for runner messages
type Msg interface{}

// ContainerLaunchedMsg is sent when a container is launched
type ContainerLaunchedMsg struct {
	Message     string `msgpack:"message,omitempty"`
	ContainerID string `msgpack:"container_id,omitempty"`
}

// ApprovalRequestMsg is sent when a command needs user approval.
// ResponseChan is an in-process field only; over the RPC wire this
// message becomes a daemon→TUI request that expects a reply.
type ApprovalRequestMsg struct {
	Command      string    `msgpack:"command"`
	ResponseChan chan bool `msgpack:"-"`
}

// ClearSchedulerMsg is sent when the runner needs to clear the scheduler queue.
// Always in-process only.
type ClearSchedulerMsg struct {
	ResultChan chan int `msgpack:"-"`
}

// SandboxUnhealthyMsg is sent when a stale container is detected, killed, and recreated.
type SandboxUnhealthyMsg struct {
	Message       string `msgpack:"message,omitempty"`
	ContainerName string `msgpack:"container_name,omitempty"`
}

// CommandDeniedError is returned when a user denies a host command approval request
type CommandDeniedError struct {
	Command string
}

func (e CommandDeniedError) Error() string {
	return fmt.Sprintf("command denied by user: `%s`", e.Command)
}

// PodmanUnavailableError is returned when podman is not available
type PodmanUnavailableError struct {
	Reason string
}

func (e PodmanUnavailableError) Error() string {
	return e.Reason
}

// SandboxMissingError is returned when the sandbox image is missing.
// It is contextual: if .agents/ exists under ProjectRoot the user has
// already run :init and the issue is a missing image build.
type SandboxMissingError struct {
	ImageName   string
	ProjectRoot string
}

func (e SandboxMissingError) Error() string {
	if _, err := os.Stat(filepath.Join(e.ProjectRoot, ".agents")); err == nil {
		return fmt.Sprintf("Sandbox container image '%s' is missing.\nDid you run `just build-sandbox` ?", e.ImageName)
	}
	return "Sandbox container image is missing.\nDid you run `:init` ?"
}

// SandboxSetupMissingError is returned when project sandbox files are missing.
type SandboxSetupMissingError struct{}

func (e SandboxSetupMissingError) Error() string {
	return "Sandbox files are missing. Did you run `:init`?"
}

// SandboxFallbackError is returned when a command fell back to the
// host because the sandbox was unavailable. The caller receives the
// host output but must know the sandbox was bypassed.
type SandboxFallbackError struct {
	Err         error // original sandbox error
	FallbackErr error // error from the host fallback (may be nil)
}

func (e SandboxFallbackError) Error() string {
	return fmt.Sprintf("sandbox unavailable, command ran on host: %v", e.Err)
}

func (e SandboxFallbackError) Unwrap() error { return e.Err }

// HostTimeouts carries the host-runner deadline configuration. It is
// passed through the tool layer so ephemeral HostRunner instances share
// the same timeout discipline as PodmanRunner. Zero values mean "use
// the runner defaults".
type HostTimeouts struct {
	// Command bounds each host command execution (run_shell_command.timeout_minutes).
	Command time.Duration
	// Approval bounds the wait for a user approval decision
	// (sandbox.approval_timeout).
	Approval time.Duration
}

// ResolveHostTimeouts maps the configured command timeout (in minutes)
// and approval timeout onto HostTimeouts. It performs no fallback — zero
// values pass through, and HostRunner.SetTimeouts applies the runner
// defaults when it consumes them.
func ResolveHostTimeouts(commandTimeoutMinutes int, approvalTimeout time.Duration) HostTimeouts {
	return HostTimeouts{
		Command:  time.Duration(commandTimeoutMinutes) * time.Minute,
		Approval: approvalTimeout,
	}
}

func InitShellRunner(config *Config, repoInfo repo.RepoInfo) Runner {
	// Resolve image name using same default as NewPodmanRunner
	imageName := config.ImageName
	if imageName == "" {
		imageName = fmt.Sprintf("localhost/asimi/sandbox/%s:latest", repoInfo.Slug)
	}

	// Auto-detect and assign shell runner
	if IsPodmanAvailable(imageName) {
		slog.Info("using podman shell runner", "image", imageName)
		var fallback Runner
		if config.AllowHostFallback {
			host := NewHostRunner(uint64(os.Getpid()), repoInfo.ProjectRoot)
			host.SetTimeouts(HostTimeouts{
				Command:  time.Duration(config.TimeoutMinutes) * time.Minute,
				Approval: config.ApprovalTimeout,
			})
			fallback = host
		}
		runner := NewPodmanRunner(config, repoInfo, uint64(os.Getpid()), fallback)
		SetRunner(runner)
		return runner
	}

	// Podman is not available or image is missing. Return a PodmanRunner
	// with no fallback so that Run() returns SandboxMissingError rather
	// than silently executing on the host.
	slog.Warn("podman not available or image missing; commands will fail until sandbox is set up", "image", imageName)
	runner := NewPodmanRunner(config, repoInfo, uint64(os.Getpid()), nil)
	SetRunner(runner)
	return runner
}
func HostRun(ctx context.Context, in Input, projectRoot string) (Output, error) {
	runner := NewHostRunner(0, projectRoot)
	out, err := runner.Run(ctx, in)
	slog.Debug("Run a host command", "cmd", in.Command, "err", err, "out", out)
	return out, err
}
