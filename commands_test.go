package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/ministers"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/worktree"
	"github.com/afittestide/asimi/storage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindCommand(t *testing.T) {
	registry := NewCommandRegistry()

	tests := []struct {
		name            string
		input           string
		expectFound     bool
		expectCommand   string
		expectMatches   int
		expectAmbiguous bool
	}{
		{
			name:          "exact match with colon",
			input:         ":quit",
			expectFound:   true,
			expectCommand: "quit",
			expectMatches: 1,
		},
		{
			name:            "partial match single - qu",
			input:           ":qu",
			expectFound:     false,
			expectMatches:   2, // quit, quitall
			expectAmbiguous: true,
		},
		{
			name:            "partial match single - qui",
			input:           ":qui",
			expectFound:     false,
			expectMatches:   2, // quit, quitall
			expectAmbiguous: true,
		},
		{
			name:          "partial match single - q",
			input:         ":q",
			expectFound:   true,
			expectCommand: "q",
			expectMatches: 1,
		},
		{
			name:          "exact match qa",
			input:         ":qa",
			expectFound:   true,
			expectCommand: "qa",
			expectMatches: 1,
		},
		{
			name:          "exact match quitall",
			input:         ":quitall",
			expectFound:   true,
			expectCommand: "quitall",
			expectMatches: 1,
		},
		{
			name:          "partial match single - h",
			input:         ":h",
			expectFound:   true,
			expectCommand: "help",
			expectMatches: 1,
		},
		{
			name:          "partial match single - n",
			input:         ":n",
			expectFound:   true,
			expectCommand: "new",
			expectMatches: 1,
		},
		{
			name:            "ambiguous match - c",
			input:           ":c",
			expectFound:     false,
			expectMatches:   3, // compact, context, continue
			expectAmbiguous: true,
		},
		{
			name:            "ambiguous match - co",
			input:           ":co",
			expectFound:     false,
			expectMatches:   3, // compact, context, continue
			expectAmbiguous: true,
		},
		{
			name:          "partial disambiguated - com",
			input:         ":com",
			expectFound:   true,
			expectCommand: "compact",
			expectMatches: 1,
		},
		{
			name:            "ambiguous match - con",
			input:           ":con",
			expectFound:     false,
			expectMatches:   2, // context, continue
			expectAmbiguous: true,
		},
		{
			name:          "no match",
			input:         ":xyz",
			expectFound:   false,
			expectMatches: 0,
		},
		{
			name:          "empty input",
			input:         "",
			expectFound:   false,
			expectMatches: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, matches, found := registry.FindCommand(tt.input)

			require.Equal(t, tt.expectFound, found, "found mismatch")
			require.Equal(t, tt.expectMatches, len(matches), "matches count mismatch")

			if tt.expectFound {
				require.Equal(t, tt.expectCommand, cmd.Name, "command name mismatch")
			}

			if tt.expectAmbiguous {
				require.False(t, found, "should not find unique match for ambiguous input")
				require.Greater(t, len(matches), 1, "ambiguous should have multiple matches")
			}
		})
	}
}

