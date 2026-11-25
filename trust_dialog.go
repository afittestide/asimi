package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/afittestide/asimi/internal/config"
	"github.com/charmbracelet/lipgloss"
)

// TrustDialog is the launch-time asset chooser from gh #180: it enumerates the
// concrete host-asset seams the committed config *requests* and lets the Ruler
// grant them per asset (edict 1036). A request is not a grant — nothing is
// granted until the Ruler confirms, and any seam left untouched is absent from
// the mandate.
//
// The dialog deliberately lists only the config-requested seams (mounts and
// passthrough env). The working-tree bind, the overlay/cache volumes, the host
// network namespace and run_on_host are NOT represented: the dialog must never
// imply a boundary it does not hold (see intent/docs/sandbox-seams.md).
//
// --isolated-host (seam 8) is the sharpest case: when the operator passes it
// the sandbox is bypassed entirely, so the dialog must not read as if a sandbox
// were in force. That is not something the wording alone can promise, so it is
// ENFORCED, not merely commented: the title names the session's *requested*
// assets and the footer names the one seam always open — read-write access to
// the project directory, true whether or not the sandbox exists. The seam
// list is built from config.SessionSeams, which has no SeamKind for the flag,
// so no row can correspond to it.
type TrustDialog struct {
	seams []config.Seam
	// granted[i] mirrors seams[i]; writable[i] is meaningful for mounts only
	// and is ignored for env seams.
	granted  []bool
	writable []bool
	cursor   int

	// allowAll is the explicit, named "Allow all" choice. It is a row of its
	// own and starts false: it must be an affirmative action, never the
	// pre-checked default.
	allowAll bool

	// loadErr records that the request never reached the Court. It is the
	// whole difference between "the repository requested nothing" and "nobody
	// could read the request": the first is an honest empty chooser, the
	// second is not a chooser at all — nothing may be granted and the dialog
	// must say why (edict 1036).
	loadErr error

	// Optional welcome notice flags folded into the dialog header.
	updateAvail   bool
	configCreated bool
}

// NewTrustDialog builds a dialog over the requested seams. Every seam starts
// UNgranted (safe by default); the Ruler opts in per asset.
func NewTrustDialog(seams []config.Seam) *TrustDialog {
	d := &TrustDialog{
		seams:    seams,
		granted:  make([]bool, len(seams)),
		writable: make([]bool, len(seams)),
	}
	// A mount the repo requested writable may be presented as "writable
	// requested" but it is still read-only until the Ruler opens it: the safe
	// default is ro, so writable starts false for every seam.
	return d
}

// NewUnreadTrustDialog builds the dialog for a request that could not be read.
// It has no rows and grants nothing: a failed read must never look like a
// successful empty request, and it must never invite a grant that the Court
// could not act on (edict 1036).
func NewUnreadTrustDialog(err error) *TrustDialog {
	return &TrustDialog{loadErr: err}
}

// LoadErr reports why the request could not be read, or nil when it was read.
func (d *TrustDialog) LoadErr() error {
	if d == nil {
		return nil
	}
	return d.loadErr
}

// rows is the number of selectable rows: the seams plus the Allow all row. An
// unreadable request has no rows at all — there is nothing to choose from and
// no bulk choice that could mean anything.
func (d *TrustDialog) rows() int {
	if d == nil || d.loadErr != nil {
		return 0
	}
	return len(d.seams) + 1
}

// allowAllRow is the index of the Allow all row.
func (d *TrustDialog) allowAllRow() int { return len(d.seams) }

// Move adjusts the cursor by delta, clamping to the selectable rows.
func (d *TrustDialog) Move(delta int) {
	if d == nil || d.rows() == 0 {
		return
	}
	d.cursor += delta
	if d.cursor < 0 {
		d.cursor = d.rows() - 1
	}
	if d.cursor >= d.rows() {
		d.cursor = 0
	}
}

