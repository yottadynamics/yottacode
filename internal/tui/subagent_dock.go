package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/yottadynamics/yottacode/internal/subagents"
)

// dockMaxRows caps how many running subagents the dock lists before it
// collapses the rest into a "+N more" line, so a large fan-out can't push
// the conversation off-screen.
const dockMaxRows = 8

// renderSubagentDock renders the pinned live-status region shown just above
// the status bar: a header plus, per running subagent, a two-line entry —
// line 1 has the agent type, latest activity, and elapsed time; line 2 has
// the model and a live context-fill bar (and the branch for dispatch
// worktree tasks). Returns "" when no subagent is running, so the dock
// collapses to nothing on an idle session.
//
// defaultModel is shown for children that inherited the session model
// (Task.Model is "" in that case). No separator rule is drawn — the dock
// sits flush below the cmdline.
//
// It reads the live registry snapshot at render time rather than tracking
// events, so it's always current; elapsed times + context fill advance on
// the spinner tick that drives redraws while subagents run.
func renderSubagentDock(tasks []subagents.Task, width int, defaultModel string, focused bool, cursor int) string {
	if width < 20 {
		width = 20
	}
	running := make([]subagents.Task, 0, len(tasks))
	for _, t := range tasks {
		if t.Status == subagents.TaskRunning {
			running = append(running, t)
		}
	}
	if len(running) == 0 {
		return ""
	}
	// Clamp the focus cursor defensively: a subagent may have finished since
	// the last keystroke, leaving the cursor past the end of the running set.
	if cursor >= len(running) {
		cursor = len(running) - 1
	}
	if cursor < 0 {
		cursor = 0
	}

	// Header: count + (when the whole set shares one dispatch batch) the
	// batch id, so a fan-out reads as one unit. A dim hint trails it: how
	// to enter keyboard focus, or the nav keys once focused.
	header := fmt.Sprintf("subagents · %d running", len(running))
	if batch := commonBatch(running); batch != "" {
		header += " · batch " + batch
	}
	hint := "tab to inspect"
	if focused {
		hint = "↑/↓ select · enter open · q/esc exit"
	}

	var b strings.Builder
	b.WriteString(styleSubagentTableHeader.Render(header))
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render(hint))
	b.WriteString("\n")

	shown := running
	overflow := 0
	if len(shown) > dockMaxRows {
		overflow = len(shown) - dockMaxRows
		shown = shown[:dockMaxRows]
	}
	for i, t := range shown {
		b.WriteString(renderDockRow(t, width, defaultModel, focused && i == cursor))
		if i < len(shown)-1 || overflow > 0 {
			b.WriteString("\n")
		}
	}
	if overflow > 0 {
		b.WriteString(styleSubagentMeta.Render(fmt.Sprintf("  … +%d more", overflow)))
	}
	return b.String()
}

// renderDockRow renders one running-subagent entry as aligned columns:
//
//	▸ <agent>     <latest activity>        <model>          <ctx>      <source>-<id>
//
// agent/model/ctx are fixed-width so they line up across rows; the activity
// flexes to fill the gap between them. The line ends with a stable
// identifier (see dockTaskLabel) so every row is traceable to /subagents.
func renderDockRow(t subagents.Task, width int, defaultModel string, selected bool) string {
	const (
		indent = 2
		agentW = 11
		modelW = 26
		ctxW   = 11
		nGaps  = 4 // single spaces separating the 5 content fields after the glyph
	)
	// Selected rows get a "▶" marker + underlined agent name so the focus
	// cursor is unmistakable; unselected rows use the lighter "▸".
	glyph := "▸ "
	glyphStyle := styleSubagentRunning
	agentStyle := styleSubagentLabel
	if selected {
		glyph = "▶ "
		agentStyle = styleSubagentLabel.Underline(true)
	}
	model := strings.TrimSpace(t.Model)
	if model == "" {
		model = defaultModel
	}
	dim := lipgloss.NewStyle().Foreground(colorDim)
	label := dockTaskLabel(t)

	agentCell := padRight(truncateForRender(t.AgentType, agentW), agentW)
	modelCell := padRight(truncateForRender(shortModel(model), modelW), modelW)
	ctxCell := padRight(dockCtxSegment(t.CtxTokens, t.CtxWindow), ctxW)

	// Activity flexes to fill what's left before the fixed model/ctx/label
	// columns, so those columns align across every row.
	flexW := width - indent - lipgloss.Width(glyph) - agentW - modelW - ctxW - lipgloss.Width(label) - nGaps
	if flexW < 8 {
		flexW = 8
	}
	activityCell := padRight(truncateForRender(latestActivity(t), flexW), flexW)

	return strings.Repeat(" ", indent) +
		glyphStyle.Render(glyph) +
		agentStyle.Render(agentCell) + " " +
		styleSubagentActivity.Render(activityCell) + " " +
		dim.Render(modelCell) + " " +
		dim.Render(ctxCell) + " " +
		dim.Render(label)
}

