package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// blockingMutatorTool is a ParallelSafe Mutator that signals startedCh right
// after Execute begins (i.e. after the loop's mutation lock is already
// acquired — see executeToolCallImpl) and blocks until releaseCh is closed.
// paths is what PathsToSnapshot reports, standing in for the file(s) a real
// edit tool would touch.
type blockingMutatorTool struct {
	name      string
	paths     []string
	startedCh chan struct{}
	releaseCh chan struct{}
}

func (b *blockingMutatorTool) Name() string                         { return b.name }
func (b *blockingMutatorTool) Description() string                  { return "test " + b.name }
func (b *blockingMutatorTool) Schema() map[string]any               { return map[string]any{"type": "object"} }
func (b *blockingMutatorTool) RequiresApproval(string) bool         { return false }
func (b *blockingMutatorTool) ParallelSafe(string) bool             { return true }
func (b *blockingMutatorTool) PreviewCall(string) string            { return b.name + "()" }
func (b *blockingMutatorTool) PathsToSnapshot(_, _ string) []string { return b.paths }
func (b *blockingMutatorTool) Execute(ctx context.Context, _ string) (string, error) {
	close(b.startedCh)
	select {
	case <-b.releaseCh:
		return "ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestExecuteToolCallsParallel_MutationLockRejectsSameFile pins the fix for
// the corruption scenario where two subagents (or a batch of parallel Agent
// calls from one parent message) share the parent's cwd with no worktree
// isolation and independently read-modify-write the same file: without a
// guard, both edits land and either clobber or duplicate each other with no
// error at all. With MutationLocks set, whichever call loses the race for
// the shared path must fail fast with a clear, recoverable error instead of
// running its Execute concurrently with the winner.
func TestExecuteToolCallsParallel_MutationLockRejectsSameFile(t *testing.T) {
	startedA := make(chan struct{})
	startedB := make(chan struct{})
	release := make(chan struct{})

	reg := NewRegistry()
	reg.Register(&blockingMutatorTool{name: "a", paths: []string{"/repo/shared.go"}, startedCh: startedA, releaseCh: release})
	reg.Register(&blockingMutatorTool{name: "b", paths: []string{"/repo/shared.go"}, startedCh: startedB, releaseCh: release})

	calls := []adapter.ToolCall{
		{ID: "1", Name: "a", ArgsJSON: "{}"},
		{ID: "2", Name: "b", ArgsJSON: "{}"},
	}
	cfg := LoopConfig{Registry: reg, Cwd: NewCwdRef("/repo"), MutationLocks: &MutationLockRegistry{}}
	events := make(chan Event, 64)
	decisions := make(chan Decision)

	resultsCh := make(chan []toolExecResult, 1)
	errCh := make(chan error, 1)
	go func() {
		results, err := executeToolCallsParallel(context.Background(), cfg, calls, events, decisions)
		resultsCh <- results
		errCh <- err
	}()

	// Exactly one of the two blocking tools should reach Execute (the lock
	// winner); the other must fail before ever getting there. A closed
	// channel stays permanently ready to receive, so once a winner is
	// picked here the loser's channel — not both again — is the only one
	// worth checking next.
	var loserStarted chan struct{}
	select {
	case <-startedA:
		loserStarted = startedB
	case <-startedB:
		loserStarted = startedA
	case <-time.After(2 * time.Second):
		t.Fatal("neither tool call started — the lock winner never reached Execute")
	}

	// The loser must not also start: give it a beat, then confirm it never
	// closes its started channel.
	select {
	case <-loserStarted:
		t.Fatal("both tool calls reached Execute concurrently on the same path")
	case <-time.After(150 * time.Millisecond):
		// Expected: the loser hit the lock conflict path, which returns
		// immediately without ever calling Execute.
	}

	close(release)

	var results []toolExecResult
	select {
	case results = <-resultsCh:
	case <-time.After(2 * time.Second):
		t.Fatal("executeToolCallsParallel did not return after releasing the winner")
	}
	if err := <-errCh; err != nil {
		t.Fatalf("executeToolCallsParallel returned unexpected error: %v", err)
	}

	var oks, conflicts int
	for _, r := range results {
		switch {
		case r.content == "ok":
			oks++
		case strings.Contains(r.content, "currently being edited by another tool call"):
			conflicts++
		default:
			t.Fatalf("unexpected result content: %q", r.content)
		}
	}
	if oks != 1 || conflicts != 1 {
		t.Fatalf("got oks=%d conflicts=%d, want exactly one of each", oks, conflicts)
	}
}
