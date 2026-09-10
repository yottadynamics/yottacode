# MCP servers

yottacode is a client for Anthropic's [Model Context Protocol](https://modelcontextprotocol.io/). Configure an MCP server in `~/.yottacode/config.toml` and its tools register alongside yottacode's native tools — same approval modal, same permission rules, same dispatch path. The model doesn't know (or care) which tools are native vs MCP.

## What v1 ships

v1 covers the part of the MCP ecosystem most users actually need:

- **Stdio, streamable HTTP, and legacy SSE transports.** yottacode can launch a server as a local subprocess (stdio, talking JSON-RPC over stdin/stdout — covers every server published as `@modelcontextprotocol/server-*` and most community servers) or connect to a remote server over streamable HTTP or SSE.
- **OAuth 2.1 for streamable-HTTP servers.** `auth = "oauth"` runs the full authorization-code + PKCE flow (RFC 8707 resource indicators, RFC 9728 protected-resource discovery) against servers that require interactive sign-in instead of a static token — see "OAuth authentication" below. Not available over the legacy `sse` transport.
- **Non-blocking startup.** MCP servers initialize in the background after the TUI renders. A slow server (e.g. `npx -y` downloading a package for the first time) does not block the prompt. `/mcp` shows "starting..." for servers still initializing; tools register automatically once they come up.
- **Tools only.** MCP's `resources` and `prompts` primitives are deferred — they're rare in practice and add real surface. Most servers ship only tools anyway. Elicitation and sampling are deferred too: yottacode never advertises either capability, so a server that tries one gets a clean "client does not support" error immediately — it can't hang a session waiting on a response yottacode will never send.
- **Per-tool approval — annotations are advisory, not a bypass.** Each MCP tool defaults to the approval modal. A server's `readOnlyHint` annotation can skip the modal only when you've explicitly opted a server into `trust_annotations` (see "Global MCP policy" below) — and only for tools the server also doesn't mark destructive or open-world. A tool with no annotations at all is treated as destructive and always prompts. Users can still elevate or restrict trust with explicit `MCP(...)` permission rules.
- **Subprocess sandboxing inherited from your OS.** yottacode does not run MCP servers in an isolated container. Each subprocess inherits yottacode's environment and filesystem permissions. Treat MCP servers as you'd treat any binary you choose to run.

Deferred to follow-ups:

- **MCP resources and prompts** — rarely used in practice; most servers only expose tools.

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

[[mcp_servers]]
name      = "linear"
transport = "http"
url       = "https://mcp.linear.app/mcp"
headers   = { Authorization = "Bearer $LINEAR_API_KEY" }
include   = ["get_*", "list_*", "create_issue"]
exclude   = ["delete_*"]

