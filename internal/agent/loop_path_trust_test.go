package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// retryingTool returns ErrPathOutsideWorkspace on first call and a
// success string on every subsequent call. Models the
// "TUI mutated AllowedPaths between attempts" path so the loop's
// retry-after-elevation flow can be unit-tested without dragging
// the real write tools into the harness.
type retryingTool struct {
	name     string
	sentinel *ErrPathOutsideWorkspace
	calls    int32
	output   string
}

func (r *retryingTool) Name() string                 { return r.name }
func (r *retryingTool) Description() string          { return "retrying " + r.name }
func (r *retryingTool) Schema() map[string]any       { return map[string]any{"type": "object"} }
func (r *retryingTool) RequiresApproval(string) bool { return false }
func (r *retryingTool) ParallelSafe(string) bool     { return false }
func (r *retryingTool) PreviewCall(string) string    { return r.name + "()" }
func (r *retryingTool) Execute(_ context.Context, _ string) (string, error) {
	if n := atomic.AddInt32(&r.calls, 1); n == 1 {
		return "", r.sentinel
	}
	return r.output, nil
}

// runPathTrustTurn drains events from one Turn, intercepting
// PathTrustElevationNeeded with the supplied verdict. Returns the
// collected events plus the terminal error.
func runPathTrustTurn(
	t *testing.T,
	cfg LoopConfig,
	history *[]adapter.Message,
	verdict Decision,
) ([]Event, error) {
	t.Helper()
	events := make(chan Event, 64)
	decisions := make(chan Decision, 2)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- Turn(context.Background(), cfg, history, events, decisions)
		close(events)
	}()
	var collected []Event
	for ev := range events {
		collected = append(collected, ev)
		if _, ok := ev.(PathTrustElevationNeeded); ok {
			decisions <- verdict
		}
	}
	wg.Wait()
	return collected, <-errCh
}

func TestLoop_PathTrustElevation_AllowRetries(t *testing.T) {
	cwd := t.TempDir()
	reg := NewRegistry()
	reg.Register(&retryingTool{
		name:     "write_out",
		sentinel: &ErrPathOutsideWorkspace{Path: "/elsewhere/x.go", Cwd: cwd},
		output:   "written",
	})

	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_out", ArgsJSON: `{}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runPathTrustTurn(t, cfg, &hist, PathTrustSession)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// The elevation event must fire exactly once.
	var elev int
	var toolResult *ToolResult
	for i := range events {
		switch ev := events[i].(type) {
		case PathTrustElevationNeeded:
			elev++
		case ToolResult:
			r := ev
			toolResult = &r
		}
	}
	if elev != 1 {
		t.Errorf("PathTrustElevationNeeded fired %d times, want 1", elev)
	}
	if toolResult == nil {
		t.Fatal("expected a ToolResult after retry")
	}
	if toolResult.Errored {
		t.Errorf("retry should succeed; got errored result %q", toolResult.Output)
	}
	if !strings.Contains(toolResult.Output, "written") {
		t.Errorf("retry result = %q, want it to include the success output", toolResult.Output)
	}
}

func TestLoop_PathTrustElevation_DenyFallsThrough(t *testing.T) {
	cwd := t.TempDir()
	reg := NewRegistry()
	reg.Register(&retryingTool{
		name:     "write_out",
		sentinel: &ErrPathOutsideWorkspace{Path: "/elsewhere/x.go", Cwd: cwd},
		output:   "written",
	})

	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_out", ArgsJSON: `{}`})},
		{sseToken("done"), sseDone("done")},
	}}
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runPathTrustTurn(t, cfg, &hist, Deny)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	var toolResult *ToolResult
	for i := range events {
		if r, ok := events[i].(ToolResult); ok {
			toolResult = &r
		}
	}
	if toolResult == nil {
		t.Fatal("expected a ToolResult after deny")
	}
	if !toolResult.Errored {
		t.Errorf("denied write should surface as errored ToolResult; got %+v", toolResult)
	}
	if !strings.Contains(toolResult.Output, "outside session workspace") {
		t.Errorf("denied result should carry the structured error; got %q", toolResult.Output)
	}
}
