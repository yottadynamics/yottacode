# CLI usage

`yottacode` has two primary entry points:

- `yottacode` — launch the interactive TUI
- `yottacode run "<prompt>"` — run one prompt non-interactively

Both use the same agent, tools, provider configuration, memory, and sessions.

## Global provider flags

These work for the TUI and `run` mode.

| Flag | Env var | Required | Purpose |
|---|---|---:|---|
| `--model`, `-m` | `YOTTACODE_MODEL` | yes | Model id to use |
| `--base-url` | `YOTTACODE_BASE_URL` | yes | Provider base URL |
| `--api-key` | `YOTTACODE_API_KEY` | no | Bearer token |
| `--provider` | `YOTTACODE_PROVIDER` | no | Provider override |
| `--reasoning-effort` | `YOTTACODE_REASONING_EFFORT` | no | Reasoning hint: `low`, `medium`, `high` |
| `--enable-web-search` | `YOTTACODE_ENABLE_WEB_SEARCH` | no | Enable hosted web search when supported |
| `--disable-web-search` | `YOTTACODE_DISABLE_WEB_SEARCH` | no | Disable default hosted web search |
| `--enable-x-search` | `YOTTACODE_ENABLE_X_SEARCH` | no | Enable xAI `x_search` |
| `--enable-code-interpreter` | `YOTTACODE_ENABLE_CODE_INTERPRETER` | no | Enable hosted code interpreter when supported |
| `--allow-paths` | `YOTTACODE_ALLOW_PATHS` | no | Extra write roots |
| `--yolo` | — | no | Dangerous: skip approval prompts and raise the iteration cap (yolo mode overlay). Explicit deny rules still apply. Launch-only; use `/yolo` to toggle mid-session. |
| `--max-iterations` | — | no | Tool-call cap per turn; default `128` (auto raises to `512`; `--yolo` raises to `2560` by default) |
| `--permission-mode` | — | no | TUI only — startup mode: `default` \| `plan` \| `auto`. Mirrors Claude Code's `--permission-mode`. |
| `--plan-resume` | — | no | TUI only — resume a saved plan by slug/substring (implies `--permission-mode plan`) |
| `--resume` | — | no | Resume a session by id or name |
| `--continue` / `-c` | — | no | Resume the most recent session in the current directory (mirrors `claude --continue`). Mutually exclusive with `--resume`. |
| — | `YOTTACODE_NO_UPDATE_CHECK` | no | Set to `1` to disable the once-a-day GitHub release check on TUI startup. |

Precedence is flags, then environment variables, then config file where supported, then a clean error for missing required values.

## Interactive TUI

```bash
yottacode
```

The TUI is best for multi-turn coding work. It provides streaming output, slash commands, approval modals, session controls, and inline scrollback.

Fresh sessions open on a welcome card. Hover an action to select it, click to activate it, or use the shortcut shown at the right edge of the row. The cmdline placeholder invites users to build anything; clicking the cmdline briefly brightens the existing border color so the terminal UI feels focusable:

- `Ctrl+W` — New worktree
- `Ctrl+R` — Resume session
- `Ctrl+P` — Enter plan mode
- `?` — Help / cheatsheet when input is empty

The welcome card intentionally stays action-only. Runtime state such as model, provider, branch, directory, memory, context, and tool status lives in the cmdline/status bar instead of being duplicated in the card.

Terminal tab/window titles use the compact form `<icon> · yottacode · <branch-or-worktree> · <dir>` so multiple sessions are distinguishable without opening each tab. The first icon is live-updated when the terminal supports title updates:

- `◐` / `◓` / `◑` / `◒` — working; cycles while the agent turn, tool call, or summarizer is active
- `◆` — needs input; an approval, path-trust prompt, or exit decision is waiting
- `↵` — queued input; a follow-up message is waiting to be consumed
- `○` — idle and ready for input

Managed worktree names are shown as `wt:<name>` and take precedence over the branch label.

Useful keys:

- `Enter` — submit
- `Ctrl+J` — insert newline
- `Ctrl+C` — cancel an in-flight turn
- `Ctrl+D` — quit when input is empty
- `?` — show cheatsheet when input is empty
- `/` — open slash-command palette

See [TUI slash commands](tui-slash-commands.md).

## One-shot mode

```bash
yottacode run "summarize this repo"
```

Use `run` for scripts, CI jobs, and shell pipelines. The run-specific output
flags are:

| Flag | Default | Purpose |
|---|---|---|
| `--format text\|json` | `text` | Select answer-only text or one structured stdout object |
| `--json` | off | Append the legacy status receipt to stderr; retained for compatibility |

### Text output

Text mode preserves the existing streamed assistant-content stdout contract, including
its terminating newline. Reasoning and consistently prefixed operational status
lines go to stderr, so redirects remain clean:

```bash
yottacode run "write a changelog entry for the current git diff" > draft.md
```

### Structured JSON output

`--format json` buffers the answer and emits exactly one newline-terminated JSON
object to stdout after the turn:

```bash
yottacode run --format json "triage the failures in test-output.txt" | jq .
```

```json
{
  "content": "The failure starts in...",
  "tool_calls": [
    {"name": "read_file", "summary": "read_file(test-output.txt)"}
  ],
  "usage": {
    "input_tokens": 1240,
    "output_tokens": 380,
    "cache_read_tokens": 900
  },
  "exit_reason": "ok",
  "error": null,
  "session_id": "20260908-143012.123456"
}
```

