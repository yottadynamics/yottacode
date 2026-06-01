---
name: brainstorming
description: Pre-plan design interview. Use when the user asks for a feature or change and the right shape isn't obvious yet — before writing-plans, before code. Surfaces requirements, constraints, and trade-offs so the eventual plan rests on a real spec.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/obra/superpowers/tree/main/skills/brainstorming
---

# Brainstorming

The most expensive bugs are designed-in: the user asked for X, you built X, and X turned out to be the wrong thing. Brainstorming is the cheap conversation that closes the gap between *what the user asked for* and *what they actually need* before any code is written.

Use this skill when the task description leaves you with material uncertainty about scope, behavior, or constraints. Skip it for tasks where the spec is already obvious.

## 1. Read the request carefully

Quote the user's request back to yourself in plain language. State the obvious interpretation, then check whether any of these apply:

- **Underspecified** — multiple reasonable interpretations of what "done" looks like.
- **Implicit assumptions** — phrases like "the usual way", "like before", "you know what I mean".
- **Cross-cutting impact** — the change touches systems the user didn't name (auth, data model, public API).
- **Reversibility** — once built, can this be undone cheaply? If not, design matters more.

If none of these apply, you can skip brainstorming entirely. Acknowledge the spec and proceed to `writing-plans` (or straight to code for trivial changes).

## 2. Ask one round of structured questions

If brainstorming is warranted, surface 3-6 questions in one batch. Mix:

- **Scope questions** — "Should this also handle X, or just Y?"
- **Behavior questions** — "When Z happens, what should the user see?"
- **Constraint questions** — "Is there a budget I should know about — time, memory, dependencies, backward-compat?"
- **Audience questions** — "Who is this for — engineers, end users, internal scripts?"
- **Success questions** — "How will we know this is working?"

Each question:

1. Names a real decision (not a yes/no on something already settled).
2. Offers your recommended answer where you have one ("I'd default to X because of Y — does that fit?").
3. Is answerable in one or two sentences.

End the turn. **Do not write a plan yet.** The plan needs the answers; pre-writing it commits you to a shape the user might redirect.

## 3. Probe past "should-want" answers

Some answers sound sophisticated but name no specifics — "make it scalable", "clean architecture", "the best-practice way". These are usually borrowed goals, not the real one. When a key answer comes back like this, ask one follow-up before accepting it:

> If you didn't have to justify this to anyone, what would you actually want?

The honest answer is the spec. One direct probe, then move on — don't stack more abstractions on top.

## 4. Recognize when the user wants to skip

A user who replies "just do it" or "use your judgment" is opting out. Honor that — proceed to `writing-plans` with your best read, and call out the calls you made so they can intercept if you guessed wrong.

A user who answers some questions and ignores others wants forward motion on the answered parts. Don't re-ask the ignored ones; mark them as your-call and proceed.

## 5. Constraints I'll look for even without asking

Before asking, scan for constraints already on the table:

- **Existing code** — what patterns does this codebase follow? A new feature usually fits an existing shape.
- **Repo memory** — `.yottacode/YOTTACODE.md` or `CLAUDE.md` or `AGENTS.md` often spell out the team's conventions.
- **Tests** — what does the test suite say is in scope? A test file with a TODO often hints at the next feature.
- **Recent commits** — `git log --oneline -20` often shows where the user has been working; the new request usually continues that direction.

If a constraint is documented or obvious, don't ask about it. Asking about something the answer is already in the repo costs the user trust.

## 6. The hand-off to plan-writing

Once you have enough to commit to a shape, summarize back to the user in 2-4 lines:

> Got it — I'll build the SSH key rotation flow as a CLI subcommand (`yottacode keys rotate`), generating an ed25519 keypair, pushing the new key, and removing the old one after a verification step. No password fallback. Backward-compat for the existing `~/.ssh/yottacode_rsa` file as a one-time migration. Out of scope: rotating host keys or other users' `authorized_keys`. Sound right?

Always name what you're **not** doing. One out-of-scope line surfaces the silent disagreements about non-goals — they cause half of all misalignments — and it becomes the scope boundary in the eventual plan.

Then either:
- Wait for confirmation ("yes" / "looks right") → hand off to `writing-plans`.
- Adjust on push-back → restate, re-confirm.

This summary is the first checkpoint where the user can catch a misread cheaply. It also doubles as the "Context" section of the eventual plan.

## 7. When to refuse to brainstorm

Some requests are genuinely simple and brainstorming adds friction:

- "Fix the typo on line 42."
- "Rename `foo` to `bar`."
- "Add a logger.Info call when the cache misses."
- "Bump the dep from 1.2.3 to 1.3.0."

For these, acknowledge and act. Brainstorming a typo wastes a turn and erodes the user's trust that you can tell signal from noise.

## 8. Anti-patterns

- **Asking too many questions** — 7+ questions in one batch feels like an interrogation. Cap at 6; ask the rest in round 2 if needed.
- **Asking with no recommendation** — questions like "What should this do?" abdicate. Always offer a default.
- **Asking what you can see** — "What language is this project in?" when you can just look. The user notices.
- **Brainstorming after the user has already answered** — circling back to scope questions mid-implementation re-opens settled ground.
