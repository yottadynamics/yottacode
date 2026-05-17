---
name: receiving-code-review
description: Apply code review feedback with discipline — verify each comment before changing code, group changes coherently, push back when wrong, and signal completion explicitly. Use when the user hands you review comments from a PR, a reviewer, or another agent.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/obra/superpowers/tree/main/skills/receiving-code-review
---

# Receiving code review

Review feedback is not a list of edits to apply. It's a set of *claims* about the code, some of which are right, some wrong, and some context-dependent. Applying every comment without thought ships bugs introduced by misread reviews; rejecting every comment loses the value of the review entirely. The discipline is: verify, then act.

## 1. Read every comment before touching any code

Skim the full set once. Three things to recognize:

- **Critical** — bug, security, broken contract. Must address.
- **Strong** — style, structure, naming the reviewer cares about. Usually address.
- **Nit** — preference. Address if cheap, skip otherwise.

If the reviewer didn't tag severity, you tag it. The tag drives your priority.

Also note when comments **contradict each other** — two reviewers, or one reviewer at two points. Surface the contradiction out loud before resolving it; don't pick a side silently.

## 2. Verify each comment against the code

For each comment, before changing anything, answer one of:

- **Right** — the comment correctly identifies a real issue. Plan a fix.
- **Wrong** — the comment misreads the code, or applies a pattern that doesn't fit here. Plan a response.
- **Partial** — there's a real issue but the suggested fix doesn't match. Plan an alternative.
- **Outdated** — the code has changed since the comment landed. Mark resolved without a code change.

Verification is not a courtesy step. A common failure mode is "reviewer said the cache key should include the user ID, so I added it" — and then the test breaks because the cache key was deliberately keyed without user ID to support shared rendering. Read the code at the cited lines before applying the change.

## 3. Push back when you're right

If a comment is wrong or partial, reply with:

1. A one-sentence acknowledgement that you read it ("I see the concern about the cache key.").
2. The concrete reason it doesn't apply ("This path is keyed without user ID intentionally — the cache is shared across users for the public landing page; see `cache_strategy.md`.").
3. A proposal: leave as-is, or address a different concern the comment is gesturing at.

Pushing back is part of the job. Silent compliance to wrong comments degrades the codebase and trains the reviewer to leave more low-quality comments. Be respectful, be specific, be brief.

## 4. Group changes coherently

Apply changes in batches that hold together:

- **One refactor → one commit.** If the reviewer asked to rename `Foo` to `Bar`, that's one commit, not eight.
- **Behavioral fixes separate from style.** A commit that mixes "fix off-by-one" with "rename variables" is hard to review on the second pass.
- **Don't bundle resolved nits into a behavioral commit** — if the bug fix is the load-bearing change, surface it; let the nits land in a tail commit.

Use commit messages that reference the comment ("fix: deref nil cursor on empty result — addresses comment from <reviewer>"). The next reviewer should be able to walk the new commits and check off comments against them.

## 5. Test after every batch

After each commit-sized batch, re-run the relevant tests. Two reasons:

- Catch regressions introduced by the change.
- Catch regressions introduced by *resolving* a comment that turned out to be wrong but you applied anyway.

A change set that "addresses all 14 comments" but hasn't been re-tested between batches is a single black box on the next review pass.

## 6. Reply explicitly per comment

On the PR (or in your reply to the user), respond to every comment with one of:

- "Fixed in `<commit-sha>`."
- "Disagree — `<reason>`. Suggest leaving as-is."
- "Resolved already in `<commit-sha>` before this comment landed."
- "Deferred — `<reason>`. Tracking in `<issue/TODO>`."

A comment with no reply is a comment the reviewer thinks was lost. Even "won't fix, because X" is better than silence.

## 7. Signal completion explicitly

When the batch is done:

1. Summarize the changes by category (behavioral / style / docs / tests).
2. List the comments you pushed back on, with the reasons.
3. Re-request review by tagging the reviewer.

Don't say "done with the review" without the summary — the reviewer has to re-load context, and the summary saves them the work.

## 8. Anti-patterns

- **Apply-every-comment reflex** — losing the verify step ships review-induced bugs.
- **Apply-then-explain** — making the change before stating why turns push-back into a revert. State the reasoning first.
- **One mega-commit** — fourteen comments in one diff means the reviewer can't tell which comment drove which change.
- **Silent disagreement** — skipping a comment without replying signals you missed it or ignored it. Either is bad.
- **Re-litigating settled decisions** — if a comment touches a decision already aired and made, point at where it was made; don't re-debate.
