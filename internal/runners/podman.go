package runners

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	spec "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/afittestide/asimi/internal/repo"
	"go.podman.io/podman/v6/pkg/api/handlers"
	"go.podman.io/podman/v6/pkg/bindings"
	"go.podman.io/podman/v6/pkg/bindings/containers"
	"go.podman.io/podman/v6/pkg/specgen"
)

// PodmanRunner executes shell commands in a podman container using per-command
// `podman exec` sessions.
type PodmanRunner struct {
	imageName        string
	containerName    string
	allowFallback    bool
	noCleanup        bool
	config           *Config
	repoInfo         repo.RepoInfo
	msgChan          chan<- Msg
	fallback         Runner
	mu               sync.Mutex
	conn             context.Context
	establishConn    func(ctx context.Context) (context.Context, error)
	checkImage       func(ctx context.Context) error
	containerStarted bool
}

// healthcheckTimeout is the maximum time to wait for a healthcheck command.
const healthcheckTimeout = 5 * time.Second

// NewPodmanRunner creates a new PodmanRunner
func NewPodmanRunner(cfg *Config, repoInfo repo.RepoInfo, connID uint64, fallback Runner) *PodmanRunner {
	imageName := cfg.ImageName
	if imageName == "" {
		imageName = fmt.Sprintf("localhost/asimi/sandbox/%s:latest", repoInfo.Slug)
	}

	containerName := fmt.Sprintf("asimi-shell-%s-%d", repo.SanitizeSegment(repoInfo.Slug), connID)
	slog.Debug("NewPodmanRunner", "containerName", containerName, "imageName", imageName, "slug", repoInfo.Slug)

	r := &PodmanRunner{
		imageName:     imageName,
		containerName: containerName,
		allowFallback: cfg.AllowHostFallback,
		noCleanup:     cfg.NoCleanup,
		config:        cfg,
		repoInfo:      repoInfo,
		fallback:      fallback,
	}
	r.establishConn = r.establishConnection
	r.checkImage = r.defaultCheckImage
	return r
}

func (r *PodmanRunner) SetMessageChannel(msgChan chan<- Msg) {
	r.msgChan = msgChan
}

