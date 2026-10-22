package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/compaction"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// captureStreamer records the last user-message body it was asked to
// stream, so a test can assert how the summary INPUT was budgeted. It
// always replies with a fixed short summary.
type captureStreamer struct {
	mu           syncutil.Mutex
	lastUserBody string
	reply        string
}

func (c *captureStreamer) ChatStream(_ context.Context, msgs []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	c.mu.Lock()
	for _, m := range msgs {
		if m.Role == adapter.RoleUser {
			c.lastUserBody = m.Content
		}
	}
	reply := c.reply
	c.mu.Unlock()
	out := make(chan adapter.StreamEvent, 4)
	out <- adapter.StreamEvent{Kind: adapter.EventTokenDelta, Token: reply}
	out <- adapter.StreamEvent{Kind: adapter.EventDone, Final: &adapter.Message{}}
	close(out)
	return out
}

// subagentHistory builds a [system, task, (assistant+tool)*n] history
// shaped like a real subagent loop: one user message (the task) followed
// by many tool-call / tool-result rounds. toolSize controls how big each
// tool result is so the test can push the history over a window.
func subagentHistory(rounds, toolSize int) []adapter.Message {
	h := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "You are an auditing subagent."},
		{Role: adapter.RoleUser, Content: "Audit the codebase for bugs."},
	}
	for i := range rounds {
		id := fmt.Sprintf("call-%d", i)
		h = append(h,
			adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
				{ID: id, Name: "read_file", ArgsJSON: fmt.Sprintf(`{"path":"f%d.go"}`, i)},
			}},
			adapter.Message{Role: adapter.RoleTool, ToolCallID: id, Content: strings.Repeat("x", toolSize)},
		)
	}
	return h
}

