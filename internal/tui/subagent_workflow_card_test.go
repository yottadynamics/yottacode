package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

const researchPhaseLine = "deep-research: Plan ✓ · Research ● · Verify ○ · Report ○ — 2 question(s), up to 2 at a time"

func workflowTask(activity, query string) subagents.Task {
	t := runningTask(agent.DeepResearchTaskType, "", "", activity)
	t.ID = "03016201abcdef"
	t.Prompt = query
	return t
}

func TestWorkflowCurrentPhase(t *testing.T) {
	for line, want := range map[string]string{
		"deep-research: Plan ● · Research ○ · Verify ○ · Report ○": "Plan",
		researchPhaseLine: "Research",
		"deep-research: Plan ✓ · Research ✓ · Verify ✓ · Report ●": "Report",
		"deep-research: Plan ✓ · Research ✓ · Verify ✓ · Report ✓": "Finishing",
		"starting…": "Starting",
		"":          "Starting",
		"deep-research: Plan ○ · Research ○ · Verify ○ · Report ○": "Starting",
	} {
		if got := workflowCurrentPhase(line); got != want {
			t.Errorf("workflowCurrentPhase(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestRenderWorkflowCard(t *testing.T) {
	wf := workflowTask(researchPhaseLine, "chunkah   vs\nbuild-chunked-oci")
	planner := runningTask("research-planner", "", "", "x")
	planner.Status = subagents.TaskCompleted
	all := []subagents.Task{
		runningTask("researcher", "", "", "x"),
		runningTask("researcher", "", "", "y"),
		runningTask("Explore", "", "", "unrelated"), // not a workflow agent
		planner,
		wf,
	}
	for _, i := range []int{0, 1, 3} {
		all[i].Started = time.Now() // started after the workflow
	}
	out := stripANSI(renderWorkflowCard(wf, all, 120, false))
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("card should be 3 lines, got %d:\n%s", len(lines), out)
	}
	// Only the agents running now are counted as "running" (they match the rows
	// beneath the card); the finished planner is a separate "done" count, and
	// the unrelated Explore subagent is not the workflow's at all.
	if !strings.Contains(lines[0], "Workflow deep-research — Research · 2 agents running · 1 done · ") || !strings.Contains(lines[0], "deep-research-03016201") {
		t.Errorf("title line wrong: %q", lines[0])
	}
	if !strings.Contains(lines[1], "Plan ✓ · Research ● · Verify ○ · Report ○") || strings.Contains(lines[1], "deep-research:") {
		t.Errorf("phase line wrong (prefix should be dropped): %q", lines[1])
	}
	if !strings.Contains(lines[2], "“chunkah vs build-chunked-oci”") {
		t.Errorf("query line wrong (whitespace should collapse): %q", lines[2])
	}
	if sel := stripANSI(renderWorkflowCard(wf, all, 120, true)); !strings.HasPrefix(strings.TrimLeft(sel, " "), "▶") {
		t.Errorf("selected card should carry the ▶ marker: %q", sel)
	}
}

func TestRenderWorkflowCard_NarrowWidthDoesNotPanic(t *testing.T) {
	wf := workflowTask(researchPhaseLine, strings.Repeat("q", 300))
	for _, w := range []int{20, 40, 60} {
		out := stripANSI(renderWorkflowCard(wf, []subagents.Task{wf}, w, false))
		for _, l := range strings.Split(out, "\n") {
			if got := len([]rune(l)); got > w+4 {
				t.Errorf("width %d: line is %d runes: %q", w, got, l)
			}
		}
	}
}

// The dock shows a Workflows section above the subagents section, and the
// workflow task itself is not counted as one of the agents.
func TestRenderSubagentDock_WorkflowSectionAboveAgents(t *testing.T) {
	tasks := []subagents.Task{
		runningTask("researcher", "", "", "web_search(a)"),
		runningTask("researcher", "", "", "web_search(b)"),
		workflowTask(researchPhaseLine, "compare x and y"),
	}
	out := stripANSI(renderSubagentDock(tasks, 120, "m", false, 0))
	wfHeader := strings.Index(out, "Workflows · 1")
	card := strings.Index(out, "Workflow deep-research — Research")
	agentsHeader := strings.Index(out, "subagents · 2 running")
	rows := strings.Index(out, "web_search(a)")
	if wfHeader < 0 || card < 0 || agentsHeader < 0 || rows < 0 {
		t.Fatalf("missing a piece of the dock (wf=%d card=%d agents=%d rows=%d):\n%s", wfHeader, card, agentsHeader, rows, out)
	}
	if !(wfHeader < card && card < agentsHeader && agentsHeader < rows) {
		t.Errorf("order should be Workflows header, card, subagents header, rows:\n%s", out)
	}
	if strings.Count(out, "tab to inspect") != 1 {
		t.Errorf("the hint should trail only the first header:\n%s", out)
	}
}

// A workflow that has not spawned anything yet shows just its card — no empty
// "subagents · 0 running" header.
func TestRenderSubagentDock_WorkflowAloneHasNoAgentsHeader(t *testing.T) {
	out := stripANSI(renderSubagentDock([]subagents.Task{workflowTask("", "q")}, 120, "m", false, 0))
	if !strings.Contains(out, "Workflows · 1") || strings.Contains(out, "subagents ·") {
		t.Errorf("workflow-only dock wrong:\n%s", out)
	}
	if !strings.Contains(out, "Starting") {
		t.Errorf("a workflow with no phase yet should read Starting:\n%s", out)
	}
}

// Keyboard focus must index the same order the dock renders: workflow first,
// even though the registry lists newest-first and the agents are newer.
func TestDockRunning_WorkflowsFirstAndFocusMatchesRender(t *testing.T) {
	reg := subagents.NewRegistry()
	wf := workflowTask(researchPhaseLine, "q")
	wf.Started = time.Now().Add(-time.Minute)
	reg.Add(&wf)
	for _, name := range []string{"researcher", "researcher"} {
		a := runningTask(name, "", "", "x")
		a.ID = subagents.NewTaskID()
		a.Started = time.Now()
		reg.Add(&a)
	}
	got := dockRunning(reg.List())
	if len(got) != 3 || !isWorkflowTask(got[0]) {
		t.Fatalf("workflow must come first, got %v", func() []string {
			var n []string
			for _, g := range got {
				n = append(n, g.AgentType)
			}
			return n
		}())
	}
	m := Model{subagentTasks: reg}
	focus := m.runningSubagents()
	for i := range got {
		if focus[i].ID != got[i].ID {
			t.Errorf("focus order diverges from render order at %d", i)
		}
	}
}
