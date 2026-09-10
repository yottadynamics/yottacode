package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// drainCloseDecisions runs Turn and, instead of answering a decision
// request the normal way, closes `decisions` out from under it the
// first time an event matching shouldClose fires. This models a
// decisions channel vanishing while a tool call is still waiting on a
// verdict — e.g. a mid-session provider/model switch tearing down and
// replacing the turn's channels while an approval or path-trust prompt
// is in flight. Unlike a context cancellation, this produces a genuine
// non-cancel error, which is exactly the class of abort
// executeToolCall(s)'s orphan-repair previously only handled for
// isCancelErr.
func drainCloseDecisions(
	t *testing.T,
	ctx context.Context,
	cfg LoopConfig,
	history *[]adapter.Message,
	shouldClose func(Event) bool,
) ([]Event, error) {
	t.Helper()
	events := make(chan Event, 64)
	decisions := make(chan Decision, 1)
	errCh := make(chan error, 1)
	go func() {
		err := Turn(ctx, cfg, history, events, decisions)
		close(events)
		errCh <- err
	}()
	closed := false
	var collected []Event
	for ev := range events {
		collected = append(collected, ev)
		if !closed && shouldClose(ev) {
			closed = true
			close(decisions)
		}
	}
	return collected, <-errCh
}

// assertFullyPaired walks history and fails if any assistant tool_call
// lacks a matching tool_result — the exact invariant a strict backend
// (the Codex/openai-auth "No tool output found for function call X"
// case) enforces on the very next request.
func assertFullyPaired(t *testing.T, history []adapter.Message) {
	t.Helper()
	haveResult := map[string]bool{}
	for _, m := range history {
		if m.Role == adapter.RoleTool {
			haveResult[m.ToolCallID] = true
		}
	}
	for _, m := range history {
		if m.Role != adapter.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if !haveResult[tc.ID] {
				t.Errorf("tool_call %s (%s) has no matching tool_result — this would 400 against a strict backend", tc.ID, tc.Name)
			}
		}
	}
}

// TestToolError_DecisionsChannelClosedDuringApproval_SynthesizesOrphan
// reproduces the root cause behind an "openai-auth: HTTP 400: No tool
// output found for function call X" report: a tool call awaiting
// approval whose decisions channel closes out from under it (a
// non-cancel error) used to return immediately with NOTHING appended
// to history for that call, leaving a dangling tool_use that a later
// turn (e.g. after a mid-session provider/model switch forces a full,
// uncached history replay) serializes straight into a request. The
// fix broadens executeToolCalls' orphan-repair to run on any abort,
// not just isCancelErr.
func TestToolError_DecisionsChannelClosedDuringApproval_SynthesizesOrphan(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "call_1", Name: "needs_approval", ArgsJSON: `{}`})},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "needs_approval", requiresApproval: true, output: "should never be seen"})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 3}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := drainCloseDecisions(t, context.Background(), cfg, &hist, func(ev Event) bool {
		_, ok := ev.(ApprovalNeeded)
		return ok
	})

	if err == nil {
		t.Fatalf("expected a non-nil error when the decisions channel closes mid-approval")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("this abort is not a cancellation; isCancelErr must not match it, got %v", err)
	}
	if !strings.Contains(err.Error(), "decisions channel closed") {
		t.Errorf("expected the decisions-channel-closed error to propagate, got %v", err)
	}

	if got, want := len(hist), 3; got != want {
		t.Fatalf("history len = %d, want %d (user, assistant, tool); hist=%+v", got, want, hist)
	}
	if hist[1].Role != adapter.RoleAssistant || len(hist[1].ToolCalls) != 1 || hist[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("hist[1] should be assistant with tool_call call_1; got %+v", hist[1])
	}
	if hist[2].Role != adapter.RoleTool || hist[2].ToolCallID != "call_1" {
		t.Fatalf("hist[2] should be the tool_result for call_1; got %+v", hist[2])
	}
	if hist[2].Content != interruptedToolResult {
		t.Errorf("orphaned call must carry the synthetic marker; got %q", hist[2].Content)
	}
	assertFullyPaired(t, hist)

	if !hasEvent[ErrorEvent](events) {
		t.Errorf("a non-cancel abort should surface as ErrorEvent")
	}
	if hasEvent[TurnInterrupted](events) {
		t.Errorf("a non-cancel abort must not be reported as TurnInterrupted (that label implies user-initiated cancel)")
	}
}

