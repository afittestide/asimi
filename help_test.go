package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelpTopicFromEmbed(t *testing.T) {
	hw := NewHelpWindow()

	topics := []string{
		"index", "modes", "commands", "navigation", "editing",
		"files", "sessions", "context", "models", "login", "config", "quickref",
		"tutorial",
	}

	for _, topic := range topics {
		t.Run(topic, func(t *testing.T) {
			content := hw.getHelpTopic(topic)
			if content == "" {
				t.Fatalf("getHelpTopic(%q) returned empty content", topic)
			}
			if !strings.HasPrefix(content, "# ") {
				t.Errorf("getHelpTopic(%q) should start with '# ' header, got: %q", topic, content[:min(50, len(content))])
			}
		})
	}
}

func TestHelpTopicEmpty(t *testing.T) {
	hw := NewHelpWindow()
	content := hw.getHelpTopic("")
	if content == "" {
		t.Fatal("getHelpTopic(\"\") should return index content")
	}
	if !strings.HasPrefix(content, "# Asimi Help Index") {
		t.Errorf("getHelpTopic(\"\") should return index content starting with '# Asimi Help Index'")
	}
}

func TestHelpTopicNotFound(t *testing.T) {
	hw := NewHelpWindow()
	content := hw.getHelpTopic("nonexistent")
	if !strings.Contains(content, "Help Topic Not Found") {
		t.Errorf("getHelpTopic(\"nonexistent\") should return 'not found' message")
	}
}

func TestHelpSetGetTopic(t *testing.T) {
	hw := NewHelpWindow()

	if hw.GetTopic() != "index" {
		t.Errorf("default topic should be 'index', got %q", hw.GetTopic())
	}

	hw.SetTopic("modes")
	if hw.GetTopic() != "modes" {
		t.Errorf("topic should be 'modes', got %q", hw.GetTopic())
	}

	hw.SetTopic("")
	if hw.GetTopic() != "index" {
		t.Errorf("empty topic should default to 'index', got %q", hw.GetTopic())
	}
}

func TestHelpRenderContent(t *testing.T) {
	hw := NewHelpWindow()
	hw.SetTopic("modes")

	rendered := hw.RenderContent()
	if rendered == "" {
		t.Fatal("RenderContent() returned empty string")
	}
}

func TestHelpRenderContentAllTopics(t *testing.T) {
	topics := []string{
		"index", "modes", "commands", "navigation", "editing",
		"files", "sessions", "context", "models", "login", "config", "quickref",
		"tutorial",
	}
	for _, topic := range topics {
		t.Run(topic, func(t *testing.T) {
			hw := NewHelpWindow()
			hw.SetTopic(topic)
			rendered := hw.RenderContent()
			if rendered == "" {
				t.Fatalf("RenderContent() returned empty for topic %q", topic)
			}
			// Glamour renders headers, tables, bold, blockquotes, code blocks.
			// In a TTY context, ** markers are converted to ANSI bold;
			// in a no-TTY context (like CI), the ASCII style keeps them.
			// We only verify non-empty output and no crashes here.
		})
	}
}

func TestHelpRenderContentWordWrap(t *testing.T) {
	hw := NewHelpWindow()
	hw.SetSize(40, 20)
	hw.SetTopic("index")

	rendered := hw.RenderContent()
	if rendered == "" {
		t.Fatal("RenderContent() returned empty string")
	}

	// No line should exceed the wrap width (40 - 2 for padding = 38)
	// We allow some tolerance for ANSI codes by stripping them
	for _, line := range strings.Split(rendered, "\n") {
		stripped := stripANSI(line)
		if len(stripped) > 40 {
			t.Errorf("line length %d exceeds width 40: %q", len(stripped), stripped[:min(40, len(stripped))])
		}
	}
}

// TestDocsTreeLivesUnderIntent pins the docs relocation performed by edict 1017.
// The docs tree moved from ./docs/ to ./intent/docs/ (depth 1 -> 2), which broke
// every relative link inside it. Nothing else in the suite reads the docs tree,
// so a stray re-introduction of ./docs/ or a stale `docs/x.md` link would ship
// unnoticed — the exact failure mode the edict exists to prevent.
func TestDocsTreeLivesUnderIntent(t *testing.T) {
	// 1. The tree moved: ./docs/ is gone, intent/docs/ holds the files.
	if _, err := os.Stat("docs"); !os.IsNotExist(err) {
		t.Errorf("./docs/ must not exist after the move to intent/docs/ (stat err: %v)", err)
	}
	entries, err := os.ReadDir("intent/docs")
	require.NoError(t, err, "intent/docs/ must exist")
	var mdCount int
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			mdCount++
		}
	}
	// 13 files: 12 relocated in e1017 plus sandbox-seams.md, added on main at
	// e1036 and relocated here by the same move.
	assert.Equal(t, 13, mdCount, "intent/docs/ must hold the relocated markdown files")

	// 2. The blocking reference: help/tutorial.md is embedded via help.go
	// (//go:embed help/*.md), so a dead link there is user-visible help text.
	tutorial, err := os.ReadFile("help/tutorial.md")
	require.NoError(t, err)
	assert.Contains(t, string(tutorial), "intent/docs/court_guide.md",
		"help/tutorial.md must point at the relocated court guide")
	assert.FileExists(t, "intent/docs/court_guide.md")

	// 3. Every live link surface must reference the new location. Historical
	// records (CHANGELOG, intent/letters) and synthetic test fixtures are
	// deliberately excluded — they are not live pointers.
	surfaces := []string{
		"README.md",
		"help/tutorial.md",
		"Justfile",
		".agents/rituals.yaml",
		".agents/skills/write-rituals/SKILL.md",
		"security/vuln-allowlist.txt",
		".gitignore",
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			surfaces = append(surfaces, filepath.Join("intent/docs", e.Name()))
		}
	}

	for _, path := range surfaces {
		data, err := os.ReadFile(path)
		require.NoError(t, err, "link surface %s must exist", path)
		for i, line := range strings.Split(string(data), "\n") {
			for rest := line; ; {
				idx := strings.Index(rest, "docs/")
				if idx < 0 {
					break
				}
				before := rest[:idx]
				// Accept the new location, and external URLs (e.g. podman.io/docs/).
				ok := strings.HasSuffix(before, "intent/") || strings.Contains(before, "://")
				if !ok {
					t.Errorf("%s:%d references the old docs path: %q", path, i+1, strings.TrimSpace(line))
				}
				rest = rest[idx+len("docs/"):]
			}
		}
	}
}