func (r *PodmanRunner) initialize(ctx context.Context) error {
	slog.Debug("initializing podman shell runner")

	r.mu.Lock()
	hasConnection := r.conn != nil
	r.mu.Unlock()

	if !hasConnection {
		if err := r.preflightSandbox(ctx); err != nil {
			return err
		}

		slog.Debug("establishing podman connection")
		conn, err := r.establishConn(context.Background())
		if err != nil {
			return fmt.Errorf("failed to connect to podman: %w", err)
		}
		r.mu.Lock()
		r.conn = conn
		r.mu.Unlock()
		slog.Debug("podman connection established")
	} else if r.conn.Err() != nil {
		slog.Info("podman connection cancelled, re-establishing", "error", r.conn.Err())
		r.mu.Lock()
		r.conn = nil
		r.mu.Unlock()

		conn, err := r.establishConn(context.Background())
		if err != nil {
			return fmt.Errorf("failed to reconnect to podman: %w", err)
		}
		r.mu.Lock()
		r.conn = conn
		r.mu.Unlock()
		slog.Debug("podman connection re-established")
	}

	existingRunning := false
	containerWasAlreadyStarted := false

	r.mu.Lock()
	if !r.containerStarted {
		// Claim the work atomically: no other goroutine will enter this path.
		r.containerStarted = true
		r.mu.Unlock()

		slog.Debug("ensuring container for this instance", "containerName", r.containerName)

		inspectData, err := containers.Inspect(r.conn, r.containerName, nil)
		if err == nil {
			if inspectData.State.Running {
				slog.Debug("container already running", "containerName", r.containerName)
				existingRunning = true
				r.sendContainerLaunched(fmt.Sprintf("%d", inspectData.State.Pid))
			} else {
				slog.Debug("starting existing container", "containerName", r.containerName)
				if err := containers.Start(r.conn, r.containerName, nil); err != nil {
					r.mu.Lock()
					r.containerStarted = false
					r.mu.Unlock()
					return fmt.Errorf("failed to start existing container: %w", err)
				}
				slog.Debug("existing container started", "containerName", r.containerName)
				startedInspect, err := containers.Inspect(r.conn, r.containerName, nil)
				if err != nil {
					r.mu.Lock()
					r.containerStarted = false
					r.mu.Unlock()
					return fmt.Errorf("failed to inspect started container: %w", err)
				}
				r.sendContainerLaunched(fmt.Sprintf("%d", startedInspect.State.Pid))
			}
		} else {
			slog.Debug("container doesn't exist, creating new one", "containerName", r.containerName)
			containerID, err := r.createContainer(ctx)
			if err != nil {
				r.mu.Lock()
				r.containerStarted = false
				r.mu.Unlock()
				return err
			}
			r.sendContainerLaunched(containerID)
		}
	} else {
		r.mu.Unlock()

		// Container may still be starting (another goroutine is creating it).
		// Retry with backoff to avoid immediately resetting containerStarted.
		inspectData, err := containers.Inspect(r.conn, r.containerName, nil)
		if err != nil {
			for retry := 0; retry < 5; retry++ {
				time.Sleep(10 * time.Millisecond)
				if inspectData, err = containers.Inspect(r.conn, r.containerName, nil); err == nil {
					break
				}
			}
		}
		if err != nil {
			slog.Info("container inspect failed, resetting", "error", err)
			r.mu.Lock()
			r.containerStarted = false
			r.mu.Unlock()
			return r.initialize(ctx)
		}
		if !inspectData.State.Running {
			slog.Info("container stopped externally, resetting for re-creation", "containerName", r.containerName)
			r.mu.Lock()
			r.containerStarted = false
			r.mu.Unlock()
			return r.initialize(ctx)
		}

		containerWasAlreadyStarted = true
		slog.Debug("container already started, skipping checks", "containerName", r.containerName)
	}

	// Healthcheck: when reusing an already-running container.
	if existingRunning || containerWasAlreadyStarted {
		if err := r.healthcheck(ctx); err != nil {
			slog.Info("container unhealthy, force-killing and recreating", "containerName", r.containerName, "error", err)

			forceTrue := true
			volumesTrue := true
			if _, rmErr := containers.Remove(r.conn, r.containerName, &containers.RemoveOptions{Force: &forceTrue, Volumes: &volumesTrue}); rmErr != nil {
				return fmt.Errorf("failed to remove unhealthy container: %w", rmErr)
			}

			r.mu.Lock()
			r.containerStarted = false
			r.mu.Unlock()

			if r.msgChan != nil {
				r.msgChan <- SandboxUnhealthyMsg{
					Message:       "🔄 Stale container detected and recreated",
					ContainerName: r.containerName,
				}
			}

			return r.initialize(ctx)
		}
		slog.Info("existing container is healthy", "containerName", r.containerName)
	}

	slog.Debug("initialization complete")
	return nil
}

func (r *PodmanRunner) preflightSandbox(ctx context.Context) error {
	if err := r.checkSandboxFiles(); err != nil {
		return err
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return r.checkImage(probeCtx)
}

func (r *PodmanRunner) defaultCheckImage(ctx context.Context) error {
	return CheckSandboxImageAvailable(ctx, r.imageName, r.repoInfo.ProjectRoot)
}

func (r *PodmanRunner) checkSandboxFiles() error {
	root := r.projectWorkingRoot()
	if root == "" {
		return nil
	}

	required := []string{
		".agents/sandbox",
		".agents/sandbox/Dockerfile",
		".agents/sandbox/bashrc",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			return SandboxSetupMissingError{}
		}
	}

	return nil
}

