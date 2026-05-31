package agent

import (
	"context"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// executeToolCallsParallel now joins every worker error instead of returning
// only the first one off the channel. The contract the caller (Turn) depends
// on is that a cancelled batch still produces an error that isCancelErr
// recognizes — errors.Join must not break that. This drives two workers into
// the cancel path simultaneously (so more than one error lands on the channel)
// and asserts the joined result is still detected as a cancellation, with the
// results slice fully populated so each tool_use ID can be paired with a
// synthetic interrupt marker.
func TestExecuteToolCallsParallel_CancelSurvivesJoin(t *testing.T) {
	startedA := make(chan struct{})
	startedB := make(chan struct{})

	reg := NewRegistry()
	reg.Register(&waiterTool{name: "a", started: startedA, parallelSafe: true})
	reg.Register(&waiterTool{name: "b", started: startedB, parallelSafe: true})

	calls := []adapter.ToolCall{
		{ID: "1", Name: "a", ArgsJSON: "{}"},
		{ID: "2", Name: "b", ArgsJSON: "{}"},
	}
	cfg := LoopConfig{Registry: reg}
	events := make(chan Event, 64)
	decisions := make(chan Decision)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel only once both workers are blocked inside Execute, so both
	// hit ctx.Done() and both push an error onto the channel.
	go func() {
		<-startedA
		<-startedB
		cancel()
	}()

	results, err := executeToolCallsParallel(ctx, cfg, calls, events, decisions)
	if err == nil {
		t.Fatal("expected a cancellation error, got nil")
	}
	if !isCancelErr(err) {
		t.Fatalf("joined error must still satisfy isCancelErr, got %v", err)
	}
	if len(results) != len(calls) {
		t.Fatalf("results len = %d, want %d", len(results), len(calls))
	}
}
