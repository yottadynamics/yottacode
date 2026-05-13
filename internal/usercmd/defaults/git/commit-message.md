---
description: Write a one-line commit message and commit (approval modal pops for verification)
---
Generate a **single-line** commit message for the staged changes,
then commit using it. The `run_bash` approval modal fires for the
commit step — that's the user's verification window, where they see
the exact command + message before it lands.

1. **Gather context in one bash call:**

   ```bash
   git diff --cached --name-status && \
   echo "---STYLE-LOG---" && \
   git log -15 --format=%s && \
   echo "---BRANCH---" && \
   git branch --show-current 2>/dev/null && \
   echo "---BRANCH-COMMITS---" && \
   git log -10 --no-merges --not --remotes=origin --format=%s 2>/dev/null && \
   echo "---PROSE---" && \
   git diff --cached -- CHANGELOG.md README.md 'docs/*.md' 'docs/**/*.md' 2>/dev/null | head -140 && \
   echo "---UNSTAGED-MODIFIED---" && \
   git diff --name-only 2>/dev/null && \
   echo "---UNTRACKED---" && \
   git ls-files --others --exclude-standard 2>/dev/null
   ```

   - If the staged file list is empty: stop and tell the user there
     are no staged changes (suggest `git add`). Do NOT proceed to
     commit.
   - If git fails: stop and surface the error. Do NOT proceed.

2. **Pick the message content** using this priority order, stopping
   at the first source that gives a clear answer:

   1. **PROSE section** — staged additions to CHANGELOG / README /
      docs describe the change in the user's own words. Compress
      into one line.
   2. **BRANCH-COMMITS section** — your own already-made commits on
      this branch.
   3. **BRANCH name** — strip the prefix (`feature/`, `fix/`,
      `chore/`, etc.) and rewrite as imperative.
   4. **File list** — last-resort inference from name-status.

3. **Detect the dominant commit style** from STYLE-LOG:
   - Conventional Commits (`feat:`, `fix:`, etc.)
   - Plain imperative ("Add X", "Fix Y")
   - Ticket prefix (`[FOO-123]`, `JIRA-456:`)

4. **Compose ONE subject line:**
   - ≤ 72 chars total
   - Imperative mood
   - No trailing period
   - Match the detected style
   - Name the **feature or outcome** as the strongest signal
     described it

5. **Print the result, with an FYI when there are unstaged or
   untracked files.**

   First, the standard three-line output:
   - Line 1: `Matched <style>. Source: <prose|branch-commits|branch-name|file-list>.`
   - Lines 2-4: a fenced code block containing only the subject

   **Then, if either the UNSTAGED-MODIFIED or UNTRACKED section had
   any entries**, print an FYI block immediately below the fence:

   ```
   Note: This commit will include ONLY the staged files. The
   following are NOT included — run `git add` first if you want them
   in this commit:

   Unstaged modifications:
     - <path>
     - <path>
   Untracked files:
     - <path>
   ```

   Show each section only when it has entries. Cap each list at 10
   paths and append `… and N more` when there are more than 10.

   If both sections are empty, **skip the FYI entirely** — don't
   print a "no other changes" reassurance line, just go straight to
   the commit step.

6. **Run the commit.** Use a heredoc with a single-quoted delimiter
   so the message bypasses shell expansion (any `$`, backticks, or
   quotes in the message stay literal):

   ```bash
   git commit -F - <<'EOF'
   <the exact subject from above>
   EOF
   ```

   The `run_bash` approval modal pops here. The user sees the full
   command + message and approves or denies.

7. **Report the outcome:**
   - **Approved and succeeded** — print the resulting commit:
     `git log -1 --oneline` and surface its output.
   - **Approved but git failed** (pre-commit hook, conflict, etc.)
     — surface the failure verbatim. Do NOT auto-retry, auto-stage,
     auto-amend, or auto-fix the hook. The user resolves and
     re-runs `/git:commit-message`.
   - **Denied** — print one line: `Commit not applied — staged
     changes unchanged.` Stop.

**Hard prohibitions:**

- No body, footer, or trailing prose after the subject
- Do not guess from directory names alone when PROSE, branch
  commits, or the branch name name the feature explicitly
- If a one-liner cannot summarize the change, recommend splitting
  the commit — do not fall back to a longer message
- Do not commit if step 1 detected zero staged changes
- Do not run `git add` to "fix" missing staging — that's the
  user's call (the FYI in step 5 is informational, not a prompt
  to act)
- Do not run `git commit --amend` to rewrite an existing commit
  unless the user explicitly asked for it elsewhere
