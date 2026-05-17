---
name: diagnose
description: Real-engineer debugging loop — reproduce, minimise, hypothesise, instrument, fix, regression-test. Use when the user reports "X is broken", "this used to work", "I'm getting error Y", or any unexplained failure. Replaces shotgun-debugging with a disciplined cycle.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/mattpocock/skills
---

# Diagnose

Bugs are explanations of the gap between the model in your head and the behavior of the system. Closing the gap goes through six steps, in order. Skipping a step almost always means the fix is in the wrong place.

## 1. Reproduce

Before changing anything, get the failure to happen on demand. A bug you can't reproduce is a bug you can't verify you fixed.

- Capture the **exact command, input, environment, and observation**. "It crashed" is not a reproducer.
- If the failure is intermittent, run it in a loop (`for i in {1..50}; do ...; done`) and record the failure rate.
- If it depends on data, save a snapshot of the problematic input — don't trust live data to stay broken.

If you cannot reproduce in this session, stop and ask the user for the missing input. Do not "fix" code that you can't prove is broken.

## 2. Minimise

Strip away everything that isn't necessary to trigger the failure. The smaller the reproducer, the smaller the hypothesis space.

- Delete unrelated flags / config / environment variables until the failure goes away — the last thing you deleted matters.
- For a failing test: inline the helpers, remove unrelated assertions, hard-code the inputs.
- For a failing request: replay it via `curl`, not the framework that wraps it.
- For a build error: cut the file down to the smallest fragment that still errors.

The endpoint is a reproducer you could paste into an issue. If you can't fit it in 30 lines, keep cutting.

## 3. Hypothesise

State out loud what you think is happening, and what would prove or disprove it.

- "The bug is in the JSON parser because the input has a trailing comma." → check with a trailing-comma-removed input.
- "The bug is a race between A and B." → check by adding a 100ms sleep between them; if the bug goes away, the hypothesis survives.
- "The bug is the cache returning stale data." → check by disabling the cache; if the bug goes away, the hypothesis survives.

Pick one hypothesis at a time. Two changes at once = no signal.

## 4. Instrument

When the hypothesis isn't directly observable, add an observation. Don't guess.

| Question | Instrument |
|---|---|
| "What is this variable at line N?" | Log it. Or `dlv` / `gdb` / a debugger if local. |
| "Which code path runs?" | Log on entry to each branch. |
| "Why is the response slow?" | Time each step; sum to the total. |
| "Where does the data get corrupted?" | Log the value before and after each transformer. |
| "Did this function get called at all?" | Log on entry. |

Print statements are not unprofessional. They are the fastest path to a confirmed hypothesis. Remove them after the fix lands.

## 5. Fix

Once you have a confirmed root cause, apply the minimum change that addresses it.

- **Fix the root cause, not the symptom.** If the bug is "the API returns 500 because a nil pointer dereferences", the fix is "don't dereference nil here", not "wrap the response in a try/catch".
- **Don't bundle unrelated cleanup.** The fix should be readable in one diff hunk.
- **State why the fix works** in the commit message. Not what changed (diff already shows that), but why this change closes the gap.

If the fix would be much bigger than the bug, stop and reconsider — you might be confusing a symptom for the cause.

## 6. Regression-test

Every bug fix ships with a test that fails on the broken version and passes on the fixed version.

- Find the right level: unit if the bug is in one function, integration if it spans modules, end-to-end if it's a user-visible flow.
- The test should mention the bug — issue number, commit, or a sentence — so the future reader knows why the test exists.
- The test goes in the same PR as the fix. Tests landed separately ("we'll add one later") almost never get added.

## When to escalate

Stop and ask the user when:

- You can't reproduce after a reasonable attempt (≤ 5 minutes of agent time on a clear repro).
- Your best hypothesis would require touching code outside the scope you were asked to change.
- The fix would require a schema migration, infra change, or third-party config change — those need human sign-off before code changes.

Sketch the smallest viable next step rather than implementing a sweeping change without alignment.