// Toggle flips the grant on the row under the cursor. On the Allow all row it
// flips every seam at once, as an explicit choice.
func (d *TrustDialog) Toggle() {
	if d == nil || d.loadErr != nil {
		return
	}
	if d.cursor == d.allowAllRow() {
		d.ToggleAllowAll()
		return
	}
	if d.cursor < 0 || d.cursor >= len(d.seams) {
		return
	}
	d.granted[d.cursor] = !d.granted[d.cursor]
	if !d.granted[d.cursor] {
		d.writable[d.cursor] = false
	}
}

// ToggleAllowAll flips the named "Allow all" choice and every requested seam
// with it (mounts, env vars, and MCP servers). It is the affirmative,
// all-or-nothing action — never the pre-checked default.
//
// Host execution (seam 7) is deliberately left ON THE RULER'S OWN ROW: it is
// strictly more authority, so it is only ever granted by ticking that row by
// name.
func (d *TrustDialog) ToggleAllowAll() {
	if d == nil || d.loadErr != nil {
		return
	}
	d.allowAll = !d.allowAll
	for i, seam := range d.seams {
		if seam.Kind == config.SeamHostExec {
			continue
		}
		d.granted[i] = d.allowAll
		if !d.allowAll {
			d.writable[i] = false
		}
	}
	d.cursor = d.allowAllRow()
}

// ToggleWritable opens (or closes) the mount under the cursor for writing.
// It is inert for env seams and for ungranted mounts: a declined seam has no
// authority to widen.
func (d *TrustDialog) ToggleWritable() {
	if d == nil || d.loadErr != nil || d.cursor < 0 || d.cursor >= len(d.seams) {
		return
	}
	if d.seams[d.cursor].Kind != config.SeamMount || !d.granted[d.cursor] {
		return
	}
	d.writable[d.cursor] = !d.writable[d.cursor]
}

// Selections is the Ruler's answer, ready for ApplyMandate. An untouched
// dialog yields the most restrictive answer: no grants at all.
func (d *TrustDialog) Selections() config.Selections {
	sel := config.Selections{
		Mounts:     map[string]bool{},
		Env:        map[string]bool{},
		MCPServers: map[string]bool{},
	}
	// An unreadable request grants nothing. This is the last veto: every
	// mutator already refuses when loadErr is set, but the whole point of that
	// invariant is that no path can slip past it, so the answer itself is
	// filtered too (edict 1036).
	if d == nil || d.loadErr != nil {
		return sel
	}
	for i, s := range d.seams {
		if !d.granted[i] {
			continue
		}
		switch s.Kind {
		case config.SeamEnv:
			sel.Env[s.EnvName] = true
		case config.SeamMount:
			sel.Mounts[s.Mount.Source] = d.writable[i]
		case config.SeamHostExec:
			// Host execution is granted only by its own row. It is excluded
			// from Allow all (see ToggleAllowAll) because it is strictly more
			// authority than the sandbox: no blanket choice may reach it.
			sel.HostExec = true
		case config.SeamMCPServer:
			// MCP servers spawn unsandboxed stdio processes on the host; they
			// start ungranted and are enabled either by their own row or via Allow all.
			sel.MCPServers[s.MCPServer.ID] = true
		}
	}
	return sel
}

// GrantedCount reports how many seams the Ruler has granted. An unreadable
// request has granted nothing, whatever stale rows the dialog may carry.
func (d *TrustDialog) GrantedCount() int {
	if d == nil || d.loadErr != nil {
		return 0
	}
	n := 0
	for _, g := range d.granted {
		if g {
			n++
		}
	}
	return n
}

