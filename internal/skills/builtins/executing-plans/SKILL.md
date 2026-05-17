---
name: executing-plans
description: Execute an approved plan step-by-step with checkpoints, batched edits, and visible progress. Use when a plan has been approved and you're about to start implementing, or when the user says "go ahead with the plan", "implement it", or hands you a checklist of steps.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/obra/superpowers/tree/main/skills/executing-plans
---

# Executing plans

Once a plan is approved, the job is to land it without surprises. Surprises mean either (a) the plan was wrong and you're improvising, or (b) the user can no longer predict what's about to happen — both erode trust. This skill is the discipline that keeps execution boring.

## Before you start

1. **Re-read the plan.** The approved version may differ from your memory; the file on disk is authoritative.
2. **Lay out the steps in `todo_write`.** The user sees this as a tracker card; you use it to keep yourself honest. One todo per plan step, in order.
3. **Mark the first step `in_progress`.** Always exactly one in_progress; flip it to completed before starting the next.

If the plan has more than ~6 steps, group related steps into one todo (e.g. "Step 3-5: add the three parser variants"). The todo card is signal, not a transcript.

## Step-by-step rhythm

For each step:

1. **State what you're about to do** in one sentence: "Step 2: add `ParseSkillFile` in `internal/skills/skills.go`." This is the user's last cheap chance to interject.
2. **Make the edits.** Batch related edits in a single tool call when the tool allows; multi-file changes can run as parallel writes when independent.
3. **Run the smallest verification that closes the step.** Compile check, the relevant test, or a manual smoke. *Don't move on if it fails.*
4. **Flip the todo to completed.** Move the next item to in_progress in the same `todo_write` call.

If a step fails verification:

- State *why* it failed in one sentence.
- Decide: fix in place, or back out and revise the plan?
- If revising the plan, say so out loud and update the plan file before continuing.

## Batching and parallelism

Some operations parallelize cleanly:

- **Reads** of independent files — emit one `read_many_files` call instead of N sequential `read_file` calls.
- **Writes** to independent new files — emit them in a single assistant message; the loop runs them concurrently.
- **Subagent dispatches** for independent investigations — emit multiple `Agent` calls in one message.

Don't parallelize:
- Edits that depend on each other (later edit reads earlier edit's output).
- Anything mutating shared state.
- The first write of a new file followed by edits to it — sequential to be safe.

## Checkpoints

After each meaningful step (every file written or test passed), the checkpoint system has captured a pre-image. You can `rollback` if the next step breaks something — that's by design. Don't burn checkpoints unnecessarily, but don't be afraid of them either.

For multi-step changes that touch the same file repeatedly, snapshot the file's *intended end state* in a comment of the plan file before you start editing, so a partial rollback is recoverable.

## Communication during execution

Default to terse:

- After a step succeeds: nothing, or one sentence if the step had a notable result.
- After a verification: state what you ran and what came back.
- After all steps: a one-paragraph summary of what landed, plus the verification command for the whole change.

Avoid:

- Running commentary ("Now I'll run the test…"). The tool call shows that.
- Re-summarizing the plan after each step. The user has the plan.
- Apologizing for routine outcomes. Save apologies for actual mistakes.

## When the plan turns out wrong

The plan was a best-effort guess at execution shape. Sometimes it doesn't survive contact with the code. When that happens:

1. Stop. Don't silently improvise.
2. State what you found and how it diverges from the plan.
3. Propose a *small* amendment — the smallest change to the plan that addresses what you learned.
4. Ask if you should proceed with the amended plan.

The user's tolerance for "I had to deviate" is much higher when you flag it than when you bury it.

## When the plan is done

Run the full verification section from the plan. Not a subset, not just the tests that touch the files you wrote — every check the plan promised.

Report:

- "Plan complete." (Or "Plan complete with caveats: …" if anything is partial.)
- The exact verification commands you ran and their outcomes.
- One sentence summary of the user-visible change.

Then stop. Don't tack on bonus features or refactors that "look like they belong" — the plan was the contract.
