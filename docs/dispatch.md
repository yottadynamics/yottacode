# Dispatch & integrate (experimental)

`dispatch` fans a batch of independent subtasks out to subagents that run
**concurrently**, then blocks until they all finish and returns their
results together for the main agent to assemble. Write-capable subtasks
each run in their **own git worktree + branch**, so they never clobber each
other or your working tree. `integrate` then merges those branches into a
single integration branch you open a PR from.

This is the building block for "decompose a large PR into smaller
independent tasks, implement them in parallel, assemble the result."

> **Status: experimental beta — opt-in, and we want your feedback.** The
> feature is merged and tested, but the UX and model behavior are still
> settling. Enable it (for the full background experience, turn on both flags):
>
> ```bash
> yottacode --experimental dispatch --experimental background_subagents
> ```
>
> (or `YOTTACODE_EXPERIMENTAL=dispatch,background_subagents`, or set both under
> `[experimental]` in `~/.yottacode/config.toml`.) `dispatch` alone is enough
> for the `dispatch`/`integrate` tools; `background_subagents` additionally
> enables `run_in_background:true` on the standalone `Agent` tool. See
> [experimental.md](experimental.md) for every way to enable.
>
> **Hit a bug or a rough edge?** Please file it on GitHub Issues with the
> **`dispatch-beta`** label — include what you dispatched, what happened, and
> the relevant transcript (open `/subagents`, press Enter on the task). Skim
> the **[Known limitations (beta)](#known-limitations-beta)** at the bottom
> first — a few sharp edges are known and listed there.

## The model

```
dispatch({ goal, tasks:[{subagent_type, description, prompt, files[]}], background? })
   │  validate · classify read-only vs write · overlap-guard
   ├─ write task → own worktree+branch off HEAD; isolated toolset
   ├─ write task → ...                          (run concurrently)
   └─ read task  → shared working dir, no worktree (research only)
   │  each write task is auto-committed to its branch when it finishes
   ▼  BACKGROUND (default for write batches): returns a batch id + branches
      immediately, non-blocking; workers run on; you integrate later
   ▼  FOREGROUND (default for all-read batches): blocks, returns every
      subtask's findings together for you to assemble now
integrate({ branches:[...] })
   │  fresh integration worktree; git merge --no-ff each branch in order
   │  conflict → stops, reports the conflicted files to resolve + resume
   ▼  clean → one branch ready; push it and open a PR
```

## Foreground vs background

`dispatch` runs in one of two modes:

- **Background** (default when the batch has any **write** task): the call
  returns **immediately** with a batch id and the worker branches, and does
  **not** block the main agent — the workers keep implementing in parallel
  in their worktrees. You watch the live dock, then call `integrate` once
  they finish. This is the path for "implement a large PR in parallel
  without tying up the session." Background workers **auto-approve their own
  tool calls within their isolated worktree** (they have no UI to prompt;
  file writes are confined to the worktree, and the review gate is the PR).
- **Foreground** (default for an **all-read / research** batch): the call
  **blocks**, runs the subtasks concurrently, and returns every subtask's
  findings together for the main agent to assemble right away. No worktrees.
  Foreground children forward any approval to your modal.

Override the default with the `background` argument (`true`/`false`). In a
non-interactive (`oneshot`) session there's nowhere to host detached
workers, so background silently falls back to foreground/blocking.

## Partition by files — the key rule

Merges stay clean **by construction** only if no two write subtasks touch
the same file. So each write subtask must declare the files it **owns** via
`files`, and those sets must not overlap. `dispatch` rejects the call up
front if they do (or if a write subtask omits `files`).

- A subtask may **read** any file for context.
- A subtask must only **create/edit** files in its own `files` set.
- Read-only subtasks (e.g. `explore`, `plan`) ignore `files` — they write
  nothing and share the working directory.

If you can't predict file ownership up front, do a read-only `dispatch`
first to map the work, then a write `dispatch` with the partition you
learned.

## Example

```jsonc
dispatch({
  "goal": "add a /health endpoint with config + tests",
  "tasks": [
    { "subagent_type": "implement", "description": "handler",
      "prompt": "Add a GET /health handler returning {status:\"ok\"}.",
      "files": ["internal/api/health.go", "internal/api/routes.go"] },
    { "subagent_type": "implement", "description": "config flag",
      "prompt": "Add a HealthEnabled config flag, default true.",
      "files": ["internal/config/config.go"] },
    { "subagent_type": "test", "description": "tests",
      "prompt": "Add a table test for the /health handler.",
      "files": ["internal/api/health_test.go"] }
  ]
})
```

The `implement` / `test` / `docs` roles are write-capable and
background-by-default — they're built for exactly this fan-out (each owns a
disjoint file set). A common full arc is **`Plan`** (design the split) →
**`[implement, test, docs]`** (build in parallel) → **`review`** +
**`verification`** (read-only critique + adversarial build/test). See
[subagents.md](subagents.md) for the full roster.