// dockTaskLabel is the trailing identifier on a dock row: the dispatch
// worktree branch when present (already "dispatch-<batch>-<n>"), otherwise
// "<source>-<id>" — source is "dispatch" for a batch member or the lowered
// agent type for a standalone subagent, and id is the short task id shown
// in /subagents and the scrollback cards.
func dockTaskLabel(t subagents.Task) string {
	if b := shortBranch(t.Branch); b != "" {
		return b
	}
	source := strings.ToLower(t.AgentType)
	if t.BatchID != "" {
		source = "dispatch"
	}
	id := t.ID
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		return source
	}
	return source + "-" + id
}

// dockCtxSegment renders the subagent's context window size + fill percent,
// e.g. "128K (1%)". The fill bar + raw used-token count were dropped for
// compactness on the single-line row — the window + percent are the
// at-a-glance signal. Returns "" until the window is known (CtxWindow <= 0).
func dockCtxSegment(tokens, window int) string {
	if window <= 0 {
		return ""
	}
	pct := 0
	if tokens > 0 {
		pct = int(float64(tokens) / float64(window) * 100)
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("%s (%d%%)", formatTokens(window), pct)
}

// shortModel strips a vendor/namespace prefix so the dock shows just the
// model name: "deepseek-ai/deepseek-v4-pro" → "deepseek-v4-pro".
func shortModel(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 && i+1 < len(model) {
		return model[i+1:]
	}
	return model
}

// latestActivity returns the most recent activity line for a task, or a
// placeholder when none has been recorded yet.
func latestActivity(t subagents.Task) string {
	if len(t.Activities) == 0 {
		return "starting…"
	}
	return strings.TrimSpace(t.Activities[len(t.Activities)-1])
}

// shortBranch compacts a dispatch worktree branch for the dock: it strips
// the "worktree-" management prefix and keeps the tail (which carries the
// distinguishing "...-N" suffix). Empty for read-only subtasks. The prefix
// is duplicated here rather than imported from internal/worktree to keep the
// TUI package free of that dependency for one string.
func shortBranch(branch string) string {
	if branch == "" {
		return ""
	}
	return strings.TrimPrefix(branch, "worktree-")
}

// runningSubagents returns the currently-running tasks in the same order
// the dock renders them (registry List() order, newest-first), capped at
// dockMaxRows so the focus cursor only ever addresses a visible row.
func (m Model) runningSubagents() []subagents.Task {
	if m.subagentTasks == nil {
		return nil
	}
	var out []subagents.Task
	for _, t := range m.subagentTasks.List() {
		if t.Status == subagents.TaskRunning {
			out = append(out, t)
			if len(out) == dockMaxRows {
				break
			}
		}
	}
	return out
}

// updateDockFocus handles keystrokes while the live dock has focus:
// up/down (or k/j) move the cursor among running subagents, Enter opens the
// selected subagent's transcript, Esc/Tab/q return focus to the cmdline.
// Exits automatically if every subagent finished while focused.
func (m Model) updateDockFocus(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	running := m.runningSubagents()
	if len(running) == 0 {
		m.dockFocused = false
		return m, nil
	}
	if m.dockCursor >= len(running) {
		m.dockCursor = len(running) - 1
	}
	if m.dockCursor < 0 {
		m.dockCursor = 0
	}
	switch msg.Code {
	case tea.KeyEsc, tea.KeyTab:
		m.dockFocused = false
		return m, nil
	case tea.KeyUp:
		if m.dockCursor > 0 {
			m.dockCursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.dockCursor < len(running)-1 {
			m.dockCursor++
		}
		return m, nil
	case tea.KeyEnter:
		t := running[m.dockCursor]
		m.dockFocused = false
		return m.openTranscriptInPager(t.TranscriptPath, t.ID[:8], t.Status == subagents.TaskRunning)
	default:
		if r := []rune(msg.Text); len(r) == 1 {
			switch r[0] {
			case 'k':
				if m.dockCursor > 0 {
					m.dockCursor--
				}
			case 'j':
				if m.dockCursor < len(running)-1 {
					m.dockCursor++
				}
			case 'q':
				m.dockFocused = false
			}
		}
	}
	return m, nil
}

// commonBatch returns the shared BatchID when every task belongs to the
// same dispatch batch, else "". Lets the header label a coherent fan-out.
func commonBatch(tasks []subagents.Task) string {
	batch := ""
	for _, t := range tasks {
		if t.BatchID == "" {
			return ""
		}
		if batch == "" {
			batch = t.BatchID
		} else if batch != t.BatchID {
			return ""
		}
	}
	return batch
}
