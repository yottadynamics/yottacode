package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/compaction"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
)

// DeriveCompactionThreshold computes the in-loop compaction trigger from
// auto_threshold. Keeping this in the agent package ensures TUI, oneshot, and
// ACP runtime construction use the same safety policy.
func DeriveCompactionThreshold(autoThreshold float64) float64 {
	if autoThreshold >= 1.0 {
		return 1.0
	}
	t := 0.70
	if cap := autoThreshold - 0.10; cap < t {
		t = cap
	}
	if t < 0.10 {
		t = 0.10
	}
	return t
}

// verbatim as the recent tail after a compaction. The rest of the budget
// covers the system prompt, the original task, the synthetic progress
// summary, and the next turn's output. 0.35 leaves enough headroom that
// compaction drops well below the trigger threshold, so it won't re-fire
// until the history grows back near the window.
const defaultCompactionTargetRatio = 0.35

func compactionTargetRatio(cc *CompactionConfig) float64 {
	if cc == nil || cc.TargetRatio <= 0 {
		return defaultCompactionTargetRatio
	}
	return cc.TargetRatio
}

// subagentCompactionThreshold is the fraction of the window at which a
// subagent's in-loop compaction fires. Set below the TUI's 0.85
// auto-summarize default so the summary call itself — which sends the
// dropped middle as input — still fits comfortably inside the window.
const subagentCompactionThreshold = 0.75

// compactionAbsoluteMargin is the minimum number of tokens of headroom
// in-loop compaction must leave before the resolved window, regardless of
// what Threshold*Window computes to. A purely percentage-based trigger
// assumes the resolved window is trustworthy; it often isn't — some
// backends enforce well under their advertised context_window (see
// catalog.ResolveWindowForProvider's doc comment), so a "safe" fraction of
// an over-estimated window can still leave too little real room. It's also
// not just the conversation that counts against the trigger: tool schemas
// ride on every request (already counted via toolSchemaTokens below) and
// a single large turn can add tens of thousands of tokens between checks.
// 50,000 comfortably covers a realistically large registered toolset
// (measured system-prompt overhead alone on a real session ran ~14K) plus
// one big turn's growth, independent of how large window is.
const compactionAbsoluteMargin = 50_000

// CompactionTriggerTokens returns the absolute token count at which
// in-loop compaction fires for the given fraction and resolved window: the
// smaller of threshold*window and window-compactionAbsoluteMargin, floored
// so the margin can never push the trigger below half the window (a small
// or already-conservative window shouldn't be squeezed into firing
// immediately). Exported so /context can display the exact number driving
// the "fires at" line without duplicating the formula.
func CompactionTriggerTokens(threshold float64, window int) int {
	if window <= 0 {
		return 0
	}
	byFraction := threshold * float64(window)
	byMargin := float64(window) - compactionAbsoluteMargin
	floor := 0.5 * float64(window)
	if byMargin < floor {
		byMargin = floor
	}
	if byMargin < byFraction {
		return int(byMargin)
	}
	return int(byFraction)
}

// minCompactionWindow is the smallest context window for which in-loop
// compaction is worth enabling. Below it, compactionInputBudget (window
// − the per-turn output reservation − the prompt) goes non-positive, so
// there's no room to summarize anything — compaction can't make progress
// and would only burn a doomed summary call each iteration. Models this
// small are barely usable for agentic work anyway; they degrade to the
// provider's own context limit. 16384 keeps the input budget comfortably
// positive (~16384 − 8192 output − ~2200 overhead ≈ 6K of input room).
const minCompactionWindow = 16384

const compactionProgressMarker = compaction.ProgressMarker

var (
	errCompactionNoOlderHistory  = errors.New("compaction: no older history to summarize; the retained context alone is over the trigger")
	errCompactionCannotConverge  = errors.New("compaction: summarizing the older history would not bring the request under the trigger")
	errEmptyCompactionSummarizer = errors.New("compaction: no summarizer available")
)

