# Compaction gap analysis: yottacode vs oh-my-pi

Date: 2026-10-11. Reference: `can1357/oh-my-pi` `docs/compaction.md`,
`handoff-generation-pipeline.md`, `memory.md` (docs only, source not read).
Audited: `internal/agent/compaction.go`, `internal/agent/loop.go:480-515`,
`internal/tui/cmd_summarize.go`, `internal/agentruntime/runtime.go:695`,
`internal/config/config.go` (context section).

## 1. What yottacode has today

Two independent compaction paths:

| | In-loop (`agent.compact`) | TUI `/summarize` + auto-summarize |
|---|---|---|
| Where | `compaction.go`, called at top of every loop iteration | `cmd_summarize.go`, between turns at `auto_threshold` (0.85) |
| Trigger | `min(threshold*window, window-50k)`, floored at 0.5*window; threshold = `min(0.70, auto-0.10)`; tool schemas counted | fraction of window |
| Kept | system + first user msg (task) + tail up to `target_ratio` (0.35) of window, snapped to an assistant msg | last 5 turns, 0.4*window byte budget |
| Summary | one prompt: goal / findings / decisions / next step; folded into the task message | 4 fixed sections (decisions, code changes, open questions, preferences) |
| Safety | convergence guard, tool-result budget cap on tail (4096/256 tokens), pre-compact snapshot, forced run on provider overflow (1 attempt), best-effort failure | snapshot, 5 min timeout, fallbacks, `ensureFourSections` |

Strengths worth keeping: convergence guard, absolute-token margin against
over-reported windows, summarizer-window-aware input budget, roles stay
alternating (summary folded into task), orphaned tool_use repair, snapshots.

## 2. Gaps (ranked by value / effort)

### G1. No tool-result pruning before summarizing (high value, low effort)
oh-my-pi prunes first: protects newest 40k tool tokens, requires >=20k
savings, blanks superseded reads and zero-result searches in place
(`[Output truncated - N tokens]`), and only when cache-safe. We only truncate
tool results in the *retained tail* at compaction time (`capRetainedToolMessages`)
and to 600 chars inside the summary input. Old large `read_file`/`run_bash`
output sits in context at full size until a full LLM compaction fires.
Effect: more compactions than needed, each costing a summary call and
losing detail.
Proposal: new `internal/contextwindow/prune.go`, run in `maybeCompact` before
the threshold check; mutate only messages older than a protected window;
keep call/result pairs (blank content only). Cache safety: only prune when
the message is further back than N positions from the end, or session idle
past cache TTL. Tests: pairing preserved, errors never pruned, idempotent.

### G2. Summary is not iterative (high value, low effort)
Each compaction summarizes the middle, but the previous progress note lives
inside the task message and is re-fed as ordinary user text; the `/summarize`
path re-summarizes from scratch. oh-my-pi has a distinct update prompt that
takes `<previous-summary>` and merges. Repeated compactions in long sessions
degrade (summary of summary) and the in-loop note grows unbounded by being
appended to the task each time (`assembleCompacted` appends the marker and a
new summary; a second compaction appends again onto an already-extended task).
Proposal: detect `compactionProgressMarker` in the task, split it off, pass it
as `<previous-summary>`, and replace (not append) the note.

### G3. File-operation tracking absent (medium value, low effort)
oh-my-pi appends a capped `<files>` tree (Read / Write / RW). Our in-loop
summary only mentions paths if the model chose to. `renderForCompaction`
reduces tool calls to "ASSISTANT used tool NAME": **arguments are dropped**, so
the summarizer cannot see which file was read or edited. That is a real
quality bug independent of the comparison.
Proposal: (a) include a truncated args line (<=200 chars) in the rendered
body; (b) deterministically extract paths from read/edit/write tool calls and
append a capped file list to the summary, not LLM-dependent.

### G4. Two prompts, two code paths with duplicated constants (medium, medium)
`maxRetainedToolTokens`, `minRetainedToolTokens`, truncation marker,
tail-selection and summary prompts are duplicated between `agent/compaction.go`
and `tui/cmd_summarize.go`. The in-loop prompt has no structure; `/summarize`
lacks "next step" and "findings" (the most useful fields for resuming).
Proposal: one `contextwindow`/`compaction` package with one prompt family
(first + update), used by both. Larger refactor; do after G1-G3.

### G5. Cut-point logic is coarse (medium, medium)
Tail start is snapped *forward* to the next assistant message, which can
discard more than the budget implies (possibly an empty tail) and can drop the
user message that prompted the retained assistant turn. oh-my-pi never cuts at
a tool result, handles split turns (summary of turn prefix merged in), and
does not move entries. Also `keepRecent` is a fixed fraction of window; theirs
adapts to measured usage ratio.
Proposal: allow cutting at user messages; when cutting mid-turn, keep the
preceding user message verbatim.

### G6. Estimation is chars/4 only (medium, medium)
Trigger accuracy depends on `EstimateTokens`. oh-my-pi adapts using measured
provider usage. We already receive provider usage per response; calibrate the
estimator (ratio = reported prompt tokens / estimated) and apply it to the
trigger and `keepRecent`. Cheap and removes part of the need for the 50k margin.

### G7. Single recovery path, one attempt (low-medium, low)
Overflow recovery is forced compaction once. oh-my-pi adds incomplete-output
(`stopReason=length`) recovery, progress guard with warning, and a fallback
chain. We have `continueReasonTruncatedOutput` in loop.go but it is not tied
to compaction. Proposal: a minimal fallback chain
`llm summary -> prune-only -> tail-cap-only` and a user-visible warning when
no method reduces below the trigger (currently silent `return false, nil`).

### G8. Speculative / async compaction (low value now)
oh-my-pi starts early in a band below the threshold. Needs background work and
cache-prefix care; defer until G1-G3 reduce frequency.

### G9. Idle compaction, branch summaries, handoff, hooks (low)
Idle: only helps cache-expiry cases. Branch summaries: no tree navigation in
yottacode. Handoff document: overlaps with the existing memory/session
resume story. Hooks: no extension system equivalent; skip.

### Not recommended
- snapcompact (image-rendered history): vision-model dependent, provider-specific,
  conflicts with portability principle.
- Provider-native/remote compaction (OpenAI compact, Anthropic server-side):
  provider-specific; revisit once the local pipeline is solid, as an optional
  first method behind a capability flag.

## 3. Suggested sequencing

1. G3a (include tool-call args in summary input) - tiny, pure quality fix.
2. G1 tool-result pruning + tests.
3. G2 iterative summary (replace, not append) + tests.
4. G3b deterministic file list.
5. G6 estimator calibration.
6. G5 cut-point improvements.
7. G7 fallback chain and warning.
8. G4 unify the two paths (own PR).

Each step ships with tests and a docs update (`docs/sessions.md`,
`docs/configuration.md` where settings change). Candidate new settings
(only if needed): `context.prune_enabled` (default true),
`context.prune_protect_tokens` (default 40000). Keep everything else derived,
consistent with the existing "not independently configurable" stance.

## 4. Open questions
- Is prompt-cache position known to the loop (needed for G1 cache-safe
  timing)? Anthropic adapter sets `cache_control`; others do not. Default to
  pruning only far-back messages if unknown.
- Should the main-thread loop prune with the same thresholds as subagents?
- Verify claim in G2 by test: run compaction twice and inspect the task
  message (read from code only, not reproduced).
