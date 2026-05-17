---
name: handoff
description: Generate a concise handoff document that a fresh agent (or the same agent in a new session) can use to pick up where the current one left off. Use when the user asks to "summarize where we are", "make a handoff", "compact context before /clear", or any session-transition request.
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/mattpocock/skills/tree/main/skills/productivity/handoff
---

# Session handoff

A handoff is not a transcript. It's a *recovery document*: enough state for a fresh agent to resume the work productively, without forcing them to re-derive the context from scratch.

Use this skill when the current session has accumulated context that a `/clear` or session-end would discard, AND the work isn't done yet. For genuinely-finished work, write the verification summary (per `verification-before-completion`) and stop — no handoff needed.

## 1. What goes in a handoff

The minimum useful set, in order:

### a. Goal — one paragraph

What is this session trying to accomplish? Not the original prompt verbatim — the *current* goal after any refinements. Include any scope decisions the user made along the way.

### b. State — what's done, what's not

A short status. Three buckets:

- **Done** — work that's landed (files written, tests passing, PR opened).
- **In progress** — work the next session needs to continue.
- **Not started** — known remaining work.

Cite file paths, not just descriptions. "Wrote the parser" is less useful than "Wrote the parser in `internal/skills/skills.go:80-170`."

### c. Decisions

Non-obvious choices the user made or the agent made-and-confirmed. Examples:

- "We're using a slice, not a map, because order matters."
- "Disabled retry on idempotent failures because they masked a real bug."
- "Skipped Windows support — not in scope per user."

These prevent the next agent from re-litigating settled ground.

### d. Open questions

Things the user has not answered (or has not yet been asked). Each item:

- The question.
- Why it matters (which decisions depend on it).
- Your current recommended answer, if you have one.

### e. Next concrete step

The single most useful next action for the resuming agent. Should be specific enough to execute without re-investigating:

> Run `go test ./internal/skills/... -run TestLoadAll` to confirm precedence test passes, then move to wiring `findSlash` extension in `internal/tui/commands.go:165`.

### f. References

A short list of the load-bearing artifacts:

- The plan file (if one exists).
- The active TODO list.
- The PR or branch.
- Any external docs the user pointed at.

Each as a path or URL the next agent can open directly.

## 2. What to leave out

- **Conversation history.** The next agent doesn't need to know which questions you asked first. They need the answers.
- **Tool-call traces.** No "I ran grep, then read X, then..." — that's noise.
- **Self-reflection.** Save apologies and meta-commentary for the human.
- **Speculation.** Don't invent state that wasn't established. If the answer to "is X working?" is "I don't know", say so explicitly.

A handoff that's 80% noise and 20% signal is worse than a handoff that's 100% signal and short.

## 3. Format

Markdown, ~300-800 words total. Sections in the order above. Keep each section terse.

Surface the handoff in one of three ways depending on the user's intent:

- **In the chat reply** — for "summarize where we are" requests where the user wants to see it now.
- **Written to a file** — `.yottacode/handoff.md` or wherever the user names. Suggest this for `/clear` workflows where the next session reads the file at startup.
- **Folded into `YOTTACODE.md`** — for state that should persist as project context, not session context. Use sparingly; YOTTACODE.md is for facts about the project, not in-flight work.

State explicitly which surface you used.

## 4. The "fresh agent reads this cold" test

Before you call the handoff done, imagine a fresh agent reading it with no context — no memory, no transcript, no prior session. Can they:

1. State what they're being asked to work on?
2. Pick the next concrete action without asking the user a question?
3. Avoid touching code that's already settled?

If any answer is no, the handoff is incomplete. Most often the gap is missing file paths, missing decisions, or a vague "next step".

## 5. When the work is genuinely done

Don't write a handoff for finished work. Write the completion summary per `verification-before-completion`:

- What changed.
- What verification was run and what it returned.
- What's still untested or out of scope.

A handoff for finished work signals "I don't believe this is done", which erodes trust if the work *is* done.

## 6. Anti-patterns

- **Handoff as transcript** — re-narrating the session. Costs words, adds no signal.
- **Handoff without next step** — leaves the next agent at the same uncertainty the user wanted to escape.
- **Handoff that hides open questions** — burying them mid-paragraph instead of in their own section means the next agent misses them.
- **Stale handoff** — written hours before /clear, no longer accurate. Generate fresh; don't reuse.
- **Handoff written as if to the user** — the audience is the next agent, not the human. Phrase accordingly.
