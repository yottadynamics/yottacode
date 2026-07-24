# Architecture

`yottacode` is deliberately small and layered: one agent loop, one event
channel, two user interfaces, and a set of structured tools.

## Core Data Flow

```text
                ┌──────────────────────┐
                │   User Input         │
                │  (CLI or TUI)        │
                └──────────┬───────────┘
                           │
                ┌──────────▼───────────┐
                │    TUI / Oneshot     │
                │   (Consumer)         │
                └──────────┬───────────┘
          events ◄──────── │ decisions │
                           ▼
                ┌──────────▼───────────┐
                │   agent.Turn         │
                │  (Main Loop)         │
                │  ┌───────────────┐  │
                │  │ Tool Registry │◄─┘
                │  └───────────────┘  │
                │          │           │
                │          ▼           │
                │    Adapter Stream    │
                └──────────────────────┘
                           │
                ┌──────────▼───────────┐
                │  Model Provider API  │
                │  (OpenAI-compatible) │
                └──────────────────────┘
```

## Package Layout

```text
cmd/yottacode/                cobra root command + run subcommand

internal/
  cli/                        ChatOptions resolution
  adapter/                    OpenAI-compatible streaming + OpenAI Responses routing
  agent/                      Turn loop, tool registry, approvals, write-path validation
  session/                    Session persistence and resume logic
  memory/                     USER.md / YOTTACODE.md loading, agent-managed memory store, retrieval orchestrator
  recall/                     SQLite + FTS5 indexing for /recall
  tui/                        Bubble Tea interface, approval UI, slash commands
  oneshot/                    non-interactive `yottacode run` consumer
  version/                    version string
```

## Core Runtime Model

`internal/agent.Turn` is the only part of the system that talks to the model
and executes tools. Everything else either prepares its inputs or consumes its
events.

```text
user input
   |
   v
consumer (tui or oneshot)
   |                 ^
   | events          | decisions
   v                 |
agent.Turn --------------------> tool registry
   |
   v
adapter stream
```

This split is the reason the TUI and `yottacode run` can share nearly all of
the execution stack.

## Event Flow

The agent emits typed events such as:

- streamed assistant content
- streamed reasoning updates
- approval requests and auto-approval notices
- tool start and tool result messages
- iteration-cap warnings
- terminal completion or error events

Consumers decide how to render those events. The Bubble Tea UI turns them into
transcript rows, status changes, and approval modals. The one-shot runner sends
answer content to stdout and operational detail to stderr.

## Session Lifecycle

### `yottacode`

Running `yottacode` with no subcommand starts the interactive TUI directly.

Startup flow:

1. Parse flags and resolve environment variables.
2. Open or resume a session.
3. Load `USER.md`, `YOTTACODE.md`, and any agent-managed memories under user and project scope.
4. Build the model adapter and tool registry.
5. Start the Bubble Tea program and hand user turns to `agent.Turn`.

### `yottacode run`

`yottacode run "<prompt>"` uses the same core loop, but without the TUI.

- Prompt input comes from the CLI argument or stdin.
- Assistant content is written to stdout.
- Reasoning, tool status, and errors are written to stderr.
- Approval-required tool calls fail unless an `allow` rule in
  `.yottacode/permissions.json` matches them, or
  `--yolo` is set (DANGEROUS).

## Tools And Safety Layers

The agent exposes twenty-eight structured tools in [`tools.md`](tools.md). Two
independent safety systems gate every model-emitted call:

- **Permissions** (`internal/permissions/`) — project-local
  `.yottacode/permissions.json` (committable) and
  `.yottacode/permissions.local.json` (gitignored) carry
  pattern-based allow / ask / deny rules per tool. Decision precedence
  is deny > allow > ask > default.
- **Write-path validation** (`internal/agent/writepath.go`) —
  filesystem mutators (write/edit/mkdir/copy/move/delete) are confined
  to cwd, refuse symlinks, and refuse a hardcoded deny list of
  yottacode and git internal paths. `apply_diff` parses its diff
  header so each touched file goes through the same validator —
  the patch surface can't bypass the deny list.
