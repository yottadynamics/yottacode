# Configuration

`yottacode` does not ship with built-in defaults for the required provider
settings. If `--model` or `--base-url` is missing, startup fails immediately
with a clear error.

## Flags And Environment Variables

Every flag below works for both `yottacode` and `yottacode run`.
The same provider flags also apply to `yottacode doctor`.

| Flag | Env var | Required | Notes |
|---|---|---|---|
| `--model`, `-m` | `YOTTACODE_MODEL` | yes | Provider-specific model id (run `/model list` to see what your account allows) |
| `--base-url` | `YOTTACODE_BASE_URL` | yes | OpenAI-compatible base URL such as `http://localhost:11434/v1` or `https://api.openai.com/v1` |
| `--api-key` | `YOTTACODE_API_KEY` | no | Bearer token for authenticated providers |
| `--provider` | `YOTTACODE_PROVIDER` | no | Provider profile name or provider kind hint |
| `--reasoning-effort` | `YOTTACODE_REASONING_EFFORT` | no | Reasoning effort for providers that support it: `low`, `medium`, or `high` (unset = provider default). Applies to OpenAI, Anthropic, Gemini, and xAI via each one's native knob — see [providers.md](providers.md#reasoning-effort). Change it mid-session with [`/effort`](tui-slash-commands.md). |
| `--enable-web-search` | `YOTTACODE_ENABLE_WEB_SEARCH` | no | Enable provider-native web search when supported |
| `--disable-web-search` | `YOTTACODE_DISABLE_WEB_SEARCH` | no | Disable provider-native web search even when OpenAI/xAI would enable it by default |
| `--enable-x-search` | `YOTTACODE_ENABLE_X_SEARCH` | no | Enable xAI `x_search` when supported (xAI enables it by default; kept for explicit opt-in on older configs) |
| `--enable-code-interpreter` | `YOTTACODE_ENABLE_CODE_INTERPRETER` | no | Enable provider-native code interpreter when supported |
| `--search-allowed-domains` | `YOTTACODE_SEARCH_ALLOWED_DOMAINS` | no | Comma-separated allowlist for provider-native web search |
| `--search-excluded-domains` | `YOTTACODE_SEARCH_EXCLUDED_DOMAINS` | no | Comma-separated blocklist for provider-native web search |
| `--x-search-allowed-handles` | `YOTTACODE_X_SEARCH_ALLOWED_HANDLES` | no | Comma-separated X handle allowlist for xAI `x_search` |
| `--x-search-excluded-handles` | `YOTTACODE_X_SEARCH_EXCLUDED_HANDLES` | no | Comma-separated X handle blocklist for xAI `x_search` |
| `--x-search-from-date` | `YOTTACODE_X_SEARCH_FROM_DATE` | no | Inclusive lower bound for xAI `x_search` in `YYYY-MM-DD` form |
| `--x-search-to-date` | `YOTTACODE_X_SEARCH_TO_DATE` | no | Inclusive upper bound for xAI `x_search` in `YYYY-MM-DD` form |
| `--system` | — | no | Override the default system prompt |
| `--resume` | — | no | Resume a session by id or name |
| `--continue` / `-c` | — | no | Resume the most recent session whose cwd matches the current directory. Mirrors Claude Code's `--continue`. Mutually exclusive with `--resume`. |
| `--yolo` | — | no | DANGEROUS: auto-approve every tool call without prompting and raise the iteration cap to a large finite bound (`deny` rules in `permissions.json` still apply). Mirrors Claude Code's flag. Also toggleable mid-session with `/yolo` — restart without the flag, or run `/yolo` again, to recover. |
| `--max-iterations` | — | no | Tool-call cap per turn; defaults to `100`. Auto mode raises the effective cap to 4× (400). `--yolo` raises it to a large finite bound (not unlimited). |
| `--allow-paths` | `YOTTACODE_ALLOW_PATHS` | no | Comma-separated extra write roots in addition to the current working directory |
| `--permission-mode` | — | no | Startup permission mode: `default` (no startup mode), `plan` (read-only research; describe the task as your first message), or `auto` (edits auto-allow; bash & commits still prompt). Mirrors Claude Code's `--permission-mode`. No-op for `yottacode run`. |
| `--plan-resume` | — | no | Resume an existing plan by slug or substring (matched against `~/.yottacode/plans/`, newest-first). Implies `--permission-mode plan`. No-op for `yottacode run`. |