// newChildCompaction builds the in-loop CompactionConfig for a child
// subagent, or nil when the child window is too small for compaction to
// help (below minCompactionWindow the summary input budget would be
// non-positive — see minCompactionWindow). childWindow is the resolved
// child-model window; summarizer + summarizerWindow describe the
// (possibly cheaper, possibly smaller-window) model that runs the summary
// call. summarizer may be nil — maybeCompact then falls back to the
// loop's own adapter; summarizerWindow 0 means "same window as the child."
func newChildCompaction(childWindow int, summarizer Streamer, summarizerWindow int) *CompactionConfig {
	if childWindow < minCompactionWindow {
		return nil
	}
	return &CompactionConfig{
		Window:           childWindow,
		Threshold:        subagentCompactionThreshold,
		TargetRatio:      defaultCompactionTargetRatio,
		Summarizer:       summarizer,
		SummarizerWindow: summarizerWindow,
	}
}

// maybeCompact summarizes the loop's older history in place when it
// approaches the configured context window. It is a no-op unless
// cfg.Compaction is enabled and the estimated history size has crossed
// Threshold*Window.
//
// The rewrite keeps the leading system message(s) and the original task
// (the first user message) as anchors, folds one synthetic progress
// summary into the task's user message, and retains a recent verbatim
// tail. The tail is snapped to start on an assistant message so it never
// begins with an orphaned tool_result whose tool_use was summarized away
// — the pairing invariant the providers enforce. Folding the summary
// (rather than inserting it as its own assistant turn before the
// assistant-led tail) keeps the roles alternating, which Gemini and
// Claude 4.6+ require.
//
// Compaction is best-effort: a failed summary call leaves history
// untouched and surfaces a ContextCompacted{Err} for the transcript
// rather than aborting the turn. The only error maybeCompact returns is
// a context cancellation observed while emitting the success event.
func maybeCompact(ctx context.Context, cfg LoopConfig, history *[]adapter.Message, events chan<- Event) error {
	_, err := compact(ctx, cfg, history, events, false)
	return err
}

