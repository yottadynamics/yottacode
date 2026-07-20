package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
)

// captureStreamer records the last user-message body it was asked to
// stream, so a test can assert how the summary INPUT was budgeted. It
// always replies with a fixed short summary.
type captureStreamer struct {
	mu           sync.Mutex
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
	for range rounds {
		h = append(h,
			adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
				{Name: "read_file", ArgsJSON: `{"path":"x.go"}`},
			}},
			adapter.Message{Role: adapter.RoleTool, Content: strings.Repeat("x", toolSize)},
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
	if evs := drainEvents(events); len(evs) != 0 {
		t.Errorf("expected no ContextCompacted event when skipping, got %d", len(evs))
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
	const marker = "[earlier middle omitted due to compaction budget]"
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
func TestRenderForCompaction_TinyWindowTrimsInsteadOfReturningAll(t *testing.T) {
	middle := subagentHistory(40, 500)[2:] // ~5K tokens of segments, no anchors
	body := renderForCompaction(middle, 8192)
	const marker = "[earlier middle omitted due to compaction budget]"
	if !strings.Contains(body, marker) {
		t.Errorf("tiny window should trim (window/2 fallback), got full body without the omit marker")
	}
	if got := (len(body) + 3) / 4; got > 8192/2+256 {
		t.Errorf("trimmed body = %d tokens, want <= ~%d (window/2 budget)", got, 8192/2)
	}
}

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
