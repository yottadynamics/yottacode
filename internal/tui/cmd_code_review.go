package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// cmdCodeReview handles `/code-review [low|medium|high]` — the
// multi-agent diff-review flow modeled on Anthropic Claude Code's
// /code-review. The orchestrator reads the local diff via the
// code_review_context composite tool, crafts review "angles" from it,
// fans them out to read-only background subagents, dedups their
// candidate findings, verifies them (effort-gated), and emits one
// structured report.
//
// Unlike /git-review-pr (which reviews an existing PR through the
// github.Interface), this reviews the LOCAL diff and needs no GitHub
// access. Its phase-2 fan-out is data-dependent (one verifier per
// deduped finding, a count unknown until runtime), so the
// orchestration lives in the directive rather than in Go — the main
// agent runs that loop itself.
func cmdCodeReview(m Model, args []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[code-review] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	effort, notice := parseEffort(args)
	if notice != "" {
		m.appendLine(styleAuto.Render(notice))
	}
	display := "/code-review " + effort
	prompt := codeReviewDirective(effort)
	out, cmd := m.startTurnWithDisplay(prompt, display)
	return out.(Model), cmd
}

// parseEffort folds the optional first arg to one of low|medium|high,
// defaulting to medium. The canonical logic lives in
// internal/promptmacros (shared with internal/acp) — this stays as a
// thin same-named delegator so the existing test suite keeps working
// unmodified.
func parseEffort(args []string) (effort, notice string) {
	return promptmacros.ParseEffort(args)
}

// codeReviewDirective is the prompt /code-review hands the
// orchestrator. The canonical text lives in internal/promptmacros
// (shared with internal/acp) — this stays as a thin same-named
// delegator so the existing test suite keeps working unmodified.
func codeReviewDirective(effort string) string {
	return promptmacros.CodeReviewDirective(effort)
}
