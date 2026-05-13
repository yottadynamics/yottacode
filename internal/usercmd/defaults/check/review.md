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

4. **Verify before asserting.** Before listing any finding that
   makes a claim about runtime behavior crossing a function or file
   boundary, prove it by tracing the data flow:

   - **Negative claims** ("this is only logged at X", "this never
     fires", "Y is silent about Z", "no one consumes this", "this
     warning is dropped") — these feel safe to make from the diff
     alone but are exactly where reviewers lose credibility by being
     confidently wrong. Before asserting, grep for the
     symbol/type/constant across the package (and the importing
     packages) to find the consumer. If you can find a consumer that
     handles the case, the claim is wrong — don't ship the finding.
   - **Positive cross-boundary claims** ("this introduces a deadlock
     against X", "this races with Y in package Z") — same rule,
     trace the interaction.
   - **Couldn't verify?** Downgrade the finding to a **Nit** with the
     prefix `(unverified)` rather than asserting it as a Blocker or
     Suggestion. A false positive in the Blocker / Suggestion lanes
     poisons the rest of the review's credibility.

   Concrete example of the failure mode: a diff in
   `internal/foo/load.go` appends a warning to a slice, and the
   reviewer flags it as "only logged at load time, promote to a
   visible startup message." If they had grepped for the warning
   type across the package, they'd find the consumer at
   `internal/foo/run.go` that already renders it as a styled
   startup notice. The finding was wrong because it skipped the
   one-indirection trace.

5. **Review across these dimensions:**

   - **Correctness** — does the code do what the diff implies it
     does? Look for off-by-one, wrong default, swapped arguments,
     missing nil/empty checks, race conditions, unhandled edge
     cases. Cross-check against any pre-existing tests touched.
     Also scan for **error-handling specifics**:
     - Errors swallowed: `_ = err`, empty `if err != nil { }`
       blocks, ignored function returns where the doc says they
       indicate failure
     - Wrap chains lost: `fmt.Errorf("... %v", err)` instead of
       `%w` — breaks `errors.Is` / `errors.As` downstream
     - Generic messages without context (return-bare-err patterns
       where the caller can't tell which operation failed)
     - **Resource cleanup**: `Open()` / `Acquire()` without a
     matching `defer Close()` / `defer Release()`; goroutines
     spawned without a context cancellation path; channels left
     unclosed when senders go away.
   - **API / Compatibility** — would this change break a
     downstream caller? Specifically:
     - Renamed or removed exported / public functions, types,
       methods, constants
     - Changed signatures of exported symbols (parameter added,
       return type changed, receiver changed)
     - Removed CLI flags, subcommands, or aliases
     - Removed or renamed config keys (or changed their default)
     - Changed JSON / protobuf / DB schemas without a backward-read
       path
     If yes and the project isn't pre-1.0, flag as Blocker unless
     the diff also includes the migration / deprecation path. For
     internal-only symbols (lowercase in Go, leading underscore in
     Python, `internal` keyword in Java/Rust packages), this isn't
     a compatibility issue — just note it as Scope.
   - **Scope** — does anything in the diff fall outside the stated
     intent? Refactors mixed into bug fixes, formatting drift,
     unrelated package changes. Note them — they're not always wrong
     but the reviewer should know.
   - **Tests** — two questions: are tests **present**, and do they
     **actually verify the claim**?
     - Presence: for bug fixes, is there a regression test? For
       features, are happy + edge paths covered?
     - **Quality**: do the assertions actually catch the bug the
       PR claims to fix, or could they pass even if the fix were
       absent? Watch for: `assert true == true` patterns, tests
       that only verify mock-call counts (testing the mock, not
       the code), brittle timing-dependent assertions
       (`time.Sleep`-based), env-dependent values not isolated
       via `t.Setenv` / fixtures, missing negative cases (only
       happy-path coverage).
     Flag missing-or-weak tests as a finding, not as a fail.
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

6. **Report findings** as a structured markdown list, grouped by
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
     bugs, security issues, broken tests, swallowed errors on
     code paths users hit, **unmigrated API / CLI / config
     compatibility breaks** (in post-1.0 projects), scope
     explosions.
   - **Suggestion** — should address, but not a hard veto. Missing
     tests, **weak tests that pass for the wrong reason**, style
     drift, mild over-engineering, missing docs, internal-only
     symbol renames without a deprecation comment.
   - **Nit** — cosmetic or preference-shaped, OR findings prefixed
     `(unverified)` from the step-4 trace check. Skip these
     entirely if the diff is small; they're noise on a fast review.

   End with a one-paragraph summary: total findings by level, your
   overall recommendation (ship / fix-blockers-first / needs-discussion),
   and one specific thing the PR did well (only when sincere — don't
   manufacture a compliment).

If the diff is empty (no changes vs base), say so and stop. If the
diff is enormous (> 1000 lines), do a representative review and call
out that you sampled; suggest splitting before merge. Reference exact
file:line locations for every finding — vague findings without
locations are not actionable.
