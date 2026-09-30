package runners

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"time"
)

const (
	// DefaultCommandTimeout bounds host commands when no timeout is configured.
	// It mirrors PodmanRunner's default so both runners share the same
	// timeout discipline.
	DefaultCommandTimeout = 10 * time.Minute
	// DefaultApprovalTimeout bounds how long a host command waits for a
	// user approval decision before failing.
	DefaultApprovalTimeout = 2 * time.Minute
)

// HostRunner runs commands directly on the host system
type HostRunner struct {
	projectRoot     string
	msgChan         chan<- Msg
	timeout         time.Duration // per-command deadline (0 → DefaultCommandTimeout)
	approvalTimeout time.Duration // approval wait deadline (0 → DefaultApprovalTimeout)
}

// NewHostRunner creates a new HostRunner
func NewHostRunner(connID uint64, projectRoot string) *HostRunner {
	return &HostRunner{
		projectRoot:     projectRoot,
		timeout:         DefaultCommandTimeout,
		approvalTimeout: DefaultApprovalTimeout,
	}
}

// SetTimeouts configures the command and approval-wait deadlines. Zero or
// negative values fall back to the respective defaults.
func (r *HostRunner) SetTimeouts(timeouts HostTimeouts) {
	if timeouts.Command <= 0 {
		timeouts.Command = DefaultCommandTimeout
	}
	if timeouts.Approval <= 0 {
		timeouts.Approval = DefaultApprovalTimeout
	}
	r.timeout = timeouts.Command
	r.approvalTimeout = timeouts.Approval
}

func (r *HostRunner) SetMessageChannel(msgChan chan<- Msg) {
	r.msgChan = msgChan
}

func (r *HostRunner) Run(ctx context.Context, input Input) (Output, error) {
	var output Output

	// Check if approval is required
	if !input.BypassApproval {
		approved, err := r.requestApproval(ctx, input.Command)
		if err != nil {
			output.Output = fmt.Sprintf("Error requesting approval: %v", err)
			output.ExitCode = "1"
			return output, err
		}

		if !approved {
			output.Output = "Command execution denied by user"
			output.ExitCode = "1"
			return output, CommandDeniedError{Command: input.Command}
		}
	}

	// Bound the command with a deadline so a never-exiting streaming
	// command cannot hang the caller forever (mirrors PodmanRunner).
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd.exe", "/c", input.Command)
	} else {
		cmd = exec.CommandContext(runCtx, "bash", "-c", input.Command)
	}
	// Children must never emit interactive credential prompts; see
	// NonInteractiveGitEnv for why.
	cmd.Env = append(os.Environ(), NonInteractiveGitEnv()...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if r.projectRoot != "" {
		cmd.Dir = r.projectRoot
	}

	runErr := cmd.Run()
	output.Output = stdout.String() + "\n" + stderr.String()

	if runErr != nil {
		// Deadline exceeded is a *result*, not an error — the LLM sees the
		// timeout message and can adapt (exit code 124, like timeout(1)).
		if runCtx.Err() == context.DeadlineExceeded {
			slog.Warn("host command timed out",
				"command", input.Command, "timeout", r.timeout)
			output.Output = fmt.Sprintf("Command timed out after %v", r.timeout)
			output.ExitCode = "124"
			return output, nil
		}
		if ctx.Err() != nil {
			output.ExitCode = "-1"
			return output, ctx.Err()
		}
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			output.ExitCode = fmt.Sprintf("%d", exitErr.ExitCode())
		} else {
			output.ExitCode = "-1"
		}
	} else {
		if cmd.ProcessState != nil {
			output.ExitCode = fmt.Sprintf("%d", cmd.ProcessState.ExitCode())
		} else {
			output.ExitCode = "0"
		}
	}

	return output, nil
}

func (r *HostRunner) Restart(ctx context.Context) error {
	// No-op for host shell runner
	return nil
}

func (r *HostRunner) Close(ctx context.Context) error {
	// No-op for host shell runner
	return nil
}

func (r *HostRunner) RunnerType() string {
	return "host"
}

func (r *HostRunner) GetOS() string {
	return runtime.GOOS
}

func (r *HostRunner) AllowFallback(allow bool) {
}

// HealthCheck returns nil — the host is always healthy.
func (r *HostRunner) HealthCheck(ctx context.Context) error {
	return nil
}

// requestApproval sends an approval request via the message channel and waits for response
func (r *HostRunner) requestApproval(ctx context.Context, command string) (bool, error) {
	if r.msgChan == nil {
		// No message channel configured - deny by default for safety
		slog.Warn("Host command approval requested but no message channel configured", "command", command)
		return false, fmt.Errorf("no approval mechanism configured")
	}

	responseChan := make(chan bool, 1)
	request := ApprovalRequestMsg{
		Command:      command,
		ResponseChan: responseChan,
	}

	// Send the approval request
	select {
	case r.msgChan <- request:
		// Request sent successfully
	case <-ctx.Done():
		return false, ctx.Err()
	}

	// Wait for the response, bounded by the approval timeout so a lost or
	// unanswered prompt fails loudly instead of hanging the turn forever.
	// responseChan is buffered (capacity 1), so a late human approval after
	// timeout is safely dropped.
	approvalCtx, cancel := context.WithTimeout(ctx, r.approvalTimeout)
	defer cancel()
	select {
	case approved := <-responseChan:
		return approved, nil
	case <-approvalCtx.Done():
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		slog.Warn("host command approval timed out",
			"command", command, "timeout", r.approvalTimeout)
		return false, fmt.Errorf("approval timed out after %v for command: %s", r.approvalTimeout, command)
	}
}
