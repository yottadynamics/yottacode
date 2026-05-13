---
description: Draft a PR title and body for the current branch
argument-hint: '[base-branch]'
---
Generate a PR title and description for the current branch's changes
against `$1` (or the repo's default branch if `$1` is empty). Do
**not** open the PR — your job is the writeup. The user will paste
into a PR or run `gh pr create` themselves.

1. **Sanity-check the repo.** `git rev-parse --is-inside-work-tree`
   must return `true`. If not, stop.

2. **Resolve the base branch.**
   - If `$1` is set, use it. Verify with `git rev-parse --verify "$1"`.
   - Otherwise detect: try `git symbolic-ref refs/remotes/origin/HEAD`
     (yields e.g. `refs/remotes/origin/main`). Fall back to checking
     for `main`, then `master`, then `develop`. If none exist locally
     or on origin, stop and ask the user which branch is the base.
   - The current branch must not equal the base. Stop if it does.

3. **Read the diff.**
   - `git diff <base>...HEAD --stat` for the file-level summary.
   - `git diff <base>...HEAD` for the full diff (note the **three-dot**
     form — that's "changes on HEAD since branching from base", not
     "all differences").
   - `git log <base>..HEAD --format="%h %s%n%b%n---"` for the commit
     log on this branch (two-dot form — that's the commits unique to
     this branch).

4. **Check for a PR template.** Look at `.github/pull_request_template.md`,
   `.github/PULL_REQUEST_TEMPLATE.md`, `docs/pull_request_template.md`,
   or `PULL_REQUEST_TEMPLATE.md` at the repo root. If one exists, read
   it and shape the body to fill in its sections. Preserve the section
   order and headers exactly.

5. **Compose the writeup:**

   **Title** — one line, ≤ 70 chars. Imperative mood. Should describe
   the *outcome* of the PR, not the mechanics. Match the dominant
   commit-message style (Conventional Commits prefix, ticket
   reference, etc. — same detection as for commit-message).

   **Body** — when there's no template, default to this skeleton:

   ```
   ## Summary

   <1-3 bullets, the "why" not the "what" — the diff shows the what>

   ## Changes

   <1-line per logical change, grouped if multiple are related>

   ## Test plan

   - [ ] <what reviewer should run/check to verify; concrete>
   - [ ] <…>

   ## Notes for reviewer

   <optional: anything non-obvious — design alternatives considered,
   follow-ups deferred, parts that look weird but are intentional>
   ```

   When a template exists, replace the skeleton with the template's
   sections.

6. **Output:** a single fenced markdown block containing the body, with
   the title on a separate line above the fence (prefixed `Title: `).
   Below the fence, give one short paragraph noting the base branch
   resolved, the commit count on the branch, and — if `gh` is on PATH
   (`which gh`) — the exact `gh pr create` invocation the user can
   run, e.g.:

   ```
   gh pr create --base <base> --title "<title>" --body-file -
   ```

   followed by a hint that they can pipe the body into stdin.

Do not invent test plan items the diff can't support. Do not paste
the diff verbatim into the body — describe it. Cap the body at
roughly 80 lines; long PRs should link out to design docs / RFCs
rather than dump them into the description.