func drainEvents(ch chan Event) []Event {
	var out []Event
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

// TestCompactionTriggerTokens_AbsoluteMarginCapsLargeWindows pins the
// defense-in-depth safety net: a percentage-based trigger alone assumes the
// resolved window is trustworthy, which isn't always true (some backends
// enforce well under their advertised limit). For a large enough window,
// the absolute margin — not the fraction — must be the binding constraint,
// so a "safe" percentage of an over-estimated window still leaves a real,
// bounded amount of headroom.
func TestCompactionTriggerTokens_AbsoluteMarginCapsLargeWindows(t *testing.T) {
	// threshold*window (0.90 * 1,000,000 = 900,000) would leave only
	// 100,000 tokens of headroom; the margin (window-50,000 = 950,000) is
	// larger, so the FRACTION should still be the tighter, binding limit
	// here — showing the margin doesn't kick in needlessly when the
	// fraction is already conservative enough.
	if got := CompactionTriggerTokens(0.90, 1_000_000); got != 900_000 {
		t.Errorf("got %d, want 900000 (fraction should bind)", got)
	}
	// Now push the fraction close to 1.0 — the fraction alone would leave
	// almost no headroom (990,000 of 1,000,000). The absolute margin must
	// cap it well below that.
	got := CompactionTriggerTokens(0.99, 1_000_000)
	if got != 950_000 {
		t.Errorf("got %d, want 950000 (compactionAbsoluteMargin should bind)", got)
	}
}

// TestCompactionTriggerTokens_NeverBelowHalfWindow ensures the absolute
// margin can't squeeze a small or already-conservative window into firing
// almost immediately — it's a backstop against an over-estimated window,
// not a way to make compaction more aggressive for a window that's already
// modest in absolute terms.
func TestCompactionTriggerTokens_NeverBelowHalfWindow(t *testing.T) {
	// window=60,000: margin alone (60,000-50,000=10,000) would be far
	// below half the window (30,000) — the half-window floor must win.
	if got := CompactionTriggerTokens(0.99, 60_000); got != 30_000 {
		t.Errorf("got %d, want 30000 (half-window floor should bind)", got)
	}
}

func TestCompactionTriggerTokens_ZeroWindow(t *testing.T) {
	if got := CompactionTriggerTokens(0.7, 0); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestMaybeCompact_ShrinksAndPreservesAnchorsAndPairing(t *testing.T) {
	history := subagentHistory(8, 800) // ~1.6K tokens of tool output
	cfg := LoopConfig{
		Compaction: &CompactionConfig{
			Window:    2000,
			Threshold: 0.5, // trigger at ~1000 tokens
			Summarizer: &scriptedStreamer{turns: [][]adapter.StreamEvent{{
				{Kind: adapter.EventTokenDelta, Token: "Read several files; found a nil-deref in x.go. Next: check y.go."},
				{Kind: adapter.EventDone, Final: &adapter.Message{}},
			}}},
		},
	}
	before := contextwindow.EstimateTokens(history)
	h := history
	events := make(chan Event, 8)

	if err := maybeCompact(context.Background(), cfg, &h, events); err != nil {
		t.Fatalf("maybeCompact: %v", err)
	}

	if contextwindow.EstimateTokens(h) >= before {
		t.Fatalf("history did not shrink: before=%d after=%d", before, contextwindow.EstimateTokens(h))
	}
	// Anchors preserved: system stays, original task text stays verbatim
	// at the head of the (now summary-folded) user message.
	if h[0].Role != adapter.RoleSystem {
		t.Errorf("h[0] role = %v, want system", h[0].Role)
	}
	if h[1].Role != adapter.RoleUser || !strings.HasPrefix(h[1].Content, "Audit the codebase for bugs.") {
		t.Errorf("task text not preserved at head of h[1]: %+v", h[1])
	}
	// Summary folded into the task turn (not a separate assistant message,
	// which would create a consecutive-assistant pair before the tail).
	if !strings.Contains(h[1].Content, compactionProgressMarker) {
		t.Errorf("compaction marker missing from folded task: %q", h[1].Content)
	}
	// Tool pairing intact: the retained tail begins right after the task
	// and must start on an assistant message, never an orphaned
	// tool_result.
	if len(h) > 2 && h[2].Role != adapter.RoleAssistant {
		t.Errorf("tail starts on %v, want assistant (orphaned tool_result risk)", h[2].Role)
	}
	// The whole point: no two assistant turns in a row anywhere (Gemini
	// rejects it; Claude 4.6+ treats it as prefill).
	assertNoConsecutiveAssistant(t, h)

	evs := drainEvents(events)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	cc, ok := evs[0].(ContextCompacted)
	if !ok {
		t.Fatalf("event type = %T, want ContextCompacted", evs[0])
	}
	if cc.Err != nil || cc.After >= cc.Before {
		t.Errorf("ContextCompacted = %+v, want Err=nil and After<Before", cc)
	}
}

func TestMaybeCompact_NoOpWhenDisabledOrUnderThreshold(t *testing.T) {
	history := subagentHistory(8, 800)

	// Compaction nil → untouched.
	h := history
	if err := maybeCompact(context.Background(), LoopConfig{}, &h, make(chan Event, 1)); err != nil {
		t.Fatalf("maybeCompact: %v", err)
	}
	if len(h) != len(history) {
		t.Errorf("nil compaction mutated history: %d != %d", len(h), len(history))
	}

	// Under threshold → untouched (huge window).
	h2 := history
	cfg := LoopConfig{Compaction: &CompactionConfig{Window: 1_000_000, Threshold: 0.85}}
	if err := maybeCompact(context.Background(), cfg, &h2, make(chan Event, 1)); err != nil {
		t.Fatalf("maybeCompact: %v", err)
	}
	if len(h2) != len(history) {
		t.Errorf("under-threshold compaction mutated history: %d != %d", len(h2), len(history))
	}
}

// TestMaybeCompact_ThresholdAtOrAboveOneIsFullyDisabled pins the fix for a
// regression the absolute-margin trigger (CompactionTriggerTokens)
// introduced: Threshold >= 1.0 is meant to mean "disabled" (deriveCompactionThreshold
// / DeriveCompactionThreshold return exactly 1.0 for that), but
// CompactionTriggerTokens(1.0, window) computes window-compactionAbsoluteMargin,
// not "never" — a real, finite, active trigger. Without the explicit >= 1.0
// guard in compact(), a session with auto_threshold=1.0 (an explicit opt-out
// of both auto-summarize and this derived safety net) would still get
// preemptively compacted anyway. History that already exceeds even that
// absolute-margin-derived point must still stay untouched with Threshold=1.0.
func TestMaybeCompact_ThresholdAtOrAboveOneIsFullyDisabled(t *testing.T) {
	history := subagentHistory(8, 800)
	window := 2000 // small enough that history's tokens exceed window-compactionAbsoluteMargin trivially
	h := history
	cfg := LoopConfig{Compaction: &CompactionConfig{Window: window, Threshold: 1.0}}
	if err := maybeCompact(context.Background(), cfg, &h, make(chan Event, 1)); err != nil {
		t.Fatalf("maybeCompact: %v", err)
	}
	if len(h) != len(history) {
		t.Errorf("Threshold=1.0 should fully disable preemptive compaction, but history changed: %d != %d", len(h), len(history))
	}

	// force=true (provider-overflow recovery) must still bypass this and be
	// able to compact even with Threshold=1.0.
	h2 := history
	forceCfg := LoopConfig{Compaction: &CompactionConfig{
		Window: window, Threshold: 1.0,
		Summarizer: &scriptedStreamer{turns: [][]adapter.StreamEvent{{
			{Kind: adapter.EventTokenDelta, Token: "summary"},
			{Kind: adapter.EventDone, Final: &adapter.Message{}},
		}}},
	}}
	if _, err := compact(context.Background(), forceCfg, &h2, make(chan Event, 1), true); err != nil {
		t.Fatalf("compact(force=true): %v", err)
	}
	if len(h2) == len(history) {
		t.Error("force=true should still compact despite Threshold=1.0")
	}
}

func TestMaybeCompact_SkipsWhenAnchorsExceedBudget(t *testing.T) {
	// A task anchor larger than threshold*window plus a tail that fills the
	// retain budget: dropping the (non-empty) middle still can't get the
	// request under the line. Compaction must skip rather than burn a
	// summary call every iteration that never converges. The scripted
	// summarizer returns a valid summary, so if compaction wrongly proceeds
	// the history WOULD change and this test catches it.
	h := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: strings.Repeat("x", 5200)}, // ~1300 tokens > 0.5*2000
	}
	for range 6 { // ~1050 tokens of tail+middle so the middle is non-empty
		h = append(h,
			adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{Name: "read_file", ArgsJSON: "{}"}}},
			adapter.Message{Role: adapter.RoleTool, Content: strings.Repeat("y", 700)},
		)
	}
	cfg := LoopConfig{Compaction: &CompactionConfig{
		Window:    2000,
		Threshold: 0.5,
		Summarizer: &scriptedStreamer{turns: [][]adapter.StreamEvent{{
			{Kind: adapter.EventTokenDelta, Token: "compacted summary that should never be applied"},
			{Kind: adapter.EventDone, Final: &adapter.Message{}},
		}}},
	}}
	want := len(h)
	hh := h
	events := make(chan Event, 4)
	if err := maybeCompact(context.Background(), cfg, &hh, events); err != nil {
		t.Fatalf("maybeCompact: %v", err)
	}
	if len(hh) != want {
		t.Errorf("expected no compaction (anchors+tail exceed budget), history changed %d → %d", want, len(hh))
	}
	evs := drainEvents(events)
	if len(evs) != 1 {
		t.Fatalf("expected exactly one warning event when skipping, got %d", len(evs))
	}
	if ce, ok := evs[0].(ContextCompacted); !ok || !errors.Is(ce.Err, errCompactionCannotConverge) {
		t.Errorf("expected cannot-converge warning, got %#v", evs[0])
	}
	// The warning is deduped until the request drops back under the trigger.
	if err := maybeCompact(context.Background(), cfg, &hh, events); err != nil {
		t.Fatalf("second maybeCompact: %v", err)
	}
	if evs := drainEvents(events); len(evs) != 0 {
		t.Errorf("repeat warning not deduped: %d events", len(evs))
	}
}

