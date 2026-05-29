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
    <name>.vec                                # embedding sidecar (semantic mode)
    .archive/                                 # prior versions kept on overwrite (see below)
      <name>.<stamp>.md
  projects/
    <project_slug>/
      memory/                                 # project-scope (this repo only, private to you)
        MEMORY.md
        <name>.md
        .archive/
```

The `.archive/` subdirectory holds the prior version of any memory that
`memory_save` overwrote, so an update can never silently destroy a
different memory that reused the name. It's a dotted subdir, so the
scanner skips it — archived versions never appear in the index,
retrieval, or `memory list`. `memory_forget` does not prune `.archive/`
yet; remove it by hand (`rm -rf ~/.yottacode/memory/.archive`) if it grows.

`<project_slug>` is derived from the git remote (`https://github.com/user/repo.git` → `github-com-user-repo`); falls back to `filepath.Base(cwd)` for non-git directories. Slugs are not guaranteed collision-free: two non-git repos can collide on basename, and the remote-derived slug collapses the org/repo boundary (`.` and `/` both become `-`), so distinct remote URLs can also map to the same slug and share a project-memory directory. A collision means one repo's project memories load/edit in the other.

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

### Types — four conventions, free-form underneath

`type` is a short label the agent attaches when saving. Four labels are
**conventional** (and group together, in this order, in `MEMORY.md`):

- **`user`** — preferences, style, tooling. ("Prefer two-space indents." "Don't summarize after every change.")
- **`feedback`** — corrections the user gave you. ("Don't generate stack traces in final answers — cut to the fix.")
- **`project`** — load-bearing facts about this repo. ("The schema migration runner reads `migrations/sql/*.up.sql`, not `*.sql`.")
- **`reference`** — material to look back at. (API shapes, command incantations, "what does `make ship` actually do".)

But the set is **not closed**: when none of the four fit, the agent may coin
its own short label — e.g. `decision`, `gotcha`, `api-shape`. A custom type
is validated only as a label (lowercased + trimmed; lowercase letters,
digits, spaces, hyphens or underscores; ≤32 chars) and renders as its own
`## <type>` group in the index, after the four conventional ones
(alphabetically). The `type` only labels and groups — it never restricts
what the body can hold and is not a retrieval filter. The body content is
unconstrained regardless of type.

### What the agent saves

The agent is designed to be **self-learning** — it actively builds its understanding of you and your work across sessions and projects, so every future conversation starts smarter than the last.

Save when:

- The user states a durable preference, correction, or project fact.
- The user **confirms or validates** a non-obvious approach — save what worked and why.
- The user supplies a reference you'd otherwise re-derive every turn.
- The agent observes a **recurring pattern**: the user always approves a certain style, always rejects a certain approach, always asks for the same thing. The agent doesn't wait for "remember this" — if it sees a pattern twice, it saves it.
- A task outcome teaches something: an approach that failed and why, a subtle constraint discovered, a debugging technique that cracked a hard problem.

Don't save:

- Code patterns derivable from a quick grep.
- Ephemeral state ("we're mid-refactor of the user model").
- Git-derivable info (current branch, last commit message).
- One-off task instructions.
- Anything sensitive (API keys, internal URLs, PII).

### Scope selection — cross-project learning

Scope selection is critical for building knowledge that transfers across projects:

- **`scope=user`** (stored in `~/.yottacode/memory/`, loaded in **every** project): anything about the person, not the repo. Coding style, communication preferences, tool preferences, workflow patterns, feedback corrections, debugging approaches, domain expertise areas. The test: "would this help me in a completely different repo for this user?" If yes, it's user-scope.
- **`scope=project`** (stored per-repo, loaded only in that repo): **only** for facts that are meaningless outside this specific codebase — architecture decisions, naming conventions unique to this repo, team-specific processes, deployment targets.
- **Default to user-scope.** Most things the agent learns about how someone works, thinks, and prefers are portable. Project-scope is the exception, not the default.
- When saving a project-scope memory, the agent considers: is the underlying principle user-scope? E.g., "user wants table-driven tests in this Go repo" is really "user prefers table-driven tests" (user-scope) — the Go repo is just where it was learned.

The full guidance lives in the agent's system prompt; see `internal/agent/prompt.go` for the current copy.

**Precedence — project shadows user.** If the same memory name exists in both scopes, the project-scope version wins *in that repo*: its body injects and the user-scope twin's body is suppressed (it would otherwise duplicate or contradict). This matches how slash commands and config layering resolve project-over-user. The user file stays on disk and still applies in every other repo, where no project twin shadows it.

### The five tools

