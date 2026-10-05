package main

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/afittestide/asimi/internal/courtapi"
	"github.com/afittestide/asimi/storage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/maximhq/bifrost/core/schemas"
)

// Model represents a unified model across all providers
type Model struct {
	ID              string // Model identifier (e.g., "claude-3-5-sonnet-latest", "gpt-4o")
	DisplayName     string // Human-readable name
	Provider        string // Provider key (e.g., "anthropic", "openai", "googleai")
	Description     string // Optional description
	Status          string // "active" (currently selected), "ready" (key found), "login_required", "manual_entry", "diagnostic"
	ContextLength   int    // Max context window in tokens, from bifrost
	ContextApprox   bool   // ContextLength came from the local registry, not the provider
	Modality        string // e.g. "text->text", from bifrost Architecture
	PricePrompt     string // Prompt price per token, from bifrost Pricing
	PriceCompletion string // Completion price per token, from bifrost Pricing
	OnSelect        tea.Cmd
}

// modelsCache caches the last successful catalog per provider for the
// lifetime of the process (the TUI is a single process). It is deliberately
// unbounded — one entry per configured provider — and has no TTL: a warm cache
// is the whole point of :model opening instantly.
type modelsCache struct {
	mu          sync.Mutex
	byProvider  map[string][]Model // successfully fetched models, per provider
	diagnostics []string           // last diagnostics
	fetched     bool               // has a successful population happened
}

// modelCatalog is the package-level session cache for the model picker.
var modelCatalog = modelsCache{byProvider: map[string][]Model{}}

// getOllamaBaseURL returns the Ollama API base URL
func getOllamaBaseURL() string {
	if envURL := os.Getenv("OLLAMA_HOST"); envURL != "" {
		return strings.TrimSuffix(envURL, "/")
	}
	return "http://localhost:11434"
}

// checkOllamaAvailable checks if Ollama is running and accessible
func checkOllamaAvailable() bool {
	baseURL := getOllamaBaseURL()
	client := &http.Client{
		Timeout: 2 * time.Second,
	}

	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// read returns the cached, flattened catalog when a successful population has
// happened. It re-derives statuses from config so a changed active model is
// reflected without refetching. Never touches the network.
func (c *modelsCache) read(config *Config) ([]Model, []string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.fetched {
		return nil, nil, false
	}
	return buildModelList(config, c.byProvider), append([]string(nil), c.diagnostics...), true
}

// merge replaces the per-provider entries that succeeded this round, keeps the
// prior entries for providers that failed (a transient error must not empty the
// list), prunes providers no longer configured, and records the new diagnostics.
// It returns the merged, flattened catalog so the caller can render it.
func (c *modelsCache) merge(config *Config, fetched map[string][]Model, diagnostics, configuredProviders []string) ([]Model, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	kept := make(map[string]bool, len(configuredProviders))
	for _, p := range configuredProviders {
		kept[p] = true
	}
	for p := range c.byProvider {
		if !kept[p] {
			delete(c.byProvider, p)
		}
	}
	for provider, models := range fetched {
		c.byProvider[provider] = models
	}

	c.diagnostics = append([]string(nil), diagnostics...)
	if len(c.byProvider) > 0 {
		c.fetched = true
	}
	return buildModelList(config, c.byProvider), append([]string(nil), c.diagnostics...)
}

// populated reports whether a successful population has happened.
func (c *modelsCache) populated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetched
}

