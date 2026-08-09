package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// cmdGitCommit handles `/git-commit` — the procedural git-commit
// flow that replaces the legacy markdown directive
// `/git:commit-message`.
//
// The slug is prefixed `git-` rather than namespaced `git:`. Built-in
// slugs are flat (no `:` separator — that's reserved for custom-command
// path derivation), and the `git-` prefix gives palette discoverability:
// typing `/git` filters to every git-related built-in. Sibling commands
// in this family: `/git-create-pr`.
//
// Where the legacy directive injected 118 lines of prose and asked the
// model to parse bash output, decide early-exit conditions, infer
// style, and obey "hard prohibitions" in English, this handler hands
// the model two composite tools (git_commit_context + git_commit_apply)
// and a ~20-line prompt naming them. The reliability work moves into
// Go: empty staging early-exits at the apply tool deterministically,
// subject validation rejects oversize / trailing-period / multi-line
// messages *before* invoking git, hook failures return as typed
// envelope fields the model surfaces verbatim, and there is no path
// the model can take that ends in `git commit --amend` without the
// user explicitly asking for it.
//
// The slash command itself is a thin wrapper — same shape as cmdInit:
// build the directive, submit via startTurn, let the normal agent loop
// + approval modal handle the rest. The composite tools are where the
// foundation work landed.
func cmdGitCommit(m Model, _ []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render(SysMsg(SysWarning, "git-commit", "turn already running", "wait or Esc to cancel")))
		return m, nil
	}
	prompt := gitCommitDirective()
	out, cmd := m.startTurnWithDisplay(prompt, "/git-commit")
	return out.(Model), cmd
}

// gitCommitDirective is the prompt body the /git-commit handler
// hands the agent. The canonical text lives in internal/promptmacros
// (shared with internal/acp) — this stays as a thin same-named
// delegator so the existing test suite keeps working unmodified.
func gitCommitDirective() string {
	return promptmacros.GitCommitDirective()
}
