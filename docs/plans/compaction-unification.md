# Compaction unification (gap-analysis step 8)

Status: implemented (uncommitted, stacked on steps 1-7 in the same worktree).
Deviations: calibration is not wired into the TUI path (it already budgets from
provider-reported `contextTokens`); the TUI path gained prune-first for auto
only; the old synthetic summary pair is no longer retained as a "turn".

## Decisions (2026-10-11)
1. One summary format, a superset: the existing four sections (Decisions made,
   Code changes, Open questions, Preferences expressed) plus "Goal and
   progress" and "Next step". `ensureFourSections` keeps its guarantee.
2. Two layouts stay. Subagent/in-loop: summary folded into the task message.
   TUI: synthetic user preamble + assistant summary + last <=5 turns (newest
   turn always kept). Only the engine is shared.
3. Clean cut-over: old prompts, `renderHistoryForSummarization` and duplicated
   constants are deleted in the same change.

## Design
New package `internal/compaction` (imports neither `agent` nor `tui`):
- `Streamer` interface; `Summarize(ctx, s, Input{Middle, PrevSummary, Window}, onFallback)`.
- First-compaction and update prompts (single source).
- Input rendering with truncated tool args.
- File-ops extraction, `<files>` block, previous-note parsing that recognizes
  both note shapes: `[Earlier progress compacted ...]` (in-loop) and
  `[Session summary - compressed at ...]` (TUI).
- Shared retained-tool cap constants and truncation marker.
- `contextwindow` keeps prune + estimation; calibrator moves here or stays in
  `agent` if the dependency direction is awkward (decide while implementing).

TUI path gains: prune-first, calibrated trigger, iterative update (no
summary-of-summary), file list, convergence guard + one-shot warning.

## Implementation order
1. Extract engine into `internal/compaction`; point `agent` at it (no behavior
   change; existing agent tests are the safety net).
2. Point `tui/cmd_summarize.go` at it; port prompt/format; delete duplicates.
3. Two-shape previous-note parsing + tests (in-loop note then /summarize and
   the reverse).
4. Port TUI tests: summarize, routing, guard, memory-race, subagent gate.
5. Docs: `docs/sessions.md` (summary format, snapshots), `docs/configuration.md`.

## Risks to check explicitly
- `compactionSeq` staleness handling across a prune-only or fallback result.
- fast->smart degrade and `EventFallback` surfacing still work.
- `resume --summarized` / `readNewestSnapshot` with OLD on-disk snapshots
  (marker-only parse) and NEW six-section ones.
- Session gist/picker extraction keyed on `session.SummaryPreamble`.
- Memory-race test around history cloning under `histMu`.
