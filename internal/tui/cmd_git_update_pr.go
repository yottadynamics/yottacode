package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cmdGitUpdatePR handles `/git-update-pr` — the fifth and final
// member of the `/git-*` family (commit / create-pr / review-pr /
// push / update-pr). Refreshes an existing PR's title and body
// after follow-up commits have made the original description
// stale.
//
// Reuses the existing gh_pr_review_context for the gathering
// step (it already fetches PR metadata + commit list + diff for
// the same ref). The new piece is gh_pr_update, which validates
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
// agent. Shares the same shape as the other /git-* directives:
// short, names the composite tools, surfaces the typed-flag
// branches.
//
// The keep-title-if-still-accurate guidance is the load-bearing
// model brief — without it the model rewrites titles unnecessarily
// every invocation, which churns the PR's GitHub history with
// cosmetic noise.
func gitUpdatePRDirective(ref string) string {
	refArgLine := `Step 1 — call gh_pr_review_context with no arguments. It will
use the current branch's PR.`
	if ref != "" {
		refArgLine = "Step 1 — call gh_pr_review_context with ref=\"" + ref + "\"."
	}

	return `Refresh an existing pull request's title and body to match the
current commit list.

` + refArgLine + ` It returns a typed snapshot
under section headers (## state, ## pr, ## checks.summary,
## checks, ## diff). The ## pr block carries the EXISTING title
and body — read them before composing the refreshed versions so
you know what's there now.

Step 2 — branch on ## state BEFORE composing anything:
- gh_unavailable=true → surface "[git-update-pr] gh CLI
  unavailable or unauthenticated — run gh auth login" and STOP.
- not_found=true → surface "[git-update-pr] no PR found for
  <ref>. Run /git-create-pr to open one instead." and STOP. Do
  NOT auto-route to create.

Step 3 — compose the refreshed title and body:
- TITLE: keep the existing title verbatim when the new commits
  are still consistent with it. Only rewrite the title when
  scope materially changed (e.g., started as "fix X" but the
  commit list now also adds Y). Title churn for cosmetic
  reasons (rewording, capitalization fixes) is NOT a reason to
  edit — it just adds noise to the PR's GitHub history.
- BODY: regenerate from the full ## commits.log. If the
  existing body had a recognizable structure (Summary / Changes
  / Test plan / Notes), preserve that section order and update
  each section's contents. Otherwise use the standard skeleton:

  ## Summary
  <1-3 bullets, the "why">

  ## Changes
  <1 line per logical change drawn from ## commits.log>

  ## Test plan
  - [ ] <concrete check the reviewer should run>

  ## Notes for reviewer
  <optional: design alternatives, deferred follow-ups>

  Cap the body at ~80 lines. Do NOT paste the diff verbatim —
  describe it.

Step 4 — call gh_pr_update with the refreshed title and body.
The approval modal fires showing both inline; user approves or
denies.

Step 5 — surface the result envelope verbatim:
- "updated=true url=... number=..." → emit a one-line
  "PR updated: <url>" plus a brief note on what changed (title
  rewritten / body refreshed / both).
- "updated=false reason=validation error=..." → surface the
  error, then either (a) compose a new title/body that satisfies
  the rule and call gh_pr_update once more, or (b) stop. Pick
  (b) after one retry.
- "updated=false reason=not_found" → surface and stop. Same
  routing rule as Step 2.
- "updated=false reason=gh_error" → surface the gh output
  verbatim and STOP. Do NOT auto-retry, auto-edit labels /
  reviewers / base / draft, or auto-merge.

Hard prohibitions:
- Do NOT touch fields other than title and body. Labels,
  reviewers, base branch, draft state, milestone, and projects
  are all off-limits for this command.
- Do NOT close, reopen, or merge the PR.
- Do NOT rewrite the title for cosmetic reasons. Title rewrite
  is for material scope changes only.
- Do NOT paste the diff verbatim into the body — describe it.`
}
