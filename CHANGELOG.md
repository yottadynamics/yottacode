# Changelog

All notable changes to yottacode will be documented in this file. The
format roughly follows [Keep a Changelog](https://keepachangelog.com/);
the project uses semantic versioning once it's past `1.0.0`.

## Unreleased

### Added

- **PDF table extraction and docx template-fill generation**, via
  `python3`+`pdfplumber`/`python-docx` routed through the same command
  sandbox `pandoc`/`weasyprint` already use. `read_document`'s PDF path
  now returns detected tables as additional `page N table M` sections
  when that dependency is reachable — best-effort, silently absent
  otherwise, on top of the always-available plain-text result.
  `create_document`'s `format=docx` gains a `template`/
  `content.replacements` mode that fills `{{name}}` tokens in an
  existing docx in place instead of generating a new one from
  `content.blocks`. `infra/documents.Containerfile` now stages both
  Python packages (pip, pinned versions); a host-only session with no
  podman sandbox gets the same driver scripts via the yottacode binary's
  own embedded copy, materialized to `~/.yottacode/cache/doc-helpers/`
  on first use — no separate install step beyond `pip install
  pdfplumber python-docx` themselves. See
  [`document-generation.md`](docs/document-generation.md#requirements).

- **PDF OCR fallback**, via `python3`+`pytesseract`+`pdf2image` (backed
  by `tesseract-ocr`), routed through the same command sandbox as the
  PDF table-extraction tier. `read_document`'s PDF path now recovers
  text from a page with no embedded text layer at all as additional
  `page N (ocr)` sections, with a warning noting the text came from OCR
  and may contain recognition errors. Per-page, not just whole-document:
  a partially-scanned PDF (some pages with real text, some without)
  gets OCR only on the pages that actually need it, not the whole
  window — a fully-scanned document still gets one OCR call over the
  whole range, unchanged from the initial version of this feature.
  Best-effort and silently absent — falling back to the existing "may
  be scanned/image-only" warning — when the dependencies aren't
  reachable. A new `ocr_lang` param (Tesseract language code, or
  `+`-joined codes for mixed-language text; also on `search_document`)
  picks the OCR language; defaults to English, which is the only
  language pack the published `documents` image bundles by default —
  see `infra/documents.Containerfile` for the escape hatch to add more.
  `infra/documents.Containerfile` now stages `tesseract-ocr` (apt) and
  `pytesseract`/`pdf2image` (pip, pinned); the host-fallback path reuses
  the same embedded-script materialization `pdfplumber`/`python-docx`
  already use. See
  [`document-generation.md`](docs/document-generation.md#requirements).

- **`search_document(path, query, max_results?, ocr_lang?)`** — a new
  read-only, no-approval agent tool for query-based retrieval over a
  document's extracted content, for when you don't know which
  page/section of a large PDF/docx/xlsx holds what you need. Reuses
  `read_document`'s own extraction pipeline and `internal/memory`'s
  BM25 ranking engine; returns numbered, scored, snippeted results
  ranked by relevance instead of requiring blind `offset` paging. See
  [`tools.md`](docs/tools.md#search_document).

- **`[subagents] max_concurrent_subagents`** makes the session-wide
  subagent concurrency cap (previously a fixed 8) configurable — shared
  by `dispatch` workers and standalone `Agent` spawns alike, since they
  contend for the same provider streams and (with the command sandbox
  enabled) concurrent containers. Defaults to 8. `dispatch`'s own
  per-call task-decomposition limit (also 8) is now a separate, fixed
  constant, so raising the concurrency cap no longer silently raises how
  many subtasks one `dispatch` call may request. See
  [`configuration.md`](docs/configuration.md#subagents).

### Changed

- **`create_document` and `read_document` graduated to GA for every format,
  including docx/pdf generation and PDF extraction.** Both tools are now
  fully default-on — no experimental flag needed. `document_generation`/
  `document_ingestion` become graduated no-op compatibility flags. A missing
  `pandoc`/`weasyprint`/`pdftotext` binary returns an actionable error
  naming exactly where it looked (host `PATH` or `[sandbox].image`), rather
  than the tool being absent or failing silently. See
  [`document-generation.md`](docs/document-generation.md), including its
  new security note on unsandboxed native parsing for PDF/docx's optional
  richer-parsing tier.

### Fixed

- **`dispatch`'s file-partition safety guarantee had real holes.** A worker
  that ended errored (e.g. it left out-of-scope changes uncommitted) could
  still have its branch recommended for `integrate` if it had an earlier
  legitimate commit — the per-task report, the background wake message, and
  the live-dock badge now all check the errored/failed state, not just
  commit presence. The commit-time ownership scan also missed a rename's
  SOURCE path (only the destination was checked, so `git mv` could smuggle
  a sibling task's file in undetected), misparsed a `git status` rename
  line by scanning for literal `" -> "` text instead of the actual status
  code (misfiring on an untracked file whose name happens to contain that
  substring), and missed a same-file-different-case collision on
  case-insensitive filesystems (macOS). A new dispatch call now also checks
  its file claims against every other still-running dispatch task's
  claims, not just its own batch, closing a cross-call collision gap.
- **Dispatch worker lifecycle and leak fixes.** A late panic (e.g. inside
  the async completion callback, which runs after the real result is
  already recorded) could overwrite an already-successfully-completed
  task's result with a fabricated panic message; the panic-recovery path
  now skips entirely once the task is already terminal. A foreground
  worker whose sandbox failed to construct, or that panicked, previously
  left its scrollback card stuck "running" forever — both paths now emit
  the completion event. `get_subagent_result`'s wait no longer leaks a
  registry channel on every timed-out poll, and an adversarial
  `wait_seconds` value can no longer integer-overflow past its own 10-minute
  clamp. Worktree cleanup on a mid-batch failure now uses a
  cancellation-detached context so it can't be cut short by the very
  cancellation that triggered it.

## 0.4.0 — 2026-08-12

> Public launch cut — broad daily-driver improvements across providers, TUI,
> memory/recall, subagents, LSP/code intelligence, GitHub PR workflows,
> document/media tools, and safety hardening. This release intentionally does
> not claim the deferred v0.5.0 launch-polish items: Homebrew, full paid-provider
> cost estimates, complete launch asset set, or broad PDF/Office ingestion.

### Added

- **Advisor/implementer model routing** with `/advisor`, routed Plan/Auto mode
  switches, advisor fallback wording, and the `consult_advisor` tool for routed
  implementer sessions. (#131, #139, #146, #147)
- **Google Vertex AI provider support** for Vertex-hosted Claude and Gemini
  models using Application Default Credentials, curated model lists, provider
  docs, and reasoning-effort plumbing. (#109)
- **Recurring task loops** via `/loop`, the `loop_control` self-stop tool, and a
  higher default max-iteration ceiling for longer agent workflows. (#104, #108,
  #112, #212)
- **Automatic semantic session recall** with sensitive-project quarantine,
  recall picker polish, worktree/repo-root scope fixes, vector cleanup, debug
  logging, and opt-out behavior for `retrieval.session_recall.top_k = 0`. (#103,
  #107, #113, #121)
- **Memory maintenance and capture upgrades** including periodic capture
  reminders, quick-capture defaults, audit/health/proposal reports,
  approval-gated curation, archive maintenance, and harder save/search
  consistency. (#114, #115, #116, #117, #163, #164)
- **Background subagents, local code review, and dispatch improvements** covering
  promoted background workers, local review workflows, batch stop commands,
  token-budget enforcement, atomic concurrency admission, completion wakeups,
  safer worker shutdown, orphaned worktree reclamation, and serialized worktree
  cleanup. (#79, #98, #101, #160, #184, #210)
- **GitHub PR workflow helpers** for PR CI context: checking failed logs, rerunning
  failed jobs, watching PR checks, and typed PR check-waiting. (#178, #179)
- **GA LSP code intelligence** with startup install hints, status/advisory cards,
  diagnostics, document symbols, signature help, type definitions,
  implementations, code-action previews, rename/format previews, selection
  ranges, document highlights, safer WorkspaceEdit application, smoke coverage,
  and changed-file diagnostic summaries. (#126, #127, #128, #133, #135, #142,
  #150, #165, #169, #175, #177, #180, #188)
- **GA `syntax_range` structural ranges** for Go, TypeScript/JavaScript, Python,
  and Rust, plus offline symbol fallback improvements for code-map/LSP surfaces.
  (#157, #181)
- **Experimental Code Map** for repo snapshots, Go import impact/cycles, and
  Mermaid diagrams. (#136)
- **Structured document ingestion** through experimental `read_document` with
  Phase A CSV/TSV/JSON/JSONL/XML/HTML extraction, `offset` paging, `max_bytes`,
  BOM/delimiter/header/JSONL hardening, capped XML/HTML previews, truncation
  warnings, and agent-prompt discoverability. (#183, #185)
- **Experimental document generation workflow** and docs for local xlsx/pptx/docx
  /pdf-oriented document generation surfaces. (#209)
- **Local marketing video tools** for prompt-driven ffmpeg/ffprobe composition,
  polished video templates, and docs. (#132, #170, #171)
- **ACP server adapter** for Agent Client Protocol integration. (#193)
- **Experimental Podman command sandbox** plus sandbox picker flow fixes. (#190,
  #192)
- **Official skills catalog installs** and visible skills progress indicators.
  (#110, #124)
- **TUI session inspect and usage-efficiency tooling** with session picker/export,
  paged session and inspect pickers, usage outlier attribution, cache metric
  fixes, and false-positive cleanup. (#201, #202, #203, #205, #207, #208, #211)
- **New themes and theme polish** including `studio-dark`, `yottacode-dark`,
  README/demo assets, and recording-oriented palettes. (#168, #173, #200)
- **Mode and permission UX updates** including `/yolo`, `/auto`, full mode-cycle
  controls, immediate foreground-only mode indicators, and yolo terminology.
  (#122, #196, #199)

### Changed

- **TUI rendering was substantially redesigned**: fullscreen owned transcript,
  popup compositor, Charm v2 migration, startup/welcome redesign, status-bar
  polish, scroll/picker/menu improvements, grouped read/tool cards, approval
  modal scrolling, mouse selection/hover polish, and more consistent slash
  submenu overlays. (#140, #141, #143, #144, #148, #149, #151, #152, #153, #154,
  #159, #167, #176, #191, #194, #195, #197, #198, #204, #206, #213, #214)
- **README and public positioning were refreshed** with adoption-focused copy,
  traction positioning, and a GIF-based demo path that renders reliably on
  GitHub. (#172, #173, #174)
- **Model catalog and provider resolution improved**: live model catalog fetches
  for OpenAI auth, declared model fallback for non-curated providers, filtering
  non-chat models from pickers, host-qualified model ids, reasoning metadata for
  versioned/provider-qualified ids, and safer provider fallback behavior. (#99,
  #119, #123, #189)
- **Tool output and recoverability UX improved** with compact anchored-edit error
  cards, anchored edit recovery, clearer patch failure notices, argument
  tolerance, and bounded tool-call/log output. (#125, #129, #156, #162, #187)
- **Prompt-cache behavior is now explicit** for mid-session model/provider
  switches, and Anthropic cache TTL is configurable. (#186)
- **Documentation guidance was corrected** where the experimental-feature
  graduation process did not match how the project actually keeps old flags
  recognized after GA. (#185)

### Fixed

- **Security and permission hardening** for approval controls and `apply_diff`
  path validation, including JSON-escaped patch handling that previously could
  bypass deny-listed write paths. (#125, #164)
- **PR tools now resolve the live branch after entering a worktree**, avoiding PR
  operations accidentally targeting the startup checkout. (#134)
- **Session resume, picker gists, archive restore, image-token accounting, queued
  mid-turn input editing, and transcript scratch leakage** were fixed or polished.
  (#118, #121, #137)
- **Large file and log handling is safer**: `read_file` can page beyond the first
  512 KiB, `run_bash` output is capped at 256 KiB, and `pr_check_logs` now clamps
  its documented `max_lines` ceiling. (#185, #187)
- **LSP and dispatch CI flakes were hardened**, including gopls smoke-test temp
  workspaces and a longer dispatch cleanup deadline. (#166, #188)
- **TUI scroll and popup defects** were fixed in transcript, approval, inspect,
  session, mouse, and help-popup flows. (#195, #204, #206, #211, #214)
- **Context-compaction recovery banners now point at a working command.**
  Auto/manual `/summarize` and mid-turn compaction used to suggest a
  `/recall <session-id>` command that could never actually match anything
  — `/recall` is a full-text search over message content, not a session-id
  lookup — so the suggested recovery step reliably failed. The banner now
  prints a copyable `yottacode sessions resume <id>` command that reopens
  the full pre-compression transcript as a fresh session. Retained
  tool-message capping is also budget-aware now: a single turn with many
  large tool results is capped by their *combined* size against the
  retain budget instead of each message independently, fixing sessions
  that could re-trigger auto-summarize repeatedly without ever converging
  under the threshold.
- **Quitting can no longer hang indefinitely.** `/quit` and idle Ctrl+D run
  one final turn so the model can save durable memories before the
  session context is gone — that turn had no deadline of its own, so a
  slow or unresponsive provider could hold the app open with no way out
  short of remembering to press Esc. It's now bounded to 20s; past that
  the turn is canceled exactly as Esc/Ctrl+C would, and the quit
  completes on its own.


## 0.3.0 — 2026-06-10

> Memory + ecosystem — persistent agent memory with semantic recall,
> installable skills, an MCP client, typed git/GitHub workflow commands,
> worktrees, themes, and a one-line installer.

### Added

- **Persistent agent memory (`/memory`, `/recall`).** Five tools —
  `memory_save`, `memory_search`, `memory_get`, `memory_forget`, and
  `session_recall` — let the agent file and retrieve durable memories
  across sessions; relevant memories are retrieved and injected each
  turn. Three retrieval strategies: `keyword`, `bm25` (Porter stemming +
  synonym expansion), and `semantic` (local Ollama embeddings blended
  with BM25); the default `auto` probes for a local embedding model at
  session start and falls back to `bm25`. Retrieval runs in the turn
  goroutine, so a slow embed reads as ordinary model latency and Esc
  cancels it; embed requests send `keep_alive: 30m` so Ollama keeps the
  model resident between turns. `/memory` opens a picker over the
  curated files (USER.md, YOTTACODE.md) and agent-managed memories;
  `/recall <query>` searches past sessions. Tunables live under
  `[retrieval]`: `strategy`, `top_k`, `max_bytes`, `min_score`,
  `embedding_model`, `semantic_weight`. See
  [`docs/memory.md`](docs/memory.md).
- **Agent skills (`/skills`).** Reusable capability playbooks the model
  invokes on demand via the `Skill` tool. 17 built-in skills ship
  embedded in the binary (test-driven-development, diagnose,
  writing-plans, security-auditor, performance-profiler, …); user
  skills live in `~/.yottacode/skills/`, project skills in
  `.yottacode/skills/`, with project > user > built-in shadowing. Only
  a skill's name+description metadata rides in the context window — the
  body loads when invoked. Built-ins respect the `[skills] default_on`
  config block; `/skills` toggles enablement per session.
- **Install skills from the TUI.** `/skills` grew a management menu:
  **Catalog** (Built-in / Installed tabs; Space toggles enablement,
  Enter previews the body in `$PAGER`), **Install** (local path,
  `https://…/SKILL.md` URL, or GitHub `owner/repo` shorthand — repo
  installs walk sub-assets like `scripts/` and `references/`),
  **Check** (drift report against the lockfile), and **Update**
  (re-fetch from the recorded source). Provenance — source, hash,
  install time — is recorded in `~/.yottacode/skills/.lock.json`.
- **Typed git/GitHub workflow commands.** Six procedural slash
  commands — `/git-commit`, `/git-push`, `/git-create-pr`,
  `/git-update-pr`, `/git-review-pr`, `/git-create-issue` — replace the
  markdown starter-kit directives (`/git:commit-message`,
  `/git:create-pr`) with typed composite tools: read-only context
  snapshots (`git_commit_context`, `gh_pr_context`, `gh_issue_context`)
  feed validated, approval-gated apply tools (`git_commit_apply`,
  `gh_pr_create`, `gh_pr_update`, `gh_issue_create`), so base
  resolution, title validation, ahead-count gating, and hook-failure
  detection are deterministic code instead of prose inference.
  `/git-review-pr` renders a structured review (failing checks /
  blockers / suggestions / nits) into scrollback.
- **GitHub integration via typed client — no more `gh` CLI
  dependency.** All GitHub traffic goes through a typed `go-github`
  REST client with token discovery across `$GITHUB_TOKEN`,
  `gh auth token`, and `~/.yottacode/github.json` (written by
  `yottacode setup github`). Read tools return typed snapshots, write
  tools validate before dialing and return structured result envelopes,
  identical in-session reads are served from cache, and permission
  rules use the `Github(<action>)` shape. `doctor` gains a GitHub
  section with rate-limit state and a `--no-github` opt-out for
  token-less CI. See [`docs/github.md`](docs/github.md).
- **Folder trust.** First launch in an unfamiliar directory asks
  whether you trust it; decisions persist to
  `~/.yottacode/trusted-roots.json` and subfolders inherit the answer.
  `yottacode trust list|add|remove|clear` manages roots;
  `--allow-paths`, `$YOTTACODE_ALLOW_PATHS`, and `YOTTACODE_TRUST_ALL=1`
  are session-scoped overrides. Writes outside the workspace pop an
  inline elevation prompt (allow once / trust for session / reject).
  See [`docs/security-and-allow-lists.md`](docs/security-and-allow-lists.md).
- **Worktrees (`--worktree <name>`).** Run parallel sessions against
  the same repo without collisions: each worktree materializes under
  `~/.yottacode/worktrees/<repo-slug>/<name>/` on branch
  `worktree-<name>`. Agent tools `enter_worktree` / `exit_worktree` /
  `worktree_status` manage them mid-session — exit auto-removes clean
  worktrees and prompts on dirty ones — and a `.worktreeinclude` file
  (gitignore syntax) copies gitignored files like `.env` into fresh
  worktrees. Worktree sessions inherit trust from the originating
  repo. See [`docs/worktrees.md`](docs/worktrees.md).
- **Dispatch fan-out (experimental: `dispatch`).** The `dispatch` tool
  fans a batch of up to 8 independent subtasks out to concurrent
  subagent workers — each write task in its own worktree + branch,
  partitioned by file ownership — and `integrate` merges the worker
  branches into one integration branch, stopping on conflicts for
  manual resolution. Background batches run unattended with file
  writes auto-allowed and `run_bash` disabled; catastrophic commands
  are refused unconditionally, even under `--yolo`. Worktrees and
  branches are reclaimed on worker exit and at session teardown.
  Requires the `dispatch` experimental flag (`background_subagents`
  has graduated — see Changed below). See
  [`docs/dispatch.md`](docs/dispatch.md).
- **Background subagents: live dock + completion cards.** A live dock
  above the status bar shows each running subagent's type, latest
  activity, model, and context fill; Tab focuses the dock, Enter opens
  a transcript. Background completions surface a card in scrollback on
  the next render. Foreground subagent batches now fan out
  concurrently as well.
- **GitHub Copilot provider (`copilot`).** `yottacode copilot-auth
  login` runs GitHub's device-code flow and stores tokens under
  `~/.yottacode/auth/`; `/provider add` runs the same flow inline in
  the TUI. The `/model` picker lists the account's available models
  and marks plan-gated ones. `copilot-auth status|models|logout`
  manage the token lifecycle. See
  [`docs/providers.md`](docs/providers.md).
- **`web_search` tool.** DuckDuckGo-backed web search (titles, URLs,
  snippets; `max_results` up to 20) for providers without hosted
  search — Ollama, NVIDIA NIM, and other OpenAI-compatible endpoints.
  Providers with native hosted search keep their own tools.
- **Themes (`/theme`).** Eleven built-in palettes (catppuccin,
  gruvbox, nord, one-dark, solarized-dark, tokyo-night, terminal,
  dimmed, high-contrast, low-contrast, no-color) with a live two-pane
  preview picker; Enter persists to `[theme] name` in `config.toml`,
  `/theme set <name>` scripts it. When `NO_COLOR` is set and no theme
  is configured, the monochrome `no-color` palette auto-activates per
  the no-color.org convention. See [`docs/themes.md`](docs/themes.md).
- **`/usage` command.** Per-session token totals by model, a rolling
  today total, live rate-limit headroom where available, and
  provider-specific account blocks (plan + reset windows for
  subscription auth; billing links for pay-per-use). No dollar
  estimates — providers don't expose stable pricing APIs, and
  hand-maintained price tables drift. Renders as an inline overlay;
  safe mid-turn.
- **Provider health checks in `doctor`.** `yottacode doctor` (and
  `/doctor`) now actively probes the configured provider — endpoint
  reachability, auth validity, model visibility — distinguishing
  network, auth, and model-visibility failures instead of echoing
  static config. Token-store providers (`openai-auth`, `copilot`) are
  probed without spending API quota.
- **Context windows resolved from the models.dev catalog.** The
  embedded model catalog is augmented by a local snapshot of the
  public models.dev registry, so newly released models resolve a
  correct window before the embedded catalog is regenerated. Windows
  resolve per provider *kind* — `openai-auth/gpt-5` can pin a smaller
  limit than the same model id on `api.openai.com` (the Codex backend
  enforces ~272 K where the API allows 1 M+). Live traffic passively
  corrects drift: an overflow-rejected turn shrinks the stored window,
  a provider-reported input above the resolved window raises it, and
  corrections persist to `~/.yottacode/context-windows.json` with
  longest-prefix matching. See [`docs/models.md`](docs/models.md).
- **`enter_plan_mode` tool.** When you ask for a plan in natural
  language, the model can now genuinely enter plan mode instead of
  role-playing it: the call renders a `[Y]/[N]` confirmation card that
  runs the same entry sequence as `/plan`. Never auto-approved — not
  in auto mode, not under `--yolo`, not via Allow rules — because the
  approval handshake is what flips the shared mode state; the same
  guard `exit_plan_mode` already had.
- **Status bar effort + PR chips.** An `effort:` chip appears on
  reasoning-capable models when a reasoning level is set, and a
  `PR #N` chip appears when the current branch has an open pull
  request (best-effort; omitted without GitHub auth). Both drop first
  on narrow terminals.
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
  summarization never shared that cache). `auto` mode routes read-only
  search subagents (`Explore`, `Plan`) and summarization to `fast_model`
  via a deterministic, zero-token tool-set heuristic; agents that can
  mutate or run commands (`general-purpose`, `verification`) route to
  `smart_model`. A subagent's explicit `model:` frontmatter (previously
  parsed but ignored) is now honored and always wins over the heuristic.
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

- **Background subagents graduated to GA (read-only by default).**
  `run_in_background:true` on the `Agent` tool is now generally
  available in the interactive TUI — no experimental flag required.
  Standalone background runs are read-only: a deterministic background
  approval policy denies every approval-requiring tool before parent
  auto/yolo modes can leak in. Write-capable unattended work goes
  through `dispatch`, where worktree isolation and file-scope
  ownership make unattended writes safe. The `/code-review` slash
  command is unblocked (it no longer requires the experimental gate).
  The `background_subagents` feature flag remains recognized as a
  no-op for one release so existing configs don't break.
- **BREAKING: agent memory and subagent transcripts moved under one
  `~/.yottacode/memory/` tree.** User-scope memories now live in
  `memory/user/`, project-scope memories in `memory/projects/<slug>/`,
  and each project's subagent run transcripts nest at
  `memory/projects/<slug>/subagents/`. The legacy locations
  (`~/.yottacode/memory/*.md` flat files and the entire
  `~/.yottacode/projects/` tree) are no longer read or written — there
  is no migration; the new tree is created fresh on first run. The
  whole memory tree now also honors the `$YOTTACODE_HOME` override,
  matching skills, plans, and agent definitions. `memory_save` steers
  scope choice toward `user` (portable learnings) by default and
  appends a scope-check reminder when a portable-typed memory is filed
  project-scope.
- **`git` tool: flag-aware read-only policy + risk-tiered approvals.**
  Read-only subcommands (status, diff, log, show, blame, grep, …)
  auto-run; ambiguous ones (branch, tag, stash, remote) classify by
  their flags, so `git branch --list` auto-runs while `git branch -d`
  prompts, and `--output` on an otherwise read-only command falls back
  to approval because it writes a file. High-risk mutations
  (`reset --hard`, `clean -f`, `push --force`, history rewrites)
  replace the default approval preview with a bolded, risk-specific
  warning so they can't be approved by reflex. Six new read-only
  review surfaces (`git_diff_stat`, `git_diff_staged`,
  `git_diff_unstaged`, `git_commits_between`,
  `git_branch_ahead_behind`, `git_branch_diff`) and two commit helpers
  (`git_commit_amend`, `git_commit_fixup`) round out branch review and
  fixup flows. See [`docs/tools.md`](docs/tools.md).
- **Gemini provider polish.** The model pickers (`/model`,
  `/model list`, `/provider add`) read the embedded Gemini catalog
  plus the models.dev snapshot, so new Gemini models appear without a
  binary update; API errors are summarized to HTTP status, Google
  status, primary message, and retry hint instead of the full JSON
  error envelope.
- **xAI provider polish.** Hosted `web_search` is on by default
  (domain filters via `$YOTTACODE_SEARCH_ALLOWED_DOMAINS`), `x_search`
  covers X posts/users/threads with handle and date filters, and
  `code_interpreter` is available opt-in; `grok-*-mini` models accept
  `reasoning_effort`. Model catalog refreshed.
- **TUI contrast + polish.** Palette role mapping and width alignment
  tightened across cards and chrome; `edit_file` approval diffs
  highlight the changed span within a line (reverse video) for
  same-shape replacements; the input prompt and user-echo chevrons
  render in brand green; auto-approval lines carry the status-OK
  prefix.
- **User slash commands now honor `$YOTTACODE_HOME`.** The global
  custom-command dir resolves through the same root rule as skills,
  plans, agents, and the memory tree, so `commands/` follows the
  override when it is set (previously always `~/.yottacode/commands`).
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

- **Mid-turn failures no longer corrupt the conversation.** User
  interrupts (Esc, Ctrl+C) inject synthetic `tool_result` entries for
  orphaned tool calls so replayed history stays valid; tool-argument
  parse failures surface to the model as recoverable errors instead of
  killing the turn; failed parallel tool batches drain every error
  rather than reporting only the first.
- **Subagents now follow provider/model switches.** The `Agent` tool's
  adapter went stale after `/provider`, `/model`, or effort changes —
  a subagent spawned after switching away from `openai-auth` failed
  with "no token file". The adapter is now synced on every switch.
- **Gemini thinking models: tool calling fixed.** Gemini 3-era models
  attach a `thoughtSignature` to each function call and require it
  back on history replay; the adapter now round-trips it, substituting
  Google's documented bypass token for history recorded before the
  upgrade so existing sessions keep working.
- **ChatGPT-account sessions no longer overrun the Codex input
  limit.** The `openai-auth` provider resolved context windows from
  the api.openai.com catalog numbers, so the usage bar and
  auto-summarize thresholds sat ~4× too high and long sessions hit
  hard 400s instead of summarizing. Per-provider-kind window
  resolution (see the models.dev entry above) pins the Codex backend's
  real limits, and a window shrink re-runs the context check in the
  same turn, so an over-window session summarizes instead of failing
  again.
- **A malformed tool call from an OpenAI-compatible provider no longer
  wedges the whole session.** Some models (and truncated streams) emit a
  tool call with empty or incomplete JSON arguments. The empty case ran
  the tool against `""` — `write_file: invalid args: unexpected end of
  JSON input` — and, worse, the malformed call was recorded into history
  and replayed verbatim on every later request, so strict providers like
  NVIDIA NIM rejected *every* subsequent turn with a 400 (`Expecting ','
  delimiter`), surviving "continue", a new prompt, and even a model
  switch. yottacode now normalizes an empty arguments payload to `{}` at
  the dispatch layer, so no-argument tools like `exit_plan_mode` /
  `git_push` run on every provider instead of erroring; the Chat
  Completions adapter additionally rejects a genuinely truncated or
  unparseable call — including one cut off with `finish_reason:
  length`/`content_filter` — before it reaches a tool or enters history.
  As a backstop, replayed history is re-sanitized when each request is
  built, so any malformed call recorded earlier (or produced by another
  adapter) is sent as `{}` instead of bricking the conversation.
- **Multi-line pastes no longer corrupt the transcript echo.** Terminals
  transmit bracketed-paste line breaks as carriage returns (CR), not
  newlines — so a pasted list arrived `\r`-separated, slipped past the
  `\n`-based multi-line checks, and went into the input buffer with raw
  CRs. On submit, each CR returned the cursor to column 0 while the
  transcript echoed the message, overprinting every pasted line onto the
  previous one — a 31-package list rendered as the chimera
  `skillsegopslwmponent`. Paste content is now normalized (CR / CRLF →
  LF) at the key-event boundary before any routing, so multi-line pastes
  take the `[Pasted text #N: …]` marker detour and round-trip with clean
  line breaks.
- **Tool calls from open models that string-encode arguments now work.**
  Some OpenAI-compatible models — notably Meta Llama 3.1/3.3 instruct on
  NVIDIA NIM, Ollama, vLLM — emit numeric and boolean tool arguments as
  JSON strings (`{"max_results":"5"}`), which Go's strict decoder rejected
  with `cannot unmarshal string into Go struct field … of type int`,
  causing the model to loop retrying the same failing call. yottacode now
  normalizes string-encoded scalars against each tool's schema at the
  dispatch layer, before the tool runs — covering every built-in and MCP
  tool. The coercion is conservative (only string→int/number/bool, only
  when it parses cleanly) and fail-open (unparseable args, no schema, and
  already-typed values pass through unchanged), so providers that were
  already correct see no behavior change. See
  `yottacode-roadmap/tool-arg-coercion.md`.
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
