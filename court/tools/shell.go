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
	shouldRunOnHost func(cmd string) (runOnHost, needsApproval bool)
	runner          runners.Runner
	msgChan         *chan<- runners.Msg // pointer to Court.msgChan — single source of truth
	projectRoot     string              // working directory for ephemeral HostRunner
	timeouts        runners.HostTimeouts
}

// NewRunShellCommand creates a new RunShellCommand tool.
// runner is the per-court shell runner (may be nil — tools that need it must check).
// msgChan is the approval channel passed to ephemeral HostRunner instances (may be nil).
// The optional timeouts argument carries the configured command/approval
// deadlines for ephemeral HostRunner instances (zero values fall back to
// runner defaults).
func NewRunShellCommand(
	hostChecker func(string) (bool, bool),
	runner runners.Runner,
	msgChan *chan<- runners.Msg,
	projectRoot string,
	timeouts ...runners.HostTimeouts,
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
// command/approval timeouts and the shared approval channel.
func (t *RunShellCommand) newHostRunner() *runners.HostRunner {
	hostRunner := runners.NewHostRunner(0, t.projectRoot)
	hostRunner.SetMessageChannel(t.msgChanValue())
	hostRunner.SetTimeouts(t.timeouts)
	return hostRunner
}

func (t *RunShellCommand) Call(ctx context.Context, input string) (string, error) {
	var params runners.Input
	err := json.Unmarshal([]byte(input), &params)
	if err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	var output runners.Output
	var runErr error

	runnerInput := runners.Input{
		Command:        params.Command,
		Description:    params.Description,
		BypassApproval: params.BypassApproval,
	}

	// Check if command should run on host based on config patterns
	runOnHost, requiresApproval := false, true
	if t.shouldRunOnHost != nil {
		runOnHost, requiresApproval = t.shouldRunOnHost(params.Command)
	}

	if runOnHost {
		// Set the approval flag based on config patterns
		runnerInput.BypassApproval = !requiresApproval

		// Create ephemeral host runner and run directly on host
		hostRunner := t.newHostRunner()
		runnerOutput, err := hostRunner.Run(ctx, runnerInput)
		output.Output = runnerOutput.Output
		output.ExitCode = runnerOutput.ExitCode
		runErr = err
	} else if t.runner != nil {
		runnerOutput, err := t.runner.Run(ctx, runnerInput)
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
		// states (missing image or missing .agents/sandbox files). Fall
		// back to host execution without attempting a restart.
		if _, isMissing := runErr.(runners.SandboxMissingError); isMissing {
			slog.Warn("sandbox not available, running on host", "command", runnerInput.Command)
			hostRunner := t.newHostRunner()
			bypassApproval := t.msgChan == nil || *t.msgChan == nil
			hostOutput, hostErr := hostRunner.Run(ctx, runners.Input{
				Command:        runnerInput.Command,
				Description:    runnerInput.Description,
				BypassApproval: bypassApproval,
			})
			output.Output = hostOutput.Output
			output.ExitCode = hostOutput.ExitCode
			runErr = hostErr
		} else if _, isSetupMissing := runErr.(runners.SandboxSetupMissingError); isSetupMissing {
			slog.Warn("sandbox setup missing, running on host", "command", runnerInput.Command)
			hostRunner := t.newHostRunner()
			bypassApproval := t.msgChan == nil || *t.msgChan == nil
			hostOutput, hostErr := hostRunner.Run(ctx, runners.Input{
				Command:        runnerInput.Command,
				Description:    runnerInput.Description,
				BypassApproval: bypassApproval,
			})
			output.Output = hostOutput.Output
			output.ExitCode = hostOutput.ExitCode
			runErr = hostErr
		} else if runErr != nil {
			// For transient harness errors, try to restart and retry once
			if restartErr := t.runner.Restart(ctx); restartErr != nil {
				return "", fmt.Errorf("command failed and restart failed: %w (restart error: %v)", runErr, restartErr)
			}

			runnerOutput, err = t.runner.Run(ctx, runnerInput)
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
