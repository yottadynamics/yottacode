# Subagents

A **subagent** is a typed agent that yottacode dispatches in its own
context window. The parent session sees only the subagent's final
answer — the child's reasoning, tool calls, and tool outputs never
enter the parent model's adapter context. Use subagents to delegate
work that would otherwise blow up the main conversation: deep
codebase searches, planning explorations, regression test runs,
multi-file surveys.

This mirrors Claude Code's `Agent` / `Task` tool surface. The `Agent`
tool is exposed to the parent model; the model dispatches by
`subagent_type`, and yottacode handles the rest.

## Built-in agents

Eight agent types ship with the binary:

| Name | Tools | Purpose |
| --- | --- | --- |
| `general-purpose` | all parent tools (except `Agent` itself) | Answer open-ended questions. Falls back to writing if the task demands it. |
| `Explore` | read-only (read_file, grep, glob, list_*, git read subcommands, fetch_url) | Fast code search and location lookup. |
| `Plan` | Explore's tools + `todo_write` | Produce a written plan for a coding task. Ends with a `### Critical Files for Implementation` trailer. |
| `verification` | Explore's tools + `run_bash` | Adversarially verify a change: run builds / tests / probes, try to break it, end with a `VERDICT: PASS\|FAIL\|PARTIAL` line. Background-by-default. |
| `implement` | read + full write set + `run_tests` + `run_bash` | Build one well-scoped component end-to-end, staying inside its owned files. Write-capable, **background-by-default** — the workhorse write task in a `dispatch` fan-out. |
| `test` | read + write + `run_tests` + `run_bash` | Write/update and run tests for a component, owning the test files only. Write-capable, **background-by-default** — pairs with `implement` on disjoint files. |
| `docs` | read + `write_file`/`edit_file` + git read + `fetch_url` | Update documentation and comments for a change, owning the doc files only. Write-capable, **background-by-default**. |
| `review` | read-only (Explore's tools + more git read) | Read-only critique of a diff — findings ranked by severity (file:line + scenario). Cannot edit; complements `verification`. Foreground. |

The `implement` / `test` / `docs` / `review` roster rounds out the
**parallel-implementation** story behind `dispatch`: a typical fan-out is
`Plan` → `[implement, test, docs]` (disjoint files, in parallel) → `review`
+ `verification`. See [dispatch.md](dispatch.md).

The built-in definitions live in `internal/subagents/builtins/*.md`
and are embedded in the binary — they ship without any setup.

### `verification` in depth

Dispatch after non-trivial work (3+ file edits, backend / API
changes, infra changes). Pass the agent the original user task
description, the files changed, and the approach taken — the prompt
is structured around that input. The agent:

1. Reads project conventions (`PROJECT.md`, `README`, `AGENTS.md`).
2. Runs build / tests / linters as the universal baseline. A broken
   build is automatic FAIL.
3. Applies a strategy specific to the change type (frontend, backend,
   CLI, infra, library, bug fix, data pipeline, migration, refactor).
4. Runs at least one adversarial probe (concurrency, boundary,
   idempotency, orphan operation) before issuing PASS.
5. Returns a structured report with `### Check:` blocks (each with
   the exact command run and the actual output observed), terminated
   by a single parseable line:

   ```
   VERDICT: PASS
   ```

   or `FAIL` / `PARTIAL`. Use `PARTIAL` only for environmental
   limitations (no test framework, server can't start) — never as a
   hedge against "I'm unsure."

The agent is read-only against the project directory: it can write
ephemeral test scripts to `/tmp` via `run_bash` but cannot edit,
create, or delete project files, install dependencies, or run git
write operations.

**Background-by-default**: the agent's frontmatter declares
`background: true`, so dispatches default to a detached background
task even when the caller omits `run_in_background`. The parent gets
a task id back and can keep working; the verdict surfaces via the
`SubagentBackgroundDone` event. In oneshot mode (where background
isn't available), the dispatch silently falls back to foreground so
the verdict still lands inline.

## Custom agents

Drop a markdown file under either directory:

- `.yottacode/agents/*.md` (per-project; commit alongside the repo)
- `~/.yottacode/agents/*.md` (per-user; available in every project)

Project definitions win on name collision over user definitions, which
win over the built-ins.

### File format

```markdown
---
name: ReleaseScout
description: Audits the branch's release readiness — tests, gates, dirty checkouts.
tools: [read_file, read_many_files, grep, glob, list_dir, git_branch_status, git_diff_files]
model: claude-haiku-4-5
---

You are ReleaseScout. The parent has asked you to assess this branch's
release readiness. Investigate test status, feature gates, and any
uncommitted state. Report a punch list: ✓ ready · ✗ blockers · ? open.
```

Fields:

- `name` (required) — letters/digits/underscore/hyphen, max 64 chars. Used directly as `subagent_type` in tool calls.
- `description` (required) — one line shown to the parent model in the `Agent` tool schema.
- `tools` (optional) — allowlist of tool names. Defaults to "inherit all parent tools (minus `Agent` itself)". Use `*` or `["*"]` to be explicit.
- `model` (optional) — adapter model override for this agent. Honored when [cache-safe task routing](models.md#cache-safe-task-routing) is enabled (`[router].mode` = `manual` or `auto`); it always wins over the auto heuristic. With routing `off` the field is parsed but inert. Useful for pinning a search-heavy agent to a cheaper model, or a high-stakes agent to a stronger one.
- `background` (optional) — when `true`, dispatches default to background unless the caller explicitly passes `run_in_background:false`. Falls back to foreground in sessions where background isn't available (oneshot). Use this for slow off-turn checks the parent shouldn't block on (e.g. the `verification` builtin).
- Body — the agent's system prompt. Be specific about what the parent should expect back.

Unknown tool names in `tools:` emit a startup warning and are silently
dropped from the allowlist.

## Foreground vs background

The `Agent` tool accepts `run_in_background: true`:

- **Foreground (default, always available)**: the parent's tool call
  blocks until the child produces a final reply. The child's result
  lands directly in the parent's message history as a single `tool`
  role message. Use when the parent needs the answer to decide its
  next step in the same turn.
- **Background** (**experimental — see [experimental.md](experimental.md)**):
  the call returns immediately with a task id. The child runs to
  completion in a detached goroutine. The TUI surfaces completion
  via a `SubagentBackgroundDone` card on the next render cycle;
  `oneshot` rejects background calls because it has no long-running
  session to host them. Use when the parent can keep working
  without the answer.

  Background subagents are **gated behind the
  `background_subagents` experimental feature**. Without the gate,
  the model's `run_in_background:true` calls return a recoverable
  error pointing at how to enable. Enable with any of:
  - `yottacode --experimental background_subagents`
  - `YOTTACODE_EXPERIMENTAL=background_subagents`
  - `[experimental]\nbackground_subagents = true` in `~/.yottacode/config.toml`

  See [experimental.md](experimental.md) for the broader system.
  The gate exists because the model's reflexes around background
  subagents need more iteration — it tends to spawn one and then
  duplicate the work itself, producing slow or contradictory
  results. Foreground delegation is the stable surface.

The trade is between **context isolation** (both variants give it),
**parallelism** (both variants now — foreground subagents emitted in
the same assistant message fan out concurrently via the loop's
parallel-batch path; background still adds long-running off-turn
parallelism on top), and **causal chaining** (only foreground — the
parent gets the answer in the same turn it asked).

## Mode propagation

A subagent runs under the **same mode as its parent**. The parent's
`PlanModeState`, `AutoModeState`, and `YoloModeState` pointers are
shared with the child's `LoopConfig`, so the child observes the same
runtime flags. Mode changes on the parent (`/plan`, `Shift+Tab`)
propagate to in-flight subagents on the next tool dispatch — same
atomic-pointer mechanism the parent loop uses for itself.

**Why this design**: Claude Code itself has an open ambiguity about
plan-mode subagent behavior
([anthropic/claude-code#4750](https://github.com/anthropics/claude-code/issues/4750)).
Without a canonical reference, yottacode picks the safer invariant:
*"plan mode forbids mutations transitively, including through
delegation."* The rule is also easy to remember — *"a subagent runs
under the same mode as its parent."*

### Plan mode + subagents

When the parent is in plan mode (entered via `/plan`, `Shift+Tab`,
or `--permission-mode plan`):

- The child enters plan mode with the **same plan file** as the
  parent (pointer-shared `PlanModeState`).
- The child can call any read-only tool freely
  (`read_file`, `grep`, `glob`, `list_*`, `git_*` read subcommands,
  `fetch_url`, `todo_write`).
- The child's write attempts go through `PlanModeGate`, which
  allows writes ONLY to the parent's plan file. Any other write
  target (other than the plan file) returns the gate's block
  message as the tool result — same UX the parent gets in plan mode.
- The child cannot call `exit_plan_mode` (filtered from the child
  registry — leaving plan mode is the parent's decision, not a
  subagent's).
- The Explore and Plan built-ins remain fully useful during planning
  (their tool allowlists are already read-only); a general-purpose
  subagent becomes effectively read-only under a plan-mode parent.

**Visible effect**: a parent that says *"dispatch a Plan subagent to
draft how we'd add a /history command"* gets a useful plan back even
if it spawned the Plan subagent mid-investigation. The child can
investigate the codebase, append to the plan file (the same one the
parent is composing), and return a structured reply.

**Prompt nudge**: the plan-mode addendum also steers the parent to
dispatch subagents with `run_in_background:false`, so each
subagent's findings return in the same turn and fold directly into
the plan body — rather than landing via `get_subagent_result` after
the plan is already being written.

### Auto mode + subagents

When the parent is in auto mode (entered via `Shift+Tab`,
`--permission-mode auto`, or the plan-card `[A]` auto-approval hotkey):

- The child auto-allows non-safety-floor mutating tools (no per-call
  modal). Same rule as for the parent: edits, writes, and most git
  subcommands silently auto-execute; the safety floor (`run_bash`,
  `git_commit`, `git_checkpoint`, `rollback`) still triggers
  approval.
- The child's iteration budget is `childIterationCap × 4` = **160**
  iterations (vs. the standard 40), inherited from auto mode's
  multiplier. A child that runs deeper than 40 model→tool round-trips
  is rare but legitimate for "do all this work unattended" workflows.
- Foreground subagent + safety-floor tool → child's `ApprovalNeeded`
  forwards to the parent's modal (per the approval-flow rule below).
  The `[subagent:<type>]` badge makes it clear which agent is asking
  for the bash/commit.
- Background subagent + safety-floor tool → auto-deny (the
  background contract still holds; nobody's watching, so even
  auto-mode safety-floor approvals can't run).

**Visible effect**: a parent in auto mode that delegates *"refactor
all the import paths in pkg/X"* to a foreground subagent gets the
refactor done with no per-edit modal — just like a parent doing the
refactor directly under auto mode. The `git_commit` step at the end
still pops the approval modal (with `[subagent:general-purpose]`
prefix) because that's safety-floor.

### Yolo mode + subagents

`YoloModeState` is process-wide and pointer-shared. Once entered
(via `--yolo` at startup), it applies to all
subagents in the session, including background runs. The
yolo override skips every approval — including the safety floor —
and removes the iteration cap entirely. Use only in trusted
unattended contexts; subagents inherit the same risk profile as the
parent.

## Approval flow

Two cases, governed by foreground vs background:

- **Foreground subagent** + child tool needs approval → the request
  forwards to the parent's modal with a `[subagent:<type>]` badge in
  the preview so the user knows which agent wants what. The user's
  verdict routes back to the child via the parent's decisions channel.
  This works because while the parent's `Agent.Execute` call is
  running, the parent loop is itself blocked, so the decisions
  channel has no competing reader.
- **Background subagent** + child tool needs approval → **auto-denied**
  with a steering message. The parent's turn may have ended hours
  ago; a surprise modal arriving long after spawn is bad UX. Pre-authorize
  via `permissions.json` if the background subagent genuinely needs
  mutating tools.

## /subagents command

| Form | Effect |
| --- | --- |
| `/subagents` | Open the picker overlay in **tasks** view |
| `/subagents types` | Open the picker overlay in **types** view |
| `/subagents stop <id-prefix>` | Cancel a running task from the cmdline |

Inside the picker:

| Key | Effect |
| --- | --- |
| `↑` / `↓` | Move cursor |
| `Enter` | Open the highlighted task's transcript in `$PAGER` (tasks view only) |
| `t` | Toggle between **tasks** and **types** views |
| `s` | Stop the highlighted task (tasks view, running only) |
| `r` | Refresh the snapshot |
| `Esc` | Close the picker |

Task ids are 16-char hex; the first 8 chars are usually unique
enough for the `/subagents stop` cmdline form.

`/subagents` is the **after-the-fact** browser. While subagents are
running, a **live dock** sits at the bottom, a blank line **below the
status bar**. Each running subagent is one aligned row — agent type ·
latest activity · short model name · live context (`128K (1%)` = window +
percent used) · and a trailing identifier (`dispatch-<id>` for a dispatch
task, else `<type>-<id>`, matching the scrollback cards and `/subagents`).
It updates on each redraw (and keeps refreshing while background workers
run) and collapses to nothing when nothing is running.

Press **Tab** to focus the dock (when no completion palette is open and a
subagent is running): `↑`/`↓` (or `k`/`j`) move between subagents, `Enter`
opens the selected one's transcript in the pager, `Esc` returns to the
cmdline. It's keyboard-only by design — yottacode doesn't capture the mouse,
so terminal text selection / copy-paste keeps working.

## Transcripts

Every subagent run writes its full transcript (every event, every
tool call) to
`~/.yottacode/memory/projects/<slug>/subagents/<agent>-<id>.md` —
inside the project's memory dir, so all per-project agent state is
one `ls` away (the memory loader skips subdirectories, so transcripts
never load as memories). The
parent's context never includes this content, but the user can
inspect it through the `/subagents` picker (Enter on a row) or by
opening the file directly.

### Format

The transcript is markdown. Each tool call renders as a section:

```markdown
### Grep("AuthMiddleware")

​```
internal/auth/middleware.go:23:func AuthMiddleware(next http.Handler) http.Handler {
internal/auth/middleware.go:45:    return AuthMiddleware
​```

_12ms_
```

Streamed assistant content accumulates into one paragraph per message
(not one line per token). Tool outputs are fenced with a language
hint when one makes sense (`bash` for `run_bash` / `run_tests`,
`diff` for `edit_file` / `apply_diff` / `git_diff_files`, plain
otherwise). Errored tool calls carry an `_errored_` tag in the meta
footer below the fenced block.

The full tool output is preserved — no truncation. The live TUI
truncates tool cards at a fixed line cap so the scrollback stays
skimmable; the transcript is the place you go when you need the
complete output. (The parent agent never sees these outputs anyway —
only the child's final reply crosses the subagent boundary.)

`TurnDone` and `IterCap` collapse into horizontal rules (`---`) so
turn boundaries are visible without a separate event line per
boundary. The final `**Outcome:** runner_completed` (or
`runner_canceled` / `runner_iter_cap` / `runner_errored` /
`runner_no_final_reply`) sits at the end of the file as a guaranteed
end-of-record marker, followed by the `## Final result` section
carrying the same string the parent model received.

### Viewing a transcript

Pressing Enter on a row in the `/subagents` picker suspends the
TUI and opens the transcript in your pager. The default is plain
`less -R`: ANSI color through, no auto-quit so you always have
time to read.

You open at the top of the file. Standard `less` keys apply:

| Key | Effect |
|---|---|
| `↑` / `↓` / `k` / `j` | Scroll one line up/down |
| `PgUp` / `PgDn` / `b` / `Space` | Scroll one screen up/down |
| `g` / `G` | Jump to top / bottom of file |
| `/<pattern>` | Search forward (`?<pattern>` for backward) |
| `n` / `N` | Next / previous search match |
| `r` | Re-read the file and redraw (useful for seeing what a running subagent has written since you opened it) |
| `q` | Quit and return to the TUI |

**Seeing new content as the subagent runs**: press `r`. less re-reads
the file and redraws with whatever has been written since you
opened it. Press it again to refresh again.

**Live tail** (advanced): less supports `Shift+F` to enter
`tail -f`-style follow mode, but the follow-mode UX is awkward —
it drops into a `Waiting for data... (interrupt to abort)` state
that only `Ctrl+C` exits. The `r` refresh workflow covers the same
"see new lines" need without the dance.

**Pager resolution order**: `$YOTTACODE_PAGER` → `$PAGER` →
`less -RF` → `more` → inline scrollback fallback. If `$PAGER` is
set, we honor it verbatim — `$PAGER=less -FRSX` keeps your flags
untouched, no injection.

**Pager resolution order**: `$YOTTACODE_PAGER` → `$PAGER` →
`less -RF` (with our keys-hint prompt) → `more` → inline scrollback
fallback. If `$PAGER` is set, we honor it verbatim — your
`$PAGER=less -FRSX` keeps your flags untouched and our key hint is
not injected (your pager, your rules).

## Recursion guard

The child registry **always** excludes the `Agent` tool itself. A
malicious or accidental config that names `Agent` in its `tools:`
allowlist cannot reintroduce it — the exclusion runs after the
allowlist filter, every time. Subagents cannot spawn subagents.

## Iteration cap

Child subagents always run under the standard `MaxIterations` cap (40
in the current build). The parent's auto-mode 4× multiplier and yolo's
uncapped budget do **not** apply to children. A subagent that needs
more than 40 model→tool round-trips is almost certainly stuck — split
the work into separate subagent calls instead.

## Token cost

Subagents make their own API calls against the same provider key as
the parent. Token usage rolls into the session's overall counter via
the shared adapter. Per-subagent token counts are surfaced in the
`SubagentDone` event and stored in the task registry.

With [cache-safe task routing](models.md#cache-safe-task-routing)
enabled, read-only/search subagents run on the cheaper `fast_model`,
heavier ones on `smart_model`, and any agent with an explicit `model:`
on whatever it names — all in an isolated context that never shared the
main thread's prompt cache, so routing is a pure saving. The model each
subagent ran on shows in the `/subagents` picker and on its completion
card. (Per-subagent token figures are estimates; yottacode does not yet
aggregate per-model token totals across a session.)

## Dispatch & integrate (experimental)

Where a single `Agent` call delegates one subtask, **`dispatch`** fans a
whole batch out at once: each subtask runs as a concurrent subagent, and
write-capable ones run in their own git worktree + branch so they never
clobber each other. **`integrate`** then merges those branches into one
integration branch for a PR. Subtasks are partitioned by declared file
ownership so the merge stays clean by construction.

Write/implementation batches run in the **background** by default
(non-blocking — returns a batch handle immediately, workers auto-approve
within their worktree, you `integrate` when done); all-read/research batches
run in the **foreground** (blocking) and return every subtask's findings
together for the main agent to assemble right away.

This is gated behind the `dispatch` experimental feature and is the path
for "decompose a large PR into independent parallel tasks." Full guide:
[dispatch.md](dispatch.md).

## Known limitations

Things that are imperfect but not bugs — worth knowing about so the
behavior doesn't surprise you.

### Concurrent shared-cwd write children have no stale-read guard

`dispatch` write subtasks are physically isolated (each in its own git
worktree), so they cannot clobber each other. But two concurrent
**standalone `Agent`** write-capable children (spawned in one parallel
batch) share the parent's working directory, and there is no guard that
warns when one writes a file another had read. The clean path for parallel
*writes* is `dispatch` (worktree-isolated); concurrent shared-cwd write
children are the un-guarded case. A cross-task stale-read warning is
deferred — doing it correctly needs per-task file-access tracking threaded
through the fs tools, which is more than a quick advisory check.

### Multi-line tool cards can interleave with other output

When two multi-line cards (e.g. a tool result card and a subagent
start card) emit close in time, their lines may print interleaved
1:1 instead of one fully landing before the other starts. The
content is still correct; the visual block structure isn't.

This is a TUI rendering interaction with Bubbletea's inline-mode
print pipeline — not subagent-specific. It affects any concurrent
multi-line emission. We'll address it in a focused rendering pass
in a future release.

### Model occasionally picks the wrong agent type

The parent model selects `subagent_type` from the prompt and the
tool descriptions. It mostly picks well, but sometimes routes a
trivial lookup ("how many files?") through `general-purpose`
instead of `Explore`. `general-purpose` then over-investigates
because its prompt encourages thorough research. Result: a 10× or
worse latency vs. what `Explore` would have produced.

Workaround: name the agent explicitly in your prompt ("Use the
**Explore** subagent to find..."). Otherwise the model's choice
is what you get. Prompt steering in `DefaultSystemPrompt` nudges
toward correct selection but isn't enforcement.

### Background subagents need babysitting

Even with `background_subagents` enabled, the parent's reflexes
around fire-and-forget delegation are uneven:

- It may spawn a background subagent and then duplicate the work
  itself with `find` / `list_project_structure` / `bash`,
  reporting its own answer instead of the subagent's.
- It may pick `general-purpose` for a read-only count (slow + may
  trip the auto-deny on `run_bash`).

Foreground delegation is the stable, recommended surface today.
Background is gated experimental — opt in for parallel
investigations where you're willing to manage the workflow.

### Background subagents don't survive process restart

Tasks live in an in-process registry. Quitting yottacode forgets
them. Their transcript files on disk persist (you can read them
manually), but the task list and the `get_subagent_result` tool
won't surface them in a new session.

### Per-config permission narrowing isn't supported yet

Today subagents inherit the parent's `permissions.json` rules
unchanged. A future `permissions:` field on the agent definition
would let a custom agent declare narrower rules ("this agent
can't touch git"); not yet implemented.

### Background subagents can't prompt for approval

Foreground subagents forward approval requests to the parent's
modal. Background subagents auto-deny — there's no live UI to
prompt against. Pre-authorize the tools the subagent needs in
`permissions.json` (`allow` rules) if your background workflow
needs mutations.

## Why this design

The point of subagents is **context isolation**. The parent's
adapter context tracks one conversation thread; investigating
"where is X implemented across these 50 files?" would balloon that
context with read_file outputs, none of which the parent needs after
the answer is found. By delegating to an `Explore` subagent, the
parent gets back a single concise reply — the equivalent of asking a
colleague to look something up instead of doing it yourself.

## Open decisions (next steps)

Design questions surfaced but not yet decided. Each is recorded so
the choice gets made deliberately rather than via silent scope creep.

### Should background subagents be hard-restricted to read-only tools?

**Current behavior**: background subagents can be configured with any
tools (per agent definition). Mutating tools auto-deny at runtime
because no UI is attached, but they remain *present in the child's
tool schema*. A user can opt mutating tools back in by allowlisting
them in `.yottacode/permissions.json`, after which the auto-deny is
bypassed and a background subagent can write to disk unattended.

**Proposed alternative**: in `buildChildRegistry`, when constructing
the child for a background run, strip any tool whose
`RequiresApproval("")` returns true. The child's model would never
see mutating tools in its schema and cannot call them at all. The
schema-level enforcement removes both the auto-deny path AND the
`permissions.json` escape valve for background subagents.

**Trade-offs:**

| | Keep current | Hard-strip in background |
| --- | --- | --- |
| Background can write disk under user-allowlist? | Yes (via `permissions.json`) | No (schema-strip is unconditional) |
| Model wastes turns calling tools that auto-deny? | Sometimes | Never |
| Safety of unattended runs | Depends on `permissions.json` discipline | Always read-only |
| Power-user "background formatter" workflow | Possible | Not possible |
| Implementation cost | 0 (current) | ~15 LOC |
| Diverges from "foreground = write-capable" symmetry | No | Yes (asymmetric) |

**Decision deferred to**: a future session once we have more usage
data on whether anyone actually relies on writable-via-allowlist
background subagents. Until then, foreground forwards approvals,
background auto-denies, both keep their full configured toolset
in-schema.

### Should ALL subagents (foreground + background) be hard-restricted to read-only?

The stronger version of the above question. Discussed at length in
this session; the recommendation was yes (delete the
approval-forwarding plumbing entirely, simplify the mental model to
"subagents investigate, parent acts"), but no action was taken. Worth
revisiting after we have a few more real-world workflows that
exercise foreground approval forwarding — if it never gets used, the
case for keeping the plumbing weakens.

### Related: per-agent `mutating: true` opt-in frontmatter field

If we go hard read-only on subagents, add an optional
`mutating: true` frontmatter field to agent definitions so a power
user can opt back in for a specific custom agent. Default would
remain read-only; the field would be undocumented in v1 to avoid
encouraging the pattern. Deferred until the hard read-only decision
is made.

### Should subagents share the parent's plan file, or get their own?

**Current behavior**: plan-mode subagents write to the parent's
plan file directly. A `Plan` subagent dispatched mid-investigation
can extend the same `~/.yottacode/plans/<slug>.md` the parent is
composing. The user sees the merged result.

**Proposed alternatives:**

1. **Separate child plan files** —
   `~/.yottacode/plans/<parent-slug>/<child-id>.md`. The child drafts
   its own plan in isolation; the parent reads it back and decides
   whether to incorporate. Cleaner separation but requires the parent
   to do a merge step.
2. **Subagents cannot write to any plan file** — plan-mode children
   become strictly read-only investigators. They name what should be
   added to the plan in their final reply; the parent applies it.
   Strictest invariant, most consistent with the "subagents
   investigate, parent acts" framing from the hard-read-only
   discussion.

**Trade-offs:**

| | Share parent's plan file (current) | Separate child files | Subagents can't write plan file |
| --- | --- | --- | --- |
| Plan file authoritative source | Yes (one file) | Multiple files to merge | Yes (one file, parent-only) |
| Subagent can plan in parallel | Risk: overwriting parent's edits | Yes (isolated files) | No (parent must transcribe) |
| Recovery if subagent goes off-track | User manually edits plan file | Discard the child file | Nothing to recover from |
| Mental model | "subagent extends the plan" | "subagent drafts a draft" | "subagent reports findings" |

**Decision deferred**: requires more real-world usage of `Plan`
subagent invocations to see whether the current shared-file behavior
causes real friction.

### Should auto-mode subagents get the full 4× iteration budget?

**Current behavior**: a child under an auto-mode parent gets
`childIterationCap × 4` = **160 iterations**. The 4× multiplier
matches what the parent gets under auto mode.

**Concern**: 160 iterations of unattended, auto-approving mutations
is a lot. A subagent that legitimately needs more than the standard
40 iterations is rare; one that needs more than 80 is almost
certainly stuck. The current cap optimizes for the rare "let it run"
case at the cost of much wider blast radius for stuck runs.

**Proposed alternatives:**

1. **Tighter ceiling for children regardless of parent mode**:
   `childIterationCap = 40` always, no multiplier. The "let it run"
   intent applies to the parent, not transitively. Conservative.
2. **Scaled multiplier**: `childIterationCap × 2` under auto mode =
   80 iterations. Recognizes auto mode's intent without going as wide
   as the parent's 4×. Middle ground.
3. **Per-foreground-vs-background ceiling**: foreground children
   inherit 4× (user is watching); background children stay at 40
   regardless (unattended → bound tighter). Honors the foreground/
   background symmetry already at play in the approval-flow design.

**Decision deferred**: revisit once we've observed real subagent
runs hitting the cap. If `runner_iter_cap` outcomes are common,
that's evidence the current 160 is reasonable; if they're never
hit, evidence the lower cap would suffice.

### Should auto-mode subagent mutations be visible in scrollback?

**Current behavior**: in auto mode, mutating tool calls from the
child auto-allow silently. The parent's scrollback shows the
subagent's progress ticks (`├ edit_file(...)`), but no per-edit
modal flashes. This matches what the parent does in auto mode for
its own calls.

**Concern**: when a foreground subagent does 15 file edits
auto-approved under the parent's auto mode, the user sees the ticks
but doesn't get the file-by-file diff cards a normal foreground
tool call would produce. Information density goes down.

**Proposed alternatives:**

1. **Render full tool cards for child auto-approved mutations** —
   even when no modal is shown, emit a tool-card line in parent
   scrollback showing the diff/preview. Symmetric with how parent
   auto-mode renders mutations today. ~30 LOC in `runChild`'s event
   translator.
2. **Aggregate into a summary line** — at end of subagent run, emit
   a count-summary card: `subagent[X] · edited 7 files, wrote 2 new
   files, ran 3 git commands`. Less detail per change but cleaner
   scrollback.
3. **Status quo** — terse ticks only; users open `/subagents view
   <id>` for the full transcript when they want detail.

**Decision deferred**: contingent on whether subagents-with-mutations
remain a supported workflow at all. If we move to hard read-only,
this question disappears.

### Should subagents be allowed to call `exit_plan_mode`?

**Current behavior**: `exit_plan_mode` is filtered from every child
registry, regardless of the agent's `tools:` declaration. Leaving
plan mode is the parent's decision; subagents propose plans, they
don't approve them.

**Concern**: when a Plan subagent finishes a thorough plan, it'd be
natural for it to signal "I think we're done planning, ready to
implement." Today it has to express that as plain text in its
final reply, and the parent decides whether to act on it.

**Possible alternative**: allow Plan-typed subagents to call
`exit_plan_mode` as a signal — the call still surfaces the
approval card to the parent's user, who answers. The user approval
remains the canonical gate.

**Recommendation**: keep the current behavior. The "subagent
proposes, parent disposes" model is cleaner than letting children
trigger user-facing approvals from their own context. If the Plan
subagent's final text reads *"plan complete; recommend exit_plan_mode
and implement"*, the parent can take it from there with a single
call. Marking as low-priority deferred.