// buildModelList flattens per-provider models into a sorted catalog, deriving
// the active/ready status from config, and appends the pinned manual-entry row
// as the final item.
func buildModelList(config *Config, byProvider map[string][]Model) []Model {
	currentProvider := config.LLM.Provider
	currentModel := config.LLM.Model

	var allModels []Model
	for provider, models := range byProvider {
		for _, m := range models {
			status := "ready"
			if currentProvider == provider && m.ID == currentModel {
				status = "active"
			}
			displayName := m.DisplayName
			if displayName == "" {
				displayName = m.ID
			}
			allModels = append(allModels, Model{
				ID:              m.ID,
				DisplayName:     displayName,
				Provider:        provider,
				Description:     m.Description,
				Status:          status,
				ContextLength:   m.ContextLength,
				ContextApprox:   m.ContextApprox,
				Modality:        m.Modality,
				PricePrompt:     m.PricePrompt,
				PriceCompletion: m.PriceCompletion,
			})
		}
	}

	sortModels(allModels, currentProvider)

	// Pinned manual-entry escape hatch, always last and the only manual_entry row.
	allModels = append(allModels, Model{
		DisplayName: "Type a model name…",
		Status:      "manual_entry",
		OnSelect:    func() tea.Msg { return enterModelNameRawMsg{} },
	})

	return allModels
}

// maxConcurrentModelFetches bounds the number of in-flight provider fetches so
// a ruler with many keys does not open dozens of sockets at once.
const maxConcurrentModelFetches = 8

// fetchAllModels aggregates models from all configured providers via bifrost's
// ListModelsRequest (per-provider). It maps schemas.Model to asimi's Model
// struct, preserving the active/ready status semantics, and returns a list of
// diagnostics for providers that could not be reached.
//
// When the session cache is warm and forceRefresh is false, it returns the
// cached catalog immediately without any network call. On a miss (or a forced
// refresh) it fetches every provider concurrently, then repopulates the cache.
func fetchAllModels(config *Config, court courtapi.Client, forceRefresh bool) ([]Model, []string) {
	if !forceRefresh {
		if models, diagnostics, ok := modelCatalog.read(config); ok {
			return models, diagnostics
		}
	}

	ollamaAvailable := checkOllamaAvailable()
	configuredProviders := getConfiguredProviderKeys(ollamaAvailable)

	byProvider, diagnostics := fetchConfiguredProviders(config, court, configuredProviders)
	return modelCatalog.merge(config, byProvider, diagnostics, configuredProviders)
}

// fetchConfiguredProviders fans out one bounded goroutine per configured
// provider, collecting successful models per provider and sorted diagnostics.
func fetchConfiguredProviders(config *Config, court courtapi.Client, providers []string) (map[string][]Model, []string) {
	type fetchResult struct {
		provider string
		models   []Model
		err      error
	}

	results := make(chan fetchResult, len(providers))
	sem := make(chan struct{}, maxConcurrentModelFetches)
	var wg sync.WaitGroup

	for _, provider := range providers {
		wg.Add(1)
		go func(provider string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			models, err := fetchModelsForProvider(config, court, provider)
			results <- fetchResult{provider: provider, models: models, err: err}
		}(provider)
	}

	wg.Wait()
	close(results)

	byProvider := make(map[string][]Model, len(providers))
	var diagnostics []string
	for r := range results {
		if r.err != nil {
			slog.Warn("failed to fetch models for provider", "provider", r.provider, "error", r.err)
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %v", r.provider, r.err))
			continue
		}
		byProvider[r.provider] = r.models
	}

	sort.Strings(diagnostics)
	return byProvider, diagnostics
}

// fetchModelsForProvider fetches models from a single provider via bifrost
// through the court's ListModels method. This is a variable so tests can
// override it.
var fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
	if court == nil {
		return nil, fmt.Errorf("no court available")
	}
	resp, err := court.ListModels(provider)
	if err != nil {
		return nil, err
	}

	var models []Model
	for _, m := range resp.Data {
		displayName := m.ID
		if m.Name != nil && *m.Name != "" {
			displayName = *m.Name
		}
		description := ""
		if m.Description != nil {
			description = *m.Description
		}
		model := Model{
			ID:          m.ID,
			DisplayName: displayName,
			Description: description,
		}
		if m.ContextLength != nil {
			model.ContextLength = *m.ContextLength
		}
		if model.ContextLength == 0 {
			if approx := court.ApproximateContextSize(provider, m.ID); approx > 0 {
				model.ContextLength = approx
				model.ContextApprox = true
			}
		}
		if m.Architecture != nil && m.Architecture.Modality != nil {
			model.Modality = *m.Architecture.Modality
		}
		if m.Pricing != nil {
			if m.Pricing.Prompt != nil {
				model.PricePrompt = *m.Pricing.Prompt
			}
			if m.Pricing.Completion != nil {
				model.PriceCompletion = *m.Pricing.Completion
			}
		}
		models = append(models, model)
	}
	return models, nil
}

