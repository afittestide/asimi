package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestSkillsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, ".agents", "skills")
	require.NoError(t, os.MkdirAll(skillsDir, 0755))
	return dir
}

func writeSkill(t *testing.T, skillsRoot, name, frontmatter, body string) {
	skillDir := filepath.Join(skillsRoot, ".agents", "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0755))
	content := "---\n" + frontmatter + "---\n" + body
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644))
}

func TestDiscover_NoSkillsDir(t *testing.T) {
	dir := t.TempDir()
	m := Discover(dir)
	assert.Nil(t, m)
}

func TestDiscover_EmptySkillsDir(t *testing.T) {
	dir := setupTestSkillsDir(t)
	m := Discover(dir)
	assert.NotNil(t, m)
	assert.Empty(t, m)
}

func TestDiscover_SingleSkillAllMinisters(t *testing.T) {
	dir := setupTestSkillsDir(t)
	writeSkill(t, dir, "go-testing",
		"name: go-testing\n"+
			"description: Patterns for writing Go tests\n",
		"# Go Testing\nInstructions here.\n")

	m := Discover(dir)
	require.NotNil(t, m)
	all, ok := m["*"]
	require.True(t, ok, "expected '*' key for all-ministers skills")
	require.Len(t, all, 1)
	assert.Equal(t, "go-testing", all[0].Name)
	assert.Equal(t, "Patterns for writing Go tests", all[0].Description)
	assert.Empty(t, all[0].Ministers)
}

func TestDiscover_SkillWithSpecificMinisters(t *testing.T) {
	dir := setupTestSkillsDir(t)
	writeSkill(t, dir, "deployment",
		"name: deployment\n"+
			"description: How to deploy this project\n"+
			"ministers: [forge, war]\n",
		"# Deployment\nDeploy steps.\n")

	m := Discover(dir)
	require.NotNil(t, m)

	forge, ok := m["forge"]
	require.True(t, ok)
	require.Len(t, forge, 1)
	assert.Equal(t, "deployment", forge[0].Name)

	war, ok := m["war"]
	require.True(t, ok)
	require.Len(t, war, 1)
	assert.Equal(t, "deployment", war[0].Name)

	_, ok = m["*"]
	assert.False(t, ok, "should not have '*' key when ministers are specified")
}

func TestDiscover_MixedScopedAndUnscoped(t *testing.T) {
	dir := setupTestSkillsDir(t)
	writeSkill(t, dir, "go-testing",
		"name: go-testing\n"+
			"description: Patterns for writing Go tests\n",
		"# Go Testing\n")
	writeSkill(t, dir, "deployment",
		"name: deployment\n"+
			"description: How to deploy\n"+
			"ministers: [forge]\n",
		"# Deployment\n")

	m := Discover(dir)
	require.NotNil(t, m)

	// go-testing has no ministers → appears in "*"
	all := m["*"]
	require.Len(t, all, 1)
	assert.Equal(t, "go-testing", all[0].Name)

	// deployment has ministers: [forge]
	forge := m["forge"]
	require.Len(t, forge, 1)
	assert.Equal(t, "deployment", forge[0].Name)

	// Other ministers only get the unscoped one via ForMinister
	secretary := ForMinister(m, "secretary")
	require.Len(t, secretary, 1)
	assert.Equal(t, "go-testing", secretary[0].Name)
}

func TestForMinister_CombinesAllAndSpecific(t *testing.T) {
	m := map[string][]SkillInfo{
		"*":         {{Name: "go-testing", Description: "Tests"}},
		"forge":     {{Name: "deployment", Description: "Deploy"}},
		"secretary": {{Name: "secretary-skill", Description: "Secrets"}},
	}

	// forge gets both
	forge := ForMinister(m, "forge")
	require.Len(t, forge, 2)
	assert.Equal(t, "go-testing", forge[0].Name)
	assert.Equal(t, "deployment", forge[1].Name)

	// chancellor only gets unscoped
	chancellor := ForMinister(m, "chancellor")
	require.Len(t, chancellor, 1)
	assert.Equal(t, "go-testing", chancellor[0].Name)
}

func TestForMinister_NilMap(t *testing.T) {
	assert.Nil(t, ForMinister(nil, "forge"))
}

func TestForMinister_Empty(t *testing.T) {
	m := map[string][]SkillInfo{}
	assert.Nil(t, ForMinister(m, "forge"))
}

func TestFormatIndex_NonEmpty(t *testing.T) {
	skills := []SkillInfo{
		{Name: "go-testing", Description: "Patterns for writing Go tests"},
		{Name: "deployment", Description: "How to deploy"},
	}
	idx := FormatIndex(skills)
	assert.Contains(t, idx, "go-testing")
	assert.Contains(t, idx, "Patterns for writing Go tests")
	assert.Contains(t, idx, "deployment")
	assert.Contains(t, idx, "How to deploy")
	assert.Contains(t, idx, "read the full SKILL.md")
}

