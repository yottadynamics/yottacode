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
| `--bypass-permissions` | — | no | Dangerous: skip approval prompts except explicit deny rules |
| `--max-iterations` | — | no | Tool-call cap per turn; default `25` |
| `--resume` | — | no | Resume a session by id or name |

Precedence is flags, then environment variables, then config file where supported, then a clean error for missing required values.

## Interactive TUI

```bash
yottacode
```

The TUI is best for multi-turn coding work. It provides streaming output, slash commands, approval modals, session controls, and inline scrollback.

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

Use `run` for scripts, CI jobs, or shell pipelines. stdout contains the final assistant response. stderr contains reasoning, progress, and tool status.

Examples:

```bash
yottacode run "write a changelog entry for the current git diff"
```

```bash
yottacode run --max-iterations 10 "find likely flaky tests in this repo"
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

`doctor` probes the provider `/models` endpoint and reports reachability, auth status, model visibility, provider capabilities, and configuration warnings. The JSON form is intended for scripts.

## ChatGPT OAuth

```bash
yottacode openai-auth login
yottacode openai-auth status
yottacode openai-auth status --json
yottacode openai-auth logout
```

These commands manage the `openai-auth` provider's browser OAuth credentials and account-specific model list.

## Sessions

```bash
yottacode sessions list
yottacode sessions list --json
yottacode sessions resume <id-or-name>
yottacode sessions resume <id-or-name> --summarized
yottacode sessions rename <id-or-name> <new-name>
yottacode sessions export <id-or-name>
yottacode sessions export <id-or-name> path.md --force
```

Sessions are saved automatically after completed turns in `~/.yottacode/sessions/`.

## Memory

```bash
yottacode memory list                              # default scope: project
yottacode memory list --scope user
yottacode memory forget --scope <user|project> <name>
```

Use the TUI `/memory` picker to edit `USER.md` / `YOTTACODE.md` and browse agent-managed memories in `vim`. See [Memory](../memory.md) for the full layout and the `memory_save` / `memory_forget` tools the agent uses to curate this layer.

## Version

```bash
yottacode version
```
