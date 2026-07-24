# LSP Code Intelligence

`lsp_code_intelligence` is an experimental, opt-in bridge from yottacode tools to local Language Server Protocol (LSP) servers. It adds editor-like read-only code navigation without making yottacode an IDE or installing anything on the user's machine. Interactive and oneshot sessions reuse a bounded pool of initialized servers so repeated tool calls do not pay startup cost every time; the pool is closed when the session exits.

Enable it with any experimental-feature path:

```bash
yottacode --experimental lsp_code_intelligence
# or
export YOTTACODE_EXPERIMENTAL=lsp_code_intelligence
```

Or in `~/.yottacode/config.toml`:

```toml
[experimental]
lsp_code_intelligence = true
```

## Supported languages

| Language | Server command | Install hint |
|---|---|---|
| Go | `gopls` | `go install golang.org/x/tools/gopls@latest` and ensure `$(go env GOPATH)/bin` is on `PATH` |
| TypeScript/JavaScript | `typescript-language-server --stdio` | `npm install -g typescript typescript-language-server` |
| Python | `pyright-langserver --stdio` | `npm install -g pyright` |
| Rust | `rust-analyzer` | Install with `rustup`, your package manager, or the rust-analyzer project instructions |

Missing servers are not fatal. yottacode reports the missing command and an install hint through `lsp_status`, `/lsp`, `/health`, and `/config-doctor`.

## Tools

When the feature flag is enabled, yottacode registers these read-only tools:

| Tool | Purpose |
|---|---|
| `lsp_status` | Detect supported languages in the workspace and show server availability/install hints |
| `lsp_symbols` | Search workspace symbols through LSP, with a regex fallback when no server is installed |
| `lsp_definition` | Find definition locations for a file position |
| `lsp_references` | Find reference locations for a file position |
| `lsp_diagnostics` | Return compile/type diagnostics for a source file |
| `lsp_hover` | Show hover/type/documentation text for a source position |
| `lsp_code_actions` | List quick fixes/refactors for a range without applying them |
| `lsp_call_hierarchy` | Show incoming/outgoing call hierarchy for a source position |

Positions are zero-based line and UTF-16 character offsets, matching LSP. Output locations are rendered one-based as `path:line:column` for terminal readability.

`lsp_code_actions` is intentionally read-only. It lists actions but does not apply edits; applying language-server edits would require a separate approval-gated tool.

## Slash commands

| Command | Purpose |
|---|---|
| `/lsp [path]` | Show detected languages, server commands, installed/missing state, and install hints |
| `/health` | Show git/provider/memory/LSP/MCP/experimental status in one workspace card |
| `/config-doctor` | Validate local dependencies such as LSP commands, MCP commands, and memory embeddings |
| `/handoff` | Print a concise session handoff-note template |

## Command overrides

If the server binary is not on `PATH`, or you need wrapper arguments, configure per-language commands:

```toml
[lsp.servers]
go = ["/opt/homebrew/bin/gopls"]
typescript = ["typescript-language-server", "--stdio"]
python = ["pyright-langserver", "--stdio"]
rust = ["rust-analyzer"]
```

Valid keys are `go`, `typescript`, `python`, and `rust`. Overrides execute directly without a shell; the first array item is the binary checked and launched.

## Production hardening behavior

The experimental bridge now includes several production-readiness behaviors:

- Workspace roots are detected from language markers such as `go.mod`, `package.json`, `pyproject.toml`, and `Cargo.toml` instead of always using the file's directory.
- Documents are opened with `textDocument/didOpen` before position-based requests so servers see the same file contents yottacode read from disk.
- Server capabilities returned by `initialize` are checked before optional methods; unsupported methods return an explicit `unavailable` result rather than a misleading empty response.
- `lsp_status`, `/lsp`, and `/health` expose manager stats: open servers, starts, reuses, evictions, and last startup latency.
- Smoke tests exist for all supported servers and skip automatically when the binary is not installed.

## Troubleshooting

1. Run `/lsp` to confirm yottacode detects your language and command.
2. Run `/config-doctor` to find missing binaries or broken override names.
3. Confirm the server works outside yottacode, for example `gopls version` or `pyright-langserver --version`.
4. If a workspace symbol query fails, yottacode may fall back to an approximate regex symbol index. Definition, references, diagnostics, hover, code actions, and call hierarchy require a real server.

## Production promotion checklist

Before graduating this feature from experimental to default-on, verify:

- Real-server smoke tests pass for `gopls`, `typescript-language-server`, `pyright-langserver`, and `rust-analyzer`.
- Workspace root detection picks the expected language project root for nested files.
- Server capability checks report unsupported optional methods clearly.
- Cancellation/timeouts kill server subprocesses without leaks.
- Diagnostics, code actions, and call hierarchy outputs are bounded and readable.
- Path validation applies to every file/workspace argument.
- Custom server commands are documented as local user-configured execution.
- `/lsp`, `/health`, and `/config-doctor` give actionable setup hints.
