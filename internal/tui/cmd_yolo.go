package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// enterYoloMode is the activator for the yolo mode overlay (every
// tool auto-runs, no iteration cap). Internally still called "yolo" —
// the Go identifier predates the public flag rename and is
// implementation detail — and user-facing text now reads "yolo mode"
// to align with the --yolo flag a user just passed. Called from Run()
// when that flag is set, and also by the /yolo slash command handler
// (cmdYolo) for mid-session entry.
//
// Yolo mode is an orthogonal overlay, NOT a mode: entering it does
// NOT exit auto or plan mode — the overlay sits on top of whichever
// mode is active and the precedence switch in the agent loop picks
// the yolo branch first. Mirrors the original --yolo path, so the
// slash command and the startup flag are interchangeable entry points
// for the same state.
//
// Idempotent: if the overlay is already active (defensive — the
// startup path calls this once), no-op without printing a duplicate
// banner.
//
// The entry log is verbose by design — this is the danger setting and
// the user should see exactly what they opted into. Explicit deny
// rules in .yottacode/permissions.json still win.
func enterYoloMode(m Model) Model {
	state := m.cfg.YoloMode
	if state == nil || state.IsActive() {
		return m
	}
	state.Active.Store(true)
	// No leading blank: enterYoloMode runs at startup (before the first
	// WindowSizeMsg) so these lines are deferred into historyLines and
	// emitted by the startup handler, which already inserts a blank line
	// after the welcome box. The trailing blank separates the banner from
	// whatever follows (startup notices / the live footer).
	m.appendLine(styleYoloBannerLabel.Render(YoloModeIcon+" yolo mode active") +
		" " + styleYoloBannerHint.Render("— every tool auto-runs (NO safety floor) · no iteration cap"))
	m.appendLine(styleYoloBannerHint.Render("  explicit deny rules in permissions.json still win · /yolo again or restart without --yolo to recover"))
	m.appendLine("")
	return m
}

// exitYoloMode is the cleanup half invoked by /yolo when the overlay
// is already on. Mutates the shared YoloModeState in place so the
// agent goroutine sees the new state on its next tool dispatch.
//
// YoloMode is the SOLE representation of the bypass in the TUI — the
// LoopConfig no longer carries a BypassPermissions bool (see run.go),
// so clearing Active here is sufficient to restore tool approvals.
// (BypassPermissions survives only on the non-TUI oneshot/CI path,
// which never constructs a YoloModeState; the loop's approval switch
// reads it there.) Earlier this function had to leave a second
// TUI-side flag alone, which meant /yolo off didn't actually restore
// approvals for a --yolo-launched session — removing that flag is what
// made this a clean single-flag toggle.
//
// Orthogonality preserved: exiting yolo mode does NOT enter or exit
// any mode — whichever mode was active before the overlay (auto,
// plan, or none) becomes the sole approval gate again. This is the
// mirror of enterYoloMode's "do not touch other modes" contract.
//
// Idempotent: no-op when the overlay is already off.
func exitYoloMode(m *Model) {
	state := m.cfg.YoloMode
	if state == nil {
		return
	}
	wasActive := state.Active.Swap(false)
	if !wasActive {
		return
	}
	m.appendLine(styleYoloBannerLabel.Render(YoloModeIcon+" yolo mode exited") +
		" " + styleYoloBannerHint.Render("— tool approvals restored · /yolo again to re-enter"))
}

// cmdYolo is the slash-command handler for /yolo. Toggle shape:
//
//	/yolo  — toggle yolo mode on/off (mirror of --yolo at startup).
//
// Mirrors /plan: idempotent at the boundaries — invoking while active
// exits; invoking when off enters. Entry runs the same path as the
// --yolo startup flag (enterYoloMode) so the banner and precedence
// match; exit runs exitYoloMode so the agent loop's tool-approval
// gate restores the normal (or current-mode) prompt flow. Yolo mode
// is an orthogonal overlay, so neither half touches auto or plan
// mode — whichever mode was active before the overlay becomes the
// sole approval gate again after exit.
//
// Unlike --yolo at startup (one-way, recover by restart), /yolo can
// toggle the overlay off mid-session — the user explicitly asked for
// this escape hatch. Explicit deny rules in
// .yottacode/permissions.json still win in either direction.
func cmdYolo(m Model, _ []string) (Model, tea.Cmd) {
	state := m.cfg.YoloMode
	if state == nil {
		m.appendLine(styleError.Render("[yolo] yolo mode is not available in this build"))
		return m, nil
	}
	if state.IsActive() {
		exitYoloMode(&m)
		return m, nil
	}
	return enterYoloMode(m), nil
}