Precedence is:

1. Explicit flags
2. Environment variables
3. Matching provider profile in `~/.yottacode/config.toml`
4. Error for missing required values

Resolution lives in [`internal/cli/options.go`](../internal/cli/options.go).

## Examples

```bash
# Local Ollama (no API key) — flags form
yottacode --provider ollama --model <your-model-id> --base-url http://localhost:11434/v1

# Same thing through env vars
export YOTTACODE_PROVIDER=ollama
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=http://localhost:11434/v1
yottacode

# OpenAI API key
export YOTTACODE_PROVIDER=openai
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=https://api.openai.com/v1
export YOTTACODE_API_KEY=sk-...
yottacode

# ChatGPT OAuth (first run: yottacode openai-auth login)
export YOTTACODE_PROVIDER=openai-auth
export YOTTACODE_MODEL=<your-model-id>     # /model list shows what your account allows
export YOTTACODE_BASE_URL=https://chatgpt.com/backend-api/codex
yottacode

# GitHub Copilot (first run: yottacode copilot-auth login)
export YOTTACODE_PROVIDER=copilot
export YOTTACODE_MODEL=claude-haiku-4.5
export YOTTACODE_BASE_URL=https://api.githubcopilot.com
yottacode

# Anthropic
export YOTTACODE_PROVIDER=anthropic
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=https://api.anthropic.com
export YOTTACODE_API_KEY=sk-ant-...
yottacode

# Claude on Google Vertex AI (first run: gcloud auth application-default login)
# No API key — Application Default Credentials mint a fresh token per request.
export YOTTACODE_PROVIDER=vertex-anthropic
export YOTTACODE_MODEL=claude-sonnet-4-5@20250929   # Vertex needs the @version suffix
export YOTTACODE_BASE_URL=https://aiplatform.googleapis.com/v1/projects/<your-project>/locations/global
yottacode

# Gemini on Google Vertex AI (same credentials, different surface)
export YOTTACODE_PROVIDER=vertex
export YOTTACODE_MODEL=google/gemini-2.5-pro        # publisher-namespaced on the shim
export YOTTACODE_BASE_URL=https://us-central1-aiplatform.googleapis.com/v1/projects/<your-project>/locations/us-central1/endpoints/openapi
yottacode

# xAI with default web_search + x_search
export YOTTACODE_PROVIDER=xai
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=https://api.x.ai/v1
export YOTTACODE_API_KEY=xai-...
export YOTTACODE_SEARCH_ALLOWED_DOMAINS=docs.x.ai,arxiv.org
export YOTTACODE_X_SEARCH_ALLOWED_HANDLES=xai
yottacode

# Opt out of default hosted web search
export YOTTACODE_DISABLE_WEB_SEARCH=1
yottacode

# Allow writes into sibling repos too
export YOTTACODE_ALLOW_PATHS=/home/me/shared-configs,/home/me/other-repo
yottacode
```

## Isolation

There is no in-process sandbox, and there will not be one. yottacode
keeps its core small and does not ship bwrap/firejail/landlock
backends. `run_bash` and every other tool run directly on the host.
**For real isolation, run yottacode inside a container or
devcontainer** — that protects every tool (not just shell commands)
and is portable across host distros.

## Provider Diagnostics

At startup, `yottacode` resolves a provider profile from the configured
endpoint, model, and feature flags. That profile drives:

- adapter routing (`chat.completions` vs Responses API)
- which provider-native tools are actually enabled
- static diagnostics shown in the startup card, footer, and `/provider`

Examples of diagnostics:

- unsupported built-in tool flags on the selected provider
- `x_search` filters used on a non-xAI endpoint
- empty API keys for remote providers
- suspicious provider/model mismatches such as `grok-*` on `openai`

Use `/provider` to inspect the resolved static state, or `/doctor` to run an
active `/models` probe against the configured endpoint.

### Default Hosted Web Search

`yottacode` enables provider-native `web_search` by default for:

- OpenAI
- xAI

That default can be disabled with `--disable-web-search` or
`YOTTACODE_DISABLE_WEB_SEARCH=1`. xAI also enables provider-native
`x_search` by default so Grok can search X posts, users, and threads; use the
`YOTTACODE_X_SEARCH_*` filters below to narrow that surface.

For Ollama and generic OpenAI-compatible endpoints, hosted provider tools stay
off by default. Those models can use the local `fetch_url` tool instead.

