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
| `--reasoning-effort` | `YOTTACODE_REASONING_EFFORT` | no | Hint for supported reasoning models: `low`, `medium`, or `high` |
| `--enable-web-search` | `YOTTACODE_ENABLE_WEB_SEARCH` | no | Enable provider-native web search when supported |
| `--disable-web-search` | `YOTTACODE_DISABLE_WEB_SEARCH` | no | Disable provider-native web search even when OpenAI/xAI would enable it by default |
| `--enable-x-search` | `YOTTACODE_ENABLE_X_SEARCH` | no | Enable xAI `x_search` when supported |
| `--enable-code-interpreter` | `YOTTACODE_ENABLE_CODE_INTERPRETER` | no | Enable provider-native code interpreter when supported |
| `--search-allowed-domains` | `YOTTACODE_SEARCH_ALLOWED_DOMAINS` | no | Comma-separated allowlist for provider-native web search |
| `--search-excluded-domains` | `YOTTACODE_SEARCH_EXCLUDED_DOMAINS` | no | Comma-separated blocklist for provider-native web search |
| `--x-search-allowed-handles` | `YOTTACODE_X_SEARCH_ALLOWED_HANDLES` | no | Comma-separated X handle allowlist for xAI `x_search` |
| `--x-search-excluded-handles` | `YOTTACODE_X_SEARCH_EXCLUDED_HANDLES` | no | Comma-separated X handle blocklist for xAI `x_search` |
| `--x-search-from-date` | `YOTTACODE_X_SEARCH_FROM_DATE` | no | Inclusive lower bound for xAI `x_search` in `YYYY-MM-DD` form |
| `--x-search-to-date` | `YOTTACODE_X_SEARCH_TO_DATE` | no | Inclusive upper bound for xAI `x_search` in `YYYY-MM-DD` form |
| `--system` | — | no | Override the default system prompt |
| `--resume` | — | no | Resume a session by id or name |
| `--bypass-permissions` | — | no | DANGEROUS: auto-approve every tool call without prompting (`deny` rules in `permissions.json` still apply). Renamed from the deprecated `--yolo`; no compatibility alias |
| `--max-iterations` | — | no | Tool-call cap per turn; defaults to `25` |
| `--allow-paths` | `YOTTACODE_ALLOW_PATHS` | no | Comma-separated extra write roots in addition to the current working directory |

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

# Anthropic
export YOTTACODE_PROVIDER=anthropic
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=https://api.anthropic.com
export YOTTACODE_API_KEY=sk-ant-...
yottacode

# xAI with default web_search + explicit x_search
export YOTTACODE_PROVIDER=xai
export YOTTACODE_MODEL=<your-model-id>
export YOTTACODE_BASE_URL=https://api.x.ai/v1
export YOTTACODE_API_KEY=xai-...
export YOTTACODE_SEARCH_ALLOWED_DOMAINS=docs.x.ai,arxiv.org
export YOTTACODE_ENABLE_X_SEARCH=1
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
`YOTTACODE_DISABLE_WEB_SEARCH=1`.

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
  sessions/<id>.json             saved conversations
  index.sqlite                   FTS5 index for /recall
  USER.md                        optional global user memory (human-only)
  memory/<name>.md               agent-managed user-scope memories
  memory/MEMORY.md               auto-generated index of user-scope memories
  projects/<slug>/memory/        agent-managed project-scope memories (per-user)
  config.toml                    tunables (context watermarks, retrieval)
```

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

## Runtime Reconfiguration

The TUI supports changing the active session configuration without restarting:

- `/model <name>`
- `/provider` (use `/provider use <name>` to swap endpoint + key in one step)
- `/doctor`
- `/permissions` (prints the shared + local rule file paths; edit either file directly)
- `/memory` (edit `USER.md` / `YOTTACODE.md`, browse user-scope and project-scope memories)

These changes apply to the current session only. They do not rewrite your
shell configuration or future launch defaults.
