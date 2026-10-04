package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yottadynamics/yottacode/internal/subagents"
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

	// verify is set for a loop armed with --verify: loop_control{stop} is refused
	// until a verification subagent started this iteration returned
	// VERDICT: PASS (or the model reports a hard blocker). turnStart (unix nano)
	// bounds which verification runs count as "this iteration's".
	verify    atomic.Bool
	turnStart atomic.Int64
	// stopBlocked records that the accepted stop bypassed the verify gate by
	// declaring an external blocker, so the TUI can label it unverified.
	stopBlocked atomic.Bool
	// gateNote is the one-line reason the verify gate last refused a stop this
	// iteration; the TUI copies it onto the loop's status row at turn end.
	gateNote atomic.Pointer[string]
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
		s.stopBlocked.Store(false)
		s.reason.Store(nil)
		s.ctx.Store(nil)
		s.verify.Store(false)
		s.turnStart.Store(0)
		s.gateNote.Store(nil)
	}
}

// SetVerify marks the current loop iteration as verify-gated (see verify) and
// records when the iteration started. Set by the TUI just before a loop's prose
// turn starts. Nil-safe.
func (s *LoopControlState) SetVerify(verify bool, turnStart time.Time) {
	if s == nil {
		return
	}
	s.verify.Store(verify)
	if verify {
		s.turnStart.Store(turnStart.UnixNano())
	} else {
		s.turnStart.Store(0)
	}
}

// Verify reports whether the current iteration's stop is verify-gated. Nil-safe.
func (s *LoopControlState) Verify() bool {
	return s != nil && s.verify.Load()
}

func (s *LoopControlState) recordGateNote(note string) {
	if s == nil {
		return
	}
	s.gateNote.Store(&note)
}

// ConsumeGateNote returns the verify gate's last refusal note for this
// iteration ("" if it never refused), clearing it. The TUI calls it at turn end,
// before SetTurnActive(false), to show why a --verify loop is still running.
// Nil-safe.
func (s *LoopControlState) ConsumeGateNote() string {
	if s == nil {
		return ""
	}
	if p := s.gateNote.Swap(nil); p != nil {
		return *p
	}
	return ""
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
// blocked marks a stop that bypassed the verify gate by declaring an external
// blocker.
func (s *LoopControlState) requestStop(reason string, blocked bool) {
	if s == nil {
		return
	}
	r := reason
	s.reason.Store(&r)
	s.stopBlocked.Store(blocked)
	s.stop.Store(true)
}

// ConsumeStop reports whether the model asked to stop this turn's loop, resetting
// the flag so it fires at most once. The returned reason is the model's stated
// justification (may be empty). Nil-safe.
func (s *LoopControlState) ConsumeStop() (bool, string) {
	stop, reason, _ := s.ConsumeStopDetail()
	return stop, reason
}

// ConsumeStopDetail is ConsumeStop plus whether the stop bypassed the verify
// gate by declaring a blocker (an unverified stop). Nil-safe.
func (s *LoopControlState) ConsumeStopDetail() (stop bool, reason string, blocked bool) {
	if s == nil || !s.stop.Swap(false) {
		return false, "", false
	}
	if p := s.reason.Swap(nil); p != nil {
		reason = *p
	}
	return true, reason, s.stopBlocked.Swap(false)
}

// LoopControlTool lets a /loop prose iteration end its own loop once the agent
// judges the loop's goal met — e.g. `/loop 2m check CI and stop when green`. It
// is advertised ONLY while a /loop prose iteration owns the turn (see the
// loop_control gate in iterationToolFilter); in any other turn it is hidden, so
// the model cannot stop a loop that isn't running. Stopping takes effect after
// the current turn finishes: the TUI disarms the loop so it does not re-fire.
type LoopControlTool struct {
	State *LoopControlState
	// Tasks is the session's subagent registry. The verify gate reads it to find
	// this iteration's verification run; nil leaves a verify-gated stop refused
	// (the model can still report a blocker).
	Tasks *subagents.Registry
}

// verificationAgentType is the subagent whose VERDICT line gates a --verify loop.
const verificationAgentType = "verification"

// verifyGateTail caps how much of a failed verification's report is echoed back
// in the refusal, keeping the tool result small.
const verifyGateTail = 1200

func (t *LoopControlTool) Name() string { return "loop_control" }

func (t *LoopControlTool) Description() string {
	return "End the /loop that is running the current turn, once its goal is met. " +
		"Only available while a /loop iteration is running (e.g. \"/loop 2m check CI and stop when all checks are green\"). " +
		"Call it with action \"stop\" and a short reason the moment the loop's stated stop-condition is satisfied — the loop disarms after this turn finishes so it stops repeating. " +
		"You do NOT need it to keep looping (that is the default), and it does NOT end the current turn — finish your reply as usual. " +
		"If the loop was armed with --verify, stop is refused until the `verification` agent has run this iteration and returned VERDICT: PASS; " +
		"if something outside your control blocks the work (missing credential, denied permission), call it with blocked: true and say what."
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
			"blocked": map[string]any{
				"type":        "boolean",
				"description": "Only for loops armed with --verify: true to stop WITHOUT a passing verification because something outside your control blocks the work. The stop is shown to the user as unverified. Never use it just to skip verification.",
			},
		},
		"required": []any{"action"},
	}
}

