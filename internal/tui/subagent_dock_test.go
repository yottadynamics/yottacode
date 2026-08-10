package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

func runningTask(agent, branch, batch, activity string) subagents.Task {
	return subagents.Task{
		AgentType:  agent,
		Status:     subagents.TaskRunning,
		Started:    time.Now().Add(-15 * time.Second),
		Branch:     branch,
		BatchID:    batch,
		Activities: []string{activity},
	}
}

func TestRenderSubagentDock_EmptyWhenNoneRunning(t *testing.T) {
	if got := renderSubagentDock(nil, 80, "m", false, 0); got != "" {
		t.Errorf("nil tasks: want empty, got %q", got)
	}
	done := []subagents.Task{{AgentType: "x", Status: subagents.TaskCompleted}}
	if got := renderSubagentDock(done, 80, "m", false, 0); got != "" {
		t.Errorf("only-completed tasks: want empty dock, got %q", got)
	}
}

func TestRenderSubagentDock_ListsRunning(t *testing.T) {
	tasks := []subagents.Task{
		runningTask("writer", "worktree-dispatch-ab12cd34-1", "ab12cd34", "editing parser.go"),
		runningTask("writer", "worktree-dispatch-ab12cd34-2", "ab12cd34", "running tests"),
		{AgentType: "x", Status: subagents.TaskCompleted}, // filtered out
	}
	out := renderSubagentDock(tasks, 120, "session-model", false, 0)
	for _, want := range []string{"2 running", "batch ab12cd34", "writer", "editing parser.go", "running tests"} {
		if !strings.Contains(out, want) {
			t.Errorf("dock missing %q in:\n%s", want, out)
		}
	}
	// Branch is shown with the management prefix stripped (now on line 2).
	if !strings.Contains(out, "dispatch-ab12cd34-1") {
		t.Errorf("dock should show shortened branch, got:\n%s", out)
	}
	if strings.Contains(out, "worktree-dispatch-ab12cd34-1") {
		t.Errorf("dock should strip the worktree- prefix, got:\n%s", out)
	}
	// Inherited model (Task.Model == "") falls back to the session model.
	if !strings.Contains(out, "session-model") {
		t.Errorf("dock should show the (inherited) model, got:\n%s", out)
	}
	// No separator rule line.
	if strings.Contains(out, "────") {
		t.Errorf("dock should not draw a separator rule, got:\n%s", out)
	}
}

func TestRenderSubagentDock_ModelAndContext(t *testing.T) {
	task := runningTask("writer", "deepseek-ai/deepseek-v4-pro-branch", "", "editing")
	task.Branch = "worktree-dispatch-cf94fbda-1"
	task.Model = "deepseek-ai/deepseek-v4-pro" // vendor-prefixed routed model
	task.CtxTokens = 1700
	task.CtxWindow = 128000
	out := renderSubagentDock([]subagents.Task{task}, 140, "session-model", false, 0)
	// Model shows WITHOUT the vendor prefix.
	if !strings.Contains(out, "deepseek-v4-pro") {
		t.Errorf("dock should show the short model, got:\n%s", out)
	}
	if strings.Contains(out, "deepseek-ai/") {
		t.Errorf("dock should strip the vendor prefix, got:\n%s", out)
	}
	// ctx = window + percent only — no bar, no used-token count, no "ctx" label.
	for _, want := range []string{"128K", "(1%)"} {
		if !strings.Contains(out, want) {
			t.Errorf("dock ctx missing %q in:\n%s", want, out)
		}
	}
	for _, absent := range []string{"ctx ", "1.7K", "█", "░"} {
		if strings.Contains(out, absent) {
			t.Errorf("dock should not contain %q anymore, got:\n%s", absent, out)
		}
	}
}

func TestDockCtxSegment(t *testing.T) {
	if got := dockCtxSegment(0, 0); got != "" {
		t.Errorf("unknown window: want empty, got %q", got)
	}
	got := dockCtxSegment(1700, 128000)
	if got != "128K (1%)" {
		t.Errorf("ctx segment = %q, want %q", got, "128K (1%)")
	}
}

func TestRenderSubagentDock_Hints(t *testing.T) {
	tasks := []subagents.Task{runningTask("writer", "", "", "x")}
	unfocused := renderSubagentDock(tasks, 100, "m", false, 0)
	if !strings.Contains(unfocused, "tab to inspect") {
		t.Errorf("unfocused hint should show 'tab to inspect', got:\n%s", unfocused)
	}
	focused := renderSubagentDock(tasks, 100, "m", true, 0)
	if !strings.Contains(focused, "exit") {
		t.Errorf("focused hint should show the exit key, got:\n%s", focused)
	}
}

func TestDockTaskLabel(t *testing.T) {
	// dispatch worktree task → its branch (already "dispatch-…")
	if got := dockTaskLabel(subagents.Task{Branch: "worktree-dispatch-ab12cd34-1"}); got != "dispatch-ab12cd34-1" {
		t.Errorf("worktree task label = %q, want dispatch-ab12cd34-1", got)
	}
	// dispatch read task (batch member, no branch) → dispatch-<short id>
	if got := dockTaskLabel(subagents.Task{BatchID: "b1", AgentType: "Explore", ID: "92ea734df7395c16"}); got != "dispatch-92ea734d" {
		t.Errorf("dispatch read label = %q, want dispatch-92ea734d", got)
	}
	// standalone subagent → <lowered type>-<short id>
	if got := dockTaskLabel(subagents.Task{AgentType: "Explore", ID: "b5ca33acf2c0d5e4"}); got != "explore-b5ca33ac" {
		t.Errorf("standalone label = %q, want explore-b5ca33ac", got)
	}
}

