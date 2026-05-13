# yottacode v0.2.0

**Release Date:** 2026-05-13

> Control flow + safety triad — typed subagents, per-prompt checkpoints, plan & auto modes, and a custom-command starter kit.

---

## ✨ Highlights

- **Typed subagents (`Agent` tool + `/subagents` picker)** — delegate research, code search, and planning to typed subagents that run in their own context window. Three built-in types (`general-purpose`, `Explore`, `Plan`) plus custom `.yottacode/agents/*.md` definitions. Mode and approval flow forward into child runs ([#5](https://github.com/yottadynamics/yottacode/pull/5)).
- **Per-prompt checkpoints (`/checkpoints` / `Esc Esc`)** — automatic file-snapshot rewind that mirrors Claude Code's `/rewind`. Every user message snapshots conversation + pre-edit file contents under `~/.yottacode/checkpoints/<session>/`, content-addressed and deduped with a 30-day TTL ([#8](https://github.com/yottadynamics/yottacode/pull/8)).
- **Plan and auto modes** — `/plan` enters a read-only design phase; approving with `[Y]` exits into auto mode for the implementation. Safety floor preserved: `run_bash`, `git_commit`, `git_checkpoint`, and `rollback` still prompt. Mode state propagates into subagent runs ([#4](https://github.com/yottadynamics/yottacode/pull/4)).
- **`todo_write` tool + inline todo cards** — model-driven plan tracking that mirrors Claude Code's `TodoWrite`. The TUI renders each snapshot as a scrollback card; the list persists to `session.Todos` and restores via `/sessions <id>` ([#3](https://github.com/yottadynamics/yottacode/pull/3)).
- **Mid-turn interrupts** — `Ctrl+C` cancels in-flight turns cleanly: streaming stops, the in-progress tool call receives a context cancellation, and conversation history is repaired so the next turn doesn't trip on a dangling `tool_use`. Read-only slash commands marked `PreservesTurn` don't cancel an active turn when run mid-flight ([#7](https://github.com/yottadynamics/yottacode/pull/7)).
- **Custom slash commands + built-in starter kit** — user/project commands loaded from `~/.yottacode/commands/` and `.yottacode/commands/`. Four embedded defaults ship with the binary: `/git:commit-message`, `/git:create-pr`, `/check:review`, `/check:verify`. YAML frontmatter (`description`, `argument-hint`), `$ARGUMENTS` / `$1..$9` substitution, and `@<path>` file references supported ([#10](https://github.com/yottadynamics/yottacode/pull/10)).
- **Experimental feature flag system** — `--experimental`, `$YOTTACODE_EXPERIMENTAL`, and `[experimental]` in `config.toml` gate not-yet-stable features behind opt-in. First feature: `background_subagents` (off by default) ([#5](https://github.com/yottadynamics/yottacode/pull/5)).
- **Permission file integrity checks** — `permissions.json` and `permissions.local.json` are validated on load for shape, duplicate rules, and known rule-type prefixes. Malformed files surface a startup warning naming the offending entry rather than silently dropping rules ([#11](https://github.com/yottadynamics/yottacode/pull/11)).

---

## 🐛 Bug Fixes & Improvements

- **TUI `write_file` card** no longer mis-renders when the target is a new file. Previously fell through to an edit-style diff with an empty `before` block; new files now render with a clear new-file indicator and full body preview ([#9](https://github.com/yottadynamics/yottacode/pull/9)).
- **Built-in tool output rendering refactored** in the TUI. Tool-result cards now compose from a per-tool renderer interface so future tools can ship with custom card shapes without touching the main TUI loop ([#2](https://github.com/yottadynamics/yottacode/pull/2)).

---

**Full Changelog**: [v0.1.0...v0.2.0](https://github.com/yottadynamics/yottacode/compare/v0.1.0...v0.2.0)
