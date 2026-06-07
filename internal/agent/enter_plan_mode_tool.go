package agent

import (
	"context"
	"fmt"
)

// EnterPlanModeTool is the model's request to switch the session into
// plan mode. Mirrors Claude Code's `EnterPlanMode`: when the user asks
// to plan first in natural language ("make a plan before coding",
// "drop into plan mode"), the model calls this instead of role-playing
// a mode it isn't in — without the tool, the model has no way to make
// the request real and tends to claim a mode change that never
// happened, with none of plan mode's read-only enforcement behind it.
//
// The tool itself is intentionally minimal, the same shape as
// ExitPlanModeTool: RequiresApproval=true routes the call through the
// approval flow, the TUI renders a dedicated [Y]/[N] card for it, and
// on approve the TUI runs the same entry sequence as /plan (exit auto
// mode, flip the shared PlanModeState, derive the plan file from the
// turn's user message) BEFORE forwarding the decision. Execute
// therefore only runs once the mode is genuinely active.
//
// The schema filter in streamIteration advertises this tool only while
// plan mode is OFF (inverse of exit_plan_mode), and
// executeToolCallImpl's boundary-tool guard ensures the call always
// prompts — yolo, auto mode, bypass, and permissions Allow rules never
// auto-approve it, because the approval IS the handshake that flips
// the TUI's state. Registered in the TUI build only: oneshot has no
// approval surface to host the handshake.
type EnterPlanModeTool struct {
	// State is the session's shared plan-mode state, wired at
	// registration. Execute reads it on the approve path to report the
	// resolved plan file back to the model (the TUI fills it before
	// sending AllowOnce, so the channel receive orders the read).
	State *PlanModeState
}

func (t *EnterPlanModeTool) Name() string { return "enter_plan_mode" }

func (t *EnterPlanModeTool) Description() string {
	return "Request to switch the session into plan mode — a read-only research mode where you investigate the codebase and write an implementation plan for user approval before any code changes. " +
		"Use this when the user asks you to plan first (\"make a plan\", \"drop into plan mode\", \"let's design this before implementing\") or when you're about to start a non-trivial implementation whose approach the user has not yet agreed to. " +
		"The user sees a confirmation card; once they approve, you lose write access except to the plan file, and you present the finished plan via exit_plan_mode. " +
		"Do NOT use this for pure research or Q&A that ends in an answer rather than code changes, and do NOT call it when plan mode is already active."
}

// Schema is an empty object — like exit_plan_mode, the tool carries no
// arguments. The plan topic comes from the conversation itself: the
// TUI derives the plan-file slug from the turn's user message.
func (t *EnterPlanModeTool) Schema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// RequiresApproval is always true: entering plan mode mid-turn changes
// what every subsequent tool call is allowed to do, so the user
// confirms the transition. The loop's boundary-tool guard keeps this
// prompt alive even under yolo/auto/bypass.
func (t *EnterPlanModeTool) RequiresApproval(string) bool { return true }

func (t *EnterPlanModeTool) PreviewCall(string) string {
	return "enter_plan_mode (read-only planning until the plan is approved)"
}

// Execute is only reached on the approve path — the TUI has already
// flipped the shared state and (when the turn carried a user message)
// resolved the plan file. Report both so the model can start writing
// the plan immediately; the per-iteration plan-mode addendum reinforces
// the rules from the next iteration on.
func (t *EnterPlanModeTool) Execute(_ context.Context, _ string) (string, error) {
	if t.State.IsActive() && t.State.PlanFile != "" {
		return fmt.Sprintf(
			"entered plan mode. You are now read-only except for the plan file at %s — investigate the codebase, then write your plan there and call exit_plan_mode when it's ready for approval.",
			t.State.PlanFile,
		), nil
	}
	return "entered plan mode. You are now read-only; your next user message will name the plan file — until then, investigate with read tools and ask clarifying questions.", nil
}

// EnterPlanModeRefusalMessage is what the loop returns to the model
// when the user picks [N] at the enter-plan-mode card. Phrased firmly
// so the model continues in the CURRENT mode instead of re-requesting
// — the user saw the card and said no; re-calling would loop it.
const EnterPlanModeRefusalMessage = "user declined entering plan mode. " +
	"Continue in the current mode and do NOT call enter_plan_mode again this turn. " +
	"If they want plan mode later they can enter it themselves with /plan or Shift+Tab."