func (r *PodmanRunner) projectWorkingRoot() string {
	if r.repoInfo.ProjectRoot == "" {
		return ""
	}
	if r.repoInfo.WorktreePath != "" {
		return filepath.Join(r.repoInfo.ProjectRoot, r.repoInfo.WorktreePath)
	}
	return r.repoInfo.ProjectRoot
}

// healthcheck runs a simple exec command to verify the container is responsive.
func (r *PodmanRunner) healthcheck(ctx context.Context) error {
	r.mu.Lock()
	conn := r.conn
	r.mu.Unlock()

	// Derive from conn (has podman client) with timeout, but also respect
	// the caller's ctx for cancellation.
	hcCtx, cancel := context.WithTimeout(conn, healthcheckTimeout)
	defer cancel()

	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-hcCtx.Done():
		}
	}()

	_, _, err := r.execCommand(hcCtx, "echo __ASIMI_HEALTHY")
	if err != nil {
		return fmt.Errorf("healthcheck: exec failed: %w", err)
	}
	slog.Info("healthcheck passed")
	return nil
}

func (r *PodmanRunner) establishConnection(ctx context.Context) (context.Context, error) {
	slog.Debug("attempting to establish podman connection")

	currentUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("failed to get current user: %w", err)
	}

	// macOS applehv machine sockets live under $TMPDIR/podman/ (podman 4.x+ on Apple Silicon)
	tmpPodmanDir := filepath.Join(os.TempDir(), "podman")
	if matches, err := filepath.Glob(filepath.Join(tmpPodmanDir, "*-api.sock")); err == nil {
		// Try the default machine socket first, then any others
		var sorted []string
		defaultSock := filepath.Join(tmpPodmanDir, "podman-machine-default-api.sock")
		for _, m := range matches {
			if m == defaultSock {
				sorted = append([]string{m}, sorted...)
			} else {
				sorted = append(sorted, m)
			}
		}
		for _, sock := range sorted {
			slog.Debug("trying macOS applehv podman socket", "socket", sock)
			conn, err := bindings.NewConnection(context.Background(), "unix://"+sock)
			if err == nil {
				slog.Debug("successfully connected via macOS applehv socket", "socket", sock)
				return conn, nil
			}
			slog.Debug("failed to connect via macOS applehv socket", "socket", sock, "error", err)
		}
	}

	// macOS qemu machine socket (older podman installations)
	macOSSocket := filepath.Join(currentUser.HomeDir, ".local/share/containers/podman/machine/podman.sock")
	slog.Debug("trying macOS qemu podman socket", "socket", macOSSocket)
	if _, err := os.Stat(macOSSocket); err == nil {
		conn, err := bindings.NewConnection(context.Background(), "unix://"+macOSSocket)
		if err == nil {
			slog.Debug("successfully connected via macOS qemu socket")
			return conn, nil
		}
		slog.Debug("failed to connect via macOS socket", "error", err)
	}

	// Linux: user runtime socket
	userSocket := fmt.Sprintf("unix:///run/user/%s/podman/podman.sock", currentUser.Uid)
	slog.Debug("trying user socket", "socket", userSocket)
	if conn, err := bindings.NewConnection(context.Background(), userSocket); err == nil {
		slog.Debug("successfully connected via user socket")
		return conn, nil
	}

	// Linux: root system socket
	slog.Debug("trying system socket")
	conn, err := bindings.NewConnection(context.Background(), "unix:///var/run/podman/podman.sock")
	if err != nil {
		slog.Debug("failed to connect via system socket", "error", err)
		return nil, err
	}

	slog.Debug("successfully connected via system socket")
	return conn, nil
}

