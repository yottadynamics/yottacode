package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// workflowAgentTypes are the subagent types a deep-research run spawns; the
// card counts the ones started since the workflow began.
var workflowAgentTypes = map[string]bool{
	"research-planner": true, "researcher": true,
	"research-verifier": true, "research-synthesizer": true,
}

// isWorkflowTask reports whether t is a workflow-level task (rendered as a
// card) rather than an ordinary subagent (rendered as a row).
func isWorkflowTask(t subagents.Task) bool {
	return t.AgentType == agent.DeepResearchTaskType
}

// renderWorkflowCard renders a running workflow as a three-line dock card:
//
//	◆ Workflow deep-research — Research · 4 agents running · 1 done · 58s   deep-research-03016201
//	    Plan ✓ · Research ● · Verify ○ · Report ○ — 4 question(s), up to 4 at a time
//	    “chunkah vs build-chunked-oci”
//
// all is every task in the registry snapshot, used only to count the workflow's
// agents that are running now (the rows beneath the card) and those finished.
// The card occupies the same slot a row would, so dock focus and Enter-to-open
// index it like any other running task.
func renderWorkflowCard(t subagents.Task, all []subagents.Task, width int, selected bool) string {
	const indent = 2
	glyph := "◆ "
	titleStyle := styleSubagentLabel
	if selected {
		glyph = "▶ "
		titleStyle = styleSubagentLabel.Underline(true)
	}
	dim := lipgloss.NewStyle().Foreground(colorDim)
	label := dockTaskLabel(t)

	phaseLine := latestActivity(t)
	// Count what the rows below show: agents running now, with the ones already
	// finished kept separate. A cumulative "started" total read as a mismatch
	// ("5 agents" over 2 visible rows once the planner and researchers were done).
	running, done := 0, 0
	for _, o := range all {
		if !workflowAgentTypes[o.AgentType] || o.Started.Before(t.Started) {
			continue
		}
		if o.Status == subagents.TaskRunning {
			running++
		} else {
			done++
		}
	}
	counts := pluralAgents(running) + " running"
	if done > 0 {
		counts += fmt.Sprintf(" · %d done", done)
	}
	title := fmt.Sprintf("Workflow %s — %s · %s · %s",
		t.AgentType, workflowCurrentPhase(phaseLine), counts,
		formatDuration(time.Since(t.Started)))

	// The trailing task label is the first thing to go on a narrow terminal:
	// the title (workflow, phase, agents, elapsed) is what matters.
	titleW := width - indent - lipgloss.Width(glyph) - lipgloss.Width(label) - 2
	if titleW < 24 {
		label = ""
		titleW = width - indent - lipgloss.Width(glyph)
	}
	titleW = max(titleW, 12)
	body := strings.Repeat(" ", indent+2)
	bodyW := max(width-len(body), 12)

	var b strings.Builder
	b.WriteString(strings.Repeat(" ", indent))
	b.WriteString(styleSubagentRunning.Render(glyph))
	if label == "" {
		b.WriteString(titleStyle.Render(truncateForRender(title, titleW)))
	} else {
		b.WriteString(titleStyle.Render(padRight(truncateForRender(title, titleW), titleW)))
		b.WriteString("  ")
		b.WriteString(dim.Render(label))
	}
	b.WriteString("\n" + body + styleSubagentActivity.Render(truncateForRender(strings.TrimPrefix(phaseLine, t.AgentType+": "), bodyW)))
	if q := strings.TrimSpace(strings.Join(strings.Fields(t.Prompt), " ")); q != "" {
		b.WriteString("\n" + body + dim.Render(truncateForRender("“"+q+"”", bodyW)))
	}
	return b.String()
}

func pluralAgents(n int) string {
	if n == 1 {
		return "1 agent"
	}
	return fmt.Sprintf("%d agents", n)
}

// workflowCurrentPhase names the phase marked ● in a WorkflowPhase line such as
// "deep-research: Plan ✓ · Research ● · Verify ○ · Report ○ — 4 question(s)".
// Before the first phase event it reads "Starting"; once every phase is ✓ it
// reads "Finishing" (the report is being written and the task closed).
func workflowCurrentPhase(line string) string {
	_, rest, ok := strings.Cut(line, ": ")
	if !ok {
		return "Starting"
	}
	rest, _, _ = strings.Cut(rest, " — ")
	parts := strings.Split(rest, " · ")
	allDone := len(parts) > 0
	for _, p := range parts {
		if name, ok := strings.CutSuffix(p, " ●"); ok {
			return name
		}
		if !strings.HasSuffix(p, " ✓") {
			allDone = false
		}
	}
	if allDone && rest != "" {
		return "Finishing"
	}
	return "Starting"
}
