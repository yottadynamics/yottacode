package agent

import (
	"strings"
	"testing"
)

// The default system prompt enumerates every built-in tool the model
// can call. If a new tool ships and the prompt forgets to mention it,
// the model won't know to use it — silent feature regression.
//
// canonicalTools is the list the prompt advertises today. Add the new
// tool here AND to DefaultSystemPrompt when you ship one. The test
// fails if either side drops out of sync.
//
// Why a hand-rolled list rather than introspecting a Registry: the
// prompt's purpose is to surface tools to the *model*, and the prompt
// itself is the authoritative copy. Introspection would let an
// accidentally-unregistered tool slide silently.
var canonicalTools = []string{
	"read_file",
	"read_many_files",
	"write_file",
	"edit_file",
	"apply_diff",
	"mkdir",
	"copy_file",
	"move_file",
	"delete_file",
	"list_dir",
	"list_project_structure",
	"glob",
	"grep",
	"fetch_url",
	"memory_save",
	"memory_forget",
	"memory_search",
	"memory_audit",
	"session_recall",
	"list_git_changed_files",
	"git_branch_status",
	"git_show_file_at_rev",
	"git_diff_files",
	"git_stage_files",
	"git_unstage_files",
	"git_create_branch",
	"git_commit",
	"git_log_file",
	"git_blame_lines",
	"git_merge_base",
	"git_checkpoint",
	"rollback",
	"run_tests",
	"lsp_status",
	"lsp_symbols",
	"lsp_definition",
	"lsp_references",
	"lsp_hover",
	"lsp_diagnostics",
	"lsp_code_actions",
	"lsp_call_hierarchy",
	"run_bash",
	"git",
	"todo_write",
	"enter_plan_mode",
	"exit_plan_mode",
	"Skill",
}

func TestDefaultSystemPrompt_NamesEveryRegisteredTool(t *testing.T) {
	for _, name := range canonicalTools {
		if !strings.Contains(DefaultSystemPrompt, name) {
			t.Errorf("DefaultSystemPrompt does not mention tool %q — add it to the prompt or remove it from canonicalTools", name)
		}
	}
}

func TestDefaultSystemPrompt_KeepsActionDirectives(t *testing.T) {
	// These short directives are the load-bearing copy that gets the
	// model to use tools rather than describe them. They've been
	// trimmed away by accident in the past — guard them.
	for _, want := range []string{
		"Prefer tools over guessing",
		"Use edit_file for surgical changes",
		"call list_project_structure ONCE",
		"call read_many_files with all paths in a single call",
		"Do not narrate routine tool use",
		"Project memory upkeep",
		"YOTTACODE.md to reflect the new reality",
		"only when the project's *framing* has changed",
		"Memory management",
		"memory_save, memory_forget, memory_search, memory_audit, and session_recall",
		"Do NOT save",
		"Multi-step planning",
		"call todo_write BEFORE you start work",
		"Do NOT call todo_write for trivial single-step requests",
		"Creating or updating a todo card is NOT itself a request for permission",
		// Mode-switching honesty: the model may REQUEST plan mode via
		// enter_plan_mode but can never self-escalate to auto/yolo,
		// and must never claim a mode changed when it didn't. This
		// copy is what stops the "Okay, I'm in plan mode now"
		// role-play failure.
		"Mode switching",
		"You can REQUEST plan mode yourself",
		"You can NOT enable auto mode",
		"NEVER claim a mode changed",
		// Large-file read guidance: discourages the chunked-pagination
		// reflex (L0+100, L100+100, …) that wastes turns and context
		// on a file that fits in a single read.
		"prefer ONE read_file call with a generous limit",
		"stop — grep for the symbol you're actually hunting instead",
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("DefaultSystemPrompt is missing required directive %q", want)
		}
	}
}

// PlanModeAddendum is what the loop prepends to the system message
// when /plan is active. It's not part of DefaultSystemPrompt but
// shares the same load-bearing copy invariants — guard the directives
// the model needs to recognize the mode and the exit surface.
func TestPlanModeAddendum_KeepsCoreDirectives(t *testing.T) {
	for _, want := range []string{
		"PLAN MODE",
		"MUST NOT make any edits",
		"single allowed plan file",
		"call exit_plan_mode with NO arguments",
		"AUTO-APPROVAL ([A])",
		"MANUAL APPROVAL ([M])",
		"LATER ([L])",
		"KEEP PLANNING ([K])",
		"END THE TURN immediately",
		"Looping exit_plan_mode without user feedback",
		// Anti-investigate framing for recap-style questions — the
		// plan body is already in the addendum; the model shouldn't
		// go grep-hunting to answer "what was I planning."
		"answer DIRECTLY from this body",
		"Do NOT call read_file on the plan file",
		// Open-questions invariant: material ambiguity must be
		// resolved BEFORE exit_plan_mode. The approval modal is
		// hotkey-only — a plan with dangling open questions next to
		// it is a UX dead-end (the user can't type answers there).
		"Resolve material ambiguity BEFORE calling exit_plan_mode",
		"END THE TURN. Do NOT call exit_plan_mode in the same turn",
		"approval modal accepts hotkeys only",
		// Foreground-subagent nudge: plan-mode research benefits from
		// same-turn findings, so the addendum steers the parent toward
		// run_in_background:false when dispatching subagents.
		"Prefer run_in_background:false",
	} {
		if !strings.Contains(PlanModeAddendum, want) {
			t.Errorf("PlanModeAddendum is missing required directive %q", want)
		}
	}
	// Has exactly two %s slots so fmt.Sprintf can fill both the
	// plan-file path AND the file's current contents — the addendum
	// embeds the live plan body every iteration so resume + manual
	// edits Just Work.
	if strings.Count(PlanModeAddendum, "%s") != 2 {
		t.Errorf("PlanModeAddendum should contain exactly two %%s slots (path + contents); got %d", strings.Count(PlanModeAddendum, "%s"))
	}
	// Embed delimiters so the model can lift the content out
	// reliably even if it contains tricky characters.
	for _, want := range []string{"---PLAN-FILE-START---", "---PLAN-FILE-END---"} {
		if !strings.Contains(PlanModeAddendum, want) {
			t.Errorf("PlanModeAddendum missing delimiter %q", want)
		}
	}
}
