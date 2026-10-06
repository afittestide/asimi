// Package main implements the /context command for displaying context usage information.

package main

import (
	"fmt"
	"math"
	"strings"
)

const (
	contextBarWidth          = 10
	autocompactBufferRatio   = 0.225
	memoryFileOverheadTokens = 20
	defaultUnknownContextRef = 8192
)

// ContextInfo holds information about context usage.
type ContextInfo struct {
	Model              string
	TotalTokens        int
	TotalTokensApprox  bool // TotalTokens is a registry estimate, not provider-reported
	UsedTokens         int
	SystemPromptTokens int
	SystemToolsTokens  int
	MemoryFilesTokens  int
	MessagesTokens     int
	FreeTokens         int
	AutocompactBuffer  int
}

// renderContextInfo renders the context information as a formatted string.
func renderContextInfo(info ContextInfo) string {
	var b strings.Builder
	total := info.TotalTokens
	if total <= 0 {
		total = info.UsedTokens + info.AutocompactBuffer + info.FreeTokens
	}
	if total <= 0 {
		total = 1
	}

	usedPercent := percentage(clampInt(info.UsedTokens, 0, total), total)
	systemPromptPercent := percentage(info.SystemPromptTokens, total)
	systemToolsPercent := percentage(info.SystemToolsTokens, total)
	memoryFilesPercent := percentage(info.MemoryFilesTokens, total)
	messagesPercent := percentage(info.MessagesTokens, total)

	// An estimate must never be shown as exact: prefix the window with "~"
	// when it came from the registry rather than the provider.
	totalLabel := formatTokenCount(info.TotalTokens)
	if info.TotalTokensApprox && info.TotalTokens > 0 {
		totalLabel = "~" + totalLabel
	}

	b.WriteString("  ⎿  Context Usage\n")
	b.WriteString(fmt.Sprintf("     %s   %s · %s/%s tokens (%.1f%%)\n",
		renderContextBar(info),
		info.Model,
		formatTokenCount(info.UsedTokens),
		totalLabel,
		usedPercent,
	))

	b.WriteString(formatContextLine("System prompt", info.SystemPromptTokens, total, systemPromptPercent))
	b.WriteString(formatContextLine("System tools", info.SystemToolsTokens, total, systemToolsPercent))
	b.WriteString(formatContextLine("Memory files", info.MemoryFilesTokens, total, memoryFilesPercent))
	b.WriteString(formatContextLine("Messages", info.MessagesTokens, total, messagesPercent))
	b.WriteString(formatFreeSpaceLine(info, total))

	return b.String()
}

// renderContextBar creates a visual bar representation of context usage.
func renderContextBar(info ContextInfo) string {
	total := info.TotalTokens
	if total <= 0 {
		total = info.UsedTokens + info.AutocompactBuffer + info.FreeTokens
	}
	if total <= 0 {
		total = 1
	}

	usedTokens := clampInt(info.UsedTokens, 0, total)
	bufferTokens := clampInt(info.AutocompactBuffer, 0, total-usedTokens)
	freeTokens := total - usedTokens - bufferTokens
	if freeTokens < 0 {
		freeTokens = 0
	}

	segments := make([]string, 0, contextBarWidth)
	remaining := contextBarWidth

	addSegments := func(tokens int, fill, partial string) {
		if remaining == 0 || tokens <= 0 {
			return
		}
		percentage := float64(tokens) / float64(total) * 100
		fullSegments, partialSegment := calculateBarSegments(percentage)
		if fullSegments > remaining {
			fullSegments = remaining
			partialSegment = false
		}
		for i := 0; i < fullSegments && remaining > 0; i++ {
			segments = append(segments, fill)
			remaining--
		}
		if partialSegment && remaining > 0 {
			if partial == "" {
				partial = fill
			}
			segments = append(segments, partial)
			remaining--
		}
	}

	addSegments(usedTokens, "⛁", "⛀")
	addSegments(freeTokens, "⛶", "")
	addSegments(bufferTokens, "⛝", "")

	for len(segments) < contextBarWidth {
		segments = append(segments, "⛶")
	}

	return strings.Join(segments, " ")
}