func (r *PodmanRunner) createContainer(ctx context.Context) (string, error) {
	slog.Debug("creating new container", "image", r.imageName, "containerName", r.containerName, "noCleanup", r.noCleanup)

	s := specgen.NewSpecGenerator(r.imageName, false)
	s.Name = r.containerName
	autoRemove := !r.noCleanup
	s.Remove = &autoRemove
	if r.noCleanup {
		slog.Info("Container will NOT be auto-removed on exit (--no-cleanup flag set)")
	}

	terminal := false
	s.Terminal = &terminal
	s.Env = map[string]string{"TERM": "dumb"}
	for _, kv := range NonInteractiveGitEnv() {
		name, val, _ := strings.Cut(kv, "=")
		s.Env[name] = val
	}
	for _, name := range r.config.PassthroughEnv {
		if val, ok := os.LookupEnv(name); ok {
			s.Env[name] = val
		}
	}
	s.NetNS = specgen.Namespace{NSMode: specgen.Host}
	s.Command = []string{"sleep", "infinity"}

	absPath, err := filepath.Abs(r.repoInfo.ProjectRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	slog.Debug("mounting directory to container", "source", absPath, "destination", absPath)

	mounts := []spec.Mount{
		{
			Type:        "bind",
			Source:      absPath,
			Destination: absPath,
		},
	}

	for _, m := range r.config.AdditionalMounts {
		slog.Debug("adding additional mount", "source", m.Source, "destination", m.Destination)
		mounts = append(mounts, spec.Mount{
			Type:        "bind",
			Source:      m.Source,
			Destination: m.Destination,
		})
	}

	for _, relPath := range r.config.PlatformOverlays {
		overlayDest := filepath.Join(absPath, relPath)
		info, err := os.Stat(overlayDest)
		if err != nil {
			slog.Warn("platform overlay host path does not exist", "path", overlayDest, "overlay", relPath)
			continue
		}
		if info.IsDir() {
			volumeName := fmt.Sprintf("asimi-overlay-%s-%s", md5Hash(absPath), sanitizePath(relPath))
			slog.Debug("adding directory overlay as named volume", "volume", volumeName, "destination", overlayDest)
			s.Volumes = append(s.Volumes, &specgen.NamedVolume{
				Name: volumeName,
				Dest: overlayDest,
			})
		} else {
			overlayDataDir, err := overlayFileDir(absPath)
			if err != nil {
				return "", fmt.Errorf("failed to create overlay data directory: %w", err)
			}
			overlayFilePath := filepath.Join(overlayDataDir, sanitizePath(relPath))
			if _, err := os.Stat(overlayFilePath); err != nil {
				if err := os.WriteFile(overlayFilePath, nil, 0644); err != nil {
					return "", fmt.Errorf("failed to create overlay file: %w", err)
				}
			}
			slog.Debug("adding file overlay as bind mount", "source", overlayFilePath, "destination", overlayDest)
			mounts = append(mounts, spec.Mount{
				Type:        "bind",
				Source:      overlayFilePath,
				Destination: overlayDest,
			})
		}
	}

	s.Mounts = mounts

	slog.Debug("calling CreateWithSpec")
	createResponse, err := containers.CreateWithSpec(r.conn, s, nil)
	if err != nil {
		if strings.Contains(err.Error(), "is already in use") {
			slog.Info("container name already in use, removing stale container", "containerName", r.containerName)
			force := true
			volumes := true
			if _, rmErr := containers.Remove(r.conn, r.containerName, &containers.RemoveOptions{Force: &force, Volumes: &volumes}); rmErr != nil {
				return "", fmt.Errorf("failed to remove stale container: %w", rmErr)
			}
			slog.Info("stale container removed, retrying creation", "containerName", r.containerName)
			createResponse, err = containers.CreateWithSpec(r.conn, s, nil)
			if err != nil {
				return "", fmt.Errorf("failed to create container after removing stale one: %w", err)
			}
		} else {
			return "", fmt.Errorf("failed to create container: %w", err)
		}
	}
	slog.Debug("container created", "containerID", createResponse.ID)

	slog.Debug("starting container", "containerID", createResponse.ID)
	if err := containers.Start(r.conn, createResponse.ID, nil); err != nil {
		return "", fmt.Errorf("failed to start container: %w", err)
	}
	slog.Debug("container started successfully", "containerID", createResponse.ID)

	return createResponse.ID, nil
}

// sendContainerLaunched notifies the TUI that a container is running.
func (r *PodmanRunner) sendContainerLaunched(containerID string) {
	if r.msgChan != nil {
		r.msgChan <- ContainerLaunchedMsg{
			Message:     "🐳 Container launched",
			ContainerID: containerID,
		}
	}
}

// execCommand creates a podman exec session, starts and attaches to it,
// collects stdout/stderr, inspects the exit code, and removes the session.
// The caller is responsible for deriving ctx from r.conn with the appropriate
// timeout and cancellation forwarding (see Run and healthcheck).
// Returns the combined output and the exit code.
func (r *PodmanRunner) execCommand(ctx context.Context, command string) (string, int, error) {
	env := []string{"BASH_ENV=/root/.bashrc", "TERM=dumb"}
	env = append(env, NonInteractiveGitEnv()...)
	for _, name := range r.config.PassthroughEnv {
		if val, ok := os.LookupEnv(name); ok {
			env = append(env, fmt.Sprintf("%s=%s", name, val))
		}
	}

	workingDir := r.projectWorkingRoot()

	execConfig := &handlers.ExecCreateConfig{
		ExecCreateRequest: struct {
			User         string
			Privileged   bool
			Tty          bool
			ConsoleSize  *[2]uint `json:",omitempty"`
			AttachStdin  bool
			AttachStderr bool
			AttachStdout bool
			DetachKeys   string
			Env          []string
			WorkingDir   string
			Cmd          []string
		}{
			Cmd:          []string{"bash", "-c", command},
			WorkingDir:   workingDir,
			Env:          env,
			Tty:          false,
			AttachStdout: true,
			AttachStderr: true,
		},
	}

	slog.Debug("creating exec session", "command", command)
	execID, err := containers.ExecCreate(ctx, r.containerName, execConfig)
	if err != nil {
		return "", -1, fmt.Errorf("exec create failed: %w", err)
	}

	var stdoutBuf, stderrBuf bytes.Buffer

	opts := (&containers.ExecStartAndAttachOptions{}).
		WithOutputStream(&stdoutBuf).
		WithErrorStream(&stderrBuf).
		WithAttachOutput(true).
		WithAttachError(true)

	slog.Debug("starting exec session", "execID", execID)
	if err := containers.ExecStartAndAttach(ctx, execID, opts); err != nil {
		return "", -1, fmt.Errorf("exec start failed: %w", err)
	}

	inspectData, err := containers.ExecInspect(ctx, execID, nil)
	if err != nil {
		return "", -1, fmt.Errorf("exec inspect failed: %w", err)
	}

	// Cleanup exec session (best-effort).
	if rmErr := containers.ExecRemove(ctx, execID, nil); rmErr != nil {
		slog.Debug("exec remove failed (non-fatal)", "execID", execID, "error", rmErr)
	}

	output := stdoutBuf.String()
	if stderrBuf.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderrBuf.String()
	}

	return output, inspectData.ExitCode, nil
}

