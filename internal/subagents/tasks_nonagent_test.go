package subagents

import (
	"fmt"
	"testing"
)

// A background shell job is a task like any other for the dock and stop, but it
// must not consume the subagent concurrency caps: a long-lived dev server would
// otherwise starve Agent/dispatch admission.
func TestNonAgentTasksAreExcludedFromConcurrencyCaps(t *testing.T) {
	r := NewRegistry()
	for i := range 3 {
		r.Add(&Task{ID: fmt.Sprintf("shell-%d", i), AgentType: "shell", Status: TaskRunning, Background: true, NonAgent: true})
	}
	if got := r.ActiveCount(); got != 0 {
		t.Fatalf("ActiveCount = %d, want 0 (shell jobs are not agent runs)", got)
	}
	if got := r.ActiveTotalCount(); got != 3 {
		t.Fatalf("ActiveTotalCount = %d, want 3", got)
	}
	// A cap of 1 still admits an agent task alongside three running shell jobs.
	if !r.TryReserve(&Task{ID: "agent-1", AgentType: "review", Status: TaskRunning, Background: true}, 1, false) {
		t.Fatalf("TryReserve refused an agent task because shell jobs were running")
	}
	if r.TryReserve(&Task{ID: "agent-2", AgentType: "review", Status: TaskRunning, Background: true}, 1, false) {
		t.Fatalf("the cap must still bound agent tasks")
	}
	if r.TryReserveBatch([]*Task{{ID: "b1", Status: TaskRunning, Background: true}}, 1, false) {
		t.Fatalf("TryReserveBatch must count the running agent task and refuse")
	}
	if got := r.ActiveCount(); got != 1 {
		t.Fatalf("ActiveCount = %d, want 1", got)
	}
}

func TestNonAgentTaskCanStillBeCanceled(t *testing.T) {
	r := NewRegistry()
	r.Add(&Task{ID: "shell-1", AgentType: "shell", Status: TaskRunning, Background: true, NonAgent: true})
	canceled := false
	r.AttachCancel("shell-1", func() { canceled = true })
	if !r.Cancel("shell-1") || !canceled {
		t.Fatalf("/subagents stop must reach a shell job's cancel func")
	}
}
