# Code Map

Code Map is an **experimental** feature (`code_map`). It gives the agent one
read-only tool, `code_impact`, that answers questions about how files in the
repository depend on each other:

- Who depends on this file, directly and indirectly?
- What does it depend on?
- Which test directories should I run after changing it?
- Which docs mention it, and is it part of an import cycle?

It answers from an import graph built over the workspace, so it works without a
language server. There is no TUI surface: no slash command, no overlay.

Contents: [When to use it](#when-to-use-it) · [Enabling it](#enabling-it) ·
[How the agent uses it](#how-the-agent-uses-it) · [The tool](#the-tool) ·
[Reading the output](#reading-the-output) · [Architecture](#architecture) ·
[Language support](#language-support) · [Trust model](#trust-model) ·
[Troubleshooting](#troubleshooting) · [Evaluation](#evaluation) ·
[Non-goals](#non-goals)

## When to use it

Use `code_impact` for **file-level, structural** questions, before an edit:

| Question | Tool |
|---|---|
| What breaks or needs re-testing if I change this file? | `code_impact` |
| Which test packages should I run? | `code_impact` with `mode: "tests"` |
| Is this file in an import cycle? | `code_impact` with `mode: "cycles"` |
| Where is this symbol used? | `lsp_references` (or `grep` for a known name) |
| What symbols does this file define? | `lsp_document_symbols` |
| What does the repository look like? | `list_project_structure` |

It is deliberately not a symbol-level tool. In evaluation, for "who calls this
exact function" it matched a plain text search on recall and listed more files,
so the prompt steers the model to LSP or `grep` for that. Its distinguishing
outputs are indirect dependents and test selection (see [Evaluation](#evaluation)).

## Enabling it

Code Map is off by default. Turn it on with any of:

```
yottacode --experimental code_map
YOTTACODE_EXPERIMENTAL=code_map yottacode
```

or persistently in `~/.yottacode/config.toml`:

```toml
[experimental]
code_map = true
```

See [experimental.md](experimental.md). When the flag is off the tool is not
registered, the system-prompt section is absent, and the tool name is silently
dropped from built-in subagent allowlists (no warnings).

## How the agent uses it

### Where it is wired in

TUI, oneshot, and ACP sessions are all built by the same
`agentruntime.Builder`, so they get identical behavior. With the flag on, build
does three things (`internal/agentruntime/runtime.go`):

1. **Creates a `CachedProvider`** over the working directory and starts a
   filesystem watch. Nothing is indexed yet; the first query triggers the build.
2. **Registers the `code_impact` tool** in the core tool set
   (`internal/agent/toolset.go`), holding the provider, the session's read deny
   list, and the LSP client factory used by the optional call-hierarchy
   supplement.
3. **Appends a short section to the system prompt**
   (`agent.CodeMapPromptAddendum`). It tells the model to call `code_impact`
   before editing a file other code may depend on, to prefer `lsp_*` and
   `list_project_structure` for symbol and layout questions, to treat
   `[heuristic]` sections as guesses, and to read a `PARTIAL` header as "absence
   proves nothing". It is kept out of `DefaultSystemPrompt` so the steering is
   present only when the tool is.

On shutdown the runtime closes the provider, which stops the watch goroutine.

### Who can call it

- **Main agent:** yes, like any registered tool. It needs **no approval**: it is
  in the read-only tool set and never writes. It is parallel-safe, so the
  scheduler can run it alongside other reads.
- **Plan mode:** allowed, since plan mode permits read-only tools. This is the
  intended moment for it: assessing blast radius while planning.
- **Subagents, including `dispatch` workers:** a child's tool registry is cloned
  from the parent's and filtered by the agent definition's `tools:` allowlist, so
  a child has `code_impact` only if the parent does (flag on) *and* the definition
  allows it (an empty allowlist inherits everything). The built-ins `Explore`,
  `Plan`, `review`, `code-verifier`, `verification`, `test`, `implement`, and
  `docs` list it; the `Explore`, `Plan`, and `review` prompts also say when to
  reach for it (dependents, cycles, test selection). A custom subagent can name
  it in its own allowlist.
- **Background and unattended agents:** allowed. It is in the read-only set that
  unattended runs may use without prompting.
- **Deny list:** files on the session's read deny list are never listed in
  results, and asking about one is refused. See [Trust model](#trust-model).

### How it combines with LSP

Code Map and LSP answer different questions and are designed not to depend on
each other:

- The **import graph is built without a language server.** Missing, stale, or
  slow servers cannot make `code_impact` fail.
- **The index does not use LSP.** Symbols come from yottacode's offline parser
  layer (regex for languages without one). Asking gopls for every file's symbols
  produced the same symbols but made a full rebuild take 18.6 s instead of 1.4 s
  on 1,100 Go files.
- **`include_calls: true`** adds live call-hierarchy callers and callees for the
  target's exported symbols, under a 3 second timeout. If no server is
  available, disabled, or slow, the static answer is returned with a one-line
  note in the `[lsp]` section instead of an error.
- **`lsp_impact`** (the composite LSP report for a source position) embeds the
  same import report as a `code_map_import_impact` section, so one call yields
  hover, definitions, references, calls, diagnostics, and file-level blast
  radius. When Code Map is off that section says so.

### A typical sequence

1. The user asks to refactor `internal/auth/store.go`.
2. The model calls `code_impact {"path": "internal/auth/store.go"}`. The header
   says whether the index is complete; the report lists dependents, the test
   directories to run, and cycles.
3. For symbol-level detail it follows up with `lsp_references` on the specific
   function it will change.
4. It edits, then runs the test directories the report named instead of the
   whole suite.

## The tool

`code_impact` arguments:

| Argument | Meaning |
|---|---|
| `path` (required) | File path or path fragment. An exact path wins, then a unique match on a directory boundary (`a/main.go`), then a unique substring. A fragment naming several files returns the candidates and runs no analysis. |
| `mode` | `impact` (default), `tests`, `dependents`, `dependencies`, or `cycles`. |
| `scope` | How tests are chosen: `package` (default) or `file`. |
| `depth` | Import hops of indirect dependents to follow; default 3, `-1` for all. |
| `budget` | Approximate output size in tokens; default 1500, max 4000. |
| `include_calls` | Add live LSP callers/callees for the target's exported symbols (default false). |

Modes:

- **`impact`** returns, in order: direct dependents, indirect dependents (each
  file listed once), direct dependencies, test directories to run, likely
  docs/config, and import cycles through the target.
- **`tests`** returns only the test directories.
- **`dependents`**, **`dependencies`**, **`cycles`** return only that list.

### Test selection and `scope`

Tests are chosen at directory (package) granularity.

- **`scope=package` (default):** the target's directory plus every directory
  that imports it, directly or transitively, that contains test files. This is
  the conservative choice, and the one to use when deciding what to run. On this
  repository it reproduced `go list`'s affected test packages exactly (100%
  recall) while running about a quarter of all test packages.
- **`scope=file`:** only the directories of the target's file-level dependents.
  It runs fewer tests but recalled about half of the affected ones, because it
  drops a package that imports the target's package without using the changed
  file, even though behavior can still reach it through package state or an
  intermediate package.

A "test file" is recognized by name (`*_test.go`, `*.test.ts`, `*.spec.*`,
`test_*.py`, `*_test.py`, `*_test.rs`) or by living under a `tests`, `test`, or
`__tests__` directory.

## Reading the output

A real example (`mode` omitted, so `impact`):

```
code_impact impact base/base.go
index: 7 files, version 06e8e32e
direct dependents [import] (1)
  mid/mid.go
indirect dependents (depth 3) [import] (0)
  (none)
direct dependencies [import] (0)
  (none)
test directories to run (package level) [import] (3)
  base/ (1 test files)
  mid/ (1 test files)
  side/ (1 test files)
likely docs/config [heuristic] (0)
  (none)
cycles [import] (0)
  (none)
```

- **First line:** mode and the resolved target file.
- **`index:` line:** how many files are indexed, a content version (same version
  means the same index), and, when applicable, a `PARTIAL` notice and a count of
  excluded generated files.
- **Section headers:** `title [provenance] (count)`. A trailing `+` on the count
  (`(500+)`) means the query cap was hit.
- **Provenance tags:**
  - `[import]`: derived from resolved import edges.
  - `[heuristic]`: matched by name (docs mentioning the file). A guess.
  - `[lsp]`: from a live language-server query (`include_calls`).
- **Budget:** when output exceeds `budget`, every section keeps its title and its
  true count, and says `…[N more omitted: output budget]`. Nothing is dropped
  silently.
- **Determinism:** the same query over the same index produces identical bytes,
  and building the same tree twice produces the same index version (the `index:`
  line), which keeps the prompt cache warm.

## Architecture

### Data flow

```
 agent turn
    │  code_impact {path, mode, ...}
    ▼
 CodeImpactTool (internal/agent/codemap_tools.go)
    │  1. resolve path  ──► CodeIndex.FindFiles   (unique | ambiguous | none)
    │  2. deny-list check, filter results
    │  3. Provider.Index(ctx)
    ▼
 CachedProvider (internal/codemap/provider.go)
    │  snapshot clean? ──yes──► return cached *CodeIndex
    │        │ no (dirty / first call / fingerprint changed)
    │        ▼
    │   Build(ctx, opts)  ── walk ► classify ► parse imports ► symbols ► edges
    ▼
 CodeIndex (immutable snapshot)
    │  Impact / Dependents / Dependencies / Cycles / TestDirsToRun / ...
    ▼
 Report.Render(budget)  (internal/codemap/report.go)
    │  provenance-tagged sections, byte budget, true counts
    ▼
 tool result text  ──►  model
```

### Components

| File | Role |
|---|---|
| `internal/codemap/builder.go` | Walks the workspace; classifies files; skips excluded directories and generated files; enforces the file cap and time budget; collects per-language imports and `tsconfig.json` files; produces the snapshot. |
| `internal/codemap/import_edges.go` | Resolves imports to edges for Go, TypeScript/JavaScript, Python, and Rust, including tsconfig `paths` and Python suffix resolution. |
| `internal/codemap/index.go` | The immutable `CodeIndex`: nodes, edges, `IndexInfo`, and queries (`Impact`, `Dependents`, `Dependencies`, `Cycles`, `FindFiles`). |
| `internal/codemap/tests.go` | Test-file detection and test-directory selection (`TestDirsToRun`, `TestDirsFromImpact`). |
| `internal/codemap/relate.go` | Likely-docs matching and per-language test pairing helpers. |
| `internal/codemap/report.go` | `Report` rendering: provenance tags, byte budget, truthful counts. |
| `internal/codemap/provider.go` | `CachedProvider`: snapshot cache, `fsnotify` watch, fingerprint fallback. |
| `internal/codemap/fallback_source.go` | The symbol source: the offline parser layer, regex for other languages. |
| `internal/agent/codemap_tools.go` | The `code_impact` tool: argument handling, deny-list filtering, section assembly, LSP supplement. |
| `internal/agent/lsp_impact_tool.go` | Embeds the import report in `lsp_impact`. |
| `internal/agent/prompt.go` | `CodeMapPromptAddendum`. |
| `internal/agentruntime/runtime.go` | Feature gating, provider construction, prompt addendum, shutdown. |
| `internal/codemap/eval_test.go` | Evaluation harness (build tag `codemap_eval`). |

### Index lifecycle

- **Lazy build.** The index is built on the first `code_impact` call, not at
  session start, and lives in memory only. Nothing is written to disk.
- **Invalidation.** An `fsnotify` watch registers every directory and, on a
  relevant change (create, remove, rename, or a write to a source or markdown
  file), sets a debounced dirty flag (default 300 ms). The next call after a
  change rebuilds the whole index. There is no incremental patching, so there is
  no patch state that can drift out of sync with the files.
- **Ordering.** The dirty flag is cleared *before* a rebuild starts, so a change
  that lands mid-build marks the next snapshot stale instead of being lost. A
  failed or canceled rebuild leaves the flag set so the next call retries.
- **Fallback.** If the watch cannot start (more than 4000 directories, no
  inotify, setup failure), each call compares a cheap workspace fingerprint
  (file count, newest modification time, total size) and rebuilds only when it
  changed.
- **Cost.** A rebuild re-parses everything up to the bounds below. Measured on
  real trees: 0.3 s for 477 Go files (`k8s.io/apimachinery`), 0.5 s for 2,070
  (`docker`), 1.4 s for 1,100 (this repository), 0.9 s for the 2,185 hand-written
  files of the Azure SDK for Go (another 14,969 generated files are excluded), and
  7.5 s for 2,168 Python files (`langflow`; the Python scanner is the slow one).
  Queries then take 3 to 100 ms. The first query in a session pays the build
  once, and so does the first query after any edit. Nothing larger than about
  2,200 indexed files has been measured.

### Bounds

| Bound | Default | On reaching it |
|---|---|---|
| File cap | 5000 files | Build stops; index flagged `PARTIAL`. |
| Build time | 20 seconds | Build stops; index flagged `PARTIAL`. |
| Per-query cap | 500 files per section | Count shown with a trailing `+`. |
| Output budget | 1500 tokens (max 4000) | Items omitted with a true count. |
| Doc corpus | 256 KB per file, 16 MB total | Remaining documents are not searched. |
| LSP supplement | 3 seconds | Static answer returned with a note. |

A snapshot that stopped at the **time budget** is served for one minute and then
rebuilt on the next query, since the stop usually reflects a loaded machine, not
the repository. A snapshot that stopped at the **file cap** is not retried on a
timer, because the same files would hit the same cap; it is rebuilt when a file
changes like any other.

### Exclusions

The walk skips hidden directories plus `node_modules`, `vendor`, `target`,
`build`, `dist`, `__pycache__`, and `coverage`. Generated source is left out
because nobody edits it by hand: `*.pb.go`, `*.min.js`, and any file whose first
kilobyte has an `@generated` line, a `Code generated ...` line, or a `Generated
by ... DO NOT EDIT` banner. The count appears in the `index:` line. A generated
file that imports your code therefore does not show up as a dependent of it.

### Concurrency

The provider serializes builds behind a mutex. Queries run against an immutable
snapshot, so after the snapshot is returned there is no locking. The markdown
corpus and the per-directory test graph are each computed once per snapshot
(`sync.Once`). The LSP supplement runs under its own timeout and never holds the
provider lock. The watch goroutine is tied to the runtime's lifetime.

## Language support

| Language | Symbols | Import edges |
|---|---|---|
| Go | AST | Exact when `go.mod` files are present: the longest matching module path decides what is local (nested modules each resolve under their own path), and everything else, including `net/http` or a third-party package that shares a name with a local one, is external. With no `go.mod` anywhere (GOPATH-style or `+incompatible` trees) the import is matched to the repository directory with the longest path suffix. Edges narrow to the file(s) declaring a referenced symbol; never target `_test.go`. Falls back to every non-test file in the package when it cannot tell (aliases, blank/dot imports, unmatched names). |
| TypeScript / JavaScript | structural scanner | Relative imports; bare specifiers through the nearest `tsconfig.json` / `jsconfig.json` `paths` and `baseUrl` (comments and trailing commas allowed; relative `extends` chains are followed, with a child's `paths` and `baseUrl` replacing its parent's, while package-name extends such as `@tsconfig/node20` are ignored). Unclaimed bare specifiers are external packages. |
| Python | structural scanner | Relative imports; absolute imports from the repository root; otherwise by matching the dotted path against the tail of module paths (covers `src/` and namespaced layouts). The closest match wins, a tie is left unresolved, and a one-segment name (`json`) only resolves inside the importer's directory chain so the standard library is not mistaken for repo files. `from pkg import name` also tries `pkg.name`. |
| Rust | structural scanner | `mod x;` and `use crate::` / `self::` / `super::` resolved against the crate's module tree. |
| Markdown | n/a | Indexed as plain nodes so the docs section can find mentions. |
| Other languages | Regex fallback | No import edges. |

Unresolved standard-library, third-party, or ambiguous imports are omitted rather
than invented.

## Trust model

- **Provenance.** Every section says how it was derived. Treat `[heuristic]` as a
  hint, never as proof.
- **Honest partiality.** `PARTIAL` in the `index:` line means the build hit the
  file cap or the time budget; a missing dependent then proves nothing.
- **Honest truncation.** Omitted items are counted, never dropped silently.
- **No silent guessing.** An ambiguous path returns candidates; unresolved
  imports are omitted, not approximated.
- **Read-only.** No approval, no writes, no checkpoints. The only write-capable
  parts of the old Code Map (`to_file` exports) were removed.
- **Deny list.** Files on the read deny list are filtered from every list
  (including test directories, cycles, ambiguity candidates, and LSP callers), do
  not count toward whether a path is ambiguous, and a denied target is refused, so
  the graph cannot be used to learn denied paths. Note that denied files still shape
  the index internally (their imports are parsed), but their names are never
  returned.
- **Not authoritative.** The graph is static. It does not see reflection,
  dynamic imports, build tags, code generation outside the excluded patterns, or
  runtime wiring. Verify against the source before relying on a result.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Tool not available | Flag is off. Start with `--experimental code_map`. |
| `PARTIAL: stopped at the N-file cap` | Repository exceeds the cap. Results cover only the files indexed. Query a narrower tree or treat absence as unknown. |
| `PARTIAL: stopped after the 20s build budget` | Build ran out of time, typically a very large tree or a heavily loaded machine. The next query after one minute retries the build. |
| `no indexed file matches` | Only supported source and markdown files are indexed, and generated files and excluded directories are skipped. |
| `"x.go" matches N files` | The fragment is ambiguous. Pass a longer path. |
| Python or TypeScript file shows no dependents | Check the resolution rules in [Language support](#language-support); a tie between same-named modules, a one-segment import, or an unclaimed alias is left unresolved. |
| A generated file is missing from dependents | Generated files are excluded; the `index:` line gives the count. |
| `include_calls` returns only a note | No language server for that file, disabled by config, or it timed out. The static answer is unaffected. |

## Evaluation

`internal/codemap/eval_test.go` (build tag `codemap_eval`, excluded from normal
runs) scores the graph against ground truth:

```
go test -tags codemap_eval -run TestEval -timeout 40m -v ./internal/codemap/
```

Environment: `CODEMAP_EVAL_N` (case count), `CODEMAP_EVAL_OUT` (report file),
`CODEMAP_EVAL_EXT` (external repository, default `~/repos/langflow`).

- **Go blast radius, by mutation testing.** Rename an exported function across
  its package, let `go vet` report which files outside the package break, and
  compare `code_impact` and a plain text search against that. Result on this
  repository (30 cases): both found 100% of breaking files; `code_impact` listed
  more files per true breakage (6.8 vs 4.3). Its distinguishing output is the
  indirect dependents it adds (about 18 files per query).
- **Go test selection.** Against `go list`'s affected test packages: own package
  only 7%, `scope=file` 54%, `scope=package` 100%.
- **Python.** Edge agreement with an independent `ast` resolver on a
  `src`-layout project: 99% recall and precision (the reference also resolves by
  path suffix, so this shows the two approximations agree, not that both are
  right).
- **TypeScript.** 99% of `@/` alias imports in a known-compiling project
  resolve.

- **Live agent runs** (`yottacode run`, one model, this repository; informal).
  For "which test packages should I run after changing `internal/codemap/index.go`",
  with the flag on the model made one `code_impact` call and listed all 11
  affected packages in 9 of 9 runs; with it off it made one to three calls
  (`glob`/`grep`/`list_project_structure`, or a delegated subagent) and listed 2
  or 3 of the 11. For "where is this method used" it correctly chose `grep` and
  `lsp_references` instead. On a Python project and a TypeScript project
  (langflow) it reported 66 direct importers where an independent resolver
  found 66, and 173 where the true count is 173, while a plain `grep` run
  without the tool counted 64 and 47. These runs also exposed three bugs that unit tests
  had missed (empty LSP call hierarchy, a wrong query position, and an 18.6 s
  index build through gopls), all fixed.

What has not been measured: a controlled comparison across tasks, repositories,
and models of whether an agent with `code_impact` does better, cheaper, or
faster than one with `grep` and LSP. Any decision to graduate the flag is still
open. If the graph does not earn its
keep it should be deleted rather than promoted.

## Non-goals

- A TUI surface or additional slash commands.
- An MCP server exposing these tools. yottacode is MCP-client-only by design;
  this is permanent, not deferred.
- Persisting the index to disk, or incremental patching, until measurement shows
  rebuild latency is a real problem.
- Treating heuristic or LSP call data as authoritative.
- Rendering whole-repository graphs.
