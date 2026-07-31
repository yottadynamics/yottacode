# Memory

yottacode keeps three kinds of memory, each with one job:

- **Trust anchors** — the curated files (`USER.md`, `YOTTACODE.md`) injected verbatim into every turn's system prompt.
- **Agent-managed typed memories** — markdown files the agent writes via dedicated tools (`memory_save`, `memory_forget`) when something is worth remembering across sessions.
- **Recall + summarization** — search across past sessions (manual `/recall`, the agent's `session_recall` tool, and **automatic per-turn semantic recall**) plus on-demand compression of long histories.

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
       │   ③ ~/.yottacode/memory/user/             user-scope           │
       │      ├── MEMORY.md    auto-generated table of contents         │
       │      └── <name>.md    typed memories (one file each)           │
       │                                                                │
       │   ④ ~/.yottacode/memory/projects/<slug>/  project-scope        │
       │      ├── MEMORY.md    auto-generated table of contents         │
       │      ├── <name>.md    typed memories (one file each)           │
       │      └── subagents/   that project's subagent transcripts      │
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

It executes inside the turn goroutine, not on the input thread: the `semantic` strategy embeds the query via a local Ollama call, and a cold model load can take seconds — running it before the turn used to freeze the input until the user's message echoed. Off the input thread the cost reads as ordinary model latency under the spinner, and Esc cancels an in-flight embed along with the rest of the turn. Embed requests also send `keep_alive: 30m` so Ollama keeps the model resident between turns instead of evicting it after its default ~5 minutes (each successful call re-extends the lease; an active session pays the cold load at most once).

> **Beyond the four sources above.** When automatic session recall is enabled, this same per-turn rebuild also appends a `## Prior conversations` block after the memory tail. It is **not** one of the four memory sources — it comes from the session index (`~/.yottacode/index.sqlite`), not the memory store, and is reads-only. See [Layer 3 — Recall + summarization](#layer-3--recall--summarization).

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
  memory/
    user/                                     # user-scope (cross-project)
      MEMORY.md                               # auto-generated index
      <name>.md                               # one file per memory
      <name>.vec                              # embedding sidecar (semantic mode)
      .archive/                               # prior versions kept on overwrite (see below)
        <name>.<stamp>.md
    projects/
      <project_slug>/                         # project-scope (this repo only, private to you)
        MEMORY.md
        <name>.md
        <name>.vec                            # embedding sidecar (same as user scope)
        .archive/
        subagents/                            # that project's subagent run transcripts
                                              #   (skipped by the memory scanner)
```

The `.archive/` subdirectory holds the prior version of any memory that
`memory_save` overwrote (named `<name>.<unix-nano>.<rand>.md` — the
timestamp is for humans, the random suffix guarantees two concurrent
archivers can't clobber each other), so an update can never silently
destroy a different memory that reused the name. It's a dotted subdir, so
the scanner skips it — archived versions never appear in the index,
retrieval, or `memory list`.

Archive maintenance is explicit. `yottacode memory archive list` shows archive
counts, oldest/newest timestamps, and byte totals by memory. `yottacode memory
archive prune` deletes only files inside `.archive/`, never live memory files;
it defaults to dry-run and requires retention flags such as `--older-than-days`
or `--keep-latest`. The agent-facing `memory_archive_prune` tool follows the
same model: dry runs are read-only, while actual deletion (`dry_run:false`) is
approval-gated.

Mechanical curation actions also record a compact JSONL history under
`.history/<name>.jsonl`, for example when `memory_curate_apply` deletes an empty
entry or moves a portable project memory to user scope. History files are not
scanned as memories and do not enter retrieval.

`<project_slug>` is derived from the git remote (`https://github.com/user/repo.git` → `github-com-user-repo`); falls back to `filepath.Base(cwd)` for non-git directories. Slugs are not guaranteed collision-free: two non-git repos can collide on basename, and the remote-derived slug collapses the org/repo boundary (`.` and `/` both become `-`), so distinct remote URLs can also map to the same slug and share a project-memory directory. A collision means one repo's project memories load/edit in the other.

Per-project memories live in your home directory, not in the repo. They're private to this user/machine — a clone of the same repo on a different machine starts with an empty project memory. Use `YOTTACODE.md` for things the team should share; use project-scope memory for what *you* personally want to remember about working in this repo.

### File shape

Every memory file has YAML frontmatter plus a markdown body:

`source_session` and `source_turn` are optional provenance fields written by
the TUI and one-shot runners when session metadata is available. They identify
where the memory was captured without embedding transcript text into the memory
file; use session recall or the saved session file when you need the full source
conversation. Hand-written memories and older files may omit these fields.

```
---
name: jwt-refresh-flow
type: project
description: How auth refresh interacts with the token cache
created: 2026-05-08T12:34:56Z
source_session: 20260508-123456.000000
source_turn: 7
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
(`user`, `project`, `projects`, `memory`, `index`, `sessions`,
`subagents`, `yottacode`, `feedback`, `reference`) so a memory file can't
collide with a structural filename or layout directory. `memory_save` also refuses path traversal and won't write
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
its own short label — e.g. `decision`, `gotcha`, `architecture`,
`constraint`, `pattern`, `security`, `ops`, or `api-shape`. A custom type
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

The agent is designed to be **self-learning** — it actively builds its understanding of you and your work across sessions and projects, so every future conversation starts smarter than the last. Agent-managed memory is a durable knowledge base, not only a sparse preference list. The save bias intentionally leans toward recall: an unsaved insight disappears when session context is gone, while a marginal note is cheap because only its one-line index entry is always loaded and the body is retrieved on demand.

Save when:

- The user states a durable preference, correction, or project fact.
- The user **confirms or validates** a non-obvious approach — save what worked and why.
- A design or architecture **decision and the reason for it** emerges, including alternatives rejected and why.
- The agent reverse-engineers **how a subsystem works** and would otherwise have to rediscover it later.
- The session exposes a non-obvious **constraint, invariant, or gotcha**.
- The code relies on a durable **security or permission assumption**.
- The work surfaces an **ops or release fact** — how something deploys, what CI enforces, or what release tooling requires — that is durable rather than one-off status.
- The user supplies a reference you'd otherwise re-derive every turn.
- The agent observes a **recurring pattern**: the user always approves a certain style, always rejects a certain approach, always asks for the same thing. The agent doesn't wait for "remember this" — if it sees a pattern twice, it saves it.
- A task outcome teaches something: an approach that failed and why, a subtle constraint discovered, a debugging technique that cracked a hard problem.

Don't save:

- Secrets, tokens, credentials, internal URLs, or PII.
- Ephemeral in-flight task state ("we're mid-refactor of the user model").
- Git-derivable info (current branch, last commit SHA).
- One-off task instructions.
- Code facts trivially derivable from a quick grep.
- **Work-log artifacts that go stale within days.** A specific PR/issue number as the headline fact, a commit SHA, "Phase N done", file counts, and similar status notes do not belong in memory.

The boundary is durable knowledge vs. task log. Do save the durable knowledge even when the surrounding task is transient: the decision, rationale, gotcha, constraint, or way the system actually works. Don't save merely that a task happened.

#### What a good memory looks like

The body is where the value lives — and where "vague memory" failures show up. The guidance (in `prompt.go` and the `memory_save` `content` schema) steers the agent to write each memory for a future agent with **none** of the current session's context:

- **Specific and self-contained.** Concrete particulars — names, file paths, the decision *and its rationale*, the exact constraint or value — so future-you can act without re-deriving anything.
- **The body must add substance beyond the one-line description, never restate it.** A memory whose body echoes its description (description `X shipped in PR #75` / body `X shipped in PR #75`) carries zero information and is delete-grade. The description is the headline; the body is the story.
- **Declarative facts, not self-instructions.** `User prefers table-driven Go tests` ✓ — `Always write table-driven tests` ✗. Imperative phrasing gets re-read next session as a standing order and can override the user's actual request.
- **Prioritize what reduces future steering** — the most valuable memory is one that stops the user from having to correct or remind the agent about the same thing again.

### Proactive saving — reinforcement points

Standing guidance in a system prompt loses the model's attention over a
long agentic session — by the time something durable surfaces, the
"when to save" section is thousands of tokens back. Four reinforcement
points re-surface the capability at the moments that matter. All four
are **reminders, not extractors**: the harness only picks the moment;
whether and what to save stays the model's in-band judgment, and every
reminder carries an explicit "if genuinely nothing durable is unsaved, save nothing" out so
models don't compliance-save junk. The escape hatch is a floor, not the
bias: reminders ask the model to capture anything durable it has not
saved yet, including decisions, rationale, gotchas, and how things work.

1. **Closing nudge (every turn).** The composed system prompt *ends* on
   the save nudge — the most-attended instruction position — instead of
   pure act-on-the-request framing. It appears even on a cold start with
   zero memory sources on disk: a store that never receives its first
   save never bootstraps.
2. **Pre-compaction reminder.** When context usage first crosses
   `context.warn_threshold`, the next user message carries a one-line
   reminder to persist anything durable — preferences, corrections,
   decisions and their rationale, gotchas, how things work, and project
   facts — *before* auto-summarization compacts the older turns away.
   The transcript shows a muted notice when it arms; the reminder itself
   is model-facing only. It disarms when usage drops back below the
   threshold.
3. **Periodic capture reminder** (`[memory] capture_reminder_every_turns`,
   default `6`). Every Nth user message carries a mid-session checkpoint
   asking the model to persist anything durable it hasn't saved yet. This
   exists because the other two mid-session points have narrow triggers:
   the pre-compaction reminder only fires if a session crosses the
   summarize watermark (most never do), and the final turn needs a
   graceful quit — so a medium session ended with `Ctrl+C` previously got
   no reinforcement at all beyond the standing prompt. It rides a message
   you were sending anyway (history copy only, like the pre-compaction
   reminder), so it costs no extra turn, and it stands down when a
   pre-compaction reminder is already pending — that one is more urgent
   and asks for the same thing. Set to `0` to disable.
4. **Final turn on quit** (`[memory] final_turn_on_quit`, default
   `true`). A graceful exit — `/quit` or `Ctrl+D` while idle — runs one
   last visible turn prompting the model to save unsaved durable
   learnings — including decisions, rationale, gotchas, and subsystem
   knowledge — then completes the quit when the turn ends. `Esc` or
   `Ctrl+C` during the turn skips it (cancels and quits); `Ctrl+C` *as*
   the quit gesture always exits immediately, no final turn — those
   sessions are covered by the periodic reminder above. A session with no
   turns started this launch quits instantly.

The save-side behavior is gated by an eval mirroring the retrieval one:
`go test ./internal/agent -run Proactivity -v` runs fixture turns that
state durable facts mid-task against a local Ollama chat model (skipped
when no tool-calling-capable model is available; deterministic
prompt-content pins always run). Each fixture carries a scope ground
truth, so the eval measures both *whether* the model saves unprompted
and *where* the save lands (user vs project scope) — including a trap
fixture stating a portable preference mid-repo-work. See
`internal/agent/memory_proactivity_eval_test.go`.

### Scope selection — cross-project learning

Scope selection is critical for building knowledge that transfers across projects:

- **`scope=user`** (stored in `~/.yottacode/memory/user/`, loaded in **every** project): anything about the person, not the repo. Coding style, communication preferences, tool preferences, workflow patterns, feedback corrections, debugging approaches, domain expertise areas. The test: "would this help me in a completely different repo for this user?" If yes, it's user-scope.
- **`scope=project`** (stored per-repo, loaded only in that repo): **only** for facts that are meaningless outside this specific codebase — architecture decisions, naming conventions unique to this repo, team-specific processes, deployment targets.
- **Default to user-scope.** Most things the agent learns about how someone works, thinks, and prefers are portable. Project-scope is the exception, not the default.
- When saving a project-scope memory, the agent considers: is the underlying principle user-scope? E.g., "user wants table-driven tests in this Go repo" is really "user prefers table-driven tests" (user-scope) — the Go repo is just where it was learned.
- As a backstop, a save that pairs `scope=project` with a portable type (`user` or `feedback`) gets a **scope-check reminder appended to the tool result** — a preference or correction that's repo-only is a near-contradiction, so the agent is prompted (but never forced) to re-save it as user-scope and forget the project copy. Repo-bound `type=project` facts and free-form labels never trigger it.

The full guidance lives in the agent's system prompt; see `internal/agent/prompt.go` for the current copy.

**Precedence — project shadows user.** If the same memory name exists in both scopes, the project-scope version wins *in that repo*: its body injects and the user-scope twin's body is suppressed (it would otherwise duplicate or contradict). This matches how slash commands and config layering resolve project-over-user. The user file stays on disk and still applies in every other repo, where no project twin shadows it.

### The five tools

The agent has seven memory tools. Most are silent by default (no approval modal — they're as ordinary as `read_file`); destructive archive pruning is the exception and requires approval when `dry_run:false`:

- **`memory_save`** — creates a memory file, or updates an existing one of the same name. Only `name` and `content` are required: the five-field form competes with the primary task at exactly the moment something durable surfaces, which is when capture gets skipped. Omitted fields default — `scope` to `user` (baking in the default-to-user steering), `type` to `note` (so quick captures group under one index section, a natural queue for later curation), and `description` to the first non-empty line of the body. The schema still asks for all five and the model should fill them when it can; the defaults are a floor, not a target. On a same-name update the prior version is **archived** to `<memdir>/.archive/<name>.<stamp>.md` (recoverable, never silently lost; excluded from the index, retrieval, and `memory list`) and the original `created` timestamp is preserved. The result reports `created` vs `updated`, whether a version was archived, and for updates a compact changed-fields summary (`type`, `description`, `source`, and body byte-count changes, or `changes: none`). Updates `MEMORY.md`. Generates a `.vec` sidecar when an embedding model is available; if embedding is unavailable, the save still succeeds and the result notes that the semantic index wasn't updated.
- **`memory_forget`** — deletes a memory file by name. Updates `MEMORY.md`. Errors when the named memory doesn't exist (so the agent learns the right names).
- **`memory_search`** — searches across user and/or project memory stores, returning ranked results with relevance scores (zero-relevance entries are omitted). The agent uses this to check for duplicates before saving, find related memories when reasoning about a topic, or verify a remembered fact. Accepts `scope` (`all`, `user`, `project`) and `limit` parameters.
- **`memory_get`** — returns the full, untruncated contents (frontmatter + body) of one memory by `scope` + `name`. Used before updating a memory so the agent can preserve the parts it isn't changing, instead of blindly overwriting from the 300-char `memory_search` preview.
- **`memory_audit`** — read-only curation report with issue-list, plan, proposal, and summary modes.
- **`memory_archive_prune`** — inventories or prunes archived prior versions. Omitted `dry_run` defaults to true and is read-only; `dry_run:false` deletes only selected `.archive/` files and requires approval.
- **`session_recall`** — searches across all past sessions via the FTS5 full-text index. Returns ranked snippets with session metadata (name, date, model). The agent uses this to find prior discussions, check if an issue was already resolved, or pull in context from earlier conversations. Supports FTS5 query syntax (OR, exact phrases in quotes): the raw query is tried first so operators work, and only if that's a syntax error is a sanitized version retried — so a naive hyphenated or punctuation-heavy query still returns results instead of erroring.

The introspection tools (`memory_search`, `memory_get`, `session_recall`) are the key to self-learning — they let the agent think based on its own accumulated knowledge rather than relying only on what the retrieval orchestrator injects each turn.

All memory tools resolve to the `Memory` permission namespace (save / forget /
search / get / audit / archive prune / recall), so a single rule gates every memory operation — every
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

**In-process serialization.** `memory_save`, `memory_forget`, and mechanical
curation applies hold process-wide mutexes for the two mutation scopes that
matter: the individual memory path and the scope-wide index. The per-path lock
serializes same-name read → archive → write races, so two concurrent saves to
one memory archive each predecessor instead of silently losing an intermediate
version. The scope lock covers the body mutation plus `MEMORY.md` regeneration,
so two different-name saves in the same scope cannot render from different
directory snapshots and let a stale index land last. These locks matter because
memory tools can be reachable from detached/background agent work as well as the
main loop.

**Cross-process guarantee.** There is **no OS-level file lock** (no
`flock`/`fcntl`), so the per-path mutex doesn't reach across processes —
two separate yottacode processes, or a process plus a concurrent `yottacode
memory` CLI invocation, share no lock. The exact guarantee that still
holds:

- **Body files are never corrupted.** Each memory is a distinct path; the
  atomic rename is last-writer-wins *on a valid file*, and `ArchivePrior`
  stages through a unique temp so a prior version is never clobbered. A
  save that cannot read an existing file now errors before writing, so an
  unreadable memory is not treated as absent and overwritten without an
  archive.
- **In-process `MEMORY.md` regeneration is serialized.** Different-name
  mutations in one yottacode process hold a scope lock around the mutation
  and the directory scan/render/write, so the always-loaded table of
  contents stays complete after concurrent saves/forgets.

For a single-user desktop tool this residual cross-process race is rare (a
millisecond window needing two simultaneously-mutating processes) and bounded.
An advisory directory lock around memory mutations would close it; it's an
accepted, documented gap rather than a shipped guard.

### Per-turn retrieval

Memory grows over time. By the time you have dozens of memories, dumping every body into every prompt is wasteful. The retrieval orchestrator scores each memory body against the current user prompt and injects only the top-K.

What's filtered:

- **Per-entry bodies** under both scopes — scored, ranked, capped at `retrieval.top_k` and `retrieval.max_bytes`.

What is NOT filtered:

- `USER.md`, `YOTTACODE.md` — always in full.
- Both `MEMORY.md` indexes — always in full. The model needs to know which files exist even when their bodies aren't injected.

The same rebuild also injects an **episodic** counterpart to this semantic retrieval: relevant excerpts from your past *conversations*, found by semantic search over the session index. See [Automatic recall of prior conversations](sessions.md#automatic-recall-of-prior-conversations). It reads sessions only — it never writes memory — and is configured separately under `[retrieval.session_recall]`.

#### Retrieval strategies

yottacode supports three scoring strategies, selectable via config:

| Strategy | How it scores | When to use |
|---|---|---|
| `keyword` | Exact token overlap, name/type/description weighted 3x over body | Legacy fallback; fast, fully transparent |
| `bm25` | Porter stemming + synonym expansion + Okapi BM25 ranking (IDF weighting, term saturation, length normalization) | Default when no embedding model is available. Handles "fakes" → "mocks", "running" → "run", "db" → "database" |
| `semantic` | BM25 score (60%) + cosine similarity from local Ollama embeddings (40%) | When you want conceptual matching — "error handling philosophy" finds memories about soft failures even without shared keywords |
| `auto` **(default)** | Probes for a local Ollama embedding model at session start. If found → `semantic`; otherwise → `bm25` | Recommended. Zero config, best available scoring |

**BM25** is the baseline — pure Go, zero dependencies, deterministic. It ships a Porter stemmer and ~15 hand-curated synonym groups for programming/dev vocabulary (test/mock/fake, database/db/sql, deploy/release/ship, auth/login/credential, etc.). This alone is a major upgrade over raw keyword matching. Synonym-derived query terms are scored at a fractional weight (half of an exact term) so a memory that incidentally touches several distinct synonyms of a group can't outrank one that uses the exact term you searched for — recall stays up, exact-match precision wins ties. (The CLI / TUI `/memory search` preview uses equal weights; the agent's retrieval applies the down-weight.)

**Semantic** layers local embeddings on top when a local Ollama server is available with an embedding model installed. Vector sidecars (`.vec` files) are stored alongside memory `.md` files and generated automatically on `memory_save`. The combined score blends BM25 (which excels at exact matches like file paths and function names) with cosine similarity (which captures conceptual relationships) — by default **60% BM25 / 40% cosine**, tunable via `retrieval.semantic_weight` (the cosine fraction; BM25 gets the rest). Raise it to trust meaning-based matches more on paraphrased queries, lower it (or set `0.0`) to lean on exact keywords. Because the blended score is re-normalized to top=1.0, only the ratio matters. A sidecar produced by a *different* embedding model than the one in use is skipped for the cosine term (cross-model vectors aren't comparable) — that entry simply ranks on BM25 until `memory reindex` rebuilds it.

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
semantic_weight = 0.4               # cosine fraction of the semantic blend; BM25 gets 1 - this (0=pure BM25, 1=pure cosine)
```

`top_k` and `max_bytes` are independent caps applied together: retrieval stops at whichever binds first. The byte cap drops the least-relevant tail (entries are rank-ordered), but the single top-ranked entry is always admitted even if it alone exceeds `max_bytes`.

#### Measuring retrieval accuracy

Retrieval quality is *measured*, not guessed. A relevance-eval harness lives in `internal/memory/eval_test.go`: a labeled fixture (a corpus of memories plus query→expected-memory cases) scored with standard IR metrics — **Hit@1**, **Hit@3**, and **MRR** (mean reciprocal rank).

```
go test ./internal/memory -run Relevance -v
```

- **`TestRetrievalRelevance_BM25`** is the deterministic, dependency-free gate: it runs the fixture through BM25 and fails if quality falls below calibrated floors, so a regression in stemming / synonym expansion / headline weighting is caught in CI.
- **`TestRetrievalRelevance_Semantic`** runs the same fixture through the BM25+embedding blend when a local Ollama model is available (skipped otherwise) and logs a BM25-vs-semantic comparison — including a **paraphrase / low-overlap** set, the regime where keyword scoring is weakest and semantic cosine earns its keep.

On topic-distinct memories BM25 alone already scores perfectly; semantic's measurable advantage shows up on paraphrased, low-keyword-overlap queries. Add cases to the fixture to harden the gate or to characterize a new scoring change before shipping it.

### `/memory` picker

The TUI's `/memory` command opens a six-row picker (plus a conditional seventh row):

| Row | Action |
|---|---|
| Project context | Edits `<repo>/.yottacode/YOTTACODE.md` in vim |
| User preferences | Edits `~/.yottacode/USER.md` in vim |
| Browse user memories | Sub-list of `~/.yottacode/memory/user/*.md` |
| Browse project memories | Sub-list of `~/.yottacode/memory/projects/<slug>/*.md` |
| Search memories | Opens a query box; ranks saved memories and lets you open one (see below) |
| Reindex embeddings | Generates `.vec` sidecars for semantic retrieval (requires Ollama) |
| Enable semantic search | Appears only when no embedding model is active (e.g. first run without Ollama); pulls an Ollama embedding model and reindexes |

In the browse sub-lists: `Enter` opens the chosen memory in vim, `d` deletes it (and regenerates `MEMORY.md`), `f` opens the folder in your file manager, `Esc` returns to the root menu.

**Searching memories.** Two entry points land in the same interactive results overlay: the **Search memories** picker row (which opens a query box first), or `/memory search <query>` typed directly. Results are ranked by configured retrieval semantics and apply the same project-shadows-user precedence as prompt injection (each row shows scope + score + description). `↑`/`↓` scroll, `Enter` opens the highlighted memory in vim, and `Esc` steps back (results → root → close). Exiting the editor returns you to the **same results** — the query isn't lost. Crucially, results render in the overlay and are **never printed into the conversation transcript**, so searching doesn't pollute your session scrollback. It's a deterministic, zero-token way to find "what do I have stored about X" without spending a model turn (the interactive equivalent of the `yottacode memory search` CLI command). Use `/recall <query>` for the analogous search over past *sessions*.

### Cobra subcommands (for scripts)

The same actions are exposed as non-interactive subcommands so CI or one-off shells can list, delete, audit, and reindex memories without launching the TUI:

```
yottacode memory list [--scope user|project]   # default: project
yottacode memory forget --scope <s> <name>
yottacode memory reindex                       # generate .vec sidecars for all memories
yottacode memory search <query>                # search memories with configured retrieval (same precedence as injection)
yottacode memory audit                         # read-only curation report for notes/duplicates/scope/body issues
yottacode memory audit --plan                  # group issues into a read-only curation plan
yottacode memory audit --propose               # draft subjective curation proposals without applying them
yottacode memory health                        # show compact read-only memory health counts
yottacode memory archive list                  # summarize archived prior memory versions
yottacode memory archive prune --dry-run       # preview explicit archive pruning
```

`memory health` and `memory_audit({"summary":true})` expose the compact
health layer: total memories, total issues, quick notes, old quick notes,
duplicate descriptions, vague bodies, empty bodies, and portable scope mistakes.
Use this when the agent only needs to know whether memory needs attention, not
the full curation queue.

`memory audit` is Memory Curation Phase 1: it is deliberately read-only and surfaces the queue a human-like agent needs to consolidate over time — quick-capture `type=note` entries, duplicate descriptions, empty or description-only bodies, and portable `user`/`feedback` memories that landed in project scope. Memory Curation Phase 2 exposes the same report to the agent as `memory_audit`, so an explicit curation turn can inspect the queue, fetch full entries with `memory_get`, then apply each decision with `memory_save` and `memory_forget` instead of relying on shell commands. Memory Curation Phase 3 makes the report actionable: every issue includes a suggested next step such as `memory_get` both duplicates, `memory_save` a consolidated durable entry, then `memory_forget` stale notes. Memory Curation Phase 4 adds provenance and age: audit output includes each memory's `created` date, age in days, and highlights quick notes older than 30 days as priority curation candidates. Memory Curation Phase 5 adds `--plan` / `{"plan":true}` mode, which groups issues into read-only batches such as duplicate merges, quick-note promotion, portable scope moves, and empty-entry cleanup. Memory Curation Phase 6 adds the approval-gated `memory_curate_apply` agent tool for strictly mechanical fixes only: deleting entries that still have an `empty-body` audit issue, or moving `portable-in-project` user/feedback memories to user scope when no user memory with that name exists. Memory Curation Phase 7 adds `--propose` / `{"propose":true}` mode for subjective cases: it drafts merge, rewrite, promote, or forget suggestions with source excerpts and explicit uncertainty, but does not apply them. It does not rewrite, merge, or promote notes automatically. The audit surfaces remain read-only; all apply operations route through explicit tools.

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

  memory_audit(scope=all)
        │
        └── explicit curation pass → memory_get full entries, then
            memory_save consolidated facts and memory_forget stale notes
```

The agent decides autonomously when to search, save, update, or forget — the tools give it the capability, but the LLM owns the judgment about when and what to remember. `memory_audit` is the read-only trigger for deliberate curation: it finds the rough edges, but every write still goes through the normal memory tools so the transcript shows what changed.

---

## Layer 3 — Recall + summarization

Past sessions are searchable **three ways**, all over the same SQLite index at `~/.yottacode/index.sqlite`:

- **`/recall <query>`** — user-initiated FTS5 full-text search across every saved session in `~/.yottacode/sessions/`. Useful for "I remember we discussed X — which session was that in?"
- **`session_recall` tool** — the agent runs the same FTS5 search proactively when it suspects a topic came up before, without you asking.
- **Automatic recall (semantic)** — each turn, yottacode embeds your message, semantically searches past sessions, and injects the most relevant excerpts into the system prompt **on its own** — the episodic counterpart to the per-turn memory retrieval above. Reads-only (it never writes memory), project-scoped by default (the repo root and everything under it), and requires a local embedding model (falls back to the manual tool when unavailable). Repos marked with `yottacode sensitive add` are excluded in both directions — see [Sensitive projects](sessions.md#sensitive-projects). Configured under `[retrieval.session_recall]`; full details in [Automatic recall of prior conversations](sessions.md#automatic-recall-of-prior-conversations).

The FTS index is rebuilt incrementally on every session save and backfilled at startup. When semantic recall is on, message embeddings live in a `message_vectors` table in that same index — backfilled in the background at startup and incrementally after each turn (so a conversation is recallable in later sessions without a restart) — and the thinking-row footer shows `recalled N conversations` when a turn pulls prior context in. Set `YOTTACODE_RECALL_DEBUG=1` to log every candidate — its cosine score and whether it was injected or dropped, including on turns that inject nothing — to `~/.yottacode/recall-debug.log` for tuning `min_score`. The log records a short query digest, not the raw prompt text, so debug tuning does not create a second on-disk copy of secrets or PHI.

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
- **Project-scope memory is per-user.** Two developers on the same repo see different `~/.yottacode/memory/projects/<slug>/` dirs. Use `YOTTACODE.md` (in the repo) for things the team should share.
- **The curated layer never gets filtered.** Whatever you write in `USER.md` and `YOTTACODE.md` lands in every system prompt — keep them concise. The "Large file will impact performance" notice fires past 40k bytes.