// getConfiguredProviderKeys returns the list of provider keys (asimi naming)
// that have credentials configured or are locally available.
func getConfiguredProviderKeys(ollamaAvailable bool) []string {
	var providers []string

	// Check all standard providers for credentials.
	// Skip ollama here — it's keyless so checkProviderAuth always returns
	// true; it's handled by the local availability check below.
	for _, sp := range schemas.StandardProviders {
		providerKey := bifrostProviderToAsimi(string(sp))
		if providerKey == "ollama" {
			continue
		}
		if checkProviderAuth(providerKey) {
			providers = append(providers, providerKey)
		}
	}

	// Ollama is checked via local availability, not credentials
	if ollamaAvailable {
		providers = append(providers, "ollama")
	}

	return providers
}

// bifrostProviderToAsimi maps a bifrost provider string to asimi's provider key.
// Most are identical; "gemini" maps to "googleai" for backward compatibility.
func bifrostProviderToAsimi(bifrostProvider string) string {
	switch bifrostProvider {
	case "gemini":
		return "googleai"
	default:
		return bifrostProvider
	}
}

// asimiProviderToBifrost maps an asimi provider key to bifrost's provider string.
func asimiProviderToBifrost(asimiProvider string) string {
	switch asimiProvider {
	case "googleai":
		return "gemini"
	default:
		return asimiProvider
	}
}

// formatContextLength renders a token count as a compact human string (e.g. 262K).
func formatContextLength(tokens int) string {
	switch {
	case tokens >= 1000000:
		return fmt.Sprintf("%dM", tokens/1000000)
	case tokens >= 1000:
		return fmt.Sprintf("%dK", int(math.Round(float64(tokens)/1000)))
	default:
		return strconv.Itoa(tokens)
	}
}

