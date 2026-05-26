# Memory

yottacode keeps three kinds of memory, each with one job:

- **Trust anchors** — the curated files (`USER.md`, `YOTTACODE.md`) injected verbatim into every turn's system prompt.
- **Agent-managed typed memories** — markdown files the agent writes via dedicated tools (`memory_save`, `memory_forget`) when something is worth remembering across sessions.
- **Recall + summarization** — full-text search across past sessions plus on-demand compression of long histories.

The system is offline-first, deterministic, and entirely file-based. Every memory is a markdown file you can read, edit, or delete with your editor. The TUI's `/memory` picker is a convenience for the same on-disk state.

---

## How the four memory sources flow into the prompt

The agent reads from four distinct on-disk locations every turn. Two are unfiltered "trust anchors"; the other two are agent-managed and pass per-entry bodies through a relevance filter (their indexes still inject in full).

```
                       ON DISK — four memory sources
       ┌────────────────────────────────────────────────────────────────┐
       │                                                                │
       │   TRUST ANCHORS  (always injected verbatim, never filtered)    │
       │   ──────────────────────────────────────────────────────────   │
       │                                                                │
       │   ① ~/.yottacode/USER.md             cross-project, human-only │
       │   ② <repo>/.yottacode/YOTTACODE.md   per-repo, agent-writable  │
       │                                       through approval modal   │
       │                                                                │
       │   AGENT-MANAGED  (index in full · per-entry bodies filtered)   │
       │   ──────────────────────────────────────────────────────────   │
       │                                                                │
       │   ③ ~/.yottacode/memory/                  user-scope           │
       │      ├── MEMORY.md    auto-generated table of contents         │
       │      └── <name>.md    typed memories (one file each)           │
       │                                                                │
       │   ④ ~/.yottacode/projects/<slug>/memory/  project-scope        │
       │      ├── MEMORY.md    auto-generated table of contents         │
       │      └── <name>.md    typed memories (one file each)           │
       │                                                                │
       │   ③ + ④ are written by memory_save, deleted by memory_forget   │
       │                                                                │
       └────────────────────────────────────────────────────────────────┘
                                  │
                                  │  memory.Load(cwd)   — read all four
                                  ▼
       ┌────────────────────────────────────────────────────────────────┐
       │                  Loaded struct (in-memory)                     │
       │                                                                │
       │     UserText                ProjectText                        │
       │     UserMemoryIndex         ProjectMemoryIndex                 │
       │     UserMemories[]          ProjectMemories[]                  │
       └────────────────────────────────────────────────────────────────┘
                                  │
                                  │  SystemPromptFor(base, loaded, turnInput, cfg)
                                  │     ─ trust anchors pass through unchanged
                                  │     ─ MEMORY.md indexes pass through unchanged
                                  │     ─ memory bodies are scored against turnInput
                                  │       and capped at cfg.top_k (shared budget
                                  │       across user + project scopes)
                                  ▼
       ┌────────────────────────────────────────────────────────────────┐
       │            Composed system prompt (rebuilt per turn)           │
       │                                                                │
       │   <base agent-identity prompt>                                 │
       │   ─── opens BACKGROUND REFERENCE block ───                     │
       │   ① ## User preferences         ← USER.md       (full)         │
       │   ② ## Project context          ← YOTTACODE.md  (full)         │
       │   ③ ## User memory index        ← MEMORY.md     (full)         │
       │      ### <name> [type]          ← top-K bodies  (filtered)     │
       │   ④ ## Project memory index     ← MEMORY.md     (full)         │
       │      ### <name> [type]          ← top-K bodies  (filtered)     │
       │   ─── closes BACKGROUND block, action directive ───            │
       └────────────────────────────────────────────────────────────────┘
                                  │
                                  ▼
                          sent to the model
```

The rebuild runs at the start of every turn (`internal/tui/cmd_retrieval.go`), so a `memory_save` mid-conversation lands in the next turn's prompt without an explicit reload. Disk errors leave the previous prompt in place — they don't fail the turn.