func TestMaybeCompact_SummaryFailureIsSoftAndLeavesHistory(t *testing.T) {
	history := subagentHistory(8, 800)
	h := history
	cfg := LoopConfig{
		Compaction: &CompactionConfig{
			Window:    2000,
			Threshold: 0.5,
			Summarizer: &scriptedStreamer{turns: [][]adapter.StreamEvent{{
				{Kind: adapter.EventErr, Err: errors.New("provider exploded")},
			}}},
		},
	}
	events := make(chan Event, 4)
	if err := maybeCompact(context.Background(), cfg, &h, events); err != nil {
		t.Fatalf("maybeCompact should soft-fail, got err: %v", err)
	}
	if len(h) != len(history) {
		t.Errorf("failed summary should leave history untouched, got %d != %d", len(h), len(history))
	}
	evs := drainEvents(events)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if cc := evs[0].(ContextCompacted); cc.Err == nil {
		t.Errorf("expected ContextCompacted.Err set on summary failure")
	}
}

func TestNewChildCompaction(t *testing.T) {
	s := &scriptedStreamer{}
	// Below the floor → nil (compaction can't help; budget would be
	// non-positive).
	if got := newChildCompaction(minCompactionWindow-1, s, 0); got != nil {
		t.Errorf("window below floor should disable compaction, got %+v", got)
	}
	// At/above the floor → config with the passthrough fields set.
	got := newChildCompaction(200_000, s, 32_000)
	if got == nil {
		t.Fatal("expected a CompactionConfig at/above the floor")
	}
	if got.Window != 200_000 || got.SummarizerWindow != 32_000 ||
		got.Threshold != subagentCompactionThreshold || got.TargetRatio != defaultCompactionTargetRatio || got.Summarizer != s {
		t.Errorf("unexpected config: %+v", got)
	}
	// nil summarizer is preserved (maybeCompact falls back to the loop's
	// own adapter at call time).
	if g := newChildCompaction(200_000, nil, 0); g == nil || g.Summarizer != nil {
		t.Errorf("nil summarizer should be preserved, got %+v", g)
	}
}