For shell and CI usage:

```bash
yottacode doctor
yottacode doctor --json
```

`yottacode doctor` exits non-zero when issues are found. `--json` emits a
stable machine-readable payload.

### `doctor --json` shape

Top-level fields:

- `profile`
- `base_url`
- `model`
- `http_status`
- `endpoint_reachable`
- `auth_ok`
- `model_visible`
- `available_models`
- `issues`
- `warnings`

The nested `profile` object includes:

- `provider`
- `uses_responses_api`
- `supports_reasoning`
- `supports_web_search`
- `supports_x_search`
- `supports_code_interpreter`
- `enabled_builtin_tools`
- `issues`
- `warnings`

Example:

```json
{
  "profile": {
    "provider": "openai",
    "uses_responses_api": true,
    "supports_reasoning": true,
    "supports_web_search": true,
    "supports_x_search": false,
    "supports_code_interpreter": true,
    "enabled_builtin_tools": ["web_search"],
    "warnings": ["API key is empty for a remote provider"]
  },
  "base_url": "https://api.openai.com/v1",
  "model": "<your-model-id>",
  "http_status": 200,
  "endpoint_reachable": true,
  "auth_ok": true,
  "model_visible": true,
  "available_models": ["<model-a>", "<model-b>"],
  "warnings": ["API key is empty for a remote provider"]
}
```

## On-Disk State

Most state lives under `~/.yottacode/`:

```text
~/.yottacode/
  auth/openai-auth.json          ChatGPT OAuth token store (0600; denied to model tools)
  auth/openai-auth-models.json   per-account `openai-auth` model scan (0600)
  auth/copilot.json              GitHub Copilot OAuth token store (0600; denied to model tools)
  auth/copilot-models.json       per-account `copilot` model cache (0600)
  sessions/<id>.json             saved conversations
  checkpoints/<session>/         /checkpoints + Esc Esc snapshot store
  index.sqlite                   FTS5 index for /recall
  USER.md                        optional global user memory (human-only)
  memory/user/<name>.md          agent-managed user-scope memories
  memory/user/MEMORY.md          auto-generated index of user-scope memories
  memory/projects/<slug>/        agent-managed project-scope memories (per-user);
                                 subagent run transcripts nest in subagents/
  config.toml                    tunables (context watermarks, retrieval, memory, checkpoints)
```

### Context watermarks

The `[context]` block controls how aggressively yottacode reacts as the active model's context window fills:

```toml
[context]
warn_threshold = 0.65
                         # status-bar warning + reminder

auto_threshold = 0.85
                         # turn-boundary auto-summarize; 1.0 disables

compaction_threshold = 0.70
                         # mid-turn busy-loop compaction safety net; 1.0 disables

compaction_target_ratio = 0.35
                         # recent-tail share retained after mid-turn compaction

default_window = 128000
                         # fallback tokens for unknown models
```

`auto_threshold` runs between turns and writes a pre-summary snapshot before replacing old conversation with a summary. `compaction_threshold` runs inside a long busy turn at clean loop boundaries, after tool results have landed and before the next model request. It intentionally defaults below `auto_threshold` so long-running tool loops compact before provider hard limits rather than waiting for post-turn recovery. If a provider still rejects a request for context length before any assistant content streams, yottacode force-compacts once and retries. Interactive mid-turn compaction also writes a `~/.yottacode/sessions/<id>-pre-summary-*.json` snapshot before rewriting history.

Use `/context` in the TUI to inspect the active state: resolved model window, configured thresholds, tool-schema overhead, largest context buckets, compaction enabled/disabled reason, and the latest summarize/compaction outcome.

Set a threshold to `1.0` to disable that preemptive behavior. `compaction_target_ratio` controls how much of the active window is kept verbatim as the recent tail after mid-turn compaction; the rest is reserved for the system prompt, original task, compacted progress note, tool schemas, and the next model response. `/recall` indexes the compacted session slice, not compacted-away messages; the pre-summary snapshot is the recovery record for full history.

### Checkpoints retention

`/checkpoints` and `Esc Esc` capture a per-prompt snapshot under `~/.yottacode/checkpoints/`. By default, snapshots expire 30 days after creation; the sweep runs opportunistically when a session opens. Override the window in `~/.yottacode/config.toml`:

```toml
[checkpoints]
retention_days = 30   # set to 0 to fall back to the 30-day default; smaller values prune more aggressively
```