// modelMetadataSuffix renders the dim metadata suffix for a model row,
// e.g. " · 262K ctx · $3/$15". Empty when no metadata exists. Modality is
// deliberately not rendered.
func modelMetadataSuffix(m Model) string {
	var parts []string
	if m.ContextLength > 0 {
		prefix := ""
		if m.ContextApprox {
			prefix = "~"
		}
		parts = append(parts, prefix+formatContextLength(m.ContextLength)+" ctx")
	}
	if m.PricePrompt != "" || m.PriceCompletion != "" {
		parts = append(parts, "$"+m.PricePrompt+"/$"+m.PriceCompletion)
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

// truncateToWidth truncates a string to the given display width. Widths <= 0
// yield an empty string. The metadata suffix is ASCII, so rune slicing matches
// display width here.
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	if len(runes) > width {
		runes = runes[:width]
	}
	return string(runes)
}

// sortModels orders rows with the selected provider first, the remaining
// providers alphabetically, then alphabetically by display name within each
// group. The active model keeps its alphabetical position — it is marked with
// ✓ but never hoisted.
func sortModels(models []Model, selectedProvider string) {
	sort.SliceStable(models, func(i, j int) bool {
		pi, pj := models[i].Provider, models[j].Provider
		si, sj := pi == selectedProvider, pj == selectedProvider
		if si != sj {
			return si // selected provider's group first
		}
		if pi != pj {
			return pi < pj // remaining providers alphabetically
		}
		nameI, nameJ := models[i].DisplayName, models[j].DisplayName
		if nameI == "" {
			nameI = models[i].ID
		}
		if nameJ == "" {
			nameJ = models[j].ID
		}
		return nameI < nameJ // within a provider: alphabetical by name
	})
}

// ModelsWindow is a component for displaying unified model selection across all providers
type ModelsWindow struct {
	SelectWindow[Model]
	currentModel string
	diagnostics  []string // per-provider fetch failures, shown dimly under the title

	// Search state
	searchPattern   string // current search pattern (empty = no active search)
	searchDirection int    // 1 = forward (/), -1 = backward (?)
	matchIndices    []int  // indices into Items that match the pattern
	matchCursor     int    // current position in matchIndices
}

// NewModelsWindow creates a new models window
func NewModelsWindow() ModelsWindow {
	sw := NewSelectWindow[Model]()
	sw.Height = 15
	sw.SetSize(70, 15)
	sw.SetSelectable(IsModelSelectable)

	return ModelsWindow{
		SelectWindow: sw,
		currentModel: "",
	}
}

// SetModels updates the models list (unified Model type)
func (m *ModelsWindow) SetModels(models []Model, currentModel string, diagnostics []string) {
	m.SetItems(models)
	m.currentModel = currentModel
	m.diagnostics = diagnostics
}

// SetError sets error state
func (m *ModelsWindow) SetError(err string) {
	if err == "" {
		m.SelectWindow.SetError(nil)
	} else {
		m.SelectWindow.SetError(fmt.Errorf("%s", err))
	}
}

// GetInitialSelection returns the index of the current model (or first selectable)
func (m *ModelsWindow) GetInitialSelection() int {
	for i, model := range m.Items {
		if model.Status == "active" {
			return i
		}
	}
	return m.FirstSelectableIndex(IsModelSelectable)
}

// GetSelectedModel returns the model at the given index
func (m *ModelsWindow) GetSelectedModel(index int) *Model {
	return m.GetSelectedItem(index)
}

// Search computes match indices for the given pattern and returns the index to jump to.
// direction: 1 = forward (/), -1 = backward (?)
// currentItem: the currently selected item index
// Returns -1 if no matches found.
func (m *ModelsWindow) Search(pattern string, direction int, currentItem int) int {
	if pattern == "" {
		m.searchPattern = ""
		m.searchDirection = 0
		m.matchIndices = nil
		m.matchCursor = 0
		return currentItem
	}

	m.searchPattern = pattern
	m.searchDirection = direction
	lowerPattern := strings.ToLower(pattern)
	m.matchIndices = m.matchIndices[:0]

	for i, model := range m.Items {
		if strings.Contains(strings.ToLower(model.ID), lowerPattern) ||
			strings.Contains(strings.ToLower(model.DisplayName), lowerPattern) {
			m.matchIndices = append(m.matchIndices, i)
		}
	}

	if len(m.matchIndices) == 0 {
		m.matchCursor = 0
		return -1
	}

	// Find the first match in the desired direction, with wrap-around
	if direction > 0 {
		// Forward: first match at or after currentItem+1, wrap around
		for _, idx := range m.matchIndices {
			if idx > currentItem {
				m.matchCursor = findMatchCursor(m.matchIndices, idx)
				return idx
			}
		}
		// Wrap around to first match
		m.matchCursor = 0
		return m.matchIndices[0]
	}

	// Backward: first match at or before currentItem-1, wrap around
	for i := len(m.matchIndices) - 1; i >= 0; i-- {
		if m.matchIndices[i] < currentItem {
			m.matchCursor = i
			return m.matchIndices[i]
		}
	}
	// Wrap around to last match
	m.matchCursor = len(m.matchIndices) - 1
	return m.matchIndices[len(m.matchIndices)-1]
}

// NextMatch moves through existing matchIndices and returns the next/previous match.
// direction: 1 = next match in search direction, -1 = opposite direction
// Returns -1 if no matches exist.
func (m *ModelsWindow) NextMatch(currentItem int, direction int) int {
	if len(m.matchIndices) == 0 {
		return -1
	}

	// Find the current cursor position based on currentItem
	m.matchCursor = findMatchCursor(m.matchIndices, currentItem)

	if direction > 0 {
		m.matchCursor++
		if m.matchCursor >= len(m.matchIndices) {
			m.matchCursor = 0
		}
	} else {
		m.matchCursor--
		if m.matchCursor < 0 {
			m.matchCursor = len(m.matchIndices) - 1
		}
	}

	return m.matchIndices[m.matchCursor]
}

// findMatchCursor returns the position of idx in matchIndices, or the nearest
// lower position if idx is not in the list.
func findMatchCursor(matchIndices []int, idx int) int {
	for i, mi := range matchIndices {
		if mi == idx {
			return i
		}
	}
	// Not found — find the largest index < idx
	result := 0
	for i, mi := range matchIndices {
		if mi < idx {
			result = i
		} else {
			break
		}
	}
	return result
}

// MatchCount returns the number of matches from the last search
func (m *ModelsWindow) MatchCount() int {
	return len(m.matchIndices)
}

// CurrentMatchNumber returns the 1-based position of matchCursor in matchIndices
func (m *ModelsWindow) CurrentMatchNumber() int {
	if len(m.matchIndices) == 0 {
		return 0
	}
	return m.matchCursor + 1
}

// HasSearch returns true if there's an active search pattern
func (m *ModelsWindow) HasSearch() bool {
	return m.searchPattern != ""
}

// ClearSearch resets search state
func (m *ModelsWindow) ClearSearch() {
	m.searchPattern = ""
	m.searchDirection = 0
	m.matchIndices = nil
	m.matchCursor = 0
}

// getStatusIcon returns an icon for the status
func getStatusIcon(status string) string {
	switch status {
	case "active":
		return "✓"
	case "ready":
		return ""
	case "error":
		return "⚠"
	case "diagnostic":
		return "⚠"
	case "manual_entry":
		return "✏️"
	default:
		return ""
	}
}

// IsModelSelectable returns whether a model can be selected
func IsModelSelectable(model Model) bool {
	return model.Status != "error" && model.Status != "diagnostic"
}

// RenderList renders the models list with the given selection
func (m *ModelsWindow) RenderList(selectedIndex, scrollOffset, visibleSlots int) string {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(globalTheme.PromptBorder).
		Background(globalTheme.PaneBackground).
		Padding(0, 1)

	isFirst := true
	lastProvider := ""

	config := RenderConfig[Model]{
		ConstructTitle: func(selectedIndex, totalItems int) string {
			var title string
			if m.HasSearch() {
				title = titleStyle.Render(fmt.Sprintf("Select a model [%3d/%3d]  search %q [%d/%d]:", selectedIndex+1, totalItems, m.searchPattern, m.CurrentMatchNumber(), m.MatchCount()))
			} else {
				title = titleStyle.Render(fmt.Sprintf("Select a model [%3d/%3d]:", selectedIndex+1, totalItems))
			}
			if len(m.diagnostics) > 0 {
				dim := lipgloss.NewStyle().Foreground(globalTheme.DimTextColor)
				hint := "unavailable: " + strings.Join(m.diagnostics, ", ")
				title += "\n" + dim.Render(hint)
			}
			return title
		},
		OnLoading: func(sb *strings.Builder) {
			sb.WriteString("Loading models...\n")
			sb.WriteString("\n")
			sb.WriteString("⏳ Scanning available models across all providers...\n")
		},
		OnError: func(sb *strings.Builder, err error) {
			sb.WriteString("Error loading models:\n")
			sb.WriteString("\n")
			sb.WriteString(err.Error() + "\n")
		},
		OnEmpty: func(sb *strings.Builder) {
			sb.WriteString("No models available\n")
			sb.WriteString("\n")
			sb.WriteString("Use :login to authenticate with a provider\n")
		},
		RenderItem: func(i int, model Model, isSelected bool, sb *strings.Builder) {
			if !isFirst && model.Provider != lastProvider {
				sb.WriteString("\n")
			}
			isFirst = false
			lastProvider = model.Provider

			prefix := "  "
			if model.Status == "error" || model.Status == "diagnostic" {
				providerIcon := getProviderIcon(model.Provider)
				statusIcon := getStatusIcon(model.Status)
				style := lipgloss.NewStyle().Foreground(globalTheme.Error)
				line := fmt.Sprintf("%s%s %s %s", prefix, providerIcon, statusIcon, model.Description)
				sb.WriteString(style.Render(line) + "\n")
				return
			}

			if isSelected {
				prefix = "▶ "
			}

			providerIcon := getProviderIcon(model.Provider)
			statusIcon := getStatusIcon(model.Status)

			displayText := model.DisplayName
			if displayText == "" {
				displayText = model.ID
			}

			style := lipgloss.NewStyle()
			if isSelected {
				style = style.Foreground(globalTheme.SuccessColor).Bold(true)
			} else if model.Status == "login_required" || model.Status == "manual_entry" {
				style = style.Foreground(globalTheme.DimTextColor)
			}

			line := fmt.Sprintf("%s%s %s", prefix, providerIcon, displayText)
			if statusIcon != "" {
				line = fmt.Sprintf("%s%s %s %s", prefix, providerIcon, statusIcon, displayText)
			}
			rendered := style.Render(line)
			if suffix := modelMetadataSuffix(model); suffix != "" {
				remaining := m.Width - lipgloss.Width(line)
				if remaining > 0 {
					dim := lipgloss.NewStyle().Foreground(globalTheme.DimTextColor)
					rendered += dim.Render(truncateToWidth(suffix, remaining))
				}
			}
			sb.WriteString(rendered + "\n")
		},
		IsSelectable: func(model Model) bool {
			return IsModelSelectable(model)
		},
	}

	return m.Render(selectedIndex, scrollOffset, config)
}

// Message types for model selection
type modelSelectedMsg struct {
	model    *Model
	onSelect tea.Cmd
}

// Message types for seal selection
type edictSelectedMsg struct {
	edictID uint
}

type edictsLoadedMsg struct {
	edicts []storage.ActiveEdict
}

// edictIntentUpdatedMsg is emitted when an edict intent edit completes;
// the handler shows a confirmation message and reloads the edicts list.
type edictIntentUpdatedMsg struct {
	edictID uint
	message string
}

// reloadEdictsMsg is emitted by ContentComponent when the user presses Esc
// in the edict dashboard to return to the edicts list.
type reloadEdictsMsg struct{}

// EdictSelectWindow displays a selectable list of active edicts
type EdictSelectWindow struct {
	SelectWindow[storage.ActiveEdict]
}

// NewEdictSelectWindow creates a new edict selection window
func NewEdictSelectWindow() EdictSelectWindow {
	sw := NewSelectWindow[storage.ActiveEdict]()
	sw.Height = 15
	sw.SetSize(70, 15)
	return EdictSelectWindow{SelectWindow: sw}
}

// RenderList renders the edict selection list
func (s *EdictSelectWindow) RenderList(selectedIndex, scrollOffset, visibleSlots int) string {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(globalTheme.PromptBorder).
		Background(globalTheme.PaneBackground).
		Padding(0, 1)

	config := RenderConfig[storage.ActiveEdict]{
		ConstructTitle: func(selectedIndex, totalItems int) string {
			return titleStyle.Render(fmt.Sprintf("Select edict [%3d/%3d]:", selectedIndex+1, totalItems))
		},
		OnEmpty: func(sb *strings.Builder) {
			sb.WriteString("No active edicts\n")
		},
		RenderItem: func(i int, edict storage.ActiveEdict, isSelected bool, sb *strings.Builder) {
			prefix := "  "
			if isSelected {
				prefix = "▶ "
			}

			judge := "  "
			if edict.HasJudgeSeal {
				judge = "👩‍⚖️"
			}

			chancellor := "  "
			if edict.HasChancellorSeal {
				chancellor = "☯️"
			}

			linePrefix := fmt.Sprintf("%s[%3d] %s %s ", prefix, edict.ID, judge, chancellor)
			intentWidth := s.Width - lipgloss.Width(linePrefix)
			if intentWidth < 0 {
				intentWidth = 0
			}

			intent := lipgloss.NewStyle().Inline(true).MaxWidth(intentWidth).Render(" " + edict.Intent)

			style := lipgloss.NewStyle().Inline(true).MaxWidth(s.Width)
			if isSelected {
				style = style.Foreground(globalTheme.SuccessColor).Bold(true)
			}
			sb.WriteString(style.Render(linePrefix+intent) + "\n")
		},
	}

	return s.Render(selectedIndex, scrollOffset, config)
}

// Message types for model loading
type modelsLoadedMsg struct {
	models      []Model
	Diagnostics []string
}

type modelsLoadErrorMsg struct {
	error string
}

type showModelSelectionMsg struct{}

// knownProvider reports whether the provider key is a standard bifrost
// provider (in asimi naming) or is known to the provider metadata table.
func knownProvider(provider string) bool {
	for _, sp := range schemas.StandardProviders {
		if bifrostProviderToAsimi(string(sp)) == provider {
			return true
		}
	}
	_, ok := providerMeta[provider]
	return ok
}

// parseModelArg resolves command args into a provider and model name.
// It accepts "<provider>/<name>" (single arg), "<provider> <name>" (two args),
// or "<name>" (single arg without a slash, using the current provider).
func parseModelArg(args []string, currentProvider string) (provider, name string, err error) {
	switch len(args) {
	case 0:
		return "", "", fmt.Errorf("no model specified")
	case 1:
		if i := strings.Index(args[0], "/"); i >= 0 {
			return args[0][:i], args[0][i+1:], nil
		}
		return currentProvider, args[0], nil
	default:
		return args[0], args[1], nil
	}
}

// selectModelDirect resolves a direct :model selection and emits a
// modelSelectedMsg so the existing handler performs SaveConfig and switchModel.
func selectModelDirect(model *TUIModel, args []string) tea.Cmd {
	provider, name, err := parseModelArg(args, model.config.LLM.Provider)
	if err != nil {
		return func() tea.Msg { return showToast(err.Error(), "error", 3*time.Second) }
	}
	if !knownProvider(provider) {
		return func() tea.Msg {
			return showToast(fmt.Sprintf("Unknown provider %q — use :models to pick one", provider), "error", 3*time.Second)
		}
	}
	return func() tea.Msg {
		return modelSelectedMsg{model: &Model{ID: name, DisplayName: name, Provider: provider, Status: "active"}}
	}
}

// Command handler - now works with all providers
func handleModelsCommand(model *TUIModel, args []string) tea.Cmd {
	// Refresh form: :model refresh / :models refresh forces a live refetch.
	forceRefresh := len(args) == 1 && args[0] == "refresh"
	if forceRefresh {
		args = nil
	}

	// Direct form: :model <provider>/<name>, :model <provider> <name>, or :model <name>
	if len(args) > 0 {
		return selectModelDirect(model, args)
	}

	// A warm cache renders immediately; only a cold cache needs the spinner.
	warm := !forceRefresh && modelCatalog.populated()

	showModelsCmd := model.tabs.Content().ShowUnifiedModels([]Model{}, model.config.LLM.Model, nil)
	if !warm {
		model.tabs.Content().models.SetLoading(true)
	}

	loadCmd := func() tea.Msg {
		models, diagnostics := fetchAllModels(model.config, model.court, forceRefresh)
		return modelsLoadedMsg{models: models, Diagnostics: diagnostics}
	}

	return tea.Batch(showModelsCmd, loadCmd)
}