func (r *PodmanRunner) Run(ctx context.Context, input Input) (Output, error) {
	slog.Debug("Run called", "command", input.Command)

	if err := r.initialize(ctx); err != nil {
		slog.Error("failed to initialize", "error", err)
		if r.allowFallback && r.fallback != nil {
			slog.Warn("sandbox unavailable, falling back to host shell", "command", input.Command)
			out, fallbackErr := r.fallback.Run(ctx, input)
			return out, SandboxFallbackError{Err: err, FallbackErr: fallbackErr}
		}
		return Output{}, err
	}

	timeoutMinutes := 10
	if r.config.TimeoutMinutes > 0 {
		timeoutMinutes = r.config.TimeoutMinutes
	}
	timeout := time.Duration(timeoutMinutes) * time.Minute

	r.mu.Lock()
	conn := r.conn
	r.mu.Unlock()

	// Derive from conn (has podman client) with timeout, but also respect
	// the caller's ctx for cancellation.
	execCtx, cancel := context.WithTimeout(conn, timeout)
	defer cancel()

	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-execCtx.Done():
		}
	}()

	output, exitCode, err := r.execCommand(execCtx, input.Command)
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return Output{
				Output:   fmt.Sprintf("Command timed out after %v", timeout),
				ExitCode: "124",
			}, nil
		}
		if ctx.Err() != nil {
			return Output{
				Output:   "Command cancelled",
				ExitCode: "130",
			}, ctx.Err()
		}
		return Output{}, err
	}

	return Output{
		Output:   output,
		ExitCode: fmt.Sprintf("%d", exitCode),
	}, nil
}

