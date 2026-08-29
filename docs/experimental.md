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
| `dispatch` | experimental | The `dispatch` + `integrate` tools — fan a batch of subtasks out to concurrent subagents (write-capable ones in isolated git worktrees, partitioned by file ownership), then merge committed branches into one integration branch for a PR. See [dispatch.md](dispatch.md), incl. its Known Limitations. |
| `document_generation` | **graduated** (GA) | `create_document` is now default-on for every format, including docx/pdf (via `pandoc`, routed through the active command sandbox; pdf also needs `weasyprint`). The flag is a no-op kept for one release so existing configs don't break. A missing pandoc/weasyprint binary returns an actionable error naming exactly where it looked, rather than failing silently. See [document-generation.md](document-generation.md). |
| `document_ingestion` | **graduated** (GA) | `read_document` is now default-on for every format, including PDF (via `pdftotext`/`pdfinfo`, routed through the active command sandbox). The flag is a no-op kept for one release so existing configs don't break. A missing pdftotext/pdfinfo binary returns an actionable error naming exactly where it looked, rather than failing silently. See [tools.md](tools.md#read_document). |
| `lsp_code_intelligence` | **graduated** (GA) | LSP Code Intelligence is now default-on in TUI and oneshot sessions. The flag is a no-op kept for one release so existing configs don't break. Servers are still lazy-started only when semantic tools need them, never installed automatically, and missing servers degrade to install hints plus lexical fallback. See [lsp.md](lsp.md). |
| `sandbox` | **graduated** (GA) | The command sandbox is now governed by `[sandbox].backend = "podman"`; this flag is a no-op kept for one release so existing configs don't break. When enabled in config, supported command execution routes through lazy rootless Podman profile containers with host networking as the temporary default, project-dir-only mounts, and no host fallback on startup/use failure. See [sandbox.md](sandbox.md). |
| `syntax_ranges` | **graduated** (GA) | The read-only `syntax_range` agent tool is now default-on for Go, TypeScript/JavaScript, Python, and Rust, returning parser-backed block/function/type/file ranges around a source position plus `read_file(anchors=true)` hints. The flag is a no-op kept for one release so existing configs don't break. See [tools.md](tools.md#syntax_range). |

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
lsp_code_intelligence = true    # GA/no-op compatibility flag
syntax_ranges = true            # GA/no-op compatibility flag
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

1. **Flip every use site to a literal `true`.** The plumbing field stays;
   only the `expSet.IsEnabled(...)` read goes away. Check *both*
   entry points — `internal/tui/run.go` and
   `internal/oneshot/oneshot.go` — and remember a feature often has
   more than one gate per file (tool construction and prompt
   composition are separate sites).
2. **Keep the constant** in `internal/experimental/features.go` and
   leave it in `All()`. Rewrite its doc comment to the stock form:
   *"X is a graduated no-op flag kept recognized for one release so
   old configs don't warn or break."*
3. **Add it to `IsGraduated()`.** This is what makes `/experimental`
   render `[GA]` instead of `[off]`.
4. **Rewrite its `Description()`** to say the feature has graduated
   and the flag is recognized as a no-op for compatibility.
5. **Update the docs**: this file's catalog row (status →
   `**graduated** (GA)`, config sample comment → `# GA/no-op
   compatibility flag`), the feature's own doc page, the per-tool
   line in `tools.md`, a `CHANGELOG.md` entry under Unreleased, and
   a `README.md` bullet if the capability is headline-worthy.

Because the constant stays recognized, an existing
`--experimental foo` (or `[experimental] foo = true`) for a
graduated feature is **completely silent** — no warning, and the
feature works because it's default-on now. That's the point: a
graduation must never make someone's working config start
complaining. `features_test.go` enforces the invariant that
everything in `All()` is `Recognized()` and has a non-empty
`Description()`, so a half-finished graduation fails CI.

Dropping the constant outright is a separate, later cleanup — and
it *does* make the name warn like a typo, so it should wait until
the flag has been a documented no-op for at least a release.

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
