package agent

import "sync/atomic"

// YoloModeState is the per-session "no questions asked" flag. When
// active, the loop auto-approves every tool call WITHOUT the safety
// floor that auto mode keeps (run_bash, git_commit, git_checkpoint,
// rollback all auto-allow silently), and raises the iteration cap to a
// large but FINITE bound (see yoloIterationCap — not literally
// uncapped). Explicit Deny rules in permissions.json still win — yolo
// is "skip prompts," not "ignore my policy."
//
// Orthogonal OVERLAY, not a mode: it sits on top of whichever mode
// (auto, plan, or none) is active and does NOT turn the others off.
// The loop's approval switch checks the yolo case ahead of the auto
// and plan cases, so it dominates while active; exiting it hands the
// gate back to whatever mode was underneath.
//
// NOT in the Shift+Tab mode cycle. Entered by launching with --yolo or
// by the /yolo slash command mid-session (cmdYolo), and /yolo also
// exits it — the deliberate escape hatch the flag alone doesn't give.
// In the TUI this is the SOLE representation of the bypass; the
// separate LoopConfig.BypassPermissions bool is used only on the
// non-TUI oneshot/CI path, which never constructs a YoloModeState.
type YoloModeState struct {
	Active atomic.Bool
}

// IsActive is a nil-safe check used by the loop.
func (y *YoloModeState) IsActive() bool {
	return y != nil && y.Active.Load()
}
