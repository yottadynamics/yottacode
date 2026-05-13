---
description: Verify a change is actually done — build, test, and lint pass + scope matches the ask
argument-hint: '[what-was-asked]'
---
Confirm the current change is genuinely done — not "looks plausible
in the diff." Tests pass, build is clean, and the actual changes
match what was asked.

Optional argument `$ARGUMENTS` is a free-form description of the
original task ("fix the off-by-one in token counting", "add OAuth
flow"). When provided, cross-check the diff against that statement.

1. **Inventory the change.** Identify what's actually different:
   - Uncommitted changes: `git status --porcelain` and `git diff`.
   - Committed changes on this branch (if applicable): resolve the
     base branch the same way `/git:pr-description` does, then
     `git diff <base>...HEAD --stat` for context.
   If both staged/unstaged and committed work exist, evaluate all of
   them together — "done" means done across the whole change.

2. **Detect the project's verification commands** by stack:
   - Go: `go build ./...` and `go test ./...`. Add `go vet ./...`
     when present.
   - Node: pick the lockfile-implied pkg manager (`package-lock.json`
     → npm, `pnpm-lock.yaml` → pnpm, `yarn.lock` → yarn, `bun.lockb`
     → bun). Look up the `test` and `lint` scripts in `package.json`.
   - Rust: `cargo build` and `cargo test`. Add `cargo clippy` when a
     `clippy.toml` or `.clippy.toml` exists.
   - Python: respect `pytest.ini` / `pyproject.toml` `[tool.pytest]`
     / `tox.ini`. Fall back to `python -m pytest` if a `tests/` dir
     exists.
   - Make: if a top-level `Makefile` defines `test`, `check`, or
     `verify`, prefer those — they encode the project's own
     conventions.
   - Unknown: stop and ask the user how the project is verified.
   If multiple stacks coexist (Go + Node, etc.), run each.

3. **Run the verification commands.** Each one individually. Capture
   stdout + stderr + exit code. Do not parallelize — sequencing makes
   the first failure easy to surface.

4. **Cross-check scope.** If `$ARGUMENTS` was supplied:
   - Does the diff actually do what the ask describes?
   - Does the diff do anything **else** that wasn't asked? Note it.
   - Are there obvious missing pieces (a feature ask with no tests,
     a bug fix with no regression test, a docs claim with no docs
     update)?
   If `$ARGUMENTS` is empty, infer the intent from commit messages
   and apply the same cross-check (lower confidence — flag the
   inference).

5. **Report a verdict.** Structure:

   ```
   ## Verification

   - **Build**: ✓ / ✗  (one-line summary, command run)
   - **Tests**: ✓ / ✗  (one-line: passes/total, time)
   - **Lint / vet**: ✓ / ✗ / (skipped — not configured)
   - **Scope**: matches / mismatched / missing-pieces

   ## Failures (if any)

   <per failure: command, exit code, the salient output excerpt,
   and the suspected cause — not a fix>

   ## Verdict

   <one of: "Done — safe to commit/push", "Not done — failures above",
   "Done with caveats — see scope note">
   ```

Do not invent test counts or coverage numbers — read them from the
actual tool output. If a verification step takes very long (>60s on
a clean run), say so once in the output but let it finish — partial
verification is worse than slow verification. Each command you run
still flows through the normal approval gates.
