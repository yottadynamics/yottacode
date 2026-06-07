package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
// (gh_issue_context + gh_issue_create) and submits via startTurn. The
// reliability work — template detection, label/assignee availability,
// gh-unavailable fall-through, title validation — lives in Go inside
// those tools, not in the prompt.
//
// One optional arg: `/git-create-issue <title>` pins the issue title.
// With no arg, the workflow starts with context gathering and lets the
// user compose the issue interactively.
func cmdGitCreateIssue(m Model, args []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[git-create-issue] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	title := ""
	if len(args) > 0 {
		title = strings.TrimSpace(args[0])
	}
	display := "/git-create-issue"
	if title != "" {
		display += " " + title
	}
	prompt := gitCreateIssueDirective(title)
	out, cmd := m.startTurnWithDisplay(prompt, display)
	return out.(Model), cmd
}

// gitCreateIssueDirective is the prompt the /git-create-issue handler
// hands the agent. Deliberately short because the composite tools
// own the state machine: the directive only describes the three-step
// shape and the surfacing rules for each typed result branch.
//
// The title argument, when supplied, is splice-injected into the
// gh_issue_context call so the model doesn't have to thread it as a
// string parameter through prose interpretation.
func gitCreateIssueDirective(title string) string {
	titleLine := `Step 1 — call gh_issue_context with no arguments. It returns a typed snapshot
under section headers (## state, ## template).`
	if title != "" {
		titleLine = "Step 1 — call gh_issue_context with no arguments. You will use title=" + title + " in the create step."
	}

	return `Create a GitHub issue in the current repository.

` + titleLine + `

Step 2 — read the ## state header and branch on its flags BEFORE
composing anything:
- gh_available=false → fall through to draft-only: emit the title
  and body you would have used as plain scrollback text, then tell
  the user to run gh auth login or paste the body into the GitHub
  UI manually. STOP. Do not attempt the create call.

Step 3 — compose a title and body using the snapshot:
- Title: ≤72 chars, imperative mood, no trailing period. Describe
  the outcome, not the mechanics.
- Body: if ## template.content exists, fill that template
  preserving its section order and headers. Otherwise use this
  default skeleton:

  ## Summary
  <1-3 bullets, the "why">

  ## Details
  <additional context, reproduction steps, etc.>

  ## Checklist
  - [ ] <concrete check>

  Cap the body at ~80 lines.

Step 4 — call gh_issue_create with title / body / labels / assignees.
The approval modal fires showing the full title + body inline; the user
approves or denies.

Step 5 — surface the result envelope verbatim:
- "created=true url=... number=..." → emit a "Issue created: <url>"
  line plus a one-line summary (title, labels).
- "created=false reason=validation error=..." → surface the error,
  then either (a) compose a new title/body that satisfies the rule
  and call gh_issue_create once more, or (b) stop and ask. Pick (b)
  after one retry.
- "created=false reason=gh_unavailable" → fall through to
  draft-only as described in step 2.
- "created=false reason=gh_error" → surface the gh output verbatim
  and STOP. Do NOT auto-retry, auto-edit, or gh issue edit. The user
  needs to see the error and decide.

Hard prohibitions — none of these are reachable through the
composite tools, but the model must not invoke them via other
tools either:
- Do NOT run gh issue edit — this command creates new issues only.
- Do NOT auto-assign labels, projects, or milestones beyond what
  the user explicitly provided.
- Do NOT invent checklist items the diff cannot support.`
}