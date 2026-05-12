# TUI slash commands

Type `/` in the TUI to open the slash-command palette. The palette filters as you type, supports Tab completion, and can be dismissed with `Esc`.

## Command reference

| Command | Args | What it does |
|---|---|---|
| `/help` | — | List all commands with help text |
| `/quit` | — | Exit yottacode |
| `/clear` | — | Save the current session and start a fresh one |
| `/permissions` | — | Print shared and local permission file paths |
| `/system` | — | Show the active system prompt, including injected memory |
| `/sessions` | `[id\|name]` | Open the sessions picker or resume a known session directly |
| `/model` | `<name>` | Switch the active model for this session |
| `/provider` | — | Show resolved provider, API style, built-ins, capabilities, and diagnostics |
| `/doctor` | — | Probe the provider `/models` endpoint |
| `/redo` | — | Rewind the last user message and put it back in the input box |
| `/recall` | `<query>` | Search across saved sessions |
| `/summarize` | — | Compress the current session after snapshotting it |
| `/memory` | — | Edit curated memory or browse agent-managed memories |
| `/setup` | — | Suspend the TUI and rerun setup |
| `/init` | — | Ask the agent to draft or refresh `.yottacode/YOTTACODE.md` |
| `/plan` | — | Toggle plan mode (also `Shift+Tab`). Type `/plan list` to open a picker and resume an earlier plan. |
| `/subagents` | `[list \| view <id> \| stop <id> \| types]` | List subagent runs, view a transcript, stop a running task, or list available agent types. See [subagents.md](subagents.md). |

