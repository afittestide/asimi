package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/afittestide/asimi/internal/repo"
	"github.com/charmbracelet/lipgloss"
)

// StatusComponent represents the status bar component
type StatusComponent struct {
	Provider    string
	Model       string
	Connected   bool
	HasError    bool // Track if there's a model error
	Verified    bool // True once a session confirms the provider works
	Width       int
	Style       lipgloss.Style
	repoInfo    *repo.RepoInfo // Git repository information
	mode        string
	ViPendingOp string

	// Waiting indicator
	waitingForResponse bool
	waitingSince       time.Time
	ContainerID        string

	// In-flight shell command stopwatch
	commandRunning   bool
	commandStartTime time.Time

	// Stream rate tracking
	streamCharsTotal int
	streamStartTime  time.Time
	streamRate       float64

	// Context usage percentage (updated by TUI on stream/tab switch)
	ContextPercent float64
}

// NewStatusComponent creates a new status component
func NewStatusComponent(width int) StatusComponent {
	return StatusComponent{
		Width: width,
		Style: lipgloss.NewStyle().
			Foreground(globalTheme.TextColor),
		mode: "INSERT", // start in insert mode
	}
}

// SetProvider sets the current provider and model
func (s *StatusComponent) SetProvider(provider, model string, connected bool) {
	s.Provider = provider
	s.Model = model
	s.Connected = connected
}

// SetRepoInfo sets the repository information
func (s *StatusComponent) SetRepoInfo(repoInfo *repo.RepoInfo) {
	s.repoInfo = repoInfo
}

// StartWaiting marks the status component as waiting for a model response
func (s *StatusComponent) StartWaiting() {
	s.waitingForResponse = true
	s.waitingSince = time.Now()
}

// StopWaiting clears the waiting indicator
func (s *StatusComponent) StopWaiting() {
	s.waitingForResponse = false
}

// StartCommand marks a shell command as in flight and starts its stopwatch
func (s *StatusComponent) StartCommand() {
	s.commandRunning = true
	s.commandStartTime = time.Now()
}

// StopCommand clears the shell command stopwatch
func (s *StatusComponent) StopCommand() {
	s.commandRunning = false
	s.commandStartTime = time.Time{}
}

