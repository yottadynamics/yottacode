package agent

import "sync/atomic"

// YoloModeState is the per-session "no questions asked" flag. When
// active, the loop auto-approves every tool call WITHOUT the safety
// floor that auto mode keeps (run_bash, git_commit, git_checkpoint,
// rollback all auto-allow silently), AND removes the iteration cap.
// Explicit Deny rules in permissions.json still win — yolo is "skip
// prompts," not "ignore my policy."
//
// Mutually exclusive with AutoMode and PlanMode at the TUI layer:
// entering yolo turns the other two off. The loop-level gate doesn't
// enforce this on its own; the TUI does.
//
// Intentionally NOT in the Shift+Tab mode cycle — you have to type
// /yolo to enter so it can't be reached by accident.
type YoloModeState struct {
	Active atomic.Bool
}

// IsActive is a nil-safe check used by the loop.
func (y *YoloModeState) IsActive() bool {
	return y != nil && y.Active.Load()
}
