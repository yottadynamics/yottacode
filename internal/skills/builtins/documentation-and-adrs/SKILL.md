---
name: documentation-and-adrs
description: Capture the *why* behind decisions as you ship — ADRs for choices that are expensive to reverse, why-comments for non-obvious code, and rules files that stay current. Use when making an architectural decision, changing a public API, or when you've had to explain the same thing twice.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/addyosmani/agent-skills/tree/main/skills/documentation-and-adrs
---

# Documentation & ADRs

Code already shows *what* was built. Documentation earns its place only when it records what the code can't: the constraints in play, the alternatives rejected, and the reason this shape won. A comment that restates the line above it is noise; one that explains why the obvious approach was avoided is the most valuable line in the file.

## 1. Record decisions in the same change that makes them

Write the rationale in the same diff as the decision — not "later, once it stabilises". Later never comes, and by then you've forgotten the alternatives you weighed. A decision documented while it's fresh costs ten minutes; reconstructed from git archaeology six months on, it costs an afternoon and is usually wrong. Code, the choice it encodes, and the record of why are one unit of work.

## 2. When a decision deserves an ADR

Write an Architecture Decision Record when a choice is expensive to reverse or will outlive your memory of it:

- Picking a framework, library, or major dependency.
- Designing a data model or storage schema.
- Choosing an auth strategy or trust boundary.
- Settling an API, protocol, or wire-format shape.
- Any "we considered X and Y, went with Y" you'd otherwise re-litigate.

Skip it for reversible, local, or self-evident choices. An ADR for "named the counter `count`" erodes trust in the ones that matter.

## 3. ADR format

Store one file per decision in `docs/decisions/`, numbered sequentially (`0007-cache-eviction-policy.md`):

```
# ADR-0007: <decision title>

## Status
Proposed | Accepted | Superseded by ADR-0012 | Deprecated

## Date
YYYY-MM-DD

## Context
The forces at play — requirements, constraints, what prompted the decision.

## Decision
What we chose, in one or two sentences.

## Alternatives considered
Each option weighed, with the reason it lost.

## Consequences
What this commits us to — the good, the bad, and the new constraints.
```

ADRs are append-only history. When a decision changes, write a *new* ADR that supersedes the old one and flip the old one's status to `Superseded by ADR-NNNN` — don't edit or delete it. The trail of superseded decisions is the point: it shows future readers the road not taken and why you turned off it.

## 4. Comments: flag the non-obvious, nothing else

Reserve inline comments for intent the reader can't recover from the code itself — the reasoning behind a surprising choice:

```go
// Sliding window resets at the boundary, not on a fixed timer, so a
// burst straddling two windows can't slip through at double the rate.
if now.Sub(windowStart) > windowSize {
    count, windowStart = 0, now
}
```

…and gotchas that will bite the next caller:

```go
// MUST run before the first render: a later call flashes unstyled
// content because the theme context isn't available post-hydration.
```

Don't comment what the code plainly says, don't leave a `TODO` for work you can do right now, and don't keep commented-out code — version control already remembers it.

## 5. Document the surfaces others depend on

A function called across a boundary states its contract: what the arguments mean, what it returns, what it errors on. A README states the one-paragraph purpose, how to run the thing, and the command reference. A changelog groups each release into Added / Changed / Fixed with PR references. None of this has to be exhaustive — it has to answer the first question a newcomer asks before they have to read the source.

## 6. Keep the rules files current

The conventions an agent reads first — `CLAUDE.md`, `AGENTS.md`, `.yottacode/YOTTACODE.md` — are documentation with the shortest half-life. When a decision changes how work is done in this repo, update the rules file in the same pass. A stale rule is worse than no rule: it's confidently wrong, and both humans and agents will follow it off the cliff.

## 7. Anti-patterns

- A hard-to-reverse decision with no written rationale — the next person re-litigates it from scratch.
- Comments that parrot the code instead of explaining intent.
- "Document it once the API stabilises" — writing it down is part of how it stabilises.
- Commented-out code and aging `TODO`s left as litter.
- An ADR for a trivial or easily-reversed choice — noise that buries the decisions that matter.
- Shipping a behaviour change without touching the rules file that describes the old behaviour.

## 8. Verification

- Every expensive-to-reverse decision in this change has an ADR or a why-comment.
- New public functions/commands state their contract.
- Non-obvious code and known gotchas are flagged at the point they bite.
- No commented-out code or do-it-now `TODO`s left behind.
- README and rules files (`CLAUDE.md` / `AGENTS.md` / `YOTTACODE.md`) still match reality.