See [`tui-slash-commands.md`](tui-slash-commands.md#checkpoints---checkpoints--esc-esc) for the full feature.

### Final memory turn on quit

A graceful exit (`/quit` or `Ctrl+D` while idle) runs one last agent turn prompting the model to persist durable learnings via `memory_save` before the session context is gone. The turn renders in the transcript like any other; `Esc` or `Ctrl+C` skips it and completes the quit, and `Ctrl+C` as the quit gesture itself always exits immediately. A session with no turns started this launch quits instantly. Disable for always-instant exits:

```toml
[memory]
final_turn_on_quit = false
```

### Periodic capture reminder

Every Nth user message carries a mid-session reminder to persist anything durable the model hasn't saved yet. It covers the sessions the other reinforcement points miss: those that never reach the auto-summarize watermark, and those ended with `Ctrl+C` (which never runs the final turn above). It is appended to a message you were sending anyway — not an extra turn, and not a per-turn nudge — and it stands down when a pre-compaction reminder is already pending.

```toml
[memory]
capture_reminder_every_turns = 6   # 0 disables
```

See [`memory.md`](memory.md#proactive-saving--reinforcement-points) for the other proactive-save reinforcement points.

### Theme

`/theme` switches the TUI color palette and persists the choice. Pin a non-default theme in `~/.yottacode/config.toml`:

```toml
[theme]
name = "catppuccin"
# one of: terminal, catppuccin, dimmed, gruvbox, high-contrast,
#         low-contrast, no-color, nord, one-dark, solarized-dark,
#         tokyo-night
```

Omit the section to ride the default (`terminal`). Unknown names are rejected at load time — see [themes.md](themes.md) for the full palette catalog.

Per-repo state lives under `<repo>/.yottacode/`:

```text
<repo>/.yottacode/
  YOTTACODE.md                  optional per-repo project memory (human-seeded;
                              the agent keeps it fresh through approval-gated
                              writes)
  permissions.json            committable team-shared permission rules
  permissions.local.json      gitignored personal additions (where the
                              modal's [a]lways-allow path writes to)
```

See [`memory.md`](memory.md) for how the agent-managed memory layer
works and how to inspect or prune it.

`USER.md`, `YOTTACODE.md`, and both `permissions*.json` files are safe to
edit directly — opening either memory file via `/memory` (yottacode
suspends to `vim`) reloads on exit, and external edits to either
`permissions*.json` are picked up automatically on the next tool call
(run `/permissions` to print the two paths). Session files and the FTS
index are application-managed.

`USER.md` is in the agent's write-deny list (global preferences are
out of scope for a project-scoped agent to curate). `YOTTACODE.md` is
**not** in the deny list: the agent updates it through `edit_file` /
`write_file` with the standard approval modal gating each change. To
make `YOTTACODE.md` human-only on a specific project, add a deny rule
to `<repo>/.yottacode/permissions.json`:

```json
{ "permissions": { "deny": ["Edit(.yottacode/YOTTACODE.md)", "Write(.yottacode/YOTTACODE.md)"] } }
```

Add `**/.yottacode/permissions.local.json` to your `.gitignore` so
personal allow rules don't leak into the team-shared file.

A missing, empty, or whitespace-only `permissions.json` /
`permissions.local.json` is treated as "no rules" — yottacode no longer
fails to start when either file exists but has no content. Opening
either path from `/permissions` seeds the file with the full
`{allow, ask, deny}` skeleton before vim launches, so you always edit a
fully-shaped file instead of an empty buffer. Files that already have
content are never overwritten.

### Rule Prefixes

Each rule has the shape `<Tool>(<pattern>)`. Supported tool prefixes:

| Prefix | Applies to | Pattern matches against |
|---|---|---|
| `Bash` | `run_bash` | Full shell command text |
| `Read` | `read_file`, `read_many_files` | cwd-relative path (doublestar) |
| `Write` | `write_file` | cwd-relative path (doublestar) |
| `Edit` | `edit_file`, `apply_diff` | cwd-relative path (doublestar) |
| `Mkdir` | `mkdir` | cwd-relative path (doublestar) |
| `Copy` / `Move` / `Delete` | the same-named tools | path or `src -> dst` (string) |
| `List` | `list_dir`, `list_project_structure` | cwd-relative path (doublestar) |
| `Glob` / `Grep` | the same-named tools | pattern string |
| `Fetch` | `fetch_url` | URL (string) |
| `Git` | unified `git` + discrete `git_*` helpers | joined args (string) |
| `Github` | every `gh_*` tool (PR + issue surface) | canonical verb name (string) |
| `Memory` | `memory_save` / `memory_forget` | `op scope:name` (string) |
| `Tests` / `Rollback` | the same-named tools | empty descriptor (binary allow/deny) |

`Github(...)` descriptors are the canonical verb name extracted from
the tool name (independent of the resource-first tool naming so the
roadmap's `Github(read_*)` style works):

| Tool | Verb |
|---|---|
| `gh_pr_read` | `read_pr` |
| `gh_pr_review_context` | `read_pr_review_context` |
| `gh_pr_create` | `create_pr` |
| `gh_pr_update` | `update_pr` |
| `gh_pr_add_comment` | `add_pr_comment` |
| `gh_issue_read` | `read_issue` |
| `gh_issue_list` | `list_open_issues` |
| `gh_issue_create` | `create_issue` |

Wildcards work as in any other rule, so:

- `Github(read_*)` covers every read verb
- `Github(*_pr)` covers every PR-targeting verb
- `Github(*)` is the catch-all (use sparingly — `Allow` it and writes auto-approve)

Owner/repo scoping (`Github(create_pr owner/repo)`) is not yet
implemented — every call currently resolves against the cwd's git
remote. The roadmap tracks per-repo scoping for the cloud bot work
(SaaS Phase 2).

### Starter Rule Set

The default skeleton ships with empty arrays — yottacode is unopinionated
about which rules a project wants. The set below is a curated starting
point you can paste into `<repo>/.yottacode/permissions.json` and prune
to taste. Decision precedence is `Deny > Allow > Ask > Default`, so the
`deny` block always wins even if a broader `allow` is added later.

```json
{
  "permissions": {
    "allow": [
      "Bash(git status)",
      "Bash(git status *)",
      "Bash(git diff)",
      "Bash(git diff *)",
      "Bash(git log)",
      "Bash(git log *)",
      "Bash(git show *)",
      "Bash(git branch)",
      "Bash(git branch -*)",
      "Bash(git remote -v)",
      "Bash(ls)",
      "Bash(ls *)",
      "Bash(pwd)",
      "Bash(echo *)",
      "Bash(which *)",
      "Bash(head *)",
      "Bash(tail *)",
      "Bash(wc *)",
      "Github(read_*)",
      "Github(list_open_issues)"
    ],
    "ask": [
      "Bash(git push *)",
      "Bash(git reset --hard *)",
      "Bash(git rebase *)",
      "Bash(gh pr create *)",
      "Bash(gh pr merge *)",
      "Bash(gh pr close *)",
      "Bash(gh issue create *)",
      "Github(create_pr)",
      "Github(create_issue)",
      "Github(update_pr)",
      "Github(add_pr_comment)",
      "Bash(gh release *)",
      "Bash(npm publish*)",
      "Bash(cargo publish*)",
      "Bash(docker push *)",
      "Read(**/.env)",
      "Read(**/.env.*)",
      "Read(**/*.pem)",
      "Read(**/id_rsa)",
      "Read(**/credentials*)"
    ],
    "deny": [
      "Bash(rm -rf /*)",
      "Bash(rm -rf ~*)",
      "Bash(sudo rm -rf *)",
      "Bash(curl * | sh)",
      "Bash(curl * | bash)",
      "Bash(wget * | sh)",
      "Bash(wget * | bash)",
      "Bash(* | sudo sh)",
      "Bash(* | sudo bash)",
      "Bash(dd if=* of=/dev/*)",
      "Bash(mkfs.*)",
      "Bash(chmod -R 777 /)",
      "Bash(chmod -R 777 /*)",
      "Edit(/etc/**)",
      "Edit(/usr/**)",
      "Edit(/bin/**)",
      "Edit(/sbin/**)",
      "Edit(/boot/**)",
      "Write(/etc/**)",
      "Write(/usr/**)",
      "Delete(/etc/**)",
      "Delete(/usr/**)"
    ]
  }
}
```

Pattern semantics that catch new authors out:

- `*` matches the empty sequence too, so `Bash(rm -rf /*)` covers both
  `rm -rf /` and `rm -rf /home/user` with one rule — you don't need a
  separate `rm -rf /` entry.
- The space before `*` is literal: `Bash(git status *)` matches
  `git status -s` but not the bare `git status`. Add both forms when
  you want to cover the command with and without arguments.
- Path-typed rules (`Read`, `Write`, `Edit`, `Delete`, …) use
  doublestar; `Read(**/.env)` matches both top-level `.env` and nested
  `services/api/.env`.

Personal additions go in `permissions.local.json` (gitignored). A
common starter for a Go project:

```json
{
  "permissions": {
    "allow": [
      "Bash(go *)",
      "Bash(make *)",
      "Bash(gofmt *)",
      "Bash(goimports *)"
    ],
    "ask": [],
    "deny": []
  }
}
```

## MCP servers

yottacode is a client for Anthropic's Model Context Protocol. Each `[[mcp_servers]]` block in `~/.yottacode/config.toml` launches a subprocess at session start and registers its tools under the `mcp/<name>/<tool>` namespace.

```toml
[[mcp_servers]]
name    = "filesystem"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-filesystem", "/home/me/workspace"]

[[mcp_servers]]
name     = "github"
command  = "npx"
args     = ["-y", "@modelcontextprotocol/server-github"]
env      = { GITHUB_PERSONAL_ACCESS_TOKEN = "$GITHUB_PAT" }
```

`env` values support `$VAR` substitution from yottacode's process environment so secrets stay out of the config file. v1 supports stdio transport only. See [`mcp.md`](mcp.md) for the full reference, including permission rules (`MCP(...)`), the `/mcp` slash command, and a curated server list.

## Subagents

The `[subagents]` block bounds cumulative subagent spend per session —
a backstop against an enthusiastic (or adversarial) prompt fanning out
unbounded child loops on your API key. The per-child iteration cap and
the concurrency cap bound one wave; this bounds the session total.

```toml
[subagents]
session_token_budget = 8000000   # estimated tokens; default 8M
```

The figure is in **estimated** tokens (the same 4-chars-per-token
heuristic the status bar uses), counted across every finished subagent
in the session. Once the budget is exhausted, new spawns return a
recoverable error to the model instead of running. The cap is always
on: values `<= 0` fall back to the 8M default rather than disabling it
— a deliberate floor, since the budget exists precisely for sessions
nobody is watching. Completed spend is counted, so one in-flight wave
can overshoot by at most the concurrency cap.

See [subagents.md](subagents.md) for the agent types, background
dispatch, and `notify_on_done` wake semantics this budget guards.

## Model routing

The `[router]` block hosts two independent, opt-in features.

**Cache-safe task routing** runs isolated work (subagents, history
compaction) on a cheap model while your main conversation stays on your
chosen model — a pure cost saving with no prompt-cache churn:

```toml
[router]
  mode        = "auto"                          # off | manual | auto (default off)
  fast_model  = "anthropic:claude-haiku-4-5"
  smart_model = "anthropic:claude-opus-4-6"
```

- `mode = "off"` (or absent) — disabled; fully backward compatible.
- `mode = "manual"` — only routes subagents that declare an explicit `model:`.
- `mode = "auto"` — also routes read-only/search subagents and summarization to `fast_model`.

`fast_model` / `smart_model` are required when `mode` is not `off` and
use the `"<provider>"` or `"<provider>:<model>"` grammar; the model must
exist in that provider's `models`. See [`models.md`](models.md#cache-safe-task-routing)
for the cost rationale and the auto heuristic.

**Multi-provider failover** (separate feature, same block) dispatches
each main-thread turn across an ordered candidate list, falling through
on early failure:

```toml
[router]
  enabled                  = true
  policy                   = "fallback-chain"   # fallback-chain | cheap-first
  candidates               = ["anthropic:claude-haiku-4-5", "openai:gpt-4o"]
  health_window_seconds    = 60
  health_failure_threshold = 3
```

The two are orthogonal: `enabled`/`candidates` control failover across
providers; `mode`/`fast_model`/`smart_model` control task routing. You
can set either, both, or neither.

## Runtime Reconfiguration

The TUI supports changing the active session configuration without restarting:

- `/model <name>`
- `/provider` (use `/provider use <name>` to swap endpoint + key in one step)
- `/doctor`
- `/permissions` (prints the shared + local rule file paths; edit either file directly)
- `/memory` (edit `USER.md` / `YOTTACODE.md`, browse user-scope and project-scope memories)

These changes apply to the current session only. They do not rewrite your
shell configuration or future launch defaults.