func TestFormatIndex_Empty(t *testing.T) {
	assert.Equal(t, "", FormatIndex(nil))
	assert.Equal(t, "", FormatIndex([]SkillInfo{}))
}

func TestParseFrontmatter_Valid(t *testing.T) {
	data := []byte("---\nname: go-testing\ndescription: Patterns for writing Go tests\n---\n# Body\n")
	info, err := ParseFrontmatter(data)
	require.NoError(t, err)
	assert.Equal(t, "go-testing", info.Name)
	assert.Equal(t, "Patterns for writing Go tests", info.Description)
	assert.Empty(t, info.Ministers)
}

func TestParseFrontmatter_WithMinisters(t *testing.T) {
	data := []byte("---\nname: deployment\ndescription: How to deploy\nministers:\n  - forge\n  - war\n---\n# Body\n")
	info, err := ParseFrontmatter(data)
	require.NoError(t, err)
	assert.Equal(t, "deployment", info.Name)
	assert.Equal(t, []string{"forge", "war"}, info.Ministers)
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	_, err := ParseFrontmatter([]byte("# Just markdown\n"))
	assert.Error(t, err)
}

func TestParseFrontmatter_Unclosed(t *testing.T) {
	_, err := ParseFrontmatter([]byte("---\nname: test\n"))
	assert.Error(t, err)
}

func TestParseFrontmatter_Empty(t *testing.T) {
	info, err := ParseFrontmatter([]byte("---\n---\n"))
	require.NoError(t, err)
	assert.Empty(t, info.Name)
	assert.Empty(t, info.Description)
	assert.Empty(t, info.Ministers)
}

func TestParseFrontmatter_WindowsLineEndings(t *testing.T) {
	data := []byte("---\r\nname: go-testing\r\ndescription: Windows tests\r\n---\r\n# Body\r\n")
	info, err := ParseFrontmatter(data)
	require.NoError(t, err)
	assert.Equal(t, "go-testing", info.Name)
	assert.Equal(t, "Windows tests", info.Description)
}

func TestParseFrontmatter_InvalidYAML(t *testing.T) {
	data := []byte("---\nname: [unclosed list\n---\n# Body\n")
	_, err := ParseFrontmatter(data)
	assert.Error(t, err)
}

func TestDiscover_EmptyProjectRoot(t *testing.T) {
	m := Discover("")
	assert.Nil(t, m)
}

func TestDiscover_SkipsNonDirectoryEntries(t *testing.T) {
	dir := setupTestSkillsDir(t)
	// Create a file directly in the skills directory (not a directory)
	err := os.WriteFile(filepath.Join(dir, ".agents", "skills", "not-a-dir.md"), []byte("---\nname: test\n---\n"), 0644)
	require.NoError(t, err)

	m := Discover(dir)
	assert.NotNil(t, m)
	assert.Empty(t, m)
}

// TestShippedWriteRitualsSkill guards the acceptance criteria of edict 995: the
// write-rituals skill must ship in .agents/skills, parse under ParseFrontmatter,
// and be discoverable by (and only by) the ministers who author rituals. Unlike
// the fixture-based tests above, this asserts the real artifact — a later edit
// that breaks its frontmatter or widens its minister scope must fail here.
func TestShippedWriteRitualsSkill(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	skillPath := filepath.Join(root, ".agents", "skills", "write-rituals", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	require.NoError(t, err, "shipped write-rituals SKILL.md must exist")

	info, err := ParseFrontmatter(data)
	require.NoError(t, err, "shipped write-rituals frontmatter must parse")
	assert.Equal(t, "write-rituals", info.Name)
	assert.NotEmpty(t, info.Description, "description must render in the skills index")
	assert.Equal(t, []string{"secretary", "strategist"}, info.Ministers)

	m := Discover(root)
	require.NotNil(t, m)

	for _, want := range []string{"secretary", "strategist"} {
		names := skillNames(m[want])
		assert.Contains(t, names, "write-rituals", "write-rituals must be discoverable by %s", want)
	}
	for _, other := range []string{"forge", "judge", "chancellor"} {
		names := skillNames(m[other])
		assert.NotContains(t, names, "write-rituals", "write-rituals must not be discoverable by %s", other)
	}
}

func skillNames(skills []SkillInfo) []string {
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return names
}

func TestDiscover_SkipsDirWithoutSkillFile(t *testing.T) {
	dir := setupTestSkillsDir(t)
	// Create a directory but no SKILL.md inside it
	skillDir := filepath.Join(dir, ".agents", "skills", "empty-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0755))

	m := Discover(dir)
	assert.NotNil(t, m)
	assert.Empty(t, m)
}
