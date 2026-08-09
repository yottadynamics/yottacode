package tui

import (
	"fmt"
	"image/color"
	"os"
)

// capturedTerminalBackground mirrors Model.originalTerminalBackground as
// it's captured (see the tea.BackgroundColorMsg case in update). It only
// exists so run.go can register a restore defer BEFORE tea.NewProgram
// starts — matching the subagent-teardown defer's "must run on panics
// and early error-returns too" discipline — since the model's own copy
// isn't reachable until prog.Run() returns with the final model, which
// is too late to matter if the panic happens inside the run loop
// itself. A package-level var for one specific crash-safety guarantee,
// not a general escape hatch — everything else about the real
// background stays on Model (View, per-render) as normal.
var capturedTerminalBackground color.Color

// oscSetBackground formats the OSC 11 escape sequence that repaints the
// terminal's real background to c. Hand-rolled rather than routed
// through a color-sequence library: OSC 11 is a small, stable, widely
// documented format, and building it directly here keeps this one use
// (the exit-time restore write below) self-contained instead of adding
// a dependency for one fixed string.
//
// Only used for that final write. The live in-session repaint goes
// through Bubbletea v2's own declarative tea.View.BackgroundColor field
// (set in Model.View, model.go) rather than a hand-rolled dirty-flag
// queue — Bubbletea already owns diffing/emitting that sequence safely
// through its renderer, the same way it owns AltScreen.
func oscSetBackground(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("\x1b]11;rgb:%02x/%02x/%02x\x07", r>>8, g>>8, b>>8)
}

// restoreTerminalBackground writes the OSC 11 sequence that restores
// orig (the real background captured at startup via
// tea.RequestBackgroundColor/BackgroundColorMsg — see Model.Init and
// the agentEventMsg-adjacent case in update) directly to stdout. nil
// means the terminal never answered the startup query (unsupported, or
// an SSH/tmux hop that swallows OSC queries) — in that case no theme
// ever touched the real background either (Model.View only sets
// tea.View.BackgroundColor when it has a captured original to fall
// back to), so there's nothing to restore.
//
// Safe to call only once Bubbletea's renderer has stopped (after
// prog.Run() returns, or via a defer registered before it starts) —
// during the run loop the renderer owns stdout on its own goroutine,
// and a concurrent raw write risks interleaving mid-escape-sequence
// with it.
func restoreTerminalBackground(orig color.Color) {
	if orig == nil {
		return
	}
	os.Stdout.WriteString(oscSetBackground(orig))
}