// formatElapsed renders a duration as MM:SS, or H:MM:SS past an hour
func formatElapsed(d time.Duration) string {
	total := int(d.Seconds())
	if total < 0 {
		total = 0
	}
	h := total / 3600
	m := (total % 3600) / 60
	sec := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

// AddStreamChars updates the stream rate with new characters received
func (s *StatusComponent) AddStreamChars(n int) {
	if s.streamCharsTotal == 0 {
		s.streamStartTime = time.Now()
	}
	s.streamCharsTotal += n
	elapsed := time.Since(s.streamStartTime).Seconds()
	if elapsed > 0 {
		s.streamRate = float64(s.streamCharsTotal) / elapsed
	}
}

// ResetStreamRate clears the stream rate tracking
func (s *StatusComponent) ResetStreamRate() {
	s.streamCharsTotal = 0
	s.streamStartTime = time.Time{}
	s.streamRate = 0
}

// SetVerified marks the provider as verified (confirmed working via a session)
func (s *StatusComponent) SetVerified() { s.Verified = true }

// SetError marks the status component as having an error
func (s *StatusComponent) SetError() {
	s.HasError = true
}

// ClearError clears the error state
func (s *StatusComponent) ClearError() {
	s.HasError = false
}

// getStatusIcon returns the appropriate status icon based on connection and error state
func (s StatusComponent) getStatusIcon() string {
	if s.HasError {
		return "❌"
	}
	if s.Verified {
		return "✅"
	}
	return "❓"
}

// shortenProviderModel shortens provider and model names for display
func shortenProviderModel(provider, model string) string {
	// Shorten common provider names
	switch strings.ToLower(provider) {
	case "anthropic":
		provider = "Claude"
	case "openai":
		provider = "GPT"
	case "google", "googleai":
		provider = "Gemini"
	case "ollama":
		provider = "Ollama"
	}

	// Shorten common model names
	modelShort := model
	lowerModel := strings.ToLower(model)
	if strings.Contains(lowerModel, "claude") {
		// Handle models like "Claude-Haiku-4.5", "Claude 3.5 Sonnet", etc.
		// Extract the meaningful part after "claude"
		parts := strings.FieldsFunc(lowerModel, func(r rune) bool {
			return r == '-' || r == ' ' || r == '_'
		})

		// Skip "claude" prefix and build the short name
		if len(parts) > 1 {
			// For "claude-3-5-haiku-20240307" -> "3.5-Haiku"
			// For "claude-haiku-4.5" -> "Haiku-4.5"
			var shortParts []string
			for i := 1; i < len(parts); i++ {
				part := parts[i]
				// Skip date suffixes like "20240307"
				if len(part) == 8 && strings.ContainsAny(part, "0123456789") {
					continue
				}
				// Skip "latest" suffix
				if part == "latest" {
					continue
				}
				shortParts = append(shortParts, part)
			}

			if len(shortParts) > 0 {
				// Join parts and capitalize first letter of each word
				result := strings.Join(shortParts, "-")
				// Capitalize: "3-5-haiku" -> "3.5-Haiku"
				result = strings.ReplaceAll(result, "-5-", ".5-")
				// Capitalize model names
				result = strings.ReplaceAll(result, "haiku", "Haiku")
				result = strings.ReplaceAll(result, "sonnet", "Sonnet")
				result = strings.ReplaceAll(result, "opus", "Opus")
				modelShort = result
			}
		} else if strings.Contains(lowerModel, "instant") {
			modelShort = "Instant"
		}
	} else if strings.Contains(lowerModel, "gpt") {
		if strings.Contains(model, "4") {
			if strings.Contains(model, "turbo") {
				modelShort = "4T"
			} else {
				modelShort = "4"
			}
		} else if strings.Contains(model, "3.5") {
			modelShort = "3.5"
		}
	} else if strings.Contains(lowerModel, "gemini") {
		if strings.Contains(model, "pro") {
			modelShort = "Pro"
		} else if strings.Contains(model, "flash") {
			modelShort = "Flash"
		}
	}

	return fmt.Sprintf("%s-%s", provider, modelShort)
}

// SetAgent sets the current agent (legacy method for compatibility)
func (s *StatusComponent) SetAgent(agent string) {
	// Parse the agent string to extract provider and model info
	if strings.Contains(agent, "✅") {
		s.Connected = true
	} else {
		s.Connected = false
	}

	// Extract provider and model from agent string
	// Format is usually "✅ provider (model)" or "🔌 provider (model)"
	parts := strings.Split(agent, " ")
	if len(parts) >= 2 {
		s.Provider = parts[1]
		if len(parts) >= 3 && strings.HasPrefix(parts[2], "(") && strings.HasSuffix(parts[len(parts)-1], ")") {
			// Join all parts between parentheses
			modelParts := strings.Join(parts[2:], " ")
			s.Model = strings.Trim(modelParts, "()")
		}
	}
}

// SetWorkingDir sets the current working directory (legacy method)
func (s *StatusComponent) SetWorkingDir(dir string) {
	// This is now handled internally by getting current directory
}

// SetWidth updates the width of the status component
func (s *StatusComponent) SetWidth(width int) {
	s.Width = width
}

// View renders the status component
func (s StatusComponent) View() string {
	// Left section: mode + branch info
	leftSection := s.renderLeftSection()

	// Middle section: token usage + wait time
	middleSection := s.renderMiddleSection()

	// Right section: provider/model with status color
	rightSection := s.renderRightSection()

	// Calculate visual widths using lipgloss (handles ANSI codes and unicode)
	leftWidth := lipgloss.Width(leftSection)
	middleWidth := lipgloss.Width(middleSection)
	rightWidth := lipgloss.Width(rightSection)

	// Calculate spacing
	// Layout: [left] ... [middle] ... [right]
	totalContentWidth := leftWidth + middleWidth + rightWidth
	totalSpacing := s.Width - totalContentWidth

	if totalSpacing < 2 {
		// Not enough space - just concatenate with single spaces
		return s.Style.Render(leftSection + " " + middleSection + " " + rightSection)
	}

	// Distribute spacing: try to center the middle section
	// Ideal middle position is at (s.Width / 2) - (middleWidth / 2)
	idealMiddleStart := (s.Width - middleWidth) / 2

	// Left padding (space between left section and middle)
	leftPadding := idealMiddleStart - leftWidth
	if leftPadding < 1 {
		leftPadding = 1
	}

	// Right padding (space between middle and right section)
	rightPadding := s.Width - leftWidth - leftPadding - middleWidth - rightWidth
	if rightPadding < 1 {
		rightPadding = 1
	}

	// Build the final string
	result := leftSection + strings.Repeat(" ", leftPadding) +
		middleSection + strings.Repeat(" ", rightPadding) +
		rightSection

	return s.Style.Render(result)
}

func (s *StatusComponent) SetMode(mode string) {
	s.mode = strings.ToUpper(mode)
}

// renderLeftSection renders the left section with vi mode and branch info
func (s StatusComponent) renderLeftSection() string {
	var parts []string

	// Add vi mode indicator first
	parts = append(parts, fmt.Sprintf(" %s", s.mode))

	// Get branch from RepoInfo - it always has a value (empty string if no git)
	var branch string
	if s.repoInfo != nil {
		branch = s.repoInfo.Branch
	}

	if branch == "" {
		if timer := s.renderCommandStopwatch(); timer != "" {
			parts = append(parts, timer)
		}
		parts = append(parts, "🪾no-git")
		return strings.Join(parts, " ")
	}

	// Color branch name: yellow for main, green for others
	var bs lipgloss.Style
	if s.repoInfo != nil && s.repoInfo.IsMain {
		bs = lipgloss.NewStyle().Foreground(globalTheme.Warning)
	} else {
		bs = lipgloss.NewStyle().Foreground(globalTheme.SuccessColor)
	}

	parts = append(parts, "🌴 "+bs.Render(branch))

	// Add diff stats if available
	if s.repoInfo != nil {
		added := s.repoInfo.LinesAdded
		deleted := s.repoInfo.LinesDeleted
		if added > 0 || deleted > 0 {
			addedStyle := lipgloss.NewStyle().Foreground(globalTheme.Error)
			deletedStyle := lipgloss.NewStyle().Foreground(globalTheme.SuccessColor)

			var diffParts []string
			if added > 0 {
				diffParts = append(diffParts, addedStyle.Render(fmt.Sprintf("+%d", added)))
			}
			if deleted > 0 {
				diffParts = append(diffParts, deletedStyle.Render(fmt.Sprintf("-%d", deleted)))
			}
			parts = append(parts, "← "+strings.Join(diffParts, " "))
		}
	}

	// Add shell command stopwatch
	if timer := s.renderCommandStopwatch(); timer != "" {
		parts = append(parts, timer)
	}

	// Add shell runner indicator
	parts = append(parts, s.renderShellRunnerIndicator())

	return strings.Join(parts, " ")
}

// renderCommandStopwatch renders the in-flight shell command timer, or "" when
// no command is running.
func (s StatusComponent) renderCommandStopwatch() string {
	if !s.commandRunning || s.commandStartTime.IsZero() {
		return ""
	}
	return "⏱ " + formatElapsed(time.Since(s.commandStartTime))
}

// renderShellRunnerIndicator renders the shell runner status indicator
func (s StatusComponent) renderShellRunnerIndicator() string {
	ret := "🏖️ "
	warnStyle := lipgloss.NewStyle().Foreground(globalTheme.Warning)

	if s.ContainerID != "" {
		// Show container indicator with container ID
		id := s.ContainerID
		if id == "" {
			ret += "TBD"
		} else {
			ret += "📦 " + id[:4]
		}
		return ret
	}

	return ret + warnStyle.Render("⚠ host")
}

// renderMiddleSection renders the middle section with token usage and session age
func (s StatusComponent) renderMiddleSection() string {
	statusStyle := lipgloss.NewStyle().Foreground(globalTheme.TextColor)

	statusStr := fmt.Sprintf("🪣 %.0f%%", s.ContextPercent)
	if s.waitingForResponse && !s.waitingSince.IsZero() {
		waitSeconds := int(time.Since(s.waitingSince).Seconds())
		if waitSeconds >= 3 {
			statusStr += fmt.Sprintf("  ⏳ %ds", waitSeconds)
		}
	}

	// Style with theme text color
	return statusStyle.Render(statusStr)
}

// renderRightSection renders the right section with provider info
func (s StatusComponent) renderRightSection() string {
	providerModel := shortenProviderModel(s.Provider, s.Model)

	// Color based on status: red on error, yellow when unverified, default when verified
	style := s.Style.Copy()
	if s.HasError {
		style.Foreground(globalTheme.Error)
	} else if !s.Verified && !s.HasError {
		style.Foreground(globalTheme.Warning)
	}

	return style.Render(s.getStatusIcon() + " " + providerModel)
}