[[mcp_servers]]
name                = "gmail"
transport           = "http"
url                 = "https://gmailmcp.googleapis.com/mcp/v1"
auth                = "oauth"
oauth_client_id     = "$GOOGLE_MCP_CLIENT_ID"
oauth_client_secret = "$GOOGLE_MCP_CLIENT_SECRET"
oauth_scopes        = ["https://www.googleapis.com/auth/gmail.readonly"]
```

Fields:

| Field | Required | Notes |
|---|---|---|
| `name` | yes | Unique. Lowercase letters, digits, `-`, `_`. Must start with a letter. Used in tool names and `/mcp`. |
| `transport` | no | `""` or `"stdio"` (default) spawns `command` as a subprocess. `"http"` or `"sse"` dials `url` instead. |
| `command` | stdio only | Executable (resolved via `PATH` at session start). |
| `args` | no | Forwarded verbatim. stdio only. |
| `env` | no | Per-server env vars layered on top of yottacode's inherited environment. Values support `$VAR` substitution from the process env — keep secrets out of the config file. stdio only. |
| `url` | http/sse only | The server's HTTP/SSE endpoint. Subject to the transport security policy below — see "Remote transport security". |
| `headers` | no | Sent on every outgoing request — e.g. an `Authorization` bearer token. Values support `$VAR` substitution the same way `env` does; the expanded value is never written back to `config.toml`. http/sse only. |
| `tls_ca_file` | no | Path to a PEM CA bundle used in addition to the system roots when verifying `url`'s certificate. Does not disable certificate verification or `require_tls`. http/sse only. |
| `auth` | no | `""`/`"static-header"` (default) authenticates via `headers`. `"oauth"` runs the OAuth 2.1 flow instead — see "OAuth authentication" below. `"none"` documents that the server needs no credential. http/sse only; `"oauth"` additionally requires `transport = "http"` (not `"sse"`). |
| `oauth_client_id` | `auth = "oauth"` | This MCP client's ID, pre-registered with the server's authorization server. `$VAR`-substituted like `headers`. |
| `oauth_client_secret` | no | Paired client secret, when the authorization server issued one. `$VAR`-substituted; empty is valid for a true public client. |
| `oauth_scopes` | no | Explicit scope list to request instead of the server's full advertised set. |
| `include` / `exclude` | no | Glob lists (`path.Match` syntax, e.g. `"get_*"`) narrowing the registered tool catalog. `include` keeps only matches (empty = everything); `exclude` then drops matches. `/mcp` and `/mcp tools <name>` show the hidden count. |
| `disabled` | no | `true` skips starting this entry at session start, but it stays visible in `/mcp` with a `disabled` badge — use `/mcp enable <name>` to start it without editing `config.toml`. |

Unknown keys are rejected at load time so typos like `trnsport` surface immediately instead of silently disabling a feature.

## Global MCP policy

A `[mcp]` block (separate from the per-server `[[mcp_servers]]` list) controls how much yottacode trusts server-declared annotations and bounds every call:

```toml
[mcp]
approval_mode        = "allow-readonly"       # "ask" (default) | "allow-readonly" | "yolo-inherit"
trust_annotations     = ["filesystem", "memory"]
call_timeout_seconds  = 60
max_result_bytes      = 262144
require_tls           = true                  # default; set false only to permit plaintext remote servers
allowed_hosts         = ["mcp.linear.app"]     # empty (default) = any https host
```

| Field | Default | Notes |
|---|---|---|
| `approval_mode` | `"ask"` | `"ask"` ignores `readOnlyHint` entirely — every MCP tool prompts. `"allow-readonly"` skips the modal for a tool that is read-only, not open-world, and belongs to a server listed in `trust_annotations`. `"yolo-inherit"` is accepted for compatibility with the global `--yolo` flag. |
| `trust_annotations` | `[]` | Server names (from `[[mcp_servers]].name`) whose `readOnlyHint` may be honored under `allow-readonly`. |
| `call_timeout_seconds` | `60` | Per-call backstop, independent of the parent turn's own cancellation. `0` disables it. |
| `max_result_bytes` | `262144` | Caps a tool's flattened result before it reaches the model; longer results are truncated with a `[truncated: ...]` marker. |
| `require_tls` | `true` | Rejects `http://` URLs for remote servers except to a loopback host (`127.0.0.1`, `localhost`, `::1`). See "Remote transport security". |
| `allowed_hosts` | `[]` | Exact-match host allowlist for remote servers (no `*.` globs in v1). Empty means any `https` host is reachable. |

**Safe-by-default rule**: a tool with no server-declared annotations at all is treated as destructive and open-world, so it always prompts — `trust_annotations` cannot change that. Only a tool the server explicitly marks non-destructive, non-open-world, and read-only is eligible to skip the modal, and only under `allow-readonly`.

## Remote transport security

`require_tls` and `allowed_hosts` (above) are enforced both when a remote server connects and when you add one via CLI or `/mcp add` — a bad URL fails at add-time, not at first use. A few more guarantees for `http`/`sse` servers:

- **No cross-host redirects.** A remote server's response can't redirect the connection to a different host; a same-host redirect is still subject to `require_tls`.
- **No `insecure_skip_verify` config key, ever.** Certificate verification cannot be disabled from `config.toml`.
- **Isolated transports.** Each remote server gets its own `http.Transport` (own connection pool, own cookie jar — none), not yottacode's process-wide default.
- **DNS-rebinding protection.** For a server configured by hostname (not a literal IP or loopback address), every connection — including each hop of a redirect — is refused if the hostname resolves to a private, link-local, loopback, or unspecified address. This defeats a hostname that resolves to a legitimate public endpoint when you add it but later resolves to an internal address, which would otherwise turn a trusted configured server into a way to probe your local network. A server you configure by literal IP (public or private) is unaffected — there's no DNS layer to rebind.
- **Redaction.** `Bearer <token>`, `Authorization:` headers, `token=` query params, and `Set-Cookie:` values are scrubbed to `***` before they can reach `/mcp logs`, a start-error message, or the transcript.
- **`$VAR` in `headers`** is expanded the same way as `env` — against yottacode's process environment, with unresolved references surfaced as a startup warning. The expanded value is never written back to `config.toml`.

