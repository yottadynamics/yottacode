# LSP Code Intelligence

`lsp_code_intelligence` is an experimental, opt-in bridge from yottacode tools to local Language Server Protocol (LSP) servers. It adds editor-like read-only code navigation without making yottacode an IDE or installing anything on the user's machine. Interactive and oneshot sessions reuse a bounded pool of initialized servers so repeated tool calls do not pay startup cost every time; the pool is closed when the session exits.

The separate experimental `code_map` feature reuses this LSP surface when available to build the `/map` structure overlay and code-map agent tools. If a language server is missing, the map falls back to approximate regex symbols so the outline still works. Dependency and impact queries currently use resolvable in-workspace Go imports, including transitive dependents, import-cycle detection, and Mermaid diagram output; deeper reference/call graph views remain out of scope until reference/call edges are indexed and tested.

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

Missing servers are not fatal. yottacode reports the missing command and an install hint through the startup session advisory card, `lsp_status`, `yottacode doctor`, and LSP tool unavailable results.

## Tools

When the feature flag is enabled, yottacode registers these LSP tools. Most are read-only; `lsp_apply_workspace_edit` is the approval-gated mutation step for previously previewed server edits:

| Tool | Purpose |
|---|---|
| `lsp_status` | Detect supported languages in the workspace and show server availability/install hints |
| `lsp_symbols` | Search workspace symbols through LSP, with a regex fallback when no server is installed |
| `lsp_document_symbols` | List structural symbols declared in one source file |
| `lsp_document_highlights` | Show current-file symbol reads/writes/text occurrences for a source position |
| `lsp_selection_ranges` | Show nested syntax ranges around a source position, from expression to enclosing block/function |
| `lsp_definition` | Find definition locations for a file position |
| `lsp_type_definition` | Find type definition locations for a file position |
| `lsp_implementation` | Find implementation locations for an interface, method, or symbol |
| `lsp_references` | Find reference locations for a file position |
| `lsp_diagnostics` | Return compile/type diagnostics for a source file, distinguishing clean results from diagnostic-publish timeouts |
| `lsp_changed_files_diagnostics` | Check diagnostics for git-changed supported source files after edits |
| `lsp_hover` | Show hover/type/documentation text for a source position |
| `lsp_signature_help` | Show callable signatures and active parameter info at a source position |
| `lsp_code_actions` | List quick fixes/refactors for a range without applying them, including edit/command metadata |
| `lsp_code_action_preview` | Preview the WorkspaceEdit for one code action without writing files |
| `lsp_rename_preview` | Preview semantic rename edits as WorkspaceEdit JSON without writing files |
| `lsp_format_preview` | Preview formatting edits for one file without writing files |
| `lsp_apply_workspace_edit` | Apply a previously previewed WorkspaceEdit through yottacode path validation and approval |
| `lsp_call_hierarchy` | Show incoming/outgoing call hierarchy for a source position |

Positions are zero-based line and UTF-16 character offsets, matching LSP. Output locations are rendered one-based as `path:line:column` for terminal readability.

`lsp_code_actions` is intentionally read-only. Semantic edits use a two-step flow: preview tools (`lsp_code_action_preview`, `lsp_rename_preview`, and `lsp_format_preview`) return normalized WorkspaceEdit JSON, then `lsp_apply_workspace_edit` validates every path, snapshots affected files through the normal mutator flow, writes the edits itself, and notifies the LSP manager. The language server never writes directly to the repository.

When a server advertises `textDocument/prepareRename`, `lsp_rename_preview` preflights the target position before asking for edits and returns an explicit unavailable result for invalid rename targets. The WorkspaceEdit applier uses LSP UTF-16 character offsets, applies text edits from the bottom of each file upward, and validates every path before writing. Broad semantic refactors should still be reviewed carefully because server-proposed multi-file edits can be large.

## Session advisory

Interactive sessions show a non-blocking **LSP Code Intelligence** advisory card when the feature is enabled, supported files are detected, and a matching server is missing. The card names the affected language, calls out that go-to-definition, live diagnostics, and symbol-aware review are unavailable without the server, shows the install command, and notes that yottacode still works without it. It is deterministic TUI chrome, not model-generated text, so setup hints appear even before the model calls `lsp_status`.

For command-line diagnostics, `yottacode doctor` includes an **LSP Code Intelligence** section with the feature flag state, detected supported languages, server availability, install hints, command overrides, and manager configuration.

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
- Successful `edit_file` and `write_file` calls notify pooled servers with full-document `didChange` and `didSave`, using version counters so diagnostics can reflect yottacode edits. This sync is best-effort: a dead server pipe is evicted/retried once and never turns an otherwise-successful file write into a scary edit error.
- `lsp_diagnostics` reports whether diagnostics were actually published before the settle timeout, so “clean” is distinct from “no diagnostic publication observed yet”.
- `lsp_code_action_preview` resolves editable code actions into the same preview/apply WorkspaceEdit flow used by rename and formatting.
- `lsp_document_highlights` and `lsp_selection_ranges` provide bounded current-file context before agents choose an edit range or wider read window.
- `lsp_rename_preview` uses server-side prepare-rename validation when available so invalid cursor positions fail before any edit preview is requested.
- Server capabilities returned by `initialize` are checked before optional methods; unsupported methods return an explicit `unavailable` result rather than a misleading empty response.
- `lsp_status` exposes session manager stats: open servers, starts, reuses, evictions, and last startup latency. `yottacode doctor` reports the default manager configuration without starting a session server.
- Smoke tests exist for all supported servers and skip automatically when the binary is not installed.

## Troubleshooting

1. Restart the TUI and look for the **LSP Code Intelligence** advisory card when a supported server is missing.
2. Run `yottacode doctor --experimental lsp_code_intelligence` to see the command-line LSP Code Intelligence section.
3. Confirm the server works outside yottacode, for example `gopls version` or `pyright-langserver --version`.
4. If a workspace symbol query fails, yottacode may fall back to an approximate regex symbol index. Definition, references, diagnostics, hover, code actions, and call hierarchy require a real server.

5. Post-edit LSP sync is advisory. If a local language server dies while yottacode writes a file, yottacode evicts/retries the server and keeps raw transport text such as `broken pipe` out of the edit result.

## Production promotion checklist

Before graduating this feature from experimental to default-on, verify:

- Real-server smoke tests pass for `gopls`, `typescript-language-server`, `pyright-langserver`, and `rust-analyzer`.
- Workspace root detection picks the expected language project root for nested files.
- Server capability checks report unsupported optional methods clearly.
- Cancellation/timeouts kill server subprocesses without leaks.
- Diagnostics, code actions, and call hierarchy outputs are bounded and readable.
- Path validation applies to every file/workspace argument.
- Custom server commands are documented as local user-configured execution.
- The startup session advisory card and `yottacode doctor` give actionable setup hints.
