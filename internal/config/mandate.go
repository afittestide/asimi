package config

import (
	"fmt"
	"strings"
)

// This file defines the ONE mandate concept (edict 1036, gh #180).
//
// A mandate is the effective host authority for one client process: the session
// Config narrowed to exactly what the Ruler granted at the launch-time trust
// dialog, plus the grant record. It holds a *reference* to the (stripped)
// config — the runner reads mandate.Config, never a copy — and Grants pins the
// decision, so any future re-widening check consults Grants, not the config.
//
// E838 RECONCILIATION (ruling 4): asimi's earlier design used
// internal/mandate.Client — the Court's contract with a credential-holding
// server — as a second, rival meaning of "Mandate". Ruling 4 resolved the
// collision: there is ONE concept. That server contract is not deleted; it is
// retained as a grant STRATEGY (brokering), not a rival definition: where a
// credential can be brokered, the sandbox never receives the raw secret and
// the mandate need not carry a mount for it at all. No internal/mandate
// package exists in this tree; nothing is duplicated. If e838's client is later
// revived, it must fulfil a Grant here rather than mint a second Mandate type.
//
// NAMING: do NOT introduce internal/mandate as a second mandate type; extend
// this concept.

// SeamKind classifies a host-asset seam presented to the Ruler. The concrete
// seams are enumerated in intent/docs/sandbox-seams.md; only the requested-by-config
// seams (mounts and env) are grantable here, because the working-tree bind and
// the cache/overlay volumes are structural, and host network / host-runner /
// isolated-host are not represented in the dialog at all.
type SeamKind string

const (
	// SeamMount is a host path bind-mounted into the container.
	SeamMount SeamKind = "mount"
	// SeamEnv is a named host environment variable forwarded into the container.
	SeamEnv SeamKind = "env"
	// SeamHostExec is unsandboxed execution on the host itself (seam 7) — a
	// run_shell_command routed by the run_on_host / safe_run_on_host patterns.
	// It is strictly more authority than anything the sandbox grants, so it is
	// never implied by another grant and never granted by default: only an
	// explicit Selections.HostExec can grant it (edict 1036, seam 7).
	SeamHostExec SeamKind = "host_exec"
	// SeamMCPServer is an unsandboxed stdio MCP server subprocess spawned on
	// the host (edict 661, seam 9). It starts off by default and is stripped
	// under RestrictiveMandate; it is granted either by an explicit
	// Selections.MCPServers[id] entry or via Selections.AllowAll.
	SeamMCPServer SeamKind = "mcp_server"
)

// SeamHostRunner is the seam label of the single host-execution grant. One
// grant covers the seam: a Ruler who grants host execution has granted it for
// the session, not per command. Matches the pattern an operator approved.
const SeamHostRunner = "run_on_host"

// HostBypassFlag is the operator CLI flag that runs every shell command on the
// host with no sandbox at all (seam 8). It is deliberately NOT a seam: it is
// not requested by any committed file, and the trust dialog never presents it
// nor claims to cover it — no SeamKind corresponds to it, so SessionSeams can
// never emit it and no Selections key can grant or deny it. Choosing it is an
// operator's explicit command-line act, outside the mandate's reach; the dialog
// only ever promises the project bind and the structural seams stay open,
// which is true whether or not this flag is set (edict 1036, seam 8).
const HostBypassFlag = "--isolated-host"

// Seam is one concrete host asset a session could be granted. It is the
// dialog's input — a *request* derived from the committed config — never a
// grant by itself.
type Seam struct {
	Kind SeamKind
	// Label is the human-facing description shown in the dialog (e.g.
	// "~/.config/gcloud" or "GOOGLE_APPLICATION_CREDENTIALS").
	Label string
	// Mount is set for SeamMount seams (Source/Destination/ReadOnly as
	// requested). ReadOnly is the requested default, not the grant.
	Mount Mount
	// EnvName is set for SeamEnv seams.
	EnvName string
	// MCPServer is set for SeamMCPServer seams (ID, Command, Env).
	MCPServer MCPServerConfig
}

