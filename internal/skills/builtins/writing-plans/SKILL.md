---
name: writing-plans
description: Break a complex, multi-file engineering task into a verifiable, ordered plan with file paths and concrete edits before any code is written. Use when the task spans more than 2-3 files, when the user asks for a "plan first" workflow, or when you'd otherwise be tempted to "just start coding" on something non-trivial.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/obra/superpowers/tree/main/skills/writing-plans
---

# Writing plans

A plan is a contract between you and the user about what's going to change before it changes. The user gets the chance to redirect cheaply; you get a checklist to execute against.

The agent already has `/plan` mode for this — use it when the task warrants. This skill is the *content* of a good plan, regardless of which surface you write it on.

## When to write a plan

Write one for:

- Any change touching ≥ 3 files non-trivially.
- Any introduction of a new package, module, or top-level abstraction.
- Any migration (data, schema, dependency, file layout).
- Anything the user described in more than two sentences without sketching the approach themselves.

Skip the plan for:

- Single-file edits.
- Bug fixes with an obvious root cause and ≤ 10 lines of code change.
- Trivial doc / rename / cleanup tasks.
- Anything the user explicitly said "just do it" about.

## Plan structure

Every plan has these sections, in order. Omit only sections that have nothing to say — never invent content to fill them.

### Context
*One paragraph.* Why this change matters; what problem it solves; what prompted it. The future reader (or future you) should be able to recover the motivation without re-asking the user.

### Approach
*One option.* Your recommended approach. Not a survey of alternatives — you already picked. Briefly note trade-offs you considered against (one sentence each, no exhaustive comparison).

### Files to create or modify
A table or list. For each entry: absolute or repo-relative path · the function/method/section that changes · a one-line note on what changes. Cite line numbers when proposing edits to specific spots.

```
/internal/skills/skills.go      — new package, ParseSkillFile + validators
/internal/agent/skill_tool.go   — new SkillTool implementing Tool interface
/internal/tui/run.go:178-298    — register SkillTool after AgentTool registration
```

### Reused functions / utilities
Existing helpers you'll call into, with paths. The point is to surface "I'm going to use the existing `memory.ProjectSlug`" rather than reinvent.

### Steps
A numbered, ordered list of concrete actions. Each step is terse but unambiguous — someone on a fresh session should execute the step without re-investigating. "Add a parser" is not a step; "Add `ParseSkillFile` in `skills.go` mirroring `subagents.ParseAgentFile`" is.

### Verification
Concrete checks: which tests to run, what to build, what a manual smoke looks like, what "done" means. See `verification-before-completion` for the discipline.

### Open questions
*Only* for trivia that doesn't change the plan's shape — naming bikesheds, default values, polish details. If the answer would change which files you touch or what approach you take, that's not an open question — that's a clarification you must resolve *before* writing the plan.

## Resolving material ambiguity

If your investigation surfaces a question whose answer would change the plan itself, **don't write the plan first and ask later**. Surface the question now:

1. State the question(s) as a short, numbered list. Offer your recommended answer where you have one.
2. End the turn. Do not pre-write the plan as if the user already answered.
3. On the user's reply, fold their answers in and write the plan.

Asking after a plan is approved is a UX dead-end — the user has already moved on mentally.

## Quality bar

A plan passes review when:

- A different engineer on a fresh session could execute it without asking follow-up questions.
- Every step lands a verifiable artifact (a file change, a test, a doc).
- The verification section names a concrete check, not "tests pass."
- The "Files to create or modify" list matches the actual diff after implementation. If it doesn't, the plan was wrong; flag the divergence rather than silently drift.

Vague plans get rejected. Padding sections to look thorough makes the plan worse, not better — keep each section as long as it needs to be and no longer.

## Plan vs. exploration

A plan is for execution. If you're not ready to execute — still mapping the codebase, still don't know the shape of the answer — say so. Investigation is its own deliverable; don't fake a plan from incomplete understanding.