const (
	contextCategorySymbol        = "⛁"
	contextCategoryPartialSymbol = "⛀"
	contextFreeSymbol            = "⛶"
)

// formatContextLine builds a formatted line for a specific category.
func formatContextLine(label string, tokens, total int, percent float64) string {
	bar := renderCategoryBar(tokens, total)
	return fmt.Sprintf("     %s   %s %s: %s tokens (%.1f%%)\n",
		bar,
		contextCategorySymbol,
		label,
		formatTokenCount(tokens),
		percent,
	)
}

// formatFreeSpaceLine builds a formatted line for free space with low water mark indicator.
func formatFreeSpaceLine(info ContextInfo, total int) string {
	bar := renderFreeSpaceBar(info, total)
	totalFreeSpace := info.FreeTokens + info.AutocompactBuffer
	return fmt.Sprintf("     %s   ⛶ Free space: %s tokens (%.1f%%)\n",
		bar,
		formatTokenCount(totalFreeSpace),
		percentage(totalFreeSpace, total),
	)
}

// renderCategoryBar returns a bar showing the share of a category.
func renderCategoryBar(tokens, total int) string {
	percentage := 0.0
	if total > 0 {
		percentage = float64(tokens) / float64(total) * 100
	}
	fullSegments, partialSegment := calculateBarSegments(percentage)

	segments := make([]string, 0, contextBarWidth)
	for i := 0; i < fullSegments && len(segments) < contextBarWidth; i++ {
		segments = append(segments, contextCategorySymbol)
	}
	if partialSegment && len(segments) < contextBarWidth {
		segments = append(segments, contextCategoryPartialSymbol)
	}
	for len(segments) < contextBarWidth {
		segments = append(segments, contextFreeSymbol)
	}
	return strings.Join(segments, " ")
}

// renderFreeSpaceBar returns a bar showing free space with low water mark indicator.
func renderFreeSpaceBar(info ContextInfo, total int) string {
	// Calculate percentages for positioning
	freePercentage := 0.0
	bufferPercentage := 0.0
	if total > 0 {
		freePercentage = float64(info.FreeTokens) / float64(total) * 100
		bufferPercentage = float64(info.AutocompactBuffer) / float64(total) * 100
	}

	// Calculate segments
	freeSegments, freePartial := calculateBarSegments(freePercentage)
	bufferSegments, _ := calculateBarSegments(bufferPercentage)

	segments := make([]string, 0, contextBarWidth)

	// Add free space segments (above low water mark)
	for i := 0; i < freeSegments && len(segments) < contextBarWidth; i++ {
		segments = append(segments, "⛶")
	}
	if freePartial && len(segments) < contextBarWidth {
		segments = append(segments, "⛶")
	}

	// Add the low water mark arrow
	if len(segments) < contextBarWidth {
		segments = append(segments, "↓")
	}

	// Add buffer space segments (below low water mark)
	for i := 0; i < bufferSegments && len(segments) < contextBarWidth; i++ {
		segments = append(segments, "⛶")
	}

	// Fill remaining with empty space
	for len(segments) < contextBarWidth {
		segments = append(segments, "⛶")
	}

	return strings.Join(segments, " ")
}

// calculateBarSegments converts a percentage into bar segments (full segments and a flag for a partial segment).
func calculateBarSegments(percentage float64) (int, bool) {
	if percentage <= 0 {
		return 0, false
	}
	fullSegments := int(percentage / 10)
	if fullSegments >= contextBarWidth {
		return contextBarWidth, false
	}
	remainder := percentage - float64(fullSegments*10)
	return fullSegments, remainder > 0
}

// formatTokenCount formats a token count with appropriate units.
func formatTokenCount(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

func percentage(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round((float64(part)/float64(total))*1000) / 10
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