func TestShortModel(t *testing.T) {
	cases := map[string]string{
		"deepseek-ai/deepseek-v4-pro": "deepseek-v4-pro",
		"claude-haiku-4-5":            "claude-haiku-4-5",
		"a/b/c":                       "c",
		"":                            "",
	}
	for in, want := range cases {
		if got := shortModel(in); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderSubagentDock_NoBatchHeaderWhenMixed(t *testing.T) {
	tasks := []subagents.Task{
		runningTask("explore", "", "", "grep foo"), // no batch (standalone Agent)
		runningTask("writer", "worktree-x-1", "b1", "editing"),
	}
	out := renderSubagentDock(tasks, 100, "m", false, 0)
	if strings.Contains(out, "batch ") {
		t.Errorf("mixed/standalone tasks should not show a batch header:\n%s", out)
	}
	if !strings.Contains(out, "2 running") {
		t.Errorf("want running count, got:\n%s", out)
	}
}

func TestCommonBatch(t *testing.T) {
	same := []subagents.Task{{BatchID: "a"}, {BatchID: "a"}}
	if commonBatch(same) != "a" {
		t.Errorf("same batch: want a")
	}
	mixed := []subagents.Task{{BatchID: "a"}, {BatchID: "b"}}
	if commonBatch(mixed) != "" {
		t.Errorf("mixed batch: want empty")
	}
	withStandalone := []subagents.Task{{BatchID: "a"}, {BatchID: ""}}
	if commonBatch(withStandalone) != "" {
		t.Errorf("any empty batch: want empty")
	}
}

func dockTestRegistry(n int) *subagents.Registry {
	reg := subagents.NewRegistry()
	for i := 0; i < n; i++ {
		reg.Add(&subagents.Task{
			ID:        fmt.Sprintf("task%04d-aaaa", i),
			AgentType: "writer",
			Status:    subagents.TaskRunning,
			Started:   time.Now(),
		})
	}
	return reg
}

func TestUpdateDockFocus_Navigation(t *testing.T) {
	m := Model{subagentTasks: dockTestRegistry(3), dockFocused: true, dockCursor: 0}

	m, _ = m.updateDockFocus(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.dockCursor != 1 {
		t.Fatalf("down: cursor = %d, want 1", m.dockCursor)
	}
	m, _ = m.updateDockFocus(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.updateDockFocus(tea.KeyPressMsg{Code: tea.KeyDown}) // clamp at last
	if m.dockCursor != 2 {
		t.Fatalf("down clamp: cursor = %d, want 2", m.dockCursor)
	}
	m, _ = m.updateDockFocus(tea.KeyPressMsg{Text: "k"})
	if m.dockCursor != 1 {
		t.Fatalf("k: cursor = %d, want 1", m.dockCursor)
	}
	m, _ = m.updateDockFocus(tea.KeyPressMsg{Code: tea.KeyUp})
	m, _ = m.updateDockFocus(tea.KeyPressMsg{Code: tea.KeyUp}) // clamp at 0
	if m.dockCursor != 0 {
		t.Fatalf("up clamp: cursor = %d, want 0", m.dockCursor)
	}
	m, _ = m.updateDockFocus(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.dockFocused {
		t.Fatalf("esc should exit dock focus")
	}
}

func TestSubagentDock_TabFocusIgnoresShiftTab(t *testing.T) {
	m, _ := newPlanModeTestModel(t)
	m.subagentTasks = dockTestRegistry(1)

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.dockFocused {
		t.Fatalf("Shift+Tab should cycle agent mode, not focus the subagent dock")
	}
	if !m.cfg.PlanMode.IsActive() {
		t.Fatalf("Shift+Tab should cycle normal mode into plan mode")
	}
}

func TestUpdateDockFocus_ExitsWhenNoneRunning(t *testing.T) {
	m := Model{subagentTasks: subagents.NewRegistry(), dockFocused: true}
	m, _ = m.updateDockFocus(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.dockFocused {
		t.Fatalf("dock should unfocus when no subagent is running")
	}
}

func TestRunningSubagents_CapsAtDockMaxRows(t *testing.T) {
	m := Model{subagentTasks: dockTestRegistry(dockMaxRows + 4)}
	if got := len(m.runningSubagents()); got != dockMaxRows {
		t.Errorf("runningSubagents len = %d, want %d (capped)", got, dockMaxRows)
	}
}

func TestRenderSubagentDock_OverflowCollapses(t *testing.T) {
	var tasks []subagents.Task
	for i := 0; i < dockMaxRows+3; i++ {
		tasks = append(tasks, runningTask("writer", "", "", "working"))
	}
	out := renderSubagentDock(tasks, 100, "m", false, 0)
	if !strings.Contains(out, "+3 more") {
		t.Errorf("expected overflow line '+3 more', got:\n%s", out)
	}
}