// TestMaybeCompact_SummarizerWindowClampsInput proves the summary INPUT is
// budgeted against the SMALLER of the loop window and the summarizer's own
// window — the cache-safe-routing case where the summary runs on a cheaper
// model whose window is smaller than the child's. Without the clamp a
// middle sized to the (large) child window would overflow the summarizer.
func TestMaybeCompact_SummarizerWindowClampsInput(t *testing.T) {
	history := subagentHistory(120, 2000) // ~61K raw tokens → over a 40K*0.75 trigger, with a large middle
	const marker = "[earlier turns omitted due to compaction budget]"
	run := func(summarizerWindow int) string {
		cap := &captureStreamer{reply: "ok summary"}
		cfg := LoopConfig{Compaction: &CompactionConfig{
			Window:           40_000,
			Threshold:        0.75,
			Summarizer:       cap,
			SummarizerWindow: summarizerWindow,
		}}
		h := append([]adapter.Message(nil), history...)
		if err := maybeCompact(context.Background(), cfg, &h, make(chan Event, 4)); err != nil {
			t.Fatalf("maybeCompact: %v", err)
		}
		return cap.lastUserBody
	}
	// 20K summarizer window → input budget (~9.6K tokens) is smaller than
	// the summarizable middle (~21K), so the input must be trimmed.
	clamped := run(20_000)
	// No clamp (0) → budget against the 40K child window (~29.6K tokens) →
	// the middle fits without trimming.
	unclamped := run(0)
	if !strings.Contains(clamped, marker) {
		t.Errorf("small summarizer window should trim the summary input (expected omit marker)")
	}
	if strings.Contains(unclamped, marker) {
		t.Errorf("no clamp: the middle should fit the child window without trimming")
	}
	if len(clamped) >= len(unclamped) {
		t.Errorf("clamped input (%d bytes) should be smaller than unclamped (%d bytes)", len(clamped), len(unclamped))
	}
}

// TestRenderForCompaction_TinyWindowTrimsInsteadOfReturningAll guards the
// hardened negative-budget branch: for a window so small that
// compactionInputBudget goes non-positive (~8K), the old code returned the
// ENTIRE middle — an over-window request the summary call would reject.
// The fix trims to ~window/2 instead.

// TestMaybeCompact_NilSummarizerSoftSkips proves the defensive guard: when
// neither an injected Summarizer nor the loop's own Adapter is set, the
// summary call would nil-deref — instead we soft-skip and record the
// attempt rather than panicking.
func TestMaybeCompact_NilSummarizerSoftSkips(t *testing.T) {
	history := subagentHistory(8, 800)
	h := history
	cfg := LoopConfig{Compaction: &CompactionConfig{Window: 2000, Threshold: 0.5}} // no Adapter, no Summarizer
	events := make(chan Event, 4)
	if err := maybeCompact(context.Background(), cfg, &h, events); err != nil {
		t.Fatalf("nil summarizer should soft-skip, got err: %v", err)
	}
	if len(h) != len(history) {
		t.Errorf("nil summarizer should leave history untouched, got %d != %d", len(h), len(history))
	}
	evs := drainEvents(events)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if cc := evs[0].(ContextCompacted); cc.Err == nil {
		t.Error("expected ContextCompacted.Err set when no summarizer is available")
	}
}

// assertNoConsecutiveAssistant fails the test if any two adjacent
// messages are both assistant turns — the provider-invalid shape
// compaction must never emit.
func assertNoConsecutiveAssistant(t *testing.T, h []adapter.Message) {
	t.Helper()
	for i := 1; i < len(h); i++ {
		if h[i-1].Role == adapter.RoleAssistant && h[i].Role == adapter.RoleAssistant {
			t.Errorf("consecutive assistant messages at %d/%d — provider-invalid", i-1, i)
		}
	}
}