- **Read-path validation** (`internal/agent/writepath.go`,
  `ValidateReadPath` + `DefaultDenyReadPaths`) — the auto-execute
  read tools (`read_file`, `read_many_files`, `grep`) refuse a
  narrow list of credential-bearing locations (`~/.ssh`, `~/.aws`,
  `~/.gnupg`, `~/.netrc`, `~/.yottacode/.env`, `<cwd>/.env*`, …) so
  prompt injection can't silently exfiltrate keys. The user can
  still read these via `run_bash`, which always prompts.

There is no in-process sandbox, and there will not be one. yottacode
deliberately stays out of the OS-isolation business — no bwrap,
firejail, landlock, seccomp, or pluggable `Sandbox` backends to
maintain — so the core stays small and portable. For real isolation
across every tool (`run_bash`, `write_file`, `git`, etc.), run
yottacode itself inside a container or devcontainer.

## Agent modes

Two **modes** (mutually exclusive, control workflow shape) and one
**startup-only overlay** (orthogonal, applies on top of any mode) sit
on top of the base approval flow:

- **Plan mode** — read-only research state. Entered via `/plan`,
  `Shift+Tab`, or `--permission-mode plan`. The model can read,
  search, ask, and write only to a single plan file under
  `~/.yottacode/plans/<slug>.md`. `exit_plan_mode` surfaces the plan
  in an approval card with four hotkeys: `[A]` auto-approval
  (implement with auto mode enabled), `[M]` manual approval
  (implement, per-tool prompts continue), `[L]` save for later, `[K]`
  keep refining. State lives on `agent.PlanModeState`.
- **Auto mode** — implementation state. Entered via `Shift+Tab` or
  `--permission-mode auto` (no slash command, mirroring Claude
  Code). Mutating tools auto-allow except a safety floor
  (`run_bash`, `git_commit`, `git_checkpoint`, `rollback`).
  Effective iteration cap is 4× the configured `MaxIterations`.
  State lives on `agent.AutoModeState`.