All six top-level fields are always present. `usage` is the shared
`adapter.Usage` shape; individual zero-value counters are omitted and should be
read as zero. `tool_calls` is an empty array when no yottacode tool ran. Each
summary is the bounded, human-readable call preview and deliberately excludes
full tool results.

Stable `exit_reason` values are:

- `ok` — the turn reached a final answer.
- `error` — the turn failed; `error` contains the message.
- `iter_cap` — the agent exhausted `--max-iterations` before a final answer.

`--format json` does not change process exit behavior. Failed turns still return
a non-zero shell status, while iteration-cap behavior remains unchanged. Capture
the status explicitly when a script needs to parse failed results:

```bash
set +e
yottacode run --format json "inspect the failing build" > result.json
run_status=$?
set -e
jq . result.json
exit "$run_status"
```

Operational progress remains on stderr and never contaminates the JSON stdout
object. Errors that occur before an agent turn starts, such as invalid flags or
missing provider configuration, retain the normal CLI error output because no
run result or session exists yet.

### Legacy `--json` receipt

The older `--json` flag remains compatible: stdout is still answer-only text and
a final receipt is appended to stderr with statuses such as `success`,
`approval_required`, `tests_failed`, or `iteration_cap`. Do not combine it with
`--format json`; new integrations should use the structured stdout contract.

See [the five `yottacode run` recipes](run-recipes.md) for PR descriptions,
codemods, test triage, dependency-audit summaries, and changelog drafting.

## Setup

```bash
yottacode setup
```

Runs the first-run configuration wizard. It can write `~/.yottacode/config.toml` and help configure provider profiles.

## Provider commands

```bash
yottacode provider list
yottacode provider add openai
yottacode provider use openai
yottacode provider remove old-provider
yottacode provider refresh
yottacode provider refresh openai
```

Provider profiles live in `~/.yottacode/config.toml` as `[[providers]]` blocks.

## Model commands

```bash
yottacode model list
yottacode model list --all
yottacode model use <your-model-id>
yottacode model fetch
yottacode model fetch openai
```

`model use` updates the active `default_model` in config. In the TUI, `/model <name>` switches only the running session.

## Diagnostics

```bash
yottacode doctor
yottacode doctor --json
```

`doctor` prints a grep-friendly report with a top-level summary and consistent sections for provider, GitHub, LSP, media, and sandbox checks. It probes the provider `/models` endpoint for reachability, auth status, model visibility, provider capabilities, and bounded model-list samples; GitHub can be skipped with `--no-github`. The JSON form is intended for scripts and keeps the provider fields at the top level for compatibility while adding nested section/status objects.

## ChatGPT OAuth

```bash
yottacode openai-auth login
yottacode openai-auth status
yottacode openai-auth status --json
yottacode openai-auth logout
```

These commands manage the `openai-auth` provider's browser OAuth credentials and account-specific model list.

## GitHub Copilot auth

```bash
yottacode copilot-auth login
yottacode copilot-auth models
yottacode copilot-auth models --raw
yottacode copilot-auth status
yottacode copilot-auth status --json
yottacode copilot-auth logout
```

These commands manage the `copilot` provider's GitHub device-code OAuth credentials and model cache. `login` runs the device code flow and caches available models. `models` lists cached models and marks plan-gated ones with `[upgrade plan]`.

## Sessions

```bash
yottacode sessions list
yottacode sessions list --json
yottacode sessions resume <id-or-name>
yottacode sessions resume <id-or-name> --summarized
yottacode sessions rename <id-or-name> <new-name>
yottacode sessions export <id-or-name>
yottacode sessions export <id-or-name> path.md --force
yottacode sessions export <id-or-name> path.jsonl --force
```

Sessions are saved automatically after completed turns in `~/.yottacode/sessions/`. Export paths ending in `.jsonl` write a schema-versioned structured activity log for team audit and debugging; review it before sharing because prompts, tool args/results, paths, command output, and image metadata can contain sensitive local context.

## Memory

```bash
yottacode memory list                              # default scope: project
yottacode memory list --scope user
yottacode memory recall --query "testing preferences"
yottacode memory recall --query "deployment" --scope project --top-k 3 --format json
yottacode memory audit                             # read-only curation report
yottacode memory forget --scope <user|project> <name>
```

Use the TUI `/memory` picker to edit `USER.md` / `YOTTACODE.md` and browse agent-managed memories in `vim`. See [Memory](../memory.md) for the full layout and the `memory_save` / `memory_forget` tools the agent uses to curate this layer.

## Worktrees

```bash
yottacode --worktree <name>           # start a session in a git worktree
yottacode -w <name>                   # short form
yottacode run --worktree <name>       # one-shot in a worktree
yottacode worktree list               # list active worktrees
yottacode worktree status             # show worktree state
yottacode worktree remove <name>      # remove a worktree
yottacode worktree prune              # clean up stale worktrees
```

See [Worktrees](worktrees.md) for the full workflow and `.worktreeinclude` format.

## GitHub setup

```bash
yottacode setup github                # interactive PAT setup (or use $GITHUB_TOKEN / gh auth)
```

See [GitHub integration](github.md) for the full auth chain and available slash commands.

## Version

```bash
yottacode version
```
