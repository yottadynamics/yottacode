package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
)

// LoopControlState is the shared signal between a /loop prose iteration's agent
// turn and the TUI that schedules it. The TUI sets turnActive before firing a
// loop's prose turn and clears it when the turn ends; streamIteration reads
// IsActive to decide whether to advertise the loop_control tool. The tool sets
// stop when the model asks to end the loop, and the TUI consumes it at turn end
// to disarm the owning loop.
//
// All fields are atomic: the TUI's Update goroutine writes turnActive and reads
// stop/reason while the agent goroutine reads turnActive and writes stop/reason.
// The pointer is shared between LoopConfig and the TUI Model (and the tool), so
// the gate and the stop request need no reconstruction — the same pattern
// PlanModeState uses.
type LoopControlState struct {
	turnActive atomic.Bool
	stop       atomic.Bool
	reason     atomic.Pointer[string]
	ctx        atomic.Pointer[string] // one-line loop descriptor for the addendum
}

// IsActive reports whether the current turn is a /loop prose iteration. Nil-safe
// — oneshot, subagents, and tests leave LoopControl unset, which reads as "not a
// loop turn" and hides the tool.
func (s *LoopControlState) IsActive() bool {
	return s != nil && s.turnActive.Load()
}

// SetTurnActive marks (or unmarks) the current turn as a /loop iteration. On
// unmark it also clears any unconsumed stop so a request can never leak from one
// turn into the next. Nil-safe.
func (s *LoopControlState) SetTurnActive(active bool) {
	if s == nil {
		return
	}
	s.turnActive.Store(active)
	if !active {
		s.stop.Store(false)
		s.reason.Store(nil)
		s.ctx.Store(nil)
	}
}

// SetContext records the one-line loop descriptor injected into the
// per-iteration addendum (cadence, bounded/unbounded). Set by the TUI just
// before a loop's prose turn starts. Nil-safe.
func (s *LoopControlState) SetContext(c string) {
	if s == nil {
		return
	}
	if c == "" {
		s.ctx.Store(nil)
		return
	}
	s.ctx.Store(&c)
}

// Context returns the loop descriptor set by SetContext, or "" if none. Nil-safe.
func (s *LoopControlState) Context() string {
	if s == nil {
		return ""
	}
	if p := s.ctx.Load(); p != nil {
		return *p
	}
	return ""
}

// requestStop records the model's request to end the loop after this turn.
func (s *LoopControlState) requestStop(reason string) {
	if s == nil {
		return
	}
	r := reason
	s.reason.Store(&r)
	s.stop.Store(true)
}

// ConsumeStop reports whether the model asked to stop this turn's loop, resetting
// the flag so it fires at most once. The returned reason is the model's stated
// justification (may be empty). Nil-safe.
func (s *LoopControlState) ConsumeStop() (bool, string) {
	if s == nil || !s.stop.Swap(false) {
		return false, ""
	}
	reason := ""
	if p := s.reason.Swap(nil); p != nil {
		reason = *p
	}
	return true, reason
}

// LoopControlTool lets a /loop prose iteration end its own loop once the agent
// judges the loop's goal met — e.g. `/loop 2m check CI and stop when green`. It
// is advertised ONLY while a /loop prose iteration owns the turn (see the
// loop_control gate in iterationToolFilter); in any other turn it is hidden, so
// the model cannot stop a loop that isn't running. Stopping takes effect after
// the current turn finishes: the TUI disarms the loop so it does not re-fire.
type LoopControlTool struct {
	State *LoopControlState
}

func (t *LoopControlTool) Name() string { return "loop_control" }

func (t *LoopControlTool) Description() string {
	return "End the /loop that is running the current turn, once its goal is met. " +
		"Only available while a /loop iteration is running (e.g. \"/loop 2m check CI and stop when all checks are green\"). " +
		"Call it with action \"stop\" and a short reason the moment the loop's stated stop-condition is satisfied — the loop disarms after this turn finishes so it stops repeating. " +
		"You do NOT need it to keep looping (that is the default), and it does NOT end the current turn — finish your reply as usual."
}

// Schema: a single required "action" (only "stop" today) plus an optional
// human-facing "reason". Kept tiny so the model reaches for it decisively.
func (t *LoopControlTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []any{"stop"},
				"description": "\"stop\" disarms the loop after this turn so it stops repeating.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "One short line on why the loop is stopping, shown to the user (e.g. \"all CI checks are green\").",
			},
		},
		"required": []any{"action"},
	}
}

// RequiresApproval is false: a loop ending itself on the model's judgment is the
// whole point — gating it behind a modal would defeat hands-off polling.
func (t *LoopControlTool) RequiresApproval(string) bool { return false }

func (t *LoopControlTool) PreviewCall(argsJSON string) string {
	action, reason := parseLoopControlArgs(argsJSON)
	if strings.TrimSpace(action) == "" {
		action = "stop"
	}
	if reason != "" {
		return "loop_control: " + action + " — " + reason
	}
	return "loop_control: " + action
}

func (t *LoopControlTool) Execute(_ context.Context, argsJSON string) (string, error) {
	// Defensive: the schema gate already hides this tool outside a loop turn,
	// but a model can still synthesize the call. Refuse cleanly instead of
	// stopping a loop that isn't running.
	if !t.State.IsActive() {
		return "no /loop is running this turn, so there is nothing to stop — just finish your reply.", nil
	}
	action, reason := parseLoopControlArgs(argsJSON)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "stop", "":
		t.State.requestStop(reason)
		return "acknowledged — the loop will disarm after this turn ends and will not fire again. Finish your reply now.", nil
	default:
		return fmt.Sprintf("unknown action %q — the only supported action is \"stop\".", action), nil
	}
}

func parseLoopControlArgs(argsJSON string) (action, reason string) {
	var a struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a.Action, a.Reason
}