The agent has five memory tools, all **silent by default** (no approval modal — they're as ordinary as `read_file`):

- **`memory_save`** — creates a memory file, or updates an existing one of the same name. On a same-name update the prior version is **archived** to `<memdir>/.archive/<name>.<stamp>.md` (recoverable, never silently lost; excluded from the index, retrieval, and `memory list`) and the original `created` timestamp is preserved. The result reports `created` vs `updated` (and whether a version was archived). Updates `MEMORY.md`. Generates a `.vec` sidecar when an embedding model is available; if embedding is unavailable, the save still succeeds and the result notes that the semantic index wasn't updated.
- **`memory_forget`** — deletes a memory file by name. Updates `MEMORY.md`. Errors when the named memory doesn't exist (so the agent learns the right names).
- **`memory_search`** — searches across user and/or project memory stores, returning ranked results with relevance scores (zero-relevance entries are omitted). The agent uses this to check for duplicates before saving, find related memories when reasoning about a topic, or verify a remembered fact. Accepts `scope` (`all`, `user`, `project`) and `limit` parameters.
- **`memory_get`** — returns the full, untruncated contents (frontmatter + body) of one memory by `scope` + `name`. Used before updating a memory so the agent can preserve the parts it isn't changing, instead of blindly overwriting from the 300-char `memory_search` preview.
- **`session_recall`** — searches across all past sessions via the FTS5 full-text index. Returns ranked snippets with session metadata (name, date, model). The agent uses this to find prior discussions, check if an issue was already resolved, or pull in context from earlier conversations. Supports FTS5 query syntax (OR, exact phrases in quotes).

The introspection tools (`memory_search`, `memory_get`, `session_recall`) are the key to self-learning — they let the agent think based on its own accumulated knowledge rather than relying only on what the retrieval orchestrator injects each turn.

All five tools resolve to the `Memory` permission namespace (save / forget /
search / get / recall), so a single rule gates every memory operation — every
read included, `session_recall`'s cross-session search among them.

To require approval per memory operation, add an `ask` rule:

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

### Durability & concurrency

Every memory write (a `<name>.md`, the regenerated `MEMORY.md`, or a `.vec`
sidecar) goes through one atomic-write path: stage to a **unique** temp file
in the same directory, `fsync`, then `rename` onto the destination and
`fsync` the directory. The unique temp name means two writers — two
yottacode processes in the same repo, or a parent loop and a background
subagent — can't interleave bytes into a shared staging file or delete each
other's in-flight temp; concurrent writes become last-writer-wins on a valid
file. The `fsync` closes the crash window that a bare `rename` leaves (a file
coming back zero-length or stale after power loss). Reads are best-effort and
never block a turn.

### Per-turn retrieval

Memory grows over time. By the time you have dozens of memories, dumping every body into every prompt is wasteful. The retrieval orchestrator scores each memory body against the current user prompt and injects only the top-K.

What's filtered:

- **Per-entry bodies** under both scopes — scored, ranked, capped at `retrieval.top_k` and `retrieval.max_bytes`.

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

**BM25** is the baseline — pure Go, zero dependencies, deterministic. It ships a Porter stemmer and ~15 hand-curated synonym groups for programming/dev vocabulary (test/mock/fake, database/db/sql, deploy/release/ship, auth/login/credential, etc.). This alone is a major upgrade over raw keyword matching. Synonym-derived query terms are scored at a fractional weight (half of an exact term) so a memory that incidentally touches several distinct synonyms of a group can't outrank one that uses the exact term you searched for — recall stays up, exact-match precision wins ties. (The CLI / TUI `/memory search` preview uses equal weights; the agent's retrieval applies the down-weight.)

