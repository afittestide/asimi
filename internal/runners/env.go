package runners

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
