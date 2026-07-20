package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
)

// defaultCompactionTargetRatio is the share of the context window kept
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

// minCompactionWindow is the smallest context window for which in-loop
// compaction is worth enabling. Below it, compactionInputBudget (window
// − the per-turn output reservation − the prompt) goes non-positive, so
// there's no room to summarize anything — compaction can't make progress
// and would only burn a doomed summary call each iteration. Models this
// small are barely usable for agentic work anyway; they degrade to the
// provider's own context limit. 16384 keeps the input budget comfortably
// positive (~16384 − 8192 output − ~2200 overhead ≈ 6K of input room).
const minCompactionWindow = 16384

// compactionProgressMarker labels the folded progress summary, so the
// model (and a later reader of the transcript) knows the text appended to
// the task is a compaction artifact, not part of the original request.
const compactionProgressMarker = "[Earlier progress compacted to stay within the context window]"

// compactionPrompt asks the model to compress the dropped middle of an
// in-progress agent loop into a resumable progress note. Distinct from
// the TUI's session-summary prompt: this targets a working agent that
// must keep going, not a human reading a session recap.
const compactionPrompt = `You are compacting the working history of an in-progress agent task so it can continue with bounded context. Summarize what has happened so far so a capable agent could resume seamlessly without re-reading the dropped messages.

Capture, concisely:
- The task/goal as currently understood.
- Key findings, facts, file paths, and tool results discovered so far.
- Decisions made and approaches already ruled out.
- What remains to be done and the immediate next step.

Be terse and concrete. Do not invent. Omit pleasantries and restatements of these instructions.`

