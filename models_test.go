package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/courtapi"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetchAllModels_ReturnsEmptyWithoutAuth verifies that fetchAllModels returns
// an empty list when no providers are authenticated
func TestFetchAllModels_ReturnsEmptyWithoutAuth(t *testing.T) {
	// Clear any existing credentials
	DeleteAPIKeyFromKeyring("anthropic")
	DeleteAPIKeyFromKeyring("openai")
	DeleteAPIKeyFromKeyring("googleai")
	DeleteAPIKeyFromKeyring("openrouter")

	// Clear environment variables for this test
	originalAnthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	originalOpenAIKey := os.Getenv("OPENAI_API_KEY")
	originalGeminiKey := os.Getenv("GEMINI_API_KEY")
	originalGoogleKey := os.Getenv("GOOGLE_API_KEY")
	originalOllamaHost := os.Getenv("OLLAMA_HOST")
	originalOpenRouterKey := os.Getenv("OPENROUTER_API_KEY")

	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("GEMINI_API_KEY")
	os.Unsetenv("GOOGLE_API_KEY")
	os.Unsetenv("OPENROUTER_API_KEY")

	// Create a mock Ollama server that returns empty model list
	mockOllamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"models":[]}`))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockOllamaServer.Close()

	// Point OLLAMA_HOST to our mock server
	os.Setenv("OLLAMA_HOST", mockOllamaServer.URL)

	defer func() {
		if originalAnthropicKey != "" {
			os.Setenv("ANTHROPIC_API_KEY", originalAnthropicKey)
		}
		if originalOpenAIKey != "" {
			os.Setenv("OPENAI_API_KEY", originalOpenAIKey)
		}
		if originalGeminiKey != "" {
			os.Setenv("GEMINI_API_KEY", originalGeminiKey)
		}
		if originalGoogleKey != "" {
			os.Setenv("GOOGLE_API_KEY", originalGoogleKey)
		}
		if originalOllamaHost != "" {
			os.Setenv("OLLAMA_HOST", originalOllamaHost)
		} else {
			os.Unsetenv("OLLAMA_HOST")
		}
		if originalOpenRouterKey != "" {
			os.Setenv("OPENROUTER_API_KEY", originalOpenRouterKey)
		}
	}()

	// Override fetch to return empty for Ollama (keyless, no court in test)
	origFetch := fetchModelsForProvider
	defer func() { fetchModelsForProvider = origFetch }()
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		return nil, nil // no models, no error
	}

	config := &Config{
		LLM: LLMConfig{
			Provider: "anthropic",
			Model:    "claude-3-5-sonnet-latest",
		},
	}

	models, _ := fetchAllModels(config, nil, true)

	// With no auth and empty Ollama, the only entry is the pinned manual-entry row.
	require.Len(t, models, 1, "Expected only the pinned manual_entry row when no auth and empty Ollama")
	assert.Equal(t, "manual_entry", models[0].Status)
	assert.Equal(t, "Type a model name…", models[0].DisplayName)
}

// TestCheckProviderAuth verifies provider authentication detection
func TestCheckProviderAuth(t *testing.T) {
	// Clear credentials
	DeleteAPIKeyFromKeyring("anthropic")

	// Clear environment
	originalKey := os.Getenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_API_KEY")
	defer func() {
		if originalKey != "" {
			os.Setenv("ANTHROPIC_API_KEY", originalKey)
		}
	}()

	// Test with no credentials
	info := checkProviderAuth("anthropic")
	assert.False(t, info, "Expected false when no credentials are set")

	// Test with environment variable
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	info = checkProviderAuth("anthropic")
	assert.True(t, info, "Expected true when ANTHROPIC_API_KEY is set")
}

func TestCheckProviderAuth_OpenRouter(t *testing.T) {
	// Clear keyring
	DeleteAPIKeyFromKeyring("openrouter")

	// Clear environment
	originalKey := os.Getenv("OPENROUTER_API_KEY")
	os.Unsetenv("OPENROUTER_API_KEY")
	defer func() {
		if originalKey != "" {
			os.Setenv("OPENROUTER_API_KEY", originalKey)
		}
	}()

	// Test with no credentials
	assert.False(t, checkProviderAuth("openrouter"), "Expected false when no credentials are set")

	// Test with environment variable
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	assert.True(t, checkProviderAuth("openrouter"), "Expected true when OPENROUTER_API_KEY is set")
}

// TestCheckProviderAuth_Convention verifies convention-based env var resolution
// for providers not explicitly listed in the old switch statements.
func TestCheckProviderAuth_Convention(t *testing.T) {
	// Clear keyring
	DeleteAPIKeyFromKeyring("cohere")

	originalKey := os.Getenv("COHERE_API_KEY")
	os.Unsetenv("COHERE_API_KEY")
	defer func() {
		if originalKey != "" {
			os.Setenv("COHERE_API_KEY", originalKey)
		}
	}()

	assert.False(t, checkProviderAuth("cohere"), "Expected false when no credentials")
	t.Setenv("COHERE_API_KEY", "test-key")
	assert.True(t, checkProviderAuth("cohere"), "Expected true when COHERE_API_KEY is set")
}

// TestCheckProviderAuth_Keyless verifies that keyless providers (Ollama) are always authed
func TestCheckProviderAuth_Keyless(t *testing.T) {
	assert.True(t, checkProviderAuth("ollama"), "Expected true for keyless provider ollama")
}

// TestFetchAllModels_ManualEntryOnError verifies that when a provider's API returns
// an error, a manual_entry entry is produced (not an error entry).
func TestFetchAllModels_ManualEntryOnError(t *testing.T) {
	DeleteAPIKeyFromKeyring("openrouter")

	originalKey := os.Getenv("OPENROUTER_API_KEY")
	originalBaseURL := os.Getenv("OPENROUTER_BASE_URL")
	t.Setenv("OPENROUTER_API_KEY", "bad-key")
	defer func() {
		if originalKey != "" {
			os.Setenv("OPENROUTER_API_KEY", originalKey)
		} else {
			os.Unsetenv("OPENROUTER_API_KEY")
		}
		if originalBaseURL != "" {
			os.Setenv("OPENROUTER_BASE_URL", originalBaseURL)
		} else {
			os.Unsetenv("OPENROUTER_BASE_URL")
		}
	}()

	// Clear other providers
	DeleteAPIKeyFromKeyring("anthropic")
	DeleteAPIKeyFromKeyring("openai")
	DeleteAPIKeyFromKeyring("googleai")
	originalAnthropic := os.Getenv("ANTHROPIC_API_KEY")
	originalOpenAI := os.Getenv("OPENAI_API_KEY")
	originalGemini := os.Getenv("GEMINI_API_KEY")
	originalGoogle := os.Getenv("GOOGLE_API_KEY")
	originalOllamaHost := os.Getenv("OLLAMA_HOST")
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("GEMINI_API_KEY")
	os.Unsetenv("GOOGLE_API_KEY")

	// Mock Ollama to return empty
	mockOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[]}`))
	}))
	defer mockOllama.Close()
	os.Setenv("OLLAMA_HOST", mockOllama.URL)

	defer func() {
		if originalAnthropic != "" {
			os.Setenv("ANTHROPIC_API_KEY", originalAnthropic)
		}
		if originalOpenAI != "" {
			os.Setenv("OPENAI_API_KEY", originalOpenAI)
		}
		if originalGemini != "" {
			os.Setenv("GEMINI_API_KEY", originalGemini)
		}
		if originalGoogle != "" {
			os.Setenv("GOOGLE_API_KEY", originalGoogle)
		}
		if originalOllamaHost != "" {
			os.Setenv("OLLAMA_HOST", originalOllamaHost)
		} else {
			os.Unsetenv("OLLAMA_HOST")
		}
	}()

	// Override the fetch function to simulate an error from bifrost
	origFetch := fetchModelsForProvider
	defer func() { fetchModelsForProvider = origFetch }()
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		if provider == "openrouter" {
			return nil, fmt.Errorf("API returned status 401")
		}
		return nil, nil
	}

	config := &Config{
		LLM: LLMConfig{
			Provider: "openrouter",
			Model:    "test",
		},
	}

	models, diagnostics := fetchAllModels(config, nil, true)

	// The failed provider must NOT become a manual_entry typing prompt.
	var manualEntry *Model
	for i := range models {
		if models[i].Provider == "openrouter" {
			manualEntry = &models[i]
			break
		}
	}
	assert.Nil(t, manualEntry, "Expected NO manual_entry entry for the failed openrouter provider")

	// Instead a diagnostic is produced.
	require.Len(t, diagnostics, 1, "Expected one diagnostic for the failed provider")
	assert.Contains(t, diagnostics[0], "openrouter")
	assert.Contains(t, diagnostics[0], "401")
}