func TestMergeAdjacentAssistant(t *testing.T) {
	// A standalone progress note (text only) immediately before an
	// assistant tool-call turn — the exact shape compaction used to emit.
	in := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "task"},
		{Role: adapter.RoleAssistant, Content: "progress note"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{Name: "read_file", ArgsJSON: "{}"}}},
		{Role: adapter.RoleTool, Content: "result"},
	}
	out := mergeAdjacentAssistant(in)
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4 (the two assistant turns merge into one)", len(out))
	}
	if out[2].Role != adapter.RoleAssistant ||
		!strings.Contains(out[2].Content, "progress note") ||
		len(out[2].ToolCalls) != 1 {
		t.Errorf("merged assistant turn wrong: %+v", out[2])
	}
	assertNoConsecutiveAssistant(t, out)

	// Already-alternating history passes straight through (same backing
	// slice, no allocation).
	clean := []adapter.Message{
		{Role: adapter.RoleUser, Content: "u"},
		{Role: adapter.RoleAssistant, Content: "a"},
		{Role: adapter.RoleTool, Content: "t"},
	}
	if got := mergeAdjacentAssistant(clean); len(got) != len(clean) {
		t.Errorf("alternating history should pass through unchanged, got len %d", len(got))
	}
}

// TestRepairOrphanedToolCalls guards streamIteration's last line of
// defense against the "No tool output found for function call X" class
// of 400 (the Codex/openai-auth backend's strict pairing check): a
// dangling tool_use surviving into history from ANY cause — not just an
// in-process abort executeToolCall(s) already handles, but a hard kill,
// a crash, or a session resumed from a file that went stale/corrupted
// out of band — must still never reach the wire unpaired.
func TestRepairOrphanedToolCalls(t *testing.T) {
	// Fully dangling: the tool_use has no tool_result anywhere.
	in := []adapter.Message{
		{Role: adapter.RoleUser, Content: "task"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: "orphan-1", Name: "read_file", ArgsJSON: "{}"}}},
	}
	out := repairOrphanedToolCalls(in)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3 (user, assistant, synthesized tool result)", len(out))
	}
	if out[2].Role != adapter.RoleTool || out[2].ToolCallID != "orphan-1" {
		t.Fatalf("out[2] should be the synthesized result for orphan-1; got %+v", out[2])
	}
	if out[2].Content != orphanedToolCallRepairedResult {
		t.Errorf("synthesized content = %q, want the repair marker", out[2].Content)
	}

	// Partially paired: one of two tool_calls in the same assistant
	// message already has a real result — only the missing one gets
	// synthesized, the real one is untouched.
	partial := []adapter.Message{
		{Role: adapter.RoleUser, Content: "task"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
			{ID: "has-result", Name: "read_file", ArgsJSON: "{}"},
			{ID: "dangling", Name: "run_bash", ArgsJSON: "{}"},
		}},
		{Role: adapter.RoleTool, ToolCallID: "has-result", Content: "real content"},
	}
	out = repairOrphanedToolCalls(partial)
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4 (user, assistant, real result, synthesized result)", len(out))
	}
	// Order between the real and synthesized results is not asserted —
	// providers only require both to appear somewhere between the
	// assistant message and the next turn, not in a specific sequence.
	byID := map[string]string{}
	for _, m := range out[2:] {
		if m.Role != adapter.RoleTool {
			t.Fatalf("expected only tool results after the assistant message; got %+v", m)
		}
		byID[m.ToolCallID] = m.Content
	}
	if byID["has-result"] != "real content" {
		t.Errorf("the real tool_result must survive untouched; got %q", byID["has-result"])
	}
	if byID["dangling"] != orphanedToolCallRepairedResult {
		t.Errorf("the dangling tool_call must get a synthesized result; got %q", byID["dangling"])
	}

	// Already fully paired history passes straight through unchanged
	// (same backing slice, no allocation) — the common case.
	clean := []adapter.Message{
		{Role: adapter.RoleUser, Content: "u"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: "1", Name: "read_file"}}},
		{Role: adapter.RoleTool, ToolCallID: "1", Content: "t"},
	}
	if got := repairOrphanedToolCalls(clean); len(got) != len(clean) {
		t.Errorf("fully paired history should pass through unchanged, got len %d", len(got))
	}
}

func TestChooseCompactionTailStart_SnapsToAssistant(t *testing.T) {
	h := subagentHistory(6, 100)
	// Generous budget so the raw cut would land mid-pair on a tool
	// result; the snap must move it forward to an assistant message.
	start := chooseCompactionTailStart(h, 1, 1<<30)
	if start < len(h) && h[start].Role != adapter.RoleAssistant {
		t.Errorf("tail start role = %v, want assistant", h[start].Role)
	}
	// A budget of 0 keeps no tail (start at end), still assistant-snapped
	// (no-op past the end).
	if got := chooseCompactionTailStart(h, 1, 0); got != len(h) {
		t.Errorf("zero budget tail start = %d, want %d", got, len(h))
	}
}

