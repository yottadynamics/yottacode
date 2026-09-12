package agent

import (
	"context"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestTurnPersistsWallTimeMS(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{{sseDone("done")}}}
	history := []adapter.Message{{Role: adapter.RoleUser, Content: "hi"}}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 1}
	if _, err := runTurnSync(t, context.Background(), cfg, &history, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(history) != 2 || history[1].WallTimeMS == nil {
		t.Fatalf("assistant wall time missing: %+v", history)
	}
	if *history[1].WallTimeMS < 0 {
		t.Fatalf("assistant wall time = %d, want non-negative", *history[1].WallTimeMS)
	}
}

func TestTurnPersistsToolStatusAndLatency(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "lookup", ArgsJSON: `{}`})},
		{sseDone("done")},
	}}
	registry := NewRegistry()
	registry.Register(&mockTool{name: "lookup", output: "value"})
	history := []adapter.Message{{Role: adapter.RoleUser, Content: "look"}}
	cfg := LoopConfig{Adapter: streamer, Registry: registry, MaxIterations: 2}
	if _, err := runTurnSync(t, context.Background(), cfg, &history, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(history) < 3 || len(history[1].ToolCalls) != 1 {
		t.Fatalf("tool call missing: %+v", history)
	}
	call := history[1].ToolCalls[0]
	if call.Status != "ok" {
		t.Errorf("tool status = %q, want ok", call.Status)
	}
	if call.LatencyMS == nil || *call.LatencyMS < 0 {
		t.Errorf("tool latency = %v, want non-negative value", call.LatencyMS)
	}
}
