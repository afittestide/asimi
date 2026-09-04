package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/afittestide/asimi/internal/courtapi"
	"github.com/afittestide/asimi/storage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/maximhq/bifrost/core/schemas"
)

// Model represents a unified model across all providers
type Model struct {
	ID          string // Model identifier (e.g., "claude-3-5-sonnet-latest", "gpt-4o")
	DisplayName string // Human-readable name
	Provider    string // Provider key (e.g., "anthropic", "openai", "googleai")
	Description string // Optional description
	Status      string // "active" (currently selected), "ready" (key found), "login_required", "manual_entry"
	OnSelect    tea.Cmd
}

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

// fetchAllModels aggregates models from all configured providers via bifrost's
// ListModelsRequest (per-provider). It maps schemas.Model to asimi's Model
// struct, preserving the active/ready/manual_entry status semantics.
func fetchAllModels(config *Config, court courtapi.Client) []Model {
	var allModels []Model
	currentProvider := config.LLM.Provider
	currentModel := config.LLM.Model
	ollamaAvailable := checkOllamaAvailable()

	// Build the set of configured providers to check auth for.
	configuredProviders := getConfiguredProviderKeys(ollamaAvailable)

	if len(configuredProviders) == 0 {
		return allModels
	}

	for _, provider := range configuredProviders {
		models, err := fetchModelsForProvider(config, court, provider)
		if err != nil {
			slog.Warn("failed to fetch models for provider", "provider", provider, "error", err)
			allModels = append(allModels, Model{
				DisplayName: "Enter model name for " + providerDisplayName(provider),
				Provider:    provider,
				Status:      "manual_entry",
				Description: err.Error(),
			})
			continue
		}
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
				ID:          m.ID,
				DisplayName: displayName,
				Provider:    provider,
				Description: m.Description,
				Status:      status,
			})
		}
	}

	sortModels(allModels)
	return allModels
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
		models = append(models, Model{
			ID:          m.ID,
			DisplayName: displayName,
			Description: description,
		})
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

// sortModels sorts models: active first, then ready, then manual_entry, by provider and display name.
func sortModels(models []Model) {
	sort.Slice(models, func(i, j int) bool {
		statusPriority := map[string]int{"active": 0, "ready": 1, "manual_entry": 2, "error": 3}
		if statusPriority[models[i].Status] != statusPriority[models[j].Status] {
			return statusPriority[models[i].Status] < statusPriority[models[j].Status]
		}
		if models[i].Provider != models[j].Provider {
			return models[i].Provider < models[j].Provider
		}
		return models[i].DisplayName < models[j].DisplayName
	})
}

// ModelsWindow is a component for displaying unified model selection across all providers
type ModelsWindow struct {
	SelectWindow[Model]
	currentModel string

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
func (m *ModelsWindow) SetModels(models []Model, currentModel string) {
	m.SetItems(models)
	m.currentModel = currentModel
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
	case "manual_entry":
		return "✏️"
	default:
		return ""
	}
}

// IsModelSelectable returns whether a model can be selected
func IsModelSelectable(model Model) bool {
	return model.Status != "error"
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
			if m.HasSearch() {
				return titleStyle.Render(fmt.Sprintf("Select a model [%3d/%3d]  search %q [%d/%d]:", selectedIndex+1, totalItems, m.searchPattern, m.CurrentMatchNumber(), m.MatchCount()))
			}
			return titleStyle.Render(fmt.Sprintf("Select a model [%3d/%3d]:", selectedIndex+1, totalItems))
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
			if model.Status == "error" {
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
			sb.WriteString(style.Render(line) + "\n")
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
	models []Model
}

type modelsLoadErrorMsg struct {
	error string
}

type showModelSelectionMsg struct{}

// Command handler - now works with all providers
func handleModelsCommand(model *TUIModel, args []string) tea.Cmd {
	showModelsCmd := model.tabs.Content().ShowUnifiedModels([]Model{}, model.config.LLM.Model)
	model.tabs.Content().models.SetLoading(true)

	loadCmd := func() tea.Msg {
		models := fetchAllModels(model.config, model.court)

		// Set OnSelect for manual_entry entries
		for i := range models {
			m := &models[i]
			if m.Status == "manual_entry" {
				provider := m.Provider
				m.OnSelect = func() tea.Msg { return enterModelNameMsg{provider: provider} }
			}
		}

		return modelsLoadedMsg{models: models}
	}

	return tea.Batch(showModelsCmd, loadCmd)
}
