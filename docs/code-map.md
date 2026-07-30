# Code Map roadmap

Code Map is an experimental feature behind `code_map`. The product goal is not
"show every file and function"; that is only a raw outline. The useful product
shape is a **context assembly and blast-radius assistant** for developers and the
agent.

## Current slice

Shipped in the MVP:

- Shared `internal/codemap` index with cached snapshots.
- Directory, file, and symbol nodes.
- LSP-backed symbols when available, fallback regex symbols otherwise.
- Go import edges resolved module-path-first from `go.mod`, then package-name
  fallback.
- Parser-backed Go syntax also powers the separate experimental `syntax_range`
  tool for local edit-range selection before anchored edits.
- `/map` TUI overlay with submodes:
  - `/map`
  - `/map here [path]`
  - `/map deps <path>`
  - `/map dependents <path>`
  - `/map impact [--depth N|all] <path>`
  - `/map cycles [path]`
  - `/map diagram [path]`
- Enter on a file or symbol inserts an `@path` prompt reference so the existing
  file-ref injection path attaches the source to the next turn.
- Read-only agent tools:
  - `code_map`
  - `code_symbols`
  - `code_structure_projection`
  - `code_dependencies`
  - `code_dependents`
  - `code_impact`
  - `code_cycles`
  - `code_map_diagram`

## Phase 1 — Suggested context

Make `/map here` answer: **what should I attach before asking the agent?**

Planned work:

- Rank changed files and their neighbors instead of just listing them.
- Add a `Suggested context` section with 3–8 high-signal files.
- Include likely tests and docs near the changed area.
- Add an `a` key to attach all suggested context as `@path` refs.
- Explain why each suggestion is present: changed, imports target, imported by
  target, test for target, docs for target.

Exit criteria:

- A developer can run `/map here`, press `a`, and get a good prompt context set
  for coding/reviewing the current change.

## Phase 2 — Better impact view

Make `/map impact` answer: **what might break if I change this?**

Planned work:

- Group impact results as:
  - files this imports
  - files that import this
  - transitive dependents
  - likely tests
  - likely docs/config
  - cycles involving the target
- Rank by proximity and relevance.
- Add an agent-friendly impact projection that is compact enough to inject into
  planning turns.

Exit criteria:

- The impact view is useful for deciding which files/tests/docs to inspect or
  attach before editing.

## Phase 3 — Subsystem overview

Make `/map <area>` answer: **where do I start in this subsystem?**

Planned work:

- Identify entry points, public surface, core types, tests, and key dependencies.
- Group by package/subsystem rather than path order.
- Provide a compact architecture summary for the selected area.

Exit criteria:

- A developer unfamiliar with an area can use `/map internal/foo` to orient
  themselves quickly without reading every file.

## Phase 4 — Language coverage and precision

Make the graph more accurate across common project types.

Planned work:

- TypeScript import/dependency parser.
- Better Go test-file pairing.
- Reference/call graph support where LSP servers expose reliable data.
- Keep fallback behavior deterministic when LSP is missing.

Exit criteria:

- Code Map is useful outside Go repos, and Go results include tests/docs with
  fewer false positives.

## Phase 5 — Live index and ecosystem surfaces

Make the index feel live and expose it safely.

Planned work:

- Replace fingerprint-based cache invalidation with incremental updates or a
  watcher-backed rebuild queue.
- Add bounded export surfaces for diagrams/context projections.
- Consider MCP exposure once the local API is stable.

Exit criteria:

- Large repos do not pay full rebuild cost on every meaningful query, and other
  local tools can safely query the same index.

## Non-goals for the experimental period

- Adding more top-level slash commands such as `/outline`, `/deps`, or
  `/impact`. All code-map UX stays under `/map`.
- Rendering full-repo hairball graphs by default.
- Treating LSP call hierarchy as authoritative before it is tested per language.
- Expanding schemas piecemeal before the developer workflow proves useful.
