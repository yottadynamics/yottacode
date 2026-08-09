package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// cmdGitCreateIssue handles `/git-create-issue` — the procedural
// git-create-issue flow that creates a GitHub issue in the current repo.
//
// Slug shape mirrors `/git-create-pr`: `git-` prefix instead of `git:`
// namespace because built-in slugs are flat and the prefix gives
// palette discoverability — typing `/git` filters to every
// git-related built-in.
//
// Same shape as cmdGitCreatePR: the slash handler is a thin wrapper
// that composes a narrow directive naming the two composite tools
// (issue_context + issue_create) and submits via startTurn. The
// reliability work — template detection, gh-unavailable fall-through,
// title validation — lives in Go inside those tools, not in the prompt.
//
// One optional arg: `/git-create-issue <title>` pins the issue title.
// With no arg, the workflow starts with context gathering and lets the
// user compose the issue interactively.
func cmdGitCreateIssue(m Model, args []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[git-create-issue] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	title := issueTitleFromArgs(args)
	display := "/git-create-issue"
	if title != "" {
		display += " " + title
	}
	prompt := gitCreateIssueDirective(title)
	out, cmd := m.startTurnWithDisplay(prompt, display)
	return out.(Model), cmd
}

// issueTitleFromArgs rejoins the whitespace-tokenized slash args into
// the issue title. The canonical logic lives in internal/promptmacros
// (shared with internal/acp) — this stays as a thin same-named
// delegator so the existing test suite keeps working unmodified.
func issueTitleFromArgs(args []string) string {
	return promptmacros.IssueTitleFromArgs(args)
}

// gitCreateIssueDirective is the prompt the /git-create-issue handler
// hands the agent. The canonical text lives in internal/promptmacros
// (shared with internal/acp) — this stays as a thin same-named
// delegator so the existing test suite keeps working unmodified.
func gitCreateIssueDirective(title string) string {
	return promptmacros.GitCreateIssueDirective(title)
}
