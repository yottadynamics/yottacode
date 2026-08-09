package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// cmdGitCreatePR handles `/git-create-pr` — the procedural
// git-create-pr flow that replaces the legacy markdown directive
// `/git:create-pr`.
//
// Slug shape mirrors `/git-commit`: `git-` prefix instead of `git:`
// namespace because built-in slugs are flat and the prefix gives
// palette discoverability — typing `/git` filters to every
// git-related built-in.
//
// Same shape as cmdGitCommit: the slash handler is a thin wrapper
// that composes a narrow directive naming the two composite tools
// (pr_context + pr_create) and submits via startTurn. The
// reliability work — base resolution, ahead-count gating,
// gh-unavailable fall-through, title validation, push state
// detection — lives in Go inside those tools, not in the prompt.
//
// One optional arg: `/git-create-pr <base>` pins the base branch.
// With no arg, the workflow's deterministic base resolver picks
// origin/HEAD, then main/master/develop in order.
func cmdGitCreatePR(m Model, args []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[git-create-pr] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	base := ""
	if len(args) > 0 {
		base = strings.TrimSpace(args[0])
	}
	display := "/git-create-pr"
	if base != "" {
		display += " " + base
	}
	prompt := gitCreatePRDirective(base)
	out, cmd := m.startTurnWithDisplay(prompt, display)
	return out.(Model), cmd
}

// gitCreatePRDirective is the prompt the /git-create-pr handler
// hands the agent. The canonical text lives in internal/promptmacros
// (shared with internal/acp) — this stays as a thin same-named
// delegator so the existing test suite keeps working unmodified.
func gitCreatePRDirective(base string) string {
	return promptmacros.GitCreatePRDirective(base)
}