func TestNormalizeCommandName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{input: ":help", expected: "help"},
		{input: ":quit", expected: "quit"},
		{input: "", expected: ""},
		{input: ":new", expected: "new"},
		{input: "help", expected: "help"},
		{input: "quit", expected: "quit"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeCommandName(tt.input)
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestHandleQuitCommand_SingleTabQuits verifies that :quit on the last tab
// shuts down and quits the application (Vim semantics).
func TestHandleQuitCommand_SingleTabQuits(t *testing.T) {
	model := newTestModel(t)
	model.tabs.DismissWelcome()
	// Reduce to a single tab to simulate the "last tab" case.
	model.tabs.tabs = model.tabs.tabs[:1]
	model.tabs.activeTab = 0
	require.Equal(t, 1, model.tabs.TabCount())

	cmd := handleQuitCommand(model, []string{})

	require.NotNil(t, cmd, "quit on the last tab must return a tea.Cmd")
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	require.True(t, isQuit, "expected tea.QuitMsg, got %T", msg)
	// The tab must not have been closed by :quit on the last tab.
	require.Equal(t, 1, model.tabs.TabCount(), "single-tab :quit must not close the tab")
}

// TestHandleQuitCommand_MultiTabClosesTab verifies that :quit with multiple
// tabs open closes the current tab (Vim semantics) instead of quitting.
func TestHandleQuitCommand_MultiTabClosesTab(t *testing.T) {
	model := newTestModel(t)
	model.tabs.DismissWelcome()
	initialTabs := model.tabs.TabCount()
	require.GreaterOrEqual(t, initialTabs, 1)

	model.tabs.Add("chat2", TabType("chat"), "target2")
	require.Equal(t, initialTabs+1, model.tabs.TabCount())
	// The new tab becomes active (Add switches to it).
	require.Equal(t, "target2", model.tabs.ActiveTab().Target)

	cmd := handleQuitCommand(model, []string{})

	// Closing a tab is synchronous: no tea.Cmd is returned.
	require.Nil(t, cmd, "multi-tab :quit should return no tea.Cmd")
	require.Equal(t, initialTabs, model.tabs.TabCount(), "multi-tab :quit must close the active tab")
	// Close() switches to the adjacent tab before removing the closed one.
	assert.Equal(t, "chancellor", model.tabs.ActiveTab().Target)

	// A success toast must have been shown.
	toasts := model.commandLine.ActiveToasts()
	require.Len(t, toasts, 1)
	assert.Equal(t, "Tab closed", toasts[0].Message)
}

// TestHandleQuitCommand_MultiTabStreamingShowsError verifies that :quit refuses
// to close a streaming tab and surfaces the error via a toast, leaving the tab open.
func TestHandleQuitCommand_MultiTabStreamingShowsError(t *testing.T) {
	model := newTestModel(t)
	model.tabs.DismissWelcome()
	initialTabs := model.tabs.TabCount()

	model.tabs.Add("chat2", TabType("chat"), "target2")
	model.tabs.ActiveTab().Streaming = true

	cmd := handleQuitCommand(model, []string{})

	require.Nil(t, cmd, "refusing to close a streaming tab returns no tea.Cmd")
	require.Equal(t, initialTabs+1, model.tabs.TabCount(), "streaming tab must not be closed")

	toasts := model.commandLine.ActiveToasts()
	require.Len(t, toasts, 1)
	assert.Contains(t, toasts[0].Message, "cannot close tab while streaming")
	assert.Equal(t, "error", toasts[0].Type)
}

// TestHandleQuitAllCommand_QuitsRegardlessOfTabs verifies that :qa and :quitall
// always quit the application even when multiple tabs are open.
func TestHandleQuitAllCommand_QuitsRegardlessOfTabs(t *testing.T) {
	model := newTestModel(t)
	model.tabs.DismissWelcome()
	initialTabs := model.tabs.TabCount()

	model.tabs.Add("chat2", TabType("chat"), "target2")
	require.Equal(t, initialTabs+1, model.tabs.TabCount())

	cmd := handleQuitAllCommand(model, []string{})

	require.NotNil(t, cmd, "quitall must return a tea.Cmd")
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	require.True(t, isQuit, "expected tea.QuitMsg, got %T", msg)
	// The tabs are left untouched: :qa quits the whole application.
	require.Equal(t, initialTabs+1, model.tabs.TabCount(), ":qa must not close individual tabs")
}

// TestQuitCommandAliasesDispatch verifies that :q, :qa and :quitall map to the
// intended handlers so the behavior is reachable through the registered commands.
func TestQuitCommandAliasesDispatch(t *testing.T) {
	registry := NewCommandRegistry()

	quitCmd, ok := registry.GetCommand("q")
	require.True(t, ok)
	require.Equal(t, "q", quitCmd.Name)

	qaCmd, ok := registry.GetCommand("qa")
	require.True(t, ok)
	require.Equal(t, "qa", qaCmd.Name)

	quitAllCmd, ok := registry.GetCommand("quitall")
	require.True(t, ok)
	require.Equal(t, "quitall", quitAllCmd.Name)

	// All three must still resolve to a registered handler.
	require.NotNil(t, quitCmd.Handler)
	require.NotNil(t, qaCmd.Handler)
	require.NotNil(t, quitAllCmd.Handler)
}

// TestModelCommandAlias verifies that both :models and its :model alias are
// registered and resolve to the same picker handler.
func TestModelCommandAlias(t *testing.T) {
	registry := NewCommandRegistry()

	modelsCmd, ok := registry.GetCommand("models")
	require.True(t, ok, ":models must be registered")
	modelCmd, ok := registry.GetCommand("model")
	require.True(t, ok, ":model alias must be registered")

	require.Equal(t, "model", modelCmd.Name)
	assert.Contains(t, modelCmd.Description, "refresh")
	assert.Equal(t, "Select AI model (alias; :model refresh for a live refetch)", modelCmd.Description)
	require.NotNil(t, modelCmd.Handler)
	require.NotNil(t, modelsCmd.Handler)
}

func TestHandleInitCommand(t *testing.T) {
	skipIfNotCI(t)
	// Setup a temporary directory for the test
	tmpDir := t.TempDir()

	// Setup a mock model with no session (simulates no active session state)
	mockTUI := &TUIModel{
		status: StatusComponent{
			repoInfo: &repo.RepoInfo{ProjectRoot: tmpDir},
		},
	}

	t.Run("No session returns error message", func(t *testing.T) {
		cmd := handleInitCommand(mockTUI, []string{})
		msg := cmd()

		// Without a session, we should get a showContextMsg with error content
		sysMsg, ok := msg.(showContextMsg)
		require.True(t, ok, "Expected showContextMsg when no session, got %T", msg)
		require.Contains(t, sysMsg.content, "No model connection")
	})

	t.Run("Clear mode with no session removes files", func(t *testing.T) {
		// Create the files that clearAsimiFiles should remove
		err := os.MkdirAll(filepath.Join(tmpDir, ".agents"), 0755)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(tmpDir, ".agents/test"), []byte("test"), 0644)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("# Agents"), 0644)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(tmpDir, "Justfile"), []byte("test:"), 0644)
		require.NoError(t, err)

		cmd := handleInitCommand(mockTUI, []string{"clear"})
		msg := cmd()

		// Should still get the "no session" error after clearing files
		sysMsg, ok := msg.(showContextMsg)
		require.True(t, ok, "Expected showContextMsg when no session, got %T", msg)
		require.Contains(t, sysMsg.content, "No model connection")

		// But files should still be removed
		_, err = os.Stat(filepath.Join(tmpDir, ".agents"))
		require.True(t, os.IsNotExist(err), ".agents should be removed")
		_, err = os.Stat(filepath.Join(tmpDir, "AGENTS.md"))
		require.True(t, os.IsNotExist(err), "AGENTS.md should be removed")
		_, err = os.Stat(filepath.Join(tmpDir, "Justfile"))
		require.True(t, os.IsNotExist(err), "Justfile should be removed")
	})

	// TODO: Tests for actual init workflow require a full court setup with a configured session.
	// These tests are skipped until proper integration test infrastructure is added.
	t.Run("Clean directory - skipped without session", func(t *testing.T) {
		t.Skip("Requires court session setup - see integration tests")
	})

	t.Run("Some files exist - skipped without session", func(t *testing.T) {
		t.Skip("Requires court session setup - see integration tests")
	})

	t.Run("All files exist - skipped without session", func(t *testing.T) {
		t.Skip("Requires court session setup - see integration tests")
	})

	t.Run("Clear mode - skipped without session", func(t *testing.T) {
		t.Skip("Requires court session setup - see integration tests")
	})
}

