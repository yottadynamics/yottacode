---
name: test-driven-development
description: Drive new features and bug fixes via the red-green-refactor TDD loop. Use when the user asks to add a feature, fix a bug, or "do this with TDD"; also use proactively when the change touches non-trivial logic that benefits from a regression test.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/obra/superpowers/tree/main/skills/test-driven-development
---

# Test-driven development

TDD is not "tests after the code is written." It is a tight loop that forces you to specify behavior before implementing it. The agent applies this loop when the task is about correctness, not exploration.

## The loop

For every behavior you intend to add or change:

1. **Red** — write the smallest failing test that captures the new behavior. Run it. Confirm it fails for the *right* reason (the assertion fired, not a missing-symbol crash).
2. **Green** — write the *minimum* code that makes the test pass. Not a generalized version. Not a refactor. The most direct path.
3. **Refactor** — with the test green, clean up duplication, naming, and structure. Re-run the test after each change; never refactor with red.

Loop until the feature is complete or the next behavior calls for a new red.

## Picking the next test

The hardest part of TDD is choosing the next failing test. A test is well-chosen when:

- It captures exactly one user-visible behavior, not an implementation detail.
- The implementation needed to pass it is *smaller* than the test description.
- It would fail today and will pass once the feature lands.

Bad first tests:
- `TestNewUser_FieldsAreInitialized` — tests construction, not behavior.
- `TestAddItem` with five assertions — tests several behaviors at once.
- `TestPrivateHelper` — tests an implementation detail; will need to be deleted when the helper is renamed.

Good first tests:
- `TestCart_AddItem_IncreasesTotal` — one observable behavior.
- `TestParseURL_RejectsMissingScheme` — one input → one outcome.
- `TestQueue_DequeueOnEmpty_ReturnsErr` — one error path.

## Anti-patterns to refuse

The agent often slips into these — don't.

| Anti-pattern | Why it's wrong |
|---|---|
| Writing the production code first, then the test | The test is shaped by the code instead of the other way around; you can't tell if the test catches the bug. |
| Skipping the red step ("I know it'll fail") | The test passes the first time you run it — silent false-positive that escapes review. |
| Making the test pass by deleting the assertion | You haven't implemented anything; you've removed the safety net. |
| Implementing five behaviors in one green step | You can't isolate which change broke a regression later. |
| Refactoring while red | You don't know if the refactor broke the test or the missing code did. |
| Mocking the thing you're testing | A mocked-out subject just verifies the mock's behavior, not the real one. |

## When to deviate from strict TDD

TDD is a tool, not a religion. Reasonable deviations:

- **Exploring an unfamiliar API** — write throwaway code first to learn the shape, then start the TDD loop on the actual feature.
- **UI rendering** — start with a snapshot or a visual check; TDD'ing pixel layout is friction without payoff.
- **One-off scripts** — a script run once and discarded does not need tests.
- **Pure config / data files** — there is nothing to assert about correctness beyond schema validation.

For everything else — business logic, parsers, state machines, protocol handlers, anything someone will read in six months — TDD pays back the time you spent.

## When the user is the test author

If the user already wrote a test and asks you to "make it pass":

1. Read the test in full and state what behavior it expects.
2. Run it once unmodified — confirm it fails for the right reason.
3. Implement the minimum code; iterate on red ↔ green.
4. Refactor and re-run.
5. Never "fix" the test to make it pass unless the test is genuinely wrong — in that case, surface the issue out loud before touching the test.
