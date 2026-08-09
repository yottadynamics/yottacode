package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// cmdGitReviewPR handles `/git-review-pr` — the procedural PR-review
// flow that lets the agent self-review a pull request against a
// structured rubric (correctness, scope, tests, style, security,
// performance) and surface failing CI checks at the top.
//
// Companion to /git-create-pr in the /git-* family; together they
// give "create + review" coverage from the same surface. Shares the
// pr_review_context composite tool (Layer 1) and the typed
// github.Interface (Layer 4) — both also used by /git-create-pr —
// so the reliability work (gh availability, not-found detection,
// failing-check classification) lives in Go.
//
// Optional ref arg: PR number (`/git-review-pr 17`) or branch
// (`/git-review-pr feature/x`). With no arg, the composite tool
// uses the current branch's PR if one exists. No-PR cases surface
// a deterministic "no PR found" message and stop.
func cmdGitReviewPR(m Model, args []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[git-review-pr] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	ref := ""
	if len(args) > 0 {
		ref = strings.TrimSpace(args[0])
	}
	display := "/git-review-pr"
	if ref != "" {
		display += " " + ref
	}
	prompt := gitReviewPRDirective(ref)
	out, cmd := m.startTurnWithDisplay(prompt, display)
	return out.(Model), cmd
}

// gitReviewPRDirective is the prompt /git-review-pr hands the
// agent. The canonical text lives in internal/promptmacros (shared
// with internal/acp) — this stays as a thin same-named delegator so
// the existing test suite keeps working unmodified.
func gitReviewPRDirective(ref string) string {
	return promptmacros.GitReviewPRDirective(ref)
}
