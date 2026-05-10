# Built-in tools

Twenty-eight tools ship in `internal/agent`. The model sees their JSON-schema
parameters via the OpenAI tools API; users see them as `▸ preview` lines
in the TUI. All paths are resolved against the agent's working directory
(absolute paths are also accepted).

| Tool | Approval | One-line summary |
|---|---|---|
| [`read_file`](#read_file) | none | Read a UTF-8 file with optional byte offset/limit |
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
| [`git_stage_files`](#git_stage_files) | required | Stage specific files |
| [`git_unstage_files`](#git_unstage_files) | required | Unstage specific files |
| [`git_commit`](#git_commit) | required | Commit staged changes |
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

"Approval = required" means the tool always pauses for a `y` / `a` /
`N` from the user, unless an `allow` rule in
`<cwd>/.yottacode/permissions.json` (or its gitignored
`.local.json` sibling) matches the call, or `--bypass-permissions`
is set (DANGEROUS). See [architecture.md](architecture.md) for the
approval round-trip and the permissions schema.

---

## read_file

Read a UTF-8 text file. Returns the bytes between `offset` and `offset+limit`.
A `[truncated]` marker is appended when the window stops before EOF.

| Param | Type | Default | Notes |
|---|---|---|---|
| `path` | string | — | Absolute or cwd-relative |
| `offset` | int | `0` | Bytes; negatives clamped to 0 |
| `limit` | int | `524288` (512 KiB) | Hard cap is the same value |

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

Stage specific files with `git add -- ...`.

| Param | Type | Default |
|---|---|---|
| `paths` | []string | — |

Always prompts for approval.

## git_unstage_files

Unstage specific files with `git reset HEAD -- ...`.

| Param | Type | Default |
|---|---|---|
| `paths` | []string | — |

Always prompts for approval.

## git_commit

Create a commit from the currently staged changes.

| Param | Type | Default |
|---|---|---|
| `message` | string | — |

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
