---
name: git-investigation
description: Use `git log`, `git blame`, and `git bisect` to track down when a regression was introduced or why a line of code looks the way it does. Use when the user asks "when did X break?", "who wrote this?", "find the commit that introduced Y", or any historical inquiry about the codebase.
license: MIT
metadata:
  author: yottacode
---

# Git investigation playbook

The repository history is the single best source of truth for "why is this code shaped this way?" and "when did this break?" Don't guess from the code alone — the commit that introduced a change usually explains it in the message.

Read-only git operations are available as dedicated tools:

- `git_log_file` — commit history for a single file (with --follow across renames)
- `git_blame_lines` — per-line authorship for a line range
- `git_show_file_at_rev` — file contents at a specific commit
- `git_diff_files` — diff between two refs
- `git` (unified) — read-only subcommands auto-execute; `log`, `show`, `bisect`, etc.

## 1. Picking the right entry point

| Question | First tool |
|---|---|
| "When did line N change?" | `git_blame_lines path:N..M` |
| "What's the history of this file?" | `git_log_file` |
| "Find the commit that broke X" | `git bisect` — see §3 |
| "Diff between branches" | `git_diff_files` |
| "Show me the file at commit Y" | `git_show_file_at_rev` |
| "Find a commit by content" | `git log -S '<string>'` (pickaxe) |
| "Find a commit by message" | `git log --grep '<pattern>'` |

The pickaxe (`-S`) is the most powerful and most under-used flag. It finds commits where the count of `<string>` in any file changed — perfect for "when did someone add this method call?" or "who deleted this constant?".

## 2. Reading blame correctly

`git blame` shows who last *touched* a line. That's not necessarily who *wrote* the logic — a whitespace fix or a rename can rewrite the blame line.

- `git blame -w` ignores whitespace-only changes.
- `git blame -M` follows code movement within a file.
- `git blame -C` follows code copied/moved across files (slow on big repos).
- `git blame --since=2.years.ago` skips ancient commits.

When the blame points to a "noisy" commit (mass refactor, formatter run), follow the chain backwards: `git log <sha>^ -- <path>` to see what came before, then re-blame at that revision: `git blame <sha>^ -- <path>`.

## 3. Bisect to find a regression

Bisect is the right tool when a test or behavior was working at commit A, is broken at commit B, and the commit set between them is too large to read.

```bash
git bisect start
git bisect bad <known-bad-ref, often HEAD>
git bisect good <known-good-ref>
# git checks out the midpoint; run your test
go test ./... -run TestX
# if it fails:
git bisect bad
# if it passes:
git bisect good
# repeat until git reports "<sha> is the first bad commit"
git bisect reset
```

Automate when you can:

```bash
git bisect run go test ./... -run TestX
```

`git bisect run` returns when the first bad commit is found; the script's exit code drives the verdict (0 = good, non-zero = bad, 125 = skip).

Edge cases:
- Tests that flake will lie to bisect. Verify the test is deterministic at HEAD and at the known-good ref before starting.
- Builds that don't compile at intermediate commits should `exit 125` so bisect skips them rather than marking them bad.

## 4. Reading log effectively

The default `git log` is too verbose for an agent to consume. Always shape it:

```bash
git log --oneline -20                                  # recent commits, terse
git log --oneline --grep='auth'                        # by message
git log --oneline -S 'oldFunctionName'                 # pickaxe
git log --oneline --since=1.month --author=alice       # by date + author
git log --follow -p -- path/to/file                    # full history of one file, with patches
git log <branch>..HEAD --oneline                       # commits on HEAD not in <branch>
git log -L :functionName:path/to/file                  # history of a single function (heuristic, language-aware)
```

`-L :functionName:path` is magic when it works — it tracks one function across renames and refactors. Worth trying before reaching for blame.

## 5. Reporting findings

When you find the commit that introduced an issue:

1. State the SHA (full or short — full is unambiguous, short is readable).
2. Quote the commit message subject.
3. Describe what the commit changed (one sentence).
4. If the commit fixed something else, say what — and check the message + PR for context on the trade-off.
5. Note related commits (revert-of, follow-up fix, original landing).

Example:

> The break came in `a1b2c3d` ("auth: normalize email casing on lookup", 2025-08-14). It lower-cases the email in the `WHERE` clause, which doesn't match the historical mixed-case rows. The original code is from `9f8e7d6` (2023-01-04). A partial fix landed in `4d5e6f7` (2025-08-20) but didn't backfill the table.

Never say "git blame says X" — translate the SHA into the human story behind the change.

## 6. Never

- Run mutating git commands (`commit`, `reset`, `push`, `rebase`) during an investigation. Those are separate tasks; ask first.
- Trust blame as authorship without `-w` and a follow-up check on noisy refactors.
- Conclude "this never worked" from one blame line — check the actual landing commit and surrounding tests.
