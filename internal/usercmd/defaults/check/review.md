---
description: Self-review the current branch's diff before opening a PR
argument-hint: '[base-branch]'
---
Review the current branch's diff against `$1` (or the repo's default
branch if `$1` is empty) as if you were a thoughtful reviewer reading
the PR for the first time. Surface findings — do not fix them.

1. **Sanity-check.** `git rev-parse --is-inside-work-tree` must return
   `true`. Resolve the base branch the same way `/git:create-pr`
   does: explicit `$1`, then `git symbolic-ref refs/remotes/origin/HEAD`,
   then `main` / `master` / `develop`. Stop if the current branch
   equals the base.

2. **Read the change.**
   - `git diff <base>...HEAD --stat` — file-level overview.
   - `git diff <base>...HEAD` — full diff.
   - `git log <base>..HEAD --format="%h %s"` — commit list.
   For each file touched, also read enough surrounding context (not
   just the hunks) to evaluate whether the change makes sense in its
   neighborhood.

3. **Figure out the intent.** From the commit messages and the diff
   itself, write a one-sentence summary of what this PR claims to do.
   You'll evaluate every finding against that intent — scope creep is
   a real finding category.

4. **Review across these dimensions:**

   - **Correctness** — does the code do what the diff implies it
     does? Look for off-by-one, wrong default, swapped arguments,
     mishandled error paths, missing nil/empty checks, race
     conditions, unhandled edge cases. Cross-check against any
     pre-existing tests touched.
   - **Scope** — does anything in the diff fall outside the stated
     intent? Refactors mixed into bug fixes, formatting drift,
     unrelated package changes. Note them — they're not always wrong
     but the reviewer should know.
   - **Tests** — does the change ship with tests proportional to the
     risk? For bug fixes: is there a regression test? For features:
     are happy + edge paths covered? Flag missing tests as a finding,
     not as a fail.
   - **Style & consistency** — does the new code match patterns
     already established in the touched files / packages? Naming,
     error-handling shape, import grouping, log formatting.
   - **Security & privacy** — credentials/tokens/PII handling, input
     validation at trust boundaries, command-injection / SQL-injection
     vectors, broad-permission grants, logged sensitive data.
   - **Performance** — obvious O(n²) where n can grow, allocations
     in hot loops, synchronous I/O on request paths, unbounded
     buffers/queues. Don't speculate — only flag if the code already
     gives you reason to suspect a problem at realistic scale.
   - **Docstrings / docs** — public API changes that don't update
     adjacent docs, README, or CHANGELOG. Optional but flag-worthy.

5. **Report findings** as a structured markdown list, grouped by
   priority. Use these three levels and only these three:

   ```
   ## Blocker
   - **<file:line>** — <one-line finding>. <one-line why-it-matters
     and proposed fix direction>

   ## Suggestion
   - **<file:line>** — <…>

   ## Nit
   - **<file:line>** — <…>
   ```

   Definitions:
   - **Blocker** — would not approve a PR until fixed. Correctness
     bugs, security issues, broken tests, scope explosions.
   - **Suggestion** — should address, but not a hard veto. Missing
     tests, style drift, mild over-engineering, missing docs.
   - **Nit** — cosmetic or preference-shaped. Skip these entirely
     if the diff is small; they're noise on a fast review.

   End with a one-paragraph summary: total findings by level, your
   overall recommendation (ship / fix-blockers-first / needs-discussion),
   and one specific thing the PR did well (only when sincere — don't
   manufacture a compliment).

If the diff is empty (no changes vs base), say so and stop. If the
diff is enormous (> 1000 lines), do a representative review and call
out that you sampled; suggest splitting before merge. Reference exact
file:line locations for every finding — vague findings without
locations are not actionable.