// Grant records the Ruler's decision about one concrete seam. Granted=false is
// an explicit decline (kept so the dialog's answer is auditable). Writable is
// meaningful only for SeamMount grants.
type Grant struct {
	Kind     SeamKind
	Target   string // mount Source or env var name
	Writable bool   // mounts only
	Granted  bool
}

// Mandate is the effective host authority for one client process.
type Mandate struct {
	// OwnerPID is the os.Getpid() of the client that obtained the mandate —
	// the same PID already stamped on the sandbox container. The mandate is
	// void once this process is gone.
	OwnerPID int
	// Config is a reference to the granted config: the session config
	// narrowed to exactly what the Ruler granted. Never a copy held for
	// convenience — the runner reads this pointer directly.
	Config *Config
	// Grants is the explicit record of what the Ruler chose. Because Config
	// is a reference, Grants is what any re-widening check must consult.
	Grants []Grant
}

// Selections is the Ruler's trust-dialog answer: which requested seams were
// granted, and which mounts were opened for writing. A missing key means "not
// granted". AllowAll is the explicit, named "Allow all" choice — an
// affirmative action, never the pre-checked default. Allow-all grants REACH,
// never write: by itself it never opens a mount for writing, so a bare
// AllowAll leaves every mount read-only. Mounts supplies WRITE, one mount at a
// time; the two COMPOSE, so AllowAll plus Mounts[src]=true lands that mount
// writable — exactly the " rw" the dialog rendered (edict 1036).
type Selections struct {
	AllowAll bool
	Mounts   map[string]bool // mount Source -> writable granted
	Env      map[string]bool // env var name -> granted
	// HostExec grants unsandboxed execution on the host (seam 7). It is a
	// separate, affirmative field on purpose: host execution is strictly more
	// authority than the sandbox, so it must never arrive as a default, as a
	// repository request, or as a side effect of AllowAll — which grants REACH
	// into the sandbox, never the host. Without it, a pattern-matched host
	// command is refused (edict 1036, seam 7).
	HostExec bool
	// MCPServers grants unsandboxed stdio MCP server subprocesses on the host
	// (server ID -> granted, edict 661). Off by default and stripped under
	// RestrictiveMandate; also granted when AllowAll is true.
	MCPServers map[string]bool
}

// SessionSeams enumerates the concrete, grantable seams a session may request:
// its additional mounts, its passthrough env vars, its configured MCP servers,
// and — when the committed config requests any host pattern — host execution
// (seam 7). The working-tree bind (always rw, structural) and the cache/overlay
// volumes are not listed here; neither is HostBypassFlag, which is not a
// grantable seam — no committed file can request it and the dialog never
// implies it is covered. See intent/docs/sandbox-seams.md.
func SessionSeams(cfg *Config) []Seam {
	if cfg == nil {
		return nil
	}
	seams := make([]Seam, 0, len(cfg.Sandbox.AdditionalMounts)+len(cfg.Sandbox.PassthroughEnv)+len(cfg.MCP.Servers)+1)
	for _, m := range cfg.Sandbox.AdditionalMounts {
		// A blank source names no asset: its row would be a checkbox with no
		// label and its grant nothing to mount. The loader drops such entries;
		// this guard is defense in depth for every caller that hand-builds a
		// Config rather than loading it from committed files.
		if strings.TrimSpace(m.Source) == "" {
			continue
		}
		seams = append(seams, Seam{
			Kind:  SeamMount,
			Label: m.Source,
			Mount: m,
		})
	}
	for _, name := range cfg.Sandbox.PassthroughEnv {
		seams = append(seams, Seam{
			Kind:    SeamEnv,
			Label:   name,
			EnvName: name,
		})
	}
	for _, srv := range cfg.MCP.Servers {
		id := strings.TrimSpace(srv.ID)
		if id == "" {
			continue
		}
		cmdText := strings.Join(srv.Command, " ")
		label := fmt.Sprintf("%s (%s)", id, cmdText)
		if len(srv.Env) > 0 {
			label = fmt.Sprintf("%s [env: %s]", label, strings.Join(srv.Env, ", "))
		}
		seams = append(seams, Seam{
			Kind:  SeamMCPServer,
			Label: label,
			MCPServer: MCPServerConfig{
				ID:      id,
				Command: append([]string(nil), srv.Command...),
				Env:     append([]string(nil), srv.Env...),
			},
		})
	}
	// Host execution is offered only when the config actually requests it: with
	// no host pattern there is nothing to grant, and a row for it would claim
	// authority the session cannot use.
	if len(cfg.Sandbox.RunOnHost) > 0 || len(cfg.Sandbox.SafeRunOnHost) > 0 {
		seams = append(seams, Seam{Kind: SeamHostExec, Label: SeamHostRunner})
	}
	return seams
}