func TestClearAsimiFiles(t *testing.T) {
	// Setup a temporary directory for the test
	tmpDir := t.TempDir()

	mockTUI := &TUIModel{
		status: StatusComponent{
			repoInfo: &repo.RepoInfo{ProjectRoot: tmpDir},
		},
	}

	t.Run("Clears all infrastructure files", func(t *testing.T) {
		// Create the files that clearAsimiFiles should remove
		err := os.MkdirAll(filepath.Join(tmpDir, ".agents"), 0755)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(tmpDir, ".agents/test"), []byte("test"), 0644)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("# Agents"), 0644)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(tmpDir, "Justfile"), []byte("test:"), 0644)
		require.NoError(t, err)

		errors := clearAsimiFiles(mockTUI)
		require.Empty(t, errors, "Expected no errors, got: %v", errors)

		// Verify files are removed
		_, err = os.Stat(filepath.Join(tmpDir, ".agents"))
		require.True(t, os.IsNotExist(err), ".agents should be removed")
		_, err = os.Stat(filepath.Join(tmpDir, "AGENTS.md"))
		require.True(t, os.IsNotExist(err), "AGENTS.md should be removed")
		_, err = os.Stat(filepath.Join(tmpDir, "Justfile"))
		require.True(t, os.IsNotExist(err), "Justfile should be removed")
	})

	t.Run("Handles missing files gracefully", func(t *testing.T) {
		// Ensure no files exist
		os.RemoveAll(filepath.Join(tmpDir, ".agents"))
		os.Remove(filepath.Join(tmpDir, "AGENTS.md"))
		os.Remove(filepath.Join(tmpDir, "Justfile"))

		errors := clearAsimiFiles(mockTUI)
		require.Empty(t, errors, "Expected no errors for missing files, got: %v", errors)
	})

	t.Run("Uses projectRoot not CWD", func(t *testing.T) {
		// This is the core bug fix: clearAsimiFiles must use projectRoot, not CWD.
		// Create files in projectRoot (tmpDir) while CWD stays elsewhere.
		projectDir := filepath.Join(tmpDir, "project")
		err := os.MkdirAll(projectDir, 0755)
		require.NoError(t, err)
		err = os.MkdirAll(filepath.Join(projectDir, ".agents"), 0755)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(projectDir, ".agents/test"), []byte("test"), 0644)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(projectDir, "AGENTS.md"), []byte("# Agents"), 0644)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(projectDir, "Justfile"), []byte("test:"), 0644)
		require.NoError(t, err)

		// Point the model's repoInfo to the project subdirectory
		mockTUIWithSubdir := &TUIModel{
			status: StatusComponent{
				repoInfo: &repo.RepoInfo{ProjectRoot: projectDir},
			},
		}

		errors := clearAsimiFiles(mockTUIWithSubdir)
		require.Empty(t, errors, "Expected no errors, got: %v", errors)

		// Files in projectDir should be removed
		_, err = os.Stat(filepath.Join(projectDir, ".agents"))
		require.True(t, os.IsNotExist(err), ".agents in projectDir should be removed")
		_, err = os.Stat(filepath.Join(projectDir, "AGENTS.md"))
		require.True(t, os.IsNotExist(err), "AGENTS.md in projectDir should be removed")
		_, err = os.Stat(filepath.Join(projectDir, "Justfile"))
		require.True(t, os.IsNotExist(err), "Justfile in projectDir should be removed")
	})
}

