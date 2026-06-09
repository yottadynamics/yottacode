# Built-in tools

Forty tools ship in `internal/agent` (thirty-seven always-on plus `todo_write`
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
| [`git_push`](#git_push) | required | Push the current branch to origin with deterministic upstream detection; surfaces the PR URL when one exists |
| [`git_log_file`](#git_log_file) | none | Show history for one file |
| [`git_blame_lines`](#git_blame_lines) | none | Blame a line range in a file |
| [`git_merge_base`](#git_merge_base) | none | Find merge base between two refs |
| [`git_checkpoint`](#git_checkpoint) | required | Create a local checkpoint commit |
| [`rollback`](#rollback) | required | Reset the repo to an earlier commit |
| [`run_tests`](#run_tests) | none | Run the repo's test command |
| [`list_dir`](#list_dir) | none | One-line-per-entry directory listing |
| [`glob`](#glob) | none | Doublestar pattern match |
| [`grep`](#grep) | none | Ripgrep (or GNU grep fallback) |
| [`fetch_url`](#fetch_url) | none | Fetch a single HTTP(S) URL and return capped textual content |
| [`run_bash`](#run_bash) | required | Shell command via `/bin/sh -c` |
| [`git`](#git) | varies | Unified git invocation; read-only auto-runs, mutations prompt |
| [`todo_write`](#todo_write) | none | Maintain the agent's working task plan, rendered as a card |
| [`enter_plan_mode`](#enter_plan_mode) | required | Only callable OUTSIDE plan mode; requests the read-only planning state via a [Y]/[N] card |
| [`exit_plan_mode`](#exit_plan_mode) | required | Only callable in `/plan` mode; presents the plan for user approval |
| [`Agent`](#agent) | none | Dispatch a typed subagent that runs in its own context window; see [subagents.md](subagents.md) |

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
╭ Verb(arg)
│   <body lines, capped at 10>
╰ <summary footer>
```

The gutter glyphs (`╭ │ ╰`) render dim; the header is bold; the footer
is dim, with `exit 0` in green and `exit N≠0` / `✗ <error>` in bold
red. Body rows are indented three columns under the gutter so the
shape reads as "header, indented content, footer."

**Header verbs.** The raw tool name still appears in the agent's
tool-call log; the TUI renames it for readability. Mapping:

| Tool | Header |
|---|---|
| `run_bash` | `Bash(<command>)` |
| `read_file` | `Read(<path>)` or `Read(<path> @ L<offset>+<limit>)` (images: `Read(<path>)`) |
| `read_many_files` | `Read(N files)` |
| `write_file` | `Write(<path>)` |
| `edit_file` | `Edit(<path>, single\|all)` |
| `apply_diff` | `Patch(apply)` |
| `mkdir` | `Mkdir(<path>)` |
| `copy_file` | `Copy(<src> → <dst>)` |
| `move_file` | `Move(<src> → <dst>)` |
| `delete_file` | `Delete(<path>)` |
| `list_dir` | `List(<path>)` |
| `glob` | `Glob(<pattern>)` or `Glob(<pattern> in <root>)` |
| `grep` | `Grep("<pattern>" in <path>)` |
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
| `git_checkpoint` | `Git(checkpoint)` |
| `list_git_changed_files` | `Git(list changed)` |

ASCII control characters inside an arg (a stray `\n` in a path, a tab
in a filename, etc.) are stripped before the header renders, so a
malformed arg can never break the card's box shape. Long bash
commands are clipped to fit the terminal width with a `…)` tail.

**Body.** Carries the tool's interesting output: directory entries,
grep matches, command stdout, diff hunks. Capped at 10 visible lines
with a trailing `…N more line(s)` notice — the model still receives
the full output via the agent's tool-result event. A few tools have
card-specific body shapes:

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
- **`read_file` / `write_file`** show no body — the footer's
  `N lines · M bytes` / `wrote N bytes` carries the entire signal.

**Footer.** Summarizes the call: `N entries`, `wrote N bytes`,
`N lines · M bytes [(truncated)]`, `N matches`, `exit N` (colored), or
the tool's confirmation message. When the call errored, the footer
renders `╰ ✗ <error>` in bold red and the body shows the raw error
verbatim (per-tool body shaping is bypassed for errors).

---

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
| `paths` | []string | — | Required; max 20 files |
| `offset` | int | `0` | Bytes; negatives clamped to 0 |
| `limit` | int | `524288` | Per-file cap |

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
`git apply` runs. A `Deny(Edit(<pattern>))` rule applies if any
target path matches; an `Allow(Edit(<pattern>))` rule auto-approves
only when every target path matches (mixed-path diffs still prompt).

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

No approval.

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
"no results", not as an error.

No approval.

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

Unified git tool. Args are passed as a JSON string array, never via the
shell, so there's nothing to escape and nothing to inject.

| Param | Type | Default | Notes |
|---|---|---|---|
| `args` | []string | — | e.g. `["status"]`, `["log", "--oneline", "-n", "5"]` |

Approval policy is **based on the first arg**:

- **Auto-execute** (read-only): `status`, `diff`, `log`, `show`, `blame`,
  `grep`, `ls-files`, `rev-parse`, `branch --show-current`, etc.
- **Prompt for approval** for everything else (`commit`, `push`, `pull`,
  `branch -D`, `checkout`, `reset`, `rebase`, `merge`, …).

Destructive flags (`--force`, `-f`, `-D`, `--hard`, `--delete`, …) are
called out in the approval preview with a `⚠ DESTRUCTIVE FLAG(S):` prefix
so you don't `y` past them by reflex.

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
(`pending` / `in_progress` / `completed`, at most one `in_progress`).
No filesystem or network side effects — purely a visibility primitive,
so it never prompts for approval.

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

The full documentation — file format for custom agents, the `/subagents`
command, transcript layout, and the recursion + iteration safeguards — is
in [subagents.md](subagents.md).
