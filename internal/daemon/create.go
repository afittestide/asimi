package daemon

import (
	"context"
	"fmt"
	"strconv"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/types"
	"github.com/afittestide/asimi/storage"
)

// SessionStoreFactory creates a SessionPersister from shared storage
// and repo info. The main package passes its NewSessionStore wrapper
// so the daemon package doesn't depend on the main package.
type SessionStoreFactory func(sdb *storage.DB, repoInfo repo.RepoInfo, maxSessions, maxAgeDays int) (court.SessionPersister, error)

// createCourt creates a runner and Court for a new daemon connection.
// It loads the project config, builds the runner (PodmanRunner or
// HostRunner depending on isolatedHost), creates the Court,
// wires the session persister (using config.DefaultSessionConfig for
// defaults), and starts it.
//
// verifiedUsername is the socket-verified peer identity from serveOne. It is
// the only authoritative Court username; a committed [court] username or a
// client-supplied handshake value must never override it (edict 1006).
func createCourt(
	ctx context.Context,
	shared *Shared,
	connID uint64,
	hp types.SetContextParams,
	verifiedUsername string,
	projectCfg *config.Config,
	repoInfo repo.RepoInfo,
	newSessionStore SessionStoreFactory,
	isolatedHost bool,
) (*court.Court, runners.Runner, error) {
	if verifiedUsername == "" {
		return nil, nil, fmt.Errorf("daemon: refusing connection: socket peer username could not be verified")
	}

	var runner runners.Runner
	if isolatedHost {
		hostRunner := runners.NewHostRunner(connID, repoInfo.ProjectRoot)
		hostRunner.SetTimeouts(runners.ResolveHostTimeouts(
			projectCfg.RunShellCommand.TimeoutMinutes,
			projectCfg.Sandbox.ApprovalTimeout,
		))
		runner = hostRunner
	} else {
		runner = runners.NewPodmanRunner(&projectCfg.Sandbox, repoInfo, strconv.FormatUint(connID, 10), nil)
	}

	// The Court username is the socket-verified peer identity — never the
	// committed [court] username and never the client handshake slug. A
	// repository carrying [court] username = "<victim>" must have no effect
	// (edict 1006). The Court itself resolves the OS user, which on the
	// per-uid daemon socket is exactly this verified peer; assert they agree
	// so the invariant fails closed rather than silently diverging.
	if osUser := config.OSUsername(); osUser != verifiedUsername {
		return nil, nil, fmt.Errorf("daemon: refusing connection: socket peer %q does not match OS user %q", verifiedUsername, osUser)
	}
	// The project scope is always derived from the checkout: neither a
	// committed [court] project nor the client handshake slug may choose it
	// (edict 1005 — the client slug names a path only the server can verify).
	// ResolveProjectSlug errors only when origin is absent and the remotes name
	// different repos, in which case the handshake must refuse rather than
	// silently scope writes to the wrong project.
	project, err := repo.ResolveProjectSlug(repoInfo.ProjectRoot, repoInfo.HasVCS)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve project scope: %w", err)
	}
	courtCfg := projectCfg.Court.Resolved(project, isolatedHost)

	ct := court.NewCourt(shared.DB, courtCfg, runner, shared.Logger)
	ct.SetRepoInfo(repoInfo)
	ct.SetMCPConfig(projectCfg.MCP)

	// Wire session persister so daemon sessions are persisted to DB,
	// same as the TUI path does via ProvideCourt.
	if shared.Storage != nil && projectCfg.Session.Enabled && newSessionStore != nil {
		maxSessions := max(0, projectCfg.Session.MaxSessions)
		maxAgeDays := max(0, projectCfg.Session.MaxAgeDays)
		sessionStore, err := newSessionStore(shared.Storage, repoInfo, maxSessions, maxAgeDays)
		if err != nil {
			shared.Logger.Warn("daemon: failed to create session store", "conn_id", connID, "err", err)
		} else {
			ct.SetSessionPersister(sessionStore)
		}
	}

	if err := ct.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("court start: %w", err)
	}

	return ct, runner, nil
}

// reconfigureModel reloads the project config, reinitialises Bifrost,
// and reconfigures the Court model. It is called on every
// SetContext — both the first handshake and subsequent re-calls — so
// that the model always reflects the latest handshake params and
// on-disk config.
func reconfigureModel(ctx context.Context, ct *court.Court, hp types.SetContextParams) error {
	projectCfg, err := config.LoadProjectConfig(hp.ProjectRoot, false)
	if err != nil {
		return fmt.Errorf("load project config: %w", err)
	}

	// Apply per-run CLI/model overrides handed through the handshake.
	// These take precedence over config-file values (and env vars, which
	// have already been folded into the config by LoadProjectConfig).
	if hp.Model != "" {
		projectCfg.LLM.Model = hp.Model
	}
	if hp.Provider != "" {
		projectCfg.LLM.Provider = hp.Provider
	}
	if hp.ReasoningEffort != "" {
		if err := config.ValidateReasoningEffort(hp.ReasoningEffort); err != nil {
			return err
		}
		projectCfg.LLM.ReasoningEffort = hp.ReasoningEffort
	}

	// HasVCS is the single authoritative gitless-ground flag and SetContextParams
	// does not carry it over the wire — re-derive it so the RepoInfo never
	// treats a real git repository as gitless ground. The slug is derived here
	// too: the client's Project is advisory, never the sandbox image name or
	// Court scope (edict 1005).
	var hasVCS bool
	slug := ""
	if hp.ProjectRoot != "" {
		ri := repo.GetRepoInfoForRoot(hp.ProjectRoot)
		hasVCS = ri.HasVCS
		slug = ri.Slug
	}

	repoInfo := repo.RepoInfo{
		ProjectRoot:  hp.ProjectRoot,
		WorktreePath: hp.WorktreePath,
		Branch:       hp.Branch,
		HasVCS:       hasVCS,
		Slug:         slug,
	}

	bifrostClient, err := court.InitBifrost(
		ctx,
		projectCfg.LLM.RequestTimeoutSeconds,
		projectCfg.LLM.StreamIdleTimeoutSeconds,
		projectCfg.LLM.MaxRetries,
		projectCfg.LLM.BaseURL,
		hp.APIKeys,
		hp.CodexAccountID,
		projectCfg.LLM.AllowPrivateNetwork,
	)
	if err != nil {
		return fmt.Errorf("init bifrost: %w", err)
	}

	sessionCfg := &court.SessionConfig{
		LLM:             projectCfg.LLM,
		Sandbox:         projectCfg.Sandbox,
		RunShellCommand: projectCfg.RunShellCommand,
		AgentsFile:      projectCfg.Session.AgentsFile,
		WorkingDir:      hp.ProjectRoot,
		// Interactive Ministers build their sessions from this config, so the
		// authoritative RepoInfo must ride on it. This runs on every handshake
		// including every `:switch`; leaving it off made those sessions
		// dispatch their file tools unbound, so reads came from the registry's
		// fallback root while writes landed in the worktree (edict 1051).
		RepoInfo: repoInfo,
	}

	ct.ConfigureModel(bifrostClient, sessionCfg, repoInfo)

	return nil
}
