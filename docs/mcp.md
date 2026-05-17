# MCP servers

yottacode is a client for Anthropic's [Model Context Protocol](https://modelcontextprotocol.io/). Configure an MCP server in `~/.yottacode/config.toml` and its tools register alongside yottacode's native tools — same approval modal, same permission rules, same dispatch path. The model doesn't know (or care) which tools are native vs MCP.

## What v1 ships

v1 covers the part of the MCP ecosystem most users actually need:

- **Stdio transport.** yottacode launches each MCP server as a subprocess at session start and talks JSON-RPC over stdin/stdout. Covers every server published as `@modelcontextprotocol/server-*` and most community servers.
- **Tools only.** MCP's `resources` and `prompts` primitives are deferred — they're rare in practice and add real surface. Most servers ship only tools anyway.
- **Per-tool approval.** Each MCP tool defaults to the approval modal. Servers can hint a tool as read-only (`annotations.readOnlyHint: true`) and yottacode honors that — read-only tools auto-execute. Users can still elevate trust with explicit permission rules.
- **Subprocess sandboxing inherited from your OS.** yottacode does not run MCP servers in an isolated container. Each subprocess inherits yottacode's environment and filesystem permissions. Treat MCP servers as you'd treat any binary you choose to run.

Deferred to follow-ups: HTTP/SSE transport (for container-hosted servers with bearer-token auth), MCP resources, MCP prompts, OAuth2 device-flow auth, and clean `/mcp restart` (currently the workaround is to restart the yottacode session).

## Configuration

Add one `[[mcp_servers]]` block per server in `~/.yottacode/config.toml`. Each entry needs at minimum a `name` and a `command`:

```toml
[[mcp_servers]]
name    = "filesystem"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-filesystem", "/home/me/workspace"]

[[mcp_servers]]
name     = "memory"
command  = "npx"
args     = ["-y", "@modelcontextprotocol/server-memory"]
env      = { MEMORY_FILE = "$HOME/.yottacode/mcp-memory.json" }

[[mcp_servers]]
name     = "github"
command  = "npx"
args     = ["-y", "@modelcontextprotocol/server-github"]
env      = { GITHUB_PERSONAL_ACCESS_TOKEN = "$GITHUB_PAT" }
disabled = false
```

Fields:

| Field | Required | Notes |
|---|---|---|
| `name` | yes | Unique. Lowercase letters, digits, `-`, `_`. Must start with a letter. Used in tool names and `/mcp`. |
| `command` | yes | Executable (resolved via `PATH` at session start). |
| `args` | no | Forwarded verbatim. |
| `env` | no | Per-server env vars layered on top of yottacode's inherited environment. Values support `$VAR` substitution from the process env — keep secrets out of the config file. |
| `disabled` | no | `true` skips this entry at session start. |

Unknown keys are rejected at load time so typos like `trnsport` surface immediately instead of silently disabling a feature.

## Tool namespacing

Every MCP tool registers as `mcp/<server>/<tool>`. So the GitHub server's `create_pull_request` becomes `mcp/github/create_pull_request` — distinct from yottacode's native `gh_pr_create`. The namespacing also makes permission rules predictable; see below.

## Approval and permissions

Default policy: the approval modal fires on every MCP tool call. If the server explicitly hinted a tool as read-only, yottacode skips the modal — that's a deliberate trust delegation to the server, not a security guarantee. Treat server-declared annotations as suggestions, not contracts.

Elevate trust (or restrict it) with `MCP(<server>/<tool>)` rules in `.yottacode/permissions.json` or `.yottacode/permissions.local.json`:

```json
{
  "permissions": {
    "allow": ["MCP(filesystem/read_*)", "MCP(memory/*)"],
    "ask": ["MCP(github/*)"],
    "deny": ["MCP(*/delete_*)"]
  }
}
```

Precedence is the same as native tools: `deny` > `ask` > `allow` > default. `MCP(*)` is a valid pattern (covers everything) but you almost never want it — narrow rules per server are cheap to maintain and minimize blast radius.

## `/mcp` slash command

| Command | Effect |
|---|---|
| `/mcp` | List configured servers, their start status, and tool count. |
| `/mcp logs <name>` | Show the last ~200 lines of the server's stderr (helpful when a server crashes during init or under load). |
| `/mcp restart <name>` | Stop the named subprocess, respawn it from the stored config, and swap the registered tools to the fresh client. The tool surface may shrink or grow if the server's catalog changed between generations. |

Editing `config.toml` mid-session still requires a full yottacode restart — `/mcp restart` rebuilds from the *originally loaded* config, not from disk.

## Non-text tool results

MCP servers can return image, audio, resource-link, or embedded-resource content alongside (or instead of) text. yottacode's v1 bridge passes **text content through verbatim**; non-text blocks are replaced with explicit placeholder markers like `[image omitted: image/png, 1024 bytes — yottacode v1 tools-only bridge passes text only]`. The model sees the marker and learns the call succeeded but the data wasn't text — far safer than receiving a silent empty string and retrying or hallucinating.

If a server uses MCP's `structuredContent` field (typed JSON output) without populating `content`, the bridge JSON-marshals the structured payload as a fallback so it isn't lost.

Full multi-modal passthrough (image / audio bytes forwarded to a vision-capable model adapter) is deferred to a follow-up wedge.

## What yottacode does NOT track

MCP tools mutate state outside yottacode's file model (databases, external APIs, filesystem-via-server). yottacode's checkpoint system (`/checkpoints`, `Esc Esc`) cannot snapshot or restore MCP-driven changes — same limitation as `run_bash`. If you need rollback, use the underlying system's own mechanisms (database transactions, git, etc.).

## Curated v1 test servers

Useful starting points; each is published as an `npx`-runnable npm package.

| Server | Args | Exercises |
|---|---|---|
| `@modelcontextprotocol/server-filesystem` | `-y ... /path/to/workspace` | Stdio baseline, mixed read/write, approval modal on writes. |
| `@modelcontextprotocol/server-memory` | `-y ...` | Stateful subprocess for the session lifetime. |
| `@modelcontextprotocol/server-fetch` | `-y ...` | One read-only tool, minimal-server conformance. |
| `@modelcontextprotocol/server-sqlite` | `-y ... /path/to/test.db` | Mutation approval against a local file resource. |

## Subprocess cleanup on hard exit

On a graceful yottacode shutdown (TUI quits, Ctrl+C handled), MCP subprocesses receive a `shutdown` notification, then SIGTERM with a 3s grace, then SIGKILL. On Linux, yottacode also sets `PR_SET_PDEATHSIG = SIGTERM` so the kernel signals the children automatically if yottacode itself dies ungracefully (SIGKILL, panic, crash). On macOS and BSD there's no equivalent prctl in the Go stdlib — an ungraceful yottacode exit on those platforms can leave MCP subprocesses parented to launchd / init. `ps`/`pgrep` + `kill` is the manual cleanup path there.

## Troubleshooting

- **`command not found` at session start.** yottacode resolves `command` via `PATH`. Use an absolute path if the binary lives elsewhere.
- **Server crashes during init.** Check `/mcp logs <name>` for the stderr — npm install errors, missing env vars, or bad args show up there.
- **Tool appears in `/mcp` but the model never calls it.** The schema may be malformed; yottacode passes it through verbatim, and some models refuse tools they can't parse. Re-check the server's `tools/list` output.
- **Stale tools after editing config.** Restart the yottacode session. v1 doesn't reload `config.toml` mid-session.
