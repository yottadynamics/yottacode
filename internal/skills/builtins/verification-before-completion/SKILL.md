---
name: verification-before-completion
description: Verify every fix or feature with a concrete check before declaring it done. Use when the user asks to fix a bug, ship a feature, or "make sure X works"; also use proactively at the end of any non-trivial task. Prevents the "claimed done, isn't done" failure mode.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/obra/superpowers/tree/main/skills/verification-before-completion
---

# Verification before completion

A task is not done because the agent thinks it's done. It is done when a check the user could re-run independently confirms the new behavior is in place and the old failure mode is gone.

## The verification gate

Before declaring any task complete:

1. **State the success criterion.** Write down, in one sentence, what would now be true that wasn't true before. ("The login form rejects emails without an `@`." "The export endpoint returns 200 with a CSV body for valid input.")
2. **Pick a concrete check.** A failing test that now passes; a manual TUI flow that now behaves; an API call that now returns the right status; a log line that now appears. Not "I read the code and it looks right."
3. **Run the check.** Actually invoke it. Capture the output.
4. **Compare to the criterion.** If the output matches, the task is done. If not, the task is not done; iterate.

## Picking the right check

| Task type | Check |
|---|---|
| Bug fix with a reproducer | Re-run the reproducer; verify the bad behavior is gone |
| Bug fix without a reproducer | Write a regression test that fails on the broken version, passes on the fixed version |
| New feature | Run the new code path end-to-end; assert the visible outcome |
| Refactor | Existing test suite passes; behavior unchanged on a representative input |
| Performance change | Benchmark before vs after on the same input; cite both numbers |
| UI change | Start the dev server; navigate to the affected screen; describe what you saw |
| Doc-only change | Re-read the new doc top-to-bottom; verify links, code samples actually run |

The check must distinguish "this change works" from "this change compiles." Compilation is necessary but not sufficient.

## Ship the regression test with the fix

For every bug fix, the regression test is part of the deliverable, not optional polish. The test should:

- Fail on the broken commit.
- Pass on the fixed commit.
- Carry the issue number, the symptom, and the root cause in a comment so a future reader can recover the context.

If the bug isn't trivially testable (timing, environment, hardware), state out loud why and what you verified instead.

## Refusing the "looks right" reflex

Don't say:
- "This should work."
- "The change looks correct."
- "I've made the fix."
- "Let me know if there are any issues."

Say:
- "I ran X and got Y, which matches the criterion W. Done."
- "I can't run X here; here's what I verified instead: …"
- "X failed with Z; investigating before reporting done."

## When verification is genuinely impossible

Some changes can't be verified by the agent — production-only behavior, third-party API integration without a sandbox, hardware-dependent code. In those cases:

1. State out loud what you can't verify and why.
2. Verify everything adjacent that you *can* — unit tests, type checks, dry-run flags.
3. Give the user a specific check to run themselves, including the command, the expected output, and what to do if it fails.

Never silently skip verification. The user's mental model — "the agent said done, so it's done" — is what makes this skill load-bearing.
