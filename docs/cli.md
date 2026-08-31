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

Use `run` for scripts, CI jobs, or shell pipelines. stdout contains the final assistant response. stderr contains reasoning, progress, and tool status. Pass `--json` when an integration needs a machine-readable final receipt on stderr with the run status, iteration count, tool counts, and changed files reported by `list_git_changed_files`; stdout remains answer-only so redirects stay clean. Status values are stable enough for automation: `success`, `approval_required`, `blocked_needs_clarification`, `tests_failed`, `policy_denied`, `provider_error`, and `iteration_cap`.

Examples:

```bash
yottacode run "write a changelog entry for the current git diff"
```

```bash
yottacode run --max-iterations 100 "implement step 3 of the plan we drafted yesterday"
```

```bash
yottacode run --json "resolve the current issue and summarize changed files"
```

`--json` appends a final stderr object shaped like:

```json
{
  "status": "success",
  "iterations": 3,
  "tools": {
    "list_git_changed_files": { "count": 1 }
  },
  "changed_files": ["internal/example.go", "docs/cli.md"]
}
```

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

`doctor` probes the provider `/models` endpoint and reports reachability, auth status, model visibility, provider capabilities, configuration warnings, and optional local tooling readiness such as LSP servers and media-editing binaries. The JSON form is intended for scripts.

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
