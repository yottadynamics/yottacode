# Experimental features

Some yottacode capabilities are merged-and-tested but not yet ready
for the default experience. They live behind named feature flags so
early adopters can opt in while general users get a stable surface.
A feature lives in `experimental` when:

- The code is reliable enough to ship, but
- The UX, model behavior, or API shape isn't settled, and
- We want production users to encounter it only on deliberate
  opt-in.

When a feature stabilizes, the gate is removed and the feature
becomes a default-on capability. The flag name stays a no-op for
one release so existing configs don't break.

## Current catalog

| Name | Status | What it enables |
| --- | --- | --- |
| `background_subagents` | **graduated** (GA) | `run_in_background:true` on the Agent tool — fire-and-forget subagent dispatch with `get_subagent_result` for fetching. **Now generally available in the interactive TUI**; the flag is a no-op kept for one release so existing configs don't break. Foreground subagents are always available; background is read-only by default (standalone), with write-capable unattended work routed through dispatch. |
| `code_map` | experimental | Repository structure map. Adds the `/map` TUI overlay and read-only `code_map`, `code_symbols`, `code_structure_projection`, `code_dependencies`, `code_dependents`, `code_impact`, `code_cycles`, and `code_map_diagram` agent tools. The TUI includes `/map here` for changed-file neighborhoods and Enter-to-insert `@path` prompt refs, plus structure, dependency, impact, cycle, and Mermaid diagram modes. The index is LSP-backed when available and falls back to approximate regex symbols. Future call graph views will stay under `/map` instead of adding more slash commands. See [code-map.md](code-map.md). |
| `dispatch` | experimental · **beta** | The `dispatch` + `integrate` tools — fan a batch of subtasks out to concurrent subagents (write-capable ones in isolated git worktrees, partitioned by file ownership), then merge their branches into one integration branch for a PR. **In beta — feedback wanted; file issues with the `dispatch-beta` label.** See [dispatch.md](dispatch.md), incl. its Known Limitations. |
| `lsp_code_intelligence` | experimental | Language-server-backed code intelligence tools (`lsp_status`, `lsp_symbols`, `lsp_document_symbols`, `lsp_document_highlights`, `lsp_selection_ranges`, `lsp_definition`, `lsp_type_definition`, `lsp_implementation`, `lsp_references`, `lsp_diagnostics`, `lsp_changed_files_diagnostics`, `lsp_hover`, `lsp_signature_help`, `lsp_code_actions`, `lsp_call_hierarchy`, `lsp_impact`, preview/apply WorkspaceEdit tools with prepare-rename validation when available). Detects supported languages in the workspace, reports missing server binaries with install hints, and reports offline syntax fallback mode. Servers are never installed automatically; install `gopls`, `typescript-language-server`, `pyright-langserver`, or `rust-analyzer` on `PATH` for the languages you use. See [lsp.md](lsp.md). |
| `syntax_ranges` | experimental | Offline parser-backed syntax range selection for safer anchored edits. Adds the read-only `syntax_range` agent tool, which returns Go parser-backed block/function/type/file ranges around a source position plus `read_file(anchors=true)` hints. Tree-sitter and additional languages are future backend work, not part of the initial surface. |

(Adding a feature here is a one-constant change in
`internal/experimental/features.go`. See that file's package doc
for the contract.)

## How to enable

Three sources, merged at startup (CLI > env > config; later sources
add to earlier ones but never disable):

### 1. CLI flag (repeatable)

```bash
yottacode --experimental background_subagents
# stack multiple:
yottacode --experimental background_subagents --experimental other_feature
# or comma-separated in one invocation:
yottacode --experimental background_subagents,other_feature
```

The flag is inherited by all subcommands (`yottacode run`,
`yottacode sessions`, etc.) so the same opt-in works regardless of
entry point.

### 2. Environment variable

```bash
export YOTTACODE_EXPERIMENTAL=background_subagents
# or comma-separated:
export YOTTACODE_EXPERIMENTAL=background_subagents,other_feature
```

Useful for shell sessions / CI where setting the env once is
cleaner than threading the flag everywhere.

### 3. `~/.yottacode/config.toml`

```toml
[experimental]
background_subagents = true
code_map = true                 # optional: enables /map and code-map agent tools
lsp_code_intelligence = true    # optional: enables LSP tools when servers are installed
syntax_ranges = true            # optional: enables offline Go parser-backed syntax_range
# future:
# other_feature = true
```

Persistent across sessions; survives reinstalls. Good for the
"this is now my default" case.


## What happens when a flag is on

Each feature's code path checks `experimental.Set.IsEnabled(...)`
at the relevant decision point and changes behavior accordingly.
See the catalog above for what each flag actually controls. When
off, the feature is fully inert — no performance cost, no model
exposure.

## What happens with an unknown flag name

Typos or graduated/removed feature names land in the Set's
"unknown" bucket and produce a one-line startup warning on stderr:

```
warning: --experimental "foo_made_up" is not a recognized feature
(typo? graduated? see docs/experimental.md)
```

The session continues normally. The intent is *fail-soft*: a flag
that used to exist shouldn't break someone's config when the
feature graduates and the gate goes away.

## When a feature graduates

When the team decides a feature is ready for default-on behavior:

1. The gate at the use site is removed.
2. The constant in `internal/experimental/features.go` is dropped.
3. `Recognized(<name>)` now returns false → users with the old
   flag get the startup warning but the feature still works (it's
   default-on now).
4. After one full release cycle, the warning is removed (the name
   becomes truly unknown, no different from a typo).

This means: setting `--experimental foo` for a feature that has
since graduated is harmless — the feature still works, the user
just sees a warning that they can clean up at their leisure.

## Reading from code

```go
import "github.com/yottadynamics/yottacode/internal/experimental"

// At startup, build the Set from CLI/env/config sources:
exp := experimental.NewSet()
for _, name := range opts.Experimental { exp.Enable(name) }
// ...

// At the gate's call site:
if exp.IsEnabled(experimental.BackgroundSubagents) {
    // turn the feature on
}
```

`Set` is nil-safe on read (`(*Set)(nil).IsEnabled(...)` returns
`false`) so subsystems that haven't been wired up yet (or tests
that don't build a Set) can call without guarding.
