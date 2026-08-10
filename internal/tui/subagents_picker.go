package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// subagentsPickerMode is the active view inside the /subagents picker.
// The picker can show two distinct lists: the session's task history
// (running/done subagent runs) or the catalog of available agent
// types (built-in + custom). The `t` key toggles between them so the
// user can browse types without having to dump them into scrollback.
type subagentsPickerMode int

const (
	subagentsPickerModeTasks subagentsPickerMode = iota
	subagentsPickerModeTypes
)

// subagentsPickerState backs the /subagents inline overlay. Pattern
// mirrors plansPickerState and sessionsPickerState: cursor index
// drives the highlighted row, Up/Down navigate, Enter opens the
// transcript in the system pager (tasks mode) or is a no-op (types
// mode), `s` stops a running task, `r` refreshes the snapshot, `t`
// toggles between tasks and types views, Esc closes. Lifetime is one
// /subagents invocation.
//
// Both lists snapshot at open time (and on `r`/`t`). We deliberately
// don't auto-refresh on a tick because the user reads the overlay
// statically — a refresh keystroke is enough.
type subagentsPickerState struct {
	mode   subagentsPickerMode
	tasks  []subagents.Task
	types  []subagents.AgentConfig
	cursor int
	// status holds a one-line note the picker shows below the list
	// (e.g. "canceled task abc12345" after pressing `s`). Cleared on
	// every keystroke that changes the cursor so it doesn't linger.
	status string
}

// openSubagentsPicker captures a snapshot of the task registry and
// installs the picker on m in tasks mode. Empty registry still
// opens the picker (with an empty-state hint) so the user can
// confirm "nothing running" without re-typing the command.
func (m *Model) openSubagentsPicker() {
	m.openSubagentsPickerInMode(subagentsPickerModeTasks)
}

// openSubagentsPickerInMode opens the picker in a specific view.
// Used by `/subagents types` to jump directly to the types catalog
// instead of dumping it to scrollback.
func (m *Model) openSubagentsPickerInMode(mode subagentsPickerMode) {
	if m.subagentTasks == nil {
		m.appendLine(styleError.Render("subagents are not wired in this session (Agent tool unavailable)"))
		return
	}
	state := &subagentsPickerState{mode: mode}
	state.tasks = m.subagentTasks.List()
	if m.subagentTool != nil {
		state.types = m.subagentTool.AgentConfigs()
	}
	m.subagentsPicker = state
	m.subagentsPickerOpen = true
}

