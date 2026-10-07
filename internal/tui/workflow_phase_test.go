package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// A WorkflowPhase event from deep_research becomes one transcript line, so a
// long blocking run shows where it is instead of looking stuck.
func TestModel_WorkflowPhase_AppendsPhaseLine(t *testing.T) {
	m := newTestModel(t)
	phases := []string{"Plan", "Research", "Verify", "Report"}
	m, _ = applyMsg(m, agentEventMsg{ev: agent.WorkflowPhase{
		Workflow: "deep-research", Phases: phases, Current: 1, Detail: "4 question(s)",
	}})
	got := strings.Join(m.historyLines, "\n")
	if !strings.Contains(got, "Plan ✓ · Research ● · Verify ○ · Report ○") {
		t.Errorf("phase line missing from transcript:\n%s", got)
	}
}
