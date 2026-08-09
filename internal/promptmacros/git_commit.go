package promptmacros

// GitCommitDirective is the prompt body /git-commit hands the agent.
// Deliberately small: control flow lives in the composite tools, so the
// prompt only has to describe the two-step shape and the surfacing
// rules for the typed result envelope.
func GitCommitDirective() string {
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
