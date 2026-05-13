---
description: Write a one-line commit message and commit (approval modal pops for verification)
---
**You are not done until you have either committed or explicitly
bailed out per the early-exit conditions below.** The user invoked
this command expecting a commit to land; producing only the message
text and stopping is a bug.

The flow has two tool calls and one text summary, in this order:

1. **Tool call 1 — gather context** as one heredoc'd bash script.
   The `<<'SH'` (single-quoted delimiter) keeps it as a single
   logical command for the approval modal — splitting on `&&` would
   produce a dozen numbered items per invocation.

   ```bash
   bash <<'SH'
   git diff --cached --name-status
   echo "---STYLE-LOG---"
   git log -15 --format=%s
   echo "---BRANCH---"
   git branch --show-current 2>/dev/null
   echo "---BRANCH-COMMITS---"
   git log -10 --no-merges --not --remotes=origin --format=%s 2>/dev/null
   echo "---PROSE---"
   git diff --cached -- CHANGELOG.md README.md 'docs/*.md' 'docs/**/*.md' 2>/dev/null | head -140
   echo "---UNSTAGED-MODIFIED---"
   git diff --name-only 2>/dev/null
   echo "---UNTRACKED---"
   git ls-files --others --exclude-standard 2>/dev/null
   SH
   ```

   **Early-exit conditions** (these are the only ways to end without
   committing):
   - Staged file list empty → tell the user there are nothing staged
     (suggest `git add`). Stop.
   - Bash command failed → surface the error verbatim. Stop.

2. **Compose the subject mentally** (do not emit it yet). Priority
   order for content:
   1. PROSE — staged additions to CHANGELOG/README/docs
   2. BRANCH-COMMITS — your own commits on this branch
   3. BRANCH name — strip prefix, rewrite as imperative
   4. File list — last-resort name-status inference

   Style detection from STYLE-LOG: Conventional Commits / plain
   imperative / ticket-prefix — match the dominant pattern.

   Rules: ≤ 72 chars, imperative mood, no trailing period.

3. **Tool call 2 — commit it.** Immediately run the commit. The
   `run_bash` approval modal will fire here — the user sees the
   full command + message in the modal and approves or denies. The
   modal is the verification surface; you do not need to print the
   message to scrollback before this step.

   ```bash
   git commit -F - <<'EOF'
   <the exact subject you composed in step 2>
   EOF
   ```

   Use single-quoted heredoc delimiter so `$`, backticks, and quotes
   in the message stay literal.

4. **After the commit tool call returns, print the text summary.**
   This is the only text output you emit. Emit plain lines — do
   NOT wrap the summary in a markdown code fence (triple-backticks).
   The example below uses indentation to mark format, not fences.

       Matched <style>. Source: <prose|branch-commits|branch-name|file-list>.

       `<the subject you committed>`

       [commit-status line]

   The `[commit-status line]` depends on the tool result from step 3:
   - **Succeeded** — `Committed: <output of git log -1 --oneline>`.
     Optionally run `git log -1 --oneline` as a tiny third tool call
     to capture the SHA cleanly.
   - **Denied at the approval modal** — `Commit not applied — staged
     changes unchanged.`
   - **Approved but git failed** (pre-commit hook, conflict, etc.)
     — `Commit failed: <verbatim error>`. Do NOT auto-retry,
     auto-stage, auto-amend, or auto-fix the hook.

5. **FYI for unstaged/untracked files.** If either UNSTAGED-MODIFIED
   or UNTRACKED from step 1 had any entries AND the commit
   succeeded, append below the summary:

   ```
   Note: this commit included ONLY the staged files. Not included:

   Unstaged modifications:
     - <path>
     - <path>
   Untracked files:
     - <path>
   ```

   Show each section only when non-empty. Cap each at 10 paths with
   "… N more" overflow. Skip the FYI entirely if both sections are
   empty.

**Hard prohibitions:**

- **Do not stop after composing the subject — you must invoke the
  commit tool call (step 3) or hit an early-exit condition (step 1).**
- No body, footer, or trailing prose in the message
- Do not guess from directory names alone when PROSE, branch
  commits, or branch name name the feature explicitly
- If a one-liner cannot summarize the change, recommend splitting
  the commit — do not fall back to a longer message
- Do not run `git add` to "fix" missing staging — the FYI in step 5
  is informational, not a prompt to act
- Do not run `git commit --amend` unless explicitly asked
