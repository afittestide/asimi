package main

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/rpc"
	"github.com/afittestide/asimi/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// trustSeamsLoadedMsg carries the concrete seams the committed config
// requests. It is produced by the TUI at boot (over the wire in daemon mode)
// and drives the trust dialog. err distinguishes "the request could not be
// read" from "the request is empty" — they are different facts and the dialog
// says different things about them (edict 1036).
type trustSeamsLoadedMsg struct {
	seams []config.Seam
	err   error
}

// The seam read races the daemon's handler registration, so a transient
// failure is retried briefly. The bound keeps a genuinely broken Court from
// stalling the boot: after it, the error is reported and the restrictive
// mandate applies.
const (
	trustSeamsMaxAttempts = 8
	trustSeamsRetryDelay  = 50 * time.Millisecond
)

// loadTrustSeams asks the court for the requested seams. A read that cannot
// reach the Court is reported as an error, never as an empty request: the
// dialog must not tell the Ruler the repository asked for nothing when the
// truth is that nobody could ask it (edict 1036).
func loadTrustSeams(courtClient interface {
	SessionSeams() ([]config.Seam, error)
}) tea.Cmd {
	return func() tea.Msg {
		if courtClient == nil {
			return trustSeamsLoadedMsg{err: errors.New("no court client: the session's request cannot be read")}
		}
		var err error
		for attempt := 1; ; attempt++ {
			var seams []config.Seam
			if seams, err = courtClient.SessionSeams(); err == nil {
				return trustSeamsLoadedMsg{seams: seams}
			}
			if attempt >= trustSeamsMaxAttempts || !retryableSeamsRead(err) {
				break
			}
			time.Sleep(trustSeamsRetryDelay)
		}
		return trustSeamsLoadedMsg{err: err}
	}
}

// retryableSeamsRead reports whether a failed seam read can succeed on a
// retry: the daemon may not have registered its handlers yet (unknown method),
// or the socket may be briefly down. Any other error is reported as it is.
func retryableSeamsRead(err error) bool {
	if errors.Is(err, rpc.ErrPeerDisconnected) || errors.Is(err, rpc.ErrClosed) {
		return true
	}
	var we *wire.Error
	return errors.As(err, &we) && we.Code == wire.CodeUnknownMethod
}

// restrictiveSelections is the no-answer default: grant nothing.
func restrictiveSelections() config.Selections {
	return config.Selections{Mounts: map[string]bool{}, Env: map[string]bool{}}
}

// openTrustDialog presents the Ruler with the concrete-seam chooser. In
// handsoff mode there is no Ruler to answer, so the restrictive default is
// applied instead — handsoff must never widen authority.
//
// loadErr means the request never reached the Court. That is NOT an empty
// request, so there is nothing to choose: the restrictive mandate is applied at
// once and the dialog is shown only to explain why. A chooser that appears to
// have nothing to grant would misstate the session's authority (edict 1036).
func (m *TUIModel) openTrustDialog(seams []config.Seam, loadErr error) tea.Cmd {
	if m.court == nil {
		return nil
	}
	if m.handsoff {
		slog.Info("handsoff: applying restrictive mandate without a trust dialog")
		return m.applyMandate(restrictiveSelections())
	}
	if loadErr != nil {
		slog.Warn("failed to read the requested host assets", "error", loadErr)
		m.commandLine.AddToast(fmt.Sprintf("Trust: could not read the requested host assets (%v); the restrictive mandate applies", loadErr), "error", 5*time.Second)
		m.trustDialog = NewUnreadTrustDialog(loadErr)
		return m.applyMandate(restrictiveSelections())
	}
	// A genuinely empty request has nothing to choose. A chooser with no
	// rows is a hard modal nobody can answer, so the restrictive mandate
	// is applied at once and a toast discloses what stays open (edict
	// 1059 / zhengming 44a2a226d637953a).
	if len(seams) == 0 {
		slog.Info("empty host asset request: applying restrictive mandate without a trust dialog")
		m.commandLine.AddToast("Trust: no host assets requested — restrictive mandate applied. Working dir (rw), host network, run_on_host and --isolated-host remain as configured (docs/sandbox-seams.md).", "info", 8*time.Second)
		return m.applyMandate(restrictiveSelections())
	}
	m.trustDialog = NewTrustDialog(seams)
	return nil
}

// mandateAppliedMsg reports the outcome of applying the Ruler's answer. A
// failure is surfaced: silently discarding the Ruler's decision would leave
// them believing a grant took (edict 1036).
type mandateAppliedMsg struct {
	err error
}

// applyMandate sends the Ruler's selection to the court, which strips the
// requested config to the granted set and owns the resulting mandate.
func (m *TUIModel) applyMandate(sel config.Selections) tea.Cmd {
	court := m.court
	return func() tea.Msg {
		if court == nil {
			return mandateAppliedMsg{err: errors.New("no court client: the mandate was not applied")}
		}
		return mandateAppliedMsg{err: court.ApplyMandate(sel)}
	}
}

// handleTrustDialogKey processes the dialog's keys. It is a hard modal for
// every key it does not recognise: the trust dialog owns the keyboard while it
// is up, so an unrecognised key is swallowed (handled=true) rather than leaking
// to global handlers underneath. ctrl+c is the one exception, and it is
// intercepted upstream in TUIModel.Update before this is ever reached, so
// aborting still works and is not this function's responsibility.
func (m TUIModel) handleTrustDialogKey(keyStr string) (tea.Model, tea.Cmd, bool) {
	d := m.trustDialog
	if d == nil {
		return m, nil, false
	}
	switch keyStr {
	case "up", "k", "shift+tab":
		d.Move(-1)
		return m, nil, true
	case "down", "j", "tab":
		d.Move(1)
		return m, nil, true
	case " ", "space":
		d.Toggle()
		return m, nil, true
	case "w":
		d.ToggleWritable()
		return m, nil, true
	case "a":
		d.ToggleAllowAll()
		return m, nil, true
	case "enter":
		sel := d.Selections()
		grants := d.GrantedCount()
		m.trustDialog = nil
		if d.LoadErr() != nil {
			// The restrictive mandate was already applied when the read failed;
			// there is nothing to add and nothing to answer.
			m.commandLine.AddToast("Trust: request unreadable — nothing granted", "warning", 3*time.Second)
			return m, nil, true
		}
		m.commandLine.AddToast(fmt.Sprintf("Trust: %d asset(s) granted", grants), "info", 3*time.Second)
		return m, m.applyMandate(sel), true
	case "esc":
		m.trustDialog = nil
		if d.LoadErr() != nil {
			return m, nil, true
		}
		m.commandLine.AddToast("Trust: nothing granted", "warning", 3*time.Second)
		return m, m.applyMandate(restrictiveSelections()), true
	}
	// Hard modal: swallow anything else while the dialog is up.
	return m, nil, true
}

// renderTrustDialog renders the dialog as a modal above the prompt.
func (m TUIModel) renderTrustDialog() string {
	if m.trustDialog == nil {
		return ""
	}
	width := m.width - 8
	if width < 40 {
		width = 60
	}
	body := m.trustDialog.Render(width)
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(globalTheme.Warning).
		Padding(1, 2)
	return style.Render(body)
}
