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
  `.yottacode/permissions.json` matches them, or `--bypass-permissions`
  is set (DANGEROUS).

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

## Extension Points

Most feature work lands in one of these seams:

- Add a new tool by implementing `agent.Tool` and registering it in the TUI and
  oneshot setup paths.
- Add a new slash command in [`internal/tui/commands.go`](../internal/tui/commands.go).
- Add or expand an adapter while keeping `agent.Turn` unchanged.

Provider diagnostics follow the same seam discipline:

- static resolution and validation belong in `internal/adapter`
- active probes belong in `internal/adapter`
- `/provider`, `/doctor`, `yottacode doctor`, and oneshot preflight are thin
  consumers of that adapter-level API

The general rule is simple: keep UI concerns in `tui` or `oneshot`, provider
details in `adapter`, and agent behavior in `agent`.