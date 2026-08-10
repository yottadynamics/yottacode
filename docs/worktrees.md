# Worktrees — parallel yottacode sessions

Worktrees let two or more yottacode sessions edit the same repository
in parallel without colliding. Each session lives in a separate
directory under `~/.yottacode/worktrees/<repo-slug>/<name>/` on its own
branch, so the agent's writes in one session are invisible to the
read tools of another. Keeping the worktrees outside the repo means
they never show up in `ls`, never get walked by IDE indexers / `find`
/ `grep`, and don't depend on `.gitignore` discipline.

If you want the agent to chew through a big feature autonomously,
the recommended pattern is **plan in a worktree, then run auto
mode inside it** — three stacked safety layers that bound the blast
radius of any mistake. See [Canonical safe-autonomy
workflow](#canonical-safe-autonomy-workflow) below.

## Quickstart

```bash
# Launch the TUI inside a fresh worktree
yottacode --worktree feature-auth

# Same, but let yottacode generate the name (e.g. bright-running-fox)
yottacode --worktree

# Short flag
yottacode -w feature-auth

# Oneshot
yottacode run --worktree feature-auth "list failing tests"

# Admin from any directory inside the repo
yottacode worktree list
yottacode worktree status feature-auth
yottacode worktree remove feature-auth
yottacode worktree prune
```

Each worktree materializes at `~/.yottacode/worktrees/<repo-slug>/<name>/`
on branch `worktree-<name>`. The `<repo-slug>` is `<repo-basename>-<8-char-hash>`
(e.g. `yottacode-1a2b3c4d`), derived deterministically from the absolute
repo path so two repos with the same basename never collide. Each
yottacode worktree session uses `git worktree add` under the hood —
git stores its administrative data in the originating repo's
`.git/worktrees/<name>/` regardless of where the actual working tree
lives.

## Prerequisites

- **Workspace trust** — `--worktree` refuses on untrusted repos with
  a one-line hint: run `yottacode` once in the repo to accept the
  trust prompt, then re-run with `--worktree`. Worktree sessions
  resolve trust back to the originating repo via `git rev-parse
  --git-common-dir`; the worktree dir itself is never added to the
  persistent trust set. See
  [security-and-allow-lists.md](security-and-allow-lists.md).
- **Git ≥ 2.5** — `git worktree` is a standard git subcommand
  available in every modern git release.

## `.worktreeinclude` — bring `.env` along

A fresh git worktree starts empty of every gitignored file. That's
fine for a clean cut, but most projects rely on `.env` /
`.vscode/settings.json` / `node_modules/` / `.local-config` to
actually run. `.worktreeinclude` is a per-repo file (gitignore-style
syntax) that lists which gitignored files yottacode should copy
into each new worktree at creation time.

```
# <repo>/.worktreeinclude
.env
.env.*
.vscode/settings.json
**/.config.local
```

Lines starting with `#` are comments; blank lines are skipped.
Patterns match against repo-relative paths using `filepath.Match`
semantics, with `**` as a wildcard that spans zero or more path
segments. Missing files are silently skipped; unreadable files
return an error so you can fix permissions instead of silently
shipping a broken worktree. Symlinks (and other non-regular files
like devices or FIFOs) are skipped, not followed — a matched symlink
won't copy its target's bytes into the worktree.

Commit `.worktreeinclude` to the repo so every contributor's
worktrees inherit the same setup.

## Agent surface

The agent reaches the same machinery through three tools, all of
which require approval — `enter_worktree` and `exit_worktree` are
in the **auto-mode safety floor** (they prompt even when auto mode
is on, because they shift the agent's working context):

| Tool                | What it does                                                                  |
|---------------------|-------------------------------------------------------------------------------|
| `enter_worktree`    | Create or attach to a yottacode worktree. Args: `name?`, `base?` (`fresh`\|`head`). Swaps the session cwd to the new worktree so subsequent relative paths resolve inside it. |
| `exit_worktree`     | Leave a worktree. Args: `name?` (infers from cwd), `cleanup?` (`auto`\|`keep`\|`remove`). Auto cleanup removes clean trees and keeps dirty ones. Swaps cwd back to the originating repo root. |
| `worktree_status`   | Read-only peek: per-worktree clean/dirty state with reason chips. Useful when juggling several at once. |

Plus the thin `git_worktree_{list,add,remove,lock,unlock,prune}`
wrappers for finer-grained admin. The list / status tools are
read-only and never prompt.

## Canonical safe-autonomy workflow

The combination yottacode is designed around — plan inside a
worktree, then run autonomously in auto mode, with the worktree
disposable on the way out:

```
1. yottacode --worktree feature-x   # cwd = ~/.yottacode/worktrees/<slug>/feature-x/, branch = worktree-feature-x
                                    # (or: agent calls enter_worktree, you approve once)
2. /plan                            # still in worktree, read-only gate on
                                    # (or: ask the agent to plan — it calls enter_plan_mode, you approve)
3. (write plan)                     # plan file lands in ~/.yottacode/plans/
4. exit_plan_mode → press [A]       # still in worktree; plan approved, auto mode active
5. (agent executes the plan)        # edits land inside the worktree only
6. agent calls exit_worktree        # clean: auto-remove; dirty: keep/remove prompt
```

Three independent safety layers stacked:

1. **Filesystem isolation** (worktree) — the agent can't touch your
   main checkout while it's working in the worktree.
2. **Read-only planning** (`/plan`) — no risk of damage while
   designing. The agent writes a plan, not code.
3. **Bounded blast radius** (auto mode inside a worktree) — if the
   agent goes off the rails, `exit_worktree` with
   `cleanup="remove"` discards everything; your main checkout is
   untouched.

**Persistence:** worktree state is filesystem + git; mode is a
permission policy. They're orthogonal — a mode flip never touches
the worktree. Cycling plan/auto/yolo/normal with `Shift+Tab` (or
`/plan`, `/auto`, `/yolo`) while you're inside a worktree leaves you in the same
worktree, on the same branch, with the same uncommitted state.
Concurrent yottacode in two worktrees of the same repo is safe —
distinct cwds, distinct session records.

## Cleanup behavior

| Context                            | Clean tree (no uncommitted, untracked, or unpushed work)  | Dirty tree                                        |
|------------------------------------|----------------------------------------------------------|---------------------------------------------------|
| TUI session, `exit_worktree`       | Auto-removes worktree + branch                            | Keeps the worktree, returns "use cleanup=remove" hint |
| TUI session, `exit_worktree cleanup=remove` | Removes                                                   | Force-removes (destructive)                       |
| Oneshot / `--print`                | No auto-cleanup. Run `yottacode worktree remove <name>` afterward. | Same.                                             |

Branches that follow our `worktree-<name>` naming are deleted along
with the worktree; user-named branches are left alone.

## Permission-mode interactions

| Action                                | Plan mode   | Auto mode                          | Normal     | `--yolo` |
|---------------------------------------|-------------|------------------------------------|------------|----------------------------------|
| `enter_worktree`                      | blocked     | **prompts** (safety floor)         | prompts    | bypassed                         |
| `exit_worktree` (clean)               | blocked     | **prompts** (safety floor) → remove| prompts    | auto-remove                      |
| `exit_worktree` (dirty)               | blocked     | **prompts**; keep/remove sub-prompt fires too | prompts twice | keep/remove sub-prompt           |
| `git_worktree_list` / `worktree_status` | allow (read-only) | allow                       | allow      | allow                            |
| `git_worktree_{add,remove,lock,unlock,prune}` | blocked | auto-allow                        | prompts    | bypassed                         |
| Edits inside the worktree cwd         | blocked     | auto-allow (cwd-confined)          | prompts    | bypassed                         |

`enter_worktree` and `exit_worktree` are in the auto-mode safety
floor (alongside `run_bash`, `git_commit`, `git_checkpoint`, and
`rollback`). The plain `git_worktree_*` wrappers stay auto-allowed
in auto mode because they're narrow and explicit. Use
`[A]-always` on the safety-floor prompt once if you want the agent
to spin worktrees freely in the rest of the session.

## Base ref — `fresh` vs `head`

When the agent calls `enter_worktree`, the `base` argument controls
which commit the new branch starts from:

- `fresh` (default) — branch from `origin/HEAD` when reachable,
  otherwise local `HEAD`. No local-only commits travel with the
  worktree. Best for "give the agent a clean room."
- `head` — branch from the current local `HEAD`. Unpushed work
  follows the worktree. Best for "carry my in-progress branch into
  isolation."

The CLI flag (`yottacode --worktree foo`) always uses the `fresh`
default; the agent tool exposes the choice.

## Sessions and resume

`yottacode sessions list` shows the worktree column for each
session, and `yottacode sessions resume <id>` lands you back in the
session's recorded cwd — i.e. inside the right worktree. Sessions
that ran in the main checkout show `worktree=—`.

## What's not in v1

- **PR-fetch** (`--worktree "#1234"`) — Claude accepts this; we'll
  add it once the typed go-github client surfaces the helper.
- **`WorktreeCreate` / `WorktreeRemove` hooks** for non-git VCS
  (svn / hg / perforce) — git-first for v1.
- **Subagent isolation** (`isolation: worktree` frontmatter on
  custom subagents) — depends on custom-subagent stability; will
  follow.
- **Crash-sweep at startup** of orphan `_subagent-*` worktrees —
  paired with subagent isolation.

## Migrating from in-repo worktrees

If you used a pre-relocation build that materialized worktrees at
`<repo>/.yottacode/worktrees/<name>/`, those directories still exist
on disk but yottacode no longer addresses them. Two cleanup options:

1. **Throw them away** — they were ephemeral anyway. From the repo:

   ```bash
   # Inspect the leftover branches (worktree-* naming convention)
   git branch | grep '^  worktree-'
   # Force-remove the working trees, then delete the branches
   git worktree remove --force .yottacode/worktrees/<name>
   git branch -D worktree-<name>
   ```

2. **Promote them to the new location** — if you have uncommitted
   work in an old worktree, move the changes into a fresh worktree:

   ```bash
   # In the new location, after relocation
   yottacode --worktree <name>
   # Then copy / merge from the old <repo>/.yottacode/worktrees/<name>/
   # by hand, commit, and `git worktree remove` the old one.
   ```

No automatic migration tool ships with the relocation — the cut-over
is intentional, mirroring how procedural migrations are handled
elsewhere in yottacode (no coexistence window).

## Troubleshooting

See [troubleshooting.md](troubleshooting.md) for "worktree exits
with trust error" and "worktree from `--print` not cleaned up."
