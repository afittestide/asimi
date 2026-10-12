package main

import (
	"strings"
	"testing"
	"time"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func TestShortenProviderModel(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		expected string
	}{
		{
			name:     "Claude Haiku 4.5",
			provider: "anthropic",
			model:    "Claude-Haiku-4.5",
			expected: "Claude-Haiku-4.5",
		},
		{
			name:     "Claude 3.5 Sonnet",
			provider: "anthropic",
			model:    "Claude 3.5 Sonnet",
			expected: "Claude-3.5-Sonnet",
		},
		{
			name:     "claude-3-5-sonnet-20241022",
			provider: "anthropic",
			model:    "claude-3-5-sonnet-20241022",
			expected: "Claude-3.5-Sonnet",
		},
		{
			name:     "claude-3-haiku-20240307",
			provider: "anthropic",
			model:    "claude-3-haiku-20240307",
			expected: "Claude-3-Haiku",
		},
		{
			name:     "claude-3-5-haiku-latest",
			provider: "anthropic",
			model:    "claude-3-5-haiku-latest",
			expected: "Claude-3.5-Haiku",
		},
		{
			name:     "GPT-4 Turbo",
			provider: "openai",
			model:    "gpt-4-turbo",
			expected: "GPT-4T",
		},
		{
			name:     "GPT-3.5",
			provider: "openai",
			model:    "gpt-3.5-turbo",
			expected: "GPT-3.5",
		},
		{
			name:     "Gemini Pro",
			provider: "google",
			model:    "gemini-pro",
			expected: "Gemini-Pro",
		},
		{
			name:     "Gemini Flash",
			provider: "googleai",
			model:    "gemini-1.5-flash",
			expected: "Gemini-Flash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shortenProviderModel(tt.provider, tt.model)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestStatusComponentIcon(t *testing.T) {
	tests := []struct {
		name     string
		verified bool
		hasError bool
		expected string
	}{
		{name: "error takes priority", verified: true, hasError: true, expected: "❌"},
		{name: "error without verified", verified: false, hasError: true, expected: "❌"},
		{name: "verified shows check", verified: true, hasError: false, expected: "✅"},
		{name: "unverified shows question", verified: false, hasError: false, expected: "❓"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := StatusComponent{Verified: tt.verified, HasError: tt.hasError}
			require.Equal(t, tt.expected, s.getStatusIcon())
		})
	}
}

func TestSetVerified(t *testing.T) {
	s := StatusComponent{}
	require.False(t, s.Verified)
	s.SetVerified()
	require.True(t, s.Verified)
}

func TestRenderRightSectionIcon(t *testing.T) {
	tests := []struct {
		name     string
		verified bool
		hasError bool
		icon     string
	}{
		{name: "unverified shows question icon", verified: false, hasError: false, icon: "❓"},
		{name: "verified shows check icon", verified: true, hasError: false, icon: "✅"},
		{name: "error shows x icon", verified: false, hasError: true, icon: "❌"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := StatusComponent{
				Provider: "anthropic",
				Model:    "claude-3-5-sonnet-20241022",
				Verified: tt.verified,
				HasError: tt.hasError,
				Style:    lipgloss.NewStyle(),
			}
			rendered := s.renderRightSection()
			// Strip ANSI escape codes by checking icon presence
			require.Contains(t, rendered, tt.icon)
		})
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{name: "zero", duration: 0, expected: "00:00"},
		{name: "seconds", duration: 5 * time.Second, expected: "00:05"},
		{name: "minutes and seconds", duration: 2*time.Minute + 7*time.Second, expected: "02:07"},
		{name: "ten minutes", duration: 10 * time.Minute, expected: "10:00"},
		{name: "past an hour", duration: time.Hour + 3*time.Minute + 4*time.Second, expected: "1:03:04"},
		{name: "negative clamps to zero", duration: -5 * time.Second, expected: "00:00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, formatElapsed(tt.duration))
		})
	}
}

func TestCommandStopwatch(t *testing.T) {
	s := StatusComponent{Style: lipgloss.NewStyle()}
	require.False(t, s.commandRunning)

	// Not running: no stopwatch in the left section.
	require.NotContains(t, s.renderLeftSection(), "⏱")

	s.StartCommand()
	require.True(t, s.commandRunning)
	require.False(t, s.commandStartTime.IsZero())
	require.Contains(t, s.renderLeftSection(), "⏱ 00:00")

	s.StopCommand()
	require.False(t, s.commandRunning)
	require.True(t, s.commandStartTime.IsZero())
	require.NotContains(t, s.renderLeftSection(), "⏱")
}

func TestCommandStopwatchLeftOfShellIndicator(t *testing.T) {
	s := StatusComponent{
		Style:            lipgloss.NewStyle(),
		repoInfo:         &repo.RepoInfo{Branch: "main"},
		commandRunning:   true,
		commandStartTime: time.Now(),
	}
	rendered := s.renderLeftSection()
	require.Contains(t, rendered, "⏱")
	require.Less(t, strings.Index(rendered, "⏱"), strings.Index(rendered, "🏖️"),
		"stopwatch should render left of the shell/system indicator")
}

// TestCommandTimerCmd_NoTickWhenIdle verifies the stopwatch driver is a no-op
// when no command is in flight.
func TestCommandTimerCmd_NoTickWhenIdle(t *testing.T) {
	model := newTestModel(t)
	require.False(t, model.status.commandRunning)
	require.Nil(t, model.commandTimerCmd(),
		"no timer command should be scheduled while no shell command runs")
}

// TestCommandTimerCmd_UsesConfiguredInterval verifies the refresh interval is
// driven by UI.CommandTimerInterval rather than a hardcoded value.
func TestCommandTimerCmd_UsesConfiguredInterval(t *testing.T) {
	model := newTestModel(t)
	// A nanosecond interval makes the scheduled tick fire immediately, proving
	// the configured value (not the 3s fallback) is the one in effect.
	model.config.UI.CommandTimerInterval = time.Nanosecond
	model.status.StartCommand()

	cmd := model.commandTimerCmd()
	require.NotNil(t, cmd, "running command should schedule a stopwatch tick")

	msg := cmd()
	_, ok := msg.(commandTimerTickMsg)
	require.True(t, ok, "scheduled command should produce a commandTimerTickMsg")
}

// TestCommandTimerTick_RearmsWhileRunning verifies the tick keeps rescheduling
// itself so the elapsed display advances while a command is in flight.
func TestCommandTimerTick_RearmsWhileRunning(t *testing.T) {
	model := newTestModel(t)
	model.config.UI.CommandTimerInterval = time.Nanosecond
	model.status.StartCommand()

	_, cmd := model.handleCustomMessages(commandTimerTickMsg{})
	require.NotNil(t, cmd, "a tick received while running should schedule the next tick")

	msg := cmd()
	_, ok := msg.(commandTimerTickMsg)
	require.True(t, ok, "re-armed command should produce another commandTimerTickMsg")
}

// TestCommandTimerTick_StopsWhenFinished verifies the refresh loop terminates:
// once the command completes, the tick handler must not reschedule.
func TestCommandTimerTick_StopsWhenFinished(t *testing.T) {
	model := newTestModel(t)
	model.status.StartCommand()
	model.status.StopCommand()

	_, cmd := model.handleCustomMessages(commandTimerTickMsg{})
	require.Nil(t, cmd, "a tick received after completion must not reschedule")
}
