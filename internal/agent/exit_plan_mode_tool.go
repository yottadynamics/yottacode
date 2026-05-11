package agent

import (
	"context"
)

// ExitPlanModeTool is the model's signal that planning is finished and
// the plan file is ready for user approval. Mirrors Claude Code's
// `ExitPlanMode` exactly: the tool takes no `plan` argument — the
// content is read from the plan file the model has been writing to all
// along. Single source of truth (the file on disk), lower token usage,
// and no ambiguity about whether the approval card shows the same
// thing as the file the model intends to execute.
//
// The tool itself is intentionally minimal: RequiresApproval=true
// routes the call through the standard approval flow, the TUI reads
// the plan file from disk and renders a plan-specific approval card
// ([A]/[K] hotkeys) for `exit_plan_mode` rather than the generic
// preview, and on approve the TUI flips the shared PlanModeState.Active
// flag off before forwarding the decision. The loop's
// `deniedResultFor` is special-cased for this tool name so a [K] (Keep
// planning) returns refinement guidance to the model instead of the
// generic "denied by user".
//
// Execute therefore only runs on the approve path and unconditionally
// returns the "approved" message. The TUI is responsible for the
// "file is missing/empty" guard — it auto-denies before showing the
// approval card.
type ExitPlanModeTool struct{}

func (t *ExitPlanModeTool) Name() string { return "exit_plan_mode" }

func (t *ExitPlanModeTool) Description() string {
	return "Use this tool when you are in plan mode and have finished writing your plan to the plan file and are ready for user approval. " +
		"This tool does NOT take the plan content as a parameter — the user will see the contents of the plan file you've been writing to. " +
		"IMPORTANT: Only use this tool when the task requires planning the implementation steps of a task that requires writing code. For research tasks where you're gathering information, searching files, reading code etc., do NOT use this tool. " +
		"Before calling: ensure your plan file is complete and unambiguous. After calling: the user will see the rendered plan and pick approve (resume execution with full tool access) or keep-planning (you refine the plan further)."
}

// Schema is an empty object — exit_plan_mode takes no arguments. The
// model writes the plan to disk via write_file/edit_file first, then
// calls this tool to surface it for approval. Matches Claude Code's
// ExitPlanMode shape.
func (t *ExitPlanModeTool) Schema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// RequiresApproval is always true: every exit_plan_mode call goes
// through the approval card so the user can see the proposed plan
// before yottacode regains write access.
func (t *ExitPlanModeTool) RequiresApproval(string) bool { return true }

func (t *ExitPlanModeTool) PreviewCall(string) string {
	return "exit_plan_mode (reading plan from file)"
}

// Execute is only reached on the approve path — the loop short-circuits
// on Deny in promptForApproval and never calls the tool. Before
// showing the approval card the TUI inspects the plan file and
// auto-denies if it's missing or empty, so by the time we're here the
// file existed and the user said yes. Return the "approved" string and
// the model continues.
func (t *ExitPlanModeTool) Execute(_ context.Context, _ string) (string, error) {
	return "plan approved — exiting plan mode and resuming execution. Implement the approved plan now with full tool access.", nil
}

// ExitPlanModeRefusalMessage is what the loop returns to the model
// when the user picks [K] / Keep planning at the plan-approval card.
// Phrased firmly so the model STOPS THE TURN and waits for user
// feedback. Without the firmness, models read "call exit_plan_mode
// again when ready" and immediately re-call without changing the
// plan — looping the approval card forever. The user pressed [K]
// because they have something to say; the model must yield the turn
// so they can say it.
const ExitPlanModeRefusalMessage = "User chose to keep planning. " +
	"END THIS TURN NOW with a brief one-sentence question asking what they'd like to change about the plan. " +
	"Do NOT call exit_plan_mode again in this turn. " +
	"Do NOT edit the plan file in this turn. " +
	"Do NOT call any other tools in this turn. " +
	"Wait for the user's next message before doing anything else. " +
	"After they respond with feedback, you may revise the plan file and call exit_plan_mode again on the following turn."

// ExitPlanModeSavedForLaterMessage is what the loop returns to the
// model when the user picks [L] / approve and implement later. The
// plan is good but the user isn't starting work now — they'll resume
// via /plan list or --plan-resume in a future session. The message
// is phrased firmly to make the model END THE TURN: no more tool
// calls, no implementation, no "next steps" prose. The user will
// re-initiate when they're ready.
const ExitPlanModeSavedForLaterMessage = "user approved the plan but is saving it for implementation later. " +
	"The plan file is preserved on disk; the user will resume via /plan list when ready. " +
	"END THIS TURN NOW. Do NOT call any more tools. Do NOT implement any part of the plan. " +
	"Do NOT suggest next steps. Reply with a one-sentence acknowledgement and stop."
