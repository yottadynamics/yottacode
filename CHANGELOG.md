# Changelog

All notable changes to yottacode will be documented in this file. The
format roughly follows [Keep a Changelog](https://keepachangelog.com/);
the project uses semantic versioning once it's past `1.0.0`.

## Unreleased

### Added

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
