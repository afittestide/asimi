package ministers

import (
	"testing"

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
	for _, expected := range []string{Secretary, Forge, Judge, Chancellor, War, Rites} {
		assert.True(t, ids[expected], "builtin ministers must include %s", expected)
	}
}

func TestMinisterDef_Label(t *testing.T) {
	tests := []struct {
		name string
		def  MinisterDef
		want string
	}{
		{"both kanji and title", MinisterDef{Kanji: "中書令", Title: "Secretary"}, "Secretary"},
		{"only title", MinisterDef{Title: "Secretary"}, "Secretary"},
		{"only kanji", MinisterDef{Kanji: "中書令"}, ""},
		{"neither", MinisterDef{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.def.Label())
		})
	}
}

func TestLookupByID(t *testing.T) {
	defs := []MinisterDef{
		{ID: "secretary", Title: "Secretary"},
		{ID: "chancellor", Title: "Chancellor"},
	}

	d := LookupByID(defs, "chancellor")
	assert.Equal(t, "Chancellor", d.Title)

	// Not found — returns zero-value with just ID set
	d = LookupByID(defs, "ruler")
	assert.Equal(t, "ruler", d.ID)
	assert.Empty(t, d.Title)
}

func TestLookupMap(t *testing.T) {
	defs := []MinisterDef{
		{ID: "secretary", Title: "Secretary"},
		{ID: "chancellor", Title: "Chancellor"},
	}

	m := LookupMap(defs)
	assert.Equal(t, "Secretary", m["secretary"].Title)
	assert.Equal(t, "Chancellor", m["chancellor"].Title)
	assert.Len(t, m, 2)
}

func TestDefaultTabIDs(t *testing.T) {
	assert.Equal(t, []string{Forge, Secretary, Judge, Chancellor}, DefaultTabIDs)
}

func TestSealChainIDs(t *testing.T) {
	assert.Equal(t, []string{Judge, Chancellor, Ruler}, SealChainIDs)
}

func TestLoadMinisters_Rites(t *testing.T) {
	defs, err := LoadMinisters()
	require.NoError(t, err)

	byID := LookupMap(defs)
	rites, ok := byID[Rites]
	require.True(t, ok, "rites must be present in builtin ministers")

	assert.Equal(t, "Ministry of Rites", rites.Title)
	assert.Equal(t, "rw-r--rw-", rites.Permissions)
	assert.Empty(t, rites.ExtraTools, "rites relies on commonTools for ask_ruler")

	// Documentation mandate
	assert.Contains(t, rites.Role, "documentation")

	// Foreign-envoy / MCP classification mandate and rules
	assert.Contains(t, rites.Role, "MCP")
	assert.Contains(t, rites.Role, "foreign envoy")
	assert.Contains(t, rites.Role, "effect")
	assert.Contains(t, rites.Role, "propose")
	assert.Contains(t, rites.Role, "mcp_classification")
	assert.Contains(t, rites.Role, "---------")
	assert.Contains(t, rites.Role, "ask_ruler")
	assert.Contains(t, rites.Role, "ToolRegistry")
	assert.Contains(t, rites.Role, "unsealed")

	// Tabless and outside the seal chain
	assert.NotContains(t, DefaultTabIDs, Rites)
	assert.NotContains(t, SealChainIDs, Rites)
}
