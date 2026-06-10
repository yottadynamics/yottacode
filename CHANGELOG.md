# Changelog

All notable changes to yottacode will be documented in this file. The
format roughly follows [Keep a Changelog](https://keepachangelog.com/);
the project uses semantic versioning once it's past `1.0.0`.

## Unreleased

### Added

- **Failover chains for the fast/smart routing slots.** Each
  `[router]` task-routing slot can now be a *chain* instead of a single
  model via the plural form `fast_models` / `smart_models = ["<primary>",
  "<fallback>", …]`. On failure or timeout the call falls through primary
  → fallbacks, reusing the same `policy` + health knobs as the
  multi-provider `candidates` router — so a subagent or a summarization
  call survives a smart-model outage instead of failing. This brings the
  resilience the main thread already had to delegated work; under the
  hood a multi-model slot is just a `MultiStreamer`, which drops into the
  existing `adapter.Client` slot transparently (no changes to the agent
  tool, summarizer, or wiring). A slot uses the singular or the plural
  form, not both; the `/router` picker gains a **Smart fallback** row
  (Enter sets a one-model fallback, `d` clears it, longer chains stay in
  `config.toml` and show `(+N more)`). The fast slot has no fallback row —
  it's summarization-only, so instead, the moment the fast model fails a
  compaction the summarizer **degrades to the smart model** for the next
  attempt (a fast-provider outage can't block compaction; the fast model
  is re-probed once it recovers). A fallover
  is surfaced loudly — a warm-yellow `↻ fallback [<where>]: A → B` line in
  scrollback, tagged with the subagent type or `summarize` (main-thread
  fallbacks already rendered this way; the summarizer and subagent paths
  now do too).
- **`/router` command + picker + status-bar indicator for cache-safe
  model routing.** Cache-safe task routing between a `fast_model` and a
  `smart_model` (the `[router]` config block) is now fully controllable
  from the TUI. Bare **`/router` opens a picker** whose Routing toggle and
  model selectors all act **in place** (the picker stays open): enable
  routing first and pick the fast/smart models below — routing turns on
  once both are set — or pick the models and toggle on. Models come from
  your configured providers (embedded catalog + `providers.models`).
  Selections **persist to `config.toml`**
  and apply live — picking a catalog model also adds it to that
  provider's `providers.models` so the write validates, and `config`'s
  renderer now emits the `[router]` `mode`/`fast_model`/`smart_model`
  fields (previously only the fallback-router fields round-tripped).
  **Configuring the smart model also switches your active conversation
  model to it on close** (the smart model is your primary capable model,
  so it stays in sync — same effect as `/model <smart>`); closing without
  changing the smart model leaves the active model untouched.
  `/router on`/`/router off` are quick shortcuts (also persist); `on`
  always means `auto` — summarization runs on the fast model and every
  delegated subagent runs on the smart model (an explicit agent `model:`
  overrides this). Routing never switches the main-thread model mid-conversation,
  so it stays a pure saving with no prompt-cache cost. In auto the status
  bar's primary segment becomes the short-tagged `<smart>:<fast>` pair
  while the active model matches the smart slot (a later `/model` switch
  shows the real active model with a dim `routing: auto (…)` note);
  manual mode shows a dim `routing: manual` note. `/summarize` notices
  show `(on <model>)` when compaction is routed. The fast/smart adapters are built whenever the
  pair is configured (even in `off` mode) so toggling never rebuilds
  them. `manual` mode (route only subagents with explicit `model:`
  frontmatter) remains a config-only setting.
- **New built-in skill: `documentation-and-adrs`.** Captures the *why*
  behind decisions as you ship — ADRs in `docs/decisions/` for choices
  that are expensive to reverse, why-comments for non-obvious code, and
  keeping the rules files (`CLAUDE.md` / `AGENTS.md` / `YOTTACODE.md`)
  current. Brings the built-in set to 17. Also sharpened
  `brainstorming` with a "probe past should-want answers" step and a
  mandatory out-of-scope line in the hand-off restate.
- **`/skills` menu gains a top-level Uninstall row.** Removing an
  installed skill no longer requires knowing the Catalog→Installed-tab
  `u` shortcut: `/skills` → **Uninstall** opens a focused list of
  user-scope skills, and Enter removes the selected one. It reuses the
  same removal + registry-reload + `default_on` scrub as the Catalog
  path, so the two surfaces stay in lockstep. Built-in (embedded) and
  project-scope (committed source) skills aren't listed — neither is
  removable through `skills.Uninstall`.
- **Reasoning effort across providers.** `--reasoning-effort`
  (`low`/`medium`/`high`) now applies to every provider that supports
  reasoning, each via its native knob: OpenAI / ChatGPT-OAuth
  `reasoning.effort`, xAI `reasoning_effort` (grok-`*`-mini only;
  `grok-4` left untouched), Anthropic extended-thinking token budget,
  and Gemini `thinkingConfig.thinkingBudget`. The new `/effort` slash
  command (picker + `/effort <level>` shortcut) changes it mid-session;
  `default`/`off` returns to the provider default. Reasoning stays
  **off by default** — unset injects no reasoning parameter, so existing
  behavior is unchanged and Anthropic/Gemini thinking is strictly
  opt-in. Whether a model can think and how large a thinking budget to
  allow are sourced from the model catalog (no hand-maintained table).
  See [`docs/providers.md`](docs/providers.md#reasoning-effort).
- **Cache-safe model routing.** New `[router]` knobs `mode`
  (`off`/`manual`/`auto`), `fast_model`, and `smart_model` route
  *isolated* work — subagents and history compaction — to a cheap fast
  model while the main conversation stays on the smart model. The
  main-thread model is never switched mid-conversation, so the prompt
  cache stays warm and routing is a pure cost saving (subagents and
  summarization never shared that cache). `auto` mode routes
  summarization to `fast_model` and every delegated subagent (`Explore`,
  `Plan`, `general-purpose`, `verification`, custom) to `smart_model` —
  the capable model, independent of your active model. The fast model is
  reserved for summarization; a subagent reaches it only via an explicit
  `model:` frontmatter (previously parsed but ignored, now honored and
  always wins over the default).
  The routed model is surfaced in the `/subagents` picker and on each
  subagent's completion card. Default `off` — fully backward compatible.
  See [`docs/models.md`](docs/models.md#cache-safe-task-routing).
- **`/context` slash command.** New inspection view showing how the
  context window is being spent: a segmented progress bar painted by
  bucket (system prompt, system tools, MCP tools, memory files,
  skills, messages) plus per-bucket legend and dedicated
  `MCP tools · /mcp`, `Memory files · /memory`, and
  `Skills · /skills · loaded on demand` sections that enumerate
  individual items with token estimates. Renders as a dismissible
  inline overlay below the cmdline (any key closes it) rather than
  in chat history, so the report stays out of scrollback, the
  transcript, `/export`, and resume replay. `PreservesTurn=true` —
  safe to invoke while a turn is streaming. New helpers
  (`EstimateText`, `EstimateToolSchemas`, `SplitMessages`) live in
  `internal/contextwindow` so the same math drives the status-bar
  `ctx` segment and the new view.
- **Image support.** `read_file` now detects image files (png, jpg, gif,
  webp) and returns the image data as a native visual content block on
  providers that support it (Anthropic). On other providers the tool
  returns a text label with file metadata. The adapter layer carries a
  new `SupportsImages` capability flag on the provider profile.
- **Image paste in the TUI.** Pasting an image file path or `file:///`
  URL in the input box is detected and replaced with a compact
  `[Image #N: filename.png]` marker. The image bytes are read eagerly
  and attached to the user message as a native image content block on
  vision-capable providers (Anthropic, OpenAI, Gemini, xAI, Copilot).
  On text-only providers (Ollama, OpenAI-compatible) the marker is sent
  as plain text and the image data is not transmitted, avoiding API
  errors from models without vision support.
- MCP (Model Context Protocol) client support. Configure stdio-based
  MCP servers under `[[mcp_servers]]` in `~/.yottacode/config.toml`;
  each server's tools register as `mcp/<server>/<tool>` in the agent
  registry and flow through the existing approval modal and
  permission rules. Servers default to approval-required; the MCP
  spec's `annotations.readOnlyHint` flips a tool to auto-execute when
  the server explicitly declares it read-only. Permission rules use
  the `MCP(<server>/<tool>)` shape and support glob patterns
  (`MCP(filesystem/*)`, `MCP(*/delete_*)`). A new `/mcp` slash
  command lists configured servers and their status; `/mcp logs
  <name>` dumps recent stderr from a server. v1 covers stdio
  transport and tools only; HTTP/SSE transport, resources, prompts,
  and OAuth2 auth are tracked for follow-up wedges. Uses the official
  `github.com/modelcontextprotocol/go-sdk` for JSON-RPC framing. See
  [docs/mcp.md](docs/mcp.md) for setup, the curated test-server list,
  and troubleshooting.
- `install.sh` one-liner installer (`curl … | bash`) for fresh
  installs and in-place upgrades. Installs to `~/.yottacode/bin/` (no
  `sudo`), verifies the release archive against `SHA256SUMS`, and —
  with confirmation — appends a `PATH` export to the detected shell
  rc file (zsh / bash / fish / sh), creating a timestamped backup
  first. Honors `VERSION=`, `INSTALL_DIR=`, `NO_COLOR=`,
  `--no-modify-rc`, and `--yes`. Output uses indented step headers
  (`▸ Detecting platform (1/5)`) with UTF-8 glyphs for status
  (`✓`/`✗`/`!`/`•`), ANSI colors when stdout is a TTY, and an
  ASCII-only fallback for non-UTF-8 locales — pipes and CI runs see a
  plain log with no escape sequences. `curl`'s native progress bar
  drives the archive download.
- Pre-TUI upgrade prompt. On startup of the root interactive command,
  yottacode does an async GitHub release check (cached 24 h at
  `~/.yottacode/cache/update-check.json`); when a newer release exists
  the user sees a one-line prompt before the TUI starts and can accept
  to run `install.sh` in the foreground. The check skips automatically
  when stdin/stdout is not a terminal, and never runs for subcommands
  like `yottacode run` or `yottacode --version`.
- `YOTTACODE_NO_UPDATE_CHECK=1` env opts out of the daily check.

### Changed

- **Anthropic prompt caching now survives per-turn memory churn.** The
  system prompt is split into a stable head (the static base prompt +
  tools) and a dynamic tail (the per-turn, query-relevant memory
  bodies), with a cache breakpoint on the head. Previously the only
  system breakpoint sat *after* the volatile memory, so the entire
  `tools + system` prefix cache-missed on the first request of every new
  user turn; now the large static head keeps hitting the cache across
  turns while only the small memory tail re-caches. Composer marks the
  boundary via the new `adapter.Message.CacheHeadBytes`; the Anthropic
  adapter honors it, other providers (which cache the longest stable
  prefix automatically) ignore it.
- Recommended install path moved from `/usr/local/bin/` (manual `sudo
  install`) to `~/.yottacode/bin/` via `install.sh`. The README's old
  manual block is preserved under a collapsed "Manual install" section
  for users who want to pin a specific version without the script.
- **`read_file` now speaks lines, not bytes.** `offset` is a 1-indexed
  start line (default 1), `limit` is a maximum number of lines
  (default 2000), and the response is `cat -n` style — every line is
  prefixed with its line number and a tab. Lets the model cite
  `file:line` directly, feed exact text to `edit_file`, and stop
  reaching for `sed -n 'A,Bp' file` via `run_bash` to pull a range.
  The 512 KiB byte cap is preserved as a defense-in-depth limit on
  pathological files. Breaking: any caller passing byte offsets to
  `read_file` must switch to line numbers.
- **Flush-left conversation canvas.** The scrollback canvas now shares a
  single column-0 left edge with the chrome (welcome box, input frame,
  status bar): tool-card gutters (`╭ │ ╰`), the user-echo chevron (`❯`),
  banners, and the status line all sit at column 0, with the text they
  introduce indented two spaces. Previously a global 2-column margin
  pushed scrollback content right of the box borders (and compounded
  with per-style padding into a 4-column prose indent), and the status
  bar carried its own 2-space inset. Card header/body/footer text also
  align at the same column now — the body gutter was `│ ` + an extra
  space, leaving body text one column right of the header.

### Fixed

- **Manual-mode routing no longer sends summarization to the fast
  model.** The docs stated summarization is routed to `fast_model` only
  in `auto` mode, but the wiring routed it whenever routing was *enabled*
  — so `manual` mode (meant to route only subagents with an explicit
  `model:` frontmatter) silently compacted on the fast model too.
  Summarization is now gated behind `auto`, matching the documented
  behavior: `off` and `manual` keep compaction on the active model; only
  `auto` routes it to the fast model.
- **Status bar / input box no longer vanish after closing `/context` or
  the `/skills` menu.** A full-screen overlay renders taller than the
  bare footer, and inline-mode Bubbletea (no alt-screen) doesn't
  re-anchor a shrinking live frame to the terminal bottom — so closing
  one quietly (Esc / any key, with nothing emitted to scrollback) left
  the footer stranded mid-screen until the next redraw. Opening another
  menu or submitting a prompt "fixed" it. Overlays that emit a line on
  close — `/models` / `/providers` selection — were re-anchored for free
  by that `tea.Println`, which is why they never showed the bug. Quiet
  closes now force the same `ClearScreen` + scrollback-replay the resize
  path uses, re-anchoring the frame so the chrome comes straight back;
  closes that already emit a line are left untouched (no double redraw).
- **`/context` now reports each skill's real in-window cost, not its
  on-disk body.** A skill occupies the window through its
  name+description *metadata* line — baked into the system prompt
  (`appendSkillsSection`) and mirrored into the `Skill` tool schema —
  while the body loads on demand only when the skill is invoked. The
  Skills section previously listed each row's full body estimate (the
  on-disk size, ~22 K tokens across the built-in set) tagged
  `(on demand)`, which is not what's loaded. Each skill row now shows the
  loaded metadata cost; the body is excluded. Skills still don't feed the
  usage total or the segmented bar — that metadata is already counted
  under the System prompt and System tools buckets, so the section
  attributes it per skill rather than double-counting. Custom commands,
  which genuinely cost nothing until invoked, keep the `(on demand)` tag.
- **Skill metadata is no longer duplicated into the system prompt.** The
  name+description list was emitted both into the system prompt
  (`appendSkillsSection`) *and* into the `Skill` tool's schema
  description — so every turn carried it twice. The tool schema is the
  load-bearing copy (its description tells the model which names are
  valid to pass), so the system-prompt section now just frames the
  surface and points at that list instead of re-enumerating it. Halves
  the always-loaded skill-metadata cost (the system prompt drops it;
  `Skill` tool schema keeps it) with no change to how the model
  discovers or invokes skills, and makes `/context`'s per-skill figure
  the true single-copy in-window cost.
- **`/context` Skills section is now enablement-aware.** Skills are off by
  default, and a skill's metadata only enters the window once it's enabled
  (it lives in the `Skill` tool schema, which lists active skills). The
  section previously showed a token figure for every loaded skill,
  implying a cost that disabled skills don't actually incur — so an
  installed-but-not-counted skill looked like a discrepancy. Enabled
  skills now show their loaded metadata cost (counted under System tools);
  disabled skills show `off · not loaded`. Toggle with `/skills`.
- **`/context` gives Skills its own usage bucket.** "Estimated usage by
  category" now has a **Skills** row (built-in + user + project, all
  enabled skills), so the cost is visible at a glance instead of hidden
  inside System tools. It's carved *out* of System tools rather than added
  on top — the metadata rides in the `Skill` tool schema, which System
  tools counts, so the two are split (System tools + Skills = the full
  schema cost) and the window total is unchanged. The per-skill Skills
  section below is that bucket's breakdown. Memory files and Messages
  were also recolored (to the palette's Error/red and Content/near-white)
  so every legend + bar bucket reads as a distinct hue instead of Memory
  blurring into Skills and Messages into MCP tools.
- **Auto-summarization no longer silently no-ops on agent-heavy sessions.**
  `composeSummarizedHistory` previously retained every turn when the
  session had five or fewer user prompts, so on plan-mode sessions
  (few prompts, huge tool results per turn) the compressed history
  was as large as — or larger than — the input. Retention is now
  byte-budgeted (40 % of the model's context window, capped at
  `retainTurnsAfterSummary` turns), and any single retained tool
  result above 4 K tokens is truncated in place with a marker. The
  most recent user turn is always preserved.
- **Summarize call now budgets its own input.** When the rendered
  transcript exceeds the room left after the summarization prompt
  and the reserved output, oldest turns are dropped before the
  request is sent. Prevents the summarize call itself from
  overflowing the model window on sessions that grew past it.
- **OpenAI-compatible chat requests now send `max_tokens`.** NVIDIA
  NIM (and any provider that treats the missing field as `0`) was
  rejecting full-transcript requests with `400 Bad Request — you
  requested 0 output tokens` even when the input alone fit the
  window. The adapter now sets `max_tokens=8192` on every request,
  matching the Anthropic adapter default.
- **NVIDIA models now resolve to the correct context window.** Added
  `nvidia/nemotron` (262 144) and a `nvidia/` family fallback
  (128 000) to the `knownWindows` table so the status bar
  denominator and watermark thresholds match what the provider
  actually accepts.
- **Summarize timeout raised from 2 → 5 minutes.** Prefill on a
  200 K+ token transcript routinely takes longer than two minutes on
  slow providers; the old limit surfaced as `context deadline
  exceeded` mid-stream.
- **Scrollback indentation no longer drifts after a terminal resize.**
  On resize the conversation is replayed into scrollback; that replay
  emitted each line via a bare `tea.Println`, bypassing the
  carriage-return/erase-line prefix and width-aware re-wrap that live
  emission applies. Replayed lines therefore landed at a different
  column than freshly-emitted ones and stale-width wraps smeared across
  rows — the "indentation gets shifted at some point" symptom. The
  replay now goes through the same `queuePrintln` path as live output.
- **Startup entry banners no longer wrap at 80 columns or interleave with
  the welcome box.** Mode/permission entry banners (e.g. the
  `--dangerously-skip-permissions` notice) are emitted at construction
  time, before the first `WindowSizeMsg` — so the terminal width was
  still unknown and `queuePrintln` hard-wrapped them at its 80-column
  fallback, and the construction-time flush raced with the welcome box,
  interleaving banner fragments between box rows. Construction-time
  scrollback is now deferred and re-emitted by the startup handler at the
  real width, below the box. This also makes the banner's position
  consistent: it previously rendered above the box on first boot but
  below it after a resize (an above↔below jump that read as "the banner
  moves around").

## 0.2.0 — 2026-05-13

> Control flow + safety triad — typed subagents, per-prompt checkpoints,
> plan & auto modes, and a custom-command starter kit.

### Added

#### Built-in custom-command starter kit

Four commands now ship with the binary as embedded defaults, available
on first launch with no `~/.yottacode/commands/` setup:

- `/git:commit-message` — gathers staged diff + branch context +
  staged CHANGELOG/README/docs prose, composes a one-line subject
  matching the repo's recent commit style, then runs `git commit`
  through an approval modal (the modal is your verification — the
  message is inlined in the heredoc). Prints a `Note:` block when
  unstaged or untracked files exist so you don't accidentally commit
  without them
- `/git:create-pr [base]` — drafts title + body, auto-pushes the
  branch to origin if needed, then runs `gh pr create` through an
  approval modal (the modal is your verification surface — the full
  title + body inlined in the heredoc). Falls back to draft-only
  output when `gh` is unavailable or unauthenticated
- `/check:review [base]` — self-review of the branch diff across
  correctness / scope / tests / style / security / performance
- `/check:verify [task-or-hint]` — detects the stack (Go / Python /
  Java with Maven or Gradle / Rust, plus Makefile as the universal
  fallback), runs build/test/lint with cache discipline (Go uses
  `-count=1` mandatory), cross-checks the diff against an optional
  task description, and prints a structured **Verdict** (Done /
  Not done / Done with caveats / Inconclusive). On failure, diagnoses
  by re-running the failing test in isolation AND checking git log
  for touched test files — never declares failures "pre-existing"
  without that evidence. The argument is mixed-purpose: task
  description, stack hint, command override, or all three in prose

Defaults sit at the lowest precedence tier — a same-name file in
`~/.yottacode/commands/` (user scope) or `<cwd>/.yottacode/commands/`
(project scope) silently overrides the embedded version. The override
path is what customization looks like: copy the default to your user
dir, edit, and it wins on every invocation. The built-in commands
(`/help`, `/clear`, `/model`, `/plan`, etc.) still sit above all three
tiers and cannot be shadowed.

#### Custom slash commands

User-authored slash commands loaded from `~/.yottacode/commands/`
(user scope, applies to every session) and `<cwd>/.yottacode/commands/`
(project scope, committable so a team can share commands via git).
Each `.md` file becomes one slash command; subdirectories namespace
the name (`commands/frontend/component.md` → `/frontend:component`).
Optional YAML frontmatter sets `description` (shown in the palette
and `/help`) and `argument-hint` (changes palette Enter to fill
`/name ` rather than fire immediately, mirroring built-ins like
`/recall`). Bodies support `$ARGUMENTS` and `$1`..`$9` argument
substitution, plus `@<path>` file references via the existing
filerefs pipeline.

Conflict resolution: project commands win over user commands of the
same name; same-scope duplicates and built-in collisions are dropped
with a startup warning. The implementation mirrors Claude Code's
custom-commands surface; `` !`<bash>` `` pre-execution and per-command
`model:` / `allowed-tools:` frontmatter are intentionally out of
scope for this first cut (workaround for shell context: the body can
instruct the agent to call `run_bash` itself).

**Permissions:** custom commands are a prompt shortcut, not a
permission bypass — the substituted body is sent to the agent
immediately, but every mutating tool call the agent makes in response
(`write_file`, `edit_file`, `git_commit`, `run_bash`, …) still flows
through the normal per-tool approval system. Use auto mode or
`.yottacode/permissions.json` allow rules to reduce prompt friction on
commands you trust. See
[`docs/tui-slash-commands.md#custom-commands`](docs/tui-slash-commands.md#custom-commands).

#### Per-prompt checkpoints (`/checkpoints` / `Esc Esc`)

A new user-facing rewind surface that mirrors Claude Code's `/rewind`.
Every user message automatically creates a checkpoint capturing the
conversation history and pre-edit contents of files the agent is about
to touch; an opt-in picker (`/checkpoints` slash command, or `Esc Esc`
double-tap within 500ms) lists past prompts and offers four restore
actions: *Restore code and conversation*, *Restore conversation only*,
*Restore code only*, and *Summarize from here*. The original prompt is
prefilled in the input box after a restore so you can edit and resend.

- **File-snapshot, not git-based** — pre-images are content-addressed
  blobs under `~/.yottacode/checkpoints/<session>/` so checkpoints
  don't pollute the working tree or fight with your git history.
  Repeated edits to the same file across two checkpoints store only
  the two distinct pre-images.
- **Tracked tools**: `write_file`, `edit_file`, `apply_diff`,
  `delete_file`, `move_file`, `copy_file`. Bash mutations and git
  operations are intentionally not tracked, matching Claude Code's
  `/rewind`. Picker footer surfaces this caveat.
- **30-day TTL by default**, configurable via `[checkpoints]
  retention_days = N` in `~/.yottacode/config.toml`. Sweep runs
  opportunistically on session open; orphan blobs are GC'd.
- **Atomic restore** — files are written `.tmp` then renamed, session
  saved second, in-memory state updated last. A crash mid-restore
  leaves the checkpoint intact so you can re-run.
- **Active-turn gating** — the picker is unavailable while a turn is
  running so file restores don't race live tool writes.

New `internal/checkpoint` package with the `Store`,
`Mutator` capability marker on the `Tool` interface, and
`CheckpointWriter` hook on `LoopConfig` so any future capture site
(subagent runs, oneshot) can opt in without further plumbing.

#### Typed subagents (`Agent` tool)

A new `Agent` tool lets the parent model delegate research, code
search, planning, or any multi-file investigation to a typed
subagent that runs in its own context window. The parent only sees
the child's final reply — the child's tool calls and reasoning
stay isolated, which keeps the parent's adapter context lean
across long conversations. Mirrors Claude Code's `Agent` / `Task`
surface.

Highlights:

- **Three built-in agent types** (`general-purpose`, `Explore`,
  `Plan`) embedded in the binary; usable with no setup.
- **Custom agent definitions** under `.yottacode/agents/*.md`
  (project) and `~/.yottacode/agents/*.md` (global) with YAML
  frontmatter declaring tools / description / optional model
  override. Project entries win over global, which win over
  built-ins.
- **`/subagents` slash command** opens an inline picker overlay
  with two views: **tasks** (current session's runs) and
  **types** (loaded agent definitions). `Enter` opens the
  highlighted task's transcript in `$PAGER`; `t` toggles views;
  `s` stops a running task; `r` refreshes; `Esc` closes.
- **`/subagents stop <id-prefix>`** cancels a running task from
  the cmdline.
- **Mode propagation**: a subagent runs under the same mode as
  its parent (plan, auto, yolo all propagate by pointer-shared
  state). A plan-mode parent's child enters plan mode with the
  same plan file; auto-mode parents pass their 4× iteration
  budget to children; yolo is process-wide.
- **Approval forwarding** (foreground only): when a child tool
  call needs approval, it surfaces on the parent's modal with a
  `[subagent:<type>]` badge. The user's verdict routes back to
  the child via the parent's decisions channel.
- **Recursion guard**: child registries never contain the `Agent`
  tool itself, even when a config's `tools:` allowlist names it.
- **Transcripts** persist under
  `~/.yottacode/projects/<slug>/subagents/<agent>-<id>.md` and
  are viewable from the picker.
- **`get_subagent_result` tool** retrieves a previously-dispatched
  task's final reply by id (or unique prefix). Defaults to a 60s
  blocking wait so the parent can spawn a background subagent
  and fetch the result in one tool round-trip; configurable up
  to 600s.

#### Experimental feature flag system (`internal/experimental`)

A small named-flag registry for gating not-yet-stable features
behind opt-in. Three resolution sources — `--experimental <name>`
CLI flag (repeatable), `$YOTTACODE_EXPERIMENTAL` env (comma-
separated), and `[experimental]` config.toml section — merge at
startup with CLI > env > config precedence. Unknown names emit a
startup warning rather than failing so graduated features don't
break old configs. See `docs/experimental.md`.

**`background_subagents`** is the first feature behind the gate.
Off by default; the `run_in_background:true` argument on the
`Agent` tool returns a recoverable error pointing at the enable
instructions. Foreground subagents are always available.

#### Plan and auto modes

`/plan` enters a read-only design mode (`read_file`, `grep`,
`list_dir`, `git` only) that produces a structured plan and stops
before any mutation. The plan acceptance prompt offers `[Y]` to
approve and auto-implement, which exits plan mode and enters auto
mode for the implementation. Auto mode auto-allows `edit_file` /
`write_file` / `apply_diff` while keeping a safety floor that still
prompts for `run_bash`, `git_commit`, `git_checkpoint`, and
`rollback`. `Shift+Tab` cycles between normal / plan / auto modes
directly. Mode state propagates into subagent runs by shared
pointer, so a plan-mode parent's child enters plan mode with the
same plan file. See `docs/tui-slash-commands.md`.

#### `todo_write` tool + inline todo cards

A model-driven todo list. The `todo_write` tool replaces the
working plan with a full snapshot (Claude Code's `TodoWrite`
analogue); the loop emits one `TodoUpdate` event carrying the
snapshot; the TUI renders the new state as a scrollback card. List
persists to `session.Todos` (omitted from session JSON when empty
for back-compat) and restores via `/sessions <id>`. Self-managed by
default — the agent decides when to call it based on the active
prompt, steered by the tool description toward multi-step work.
Pairs naturally with `/plan`: the plan seeds the initial list, the
agent maintains it during execution.

#### Mid-turn interrupts

`Ctrl+C` cancels in-flight turns cleanly: streaming stops, the
in-progress tool call (if any) receives a context cancellation, and
the conversation history is repaired so the next turn doesn't trip
on a dangling `tool_use` without a matching `tool_result`. The new
slash-command flag `PreservesTurn` marks read-only inspection
commands (`/subagents`, `/help`, `/system`, `/permissions`,
`/doctor`, `/recall`) so they don't cancel an active turn when
submitted mid-run.

#### Permission file integrity checks

On load, `permissions.json` and `permissions.local.json` are
validated for shape, duplicate rules, and known rule-type prefixes.
Malformed files surface a startup warning naming the offending entry
rather than silently dropping rules. Rule resolution itself is
unchanged — deny > allow > ask > default still applies — but the
loader is no longer silent on broken input. See
`docs/security-and-allow-lists.md`.

### Changed

- Built-in tool output rendering refactored in the TUI. Tool-result
  cards now compose from a per-tool renderer interface so future
  tools can ship with custom card shapes without touching the main
  TUI loop. Existing tool output is visually unchanged; the seam is
  what's new.

### Fixed

- TUI card for `write_file` no longer mis-renders when the target
  is a new file. Previously fell through to an edit-style diff with
  an empty `before` block; new files now render with a clear
  new-file indicator and full body preview.

### Other

- `DefaultSystemPrompt` updated with context-efficiency rules
  steering the model toward Explore for lookups, Plan for design
  drafts, and the `get_subagent_result` tool for retrieving
  background subagent findings.
- New slash-command flag `PreservesTurn` marks read-only
  inspection commands (`/subagents`, `/help`, `/system`,
  `/permissions`, `/doctor`, `/recall`) so they don't cancel an
  active turn when submitted mid-run.
- The `view` and `list` subcommands of `/subagents` have been
  consolidated into the picker overlay — `/subagents` opens
  tasks view, `/subagents types` opens types view, and `Enter`
  on a row replaces `/subagents view <id>`.

### Known limitations

See `docs/subagents.md#known-limitations` for the full list. In brief:

- Multi-line tool cards can interleave with concurrent multi-line
  output (cosmetic; affects every multi-card turn, not just
  subagents).
- The parent model occasionally picks `general-purpose` for
  read-only lookups when `Explore` would be 10× faster; prompt
  steering is best-effort.
- Background subagents are experimental and gated. Even with the
  gate on, the model's reflexes around bg subagents still need
  iteration — it may spawn one then duplicate the work itself.
  Foreground is the recommended default.
