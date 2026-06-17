# yottacode v0.3.0

**Release Date:** 2026-06-10

> Memory + ecosystem — persistent agent memory with semantic recall, installable skills, an MCP client, typed git/GitHub workflow commands, worktrees, themes, and a one-line installer.

---

## ✨ Highlights

- **Persistent agent memory (`/memory` / `/recall`)** — the agent files and retrieves durable memories across sessions via `memory_save` / `memory_search` / `memory_get` / `memory_forget` / `session_recall`, with per-turn retrieval injected automatically. Three strategies — keyword, BM25, and semantic (local Ollama embeddings, auto-detected) — and one unified tree under `~/.yottacode/memory/` with user and per-project scopes ([#39](https://github.com/yottadynamics/yottacode/pull/39), [#52](https://github.com/yottadynamics/yottacode/pull/52), [#75](https://github.com/yottadynamics/yottacode/pull/75)).
- **Agent skills + installable catalog (`/skills`)** — 17 built-in capability playbooks (TDD, diagnose, security-auditor, performance-profiler, …) the model invokes on demand, costing only a metadata line until used. Install more from a local path, URL, or GitHub `owner/repo` straight from the TUI, with lockfile provenance, drift check, and update ([#30](https://github.com/yottadynamics/yottacode/pull/30), [#46](https://github.com/yottadynamics/yottacode/pull/46)).
- **MCP client (`/mcp`)** — stdio MCP servers configured under `[[mcp_servers]]` register their tools as `mcp/<server>/<tool>`, flowing through the existing approval modal and `MCP(<server>/<tool>)` permission globs; `readOnlyHint` tools auto-execute ([#27](https://github.com/yottadynamics/yottacode/pull/27)).
- **Typed git/GitHub workflow commands** — `/git-commit`, `/git-push`, `/git-create-pr`, `/git-update-pr`, `/git-review-pr`, and `/git-create-issue` replace the markdown starter-kit directives with typed context + apply tool pairs, and all GitHub traffic now goes through a typed `go-github` client — no `gh` CLI required ([#17](https://github.com/yottadynamics/yottacode/pull/17), [#19](https://github.com/yottadynamics/yottacode/pull/19), [#35](https://github.com/yottadynamics/yottacode/pull/35), [#90](https://github.com/yottadynamics/yottacode/pull/90)).
- **Git tool refresh** — flag-aware read-only policy (`git branch --list` auto-runs, `git branch -d` prompts), risk-tiered approval warnings for destructive operations, six new branch-review surfaces, and amend/fixup helpers ([#88](https://github.com/yottadynamics/yottacode/pull/88)).
- **One-line installer + update check** — `curl … | bash` installs to `~/.yottacode/bin` with SHA256 verification and shell-rc PATH setup; a daily pre-TUI check offers in-place upgrades when a new release ships ([#14](https://github.com/yottadynamics/yottacode/pull/14), [#16](https://github.com/yottadynamics/yottacode/pull/16)).
- **Worktrees + experimental dispatch** — `--worktree <name>` gives each session an isolated checkout under `~/.yottacode/worktrees/`, with `enter_worktree` / `exit_worktree` tools mid-session; the experimental `dispatch` flag fans batches of subtasks out to concurrent workers in per-task worktrees and merges their branches back ([#25](https://github.com/yottadynamics/yottacode/pull/25), [#64](https://github.com/yottadynamics/yottacode/pull/64)).
- **GitHub Copilot provider** — `yottacode copilot-auth login` (device-code flow) or `/provider add` inline; the `/model` picker shows your account's models and marks plan-gated ones ([#37](https://github.com/yottadynamics/yottacode/pull/37)).
- **Reasoning effort, cache-safe routing, prompt caching** — `/effort` sets reasoning level per provider's native knob; the `[router]` config routes subagents and summarization to a cheap model without ever touching the main thread's cache; Anthropic prompt caching survives per-turn memory churn via a stable-head breakpoint ([#57](https://github.com/yottadynamics/yottacode/pull/57), [#54](https://github.com/yottadynamics/yottacode/pull/54), [#58](https://github.com/yottadynamics/yottacode/pull/58)).
- **Inspection surfaces: `/context`, `/usage`, `doctor` probes** — segmented context-window breakdown by bucket; per-session token totals with account/rate-limit blocks; active provider health checks distinguishing network, auth, and model-visibility failures ([#47](https://github.com/yottadynamics/yottacode/pull/47), [#56](https://github.com/yottadynamics/yottacode/pull/56), [#59](https://github.com/yottadynamics/yottacode/pull/59)).
- **Context windows from the models.dev catalog** — host-matched, per-provider-kind resolution (the ChatGPT-account Codex backend pins ~272 K where the API allows 1 M+), with passive drift correction persisted to `~/.yottacode/context-windows.json` ([#63](https://github.com/yottadynamics/yottacode/pull/63), [#89](https://github.com/yottadynamics/yottacode/pull/89)).
- **Themes (`/theme`)** — eleven palettes with live two-pane preview, `NO_COLOR` auto-theme, plus intraline diff emphasis and contrast polish across the TUI ([#33](https://github.com/yottadynamics/yottacode/pull/33), [#87](https://github.com/yottadynamics/yottacode/pull/87)).
- **Image support** — `read_file` returns native image blocks and pasted image paths attach as vision content on capable providers; text-only providers get a safe marker instead of an API error ([#43](https://github.com/yottadynamics/yottacode/pull/43)).
- **Folder trust** — first-launch trust prompt with persisted roots, `yottacode trust` management, and inline elevation prompts for out-of-workspace writes ([#20](https://github.com/yottadynamics/yottacode/pull/20), [#86](https://github.com/yottadynamics/yottacode/pull/86)).
- **`web_search` tool** — DuckDuckGo-backed search for providers without hosted search (Ollama, NIM, OpenAI-compatible) ([#41](https://github.com/yottadynamics/yottacode/pull/41)).

---

## 🐛 Bug Fixes & Improvements

- **Malformed tool calls no longer wedge sessions** — empty arguments normalize to `{}`, truncated calls are rejected before reaching tools or history, and replayed history is re-sanitized, so one bad call can't brick every subsequent turn ([#77](https://github.com/yottadynamics/yottacode/pull/77)). String-encoded scalars from open models (`{"max_results":"5"}`) are coerced against the tool schema instead of erroring ([#62](https://github.com/yottadynamics/yottacode/pull/62)).
- **Stream + interrupt hardening** — user interrupts repair orphaned tool calls in history, argument parse failures surface as recoverable errors, and failed parallel tool batches report every error ([#83](https://github.com/yottadynamics/yottacode/pull/83)).
- **Subagents follow provider/model switches** — no more stale-adapter "no token file" failures after switching providers mid-session ([#69](https://github.com/yottadynamics/yottacode/pull/69)).
- **Gemini thinking models: tool calling fixed** — `thoughtSignature` round-trips on replay, with the documented bypass token covering pre-upgrade history ([#90](https://github.com/yottadynamics/yottacode/pull/90)).
- **Auto-summarization actually fires on agent-heavy sessions** — retention is byte-budgeted, the summarize call budgets its own input, and its timeout was raised to 5 minutes ([#31](https://github.com/yottadynamics/yottacode/pull/31)).
- **TUI correctness sweep** — multi-line pastes no longer corrupt the transcript echo, closing `/context` or `/skills` no longer strands the footer mid-screen, scrollback indentation survives terminal resizes, and startup banners no longer wrap at 80 columns or interleave with the welcome box.

---

**Full Changelog**: [v0.2.0...v0.3.0](https://github.com/yottadynamics/yottacode/compare/v0.2.0...v0.3.0)