// RequiresApproval is false: a loop ending itself on the model's judgment is the
// whole point — gating it behind a modal would defeat hands-off polling.
func (t *LoopControlTool) RequiresApproval(string) bool { return false }

func (t *LoopControlTool) PreviewCall(argsJSON string) string {
	action, reason, _ := parseLoopControlArgs(argsJSON)
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
	action, reason, blocked := parseLoopControlArgs(argsJSON)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "stop", "":
		gated := t.State.Verify()
		if gated && !blocked {
			if refusal, note := t.verifyGate(); refusal != "" {
				t.State.recordGateNote(note)
				return refusal, nil
			}
		}
		t.State.requestStop(reason, gated && blocked)
		if gated && blocked {
			return "acknowledged — the loop will disarm after this turn ends, shown to the user as UNVERIFIED because you reported a blocker. State the blocker plainly in your reply.", nil
		}
		return "acknowledged — the loop will disarm after this turn ends and will not fire again. Finish your reply now.", nil
	default:
		return fmt.Sprintf("unknown action %q — the only supported action is \"stop\".", action), nil
	}
}

func parseLoopControlArgs(argsJSON string) (action, reason string, blocked bool) {
	var a struct {
		Action  string `json:"action"`
		Reason  string `json:"reason"`
		Blocked bool   `json:"blocked"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return a.Action, a.Reason, a.Blocked
}

// verifyGate decides whether a verify-gated stop may proceed. It returns an
// empty refusal when this iteration's most recent verification run ended with
// VERDICT: PASS; otherwise the tool result to send the model and a one-line note
// for the loop's status row. Only runs started after the iteration began count,
// so a PASS from an earlier iteration (before the latest changes) never opens it.
func (t *LoopControlTool) verifyGate() (refusal, note string) {
	howTo := " Run the `verification` agent (Agent tool, subagent_type \"verification\", foreground) with the original task, the files you changed, your approach, and any previous FAIL findings, then call loop_control again only after it returns VERDICT: PASS. If something outside your control blocks the work, call loop_control with blocked: true and say what."
	if t.Tasks == nil {
		return "stop refused: this loop requires a passing verification, but no verification can be read here." + howTo, "stop refused: verification unavailable"
	}
	var start time.Time
	if ns := t.State.turnStart.Load(); ns != 0 {
		start = time.Unix(0, ns)
	}
	var latest *subagents.Task
	tasks := t.Tasks.List()
	for i := range tasks {
		task := &tasks[i]
		if task.Historical || !strings.EqualFold(task.AgentType, verificationAgentType) || task.Started.Before(start) {
			continue
		}
		if latest == nil || task.Started.After(latest.Started) {
			latest = task
		}
	}
	switch {
	case latest == nil:
		return "stop refused: no verification has run this iteration." + howTo, "stop refused: no verification this iteration"
	case latest.Status == subagents.TaskRunning:
		return "stop refused: the verification run is still in progress. Wait for its result (get_subagent_result), then call loop_control again after it returns VERDICT: PASS.", "stop refused: verification still running"
	case latest.Status != subagents.TaskCompleted:
		return "stop refused: the verification run did not complete (" + latest.Status.String() + ")." + howTo, "stop refused: verification " + latest.Status.String()
	}
	verdict := subagents.ParseVerdict(latest.Result)
	if verdict == subagents.VerdictPass {
		return "", ""
	}
	label := verdict
	if label == "" {
		label = "no verdict line"
	}
	report := strings.TrimSpace(latest.Result)
	if len(report) > verifyGateTail {
		report = "…" + report[len(report)-verifyGateTail:]
	}
	return fmt.Sprintf("stop refused: verification returned %s. Fix what it found, then verify again (pass its findings back so a re-check confirms each fix).%s\n\nVerification report (tail):\n%s", label, howTo, report),
		"stop refused: verification " + label
}