// BuildMandate turns the Ruler's Selections into the session's mandate by
// STRIPPING the requested config: every additional mount and passthrough env
// var absent from the selections is removed. The returned config contains only
// granted seams; because Mandate.Config is a reference, that config governs
// the sandbox. The env map never widens the request — only requested names can
// be granted.
//
// The copy is deliberately SHALLOW beyond the two replaced slices:
// PlatformOverlays, CacheVolumes and IgnoreCopyGlobs stay shared with the
// requested config. Seams 4-5 are structural (see intent/docs/sandbox-seams.md): the
// mandate narrows what the Ruler chooses, not the project's own toolchain
// plumbing. Do not "fix" this into a deep copy — an emptied CacheVolumes would
// silently rebuild a cold container and an emptied IgnoreCopyGlobs would change
// what the sandbox copies in.
func BuildMandate(ownerPID int, requested *Config, sel Selections) *Mandate {
	grants := make([]Grant, 0)
	if requested == nil {
		return &Mandate{OwnerPID: ownerPID, Config: &Config{}, Grants: grants}
	}

	granted := &Config{}
	*granted = *requested // shallow copy; the two slice fields are replaced below

	mounts := make([]Mount, 0, len(requested.Sandbox.AdditionalMounts))
	for _, m := range requested.Sandbox.AdditionalMounts {
		writable, named := sel.Mounts[m.Source]
		if !sel.AllowAll && !named {
			grants = append(grants, Grant{Kind: SeamMount, Target: m.Source, Granted: false})
			continue
		}
		// Two independent grants compose here: Allow all supplies REACH (the
		// sandbox may see the asset), and Mounts supplies WRITE (the Ruler
		// opened this one mount by name). Honouring the per-mount answer under
		// Allow all is what keeps the dialog, Selections and the mandate
		// telling the same story — a row rendered " rw" must land writable
		// (edict 1036). Because `writable` starts false and Allow all never
		// sets it, Allow all alone still never opens a mount for writing.
		openForWrite := writable
		m.ReadOnly = !openForWrite
		mounts = append(mounts, m)
		grants = append(grants, Grant{Kind: SeamMount, Target: m.Source, Writable: openForWrite, Granted: true})
	}
	granted.Sandbox.AdditionalMounts = mounts

	env := make([]string, 0, len(requested.Sandbox.PassthroughEnv))
	for _, name := range requested.Sandbox.PassthroughEnv {
		if !sel.AllowAll {
			if !sel.Env[name] {
				grants = append(grants, Grant{Kind: SeamEnv, Target: name, Granted: false})
				continue
			}
		}
		env = append(env, name)
		grants = append(grants, Grant{Kind: SeamEnv, Target: name, Granted: true})
	}
	granted.Sandbox.PassthroughEnv = env

	// Seam 7 (host execution) is recorded from the explicit grant only. The
	// requested config is never consulted: run_on_host/safe_run_on_host are a
	// repository's *request* to reach the host, and a request is not a grant.
	// Declining records the decision so the refusal is auditable.
	grants = append(grants, Grant{Kind: SeamHostExec, Target: SeamHostRunner, Granted: sel.HostExec})

	// MCP servers (edict 661) execute unsandboxed on the host via stdio and
	// can read host env vars. They are off by default and stripped unless
	// granted by ID or via AllowAll; RestrictiveMandate grants zero servers.
	mcpServers := make([]MCPServerConfig, 0, len(requested.MCP.Servers))
	for _, srv := range requested.MCP.Servers {
		id := strings.TrimSpace(srv.ID)
		if id == "" {
			continue
		}
		if !sel.AllowAll && !sel.MCPServers[id] {
			grants = append(grants, Grant{Kind: SeamMCPServer, Target: id, Granted: false})
			continue
		}
		mcpServers = append(mcpServers, srv)
		grants = append(grants, Grant{Kind: SeamMCPServer, Target: id, Granted: true})
	}
	granted.MCP.Servers = mcpServers

	return &Mandate{OwnerPID: ownerPID, Config: granted, Grants: grants}
}

