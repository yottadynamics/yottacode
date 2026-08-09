package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// cmdGitImplementIssue handles `/git-implement-issue <n>` — the
// procedural "GitHub issue → planned implementation → draft PR"
// flow. Spec: yottacode-roadmap/git-fix-issue.md (the doc was
// written under the older name; this command implements the same
// flow under /git-implement-issue).
//
// Companion to the rest of the /git-* family. Reuses the shipped
// composite tools (issue_read, git_stage_files,
// git_commit_apply, git_push, pr_create) rather than reinventing
// the mechanics; the directive's job is to sequence them and pin
// the safety rails (plan-mode checkpoint, no auto-iterate on test
// failures, draft PR is the merge gate).
//
// Required arg: issue number. With no arg the command surfaces a
// usage hint and exits without starting a turn — a slash command
// that turned "no arg" into a free-form prompt would burn tokens
// on a clarification turn we can deterministically prevent.
func cmdGitImplementIssue(m Model, args []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[git-implement-issue] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	if len(args) == 0 {
		m.appendLine(styleError.Render("[git-implement-issue] usage: /git-implement-issue <issue-number>"))
		return m, nil
	}
	raw := strings.TrimSpace(args[0])
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		m.appendLine(styleError.Render("[git-implement-issue] issue number must be a positive integer; got " + raw))
		return m, nil
	}
	display := "/git-implement-issue " + strconv.Itoa(n)
	prompt := gitImplementIssueDirective(n)
	out, cmd := m.startTurnWithDisplay(prompt, display)
	return out.(Model), cmd
}

// gitImplementIssueDirective is the prompt /git-implement-issue
// hands the agent. The canonical text lives in internal/promptmacros
// (shared with internal/acp) — this stays as a thin same-named
// delegator so the existing test suite keeps working unmodified.
func gitImplementIssueDirective(n int) string {
	return promptmacros.GitImplementIssueDirective(n)
}
