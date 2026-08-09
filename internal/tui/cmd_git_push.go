package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// cmdGitPush handles `/git-push` — pushes the current branch to
// origin and surfaces the updated PR URL if one exists.
//
// Fourth and last command in the `/git-*` family alongside
// /git-commit, /git-create-pr, and /git-review-pr. Together they
// give "commit → push → create-pr → review-pr" coverage from one
// palette family.
//
// Smaller than the other family members by design: `git push`
// itself is well-understood, so the composite tool only owns
// upstream detection (first-push -u flag), detached-HEAD early
// exit, and the no-force-push safety. The slash directive is
// correspondingly short — the model's only job is to call the
// tool and route the result.
func cmdGitPush(m Model, _ []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[git-push] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	prompt := gitPushDirective()
	out, cmd := m.startTurnWithDisplay(prompt, "/git-push")
	return out.(Model), cmd
}

// gitPushDirective is the prompt /git-push hands the agent. The
// canonical text lives in internal/promptmacros (shared with
// internal/acp) — this stays as a thin same-named delegator so the
// existing test suite keeps working unmodified.
func gitPushDirective() string {
	return promptmacros.GitPushDirective()
}
