package daemon

import (
	"context"
	"fmt"

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
func createCourt(
	ctx context.Context,
	shared *Shared,
	connID uint64,
	hp types.SetContextParams,
	projectCfg *config.Config,
	repoInfo repo.RepoInfo,
	newSessionStore SessionStoreFactory,
	isolatedHost bool,
) (*court.Court, runners.Runner, error) {
	var runner runners.Runner
	if isolatedHost {
		runner = runners.NewHostRunner(connID, repoInfo.ProjectRoot)
	} else {
		runner = runners.NewPodmanRunner(&projectCfg.Sandbox, repoInfo, connID, nil)
	}

	courtCfg := config.DefaultCourtConfig()
	if hp.Username != "" {
		courtCfg.Username = hp.Username
	}
	if hp.Project != "" {
		courtCfg.Project = hp.Project
	} else if repoInfo.Slug != "" {
		courtCfg.Project = repoInfo.Slug
	}
	if projectCfg.Court.OutputLimit > 0 {
		courtCfg.OutputLimit = projectCfg.Court.OutputLimit
	}
	courtCfg.IsolatedHost = isolatedHost

	ct := court.NewCourt(shared.DB, courtCfg, runner, shared.Logger)
	ct.SetRepoInfo(repoInfo)

	// Wire session persister so daemon sessions are persisted to DB,
	// same as the TUI path does via ProvideCourt.
	if shared.Storage != nil && projectCfg.Session.Enabled && newSessionStore != nil {
		defaults := config.DefaultSessionConfig()
		maxSessions := defaults.MaxSessions
		maxAgeDays := defaults.MaxAgeDays
		if projectCfg.Session.MaxSessions > 0 {
			maxSessions = projectCfg.Session.MaxSessions
		}
		if projectCfg.Session.MaxAgeDays > 0 {
			maxAgeDays = projectCfg.Session.MaxAgeDays
		}
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

	repoInfo := repo.RepoInfo{
		ProjectRoot:  hp.ProjectRoot,
		WorktreePath: hp.WorktreePath,
		Branch:       hp.Branch,
		Slug:         hp.Project,
	}

	bifrostClient, err := court.InitBifrost(
		ctx,
		projectCfg.LLM.RequestTimeoutSeconds,
		projectCfg.LLM.StreamIdleTimeoutSeconds,
		projectCfg.LLM.MaxRetries,
		projectCfg.LLM.BaseURL,
		hp.APIKeys,
		hp.CodexAccountID,
	)
	if err != nil {
		return fmt.Errorf("init bifrost: %w", err)
	}

	sessionCfg := &court.SessionConfig{
		LLM:        projectCfg.LLM,
		Sandbox:    projectCfg.Sandbox,
		AgentsFile: projectCfg.Session.AgentsFile,
		WorkingDir: hp.ProjectRoot,
	}

	ct.ConfigureModel(bifrostClient, sessionCfg, repoInfo)

	return nil
}