> **Auto mode and the permissions-bypass overlay are intentionally not slash commands** (mirroring Claude Code). Auto enters via `Shift+Tab` (cycle: `normal → auto → plan → normal`) or `yottacode --permission-mode auto` at startup. Permissions bypass enters only via `yottacode --dangerously-skip-permissions` at startup — there is no in-TUI toggle, no palette entry, no accidental activation. See [Auto mode](#auto-mode) and [Permissions bypass](#permissions-bypass) below.

## Provider picker

`/provider` shows the resolved provider profile and diagnostics. `/provider use <name>` switches to a configured provider directly. The provider picker also supports adding and removing profiles; adding `openai-auth` starts the browser OAuth flow inline and stores the account-specific model list after login.

## Sessions picker

`/sessions` opens a picker with actions for loading, resuming, renaming, and exporting sessions.

- Recent sessions are shown newest first.
- `/sessions <id-or-name>` resumes directly.
- Press `s` in the list, or `Ctrl+S` in the resume input, to toggle summarized resume for large transcripts.
- Export writes a Markdown transcript suitable for sharing or archiving.

## Memory picker

`/memory` opens a four-row picker:

- Project context: `./.yottacode/YOTTACODE.md`
- User preferences: `~/.yottacode/USER.md`
- Browse user memories (`~/.yottacode/memory/`)
- Browse project memories (`~/.yottacode/projects/<slug>/memory/`)

Opening a curated memory file (`USER.md`, `YOTTACODE.md`) suspends the TUI to `vim`; on exit, yottacode reloads memory and patches the active system prompt so the next turn sees your edits. The browse rows drop into a sub-list of agent-managed memories where `Enter` opens an entry in `vim`, `d` deletes it, `f` opens the folder in your file manager, and `Esc` returns to the root menu.

## Plan mode

`/plan` (or `Shift+Tab`) toggles plan mode — a read-only research state that mirrors Claude Code's `/plan`. The agent investigates the request, asks clarifying questions, and writes a plan file under `~/.yottacode/plans/<slug>.md`. While plan mode is on:

- Read-only tools (`read_file`, `grep`, `glob`, `list_*`, `git_log_file`, `fetch_url`, …) work normally.
- `todo_write` works normally.
- `write_file` / `edit_file` / `apply_diff` are blocked except when writing to the resolved plan file — writes to the plan file auto-allow without a prompt (it's the only legitimate mutation surface during planning).
- Every other mutating tool (`run_bash`, `git_commit`, `git_stage_files`, …) returns a "tool unavailable in plan mode" message to the model.
- A one-line banner immediately above the cmdline shows the mode, the plan file name (or "pending" before the file exists), and the current agent activity during a turn.

`/plan` and `Shift+Tab` take no arguments — the plan slug is derived from the first user message of the plan-mode session. The banner shows "ready — your next message names the plan" until that message arrives. You can also launch directly into plan mode with `yottacode --permission-mode plan`.

If the model surfaces material ambiguity during investigation — questions whose answers would change the plan's scope, approach, or target files — it is instructed to ask in its reply and end the turn *without* calling `exit_plan_mode`, so you can answer in your next message. The approval modal is hotkey-only ([A]/[Y]/[L]/[K]); putting dangling questions next to it would leave you with no way to type answers. Trivia that doesn't change the plan's shape can still live in the plan's "Open questions" section.

When the model finishes investigating and the plan is unambiguous, it calls the `exit_plan_mode` tool — which takes no arguments; the TUI reads the plan body from the file on disk and renders it in an approval card with four hotkeys:

- **`[A]` approve and implement** — exits plan mode and the agent immediately resumes execution. Per-tool approval prompts continue as normal.
- **`[Y]` approve and auto-implement** — exits plan mode AND enters auto mode for the implementation. Edits auto-allow; `run_bash`, `git_commit`, `git_checkpoint`, and `rollback` still prompt (safety floor).
- **`[L]` later** — exits plan mode but signals the model to *end the turn now without implementing*. The plan file stays on disk; resume any time via `/plan list` or `yottacode --plan-resume <slug>`.
- **`[K]` keep planning** — stays in plan mode; the model gets refinement guidance and is expected to revise the plan file and call `exit_plan_mode` again.

If the plan file is missing or empty when `exit_plan_mode` is called, the TUI auto-denies with a console notice.

## Auto mode

Press `Shift+Tab` from normal mode (or launch with `yottacode --permission-mode auto`) to enter auto mode — a state where mutating tools auto-allow without the per-tool approval modal. Reduces friction during a multi-step implementation when you trust the plan. Mirroring Claude Code, auto mode has no slash command; the entry points are the `Shift+Tab` cycle, the `--permission-mode auto` startup flag, and the plan-card's `[Y]` hotkey.

Safety floor (always prompts even in auto mode):

- `run_bash` — arbitrary shell commands.
- `git_commit` — writes permanent git history.
- `git_checkpoint` — writes a checkpoint commit.
- `rollback` — resets the repo state.

Auto mode and plan mode are mutually exclusive — entering one exits the other. The two share the `Shift+Tab` chord:

```
Shift+Tab cycle:  normal → auto → plan → normal
```

The plan-approval card's `[Y]` hotkey is a shortcut: it approves the plan AND enters auto mode in one keystroke, so the agent can implement the approved plan with minimal friction.

Auto mode persists across turns until you toggle it off. The banner above the cmdline (`▶ auto mode · edits auto-allow; bash & commits prompt`) is always visible while active so the state isn't easy to forget.

The default per-turn iteration cap is 50; auto mode raises the effective cap to 200 (4×). If you still hit the cap on long implementations, run `/max-iterations 500` (sanity ceiling) or relaunch with `--dangerously-skip-permissions` (no cap; see [Permissions bypass](#permissions-bypass)).

## Permissions bypass

Permissions bypass is the unrestricted overlay — every tool auto-runs (`run_bash`, `git_commit`, edits, everything), and the iteration cap is removed entirely. Intended for unattended long-running implementations where you've decided no further oversight is needed. Internally still called "yolo" in the codebase (a holdover identifier); the user-facing label everywhere now reads "permissions bypass" to match the `--dangerously-skip-permissions` flag.

Mirroring Claude Code, the overlay enters **only via `yottacode --dangerously-skip-permissions` at startup**. There is no slash command, no `Shift+Tab` binding, and no in-TUI toggle — opt in once per process, and recovery requires restarting yottacode without the flag. This is deliberate: the high-autonomy state should be a conscious one-time decision, not a key chord away.

The overlay is a **modifier**, not a mode — once active, it sits on top of normal, auto, or plan. Entering auto or plan via `Shift+Tab` does not turn bypass off. The bypass banner takes visual priority while it's on (it's the loudest signal), and when a mode (auto or plan) is also active, the mode banner picks up a `⚠ bypass` suffix instead.

Explicit `deny` rules in `.yottacode/permissions.json` still win — the bypass overlay is "skip prompts," not "ignore my policy." `Ctrl+C` is the escape hatch if a model goes into a runaway loop. The banner (`⚠ permissions bypass · all tools auto-allow · no iteration cap`) renders in red so the state isn't easy to forget; when a mode (auto or plan) is also active, the mode banner picks up a `⚠ bypass` suffix instead.

Plan-mode state is per-launch — a new `yottacode` session starts in normal mode, and resuming an old session never re-enters plan mode automatically. Plan files persist on disk under `~/.yottacode/plans/`, sorted newest-first.

To resume an earlier plan:

- **`/plan list`** opens a picker over saved plans (newest first). Enter resumes; Esc closes. Resuming attaches the plan file to plan mode (creates the mode if it's off) and re-applies the per-tool write allowance.
- **`yottacode --plan-resume <slug-or-substring>`** at launch matches the substring against saved plans (case-insensitive) and resumes the most recent match. Unmatched values fall back to a fresh plan with a stderr warning.

Plans never expire automatically — clean up the directory manually if it gets crowded.

The plan-mode gate runs *before* permissions evaluation, so explicit deny rules in `.yottacode/permissions.json` still win. `--dangerously-skip-permissions` does not skip the `exit_plan_mode` approval card — that approval is the user-visible signal, not a safety gate.

## Interrupting a turn

Pressing **Enter** while the agent is thinking captures whatever you typed, cancels the in-flight iteration, and queues the message for auto-submission the moment the loop unwinds. Any tokens that streamed before the cancel land in history as a partial assistant message, and any tool calls that were in flight or queued get a synthetic `interrupted by user` tool result so the next turn sees a valid conversation. This is the "interrupt with feedback" path — works the same in normal, plan, and auto modes.

Press **Esc** or **Ctrl+C** while a turn is running to cancel without submitting. Any queued message is dropped; the textarea contents are preserved so a draft survives an accidental Esc.

Slash commands typed mid-turn (e.g. `/clear`, `/model`) follow the same rule they always have — they cancel the turn and execute immediately. Slash commands that the codebase marks `PreservesTurn=true` (`/subagents`, `/help`) inspect without cancelling. Either way, a slash command mid-turn discards any plain-text message that was queued by an earlier Enter, so a `/clear` doesn't resurrect a stale follow-up message into a wiped session.

## Palette behavior

- Choosing a command with no args executes it immediately.
- Choosing a command that needs args fills the command prefix, such as `/model `, and waits for input.
- If you already typed a full command with args, Enter executes what you typed.

## Keyboard shortcuts

- `Enter` submits (mid-turn: interrupt and queue the new message)
- `Ctrl+J` inserts a newline
- `Esc` cancels the current turn (alias for Ctrl+C, mirrors Claude Code)
- `Ctrl+C` cancels the current turn; quits when no turn is running
- `Ctrl+D` exits when input is empty
- `?` opens the cheatsheet when input is empty
- `Shift+Tab` cycles agent modes: normal → auto → plan → normal

The TUI uses inline rendering rather than an alternate screen, so your terminal scrollback remains available.