// updateSubagentsPicker handles keystrokes while the picker is open.
// Mirrors the navigation contract of the other inline overlays:
// Up/Down to move, Enter to view, Esc to close. Additional keys:
// `s` stops a running task; `r` refreshes the task list.
func (m Model) updateSubagentsPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.subagentsPicker == nil {
		m.subagentsPickerOpen = false
		return m, nil
	}
	p := m.subagentsPicker
	rowCount := p.rowCount()
	switch msg.Code {
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		p.status = ""
		return m, nil
	case tea.KeyDown:
		if p.cursor < rowCount-1 {
			p.cursor++
		}
		p.status = ""
		return m, nil
	case tea.KeyEnter:
		// Enter only does work in tasks mode (open transcript). In
		// types mode it's a no-op — the row is informational.
		if p.mode != subagentsPickerModeTasks || len(p.tasks) == 0 {
			return m, nil
		}
		chosen := p.tasks[p.cursor]
		m.subagentsPickerOpen = false
		m.subagentsPicker = nil
		return m.openTranscriptInPager(chosen.TranscriptPath, chosen.ID[:8], chosen.Status == subagents.TaskRunning)
	case tea.KeyEsc:
		m.subagentsPickerOpen = false
		m.subagentsPicker = nil
		return m, nil
	default:
		switch msg.Text {
		case "t":
			// Toggle between tasks and types views without leaving
			// the overlay. Cursor resets to 0 because the lists are
			// disjoint and keeping an arbitrary index would be
			// surprising. The status line clears so a stale "canceled
			// X" message doesn't linger after switching views.
			if p.mode == subagentsPickerModeTasks {
				p.mode = subagentsPickerModeTypes
			} else {
				p.mode = subagentsPickerModeTasks
			}
			p.cursor = 0
			p.status = ""
			return m, nil
		case "s":
			// Stop is tasks-mode only.
			if p.mode != subagentsPickerModeTasks || len(p.tasks) == 0 {
				return m, nil
			}
			chosen := p.tasks[p.cursor]
			if chosen.Status != subagents.TaskRunning {
				p.status = fmt.Sprintf("task %s is not running (status: %s)", chosen.ID[:8], chosen.Status)
				return m, nil
			}
			if !m.subagentTasks.Cancel(chosen.ID) {
				p.status = fmt.Sprintf("could not cancel %s (no live cancel hook)", chosen.ID[:8])
				return m, nil
			}
			p.status = fmt.Sprintf("canceled %s — give it a moment to clean up", chosen.ID[:8])
			p.tasks = m.subagentTasks.List()
			if p.cursor >= len(p.tasks) {
				p.cursor = max(0, len(p.tasks)-1)
			}
			return m, nil
		case "r":
			// Refresh: re-snapshot the underlying data. Tasks change
			// over time as background runs complete; types only
			// change if the user reloads via /reload (rare), but we
			// refresh both anyway for consistency.
			p.tasks = m.subagentTasks.List()
			if m.subagentTool != nil {
				p.types = m.subagentTool.AgentConfigs()
			}
			p.status = "refreshed"
			// Clamp against the POST-refresh row count, not the stale
			// rowCount snapshotted at the top of the handler before this
			// re-snapshot — if either list shrank, the stale value would
			// leave the cursor past the end and the next Enter/s would
			// index out of range. (Matches the fresh-length clamp the
			// `s` case already uses.)
			if rc := p.rowCount(); p.cursor >= rc {
				p.cursor = max(0, rc-1)
			}
			return m, nil
		case "i":
			// Inject: feed the selected finished task's result to the model
			// as a new turn — the user-driven re-entry that complements the
			// Agent tool's notify_on_done. Tasks-mode only.
			if p.mode != subagentsPickerModeTasks || len(p.tasks) == 0 {
				return m, nil
			}
			next, cmd, status := m.injectSubagentResult(p.tasks[p.cursor])
			if status != "" {
				p.status = status
				return m, nil
			}
			// Close the picker on the model we RETURN. injectSubagentResult
			// has a value receiver, so `next` was copied from m before any
			// assignment here — clearing the fields on the local `m` after
			// the copy was a dead store that left the picker open, swallowing
			// keys while the wake turn streamed underneath it.
			nm := next.(Model)
			nm.subagentsPickerOpen = false
			nm.subagentsPicker = nil
			return nm, cmd
		}
	}
	return m, nil
}

// rowCount returns the number of rows visible in the current picker
// mode. Used by the cursor bounds check so Down can't walk past the
// end of either list.
func (p *subagentsPickerState) rowCount() int {
	if p.mode == subagentsPickerModeTypes {
		return len(p.types)
	}
	return len(p.tasks)
}

// renderSubagentsPicker draws the overlay body. Each row is one task
// with a compact representation: ID + agent type as the label, status
// + relative age + tool count + truncated prompt as the description.
// Cursor highlighting is handled by renderMenuItem (consistent with
// every other inline picker).
func renderSubagentsPicker(state *subagentsPickerState, width int, hits ...*pickerHits) string {
	var h *pickerHits
	if len(hits) > 0 {
		h = hits[0]
	}
	if state.mode == subagentsPickerModeTypes {
		return renderSubagentsPickerTypes(state, width, h)
	}
	return renderSubagentsPickerTasks(state, width, h)
}

