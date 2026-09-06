// Asimi - AI coding agent governed by a 幕府 of six ministers and a sage
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strings"
	"time"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/daemon"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/alecthomas/kong"
	tea "github.com/charmbracelet/bubbletea"
	isatty "github.com/mattn/go-isatty"
	"go.uber.org/fx"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

var program *tea.Program

// logBaseName names the log file (without extension). Default suits the TUI;
// runDaemonMode overrides it so daemon and TUI write to separate files and
// don't interleave when both run with --debug in the same cwd.
var logBaseName = "asimi"

var cli struct {
	Version         bool   `help:"Print version information"`
	Prompt          string `short:"p" help:"Prompt to send to the agent"`
	Debug           bool   `help:"Enable debug logging"`
	NoCleanup       bool   `help:"Don't remove container on exit (for debugging)"`
	Handsoff        bool   `help:"Auto-answer zhengming and YESNO prompts (hands-off mode)"`
	MaxTurns        int    `help:"Override max conversation turns (benchmark control)"`
	Model           string `help:"Override the LLM model (overrides config file and ASIMI_MODEL, for harness-driven runs)"`
	Provider        string `help:"Override the LLM provider (overrides config file and ASIMI_PROVIDER)"`
	ReasoningEffort string `help:"Overrides the LLM reasoning effort (low|medium|high), overrides config file and ASIMI_REASONING_EFFORT)"`
	IsolatedHost    bool   `help:"Run shell commands on host without podman sandbox or approval gates (for already-isolated environments)"`
	CPUProfile      string `help:"Write CPU profile to file"`
	MemProfile      string `help:"Write memory profile to file"`
	Trace           string `help:"Write execution trace to file"`
	ProfileExitMs   int    `help:"Exit after N milliseconds (for profiling startup)"`
	Atif            bool   `help:"Enable ATIF trajectory recording (agent name: asimi)"`
}

func initLogger() {
	var logDir string
	var logPath string

	// Determine log directory and path
	if cli.Debug {
		// In debug mode, log to current directory
		logDir = "."
		logPath = filepath.Join(logDir, logBaseName+".log")
		logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			panic(fmt.Errorf("failed to open log file %s: %w", logPath, err))
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})))
	} else {
		// In production mode, log to user's data directory
		homeDir, err := os.UserHomeDir()
		if err != nil {
			panic(fmt.Errorf("failed to get user home directory: %w", err))
		}
		logDir = filepath.Join(homeDir, ".local", "share", "asimi")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			panic(fmt.Errorf("failed to create log directory %s: %w", logDir, err))
		}
		logPath = filepath.Join(logDir, logBaseName+".log")
		logFile := &lumberjack.Logger{
			Filename:   logPath,
			MaxSize:    10, // megabytes
			MaxBackups: 3,
			MaxAge:     28, // days
			Compress:   true,
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}
}

