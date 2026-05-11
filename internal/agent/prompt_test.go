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
	"list_git_changed_files",
	"git_branch_status",
	"git_show_file_at_rev",
	"git_diff_files",
	"git_stage_files",
	"git_unstage_files",
	"git_commit",
	"git_log_file",
	"git_blame_lines",
	"git_merge_base",
	"git_checkpoint",
	"rollback",
	"run_tests",
	"run_bash",
	"git",
	"todo_write",
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
		"memory_save and memory_forget",
		"Do NOT save",
		"Multi-step planning",
		"call todo_write BEFORE you start work",
		"Do NOT call todo_write for trivial single-step requests",
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("DefaultSystemPrompt is missing required directive %q", want)
		}
	}
}
