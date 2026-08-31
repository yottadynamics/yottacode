# Dispatch & integrate (experimental)

`dispatch` fans a batch of independent subtasks out to subagents that run
**concurrently**. Write-capable subtasks run in the background by default, each
in its **own git worktree + branch**, while all-read batches wait for the
subtasks and return results together for the main agent to assemble. `integrate`
then merges worker branches into a single integration branch you open a PR from.


This is the building block for "decompose a large PR into smaller
independent tasks, implement them in parallel, assemble the result."

> **Status: experimental.** Enable with `--experimental dispatch`,
> `YOTTACODE_EXPERIMENTAL=dispatch`, or `[experimental] dispatch = true` in
> config. Background subagents themselves are GA in the interactive TUI, but
> dispatch/integrate remains opt-in while the decomposition and integration UX
> settles.


## The model

```
dispatch({ goal, tasks:[{subagent_type, description, prompt, files[]}], background? })
   │  validate · classify read-only vs write · overlap-guard
   ├─ write task → own worktree+branch off HEAD; isolated toolset
   ├─ write task → ...                          (run concurrently)
   └─ read task  → shared working dir, no worktree (research only)
   │  each write task is auto-committed to its branch when it finishes
   ▼  BACKGROUND (default for write batches): returns a batch id + branches
      immediately; workers run on; you integrate later
   ▼  FOREGROUND (default for all-read batches): waits, returns every
      subtask's findings together for you to assemble now
integrate({ branches:[...] })
   │  fresh integration worktree; git merge --no-ff each branch in order
   │  conflict → stops, reports the conflicted files to resolve + resume
   ▼  clean → one branch ready; push it and open a PR
```

## Foreground vs background

`dispatch` runs in one of two modes:

- **Background** (default when the batch has any **write** task): the call
  returns **immediately** with a batch id and the worker branches. The main agent
  can continue while workers keep implementing in parallel
  in their worktrees. You watch the live dock; when the **last** worker in the
  batch finishes, the agent is re-prompted automatically with every worker's
  result in one turn, so it can go straight to `integrate` without you having
  to nudge it. This is the path for "implement a large PR in parallel
  without tying up the session." Background workers apply a fixed unattended
  policy: owned-file writes are auto-approved; `run_tests`, shell, and other
  approval-requiring tools are denied because tests/shell execute code and
  there is no UI to prompt.
- **Foreground** (default for an **all-read / research** batch): the call
  waits for the subtasks, runs them concurrently, and returns every subtask's
  findings together for the main agent to assemble right away. No worktrees.
  Foreground children forward any approval to your modal.


Override the default with the `background` argument (`true`/`false`). In a
non-interactive (`oneshot`) session there's nowhere to host detached
workers, so background silently falls back to foreground/waiting mode.

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
background-capable in dispatch — they're built for exactly this fan-out (each owns a
disjoint file set). A common full arc is **`Plan`** (design the split) →
**`[implement, test, docs]`** (build in parallel) → **`review`** +
**`verification`** (read-only critique + adversarial build/test). See
[subagents.md](subagents.md) for the full roster.

This is a write batch, so it runs in the **background**: the call returns
immediately with a batch id and the three worker branches, and the workers
keep implementing in parallel. Watch the live dock; once it shows them done,
call `integrate` with only the branches the dock reported as committed:

```jsonc
integrate({ "branches": [
  "worktree-dispatch-ab12cd34-1",
  "worktree-dispatch-ab12cd34-3"
]})
```

…produces one integration branch with the committed worker changes. Push it and open
a PR (e.g. `/git-create-pr`).

A read-only batch (e.g. three `explore` tasks mapping different subsystems)
runs in the **foreground** instead: the call waits briefly and returns all
three findings together for you to synthesize immediately — no branches, no
`integrate` step.

## Approvals

`dispatch` itself is just orchestration and needs no approval. A child's own
tool calls are gated deterministically (no LLM judges commands) — how depends
on the mode:

- **Background workers** can't prompt, so they apply a fixed policy instead of
  blanket auto-approval:
  - **File writes/edits** — allowed only inside the worker's own worktree and
    declared `files` ownership, so the blast radius is its branch and its file
    partition.
  - **`run_tests` and `run_bash`** — **disabled for unattended workers running
    on the host.** Tests execute arbitrary project code (and inherit
    environment such as `GOFLAGS`); the "read-only shell" classifier is a
    first-token check that can be bypassed (e.g. `env`/`command` wrappers,
    process substitution) and `run_bash` isn't path-confined once allowed —
    so auto-allowing either would be an arbitrary-code-execution surface for
    a worker nobody is watching. **Allowed when this worker is sandboxed**
    (the parent session has the [command sandbox](sandbox.md) enabled —
    `[sandbox] backend = "podman"`): each write worker gets its own
    container mounted at its own worktree, so the blast radius is the
    container, the same bet worktree-confined file writes already make.
    Without a sandbox, a task that genuinely needs shell/tests must run in
    the **foreground** (where a human approves each call).
  - Other approval-requiring tools (git mutations, GitHub writes, etc.) are
    denied; the commit happens via dispatch's own auto-commit.
- **Foreground children forward approvals to your modal** (serialized across
  the batch), so you see and answer each one. Pair with **auto** mode to skip
  per-edit prompts on the path-confined file writes.


**Hardline floor (always on).** A small set of catastrophic commands —
`rm -rf /` / `~` / system dirs, `mkfs`, `dd` to a raw block device, fork
bombs, `shutdown`/`reboot`/`poweroff` — are refused at the `run_bash`
execution chokepoint **unconditionally**, even under `--yolo` or a background
worker. They can't be run through the agent at all; run them yourself in a
real terminal if you genuinely need to.

> Note: the hardline floor is a deterministic pattern/allowlist check, not a
> sandbox — it stays on even for sandboxed workers, because a blocked command
> could still destroy the mounted worktree. Without the command sandbox
> enabled, dispatch workers have no container isolation at all; for untrusted
> work, run yottacode itself inside a container or VM.

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
`integrate` fails fast for missing branch names; pass only branches that the dock
or foreground result reported as committed.

## Watching it run

While subagents run, a **live dock** appears just above the status bar —
one row per running subagent with its branch, latest activity, and elapsed
time. It collapses when nothing is running. After the fact, `/subagents`
lists every task and opens each one's full transcript.

A background batch also wakes the agent on its own once **every** worker in the
batch has finished — one wake turn carrying all of their results, not one per
worker. Until then, each completion just paints its banner. So a batch that
looks stalled in the dock is genuinely still running; you don't need to prompt
it to move on to `integrate`.

## Limits & notes

- At most 8 subtasks per `dispatch` call (the foreground concurrency cap).
- Dispatch spends against the session-wide subagent token budget
  (`[subagents] session_token_budget` in `~/.yottacode/config.toml`) and is
  refused once that budget is exhausted — before any worktree is created. A
  batch is N child loops at once, so this is the backstop that bounds a
  runaway fan-out; raise the budget or finish without delegating.
- Write subtasks require a git repository (worktree isolation needs git);
  read-only dispatch works anywhere.
- Not available while in plan mode for write subtasks (plan mode blocks
  writes) — read-only dispatch is fine.
- Child subagents cannot dispatch further (no recursion): `dispatch`,
  `integrate`, and `Agent` are stripped from every child's toolset.
- At most 8 **background** workers run concurrently across the whole
  session — repeated background dispatch calls are rejected once the live
  count would exceed the cap (wait for some to finish, or `/subagents stop`).
- Stop a whole batch with `/subagents stop batch <batch-id>` (the id on the
  dock header); `/subagents stop <id-prefix>` still stops one worker.
- Every worker reclaims its own worktree+branch the moment it finishes if
  they hold nothing — no commits beyond the dispatch base and a clean tree —
  whatever the outcome (completed, errored, canceled, iter-capped) and in
  both foreground and background batches. Worktrees with commits are kept
  for `integrate`; one still holding *uncommitted* work is kept and its
  path reported so partial output is never discarded.
- On a clean `integrate`, the merged task worktrees **and** their
  `worktree-dispatch-*` branches are reclaimed automatically, including after
  conflict resolution resumes (their work is safely on the integration branch).
  Missing branch names fail fast, so omit workers reported as empty/reclaimed or
  NOT committed when you call `integrate`.
  (There's no more "prune later with `git_worktree_prune`" step — that was a
  no-op against live worktrees.)
