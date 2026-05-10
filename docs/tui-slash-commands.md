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

## Slash commands during a turn

If you run a slash command while a model turn is in flight, yottacode cancels the turn first, then runs the command. Normal messages are blocked until the active turn finishes or is canceled.

## Palette behavior

- Choosing a command with no args executes it immediately.
- Choosing a command that needs args fills the command prefix, such as `/model `, and waits for input.
- If you already typed a full command with args, Enter executes what you typed.

## Keyboard shortcuts

- `Enter` submits
- `Ctrl+J` inserts a newline
- `Ctrl+C` cancels the current turn
- `Ctrl+D` exits when input is empty
- `?` opens the cheatsheet when input is empty

The TUI uses inline rendering rather than an alternate screen, so your terminal scrollback remains available.