// TestCapRetainedToolMessages_ManyOversizedFitBudget locks in the
// sum-bounding contract: capping each oversize tool message
// independently to maxRetainedToolTokens bounds each one but not their
// SUM — 30 messages each just above the ceiling could total ~3x an
// intended retain budget. capRetainedToolMessages must bound the total.
func TestCapRetainedToolMessages_ManyOversizedFitBudget(t *testing.T) {
	const n = 30
	h := subagentHistory(n, 20_000) // ~5000 tokens/round, above maxRetainedToolTokens (4096)
	tailStart := 2                  // right after the system+user header — cap the whole tail

	budget := 20_000
	out, changed := capRetainedToolMessages(h, tailStart, budget)
	if !changed {
		t.Fatal("expected capping to report a change")
	}
	if len(out) != len(h) {
		t.Fatalf("message count changed: got %d, want %d", len(out), len(h))
	}
	toolCount, toolTokens := 0, 0
	for _, m := range out[tailStart:] {
		if m.Role != adapter.RoleTool {
			continue
		}
		toolCount++
		toolTokens += estimateMsgTokens(m)
	}
	if toolCount != n {
		t.Fatalf("expected all %d tool messages to survive (none dropped), got %d", n, toolCount)
	}
	if toolTokens > budget {
		t.Fatalf("retained tool tokens %d exceed budget %d", toolTokens, budget)
	}
}

func TestMaybeCompact_IterativeNoteReplacesAndRecordsFiles(t *testing.T) {
	cs := &captureStreamer{reply: "UPDATED NOTE"}
	h := subagentHistory(40, 4000)
	h[1].Content = "Audit the codebase.\n\n" + compactionProgressMarker + "\n\nOLD NOTE\n\n<files>\n(Write) old.go\n</files>"
	cfg := LoopConfig{Compaction: &CompactionConfig{Window: 20_000, Threshold: 0.5, TargetRatio: 0.35, Summarizer: cs}}
	events := make(chan Event, 8)
	changed, err := compact(context.Background(), cfg, &h, events, false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(cs.lastUserBody, "<previous-summary>") || !strings.Contains(cs.lastUserBody, "OLD NOTE") {
		t.Errorf("previous summary not passed to summarizer: %.200q", cs.lastUserBody)
	}
	task := h[1].Content
	if strings.Count(task, compactionProgressMarker) != 1 {
		t.Errorf("notes stacked: %q", task)
	}
	if strings.Contains(task, "OLD NOTE") || !strings.Contains(task, "UPDATED NOTE") {
		t.Errorf("note not replaced: %q", task)
	}
	if !strings.Contains(task, "(Write) old.go") || !strings.Contains(task, "(Read) f0.go") {
		t.Errorf("file list not merged: %q", task)
	}
}

func TestMaybeCompact_PruneAloneSkipsSummary(t *testing.T) {
	cs := &captureStreamer{reply: "SHOULD NOT BE CALLED"}
	h := []adapter.Message{{Role: adapter.RoleSystem, Content: "sys"}, {Role: adapter.RoleUser, Content: "task"}}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("c%d", i)
		h = append(h,
			adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: id, Name: "run_bash", ArgsJSON: `{}`}}},
			adapter.Message{Role: adapter.RoleTool, ToolCallID: id, Content: strings.Repeat("x", 40_000)})
	}
	cfg := LoopConfig{Compaction: &CompactionConfig{Window: 200_000, Threshold: 0.5, TargetRatio: 0.35, Summarizer: cs}}
	events := make(chan Event, 8)
	changed, err := compact(context.Background(), cfg, &h, events, false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if cs.lastUserBody != "" {
		t.Errorf("summarizer was called despite prune sufficing")
	}
	got := drainEvents(events)
	if len(got) != 1 || !got[0].(ContextCompacted).Pruned {
		t.Errorf("expected one Pruned event, got %#v", got)
	}
}

func TestTokenCalibrator(t *testing.T) {
	var c tokenCalibrator
	if c.get() != 1 {
		t.Fatalf("unset ratio must be 1")
	}
	c.observe("m", 100, 500) // below min estimate: ignored
	if c.get() != 1 {
		t.Fatalf("tiny prompt must be ignored")
	}
	c.observe("m", 10_000, 15_000)
	if got := c.get(); got != 1.5 {
		t.Fatalf("first observation = %v, want 1.5", got)
	}
	c.observe("m", 10_000, 10_000) // EMA toward 1.0
	if got := c.get(); got != 1.25 {
		t.Fatalf("smoothed = %v, want 1.25", got)
	}
	c.observe("m", 10_000, 90_000) // clamped to 2.0
	if got := c.get(); got != 1.625 {
		t.Fatalf("clamped = %v, want 1.625", got)
	}
	var nilc *tokenCalibrator
	nilc.observe("m", 10_000, 20_000)
	if nilc.get() != 1 || nilc.scale(10) != 10 {
		t.Fatalf("nil calibrator must be identity")
	}
}

