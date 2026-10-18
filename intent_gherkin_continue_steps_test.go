package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/ministers"
	"github.com/afittestide/asimi/storage"
	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// continueTestState holds the test model and mock for "continue command" scenarios.
type continueTestState struct {
	model *TUIModel
	mock  *mockCourtClient
}

func newContinueTestState(t require.TestingT) *continueTestState {
	mock := &mockCourtClient{}
	model := newTestModel(t.(*testing.T))
	model.court = mock
	model.tabs.DismissWelcome()
	// Ensure the default tabs are set up
	model.tabs.SwitchTo(0)
	return &continueTestState{
		model: model,
		mock:  mock,
	}
}

func registerContinueStepDefs(ctx *godog.ScenarioContext, t *testing.T) {
	s := &continueTestState{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		*s = *newContinueTestState(t)
		return ctx, nil
	})

	// --- Givens ---

	ctx.Step(`^a ritual tab exists for edict (\d+)$`, func(edictID int) error {
		channelID := fmt.Sprintf("e%d", edictID)
		s.model.tabs.Add(fmt.Sprintf("Ritual:%s", channelID), "ritual", channelID)
		tab := s.model.tabs.TabByTarget(channelID)
		require.NotNil(t, tab, "ritual tab should exist")
		tab.EdictID = uint(edictID)
		tab.CurrentMinister = "forge"
		s.model.tabs.SwitchToTarget(channelID)
		assert.Equal(t, "ritual", string(s.model.tabs.ActiveTab().Type))
		return nil
	})

	ctx.Step(`^the ritual is paused \(ChatMode = true\)$`, func() error {
		tab := s.model.tabs.ActiveTab()
		tab.ChatMode = true
		return nil
	})

	ctx.Step(`^the ritual is not paused$`, func() error {
		tab := s.model.tabs.ActiveTab()
		tab.ChatMode = false
		return nil
	})

	ctx.Step(`^the edict is active \(not sealed, not cancelled\)$`, func() error {
		s.mock.getEdictFn = func(edictID uint) (*storage.Edict, error) {
			return &storage.Edict{ID: edictID, Intent: "Test intent"}, nil
		}
		s.mock.sealsFn = func() ([]storage.Seal, error) {
			return nil, nil
		}
		return nil
	})

	ctx.Step(`^the edict has the Ruler's seal$`, func() error {
		s.mock.getEdictFn = func(edictID uint) (*storage.Edict, error) {
			return &storage.Edict{ID: edictID, Intent: "Test intent"}, nil
		}
		s.mock.sealsFn = func() ([]storage.Seal, error) {
			return []storage.Seal{
				{MinisterID: ministers.Ruler, SealedAt: time.Now()},
			}, nil
		}
		return nil
	})

	ctx.Step(`^the edict is cancelled$`, func() error {
		now := time.Now()
		s.mock.getEdictFn = func(edictID uint) (*storage.Edict, error) {
			return &storage.Edict{ID: edictID, Intent: "Test intent", CancelledAt: &now}, nil
		}
		return nil
	})

	ctx.Step(`^the edict (\d+) does not exist$`, func(edictID int) error {
		s.mock.getEdictFn = func(id uint) (*storage.Edict, error) {
			return nil, fmt.Errorf("edict not found")
		}
		// Switch to the non-existent edict tab
		channelID := fmt.Sprintf("e%d", edictID)
		s.model.tabs.Add(fmt.Sprintf("Ritual:%s", channelID), "ritual", channelID)
		tab := s.model.tabs.TabByTarget(channelID)
		require.NotNil(t, tab)
		tab.CurrentMinister = "forge"
		tab.ChatMode = false
		s.model.tabs.SwitchToTarget(channelID)
		return nil
	})

	ctx.Step(`^the active tab is not a ritual tab$`, func() error {
		// Switch to the first tab (chancellor by default)
		s.model.tabs.SwitchTo(0)
		tab := s.model.tabs.ActiveTab()
		require.NotNil(t, tab)
		assert.NotEqual(t, "ritual", string(tab.Type))
		return nil
	})

	ctx.Step(`^the court is not active$`, func() error {
		s.model.court = nil
		return nil
	})

	// --- Whens ---

	ctx.Step(`^the Ruler types ":continue"$`, func() error {
		cmd := handleContinueCommand(s.model, []string{})
		if cmd != nil {
			// Execute the command to trigger event publishing
			msg := cmd()
			require.Nil(t, msg, "enactRitualForEdict should return nil message")
		}
		return nil
	})

	// --- Thens ---

	ctx.Step(`^ResumeRitual is called for the channel$`, func() error {
		assert.Len(t, s.mock.resumedChannels, 1, "ResumeRitual should be called once")
		return nil
	})

	ctx.Step(`^ChatMode is cleared$`, func() error {
		tab := s.model.tabs.ActiveTab()
		assert.False(t, tab.ChatMode, "ChatMode should be cleared")
		return nil
	})

	ctx.Step(`^a system message says "([^"]*)"$`, func(expectedText string) error {
		tab := s.model.tabs.ActiveTab()
		if tab == nil || tab.Content.Chat == nil {
			return fmt.Errorf("no active tab or chat")
		}
		for _, msg := range tab.Content.Chat.Messages {
			if msg.Type == MessageTypeSystem && strings.Contains(msg.Content, expectedText) {
				return nil
			}
		}
		return fmt.Errorf("system message containing %q not found", expectedText)
	})

	ctx.Step(`^EventRitualEnacted is published with ritual "([^"]*)" for edict (\d+)$`, func(ritualName string, edictID int) error {
		require.Len(t, s.mock.publishedEvents, 1, "should publish one event")
		assert.Equal(t, storage.EventRitualEnacted, s.mock.publishedEvents[0].eventType)
		name, ok := s.mock.publishedEvents[0].payload["ritual_name"]
		require.True(t, ok, "payload should have ritual_name")
		assert.Equal(t, ritualName, name)
		eid, ok := s.mock.publishedEvents[0].payload["edict_id"]
		require.True(t, ok, "payload should have edict_id")
		assert.Equal(t, uint(edictID), eid)
		return nil
	})

	ctx.Step(`^a toast warning says "([^"]*)"$`, func(msg string) error {
		// Toast is added to the command line — we verify the model was not
		// mutated (no events published, etc.)
		assert.Empty(t, s.mock.publishedEvents, "should not publish any event")
		return nil
	})

	ctx.Step(`^a toast error says "([^"]*)"$`, func(msg string) error {
		assert.Empty(t, s.mock.publishedEvents, "should not publish any event")
		return nil
	})

	ctx.Step(`^no ritual event is published$`, func() error {
		assert.Empty(t, s.mock.publishedEvents, "should not publish any ritual event")
		return nil
	})

	// --- Tab-close aborts the ritual (e921) ---

	ctx.Step(`^a court is active with a recorded CancelTab$`, func() error {
		// newContinueTestState already installs a mockCourtClient that records
		// CancelTab calls; nothing more to wire.
		return nil
	})

	ctx.Step(`^the ritual is actively streaming$`, func() error {
		tab := s.model.tabs.ActiveTab()
		if tab == nil {
			return fmt.Errorf("no active tab")
		}
		tab.Streaming = true
		return nil
	})

	ctx.Step(`^the Ruler types ":q"$`, func() error {
		cmd := handleQuitCommand(s.model, []string{})
		if cmd != nil {
			// Closing a tab is synchronous; a non-nil command would mean the
			// model quit instead of closing a tab.
			return fmt.Errorf(":q returned a command instead of closing the tab")
		}
		return nil
	})

	ctx.Step(`^the ritual tab is closed$`, func() error {
		if tab := s.model.tabs.TabByTarget("e647"); tab != nil {
			return fmt.Errorf("ritual tab e647 should have been closed")
		}
		return nil
	})

	ctx.Step(`^the ritual tab is still open$`, func() error {
		if tab := s.model.tabs.TabByTarget("e647"); tab == nil {
			return fmt.Errorf("ritual tab e647 should still be open")
		}
		return nil
	})

	ctx.Step(`^the ritual is aborted through the court$`, func() error {
		assert.Equal(t, []string{"e647"}, s.mock.cancelledChannels,
			"the ritual must be aborted via CancelTab")
		return nil
	})

	ctx.Step(`^the ritual was not aborted$`, func() error {
		assert.Empty(t, s.mock.cancelledChannels,
			"the ritual must not be aborted")
		return nil
	})

	ctx.Step(`^the ritual was not resumed$`, func() error {
		assert.Empty(t, s.mock.resumedChannels,
			"a running ritual must not be resumed")
		return nil
	})
}