var (
	errEmptyCompactionBody       = errors.New("compaction: nothing to summarize")
	errEmptyCompactionSummary    = errors.New("compaction: model returned empty summary")
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
	if cc == nil || cc.Window <= 0 || (!force && cc.Threshold <= 0) {
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
	threshold := cc.Threshold * float64(cc.Window)
	if !force && float64(before+overhead) < threshold {
		return false, nil
	}
	firstUser := firstUserIndex(h)
	if firstUser < 0 {
		return false, nil // no task anchor — nothing sensible to compact
	}
	tailStart := chooseCompactionTailStart(h, firstUser, int(compactionTargetRatio(cc)*float64(cc.Window)))
	capped, cappedChanged := capRetainedToolMessages(h, tailStart)
	if cappedChanged {
		h = capped
	}
	middle := h[firstUser+1 : tailStart]
	if len(middle) == 0 {
		if force && cappedChanged {
			path, snapErr := preCompactSnapshot(cc, original)
			setHistory(cfg, history, h)
			after := contextwindow.EstimateTokens(h)
			return true, send(ctx, events, ContextCompacted{Before: before, After: after, SnapshotPath: path, SnapshotErr: snapErr, Forced: true})
		}
		// The anchors + retained tail already span everything; there's
		// no older middle to summarize. Can't make progress this round —
		// leave history alone and let the turn proceed.
		return false, nil
	}
	// Convergence guard: if dropping the entire middle still wouldn't get
	// the request under the threshold, the irreducible floor (system +
	// task anchor + the retained tail + tool schemas) already exceeds it —
	// e.g. a single tool result larger than the retain budget. Summarizing
	// can't win, so skip rather than spend a summary call every iteration
	// that never gets under the line. Force skips this because the provider
	// has already proven the current request is too large, and capping the
	// retained tail may have lowered the floor.
	overage := float64(before+overhead) - threshold
	if !force && float64(contextwindow.EstimateTokens(middle)) <= overage {
		return false, nil
	}

	summarizer := cc.Summarizer
	if summarizer == nil {
		summarizer = cfg.Adapter
	}
	if summarizer == nil {
		// No summarizer wired (neither an injected Summarizer nor the
		// loop's own Adapter). Can't compact; soft-skip so the turn still
		// proceeds rather than nil-derefing on the ChatStream call.
		_ = send(ctx, events, ContextCompacted{Before: before, After: before, Err: errEmptyCompactionSummarizer, Forced: force})
		return false, nil
	}
	// Budget the summary INPUT against the SMALLER of the loop window and
	// the summarizer's own window: under cache-safe routing the summary
	// runs on a cheaper model whose window may be smaller than the child's,
	// so a middle sized to the child window would overflow the summarizer.
	budgetWindow := cc.Window
	if cc.SummarizerWindow > 0 && cc.SummarizerWindow < budgetWindow {
		budgetWindow = cc.SummarizerWindow
	}
	summary, err := summarizeForCompaction(ctx, summarizer, middle, budgetWindow)
	if err != nil {
		// Best-effort: keep history, record the attempt. The turn
		// continues and may hit the provider's own context limit, which
		// is the pre-existing (pre-compaction) behavior.
		_ = send(ctx, events, ContextCompacted{Before: before, After: before, Err: err, Forced: force})
		return false, nil
	}

	out := assembleCompacted(h, firstUser, tailStart, summary)
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
	maxRetainedToolTokens        = 4096
	retainedToolCompactionMarker = "…(truncated by compaction)"
)

func capRetainedToolMessages(h []adapter.Message, tailStart int) ([]adapter.Message, bool) {
	if tailStart < 0 {
		tailStart = 0
	}
	if tailStart > len(h) {
		tailStart = len(h)
	}
	var out []adapter.Message
	changed := false
	for i := tailStart; i < len(h); i++ {
		m := h[i]
		if m.Role != adapter.RoleTool || estimateMsgTokens(m) <= maxRetainedToolTokens {
			continue
		}
		if out == nil {
			out = append([]adapter.Message(nil), h...)
		}
		maxChars := maxRetainedToolTokens * 4
		if maxChars <= len(retainedToolCompactionMarker) {
			m.Content = retainedToolCompactionMarker
		} else {
			m.Content = m.Content[:maxChars-len(retainedToolCompactionMarker)] + retainedToolCompactionMarker
		}
		out[i] = m
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
	// Snap forward to the next assistant message to keep tool pairing
	// intact. If none remains, start lands at len(h) (empty tail).
	for start < len(h) && h[start].Role != adapter.RoleAssistant {
		start++
	}
	return start
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
func assembleCompacted(h []adapter.Message, firstUser, tailStart int, summary string) []adapter.Message {
	out := make([]adapter.Message, 0, firstUser+1+(len(h)-tailStart))
	out = append(out, h[:firstUser]...) // leading system(s)
	task := h[firstUser]                // value copy — does not mutate h
	task.Content = strings.TrimSpace(task.Content) + "\n\n" + compactionProgressMarker + "\n\n" + summary
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
	chars := len(m.Content)
	for _, tc := range m.ToolCalls {
		chars += len(tc.Name) + len(tc.ArgsJSON)
	}
	return (chars + 3) / 4
}

// summarizeForCompaction renders the dropped middle to a text body
// (budgeted to fit the window so the summary call itself doesn't
// overflow) and runs a single summary completion, returning the model's
// text.
func summarizeForCompaction(ctx context.Context, s Streamer, middle []adapter.Message, window int) (string, error) {
	body := renderForCompaction(middle, window)
	if strings.TrimSpace(body) == "" {
		return "", errEmptyCompactionBody
	}
	msgs := []adapter.Message{
		{Role: adapter.RoleSystem, Content: compactionPrompt},
		{Role: adapter.RoleUser, Content: body},
	}
	out := s.ChatStream(ctx, msgs, nil)
	var b strings.Builder
	for ev := range out {
		switch ev.Kind {
		case adapter.EventTokenDelta:
			b.WriteString(ev.Token)
		case adapter.EventErr:
			return "", ev.Err
		case adapter.EventDone:
			if b.Len() == 0 && ev.Final != nil {
				b.WriteString(ev.Final.Content)
			}
		}
	}
	summary := strings.TrimSpace(b.String())
	if summary == "" {
		return "", errEmptyCompactionSummary
	}
	return summary, nil
}

// renderForCompaction flattens the middle messages into one text body,
// keeping the most recent segments that fit a budget derived from the
// window (leaving room for the compaction prompt and the reserved
// output). Oldest segments are dropped first with a marker — in normal
// operation the middle is well under the window (compaction fires at the
// threshold and retains a fraction as the tail), so this budget is a
// safety net for the case where one iteration appended a very large tool
// result.
func renderForCompaction(middle []adapter.Message, window int) string {
	segs := make([]string, 0, len(middle))
	for _, m := range middle {
		switch m.Role {
		case adapter.RoleUser:
			segs = append(segs, "USER: "+strings.TrimSpace(m.Content)+"\n\n")
		case adapter.RoleAssistant:
			var sb strings.Builder
			if c := strings.TrimSpace(m.Content); c != "" {
				sb.WriteString("ASSISTANT: ")
				sb.WriteString(c)
				sb.WriteString("\n\n")
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&sb, "ASSISTANT used tool %s\n", tc.Name)
			}
			if sb.Len() > 0 {
				segs = append(segs, sb.String())
			}
		case adapter.RoleTool:
			out := strings.TrimSpace(m.Content)
			if len(out) > 600 {
				out = out[:600] + "…(truncated)"
			}
			segs = append(segs, "TOOL_RESULT: "+out+"\n\n")
		}
	}

	budget := compactionInputBudget(window)
	if budget <= 0 {
		// The window is too small for the standard output reservation
		// (compactionInputBudget went non-positive — e.g. an ~8K-window
		// summarizer). Returning the full middle here would send an
		// over-window request and the summary call would be rejected,
		// defeating compaction. Fall back to a conservative fraction of
		// the window so the input still has a chance of fitting; a tiny
		// window then degrades to a lossy summary rather than a guaranteed
		// failure.
		budget = window / 2
	}
	if budget <= 0 {
		return strings.TrimSpace(strings.Join(segs, ""))
	}
	used := 0
	keepFrom := len(segs)
	for i := len(segs) - 1; i >= 0; i-- {
		cost := (len(segs[i]) + 3) / 4
		if used+cost > budget && keepFrom < len(segs) {
			break
		}
		used += cost
		keepFrom = i
	}
	if keepFrom == 0 {
		return strings.TrimSpace(strings.Join(segs, ""))
	}
	var b strings.Builder
	b.WriteString("[earlier middle omitted due to compaction budget]\n\n")
	for i := keepFrom; i < len(segs); i++ {
		b.WriteString(segs[i])
	}
	return strings.TrimSpace(b.String())
}

// compactionInputBudget is the token budget for the flattened middle,
// leaving headroom for the compaction prompt and the reserved per-turn
// output (matched to the chat adapter's default).
func compactionInputBudget(window int) int {
	overhead := (len(compactionPrompt) + 3) / 4
	return window - overhead - int(adapter.ChatDefaultMaxTokens) - 2048
}