// TestFetchAllModels_PinnedManualEntryRowLast verifies the picker always
// appends exactly one "Type a model name…" row, and that it is the only
// manual_entry entry and the final item.
func TestFetchAllModels_PinnedManualEntryRowLast(t *testing.T) {
	clearProviderAuthKeys()

	originalOllamaHost := os.Getenv("OLLAMA_HOST")
	os.Unsetenv("OLLAMA_HOST")
	defer func() {
		if originalOllamaHost != "" {
			os.Setenv("OLLAMA_HOST", originalOllamaHost)
		}
	}()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	origFetch := fetchModelsForProvider
	defer func() { fetchModelsForProvider = origFetch }()
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		if provider == "anthropic" {
			return []Model{{ID: "claude-3-5-sonnet", DisplayName: "Claude 3.5 Sonnet"}}, nil
		}
		return nil, nil
	}

	models, _ := fetchAllModels(&Config{LLM: LLMConfig{Provider: "anthropic", Model: "claude-3-5-sonnet"}}, nil, true)
	require.NotEmpty(t, models)

	last := models[len(models)-1]
	assert.Equal(t, "manual_entry", last.Status, "last row should be the pinned manual_entry row")
	assert.Equal(t, "Type a model name…", last.DisplayName)
	assert.Empty(t, last.Provider, "pinned row has no provider binding")
	assert.NotNil(t, last.OnSelect, "pinned row should carry OnSelect")

	manualCount := 0
	for _, m := range models {
		if m.Status == "manual_entry" {
			manualCount++
		}
	}
	assert.Equal(t, 1, manualCount, "the pinned row must be the only manual_entry entry")
}

// TestIsModelSelectable_ManualEntry verifies that manual_entry models are selectable
func TestIsModelSelectable_ManualEntry(t *testing.T) {
	model := Model{Status: "manual_entry", Provider: "openai"}
	assert.True(t, IsModelSelectable(model), "Expected manual_entry to be selectable")

	// error should still not be selectable
	errorModel := Model{Status: "error", Provider: "openai"}
	assert.False(t, IsModelSelectable(errorModel), "Expected error to NOT be selectable")

	// diagnostic rows are never selectable
	diagModel := Model{Status: "diagnostic", Provider: "openai"}
	assert.False(t, IsModelSelectable(diagModel), "Expected diagnostic to NOT be selectable")
}

// Tests from models_window_test.go

func TestNewModelsWindowDefaults(t *testing.T) {
	window := NewModelsWindow()

	assert.Equal(t, 70, window.Width)
	assert.Equal(t, 15, window.Height)
	assert.False(t, window.Loading)
	assert.Empty(t, window.Items)
	assert.Nil(t, window.Error)
	assert.Equal(t, "", window.currentModel)
}

func TestModelsWindowSetSizeAdjustsVisibleSlots(t *testing.T) {
	window := NewModelsWindow()

	window.SetSize(80, 10)
	assert.Equal(t, 80, window.Width)
	assert.Equal(t, 10, window.Height)
	assert.Equal(t, 9, window.MaxVisible)

	window.SetSize(50, 2)
	assert.Equal(t, 2, window.Height)
	assert.Equal(t, 1, window.MaxVisible)
}

func TestModelsWindowSetModels(t *testing.T) {
	window := NewModelsWindow()
	models := []Model{
		{ID: "m1", DisplayName: "Model 1", Provider: "anthropic", Status: "ready"},
		{ID: "m2", DisplayName: "Model 2", Provider: "openai", Status: "active"},
	}

	window.SetModels(models, "m2", nil)

	assert.False(t, window.Loading)
	assert.Nil(t, window.Error)
	assert.Equal(t, 2, window.GetItemCount())
	assert.Equal(t, "m2", window.currentModel)
	assert.Equal(t, "Model 1", window.Items[0].DisplayName)
	assert.Equal(t, "Model 2", window.Items[1].DisplayName)
}

func TestModelsWindowSetLoading(t *testing.T) {
	window := NewModelsWindow()
	window.SetError("test error")

	window.SetLoading(true)
	assert.True(t, window.Loading)
	assert.Nil(t, window.Error)
}

func TestModelsWindowSetError(t *testing.T) {
	window := NewModelsWindow()

	window.SetError("something went wrong")
	assert.False(t, window.Loading)
	assert.NotNil(t, window.Error)
	assert.Equal(t, "something went wrong", window.Error.Error())

	window.SetError("")
	assert.Nil(t, window.Error)
}

func TestModelsWindowGetInitialSelection(t *testing.T) {
	window := NewModelsWindow()
	models := []Model{
		{ID: "m1", DisplayName: "Model 1", Provider: "anthropic", Status: "ready"},
		{ID: "m2", DisplayName: "Model 2", Provider: "openai", Status: "active"},
		{ID: "m3", DisplayName: "Model 3", Provider: "googleai", Status: "ready"},
	}
	window.SetModels(models, "m2", nil)

	assert.Equal(t, 1, window.GetInitialSelection())

	window.SetModels([]Model{
		{ID: "m1", DisplayName: "Model 1", Provider: "anthropic", Status: "ready"},
	}, "m_nonexistent", nil)
	assert.Equal(t, 0, window.GetInitialSelection())

	window.SetModels([]Model{}, "", nil)
	assert.Equal(t, 0, window.GetInitialSelection())
}

func TestModelsWindowGetSelectedModel(t *testing.T) {
	window := NewModelsWindow()
	models := []Model{
		{ID: "m1", DisplayName: "Model 1", Provider: "anthropic", Status: "ready"},
		{ID: "m2", DisplayName: "Model 2", Provider: "openai", Status: "active"},
	}
	window.SetModels(models, "m2", nil)

	model := window.GetSelectedModel(1)
	assert.NotNil(t, model)
	assert.Equal(t, "m2", model.ID)

	assert.Nil(t, window.GetSelectedModel(-1))
	assert.Nil(t, window.GetSelectedModel(2))
}

func TestModelsWindowRenderList(t *testing.T) {
	window := NewModelsWindow()
	window.SetSize(80, 10)

	// Test Loading State
	window.SetLoading(true)
	render := window.RenderList(0, 0, window.GetVisibleSlots())
	assert.Contains(t, render, "Loading models...")
	assert.Contains(t, render, "Scanning available models across all providers...")
	assert.NotContains(t, render, "Error loading models:")

	// Test Error State
	window.SetLoading(false)
	window.SetError("network failed")
	render = window.RenderList(0, 0, window.GetVisibleSlots())
	assert.Contains(t, render, "Error loading models:")
	assert.Contains(t, render, "network failed")
	assert.NotContains(t, render, "Loading models...")

	// Test Empty State
	window.SetError("")
	window.SetModels([]Model{}, "", nil)
	render = window.RenderList(0, 0, window.GetVisibleSlots())
	assert.Contains(t, render, "No models available")
	assert.Contains(t, render, "Use :login to authenticate with a provider")

	// Test Normal Rendering with Active/Ready and Grouping
	models := []Model{
		{ID: "claude-3-5-sonnet-latest", DisplayName: "Claude 3.5 Sonnet", Provider: "anthropic", Status: "active"},
		{ID: "claude-3-5-haiku-latest", DisplayName: "Claude 3.5 Haiku", Provider: "anthropic", Status: "ready"},
		{ID: "gpt-4o", DisplayName: "GPT-4o", Provider: "openai", Status: "ready"},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini", Provider: "openai", Status: "ready"},
		{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Provider: "googleai", Status: "ready"},
		{ID: "anthropic/claude-3.5-sonnet", DisplayName: "anthropic/claude-3.5-sonnet", Provider: "openrouter", Status: "ready"},
	}
	window.SetModels(models, "claude-3-5-sonnet-latest", nil)

	render = window.RenderList(0, 0, window.GetVisibleSlots())
	lines := strings.Split(render, "\n")
	assert.True(t, strings.HasPrefix(lines[1], "▶ 🅰️  ✓ Claude 3.5 Sonnet"))
	assert.True(t, strings.HasPrefix(lines[2], "  🅰️  Claude 3.5 Haiku"))
	assert.Equal(t, "", lines[3]) // Blank line between provider groups
	assert.True(t, strings.HasPrefix(lines[4], "  🤖 GPT-4o"))
	assert.True(t, strings.HasPrefix(lines[5], "  🤖 GPT-4o Mini"))
	assert.Equal(t, "", lines[6]) // Blank line between provider groups
	assert.True(t, strings.HasPrefix(lines[7], "  🔷 Gemini 2.5 Pro"))
	assert.Equal(t, "", lines[8]) // Blank line between provider groups
	assert.True(t, strings.HasPrefix(lines[9], "  🔀 anthropic/claude-3.5-sonnet"))

	// Test Selection
	render = window.RenderList(2, 0, window.GetVisibleSlots())
	lines = strings.Split(render, "\n")
	assert.True(t, strings.HasPrefix(lines[4], "▶ 🤖 GPT-4o"))
}