func TestRunInitGuardrails(t *testing.T) {
	skipIfNotCI(t)
	// Setup a temporary directory for the test
	tmpDir := t.TempDir()
	originalWd, err := os.Getwd()
	require.NoError(t, err)
	err = os.Chdir(tmpDir)
	require.NoError(t, err)
	defer func() {
		err := os.Chdir(originalWd)
		if err != nil {
			t.Logf("Failed to change back to original directory: %v", err)
		}
	}()

	// Setup a mock model
	mockTUI := &TUIModel{}

	t.Run("Missing files", func(t *testing.T) {
		// Run guardrails with no files present
		cmd := verifyInit(mockTUI, nil)
		msg := cmd()

		// When there are errors, runInitGuardrails returns a startConversationMsg to retry
		retryMsg, ok := msg.(startConversationMsg)
		require.True(t, ok, "Expected startConversationMsg for retry, got type: %T", msg)
		require.Contains(t, retryMsg.prompt, "❌ AGENTS.md was not created")
		require.Contains(t, retryMsg.prompt, "❌ Justfile was not created")
		require.Contains(t, retryMsg.prompt, "Issues found verifying initialization")
		require.True(t, retryMsg.RunOnHost, "Expected RunOnHost to be true")
	})

	t.Run("Files present", func(t *testing.T) {
		// Create the required files
		err := os.WriteFile("AGENTS.md", []byte("# Test AGENTS.md"), 0644)
		require.NoError(t, err)
		err = os.WriteFile("Justfile", []byte("default:\n\techo 'hello'"), 0644)
		require.NoError(t, err)

		// Run guardrails
		cmd := verifyInit(mockTUI, nil)
		msg := cmd()

		// When just commands fail (which they will in test), it returns startConversationMsg
		// When all passes, it returns showContextMsg
		switch m := msg.(type) {
		case startConversationMsg:
			// Just commands failed, which is expected in test environment
			require.Contains(t, m.prompt, "AGENTS.md created")
			require.Contains(t, m.prompt, "Justfile created")
			require.True(t, m.RunOnHost, "Expected RunOnHost to be true")
		case showContextMsg:
			// All passed (unlikely in test environment)
			require.Contains(t, m.content, "AGENTS.md created")
			require.Contains(t, m.content, "Justfile created")
		default:
			t.Fatalf("Expected startConversationMsg or showContextMsg, got: %T", msg)
		}

		// Clean up
		os.Remove("AGENTS.md")
		os.Remove("Justfile")
	})
}

