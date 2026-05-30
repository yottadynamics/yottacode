# TUI slash commands

Type `/` in the TUI to open the slash-command palette. The palette filters as you type, supports Tab completion, and can be dismissed with `Esc`.

## Command reference

| Command | Args | What it does |
|---|---|---|
| `/help` | — | List all commands with help text |
| `/quit` | — | Exit yottacode |
| `/clear` | — | Save the current session and start a fresh one |
| `/permissions` | — | Print shared and local permission file paths |
| `/system` | — | Show the active system prompt, including injected memory |
| `/sessions` | `[id\|name]` | Open the sessions picker or resume a known session directly |
| `/model` | `<name>` | Switch the active model for this session |
| `/provider` | — | Show resolved provider, API style, built-ins, capabilities, and diagnostics |
| `/doctor` | — | Probe the provider `/models` endpoint |
| `/redo` | — | Rewind the last user message and put it back in the input box |
| `/recall` | `<query>` | Search across saved sessions |
| `/summarize` | — | Compress the current session after snapshotting it |
| `/checkpoints` | — | Open the checkpoints picker — also `Esc Esc`. Restore conversation, files, or both to any prior prompt |
| `/memory` | — | Edit curated memory or browse agent-managed memories |
| `/setup` | — | Suspend the TUI and rerun setup |
| `/init` | — | Ask the agent to draft or refresh `.yottacode/YOTTACODE.md` |
| `/git-commit` | — | Compose and run a one-line commit on the staged changes. Procedural: control flow is in Go, the model only synthesizes the subject. Replaces the legacy markdown `/git:commit-message`. |
| `/git-create-pr` | `[base]` | Open a pull request for the current branch. Procedural: base resolution, ahead-count gating, push-state detection, title validation, and gh-unavailable fall-through all live in Go. Replaces the legacy markdown `/git:create-pr`. |
| `/git-review-pr` | `[ref]` | Self-review an existing pull request. Ref is a number (`17`) or branch (`feature/x`); empty defaults to the current branch's PR. Fetches PR metadata + diff + check rollup via the typed `internal/github.Interface`, surfaces failing CI at the top, emits a structured review (Failing checks / Blockers / Suggestions / Nits). Output to scrollback only — posting back to GitHub is deferred to a future `--post` flag. |
| `/git-push` | — | Push the current branch to origin. Procedural: deterministic upstream detection (adds `-u origin HEAD` only on first push), detached-HEAD early exit, no force-push surface. Surfaces "PR updated: `<url>`" when a PR exists for the branch, or points at `/git-create-pr` when one doesn't. |
| `/git-update-pr` | `[ref]` | Refresh an existing PR's title and body to match the current commit list. Ref is a number or branch; empty defaults to the current branch's PR. Keeps the existing title verbatim when scope hasn't materially changed (no cosmetic title churn); regenerates the body from the full commit log. Scope-pinned: only edits title and body — labels, reviewers, base, draft state are off-limits. |
| `/plan` | — | Toggle plan mode (also `Shift+Tab`). Type `/plan list` to open a picker and resume an earlier plan. |
| `/subagents` | `[list \| view <id> \| stop <id> \| types]` | List subagent runs, view a transcript, stop a running task, or list available agent types. See [subagents.md](subagents.md). |
| `/mcp` | `[logs <name>]` | List configured MCP servers (status + tool count), or dump a server's recent stderr with `logs <name>`. See [mcp.md](mcp.md). |
| `/theme` | `[set <name> \| <name>]` | Change the theme — opens the picker with arrow-key live preview across every registered palette (`terminal`, `catppuccin`, `dimmed`, `gruvbox`, `high-contrast`, `low-contrast`, `no-color`, `nord`, `one-dark`, `solarized-dark`, `tokyo-night`). Enter applies and persists to `~/.yottacode/config.toml`; Esc reverts. Scriptable shortcuts: `/theme set <name>` and `/theme <name>` bypass the picker. See [themes.md](themes.md). |