---

## Layer 1 — Trust anchors

The two trust anchors are the load-bearing context the agent sees on every single turn. They render in full and are never filtered.

| File | Location | Authorship | Scope |
|---|---|---|---|
| `USER.md` | `~/.yottacode/USER.md` | Human-only (in the agent's write-deny list) | Cross-project — applies to every session |
| `YOTTACODE.md` | `<repo>/.yottacode/YOTTACODE.md` | Human-seeded; the agent keeps it fresh through approval-gated writes | Per-repo — only this project |

`USER.md` holds preferences that travel with you ("prefer table-driven Go tests", "no trailing summaries"). Edit it through `/memory` (which opens vim) or directly — the model never writes there.

`YOTTACODE.md` is the project's brief. `/init` drafts it from the current repo (build commands, layout, conventions, gotchas). After non-trivial work the agent will offer to refresh it through the approval modal. Keep it under ~150 lines; bigger files get a "large memory will impact performance" notice on startup.

To make `YOTTACODE.md` human-only on a specific repo, add a deny rule to `.yottacode/permissions.json`:

```json
{ "permissions": { "deny": ["Edit(.yottacode/YOTTACODE.md)", "Write(.yottacode/YOTTACODE.md)"] } }
```

---

## Layer 2 — Agent-managed typed memories

The agent owns this layer end-to-end. It decides in-conversation what is worth remembering, and it forgets when something becomes wrong or stale.

### Layout

```
~/.yottacode/
  memory/                                     # user-scope (cross-project)
    MEMORY.md                                 # auto-generated index
    <name>.md                                 # one file per memory
  projects/
    <project_slug>/
      memory/                                 # project-scope (this repo only, private to you)
        MEMORY.md
        <name>.md
```

`<project_slug>` is derived from the git remote (`https://github.com/user/repo.git` → `github-com-user-repo`); falls back to `filepath.Base(cwd)` for non-git directories. Two distinct repos can collide on basename; initializing git gets you a unique remote-derived slug.

Per-project memories live in your home directory, not in the repo. They're private to this user/machine — a clone of the same repo on a different machine starts with an empty project memory. Use `YOTTACODE.md` for things the team should share; use project-scope memory for what *you* personally want to remember about working in this repo.

### File shape

Every memory file has YAML frontmatter plus a markdown body:

```
---
name: jwt-refresh-flow
type: project
description: How auth refresh interacts with the token cache
created: 2026-05-08T12:34:56Z
---
The refresh handler in pkg/auth/refresh.go writes the new token to
the cache *before* it returns. Tests that mock the cache must seed it
ahead of the call or the refresh path 401s on the next request.
```

`MEMORY.md` is auto-generated — a table-of-contents grouped by type, regenerated every time `memory_save` or `memory_forget` runs. Don't edit it; edit individual `<name>.md` files instead.

### The four types

The agent picks one of these when saving:

- **`user`** — preferences, style, tooling. ("Prefer two-space indents." "Don't summarize after every change.")
- **`feedback`** — corrections the user gave you. ("Don't generate stack traces in final answers — cut to the fix.")
- **`project`** — load-bearing facts about this repo. ("The schema migration runner reads `migrations/sql/*.up.sql`, not `*.sql`.")
- **`reference`** — material to look back at. (API shapes, command incantations, "what does `make ship` actually do".)

### What the agent saves

Save when:

- The user states a durable preference, correction, or project fact.
- The user supplies a reference you'd otherwise re-derive every turn.
- The same correction has come up twice — that's a pattern worth pinning.

Don't save:

- Code patterns derivable from a quick grep.
- Ephemeral state ("we're mid-refactor of the user model").
- Git-derivable info (current branch, last commit message).
- One-off task instructions.
- Anything sensitive (API keys, internal URLs, PII).

The full guidance lives in the agent's system prompt; see `internal/agent/prompt.go` for the current copy.

### The two tools

The agent has two memory tools, both **silent by default** (no approval modal — they're as ordinary as `read_file`):

- **`memory_save`** — writes a new memory file or overwrites an existing one with the same name. Updates `MEMORY.md`.
- **`memory_forget`** — deletes a memory file by name. Updates `MEMORY.md`. Errors when the named memory doesn't exist (so the agent learns the right names).

To require approval per save / forget, add an `ask` rule:

```json
{ "permissions": { "ask": ["Memory(*)"] } }
```

To deny entirely:

```json
{ "permissions": { "deny": ["Memory(*)"] } }
```

Or block only forgets while leaving saves silent:

```json
{ "permissions": { "deny": ["Memory(forget *)"] } }
```

### Per-turn retrieval

Memory grows over time. By the time you have dozens of memories, dumping every body into every prompt is wasteful. The retrieval orchestrator scores each memory body against the current user prompt and injects only the top-K.

What's filtered:

- **Per-entry bodies** under both scopes — scored, ranked, capped at `retrieval.top_k`.

What is NOT filtered:

- `USER.md`, `YOTTACODE.md` — always in full.
- Both `MEMORY.md` indexes — always in full. The model needs to know which files exist even when their bodies aren't injected.

#### Retrieval strategies

yottacode supports three scoring strategies, selectable via config:

| Strategy | How it scores | When to use |
|---|---|---|
| `keyword` | Exact token overlap, name/type/description weighted 3x over body | Legacy fallback; fast, fully transparent |
| `bm25` | Porter stemming + synonym expansion + Okapi BM25 ranking (IDF weighting, term saturation, length normalization) | Default when no embedding model is available. Handles "fakes" → "mocks", "running" → "run", "db" → "database" |
| `semantic` | BM25 score (60%) + cosine similarity from local Ollama embeddings (40%) | When you want conceptual matching — "error handling philosophy" finds memories about soft failures even without shared keywords |
| `auto` **(default)** | Probes for a local Ollama embedding model at session start. If found → `semantic`; otherwise → `bm25` | Recommended. Zero config, best available scoring |

**BM25** is the baseline — pure Go, zero dependencies, deterministic. It ships a Porter stemmer and ~15 hand-curated synonym groups for programming/dev vocabulary (test/mock/fake, database/db/sql, deploy/release/ship, auth/login/credential, etc.). This alone is a major upgrade over raw keyword matching.

**Semantic** layers local embeddings on top when a local Ollama server is available with an embedding model installed. Vector sidecars (`.vec` files) are stored alongside memory `.md` files and generated automatically on `memory_save`. The combined score blends BM25 (which excels at exact matches like file paths and function names) with cosine similarity (which captures conceptual relationships).

#### Enabling semantic retrieval

To get the full advantage of semantic memory retrieval:

1. Install [Ollama](https://ollama.com) if you haven't already
2. Pull a small embedding model (runs on CPU, no GPU needed):
   ```
   ollama pull nomic-embed-text
   ```
3. Restart yottacode — semantic retrieval activates automatically

The embedding model is small (~270MB) and fast. It runs locally — no data leaves your machine. Once installed, every `memory_save` generates a vector sidecar alongside the memory file. To generate vectors for existing memories, use `/memory` → **Reindex embeddings** or:

```
yottacode memory reindex
```

If you prefer an even smaller model (~45MB), `all-minilm` works too:

```
ollama pull all-minilm
```

Then set it in your config:

```toml
[retrieval]
embedding_model = "all-minilm"
```

#### Config tunables

```toml
[retrieval]
enabled         = true              # off → load every entry every turn (no filter)
top_k           = 10                # cap on memory bodies per turn (shared across user + project)
min_score       = 0.0               # drop entries scoring below this (0.0–1.0)
strategy        = "auto"            # "keyword" | "bm25" | "semantic" | "auto"
embedding_model = "nomic-embed-text" # Ollama model for semantic retrieval
```

### `/memory` picker

The TUI's `/memory` command opens a five-row picker:

| Row | Action |
|---|---|
| Project context | Edits `<repo>/.yottacode/YOTTACODE.md` in vim |
| User preferences | Edits `~/.yottacode/USER.md` in vim |
| Browse user memories | Sub-list of `~/.yottacode/memory/*.md` |
| Browse project memories | Sub-list of `~/.yottacode/projects/<slug>/memory/*.md` |
| Reindex embeddings | Generates `.vec` sidecars for semantic retrieval (requires Ollama) |

In the browse sub-lists: `Enter` opens the chosen memory in vim, `d` deletes it (and regenerates `MEMORY.md`), `f` opens the folder in your file manager, `Esc` returns to the root menu.

### Cobra subcommands (for scripts)

The same actions are exposed as non-interactive subcommands so CI or one-off shells can list, delete, and reindex memories without launching the TUI:

```
yottacode memory list [--scope user|project]   # default: project
yottacode memory forget --scope <s> <name>
yottacode memory reindex                       # generate .vec sidecars for all memories
```

---

## Layer 3 — Recall + summarization

These two predate the memory redesign and are unchanged.

`/recall <query>` searches every saved session in `~/.yottacode/sessions/` via an SQLite FTS5 index at `~/.yottacode/index.sqlite`. Useful for "I remember we discussed X — which session was that in?" The index is rebuilt incrementally on every session save and backfilled at TUI startup.

`/summarize` compresses the active session's transcript when context is filling up. Replaces the message history with a synopsis injected into the system prompt under `## Prior session context (summarized)`. Auto-summarization fires automatically before the next turn at `context.auto_threshold` (default 0.85 — 85% of the model's window).

---

## Decision tree: where does this go?

| Scenario | Where it lives |
|---|---|
| "I prefer table-driven tests across every project" | `USER.md` (you write) |
| "Build / test / lint commands for this repo" | `YOTTACODE.md` (`/init` drafts; agent keeps fresh) |
| "User said don't show stack traces" | `memory_save scope=user, type=feedback` (agent writes) |
| "JWT cache lives in pkg/auth/cache.go for this repo" | `memory_save scope=project, type=project` |
| "API has these public endpoints (this repo)" | `memory_save scope=project, type=reference` |
| "We're mid-refactor of the user model" | Don't save — ephemeral |
| "Look up which session we discussed X in" | `/recall <query>` |
| "Compress the current transcript" | `/summarize` |

---

## Migration from older yottacode

`PROJECT.md` was renamed to `YOTTACODE.md` in this release. If your repo has `<repo>/.yottacode/PROJECT.md`, rename it by hand:

```
mv .yottacode/PROJECT.md .yottacode/YOTTACODE.md
```

The old auto-memory directory at `~/.yottacode/auto/<slug>/` is no longer read or written. The contents are not migrated automatically — paste anything you want preserved into a `memory_save` call in a fresh session.

The `[auto_memory]` section in `~/.yottacode/config.toml` is silently ignored on load, so an upgrade doesn't break first launch. You can delete the block from your config; nothing references its values anymore.

The `--auto-memory` flag and `YOTTACODE_AUTO_MEMORY` environment variable are gone. `yottacode setup` no longer prompts for auto-memory; the agent decides per-conversation what to remember.

---

## Trust model

- **Memory tools run silently by default.** Add `ask: ["Memory(*)"]` to your permissions if you want a modal on every memory write.
- **Don't put secrets in any memory file.** They get loaded into the system prompt every turn and persist on disk in plaintext.
- **Project-scope memory is per-user.** Two developers on the same repo see different `~/.yottacode/projects/<slug>/memory/` dirs. Use `YOTTACODE.md` (in the repo) for things the team should share.
- **The curated layer never gets filtered.** Whatever you write in `USER.md` and `YOTTACODE.md` lands in every system prompt — keep them concise. The "Large file will impact performance" notice fires past 40k chars.
