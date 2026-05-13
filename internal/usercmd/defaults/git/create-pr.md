---
description: Create a PR with auto-drafted title and body (approval modal pops for verification)
argument-hint: '[base-branch]'
---
**You are not done until you have either created the PR or
explicitly bailed out per the early-exit conditions in step 1.**
Producing only the draft text and stopping is a bug — the user
invoked this command expecting a PR to land.

The flow has at least two tool calls in this order: gather context,
then `gh pr create`. The approval modal on the create step shows
the full title + body inline — that's the user's verification
surface, exactly like `/git:commit-message`.

1. **Tool call 1 — gather context** in one bash command:

   ```bash
   # Resolve base branch: explicit $1 first, then origin/HEAD, then main/master/develop.
   base="$1"
   if [ -z "$base" ]; then
     base=$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null | sed 's@^origin/@@')
   fi
   if [ -z "$base" ]; then
     for candidate in main master develop; do
       if git rev-parse --verify "$candidate" >/dev/null 2>&1; then base="$candidate"; break; fi
     done
   fi
   echo "BASE=$base" && \
   echo "---CURRENT-BRANCH---" && \
   git branch --show-current && \
   echo "---AHEAD-COUNT---" && \
   git rev-list --count "$base..HEAD" 2>/dev/null && \
   echo "---STAT---" && \
   git diff "$base"...HEAD --stat && \
   echo "---LOG---" && \
   git log "$base"..HEAD --format="%h %s%n%b%n---" && \
   echo "---GH-AVAILABLE---" && \
   { which gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1 && echo "yes" || echo "no"; } && \
   echo "---ON-ORIGIN---" && \
   { git ls-remote --exit-code --heads origin "$(git branch --show-current)" >/dev/null 2>&1 && echo "yes" || echo "no"; } && \
   echo "---PR-TEMPLATE---" && \
   for t in .github/pull_request_template.md .github/PULL_REQUEST_TEMPLATE.md docs/pull_request_template.md PULL_REQUEST_TEMPLATE.md; do
     [ -f "$t" ] && echo "FOUND: $t" && cat "$t" && break
   done
   ```

   **Early-exit conditions** (the only ways to end without creating
   the PR):
   - Current branch equals base → tell the user to switch to a
     feature branch first. Stop.
   - AHEAD-COUNT is 0 → tell the user there are no commits ahead of
     the base, nothing to PR. Stop.
   - GH-AVAILABLE is `no` → fall back to draft-only: print the
     drafted title + body in scrollback and tell the user to either
     install/auth `gh` (`gh auth login`) or paste the body into
     GitHub's web UI manually. Stop.

2. **Compose title + body mentally** (do not emit yet).

   **Title** — ≤ 70 chars, imperative mood, no trailing period.
   Describe the outcome of the PR, not the mechanics. Match the
   dominant commit-message style from the LOG section (Conventional
   Commits prefix, ticket reference, etc.).

   **Body** — if PR-TEMPLATE was found, fill its sections while
   preserving the template's exact section order and headers.
   Otherwise default skeleton:

   ```
   ## Summary

   <1-3 bullets, the "why" — the diff shows the what>

   ## Changes

   <1 line per logical change>

   ## Test plan

   - [ ] <what reviewer should run/check; concrete>

   ## Notes for reviewer

   <optional: design alternatives, follow-ups deferred, anything
   non-obvious in the diff>
   ```

   Cap the body at ~80 lines.

3. **Tool call 2 (only if needed) — push the branch.** If ON-ORIGIN
   is `no`, push it first. The approval modal fires:

   ```bash
   git push -u origin HEAD
   ```

   On denial: stop with `PR not created — branch not pushed to
   origin.` Do not try to create the PR.

4. **Tool call 3 — create the PR.** The approval modal fires here.
   This is the user's primary verification — the modal shows the
   full title + body inlined in the heredoc.

   ```bash
   gh pr create --base <BASE> --title "<title>" --body-file - <<'EOF'
   <body content>
   EOF
   ```

   Use single-quoted heredoc delimiter so `$`, backticks, and quotes
   in the body stay literal. Substitute `<BASE>` with the resolved
   base from step 1.

5. **Tool call 4 — capture the URL.** On success:

   ```bash
   gh pr view --json url --jq .url
   ```

6. **Print the text summary.** This is the only text output. Format:

   - **Succeeded** —

     ```
     PR created: <url>

     Title: <title>
     Base: <base>
     Commits in PR: <ahead-count>
     ```

   - **Approved but gh failed** (network, existing PR, missing
     permissions, etc.) — surface the error verbatim:

     ```
     PR creation failed: <verbatim error from gh>
     ```

     Do NOT auto-retry, auto-edit, or "fix" the failure.

   - **Denied at the create-PR modal** — print:

     ```
     PR not created — draft was unchanged in your branch.
     ```

**Hard prohibitions:**

- **Do not stop after composing title + body — you must invoke the
  create tool call (step 4) or hit an early-exit condition.**
- Do not run `gh pr create --draft` unless the user explicitly asks
- Do not auto-assign reviewers, labels, projects, or milestones
- Do not run `gh pr edit` or `gh pr merge` — this command creates
  new PRs only
- Do not push to a base branch that isn't the user's own feature
  branch (the `git push -u origin HEAD` only pushes the current
  branch; never `git push origin <something-else>`)
- Do not invent test-plan items the diff cannot support
- Do not paste the diff verbatim into the body — describe it