func TestVerifyInitWithRetryNilRepoInfo(t *testing.T) {
	// Verify that verifyInitWithRetry does not panic when model.status.repoInfo is nil.
	// This can happen when the function is called with a bare TUIModel (e.g., in tests
	// or before repo detection completes).
	t.Run("nil repoInfo does not panic", func(t *testing.T) {
		tmpDir := t.TempDir()
		originalWd, err := os.Getwd()
		require.NoError(t, err)
		err = os.Chdir(tmpDir)
		require.NoError(t, err)
		defer os.Chdir(originalWd)

		mockTUI := &TUIModel{}

		// This should not panic even though repoInfo is nil
		cmd := verifyInit(mockTUI, nil)
		msg := cmd()

		// Without files present, we expect a startConversationMsg for retry
		retryMsg, ok := msg.(startConversationMsg)
		require.True(t, ok, "Expected startConversationMsg for retry, got type: %T", msg)
		require.Contains(t, retryMsg.prompt, "❌ AGENTS.md was not created")
		require.Contains(t, retryMsg.prompt, "❌ Justfile was not created")
	})

	t.Run("nil repoInfo with files present returns failure", func(t *testing.T) {
		tmpDir := t.TempDir()
		originalWd, err := os.Getwd()
		require.NoError(t, err)
		err = os.Chdir(tmpDir)
		require.NoError(t, err)
		defer os.Chdir(originalWd)

		// Create required files so the function proceeds past the file check
		err = os.WriteFile("AGENTS.md", []byte("# Test"), 0644)
		require.NoError(t, err)
		err = os.WriteFile("Justfile", []byte("default:\n\techo hello"), 0644)
		require.NoError(t, err)

		mockTUI := &TUIModel{}
		cmd := verifyInit(mockTUI, nil)
		msg := cmd()

		// With nil repoInfo and files present, the function should handle
		// the missing repoInfo gracefully (return retry or error message)
		switch m := msg.(type) {
		case startConversationMsg:
			// Retry message is expected
			_ = m
		case showContextMsg:
			// Error/context message is also acceptable
			_ = m
		default:
			t.Fatalf("Expected startConversationMsg or showContextMsg, got: %T", msg)
		}
	})
}

