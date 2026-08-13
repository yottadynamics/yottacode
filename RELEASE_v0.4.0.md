# yottacode v0.4.0

**Release Date:** 2026-08-12

> Public launch cut — a stronger daily-driver terminal coding agent with broader provider support, advisor/implementer routing, subagents, recall/memory improvements, LSP-assisted source inspection, GitHub workflow helpers, and local document/media tooling foundations.

---

## ✨ Highlights

- **Advisor/implementer model routing** — `/advisor` and routed Plan/Auto mode flows let you split advisory planning from implementation work, with clear fallback behavior and a `consult_advisor` tool for routed sessions.
- **Broader provider coverage** — Google Vertex AI support joins OpenAI, Anthropic, Gemini, xAI, Ollama, ChatGPT OAuth, Copilot, and OpenAI-compatible endpoints.
- **Recurring workflows** — `/loop` and `loop_control` support bounded autonomous iteration for longer but still supervised work.
- **Memory and recall hardening** — automatic semantic session recall, sensitive-project quarantine, memory capture reminders, audit/health/proposal reports, approval-gated curation, and archive maintenance make continuity more reliable.
- **Subagents and dispatch improvements** — background subagents, local code review, token-budget enforcement, batch stop commands, safer worker shutdown, orphaned worktree reclamation, and serialized cleanup make parallel work more dependable.
- **GitHub PR workflow helpers** — typed tools now cover PR check watching, failed-log retrieval, and failed-job reruns without relying on the `gh` CLI.
- **GA LSP code intelligence** — local language-server support now includes diagnostics, symbols, signature help, type definitions, implementations, code-action previews, rename/format previews, selection ranges, document highlights, safer WorkspaceEdit application, and changed-file diagnostic summaries.
- **GA structural ranges** — `syntax_range` provides offline structural edit ranges for Go, TypeScript/JavaScript, Python, and Rust even when no language server is running.
- **Document and media foundations** — experimental `read_document` covers bounded CSV/TSV/JSON/JSONL/XML/HTML extraction; document generation foundations and local ffmpeg/ffprobe marketing-video composition/rendering are documented and available behind the relevant gates.
- **TUI polish** — fullscreen owned transcript rendering, grouped read/tool cards, startup/welcome redesign, slash-command polish, immediate foreground-only mode indicators, yolo terminology, approval modal scrolling, mouse polish, and new themes improve day-to-day use.
- **ACP and sandbox foundations** — the Agent Client Protocol adapter and experimental Podman command sandbox lay groundwork for broader integrations and safer command execution.

---

## 🐛 Bug Fixes & Improvements

- **Approval and write-path hardening** — `apply_diff` path validation was tightened, including handling JSON-escaped patch input that could previously bypass deny-listed write paths.
- **Worktree-aware PR tools** — GitHub PR wrappers now resolve the live branch after `enter_worktree`, avoiding operations targeting the startup checkout.
- **Safer large outputs** — `read_file` can page beyond the first 512 KiB, `run_bash` output is capped, and PR check-log limits are clamped consistently.
- **Session and TUI correctness** — resume, picker gists, archive restore, image-token accounting, queued mid-turn input editing, transcript scratch leakage, scroll behavior, popups, inspect/session views, mouse handling, and help-popup scrolling were fixed or polished.
- **LSP and dispatch CI hardening** — gopls smoke-test workspaces and dispatch cleanup timing were made more stable.

---

## Conservative launch framing

v0.4.0 is a capability release and public launch cut. It does **not** claim the deferred v0.5.0 launch-polish items: Homebrew install, full paid-provider spend estimates, complete launch asset coverage, broad PDF/Office ingestion, or a polished cost CLI.

---

**Full Changelog**: [v0.3.0...v0.4.0](https://github.com/yottadynamics/yottacode/compare/v0.3.0...v0.4.0)
