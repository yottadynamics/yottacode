package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// A background shell job is not a model run: its dock row shows the command it
// is running, not a "starting…" placeholder, and no model name.
func TestRenderDockRow_ShellJobShowsCommandAndNoModel(t *testing.T) {
	task := subagents.Task{
		ID:         "cd7364bd42012453",
		AgentType:  "shell",
		Prompt:     "sleep 6;   echo   hi",
		Status:     subagents.TaskRunning,
		Background: true,
		NonAgent:   true,
	}
	row := renderDockRow(task, 160, "gpt-5.6-luna", false)
	if !strings.Contains(row, "$ sleep 6; echo hi") {
		t.Errorf("row should show the command with whitespace collapsed; got %q", row)
	}
	if strings.Contains(row, "starting…") {
		t.Errorf("a shell job must not show the subagent placeholder; got %q", row)
	}
	if strings.Contains(row, "gpt-5.6-luna") {
		t.Errorf("a shell job runs no model; got %q", row)
	}
	if !strings.Contains(row, "shell-cd7364bd") {
		t.Errorf("row should keep its traceable id label; got %q", row)
	}
}

func TestRenderDockRow_SubagentUnchanged(t *testing.T) {
	task := subagents.Task{ID: "aaaa1111bbbb2222", AgentType: "review", Status: subagents.TaskRunning, Background: true}
	row := renderDockRow(task, 160, "gpt-5.6-luna", false)
	if !strings.Contains(row, "starting…") || !strings.Contains(row, "gpt-5.6-luna") {
		t.Errorf("a subagent keeps the placeholder and default model; got %q", row)
	}
}
