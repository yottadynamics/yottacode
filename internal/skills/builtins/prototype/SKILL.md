---
name: prototype
description: Build a throwaway prototype to learn an unfamiliar API or design space — explicitly time-boxed, explicitly disposable, with a "delete or integrate" gate at the end. Use when the user asks to "try X out", "see if Y works", or any exploratory task where the answer is "let's find out" rather than "let's ship it".
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/mattpocock/skills/tree/main/skills/engineering/prototype
---

# Prototype

A prototype answers a *question*, not a *requirement*. Its purpose is to make the next decision cheaper to make. The discipline is to keep that purpose visible: time-box the work, accept rough edges, and decide explicitly at the end whether the prototype's lessons go forward (and the code goes in the bin) or whether the code itself is worth integrating.

The most expensive prototype is one that quietly becomes production code because nobody decided otherwise.

## 1. Name the question

Before writing any code, state the question the prototype is answering. Examples:

- "Does Postgres's `LISTEN/NOTIFY` survive 10k concurrent listeners?"
- "Can we get acceptable LCP from server-rendering this page tree?"
- "Is the new SDK's auth flow workable with our token-rotation strategy?"
- "Does the library handle our actual data shape, or only the toy examples in its README?"

The question has to be **answerable**. A prototype with no decision criterion is just code without a home.

## 2. Time-box

Set a budget — usually small.

- **30 minutes to 2 hours** for an API spike ("does this library do what I think it does?").
- **2 to 8 hours** for a design spike ("does this architecture pattern fit our data?").
- **One day** for a vertical-slice prototype that exercises end-to-end ("can users actually do X?").

Anything longer than a day is no longer a prototype; it's a project. If the question genuinely needs more time, stop and write a plan instead.

When the budget expires, the prototype ends — even if you haven't answered the question. Sometimes the answer *is* "I needed more time than the budget allowed, and that's information." Don't blow past the budget hoping to land somewhere shippable.

## 3. Build for the question, not the future

Inside the prototype:

- **Hardcode** values the production version would parameterize.
- **Skip** error handling for paths the prototype doesn't exercise.
- **Inline** what would normally be modularized.
- **Use** the most direct library calls, even ones you wouldn't ship.
- **Print** to stdout instead of wiring telemetry.
- **No tests** — the prototype's "test" is whether the question was answered.

These shortcuts are the point. Resisting them defeats the prototype.

Two shortcuts you should *not* take:

- **Don't fake the load.** If the question is "does this scale to 10k connections", the prototype must actually open 10k connections. A prototype that lies about the question it's answering is worse than no prototype.
- **Don't fake the data.** If the question involves your real data shape, use a real sample. Toy data hides exactly the issues the prototype was built to find.

## 4. Keep prototype scope visible

Put the prototype in a place that screams "throwaway":

- A `prototype/` or `spike/` directory the team understands is disposable.
- A branch named `prototype/<question>` that won't be merged.
- A worktree dedicated to the prototype, with a `README.md` carrying the question.

Don't sneak prototype code into a feature branch alongside real changes. The two get reviewed together, and the prototype's shortcuts contaminate the real code.

## 5. The decision gate at the end

When the prototype ends — budget expired, or question answered — make one of three explicit decisions:

### a. Delete the prototype, write up the lesson

The common case. The prototype answered the question; the code itself isn't worth keeping. Write a short note (in `prototype/README.md`, or a Slack message, or a comment on the issue) capturing:

- The question.
- The answer.
- Any surprises the prototype surfaced.
- The implication for the real work.

Then delete the prototype directory or branch. The lesson lives in the writeup, not the code.

### b. Integrate selectively

The prototype contains one or two pieces worth lifting. Maybe a clean isolation of an API call, or a useful test fixture. Extract those into the real codebase via a new, intentional commit; delete the rest.

Resist the urge to "clean up the prototype" into production code. The prototype's shortcuts are now scattered throughout — surgically extracting the gems is usually less work than retroactively making the whole thing production-ready.

### c. Promote the prototype (rare)

Sometimes the prototype really did become the answer. The architecture is right, the shape is sound, the code is mostly OK. In that case:

1. Stop and acknowledge that this is no longer a prototype.
2. Rename the directory, write the tests you skipped, fix the shortcuts you took, add the error handling you elided.
3. Open a normal PR.

This is the rarest of the three outcomes. Be honest with yourself: 80%+ of prototypes should die at the gate.

## 6. Anti-patterns

- **Prototype that quietly ships.** Code with `// TODO: error handling` ends up in production because nobody pressed the decision gate.
- **Prototype that never ends.** No time-box, no question, no termination criterion — it's just an undirected hack session.
- **Prototype that lies.** Fakes the load, fakes the data, fakes the auth flow — answers a question the team didn't ask.
- **Prototype that grew tests.** Tests are an investment; prototypes are disposable. If you're writing tests, you've decided to integrate; do that explicitly.
- **Prototype that's "almost done".** Almost-done is finished. The point was to answer the question; you've answered it. Stop adding features.
