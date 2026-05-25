package agent

import (
	"context"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/worktree"
)

// When a tool swaps cfg.Cwd mid-turn (enter_worktree does this), the
// loop must emit an agent.CwdChanged event so the TUI can refresh its
// status bar and any cwd-derived display. This is the regression test
// for that emission path.
func TestLoop_EmitsCwdChangedAfterEnterWorktree(t *testing.T) {
	repo := mkRepoForAgent(t)
	cwdRef := NewCwdRef(repo)

	reg := NewRegistry()
	reg.Register(&EnterWorktreeTool{Cwd: cwdRef})

	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{
			ID:       "c1",
			Name:     "enter_worktree",
			ArgsJSON: `{"name":"cwd-evt","base":"head"}`,
		})},
		{sseToken("ok"), sseDone("ok")},
	}}

	// YoloMode keeps the test from needing an approval surface; the
	// emission path runs the same way regardless of who approved.
	yolo := &YoloModeState{}
	yolo.Active.Store(true)
	cfg := LoopConfig{
		Adapter:       streamer,
		Registry:      reg,
		MaxIterations: 5,
		Cwd:           cwdRef,
		YoloMode:      yolo,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	if !hasEvent[CwdChanged](events) {
		t.Fatalf("expected CwdChanged in event stream, got: %+v", events)
	}

	// Confirm the payload carries the new path so the TUI handler has
	// the value it needs.
	var ev CwdChanged
	for _, e := range events {
		if c, ok := e.(CwdChanged); ok {
			ev = c
			break
		}
	}
	want := worktree.Dir(repo, "cwd-evt")
	if ev.NewCwd != want {
		t.Errorf("CwdChanged.NewCwd = %q, want %q", ev.NewCwd, want)
	}

	// And the shared cwdRef should reflect the same value — the event
	// is purely a notification; the swap was made via cwdRef.Set.
	if cwdRef.Get() != want {
		t.Errorf("cwdRef.Get() = %q, want %q", cwdRef.Get(), want)
	}
}

// A tool that does NOT swap cwd must not produce a CwdChanged event.
// Guards against a regression where the loop fires the event for
// every tool call regardless of the pre/post cwd comparison.
func TestLoop_DoesNotEmitCwdChangedForNonSwappingTool(t *testing.T) {
	repo := mkRepoForAgent(t)
	cwdRef := NewCwdRef(repo)

	reg := NewRegistry()
	reg.Register(&GitWorktreeListTool{Cwd: cwdRef}) // read-only, never swaps

	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{
			ID:       "c1",
			Name:     "git_worktree_list",
			ArgsJSON: `{}`,
		})},
		{sseToken("ok"), sseDone("ok")},
	}}
	yolo := &YoloModeState{}
	yolo.Active.Store(true)
	cfg := LoopConfig{
		Adapter:       streamer,
		Registry:      reg,
		MaxIterations: 5,
		Cwd:           cwdRef,
		YoloMode:      yolo,
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[CwdChanged](events) {
		t.Errorf("read-only tool must not emit CwdChanged, got: %+v", events)
	}
}
