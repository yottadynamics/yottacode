package promptmacros

// GitPushDirective is the prompt /git-push hands the agent. The
// model has effectively one job (call git_push and route the
// typed result), so the directive is correspondingly short —
// every other line is the surfacing rule for one envelope branch.
func GitPushDirective() string {
	return `Push the current branch to origin.

Step 1 — call git_push (no arguments). The tool detects whether
the current branch already tracks an upstream and adds
"-u origin HEAD" only on first push. The approval modal fires
showing the resolved command; user approves or denies.

Step 2 — surface the result envelope verbatim:
- "pushed=true" with "pr_url=<url>" → emit:
    Pushed: <branch> → origin
    PR updated: <pr_url>
- "pushed=true" with empty pr_url → emit:
    Pushed: <branch> → origin
    (no PR exists for this branch — use /git-create-pr to open one)
- "pushed=false reason=detached_head" → surface "[git-push] HEAD
  is detached — switch to a branch first" and STOP.
- "pushed=false reason=git_error" → quote the git output verbatim
  and STOP. Do NOT auto-retry, force-push, or rebase to "fix"
  the failure.

Hard prohibitions:
- Do NOT use --force or --force-with-lease. The git_push tool
  doesn't accept them; if the user needs force-push they invoke
  the unified git tool directly.
- Do NOT push a branch other than the current one (HEAD). The
  tool only ever pushes HEAD; don't bypass it via the unified
  git tool to push other refs without an explicit user request.`
}