func TestGetProviderIcon(t *testing.T) {
	assert.Equal(t, "🅰️ ", getProviderIcon("anthropic"))
	assert.Equal(t, "🤖", getProviderIcon("openai"))
	assert.Equal(t, "🔷", getProviderIcon("googleai"))
	assert.Equal(t, "🦙", getProviderIcon("ollama"))
	assert.Equal(t, "🔀", getProviderIcon("openrouter"))
	assert.Equal(t, "  ", getProviderIcon("unknown"))
}

func TestGetStatusIcon(t *testing.T) {
	assert.Equal(t, "✓", getStatusIcon("active"))
	assert.Equal(t, "", getStatusIcon("ready"))
	assert.Equal(t, "⚠", getStatusIcon("error"))
	assert.Equal(t, "⚠", getStatusIcon("diagnostic"))
	assert.Equal(t, "✏️", getStatusIcon("manual_entry"))
	assert.Equal(t, "", getStatusIcon("unknown"))
}

func TestModelsWindowRenderList_ScrollingAndGrouping(t *testing.T) {
	window := NewModelsWindow()
	window.SetSize(80, 5)

	models := []Model{
		{ID: "claude-sonnet", DisplayName: "Claude Sonnet", Provider: "anthropic", Status: "ready"},
		{ID: "claude-haiku", DisplayName: "Claude Haiku", Provider: "anthropic", Status: "ready"},
		{ID: "gpt-4", DisplayName: "GPT-4", Provider: "openai", Status: "ready"},
		{ID: "gpt-3.5", DisplayName: "GPT-3.5", Provider: "openai", Status: "ready"},
		{ID: "gemini-pro", DisplayName: "Gemini Pro", Provider: "googleai", Status: "ready"},
	}
	window.SetModels(models, "", nil)

	render := window.RenderList(2, 1, window.GetVisibleSlots())
	lines := strings.Split(render, "\n")

	assert.Contains(t, lines[0], "Select a model")
	assert.Contains(t, lines[1], "  🅰️  Claude Haiku")
	assert.Equal(t, "", lines[2])
	assert.Contains(t, lines[3], "▶ 🤖 GPT-4")
	assert.Contains(t, lines[4], "  🤖 GPT-3.5")
}

// TestParseModelArg verifies direct-form parsing for :model.
func TestParseModelArg(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		currentProvider string
		wantProvider    string
		wantName        string
		wantErr         bool
	}{
		{
			name:         "provider slash name",
			args:         []string{"openrouter/anthropic/claude-3.5-sonnet"},
			wantProvider: "openrouter",
			wantName:     "anthropic/claude-3.5-sonnet",
		},
		{
			name:         "provider space name",
			args:         []string{"openai", "gpt-4o"},
			wantProvider: "openai",
			wantName:     "gpt-4o",
		},
		{
			name:            "bare name uses current provider",
			args:            []string{"gpt-4o"},
			currentProvider: "openai",
			wantProvider:    "openai",
			wantName:        "gpt-4o",
		},
		{
			name:    "no args is an error",
			args:    []string{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, name, err := parseModelArg(tt.args, tt.currentProvider)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantProvider, provider)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

// TestHandleModelsCommand_DirectSelection verifies :model <provider>/<name>
// resolves straight to a modelSelectedMsg without opening the picker.
func TestHandleModelsCommand_DirectSelection(t *testing.T) {
	config := &Config{LLM: LLMConfig{Provider: "anthropic", Model: "claude-3-5-sonnet-latest"}}
	model := &TUIModel{config: config, tabs: newTestTabManager()}

	cmd := handleModelsCommand(model, []string{"openrouter/anthropic/claude-3.5-sonnet"})
	require.NotNil(t, cmd)

	msg := cmd()
	selected, ok := msg.(modelSelectedMsg)
	require.True(t, ok, "expected modelSelectedMsg, got %T", msg)
	require.NotNil(t, selected.model)
	assert.Equal(t, "openrouter", selected.model.Provider)
	assert.Equal(t, "anthropic/claude-3.5-sonnet", selected.model.ID)
}

// TestHandleModelsCommand_DirectSelectionTwoArgs verifies the space-separated
// direct form.
func TestHandleModelsCommand_DirectSelectionTwoArgs(t *testing.T) {
	model := &TUIModel{config: &Config{LLM: LLMConfig{Provider: "anthropic"}}, tabs: newTestTabManager()}

	cmd := handleModelsCommand(model, []string{"openai", "gpt-4o"})
	require.NotNil(t, cmd)

	msg := cmd()
	selected, ok := msg.(modelSelectedMsg)
	require.True(t, ok, "expected modelSelectedMsg, got %T", msg)
	assert.Equal(t, "openai", selected.model.Provider)
	assert.Equal(t, "gpt-4o", selected.model.ID)
}

// TestHandleModelsCommand_UnknownProviderToast verifies an unknown provider is
// rejected with an error toast rather than selecting a bogus model.
func TestHandleModelsCommand_UnknownProviderToast(t *testing.T) {
	model := &TUIModel{config: &Config{LLM: LLMConfig{Provider: "anthropic"}}, tabs: newTestTabManager()}

	cmd := handleModelsCommand(model, []string{"not-a-provider/some-model"})
	require.NotNil(t, cmd)

	msg := cmd()
	toast, ok := msg.(toastMsg)
	require.True(t, ok, "expected a toastMsg, got %T", msg)
	assert.Equal(t, "error", toast.msgType)
	assert.Contains(t, toast.message, "Unknown provider")
}

func TestHandleModelsCommandShowsConfiguredModels(t *testing.T) {
	config := &Config{
		LLM: LLMConfig{
			Provider: "anthropic",
			Model:    "claude-3-5-sonnet-latest",
		},
	}

	model := &TUIModel{
		config: config,
		tabs:   newTestTabManager(),
	}

	cmd := handleModelsCommand(model, []string{})
	require.NotNil(t, cmd, "handleModelsCommand should return a command")

	msg := cmd()
	require.NotNil(t, msg, "Command should return a message")
}

// TestFetchAllModels_WithAPIKey verifies that models show as ready or manual_entry
// when API key is available but the court/bifrost is nil (simulating test env)
func TestFetchAllModels_WithAPIKey(t *testing.T) {
	// Clear any existing credentials
	DeleteAPIKeyFromKeyring("openai")

	// Set OpenAI API key in environment
	originalKey := os.Getenv("OPENAI_API_KEY")
	os.Setenv("OPENAI_API_KEY", "test-key")
	defer func() {
		if originalKey != "" {
			os.Setenv("OPENAI_API_KEY", originalKey)
		} else {
			os.Unsetenv("OPENAI_API_KEY")
		}
	}()

	// Override fetch to simulate error (no court available in test)
	origFetch := fetchModelsForProvider
	defer func() { fetchModelsForProvider = origFetch }()
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		return nil, fmt.Errorf("LLM client not initialized")
	}

	config := &Config{
		LLM: LLMConfig{
			Provider: "openai",
			Model:    "gpt-4o",
		},
	}

	models, diagnostics := fetchAllModels(config, nil, true)

	// With an API key set but no court, the fetch fails: no typing row, only a diagnostic.
	for _, m := range models {
		if m.Provider == "openai" {
			t.Errorf("Expected no provider-bound row for a failed fetch, got %+v", m)
		}
	}
	require.Len(t, diagnostics, 1, "Expected a diagnostic for the unreachable provider")
	assert.Contains(t, diagnostics[0], "openai")
}

// TestFetchAllModels_LoginRequiredEntryContents verifies that login_required entries
// are no longer emitted by fetchAllModels (they moved to :login)
func TestFetchAllModels_LoginRequiredEntryContents(t *testing.T) {
	DeleteAPIKeyFromKeyring("anthropic")
	DeleteAPIKeyFromKeyring("openai")
	DeleteAPIKeyFromKeyring("googleai")
	DeleteAPIKeyFromKeyring("openrouter")

	originalAnthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	originalOpenAIKey := os.Getenv("OPENAI_API_KEY")
	originalGeminiKey := os.Getenv("GEMINI_API_KEY")
	originalGoogleKey := os.Getenv("GOOGLE_API_KEY")
	originalOpenRouterKey := os.Getenv("OPENROUTER_API_KEY")
	originalOllamaHost := os.Getenv("OLLAMA_HOST")

	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("GEMINI_API_KEY")
	os.Unsetenv("GOOGLE_API_KEY")
	os.Unsetenv("OPENROUTER_API_KEY")

	mockOllamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[]}`))
	}))
	defer mockOllamaServer.Close()
	os.Setenv("OLLAMA_HOST", mockOllamaServer.URL)

	defer func() {
		if originalAnthropicKey != "" {
			os.Setenv("ANTHROPIC_API_KEY", originalAnthropicKey)
		}
		if originalOpenAIKey != "" {
			os.Setenv("OPENAI_API_KEY", originalOpenAIKey)
		}
		if originalGeminiKey != "" {
			os.Setenv("GEMINI_API_KEY", originalGeminiKey)
		}
		if originalGoogleKey != "" {
			os.Setenv("GOOGLE_API_KEY", originalGoogleKey)
		}
		if originalOpenRouterKey != "" {
			os.Setenv("OPENROUTER_API_KEY", originalOpenRouterKey)
		}
		if originalOllamaHost != "" {
			os.Setenv("OLLAMA_HOST", originalOllamaHost)
		} else {
			os.Unsetenv("OLLAMA_HOST")
		}
	}()

	// Override fetch to return empty for Ollama (which is keyless and thus "configured")
	origFetch := fetchModelsForProvider
	defer func() { fetchModelsForProvider = origFetch }()
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		return nil, nil // no models, no error
	}

	config := &Config{
		LLM: LLMConfig{
			Provider: "anthropic",
			Model:    "claude-3-5-sonnet-latest",
		},
	}

	models, _ := fetchAllModels(config, nil, true)

	// login_required entries are now handled by :login, not :models, so the
	// only row is the pinned manual-entry escape hatch.
	for _, m := range models {
		assert.NotEqual(t, "login_required", m.Status, "login_required entries should not be in fetchAllModels")
		assert.Equal(t, "manual_entry", m.Status, "only the pinned manual_entry row remains")
	}
	assert.Len(t, models, 1)
}

// TestHandleModelsCommand_NoLoginRequiredOnSelect verifies that handleModelsCommand
// no longer wires OnSelect for login_required entries (they moved to :login)
func TestHandleModelsCommand_NoLoginRequiredOnSelect(t *testing.T) {
	DeleteAPIKeyFromKeyring("anthropic")
	DeleteAPIKeyFromKeyring("openai")
	DeleteAPIKeyFromKeyring("googleai")
	DeleteAPIKeyFromKeyring("openrouter")

	originalAnthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	originalOpenAIKey := os.Getenv("OPENAI_API_KEY")
	originalGeminiKey := os.Getenv("GEMINI_API_KEY")
	originalGoogleKey := os.Getenv("GOOGLE_API_KEY")
	originalOpenRouterKey := os.Getenv("OPENROUTER_API_KEY")
	originalOllamaHost := os.Getenv("OLLAMA_HOST")

	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("GEMINI_API_KEY")
	os.Unsetenv("GOOGLE_API_KEY")
	os.Unsetenv("OPENROUTER_API_KEY")

	mockOllamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[]}`))
	}))
	defer mockOllamaServer.Close()
	os.Setenv("OLLAMA_HOST", mockOllamaServer.URL)

	defer func() {
		if originalAnthropicKey != "" {
			os.Setenv("ANTHROPIC_API_KEY", originalAnthropicKey)
		}
		if originalOpenAIKey != "" {
			os.Setenv("OPENAI_API_KEY", originalOpenAIKey)
		}
		if originalGeminiKey != "" {
			os.Setenv("GEMINI_API_KEY", originalGeminiKey)
		}
		if originalGoogleKey != "" {
			os.Setenv("GOOGLE_API_KEY", originalGoogleKey)
		}
		if originalOpenRouterKey != "" {
			os.Setenv("OPENROUTER_API_KEY", originalOpenRouterKey)
		}
		if originalOllamaHost != "" {
			os.Setenv("OLLAMA_HOST", originalOllamaHost)
		} else {
			os.Unsetenv("OLLAMA_HOST")
		}
	}()

	config := &Config{
		LLM: LLMConfig{
			Provider: "anthropic",
			Model:    "claude-3-5-sonnet-latest",
		},
	}

	model := &TUIModel{
		config: config,
		tabs:   newTestTabManager(),
	}

	cmd := handleModelsCommand(model, []string{})
	require.NotNil(t, cmd)

	msg := cmd()
	require.NotNil(t, msg)

	// Verify no login_required entries exist in the models list
	if modelsMsg, ok := msg.(modelsLoadedMsg); ok {
		for _, m := range modelsMsg.models {
			assert.NotEqual(t, "login_required", m.Status,
				"login_required entries should not be in handleModelsCommand output")
		}
	}
}

