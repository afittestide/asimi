package court

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/ministers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMinisters_BuiltinHasAllExpected(t *testing.T) {
	defs, err := LoadMinisters()
	require.NoError(t, err)

	ids := make(map[string]bool)
	for _, d := range defs {
		ids[d.ID] = true
	}
	for _, expected := range []string{ministers.Secretary, ministers.Forge, ministers.Judge, ministers.Chancellor, ministers.War, ministers.Rites} {
		assert.True(t, ids[expected], "builtin ministers must include %s", expected)
	}
}

func TestLoadMinisters_FieldsPopulated(t *testing.T) {
	defs, err := LoadMinisters()
	require.NoError(t, err)

	for _, d := range defs {
		assert.NotEmpty(t, d.ID, "ID must not be empty")
		assert.NotEmpty(t, d.Role, "Role must not be empty for %s", d.ID)
		assert.Len(t, d.Permissions, 9, "Permissions must be 9 chars for %s", d.ID)

		// Length is not enough. ministerImpl.Tools() (ministers.go) drops every
		// tool when ParsePermissions fails, so a 9-char string with a malformed
		// triple (e.g. "rw-r--rw-" for the rites seat) yields a minister with no
		// tools and no test failure. Pin parseability so a bad stanza cannot
		// silently disarm a minister.
		_, err := tools.ParsePermissions(d.Permissions)
		assert.NoError(t, err, "Permissions must parse for %s", d.ID)
	}
}

// TestRitesMinisterDefinition pins the 禮部 seat added by edict 1017: it owns
// documentation (earth + intent Write), reads heaven, and — owning no tab and
// no seal — must stay out of the interactive tab order and the seal chain.
func TestRitesMinisterDefinition(t *testing.T) {
	defs, err := LoadMinisters()
	require.NoError(t, err)

	var def MinisterDef
	found := false
	for _, d := range defs {
		if d.ID == ministers.Rites {
			def, found = d, true
			break
		}
	}
	require.True(t, found, "rites must be a builtin minister")

	assert.Equal(t, "Ministry of Rites", def.Title)
	assert.Equal(t, "禮部", def.Kanji)
	assert.NotEmpty(t, def.Role)

	perm, err := tools.ParsePermissions(def.Permissions)
	require.NoError(t, err, "rites permissions %q must parse", def.Permissions)
	assert.Equal(t, tools.Permissions{
		Earth:  tools.Access{Read: true, Write: true},
		Heaven: tools.Access{Read: true},
		Intent: tools.Access{Read: true, Write: true},
	}, perm)

	assert.NotContains(t, ministers.DefaultTabIDs, ministers.Rites, "rites owns no tab")
	assert.NotContains(t, ministers.SealChainIDs, ministers.Rites, "rites owns no seal")
}

func TestNewMinister_KanjiInSystemPrompt(t *testing.T) {
	def := MinisterDef{ID: "test", Kanji: "工部", Role: "You are forge.", Permissions: "rwxrwxrwx"}
	base := NewMinisterBase(nil, nil, nil, "u", "p", nil)
	m := NewMinister(def, base)

	expected := "工部\n\nYou are forge."
	assert.Equal(t, expected, m.SystemPrompt())
}

func TestNewMinister_SystemPromptNoKanji(t *testing.T) {
	def := MinisterDef{ID: "test", Role: "You are a test minister.", Permissions: "rwxrwxrwx"}
	base := NewMinisterBase(nil, nil, nil, "u", "p", nil)
	m := NewMinister(def, base)

	assert.Equal(t, "You are a test minister.", m.SystemPrompt())
}

func TestLoadMinisters_KanjiPopulated(t *testing.T) {
	defs, err := LoadMinisters()
	require.NoError(t, err)

	// Build expected kanji from loaded defs instead of hard-coding
	for _, d := range defs {
		assert.NotEmpty(t, d.Kanji, "kanji must not be empty for %s", d.ID)
	}
}

func TestLoadMinisters_KanjiNotInRole(t *testing.T) {
	defs, err := LoadMinisters()
	require.NoError(t, err)
	require.NotEqual(t, 0, len(defs))
}

func TestNewMinister_ToolsNilWithoutRegistry(t *testing.T) {
	def := MinisterDef{ID: "test", Permissions: "rwxrwxrwx"}
	base := NewMinisterBase(nil, nil, nil, "u", "p", nil)
	m := NewMinister(def, base)

	assert.Nil(t, m.Tools())
}

func TestLoadAllMinisters_ProjectOverride(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, ".agents")
	require.NoError(t, os.Mkdir(agentsDir, 0755))

	override := `- id: forge
  role: "custom forge role"
  permissions: "rwxr---w-"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "ministers.yaml"), []byte(override), 0644))

	defs, err := LoadAllMinisters(dir)
	require.NoError(t, err)

	found := false
	for _, d := range defs {
		if d.ID == "forge" {
			assert.Equal(t, "custom forge role", d.Role, "project override should replace role")
			found = true
		}
	}
	assert.True(t, found, "forge should be present after override")
}
