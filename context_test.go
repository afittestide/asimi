package main

import (
	"strings"
	"testing"
)

func TestCalculateBarSegments(t *testing.T) {
	tests := []struct {
		name       string
		percentage float64
		full       int
		partial    bool
	}{
		{"zero", 0, 0, false},
		{"small remainder", 5, 0, true},
		{"two segments", 20, 2, false},
		{"over capacity", 130, contextBarWidth, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			full, partial := calculateBarSegments(tc.percentage)
			if full != tc.full || partial != tc.partial {
				t.Fatalf("expected (%d,%t) got (%d,%t)", tc.full, tc.partial, full, partial)
			}
		})
	}
}

func TestRenderContextBar(t *testing.T) {
	info := ContextInfo{
		TotalTokens:       100,
		UsedTokens:        55,
		AutocompactBuffer: 20,
		FreeTokens:        25,
	}

	bar := renderContextBar(info)
	expected := "⛁ ⛁ ⛁ ⛁ ⛁ ⛀ ⛶ ⛶ ⛶ ⛝"
	if bar != expected {
		t.Fatalf("expected bar %q got %q", expected, bar)
	}

	if strings.Count(bar, " ")+1 != contextBarWidth {
		t.Fatalf("expected %d segments got %d", contextBarWidth, strings.Count(bar, " ")+1)
	}
}

func TestRenderContextInfoIncludesSections(t *testing.T) {
	info := ContextInfo{
		Model:              "claude-3-5-sonnet-latest",
		TotalTokens:        200_000,
		UsedTokens:         40_000,
		SystemPromptTokens: 2_000,
		SystemToolsTokens:  10_000,
		MemoryFilesTokens:  500,
		MessagesTokens:     1_500,
		FreeTokens:         95_000,
		AutocompactBuffer:  45_000,
	}

	output := renderContextInfo(info)

	expectedSnippets := []string{
		"Context Usage",
		"claude-3-5-sonnet-latest",
		"System prompt",
		"System tools",
		"Memory files",
		"Messages",
		"Free space",
		"↓",
	}

	for _, snippet := range expectedSnippets {
		if !strings.Contains(output, snippet) {
			t.Fatalf("expected output to contain %q\n%s", snippet, output)
		}
	}

	// Should NOT contain the old "Autocompact buffer" line
	if strings.Contains(output, "Autocompact buffer") {
		t.Fatalf("output should not contain 'Autocompact buffer' line\n%s", output)
	}
}

// e908: a registry estimate must render with a leading "~"; a
// provider-reported window must not.
func TestRenderContextInfo_ApproxWindowPrefix(t *testing.T) {
	exact := ContextInfo{Model: "m", TotalTokens: 128_000, UsedTokens: 1_000, FreeTokens: 127_000}
	out := renderContextInfo(exact)
	if strings.Contains(out, "~128.0k") {
		t.Fatalf("exact window must not be prefixed with '~'\n%s", out)
	}
	if !strings.Contains(out, "128.0k") {
		t.Fatalf("expected exact window label 128.0k\n%s", out)
	}

	approx := ContextInfo{Model: "m", TotalTokens: 128_000, TotalTokensApprox: true, UsedTokens: 1_000, FreeTokens: 127_000}
	out = renderContextInfo(approx)
	if !strings.Contains(out, "~128.0k") {
		t.Fatalf("approximate window must render with '~'\n%s", out)
	}
}

func TestHandleContextCommand(t *testing.T) {
	t.Run("no session", func(t *testing.T) {
		model := &TUIModel{}
		cmd := handleContextCommand(model, nil)
		msg := cmd()
		contextMsg, ok := msg.(showContextMsg)
		if !ok {
			t.Fatalf("expected showContextMsg got %T", msg)
		}
		if !strings.Contains(contextMsg.content, "No active session") {
			t.Fatalf("unexpected content: %s", contextMsg.content)
		}
	})
}
