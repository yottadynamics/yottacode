package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cmdGitImplementIssue handles `/git-implement-issue <n>` — the
// procedural "GitHub issue → planned implementation → draft PR"
// flow. Spec: yottacode-roadmap/git-fix-issue.md (the doc was
// written under the older name; this command implements the same
// flow under /git-implement-issue).
//
// Companion to the rest of the /git-* family. Reuses the shipped
// composite tools (gh_issue_read, git_stage_files,
// git_commit_apply, git_push, gh_pr_create) rather than reinventing
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
// hands the agent. Same shape as the other /git-* directives:
// names the tools, pins the sequencing, surfaces the typed flags
// the model branches on, and lists the hard prohibitions inline so
// they can't be argued away.
//
// The number splices in via Sprintf rather than free-form
// substitution so the model can't misread the issue reference.
func gitImplementIssueDirective(n int) string {
	num := strconv.Itoa(n)
	return `Implement the changes that resolve GitHub issue #` + num + ` and
open a draft PR. Flow is deterministic — follow each step in order.

Step 1 — call gh_issue_read with number=` + num + `. It returns a
typed snapshot under section headers (## state, ## issue,
## comments). Read ## state first and branch:
- gh_unavailable=true → surface "[git-implement-issue] GitHub
  adapter unconfigured — run yottacode setup github" and STOP.
- not_found=true → surface "[git-implement-issue] issue #` + num + `
  not found" and STOP. Do NOT guess at the user's intent.
- state=CLOSED → surface "Issue #` + num + ` is closed. Confirm
  whether to proceed (a regression of a previously-closed bug is
  the common reason)." and STOP for user confirmation. The user
  re-runs the command after confirming.
- All clean → proceed to Step 2.

Step 2 — research the codebase. Use read_file, grep, glob,
list_dir to find the relevant files and understand the existing
code. Read the issue body and comments in ## issue / ## comments
to frame the change. NO write_file / edit_file / apply_diff /
mkdir at this stage; this is the research phase.

Step 2.5 (bug-shaped issues only) — if the issue is labeled
"bug" (visible in ## issue's labels=… line), or unlabeled with a
body that describes a defect (words like "error", "broken",
"doesn't work", "fails", "crashes"), write a failing test that
reproduces the buggy behavior BEFORE implementing the fix. The
failing test becomes the regression test that ships with the fix
— this matches the project's tests-per-capability rule. Use
write_file or edit_file to add the test. Confirm it fails by
calling run_tests; the failure output is the reproduction
evidence. If the test passes (i.e. the bug isn't reproducible
from the issue body alone), STOP and surface "[git-implement-issue]
could not reproduce the bug from the issue body — ask the reporter
for clearer steps before proceeding". Skip this step for
feature / enhancement / refactor / docs issues — there's nothing
to reproduce.

Step 3 — draft an implementation plan. CALL exit_plan_mode with
a markdown plan covering: which files to change, what change to
make in each, and (for bugs) which test verifies the fix. This
fires the approval modal — the user reviews the plan and either
approves (you proceed to Step 4) or rejects (you stop and adjust).
Do NOT skip this step. The plan-mode checkpoint is the
load-bearing safety gate of this command. If exit_plan_mode is not
available in this session, instead surface the plan as a single
scrollback message and STOP for user confirmation.

Step 4 — create a fresh feature branch via the unified git tool:
git({"args":["checkout","-b","fix/issue-` + num + `-<short-slug>"]}).
<short-slug> is derived from the issue title: lowercase, only
[a-z0-9-], words joined by dashes, capped at ~30 characters. If
the branch name collides with an existing branch, append "-2"
(then "-3", etc.) before retrying.

Step 5 — implement the changes per the approved plan. Use
write_file / edit_file / apply_diff / mkdir as needed. Stay
within the scope of the plan; if the implementation surfaces a
need to expand scope, STOP and surface the discovery rather than
silently expanding.

Step 6 — call run_tests. If tests fail:
- Surface the failure verbatim.
- Do NOT auto-fix. Do NOT iterate.
- Do NOT stage, commit, push, or open a PR.
- STOP and let the user decide.
If tests pass, proceed to Step 7.

Step 7 — stage and commit. Call git_stage_files with the changed
paths, then git_commit_context to generate the message context,
then git_commit_apply to compose and run the commit. The commit
subject should be a short imperative verb summary of the change;
the body should explain the why; include "Refs #` + num + `" at the
end of the subject when style permits, otherwise in the body. Do
NOT include "Closes #` + num + `" in the commit message — that goes
in the PR body so the issue closes when the PR merges (the human
review gate), not when the commit lands.

Step 8 — call git_push. This sets the upstream and pushes the new
branch to origin. The push tool will surface failures (detached
HEAD, push-rejected); do NOT force-push and do NOT amend on
failure. STOP and let the user diagnose.

Step 9 — open the draft PR via gh_pr_create. base="main" (unless
the repo's default branch is different — check the issue body or
the prior /git-create-pr conventions). draft=true. Body must
include:
  ## Summary
  <1-3 lines on what the PR does and why>

  ## Changes
  <bulleted list of the major changes, organized by file or area>

  ## Test plan
  <bulleted checklist of what the user should verify before
   marking ready-for-review>

  Closes #` + num + `

The "Closes #` + num + `" line is load-bearing — GitHub's keyword
detection closes the issue automatically when the PR merges.
Don't substitute "Fixes" / "Resolves" / "Refs" here; "Closes" is
the most explicit and most widely understood.

After gh_pr_create returns, surface the PR URL to the user and
STOP. The draft PR is the human-review gate — the user reviews
the diff on GitHub, marks ready-for-review when satisfied, and
merges manually.

Hard constraints (each pinned by the spec — do not relax):
- Do NOT close the issue manually. The "Closes #` + num + `" line in
  the PR body does this automatically when the PR merges.
- Do NOT auto-iterate on test failures. Surface and STOP.
- Do NOT force-push (--force, --force-with-lease) at any point.
- Do NOT amend an existing commit. Always create new commits.
- Do NOT merge the PR. It opens as a draft; the user marks it
  ready-for-review and merges manually on GitHub.
- Do NOT assign reviewers, labels, milestones, or projects to the
  PR. Same scope-pinning as /git-update-pr.
- Do NOT fabricate file:line refs anywhere — cite only locations
  the diff actually shows.
- Do NOT post a comment back on the issue via gh_pr_add_comment
  at the end of this flow. The "Closes #` + num + `" link is the
  cross-reference; an explicit comment is redundant.`
}
