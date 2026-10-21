package runners

import (
	"path/filepath"
	"strings"
)

// NonInteractiveGitEnv returns the environment fragment that forces git
// (and its askpass helpers) to be non-interactive in spawned children.
// Without it, a child git process that needs credentials prints a
// `Username for 'https://...':` prompt to stderr before attempting the
// read; when that child's stderr reaches a terminal the prompt paints
// over whatever is on it — e.g. the asimi TUI. The fragment is
// platform-agnostic and harmless for non-git children.
//
// Keep the sandbox bashrc's own export as-is; this is the process-level
// guarantee, applied to host and podman children alike.
func NonInteractiveGitEnv() []string {
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"SSH_ASKPASS=/bin/false",
		"SSH_ASKPASS_REQUIRE=never",
		"GCM_INTERACTIVE=never",
	}
}

// GoCacheEnv returns the environment fragment pinning GOCACHE to a single
// absolute cache directory at the project root. Edict worktrees are now
// created with an allowlisted ignored-file copy (no .gocache travels), so
// without a shared cache each worktree's `go build`/`go test` would rebuild
// the full module graph and fill the disk. Pointing every runner at
// <projectRoot>/.gocache makes all worktrees (and the main tree) reuse the
// same content-addressed build cache, which is safe under concurrent builds
// (Go's cache writes are atomic). An empty project root yields no fragment:
// we never invent a relative cache path that would otherwise vary with the
// process cwd.
//
// Provenance (edict 946): no tracked Go/shell/Justfile/CI/config file set
// GOCACHE, so the repo-root `.gocache/` seen in the wild came from an
// untracked, host/harness-side export rather than the Court's own invocation.
// This fragment makes the setting explicit and authoritative in-process,
// dropping any inherited GOCACHE first (see EnvWithout) regardless of where
// the old value originated.
func GoCacheEnv(projectRoot string) []string {
	if projectRoot == "" {
		return nil
	}
	return []string{"GOCACHE=" + filepath.Join(projectRoot, ".gocache")}
}

// EnvWithout returns env with every NAME=... entry for the given names
// removed. It lets a caller override a variable that may already be present
// (e.g. a developer's exported GOCACHE) without leaving a duplicate entry
// whose winner depends on the child's getenv implementation.
func EnvWithout(env []string, names ...string) []string {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && drop[name] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