// RestrictiveMandate is the mandate used when no trust dialog was presented
// (headless `-p`, daemon with no TUI). It is the MOST RESTRICTIVE
// interpretation of the *requested* seams: no additional host mounts, no
// passthrough env vars, and NO host execution are granted. It does not strip
// every host seam: the shallow copy in BuildMandate leaves the structural seams
// in force — PlatformOverlays and CacheVolumes (seams 4-5), the always-present
// writable working-tree bind (seam 1) and the host network namespace (seam 6).
// Seam 7 (host execution) is NOT one of those: it is gated on an affirmative
// SeamHostExec grant, which this mandate never carries, so a headless or
// daemon-with-no-TUI session refuses host execution rather than quietly running
// it (see Court.CheckHostCommand). The committed config still records the
// *request*, but a request is not a grant (edict 1036).
func RestrictiveMandate(ownerPID int, requested *Config) *Mandate {
	return BuildMandate(ownerPID, requested, Selections{})
}

// AllowAllMandate grants every requested seam. It exists so the explicit
// "Allow all" choice is a named, testable operation rather than an implicit
// fallthrough. It grants reach only: every mount lands read-only, exactly as
// the dialog's "read-only unless you open it" promises. It never grants host
// execution: "Allow all" means every requested *asset*, and the host runner is
// neither requested by a repository nor an asset the sandbox can bound
// (edict 1036, seam 7).
func AllowAllMandate(ownerPID int, requested *Config) *Mandate {
	return BuildMandate(ownerPID, requested, Selections{AllowAll: true})
}

// OwnerAlive reports whether the mandate's owner process is still live, using
// the supplied predicate (runners passes its processAlive). A nil mandate, a
// non-positive PID, or a nil predicate is not alive: the mandate is void.
func (m *Mandate) OwnerAlive(alive func(int) bool) bool {
	return m != nil && m.OwnerPID > 0 && alive != nil && alive(m.OwnerPID)
}

// EffectiveSandbox returns the sandbox config the mandate governs. It is the
// granted (stripped) config's Sandbox section — the single source the runner
// builds container mounts and env from when a mandate is bound.
func (m *Mandate) EffectiveSandbox() SandboxConfig {
	if m == nil || m.Config == nil {
		return SandboxConfig{}
	}
	return m.Config.Sandbox
}

// WithOwner returns a COPY of the mandate with OwnerPID set to pid. It exists so
// re-binding an owner (a daemon client reconnecting) never mutates a published
// mandate: the granted config reference and every grant are carried over
// unchanged, so concurrent readers holding the old pointer stay consistent
// (edict 1036).
func (m *Mandate) WithOwner(pid int) *Mandate {
	if m == nil {
		return &Mandate{OwnerPID: pid}
	}
	cp := *m
	cp.OwnerPID = pid
	if m.Grants != nil {
		cp.Grants = append([]Grant(nil), m.Grants...)
	}
	return &cp
}

// GrantFor returns the grant recorded for a seam target, and whether one was
// recorded. Re-widening checks must consult this rather than the effective
// config.
func (m *Mandate) GrantFor(kind SeamKind, target string) (Grant, bool) {
	if m == nil {
		return Grant{}, false
	}
	for _, g := range m.Grants {
		if g.Kind == kind && g.Target == target {
			return g, true
		}
	}
	return Grant{}, false
}
