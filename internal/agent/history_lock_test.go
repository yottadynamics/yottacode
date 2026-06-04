package agent

import (
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// TestHistoryLock_ConcurrentAppendAndSnapshot exercises the locked
// history primitives the way the live loop does: the agent goroutine
// appends to the shared slice while another goroutine (the TUI Update
// loop) snapshots it for a token estimate. With cfg.HistoryLock set these
// must be race-free; run with -race. Regression for the release audit's
// session-history data race (m.sess.Messages shared across goroutines).
func TestHistoryLock_ConcurrentAppendAndSnapshot(t *testing.T) {
	var mu sync.Mutex
	cfg := LoopConfig{HistoryLock: &mu}
	hist := []adapter.Message{{Role: adapter.RoleSystem, Content: "sys"}}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // agent goroutine: append
		defer wg.Done()
		for range 1000 {
			appendHistory(cfg, &hist, adapter.Message{Role: adapter.RoleUser, Content: "u"})
		}
	}()
	wg.Add(1)
	go func() { // Update goroutine: snapshot + read
		defer wg.Done()
		for range 1000 {
			snap := snapshotHistory(cfg, &hist)
			total := 0
			for i := range snap {
				total += len(snap[i].Content)
			}
			_ = total
		}
	}()
	wg.Wait()

	if len(hist) != 1001 {
		t.Errorf("history length = %d, want 1001 (1 system + 1000 appended)", len(hist))
	}
}

// TestHistoryLock_NilIsNoOp confirms the helpers work (single-threaded)
// when no lock is configured — the subagent/oneshot/test path.
func TestHistoryLock_NilIsNoOp(t *testing.T) {
	cfg := LoopConfig{} // HistoryLock nil
	var hist []adapter.Message
	appendHistory(cfg, &hist, adapter.Message{Role: adapter.RoleUser, Content: "a"})
	snap := snapshotHistory(cfg, &hist)
	if len(snap) != 1 || snap[0].Content != "a" {
		t.Fatalf("snapshot = %+v, want one message 'a'", snap)
	}
}
