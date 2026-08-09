package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// cmdGitUpdatePR handles `/git-update-pr` — the fifth and final
// member of the `/git-*` family (commit / create-pr / review-pr /
// push / update-pr). Refreshes an existing PR's title and body
// after follow-up commits have made the original description
// stale.
//
// Reuses the existing pr_review_context for the gathering
// step (it already fetches PR metadata + commit list + diff for
// the same ref). The new piece is pr_update, which validates
// the refreshed title and dials Interface.UpdatePR.
//
// Optional ref arg: PR number (`/git-update-pr 17`) or branch
// (`/git-update-pr feature/x`). Empty defaults to the current
// branch's PR — matching the rest of the family's ref-handling.
func cmdGitUpdatePR(m Model, args []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render(SysMsg(SysWarning, "git-update-pr", "turn already running", "wait or Esc to cancel")))
		return m, nil
	}
	ref := ""
	if len(args) > 0 {
		ref = strings.TrimSpace(args[0])
	}
	display := "/git-update-pr"
	if ref != "" {
		display += " " + ref
	}
	prompt := gitUpdatePRDirective(ref)
	out, cmd := m.startTurnWithDisplay(prompt, display)
	return out.(Model), cmd
}

// gitUpdatePRDirective is the prompt /git-update-pr hands the
// agent. The canonical text lives in internal/promptmacros (shared
// with internal/acp) — this stays as a thin same-named delegator so
// the existing test suite keeps working unmodified.
func gitUpdatePRDirective(ref string) string {
	return promptmacros.GitUpdatePRDirective(ref)
}