func TestReportedPromptTokens_SumsCacheParts(t *testing.T) {
	u := &adapter.Usage{InputTokens: 10, CacheReadTokens: 200, CacheCreationTokens: 30}
	if got := reportedPromptTokens(u); got != 240 {
		t.Fatalf("got %d", got)
	}
	if reportedPromptTokens(nil) != 0 {
		t.Fatal("nil usage")
	}
}

func TestMaybeCompact_CalibrationMakesTriggerFireEarlier(t *testing.T) {
	build := func() (LoopConfig, []adapter.Message) {
		cfg := LoopConfig{Compaction: &CompactionConfig{Window: 100_000, Threshold: 0.7, TargetRatio: 0.35, Summarizer: &captureStreamer{reply: "S"}}}
		return cfg, subagentHistory(20, 6000) // ~30k estimated tokens: under the 50k trigger
	}
	cfg, h := build()
	if changed, _ := compact(context.Background(), cfg, &h, make(chan Event, 8), false); changed {
		t.Fatal("uncalibrated estimate is under the trigger; must not compact")
	}
	cfg, h = build()
	cfg.Compaction.calib.observe("m", 10_000, 20_000) // provider says prompts are 2x the estimate
	if changed, _ := compact(context.Background(), cfg, &h, make(chan Event, 8), false); !changed {
		t.Fatal("calibrated (2x) size is over the trigger; must compact")
	}
}

func TestMaybeCompact_SummaryFailureKeepsPruneGains(t *testing.T) {
	failing := &scriptedStreamer{turns: [][]adapter.StreamEvent{{{Kind: adapter.EventErr, Err: errors.New("boom")}}}}
	h := []adapter.Message{{Role: adapter.RoleSystem, Content: "sys"}, {Role: adapter.RoleUser, Content: "task"}}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("c%d", i)
		h = append(h,
			adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: id, Name: "run_bash", ArgsJSON: `{}`}}},
			adapter.Message{Role: adapter.RoleTool, ToolCallID: id, Content: strings.Repeat("x", 40_000)})
	}
	// Threshold high enough that pruning alone doesn't reach 80% of it.
	cfg := LoopConfig{Compaction: &CompactionConfig{Window: 80_000, Threshold: 0.5, TargetRatio: 0.35, Summarizer: failing}}
	events := make(chan Event, 8)
	before := contextwindow.EstimateTokens(h)
	changed, err := compact(context.Background(), cfg, &h, events, false)
	if err != nil {
		t.Fatal(err)
	}
	after := contextwindow.EstimateTokens(h)
	if !changed || after >= before {
		t.Fatalf("prune gains discarded: changed=%v %d -> %d", changed, before, after)
	}
	got := drainEvents(events)
	if len(got) != 1 {
		t.Fatalf("events: %#v", got)
	}
	if ce := got[0].(ContextCompacted); !ce.Pruned || ce.Warning == nil || ce.Err != nil {
		t.Errorf("expected pruned event carrying a warning, got %#v", ce)
	}
}

// multiTurnHistory: system, user "first", then per turn one user request
// followed by `rounds` assistant/tool rounds.
func multiTurnHistory(turns, rounds, toolSize int) []adapter.Message {
	h := []adapter.Message{{Role: adapter.RoleSystem, Content: "sys"}}
	n := 0
	for turn := 0; turn < turns; turn++ {
		h = append(h, adapter.Message{Role: adapter.RoleUser, Content: fmt.Sprintf("REQUEST-%d please do the thing", turn)})
		for r := 0; r < rounds; r++ {
			id := fmt.Sprintf("m%d", n)
			n++
			h = append(h,
				adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: id, Name: "run_bash", ArgsJSON: `{}`}}},
				adapter.Message{Role: adapter.RoleTool, ToolCallID: id, Content: strings.Repeat("x", toolSize)})
		}
	}
	return h
}

func TestChooseCompactionTailStart_NeverOpensOnToolResult(t *testing.T) {
	h := multiTurnHistory(3, 8, 400)
	for budget := 1; budget < 6000; budget += 37 {
		start := chooseCompactionTailStart(h, 1, budget)
		if start < len(h) && h[start].Role == adapter.RoleTool {
			t.Fatalf("budget %d: tail opens on a tool result at %d", budget, start)
		}
	}
}

