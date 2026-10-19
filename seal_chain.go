package main

import (
	"fmt"
	"strings"

	"github.com/afittestide/asimi/internal/ministers"
	"github.com/afittestide/asimi/storage"
	"github.com/charmbracelet/lipgloss"
)

// renderSealChain displays the seal chain status for an edict.
// Titles are derived from the built-in minister defs; IDs use well-known
// constants from the ministers package.
func renderSealChain(seals []storage.Seal, w int) string {
	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(globalTheme.ChatBorder)
	labelStyle := lipgloss.NewStyle().Foreground(globalTheme.DimTextColor)
	var b strings.Builder

	// Load builtin defs for title lookups
	defs, _ := ministers.LoadMinisters()
	defsByID := ministers.LookupMap(defs)

	// Build set of granted seals, skipping invalidated tombstones.
	granted := make(map[string]bool)
	for _, seal := range seals {
		if seal.StaleAt != nil {
			continue
		}
		granted[seal.MinisterID] = true
	}

	// Render each seal in the chain order: Judge → Chancellor → Ruler
	for i, ministerID := range ministers.SealChainIDs {
		if i > 0 {
			b.WriteString(" ")
		}

		title := defsByID[ministerID].Title
		if title == "" {
			// Minister not in builtin defs (e.g. "ruler"); capitalize the ID
			title = strings.ToUpper(ministerID[:1]) + ministerID[1:]
		}
		if granted[ministerID] {
			b.WriteString(activeStyle.Render(fmt.Sprintf("[✓ %s]", title)))
		} else {
			b.WriteString(labelStyle.Render(fmt.Sprintf("[○ %s]", title)))
		}
	}

	return lipgloss.NewStyle().Width(w).Render(b.String())
}