// TestHandleInitCommand_AutoDerivesSlug tests that handleInitCommand auto-derives
// the project slug from repoInfo when config.Court.Project is empty.
func TestHandleInitCommand_AutoDerivesSlug(t *testing.T) {
	skipIfNotCI(t)
	tmpDir := t.TempDir()

	mock := &mockCourtClient{}
	mockTUI := &TUIModel{
		config: &Config{
			Court: config.CourtConfig{
				Project: "", // empty — should trigger auto-derivation
			},
		},
		status: StatusComponent{
			repoInfo: &repo.RepoInfo{
				ProjectRoot: tmpDir,
				Slug:        "owner/myrepo",
			},
		},
		court: mock,
	}

	cmd := handleInitCommand(mockTUI, []string{})
	// createInitEdict returns nil (no tea.Cmd) but publishes an event
	require.Nil(t, cmd, "createInitEdict returns nil cmd")

	// The command chain calls createInitEdict → CreateEdictSilent → raiseCourtEvent
	// Verify the event was published
	require.Len(t, mock.publishedEvents, 1, "expected EventRitualEnacted to be published")
	assert.Equal(t, storage.EventRitualEnacted, mock.publishedEvents[0].eventType)
	assert.Equal(t, "project-init", mock.publishedEvents[0].payload["ritual_name"])

	// Verify the project name was saved to .agents/asimi.conf
	confPath := filepath.Join(tmpDir, ".agents", "asimi.conf")
	data, err := os.ReadFile(confPath)
	require.NoError(t, err, "config file should exist")
	require.Contains(t, string(data), "owner/myrepo",
		"config should contain the auto-derived slug")
}

// TestHandleInitCommand_NoSlugReturnsError tests that handleInitCommand returns
// an error message when both config.Court.Project and repoInfo.Slug are empty.
func TestHandleInitCommand_NoSlugReturnsError(t *testing.T) {
	skipIfNotCI(t)
	tmpDir := t.TempDir()

	mock := &mockCourtClient{}
	mockTUI := &TUIModel{
		config: &Config{
			Court: config.CourtConfig{
				Project: "",
			},
		},
		status: StatusComponent{
			repoInfo: &repo.RepoInfo{
				ProjectRoot: tmpDir,
				Slug:        "", // no slug — no git remote
			},
		},
		court: mock,
	}

	cmd := handleInitCommand(mockTUI, []string{})
	require.NotNil(t, cmd)
	msg := cmd()

	sysMsg, ok := msg.(showContextMsg)
	require.True(t, ok, "Expected showContextMsg when no slug, got %T", msg)
	assert.Contains(t, sysMsg.content, "No git remote found")

	// No events should have been published
	assert.Empty(t, mock.publishedEvents)
}

// TestHandleInitCommand_ProjectAlreadySet tests that handleInitCommand proceeds
// directly to createInitEdict when config.Court.Project is already set.
func TestHandleInitCommand_ProjectAlreadySet(t *testing.T) {
	skipIfNotCI(t)
	tmpDir := t.TempDir()

	mock := &mockCourtClient{}
	mockTUI := &TUIModel{
		config: &Config{
			Court: config.CourtConfig{
				Project: "existing/project",
			},
		},
		status: StatusComponent{
			repoInfo: &repo.RepoInfo{
				ProjectRoot: tmpDir,
				Slug:        "should/not/be/used",
			},
		},
		court: mock,
	}

	cmd := handleInitCommand(mockTUI, []string{})
	// createInitEdict returns nil (no tea.Cmd) but publishes an event
	require.Nil(t, cmd, "createInitEdict returns nil cmd")

	// Verify the event was published
	require.Len(t, mock.publishedEvents, 1, "expected EventRitualEnacted to be published")
	assert.Equal(t, storage.EventRitualEnacted, mock.publishedEvents[0].eventType)

	// Config file should NOT have been created (saveProjectNameAndInit was skipped)
	_, err := os.Stat(filepath.Join(tmpDir, ".agents", "asimi.conf"))
	assert.True(t, os.IsNotExist(err), "config file should not exist when project already set")
}