func renderSubagentsPickerTasks(state *subagentsPickerState, _ int, h *pickerHits) string {
	header := renderMenuHeader(
		"Subagents — tasks",
		"Enter views · i injects result · s stops · r refreshes · t shows types · Esc closes",
	)
	if len(state.tasks) == 0 {
		return header + "\n" +
			styleEmpty.Render("  no subagent tasks this session — call the Agent tool from your next prompt to spawn one") +
			"\n" + renderSubagentsPickerFooter()
	}

	// Compute the widest label so columns align cleanly.
	const idLen = 8
	maxAgentName := 6
	for _, t := range state.tasks {
		if l := len(t.AgentType); l > maxAgentName {
			maxAgentName = l
		}
	}
	if maxAgentName > 20 {
		maxAgentName = 20
	}
	labelWidth := idLen + 3 + maxAgentName // "abcd1234 · Explore"

	body := header + "\n"
	for i, t := range state.tasks {
		row := strings.Count(body, "\n")
		h.row(row, i)
		label := fmt.Sprintf("%s · %s",
			t.ID[:idLen],
			truncateForRender(t.AgentType, maxAgentName))
		mode := "fg"
		if t.Background {
			mode = "bg"
		}
		modelChip := ""
		if t.Model != "" {
			modelChip = " · " + truncateForRender(t.Model, 24)
		}
		desc := fmt.Sprintf("%s [%s] · %s · %d acts%s · %s",
			t.Status.String(),
			mode,
			relativeAge(t.Started),
			t.ToolCalls,
			modelChip,
			truncateForRender(t.Prompt, 45))
		// For a running task, append what it's doing right now — the
		// latest activity tick (the same source the dock shows) — so the
		// picker is a snapshot of *state*, not just status. Running-only:
		// a terminal task's last tick is noise next to its status. Static
		// like the rest of the row; `r` re-snapshots it.
		if t.Status == subagents.TaskRunning {
			desc += " · " + truncateForRender(latestActivity(t), 40)
		}
		body += renderMenuItem(menuItemOpts{
			Label:      label,
			LabelWidth: labelWidth,
			Desc:       desc,
			Cursor:     i == state.cursor,
		}) + "\n"
	}
	if state.status != "" {
		body += "\n" + styleMeta.Render("  "+state.status)
	}
	body += "\n" + renderSubagentsPickerFooter()
	return body
}

// renderSubagentsPickerTypes shows the catalog of available agent
// definitions as an interactive overlay so /subagents types no
// longer needs to dump multi-line content into scrollback. Each
// row is one definition with its source tag; the description hangs
// in the right column without wrapping (it's bounded by the
// menuItemOpts row layout, so over-long descriptions truncate
// rather than wrap — the full body lives in the agent's .md file).
func renderSubagentsPickerTypes(state *subagentsPickerState, _ int, h *pickerHits) string {
	header := renderMenuHeader(
		"Subagents — agent types",
		"r refreshes · t shows tasks · Esc closes",
	)
	if len(state.types) == 0 {
		return header + "\n" +
			styleEmpty.Render("  no agent types registered") +
			"\n" + renderSubagentsPickerFooter()
	}
	// Compute label width from the longest agent name + source tag.
	maxName := 6
	for _, c := range state.types {
		if l := len(c.Name); l > maxName {
			maxName = l
		}
	}
	if maxName > 24 {
		maxName = 24
	}
	const sourceW = 9 // "[builtin]", "[project]" both 9 chars
	labelWidth := maxName + 1 + sourceW

	body := header + "\n"
	for i, c := range state.types {
		row := strings.Count(body, "\n")
		h.row(row, i)
		label := fmt.Sprintf("%-*s %-*s",
			maxName, truncateForRender(c.Name, maxName),
			sourceW, "["+c.Source+"]")
		// Description truncates to a single line — overflow lives in
		// the agent's source .md file (cfg.SourcePath shown in
		// status when applicable).
		body += renderMenuItem(menuItemOpts{
			Label:      label,
			LabelWidth: labelWidth,
			Desc:       truncateForRender(c.Description, 80),
			Cursor:     i == state.cursor,
		}) + "\n"
	}
	if state.status != "" {
		body += "\n" + styleMeta.Render("  "+state.status)
	}
	body += "\n" + renderSubagentsPickerFooter()
	return body
}

// renderSubagentsPickerFooter is the shared bottom-of-overlay row
// with pager keys. Used by both task and types views so users always
// see the Shift+F live-tail key when planning a pager session.
func renderSubagentsPickerFooter() string {
	return styleSubagentMeta.Render(
		"  Pager keys after Enter:  q quit · ") +
		styleSubagentLabel.Render("Shift+F live-tail") +
		styleSubagentMeta.Render(" · r refresh · / search · arrows scroll")
}
