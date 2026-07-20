package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
)

func TestCompact_ForceCapsRetainedToolWhenMiddleEmpty(t *testing.T) {
	h := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "task"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: "c1", Name: "read_file", ArgsJSON: "{}"}}},
		{Role: adapter.RoleTool, ToolCallID: "c1", Content: strings.Repeat("x", (maxRetainedToolTokens+200)*4)},
	}
	before := contextwindow.EstimateTokens(h)
	cfg := LoopConfig{Compaction: &CompactionConfig{Window: 100_000}}
	events := make(chan Event, 2)

	changed, err := compact(context.Background(), cfg, &h, events, true)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !changed {
		t.Fatal("forced cap-only compaction reported unchanged")
	}
	if after := contextwindow.EstimateTokens(h); after >= before {
		t.Fatalf("history did not shrink: before=%d after=%d", before, after)
	}
	if !strings.HasSuffix(h[len(h)-1].Content, retainedToolCompactionMarker) {
		t.Fatalf("tool result was not capped: suffix %q", h[len(h)-1].Content[len(h[len(h)-1].Content)-40:])
	}
	evs := drainEvents(events)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	cc := evs[0].(ContextCompacted)
	if !cc.Forced || cc.Err != nil || cc.After >= cc.Before {
		t.Fatalf("ContextCompacted = %+v", cc)
	}
}

func TestCompact_PreCompactPathAndErrorAreReported(t *testing.T) {
	snapErr := errors.New("disk full")
	h := subagentHistory(8, 800)
	var snap []adapter.Message
	cfg := LoopConfig{Compaction: &CompactionConfig{
		Window:    2000,
		Threshold: 0.5,
		Summarizer: &scriptedStreamer{turns: [][]adapter.StreamEvent{{
			{Kind: adapter.EventTokenDelta, Token: "summary"},
			{Kind: adapter.EventDone, Final: &adapter.Message{}},
		}}},
		PreCompact: func(history []adapter.Message) (string, error) {
			snap = append([]adapter.Message(nil), history...)
			return "/tmp/pre.json", snapErr
		},
	}}
	events := make(chan Event, 2)
	changed, err := compact(context.Background(), cfg, &h, events, true)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !changed {
		t.Fatal("compact reported unchanged")
	}
	if len(snap) == 0 || !strings.Contains(snap[1].Content, "Audit the codebase") {
		t.Fatalf("PreCompact did not receive pre-compaction history: %+v", snap)
	}
	cc := drainEvents(events)[0].(ContextCompacted)
	if cc.SnapshotPath != "/tmp/pre.json" || !errors.Is(cc.SnapshotErr, snapErr) {
		t.Fatalf("snapshot fields = path %q err %v", cc.SnapshotPath, cc.SnapshotErr)
	}
}

func TestTurn_ContextOverflowForcesCompactionAndRetries(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{{Kind: adapter.EventErr, Err: errors.New("context_length_exceeded")}},
		{{Kind: adapter.EventTokenDelta, Token: "ok"}, {Kind: adapter.EventDone, Final: &adapter.Message{Role: adapter.RoleAssistant, Content: "ok"}}},
	}}
	h := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "task"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: "c1", Name: "read_file", ArgsJSON: "{}"}}},
		{Role: adapter.RoleTool, ToolCallID: "c1", Content: strings.Repeat("x", (maxRetainedToolTokens+200)*4)},
	}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 1, Compaction: &CompactionConfig{Window: 100_000}}
	events, err := runTurnSync(t, context.Background(), cfg, &h, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if streamer.next != 2 {
		t.Fatalf("stream calls = %d, want 2", streamer.next)
	}
	found := false
	for _, ev := range events {
		if cc, ok := ev.(ContextCompacted); ok && cc.Forced {
			found = true
		}
	}
	if !found {
		t.Fatalf("forced ContextCompacted event missing: %#v", events)
	}
}

func TestTurn_ContextOverflowAfterContentDoesNotRetry(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{{Kind: adapter.EventTokenDelta, Token: "partial"}, {Kind: adapter.EventErr, Err: errors.New("context_length_exceeded")}},
		{{Kind: adapter.EventDone, Final: &adapter.Message{Role: adapter.RoleAssistant, Content: "should not run"}}},
	}}
	h := []adapter.Message{{Role: adapter.RoleUser, Content: "task"}}
	cfg := LoopConfig{Adapter: streamer, Registry: NewRegistry(), MaxIterations: 1, Compaction: &CompactionConfig{Window: 100_000}}
	_, err := runTurnSync(t, context.Background(), cfg, &h, nil)
	if err == nil {
		t.Fatal("expected overflow error")
	}
	if streamer.next != 1 {
		t.Fatalf("stream calls = %d, want 1", streamer.next)
	}
}
