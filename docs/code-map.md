# Code Map

Code Map is an experimental feature behind `code_map`. The product goal is not
"show every file and function"; that is only a raw outline. The useful product
shape is a **context assembly and blast-radius assistant** for developers and the
agent. All 5 roadmap phases below have landed; the feature remains behind the
experimental flag pending a separate GA decision (graduating the flag is a
mechanical, later step — see `docs/experimental.md`).

## Current slice

- Shared `internal/codemap` index with cached, watcher-backed snapshots.
- Directory, file, symbol, and (for `.md`/`.mdx`) plain doc-file nodes.
- LSP-backed symbols when available, parser-backed symbols (Go, TypeScript/
  JavaScript, Python, Rust) otherwise, falling back further to regex symbols
  for other languages.
- Import edges: Go resolved module-path-first from `go.mod`, then
  package-name fallback, never landing an edge on a `_test.go` file (they're
  excluded from a package's compiled surface for an external importer, so
  they can only ever be an edge *source*, never a target — this also stops
  an external test file's `package foo_test` clause from silently shadowing
  the real package name for its directory); TypeScript/JavaScript resolved
  for relative
  specifiers (file-based resolution, trying common extensions and
  `index.*`); Python resolved for both absolute and relative
  (`from . import x`-style, including the multi-line parenthesized form)
  specifiers; Rust resolved for `mod x;`, `use crate::...`, `use self::...`,
  and `use super::...` — walking the crate's actual module tree (see
  `internal/codemap/import_edges.go`'s `resolveRustModulePath`) rather than
  a directory heuristic, so a path like `super::top` naming an item defined
  directly in the parent module (not a further submodule) resolves
  correctly. `super::` only resolves when the file's declaring parent is
  known (i.e. some file has a matching `mod x;` for it) — there's nothing
  to resolve against otherwise. Bare/external specifiers that don't resolve
  in-repo are simply skipped in every language, the same as Go's original
  behavior.
- The same parser-backed offline syntax layer also powers the separate,
  now-GA `syntax_range` tool for local edit-range selection before anchored
  edits.
- `/map` TUI overlay with submodes:
  - `/map` — structure tree; filtering to an exact directory renders a
    subsystem overview instead of a flat list.
  - `/map here [path]` — ranked suggested-context list (changed files, their
    direct import neighbors, likely tests, likely docs), with `a` to attach
    every listed file as an `@path` ref in one keystroke.
  - `/map deps <path>` / `/map dependents <path>`
  - `/map impact [--depth N|all] <path>` — direct dependencies, direct
    dependents, transitive dependents, likely tests, likely docs/config, and
    cycles, all proximity-ranked against the target.
  - `/map cycles [path]`
  - `/map diagram [--export <path>] [path]` — `--export` writes the full,
    untruncated Mermaid diagram to disk instead of only the on-screen capped
    render.
- Enter on a file or symbol inserts an `@path` prompt reference so the existing
  file-ref injection path attaches the source to the next turn.
- Read-only agent tools:
  - `code_map`
  - `code_symbols`
  - `code_structure_projection` (`to_file` writes the full projection to disk)
  - `code_dependencies`
  - `code_dependents`
  - `code_impact` (`format: "summary"` for a compact planning-turn
    projection; `include_calls` for a best-effort live LSP
    callers/callees supplement)
  - `code_cycles`
  - `code_map_diagram` (`to_file` writes the full diagram to disk)

## Phase 1 — Suggested context (done)

`/map here` ranks the changed files' neighborhood — `codemap.SuggestedContext`
scores each candidate by combined reason (changed, imports target, imported
by target, test for target, docs for target) so an edited file always sorts
first, and `a` attaches every listed file as an `@path` ref in one step.
`internal/codemap/relate.go` holds the shared `IsTestFor`/`LikelyDocsFor`
heuristics, reused by Phases 2–4 below.

## Phase 2 — Better impact view (done)

`ImpactResult` now includes `LikelyTests`/`LikelyDocs` alongside the existing
dependency/dependent/cycle groups, and every group is proximity-ranked
(same directory as the target first, then by shared path-prefix depth) rather
than plain alphabetical order. `code_impact`'s `format: "summary"` renders a
compact, counts-plus-top-names projection sized for planning-turn context,
distinct from the full sectioned report.

