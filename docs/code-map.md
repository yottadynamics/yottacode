# Code Map roadmap

Code Map is an experimental feature behind `code_map`. The product goal is not
"show every file and function"; that is only a raw outline. The useful product
shape is a **context assembly and blast-radius assistant** for developers and the
agent.

## Current slice

Shipped in the MVP:

- Shared `internal/codemap` index with cached snapshots.
- Directory, file, and symbol nodes.
- LSP-backed symbols when available, parser-backed symbols (Go, TypeScript/
  JavaScript, Python, Rust) otherwise, falling back further to regex symbols
  for other languages.
- Go import edges resolved module-path-first from `go.mod`, then package-name
  fallback.
- The same parser-backed offline syntax layer also powers the separate,
  now-GA `syntax_range` tool for local edit-range selection before anchored
  edits.
- `/map` TUI overlay with submodes:
  - `/map`
  - `/map here [path]`
  - `/map deps <path>`
  - `/map dependents <path>`
  - `/map impact [--depth N|all] <path>`
  - `/map cycles [path]`
  - `/map diagram [path]`
- `/map here` explicitly assembles context: it deterministically ranks up to 8
  high-signal changed or related files (and may return fewer), labels each with
  its reason (`changed`, `imported by target`, `imports target`, or `test for
  target`), and lets `a` insert the whole set as `@path` prompt references.
  Paths containing whitespace are omitted because the current whitespace-delimited
  `@path` syntax cannot represent them safely.
- Enter on a file or symbol inserts an `@path` prompt reference so the existing
  file-ref injection path attaches the source to the next turn.
- `/context` reports the latest turn's attached `@path` references under
  `Working set`.
- When `code_map` is enabled, the model prompt recommends the indexed tools for
  orientation and requires relevant results to be verified against source with
  `read_file` or `read_many_files` before editing.
- Read-only agent tools:
  - `code_map`
  - `code_symbols`
  - `code_structure_projection`
  - `code_dependencies`
  - `code_dependents`
  - `code_impact`
  - `code_cycles`
  - `code_map_diagram`

## Phase 1 — Suggested context (shipped)

`/map here` answers: **what should I attach before asking the agent?**

Shipped:

- Rank changed files first, then imports, importers, and likely tests.
- Show a deterministic `Suggested context` section with up to 8 unique,
  high-signal files; sparse results are expected.
- Explain why each suggestion is present: changed, imported by target, imports
  target, or test for target.
- Press `a` to insert every suggestion into the prompt as an `@path` reference;
  the existing file-ref path attaches those sources when the turn is sent. This
  is explicit assembly: opening `/map here` does not attach or inject files by
  itself.
- Omit paths containing whitespace until `@path` gains a quoting or escaping
  syntax; inserting them today would attach only the prefix before the first space.
- Show the latest turn's attached `@path` references in `/context` under
  `Working set`.
- Gate model guidance with `code_map`: when enabled, it prefers Code Map for
  orientation and verifies relevant indexed results with `read_file` or
  `read_many_files` before editing.

## Phase 2 — Better impact view

Make `/map impact` answer: **what might break if I change this?**

Planned work:

- Group impact results as:
  - files this imports
  - files that import this
  - transitive dependents
  - likely tests
  - cycles involving the target
- Rank by proximity and relevance.
- Add an agent-friendly impact projection that is compact enough to inject into
  planning turns.

Exit criteria:

- The impact view is useful for deciding which files/tests to inspect or
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

- Code Map is useful outside Go repos, and Go results include likely tests with
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
