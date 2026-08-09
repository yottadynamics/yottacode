package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// TestSubagentsPicker_ShowsLatestActivityForRunning verifies the picker
// task rows surface what a running subagent is doing right now (its
// latest activity tick), and that terminal tasks do NOT get an activity
// segment appended — their status already conveys the outcome, and the
// last tick would be noise.
func TestSubagentsPicker_ShowsLatestActivityForRunning(t *testing.T) {
	tasks := []subagents.Task{
		{
			ID:         "aaaaaaaa1111",
			AgentType:  "review",
			Status:     subagents.TaskRunning,
			Started:    time.Now().Add(-10 * time.Second),
			Activities: []string{"started", "reading auth.go"},
		},
		{
			ID:         "bbbbbbbb2222",
			AgentType:  "review",
			Status:     subagents.TaskCompleted,
			Started:    time.Now().Add(-60 * time.Second),
			Activities: []string{"done-activity-hidden"},
		},
	}
	state := &subagentsPickerState{mode: subagentsPickerModeTasks, tasks: tasks}
	out := renderSubagentsPickerTasks(state, 120)

	if !strings.Contains(out, "reading auth.go") {
		t.Errorf("running task row should show its latest activity; got:\n%s", out)
	}
	if strings.Contains(out, "done-activity-hidden") {
		t.Errorf("completed task row must not append an activity tick; got:\n%s", out)
	}
}

// TestSubagentsPicker_InjectClosesPickerOnReturnedModel pins the `i` key
// regression: injectSubagentResult has a value receiver, so the handler
// must clear subagentsPickerOpen/subagentsPicker on the model it RETURNS.
// Clearing them on the local copy after `next` was already derived was a
// dead store — the picker stayed open over the wake turn, swallowing
// every keystroke (Esc closed the overlay instead of reaching the turn).
func TestSubagentsPicker_InjectClosesPickerOnReturnedModel(t *testing.T) {
	m := newTestModel(t)
	reg := subagents.NewRegistry()
	reg.Add(&subagents.Task{ID: "doneinject001234", AgentType: "review", Status: subagents.TaskRunning, Background: true})
	reg.MarkDone("doneinject001234", subagents.TaskCompleted, "verdict: looks fine", false, 0)
	m.subagentTasks = reg
	m.subagentsPicker = &subagentsPickerState{mode: subagentsPickerModeTasks, tasks: reg.List()}
	m.subagentsPickerOpen = true

	nm, _ := m.updateSubagentsPicker(tea.KeyPressMsg{Text: "i"})
	if nm.subagentsPickerOpen || nm.subagentsPicker != nil {
		t.Errorf("after `i` the RETURNED model must have the picker closed; open=%v state=%v",
			nm.subagentsPickerOpen, nm.subagentsPicker)
	}
}
