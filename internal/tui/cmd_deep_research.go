package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// cmdDeepResearch handles `/deep-research [--breadth 2-6] <question>`.
//
// In the TUI it starts the deep_research tool itself and spends no model turn:
// the workflow is deterministic Go, so a model hop at either end only adds
// cost and room for uncited commentary (see deep_research_delivery.go). The
// finished summary is printed by deliverDeepResearch.
//
// It falls back to the shared prompt macro (the path ACP uses, where the model
// calls the tool) when a direct start isn't possible: in plan mode the tool
// must stay blocked, and a session that can't host a detached run needs the
// blocking tool call.
func cmdDeepResearch(m Model, args []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[deep-research] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	if m.summarizing {
		// Summarization owns the history and the session save until it lands;
		// the direct start would append to both (startSubagentWakeTurn holds
		// its own completions for the same reason).
		m.appendLine(styleError.Render("[deep-research] context summarization is running — try again when it finishes"))
		return m, nil
	}
	breadth, query, err := promptmacros.ParseDeepResearchArgs(args)
	if err != nil {
		m.appendLine(styleError.Render("[deep-research] " + err.Error()))
		return m, nil
	}
	display := "/deep-research " + strings.Join(args, " ")
	if m.startDeepResearchDirect(display, breadth, query) {
		return m, nil
	}
	prompt, err := promptmacros.MustGet("deep-research").Build("", args)
	if err != nil {
		m.appendLine(styleError.Render("[deep-research] " + err.Error()))
		return m, nil
	}
	out, cmd := m.startTurnWithDisplay(prompt, display)
	return out.(Model), cmd
}