- **Yolo mode overlay** — drops permission prompts on *all* tools (no
  safety floor) and removes the iteration cap entirely. Entered via
  `--yolo` at startup or `/yolo` mid-session (mirroring Claude
  Code's startup flag, with a mid-session toggle added). Sits on
  top of whichever mode is active; the banner shows the mode label
  with a `⚠ yolo mode` suffix (the standalone banner reads
  `⚠ yolo mode`). State lives on `agent.YoloModeState`.

`Shift+Tab` cycles through `normal → auto → plan → normal`. Yolo
mode is intentionally **not** in the `Shift+Tab` cycle — entry is
via the `--yolo` startup flag or the `/yolo` slash command, so the
high-autonomy state stays a conscious decision rather than a key
chord away.

## Interrupts

Mid-turn user input is a first-class flow, not a blocked interaction:
pressing **Enter** while the agent is thinking (streaming, calling a
tool, or running a foreground subagent) captures the new message and
queues it for delivery at the next tool round without cancelling the
active turn. The TUI sends the message through the per-turn `userMsgCh`;
`agent.Turn` checks that channel between tool-call batches and appends
any delivered text to history as another user message. If the model
finishes before a tool-round injection point, or if the one-message
channel is already full, the TUI falls back to `pendingInputAfterTurn` so
`turnEndedMsg` can auto-submit the message as the next turn.

Before a queued message is delivered, pressing **Up** on an empty
mid-turn textarea recalls it into the editor and drains it from the
queue. Pressing Enter after editing requeues the revised text through the
same path; Esc/Ctrl+C still drop it instead of sending.

**Esc** and **Ctrl+C** are the explicit "stop without sending"
surface: they cancel the turn and drop any queued message, but leave
the textarea contents alone. Esc mirrors Claude Code's cancel feel;
Ctrl+C keeps the terminal-native semantics.

**Synthetic `tool_result` policy.** Mid-turn cancellation must
preserve provider-valid history: every `tool_use` block in the just-
cancelled assistant message needs a matching `tool_result`, or the
next request fails. The agent loop handles this in three places:

1. `streamIteration` accumulates streamed content tokens. On
   cancel, it returns a partial assistant message with the
   accumulated content and no tool calls (any tool-use the adapter
   was mid-building is deliberately dropped — content-only messages
   are valid for every provider).
2. `executeToolCall` propagates `ctx.Err()` from a ctx-respecting
   tool (instead of swallowing it as an `error: context canceled`
   string), so the caller can route into the cancel branch.
3. `executeToolCalls` (serial and parallel) appends `"interrupted
   by user"` `tool_result` entries for every orphaned call — both
   the in-flight tool that was cancelled and any queued calls that
   never started. Parallel workers that completed cleanly before
   the cancel keep their real result.

When a queued message must fall back to cancellation, history is repaired,
the loop emits a `TurnInterrupted` event (distinct from `ErrorEvent` so
consumers render it as a calm `↩ interrupted` line, not a red error), and
returns. The TUI's auto-submit then fires the queued message into a fresh
turn that sees the partial assistant content + synthetic tool results in
history.

**Background subagents are exempt.** Their context is detached from
the parent turn (`context.Background()`, not the parent's ctx), so a
parent-turn cancel does not propagate. They continue running to
completion and surface via `SubagentBackgroundDone` whenever they
finish, regardless of which parent turn is active.

## Subagents

The `Agent` tool (`internal/agent/agent_tool.go`) is the parent's
delegation surface. When the model calls it, yottacode constructs a
fresh `LoopConfig` that reuses the parent's adapter, permissions, and
cwd, but pairs them with a filtered tool registry, fresh inactive
plan/auto mode states, a standard iteration cap, and an isolated
message history seeded from the chosen agent definition's system
prompt and the user-supplied subagent prompt. `agent.Turn` runs
recursively against that config; the child's events flow into a
runner-local channel that the parent **does not** consume directly —
the runner translates only high-level activity (subagent start /
progress / done) into events on the parent's `events` channel, so the
parent's context window never sees the child's reasoning or tool
outputs. Only the child's final assistant content is returned as the
parent's tool-result string. Foreground runs block the parent's tool
call; background runs (`run_in_background: true`, TUI-only) detach to
a goroutine bound to a session-scoped context, and surface their
completion via a long-lived inbox channel the TUI's Model drains in
parallel with the per-turn event stream. The child registry **always**
excludes `Agent` itself (hard recursion guard, even against
adversarial config) and `exit_plan_mode`. See
[subagents.md](subagents.md) for the user-facing surface — agent
definition format, built-in agents, `/subagents` command, and
limitations.

The loop reads all three flags at turn start (effective iteration
cap) and on every tool dispatch. Approval-chain priority (the internal
`YoloModeState` Go identifier still uses "yolo" in the precedence label;
the user-facing banner label is "yolo mode"):
`Deny > yolo > plan-gate > plan-file-allow > auto-allow > Allow > Ask > tool default`.
See [security-and-allow-lists.md](security-and-allow-lists.md) for
the full precedence table.

## Extension Points

Most feature work lands in one of these seams:

- Add a new tool by implementing `agent.Tool` and registering it in the TUI and
  oneshot setup paths.
- Add a new slash command in [`internal/tui/commands.go`](../internal/tui/commands.go).
- Add or expand an adapter while keeping `agent.Turn` unchanged.
- Add a new built-in subagent type by dropping a markdown file under
  [`internal/subagents/builtins/`](../internal/subagents/builtins/); `//go:embed`
  picks it up at build time without Go changes.

Provider diagnostics follow the same seam discipline:

- static resolution and validation belong in `internal/adapter`
- active probes belong in `internal/adapter`
- `/provider`, `/doctor`, `yottacode doctor`, and oneshot preflight are thin
  consumers of that adapter-level API

The general rule is simple: keep UI concerns in `tui` or `oneshot`, provider
details in `adapter`, and agent behavior in `agent`.