// TestSaveProjectNameAndInit tests the saveProjectNameAndInit helper directly.
func TestSaveProjectNameAndInit(t *testing.T) {
	skipIfNotCI(t)
	tmpDir := t.TempDir()

	mock := &mockCourtClient{}
	mockTUI := &TUIModel{
		config: &Config{
			Court: config.CourtConfig{
				Project: "",
			},
		},
		status: StatusComponent{
			repoInfo: &repo.RepoInfo{
				ProjectRoot: tmpDir,
			},
		},
		court: mock,
	}

	cmd := saveProjectNameAndInit(mockTUI, "test-org/test-repo")

	// Verify config file was created with the project name
	confPath := filepath.Join(tmpDir, ".agents", "asimi.conf")
	data, err := os.ReadFile(confPath)
	require.NoError(t, err, "config file should exist")
	assert.Contains(t, string(data), "test-org/test-repo")

	// Verify event was published (createInitEdict runs)
	require.Len(t, mock.publishedEvents, 1)
	assert.Equal(t, storage.EventRitualEnacted, mock.publishedEvents[0].eventType)
	assert.Equal(t, "project-init", mock.publishedEvents[0].payload["ritual_name"])

	// cmd is nil from createInitEdict
	assert.Nil(t, cmd)
}

// testModelForSwitch builds a model whose repoInfo points at a temp project
// root with a fake court recording SetContext/EnsureEdictWorktree calls.
func testModelForSwitch(t *testing.T) (*TUIModel, *mockCourtClient, string) {
	t.Helper()
	root := t.TempDir()
	mock := &mockCourtClient{}

	prev := resolveSwitchTarget
	resolveSwitchTarget = func(_ context.Context, gotRoot, target string) (worktree.ResolvedTarget, error) {
		switch {
		case target == "main" || target == "master":
			return worktree.ResolvedTarget{Branch: "main"}, nil
		case strings.HasPrefix(target, "e"):
			var id uint
			_, err := fmt.Sscanf(target, "e%d", &id)
			if err != nil {
				return worktree.ResolvedTarget{}, err
			}
			return worktree.ResolvedTarget{
				Branch:      worktree.Branch(id),
				WorktreeRel: worktree.RelPath(id),
				IsWorktree:  true,
				EdictID:     id,
			}, nil
		default:
			return worktree.ResolvedTarget{Branch: target}, nil
		}
	}
	t.Cleanup(func() { resolveSwitchTarget = prev })

	model := newTestModel(t)
	model.court = mock
	model.status.SetRepoInfo(&repo.RepoInfo{
		ProjectRoot: root,
		Branch:      "main",
		HasVCS:      true,
		IsMain:      true,
	})
	model.repoInfo = model.status.repoInfo
	model.tabs.DismissWelcome()
	return model, mock, root
}

func TestHandleSwitchCommand_RequiresOneArg(t *testing.T) {
	model, _, _ := testModelForSwitch(t)

	require.Nil(t, handleSwitchCommand(model, nil))
	toasts := model.commandLine.ActiveToasts()
	require.Len(t, toasts, 1)
	assert.Contains(t, toasts[0].Message, "Usage: :switch")
	assert.Equal(t, "error", toasts[0].Type)
}

func TestHandleSwitchCommand_RefusesWhileStreaming(t *testing.T) {
	model, mock, _ := testModelForSwitch(t)
	model.tabs.Add("chat2", TabType("chat"), "target2")
	model.tabs.ActiveTab().Streaming = true

	require.Nil(t, handleSwitchCommand(model, []string{"main"}))

	toasts := model.commandLine.ActiveToasts()
	require.Len(t, toasts, 1)
	assert.Equal(t, "Cannot switch while a tab is streaming", toasts[0].Message)
	assert.Equal(t, "error", toasts[0].Type)
	assert.Empty(t, mock.setContextCalls, "no SetContext may happen while streaming")
	assert.Equal(t, "main", model.repoInfo.Branch, "repoInfo must be untouched on refusal")
}

