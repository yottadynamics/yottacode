package agent

import (
	"context"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// TestLoop_ParallelToolResultsWithHistoryLockDoNotDeadlock pins a regression
// where a real (non-nil) cfg.HistoryLock — exactly what the TUI wires up, as
// opposed to the nil lock every other parallel-tool-call test in this
// package uses — deadlocked forever on any turn that batched 2+
// parallel-safe tool calls. appendToolResults locked cfg.HistoryLock and
// then, still inside that closure, called annotateToolCall, which itself
// tried to lock the same non-reentrant syncutil.Mutex again: the turn's
// goroutine blocked on its own held lock, the round counter froze, and the
// session never produced another token. See annotateToolCallLocked.
func TestLoop_ParallelToolResultsWithHistoryLockDoNotDeadlock(t *testing.T) {
	call := func(id string) adapter.ToolCall {
		return adapter.ToolCall{ID: id, Name: "read_file", ArgsJSON: `{}`}
	}
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", call("c1"), call("c2"))},
		{sseToken("done"), sseDone("done")},
	}}
	reg := NewRegistry()
	reg.Register(parallelReadTool{})
	cfg := LoopConfig{
		Adapter:       streamer,
		Registry:      reg,
		MaxIterations: 5,
		HistoryLock:   &syncutil.Mutex{},
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "read"}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := runTurnSync(t, ctx, cfg, &hist, nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Turn: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Turn deadlocked on a real HistoryLock while appending parallel tool results (round never advanced)")
	}

	var annotated int
	for _, msg := range hist {
		if msg.Role != adapter.RoleAssistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Status == "ok" && tc.LatencyMS != nil {
				annotated++
			}
		}
	}
	if annotated != 2 {
		t.Fatalf("annotated tool calls = %d, want 2; history=%+v", annotated, hist)
	}
}
