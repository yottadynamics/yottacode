package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// cmdInit handles `/init` — drafts a starter YOTTACODE.md by handing
// the agent a directive prompt and letting the normal turn loop run
// with full tool access. The agent uses list_dir / glob / grep /
// read_file to learn the repo, then calls write_file (approval-gated)
// to land .yottacode/YOTTACODE.md.
//
// The slash command is a thin wrapper: it composes the directive
// (with an "already exists — refresh, don't overwrite blindly" hint
// when the file is present) and submits via the same path a typed
// user message would take. That way the agent loop, approval modal,
// retrieval orchestrator, and session save all behave normally.
func cmdInit(m Model, _ []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[init] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	prompt := buildInitPrompt(m.cwd)
	out, cmd := m.startTurn(prompt)
	return out.(Model), cmd
}

// buildInitPrompt composes the directive sent to the agent. The
// canonical text lives in internal/promptmacros (shared with
// internal/acp) — this stays as a thin same-named delegator so the
// existing test suite keeps working unmodified.
func buildInitPrompt(cwd string) string {
	return promptmacros.BuildInitPrompt(cwd)
}