## Phase 3 — Subsystem overview (done)

Filtering `/map` to an exact directory (e.g. `/map internal/codemap`) renders
a subsystem overview instead of a flat file list: entry points (Go
`func main()`, or — for other languages — files nothing in the repo imports),
public surface (exported symbols in the directory's immediate files), core
types (the type/struct/interface/class/enum/trait subset of that surface),
tests (recursive, matched via `IsTestFor`), and key dependencies (external
targets ranked by how many of the directory's files import each one). See
`internal/codemap/subsystem.go`. No new slash command was added — `/map
<dir>` reaches this through the existing structure-mode filter path.

## Phase 4 — Language coverage and precision (done)

TypeScript/JavaScript, Python, and Rust import edges, including Rust
`self::`/`super::` module-tree resolution (see "Current slice" above for
exact scope).
Go test-file pairing is handled by the same `IsTestFor` heuristic Phase 1
introduced (a directory's `_test.go` files pair with every source file in
that directory when there's no exact-name counterpart, covering Go's
`package foo_test` convention). Reference/call graph support is a **live,
best-effort, agent-tool-only** supplement: `code_impact`'s `include_calls`
param queries LSP call hierarchy for the target file's exported symbols when
a language server is available, appending "callers (LSP)"/"callees (LSP)"
sections. This is deliberately not merged into the cached static graph (call
hierarchy is symbol-position-scoped and live, unlike the file-level import
graph) and not added to the TUI `/map impact` view (it would add an LSP
round-trip to a keystroke-driven picker).

**Known limitation, not fixed here:** Go import edges are package-level —
importing a package edges to every non-test file in it, not just the ones
whose exported symbols are actually referenced. `_test.go` files are
excluded (see "Current slice"), which removes a real chunk of the noise,
but a `depends on`/`code_impact`/diagram view for a file that imports a
large package can still list more files than are individually relevant.
Narrowing to actually-referenced files would need identifier-usage analysis
(which selector expressions the importing file actually uses), a
meaningfully larger change than the edge-resolution work in this phase —
left for a future pass if it proves to matter in practice.

## Phase 5 — Live index and bounded export surfaces (done)

`CachedProvider.StartWatch` registers an `fsnotify` watch over the workspace
(skipping the same directories `Build` skips) and, on a relevant event,
records the specific changed path and debounces into a dirty flag; `Index`
then applies a true incremental patch — see `internal/codemap/builder.go`'s
`buildState` (persistent nodes/children/import maps kept across calls) and
`applyChanges` in `provider.go` — re-deriving only the changed file(s)'
node, symbols, and per-language import list, then re-running edge
resolution and stats aggregation (pure in-memory computation, no file I/O)
over the full persisted state. A changed/created path is re-walked by its
current on-disk contents rather than the fsnotify event kind (simpler and
more correct for a debounced batch that can coalesce several events into
one net effect); a directory create re-walks that subtree fresh, since
files inside it may not get their own individual events (e.g. a whole
subtree moved in at once); a directory or file that's gone is purged,
recursively for a directory. Debounced batches larger than
`maxIncrementalChanges` (default 50 — e.g. a branch switch touching
hundreds of files) fall back to a full `Build` instead, replacing the
persistent state fresh. Oversized repos (more directories than
`maxWatchedDirs`) or any `fsnotify` setup failure fall back silently to the
original per-call fingerprint walk, so `Index` always keeps working.
`code_map_diagram` and `code_structure_projection` gained `to_file`: writing
the full, untruncated output to disk (like any other write tool —
approval-gated, checkpoint-tracked) instead of a context-window-bounded
inline result. MCP exposure is permanently out of scope, not just for this
pass — yottacode is MCP-client-only by design (`internal/mcp` is a client
for external servers only) and will not ship a server exposing its own
tools.

## Non-goals

- Adding more top-level slash commands such as `/outline`, `/deps`, or
  `/impact`. All code-map UX stays under `/map`.
- Rendering full-repo hairball graphs by default.
- Treating LSP call hierarchy as authoritative — it stays a best-effort,
  opt-in supplement (see Phase 4).
- Standing up an MCP server to expose these tools. Permanent, not deferred
  — see "Live index" above.