func TestHandleSwitchCommand_Main(t *testing.T) {
	model, mock, root := testModelForSwitch(t)
	// Pretend the court is currently in a worktree.
	model.repoInfo.WorktreePath = filepath.Join(".agents", "worktrees", "e1")
	model.repoInfo.IsWorktree = true
	model.repoInfo.IsMain = false

	cmd := handleSwitchCommand(model, []string{"main"})
	require.NotNil(t, cmd, ":switch must return a tea.Cmd that re-targets the court")

	assert.Equal(t, "main", model.repoInfo.Branch)
	assert.Equal(t, "", model.repoInfo.WorktreePath)
	assert.False(t, model.repoInfo.IsWorktree)
	assert.True(t, model.repoInfo.IsMain)
	assert.Empty(t, mock.ensuredWorktreeIDs, "main must not create a worktree")

	msg := cmd()
	assert.IsType(t, repoSwitchedMsg{}, msg)
	require.Len(t, mock.setContextCalls, 1, "SetContext must be called once")
	assert.Equal(t, root, mock.setContextCalls[0].ProjectRoot)
	assert.Equal(t, "main", mock.setContextCalls[0].Branch)
	assert.Equal(t, "", mock.setContextCalls[0].WorktreePath)
}

func TestHandleSwitchCommand_EdictEnsuresMissingWorktree(t *testing.T) {
	model, mock, root := testModelForSwitch(t)

	cmd := handleSwitchCommand(model, []string{"e999"})
	require.NotNil(t, cmd)

	require.Equal(t, []uint{999}, mock.ensuredWorktreeIDs, "missing worktree must be created")
	assert.Equal(t, "asimi/e999", model.repoInfo.Branch)
	assert.Equal(t, filepath.Join(".agents", "worktrees", "e999"), model.repoInfo.WorktreePath)
	assert.True(t, model.repoInfo.IsWorktree)
	assert.False(t, model.repoInfo.IsMain)

	msg := cmd()
	assert.IsType(t, repoSwitchedMsg{}, msg)
	require.Len(t, mock.setContextCalls, 1)
	assert.Equal(t, root, mock.setContextCalls[0].ProjectRoot)
	assert.Equal(t, "asimi/e999", mock.setContextCalls[0].Branch)
	assert.Equal(t, filepath.Join(".agents", "worktrees", "e999"), mock.setContextCalls[0].WorktreePath)
}

func TestHandleSwitchCommand_EdictReusesExistingWorktree(t *testing.T) {
	model, mock, root := testModelForSwitch(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "worktrees", "e999"), 0o755))

	cmd := handleSwitchCommand(model, []string{"e999"})
	require.NotNil(t, cmd)
	assert.Empty(t, mock.ensuredWorktreeIDs, "existing worktree must be reused without Ensure")
	assert.Equal(t, "asimi/e999", model.repoInfo.Branch)
}

func TestHandleSwitchCommand_UnknownBranchFallsBackToBranchString(t *testing.T) {
	model, mock, root := testModelForSwitch(t)

	cmd := handleSwitchCommand(model, []string{"feature/x"})
	require.NotNil(t, cmd)

	assert.Equal(t, "feature/x", model.repoInfo.Branch)
	assert.Equal(t, "", model.repoInfo.WorktreePath)
	assert.False(t, model.repoInfo.IsWorktree)
	assert.False(t, model.repoInfo.IsMain)

	msg := cmd()
	assert.IsType(t, repoSwitchedMsg{}, msg)
	require.Len(t, mock.setContextCalls, 1)
	assert.Equal(t, root, mock.setContextCalls[0].ProjectRoot)
	assert.Equal(t, "feature/x", mock.setContextCalls[0].Branch)
}

func TestHandleSwitchCommand_ResetsTabs(t *testing.T) {
	model, _, _ := testModelForSwitch(t)
	model.tabs.Add("chat2", TabType("chat"), "target2")
	require.Equal(t, len(ministers.DefaultTabIDs)+1, model.tabs.TabCount())

	require.NotNil(t, handleSwitchCommand(model, []string{"main"}))

	assert.Equal(t, len(ministers.DefaultTabIDs), model.tabs.TabCount(), "tabs must reset to defaults")
	assert.Nil(t, model.tabs.TabByTarget("target2"), "extra tabs must be discarded")
}
