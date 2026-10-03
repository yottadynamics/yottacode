package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestLoop_RecoversDeletedCwdOnceAndRunsReadOnlyTool(t *testing.T) {
	root := mkRepoForAgent(t)
	dead := filepath.Join(root, "deleted")
	if err := os.Mkdir(dead, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := NewCwdRef(root)
	cwd.Set(dead)
	if err := os.Remove(dead); err != nil {
		t.Fatal(err)
	}
	mustChdir(t, root)

	reg := NewRegistry()
	reg.Register(&ListDirTool{Cwd: cwd})
	events := make(chan Event, 16)
	cfg := LoopConfig{Registry: reg, Cwd: cwd}
	out, _, _, _, err := executeToolCallImpl(context.Background(), cfg, adapter.ToolCall{ID: "read-1", Name: "list_dir", ArgsJSON: `{}`}, events, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out == "" || cwd.Get() != root {
		t.Fatalf("recovery did not run read-only tool: cwd=%q out=%q", cwd.Get(), out)
	}
	var recovered, results int
	for len(events) > 0 {
		switch ev := <-events; ev.(type) {
		case CwdChanged:
			recovered++
		case ToolResult:
			results++
		}
	}
	if recovered != 1 || results != 1 {
		t.Fatalf("events: recovered=%d results=%d", recovered, results)
	}
}

func TestLoop_RefusesMutatingToolAfterCwdRecovery(t *testing.T) {
	root := mkRepoForAgent(t)
	dead := filepath.Join(root, "deleted")
	if err := os.Mkdir(dead, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := NewCwdRef(root)
	cwd.Set(dead)
	if err := os.Remove(dead); err != nil {
		t.Fatal(err)
	}
	mustChdir(t, root)

	reg := NewRegistry()
	reg.Register(&WriteFileTool{Cwd: cwd, WriteOpts: WritePathOptions{Cwd: cwd}})
	events := make(chan Event, 16)
	cfg := LoopConfig{Registry: reg, Cwd: cwd, BypassPermissions: true}
	out, _, _, _, err := executeToolCallImpl(context.Background(), cfg, adapter.ToolCall{ID: "write-1", Name: "write_file", ArgsJSON: `{"path":"should-not-exist","content":"nope"}`}, events, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "was not executed") {
		t.Fatalf("mutation was not refused: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("mutating tool wrote after recovery: %v", err)
	}
}

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
