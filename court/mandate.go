package court

import (
	"github.com/afittestide/asimi/internal/config"
)

// This file carries the Court-side half of the ONE mandate concept (edict
// 1036, gh #180). A mandate is the effective host authority for one client
// process: the session Config narrowed to exactly what the Ruler granted at the
// launch-time trust dialog, plus the grant record. It is owned by the client
// process's PID and dies with it.
//
// E838 RECONCILIATION: asimi once contemplated internal/mandate.Client (the
// Court's contract with a credential-holding server) as a second meaning of
// "Mandate". There is ONE concept; that server contract is retained as a grant
// STRATEGY (brokering), not a rival definition. No internal/mandate package is
// shipped here.

// SetRequestedConfig records the session config as the committed files request
// it. It is a *request*: the Ruler's grant strips it into the mandate.
func (s *Court) SetRequestedConfig(cfg *config.Config) {
	if s == nil {
		return
	}
	s.mandateMu.Lock()
	s.requestedConfig = cfg
	s.mandateMu.Unlock()
}

// RequestedConfig returns the config as requested by the committed files, or
// nil when none has been recorded. The trust dialog reads this to present the
// concrete seams.
func (s *Court) RequestedConfig() *config.Config {
	if s == nil {
		return nil
	}
	s.mandateMu.RLock()
	defer s.mandateMu.RUnlock()
	return s.requestedConfig
}

// Mandate returns the currently bound mandate, or nil. The trust dialog and
// the TUI consult this; the runner reads mandate.Config directly. A returned
// mandate is immutable — it is safe to read it without holding any Court lock.
func (s *Court) Mandate() *config.Mandate {
	if s == nil {
		return nil
	}
	s.mandateMu.RLock()
	defer s.mandateMu.RUnlock()
	return s.mandate
}

// SessionSeams enumerates the concrete, grantable seams the committed config
// requests, for presentation to the Ruler. Nil when no request is recorded —
// that is a genuinely empty answer, not a failure, so the error is always nil
// here. Transport failures are the RPC layer's to report (edict 1036).
func (s *Court) SessionSeams() ([]config.Seam, error) {
	return config.SessionSeams(s.RequestedConfig()), nil
}

// ApplyMandate binds the Ruler's trust-dialog answer as the session's mandate.
// It STRIPS the requested config to the granted set and propagates the result
// to the main runner, the RitualRunner's sandbox config, and the per-edict
// runner factory. OwnerPID is the client process that obtained the mandate.
// The bound mandate is reachable via Mandate().
func (s *Court) ApplyMandate(sel config.Selections) error {
	if s == nil {
		return nil
	}
	m := config.BuildMandate(s.SessionOwnerPID(), s.RequestedConfig(), sel)
	s.bindMandate(m)
	s.logger.Info("mandate applied",
		"owner_pid", m.OwnerPID,
		"grants", len(m.Grants),
		"mounts", len(m.EffectiveSandbox().AdditionalMounts),
		"env", len(m.EffectiveSandbox().PassthroughEnv))
	return nil
}

// ApplyRestrictiveMandate binds the mandate used when no trust dialog was
// presented (headless `-p`, daemon with no TUI): the MOST RESTRICTIVE
// interpretation — no additional host mounts, no passthrough env vars. The
// committed config still records the request, but a request is not a grant.
func (s *Court) ApplyRestrictiveMandate() *config.Mandate {
	if s == nil {
		return nil
	}
	m := config.RestrictiveMandate(s.SessionOwnerPID(), s.RequestedConfig())
	s.bindMandate(m)
	s.logger.Info("restrictive mandate applied (no trust dialog)", "owner_pid", m.OwnerPID)
	return m
}

// bindMandate publishes a newly built mandate and pushes it to every runner.
// The swap is atomic: readers either see the old immutable mandate or the new
// one, never a partial write (edict 1036).
func (s *Court) bindMandate(m *config.Mandate) {
	if s == nil {
		return
	}
	s.mandateMu.Lock()
	s.mandate = m
	s.mandateMu.Unlock()
	s.propagateMandate(m)
}

// effectiveSandboxConfig returns the sandbox config that governs container
// creation: the mandate's granted config when bound, else the session config's
// sandbox. It is the single source the runner factory reads.
func (s *Court) effectiveSandboxConfig() *config.SandboxConfig {
	if s == nil {
		return &config.SandboxConfig{}
	}
	if m := s.Mandate(); m != nil && m.Config != nil {
		sb := m.Config.Sandbox
		return &sb
	}
	if s.sessionCfg != nil {
		sb := s.sessionCfg.Sandbox
		return &sb
	}
	return &config.SandboxConfig{}
}

// propagateMandate pushes the effective sandbox config and the mandate to the
// main runner and the RitualRunner. The per-edict runner factory reads
// Court.Mandate() at creation time, so it needs no separate push.
func (s *Court) propagateMandate(m *config.Mandate) {
	sb := s.effectiveSandboxConfig()

	// Bring the live main runner (and the connection-side runner) under the
	// mandate. A host runner has no mounts to narrow; a podman runner reads
	// mandate.Config for mounts/env and stamps the owner PID.
	if pr, ok := s.runner.(interface{ SetMandate(*config.Mandate) }); ok {
		pr.SetMandate(m)
	}

	// The RitualRunner's verify step rebuilds the Court's main runner via
	// InitShellRunner; give it the granted data so a rebuild is not a re-widen.
	if s.ritualGuard != nil && s.ritualGuard.RitualRunner() != nil {
		rr := s.ritualGuard.RitualRunner()
		rr.SetConfig(sb, s.projectSlug(), s.ritualGuard.repoInfo, s.config)
		rr.SetMandate(m)
	}

	// Re-target any already-created per-edict runners so a mandate applied
	// after an edict's runner exists is not ignored.
	s.edictRunnersMu.Lock()
	for edictID, r := range s.edictRunners {
		if setter, ok := r.(interface{ SetMandate(*config.Mandate) }); ok {
			setter.SetMandate(m)
		}
		s.logger.Debug("propagated mandate to per-edict runner", "edict", edictID)
	}
	s.edictRunnersMu.Unlock()
}

// projectSlug returns the Court's configured project slug, or "".
func (s *Court) projectSlug() string {
	if s == nil || s.config == nil {
		return ""
	}
	return s.config.Project
}

// recordRequestedConfigFromSession captures the sandbox section of the session
// config as the requested set of grantable seams. SessionConfig does not carry
// the whole config.Config, so the request is rebuilt from its Sandbox section;
// that is sufficient because every grantable seam (additional_mounts,
// passthrough_env) lives there.
func (s *Court) recordRequestedConfigFromSession(sc *SessionConfig) {
	if s == nil || sc == nil {
		return
	}
	req := &config.Config{}
	req.Sandbox = sc.Sandbox
	s.mandateMu.Lock()
	s.requestedConfig = req
	s.mandateMu.Unlock()
}