// compact is maybeCompact's worker. When force is true it skips the
// threshold/convergence gates and may shrink only the retained tail by
// capping oversized tool results; that cap-only path is what lets a
// provider-limit rejection recover even when there is no older middle to
// summarize. The changed result lets callers decide whether a retry can
// make progress.
func compact(ctx context.Context, cfg LoopConfig, history *[]adapter.Message, events chan<- Event, force bool) (bool, error) {
	cc := cfg.Compaction
	// Threshold <= 0 or >= 1.0 both mean "preemptive compaction disabled"
	// (deriveCompactionThreshold/DeriveCompactionThreshold return 1.0
	// exactly for that). Without the >= 1.0 half, CompactionTriggerTokens'
	// absolute-margin cap below turns "disabled" into "fires at
	// window-50,000" instead of never — force still bypasses this so
	// provider-overflow recovery can force one attempt regardless.
	if cc == nil || cc.Window <= 0 || (!force && (cc.Threshold <= 0 || cc.Threshold >= 1.0)) {
		return false, nil
	}
	h := snapshotHistory(cfg, history)
	original := h
	before := contextwindow.EstimateTokens(h)
	// Tool schemas ride on every request but aren't part of the message
	// slice, so EstimateTokens omits them. Count them toward the trigger —
	// a subagent's cloned toolset is easily several thousand tokens — so
	// compaction fires against the real on-wire size, not an undercount.
	overhead := toolSchemaTokens(cfg.Registry)
	threshold := float64(CompactionTriggerTokens(cc.Threshold, cc.Window))
	// Estimates are scaled by the ratio learned from provider-reported prompt
	// sizes (see compaction_calibrate.go).
	scale := cc.calib.scale
	if !force && float64(scale(before+overhead)) < threshold {
		cc.warned.Store(false)
		return false, nil
	}
	firstUser := firstUserIndex(h)
	if firstUser < 0 {
		return false, nil // no task anchor — nothing sensible to compact
	}
	// Cheapest reduction first: blank superseded reads and stale bulk tool
	// output. It runs only here, past the trigger, so the prompt-cache miss it
	// causes is paid at the moment compaction would have paid it anyway. If it
	// brings the request comfortably under the line (80% of the trigger) the
	// LLM summary is skipped entirely.
	pruned := false
	if p, _, ok := contextwindow.PruneToolOutputs(h); ok {
		h, pruned = p, true
		if after := contextwindow.EstimateTokens(h); !force && float64(scale(after+overhead)) <= 0.8*threshold {
			cc.warned.Store(false)
			setHistory(cfg, history, h)
			path, snapErr := preCompactSnapshot(cc, original)
			return true, send(ctx, events, ContextCompacted{Before: before, After: after, SnapshotPath: path, SnapshotErr: snapErr, Pruned: true})
		}
	}
	// Fallback chain: when the summary can't run or can't win, still keep
	// whatever the cheaper stages (prune, tail cap) achieved rather than
	// discarding it, and tell the user why the context stayed large.
	var tailCapped bool
	reduce := func(cause error) (bool, error) {
		if !pruned && !tailCapped {
			return false, cc.warnOnce(ctx, events, before, cause, force)
		}
		after := contextwindow.EstimateTokens(h)
		if after >= before {
			return false, cc.warnOnce(ctx, events, before, cause, force)
		}
		path, snapErr := preCompactSnapshot(cc, original)
		setHistory(cfg, history, h)
		return true, send(ctx, events, ContextCompacted{Before: before, After: after, SnapshotPath: path, SnapshotErr: snapErr, Pruned: true, Forced: force, Warning: cause})
	}
	tailBudget := int(compactionTargetRatio(cc) * float64(cc.Window) / cc.calib.get())
	tailStart := chooseCompactionTailStart(h, firstUser, tailBudget)
	tailStart, latestRequest := resolveLatestRequest(h, firstUser, tailStart)
	capped, cappedChanged := capRetainedToolMessages(h, tailStart, tailBudget)
	if cappedChanged {
		h, tailCapped = capped, true
	}
	middle := h[firstUser+1 : tailStart]
	if len(middle) == 0 {
		if force && (cappedChanged || pruned) {
			path, snapErr := preCompactSnapshot(cc, original)
			setHistory(cfg, history, h)
			after := contextwindow.EstimateTokens(h)
			return true, send(ctx, events, ContextCompacted{Before: before, After: after, SnapshotPath: path, SnapshotErr: snapErr, Forced: true})
		}
		// The anchors + retained tail already span everything; there's
		// no older middle to summarize. Keep any prune/cap gains and let
		// the turn proceed with a warning.
		return reduce(errCompactionNoOlderHistory)
	}
	// Convergence guard: if dropping the entire middle still wouldn't get
	// the request under the threshold, the irreducible floor (system +
	// task anchor + the retained tail + tool schemas) already exceeds it —
	// e.g. a single tool result larger than the retain budget. Summarizing
	// can't win, so skip rather than spend a summary call every iteration
	// that never gets under the line. Force skips this because the provider
	// has already proven the current request is too large, and capping the
	// retained tail may have lowered the floor.
	overage := float64(scale(contextwindow.EstimateTokens(h)+overhead)) - threshold
	if !force && float64(scale(contextwindow.EstimateTokens(middle))) <= overage {
		return reduce(errCompactionCannotConverge)
	}

	summarizer := cc.Summarizer
	if summarizer == nil {
		summarizer = cfg.Adapter
	}
	if summarizer == nil {
		// No summarizer wired (neither an injected Summarizer nor the
		// loop's own Adapter). Can't compact; soft-skip so the turn still
		// proceeds rather than nil-derefing on the ChatStream call.
		return reduce(errEmptyCompactionSummarizer)
	}
	// Budget the summary INPUT against the SMALLER of the loop window and
	// the summarizer's own window: under cache-safe routing the summary
	// runs on a cheaper model whose window may be smaller than the child's,
	// so a middle sized to the child window would overflow the summarizer.
	budgetWindow := cc.Window
	if cc.SummarizerWindow > 0 && cc.SummarizerWindow < budgetWindow {
		budgetWindow = cc.SummarizerWindow
	}
	_, prevSummary, prevFiles := compaction.SplitNote(h[firstUser].Content)
	summary, err := compaction.Summarize(ctx, summarizer, compaction.Input{
		Middle: middle, Window: budgetWindow, Scale: cc.calib.get(), Prev: prevSummary, PrevFiles: prevFiles}, nil)
	if err != nil {
		// Best-effort: keep any prune/cap gains and record the attempt. The
		// turn continues and may hit the provider's own context limit, which
		// is the pre-existing (pre-compaction) behavior.
		return reduce(err)
	}
	cc.warned.Store(false)

	out := assembleCompacted(h, firstUser, tailStart, summary, latestRequest)
	path, snapErr := preCompactSnapshot(cc, original)
	// Whole-slice replacement under the history lock. Today only subagents
	// compact (cfg.HistoryLock nil → a plain assignment), but routing it
	// through setHistory keeps it correct if the main TUI loop ever enables
	// compaction, where another goroutine reads the same slice.
	setHistory(cfg, history, out)
	return true, send(ctx, events, ContextCompacted{Before: before, After: contextwindow.EstimateTokens(out), SnapshotPath: path, SnapshotErr: snapErr, Forced: force})
}

