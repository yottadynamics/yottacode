package promptmacros

import "fmt"

// GitReviewPRDirective is the prompt /git-review-pr hands the
// agent. Same shape as the commit / create-pr directives: short,
// names the one composite tool the model invokes, surfaces the
// typed flags the model branches on, and pins the review's
// structure so output is consistent.
//
// The ref argument splices into the tool call (when present) for
// the same reason as create-pr: avoids threading $1 through prose.
func GitReviewPRDirective(ref string) string {
	refArgLine := `Step 1 — call pr_review_context with no arguments. It will
use the current branch's PR.`
	if ref != "" {
		refArgLine = fmt.Sprintf("Step 1 — call pr_review_context with ref=%q.", ref)
	}

	return `Review a pull request and emit a structured assessment.

` + refArgLine + ` It returns a typed snapshot
under section headers (## state, ## pr, ## checks.summary,
## checks, ## diff).

Step 2 — read the ## state header and branch BEFORE composing:
- github_unavailable=true → surface "[git-review-pr] gh CLI unavailable
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
  <stylistic / cosmetic feedback (naming, magic numbers without
   constants, comment density, small duplications, TODO comments
   that should be tracked elsewhere). A diff of substantial size
   will almost always have a Nit — emit at least one when the diff
   spans more than ~3 hunks. "(none)" is correct only on
   small focused fixes where the convention is unambiguous.>

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
