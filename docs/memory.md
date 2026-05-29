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

`YOTTACODE.md` is the project's brief. `/init` drafts it from the current repo (build commands, layout, conventions, gotchas) and aims to keep it under ~150 lines. After non-trivial work the agent will offer to refresh it through the approval modal. The startup "large memory will impact performance" notice is keyed on **file size, not line count** — it fires once a curated file exceeds 40k bytes (see [Trust model](#trust-model)).

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
        <name>.vec                            # embedding sidecar (same as user scope)
        .archive/
```

The `.archive/` subdirectory holds the prior version of any memory that
`memory_save` overwrote (named `<name>.<unix-nano>.<rand>.md` — the
timestamp is for humans, the random suffix guarantees two concurrent
archivers can't clobber each other), so an update can never silently
destroy a different memory that reused the name. It's a dotted subdir, so
the scanner skips it — archived versions never appear in the index,
retrieval, or `memory list`. There is **no automatic retention policy or
config knob today**: `memory_forget` deletes the live file and its `.vec`
but does not prune `.archive/`, and nothing ages archives out. Pruning is
fully manual (`rm -rf ~/.yottacode/memory/.archive`). Each archived body
is a small markdown file, so unbounded growth is a housekeeping nit rather
than a disk concern for a single user — a configurable retention policy is
a known follow-up, not a shipped feature.

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

The **filename is the memory's identity**, not the frontmatter `name:`
field — the index links to `<name>.md` and `memory_forget` resolves a
memory by recomputing `<name>.md`, so the scanner trusts the basename and
the frontmatter `name:` is human-facing redundancy. `name` must be
kebab-case (`^[a-z0-9][a-z0-9-]{0,63}$` — lowercase alphanumeric start,
hyphens, ≤64 chars), and a small set of names is **reserved and rejected**
(`user`, `project`, `memory`, `index`, `sessions`, `yottacode`,
`feedback`, `reference`) so a memory file can't collide with a structural
filename. `memory_save` also refuses path traversal and won't write
through a symlink.

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
is validated only as a label (lowercased + trimmed, then any run of
spaces/underscores/hyphens collapsed to a single hyphen; lowercase
letters, digits, hyphens; ≤32 chars) and renders as its own
`## <type>` group in the index, after the four conventional ones
(alphabetically). The `type` only labels and groups — it never restricts
what the body can hold and is not a retrieval filter. The body content is
unconstrained regardless of type.

> **Separators are canonicalized.** Validation lowercases, trims, and then
> collapses any run of spaces, underscores, or hyphens to a single hyphen
> (trimming the ends), so `api shape`, `api_shape`, and `API-shape` all
> store as `api-shape` and group under **one** `## api-shape` section
> rather than fragmenting into three near-duplicate headers. Since `type`
> is a label and index-grouping key only — never a retrieval filter — this
> is purely about keeping the index tidy; the body and ranking are
> unaffected either way.

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
- **`session_recall`** — searches across all past sessions via the FTS5 full-text index. Returns ranked snippets with session metadata (name, date, model). The agent uses this to find prior discussions, check if an issue was already resolved, or pull in context from earlier conversations. Supports FTS5 query syntax (OR, exact phrases in quotes): the raw query is tried first so operators work, and only if that's a syntax error is a sanitized version retried — so a naive hyphenated or punctuation-heavy query still returns results instead of erroring.

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

**Atomic writes.** Every memory write (a `<name>.md`, the regenerated
`MEMORY.md`, or a `.vec` sidecar) goes through one atomic-write path: stage
to a **unique** temp file in the same directory, `fsync`, then `rename` onto
the destination and `fsync` the directory. The unique temp name means two
writers can't interleave bytes into a shared staging file or delete each
other's in-flight temp; the `fsync` closes the crash window that a bare
`rename` leaves (a file coming back zero-length or stale after power loss).
Reads are best-effort and never block a turn.

