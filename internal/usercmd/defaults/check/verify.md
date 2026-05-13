---
description: Verify a change is actually done — build, test, and lint pass + scope matches the ask
argument-hint: '[task-or-hint]'
---
Confirm the current change is genuinely done — not "looks plausible
in the diff." Tests pass, build is clean, and the actual changes
match what was asked.

Optional `$ARGUMENTS` is **mixed-purpose free-form prose**. It can
carry one of, or both of:

- **Task description** ("fix the off-by-one bug", "add OAuth flow")
  — used in step 5 to cross-check the diff against intent.
- **Stack hint or command override** ("swift", "use `cargo make
  verify`", "run scripts/verify.sh") — used in step 2 when
  auto-detection comes up Unknown, OR when the user wants to
  override the detected commands.

Read `$ARGUMENTS` at both steps and extract the signal each step
needs. Leave empty if you don't want either.

1. **Inventory the change.** Identify what's actually different:
   - Uncommitted changes: `git status --porcelain` and `git diff`.
   - Committed changes on this branch (if applicable): resolve the
     base branch the same way `/git:create-pr` does, then
     `git diff <base>...HEAD --stat` for context.
   If both staged/unstaged and committed work exist, evaluate all of
   them together — "done" means done across the whole change.

2. **Detect the project's verification commands** by stack. Run all
   applicable when multiple are detected (polyglot repos). When a
   `Makefile` defines `test` / `check` / `verify`, prefer that over
   auto-detection — Makefiles encode the project's own conventions.

   **First, check `$ARGUMENTS` for an explicit command override or
   stack hint.** If the user wrote something like ``use `cargo make
   verify` ``, `run scripts/verify.sh`, `swift test`, or just the
   bare stack name `swift` / `elixir` / `haskell` — honor it. The
   user knows their project better than we do, and this is the
   single-turn shortcut for repos whose stack isn't in the table
   below. An override wins over auto-detection; a bare stack hint
   resolves the Unknown case below.

   Coverage is scoped to the four target languages (Go, Python,
   Java, Rust) plus Make as the universal fallback. Anything else
   hits the "Unknown" row and either resolves via `$ARGUMENTS` or
   asks the user explicitly — better than guessing at commands for
   stacks we haven't validated.

   | Stack | Detection signal | Commands |
   |---|---|---|
   | **Go** | `go.mod` | `go build ./...`, `go test -count=1 ./...`, plus `go vet ./...` when configured. **`-count=1` is mandatory** — bypasses the Go test cache so results reflect the current code, not a stale earlier run. |
   | **Python** | `pytest.ini`, `pyproject.toml` `[tool.pytest]`, or `tox.ini` (else `tests/` dir as fallback signal) | `pytest` (preferred when configured), else `python -m pytest`. Add `ruff check .` or `flake8` if their config files exist. |
   | **Java (Maven)** | `pom.xml` | `mvn -B verify` (preferred — compiles + tests + plugins), else `mvn -B test`. |
   | **Java/Kotlin (Gradle)** | `build.gradle` or `build.gradle.kts` + `gradlew` | `./gradlew check` (preferred — runs tests + checks), else `./gradlew test`. |
   | **Rust** | `Cargo.toml` | `cargo build`, `cargo test`. Add `cargo clippy -- -D warnings` when `clippy.toml` or `.clippy.toml` exists. |
   | **Make** | top-level `Makefile` with `test`/`check`/`verify` target | Run that target. Polyglot projects often encode their full verification flow here — prefer it when both Make and an auto-detected stack are present. |
   | **Unknown** | none of the above match | Look at `$ARGUMENTS` first — a stack name (e.g., `swift`, `elixir`) or command (e.g., ``use `swift test` ``) resolves this row to a single-turn run. If `$ARGUMENTS` carries neither, stop and ask the user — do not guess at commands for unfamiliar stacks. |

3. **Run the verification commands.** Each one individually. Capture
   stdout + stderr + exit code. Do not parallelize — sequencing makes
   the first failure easy to surface.

4. **If anything failed in step 3, diagnose before reporting.** Do
   NOT label failures "pre-existing," "unrelated to this PR," or any
   similar dismissive phrase without evidence from steps 4a–4c.

   For each failing test or check:

   4a. **Re-run in isolation.** Run just the failing test by name.
       Syntax by stack:

       | Stack | Isolated rerun |
       |---|---|
       | Go | `go test -count=1 -run <TestName> ./<package>/` |
       | Python (pytest) | `pytest <file>::<test>` (or `pytest <file>::<Class>::<method>`) |
       | Java (Maven) | `mvn -Dtest=<Class>#<method> test` |
       | Java/Kotlin (Gradle) | `./gradlew test --tests <Class.method>` |
       | Rust (cargo) | `cargo test <test_name>` |

       For any stack outside this set, mark the diagnosis
       **Inconclusive — isolated rerun syntax not available for this
       stack**. Do not guess at the syntax.

       Two outcomes:
       - Still fails in isolation → **confirmed failure on this
         branch**.
       - Passes in isolation → **flake or test-ordering issue**.
         The test as a unit works; some sibling test leaks state.
         Surface this verdict clearly — DO NOT claim "tests pass"
         just because the isolated rerun was green.

   4b. **Check git history for the failing test file.** Run
       `git log --oneline <base>..HEAD -- <test-file>` (use the
       base branch resolved in step 1). Two outcomes:
       - File was modified in this branch → the failure likely
         relates to your work; investigate directly.
       - File was not modified in this branch → the failure may
         genuinely predate the change, BUT this is only evidence,
         not proof. Code outside the test file could still have
         caused the regression. Treat as "uncertain, needs
         investigation."

   4c. **Form a verdict per failure**, choosing from:
       - **Regression introduced by this branch** — fails in
         isolation AND test file was modified in branch
       - **Possibly pre-existing** — fails in isolation, test file
         not modified in branch (mention you cannot confirm without
         running against base)
       - **Flake / test-ordering** — passes in isolation but fails
         in the full suite
       - **Inconclusive** — diagnosis couldn't run (e.g., the failing
         entity isn't a single named test). Say so explicitly.

5. **Cross-check scope.** If `$ARGUMENTS` was supplied:
   - Does the diff actually do what the ask describes?
   - Does the diff do anything **else** that wasn't asked? Note it.
   - Are there obvious missing pieces (a feature ask with no tests,
     a bug fix with no regression test, a docs claim with no docs
     update)?
   If `$ARGUMENTS` is empty, infer the intent from commit messages
   and apply the same cross-check (lower confidence — flag the
   inference).

6. **Report a verdict.** Plain lines (no markdown code fence). The
   examples below use indentation to mark format, not fences.

       ## Verification

       - Build: ✓ / ✗  (one-line summary, command run)
       - Tests: ✓ / ✗  (one-line: passes/total, time)
       - Lint / vet: ✓ / ✗ / (skipped — not configured)
       - Scope: matches / mismatched / missing-pieces

       ## Failures (if any)

       For each failing test/check, include:
       - Command: <the command you ran>
       - Exit code: <code>
       - Output excerpt: <the salient lines, not the full log>
       - Diagnosis (from step 4):
         - In-isolation rerun: <still fails | passes (flake) | inconclusive>
         - Test file modified in branch: <yes (<N> commits) | no>
         - Verdict: <one of the four categories from 4c>

       ## Verdict

       One of:
       - "Done — safe to commit/push"
       - "Not done — failures above"
       - "Done with caveats — see scope note"
       - "Inconclusive — failures present but diagnosis ambiguous"

**Hard rules:**

- **No `-count=1`-less Go test runs.** Cache-based pass/fail
  reporting is the bug, not the feature.
- **No "pre-existing / unrelated" verdicts without the evidence
  from steps 4a–4b.** If you cannot run the diagnosis steps (e.g.,
  unable to isolate the test by name), say "Inconclusive," not
  "pre-existing."
- Do not invent test counts or coverage numbers — read them from
  the actual tool output.
- If a verification step takes very long (>60s on a clean run),
  say so once in the output but let it finish — partial
  verification is worse than slow verification.
- Each command you run still flows through the normal approval
  gates.
