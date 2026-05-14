# yottacode

**Open-source terminal coding agent for your day-to-day engineering work.**

`yottacode` is a single Go binary you run from a project directory. It gives you an interactive terminal UI for day-to-day coding, a scriptable one-shot mode for automation, structured tools for inspecting and editing real repositories, durable sessions, cross-session recall, and explicit memory — all without tying your workflow to one model provider.

> **Status:** pre-1.0 (`0.1.0`). The CLI, configuration, and on-disk formats are stabilizing. Pin a tag if you depend on yottacode from scripts.

## Getting Started

One-liner install (Linux + macOS, amd64 + arm64):

```bash
curl -fsSL https://raw.githubusercontent.com/yottadynamics/yottacode/main/install.sh | bash
```

The installer drops `yottacode` into `~/.yottacode/bin/` (no `sudo`), verifies the release archive against published SHA256 sums, and offers to add the directory to your shell `PATH` (creating a timestamped backup of the rc file before any edit). Pass `--no-modify-rc` to skip the rc edit, or `--yes` to accept it non-interactively.

Then launch the interactive setup wizard to pick a provider and model:

```bash
yottacode setup
```

After install, yottacode checks GitHub for a newer release once a day on startup (cached at `~/.yottacode/cache/update-check.json`) and offers to upgrade before the TUI starts. Set `YOTTACODE_NO_UPDATE_CHECK=1` to disable.

Windows users should run yottacode under WSL.

<details><summary>Manual install (pinned version, no installer script)</summary>

```bash
export VERSION=0.2.0
# Swap linux/darwin and amd64/arm64 to match your machine
curl -fsSL https://github.com/yottadynamics/yottacode/releases/download/v${VERSION}/yottacode_${VERSION}_linux_amd64.tar.gz \
  | tar -xz
install -m 0755 ./yottacode "$HOME/.yottacode/bin/yottacode"
```

Available archives: `yottacode_${VERSION}_{linux,darwin}_{amd64,arm64}.tar.gz`; checksums in `SHA256SUMS` on each release.

</details>

For build-from-source, cross-compilation, manual provider configuration, and other install paths, see [`docs/installation.md`](docs/installation.md).

## Features

### Multi-provider support

Native adapters for **OpenAI**, **Anthropic**, **Google Gemini**, **xAI** (Grok), **local Ollama** (no API key needed), and **ChatGPT OAuth** ("Sign in with ChatGPT"). A generic OpenAI-compatible adapter covers **NVIDIA NIM**, **Groq**, **vLLM**, **Llama Stack**, and any custom `/v1` gateway. Swap providers and models mid-session via `/provider use` and `/model`.

### Self-learning memory layer

Memory is plain text, grep-able, and split across a handful of files. **You own `USER.md`** — anything you want yottacode to remember about you globally goes there. **Everything else is curated by the agent** through `memory_save` / `memory_forget` in-conversation, with approval-gated writes for `YOTTACODE.md`.

| Path | Scope | Maintained by |
|---|---|---|
| `~/.yottacode/USER.md` | User preferences | **You** — human-edited only |
| `~/.yottacode/memory/` | Global (cross-project) | Agent — auto-curated |
| `./.yottacode/YOTTACODE.md` | Project | You seed it (or run `/init`); agent edits go through the approval modal |
| `~/.yottacode/projects/<slug>/memory/` | Project | Agent — auto-curated |

### Built-in security and approval layer

First-launch trust prompt on each new workspace (mirroring Claude Code) records consent at `~/.yottacode/trusted-roots.json`; subfolders of a trusted root inherit trust automatically. Manage roots with `yottacode trust list/add/remove/clear`. Mutating tools (writes, edits, shell, git mutations) ask before running, with a syntax-highlighted diff for edits. Project rules support `allow` / `ask` / `deny` (deny wins) for team-shared pre-approvals. Write-path validation confines writes to the working tree, blocks symlink writes, and firewalls secret-bearing paths (`.env`, `~/.ssh`, cloud credentials, auth stores) from both reads and writes.

> Tools run on the host — there is no in-process sandbox. For stronger isolation, run yottacode inside a container or devcontainer. See [`docs/security-and-allow-lists.md`](docs/security-and-allow-lists.md).

