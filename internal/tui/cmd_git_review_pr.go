package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cmdGitReviewPR handles `/git-review-pr` — the procedural PR-review
// flow that lets the agent self-review a pull request against a
// structured rubric (correctness, scope, tests, style, security,
// performance) and surface failing CI checks at the top.
//
// Companion to /git-create-pr in the /git-* family; together they
// give "create + review" coverage from the same surface. Shares the
// gh_pr_review_context composite tool (Layer 1) and the typed
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
// agent. Same shape as the commit / create-pr directives: short,
// names the one composite tool the model invokes, surfaces the
// typed flags the model branches on, and pins the review's
// structure so output is consistent.
//
// The ref argument splices into the tool call (when present) for
// the same reason as create-pr: avoids threading $1 through prose.
func gitReviewPRDirective(ref string) string {
	refArgLine := `Step 1 — call gh_pr_review_context with no arguments. It will
use the current branch's PR.`
	if ref != "" {
		refArgLine = "Step 1 — call gh_pr_review_context with ref=\"" + ref + "\"."
	}

	return `Review a pull request and emit a structured assessment.

` + refArgLine + ` It returns a typed snapshot
under section headers (## state, ## pr, ## checks.summary,
## checks, ## diff).

Step 2 — read the ## state header and branch BEFORE composing:
- gh_unavailable=true → surface "[git-review-pr] gh CLI unavailable
  or unauthenticated — run gh auth login" and STOP.
- not_found=true → surface "[git-review-pr] no PR found for
  <ref>" and STOP. Do NOT attempt a local-diff review as a
  fallback; the user can use /git-create-pr for that surface.
- failing_checks=<names> → these will be the first thing the user
  reads in the review (step 4 below). Quote them verbatim.

Step 3 — read ## pr, ## checks, and ## diff. The diff may be
truncated; if so, the snapshot's footer hints how to fetch the
full diff. Don't run that fetch unless the truncated content
clearly doesn't cover the review surface you need.

Step 4 — emit the review in this exact structure:

  ## Review of PR #<number>: <title>
  <one-line state summary: state, draft, mergeable, author, base ← head>

  ### Failing checks
  <if failing_checks is non-empty, list each verbatim with one line
   of inferred cause from the check name. Otherwise: "All checks
   passing." — single line, nothing more.>

  ### Blockers
  <issues that must be fixed before merge. Each entry: a one-line
   summary, then a file:line ref, then a 1-2 sentence recommendation.
   "(none)" if there are no blockers.>

  ### Suggestions
  <improvements that would strengthen the PR but aren't merge
   blockers. Same format as blockers. Marginal-value bar: only
   emit a suggestion if acting on it would NOTICEABLY strengthen
   the PR; if the suggestion is in the realm of "could be slightly
   better" or "consider adding…", prefer "(none)" over filler.
   "(none)" is the correct answer when the diff is clean.>

  ### Nits
  <stylistic / cosmetic feedback. Same format. "(none)" if empty.>

  ### Notes for the author
  <optional: design context, alternatives considered, follow-ups
   that should be tracked separately. Omit the section entirely
   when empty rather than writing "(none)".>

Review rubric (apply to each finding):
- Correctness: does the diff match the PR description's stated
  intent? Are edge cases handled? Off-by-one, nil/empty, error paths?
- Scope: does the diff stay focused, or does it bundle unrelated
  changes that would be better split?
- Tests: are new behaviors covered? Are bug fixes accompanied by
  regression tests? Is the test name aligned with what's verified?
- Style: does the diff match the repo's conventions visible
  elsewhere in the codebase (naming, error handling, comment
  density)?
- Security: any unvalidated input, secret leakage, path traversal,
  injection, or dependency risk?
- Performance: any obvious quadratic loops, unnecessary allocations,
  or N+1 patterns in hot paths?

Hard constraints:
- Do NOT post the review back to GitHub. Output to scrollback only.
  Posting is a separate trust gate (deferred — the v0.5.0 spec
  calls it /git-review-pr --post and we haven't shipped it).
- Do NOT propose code edits via write_file / edit_file / apply_diff.
  This is a review, not a fix-up pass; the author owns the changes.
- Do NOT fabricate file:line refs. Cite only locations the ## diff
  section actually shows. If the diff is truncated and a finding
  needs a location it doesn't cover, name the file without a line
  number and note the truncation.
- Do NOT speculate about unrelated parts of the codebase. The
  review covers the diff, not the repo.`
}
