# TUI slash commands

Type `/` in the TUI to open the slash-command palette. The palette filters as you type, supports Tab completion, uses the same `❯` row cursor as the larger sub-pickers, renders above the cmdline, and can be dismissed with `Esc`. Command matching accepts prefixes, mid-name substrings, and short word-initial filters such as `/grp` for `/git-review-pr`. Commands that open a full picker (`/model`, `/theme`, `/sessions`, `/mcp`, and the rest) render as a centered floating window over the conversation rather than replacing the cmdline area — the transcript and status bar stay visible around it.

## Command reference

| Command | Args | What it does |
|---|---|---|
| `/help` | — | List all commands in a scrollable popup. Use wheel, ↑/↓, PgUp/PgDn, Home/End to scroll; Esc or the `×` closes it. |
| `/quit` | — | Exit yottacode |
| `/clear` | — | Save the current session and start a fresh one |
| `/map` | `[query]` | Open the experimental code map. `/map here` shows changed files and their immediate import neighborhood; Enter inserts the selected file/symbol as an `@path` prompt reference. Other modes stay under the same command: `/map deps <path>`, `/map dependents <path>`, `/map impact [--depth N\|all] <path>`, `/map cycles [path]`, and `/map diagram [path]`. Enable with `--experimental code_map`. |
| `/permissions` | — | Open the shared/local permission files and show advisory warnings for broad or shadowed rules |
| `/system` | — | Show the active system prompt, including injected memory |
| `/usage` | — | Show per-session token totals by model, a per-tool call/token/error breakdown, efficiency signals (low-signal turns, repeated identical tool calls, repeated-failure guidance, and a floor waste estimate), per-subagent task detail, cache hit rate, the session's largest turn, compaction history, today's sessions individually (largest first, current session marked with an inspectable short id), live rate-limit headroom (including both Codex quota windows on a ChatGPT subscription), and a per-provider billing-dashboard link. Scrolls with ↑/↓ or PgUp/PgDn when content exceeds the terminal. No dollar estimate — token counts are exact, but cost would need an unmaintainable price table. See [cost.md](cost.md). |
| `/inspect` | — | Open a paged picker over the live session plus saved sessions, then inspect the selected session in a read-only, scrollable turn-by-turn replay without resuming it. Direct `/inspect <session-id>` still works for known ids/names/unique short ids, but the command is picker-first in the TUI. Shows each user prompt preview, assistant preview, tool calls with truncated args, tool outcomes, per-turn tokens, and low-signal markers without switching the live conversation. Export sessions from `/sessions` instead. |
| `/sessions` | `[id\|name]` | Open the sessions picker or resume a known session directly |
| `/model` | `<name>` | Switch the active model for this session |
| `/provider` | — | Show resolved provider, API style, built-ins, capabilities, and diagnostics |
| `/context` | — | Show context-window diagnostics: resolved window, thresholds, tool-schema overhead, largest buckets, compaction status, and MCP/memory/skills breakdowns |
| `/effort` | `[default\|low\|medium\|high]` | Set reasoning effort for this session on providers that support it. Bare opens a picker; a positional argument sets it directly. `default` (aliases `off`/`none`) injects no reasoning override — every provider behaves as if `/effort` were never used. Supported active models show the current level in the status bar. Session-only, mirroring `--reasoning-effort`. See [providers.md](providers.md#reasoning-effort). |
| `/advisor` | `[on\|off]` | Configure cache-safe role routing between an advisor model and an implementer model. Bare opens a picker with rows for Routing, Advisor model, Implementer, and Fallback; the picker stays open while rows act in place. The Routing row toggles on/off; model rows open a list of configured models (catalog + `providers.models`). You can enable routing first and pick the `advisor_model` / `implementer_model` below — routing turns on automatically once both are set — or pick the models and then toggle on. Choices **persist to `config.toml`** and apply live; picking a catalog model also adds it to that provider's `providers.models` so the write validates. `on`/`off` are quick shortcuts for the toggle (also persist); `on` means `auto` (default session and `/plan` → advisor; auto-mode work, summarization, and delegated subagents → implementer, unless an agent pins an explicit `model:`). Top-level implementer turns can call `consult_advisor`; advisor turns and plan mode do not expose it. Reasoning effort stays global via `/effort`; `/advisor` does not have per-role effort fields. The status bar keeps the active model visible and shows `auto` as a separate mode segment instead of a separate `routing:` chip. `manual` mode (route only subagents with explicit `model:` frontmatter) stays config-only. |
| `/doctor` | — | Probe provider auth and model access. CLI `yottacode doctor` is broader and also reports GitHub, LSP, media, and sandbox readiness. |
| `/redo` | — | Rewind the last user message and put it back in the input box |
| `/recall` | `<query>` | Search saved sessions in an interactive results overlay |
| `/summarize` | — | Compress the current session after snapshotting it. Successful automatic compaction lands as one compact system-message row with the before/after context size, `full history saved`, and a copyable `yottacode sessions resume <id>` command that reopens the full pre-compression transcript as a fresh session; warning color is reserved for failures or non-convergent summaries. |
| `/checkpoints` | — | Open the checkpoints picker — also `Esc Esc`. Restore conversation, files, or both to any prior prompt |
| `/memory` | — | Edit curated memory or browse agent-managed memories |
| `/video` | `[edit\|analyze] <path>` or `prompt <goal>` | Guide media workflows: clean up one recording, or plan an asset-based marketing video from docs/screenshots/clips and render only after approval |
| `/setup` | — | Suspend the TUI and rerun setup |
| `/init` | — | Ask the agent to draft or refresh `.yottacode/YOTTACODE.md` |
| `/git-commit` | — | Compose and run a one-line commit on the staged changes. Procedural: control flow is in Go, the model only synthesizes the subject. Replaces the legacy markdown `/git:commit-message`. |
| `/git-push` | — | Push the current branch to origin. Procedural: deterministic upstream detection (adds `-u origin HEAD` only on first push), detached-HEAD early exit, no force-push surface. Surfaces "PR updated: `<url>`" when a PR exists for the branch, or points at `/git-create-pr` when one doesn't. |
| `/git-create-pr` | `[base]` | Open a pull request for the current branch. Procedural: base resolution, ahead-count gating, push-state detection, title validation, and gh-unavailable fall-through all live in Go. Replaces the legacy markdown `/git:create-pr`. |
| `/git-update-pr` | `[ref]` | Refresh an existing PR's title and body to match the current commit list. Ref is a number or branch; empty defaults to the current branch's PR. Keeps the existing title verbatim when scope hasn't materially changed (no cosmetic title churn); regenerates the body from the full commit log. Scope-pinned: only edits title and body — labels, reviewers, base, draft state are off-limits. |
| `/git-create-issue` | `[title]` | Create a GitHub issue in the current repo. Optional title arg; interactively composes if omitted. Uses `issue_context` + `issue_create` tools for template detection, validation, and creation. |
| `/git-review-pr` | `[ref]` | Self-review an existing pull request. Ref is a number (`17`) or branch (`feature/x`); empty defaults to the current branch's PR. Fetches PR metadata + diff + check rollup via the typed `internal/github.Interface`, surfaces failing CI at the top, emits a structured review (Failing checks / Blockers / Suggestions / Nits). Output to scrollback only — posting back to GitHub is deferred to a future `--post` flag. |
| `/code-review` | `[low\|medium\|high]` | Multi-agent review of the **local** diff (the current branch against its resolved base, or the uncommitted working tree when there are no commits ahead). The main agent reads the diff via the `code_review_context` tool, whose TUI card shows a digest (`base → head`, diff source, file/line counts, and true warnings only) while the model receives the full structured snapshot. It crafts review "angles", fans them out to read-only background subagents, dedups their candidate findings, verifies them, and emits one structured review (Blockers / Suggestions / Nits) to scrollback. Effort scales the fan-out: `low` (2 finders, no verification), `medium` (4 finders, verify uncertain findings), `high` (up to 8 finders, one verifier per finding). Background subagents are GA in the interactive TUI — no experimental flag required. Finders and verifiers are read-only by design — the `review` and `code-verifier` agent types carry no shell tool at all, so verification argues from the code rather than executing it. (For build/test probes, the separate `verification` agent type with `run_bash` remains available outside this command.) Output to scrollback only; the author owns the changes. |
| `/plan` | — | Toggle plan mode (also `Shift+Tab`). Type `/plan list` to open a picker and resume an earlier plan. Agent todo cards can mark abandoned steps as skipped (`✗`) so changed-course plans do not remain visually pending. |
| `/auto` | — | Toggle auto mode (also `Shift+Tab` and `--permission-mode auto`). Edits auto-allow; shell and git-history operations still prompt. See [Auto mode](#auto-mode). |
| `/yolo` | — | Toggle yolo mode (also `Shift+Tab` and `--yolo` at startup). Auto-runs every tool with NO safety floor and raises the iteration cap to a large finite bound; explicit `deny` rules still win. Run `/yolo` again to restore approvals. See [Yolo mode](#yolo-mode). |
| `/subagents` | `[list \| view <id> \| stop <id> \| stop batch <batch-id> \| types]` | List subagent runs, view a transcript (`Enter`), stop a running task (`s`) or a whole dispatch batch, inject a finished task's result into the conversation (`i`), or list available agent types. See [subagents.md](subagents.md). |
| `/experimental` | — | List active experimental features and graduated compatibility flags. Graduated entries such as `background_subagents` and `lsp_code_intelligence` show as GA/no-op; active experiments such as `dispatch` show ON/off. Read-only; enabling active experiments happens via `--experimental <name>`, `YOTTACODE_EXPERIMENTAL`, or the `[experimental]` config block — see [experimental.md](experimental.md). |
| `/mcp` | `[logs <name>]` | List configured MCP servers (status + tool count), or dump a server's recent stderr with `logs <name>`. See [mcp.md](mcp.md). |
| `/theme` | `[set <name> \| <name>]` | Change the theme — opens the picker with arrow-key live preview across every registered palette (`terminal`, `catppuccin`, `dimmed`, `gruvbox`, `high-contrast`, `low-contrast`, `no-color`, `nord`, `one-dark`, `solarized-dark`, `studio-dark`, `tokyo-night`). Enter applies and persists to `~/.yottacode/config.toml`; Esc reverts. Scriptable shortcuts: `/theme set <name>` and `/theme <name>` bypass the picker. See [themes.md](themes.md). |
| `/loop` | `<interval> [Nx] <prompt>` | Repeat a prompt or slash command on an explicit interval. `/loop 5m <prompt>` fires every 5 minutes; `/loop 30s /context` runs a slash command every 30 seconds; `/loop 3x <prompt>` is rejected because the interval is required; `/loop stop` (or `Esc` / `Ctrl+C`) ends it. Each iteration is an ordinary turn — output streams to scrollback and is saved to the session, and the standard per-tool approval gates apply. In-memory only (ends on quit). See [Recurring loops](#recurring-loops-loop). |

Beyond the built-ins, you can ship your own slash commands by dropping markdown files in a `commands/` directory — see [Custom commands](#custom-commands).

> `Shift+Tab` cycles `normal → plan → auto → yolo → normal`. The same states are slash-addressable with `/auto`, `/plan`, and `/yolo`; `/auto` keeps the auto safety floor, while `/yolo` is the explicit always-approve path with no safety floor. See [Auto mode](#auto-mode) and [Yolo mode](#yolo-mode) below.

## Provider picker

`/provider` shows the resolved provider profile and diagnostics. `/provider use <name>` switches to a configured provider directly. The provider picker also supports adding and removing profiles; adding `openai-auth` starts the browser OAuth flow inline, and adding `copilot-auth` starts the GitHub device code flow inline. Both store account-specific model lists after login.

## Sessions and recall pickers

`/sessions` opens a picker with actions for loading, resuming, renaming, and exporting sessions.

- Recent sessions are shown newest first.
- `/sessions <id-or-name>` resumes directly.
- Press `s` in the list, or `Ctrl+S` in the resume input, to toggle summarized resume for large transcripts.
- Export writes `.md` Markdown transcripts for sharing or `.jsonl` schema-versioned structured activity logs for team audit/debugging, based on the path extension. Review JSONL files before sharing them because prompts, tool args/results, paths, command output, and image metadata can contain sensitive local context.

`/recall <query>` searches older saved sessions by content and opens a transient results overlay below the cmdline. Results are grouped by session with a hit count, so one noisy conversation does not fill the whole list. Use `↑`/`↓` to select a session, `Enter` to preview matches with the neighboring turn before and after each hit, `↑`/`↓` or `PgUp`/`PgDn` to scroll long previews, `s` to toggle summarized resume, `Enter` again to resume it, and `Esc` to go back or close. Results are not appended to the conversation transcript, so recall searches do not pollute session scrollback.

Compaction (auto or manual `/summarize`) snapshots the full pre-compression transcript before compressing and prints a copyable `yottacode sessions resume <id>` command in the receipt — run it (outside the TUI, or after quitting) to reopen that transcript verbatim as a fresh session, rather than parsing the snapshot filename by hand.

## Memory picker

`/memory` opens the memory picker:

- Project context: `./.yottacode/YOTTACODE.md`
- User preferences: `~/.yottacode/USER.md`
- Browse user memories (`~/.yottacode/memory/user/`)
- Browse project memories (`~/.yottacode/memory/projects/<slug>/`)
- Enable semantic memory search (shown when local embeddings are not active)

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
- **`.yottacode/permissions.json` allow rules** — pre-approve specific typed tools or shell invocations (`allow: ["Tests(go *)", "Github(read_*)", "Edit(docs/**)"]`). Rules apply equally to commands the agent calls from a custom-command turn and to anything else.
- **`yottacode --yolo`** — everything auto-runs, with a high but finite iteration budget. Use only for fully-trusted scripted runs. See [Yolo mode](#yolo-mode).

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

A **skill** is a reusable capability playbook the agent loads on demand. Names + descriptions are always in the window — carried by the `Skill` tool's schema so the model picks the right skill by keyword match — while the body is loaded only when invoked. Skills are spec-compliant with [agentskills.io](https://agentskills.io/specification), so a skill authored for Claude Code drops in without changes.

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
| `Catalog` | Open the picker; tabs for Official and Bundled fallback. Official is the default browse/install view and marks already-installed rows inline. |
| `Install` | Inline textinput for the source string. Submit with Enter, cancel with Esc. |
| `Uninstall` | Focused list of installed (user-scope) skills; Enter removes the selected one. Built-in and project skills aren't listed. |
| `Check` | Run the drift report. Output lands in the transcript. |
| `Update` | Re-fetch every tracked skill from its recorded source. Output lands in the transcript. |

Inside the Catalog picker:

| Key | Action |
|---|---|
| `Up` / `Down` | Move cursor within the active tab |
| `Left` / `Right` / `Tab` | Cycle Official ↔ Bundled (cursor resets) |
| `/` | Filter rows by substring (matches name + description) |
| `Space` | Installed catalog rows: toggle enablement. Not-yet-installed catalog rows: shows the install hint. Bundled rows: toggle enablement |
| `a` / `n` | Enable / disable all toggleable rows in the current tab |
| `Enter` | Official row: install if not installed, otherwise open the installed body in `$PAGER`. Bundled row: open the body in `$PAGER` |
| `u` | Uninstall the cursor row when it is an installed user-scope Official skill |
| `r` | Refresh the offline Official catalog metadata from `yottadynamics/yottacode-skills` |
| `Esc` | Save enablement toggles and close (writes the enabled set to `[skills] default_on` so it survives restart; uninstalls already took effect) |

While the filter is active: type to narrow rows, `Backspace` edits, `Enter` keeps the filter and resumes row navigation, `Esc` clears the filter and exits filter mode.

The Catalog shows every row with a status column. Official rows show `[not installed]`, `[installed]`, or `[installed/enabled]`; `installed/enabled` means the skill is available to the model this session. Bundled rows show `[ ]` / `[x]` for model access. On commit, the system prompt is recomposed so the next turn sees the updated "Available skills" section.


### Persistent default-on set

The Catalog picker auto-saves your enablement on Esc — committed toggles are written to `~/.yottacode/config.toml` as `[skills] default_on`, so the next session restores the same set without you re-picking. Matches Claude Code's auto-persisting `/skills` and Hermes Agent's saved enablement.

You can also hand-edit the block if you prefer config-as-code:

```toml
[skills]
default_on = ["test-driven-development", "diagnose"]
```

Either way, names that don't match any loaded skill produce a stderr warning at startup so a typo surfaces immediately. Without this block (or after un-toggling everything), sessions start with nothing enabled (the small-prompt default).

Uninstalling a user-scope official skill from the Catalog (`u` on an installed Official row) also scrubs its name from `default_on` so the startup warning doesn't fire for an entry the picker itself just removed.

### Install, list, show, uninstall

`/skills` overloads on subcommand — no args opens the picker; the four subcommands mirror the `yottacode skills` CLI tree for in-TUI management.

| Form | What it does |
|---|---|
| `/skills install <source> [--force]` | Install a skill from `official/<name>`, a local path, `https://.../SKILL.md` URL, or `owner/repo[/path]` GitHub shorthand. Refuses to overwrite an existing slug unless `--force` is set. In the `/skills` menu, installs run with an inline spinner so network fetches do not look frozen. |
| `/skills official <name>` | Shortcut for installing from the public `yottadynamics/yottacode-skills` catalog. Equivalent to `/skills install official/<name>`. |
| `/skills catalog refresh` | Refresh the offline Official catalog metadata cache from `yottadynamics/yottacode-skills`. Browsing the Catalog itself never contacts GitHub; installs still fetch the selected skill. In the Catalog picker, refresh shows an inline spinner until GitHub responds. |
| `/skills show <name>` | Print one skill's full body — the same bytes the model receives when it calls `Skill(skill="<name>")`. |
| `/skills uninstall <name>` | Remove a user-scope skill from `~/.yottacode/skills/`. Built-in and project-scope skills are out of scope (project skills are committed source — remove via git/rm; built-ins are embedded in the binary). |
| `/skills check [name]` | Report drift between the installed bytes and `~/.yottacode/skills/.lock.json`. Statuses: `ok`, `modified`, `missing-lock`, `orphaned-lock`, `hash-error`. Read-only. |
| `/skills update [name] [--force]` | Re-fetch from the originally-recorded source. Skips installs whose on-disk hash diverges from the lockfile unless `--force` is set, so hand-edits aren't silently overwritten. The `/skills` menu Update action shows an inline spinner while tracked skills are refetched. |

Source shapes:

```text
official/<name>            public yottacode-skills catalog
https://www.skills.sh/<owner>/<repo>/<skill>
                            skills.sh page URL; copies scripts/, references/, assets/
./path/to/skill           local directory containing SKILL.md
./path/to/skill/SKILL.md  local SKILL.md file; copies sibling resources
https://.../SKILL.md      non-GitHub single-file fetch — URL must end in /SKILL.md
owner/repo/path/to/skill  GitHub shorthand; copies resources from archive
https://github.com/owner/repo/tree/<ref>/path/to/skill
https://github.com/owner/repo/blob/<ref>/path/to/skill/SKILL.md
https://raw.githubusercontent.com/owner/repo/<ref>/path/to/skill/SKILL.md
                            GitHub URLs; copies resources from archive
```

GitHub-backed installs use repository archives rather than the GitHub Contents API, so they avoid the low unauthenticated REST rate limit while still copying full skill resource directories. Non-GitHub direct `SKILL.md` URLs remain single-file because plain HTTP has no standard directory listing for sibling resources.

### Provenance and updates

Every install records a row in `~/.yottacode/skills/.lock.json`:

```json
{
  "version": 1,
  "entries": {
    "remote-ops": {
      "name": "remote-ops",
      "source_type": "official",
      "source": "official/remote-ops",
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
2. **User scope** — `~/.yottacode/skills/<slug>/SKILL.md`, including official catalog installs from `yottadynamics/yottacode-skills`
3. **Bundled fallback** — skills compiled into the binary so yottacode has an offline starter set:
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
- Ed25519-signed skills + a richer public registry/search API — post-v0.4.0 per the roadmap. The public `yottadynamics/yottacode-skills` repo is the official free catalog; paid/private skills and future runtime plugins intentionally live in separate repositories with their own access, signing, and compatibility rules.

## Plan mode

`/plan` (or `Shift+Tab`) toggles plan mode — a read-only research state that mirrors Claude Code's `/plan`. The active state is visible immediately: the status bar shows a foreground-only `plan` mode chip, and a `▸ plan mode` banner appears above the cmdline as soon as the mode is entered (it adds the plan filename once the slug resolves). The agent investigates the request, asks clarifying questions, and writes a plan file under `~/.yottacode/plans/<slug>.md`. While plan mode is on:

- Read-only tools (`read_file`, `grep`, `glob`, `list_*`, `git_log_file`, `fetch_url`, …) work normally.
- `todo_write` works normally.
- `write_file` / `edit_file` / `apply_diff` are blocked except when writing to the resolved plan file — writes to the plan file auto-allow without a prompt (it's the only legitimate mutation surface during planning).
- Every other mutating tool (`run_bash`, `git_commit`, `git_stage_files`, …) returns a "tool unavailable in plan mode" message to the model.
- A one-line banner immediately above the cmdline shows the mode right away, then the plan file name after the file exists, and the exit keys (`exit with /plan or Shift+Tab`) whenever there is room — so how to leave the mode stays visible after the entry card scrolls away. The same keys are echoed on the entry card's footer and (as re-entry keys) on the exit log line.

`/plan` and `Shift+Tab` take no arguments — the plan slug is derived from the first user message of the plan-mode session. The banner shows `▸ plan mode` immediately, then adds the resolved plan filename after that message arrives. You can also launch directly into plan mode with `yottacode --permission-mode plan`.

**Model-requested entry.** Asking the agent to plan in natural language ("make a plan first", "drop into plan mode") makes it call the `enter_plan_mode` tool, which renders a `[Y]/[N]` confirmation card. `[Y]` runs the same entry sequence as `/plan` and derives the plan file from the message you just sent — the agent can start writing the plan in the same turn. `[N]` declines; the agent continues in the current mode. This entry request never auto-approves — not in auto mode, not under `--yolo` — and there is no model-side equivalent for auto mode: the agent cannot escalate its own permissions, only ask to restrict them.

If the model surfaces material ambiguity during investigation — questions whose answers would change the plan's scope, approach, or target files — it is instructed to ask in its reply and end the turn *without* calling `exit_plan_mode`, so you can answer in your next message. The approval modal is hotkey-only ([A]/[M]/[L]/[K]); putting dangling questions next to it would leave you with no way to type answers. Trivia that doesn't change the plan's shape can still live in the plan's "Open questions" section.

When the model finishes investigating and the plan is unambiguous, it calls the `exit_plan_mode` tool — which takes no arguments; the TUI reads the plan body from the file on disk and renders it in an approval card with four hotkeys:

- **`[A]` auto-approval** — exits plan mode AND enters auto mode for the implementation. Edits auto-allow; `run_bash`, `git_commit`, `git_checkpoint`, and `rollback` still prompt (safety floor).
- **`[M]` manual approval** — exits plan mode and the agent immediately resumes execution. Per-tool approval prompts continue as normal, so you can review each step.
- **`[L]` later** — exits plan mode but signals the model to *end the turn now without implementing*. The plan file stays on disk; resume any time via `/plan list` or `yottacode --plan-resume <slug>`.
- **`[K]` keep planning** — stays in plan mode; the model gets refinement guidance and is expected to revise the plan file and call `exit_plan_mode` again.

If the plan file is missing or empty when `exit_plan_mode` is called, the TUI auto-denies with a console notice.

## Auto mode

Type `/auto`, advance to auto through `Shift+Tab` (`normal → plan → auto`), use the plan-card `[A]` hotkey, or launch with `yottacode --permission-mode auto` to enter auto mode — a state where mutating tools auto-allow without the per-tool approval modal. Reduces friction during a multi-step implementation when you trust the plan, while keeping shell and git-history operations behind prompts.


Safety floor (always prompts even in auto mode):

- `run_bash` — arbitrary shell commands.
- `git_commit` — writes permanent git history.
- `git_checkpoint` — writes a checkpoint commit.
- `rollback` — resets the repo state.

**`run_bash` carve-out for read-only inspection.** The model habitually opens implementation work with `cd <project> && grep …` chains. To keep auto-mode flow uninterrupted, a `run_bash` call auto-allows (without showing the modal) when every segment uses a verb from a built-in read-only allowlist (`ls`, `cat`, `head`, `tail`, `wc`, `grep`, `rg`, `find`, `awk`, `cut`, `sort`, `uniq`, `diff`, `cd`, `pwd`, `which`, `echo`, `date`, `tree`, `stat`, `file`, `du`, `df`, …) AND no segment carries a risk flag (no `>` redirects, no pipe-into-shell, no sudo). Anything mutating (`rm`, `mv`, `touch`, `mkdir`, `curl`, `go test`, `sed -i`, …) still prompts.

Auto mode and plan mode are mutually exclusive; yolo is an always-approve overlay. The three share the `Shift+Tab` chord:

```
Shift+Tab cycle:  normal → plan → auto → yolo → normal
```

The chord works mid-turn too: the loop reads the mode flags on every tool dispatch, so the new mode applies from the agent's next tool call. Press once to pull the agent into read-only planning, press again to move into bounded auto mode, or press again to enter yolo when you explicitly want always-approve — all without cancelling the turn. The chord is consumed by an open palette, and while an approval modal is pending its own hotkeys own the keyboard.

The plan-approval card's `[A]` auto-approval hotkey is a shortcut: it approves the plan AND enters auto mode in one keystroke, so the agent can implement the approved plan with minimal friction. (Pick `[M]` instead if you want plan mode to exit but keep per-tool prompts.)

Auto mode persists across turns until you toggle it off. The foreground-only banner above the cmdline (`▸ auto mode · edits + read-only bash auto-allow; commits prompt · Shift+Tab cycles`) is always visible while active so the state isn't easy to forget — the trailing `Shift+Tab cycles` hint is how you leave (it drops first on narrow terminals). The entry log shows the full cycle (`auto → yolo → normal → plan`); the exit log shows the re-enter key.

The default per-turn iteration cap is 128; auto mode raises the effective cap to 512 (4×). If you still hit the cap on long implementations, run `/max-iterations 500` (sanity ceiling) or relaunch with `--yolo` (raises the cap to `max-iterations × 20`, 2560 by default and at least 1000; see [Yolo mode](#yolo-mode)).

## Yolo mode

Yolo mode is the unrestricted overlay — every tool auto-runs (`run_bash`, `git_commit`, edits, everything), and the iteration cap is raised to a generous but finite budget (`max-iterations × 20`, 2560 by default and at least 1000) so a runaway model still terminates. Intended for unattended long-running implementations where you've decided no further oversight is needed. The startup flag is `--yolo`; the slash command is `/yolo`; the in-TUI banner label reads "yolo mode" (the codebase calls the overlay state "yolo" internally).

There are three ways in:

- **`yottacode --yolo` at startup** — the startup opt-in. Running `/yolo` later can turn the overlay off.
- **`/yolo` slash command** — mid-session toggle; running `/yolo` again turns it off.
- **`Shift+Tab` cycle** — the final always-approve stop after auto mode (`normal → plan → auto → yolo → normal`).


The overlay is a **modifier**, not a mode — once active, it sits on top of normal, auto, or plan. Entering auto or plan via `Shift+Tab` does not turn yolo mode off. The yolo banner takes visual priority while it's on (it's the loudest signal), and when a mode (auto or plan) is also active, the mode banner picks up a `⚠ yolo mode` suffix instead.

Explicit `deny` rules in `.yottacode/permissions.json` still win — the yolo overlay is "skip prompts," not "ignore my policy." `Ctrl+C` is the escape hatch if a model goes into a runaway loop. The banner (`⚠ yolo mode · all tools auto-allow · no iteration cap`) renders as foreground-only red/bold text so the state is loud without relying on unreadable background blocks; when a mode (auto or plan) is also active, the mode banner picks up a `⚠ yolo mode` suffix instead.

Plan-mode state is per-launch — a new `yottacode` session starts in normal mode, and resuming an old session never re-enters plan mode automatically. Plan files persist on disk under `~/.yottacode/plans/`, sorted newest-first.

To resume an earlier plan:

- **`/plan list`** opens a picker over saved plans (newest first). Enter resumes; Esc closes. Resuming attaches the plan file to plan mode (creates the mode if it's off) and re-applies the per-tool write allowance.
- **`yottacode --plan-resume <slug-or-substring>`** at launch matches the substring against saved plans (case-insensitive) and resumes the most recent match. Unmatched values fall back to a fresh plan with a stderr warning.

Plans never expire automatically — clean up the directory manually if it gets crowded.

The plan-mode gate runs *before* permissions evaluation, so explicit deny rules in `.yottacode/permissions.json` still win. `--yolo` does not skip the `exit_plan_mode` approval card — that approval is the user-visible signal, not a safety gate.

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

Pressing **Enter** while the agent is thinking queues whatever you typed for delivery at the next safe tool boundary without cancelling the active tool call. If an approval or path-trust modal is open, the modal hotkeys still win, but any other non-empty Enter is queued visibly for delivery after the decision instead of being swallowed. If the model finishes before it reaches a tool boundary, yottacode starts a fresh turn with the queued message after the current turn ends. A second queued message stays in the textarea and shows a queue-full notice instead of interrupting the session.

Press **Esc** or **Ctrl+C** while a turn is running to cancel without submitting. Any queued message is dropped; the textarea contents are preserved so a draft survives an accidental Esc.

Slash commands typed mid-turn (e.g. `/clear`, `/model`) follow the same rule they always have — they cancel the turn and execute immediately. Slash commands that the codebase marks `PreservesTurn=true` (`/subagents`, `/help`) inspect without cancelling. Either way, a slash command mid-turn discards any plain-text message that was queued by an earlier Enter, so a `/clear` doesn't resurrect a stale follow-up message into a wiped session.

## Recurring loops (`/loop`)

`/loop` re-runs a prompt or slash command on an explicit interval until you stop it, the agent stops it (a prose loop can end itself via the `loop_control` tool once its goal is met — see below), or it expires. It's a **scheduler over the normal turn loop, not a cloud/background worker** — it spawns no durable job, never runs more than one agent turn at a time, and each iteration is an ordinary turn (output streams to scrollback and is saved to the session; the standard per-tool approval gates apply). Multiple loops can be active in one terminal session, each with a `loop-...` ID. Loops are local/in-memory: quitting yottacode stops them, and graceful exit shows a warning first.

Forms:

| Form | Behavior |
|---|---|
| `/loop 5m <prompt>` | Every 5 minutes, dispatch `<prompt>`. Any Go duration works (`30s`, `5m`, `1h`); the minimum interval is 5s. |
| `/loop 2m check current PR CI and stop when all checks are green` | Run a prose agent turn on an interval. Useful for polling CI or external state and stopping once the condition is met. |
| `/loop 30s /context` | Run a slash command on the interval instead of a prose prompt. |
| `/loop 30s 3x <prompt>` | Bounded: run three iterations, then disarm. |
| `/loop stop <id>` | Disarm one loop by ID, e.g. `/loop stop loop-a1b2c3`. |
| `/loop stop all` | Disarm every active loop. |
| `/loop` | Open a dismissable panel below the cmdline listing active loops (IDs, intervals, remaining count, expiry, payloads). Any key closes it; the loops keep running. |

Behavior notes:

- **One agent turn at a time.** If an iteration's turn is still running when another loop's interval fires, that tick is skipped (not queued) and that loop re-arms its next interval — cadence holds without stacking turns.
- **Multiple local loops.** A terminal session can have multiple active loops. Each loop gets a `loop-...` ID; use `/loop` to list them. Loops are in-memory only and stop on quit, `/clear`, or session switch.
- **Default expiry.** Every loop auto-expires after 5 days. This is a safety cap for local loops, not durable cloud scheduling.
- **Always visible.** While loops are armed, a `loop · loop-a1b2c3 · every 5m` or `loops · 3 active` banner sits above the cmdline (like the auto/plan/yolo banners), so you can't forget one is running. Bounded loops also print a `[loop] loop-a1b2c3 iteration 2/3` line each cycle.
- **Arm card.** Arming a loop prints a gutter card to scrollback with the ID, cadence, remaining count, expiry, and the full payload, so a long prose prompt stays readable instead of being crammed onto one line:

  ```
  ╭ loop · loop-y7j152
  │ every 2m · unbounded · expires in 4d
  │ Research accountants in Apex and Cary. I need a good accountant for my
  │ personal taxes
  ╰ /loop stop loop-y7j152
  ```
- **Status panel.** Bare `/loop` (no args) opens a dismissable **menu below the cmdline** — one compact row per loop (`<id>   every 1m · unbounded · expires in 4d · <payload…>`), in the same picker style as `/model`, with a `2 active loops` header and the stop/dismiss hint. It does **not** write to the session/transcript, so checking your loops never clutters scrollback. Any key dismisses it (the loops keep running). With nothing armed, `/loop` just prints a one-line hint.
- **Stopping.** Use `/loop stop <id>` to stop one loop, `/loop stop all` to stop every loop, or `/loop stop` when exactly one loop is active. If the stopped loop is the one whose iteration is running, its turn is cancelled too; stopping a *different* loop leaves the running turn alone. `Esc` or a first `Ctrl+C` stops **all** active loops (and cancels a mid-flight turn). Graceful `/quit` or `Ctrl+D` warns before stopping active loops.
- **The loop assesses itself and can stop.** During a **prose** iteration the agent is offered a `loop_control` tool **and** a system addendum that tells it to evaluate, every iteration, whether the loop should keep running. It stops when the loop's stated stop-condition is met (e.g. `/loop 2m check CI and stop when green` and CI is now green) **or** when the request is effectively a one-off it has already answered and repeating would only reproduce the same result — it calls `loop_control` with `action: "stop"` and a one-line reason, the loop disarms after that turn, and prints `[loop] <id> stopped by the agent: <reason>`. It deliberately keeps running when you're *polling* for a change that hasn't happened yet (CI still red, deploy still running), so a monitor doesn't quit just because nothing changed this tick. The tool + addendum are present **only** during a prose loop iteration — hidden from ordinary turns and from slash-command loops, so the model can't stop a loop that isn't running. (Note: an open-ended payload with no goal, like `check for accountants in Apex`, is treated as a one-off — it answers once and stops rather than re-answering forever.)
- **Permissions are not bypassed.** An iteration that hits an un-allowlisted git command (or any gated tool) pauses on the normal approval prompt and waits — nothing runs unattended that wouldn't prompt interactively. To make a loop hands-off, pick "always allow" once, add an `.yottacode/permissions.json` allow rule, or run under `--yolo`.
- **In-memory only.** The loop lives on the session in memory; quitting yottacode ends it (the output already streamed is still saved). It does not persist across restarts.
- **Guarded payloads.** `/loop`, `/quit`, and `/clear` are refused as payloads — a loop must not re-arm, exit, or reset the very session it runs in — and an unknown slash command is rejected at arm time rather than looping "unknown command" forever.
- **Payloads that start no turn.** Interval prose loops disarm when the prose can't start an agent turn, such as when no provider is configured. Interval slash loops may be informational and are allowed to repeat.
- **No background handoff.** yottacode does not move local loops to a background/cloud scheduler on exit. If loops are active, exit shows the loops that will stop and asks whether to exit anyway or stay.

Examples:

| Use case | Command | Notes |
|---|---|---|
| Watch CI until it settles | `/loop 2m check current PR CI and stop when all checks are green` | Runs a normal agent turn every 2 minutes. The agent inspects the PR/checks, explains failures, and — once all checks are green — calls `loop_control{stop}` so the loop disarms itself instead of polling forever. |
| Keep nudging on a task at a safe cadence | `/loop 2m keep fixing the current test failure, run focused tests, and stop when green` | Each iteration starts on the interval only if the prior turn is idle. Stop one loop with `/loop stop <id>` or all loops with `/loop stop all`. |
| Poll status without starting an agent turn | `/loop 30s /context` | Interval slash commands can be informational/status checks and are allowed to repeat. |
| Run a bounded check | `/loop 30s 3x /git-review-pr` | Runs at most three iterations, then disarms automatically. |
| Periodically ask for a lightweight repo health check | `/loop 10m check git status, summarize risky changes, and do not edit files` | Use prose when you want an agent turn. Permission prompts still gate tools; add allow rules only for commands you intentionally want hands-off. |

## Palette behavior

- Choosing a command with no args executes it immediately.
- Choosing a command that needs args fills the command prefix, such as `/model `, and waits for input.
- If you already typed a full command with args, Enter executes what you typed.

## Keyboard shortcuts

The TUI shows a compact contextual `keys · …` hint row above the cmdline after the first message. The row follows the active focus zone: idle input shows send/newline/palette shortcuts, palettes show movement and selection keys, approval/path-trust modals show their decision hotkeys, and active turns show queue/cancel keys.

- `Enter` submits (mid-turn: queue the new message for the next safe tool boundary; while an approval/path-trust modal is focused, non-empty text is queued instead of being swallowed)
- `Ctrl+J` inserts a newline
- `Esc` cancels the current turn (alias for Ctrl+C, mirrors Claude Code); also stops an armed `/loop`
- `Esc Esc` (idle, tapped within 500ms) opens the `/checkpoints` picker
- `Ctrl+C` cancels the current turn; stops an armed `/loop`; quits when neither is active
- `Ctrl+D` exits when input is empty
- `?` opens the cheatsheet when input is empty
- `Shift+Tab` cycles agent modes: normal → plan → auto → yolo → normal
- `PgUp` / `PgDn` scrolls the conversation transcript
- `Ctrl+Home` / `Ctrl+End` jumps to the top / bottom of the transcript

Bracketed paste is normalized before it reaches the input: CR/CRLF line endings become LF, large or multi-line text is collapsed behind a `[Pasted text #N: …]` marker and expanded on submit, and a trailing transport newline is trimmed so paste summaries do not count a phantom blank line.

Use the mouse wheel, `PgUp`/`PgDn`, and `Ctrl+Home`/`Ctrl+End` for transcript movement. Other mouse gestures are intentionally disabled: clicks, hover, drag selection, popup controls, approval decisions, and cmdline focus all stay keyboard/native-terminal owned. When transcript history overflows the frame, a slim right-edge scrollbar shows the current position. Approval and plan modals still leave wheel scrolling available for the transcript so long previews remain readable before you decide. Static scrollable popups such as `/usage` and `/inspect` use keyboard scrolling (↑/↓, PgUp/PgDn, Home/End where shown) rather than mouse controls.

