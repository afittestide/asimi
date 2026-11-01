package runners

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// imageExistsTimeout bounds the local `podman image exists` probe so a hung
// podman fails fast. A pull-on-miss is not a 5s operation and is bounded by the
// caller's context instead (see PodmanRunner.preflightSandbox).
const imageExistsTimeout = 5 * time.Second

// execPodman runs a podman subcommand and returns its combined output. It is a
// variable so tests can stub the exec seam without a real podman or network.
var execPodman = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "podman", args...).CombinedOutput()
}

// CheckSandboxImageAvailable checks if the podman daemon is running and the
// specified sandbox image exists. Pass the full image name (e.g.,
// "localhost/asimi-sandbox-myproject:latest") and projectRoot (for contextual
// error messages in SandboxMissingError).
//
// When the image is absent and imageName is a pullable remote reference, it
// pulls the image once and re-checks. This keeps a cold CI machine working with
// no extra env flag and no manual `podman pull`: the preflight no longer rejects
// a pullable image before any pull is attempted. A pull failure still surfaces
// SandboxMissingError, and a podman that is down/absent is never treated as a
// pull problem.
func CheckSandboxImageAvailable(ctx context.Context, imageName, projectRoot string) error {
	existsErr := checkImageExists(ctx, imageName, projectRoot)
	if existsErr == nil {
		return nil
	}
	if _, missing := existsErr.(SandboxMissingError); !missing {
		return existsErr // podman down / not installed / timed out — not a pull problem
	}
	if !isPullableImage(imageName) {
		return existsErr // a local build (localhost/...) must be built, not pulled
	}

	slog.Info("sandbox image absent; pulling once", "image", imageName)
	if _, pullErr := execPodman(ctx, "pull", imageName); pullErr != nil {
		slog.Warn("podman pull failed; sandbox image still missing",
			"image", imageName, "error", pullErr)
		return existsErr
	}
	return checkImageExists(ctx, imageName, projectRoot)
}

// checkImageExists runs `podman image exists` under a short probe deadline and
// maps a non-zero exit to a typed error. It never pulls.
func checkImageExists(ctx context.Context, imageName, projectRoot string) error {
	probeCtx, cancel := context.WithTimeout(ctx, imageExistsTimeout)
	defer cancel()
	output, err := execPodman(probeCtx, "image", "exists", imageName)
	if err == nil {
		return nil
	}
	return podmanImageExistsError(probeCtx.Err(), output, err, imageName, projectRoot)
}

// isPullableImage reports whether imageName names a remote reference podman can
// pull: it carries a registry (a dot in the first path segment) or a :tag, and
// is not a localhost/... image built on this machine.
func isPullableImage(imageName string) bool {
	if imageName == "" || strings.HasPrefix(imageName, "localhost/") {
		return false
	}
	ref := imageName
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i] // drop any digest
	}
	lastSlash := strings.LastIndexByte(ref, '/')
	if strings.LastIndexByte(ref, ':') > lastSlash {
		return true // an explicit :tag
	}
	first := ref
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		first = ref[:i]
	}
	return strings.ContainsAny(first, ".:")
}

// IsPodmanAvailable checks if podman daemon is running AND the specified sandbox image exists.
// Pass the full image name (e.g., "localhost/asimi/sandbox/myproject:latest").
// It is a pure local probe: it never pulls.
func IsPodmanAvailable(imageName string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), imageExistsTimeout)
	defer cancel()

	if err := checkImageExists(ctx, imageName, ""); err != nil {
		slog.Debug("podman not available or image missing", "image", imageName, "error", err)
		return false
	}

	slog.Debug("podman available with image", "image", imageName)
	return true
}

func podmanImageExistsError(ctxErr error, output []byte, err error, imageName, projectRoot string) error {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return PodmanUnavailableError{Reason: "Podman did not respond. Start it with `podman machine start`, then try again."}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return PodmanUnavailableError{Reason: "Podman is not installed or not on PATH. Install Podman, then run `:init`."}
	}

	message := strings.TrimSpace(string(output))
	if isPodmanConnectionError(message) {
		return PodmanUnavailableError{Reason: "Podman is not running. Start it with `podman machine start`, then try again."}
	}

	return SandboxMissingError{ImageName: imageName, ProjectRoot: projectRoot}
}

func isPodmanConnectionError(message string) bool {
	lower := strings.ToLower(message)
	needles := []string{
		"cannot connect to podman",
		"unable to connect",
		"connection refused",
		"podman machine",
		"podman.sock",
		"no such file or directory",
	}
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
