package tui

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func TestSummarizerOrDefault(t *testing.T) {
	routed := &scriptedAdapter{}
	fallback := &scriptedAdapter{}

	if got := summarizerOrDefault(routed, fallback); got != agentStreamer(routed) {
		t.Errorf("routed adapter should win when set")
	}
	if got := summarizerOrDefault(nil, fallback); got != agentStreamer(fallback) {
		t.Errorf("should fall back to the main adapter when routed is nil")
	}
	if got := summarizerOrDefault(nil, nil); got != nil {
		t.Errorf("both nil should yield nil, got %v", got)
	}
}

// TestSummarizeDeps_UsesRoutedAdapter proves the /resume --summarized +
// compaction path dispatches to the fast (routed) summarizer when one is
// wired, and to the main adapter otherwise — the cache-safe saving for
// summarization.
func TestSummarizeDeps_UsesRoutedAdapter(t *testing.T) {
	fast := &scriptedAdapter{}
	main := &scriptedAdapter{}

	routed := Model{
		summarizerAdapter: fast,
		cfg:               agent.LoopConfig{Adapter: main},
	}
	if deps := routed.summarizeDeps(); deps.adapter != agentStreamer(fast) {
		t.Errorf("summarizeDeps should use the routed fast adapter")
	}

	unrouted := Model{
		cfg: agent.LoopConfig{Adapter: main},
	}
	if deps := unrouted.summarizeDeps(); deps.adapter != agentStreamer(main) {
		t.Errorf("summarizeDeps should fall back to the main adapter when routing is off")
	}
}