// pathOutsideTool is parallel-safe and needs no approval, so it enters
// a parallel batch, but its Execute always reports a write outside the
// session workspace — the same sentinel a real write tool (write_file,
// apply_hashline, ...) returns when the model targets a path outside
// the workspace root. That routes it through promptForPathElevation,
// which blocks on the same decisions channel a normal approval does.
type pathOutsideTool struct {
	name         string
	parallelSafe bool
}

func (p *pathOutsideTool) Name() string                 { return p.name }
func (p *pathOutsideTool) Description() string          { return "test " + p.name }
func (p *pathOutsideTool) Schema() map[string]any       { return map[string]any{"type": "object"} }
func (p *pathOutsideTool) RequiresApproval(string) bool { return false }
func (p *pathOutsideTool) ParallelSafe(string) bool     { return p.parallelSafe }
func (p *pathOutsideTool) PreviewCall(string) string    { return p.name + "()" }
func (p *pathOutsideTool) Execute(context.Context, string) (string, error) {
	return "", &ErrPathOutsideWorkspace{Path: "/outside/target", Cwd: "/tmp/ws", AllowedRoots: []string{"/tmp/ws"}}
}

// TestToolError_DecisionsChannelClosedDuringPathTrust_SynthesizesOrphanBatch
// covers the parallel-batch sibling of the test above: a mixed batch
// where one call succeeds, one blocks on a path-trust prompt whose
// decisions channel closes (non-cancel error), and a third never even
// starts because it needed approval and broke the parallel batch
// boundary. Before the fix, executeToolCallsParallel's non-cancel
// branch discarded ALL results — including the one that had already
// completed cleanly — and left every call in the batch (plus the
// never-started one behind it) unpaired in history.
func TestToolError_DecisionsChannelClosedDuringPathTrust_SynthesizesOrphanBatch(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("",
			adapter.ToolCall{ID: "p1", Name: "fast_ok", ArgsJSON: `{}`},
			adapter.ToolCall{ID: "p2", Name: "escapes_workspace", ArgsJSON: `{}`},
			adapter.ToolCall{ID: "p3", Name: "needs_approval", ArgsJSON: `{}`},
		)},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "fast_ok", parallelSafe: true, output: "ok-result"})
	reg.Register(&pathOutsideTool{name: "escapes_workspace", parallelSafe: true})
	reg.Register(&mockTool{name: "needs_approval", requiresApproval: true, output: "never runs"})
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "fan out"}}

	events, err := drainCloseDecisions(t, context.Background(), cfg, &hist, func(ev Event) bool {
		_, ok := ev.(PathTrustElevationNeeded)
		return ok
	})

	if err == nil {
		t.Fatalf("expected a non-nil error when the decisions channel closes mid-path-trust-prompt")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("this abort is not a cancellation; isCancelErr must not match it, got %v", err)
	}

	if got, want := len(hist), 5; got != want {
		t.Fatalf("history len = %d, want %d (user, assistant, 3 tool results); hist=%+v", got, want, hist)
	}
	if hist[1].Role != adapter.RoleAssistant || len(hist[1].ToolCalls) != 3 {
		t.Fatalf("hist[1] should be assistant with 3 tool_calls; got %+v", hist[1])
	}
	gotByID := map[string]string{}
	for _, m := range hist[2:] {
		if m.Role != adapter.RoleTool {
			t.Fatalf("expected tool roles after the assistant message; got %+v", m)
		}
		gotByID[m.ToolCallID] = m.Content
	}
	if gotByID["p1"] != "ok-result" {
		t.Errorf("p1 completed cleanly in the same batch; want its real result preserved, got %q", gotByID["p1"])
	}
	if gotByID["p2"] != interruptedToolResult {
		t.Errorf("p2's path-trust prompt lost its channel; want the synthetic marker, got %q", gotByID["p2"])
	}
	if gotByID["p3"] != interruptedToolResult {
		t.Errorf("p3 was never started (its batch aborted before reaching it); want the synthetic marker, got %q", gotByID["p3"])
	}
	assertFullyPaired(t, hist)

	if !hasEvent[ErrorEvent](events) {
		t.Errorf("a non-cancel abort should surface as ErrorEvent")
	}
}