- Background workers are bound to the session: quitting yottacode cancels
  any still-running workers (and tears down their provider streams) rather
  than leaking them, then sweeps the session's dispatch worktrees one last
  time so workers the bounded drain gave up on don't leak empty worktrees
  either. The sweep keeps committed and dirty worktrees, same as above.
- When the parent session has the [command sandbox](sandbox.md)
  enabled (`[sandbox] backend = "podman"`), every write worker gets its
  **own** container mounted at its own worktree — inherited automatically,
  no separate dispatch-level opt-in. This also unlocks unattended `run_bash`
  and `run_tests` for **background** write workers (see Approvals above) —
  without a sandbox those stay denied. Container resource cost scales with
  the number of concurrent write workers, but each one is capped by the
  same `memory`/`cpus`/`pids_limit` as the parent session's container, so
  it's bounded per worker rather than unbounded. Read-only workers don't
  get their own container — they share the parent's toolset (and its
  sandbox, if any) directly, which means their `run_bash` calls compete
  for that ONE container's resource caps alongside the parent session's
  own — size `[sandbox]`'s limits accordingly if you dispatch read-only
  batches that run real shell commands (tests, linters) regularly.

## Known limitations

Sharp edges we know about:


- **The sandbox is not a container.** Worktree write-confinement + the
  deterministic shell floor are guardrails, not isolation. For untrusted or
  high-stakes work, run yottacode itself inside a VM/container.
- **Unattended `run_bash` and `run_tests` need the command sandbox.** Without
  the [command sandbox](sandbox.md) enabled, background workers
  can write owned files but cannot run shell, tests, or even a compiler — so a
  background worker commits code nothing has verified. Enabling
  `[sandbox] backend = "podman"` lifts this: each write worker gets its own
  container, and unattended shell/tests are confined to it (see Approvals in
  [the model above](#approvals)). Without a sandbox, a task that needs shell
  or tests must still run in the foreground (`background: false`), where you
  approve each call — file writes remain the only thing an unsandboxed
  background worker can do unattended, since the worktree and owned-file
  scope confine those to the worker's own branch and shell has no equivalent
  confinement on the host. "Confine, don't classify" —
  [`roadmap/dispatch-v3-collaboration.md`](../roadmap/dispatch-v3-collaboration.md#0d-revisited--unattended-shell-confine-dont-classify).

- **Kept worktrees accumulate until you integrate or discard them.** Empty
  worktrees are reclaimed automatically (per worker on finish, any outcome,
  foreground and background, plus the session-exit sweep), and `integrate`
  reclaims what it merges — but everything that's *deliberately* kept can
  still pile up: branches with commits you never integrate, worktrees holding
  an errored worker's uncommitted output, and a conflicted integration
  worktree awaiting resolution. A crashed session (kill -9, power loss) skips
  the at-exit sweep, but the next start-up runs two passes that recover the
  empty ones: the session's own rehydrated task records, then a scan of
  `git worktree list` for `worktree-dispatch-*` branches, which also catches
  worktrees from a session that died before its records were ever saved. Both
  keep anything they can't prove is empty, so deliberately-kept work is never
  swept. Clean the rest up yourself:

  ```bash
  yottacode worktree list      # see what's there
  yottacode worktree prune     # remove worktrees whose dirs are gone
  yottacode worktree remove <path>   # remove a specific one
  ```

- **The concurrency cap is per session, not per task tree.** The 8-background
  limit is a flat cap; there's no tree-wide budget yet, so deeply nested or
  rapid-fire dispatching is bounded only coarsely.
- **A shutdown mid-commit can still, rarely, leave a stale `index.lock`.**
  A worker's auto-commit deliberately runs detached from cancellation so
  just-finished work is never lost, which means quitting can't stop it — only
  outrun it. Quitting now waits out a commit that's genuinely in flight (up to
  30s, announced on stderr) instead of abandoning it after the 3s grace window,
  so this needs a wedged pre-commit hook to happen at all. If it does, the next
  `git` op in that worktree will tell you; clear the lock and retry.

These are tracked for the next iteration in
[`roadmap/dispatch-v3-collaboration.md`](../roadmap/dispatch-v3-collaboration.md)
(Layer 0).