`auth = "static-header"` (the default) is **trusted-vendor PAT over HTTPS** — a static bearer token or API key in a header, good for ~95% of MCP servers. `auth = "oauth"` is the OAuth-grade option for servers that require it — see the next section.

## OAuth authentication

`auth = "oauth"` runs the OAuth 2.1 authorization-code + PKCE flow against the server's real authorization server, built on the pinned `github.com/modelcontextprotocol/go-sdk`'s `auth`/`oauthex` packages (RFC 8707 resource indicators, RFC 9728 protected-resource metadata discovery, refresh tokens). This is the mode Google's official Workspace MCP servers — Gmail, Drive, Docs, Sheets, Slides, Calendar, Chat (`developers.google.com/workspace/guides/configure-mcp-servers`) — require; the config example above targets Gmail.

**You register your own OAuth client.** yottacode does not ship a bundled client ID — create a "Desktop app" (installed-application) OAuth 2.0 client in the vendor's console (e.g. Google Cloud console for Workspace servers) and put its client ID (and secret, if issued) in `oauth_client_id`/`oauth_client_secret`. A Desktop-app client accepts a loopback redirect on any port, which is what the sign-in flow uses; register your own rather than reusing a client meant for a different application (e.g. Claude's own `https://claude.ai/api/mcp/auth_callback` redirect only works for claude.ai).

Sign in with:

```
/mcp auth gmail          # or: yottacode mcp auth gmail
```

This opens your browser (best-effort — the URL is also printed so a headless or remote session can complete sign-in by pasting it elsewhere), waits for the redirect on a fixed local port, exchanges the code, and persists the resulting access/refresh token to `~/.yottacode/mcp-auth/<name>.json` (mode `0600`, one file per server, never written to `config.toml` or logged). A later session reuses the persisted refresh token automatically — you only need to run `/mcp auth` again after `/mcp logout` or if the server revokes access.

```
/mcp logout gmail        # or: yottacode mcp logout gmail
```

Deletes the persisted token. `/mcp auth <name>` is the **only** place sign-in runs interactively — a session-startup connection or a tool call that finds no valid token never opens a browser or blocks on you; it fails immediately with a message pointing at `/mcp auth <name>`, and the server shows as `failed` in `/mcp` until you run it. This also covers a token that stops working mid-session (revoked access, expired refresh token): the next call to that server fails the same clean way rather than popping a browser mid-turn.

**Google specifically:** Google doesn't support the MCP spec's `offline_access`-scope convention for requesting a refresh token — it uses `access_type=offline&prompt=consent` query parameters instead, which yottacode adds automatically when it detects a Google authorization endpoint. If you don't see a refresh token persist (a `/mcp auth` a few hours later re-prompts every time), your OAuth client's Google Cloud project consent screen may still be in "Testing" status — sensitive/restricted scopes like Gmail's `gmail.modify` need Google's app-verification review to work for any account beyond the ~100 test users you list explicitly.

Out of scope: MCP elicitation and sampling (see "What v1 ships") are unrelated protocol features some servers use for mid-session interactive prompts — yottacode still rejects both unconditionally regardless of `auth` mode.

## Tool namespacing

Every MCP tool registers as `mcp/<server>/<tool>`. So the GitHub server's `create_pull_request` becomes `mcp/github/create_pull_request` — distinct from yottacode's native `pr_create`. The namespacing also makes permission rules predictable; see below.

## Approval and permissions

Default policy (`approval_mode = "ask"`): the approval modal fires on every MCP tool call regardless of server-declared annotations. Treat server-declared annotations as suggestions, not contracts — see "Global MCP policy" above for the opt-in `allow-readonly` mode.

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

Precedence is the same as native tools: `deny` > `ask` > `allow` > default. Two extra guards apply only to MCP rules:

- A **destructive tool can't be auto-allowed by a glob.** `MCP(github/*)` will never silently cover a tool the server marked destructive — only an exact `MCP(github/create_issue)`-style rule can. A glob match against a destructive tool falls back to the normal approval prompt.
- **Persisting `MCP(*)` requires an explicit second confirmation.** It's a valid pattern (covers every server and tool) but the blast radius is large enough that saving it to `permissions.json` from an interactive "always allow" prompt asks you to confirm twice. This doesn't apply to a session-only allow (`[S]` in the approval modal), and it can't stop you from hand-editing `permissions.json` directly — the `/permissions` lint warning is your signal there.

The approval modal's "always allow" and "allow for this session" options are available for MCP tools the same way they are for native tools.

## `/mcp` slash command