// TestHandleModelsCommand_PinnedRowOnSelect verifies that the pinned
// manual-entry row produced by the command carries an OnSelect that opens the
// raw "provider/model" prompt, and that failed providers yield diagnostics.
func TestHandleModelsCommand_PinnedRowOnSelect(t *testing.T) {
	clearProviderAuthKeys()

	originalOllamaHost := os.Getenv("OLLAMA_HOST")
	os.Unsetenv("OLLAMA_HOST")
	defer func() {
		if originalOllamaHost != "" {
			os.Setenv("OLLAMA_HOST", originalOllamaHost)
		}
	}()

	t.Setenv("OPENROUTER_API_KEY", "bad-key")

	// Override fetch to simulate error from bifrost
	origFetch := fetchModelsForProvider
	defer func() { fetchModelsForProvider = origFetch }()
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		if provider == "openrouter" {
			return nil, fmt.Errorf("API returned status 401")
		}
		return nil, nil
	}

	config := &Config{
		LLM: LLMConfig{
			Provider: "openrouter",
			Model:    "test",
		},
	}

	models, diagnostics := fetchAllModels(config, nil, true)

	// Unreachable providers surface as diagnostics and never as typing rows.
	require.Len(t, diagnostics, 1)
	assert.Contains(t, diagnostics[0], "openrouter")

	// Exactly one manual_entry row, pinned last, with an OnSelect that opens
	// the raw provider/model prompt.
	require.NotEmpty(t, models)
	last := models[len(models)-1]
	require.Equal(t, "manual_entry", last.Status)
	require.NotNil(t, last.OnSelect, "pinned row must carry OnSelect")
	_, ok := last.OnSelect().(enterModelNameRawMsg)
	assert.True(t, ok, "pinned OnSelect should emit enterModelNameRawMsg")

	for _, m := range models {
		if m.Status == "manual_entry" {
			assert.NotNil(t, m.OnSelect, "manual_entry row must carry OnSelect")
		}
	}
}

// TestProviderEnvVar_Convention verifies convention-based env var resolution
func TestProviderEnvVar_Convention(t *testing.T) {
	assert.Equal(t, "ANTHROPIC_API_KEY", providerEnvVar("anthropic"))
	assert.Equal(t, "OPENAI_API_KEY", providerEnvVar("openai"))
	assert.Equal(t, "GEMINI_API_KEY", providerEnvVar("googleai"))
	assert.Equal(t, "OPENROUTER_API_KEY", providerEnvVar("openrouter"))
	assert.Equal(t, "COHERE_API_KEY", providerEnvVar("cohere"))
	assert.Equal(t, "MISTRAL_API_KEY", providerEnvVar("mistral"))
	assert.Equal(t, "GOOGLE_CLOUD_PROJECT", providerEnvVar("vertex"))
}