### Polished terminal UX

Inline rendering keeps your scrollback intact. Markdown-rendered assistant output, slash-command palette with Tab completion, multi-line input via `Ctrl+J`, input history, and a `?` cheatsheet overlay.

### Repo-aware tool surface

Thirty built-in tools spanning reads, writes, filesystem, search, git helpers (status / diff / blame / log / commit / checkpoints / rollback / file-at-revision), bash, tests, the `todo_write` working-plan tracker, and the `exit_plan_mode` plan-approval surface — each with explicit approval policy. See [`docs/tools.md`](docs/tools.md).

### Typed subagents

Delegate research, code search, and planning to typed subagents that run in their own context window — the parent only sees the final answer, never the child's tool calls or reasoning. Use it to keep the parent's context lean during long conversations. Three built-ins ship: **`Explore`** (read-only code search), **`Plan`** (drafts an implementation plan), **`general-purpose`** (open-ended research). Ship your own under `.yottacode/agents/<name>.md` (project) or `~/.yottacode/agents/<name>.md` (global) with YAML frontmatter declaring tools and an optional model override. `/subagents` opens an inline picker; `Enter` views any task's transcript in `$PAGER`. Mirrors Claude Code's `Agent` / `Task` tool surface. See [`docs/subagents.md`](docs/subagents.md).

> Background subagents (`run_in_background:true` for fire-and-forget delegation) are an opt-in experimental feature. Enable with `yottacode --experimental background_subagents`, `YOTTACODE_EXPERIMENTAL=background_subagents`, or `[experimental]` in `~/.yottacode/config.toml`. Foreground delegation is default-on. See [`docs/experimental.md`](docs/experimental.md).

### Read-only plan mode + auto mode

`/plan` (or `Shift+Tab`, or `yottacode --permission-mode plan` at launch) toggles a read-only research mode that mirrors Claude Code's plan mode: the agent investigates, asks clarifying questions, writes a plan file under `~/.yottacode/plans/<slug>.md`, then calls `exit_plan_mode` (no arguments — the TUI reads the file) to present the plan in an approval card. Approve with `[A]` to resume execution, or `[Y]` to enter auto mode and skip per-tool prompts during implementation.