func TestChooseCompactionTailStart_SnapsBackKeepsRound(t *testing.T) {
	h := subagentHistory(6, 100)
	// Budget that ends exactly on a tool result: backward snap must keep its
	// owning assistant message rather than dropping the whole round.
	start := chooseCompactionTailStart(h, 1, contextwindow.EstimateMessage(h[len(h)-1])+1)
	if start >= len(h) || h[start].Role != adapter.RoleAssistant || h[start].ToolCalls[0].ID != h[start+1].ToolCallID {
		t.Fatalf("tail start %d does not keep the last round intact", start)
	}
}

func TestMaybeCompact_MidTurnCutKeepsLatestRequestVerbatim(t *testing.T) {
	cs := &captureStreamer{reply: "NOTE"}
	h := multiTurnHistory(1, 60, 2000) // one request, long tool loop
	// Make it a second request so the cut lands mid-turn under REQUEST-1.
	h = append([]adapter.Message{h[0], {Role: adapter.RoleUser, Content: "REQUEST-0 earlier work"}, {Role: adapter.RoleAssistant, Content: "done"}}, h[1:]...)
	h[3].Content = "REQUEST-1 the active request"
	cfg := LoopConfig{Compaction: &CompactionConfig{Window: 40_000, Threshold: 0.5, TargetRatio: 0.2, Summarizer: cs}}
	changed, err := compact(context.Background(), cfg, &h, make(chan Event, 8), false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	task := h[1].Content
	if !strings.Contains(task, compaction.LatestRequestOpen+"\nREQUEST-1 the active request\n"+compaction.LatestRequestClose) {
		t.Errorf("active request not kept verbatim in task: %q", task)
	}
	for i := 1; i < len(h); i++ {
		if h[i].Role == adapter.RoleUser && h[i-1].Role == adapter.RoleUser {
			t.Fatalf("consecutive user messages at %d", i)
		}
	}
	if h[2].Role != adapter.RoleAssistant {
		t.Errorf("tail must open on an assistant message, got %v", h[2].Role)
	}
	// A second compaction replaces the folded request instead of stacking it.
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("n%d", i)
		h = append(h,
			adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: id, Name: "run_bash", ArgsJSON: `{}`}}},
			adapter.Message{Role: adapter.RoleTool, ToolCallID: id, Content: strings.Repeat("y", 2000)})
	}
	if changed, err := compact(context.Background(), cfg, &h, make(chan Event, 8), false); err != nil || !changed {
		t.Fatalf("second compaction changed=%v err=%v", changed, err)
	}
	if n := strings.Count(h[1].Content, compaction.LatestRequestOpen); n > 1 {
		t.Errorf("latest-request blocks stacked: %d", n)
	}
	if strings.Contains(cs.lastUserBody, compaction.LatestRequestOpen) {
		t.Errorf("stale latest-request leaked into summarizer input")
	}
}

func TestTokenCalibrator_ResetsOnModelChange(t *testing.T) {
	var c tokenCalibrator
	c.observe("a/m1", 10_000, 20_000)
	if c.get() != 2.0 {
		t.Fatalf("ratio = %v", c.get())
	}
	c.observe("b/m2", 10_000, 11_000)
	if got := c.get(); got != 1.1 {
		t.Fatalf("ratio after model change = %v, want a fresh 1.1 (not smoothed with the old model)", got)
	}
}

// The loop must feed provider-reported usage into the calibrator.
func TestLoop_FeedsCalibrator(t *testing.T) {
	big := strings.Repeat("x", 4*5000) // ~5k estimated tokens in the prompt
	done := sseDone("ok")
	done.Final.Usage = &adapter.Usage{InputTokens: 7500} // 1.5x the estimate
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{{done}}}
	cc := &CompactionConfig{Window: 1_000_000, Threshold: 0.9}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 2, Compaction: cc}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: big}}
	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := cc.calib.get(); got < 1.4 || got > 1.6 {
		t.Fatalf("calibrator ratio = %v, want ~1.5", got)
	}
}

// contextwindow hardcodes the names of tools whose output must survive
// pruning; fail loudly if a rename in this package breaks that protection.
func TestPruneExemptNamesMatchRegisteredTools(t *testing.T) {
	names := []string{SkillToolName, (&TodoWriteTool{}).Name(), (&EnterPlanModeTool{}).Name(), (&ExitPlanModeTool{}).Name()}
	for _, n := range names {
		if !contextwindow.PruneExempt(n) {
			t.Errorf("tool %q is not prune-exempt; update contextwindow.pruneExemptTools", n)
		}
	}
	if (&ReadFileTool{}).Name() != "read_file" {
		t.Errorf("read_file renamed: update contextwindow.readerTools")
	}
}
