package tui

import (
	"fmt"
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
// the issue title. runSlash splits the input line on whitespace, so
// "/git-create-issue Fix crash on resize" arrives as four fields —
// the title is their space-joined form, not args[0].
func issueTitleFromArgs(args []string) string {
	return strings.TrimSpace(strings.Join(args, " "))
}

// gitCreateIssueDirective is the prompt the /git-create-issue handler
// hands the agent. Deliberately short because the composite tools
// own the state machine: the directive only describes the three-step
// shape and the surfacing rules for each typed result branch.
//
// The title argument, when supplied, is pinned in the directive for
// the issue_create step (issue_context takes no arguments), so
// the model doesn't have to thread it through prose interpretation.
func gitCreateIssueDirective(title string) string {
	titleLine := `Step 1 — call issue_context with no arguments. It returns a typed snapshot
under section headers (## state, ## template).`
	if title != "" {
		titleLine = fmt.Sprintf("Step 1 — call issue_context with no arguments. You will use title=%q in the create step.", title)
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
- If ## templates lists more than one issue-creation target, ask the
  user which template to use unless the requested title/body clearly
  matches one option. Use template names/descriptions in the prompt.
- Use ## contact_links only as guidance. Documentation, discussions,
  contributing, and security links are not GitHub issues; for security
  reports, refuse public issue creation and point at the private URL.
- Title: ≤72 chars, imperative mood, no trailing period. Describe
  the outcome, not the mechanics. If the chosen template has
  title_prefix, preserve it when it helps the repo triage the issue.
- Body: use the chosen template content, preserving its section order
  and headers. YAML issue forms are already rendered as Markdown under
  content; fill required sections and remove placeholder comments that
  are not useful in the final issue. Use the template's labels and
  assignees only when the snapshot explicitly provides them. If no
  template is selected, use this default skeleton:

  ## Summary
  <1-3 bullets, the "why">

  ## Details
  <additional context, reproduction steps, etc.>

  ## Checklist
  - [ ] <concrete check>

  Cap the body at ~80 lines.

Step 4 — print the exact title and body you are about to post as
plain scrollback text, THEN call issue_create with title / body /
labels / assignees. The approval modal shows only the invocation
summary (title + labels + assignees), not the body — the text you
just printed is what the user is actually approving. Never call
issue_create with a body the user has not seen.

Step 5 — surface the result envelope verbatim:
- "created=true url=... number=..." → emit a "Issue created: <url>"
  line plus a one-line summary (title, labels).
- "created=false reason=validation error=..." → surface the error,
  then either (a) compose a new title/body that satisfies the rule
  and call issue_create once more, or (b) stop and ask. Pick (b)
  after one retry.
- "created=false reason=github_unavailable" → fall through to
  draft-only as described in step 2.
- "created=false reason=github_error" → surface the gh output verbatim
  and STOP. Do NOT auto-retry, auto-edit, or gh issue edit. The user
  needs to see the error and decide.

Hard prohibitions — none of these are reachable through the
composite tools, but the model must not invoke them via other
tools either:
- Do NOT run gh issue edit — this command creates new issues only.
- Do NOT auto-assign labels, projects, or milestones beyond what
  the user explicitly provided or the selected issue template declares.
- Do NOT invent checklist items the diff cannot support.`
}
