package tui

import (
	tea "charm.land/bubbletea/v2"
)

// cmdGitCommit handles `/git-commit` — the procedural git-commit
// flow that replaces the legacy markdown directive
// `/git:commit-message`.
//
// The slug is prefixed `git-` rather than namespaced `git:`. Built-in
// slugs are flat (no `:` separator — that's reserved for custom-command
// path derivation), and the `git-` prefix gives palette discoverability:
// typing `/git` filters to every git-related built-in. Sibling commands
// in this family: `/git-create-pr`.
//
// Where the legacy directive injected 118 lines of prose and asked the
// model to parse bash output, decide early-exit conditions, infer
// style, and obey "hard prohibitions" in English, this handler hands
// the model two composite tools (git_commit_context + git_commit_apply)
// and a ~20-line prompt naming them. The reliability work moves into
// Go: empty staging early-exits at the apply tool deterministically,
// subject validation rejects oversize / trailing-period / multi-line
// messages *before* invoking git, hook failures return as typed
// envelope fields the model surfaces verbatim, and there is no path
// the model can take that ends in `git commit --amend` without the
// user explicitly asking for it.
//
// The slash command itself is a thin wrapper — same shape as cmdInit:
// build the directive, submit via startTurn, let the normal agent loop
// + approval modal handle the rest. The composite tools are where the
// foundation work landed.
func cmdGitCommit(m Model, _ []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render(SysMsg(SysWarning, "git-commit", "turn already running", "wait or Esc to cancel")))
		return m, nil
	}
	prompt := gitCommitDirective()
	out, cmd := m.startTurnWithDisplay(prompt, "/git-commit")
	return out.(Model), cmd
}

// gitCommitDirective is the prompt body the /git-commit handler
// hands the agent. Deliberately small: control flow lives in the
// composite tools, so the prompt only has to describe the two-step
// shape and the surfacing rules for the typed result envelope.
//
// Kept as a package-level function (not a const) so tests can compare
// against a stable value and future model-specific tuning (e.g. a
// shorter rendition for Haiku) has one place to swap.
func gitCommitDirective() string {
	return `Commit the currently staged changes with a one-line message.

Step 1 — call git_commit_context (no arguments). It returns a typed
snapshot under section headers (## state, ## staged.name-status,
## staged.diff, ## recent.subjects, ## branch.commits, ## prose,
## unstaged, ## untracked).

Step 2 — read the snapshot:
- If "staged_empty=true" in the ## state header, surface
  "[git-commit] nothing staged — use git_stage_files first" and STOP.
  Do NOT call git_stage_files yourself; the user controls staging.
- Otherwise compose a one-line subject that matches detected_style:
    conventional   → "type(scope)?: subject" (lowercase type)
    ticket-prefix  → "ABC-123 subject"
    plain          → imperative subject only
  Priority for content (highest first):
    1. ## prose diffs (CHANGELOG / README / docs name the feature)
    2. ## branch.commits (your own commits on this branch)
    3. ## branch name with prefixes stripped, rewritten imperative
    4. ## staged.name-status as last-resort file-list inference
  Rules: ≤72 chars, imperative mood, no trailing period, no body.

Step 3 — call git_commit_apply with the message. The approval modal
fires here showing the message + the git invocation; the user
approves or denies.

Step 4 — surface the result envelope verbatim:
- "committed=true sha=..." → emit the SHA line; if the result also
  carries ## unstaged or ## untracked sections, list them under
  "Note: this commit included ONLY the staged files. Not included:".
- "committed=false reason=staged_empty" → surface and stop. Do NOT
  retry, do NOT call git_stage_files.
- "committed=false reason=validation error=..." → surface the
  error, then either (a) compose a new message that satisfies the
  rule and call git_commit_apply once more, or (b) stop and ask the
  user. Pick (b) when you've already retried once.
- "committed=false reason=hook_error" → surface the hook output
  verbatim and STOP. Do NOT auto-retry, auto-stage, or
  git commit --amend. Hook errors are the user's problem to fix.

Do not run git commit --amend under any circumstance unless the user
explicitly asks. Do not run git_stage_files to "fix" missing
staging; the unstaged/untracked sections of the snapshot are
informational, not a prompt to act.`
}
