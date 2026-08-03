# Built-in tools

Built-in tools ship in `internal/agent` (including the experimental LSP tools, `todo_write`,
and the `enter_plan_mode` / `exit_plan_mode` pair). The model sees their JSON-schema parameters via the
OpenAI tools API; the TUI renders each invocation as a bordered card with a
verb-style header (see [How tool calls render in the TUI](#how-tool-calls-render-in-the-tui)).
OpenAI-compatible provider tool calls are validated and normalized before local
execution: an empty arguments payload becomes `{}` (so no-argument tools such as
`exit_plan_mode` still run), while a truncated or otherwise-unparseable payload
is reported as an adapter error instead of being run locally. Arguments are also
re-sanitized when replaying history to the provider, so a malformed call
recorded earlier can't wedge every later request with a 400.
All paths are resolved against the agent's working directory (absolute paths
are also accepted).

In addition to the built-ins, **MCP tools** register dynamically when an `[[mcp_servers]]` block is present in `~/.yottacode/config.toml`. They appear as `mcp/<server>/<tool>` and flow through the same approval modal and permission rules — see [`mcp.md`](mcp.md) and the `MCP(...)` rule shape in [`security-and-allow-lists.md`](security-and-allow-lists.md#rule-shape). MCP tools default to approval-required unless the server explicitly hints them as read-only.

| Tool | Approval | One-line summary |
|---|---|---|
| [`read_file`](#read_file) | none | Read a text or image file (png/jpg/gif/webp) with optional line offset/limit |
| [`read_many_files`](#read_many_files) | none | Read multiple UTF-8 files in one call |
| [`write_file`](#write_file) | required | Overwrite or create a file |
| [`edit_file`](#edit_file) | required | Surgical `old_string`→`new_string` replacement |
| [`edit_anchored`](#edit_anchored) | required | Anchor-validated line edits after anchored reads |
| [`apply_diff`](#apply_diff) | required | Apply a unified diff patch |
| [`mkdir`](#mkdir) | required | Create a directory and missing parents |
| [`copy_file`](#copy_file) | required | Copy a file to a new path |
| [`move_file`](#move_file) | required | Move or rename a file or directory |
| [`delete_file`](#delete_file) | required | Delete a file or empty directory |
| [`list_git_changed_files`](#list_git_changed_files) | none | List changed files in the current repo |
| [`git_branch_status`](#git_branch_status) | none | Show branch/upstream/dirty state |
| [`git_show_file_at_rev`](#git_show_file_at_rev) | none | Read a file from a past revision |
| [`git_diff_files`](#git_diff_files) | none | Show a diff for refs and/or files |
| [`git_stage_files`](#git_stage_files) | required | Stage specific files or all changes |
| [`git_unstage_files`](#git_unstage_files) | required | Unstage specific files |
| [`git_create_branch`](#git_create_branch) | required | Create and switch to a new branch |
| [`git_commit`](#git_commit) | required | Commit staged changes |
| [`git_commit_context`](#git_commit_context) | none | Typed snapshot for drafting a commit message (paired with `git_commit_apply`) |
| [`git_commit_apply`](#git_commit_apply) | required | Validate a one-line subject and run `git commit` with structured result envelope |
| [`gh_pr_context`](#gh_pr_context) | none | Typed snapshot for opening a PR (base resolution, ahead-count, push state, gh availability, PR template) |
| [`gh_pr_create`](#gh_pr_create) | required | Validate title and open a PR via the `internal/github.Interface` adapter with typed result envelope |
| [`gh_pr_read`](#gh_pr_read) | none | Fetch PR metadata only via one API call (use instead of `run_bash gh pr view --json …`) |
| [`gh_pr_review_context`](#gh_pr_review_context) | none | Fetch PR metadata + diff + check rollup via the `internal/github.Interface` adapter for review |
| [`gh_pr_update`](#gh_pr_update) | required | Rewrite an existing PR's title and body via the `internal/github.Interface` adapter; title validation + non-empty-body guard |
| [`gh_pr_add_comment`](#gh_pr_add_comment) | required | Post a top-level conversation comment on a PR; body capped, approval-gated |
| [`gh_issue_read`](#gh_issue_read) | none | Fetch issue metadata + comments (use instead of `run_bash gh issue view --json …`) |
| [`gh_issue_list`](#gh_issue_list) | none | List open issues matching label/assignee/milestone filters |
| [`gh_issue_context`](#gh_issue_context) | none | Typed snapshot for opening an issue (repo detection, gh availability, template discovery) |
| [`gh_issue_create`](#gh_issue_create) | required | Validate title and open an issue via the `internal/github.Interface` adapter with typed result envelope |
| [`git_push`](#git_push) | required | Push the current branch to origin with deterministic upstream detection; surfaces the PR URL when one exists |
| [`git_log_file`](#git_log_file) | none | Show history for one file |
| [`git_blame_lines`](#git_blame_lines) | none | Blame a line range in a file |
| [`git_merge_base`](#git_merge_base) | none | Find merge base between two refs |
| [`git_checkpoint`](#git_checkpoint) | required | Create a local checkpoint commit |
| [`rollback`](#rollback) | required | Reset the repo to an earlier commit |
| [`run_tests`](#run_tests) | required | Run the repo's test command |
| [`media_probe`](#media_probe) | none | Inspect audio/video metadata with ffprobe |
| [`media_analyze`](#media_analyze) | none | Detect silence/fluff candidates with ffmpeg |
| [`media_compose`](#media_compose) | required | Assemble title cards, images, and clips into a draft MP4 with ffmpeg |
| [`media_render`](#media_render) | required | Render approved edits to YouTube/X MP4 or GIF preview profiles with ffmpeg |
| [`list_dir`](#list_dir) | none | One-line-per-entry directory listing |
| [`glob`](#glob) | none | Doublestar pattern match |
| [`grep`](#grep) | none | Ripgrep (or GNU grep fallback) |
| [`lsp_status`](#lsp_status) | none | Detect supported workspace languages, report server install/probe status, and show initialized capabilities |
| [`lsp_symbols`](#lsp_symbols) | none | Search workspace symbols through an installed language server |
| [`lsp_document_symbols`](#lsp_document_symbols) | none | List structural symbols declared in one source file |
| [`lsp_document_highlights`](#lsp_document_highlights) | none | Show current-file symbol reads/writes/text occurrences |
| [`syntax_range`](#syntax_range) | none | Offline parser-backed syntax ranges around a source position |
| [`lsp_selection_ranges`](#lsp_selection_ranges) | none | Show server-backed nested syntax ranges around a source position |
| [`lsp_definition`](#lsp_definition) | none | Find definition locations for a source position through an installed language server |
| [`lsp_type_definition`](#lsp_type_definition) | none | Find type definition locations for a source position through an installed language server |
| [`lsp_implementation`](#lsp_implementation) | none | Find implementation locations for an interface, method, or symbol |
| [`lsp_references`](#lsp_references) | none | Find reference locations for a source position through an installed language server |
| [`lsp_diagnostics`](#lsp_diagnostics) | none | Return compile/type diagnostics from an installed language server |
| [`lsp_changed_files_diagnostics`](#lsp_changed_files_diagnostics) | none | Return diagnostics for git-changed supported source files after edits |
| [`lsp_hover`](#lsp_hover) | none | Show hover/type/docs information at a source position |
| [`lsp_signature_help`](#lsp_signature_help) | none | Show callable signatures and active parameter info at a source position |
| [`lsp_code_actions`](#lsp_code_actions) | none | List quick fixes/refactors for a range without applying them |
| [`lsp_code_action_preview`](#lsp_code_action_preview) | none | Preview the WorkspaceEdit for one code action without applying it |
| [`lsp_rename_preview`](#lsp_rename_preview) | none | Preview semantic rename edits without applying them |
| [`lsp_format_preview`](#lsp_format_preview) | none | Preview formatting edits without applying them |
| [`lsp_apply_workspace_edit`](#lsp_apply_workspace_edit) | yes | Apply a previously previewed WorkspaceEdit after validation and approval |
| [`lsp_call_hierarchy`](#lsp_call_hierarchy) | none | Show incoming/outgoing calls for a source position |
| [`lsp_impact`](#lsp_impact) | none | Composite impact report for a source position: hover, definitions, references, calls, diagnostics, and Code Map imports |
| [`code_map`](#code_map) | none | Return a bounded directory/file/symbol structure map from the experimental code index |
| [`code_symbols`](#code_symbols) | none | Return indexed symbols for a file or query from the experimental code index |
| [`code_structure_projection`](#code_structure_projection) | none | Generate a compact code-structure projection for agent context |
| [`code_dependencies`](#code_dependencies) | none | Return direct import dependencies for an indexed file/path query |
| [`code_dependents`](#code_dependents) | none | Return direct import dependents for an indexed file/path query |
| [`code_impact`](#code_impact) | none | Return dependencies, dependents, transitive dependents, and cycles as a blast-radius summary |
| [`code_cycles`](#code_cycles) | none | Return import cycles, optionally narrowed to one indexed file/path query |
| [`code_map_diagram`](#code_map_diagram) | none | Return a Mermaid import dependency diagram, optionally focused around one file |
| [`pr_readiness_context`](#pr_readiness_context) | none | Gather a local PR readiness snapshot before opening or updating a PR |
| [`fetch_url`](#fetch_url) | none | Fetch a single HTTP(S) URL and return capped textual content |
| [`run_bash`](#run_bash) | required | Shell command via `/bin/sh -c` |
| [`git`](#git) | varies | Unified git invocation; read-only auto-runs, mutations prompt |
| [`todo_write`](#todo_write) | none | Maintain the agent's working task plan, rendered as a card |
| [`enter_plan_mode`](#enter_plan_mode) | required | Only callable OUTSIDE plan mode; requests the read-only planning state via a [Y]/[N] card |
| [`exit_plan_mode`](#exit_plan_mode) | required | Only callable in `/plan` mode; presents the plan for user approval |
| [`Agent`](#agent) | none | Dispatch a typed subagent that runs in its own context window; see [subagents.md](subagents.md) |
| [`dispatch`](#dispatch) | none | Experimental behind `dispatch`; fan multiple independent subtasks out to concurrent subagents |
| [`integrate`](#integrate) | none | Experimental behind `dispatch`; merge dispatch worker branches into one PR-ready integration branch |

"Approval = required" means the tool always pauses for a `y` / `a` /
`N` from the user, unless an `allow` rule in
`<cwd>/.yottacode/permissions.json` (or its gitignored
`.local.json` sibling) matches the call, or
`--yolo` is set (DANGEROUS). See
[architecture.md](architecture.md) for the
approval round-trip and the permissions schema.

## How tool calls render in the TUI

### Every tool call renders as a bordered card with three regions:

```
go test ./internal/tui/ -run TestDemo_CardOutput -v
go test ./internal/tui/ -run TestDemo_CardOutput -v | sed -n '/^─── /,/^--- PASS/p' | sed '/^--- PASS/d' 

```

```text
┌ Verb(arg) · 4s
│   <body lines, capped at 10>
└ <summary footer>
```

The gutter glyphs (`┌ │ └`) are tinted by result so a long transcript
stays scannable: a **failed call tints the whole `┌ │ └` frame red** so
the bad card is findable at a glance, while a clean call keeps the frame
neutral dim. The header is bold; the footer is dim, with `exit 0` in
green and `exit N≠0` / `✗ <error>` in bold red. Body rows are indented
three columns under the gutter so the shape reads as "header, indented
content, footer."

**Duration tag.** When a call takes at least one second, its elapsed
time is shown as compact header metadata (`· 4s`, `· 1m 03s`). Sub-second
calls — the vast majority — show no tag, so the timing surfaces only
when "how long did that take?" is a real question.

**Session stream spacing.** Completed turns end with the dim `◦ thought · <duration>` receipt, followed by one blank spacer row. The spacer separates the prior turn's timing receipt from the next prompt or tool block without adding another visible status message.

**Header verbs.** The raw tool name still appears in the agent's
tool-call log; the TUI renames it for readability. Mapping:

| Tool | Header |
|---|---|
| `run_bash` | `Bash(<command>)` |
| `read_file` | `Read(<path>)` or `Read(<path> @ L<offset>+<limit>)` (images: `Read(<path>)`; anchored reads append `· anchors`) |
| `read_many_files` | `Read(N files)` |
| `write_file` | `Write(<path>)` |
| `edit_file` | `Edit(<path>, single\|all)` |
| `edit_anchored` | `edit_anchored(<path>, N ops)` |
| `syntax_range` | `Syntax(range <path>:<line>:<character>)` |
| `apply_diff` | `Patch(apply)` |
| `mkdir` | `Mkdir(<path>)` |
| `copy_file` | `Copy(<src> → <dst>)` |
| `move_file` | `Move(<src> → <dst>)` |
| `delete_file` | `Delete(<path>)` |
| `list_dir` | `List(<path>)` |
| `glob` | `Glob(<pattern>)` or `Glob(<pattern> in <root>)` |
| `grep` | `Grep("<pattern>" in <path>)` |
| `lsp_status` | `LSP(status <path>)` |
| `lsp_symbols` | `LSP(symbols "<query>")` or `LSP(symbols "<query>" in <path>)` |
| `lsp_document_symbols` | `LSP(document symbols <path>)` |
| `lsp_document_highlights` | `LSP(document highlights <path>:<line>:<character>)` |
| `lsp_selection_ranges` | `LSP(selection ranges <path>:<line>:<character>)` |
| `lsp_definition` | `LSP(definition <path>:<line>:<character>)` |
| `lsp_references` | `LSP(references <path>:<line>:<character>)` |
| `lsp_impact` | `LSP(impact <path>:<line>:<character>)` |
| `lsp_signature_help` | `LSP(signature <path>:<line>:<character>)` |
| `fetch_url` | `Fetch(<url>)` |
| `run_tests` | `Test(<command>)` |
| `rollback` | `Rollback(<target>)` |
| `git` | `Git(<subcommand> <args>)` |
| `git_branch_status` | `Git(branch status)` |
| `git_show_file_at_rev` | `Git(show <path> @ <rev>)` |
| `git_diff_files` | `Git(diff <base>..<head>)` |
| `git_stage_files` / `git_unstage_files` | `Git(stage N files)` or `Git(stage all)` / `Git(unstage N files)` |
| `git_create_branch` | `Git(create branch <name>)` or `Git(create branch <name> from <start_point>)` |
| `git_commit` | `Git(commit)` |
| `git_log_file` | `Git(log <path>)` |
| `git_blame_lines` | `Git(blame <path>:L<a>-L<b>)` |
| `git_merge_base` | `Git(merge-base <base>..<head>)` |
| `git_diff_stat` / `git_diff_staged` / `git_diff_unstaged` | `Git(diff_stat …)` / `Git(diff_staged …)` / `Git(diff_unstaged …)` |
| `git_commits_between` / `git_branch_ahead_behind` / `git_branch_diff` | `Git(commits_between …)` / `Git(branch_ahead_behind …)` / `Git(branch_diff …)` |
| `git_commit_amend` / `git_commit_fixup` | `Git(commit_amend …)` / `Git(commit_fixup …)` |
| `git_checkpoint` | `Git(checkpoint)` |
| `list_git_changed_files` | `Git(list changed)` |

ASCII control characters inside an arg (a stray `\n` in a path, a tab
in a filename, etc.) are stripped before the header renders, so a
malformed arg can never break the card's box shape. Long bash
commands are clipped to fit the terminal width with a `…)` tail.

**Body.** Carries the tool's interesting output: directory entries,
grep matches, command stdout, diff hunks. Capped at 10 visible lines —
the model still receives the full output via the agent's tool-result
event. Listing-shaped tools keep the first 10 lines with a trailing
`…N more line(s)` notice; custom listing renderers use the same cap, so
large highlighted grep cards show at most 10 match rows plus `…N more
match(es)`. Command-envelope tools (`run_bash`,
`run_tests`, `git`) keep the **last** 10 lines behind a leading
`…N earlier line(s)` notice, because the verdict of a command — the
test summary, the final compiler error — lives at the end of its
output. A few tools have card-specific body shapes:

- **`run_bash` / `run_tests` / `git`** split their output into stdout,
  a `── stderr ──` separator, and stderr. The footer carries the
  process exit code (green when zero, red otherwise).
- **`edit_file`** renders a syntax-highlighted `-` (red) / `+` (green)
  diff in the body instead of the textual confirmation.
- **`git`** with a destructive flag (`--force`, `--hard`, `-D`,
  `--delete`, …) prepends a bold-red `⚠ DESTRUCTIVE FLAG(S): <flags>`
  row to the body so it's hard to `y` past by reflex.
- **`fetch_url`** drops the raw HTTP body and shows only the response
  metadata (`Status`, `Content-Type`, optional truncation note). The
  footer reports the response body size. The model still receives the
  full content; the user is spared 64+ KiB of minified markup.
- **`code_review_context`** shows the `## summary` digest and only true
  exception flags in the card; the full structured diff snapshot still goes
  to the model.
- **`read_file` / `write_file`** show no body — the footer's
  `N lines · M bytes` / `wrote N bytes` carries the entire signal. When
  multiple successful summary-only read cards land consecutively (`read_file`,
  `read_many_files`, `list_dir`, `list_project_structure`, `glob`), the TUI may
  group them into one visual card with one row per call. Wrapped continuation
  rows hang-indent under the row text, and overflow copy names the hidden row
  type (`read calls`, `list calls`, `glob calls`). This is display-only and does
  not change the individual tool results the model or saved session receive.
- **Provider-hosted tools** (for example xAI web search) reuse the same body
  wrapping and footer path as local tool cards, so card polish applies uniformly.

**Footer.** Summarizes the call: `N entries`, `wrote N bytes`,
`N lines · M bytes [(truncated)]`, `N matches`, `exit N` (colored), or
the tool's confirmation message. When the call errored, the footer
renders `└ ✗ <error>` in bold red and the body shows the raw error
verbatim (per-tool body shaping is bypassed for errors).

---

Successful context summarization and mid-turn compaction now use the shared
system-message grammar (`◇ context · summarized/compacted · ...`) and print a
literal `/recall <session-id>` command when a pre-summary snapshot is available.
The full snapshot path is intentionally omitted from normal scrollback; it
remains recoverable through sessions/recall tooling.

## read_file

Read a text or image file. For text files, output is `cat -n` style
(1-indexed line numbers). For image files (`.png`, `.jpg`, `.jpeg`,
`.gif`, `.webp`), the image data is returned as a native visual content
block that vision-capable models can see directly.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Absolute or cwd-relative |
| `offset` | int | `1` | 1-indexed start line (text files only) |
| `limit` | int | `2000` | Max lines to return (text files only) |
| `anchors` | bool | `false` | When true, prefix text rows as `line#anchor\tcontent` |

**Image support.** When the path points to a recognized image file and the
provider supports images in tool results (currently Anthropic only), the
tool reads the raw bytes (up to 20 MiB) and returns them as an image
content block alongside a text label like `[image: photo.png, image/png,
12345 bytes]`. On providers that don't support images in tool results, only
the text label is returned. The same deny list applies to images.

No approval — the model legitimately needs to read dotfiles, USER.md,
`/etc/os-release`, etc. A narrow deny list still applies:
`~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.netrc`, `~/.yottacode/.env`,
`~/.kube/config`, `~/.docker/config.json`, `~/.config/gh/hosts.yml`,
`~/.config/gcloud`, `<cwd>/.env`, `<cwd>/.env.local`. Reading those
returns an error — closes the silent prompt-injection exfiltration
vector. Use `run_bash` (which prompts) if you really need them.

## read_many_files

Read multiple UTF-8 text files in one call. Useful when the model needs
context from a handful of related files without paying for many separate
round-trips.

| Param | Type | Default | Notes |
|---|---|---|---|
| `paths` | []string or string | — | Required; max 20 files; a single string is accepted for one file |
| `offset` | int | `0` | Bytes; negatives clamped to 0 |
| `limit` | int | `524288` | Per-file cap |
| `anchors` | bool | `false` | When true, prefix each returned text line with `line#anchor\tcontent` |

Returns sections in the form:

```text
==> path/to/file <==
<content>
```

Each file gets its own `[truncated]` marker if needed.

## write_file

Full overwrite. Creates parent directories as needed.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Absolute or cwd-relative |
| `content` | string | — | Full new file contents |

Always prompts for approval. The preview shows the path and a 200-char
content snippet.

## edit_file

Surgical string replacement. Fails when `old_string` matches zero or
more-than-one place (uniqueness check), unless `replace_all=true`.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Must exist |
| `old_string` | string | — | Must be non-empty and != `new_string` |
| `new_string` | string | — | The replacement |
| `replace_all` | bool | `false` | Disable uniqueness check |

Always prompts for approval. The TUI's approval modal renders a colored
diff (red `−` / green `+`) so you see exactly what's about to change.

## edit_anchored

Apply line-oriented edits validated against anchors returned by `read_file(..., anchors=true)` or `read_many_files(..., anchors=true)`.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Must exist |
| `operations` | []object | — | Ordered operations; each object has `op` plus `anchor` or `start_anchor`/`end_anchor` |

Supported `op` values:

- `replace_range` — replace the inclusive line range from `start_anchor` to `end_anchor` with `new_text`
- `delete_range` — delete the inclusive line range from `start_anchor` to `end_anchor`
- `insert_before` — insert `new_text` before `anchor`
- `insert_after` — insert `new_text` after `anchor`

Anchors should be passed as full `line#hash` references, for example `42#a8f13c2b`. The tool re-reads the file and rejects stale or ambiguous anchors before writing, so it is the preferred path for drift-sensitive block edits that would be fragile with `edit_file` or a stale diff. Missing-anchor and stale-anchor failures are recoverable: re-read the target block with `anchors=true`, then retry with the current required `anchor` or `start_anchor`/`end_anchor` values.

Always prompts for approval.

## syntax_range

Return offline parser-backed syntax ranges around a source position. This is a read-only helper for choosing a local edit target before an anchored read/edit; it does not replace LSP semantic tools and it never writes files.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based line |
| `character` | int | — | Zero-based UTF-16 character offset |
| `max_results` | int | `50` | Clamped to `500` |

Output rows are `kind name [detail]\tpath:startLine:startColumn-endLine:endColumn\tlines=A-B\tanchor_read={...}`. Ranges are ordered smallest-to-largest so the agent can choose a nearby block, function, method, type, or file. The `anchor_read` JSON is a suggested `read_file` call with `anchors=true`; after that read, use `edit_anchored` for the actual write.

The first implementation is Go-only and uses the standard library parser. Other languages should use `lsp_selection_ranges` when a language server is installed until parser backends are added. Experimental behind `syntax_ranges`.

## apply_diff

Apply a unified diff patch using `git apply`. This is better than
`edit_file` for multi-hunk changes across one or more files.

| Param | Type | Default |
|---|---|---|
| `diff` | string | — |

Always prompts for approval. The diff header is parsed and each
touched file is run through the same write-path validator
`write_file` / `edit_file` use — yottacode-managed state, `.git`
internals, paths outside cwd, and symlinks are refused before
`git apply` runs. Before path validation, the tool rejects common
model-authored wrappers such as markdown fences and `apply_patch`-style
`*** Begin Patch` blocks with typed malformed-patch errors. After
validation, common model-authored defects such as miscounted `@@` hunk
headers and whitespace drift in context lines are tolerated while
applying. Malformed patch syntax (`corrupt patch`, bare `@@`, invalid
hunk headers) and stale context (`patch does not apply`) are classified
separately so the TUI can show compact recovery guidance instead of the
raw patch payload. Stale-context failures should prompt a fresh read —
often with `anchors=true` — and a fallback to `edit_anchored` when the
change no longer applies cleanly as a unified diff. A `Deny(Edit(<pattern>))` rule applies if any target
path matches; an `Allow(Edit(<pattern>))` rule auto-approves only when
every target path matches (mixed-path diffs still prompt).

## mkdir

Create a directory and any missing parents.

| Param | Type | Default |
|---|---|---|
| `path` | string | — |

Always prompts for approval.

## copy_file

Copy a file from `src` to `dst`. Creates destination parent directories if
needed.

| Param | Type | Default |
|---|---|---|
| `src` | string | — |
| `dst` | string | — |

Always prompts for approval.

## move_file

Move or rename a file or directory. Creates destination parent directories if
needed.

| Param | Type | Default |
|---|---|---|
| `src` | string | — |
| `dst` | string | — |

Always prompts for approval.

## delete_file

Delete a file or an empty directory.

| Param | Type | Default |
|---|---|---|
| `path` | string | — |

Always prompts for approval.

## list_git_changed_files

List changed files in the current git repo, combining staged, unstaged, and
optionally untracked files.

| Param | Type | Default |
|---|---|---|
| `staged` | bool | `true` |
| `unstaged` | bool | `true` |
| `untracked` | bool | `true` |

No approval.

## git_branch_status

Show the current branch, upstream, ahead/behind counts, and whether the
working tree is dirty.

This is a compact status helper for coding sessions where the model wants
repo state without parsing full `git status` output.

No parameters. No approval.

## git_show_file_at_rev

Read a file from a specific git revision without changing the working tree.
Useful for regressions, comparisons, and historical inspection.

| Param | Type | Default |
|---|---|---|
| `path` | string | — |
| `rev` | string | `HEAD` |

No approval.

## git_diff_files

Show a diff for specific refs and/or file paths.

| Param | Type | Default | Notes |
|---|---|---|---|
| `base` | string | current working tree | Optional base revision |
| `head` | string | — | Optional head revision |
| `paths` | []string | — | Restrict diff to one or more files |

Examples:
- diff current working tree: omit both `base` and `head`
- diff one revision vs working tree: set `base`
- diff two revisions: set both `base` and `head`

No approval.

## git_diff_stat

Compact diffstat — files touched plus line counts, no hunks. The cheap
first-pass review surface: call it before pulling full diffs so the
context budget goes to the files that matter.

| Param | Type | Default | Notes |
|---|---|---|---|
| `base` | string | working tree | Optional base revision |
| `head` | string | — | Optional head revision (requires `base`) |
| `paths` | []string | — | Optional path filter |

No approval.

## git_diff_staged

Diff of the staged changes — exactly what `git commit` would record.
The dedicated surface for `/git-commit`-style flows, so nothing has to
infer `--cached` by hand.

| Param | Type | Default |
|---|---|---|
| `paths` | []string | — |

No approval.

## git_diff_unstaged

Diff of the unstaged tracked edits (work not yet `git add`-ed).
Untracked files appear in no diff — enumerate those with
`list_git_changed_files`.

| Param | Type | Default |
|---|---|---|
| `paths` | []string | — |

No approval.

## git_stage_files

Stage specific files (`git add -- ...`) or all changes (`git add -A`).

| Param | Type | Default | Description |
|---|---|---|---|
| `paths` | []string | — | Paths to stage. Mutually exclusive with `all`. |
| `all` | bool | `false` | Stage all tracked, untracked, and deleted files (`git add -A`). Mutually exclusive with `paths`. |

Provide either `paths` or `all`, not both. Returns `staged N file(s)` for path
mode or `staged all changes` for the bulk mode.

Always prompts for approval.

## git_unstage_files

Unstage specific files with `git reset HEAD -- ...`.

| Param | Type | Default |
|---|---|---|
| `paths` | []string | — |

Always prompts for approval.

## git_create_branch

Create a new local branch and switch HEAD to it
(`git switch -c <name> [<start_point>]`). The branch name is validated via
`git check-ref-format --branch` before any switch happens, and the tool
refuses with a `branch_exists` error if a local branch by that name already
exists — it never overwrites or fast-forwards. The working tree is left
as-is (no precheck against dirty state); git itself will refuse the switch
if local changes would be clobbered.

| Param | Type | Default | Description |
|---|---|---|---|
| `name` | string | — | Branch name to create (required). |
| `start_point` | string | `HEAD` | Optional starting ref (commit / branch / tag). |

Returns `created=true branch=<name> from=<short_sha>` on success.

Always prompts for approval.

## git_commit

Create a commit from the currently staged changes.

| Param | Type | Default |
|---|---|---|
| `message` | string | — |

Always prompts for approval.

## git_commit_amend

Amend the last commit: fold the staged changes into it, optionally
replacing the message (empty keeps it). Rewrites that commit — the
approval preview carries a `⚠ rewrites the last commit` line, and the
tool description warns against amending already-pushed commits.

| Param | Type | Default | Notes |
|---|---|---|---|
| `message` | string | keep current | Replacement message; empty → `--amend --no-edit` |

Always prompts for approval.

## git_commit_fixup

Create a `fixup!` commit from the staged changes targeting an earlier
commit (`git commit --fixup=<commit>`). The history rewrite happens at
the user's later `git rebase --autosquash`, not here.

| Param | Type | Default |
|---|---|---|
| `commit` | string | — |

Always prompts for approval.

## git_commit_context

Composite read-only snapshot used to draft a one-line commit message
without parsing bash heredoc output. Returns labeled sections under
`## state`, `## staged.name-status`, `## staged.diff`, `## recent.subjects`,
`## branch.commits`, `## prose`, `## unstaged`, and `## untracked`. The
`## state` block carries the deterministic `staged_empty=`,
`detected_style=` (one of `conventional` / `ticket-prefix` / `plain`,
chosen by majority over the last 15 subjects), and `branch=` fields so
callers branch on typed flags rather than text inference.

Pair with [`git_commit_apply`](#git_commit_apply) — context tool gathers
state, apply tool validates and commits.

| Param | Type | Default |
|---|---|---|
| _(none)_ | | |

No approval. Parallel-safe.

## git_commit_apply

Composite mutator that validates a one-line subject and runs
`git commit -F -` against the currently staged changes. The
following rejections fire **before** invoking git (deterministic Go,
not model judgment):

- empty staging (`committed=false reason=staged_empty`)
- empty / whitespace-only message
- multi-line message (no body or footer accepted)
- subject longer than 72 characters
- subject ending in a period

Returns a typed envelope. On success: `committed=true sha=<hash>` plus
post-commit `## unstaged` / `## untracked` sections. On pre-commit hook
failure: `committed=false reason=hook_error` followed by the hook's
verbatim output. The tool never auto-retries, auto-stages, or amends.

| Param | Type | Default |
|---|---|---|
| `message` | string | — |

Always prompts for approval.

## gh_pr_context

Composite read-only snapshot used to open a pull request without
parsing multi-step bash output. Returns labeled sections under
`## state`, `## diff.stat`, `## commits.log`, and `## pr.template`.
The `## state` block carries deterministic flags callers branch on:
`resolved_base=`, `base_resolution=` (one of `explicit`, `origin-head`,
`fallback:<name>`, `unresolved`), `current_branch=`,
`base_equals_current=`, `ahead_count=`, `pushed_to_origin=`, and
`gh_available=`.

Base resolution priority: explicit `base` argument → `origin/HEAD`
symbolic ref → first of `main` / `master` / `develop` that exists
locally. The `pushed_to_origin` check uses `git ls-remote --exit-code
--heads origin <branch>` so empty-remote repos correctly report
`false` without crashing.

Pair with [`gh_pr_create`](#gh_pr_create) — context tool gathers
state, create tool validates the title and opens the PR.

| Param | Type | Default |
|---|---|---|
| `base` | string | (auto-resolved from `origin/HEAD` then fallback chain) |

No approval.

## gh_pr_create

Composite mutator that validates a PR title and opens the pull request
through the typed `internal/github.Interface` adapter. The following
rejections fire **before** dialing the adapter (deterministic Go, not
model judgment):

- empty title / body / base (`created=false reason=validation`)
- multi-line title
- title longer than 72 characters
- title ending in a period

Returns a typed envelope. On success: `created=true url=<url>
number=<n>`. On a missing or unauthenticated `gh` CLI:
`created=false reason=gh_unavailable` so the procedural `/git-create-pr`
can fall through to draft-only output without surfacing an opaque
exec failure. On other gh errors: `created=false reason=gh_error`
followed by the gh output verbatim. The tool never auto-retries,
auto-edits, or auto-merges.

The adapter behind this tool is `internal/github.TypedClient`,
backed by the `go-github/v66` REST client. Auth resolves through a
three-tier precedence chain: `$GITHUB_TOKEN` env var →
`gh auth token` shell-out (one-shot, cached for the session) →
`~/.yottacode/github.json` (yottacode-native PAT, written by a
future `yottacode setup github` flow). The `gh` CLI is no longer
required for API calls — only optionally used to source the token
when `$GITHUB_TOKEN` isn't set.

| Param | Type | Default |
|---|---|---|
| `base` | string | — |
| `title` | string | — |
| `body` | string | — |
| `draft` | bool | `false` |

Always prompts for approval.

## gh_pr_review_context

Composite read-only fetcher for the procedural `/git-review-pr`
flow. Calls the three Interface read methods (`ReadPR`,
`ListPRChecks`, `ReadPRDiff`) and folds their results into one
typed snapshot under `## state`, `## pr`, `## checks.summary`,
`## checks`, and `## diff` headers.

The `## state` block carries deterministic flags callers branch
on: `ref=`, `not_found=` (the gh CLI couldn't resolve the ref to
an existing PR), `gh_unavailable=` (gh missing or
unauthenticated), and `failing_checks=` (comma-separated names of
check runs whose conclusion was FAILURE, CANCELLED, TIMED_OUT, or
ACTION_REQUIRED). When `not_found` or `gh_unavailable` are true
the snapshot short-circuits — the pr/checks/diff sections are
omitted to keep the model's branching clean.

Failing-check classification covers both the GraphQL check-run
shape (Conclusion populated) and the legacy status-context shape
(State="FAILURE"/"ERROR" with empty Conclusion). The diff is
capped at 64 KiB with a truncation marker pointing at `gh pr
diff <ref>` for the full content.

| Param | Type | Default |
|---|---|---|
| `ref` | string | (uses current branch's PR) |

No approval. Touches the network (not parallel-safe).

## gh_pr_update

Composite mutator that rewrites an existing PR's title and body.
Paired with the procedural `/git-update-pr` slash command for the
"follow-up commits made the original description stale" workflow.

Deterministic guarantees:

- **Title validation** (reuses `validatePRTitle` from `gh_pr_create`):
  rejects empty, multi-line, oversize (>72 chars), and
  trailing-period titles before dialing the adapter.
- **Non-empty body guard:** empty body would clobber the existing
  PR description, which is almost never intended. Caught in Go
  with `updated=false reason=validation`.
- **Scope-pinned:** only edits title and body. Labels, base,
  reviewers, draft state, milestone, and projects are not
  accepted — `/git-update-pr` enforces the same scope at the
  prompt level.

Returns a typed envelope. On success: `updated=true url=<url>
number=<n>`. On a missing PR: `updated=false reason=not_found`
with a hint pointing at `/git-create-pr`. On gh-unavailable:
`updated=false reason=gh_unavailable`. On other gh errors:
`updated=false reason=gh_error` with the gh output verbatim. The
tool never auto-retries, auto-edits other fields, or auto-merges.

| Param | Type | Default |
|---|---|---|
| `ref` | string | (uses current branch's PR) |
| `title` | string | — |
| `body` | string | — |

Always prompts for approval.

## git_push

Composite mutator that pushes the current branch to origin.
Deterministic guarantees:

- **Upstream-aware:** detects whether the current branch already
  tracks an upstream and adds `-u origin HEAD` only on first push.
- **Detached-HEAD early exit:** rejects the push *before* invoking
  git when HEAD isn't on a branch — returns
  `pushed=false reason=detached_head`.
- **No force-push surface:** `--force` / `--force-with-lease` are
  intentionally not accepted. Use the unified `git` tool when you
  actually need force-push.

Returns a typed envelope. On success: `pushed=true branch=<name>
set_upstream=<bool>` plus a best-effort PR-URL lookup via
`internal/github.Interface.ReadPR`. When a PR exists for the
branch, `pr_number=<n>` and `pr_url=<url>` are populated so
callers (and `/git-push`) can surface a "PR updated" footer. When
no PR exists, the envelope hints at `/git-create-pr` as the next
step.

Git errors (exit non-zero) populate `pushed=false reason=git_error`
with the verbatim git output. The tool never auto-retries,
auto-force-pushes, or rebases to "fix" a rejection.

| Param | Type | Default |
|---|---|---|
| _(none)_ | | |

Always prompts for approval.

## git_log_file

Show history for a single file.

| Param | Type | Default |
|---|---|---|
| `path` | string | — |
| `limit` | int | `10` |

No approval.

## git_blame_lines

Show blame output for a line range in a file.

| Param | Type | Default |
|---|---|---|
| `path` | string | — |
| `start` | int | — |
| `end` | int | — |

No approval.

## git_merge_base

Find the merge base between two refs.

| Param | Type | Default |
|---|---|---|
| `base` | string | — |
| `head` | string | — |

No approval.

## git_commits_between

One-line commit summaries in `base..head` (commits reachable from
`head` but not `base`), newest first. The range view for review and PR
explanation.

| Param | Type | Default |
|---|---|---|
| `base` | string | — |
| `head` | string | `HEAD` |
| `limit` | integer | 20 |

No approval.

## git_branch_ahead_behind

Branch relationship in one call: `ahead=N behind=M` between `head` and
`base`, plus their merge base — replaces composing `merge-base` +
`rev-list --left-right --count` by hand.

| Param | Type | Default |
|---|---|---|
| `base` | string | — |
| `head` | string | `HEAD` |

No approval.

## git_branch_diff

One-stop branch review summary against a base branch: `## state`
(branch, merge base, ahead/behind), `## commits` (newest first, capped
at 30), `## changed-files` (name + status), and `## diffstat` —
everything except the hunks. The backbone for "what changed vs main?";
pull actual diffs afterwards with `git_diff_files` for just the files
that matter.

| Param | Type | Default |
|---|---|---|
| `base` | string | — |

No approval.

## git_checkpoint

Create a local checkpoint commit from all current changes.

| Param | Type | Default |
|---|---|---|
| `message` | string | `checkpoint` |

Always prompts for approval.

## rollback

Reset the git working tree to a target commit. Defaults to `HEAD~1`.
This is destructive and discards uncommitted changes.

| Param | Type | Default |
|---|---|---|
| `target` | string | `HEAD~1` |

Always prompts for approval.

## run_tests

Run a test command in the repo. Defaults to `go test ./...`.

| Param | Type | Default |
|---|---|---|
| `command` | string | `go test ./...` |
| `path` | string | `.` |

Prompts for approval in foreground use. Background dispatch workers cannot run `run_tests` because tests execute project code without a human approval surface.

## media_probe

Inspect a local audio/video file with `ffprobe` and return compact stream metadata: duration, container, video dimensions/fps/codec, audio codec/sample rate/channels, and rotation when present. The media bytes are never sent to the model.

| Param | Type | Default |
|---|---|---|
| `path` | string | — |

No approval. Requires `ffprobe` on `PATH`; `yottacode doctor` reports readiness.

## media_analyze

Run `ffmpeg` detectors and return a unified edit decision list for review before rendering. `mode=auto` runs audio silence detection when an audio stream exists and visual idle detection when a video stream exists, so silent terminal demos still produce useful candidates.

| Param | Type | Default |
|---|---|---|
| `path` | string | — |
| `mode` | string | `auto` |
| `detectors` | []string | selected from streams |
| `silence_threshold_db` | number | `-35` |
| `min_silence_duration` | number | `0.6` |
| `visual_noise_threshold` | number | `0.003` (`0.0015` in `terminal_demo`) |
| `min_idle_duration` | number | `1.0` (`0.8` in `terminal_demo`) |

Modes: `auto`, `audio_silence`, `visual_idle`, `terminal_demo`, or `all`. Returned candidates include `start`, `end`, `duration`, `detector`, `confidence`, and `reason` so the agent can explain why each cut was proposed. No approval. Requires `ffmpeg` on `PATH`.

## media_compose

Compose an approved storyboard into one draft MP4 with `ffmpeg`. It assembles ordered title-card, image, and clip segments after validating every input path as a read and the output path as a write. It refuses to overwrite existing files unless `overwrite=true` is passed.

| Param | Type | Default | Notes |
|---|---|---|---|
| `output` | string | — | Draft MP4 output path; must end with `.mp4` |
| `segments` | []object | — | Ordered `title`, `image`, or `clip` segments |
| `width` | int | `1920` | Canvas width |
| `height` | int | `1080` | Canvas height |
| `fps` | number | `30` | Output frame rate |
| `background_color` | string | `#0b0f0d` | Title-card background color |
| `overwrite` | bool | `false` | Must be explicit to replace an existing output |

Segment fields:

| Field | Applies to | Notes |
|---|---|---|
| `type` | all | `title`, `image`, or `clip` |
| `text` | `title` | Required title-card text |
| `path` | `image`, `clip` | Required local input path |
| `duration` | `title`, `image` | Required positive duration in seconds, capped at 300 seconds per synthetic segment |
| `keep_ranges` | `clip` | Optional single range to trim a clip; pre-render complex cuts with `media_render` first |
| `caption` | all | Reserved for storyboard metadata / future overlays |

Always prompts for approval. Requires `ffmpeg` on `PATH`. The first implementation renders video-only draft MP4s; use `media_render` afterward for final platform profiles and caption burn-in.

## media_render

Render an approved edit plan with `ffmpeg`. The tool validates the source/caption/intro/outro paths as reads, validates output paths as writes, and refuses to overwrite existing files unless `overwrite=true` is passed.

| Param | Type | Default | Notes |
|---|---|---|---|
| `input` | string | — | Source media file |
| `output` | string | — | Output file, or base output when multiple profiles are requested |
| `profiles` | []string | `["youtube_16x9"]` | `youtube_16x9`, `x_16x9`, `x_vertical_9x16`, `gif_preview`, `gif_preview_large` |
| `gif_width` | int | profile default | Optional GIF width override in pixels |
| `gif_fps` | number | `12` | Optional GIF frame-rate override |
| `speed` | number | `1` | Optional GIF playback speed multiplier, e.g. `1.5` or `2` |
| `keep_ranges` | []object | — | Approved ranges to keep, in seconds; multiple ranges are joined with ffmpeg concat |
| `cut_ranges` | []object | — | Approved ranges to remove; converted to keep ranges from probed duration when `keep_ranges` is omitted |
| `captions_path` | string | — | Optional `.srt` / `.ass` subtitle file to burn in |
| `intro_path` / `outro_path` | string | — | Optional composition assets |
| `overwrite` | bool | `false` | Must be explicit to replace an existing output |

Always prompts for approval. Requires `ffmpeg` on `PATH`. Multi-profile outputs append the profile name to the file stem, such as `demo-youtube_16x9.mp4`, `demo-x_16x9.mp4`, and `demo-gif_preview.gif`. Multi-range edits are rendered with ffmpeg `trim`/`atrim` + `concat`, so approved fluff cuts are actually removed from the final output. `gif_preview` renders a 960px-wide 12 fps looping GIF and `gif_preview_large` renders a 1440px-wide version for readable terminal text; both use ffmpeg palette generation/paletteuse. For slow terminal clips, set `speed` to `1.5` or `2` to shorten the GIF without changing the approved edit ranges. Use GIF profiles for short teasers because GIFs grow quickly. See [`marketing-videos.md`](marketing-videos.md) for the recommended screen-recording workflow.

## list_dir

One line per entry: `<type>\t<name>` where `<type>` is `d` (dir),
`f` (file), or `l` (symlink). Capped at 100 entries.

| Param | Type | Default |
|---|---|---|
| `path` | string | `.` |

No approval.

## glob

Doublestar pattern match (`**` recursive). Returns paths relative to
the cwd. Capped at 200 results.

| Param | Type | Default | Notes |
|---|---|---|---|
| `pattern` | string | — | e.g. `**/*.go`, `internal/agent/*.go` |
| `cwd` | string | tool's cwd | Roots the search |

No approval.

## grep

Prefers `rg` (ripgrep); falls back to GNU `grep -E`/`-F`. Pattern
arguments are passed via argv — no shell, no injection.

| Param | Type | Default | Notes |
|---|---|---|---|
| `pattern` | string | — | Required |
| `path` | string | `.` | File or directory |
| `regex` | bool | `false` | When false, treats pattern as a fixed string |
| `ignore_case` | bool | `false` | |
| `max_results` | int | `50` | Hard cap |

Output is capped at 256 KiB. Exit code 1 (no matches) is treated as
"no results", not as an error. The TUI card is separately capped at 10
visible match rows and then shows `…N more match(es)`; the model still
receives every match returned by the tool.

No approval.

## lsp_status

Detect supported source languages in the workspace and report whether the
matching language server command is installed on `PATH`. This tool is available
only when `lsp_code_intelligence` is enabled. It never installs anything; when a
server is missing it includes a concise install hint. Each row also reports the
offline structure fallback as `syntax=parser`, `syntax=regex`, or `syntax=none`.

Supported language servers and fallback modes:

| Language | Server command | Offline syntax | Install hint |
|---|---|---|---|
| Go | `gopls` | parser | `go install golang.org/x/tools/gopls@latest` and ensure `$(go env GOPATH)/bin` is on `PATH` |
| TypeScript/JavaScript | `typescript-language-server --stdio` | regex fallback | `npm install -g typescript typescript-language-server` |
| Python | `pyright-langserver --stdio` | regex fallback | `npm install -g pyright` |
| Rust | `rust-analyzer` | regex fallback | install through rustup, your package manager, or rust-analyzer's upstream docs |

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | `.` | Workspace path to scan |

No approval.

## lsp_symbols

Search workspace symbols through an installed language server. If `path` points
to a source file, its extension selects the language. If `path` is omitted or a
directory, yottacode scans the workspace and picks the first detected language
with an available server. Missing servers return `unavailable: ...` plus the
same install hint as `lsp_status`.

| Param | Type | Default | Notes |
|---|---|---|---|
| `query` | string | — | Required symbol query |
| `path` | string | `.` | File or workspace path used for language detection |
| `max_results` | int | `50` | Clamped to `500` |

No approval.

## lsp_document_symbols

List structural symbols declared in a single source file through the matching
language server. Hierarchical server responses are flattened and include the
parent symbol as the `Container` column when available.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `max_results` | int | `50` | Clamped to `500` |

No approval.

## lsp_document_highlights

Show current-file symbol occurrences for a source position through
`textDocument/documentHighlight`. This is narrower than `lsp_references`: it
returns only the open document's read/write/text highlights, so agents can
inspect local usage without pulling workspace-wide reference noise into context.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based line |
| `character` | int | — | Zero-based UTF-16 character offset |

Output rows are `kind\tpath:startLine:startColumn-endLine:endColumn`, where
`kind` is `text`, `read`, `write`, or `kindN` for unknown server values. No
approval.

## lsp_selection_ranges

Show nested syntax selection ranges around a source position through
`textDocument/selectionRange`. Ranges are returned from the smallest expression
to larger enclosing ranges such as statement, block, or function when the server
provides them. This helps agents choose the right read/edit scope before making
semantic changes.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based line |
| `character` | int | — | Zero-based UTF-16 character offset |
| `max_results` | int | `50` | Clamped to `500` |

Output rows are `depth\tpath:startLine:startColumn-endLine:endColumn`, with
depth `0` as the smallest range. No approval.

## lsp_definition

Find definition locations for a source position through the matching language
server. `line` and `character` are zero-based LSP positions; returned locations
are one-based `path:line:column` rows for terminal readability.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based line |
| `character` | int | — | Zero-based UTF-16 character offset |

No approval.

## lsp_type_definition

Find type definition locations for a source position. Parameters and output match `lsp_definition`.

No approval.

## lsp_implementation

Find implementation locations for an interface, method, or symbol position. Parameters and output match `lsp_definition`.

No approval.

## lsp_references

Find reference locations for a source position through the matching language
server. Like `lsp_definition`, inputs are zero-based and output locations are
one-based.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based line |
| `character` | int | — | Zero-based UTF-16 character offset |
| `include_declaration` | bool | `false` | Include the declaration in references |
| `max_results` | int | `50` | Clamped to `500` |

No approval.

## lsp_diagnostics

Return compile/type diagnostics for a source file by opening it in the matching
language server and reading `publishDiagnostics`. Output distinguishes published
clean results from `diagnostics not published before timeout`, and includes
severity, source, code, tags, and related locations when the server provides them.
Missing servers return an `unavailable` result with install hints.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |

No approval.

## lsp_changed_files_diagnostics

Run diagnostics across git-changed supported source files. Use this after edits in an LSP-supported language before declaring the change done. Multi-file results are summarized as clean/issues/pending/skipped counts so clean checks do not print one repeated row per file.

| Param | Type | Default | Notes |
|---|---|---|---|
| `max_files` | int | `20` | Maximum changed source files to inspect |

No approval.

## lsp_hover

Show hover/type/documentation information for a source position.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based line |
| `character` | int | — | Zero-based UTF-16 character offset |

No approval.

## lsp_signature_help

Show callable signatures at a source position, including the active signature
and active parameter when the server reports them. This is useful before writing
or changing a function call.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based line |
| `character` | int | — | Zero-based UTF-16 character offset |

No approval.

## lsp_code_actions

List language-server code actions and quick fixes for a range without applying
them. Output includes each action's zero-based index and whether it carries an
edit, command, related diagnostics, or requires a server-side resolve step. Use
`lsp_code_action_preview` for editable actions before applying them with
`lsp_apply_workspace_edit`.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based start line |
| `character` | int | — | Zero-based UTF-16 start character |
| `end_line` | int | `line` | Zero-based end line |
| `end_character` | int | `character` | Zero-based UTF-16 end character |

No approval.

## lsp_code_action_preview

Preview one code action's normalized WorkspaceEdit JSON without writing files.
Select the action by exact `title` or by zero-based `index` from
`lsp_code_actions`; `index` is preferred when titles repeat. The preview output
includes bounded diff hunks plus per-file preview hashes. Pass the returned
`apply_payload` to `lsp_apply_workspace_edit` only after reviewing the diff.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based start line |
| `character` | int | — | Zero-based UTF-16 start character |
| `end_line` | int | `line` | Zero-based end line |
| `end_character` | int | `character` | Zero-based UTF-16 end character |
| `title` | string | — | Exact code action title |
| `index` | int | `0` when `title` is omitted | Zero-based action index |

No approval.

## lsp_rename_preview

Preview a semantic rename as normalized WorkspaceEdit JSON without writing files. When the server supports `textDocument/prepareRename`, yottacode validates the target position first and returns an explicit unavailable result if the cursor is not renameable. Preview output includes bounded diff hunks plus per-file preview hashes. Pass the returned `apply_payload` to `lsp_apply_workspace_edit` only after reviewing the affected files.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based line |
| `character` | int | — | Zero-based UTF-16 character offset |
| `new_name` | string | — | Replacement symbol name |

No approval.

## lsp_format_preview

Preview server formatting edits for one file without writing files. Preview output includes bounded diff hunks plus per-file preview hashes so the later apply step can reject stale previews.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |

No approval.

## lsp_apply_workspace_edit

Apply a previously previewed WorkspaceEdit through yottacode's path validator, checkpoint snapshot flow, and normal approval modal. The language server never writes directly. Preview payloads carry per-file hashes so apply can reject stale previews, and overlapping edit ranges are refused before any file write. In this experimental version, still be cautious with non-ASCII / UTF-16-heavy edit ranges until broader real-server coverage lands.

| Param | Type | Default | Notes |
|---|---|---|---|
| `edit` | object | — | WorkspaceEdit object from a preview tool's `apply_payload` |

Requires approval.

## lsp_call_hierarchy

Show incoming and outgoing call hierarchy entries for a source position.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based line |
| `character` | int | — | Zero-based UTF-16 character offset |

No approval.

## lsp_impact

Return a composite impact report for a source position. The tool batches hover,
definition, references, call hierarchy, diagnostics, and optional Code Map import
blast radius into one compact result. It is intended for pre-refactor planning
and review, where raw LSP calls would otherwise require several tool rounds.
When `code_map` is not enabled, the Code Map section reports unavailable and the
LSP sections still run.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Required source file |
| `line` | int | — | Zero-based line |
| `character` | int | — | Zero-based UTF-16 character offset |
| `include_declaration` | bool | `false` | Include declarations in reference results |
| `max_results` | int | `50` | Cap per section, clamped to `500` |

No approval.

## code_map

Read-only. Experimental behind `code_map`.

Returns a bounded repository structure map from yottacode's code index:
directories, files, symbols, and cheap counts such as LOC and exported/private
symbols. Pass `query` to filter by path, symbol name, kind, or container. Output
is capped by `max_results` so agents can orient themselves without reading files
first.

## code_symbols

Read-only. Experimental behind `code_map`.

Returns indexed symbols either for one `path` or for a `query`. This is the
symbol-only companion to `code_map`; use it when names and locations are enough
and opening whole files would waste context.

## code_structure_projection

Read-only. Experimental behind `code_map`.

Generates a compact, token-efficient projection of the indexed structure: root
counts, important files, and their symbols. It is designed for agent context
projection, not for dependency or impact analysis. Dependency/call graph tools
will be added only after import/reference/call edges are indexed and tested.

## code_dependencies

Read-only. Experimental behind `code_map`.

Returns direct outgoing import dependencies for an indexed file/path query. The
current implementation resolves in-workspace Go package imports; unresolved
standard-library, third-party, or ambiguous imports are omitted rather than
invented.

## code_dependents

Read-only. Experimental behind `code_map`.

Returns direct incoming import dependents for an indexed file/path query. This is
the reverse edge of `code_dependencies` and is the safest first blast-radius
query for Go changes.

## code_impact

Read-only. Experimental behind `code_map`.

Returns a conservative impact summary: files the queried file imports, files
that import the queried file, transitive dependents up to `depth`, and import
cycles involving the target. `depth` defaults to all transitive dependents; pass a
positive integer to cap traversal. It does not yet include references, call
hierarchy, tests, or git-derived change frequency.

## code_cycles

Read-only. Experimental behind `code_map`.

Returns detected import cycles, optionally narrowed to cycles involving `path`.
The current implementation is Go-first and only reports cycles made from
resolvable in-workspace import edges.

## code_map_diagram

Read-only. Experimental behind `code_map`.

Returns a bounded Mermaid `graph TD` diagram from the import graph. Pass `path`
to focus the diagram around one file's direct incoming/outgoing import edges;
omit it for a bounded workspace diagram. This is intended for copy-pasting into
issues, docs, or PR descriptions.

## pr_readiness_context

Gather a cheap local PR-readiness snapshot: branch, dirty state, changed files,
and whether docs/tests were touched. It is read-only and does not contact
GitHub.

No parameters. No approval.

## fetch_url

Fetch a single HTTP or HTTPS URL and return capped textual content. This is
the local-network fallback for models that do not have provider-native hosted
web search.

| Param | Type | Default | Notes |
|---|---|---|---|
| `url` | string | — | Required; must start with `http://` or `https://` |
| `max_bytes` | int | `65536` | Hard cap is `262144`; larger values clamp to the default |

The tool only returns textual content types such as HTML, plain text, JSON,
XML, and JavaScript responses. Binary content types are rejected.

## run_bash

Run a shell command via `/bin/sh -c` in the session's cwd.

| Param | Type | Default | Notes |
|---|---|---|---|
| `command` | string | — | Passed verbatim to `/bin/sh -c` |

Always prompts for approval. Output is `exit=<code>\n--- stdout ---\n…\n--- stderr ---\n…`,
each stream capped at 1 MiB; truncation is announced in the result.

There is no in-process sandbox, and there will not be one — yottacode
keeps its core small and does not ship bwrap/firejail/landlock
backends. For real isolation, run yottacode inside a container or
devcontainer.

## git

Unified git tool. Args are normally passed as a JSON string array and run
without a shell, so there's nothing to escape and nothing to inject. For
model-authored calls that accidentally provide one string (for example
`"status --short"`), yottacode conservatively splits it into argv, rejects
shell control operators, and applies the same approval policy to the parsed
arguments.

| Param | Type | Default | Notes |
|---|---|---|---|
| `args` | []string or string | — | e.g. `["status"]`, `["log", "--oneline", "-n", "5"]`, or the tolerated string form `"status --short"` |

Approval policy is **allowlist-shaped and flag-aware**, in three tiers:

- **Auto-execute — unconditionally read-only subcommands**: `status`,
  `diff`, `log`, `show`, `blame`, `grep`, `ls-files`, `rev-parse`,
  `merge-base`, `describe`, etc. (One guard: `--output[=<file>]` turns
  these into a file write, so it always prompts.)
- **Auto-execute — read spellings of ambiguous subcommands**: listings
  like bare `branch` / `branch --list` / `branch --show-current`, bare
  `tag` / `tag -l`, bare `remote` / `remote -v` / `remote get-url`,
  `stash list` / `stash show`, and bare `reflog` / `reflog show`. The
  mutating spellings of the same subcommands (`branch -d`, `tag v1.0`,
  `remote add`, bare `stash` — which pushes! — `reflog expire`) prompt.
  Unknown flags fall through to a prompt; the policy never guesses.
- **Prompt for approval** for everything else (`commit`, `push`, `pull`,
  `checkout`, `reset`, `rebase`, `merge`, …). Global pre-subcommand
  flags (`-c`, `-C`, `--git-dir`, …) always prompt — they can
  reconfigure git underneath the subcommand check.

Approval previews carry **risk-tiered warning copy**. History-rewriting
or working-tree-destructive invocations get a specific
`⚠ HIGH RISK: <what it destroys>` line — `reset --hard` ("discards
every uncommitted change"), `clean -f`, `rebase`, `checkout --
<paths>`, `restore` (worktree), `branch -D`, `tag -d`,
`push --force[-with-lease]`, `reflog expire` — so they can't be
approved with the same reflex as an `add`. Dangerous flags outside the
classified set keep the generic `⚠ DESTRUCTIVE FLAG(S):` line.

Stdout is capped at 1 MiB; stderr at 64 KiB.

## todo_write

Maintain the agent's working task plan. The list is owned by the session and
rendered as a live card in the TUI's in-flight area — updates in place on
every `todo_write` call, so the user sees status flips without each call
stacking a new card. At turn end one final-state snapshot lands in
scrollback as the historical receipt for that turn.

| Param | Type | Default | Notes |
|---|---|---|---|
| `todos` | []object | — | Complete plan; previous list is replaced wholesale |

Each item has `content` (short human-readable description) and `status`
(`pending` / `in_progress` / `completed` / `skipped`, at most one
`in_progress`). Use `skipped` when the agent changes course and is
intentionally not doing a previously planned step; the TUI renders it as a
dim `✗` row instead of leaving stale pending work visible. No filesystem or
network side effects — purely a visibility primitive, so it never prompts
for approval.

The model is instructed to call `todo_write` proactively for any task with
three or more distinct steps and to update it as soon as each step finishes.
When work has both a design/research phase and an execution phase, the
model should ask for user agreement before implementation only when the user
explicitly requested a plan first, the session is in plan mode, or the next
step is risky/destructive/ambiguous. Routine todo-card creation is not a
permission gate; the approval policy still lives on the mutating tools
themselves. Pass an empty list to clear the plan; the live card disappears and
no end-of-turn snapshot is emitted.

## enter_plan_mode

The model's request to switch the session into plan mode — the surface behind
"make a plan first" / "drop into plan mode" asked in natural language. Mirrors
Claude Code's `EnterPlanMode`. Only advertised while plan mode is OFF (the
schema filter is the inverse of `exit_plan_mode`'s), and registered in the
TUI build only — oneshot has no approval surface to host the handshake.

| Param | Type | Default | Notes |
|---|---|---|---|
| _(none)_ | — | — | The plan topic comes from the conversation; the plan file is derived from the turn's user message |

The call renders a two-hotkey confirmation card:

- `[Y] enter plan mode` (also `Enter`) — runs the same entry sequence as `/plan`
  (exits auto mode if on, flips the shared state), then derives the plan file
  from the message that triggered the turn — so the agent can start writing
  the plan in the same turn instead of waiting for your next message.
- `[N] stay in current mode` — declines; the model is steered to continue in
  the current mode and not re-request.

`enter_plan_mode` NEVER auto-approves — not in auto mode, not under `--yolo`,
not via a permissions Allow rule. The approval round-trip is the handshake in
which the TUI actually flips the mode state; skipping it would tell the model
"entered plan mode" while the session never moved. The same guard applies to
`exit_plan_mode`. There is deliberately no model-side way to enter auto mode
or bypass: the agent can ask to *restrict* its own permissions, never to
escalate them.

Subagents never see this tool (or `exit_plan_mode`) — children inherit the
parent's plan-mode state by pointer, and only the top-level loop may
transition it.

## exit_plan_mode

Only callable while `/plan` mode is active (the registry hides it from the
adapter schema otherwise). Takes no arguments — the TUI reads the plan body
from the resolved plan file on disk and renders it in the approval card.
Matches Claude Code's `ExitPlanMode` shape exactly.

| Param | Type | Default | Notes |
|---|---|---|---|
| _(none)_ | — | — | The plan content comes from `~/.yottacode/plans/<slug>.md` |

If the plan file doesn't exist or is empty when `exit_plan_mode` is called,
the TUI auto-denies the call with a console notice — the model is expected
to write the plan to the file first, then call this tool.

The TUI renders the plan inside an approval card with four hotkeys:

- `[A] auto-approval` — exits plan mode AND turns on auto mode for the implementation, so mutating tools auto-allow without per-call prompts (safety floor still applies: `run_bash`, `git_commit`, `git_checkpoint`, `rollback`).
- `[M] manual approval` — exits plan mode and the agent resumes execution, but per-tool prompts continue as normal so you can review each step.
- `[L] later` — exits plan mode but ends the turn; the plan stays on disk for resume via `/plan list` or `--plan-resume`.
- `[K] keep planning` — stays in plan mode; model receives refinement guidance.

`--yolo` does NOT skip this approval — that approval
is the user-visible signal, not a safety gate.

While `/plan` is active, every other mutating tool is blocked except writes
to the resolved plan file under `~/.yottacode/plans/<slug>.md`. Read-only
tools auto-allow as usual; writes to the plan file auto-allow too (no per-edit
prompt — the plan file is the model's only legitimate mutation surface during
planning). See [tui-slash-commands.md#plan-mode](tui-slash-commands.md#plan-mode)
for the full plan-mode flow.

## Agent

Dispatch a typed subagent. The subagent runs `agent.Turn` in its own message
history, with a filtered tool registry and its own iteration budget. The
parent's adapter context never sees the subagent's intermediate reasoning or
tool calls — only the child's final reply is returned as the tool result.

Mirrors Claude Code's `Agent` / `Task` tool surface.

| Param | Type | Default | Notes |
|---|---|---|---|
| `subagent_type` | string (required) | — | The name of a registered agent definition (e.g. `general-purpose`, `Explore`, `Plan`, or a custom entry under `.yottacode/agents/`). |
| `prompt` | string (required) | — | The task for the subagent. The subagent has no access to the parent conversation, so be self-contained. |
| `description` | string | "" | A 3-5 word label shown to the user while the subagent runs. |
| `run_in_background` | boolean | `false` | If true (TUI only), return immediately with a task id; the subagent runs to completion in the background. `oneshot` rejects this with a recoverable error so the model can retry without the flag. |

## dispatch

Fan a batch of independent subtasks out to subagents that run concurrently. Write-capable subtasks each run in their own git worktree + branch and must declare disjoint `files`; read-only subtasks share the current working directory. Write batches default to background in the TUI and return a batch id plus worker branches immediately; all-read batches default to foreground and return the findings together.

| Param | Type | Default | Notes |
|---|---|---|---|
| `goal` | string | — | Overall objective the subtasks add up to. |
| `tasks` | []object | — | Two or more `{subagent_type, description, prompt, files}` entries. `files` is required for write-capable subagents and must not overlap. |
| `background` | bool | smart default | Defaults true for write batches in the TUI, false for all-read batches; oneshot falls back to foreground. |

No approval for orchestration. Experimental behind `dispatch`. Background workers auto-approve owned-file writes; `run_tests`, `run_bash`, git/GitHub mutations, and other approval-requiring tools are denied because there is no prompt surface. Pair with [`integrate`](#integrate) to assemble committed worker branches. See [dispatch.md](dispatch.md).

## integrate

Merge dispatch worker branches into one integration branch in a dedicated worktree. Experimental behind `dispatch`. It leaves the user's current checkout untouched. On conflict it stops, reports the conflicted files and integration worktree path, and can resume after the merge is resolved and committed.

| Param | Type | Default | Notes |
|---|---|---|---|
| `branches` | []string | — | Worker branches to merge in order. May be empty only when finalizing an existing `integration_branch` after resolving the last conflict. |
| `integration_branch` | string | generated | Existing or new integration branch. Reuse the same value to resume after conflicts. |
| `base` | string | `HEAD` | Base ref for a newly created integration branch. |

No approval. On clean success it reports the integration branch to push/open a PR from and reclaims merged worker worktrees/branches where safe.

## gh_issue_context

Composite read-only snapshot used to open an issue without parsing
bash output. Returns labeled sections under `## state`, `## template`,
`## templates`, `## blank_issue`, and `## contact_links`. The `## state`
block carries deterministic fields: `owner=`, `repo=`, `gh_available=`
(whether the GitHub auth token chain resolves — the `/git-create-issue`
directive branches to draft-only when `false`).

`## template` preserves the legacy single-template view (`path=`, optional
`choices=`, and `content=`). `## templates` is the richer chooser view:
each entry has an index, name, kind (`markdown` or `issue_form`), path,
optional description/title prefix/labels/assignees, and rendered Markdown
content. Discovery order matches GitHub's own precedence: directory-style
`.github/ISSUE_TEMPLATE/*.{md,yml,yaml}` first in filename order, then the
legacy single-file locations (`ISSUE_TEMPLATE.md` in `.github/`, the repo
root, or `docs/`, either casing). Markdown frontmatter is stripped. YAML
issue forms are parsed and rendered into Markdown sections because GitHub's
public create-issue API accepts a normal issue body, not a submitted form
payload.

`config.yml` is not treated as a template. Its `blank_issues_enabled` flag is
reported under `## blank_issue`, and `contact_links` are reported under
`## contact_links` so documentation, discussions, and security-report links
can be surfaced without creating public issues for them.

Pair with [`gh_issue_create`](#gh_issue_create) — context tool gathers
state, create tool validates the title and opens the issue.

| Param | Type | Default |
|---|---|---|
| _(none)_ | | |

No approval. Parallel-safe.


## gh_issue_create

Composite mutator that validates an issue title and opens the issue
through the typed `internal/github.Interface` adapter. The following
rejections fire **before** dialing the adapter (deterministic Go, not
model judgment):

- empty title (`created=false reason=validation`)
- multi-line title
- title longer than 72 characters
- title ending in a period

Returns a typed envelope. On success: `created=true url=<url>
number=<n>`. On a missing or unauthenticated `gh` CLI:
`created=false reason=gh_unavailable` so the procedural `/git-create-issue`
can fall through to draft-only output without surfacing an opaque
exec failure. On other gh errors: `created=false reason=gh_error`
followed by the gh output verbatim. The tool never auto-retries,
auto-edits, or auto-assigns labels beyond what the user explicitly provided
or the selected issue template declares.

The adapter behind this tool is `internal/github.TypedClient`,
backed by the `go-github/v66` REST client. Auth resolves through a
three-tier precedence chain: `$GITHUB_TOKEN` env var →
`gh auth token` shell-out (one-shot, cached for the session) →
`~/.yottacode/github.json` (yottacode-native PAT, written by a
future `yottacode setup github` flow). The `gh` CLI is no longer
required for API calls — only optionally used to source the token
when `$GITHUB_TOKEN` isn't set.

| Param | Type | Default |
|---|---|---|
| `title` | string | — |
| `body` | string | — |
| `labels` | []string | — |
| `assignees` | []string | — |

Always prompts for approval.

The full documentation — file format for custom agents, the `/subagents`
command, transcript layout, and the recursion + iteration safeguards — is
in [subagents.md](subagents.md).
