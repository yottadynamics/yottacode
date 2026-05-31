package tui

// enterYoloMode is the one-way startup activator for the permissions
// bypass overlay (every tool auto-runs, no iteration cap). Internally
// still called "yolo" — the Go identifier predates the public flag
// rename and is implementation detail — but user-facing text reads
// "permissions bypass" so it aligns with the
// --yolo flag a user just passed. Called from
// Run() when that flag is set. Mirroring Claude Code: the overlay has
// no slash command, no Shift+Tab binding, no mid-session toggle. The
// user opts in at process start and can only recover by restarting
// yottacode without the flag.
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
	m.appendLine(styleYoloBannerLabel.Render(YoloModeIcon+" permissions bypass active") +
		" " + styleYoloBannerHint.Render("— every tool auto-runs (NO safety floor) · no iteration cap"))
	m.appendLine(styleYoloBannerHint.Render("  explicit deny rules in permissions.json still win · restart without --yolo to recover"))
	m.appendLine("")
	return m
}
