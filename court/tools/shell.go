package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/utils"
)

// RunShellCommand is a tool for running shell commands in a persistent shell.
// It uses the runners package for actual execution.
type RunShellCommand struct {
	shouldRunOnHost func(cmd string) (runOnHost, needsApproval bool, err error)
	runner          runners.Runner
	msgChan         *chan<- runners.Msg // pointer to Court.msgChan — single source of truth
	projectRoot     string              // working directory for ephemeral HostRunner
	timeouts        func() runners.HostTimeouts
}

// NewRunShellCommand creates a new RunShellCommand tool.
// runner is the per-court shell runner (may be nil — tools that need it must check).
// msgChan is the approval channel passed to ephemeral HostRunner instances (may be nil).
// The optional timeouts argument is a lazy getter returning the configured
// command/approval deadlines for ephemeral HostRunner instances; it is
// re-read on every ephemeral host runner creation (zero values fall back to
// runner defaults).
func NewRunShellCommand(
	hostChecker func(string) (bool, bool, error),
	runner runners.Runner,
	msgChan *chan<- runners.Msg,
	projectRoot string,
	timeouts ...func() runners.HostTimeouts,
) *RunShellCommand {
	t := &RunShellCommand{
		shouldRunOnHost: hostChecker,
		runner:          runner,
		msgChan:         msgChan,
		projectRoot:     projectRoot,
	}
	if len(timeouts) > 0 {
		t.timeouts = timeouts[0]
	}
	return t
}

func (t *RunShellCommand) Name() string {
	return "run_shell_command"
}

func (t *RunShellCommand) Description() string {
	return "Executes a shell command in a persistent shell session inside a container. Current working directory is maintained between commands. IMPORTANT: Each command runs in an isolated subshell for stability and predictability."
}

// msgChanValue safely dereferences the msgChan pointer, returning a nil
// channel if the pointer itself is nil.
func (t *RunShellCommand) msgChanValue() chan<- runners.Msg {
	if t.msgChan == nil {
		return nil
	}
	return *t.msgChan
}

// newHostRunner builds an ephemeral HostRunner with the configured
// command/approval timeouts and the shared approval channel. root is the
// effective root for this call (worktree when the edict is routed into one).
func (t *RunShellCommand) newHostRunner(root string) *runners.HostRunner {
	hostRunner := runners.NewHostRunner(0, root)
	hostRunner.SetMessageChannel(t.msgChanValue())
	if t.timeouts != nil {
		hostRunner.SetTimeouts(t.timeouts())
	}
	return hostRunner
}

// runOnHostFallback runs a command on the host after the sandbox proved
// unavailable, and reports whether the mandate allowed it. The host checker is
// the consent gate: if it refuses, or the command does not match a host pattern
// at all, nothing runs and the caller returns the original sandbox error.
//
// It always requires approval: the fallback is not an operator-approved path,
// so a nil approval channel must deny rather than silently bypass, which is
// what HostRunner.requestApproval already does.
func (t *RunShellCommand) runOnHostFallback(
	ctx context.Context,
	hostRoot string,
	input runners.Input,
	sandboxErr error,
) (runners.Output, bool) {
	if t.shouldRunOnHost == nil {
		return runners.Output{}, false
	}
	runOnHost, _, err := t.shouldRunOnHost(input.Command)
	if err != nil || !runOnHost {
		slog.Warn("sandbox unavailable and host execution is not granted; refusing",
			"command", input.Command, "reason", sandboxErr, "refusal", err)
		return runners.Output{}, false
	}
	slog.Warn("sandbox unavailable, running on host (host execution granted)",
		"command", input.Command)
	hostRunner := t.newHostRunner(hostRoot)
	out, _ := hostRunner.Run(ctx, input)
	return out, true
}

// runnerFor resolves the runner a call must use: the per-call runner carried in
// context (the edict's worktree-aware runner) wins over the runner registered at
// construction time (bound to the Court's project root). Without this, a ritual
// step's shell command silently executes in the main tree.
func (t *RunShellCommand) runnerFor(ctx context.Context) runners.Runner {
	if r, ok := RunnerFromContext(ctx); ok {
		return r
	}
	return t.runner
}

