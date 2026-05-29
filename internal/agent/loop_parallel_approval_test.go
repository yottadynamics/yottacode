package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/permissions"
)

// TestApprovalGate_SerializesConcurrentRoundTrips is the core guard for
// the parallel-approval fix: when several workers in one parallel batch
// hold the same gate, only one may be inside a user-interaction
// round-trip at a time. Without the gate (or if lockApprovalGate were a
// no-op when a gate IS attached) the observed concurrency would exceed
// 1 and two workers would race on the single decisions channel / modal.
func TestApprovalGate_SerializesConcurrentRoundTrips(t *testing.T) {
	gate := new(sync.Mutex)
	ctx := WithApprovalGate(context.Background(), gate)

	var concurrent, maxConcurrent atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := lockApprovalGate(ctx)
			defer unlock()
			n := concurrent.Add(1)
			for {
				m := maxConcurrent.Load()
				if n <= m || maxConcurrent.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond) // widen the window so a leak would be seen
			concurrent.Add(-1)
		}()
	}
	wg.Wait()
	if got := maxConcurrent.Load(); got != 1 {
		t.Fatalf("approval gate allowed %d concurrent round-trips; want 1", got)
	}
}

// TestApprovalGate_NilIsNoop confirms the serial path (no gate attached)
// is unaffected: lockApprovalGate returns a usable unlock that is safe to
// call, and the returned func is idempotent (so the explicit-unlock paths
// in the subagent forwarding loop can't panic on a double call).
func TestApprovalGate_NilIsNoop(t *testing.T) {
	unlock := lockApprovalGate(context.Background())
	unlock()
	unlock() // must not panic even though no mutex was locked
}

// TestExecuteToolCallsParallel_AskRoutesDecisionsCorrectly drives the
// real parallel path: two parallel-safe, no-approval tools that each hit
// a permissions `Ask` rule run concurrently in one batch. The gate must
// keep their approval round-trips serialized so the user's per-tool
// decision routes to the right worker — read_file approved (runs),
// grep denied (does not). Before the fix the two goroutines raced on the
// shared decisions channel and could authorize the wrong tool.
func TestExecuteToolCallsParallel_AskRoutesDecisionsCorrectly(t *testing.T) {
	cwd := t.TempDir()
	permDir := filepath.Join(cwd, ".yottacode")
	if err := os.MkdirAll(permDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Ask on both tools so both prompt while batched in parallel.
	if err := os.WriteFile(filepath.Join(permDir, "permissions.json"),
		[]byte(`{"permissions":{"ask":["Read(*)","Grep(*)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	perms, err := permissions.Load(cwd)
	if err != nil {
		t.Fatalf("permissions.Load: %v", err)
	}

	reg := NewRegistry()
	reg.Register(&mockTool{name: "read_file", parallelSafe: true, output: "READ_RAN"})
	reg.Register(&mockTool{name: "grep", parallelSafe: true, output: "GREP_RAN"})

	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("",
			adapter.ToolCall{ID: "rf", Name: "read_file", ArgsJSON: `{"path":"a.txt"}`},
			adapter.ToolCall{ID: "gp", Name: "grep", ArgsJSON: `{"pattern":"x"}`},
		)},
		{sseDone("done")},
	}}
	cfg := LoopConfig{Adapter: streamer, Registry: reg, Permissions: perms, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	// Per-tool verdict keyed on the tool name. The decide func is
	// order-independent, so the routing assertion below isolates whether
	// the decision reached the correct worker.
	decide := func(a ApprovalNeeded) Decision {
		if a.ToolName == "read_file" {
			return AllowOnce
		}
		return Deny
	}

	// Timeout guard so a regression that reintroduces the deadlock fails
	// fast instead of hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = runTurnSync(t, ctx, cfg, &hist, decide)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	var readContent, grepContent string
	for _, m := range hist {
		if m.Role != adapter.RoleTool {
			continue
		}
		switch m.ToolCallID {
		case "rf":
			readContent = m.Content
		case "gp":
			grepContent = m.Content
		}
	}
	if readContent != "READ_RAN" {
		t.Errorf("read_file was approved but did not run (result %q) — decision misrouted", readContent)
	}
	if grepContent == "GREP_RAN" {
		t.Errorf("grep was denied but ran anyway — decision misrouted to the wrong tool")
	}
}