// TestProviderDisplayName verifies display names from the metadata table
func TestProviderDisplayName(t *testing.T) {
	assert.Equal(t, "Anthropic", providerDisplayName("anthropic"))
	assert.Equal(t, "OpenAI", providerDisplayName("openai"))
	assert.Equal(t, "Google AI", providerDisplayName("googleai"))
	assert.Equal(t, "OpenRouter", providerDisplayName("openrouter"))
	assert.Equal(t, "Ollama", providerDisplayName("ollama"))
	assert.Equal(t, "Vertex AI", providerDisplayName("vertex"))
	// Unknown providers return the provider name itself
	assert.Equal(t, "unknown", providerDisplayName("unknown"))
}

// TestProviderMeta_AuthType verifies auth type classification
func TestProviderMeta_AuthType(t *testing.T) {
	assert.Equal(t, AuthTypeAPIKey, providerAuthType("anthropic"))
	assert.Equal(t, AuthTypeOAuth, providerAuthType("openai"))
	assert.Equal(t, AuthTypeKeyless, providerAuthType("ollama"))
	assert.Equal(t, AuthTypeAPIKey, providerAuthType("cohere"))
	assert.Equal(t, AuthTypeGCloud, providerAuthType("vertex"))
}

// TestFetchAllModels_WithMockBifrost verifies that fetchAllModels correctly
// maps schemas.Model from bifrost to asimi's Model struct.
func TestFetchAllModels_WithMockBifrost(t *testing.T) {
	DeleteAPIKeyFromKeyring("anthropic")
	DeleteAPIKeyFromKeyring("openai")
	DeleteAPIKeyFromKeyring("googleai")
	DeleteAPIKeyFromKeyring("openrouter")

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	originalOllamaHost := os.Getenv("OLLAMA_HOST")
	mockOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[]}`))
	}))
	defer mockOllama.Close()
	os.Setenv("OLLAMA_HOST", mockOllama.URL)
	defer func() {
		if originalOllamaHost != "" {
			os.Setenv("OLLAMA_HOST", originalOllamaHost)
		} else {
			os.Unsetenv("OLLAMA_HOST")
		}
	}()

	// Override fetch to return mock models
	origFetch := fetchModelsForProvider
	defer func() { fetchModelsForProvider = origFetch }()
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		if provider == "anthropic" {
			return []Model{
				{ID: "claude-3-5-sonnet", DisplayName: "Claude 3.5 Sonnet"},
				{ID: "claude-3-5-haiku", DisplayName: "Claude 3.5 Haiku"},
			}, nil
		}
		return nil, nil
	}

	config := &Config{
		LLM: LLMConfig{
			Provider: "anthropic",
			Model:    "claude-3-5-sonnet",
		},
	}

	models, _ := fetchAllModels(config, nil, true)
	assert.GreaterOrEqual(t, len(models), 2)

	// Verify the active model is marked
	for _, m := range models {
		if m.Provider == "anthropic" && m.ID == "claude-3-5-sonnet" {
			assert.Equal(t, "active", m.Status)
		}
	}
}

// TestBifrostProviderToAsimi verifies the provider key mapping
func TestBifrostProviderToAsimi(t *testing.T) {
	assert.Equal(t, "googleai", bifrostProviderToAsimi("gemini"))
	assert.Equal(t, "anthropic", bifrostProviderToAsimi("anthropic"))
	assert.Equal(t, "openai", bifrostProviderToAsimi("openai"))
}

// TestAsimiProviderToBifrost verifies the inverse mapping of asimi → bifrost
func TestAsimiProviderToBifrost(t *testing.T) {
	assert.Equal(t, "gemini", asimiProviderToBifrost("googleai"))
	assert.Equal(t, "anthropic", asimiProviderToBifrost("anthropic"))
	assert.Equal(t, "openai", asimiProviderToBifrost("openai"))
	assert.Equal(t, "cohere", asimiProviderToBifrost("cohere"))
}

// TestAsimiProviderToBifrostRoundTrip verifies that the two mapping functions
// are inverses when starting from the bifrost side.
func TestAsimiProviderToBifrostRoundTrip(t *testing.T) {
	// bifrost → asimi → bifrost should be identity
	bifrostProviders := []string{"anthropic", "openai", "gemini", "cohere", "mistral"}
	for _, p := range bifrostProviders {
		asimi := bifrostProviderToAsimi(p)
		bifrost := asimiProviderToBifrost(asimi)
		assert.Equal(t, p, bifrost, "round-trip should be identity for %s", p)
	}

	// asimi → bifrost → asimi should be identity
	asimiProviders := []string{"anthropic", "openai", "googleai", "cohere", "mistral"}
	for _, p := range asimiProviders {
		bifrost := asimiProviderToBifrost(p)
		asimi := bifrostProviderToAsimi(bifrost)
		assert.Equal(t, p, asimi, "reverse round-trip should be identity for %s", p)
	}
}

// mockListModelsCourt is a minimal courtapi.Client that returns canned
// ListModels responses. It embeds courtapi.Client so all other methods
// panic if called (which is fine for these tests).
type mockListModelsCourt struct {
	courtapi.Client
	resp *schemas.BifrostListModelsResponse
	err  error

	approx map[string]int // "provider:model" -> size
}

func (m *mockListModelsCourt) ListModels(provider string) (*schemas.BifrostListModelsResponse, error) {
	return m.resp, m.err
}

func (m *mockListModelsCourt) ApproximateContextSize(provider, model string) int {
	return m.approx[provider+":"+model]
}

// TestFetchModelsForProvider_MapsBifrostModels verifies that the real
// fetchModelsForProvider correctly maps schemas.Model fields to asimi's Model,
// including nil-to-empty-string handling for Name and Description pointers and
// the typed metadata fields (ContextLength/Pricing/Modality).
func TestFetchModelsForProvider_MapsBifrostModels(t *testing.T) {
	name1 := "Claude 3.5 Sonnet"
	desc1 := "Most intelligent model"
	ctx := 262144
	modality := "text->text"
	prompt := "3"
	completion := "15"
	models := []schemas.Model{
		{
			ID:            "claude-3-5-sonnet",
			Name:          &name1,
			Description:   &desc1,
			ContextLength: &ctx,
			Architecture:  &schemas.Architecture{Modality: &modality},
			Pricing:       &schemas.Pricing{Prompt: &prompt, Completion: &completion},
		},
		{ID: "claude-3-5-haiku"}, // nil Name, Description and metadata
	}
	court := &mockListModelsCourt{
		resp: &schemas.BifrostListModelsResponse{Data: models},
	}

	result, err := fetchModelsForProvider(&Config{}, court, "anthropic")
	require.NoError(t, err)
	require.Len(t, result, 2)

	assert.Equal(t, "claude-3-5-sonnet", result[0].ID)
	assert.Equal(t, "Claude 3.5 Sonnet", result[0].DisplayName)
	assert.Equal(t, "Most intelligent model", result[0].Description)
	assert.Equal(t, 262144, result[0].ContextLength)
	assert.Equal(t, "text->text", result[0].Modality)
	assert.Equal(t, "3", result[0].PricePrompt)
	assert.Equal(t, "15", result[0].PriceCompletion)

	// nil Name falls back to ID; nil Description falls back to ""
	assert.Equal(t, "claude-3-5-haiku", result[1].ID)
	assert.Equal(t, "claude-3-5-haiku", result[1].DisplayName)
	assert.Equal(t, "", result[1].Description)
	assert.Equal(t, 0, result[1].ContextLength)
	assert.Equal(t, "", result[1].Modality)
	assert.Equal(t, "", result[1].PricePrompt)
	assert.Equal(t, "", result[1].PriceCompletion)
}

// TestFetchModelsForProvider_NilCourtReturnsError verifies the nil court guard
func TestFetchModelsForProvider_NilCourtReturnsError(t *testing.T) {
	_, err := fetchModelsForProvider(&Config{}, nil, "anthropic")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no court available")
}

// TestFetchModelsForProvider_CourtError verifies error propagation
func TestFetchModelsForProvider_CourtError(t *testing.T) {
	court := &mockListModelsCourt{err: fmt.Errorf("provider unavailable")}
	_, err := fetchModelsForProvider(&Config{}, court, "openai")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "provider unavailable")
}

// TestFetchModelsForProvider_EmptyResponse verifies empty responses are handled
func TestFetchModelsForProvider_EmptyResponse(t *testing.T) {
	court := &mockListModelsCourt{
		resp: &schemas.BifrostListModelsResponse{Data: nil},
	}
	result, err := fetchModelsForProvider(&Config{}, court, "cohere")
	require.NoError(t, err)
	assert.Empty(t, result)
}

// TestProviderBaseURLEnvVar_Convention verifies convention-based base URL env var
func TestProviderBaseURLEnvVar_Convention(t *testing.T) {
	assert.Equal(t, "ANTHROPIC_BASE_URL", providerBaseURLEnvVar("anthropic"))
	assert.Equal(t, "OPENAI_BASE_URL", providerBaseURLEnvVar("openai"))
	assert.Equal(t, "GEMINI_BASE_URL", providerBaseURLEnvVar("googleai"))
	assert.Equal(t, "AZURE_OPENAI_BASE_URL", providerBaseURLEnvVar("azure"))
	assert.Equal(t, "COHERE_BASE_URL", providerBaseURLEnvVar("cohere"))
	assert.Equal(t, "MISTRAL_BASE_URL", providerBaseURLEnvVar("mistral"))
}

// TestProviderBaseURLFromEnv verifies that base URL env vars are resolved
func TestProviderBaseURLFromEnv(t *testing.T) {
	t.Setenv("COHERE_BASE_URL", "https://api.cohere.ai")
	assert.Equal(t, "https://api.cohere.ai", providerBaseURLFromEnv("cohere"))

	t.Setenv("GEMINI_BASE_URL", "https://custom-gemini.example.com")
	assert.Equal(t, "https://custom-gemini.example.com", providerBaseURLFromEnv("googleai"))

	os.Unsetenv("COHERE_BASE_URL")
	assert.Equal(t, "", providerBaseURLFromEnv("cohere"))
}

// clearProviderAuthKeys unsets the API-key env vars and clears the keyring
// entries for every standard provider, so getConfiguredProviderKeys starts
// from a clean slate regardless of the shell/CI environment. Iterating over
// schemas.StandardProviders keeps this list in sync as providers are added.
func clearProviderAuthKeys() {
	for _, sp := range schemas.StandardProviders {
		key := bifrostProviderToAsimi(string(sp))
		os.Unsetenv(providerEnvVar(key))
		// Special-case aliases checked in checkProviderAuth/providerEnvVar.
		if key == "googleai" {
			os.Unsetenv("GEMINI_API_KEY")
			os.Unsetenv("GOOGLE_API_KEY")
		}
		if key == "bedrock" {
			os.Unsetenv("AWS_ACCESS_KEY_ID")
			os.Unsetenv("AWS_SECRET_ACCESS_KEY")
		}
		if key == "vertex" {
			os.Unsetenv("GOOGLE_CLOUD_REGION")
			os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
		}
		_ = DeleteAPIKeyFromKeyring(key)
	}
}

// TestGetConfiguredProviderKeys_NoAuth verifies that without auth, only
// keyless providers are included
func TestGetConfiguredProviderKeys_NoAuth(t *testing.T) {
	clearProviderAuthKeys()

	// Without Ollama available, no providers
	providers := getConfiguredProviderKeys(false)
	assert.Empty(t, providers)
}

// TestGetConfiguredProviderKeys_ConventionAuth verifies that a convention-based
// env var for a new provider (not in the old hardcoded switches) is detected
func TestGetConfiguredProviderKeys_ConventionAuth(t *testing.T) {
	clearProviderAuthKeys()

	t.Setenv("COHERE_API_KEY", "test-key")

	providers := getConfiguredProviderKeys(false)
	assert.Contains(t, providers, "cohere")
}

// TestFormatContextLength verifies the compact human rendering of token counts.
func TestFormatContextLength(t *testing.T) {
	tests := []struct {
		tokens int
		want   string
	}{
		{0, "0"},
		{512, "512"},
		{999, "999"},
		{1000, "1K"},
		{262144, "262K"},
		{999999, "1000K"}, // rounds up rather than truncating
		{1000000, "1M"},
		{2500000, "2M"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, formatContextLength(tt.tokens), "tokens=%d", tt.tokens)
	}
}

// TestTruncateToWidth verifies width clamping, including the non-positive guard.
func TestTruncateToWidth(t *testing.T) {
	assert.Equal(t, "", truncateToWidth("anything", 0), "width 0 must yield empty")
	assert.Equal(t, "", truncateToWidth("anything", -5), "negative width must yield empty")
	assert.Equal(t, "hello", truncateToWidth("hello", 5), "exact fit must pass through")
	assert.Equal(t, "hello", truncateToWidth("hello", 50), "short string must pass through")
	assert.Equal(t, "hel", truncateToWidth("hello", 3), "long string must be clamped")
	// Multi-byte content is sliced by rune, not byte.
	assert.Equal(t, "të", truncateToWidth("tëst", 2), "rune slicing, not byte slicing")
}

// TestSelectModelDirect_NoArgsEmitsErrorToast verifies the defensive parse-error
// path returns an error toast rather than panicking or selecting a bogus model.
func TestSelectModelDirect_NoArgsEmitsErrorToast(t *testing.T) {
	model := &TUIModel{config: &Config{LLM: LLMConfig{Provider: "anthropic"}}, tabs: newTestTabManager()}

	cmd := selectModelDirect(model, nil)
	require.NotNil(t, cmd)

	result := cmd()
	toast, ok := result.(toastMsg)
	require.True(t, ok, "expected toastMsg, got %T", result)
	assert.Equal(t, "error", toast.msgType)
	assert.Contains(t, toast.message, "no model specified")
}

// TestModelMetadataSuffix verifies the dim metadata suffix composition and the
// empty case when a model carries no metadata.
func TestModelMetadataSuffix(t *testing.T) {
	assert.Equal(t, "", modelMetadataSuffix(Model{ID: "plain"}),
		"no metadata → no suffix")

	assert.Equal(t, " · 262K ctx",
		modelMetadataSuffix(Model{ContextLength: 262144}))

	assert.Equal(t, " · $3/$15",
		modelMetadataSuffix(Model{PricePrompt: "3", PriceCompletion: "15"}))

	// Partial pricing still renders (documented cosmetic quirk: missing side is blank).
	assert.Equal(t, " · $3/$",
		modelMetadataSuffix(Model{PricePrompt: "3"}))

	assert.Equal(t, " · 262K ctx · $3/$15",
		modelMetadataSuffix(Model{
			ContextLength:   262144,
			PricePrompt:     "3",
			PriceCompletion: "15",
			Modality:        "text->text",
		}))

	// Modality is no longer rendered: a model whose only field is Modality
	// yields no suffix.
	assert.Equal(t, "", modelMetadataSuffix(Model{Modality: "text->text"}))
}

// TestSortModels_SelectedProviderFirst verifies the new ordering: the selected
// provider's group comes first even when other providers sort alphabetically
// earlier, remaining providers follow alphabetically, rows within a provider are
// alphabetical by display name, and an active row is not hoisted.
func TestSortModels_SelectedProviderFirst(t *testing.T) {
	models := []Model{
		{ID: "google-b", DisplayName: "Google B", Provider: "googleai"},
		{ID: "anthropic-b", DisplayName: "Anthropic B", Provider: "anthropic"},
		{ID: "openai-z", DisplayName: "OpenAI Z", Provider: "openai", Status: "active"},
		{ID: "anthropic-a", DisplayName: "Anthropic A", Provider: "anthropic"},
		{ID: "google-a", DisplayName: "Google A", Provider: "googleai"},
		{ID: "openai-a", DisplayName: "OpenAI A", Provider: "openai"},
	}

	sortModels(models, "openai")

	got := make([]string, len(models))
	for i, m := range models {
		got[i] = m.Provider + "/" + m.DisplayName
	}
	assert.Equal(t, []string{
		"openai/OpenAI A",       // selected provider first
		"openai/OpenAI Z",       // active row stays alphabetical, not hoisted
		"anthropic/Anthropic A", // remaining providers alphabetical
		"anthropic/Anthropic B",
		"googleai/Google A",
		"googleai/Google B",
	}, got)
}

// TestSortModels_FallsBackToID verifies rows without a display name sort by ID.
func TestSortModels_FallsBackToID(t *testing.T) {
	models := []Model{
		{ID: "b-model", Provider: "openai"},
		{ID: "a-model", Provider: "openai"},
	}
	sortModels(models, "openai")
	assert.Equal(t, "a-model", models[0].ID)
	assert.Equal(t, "b-model", models[1].ID)
}

// TestBuildModelList_PinnedRowStaysLast verifies the pinned manual-entry row
// remains the final row after the selected-provider-first sort.
func TestBuildModelList_PinnedRowStaysLast(t *testing.T) {
	config := &Config{LLM: LLMConfig{Provider: "openai", Model: "openai-z"}}
	byProvider := map[string][]Model{
		"openai":    {{ID: "openai-z", DisplayName: "OpenAI Z"}, {ID: "openai-a", DisplayName: "OpenAI A"}},
		"anthropic": {{ID: "claude", DisplayName: "Claude"}},
	}

	models := buildModelList(config, byProvider)
	require.NotEmpty(t, models)

	last := models[len(models)-1]
	assert.Equal(t, "manual_entry", last.Status)
	assert.Equal(t, "Type a model name…", last.DisplayName)
	assert.Equal(t, 1, countStatus(models, "manual_entry"))
	assert.Equal(t, "openai", models[0].Provider, "selected provider's rows come first")
}

// TestModelsWindowRenderList_Diagnostics verifies diagnostics are rendered inline
// under the title rather than as selectable provider rows.
func TestModelsWindowRenderList_Diagnostics(t *testing.T) {
	window := NewModelsWindow()
	window.SetSize(100, 10)

	models := []Model{
		{ID: "gpt-4o", DisplayName: "GPT-4o", Provider: "openai", Status: "ready"},
	}
	window.SetModels(models, "gpt-4o", []string{"openrouter: API returned status 401", "vertex: no GOOGLE_CLOUD_PROJECT"})

	render := window.RenderList(0, 0, window.GetVisibleSlots())
	assert.Contains(t, render, "unavailable:",
		"diagnostics must be surfaced under the title")
	assert.Contains(t, render, "openrouter: API returned status 401")
	assert.Contains(t, render, "vertex: no GOOGLE_CLOUD_PROJECT")
}

// TestModelsWindowRenderList_NoDiagnostics verifies no diagnostic line appears
// when there are no fetch failures.
func TestModelsWindowRenderList_NoDiagnostics(t *testing.T) {
	window := NewModelsWindow()
	window.SetSize(100, 10)

	window.SetModels([]Model{
		{ID: "gpt-4o", DisplayName: "GPT-4o", Provider: "openai", Status: "ready"},
	}, "gpt-4o", nil)

	render := window.RenderList(0, 0, window.GetVisibleSlots())
	assert.NotContains(t, render, "unavailable:")
}

// TestModelsWindowRenderList_MetadataSuffix verifies the per-row metadata suffix
// is rendered, and that it is truncated to the pane width.
func TestModelsWindowRenderList_MetadataSuffix(t *testing.T) {
	window := NewModelsWindow()
	window.SetSize(100, 10)

	window.SetModels([]Model{
		{
			ID:              "claude-3-5-sonnet",
			DisplayName:     "Claude 3.5 Sonnet",
			Provider:        "anthropic",
			Status:          "active",
			ContextLength:   262144,
			PricePrompt:     "3",
			PriceCompletion: "15",
			Modality:        "text->text",
		},
	}, "claude-3-5-sonnet", nil)

	render := window.RenderList(0, 0, window.GetVisibleSlots())
	assert.Contains(t, render, "262K ctx")
	assert.Contains(t, render, "$3/$15")
	assert.NotContains(t, render, "text->text")

	// A narrow pane must clamp the suffix rather than overflow.
	narrow := NewModelsWindow()
	narrow.SetSize(28, 10)
	narrow.SetModels([]Model{
		{
			ID:              "claude-3-5-sonnet",
			DisplayName:     "Claude 3.5 Sonnet",
			Provider:        "anthropic",
			Status:          "active",
			ContextLength:   262144,
			PricePrompt:     "3",
			PriceCompletion: "15",
		},
	}, "claude-3-5-sonnet", nil)
	narrowRender := narrow.RenderList(0, 0, narrow.GetVisibleSlots())
	for _, line := range strings.Split(narrowRender, "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), 28, "row must not exceed pane width: %q", line)
	}
}

// resetModelCache clears the package-level session catalog so each test starts
// from a cold cache. Call it (and defer it) at the top of cache-sensitive tests.
func resetModelCache() {
	modelCatalog.mu.Lock()
	modelCatalog.byProvider = map[string][]Model{}
	modelCatalog.diagnostics = nil
	modelCatalog.fetched = false
	modelCatalog.mu.Unlock()
}

// installCountingFetch overrides fetchModelsForProvider with a fake that counts
// invocations per provider and returns canned models. It restores the original
// on test cleanup.
func installCountingFetch(t *testing.T, calls *int32, models func(provider string) ([]Model, error)) {
	t.Helper()
	origFetch := fetchModelsForProvider
	t.Cleanup(func() { fetchModelsForProvider = origFetch })
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		atomic.AddInt32(calls, 1)
		return models(provider)
	}
}

// TestFetchAllModels_CacheHitMakesNoSecondNetworkCall verifies that a warm cache
// answers fetchAllModels without invoking the provider fetch a second time.
func TestFetchAllModels_CacheHitMakesNoSecondNetworkCall(t *testing.T) {
	resetModelCache()
	defer resetModelCache()
	clearProviderAuthKeys()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	var calls int32
	installCountingFetch(t, &calls, func(provider string) ([]Model, error) {
		return []Model{{ID: "claude-3-5-sonnet", DisplayName: "Claude 3.5 Sonnet"}}, nil
	})

	config := &Config{LLM: LLMConfig{Provider: "anthropic", Model: "claude-3-5-sonnet"}}

	first, diags := fetchAllModels(config, nil, false)
	require.NotEmpty(t, first)
	firstCalls := atomic.LoadInt32(&calls)
	require.Positive(t, firstCalls, "cold cache must fetch live")

	second, secondDiags := fetchAllModels(config, nil, false)
	assert.Equal(t, firstCalls, atomic.LoadInt32(&calls),
		"warm cache must not fetch again")
	assert.Len(t, second, len(first))
	assert.Equal(t, diags, secondDiags)

	// The pinned row is still last after a cached read.
	last := second[len(second)-1]
	assert.Equal(t, "manual_entry", last.Status)
	assert.Equal(t, "Type a model name…", last.DisplayName)
	assert.Equal(t, 1, countStatus(second, "manual_entry"))
}

// TestFetchAllModels_ForceRefreshBypassesCache verifies forceRefresh triggers a
// live fetch even when the cache is warm.
func TestFetchAllModels_ForceRefreshBypassesCache(t *testing.T) {
	resetModelCache()
	defer resetModelCache()
	clearProviderAuthKeys()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	var calls int32
	installCountingFetch(t, &calls, func(provider string) ([]Model, error) {
		return []Model{{ID: "claude-3-5-sonnet"}}, nil
	})

	config := &Config{LLM: LLMConfig{Provider: "anthropic", Model: "claude-3-5-sonnet"}}

	fetchAllModels(config, nil, false)
	afterFirst := atomic.LoadInt32(&calls)

	fetchAllModels(config, nil, true)
	assert.Greater(t, atomic.LoadInt32(&calls), afterFirst,
		"forceRefresh must fetch live despite a warm cache")
}

// TestFetchAllModels_FailedProviderKeepsCachedModels verifies a transient error
// on refresh does not empty a provider's previously cached models.
func TestFetchAllModels_FailedProviderKeepsCachedModels(t *testing.T) {
	resetModelCache()
	defer resetModelCache()
	clearProviderAuthKeys()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	origFetch := fetchModelsForProvider
	t.Cleanup(func() { fetchModelsForProvider = origFetch })

	// First round succeeds.
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		return []Model{{ID: "claude-3-5-sonnet", DisplayName: "Claude 3.5 Sonnet"}}, nil
	}
	config := &Config{LLM: LLMConfig{Provider: "anthropic", Model: "claude-3-5-sonnet"}}
	fetchAllModels(config, nil, false)

	// Second round fails for the same provider.
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		return nil, fmt.Errorf("API returned status 401")
	}
	models, diagnostics := fetchAllModels(config, nil, true)

	assert.Contains(t, diagnostics, "anthropic: API returned status 401")
	var found bool
	for _, m := range models {
		if m.Provider == "anthropic" && m.ID == "claude-3-5-sonnet" {
			found = true
		}
	}
	assert.True(t, found, "a failed refresh must retain previously cached models")
}

// TestFetchAllModels_PrunesUnconfiguredProviders verifies that a provider no
// longer configured is dropped from the cache.
func TestFetchAllModels_PrunesUnconfiguredProviders(t *testing.T) {
	resetModelCache()
	defer resetModelCache()
	clearProviderAuthKeys()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	origFetch := fetchModelsForProvider
	t.Cleanup(func() { fetchModelsForProvider = origFetch })
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		return []Model{{ID: provider + "-model"}}, nil
	}

	config := &Config{LLM: LLMConfig{Provider: "anthropic"}}
	fetchAllModels(config, nil, false)

	// Drop anthropic's credentials so it is no longer configured.
	clearProviderAuthKeys()
	models, _ := fetchAllModels(config, nil, true)

	for _, m := range models {
		assert.NotEqual(t, "anthropic", m.Provider, "pruned provider must not linger in the cache")
	}
}

// TestFetchAllModels_ProvidersFetchedConcurrently verifies the fan-out runs
// providers in parallel: N providers each sleeping D must complete in well under
// N*D. Ordering of the resulting catalog must stay deterministic.
func TestFetchAllModels_ProvidersFetchedConcurrently(t *testing.T) {
	resetModelCache()
	defer resetModelCache()
	clearProviderAuthKeys()

	// Configure several providers via env so getConfiguredProviderKeys returns them.
	providers := []string{"anthropic", "openai", "cohere", "mistral", "groq", "deepinfra"}
	for _, p := range providers {
		t.Setenv(providerEnvVar(p), "test-key")
	}

	const sleep = 100 * time.Millisecond
	origFetch := fetchModelsForProvider
	t.Cleanup(func() { fetchModelsForProvider = origFetch })
	fetchModelsForProvider = func(config *Config, court courtapi.Client, provider string) ([]Model, error) {
		time.Sleep(sleep)
		return []Model{{ID: provider + "-b"}, {ID: provider + "-a"}}, nil
	}

	config := &Config{LLM: LLMConfig{Provider: "anthropic", Model: "anthropic-a"}}

	start := time.Now()
	models, _ := fetchAllModels(config, nil, false)
	elapsed := time.Since(start)

	sequential := time.Duration(len(providers)) * sleep
	assert.Less(t, elapsed, sequential/2,
		"providers should be fetched concurrently: elapsed %v, want well under %v", elapsed, sequential)

	// Deterministic ordering: selected provider first, then the remaining
	// providers alphabetically, then by display name within each group.
	// Re-running with a warm cache (no fetch) must yield the identical order.
	cached, _ := fetchAllModels(&Config{LLM: LLMConfig{Provider: "anthropic", Model: "anthropic-a"}}, nil, false)
	require.Equal(t, len(models), len(cached))
	for i := range models {
		assert.Equal(t, models[i].Provider, cached[i].Provider, "row %d provider", i)
		assert.Equal(t, models[i].ID, cached[i].ID, "row %d id", i)
	}
}

// TestModelMetadataSuffix_ApproxContext verifies approximate context windows are
// prefixed with ~ and exact ones are not.
func TestModelMetadataSuffix_ApproxContext(t *testing.T) {
	assert.Equal(t, " · 200K ctx", modelMetadataSuffix(Model{ContextLength: 200_000}))
	assert.Equal(t, " · ~200K ctx",
		modelMetadataSuffix(Model{ContextLength: 200_000, ContextApprox: true}))
}

// TestFetchModelsForProvider_FillsApproxContext verifies a provider that reports
// no context length gets an approximate value from the registry helper.
func TestFetchModelsForProvider_FillsApproxContext(t *testing.T) {
	court := &mockListModelsCourt{
		resp: &schemas.BifrostListModelsResponse{Data: []schemas.Model{
			{ID: "gpt-4o"},
			{ID: "unknown-model"},
		}},
		approx: map[string]int{"openai:gpt-4o": 128_000},
	}

	result, err := fetchModelsForProvider(&Config{}, court, "openai")
	require.NoError(t, err)
	require.Len(t, result, 2)

	assert.Equal(t, 128_000, result[0].ContextLength)
	assert.True(t, result[0].ContextApprox, "registry-filled context must be marked approximate")
	assert.Contains(t, modelMetadataSuffix(result[0]), "~128K ctx")

	assert.Equal(t, 0, result[1].ContextLength, "unknown models stay without a context")
	assert.False(t, result[1].ContextApprox)
}

// TestFetchModelsForProvider_ProviderContextNotMarkedApprox verifies a
// provider-reported context is never marked approximate.
func TestFetchModelsForProvider_ProviderContextNotMarkedApprox(t *testing.T) {
	ctx := 262_144
	court := &mockListModelsCourt{
		resp: &schemas.BifrostListModelsResponse{Data: []schemas.Model{
			{ID: "claude-3-5-sonnet", ContextLength: &ctx},
		}},
		approx: map[string]int{"anthropic:claude-3-5-sonnet": 200_000},
	}

	result, err := fetchModelsForProvider(&Config{}, court, "anthropic")
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, 262_144, result[0].ContextLength)
	assert.False(t, result[0].ContextApprox)
	assert.Equal(t, " · 262K ctx", modelMetadataSuffix(result[0]))
}

// TestFetchAllModels_CacheReadRederivesActiveStatus verifies that a warm cache
// still reflects a changed active model, without a second network call: the
// status is derived from config at read time, not frozen into the cache.
func TestFetchAllModels_CacheReadRederivesActiveStatus(t *testing.T) {
	resetModelCache()
	defer resetModelCache()
	clearProviderAuthKeys()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	var calls int32
	installCountingFetch(t, &calls, func(provider string) ([]Model, error) {
		return []Model{
			{ID: "claude-3-5-sonnet"},
			{ID: "claude-3-7-sonnet"},
		}, nil
	})

	config := &Config{LLM: LLMConfig{Provider: "anthropic", Model: "claude-3-5-sonnet"}}
	first, _ := fetchAllModels(config, nil, false)
	firstCalls := atomic.LoadInt32(&calls)
	require.Equal(t, "claude-3-5-sonnet", activeModelID(first), "initial active model")

	// Change the active model; a cached read must reflect it with no fetch.
	config.LLM.Model = "claude-3-7-sonnet"
	second, _ := fetchAllModels(config, nil, false)
	assert.Equal(t, firstCalls, atomic.LoadInt32(&calls), "changing the active model must not refetch")
	assert.Equal(t, "claude-3-7-sonnet", activeModelID(second),
		"cached read must re-derive the active model from config")
}

// activeModelID returns the ID of the row carrying the "active" status, or ""
// when there is exactly zero or more than one active row.
func activeModelID(models []Model) string {
	id := ""
	n := 0
	for _, m := range models {
		if m.Status == "active" {
			id = m.ID
			n++
		}
	}
	if n != 1 {
		return ""
	}
	return id
}

// countStatus counts rows carrying the given status.
func countStatus(models []Model, status string) int {
	n := 0
	for _, m := range models {
		if m.Status == status {
			n++
		}
	}
	return n
}

// batchModels extracts a modelsLoadedMsg from a handleModelsCommand tea.Batch.
func batchModels(t *testing.T, cmd tea.Cmd) (modelsLoadedMsg, bool) {
	t.Helper()
	if cmd == nil {
		return modelsLoadedMsg{}, false
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		m, ok := msg.(modelsLoadedMsg)
		return m, ok
	}
	for _, c := range batch {
		if c == nil {
			continue
		}
		if m, ok := c().(modelsLoadedMsg); ok {
			return m, true
		}
	}
	return modelsLoadedMsg{}, false
}

// TestHandleModelsCommand_RefreshArgForcesRefetch verifies that ":model refresh"
// bypasses the warm cache while a bare ":model" serves from it.
func TestHandleModelsCommand_RefreshArgForcesRefetch(t *testing.T) {
	resetModelCache()
	defer resetModelCache()
	clearProviderAuthKeys()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	var calls int32
	installCountingFetch(t, &calls, func(provider string) ([]Model, error) {
		return []Model{{ID: "claude-3-5-sonnet"}}, nil
	})

	model := &TUIModel{config: &Config{LLM: LLMConfig{Provider: "anthropic", Model: "claude-3-5-sonnet"}}, tabs: newTestTabManager()}

	// Warm the cache.
	_, _ = batchModels(t, handleModelsCommand(model, nil))
	warm := atomic.LoadInt32(&calls)

	// A bare :model uses the cache.
	_, _ = batchModels(t, handleModelsCommand(model, nil))
	assert.Equal(t, warm, atomic.LoadInt32(&calls), "bare :model must serve from cache")

	// :model refresh forces a live fetch.
	_, _ = batchModels(t, handleModelsCommand(model, []string{"refresh"}))
	assert.Greater(t, atomic.LoadInt32(&calls), warm, ":model refresh must bypass the cache")
}