**Semantic** layers local embeddings on top when a local Ollama server is available with an embedding model installed. Vector sidecars (`.vec` files) are stored alongside memory `.md` files and generated automatically on `memory_save`. The combined score blends BM25 (which excels at exact matches like file paths and function names) with cosine similarity (which captures conceptual relationships). A sidecar produced by a *different* embedding model than the one in use is skipped for the cosine term (cross-model vectors aren't comparable) — that entry simply ranks on BM25 until `memory reindex` rebuilds it.

**Score normalization & `min_score`.** All strategies normalize their top match to 1.0, so `retrieval.min_score` means the same thing regardless of strategy — and doesn't silently start dropping every memory the moment `auto` resolves to `semantic` (Ollama present).

**Interactive timeout & fallback.** On the per-turn path the embedding call is bounded by a short timeout; if Ollama is slow or goes away mid-session, retrieval falls back to BM25 for that turn rather than blocking the UI. Batch `memory reindex` keeps the longer timeout.

**Caching.** The BM25 corpus (keyed by a content fingerprint of the memory set) and parsed `.vec` vectors (keyed by file mtime + size) are cached across turns, so a steady-state turn re-ranks without re-stemming every body or re-reading every sidecar. The caches self-invalidate when a memory body or its `.vec` changes (the corpus by content fingerprint, vectors by mtime + size).

#### Enabling semantic retrieval

To get the full advantage of semantic memory retrieval:

1. Install [Ollama](https://ollama.com) if you haven't already
2. Pull a small embedding model:
   ```
   ollama pull nomic-embed-text
   ```
3. Restart yottacode — semantic retrieval activates automatically

`nomic-embed-text` runs entirely on CPU — no GPU required. The model is small (~270MB) and fast, and it runs locally so no data leaves your machine. Once installed, every `memory_save` generates a vector sidecar alongside the memory file. To generate vectors for existing memories, use `/memory` → **Reindex embeddings** or:

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
max_bytes       = 24000             # cap on combined injected body bytes per turn (0 = unlimited)
min_score       = 0.0               # 0.0 = no relevance floor (every entry up to top_k); >0 drops below it
strategy        = "auto"            # "keyword" | "bm25" | "semantic" | "auto"
embedding_model = "nomic-embed-text" # Ollama model for semantic retrieval
```

`top_k` and `max_bytes` are independent caps applied together: retrieval stops at whichever binds first. The byte cap drops the least-relevant tail (entries are rank-ordered), but the single top-ranked entry is always admitted even if it alone exceeds `max_bytes`.

### `/memory` picker

The TUI's `/memory` command opens a five-row picker (plus a conditional sixth row):

| Row | Action |
|---|---|
| Project context | Edits `<repo>/.yottacode/YOTTACODE.md` in vim |
| User preferences | Edits `~/.yottacode/USER.md` in vim |
| Browse user memories | Sub-list of `~/.yottacode/memory/*.md` |
| Browse project memories | Sub-list of `~/.yottacode/projects/<slug>/memory/*.md` |
| Reindex embeddings | Generates `.vec` sidecars for semantic retrieval (requires Ollama) |
| Enable semantic search | Appears only when no embedding model is active (e.g. first run without Ollama); pulls an Ollama embedding model and reindexes |

In the browse sub-lists: `Enter` opens the chosen memory in vim, `d` deletes it (and regenerates `MEMORY.md`), `f` opens the folder in your file manager, `Esc` returns to the root menu.

### Cobra subcommands (for scripts)

The same actions are exposed as non-interactive subcommands so CI or one-off shells can list, delete, and reindex memories without launching the TUI:

```
yottacode memory list [--scope user|project]   # default: project
yottacode memory forget --scope <s> <name>
yottacode memory reindex                       # generate .vec sidecars for all memories
yottacode memory search <query>                # search memories by query (same as memory_search tool)
```

### Agent introspection flow

The agent's self-learning loop uses these tools together:

```
  session_recall("was this discussed before?")
        │
        ▼
  memory_search("do I already know about X?")
        │
        ├── found a match → use it, update if stale
        │
        └── no match → learn from this session
                │
                ├── memory_save(scope=user, ...) for portable knowledge
                └── memory_save(scope=project, ...) for repo-specific facts
```

The agent decides autonomously when to search, save, update, or forget — the tools give it the capability, but the LLM owns the judgment about when and what to remember.

---

## Layer 3 — Recall + summarization

`/recall` remains available as a user-initiated slash command. The agent can now also search past sessions proactively via the `session_recall` tool — same FTS5 index, same ranked results, but the agent decides when to look.

`/recall <query>` searches every saved session in `~/.yottacode/sessions/` via an SQLite FTS5 index at `~/.yottacode/index.sqlite`. Useful for "I remember we discussed X — which session was that in?" The index is rebuilt incrementally on every session save and backfilled at TUI startup.

`/summarize` compresses the active session's transcript when context is filling up. Replaces the message history with a synopsis injected into the system prompt under `## Prior session context (summarized)`. Auto-summarization fires automatically before the next turn at `context.auto_threshold` (default 0.85 — 85% of the model's window).

---

## Decision tree: where does this go?

| Scenario | Where it lives | Why this scope |
|---|---|---|
| "I prefer table-driven tests" | `USER.md` (you write) or `memory_save scope=user, type=user` (agent learns) | Portable — applies in every repo |
| "Build / test / lint commands for this repo" | `YOTTACODE.md` (`/init` drafts; agent keeps fresh) | Repo-specific, team-shareable |
| "User said don't show stack traces" | `memory_save scope=user, type=feedback` | Portable — a communication preference |
| "User approved the bundled-PR approach" | `memory_save scope=user, type=feedback` | Portable — a validated workflow pattern |
| "An approach failed because of X constraint" | `memory_save scope=user, type=feedback` | Portable — lesson learned |
| "JWT cache lives in pkg/auth/cache.go" | `memory_save scope=project, type=project` | Meaningless outside this repo |
| "API has these public endpoints (this repo)" | `memory_save scope=project, type=reference` | Repo-specific API surface |
| "We're mid-refactor of the user model" | Don't save — ephemeral | |
| "Look up which session we discussed X in" | `/recall <query>` | |
| "Compress the current transcript" | `/summarize` | |

---

## Trust model

- **Memory tools run silently by default.** Add `ask: ["Memory(*)"]` to your permissions if you want a modal on every memory write.
- **Don't put secrets in any memory file.** They get loaded into the system prompt every turn and persist on disk in plaintext.
- **Project-scope memory is per-user.** Two developers on the same repo see different `~/.yottacode/projects/<slug>/memory/` dirs. Use `YOTTACODE.md` (in the repo) for things the team should share.
- **The curated layer never gets filtered.** Whatever you write in `USER.md` and `YOTTACODE.md` lands in every system prompt — keep them concise. The "Large file will impact performance" notice fires past 40k chars.