// Covers reports whether the dialog governs the named seam. It is how the
// "the dialog never implies it covers --isolated-host" guarantee is asserted
// rather than merely commented: no row may ever name the operator's CLI flag,
// because the rows come from config.SessionSeams and neither SeamKind can
// represent a flag. A caller that gets false here must not read a grant from
// this dialog as covering that seam (edict 1036, seam 8).
func (d *TrustDialog) Covers(seam string) bool {
	if d == nil || seam == "" {
		return false
	}
	for _, s := range d.seams {
		if s.Label == seam || s.EnvName == seam || s.Mount.Source == seam || s.MCPServer.ID == seam {
			return true
		}
	}
	return false
}

// Render draws the chooser.
func (d *TrustDialog) Render(width int) string {
	if d == nil {
		return ""
	}
	if width < 40 {
		width = 60
	}
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(globalTheme.Warning)
	hintStyle := lipgloss.NewStyle().Foreground(globalTheme.TextColor)
	dimStyle := lipgloss.NewStyle().Foreground(globalTheme.DimTextColor)
	cursorStyle := lipgloss.NewStyle().Foreground(globalTheme.SuccessColor).Bold(true)

	var b strings.Builder
	b.WriteString(renderWelcomeHeader(width, d.updateAvail, d.configCreated))
	b.WriteString("\n\n")
	b.WriteString(titleStyle.Render("Allow this session access"))
	b.WriteString("\n")
	b.WriteString(hintStyle.Render("The following requested host assets are up for your answer:"))
	b.WriteString("\n\n")

	switch {
	case d.loadErr != nil:
		// The request could not be read. That is NOT "the repository requested
		// nothing": the repository may well have requested six assets and the
		// Court simply never answered. Say so, and grant nothing.
		b.WriteString(hintStyle.Render("  (the requested host assets could not be read from the Court:"))
		b.WriteString("\n")
		b.WriteString(hintStyle.Render(fmt.Sprintf("   %s)", d.loadErr)))
		b.WriteString("\n")
		b.WriteString(hintStyle.Render("   Nothing is granted: the restrictive mandate applies."))
		b.WriteString("\n")
	case len(d.seams) == 0:
		b.WriteString(hintStyle.Render("  (the repository requested no extra host assets)"))
		b.WriteString("\n")
	}

	for i, s := range d.seams {
		cursor := "  "
		if d.cursor == i {
			cursor = cursorStyle.Render("> ")
		}
		box := "[ ]"
		if d.granted[i] {
			box = "[x]"
		}
		mode := ""
		switch s.Kind {
		case config.SeamMount:
			if d.granted[i] && d.writable[i] {
				mode = "rw"
			} else {
				mode = "ro"
			}
			b.WriteString(fmt.Sprintf("%s%s %s %s\n", cursor, box, s.Label, mode))
		case config.SeamEnv:
			b.WriteString(fmt.Sprintf("%s%s %s - env var\n", cursor, box, s.Label))
		case config.SeamHostExec:
			b.WriteString(fmt.Sprintf("%s%s host %s (commands run OFF the sandbox, on this machine)\n",
				cursor, box, s.Label))
		case config.SeamMCPServer:
			b.WriteString(fmt.Sprintf("%s%s mcp %s (runs OFF the sandbox, on this machine)\n",
				cursor, box, s.Label))
		}
	}

	if d.loadErr == nil {
		allowCursor := "  "
		if d.cursor == d.allowAllRow() {
			allowCursor = cursorStyle.Render("> ")
		}
		allowBox := "[ ]"
		if d.allowAll {
			allowBox = "[x]"
		}
		b.WriteString(fmt.Sprintf("%s%s Allow all\n", allowCursor, allowBox))
	}

	cwd, _ := os.Getwd()
	b.WriteString(hintStyle.Render("  [x] Read-write access to " + cwd))
	b.WriteString("\n\n")

	if d.loadErr != nil {
		b.WriteString(dimStyle.Render("enter/esc dismiss"))
	} else {
		b.WriteString(dimStyle.Render("↑/↓ move · space toggle · w rw/ro · a allow all · enter confirm · esc deny"))
	}

	b.WriteString("\n")
	return b.String()
}
