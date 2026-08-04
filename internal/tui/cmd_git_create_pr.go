package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cmdGitCreatePR handles `/git-create-pr` — the procedural
// git-create-pr flow that replaces the legacy markdown directive
// `/git:create-pr`.
//
// Slug shape mirrors `/git-commit`: `git-` prefix instead of `git:`
// namespace because built-in slugs are flat and the prefix gives
// palette discoverability — typing `/git` filters to every
// git-related built-in.
//
// Same shape as cmdGitCommit: the slash handler is a thin wrapper
// that composes a narrow directive naming the two composite tools
// (pr_context + pr_create) and submits via startTurn. The
// reliability work — base resolution, ahead-count gating,
// gh-unavailable fall-through, title validation, push state
// detection — lives in Go inside those tools, not in the prompt.
//
// One optional arg: `/git-create-pr <base>` pins the base branch.
// With no arg, the workflow's deterministic base resolver picks
// origin/HEAD, then main/master/develop in order.
func cmdGitCreatePR(m Model, args []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[git-create-pr] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	base := ""
	if len(args) > 0 {
		base = strings.TrimSpace(args[0])
	}
	display := "/git-create-pr"
	if base != "" {
		display += " " + base
	}
	prompt := gitCreatePRDirective(base)
	out, cmd := m.startTurnWithDisplay(prompt, display)
	return out.(Model), cmd
}

// gitCreatePRDirective is the prompt the /git-create-pr handler
// hands the agent. Deliberately short because the composite tools
// own the state machine: the directive only describes the two-step
// shape and the surfacing rules for each typed result branch.
//
// The base argument, when supplied, is splice-injected into the
// pr_context call so the model doesn't have to thread it as a
// string parameter through prose interpretation. (The tool also
// accepts no-base and runs its fallback chain, so empty base is
// also fine.)
func gitCreatePRDirective(base string) string {
	baseArgLine := `Step 1 — call pr_context with no arguments. It will resolve the
base branch via origin/HEAD → main/master/develop.`
	if base != "" {
		baseArgLine = "Step 1 — call pr_context with base=\"" + base + "\"."
	}

	return `Open a GitHub pull request for the current branch.

` + baseArgLine + ` It returns a typed snapshot
under section headers (## state, ## diff.stat, ## commits.log,
## pr.template).

Step 2 — read the ## state header and branch on its flags BEFORE
composing anything:
- base_equals_current=true → surface "[git-create-pr] HEAD is already
  on the base branch — switch to a feature branch first" and STOP.
- ahead_count=0 → surface "[git-create-pr] no commits ahead of <base>;
  nothing to PR" and STOP. Do NOT try to create an empty PR.
- gh_available=false → fall through to draft-only: emit the title
  and body you would have used as plain scrollback text, then tell
  the user to run gh auth login or paste the body into the GitHub
  UI manually. STOP. Do not attempt the create call.
- pushed_to_origin=false → push the current branch first with the
  unified git tool: call git with args=["push","-u","origin","HEAD"].
  The approval modal fires. If the user denies, surface
  "[git-create-pr] PR not created — branch not pushed to origin"
  and STOP.

Step 3 — compose a title and body using the snapshot:
- Title: ≤72 chars, imperative mood, no trailing period. Describe
  the outcome, not the mechanics. If the ## commits.log shows a
  dominant style (Conventional Commits prefix, ticket reference),
  match it.
- Body: if ## pr.template names a path, fill that template
  preserving its section order and headers. Otherwise use this
  default skeleton:

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

Step 4 — call pr_create with base / title / body. The approval
modal fires showing the full title + body inline; the user
approves or denies.

Step 5 — surface the result envelope verbatim:
- "created=true url=... number=..." → emit a "PR created: <url>"
  line plus a one-line summary (title, base, commit count).
- "created=false reason=validation error=..." → surface the error,
  then either (a) compose a new title/body that satisfies the rule
  and call pr_create once more, or (b) stop and ask. Pick (b)
  after one retry.
- "created=false reason=github_unavailable" → fall through to
  draft-only as described in step 2.
- "created=false reason=github_error" → surface the gh output verbatim
  and STOP. Do NOT auto-retry, auto-edit, gh pr merge, or
  gh pr edit. The user needs to see the error and decide.

Hard prohibitions — none of these are reachable through the
composite tools, but the model must not invoke them via other
tools either:
- Do NOT run gh pr edit or gh pr merge — this command creates new
  PRs only.
- Do NOT auto-assign reviewers, labels, projects, or milestones.
- Do NOT push to any branch other than the current one (HEAD).
- Do NOT invent test-plan items the diff cannot support.`
}