func preCompactSnapshot(cc *CompactionConfig, history []adapter.Message) (string, error) {
	if cc == nil || cc.PreCompact == nil {
		return "", nil
	}
	return cc.PreCompact(history)
}

const (
	maxRetainedToolTokens        = compaction.MaxRetainedToolTokens
	minRetainedToolTokens        = compaction.MinRetainedToolTokens
	retainedToolCompactionMarker = compaction.RetainedToolMarker
)

// capRetainedToolMessages shrinks h[tailStart:]'s tool messages so their
// COMBINED token cost fits budgetTokens whenever achievable without
// truncating any one below minRetainedToolTokens, via the shared
// contextwindow.ToolBudgetCaps — also used by the TUI's capRetainedToolBudget
// (internal/tui/cmd_summarize.go), which needs the identical algorithm for
// its own tail. Only the message-shaping details below (truncation marker,
// lazy-copy plumbing, return shape) differ per caller. A per-message-only
// cap (each oversize tool message truncated independently to
// maxRetainedToolTokens) bounds each message but not their sum: a tail with
// many moderately-large tool results (e.g. 30+ tool calls before the
// retained tail's anchor) could still total several times budgetTokens.
func capRetainedToolMessages(h []adapter.Message, tailStart, budgetTokens int) ([]adapter.Message, bool) {
	if tailStart < 0 {
		tailStart = 0
	}
	if tailStart > len(h) {
		tailStart = len(h)
	}
	tail := h[tailStart:]
	toolIdxs, raw, caps := contextwindow.ToolBudgetCaps(tail, budgetTokens, maxRetainedToolTokens, minRetainedToolTokens)
	if len(toolIdxs) == 0 {
		return h, false
	}

	var out []adapter.Message
	changed := false
	for k, i := range toolIdxs {
		m := tail[i]
		if raw[k] <= caps[k] {
			continue
		}
		if out == nil {
			out = append([]adapter.Message(nil), h...)
		}
		maxChars := caps[k] * 4
		if maxChars <= len(retainedToolCompactionMarker) {
			m.Content = retainedToolCompactionMarker
		} else {
			m.Content = m.Content[:maxChars-len(retainedToolCompactionMarker)] + retainedToolCompactionMarker
		}
		out[tailStart+i] = m
		changed = true
	}
	if !changed {
		return h, false
	}
	return out, true
}

// toolSchemaTokens estimates the token cost of the tool schemas the
// registry advertises on every request. Counted toward compaction's
// trigger because they're part of the real request size yet absent from
// the message history EstimateTokens sees. Zero when no registry is wired
// (oneshot / tests).
func toolSchemaTokens(reg *Registry) int {
	if reg == nil {
		return 0
	}
	return contextwindow.EstimateToolSchemas(reg.AsAdapterTools())
}

// firstUserIndex returns the index of the first user message (the task),
// or -1 when there is none.
func firstUserIndex(h []adapter.Message) int {
	for i := range h {
		if h[i].Role == adapter.RoleUser {
			return i
		}
	}
	return -1
}

// chooseCompactionTailStart returns the history index where the retained
// verbatim tail begins: the largest suffix after the task (firstUser)
// that fits budgetTokens, snapped forward to start on an assistant
// message so the tail never opens with a tool_result whose tool_use was
// summarized away.
func chooseCompactionTailStart(h []adapter.Message, firstUser, budgetTokens int) int {
	start := len(h)
	used := 0
	for i := len(h) - 1; i > firstUser; i-- {
		used += estimateMsgTokens(h[i])
		start = i
		if used >= budgetTokens {
			break
		}
	}
	if budgetTokens <= 0 {
		return len(h)
	}
	// Snap BACKWARD to the assistant message that owns the cut: the tail must
	// not open with a tool_result whose tool_use was summarized away, and a
	// backward snap keeps that whole round instead of discarding it (a
	// forward snap could leave an empty tail). It overshoots the budget by at
	// most one round, which capRetainedToolMessages then bounds.
	for i := min(start, len(h)-1); i > firstUser; i-- {
		if h[i].Role == adapter.RoleAssistant {
			return i
		}
		if h[i].Role == adapter.RoleUser && i > firstUser {
			// A user turn boundary: the cut is clean here (resolveLatestRequest
			// folds this message into the task so roles stay alternating).
			return i
		}
	}
	// No assistant/user boundary between the task and the cut: fall back to
	// the next assistant after it (possibly none, leaving an empty tail).
	for start < len(h) && h[start].Role != adapter.RoleAssistant {
		start++
	}
	return start
}