func (t *RunShellCommand) Call(ctx context.Context, input string) (string, error) {
	var params runners.Input
	err := json.Unmarshal([]byte(input), &params)
	if err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	var output runners.Output
	var runErr error

	runner := t.runnerFor(ctx)
	// Ephemeral host runners must also land in the edict's effective root, not
	// the registered project root.
	hostRoot := resolvedRoot(ctx, t.projectRoot)

	runnerInput := runners.Input{
		Command:        params.Command,
		Description:    params.Description,
		BypassApproval: params.BypassApproval,
	}

	// Check if command should run on host based on config patterns. A non-nil
	// error is a REFUSAL (the mandate carries no host-execution grant): return
	// it without running the command anywhere. Falling through would run it in
	// the sandbox — or, worse, on the host — under authority the mandate never
	// granted (edict 1036, seam 7).
	runOnHost, requiresApproval := false, true
	if t.shouldRunOnHost != nil {
		var hostErr error
		runOnHost, requiresApproval, hostErr = t.shouldRunOnHost(params.Command)
		if hostErr != nil {
			return "", hostErr
		}
	}

	if runOnHost {
		// Set the approval flag based on config patterns
		runnerInput.BypassApproval = !requiresApproval

		// Create ephemeral host runner and run directly on host
		hostRunner := t.newHostRunner(hostRoot)
		runnerOutput, err := hostRunner.Run(ctx, runnerInput)
		output.Output = runnerOutput.Output
		output.ExitCode = runnerOutput.ExitCode
		runErr = err
	} else if runner != nil {
		runnerOutput, err := runner.Run(ctx, runnerInput)
		output.Output = runnerOutput.Output
		output.ExitCode = runnerOutput.ExitCode
		runErr = err

		// If we got a SandboxFallbackError, the command already ran
		// on the host — don't retry, just propagate the error so the
		// caller knows the sandbox was bypassed.
		if _, isFallback := runErr.(runners.SandboxFallbackError); isFallback {
			return "", runErr
		}

		// PodmanUnavailableError means podman itself is down (not installed,
		// not running, or timed out). Propagate the error — restarting the
		// container won't help.
		if _, isPodmanDown := runErr.(runners.PodmanUnavailableError); isPodmanDown {
			return "", runErr
		}

		// SandboxMissingError and SandboxSetupMissingError are permanent
		// states (missing image or missing .agents/sandbox files).
		//
		// Falling back to the host here is a HOST-EXECUTION decision, not a
		// retry: it lands the command off the sandbox with more authority than
		// the mandate granted. So it takes the same grant as seam 7, and the
		// approval bypass that used to be inferred from a nil channel is gone —
		// it was unsandboxed execution with nobody asked. Without the grant the
		// sandbox error is returned unchanged (edict 1036, seam 7).
		if _, isMissing := runErr.(runners.SandboxMissingError); isMissing {
			hostOutput, granted := t.runOnHostFallback(ctx, hostRoot, runnerInput, runErr)
			if !granted {
				return "", runErr
			}
			output.Output = hostOutput.Output
			output.ExitCode = hostOutput.ExitCode
			runErr = nil
		} else if _, isSetupMissing := runErr.(runners.SandboxSetupMissingError); isSetupMissing {
			hostOutput, granted := t.runOnHostFallback(ctx, hostRoot, runnerInput, runErr)
			if !granted {
				return "", runErr
			}
			output.Output = hostOutput.Output
			output.ExitCode = hostOutput.ExitCode
			runErr = nil
		} else if runErr != nil {
			// For transient harness errors, try to restart and retry once
			if restartErr := runner.Restart(ctx); restartErr != nil {
				return "", fmt.Errorf("command failed and restart failed: %w (restart error: %v)", runErr, restartErr)
			}

			runnerOutput, err = runner.Run(ctx, runnerInput)
			output.Output = runnerOutput.Output
			output.ExitCode = runnerOutput.ExitCode
			runErr = err
		}
	} else {
		return "", fmt.Errorf("no runner configured")
	}

	if runErr != nil {
		return "", runErr
	}

	outputBytes, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("failed to marshal output: %w", err)
	}

	return string(outputBytes), nil
}

func (t *RunShellCommand) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command to run",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Why we run this command, will be displayed to the user",
			},
		},
		"required": []string{"command"},
	}
}

// Format formats a run_shell_command tool call for display
func (t *RunShellCommand) Format(input, result string, err error) string {
	var params runners.Input
	json.Unmarshal([]byte(input), &params)

	msg := utils.NewMsgBlockBuilder("")
	msg.WriteLn(params.Description)
	msg.Writef("$ %s", params.Command)

	if err != nil {
		msg.WriteLn()
		msg.Writef("ERROR: %v", err)
	} else if result != "" {
		var output map[string]interface{}
		if json.Unmarshal([]byte(result), &output) == nil {
			if ec, ok := output["exitCode"].(string); ok && ec != "0" {
				msg.WriteLn()
				msg.WriteString(ec)
			}
		} else {
			msg.WriteLn()
			msg.Writef("ERROR: %v", err)
		}
	}

	return msg.String() + "\n"
}