Auto mode enters via `Shift+Tab` or `yottacode --permission-mode auto` at launch — useful when you trust a multi-step implementation and want to skip approval friction. `run_bash`, `git_commit`, `git_checkpoint`, and `rollback` remain in the safety floor and still prompt. `Shift+Tab` cycles through normal → auto → plan → normal. Mirroring Claude Code, there is no `/auto` slash command — auto enters via the keybinding or the startup flag, and the permissions-bypass overlay (every tool auto-runs, no iteration cap) enters only via `yottacode --dangerously-skip-permissions` at startup. See [`docs/tui-slash-commands.md#plan-mode`](docs/tui-slash-commands.md#plan-mode) and [`docs/tui-slash-commands.md#auto-mode`](docs/tui-slash-commands.md#auto-mode).

### Per-prompt checkpoints (`/checkpoints` / `Esc Esc`)

Every user message gets an automatic checkpoint capturing the conversation plus the pre-edit contents of any files the agent is about to touch. `/checkpoints` or double-tap `Esc` opens a picker over past prompts; pick one and choose to restore conversation, files, or both — the original prompt reappears in the input box so you can edit and resend. Mirrors Claude Code's `/rewind`. 30-day TTL by default, configurable in `config.toml`. Bash and git mutations are not tracked. See [`docs/tui-slash-commands.md#checkpoints---checkpoints--esc-esc`](docs/tui-slash-commands.md#checkpoints---checkpoints--esc-esc).

### Custom slash commands

Drop a markdown file into `~/.yottacode/commands/` (user scope) or `.yottacode/commands/` (project scope, committable) and it shows up as `/<name>` in the palette. Bodies support `$ARGUMENTS` / `$1`..`$9` argument substitution, optional YAML frontmatter (`description`, `argument-hint`), and `@<path>` file references. Subdirectories namespace commands as `/ns:name`. Mirrors Claude Code's custom-commands surface. See [`docs/tui-slash-commands.md#custom-commands`](docs/tui-slash-commands.md#custom-commands).

### Cross-session recall

`/recall <query>` runs local SQLite FTS5 search across every saved session. `/summarize` compacts long sessions after snapshotting the full pre-summary transcript. Per-turn atomic save means crashed terminals don't lose work.

### Scriptable one-shot mode

`yottacode run "<prompt>"` for CI and automation — stdout = answer, stderr = reasoning + tool status. Composes cleanly with pipes and CI logs.

## Common commands

In the TUI:

```text
/help                 show this list
/quit                 exit yottacode
/clear                start a fresh session (current is saved)
/permissions          show where permissions are configured
/system               show the active system prompt
/sessions             open the sessions menu (or /sessions <id|name> to resume directly)
/model                open the model picker (subcommands: list [all], <name>)
/provider             open the provider menu (subcommands: list, use, add, remove, models)
/doctor               probe provider auth and model access
/redo                 edit and re-run the most recent message
/recall <query>       full-text search across every saved session
/summarize            compress session history into a structured summary
/memory               open the memory picker (USER.md / YOTTACODE.md / saved memories)
/max-iterations <N>   cap tool-call iterations per turn (default: 50; auto 4×; --dangerously-skip-permissions removes the cap)
/setup                re-run the setup wizard (reloads config on return)
/init                 draft .yottacode/YOTTACODE.md from the current repo
/plan                 toggle plan mode (read-only research + plan file) — also Shift+Tab, or `yottacode --permission-mode plan` at launch
/plan list            resume an earlier plan from ~/.yottacode/plans/ — also `yottacode --plan-resume <slug>`
```

Auto mode and the permissions-bypass overlay are intentionally not slash commands (mirroring Claude Code):

- **Auto mode** — `Shift+Tab` from normal mode, or `yottacode --permission-mode auto` at launch
- **Permissions bypass** (every tool auto-runs, no iteration cap, DANGEROUS — deny rules still win) — `yottacode --dangerously-skip-permissions` at launch only; no in-TUI toggle

From the shell:

```bash
yottacode doctor
yottacode provider list
yottacode provider use openai
yottacode model list
yottacode sessions list
yottacode sessions resume <id-or-name>
yottacode --continue                       # most recent session in this directory
yottacode memory list
yottacode run "explain this repository"
```

Full references: [`docs/cli.md`](docs/cli.md) and [`docs/tui-slash-commands.md`](docs/tui-slash-commands.md).

## Documentation

- [`docs/quickstart.md`](docs/quickstart.md) — first successful session
- [`docs/installation.md`](docs/installation.md) — build and install options
- [`docs/configuration.md`](docs/configuration.md) — flags, env vars, config file, diagnostics
- [`docs/providers.md`](docs/providers.md) and [`docs/models.md`](docs/models.md) — provider/model setup and switching
- [`docs/tools.md`](docs/tools.md) — built-in tools and approval behavior
- [`docs/security-and-allow-lists.md`](docs/security-and-allow-lists.md) — approvals, permissions, path policy, isolation guidance
- [`docs/memory.md`](docs/memory.md) and [`docs/sessions.md`](docs/sessions.md) — context, recall, persistence
- [`docs/tui-slash-commands.md`](docs/tui-slash-commands.md) and [`docs/cli.md`](docs/cli.md) — command reference
- [`docs/architecture.md`](docs/architecture.md) and [`docs/development.md`](docs/development.md) — internals and contribution workflow
- [`docs/troubleshooting.md`](docs/troubleshooting.md) and [`docs/faq.md`](docs/faq.md)

## Development

```bash
go test ./...                    # unit tests
go test -tags=integration ./...  # live-provider integration tests
go test -race ./...              # race detector
go test -cover ./...             # coverage
```

See [`docs/development.md`](docs/development.md) for build, test, and adapter-extension guidance.

## Contributing

Issues and pull requests are welcome at <https://github.com/yottadynamics/yottacode>. New capabilities should include tests and docs. Before opening a PR, run:

```bash
go test ./...
go vet ./...
```

## License

MIT. See [`LICENSE`](LICENSE).
