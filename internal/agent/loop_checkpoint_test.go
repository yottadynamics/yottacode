package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// mockMutatorTool extends mockTool with a hardcoded PathsToSnapshot
// return — lets a test assert that the loop's checkpoint hook fires
// for tools that implement Mutator and skips ones that don't.
type mockMutatorTool struct {
	mockTool
	paths []string
}

func (m *mockMutatorTool) PathsToSnapshot(_, _ string) []string {
	return m.paths
}

// recordingCheckpointWriter remembers every SnapshotPath call. Goroutine-safe
// because Turn's tool dispatch is sequential per turn but the test could
// observe across goroutine boundaries.
type recordingCheckpointWriter struct {
	mu    sync.Mutex
	calls []struct{ Session, CP, Path string }
}

func (r *recordingCheckpointWriter) SnapshotPath(session, cp, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, struct{ Session, CP, Path string }{session, cp, path})
	return nil
}

func TestLoop_MutatorToolSnapshotsBeforeExecute(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "edit", ArgsJSON: `{}`})},
		{sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockMutatorTool{
		mockTool: mockTool{name: "edit", output: "edited"},
		paths:    []string{"/abs/foo.go"},
	})
	cw := &recordingCheckpointWriter{}
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, Checkpoints: cw}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "edit"}}

	ctx := WithCheckpoint(context.Background(), "sess-A", "cp-1")
	if _, err := runTurnSync(t, ctx, cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	if len(cw.calls) != 1 {
		t.Fatalf("snapshot calls = %d, want 1", len(cw.calls))
	}
	got := cw.calls[0]
	if got.Session != "sess-A" || got.CP != "cp-1" || got.Path != "/abs/foo.go" {
		t.Errorf("snapshot call = %+v, want sess-A/cp-1/foo.go", got)
	}
}

func TestLoop_NonMutatorToolDoesNotSnapshot(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "lookup", ArgsJSON: `{}`})},
		{sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "lookup", output: "value"})
	cw := &recordingCheckpointWriter{}
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, Checkpoints: cw}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "look"}}

	ctx := WithCheckpoint(context.Background(), "sess-A", "cp-1")
	if _, err := runTurnSync(t, ctx, cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(cw.calls) != 0 {
		t.Errorf("non-mutator should not trigger snapshot; got %d calls", len(cw.calls))
	}
}

func TestLoop_NilCheckpointsIsNoOp(t *testing.T) {
	// Same setup as the mutator test but with cfg.Checkpoints=nil.
	// Asserts oneshot and tests that don't wire checkpoints stay green.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "edit", ArgsJSON: `{}`})},
		{sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockMutatorTool{
		mockTool: mockTool{name: "edit", output: "edited"},
		paths:    []string{"/abs/foo.go"},
	})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "edit"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
}

func TestLoop_CheckpointWithoutContextSkipsSnapshot(t *testing.T) {
	// cfg.Checkpoints set but no checkpoint id on ctx — happens if the
	// caller forgets to wire WithCheckpoint. The hook should be a no-op
	// (no panic, no error, no SnapshotPath call).
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "edit", ArgsJSON: `{}`})},
		{sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockMutatorTool{
		mockTool: mockTool{name: "edit", output: "edited"},
		paths:    []string{"/abs/foo.go"},
	})
	cw := &recordingCheckpointWriter{}
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, Checkpoints: cw}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "edit"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(cw.calls) != 0 {
		t.Errorf("ctx without checkpoint id should skip snapshot; got %d calls", len(cw.calls))
	}
}

func TestWithCheckpoint_EmptyIDsLeaveCtxUnchanged(t *testing.T) {
	ctx := WithCheckpoint(context.Background(), "", "")
	sess, cp := CheckpointFromContext(ctx)
	if sess != "" || cp != "" {
		t.Errorf("WithCheckpoint(\"\",\"\"); got sess=%q cp=%q", sess, cp)
	}
}

func TestWithCheckpoint_RoundTrip(t *testing.T) {
	ctx := WithCheckpoint(context.Background(), "S", "C")
	sess, cp := CheckpointFromContext(ctx)
	if sess != "S" || cp != "C" {
		t.Errorf("round trip got sess=%q cp=%q, want S/C", sess, cp)
	}
}