// maxLatestRequestChars bounds the verbatim user request folded into the
// task so a pasted log can't undo the compaction.
const maxLatestRequestChars = 8000

// resolveLatestRequest finds the user message that started the work the
// retained tail continues, when that message itself falls in the summarized
// middle (a long tool loop under one request) or sits exactly at the cut.
// Returns the (possibly advanced) tail start and the request text to keep
// verbatim. The request is folded into the task message rather than kept as
// its own turn: a standalone user message before the assistant-led tail
// would sit next to the task user message, and Gemini/Claude need strict
// alternation. Single-request loops (subagents) return "".
func resolveLatestRequest(h []adapter.Message, firstUser, tailStart int) (int, string) {
	// A cut exactly on a user turn: preserve that request verbatim on the
	// task, then begin the retained tail at its assistant reply.
	var latestRequest string
	if tailStart < len(h) && tailStart > firstUser && h[tailStart].Role == adapter.RoleUser {
		latestRequest = strings.TrimSpace(h[tailStart].Content)
		if len(latestRequest) > maxLatestRequestChars {
			latestRequest = latestRequest[:maxLatestRequestChars] + "…(truncated)"
		}
		tailStart++
	}
	for tailStart < len(h) && h[tailStart].Role == adapter.RoleUser && tailStart > firstUser {
		tailStart++
	}
	if latestRequest != "" {
		return tailStart, latestRequest
	}
	for i := min(tailStart, len(h)) - 1; i > firstUser; i-- {
		if h[i].Role != adapter.RoleUser {
			continue
		}
		req := strings.TrimSpace(h[i].Content)
		if len(req) > maxLatestRequestChars {
			req = req[:maxLatestRequestChars] + "…(truncated)"
		}
		return tailStart, req
	}
	return tailStart, ""
}

// assembleCompacted builds the post-compaction history: leading
// system(s), then the task with the progress summary folded into it,
// then the retained verbatim tail.
//
// The summary is folded into the task's user message rather than inserted
// as its own assistant turn. The retained tail is snapped to begin on an
// assistant message (so it never opens with an orphaned tool_result), so
// a standalone assistant summary would land directly before it —
// producing two consecutive assistant turns. Gemini rejects that outright
// (strict user/model alternation) and Claude 4.6+ treats a consecutive
// assistant pair as prefill; only OpenAI-style backends tolerate it.
// Folding keeps the sequence alternating (user task → assistant tail) for
// every provider while preserving the original task text verbatim.
func assembleCompacted(h []adapter.Message, firstUser, tailStart int, summary, latestRequest string) []adapter.Message {
	out := make([]adapter.Message, 0, firstUser+1+(len(h)-tailStart))
	out = append(out, h[:firstUser]...) // leading system(s)
	task := h[firstUser]                // value copy — does not mutate h
	base, _, _ := compaction.SplitNote(task.Content)
	task.Content = strings.TrimSpace(base) + "\n\n" + compactionProgressMarker + "\n\n" + summary
	if latestRequest != "" {
		task.Content += "\n\n" + compaction.LatestRequestOpen + "\n" + latestRequest + "\n" + compaction.LatestRequestClose
	}
	out = append(out, task)
	out = append(out, h[tailStart:]...)
	return out
}

