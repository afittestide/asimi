package court

import (
	"context"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/ministers"
)

// MinisterDef is an alias for ministers.MinisterDef so existing code in
// the court package continues to work without mass refactoring.
type MinisterDef = ministers.MinisterDef

// LoadMinisters delegates to ministers.LoadMinisters.
func LoadMinisters() ([]MinisterDef, error) {
	return ministers.LoadMinisters()
}

// LoadAllMinisters delegates to ministers.LoadAllMinisters.
func LoadAllMinisters(projectDir string) ([]MinisterDef, error) {
	return ministers.LoadAllMinisters(projectDir)
}

// commonTools lists tool names available to every minister, regardless of
// their extra_tools definition. These are resolved via the tool registry's
// ExtraTools mechanism (the minister ID is passed for factory tools).
// tian_ledger is common — the Tian ledger is open to all ministers.
// consult_minister is NOT included here — only the secretary needs it,
// and it is provided via the secretary's extra_tools list.
var commonTools = []string{"ask_ruler", "tian_ledger"}

// ministerImpl is the generic, YAML-driven minister type. It replaces the
// five hand-coded minister structs (Secretary, Forge, Judge, Chancellor,
// War) with a single implementation that derives its behaviour
// from a MinisterDef.
type ministerImpl struct {
	*MinisterBase
	def MinisterDef
}

// NewMinister creates a generic minister from a MinisterDef and shared base.
func NewMinister(def MinisterDef, base *MinisterBase) *ministerImpl {
	base.ministerID = def.ID
	m := &ministerImpl{
		MinisterBase: base,
		def:          def,
	}
	m.self = m
	return m
}

// ID returns the minister's unique identifier (from the def).
func (m *ministerImpl) ID() string {
	return m.def.ID
}

// SystemPrompt returns the kanji (if present) followed by the role text,
// fed to system_base.tmpl as the Role template variable.
func (m *ministerImpl) SystemPrompt() string {
	if m.def.Kanji != "" {
		return m.def.Kanji + "\n\n" + m.def.Role
	}
	return m.def.Role
}

// Tools returns the minister's LLM tools, derived from the permission
// string in the def via the central tool registry. No fallback path —
// the toolRegistry is always wired before ministers run.
func (m *ministerImpl) Tools() []Tool {
	if m.toolRegistry == nil {
		return nil
	}
	perm, err := tools.ParsePermissions(m.def.Permissions)
	if err != nil {
		m.logger.Warn("invalid permissions in minister def", "id", m.def.ID, "perm", m.def.Permissions, "error", err)
		return nil
	}
	public := m.toolRegistry.ForPermissions(perm)
	extra := m.toolRegistry.ExtraTools(m.def.ID, m.def.ExtraTools)
	common := m.toolRegistry.ExtraTools(m.def.ID, commonTools)
	result := make([]Tool, 0, len(public)+len(extra)+len(common))
	for _, t := range public {
		result = append(result, t)
	}
	for _, t := range extra {
		result = append(result, t)
	}
	for _, t := range common {
		result = append(result, t)
	}
	return result
}

// Run starts the minister's processing loop.
func (m *ministerImpl) Run(ctx context.Context) {
	m.RunLoop(ctx, m, nil, m.MinisterBase.processTask)
}