**In-process serialization.** `memory_save` holds a per-path mutex across
its whole *read → archive → write → regenerate-index* sequence. This
matters because the same tool instance is shared into detached background
subagents: without the lock, two concurrent saves to the same name would
both read the same prior, both archive only that prior, and the second
`rename` would silently drop the first writer's *new* content. The lock
makes the sequence serial, so each writer archives the previous writer's
version and nothing is lost. (`memory_forget` and the TUI delete don't take
the lock; they're already serialized within a single agent loop because the
memory tools aren't parallel-safe.)

**Cross-process guarantee.** There is **no OS-level file lock** (no
`flock`/`fcntl`), so the per-path mutex doesn't reach across processes —
two separate yottacode processes, or a process plus a concurrent `yottacode
memory` CLI invocation, share no lock. The exact guarantee that still
holds:

- **Body files are never lost or corrupted.** Each memory is a distinct
  path; the atomic rename is last-writer-wins *on a valid file*, and
  `ArchivePrior` stages through a unique temp so a prior version is never
  clobbered.
- **Only `MEMORY.md` can go transiently stale.** Index regeneration is a
  read-modify-write over the whole directory (scan all `*.md` → render →
  atomic-write), so an unlucky interleave can let a regen rendered from an
  older scan land last and drop a just-added entry's *table-of-contents
  line*. This is **cosmetic and self-healing**: `MEMORY.md` is a rendered
  convenience index, not the source of truth — retrieval and injection scan
  the directory directly (`Load` → `scanMemoryDir`), so the dropped entry's
  body still injects and is still searchable, and the next save/forget
  rewrites the index from a fresh scan. No memory disappears.

For a single-user desktop tool this residual race is rare (a millisecond
window needing two simultaneously-mutating processes) and harmless. An
advisory directory lock around index regeneration would close it; it's an
accepted, documented gap rather than a shipped guard.

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

**Interactive timeout & fallback.** On the synchronous, user-facing paths — both per-turn retrieval and `memory_save` — the embedding call is bounded by a short ~2s timeout. If Ollama is slow or goes away mid-session, retrieval falls back to BM25 for that turn and `memory_save` still completes (the `.md` is written; only the `.vec` is skipped, with a note to run `memory reindex` later) — neither blocks the UI. Batch `memory reindex` keeps the longer 30s timeout.

**Caching.** The BM25 corpus (keyed by a content fingerprint of the memory set) and parsed `.vec` vectors (keyed by file mtime + size) are cached across turns, so a steady-state turn re-ranks without re-stemming every body or re-reading every sidecar. The caches self-invalidate when a memory body or its `.vec` changes (the corpus by content fingerprint, vectors by mtime + size).

#### Changing the embedding model

Each `.vec` sidecar records **which model produced it**. The file starts
with a `YVEC` magic header followed by the embedding model name and
dimension count, then the float32 vector. Two consequences:

- **Retrieval is self-protecting.** `entryCosine` only blends a sidecar's
  cosine into the score when its recorded model matches the active model.
  A sidecar from a *different* model — or a pre-header *legacy* sidecar
  with no model recorded — contributes cosine 0 and the entry ranks on
  BM25 alone. So switching `embedding_model` never injects garbage
  similarity; at worst you lose the semantic boost on not-yet-reindexed
  entries until you rebuild them. (Legacy raw-float32 `.vec` files written
  before the header existed are still readable and are simply treated as
  "needs re-embed".)
- **`memory reindex` is the migration path, and it's incremental.**
  Reindex calls `NeedsReembed`, which re-embeds only entries whose sidecar
  is missing, legacy, or from a different model — entries already embedded
  with the current model are skipped and reported as "up-to-date". So
  after changing the model you run `yottacode memory reindex` (or `/memory`
  → **Reindex embeddings**) once and only the stale sidecars are rewritten.

There is **no automatic trigger** that detects a model change and reindexes
for you — the rebuild is a manual (but cheap and incremental) step. Until
you run it, affected entries fall back to BM25 rather than producing wrong
results.

#### Enabling semantic retrieval

To get the full advantage of semantic memory retrieval:

1. Install [Ollama](https://ollama.com) if you haven't already
2. Pull a small embedding model:
   ```
   ollama pull nomic-embed-text
   ```
3. Restart yottacode — semantic retrieval activates automatically

At session start yottacode runs a short (~800ms) probe against the Ollama
server (`/api/tags`) that distinguishes three states: server unreachable
(stay on BM25, silent), server reachable but the configured model missing
(stay on BM25 and print a one-line `[memory] embedding model … not
installed — run: ollama pull …` notice), and model present (resolve `auto`
→ `semantic`). The probe is deliberately separate from the per-turn
embedding timeout so a missing model surfaces a targeted hint instead of
silently degrading.

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

`/memory search <query>` skips the picker and ranks your saved memories inline — same BM25 scoring the agent's retrieval uses, printed straight to the transcript with relevance scores. It's a deterministic, zero-token way to see "what do I have stored about X" without spending a model turn (the equivalent of the `yottacode memory search` CLI command, in the TUI). Use `/recall <query>` for the analogous search over past *sessions*.

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
- **The curated layer never gets filtered.** Whatever you write in `USER.md` and `YOTTACODE.md` lands in every system prompt — keep them concise. The "Large file will impact performance" notice fires past 40k bytes.