// mergeAdjacentAssistant coalesces consecutive assistant messages into a
// single turn — joining their text and concatenating their tool calls.
// It's a defensive normalization run just before the provider call:
// providers reject or mishandle two assistant turns in a row (Gemini
// requires strict user/model alternation; Claude 4.6+ reads a consecutive
// assistant pair as prefill), and merging them into one valid turn
// (text followed by tool_use blocks) is what every provider expects.
//
// assembleCompacted already avoids the adjacency by folding the summary
// into the task, so in normal operation this is a no-op — it returns the
// input slice untouched (no allocation) when nothing is adjacent, and
// exists only so any other source of same-role adjacency can't reach the
// wire malformed.
func mergeAdjacentAssistant(msgs []adapter.Message) []adapter.Message {
	adjacent := false
	for i := 1; i < len(msgs); i++ {
		if msgs[i-1].Role == adapter.RoleAssistant && msgs[i].Role == adapter.RoleAssistant {
			adjacent = true
			break
		}
	}
	if !adjacent {
		return msgs
	}
	out := make([]adapter.Message, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == adapter.RoleAssistant && m.Role == adapter.RoleAssistant {
			prev := out[n-1]
			prev.Content = joinAssistantContent(prev.Content, m.Content)
			prev.ToolCalls = append(append([]adapter.ToolCall(nil), prev.ToolCalls...), m.ToolCalls...)
			prev.Images = append(append([]adapter.ImageBlock(nil), prev.Images...), m.Images...)
			out[n-1] = prev
			continue
		}
		out = append(out, m)
	}
	return out
}

// orphanedToolCallRepairedResult is the synthetic tool_result content
// repairOrphanedToolCalls injects. Deliberately distinct from
// interruptedToolResult ("interrupted by user"): by the time this
// repair runs, the call could have been orphaned by anything —
// including a hard kill (SIGKILL, a crashed process, or a hung
// operation the user couldn't interrupt cleanly) that gave no in-process
// code the chance to run isCancelErr's own repair before the process
// died and the corrupted history got persisted. Telling the model the
// outcome is unknown, rather than implying the user stopped it, is the
// honest description of what repairOrphanedToolCalls actually knows.
const orphanedToolCallRepairedResult = "no result was recorded for this tool call (history repaired before sending this request) — its outcome is unknown; do not assume it succeeded or failed"

// repairOrphanedToolCalls is streamIteration's second defensive
// normalization, run immediately after mergeAdjacentAssistant and for
// the same reason: some providers (the Codex/openai-auth backend is the
// strict one in practice) reject the ENTIRE request outright — "No tool
// output found for function call X" — if any assistant tool_use lacks a
// matching tool_result. executeToolCall(s) already keeps in-process
// aborts (cancellation, a genuine error) from producing this, but that
// repair only runs if the process is still alive to run it. A hard
// kill, a crash, or a session resumed from a file edited/corrupted out
// of band bypasses all of that — this is the last line of defense,
// re-checked on every single iteration for every provider, so a
// dangling tool_use from ANY cause is nonetheless always healed before
// it ever reaches the wire. Mirrors mergeAdjacentAssistant: returns the
// input slice untouched (no allocation) when nothing is orphaned, which
// is the common case.
func repairOrphanedToolCalls(msgs []adapter.Message) []adapter.Message {
	hasResult := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if m.Role == adapter.RoleTool {
			hasResult[m.ToolCallID] = true
		}
	}
	orphaned := false
outer:
	for _, m := range msgs {
		if m.Role != adapter.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if !hasResult[tc.ID] {
				orphaned = true
				break outer
			}
		}
	}
	if !orphaned {
		return msgs
	}
	out := make([]adapter.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m)
		if m.Role != adapter.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if !hasResult[tc.ID] {
				out = append(out, adapter.Message{
					Role:       adapter.RoleTool,
					Content:    orphanedToolCallRepairedResult,
					ToolCallID: tc.ID,
				})
			}
		}
	}
	return out
}

// joinAssistantContent concatenates two assistant text bodies, dropping
// the blank-line separator when either side is empty (a tool-call-only
// assistant message carries no text).
func joinAssistantContent(a, b string) string {
	switch {
	case strings.TrimSpace(a) == "":
		return b
	case strings.TrimSpace(b) == "":
		return a
	default:
		return a + "\n\n" + b
	}
}

// estimateMsgTokens is the same 4-chars-per-token heuristic
// contextwindow uses, inlined to avoid a per-call slice allocation when
// chooseCompactionTailStart walks a long history.
func estimateMsgTokens(m adapter.Message) int {
	return contextwindow.EstimateMessage(m)
}

// warnOnce emits a single "compaction could not reduce context" event per
// episode (reset once the request size drops back under the trigger), so a
// persistently-over-trigger loop doesn't warn on every iteration. Forced
// (provider-overflow) attempts always report.
func (cc *CompactionConfig) warnOnce(ctx context.Context, events chan<- Event, before int, cause error, force bool) error {
	if cc.warned.Swap(true) && !force {
		return nil
	}
	return send(ctx, events, ContextCompacted{Before: before, After: before, Err: cause, Forced: force})
}