Beyond the built-ins, you can ship your own slash commands by dropping markdown files in a `commands/` directory — see [Custom commands](#custom-commands).

> **Auto mode and the permissions-bypass overlay are intentionally not slash commands** (mirroring Claude Code). Auto enters via `Shift+Tab` (cycle: `normal → auto → plan → normal`) or `yottacode --permission-mode auto` at startup. Permissions bypass enters only via `yottacode --dangerously-skip-permissions` at startup — there is no in-TUI toggle, no palette entry, no accidental activation. See [Auto mode](#auto-mode) and [Permissions bypass](#permissions-bypass) below.

## Provider picker

`/provider` shows the resolved provider profile and diagnostics. `/provider use <name>` switches to a configured provider directly. The provider picker also supports adding and removing profiles; adding `openai-auth` starts the browser OAuth flow inline, and adding `copilot-auth` starts the GitHub device code flow inline. Both store account-specific model lists after login.

## Sessions picker

`/sessions` opens a picker with actions for loading, resuming, renaming, and exporting sessions.

- Recent sessions are shown newest first.
- `/sessions <id-or-name>` resumes directly.
- Press `s` in the list, or `Ctrl+S` in the resume input, to toggle summarized resume for large transcripts.
- Export writes a Markdown transcript suitable for sharing or archiving.

## Memory picker

`/memory` opens a four-row picker:

- Project context: `./.yottacode/YOTTACODE.md`
- User preferences: `~/.yottacode/USER.md`
- Browse user memories (`~/.yottacode/memory/`)
- Browse project memories (`~/.yottacode/projects/<slug>/memory/`)

Opening a curated memory file (`USER.md`, `YOTTACODE.md`) suspends the TUI to `vim`; on exit, yottacode reloads memory and patches the active system prompt so the next turn sees your edits. The browse rows drop into a sub-list of agent-managed memories where `Enter` opens an entry in `vim`, `d` deletes it, `f` opens the folder in your file manager, and `Esc` returns to the root menu.

## Custom commands

Drop a markdown file in either location and it becomes a slash command:

- `~/.yottacode/commands/` — **user scope**, applies to every yottacode session for this user
- `<cwd>/.yottacode/commands/` — **project scope**, committable so a team can share commands via git

The filename (without `.md`) becomes the command name. Subdirectories namespace the name with `:` — `commands/frontend/component.md` → `/frontend:component`. Each path segment must be lowercase letters, digits, or hyphens, starting with a letter or digit; invalid segments cause the file to be skipped with a startup warning.

### Frontmatter

Optional YAML block at the top of the file sets metadata shown in the palette and `/help`:

```markdown
---
description: Review a PR and suggest changes
argument-hint: <pr-number>
---
You are reviewing PR #$1.

@docs/code-review.md

Fetch the PR diff with `gh pr diff $1`, then walk the checklist file by file.
```

- `description` — shown in the right column of `/help` and the palette. Defaults to `(custom command)` when omitted.
- `argument-hint` — shown after the command name and used by the palette: when set, pressing Enter on a highlighted command fills `/name ` and waits for input (matching how `/recall <query>` behaves) instead of firing immediately. Leave empty for commands that take no arguments.
  - **YAML gotcha:** if your hint uses square brackets (the common `[optional]` convention), quote the value: `argument-hint: '[base-branch]'`. Bare `[...]` is YAML flow-sequence syntax and will fail to parse as a string. Angle brackets like `<required>` don't need quoting.

Unknown frontmatter keys are silently ignored, so the file stays forward-compatible if more keys ship later.

### Argument substitution

Two forms are recognized in the body:

- `$ARGUMENTS` — the entire post-name remainder. For `/review-pr 123 force`, `$ARGUMENTS` expands to `123 force`.
- `$1` .. `$9` — positional arguments (whitespace-split). Missing positionals expand to the empty string.

`$10` is parsed as `$1` followed by a literal `0` — only single-digit positionals are recognized. This matches Claude Code's documented surface; if you need more arguments, use `$ARGUMENTS`.

A literal `$ABC` (not one of the recognized tokens) is left as-is.

### File references in the body

Any `@<path>` token in the resolved body is picked up by the same filerefs pipeline that handles user-typed `@` references. The file's contents inject into the system prompt before the turn fires, and the `@` is stripped from the user message so the model sees a plain path. This lets you pin a command to a specific spec or checklist file:

```markdown
---
description: Audit security-sensitive changes
---
Audit the staged diff against @docs/security-and-allow-lists.md.
List anything that violates the deny lists or skips approval gates.
```

### Conflict resolution

- **Same name in the same scope**: both copies are dropped with a startup error. The loader can't pick a winner safely — you should rename one of the files.
- **Same name across scopes**: project wins, user is shadowed. A startup notice tells you which file got shadowed.
- **Same name as a built-in** (e.g. you create `commands/help.md`): the custom command is dropped with a startup warning. Built-ins always win at the dispatcher.

Startup notices render in the same scrollback band as memory diagnostics — error lines in red, warnings in muted yellow.

### Permissions and approvals

Custom commands are a **prompt shortcut, not a permission bypass**. Typing `/release 0.2.0` does not pop an approval modal — the substituted body is sent to the agent immediately, the same way typing the prompt by hand would be. But every mutating tool the agent calls in response (`write_file`, `edit_file`, `apply_diff`, `git_commit`, `run_bash`, …) still goes through the normal per-tool approval system. A multi-step command like `/release` will surface multiple approval modals during execution, one per mutating tool call.

Three ways to reduce friction on commands you trust:

- **Auto mode** (`Shift+Tab` or `yottacode --permission-mode auto`) — edits auto-allow; `run_bash`, `git_commit`, `git_checkpoint`, and `rollback` remain in the safety floor and still prompt. See [Auto mode](#auto-mode).
- **`.yottacode/permissions.json` allow rules** — pre-approve specific shell invocations or tool patterns (`allow: ["Bash(go test*)", "Bash(go mod tidy)"]`). Rules apply equally to commands the agent calls from a custom-command turn and to anything else.
- **`yottacode --dangerously-skip-permissions`** — everything auto-runs, with a high but finite iteration budget. Use only for fully-trusted scripted runs. See [Permissions bypass](#permissions-bypass).

Per-command `allowed-tools:` frontmatter (a Claude Code feature that scopes which tools a command can call) is **not** supported in v1; the closest equivalent today is auto mode plus an `.yottacode/permissions.json` allow list. See [Out of scope](#out-of-scope-for-now).

### Out of scope (for now)

To keep the v1 surface tight, two Claude Code features were deferred:

- **`` !`<bash cmd>` `` pre-execution** — embedding shell output in the prompt before send-off. Would require a per-command permission gate, output truncation, and timeout policy. Workaround: the body can tell the agent to call `run_bash` itself (one extra round-trip, but uses the existing approval gates).
- **Per-command `model:` / `allowed-tools:` frontmatter** — pinning a command to a specific model or tool subset.

Hot-reload is also deferred — changes to `commands/` files take effect the next time yottacode starts (same behavior as memory files).

### Worked example end-to-end

1. Create `~/.yottacode/commands/review-pr.md`:

   ```markdown
   ---
   description: Review a PR and suggest changes
   argument-hint: <pr-number>
   ---
   You are reviewing PR #$1.

   @docs/code-review.md

   Fetch the PR diff with `gh pr diff $1`, then walk the checklist file by file.
   Report findings in markdown.
   ```

2. Launch `yottacode`. The palette filter `/rev` shows `/review-pr <pr-number>   Review a PR and suggest changes` alongside built-ins.
3. Hit Enter on the row — the input fills `/review-pr ` (because `argument-hint` is set).
4. Type `123` and submit.
5. The user message that lands in scrollback reads `You are reviewing PR #123. docs/code-review.md …`, with `docs/code-review.md` injected into the system prompt for that turn.
6. The agent runs the normal turn loop — tool calls, streaming reply, approvals all behave the same as if you'd typed the prompt by hand.

### Built-in defaults

Two markdown commands ship with the binary and are available on first launch — no setup, no `~/.yottacode/commands/` files needed. Both cover pre-commit / pre-PR correctness checks. They appear in the palette and `/help` under the **Custom commands:** section, tagged `(default)` so you can see they're shipped rather than authored.

```
/check:review        [base]           (default)
/check:verify        [task-or-hint]   (default)
```

> **Note:** the git workflows that used to ship as markdown defaults
> (`/git:commit-message`, `/git:create-pr`) are now the procedural
> built-ins `/git-commit` and `/git-create-pr` (see the command
> reference above). They're driven by composite Layer-1 tools so
> empty staging, ahead-count gating, oversize titles / subjects,
> trailing periods, push-state detection, gh-unavailable
> fall-through, and hook failures are caught deterministically
> rather than asked of the model in prose.
>
> Typing `/git` in the palette filters to every git-related
> built-in. The full family today is `/git-commit`,
> `/git-create-pr`, `/git-push`, `/git-update-pr`, and
> `/git-review-pr` — five procedural commands covering the
> commit → push → open PR → refresh PR → review PR workflow.
> The slugs are flat (`git-commit`, not `git:commit`) because the
> `:` namespace is reserved for custom-command path derivation;
> built-in slugs use the kebab prefix for the same palette-filter
> effect.

#### What each does

| Command | Role |
|---|---|
| `/check:review` `[base]` | Self-reviews the branch diff against the resolved base across six dimensions (correctness, scope, tests, style, security, performance). Emits findings grouped **Blocker / Suggestion / Nit** with `file:line` refs and a one-paragraph recommendation. |
| `/check:verify` `[task-or-hint]` | Detects the project's stack — **Go, Python, Java (Maven or Gradle), Rust**, plus `Makefile` as the universal fallback — and runs the appropriate build / test / lint commands. **Go runs with `-count=1` mandatory** to bypass the test cache (no stale-pass surprises). On failure, diagnoses by re-running the failing test in isolation AND checking `git log` to see if the test was touched in this branch — never declares "pre-existing" without that evidence. The argument is mixed-purpose free-form: a task description (cross-checked against the diff for scope drift) and/or a stack hint or command override (e.g., ``use `cargo make verify` ``) that single-turns unsupported stacks. Anything outside the four supported stacks falls through to "Unknown — ask the user" rather than guessing. Prints a structured **Verdict** (Done / Not done / Done with caveats / Inconclusive). |

Both use only existing tools (`run_bash`, `read_file`, `git_*`) — no new infrastructure. Each invocation runs through the normal per-tool approval gates; see [Permissions and approvals](#permissions-and-approvals).

#### Overriding a default

To customize a default's body (e.g. you want `/check:review` to enforce a team-specific checklist), drop a file at the **same name path** in user or project scope:

```
~/.yottacode/commands/check/review.md   → overrides the default for you everywhere
<repo>/.yottacode/commands/check/review.md  → overrides for anyone working in that repo
```

The override is **silent** — no startup warning fires when a user/project file shadows a default, because customizing the starter kit is the documented use case, not a misconfiguration. The override wins on every invocation; delete the file to fall back to the embedded default.

Precedence summary (highest priority first):

1. Project scope (`<cwd>/.yottacode/commands/`)
2. User scope (`~/.yottacode/commands/`)
3. Built-in defaults (embedded in the binary)

Built-in commands like `/help`, `/clear`, `/model`, `/plan` sit above all three tiers and cannot be shadowed.

## Agent Skills

A **skill** is a reusable capability playbook the agent loads on demand. Names + descriptions are always in the system prompt so the model picks the right skill by keyword match; the body is loaded only when invoked. Skills are spec-compliant with [agentskills.io](https://agentskills.io/specification), so a skill authored for Claude Code drops in without changes.

### Default policy: off

**Skills are off by default each session.** The model sees no skill list in its system prompt at startup; the SkillTool's `Skill(skill="<name>")` call returns "unknown skill" until you opt in. Open `/skills` to pick which skills to expose — selection lasts the session. A startup line like `[skills] 10 available — type /skills to enable for this session` surfaces the gate.

This trades convenience for context discipline: the model can't ambient-reach for a skill the user didn't ask about, and the prompt stays small.

### Two ways to invoke

- **Model-side** — once you've enabled it via `/skills`, the agent can call `Skill(skill="<name>")` when a user request matches a skill's described scope. The tool returns the body so the model can apply it in the current turn.
- **User-side** — type `/<skill-name>` to inject the skill body yourself, optionally with extra context (`/remote-ops tail logs on prod-app-01`). Slash invocations **bypass the enablement gate** because typing the slash IS the selection. The body lands in the next user message and the model continues from there.

### The `/skills` menu

`/skills` opens a top-level menu — pick a row to act on:

| Item | What it does |
|---|---|
| `Catalog` | Open the picker; tabs for Built-in vs Installed (user + project). |
| `Install` | Inline textinput for the source string. Submit with Enter, cancel with Esc. |
| `Check` | Run the drift report. Output lands in the transcript. |
| `Update` | Re-fetch every tracked skill from its recorded source. Output lands in the transcript. |

Inside the Catalog picker:

| Key | Action |
|---|---|
| `Up` / `Down` | Move cursor within the active tab |
| `Left` / `Right` / `Tab` | Cycle Built-in ↔ Installed (cursor resets) |
| `/` | Filter rows by substring (matches name + description) |
| `Space` | Toggle the cursor row's enablement |
| `a` / `n` | Enable / disable all in the current tab |
| `Enter` | Open the cursor row's body in `$PAGER` (for review) |
| `u` | Uninstall the cursor row (Installed tab only; built-ins are embedded) |
| `Esc` | Save enablement toggles and close (writes the enabled set to `[skills] default_on` so it survives restart; uninstalls already took effect) |

While the filter is active: type to narrow rows, `Backspace` edits, `Enter` keeps the filter and resumes row navigation, `Esc` clears the filter and exits filter mode.

The Catalog shows every loaded skill in the active tab with its source tag and description. On commit, the system prompt is recomposed so the next turn sees the updated "Available skills" section.

### Persistent default-on set

The Catalog picker auto-saves your enablement on Esc — committed toggles are written to `~/.yottacode/config.toml` as `[skills] default_on`, so the next session restores the same set without you re-picking. Matches Claude Code's auto-persisting `/skills` and Hermes Agent's saved enablement.

You can also hand-edit the block if you prefer config-as-code:

```toml
[skills]
default_on = ["test-driven-development", "diagnose"]
```

Either way, names that don't match any loaded skill produce a stderr warning at startup so a typo surfaces immediately. Without this block (or after un-toggling everything), sessions start with nothing enabled (the small-prompt default).

Uninstalling a skill from the Catalog (Installed tab → `u`) also scrubs its name from `default_on` so the startup warning doesn't fire for an entry the picker itself just removed.

### Install, list, show, uninstall

`/skills` overloads on subcommand — no args opens the picker; the four subcommands mirror the `yottacode skills` CLI tree for in-TUI management.

| Form | What it does |
|---|---|
| `/skills install <source> [--force]` | Install a skill from a local path, `https://.../SKILL.md` URL, or `owner/repo[/path]` GitHub shorthand. Refuses to overwrite an existing slug unless `--force` is set. |
| `/skills show <name>` | Print one skill's full body — the same bytes the model receives when it calls `Skill(skill="<name>")`. |
| `/skills uninstall <name>` | Remove a user-scope skill from `~/.yottacode/skills/`. Built-in and project-scope skills are out of scope (project skills are committed source — remove via git/rm; built-ins are embedded in the binary). |
| `/skills check [name]` | Report drift between the installed bytes and `~/.yottacode/skills/.lock.json`. Statuses: `ok`, `modified`, `missing-lock`, `orphaned-lock`, `hash-error`. Read-only. |
| `/skills update [name] [--force]` | Re-fetch from the originally-recorded source. Skips installs whose on-disk hash diverges from the lockfile unless `--force` is set, so hand-edits aren't silently overwritten. |

Source shapes:

```text
./path/to/skill           local directory containing SKILL.md
./path/to/skill/SKILL.md  local SKILL.md file (no resources copied)
https://.../SKILL.md      single-file fetch — URL must end in /SKILL.md
owner/repo                GitHub repo root (must contain SKILL.md)
owner/repo/path/to/skill  GitHub subpath; scripts/, references/, assets/
                          are walked via the Contents API
```

The installer writes into `~/.yottacode/skills/<slug>/` where `<slug>` is taken from the SKILL.md frontmatter `name` — so a source dir named `weird-name` whose frontmatter says `name: sample` lands at `~/.yottacode/skills/sample/`. The on-disk dir name is always the canonical slug.

Authenticated GitHub fetches: set `GITHUB_TOKEN` to lift the 60-req/hr unauthenticated rate limit on the Contents API. Only sent to `api.github.com` hosts.

### Provenance and updates

Every install records a row in `~/.yottacode/skills/.lock.json`:

```json
{
  "version": 1,
  "entries": {
    "remote-ops": {
      "name": "remote-ops",
      "source_type": "github",
      "source": "obra/superpowers/skills/remote-ops",
      "hash": "sha256:…",
      "installed_at": "2026-05-27T12:00:00Z",
      "trust": "unverified"
    }
  }
}
```

The lockfile is dot-prefixed so the skill loader skips it. `trust` is reserved for a future signing/trust subsystem and is always `"unverified"` in this release.

`/skills check` compares the on-disk hash of every installed skill to its recorded hash; `/skills update` re-runs the installer against the recorded `source` and refreshes the lockfile entry. The dirty check on `update` ensures a hand-edit is never silently overwritten — you'll see `skipped-user-modified` until you pass `--force` to confirm the overwrite.

### Discovery

Three tiers, project wins:

1. **Project scope** — `<cwd>/.yottacode/skills/<slug>/SKILL.md` (committable)
2. **User scope** — `~/.yottacode/skills/<slug>/SKILL.md`
3. **Built-in** — 16 skills compiled into the binary:
   - **Engineering loop**: `test-driven-development`, `verification-before-completion`, `diagnose`, `writing-plans`, `executing-plans`, `brainstorming`, `receiving-code-review`, `handoff`
   - **Architecture & perf**: `improve-codebase-architecture`, `prototype`, `performance-profiler`
   - **Targeted reviews**: `dockerfile-review`, `security-auditor`, `webapp-testing`
   - **Ops & history**: `remote-ops`, `git-investigation`

A skill's directory name must match its frontmatter `name` exactly. Names that would shadow a built-in slash command (`help`, `plan`, etc.) are dropped with a startup warning.

### SKILL.md format

```markdown
---
name: remote-ops
description: SSH/scp/rsync playbook for connecting to remote hosts.
license: MIT
metadata:
  author: you
  slash: "true"
allowed-tools: Bash(ssh:*) Bash(scp:*) Read
---
# Remote operations

…body in markdown…
```

| Field | Required | Notes |
|---|---|---|
| `name` | yes | `[a-z0-9-]{1,64}`, must match parent dir |
| `description` | yes | 1-1024 chars; keyword-rich (drives matching) |
| `license` | no | string or LICENSE.txt reference |
| `compatibility` | no | ≤500 chars, documentation-only |
| `metadata` | no | free-form map for host-specific keys |
| `metadata.slash` | no | `"false"` opts out of the `/<name>` palette entry; default is exposed |
| `allowed-tools` | no | **parsed but not enforced in v1** — gated on the per-tool sandbox direction |

A skill may ship `scripts/`, `references/`, `assets/` subdirectories. The body references them by relative path (`./scripts/check.sh`); the agent reads them via `read_file` or runs them via `run_bash` on demand.

### Out of scope (for now)

- `allowed-tools` enforcement — landing alongside the broader per-tool sandbox work.
- Ed25519-signed skills + a public registry — post-v0.4.0 per the roadmap. The lockfile's `trust` field is reserved for this.

## Plan mode

`/plan` (or `Shift+Tab`) toggles plan mode — a read-only research state that mirrors Claude Code's `/plan`. The agent investigates the request, asks clarifying questions, and writes a plan file under `~/.yottacode/plans/<slug>.md`. While plan mode is on:

- Read-only tools (`read_file`, `grep`, `glob`, `list_*`, `git_log_file`, `fetch_url`, …) work normally.
- `todo_write` works normally.
- `write_file` / `edit_file` / `apply_diff` are blocked except when writing to the resolved plan file — writes to the plan file auto-allow without a prompt (it's the only legitimate mutation surface during planning).
- Every other mutating tool (`run_bash`, `git_commit`, `git_stage_files`, …) returns a "tool unavailable in plan mode" message to the model.
- A one-line banner immediately above the cmdline shows the mode, the plan file name (or "pending" before the file exists), and the current agent activity during a turn.

`/plan` and `Shift+Tab` take no arguments — the plan slug is derived from the first user message of the plan-mode session. The banner shows "ready — your next message names the plan" until that message arrives. You can also launch directly into plan mode with `yottacode --permission-mode plan`.

If the model surfaces material ambiguity during investigation — questions whose answers would change the plan's scope, approach, or target files — it is instructed to ask in its reply and end the turn *without* calling `exit_plan_mode`, so you can answer in your next message. The approval modal is hotkey-only ([A]/[M]/[L]/[K]); putting dangling questions next to it would leave you with no way to type answers. Trivia that doesn't change the plan's shape can still live in the plan's "Open questions" section.

When the model finishes investigating and the plan is unambiguous, it calls the `exit_plan_mode` tool — which takes no arguments; the TUI reads the plan body from the file on disk and renders it in an approval card with four hotkeys:

- **`[A]` auto-approval** — exits plan mode AND enters auto mode for the implementation. Edits auto-allow; `run_bash`, `git_commit`, `git_checkpoint`, and `rollback` still prompt (safety floor).
- **`[M]` manual approval** — exits plan mode and the agent immediately resumes execution. Per-tool approval prompts continue as normal, so you can review each step.
- **`[L]` later** — exits plan mode but signals the model to *end the turn now without implementing*. The plan file stays on disk; resume any time via `/plan list` or `yottacode --plan-resume <slug>`.
- **`[K]` keep planning** — stays in plan mode; the model gets refinement guidance and is expected to revise the plan file and call `exit_plan_mode` again.

If the plan file is missing or empty when `exit_plan_mode` is called, the TUI auto-denies with a console notice.

## Auto mode

Press `Shift+Tab` from normal mode (or launch with `yottacode --permission-mode auto`) to enter auto mode — a state where mutating tools auto-allow without the per-tool approval modal. Reduces friction during a multi-step implementation when you trust the plan. Mirroring Claude Code, auto mode has no slash command; the entry points are the `Shift+Tab` cycle, the `--permission-mode auto` startup flag, and the plan-card's `[A]` auto-approval hotkey.

Safety floor (always prompts even in auto mode):

- `run_bash` — arbitrary shell commands.
- `git_commit` — writes permanent git history.
- `git_checkpoint` — writes a checkpoint commit.
- `rollback` — resets the repo state.

**`run_bash` carve-out for read-only inspection.** The model habitually opens implementation work with `cd <project> && grep …` chains. To keep auto-mode flow uninterrupted, a `run_bash` call auto-allows (without showing the modal) when every segment uses a verb from a built-in read-only allowlist (`ls`, `cat`, `head`, `tail`, `wc`, `grep`, `rg`, `find`, `awk`, `cut`, `sort`, `uniq`, `diff`, `cd`, `pwd`, `which`, `echo`, `date`, `tree`, `stat`, `file`, `du`, `df`, …) AND no segment carries a risk flag (no `>` redirects, no pipe-into-shell, no sudo). Anything mutating (`rm`, `mv`, `touch`, `mkdir`, `curl`, `go test`, `sed -i`, …) still prompts.

Auto mode and plan mode are mutually exclusive — entering one exits the other. The two share the `Shift+Tab` chord:

```
Shift+Tab cycle:  normal → auto → plan → normal
```

The plan-approval card's `[A]` auto-approval hotkey is a shortcut: it approves the plan AND enters auto mode in one keystroke, so the agent can implement the approved plan with minimal friction. (Pick `[M]` instead if you want plan mode to exit but keep per-tool prompts.)

Auto mode persists across turns until you toggle it off. The banner above the cmdline (`▸ auto mode · edits + read-only bash auto-allow; commits prompt`) is always visible while active so the state isn't easy to forget.

The default per-turn iteration cap is 50; auto mode raises the effective cap to 200 (4×). If you still hit the cap on long implementations, run `/max-iterations 500` (sanity ceiling) or relaunch with `--dangerously-skip-permissions` (raises the cap to `max-iterations × 20`, at least 1000; see [Permissions bypass](#permissions-bypass)).

## Permissions bypass

Permissions bypass is the unrestricted overlay — every tool auto-runs (`run_bash`, `git_commit`, edits, everything), and the iteration cap is raised to a generous but finite budget (`max-iterations × 20`, at least 1000) so a runaway model still terminates. Intended for unattended long-running implementations where you've decided no further oversight is needed. Internally still called "yolo" in the codebase (a holdover identifier); the user-facing label everywhere now reads "permissions bypass" to match the `--dangerously-skip-permissions` flag.

Mirroring Claude Code, the overlay enters **only via `yottacode --dangerously-skip-permissions` at startup**. There is no slash command, no `Shift+Tab` binding, and no in-TUI toggle — opt in once per process, and recovery requires restarting yottacode without the flag. This is deliberate: the high-autonomy state should be a conscious one-time decision, not a key chord away.

The overlay is a **modifier**, not a mode — once active, it sits on top of normal, auto, or plan. Entering auto or plan via `Shift+Tab` does not turn bypass off. The bypass banner takes visual priority while it's on (it's the loudest signal), and when a mode (auto or plan) is also active, the mode banner picks up a `⚠ bypass` suffix instead.

Explicit `deny` rules in `.yottacode/permissions.json` still win — the bypass overlay is "skip prompts," not "ignore my policy." `Ctrl+C` is the escape hatch if a model goes into a runaway loop. The banner (`⚠ permissions bypass · all tools auto-allow · high iteration cap`) renders in red so the state isn't easy to forget; when a mode (auto or plan) is also active, the mode banner picks up a `⚠ bypass` suffix instead.

Plan-mode state is per-launch — a new `yottacode` session starts in normal mode, and resuming an old session never re-enters plan mode automatically. Plan files persist on disk under `~/.yottacode/plans/`, sorted newest-first.

To resume an earlier plan:

- **`/plan list`** opens a picker over saved plans (newest first). Enter resumes; Esc closes. Resuming attaches the plan file to plan mode (creates the mode if it's off) and re-applies the per-tool write allowance.
- **`yottacode --plan-resume <slug-or-substring>`** at launch matches the substring against saved plans (case-insensitive) and resumes the most recent match. Unmatched values fall back to a fresh plan with a stderr warning.

Plans never expire automatically — clean up the directory manually if it gets crowded.

The plan-mode gate runs *before* permissions evaluation, so explicit deny rules in `.yottacode/permissions.json` still win. `--dangerously-skip-permissions` does not skip the `exit_plan_mode` approval card — that approval is the user-visible signal, not a safety gate.

## Checkpoints (`/checkpoints` / `Esc Esc`)

Every user prompt automatically creates a checkpoint *before* the agent responds: a snapshot of the conversation history plus the pre-edit contents of any files the agent is about to touch. `/checkpoints` or **`Esc Esc`** (double-tap within 500ms) opens a picker over those checkpoints, newest first.

Pick a checkpoint and choose one of four actions:

- **Restore code and conversation** — rewrite tracked files and rewind history to the moment that prompt was sent. The original prompt reappears in the input box so you can edit and resend.
- **Restore conversation only** — rewind history; files are left untouched.
- **Restore code only** — rewrite tracked files; conversation continues from where it is now.
- **Summarize from here** — compress history up to that prompt, then keep going. Files untouched.

**What's tracked.** Only file changes made through the `write_file`, `edit_file`, `apply_diff`, `delete_file`, `move_file`, and `copy_file` tools. Mirrors Claude Code's `/rewind`. **Bash mutations (`rm`, `sed`, `mv`, redirects), git operations, and external edits to files are NOT tracked** — those are off-checkpoint side effects.

**Storage.** `~/.yottacode/checkpoints/<session>/`. File pre-images are content-addressed and deduped across checkpoints, so editing the same file twice only stores its two distinct pre-images. Checkpoints expire 30 days after creation by default; configure with `[checkpoints] retention_days = N` in `~/.yottacode/config.toml`. Sweep runs opportunistically on session open.

**Caveats.** Directories (`mkdir`) aren't restored. Permission bits on restored files are limited to `0o777` (no setuid/setgid). Restoring code under an active turn is not allowed — the picker is gated until the turn ends.

## Interrupting a turn

Pressing **Enter** while the agent is thinking captures whatever you typed, cancels the in-flight iteration, and queues the message for auto-submission the moment the loop unwinds. Any tokens that streamed before the cancel land in history as a partial assistant message, and any tool calls that were in flight or queued get a synthetic `interrupted by user` tool result so the next turn sees a valid conversation. This is the "interrupt with feedback" path — works the same in normal, plan, and auto modes.

Press **Esc** or **Ctrl+C** while a turn is running to cancel without submitting. Any queued message is dropped; the textarea contents are preserved so a draft survives an accidental Esc.

Slash commands typed mid-turn (e.g. `/clear`, `/model`) follow the same rule they always have — they cancel the turn and execute immediately. Slash commands that the codebase marks `PreservesTurn=true` (`/subagents`, `/help`) inspect without cancelling. Either way, a slash command mid-turn discards any plain-text message that was queued by an earlier Enter, so a `/clear` doesn't resurrect a stale follow-up message into a wiped session.

## Palette behavior

- Choosing a command with no args executes it immediately.
- Choosing a command that needs args fills the command prefix, such as `/model `, and waits for input.
- If you already typed a full command with args, Enter executes what you typed.

## Keyboard shortcuts

- `Enter` submits (mid-turn: interrupt and queue the new message)
- `Ctrl+J` inserts a newline
- `Esc` cancels the current turn (alias for Ctrl+C, mirrors Claude Code)
- `Esc Esc` (idle, tapped within 500ms) opens the `/checkpoints` picker
- `Ctrl+C` cancels the current turn; quits when no turn is running
- `Ctrl+D` exits when input is empty
- `?` opens the cheatsheet when input is empty
- `Shift+Tab` cycles agent modes: normal → auto → plan → normal

The TUI uses inline rendering rather than an alternate screen, so your terminal scrollback remains available.