func (r *PodmanRunner) Restart(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	slog.Info("restarting container attachment", "containerName", r.containerName)

	r.containerStarted = false

	if r.msgChan != nil {
		resultChan := make(chan int, 1)
		r.msgChan <- ClearSchedulerMsg{ResultChan: resultChan}
		abortedCount := <-resultChan
		if abortedCount > 0 {
			slog.Info("aborted pending tool calls during restart", "count", abortedCount)
		}
	}

	slog.Info("container attachment restarted - will reconnect on next command")
	return nil
}

func (r *PodmanRunner) Close(ctx context.Context) error {
	slog.Debug("closing podman shell runner", "noCleanup", r.noCleanup)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.conn != nil && r.containerStarted {
		timeout := uint(1)
		slog.Debug("stopping container", "containerName", r.containerName, "timeout", timeout)
		if err := containers.Stop(r.conn, r.containerName, &containers.StopOptions{Timeout: &timeout}); err != nil {
			slog.Debug("stop returned error (may already be stopped)", "error", err)
		}

		if !r.noCleanup {
			slog.Debug("removing container", "containerName", r.containerName)
			force := true
			volumes := true
			if _, err := containers.Remove(r.conn, r.containerName, &containers.RemoveOptions{
				Force:   &force,
				Volumes: &volumes,
			}); err != nil {
				slog.Debug("remove returned error", "error", err)
			} else {
				slog.Debug("container removed successfully", "containerName", r.containerName)
			}
		} else {
			slog.Info("Container NOT removed (--no-cleanup flag set)", "containerName", r.containerName)
			slog.Info("To manually remove the container later, run:", "command", fmt.Sprintf("podman rm -f %s", r.containerName))
		}
	}

	r.containerStarted = false

	slog.Debug("podman shell runner closed successfully")
	return nil
}

func (r *PodmanRunner) RunnerType() string {
	return "podman"
}

func (r *PodmanRunner) GetOS() string {
	return "linux"
}

func (r *PodmanRunner) ContainerID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.containerStarted {
		return r.containerName
	}
	return ""
}

func (r *PodmanRunner) GetImageName() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.imageName
}

func (r *PodmanRunner) AllowFallback(allow bool) {
	r.allowFallback = allow
}

// HealthCheck verifies the sandbox container is healthy by running uname
// and checking that "Linux" appears in the output.
func (r *PodmanRunner) HealthCheck(ctx context.Context) error {
	result, err := r.Run(ctx, Input{
		Command:     "uname",
		Description: "sandbox health check",
	})
	if err != nil {
		return fmt.Errorf("sandbox health check failed: %w", err)
	}
	if result.ExitCode != "0" {
		return fmt.Errorf("sandbox health check failed: uname exited with %s", result.ExitCode)
	}
	if !strings.Contains(result.Output, "Linux") {
		return fmt.Errorf("sandbox health check failed: expected Linux in output, got: %s", result.Output)
	}
	return nil
}

// md5Hash returns the hex MD5 hash of s.
func md5Hash(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", h)
}

// overlayFileDir computes and creates the per-platform data directory for file overlays.
func overlayFileDir(absPath string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	dir := filepath.Join(homeDir, ".local", "share", "asimi", "overlays", md5Hash(absPath))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create overlay data directory: %w", err)
	}
	return dir, nil
}

// sanitizePath replaces non-alphanumeric characters with underscores.
func sanitizePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
