package agent

import "sync/atomic"

// AutoModeState is the per-session, runtime-mutable auto-mode flag the
// loop reads on every tool dispatch. When active, mutating tools that
// would normally hit an approval modal auto-allow with Source=auto-mode
// — EXCEPT for the safety floor (run_bash, git_commit, git_checkpoint,
// rollback), which always prompt regardless of mode.
//
// Mutually exclusive with plan mode at the TUI layer: entering one
// turns the other off. The loop-level gates don't enforce this on
// their own — they just observe whichever flag is set.
//
// The pointer is shared between LoopConfig and the TUI Model so a
// flip from /auto, Shift+Tab, or the plan-card [Y] hotkey takes
// effect on the next iteration with no reconstruction. atomic.Bool
// keeps that benign race detector-clean.
type AutoModeState struct {
	Active atomic.Bool
}

// IsActive is a nil-safe check used by the loop.
func (a *AutoModeState) IsActive() bool {
	return a != nil && a.Active.Load()
}

// IsAutoModeSafetyFloor returns true for tools whose approval prompt
// must NOT be skipped by auto mode. These are the calls that run
// arbitrary code (run_bash) or write permanent / hard-to-reverse
// history (git_commit, git_checkpoint, rollback). The user opted into
// auto mode to skip edit-by-edit approval friction — not to silently
// hand over shell access or amend git history.
//
// To get true blanket auto-approval (including run_bash and commits),
// launch with --dangerously-skip-permissions; that's the user-explicit
// "yolo" path and is intentionally session-wide so it can't be toggled
// away in the middle of a run (mirroring Claude Code).
func IsAutoModeSafetyFloor(toolName string) bool {
	switch toolName {
	case "run_bash", "git_commit", "git_checkpoint", "rollback":
		return true
	}
	return false
}
