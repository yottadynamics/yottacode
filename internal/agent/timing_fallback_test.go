package agent

import (
	"context"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// lastAssistant returns the last RoleAssistant message in hist, failing
// the test if there isn't one.
func lastAssistant(t *testing.T, hist []adapter.Message) adapter.Message {
	t.Helper()
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Role == adapter.RoleAssistant {
			return hist[i]
		}
	}
	t.Fatalf("no RoleAssistant message in history: %+v", hist)
	return adapter.Message{}
}

// TestStreamIteration_StampsLatencyAndTTFT confirms both timing fields
// land on the persisted assistant message: LatencyMS always (a streamed
// call takes measurable time), TTFTMs only when a visible token actually
// arrived.
func TestStreamIteration_StampsLatencyAndTTFT(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseToken("hel"), sseToken("lo"), sseDone("hello")},
	}}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	final := lastAssistant(t, hist)
	if final.LatencyMS == nil {
		t.Error("expected LatencyMS to be stamped")
	}
	if final.TTFTMs == nil {
		t.Error("expected TTFTMs to be stamped — tokens were streamed")
	} else if *final.TTFTMs > *final.LatencyMS {
		t.Errorf("TTFTMs (%d) should never exceed LatencyMS (%d)", *final.TTFTMs, *final.LatencyMS)
	}
}

// TestStreamIteration_NoVisibleTokenLeavesTTFTNil confirms a pure
// tool-call turn (no content, no reasoning token ever streamed) leaves
// TTFTMs nil rather than a fabricated zero — "not applicable," not
// "instant."
func TestStreamIteration_NoVisibleTokenLeavesTTFTNil(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "spinner", ArgsJSON: `{}`})},
		{sseToken("done"), sseDone("done")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "spinner", output: "ok"})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	// The first assistant message (the tool-call-only turn) is the one
	// with no visible token — find it by its ToolCalls, since
	// lastAssistant would return the second (text) turn instead.
	var toolTurn *adapter.Message
	for i := range hist {
		if hist[i].Role == adapter.RoleAssistant && len(hist[i].ToolCalls) > 0 {
			toolTurn = &hist[i]
			break
		}
	}
	if toolTurn == nil {
		t.Fatal("expected a tool-call assistant message in history")
	}
	if toolTurn.LatencyMS == nil {
		t.Error("expected LatencyMS to be stamped even with no visible token")
	}
	if toolTurn.TTFTMs != nil {
		t.Errorf("expected TTFTMs to stay nil for a turn with no streamed token, got %d", *toolTurn.TTFTMs)
	}
}

// TestStreamIteration_StampsFallbackCountAndReason confirms a fallback
// mid-stream (previously only visible as a transient Fallback event to
// the TUI) also lands on the persisted assistant message, so an export
// can show a turn actually took two attempts and why.
func TestStreamIteration_StampsFallbackCountAndReason(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{
			{
				Kind:           adapter.EventFallback,
				FallbackFrom:   "anthropic/claude-haiku-4-5",
				FallbackTo:     "openai/gpt-4o",
				FallbackReason: "rate limited",
				FallbackPolicy: "fallback-chain",
			},
			sseToken("hello"),
			sseDone("hello"),
		},
	}}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	final := lastAssistant(t, hist)
	if final.FallbackCount != 1 {
		t.Errorf("FallbackCount = %d, want 1", final.FallbackCount)
	}
	if final.FallbackReason != "rate limited" {
		t.Errorf("FallbackReason = %q, want %q", final.FallbackReason, "rate limited")
	}
}

// TestStreamIteration_NoFallbackLeavesCountZero confirms the
// overwhelmingly common single-provider case doesn't grow noise —
// FallbackCount stays 0 and FallbackReason stays empty when no
// EventFallback ever fired.
func TestStreamIteration_NoFallbackLeavesCountZero(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseToken("hi"), sseDone("hi")},
	}}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	final := lastAssistant(t, hist)
	if final.FallbackCount != 0 {
		t.Errorf("FallbackCount = %d, want 0", final.FallbackCount)
	}
	if final.FallbackReason != "" {
		t.Errorf("FallbackReason = %q, want empty", final.FallbackReason)
	}
}
