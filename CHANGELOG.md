# Changelog

All notable changes to yottacode will be documented in this file. The
format roughly follows [Keep a Changelog](https://keepachangelog.com/);
the project uses semantic versioning once it's past `1.0.0`.

## Unreleased

### Added

#### Built-in custom-command starter kit

Four commands now ship with the binary as embedded defaults, available
on first launch with no `~/.yottacode/commands/` setup:

- `/git:commit-message` — gathers staged diff + branch context +
  staged CHANGELOG/README/docs prose, composes a one-line subject
  matching the repo's recent commit style, then runs `git commit`
  through an approval modal (the modal is your verification — the
  message is inlined in the heredoc). Prints a `Note:` block when
  unstaged or untracked files exist so you don't accidentally commit
  without them
- `/git:create-pr [base]` — drafts title + body, auto-pushes the
  branch to origin if needed, then runs `gh pr create` through an
  approval modal (the modal is your verification surface — the full
  title + body inlined in the heredoc). Falls back to draft-only
  output when `gh` is unavailable or unauthenticated
- `/check:review [base]` — self-review of the branch diff across
  correctness / scope / tests / style / security / performance
- `/check:verify [task-or-hint]` — detects the stack (Go / Python /
  Java with Maven or Gradle / Rust, plus Makefile as the universal
  fallback), runs build/test/lint with cache discipline (Go uses
  `-count=1` mandatory), cross-checks the diff against an optional
  task description, and prints a structured **Verdict** (Done /
  Not done / Done with caveats / Inconclusive). On failure, diagnoses
  by re-running the failing test in isolation AND checking git log
  for touched test files — never declares failures "pre-existing"
  without that evidence. The argument is mixed-purpose: task
  description, stack hint, command override, or all three in prose

Defaults sit at the lowest precedence tier — a same-name file in
`~/.yottacode/commands/` (user scope) or `<cwd>/.yottacode/commands/`
(project scope) silently overrides the embedded version. The override
path is what customization looks like: copy the default to your user
dir, edit, and it wins on every invocation. The built-in commands
(`/help`, `/clear`, `/model`, `/plan`, etc.) still sit above all three
tiers and cannot be shadowed.

#### Custom slash commands

User-authored slash commands loaded from `~/.yottacode/commands/`
(user scope, applies to every session) and `<cwd>/.yottacode/commands/`
(project scope, committable so a team can share commands via git).
Each `.md` file becomes one slash command; subdirectories namespace
the name (`commands/frontend/component.md` → `/frontend:component`).
Optional YAML frontmatter sets `description` (shown in the palette
and `/help`) and `argument-hint` (changes palette Enter to fill
`/name ` rather than fire immediately, mirroring built-ins like
`/recall`). Bodies support `$ARGUMENTS` and `$1`..`$9` argument
substitution, plus `@<path>` file references via the existing
filerefs pipeline.

Conflict resolution: project commands win over user commands of the
same name; same-scope duplicates and built-in collisions are dropped
with a startup warning. The implementation mirrors Claude Code's
custom-commands surface; `` !`<bash>` `` pre-execution and per-command
`model:` / `allowed-tools:` frontmatter are intentionally out of
scope for this first cut (workaround for shell context: the body can
instruct the agent to call `run_bash` itself).

**Permissions:** custom commands are a prompt shortcut, not a
permission bypass — the substituted body is sent to the agent
immediately, but every mutating tool call the agent makes in response
(`write_file`, `edit_file`, `git_commit`, `run_bash`, …) still flows
through the normal per-tool approval system. Use auto mode or
`.yottacode/permissions.json` allow rules to reduce prompt friction on
commands you trust. See
[`docs/tui-slash-commands.md#custom-commands`](docs/tui-slash-commands.md#custom-commands).

#### Per-prompt checkpoints (`/checkpoints` / `Esc Esc`)

A new user-facing rewind surface that mirrors Claude Code's `/rewind`.
Every user message automatically creates a checkpoint capturing the
conversation history and pre-edit contents of files the agent is about
to touch; an opt-in picker (`/checkpoints` slash command, or `Esc Esc`
double-tap within 500ms) lists past prompts and offers four restore
actions: *Restore code and conversation*, *Restore conversation only*,
*Restore code only*, and *Summarize from here*. The original prompt is
prefilled in the input box after a restore so you can edit and resend.

- **File-snapshot, not git-based** — pre-images are content-addressed
  blobs under `~/.yottacode/checkpoints/<session>/` so checkpoints
  don't pollute the working tree or fight with your git history.
  Repeated edits to the same file across two checkpoints store only
  the two distinct pre-images.
- **Tracked tools**: `write_file`, `edit_file`, `apply_diff`,
  `delete_file`, `move_file`, `copy_file`. Bash mutations and git
  operations are intentionally not tracked, matching Claude Code's
  `/rewind`. Picker footer surfaces this caveat.
- **30-day TTL by default**, configurable via `[checkpoints]
  retention_days = N` in `~/.yottacode/config.toml`. Sweep runs
  opportunistically on session open; orphan blobs are GC'd.
- **Atomic restore** — files are written `.tmp` then renamed, session
  saved second, in-memory state updated last. A crash mid-restore
  leaves the checkpoint intact so you can re-run.
- **Active-turn gating** — the picker is unavailable while a turn is
  running so file restores don't race live tool writes.

New `internal/checkpoint` package with the `Store`,
`Mutator` capability marker on the `Tool` interface, and
`CheckpointWriter` hook on `LoopConfig` so any future capture site
(subagent runs, oneshot) can opt in without further plumbing.

#### Typed subagents (`Agent` tool)

A new `Agent` tool lets the parent model delegate research, code
search, planning, or any multi-file investigation to a typed
subagent that runs in its own context window. The parent only sees
the child's final reply — the child's tool calls and reasoning
stay isolated, which keeps the parent's adapter context lean
across long conversations. Mirrors Claude Code's `Agent` / `Task`
surface.

Highlights:

- **Three built-in agent types** (`general-purpose`, `Explore`,
  `Plan`) embedded in the binary; usable with no setup.
- **Custom agent definitions** under `.yottacode/agents/*.md`
  (project) and `~/.yottacode/agents/*.md` (global) with YAML
  frontmatter declaring tools / description / optional model
  override. Project entries win over global, which win over
  built-ins.
- **`/subagents` slash command** opens an inline picker overlay
  with two views: **tasks** (current session's runs) and
  **types** (loaded agent definitions). `Enter` opens the
  highlighted task's transcript in `$PAGER`; `t` toggles views;
  `s` stops a running task; `r` refreshes; `Esc` closes.
- **`/subagents stop <id-prefix>`** cancels a running task from
  the cmdline.
- **Mode propagation**: a subagent runs under the same mode as
  its parent (plan, auto, yolo all propagate by pointer-shared
  state). A plan-mode parent's child enters plan mode with the
  same plan file; auto-mode parents pass their 4× iteration
  budget to children; yolo is process-wide.
- **Approval forwarding** (foreground only): when a child tool
  call needs approval, it surfaces on the parent's modal with a
  `[subagent:<type>]` badge. The user's verdict routes back to
  the child via the parent's decisions channel.
- **Recursion guard**: child registries never contain the `Agent`
  tool itself, even when a config's `tools:` allowlist names it.
- **Transcripts** persist under
  `~/.yottacode/projects/<slug>/subagents/<agent>-<id>.md` and
  are viewable from the picker.
- **`get_subagent_result` tool** retrieves a previously-dispatched
  task's final reply by id (or unique prefix). Defaults to a 60s
  blocking wait so the parent can spawn a background subagent
  and fetch the result in one tool round-trip; configurable up
  to 600s.

#### Experimental feature flag system (`internal/experimental`)

A small named-flag registry for gating not-yet-stable features
behind opt-in. Three resolution sources — `--experimental <name>`
CLI flag (repeatable), `$YOTTACODE_EXPERIMENTAL` env (comma-
separated), and `[experimental]` config.toml section — merge at
startup with CLI > env > config precedence. Unknown names emit a
startup warning rather than failing so graduated features don't
break old configs. See `docs/experimental.md`.

**`background_subagents`** is the first feature behind the gate.
Off by default; the `run_in_background:true` argument on the
`Agent` tool returns a recoverable error pointing at the enable
instructions. Foreground subagents are always available.

### Other

- `DefaultSystemPrompt` updated with context-efficiency rules
  steering the model toward Explore for lookups, Plan for design
  drafts, and the `get_subagent_result` tool for retrieving
  background subagent findings.
- New slash-command flag `PreservesTurn` marks read-only
  inspection commands (`/subagents`, `/help`, `/system`,
  `/permissions`, `/doctor`, `/recall`) so they don't cancel an
  active turn when submitted mid-run.
- The `view` and `list` subcommands of `/subagents` have been
  consolidated into the picker overlay — `/subagents` opens
  tasks view, `/subagents types` opens types view, and `Enter`
  on a row replaces `/subagents view <id>`.

### Known limitations

See `docs/subagents.md#known-limitations` for the full list. In brief:

- Multi-line tool cards can interleave with concurrent multi-line
  output (cosmetic; affects every multi-card turn, not just
  subagents).
- The parent model occasionally picks `general-purpose` for
  read-only lookups when `Explore` would be 10× faster; prompt
  steering is best-effort.
- Background subagents are experimental and gated. Even with the
  gate on, the model's reflexes around bg subagents still need
  iteration — it may spawn one then duplicate the work itself.
  Foreground is the recommended default.