| Command | Effect |
|---|---|
| `/mcp` | List configured servers — including disabled ones with a `disabled` badge — their start status, and tool count. |
| `/mcp add <name> --command <cmd> [args...]` | Add a stdio server to `config.toml`, start it immediately, and register its tools — no restart needed. Everything after `--command` is the executable + arguments. |
| `/mcp add <name> --transport http\|sse --url <url> [--header K=V]... [--env K=V]... [--disabled]` | Add a remote server. `--header`/`--env` are repeatable. `--disabled` adds the entry without starting it. |
| `/mcp remove <name>` | Remove a server from `config.toml`. Requires a restart to take effect. |
| `/mcp logs <name>` | Show the last ~200 lines of the server's log output (connect errors, HTTP status, stderr for stdio) — secrets redacted. Helpful when a server crashes during init or under load. |
| `/mcp restart <name>` | Stop the named client, respawn it from the stored config, and swap the registered tools to the fresh client. The tool surface may shrink or grow if the server's catalog changed between generations. |
| `/mcp enable <name>` | Start a disabled server and register its tools, without a session restart. |
| `/mcp disable <name>` | Stop a server and mark it disabled, without removing it from `config.toml`. |
| `/mcp tools <name>` | Read-only inspector: lists the server's full catalog with a shown/hidden marker per tool, reflecting `include`/`exclude`. |
| `/mcp auth <name>` | Run the OAuth 2.1 sign-in flow for an `auth = "oauth"` server and persist the resulting token. See "OAuth authentication" above. |
| `/mcp logout <name>` | Delete a server's persisted OAuth token. |

Examples:

```
/mcp add podman --command npx -y podman-mcp-server@latest
/mcp add filesystem --command npx -y @modelcontextprotocol/server-filesystem /home/me/workspace
/mcp add excalidraw --command node /path/to/excalidraw-mcp/dist/index.js --stdio
/mcp add linear --transport http --url https://mcp.linear.app/mcp --header "Authorization=Bearer $LINEAR_API_KEY"
/mcp add gmail --transport http --url https://gmailmcp.googleapis.com/mcp/v1 \
    --auth oauth --oauth-client-id "$GOOGLE_MCP_CLIENT_ID" --oauth-client-secret "$GOOGLE_MCP_CLIENT_SECRET" \
    --oauth-scope https://www.googleapis.com/auth/gmail.readonly
/mcp auth gmail
```

## Non-text tool results

MCP servers can return image, audio, resource-link, or embedded-resource content alongside (or instead of) text. yottacode's v1 bridge passes **text content through verbatim**; non-text blocks are replaced with explicit placeholder markers like `[image omitted: image/png, 1024 bytes — yottacode v1 tools-only bridge passes text only]`. The model sees the marker and learns the call succeeded but the data wasn't text — far safer than receiving a silent empty string and retrying or hallucinating.

If a server uses MCP's `structuredContent` field (typed JSON output) without populating `content`, the bridge JSON-marshals the structured payload as a fallback so it isn't lost.

Full multi-modal passthrough (image / audio bytes forwarded to a vision-capable model adapter) is deferred to a follow-up wedge.

## What yottacode does NOT track

MCP tools mutate state outside yottacode's file model (databases, external APIs, filesystem-via-server). yottacode's checkpoint system (`/checkpoints`, `Esc Esc`) cannot snapshot or restore MCP-driven changes — same limitation as `run_bash`. If you need rollback, use the underlying system's own mechanisms (database transactions, git, etc.).

## Locally-built servers

Some MCP servers are not published as npm packages and must be cloned and built locally. Use `node` (or the appropriate runtime) as the `command` and point `args` at the built entrypoint:

```toml
[[mcp_servers]]
name    = "excalidraw"
command = "node"
args    = ["/home/me/excalidraw-mcp/dist/index.js", "--stdio"]
```

Build steps vary per server — check the server's README. Typical pattern for Node.js servers:

```bash
git clone https://github.com/<org>/<server>.git
cd <server>
pnpm install && pnpm run build
```

Then point `args` at the built `dist/index.js` (or equivalent). Most stdio servers accept a `--stdio` flag to select that transport.

## Curated test servers

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
- **Stale tools after editing config.** Use `/mcp restart <name>` to reload from the originally-loaded config, or restart the yottacode session to pick up `config.toml` changes from disk.
- **`mcp policy: ... not allowed` at add time or connect time.** The URL failed `require_tls` (plaintext to a non-loopback host) or isn't in `allowed_hosts`. Fix the URL, switch to `https://`, or adjust the `[mcp]` policy block.