This is a write batch, so it runs in the **background**: the call returns
immediately with a batch id and the three worker branches, and the workers
keep implementing in parallel. Watch the live dock; once it shows them done:

```jsonc
integrate({ "branches": [
  "worktree-dispatch-ab12cd34-1",
  "worktree-dispatch-ab12cd34-2",
  "worktree-dispatch-ab12cd34-3"
]})
```

…produces one integration branch with all three changes. Push it and open
a PR (e.g. `/git-create-pr`).

A read-only batch (e.g. three `explore` tasks mapping different subsystems)
runs in the **foreground** instead: the call blocks briefly and returns all
three findings together for you to synthesize immediately — no branches, no
`integrate` step.

## Approvals

`dispatch` itself is just orchestration and needs no approval. A child's own
tool calls are gated deterministically (no LLM judges commands) — how depends
on the mode:

- **Background workers** can't prompt, so they apply a fixed policy instead of
  blanket auto-approval:
  - **File writes/edits** — allowed; the worktree child registry confines them
    to the worker's own worktree, so the blast radius is its branch.
  - **`run_tests`** — allowed, so a worker can verify its change.
  - **`run_bash`** — **disabled** for unattended workers in the beta. The
    "read-only shell" classifier is a first-token check that can be bypassed
    (e.g. `env`/`command` wrappers, process substitution) and `run_bash` isn't
    path-confined once allowed, so auto-allowing it would be an arbitrary-code-
    execution surface for a worker nobody is watching. A task that genuinely
    needs shell must run in the **foreground** (where a human approves each
    call), or use **`run_tests`**. (A token-aware classifier that re-enables
    safe read-only shell is tracked as dispatch-v3 Layer 0.)
  - Everything else (the commit happens via dispatch's own auto-commit).
- **Foreground children forward approvals to your modal** (serialized across
  the batch), so you see and answer each one. Pair with **auto** mode to skip
  per-edit prompts on the path-confined file writes.

**Hardline floor (always on).** A small set of catastrophic commands —
`rm -rf /` / `~` / system dirs, `mkfs`, `dd` to a raw block device, fork
bombs, `shutdown`/`reboot`/`poweroff` — are refused at the `run_bash`
execution chokepoint **unconditionally**, even under `--yolo` or a background
worker. They can't be run through the agent at all; run them yourself in a
real terminal if you genuinely need to. (Mirrors hermes's hardline blocklist
and Claude Code's `rm -rf /` circuit breaker.)

> Note: the hardline floor and the read-only-shell policy are deterministic
> pattern/allowlist checks, not a sandbox. For untrusted work, run yottacode
> itself inside a container or VM.

## Conflicts during integrate

If two branches do touch the same file, `integrate` stops at the first
conflict and reports the conflicted files plus the integration worktree
path. Resolve the conflict there (edit, `git add`, commit the merge), then
call `integrate` again with the **same** `integration_branch` and the
**remaining** branches to continue.

Alternatively, to drop the conflicting branch from this round instead of
resolving it in place, run `git merge --abort` in the integration worktree
and call `integrate` again with the same `integration_branch` and just the
remaining branches — then re-include the dropped branch in a later call once
it's fixed. The conflict report spells out both options.

## Commit reporting

Each write worker is auto-committed to its branch when it finishes, but the
result doesn't assume that always succeeds. A worker's branch state is
derived from the branch itself (`git rev-list base..branch`), so a worker
that committed its own work and left a clean tree is still recognized as
having produced commits. When a worker produces **nothing committable** —
an empty change, a **pre-commit hook / lint rejection**, or an errored run
that left uncommitted work — that's reported with the reason (and the
worktree path for an errored worker), instead of a misleading "no changes".
For a background batch the per-worker commit status (committed SHA, or the
not-committed reason) lands on the dock banner as each worker finishes, and
`integrate` simply skips any branch that ended up empty.

## Watching it run

While subagents run, a **live dock** appears just above the status bar —
one row per running subagent with its branch, latest activity, and elapsed
time. It collapses when nothing is running. After the fact, `/subagents`
lists every task and opens each one's full transcript.

## Limits & notes

- At most 8 subtasks per `dispatch` call (the foreground concurrency cap).
- Write subtasks require a git repository (worktree isolation needs git);
  read-only dispatch works anywhere.
- Not available while in plan mode for write subtasks (plan mode blocks
  writes) — read-only dispatch is fine.
- Child subagents cannot dispatch further (no recursion): `dispatch`,
  `integrate`, and `Agent` are stripped from every child's toolset.
- At most 8 **background** workers run concurrently across the whole
  session — repeated background dispatch calls are rejected once the live
  count would exceed the cap (wait for some to finish, or `/subagents stop`).
- On a clean `integrate`, the merged task worktrees **and** their
  `worktree-dispatch-*` branches are reclaimed automatically (their work is
  safely on the integration branch). Empty skipped branches are removed too;
  a worktree still holding *uncommitted* work is kept and its path reported
  so nothing is discarded. (There's no more "prune later with
  `git_worktree_prune`" step — that was a no-op against live worktrees.)
- Background workers are bound to the session: quitting yottacode cancels
  any still-running workers (and tears down their provider streams) rather
  than leaking them.

## Known limitations (beta)

Sharp edges we know about — read these before filing a `dispatch-beta` issue:

- **The sandbox is not a container.** Worktree write-confinement + the
  deterministic shell floor are guardrails, not isolation. For untrusted or
  high-stakes work, run yottacode itself inside a VM/container.
- **Unattended `run_bash` is disabled.** Background workers can write files
  and `run_tests`, but cannot run arbitrary shell (the read-only classifier is
  bypassable and `run_bash` isn't path-confined). A task that needs shell must
  run in the foreground, where you approve each call. Safe read-only shell for
  background workers returns once the token-aware classifier lands.
- **Worktrees accumulate outside the clean-merge path.** `integrate` reclaims
  what it merges, and empty background worktrees are reclaimed on finish — but
  a foreground write batch, an abandoned (never-integrated) background batch, a
  conflicted integrate, or a crashed session can leave worktrees behind. There
  is no startup garbage-collection yet. Clean them up yourself:

  ```bash
  yottacode worktree list      # see what's there
  yottacode worktree prune     # remove worktrees whose dirs are gone
  yottacode worktree remove <path>   # remove a specific one
  ```

- **The concurrency cap is per session, not per task tree.** The 8-background
  limit is a flat cap; there's no tree-wide budget yet, so deeply nested or
  rapid-fire dispatching is bounded only coarsely.
- **A shutdown mid-commit can leave a stale `index.lock`.** Rare, and the next
  `git` op in that worktree will tell you; clear the lock and retry.

These are tracked for the next iteration in
`yottacode-roadmap/dispatch-v3-collaboration.md` (Layer 0).