func runInteractiveMode() error {
	startTime := time.Now()

	// Check if we are running in a terminal (skip check if profiling with auto-exit)
	if cli.ProfileExitMs == 0 && !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsTerminal(os.Stdin.Fd()) {
		fmt.Println("This program requires a terminal to run.")
		fmt.Println("Please run it in a terminal emulator.")
		return nil
	}

	if cli.Debug {
		slog.Debug("[TIMING] Terminal check completed", "duration", time.Since(startTime))
	}

	fmt.Printf("Asimi %s loading...\n", utils.AsimiVersion)

	var tuiModel *TUIModel
	var fxOptions []fx.Option

	// Always silence the fx logger — its PROVIDE/RUNNING output is noise,
	// even in debug mode. We use slog for all application logging.
	fxOptions = append(fxOptions, fx.NopLogger)

	// Add core providers
	fxOptions = append(fxOptions,
		fx.Provide(
			ProvideLogger,
			ProvideRepoInfo,
			ProvideConfig,
			ProvideStorage,
			ProvideGormDB,
			ProvideScheduler,
			ProvideShellRunner,
			ProvidePromptHistory,
			ProvideCommandHistory,
			ProvideSessionHistory,
			ProvideTUIModel,
			StartTUI,
			ProvideCourt,
		),
		fx.Populate(&tuiModel),
	)

	// Create fx app with all providers
	app := fx.New(fxOptions...)

	// Start the fx app (runs OnStart hooks for async initialization)
	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		return fmt.Errorf("failed to start fx app: %w", err)
	}
	// Wire modes. Precedence:
	//   ASIMI_DAEMON_SOCKET=/path — connect to a specific running daemon
	//   ASIMI_LOOPBACK=1          — in-process net.Pipe loopback (testing)
	//   default                   — autostart daemon (spawn if needed)
	var onProgramReady func(*tea.Program)
	if sock := os.Getenv("ASIMI_DAEMON_SOCKET"); sock != "" {
		hook, err := installDaemonSocket(ctx, tuiModel, sock)
		if err != nil {
			return fmt.Errorf("daemon socket: %w", err)
		}
		onProgramReady = hook
	} else if os.Getenv("ASIMI_LOOPBACK") != "" {
		hook, err := installRPCLoopback(ctx, tuiModel)
		if err != nil {
			return fmt.Errorf("loopback: %w", err)
		}
		onProgramReady = hook
	} else {
		hook, err := installDaemonAutostart(ctx, tuiModel)
		if err != nil {
			return fmt.Errorf("daemon autostart: %w", err)
		}
		onProgramReady = hook
	}

	tuiProgram := tea.NewProgram(tuiModel, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if onProgramReady != nil {
		onProgramReady(tuiProgram)
	}

	subCtx, cancelSub := context.WithCancel(ctx)
	defer cancelSub()
	events := tuiModel.court.Subscribe(subCtx)
	go func() {
		for {
			select {
			case <-subCtx.Done():
				return
			case msg := <-events:
				tuiProgram.Send(msg)
			}
		}
	}()
	defer app.Stop(ctx)

	slog.Debug("[TIMING] fx app initialized", "duration", time.Since(startTime))

	// Check for updates with visual feedback before TUI enters alt-screen
	fmt.Print("Checking for Updates...")
	type updateResult struct {
		hasUpdate bool
	}
	resultCh := make(chan updateResult, 1)
	updateCtx, updateCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer updateCancel()
	go func() {
		hasUpdate := AutoCheckForUpdates(updateCtx, utils.AsimiVersion)
		resultCh <- updateResult{hasUpdate: hasUpdate}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var updateAvailable bool
checkLoop:
	for {
		select {
		case res := <-resultCh:
			updateAvailable = res.hasUpdate
			break checkLoop
		case <-ticker.C:
			fmt.Print(".")
		case <-updateCtx.Done():
			slog.Debug("pre-TUI update check timed out")
			break checkLoop
		}
	}

	// Clear the line
	fmt.Print("\r" + strings.Repeat(" ", 40) + "\r")

	if updateAvailable {
		tuiModel.updateAvailable = true
	}

	// If profile-exit-ms is set, schedule an exit after that duration
	if cli.ProfileExitMs > 0 {
		go func() {
			time.Sleep(time.Duration(cli.ProfileExitMs) * time.Millisecond)
			if cli.Debug {
				slog.Debug("[TIMING] Auto-exiting after N ms for profiling", "ms", cli.ProfileExitMs)
			}
			program.Send(tea.Quit())
		}()
	}

	// fire an event to get the court going
	// to avoid race condition where health check runs before model is ready
	_, runErr := tuiProgram.Run()

	if runErr != nil {
		return fmt.Errorf("alas, there's been an error: %w", runErr)
	}

	slog.Debug("[TIMING] Total Run() time", "duration", time.Since(startTime))
	return nil
}

type errMsg struct{ err error }

// llmInitSuccessMsg is sent when LLM initialization completes
// successfully. The bifrost client lives daemon-side now; callers use
// it only as a "we're ready, paint the provider" signal.
//
// fromSwitch marks the message as coming from a mid-session model switch
// (via switchModel) rather than a genuine boot. The handler must not
// re-fire court_started for a model switch: that lifecycle event seeds
// new-tab greetings and persists tian_events rows, both of which should
// happen exactly once per process, not on every model change.
type llmInitSuccessMsg struct {
	fromSwitch bool
}

// llmInitErrorMsg is sent when LLM initialization fails
type llmInitErrorMsg struct {
	err error
}

// compactCompleteMsg is sent when conversation compaction completes successfully
type compactCompleteMsg struct {
	summary string
}

// compactErrorMsg is sent when conversation compaction fails
type compactErrorMsg struct {
	err error
}

// updateAvailableMsg is sent when a newer version is available
type updateAvailableMsg struct{}

// validateConfigs checks user and project TOML configs before FX DI
// starts. A broken config produces an unreadable FX dependency-chain
// error; catching it here gives the user a clean diagnostic.
func validateConfigs() error {
	_, userCfgPath, _ := config.UserConfigPath()
	if err := config.ValidateConfigFile(userCfgPath); err != nil {
		return err
	}
	if projectRoot, err := os.Getwd(); err == nil {
		projectCfgPath := filepath.Join(projectRoot, ".agents", "asimi.conf")
		if err := config.ValidateConfigFile(projectCfgPath); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	// Validate configs before any dispatch so the user sees clean
	// TOML errors instead of FX dependency-chain noise. Do this
	// once here — both the TUI and the daemon need it.
	if err := validateConfigs(); err != nil {
		fmt.Fprintln(os.Stderr, "asimi:", err)
		os.Exit(1)
	}

	// Subcommand dispatch. A proper kong refactor can come later;
	// today a leading `daemon` arg is enough to branch cleanly.
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
		kong.Parse(&cli)
		if err := daemon.Run(initDaemonShared); err != nil {
			fmt.Fprintln(os.Stderr, "daemon:", err)
			os.Exit(1)
		}
		return
	}

	startTime := time.Now()
	kong.Parse(&cli)

	// Handle --version flag
	if cli.Version {
		fmt.Printf("Asimi CLI v%s\n", utils.AsimiVersion)
		os.Exit(0)
	}

	// Start profiling if requested
	if cli.CPUProfile != "" {
		f, err := os.Create(cli.CPUProfile)
		if err != nil {
			slog.Error("Could not create CPU profile", "error", err)
			os.Exit(1)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			slog.Error("Could not start CPU profile", "error", err)
			os.Exit(1)
		}
		defer pprof.StopCPUProfile()
		slog.Error("CPU profiling enabled", "file", cli.CPUProfile)
	}

	if cli.Trace != "" {
		f, err := os.Create(cli.Trace)
		if err != nil {
			slog.Error("Could not create trace file", "error", err)
			os.Exit(1)
		}
		defer f.Close()
		if err := trace.Start(f); err != nil {
			slog.Error("Could not start trace", "error", err)
			os.Exit(1)
		}
		defer trace.Stop()
		slog.Info("Execution tracing enabled", "writing to", cli.Trace)
	}

	// Log startup timing
	if cli.Debug {
		slog.Debug("[TIMING] main() started", "time", startTime)
	}

	// Determine if we should run in non-interactive mode
	// Non-interactive mode is triggered by:
	// 1. Explicit -p flag: asimi -p "prompt here"
	// 2. Non-interactive stdin (pipe/redirect): echo "prompt" | asimi
	isStdinTerminal := isatty.IsTerminal(os.Stdin.Fd())
	hasPromptArg := cli.Prompt != ""

	// If no -p flag but stdin is not a terminal, read from stdin
	if !hasPromptArg && !isStdinTerminal {
		// Read prompt from stdin
		var builder strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				builder.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		cli.Prompt = strings.TrimSpace(builder.String())
		hasPromptArg = cli.Prompt != ""
	}

	// For non-interactive mode, initialize the old logger
	// For interactive mode, the fx-provided logger will be used
	if hasPromptArg {
		initLogger()
		if cli.Debug {
			slog.Debug("[TIMING] initLogger() completed", "duration", time.Since(startTime))
		}
	}

	if hasPromptArg {
		os.Exit(runHeadlessMode())
	}

	// Interactive mode
	if err := runInteractiveMode(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// Write memory profile if requested
	if cli.MemProfile != "" {
		f, err := os.Create(cli.MemProfile)
		if err != nil {
			slog.Error("Could not create memory profile", "error", err)
			os.Exit(1)
		}
		defer f.Close()
		runtime.GC() // get up-to-date statistics
		if err := pprof.WriteHeapProfile(f); err != nil {
			slog.Error("Could not write memory profile", "error", err)
			os.Exit(1)
		}
		slog.Info("Memory profile written", "file", cli.MemProfile)
	}

	slog.Debug("[TIMING] Total execution time", "duration", time.Since(startTime))
}

// atifAgentName returns the agent name for ATIF trajectory recording.
// Returns "asimi" when --atif is set, empty string otherwise.
func atifAgentName() string {
	if cli.Atif {
		return "asimi"
	}
	return ""
}
