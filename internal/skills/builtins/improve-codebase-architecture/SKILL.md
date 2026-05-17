---
name: improve-codebase-architecture
description: Review and improve the architectural shape of a module, package, or service — coupling, cohesion, module boundaries, dependency direction, deletion testing. Use when the user asks "is this code well-structured?", "should I split this module?", or "what's wrong with this architecture?".
license: MIT
metadata:
  author: yottacode
  inspired-by: https://github.com/mattpocock/skills/tree/main/skills/engineering/improve-codebase-architecture
---

# Improve codebase architecture

Architectural review is a different lens than code review. A function can be well-named, well-tested, and well-typed, and still sit in the wrong module, depend on the wrong things, or carry hidden coupling that bites in six months. Architectural review asks: **does this code's shape match its job?**

Apply this when the question is about *structure*, not *correctness*. For a security review use `security-auditor`; for a fix use `diagnose`.

## 1. Frame the unit of review

Architecture is fractal. Be explicit about which level you're reviewing:

- **Package / module** — the public surface, the boundaries, the inbound/outbound deps.
- **Service** — its responsibilities, what other services it talks to, where its data lives.
- **Repository** — top-level layout, what each top dir means, where shared code lives.
- **Function group** — a cluster of functions that compose into one operation.

State which level the user is asking about. If they didn't say, ask once before reviewing.

## 2. The four shape questions

For the chosen unit, answer these in order:

### a. What is this responsible for?

State the unit's job in one sentence. If the sentence needs an "and" between two unrelated concerns, the unit has more than one job. Examples:

- "Parses TOML config files." — one job.
- "Parses TOML config files and applies environment-variable overrides." — two jobs, candidate for split.
- "Handles HTTP routing, request validation, business logic, and database access." — four jobs, definitely too much.

### b. What does it depend on?

List the inbound and outbound dependencies. Look for:

- **Wrong-direction deps** — does a low-level utility import a high-level orchestrator? That's a layering inversion.
- **Cyclic deps** — does A import B which imports A? Cycles signal that the split is wrong.
- **Surprise deps** — does a "pure" module import a logger / database / time source? Those are testability sinks.

### c. What's its public surface?

Look at what the unit exports. Apply the **deletion test**: for each exported symbol, ask "if I deleted this, what would break?"

- If **nothing** would break, the export is dead. Delete it.
- If **only the tests** would break, the symbol exists to satisfy tests, not to serve a real consumer. Consider making the test use the real surface instead.
- If **one consumer in this repo** uses it, consider whether the consumer should reach for a different abstraction.
- If **many consumers** use it, the export is load-bearing and the next questions matter more.

A unit with a 30-symbol public surface is almost always carrying dead exports.

### d. How deep is the unit?

A **deep module** has a narrow public surface and a lot of implementation behind it. A **shallow module** is mostly interface — pass-through code where every internal function maps 1:1 to an exported one. Shallow modules don't pay rent: they add a layer without adding abstraction.

Examples:

- `crypto/aes.NewCipher` returning a `cipher.Block` — deep. Tiny surface, huge implementation.
- `package userutil` with 17 exported `Get*` and `Set*` functions that each do one DB call — shallow. The package is a directory, not an abstraction.

Aim for deeper modules. If you can't, the layer probably shouldn't exist as its own unit.

## 3. Coupling and cohesion checks

For each module in scope:

- **Cohesion**: do the things inside the module belong together? If you can group the contents into two clusters that barely touch each other, you have two modules pretending to be one.
- **Coupling**: how much does this module need to know about other modules to function? A module that imports five packages and threads three context structs through every function is over-coupled.

The ideal is **high cohesion, low coupling**. Both are necessary; one is not enough.

## 4. Boundaries to flag

These are the patterns that almost always mean rework is needed:

- A module's tests reach into another module's internals. The boundary is leaky.
- A struct has both data fields and method receivers that mutate "private" fields via reflection or unexported helpers passed through a function pointer. The encapsulation is theater.
- Two modules round-trip data through a shared third (often called "types" or "common") so they don't have to import each other. The third is hiding a cycle.
- A "manager" or "service" or "helper" module exists with no clearer name. Anonymity is a smell; if it has a real job, it can have a real name.

## 5. The output

For an architectural review, give the user:

1. **Unit summary** — what's being reviewed.
2. **One-sentence responsibility** — your read of its job.
3. **Findings** — grouped as Critical / Strong / Nit, the way `security-auditor` and `dockerfile-review` do.
4. **Refactor plan** — if changes are warranted, a short ordered list. Each item names the unit and the action.
5. **Open questions** — anything you couldn't answer from reading the code that the user needs to decide.

## 6. When not to refactor

Some pathologies aren't worth fixing:

- The unit is being deleted soon. Don't refactor a module that's getting retired in 30 days.
- The fix is bigger than the bug. If straightening out the module costs 2 weeks and the current shape costs 4 hours/year in pain, leave it.
- The team doesn't share your conclusion. Refactors that one person thinks are obvious and others find baffling lose more than they gain.

Surface these explicitly rather than refactoring anyway.

## 7. Anti-patterns

- **Refactoring without a stated goal.** "Make it cleaner" is not a goal. "Reduce the public surface from 17 to 4 symbols" is.
- **Adding indirection in the name of decoupling.** A pointless interface is worse than a direct dependency.
- **Splitting before the seam is clear.** Two modules pretending to be one is bad; three modules with messy seams is worse. Split when the boundary becomes obvious; not before.
- **Bundling refactor with behavior change.** Ship one or the other in a given PR; mixing them makes review impossible.
