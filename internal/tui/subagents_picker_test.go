package tui

import (
	"strings"
	"testing"
	"time"

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
