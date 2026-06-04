package tui

import (
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// TestSummarizeCmd_SnapshotsUnderLock is a regression for the release
// review's summarize data race: summarizeCmd used to clone m.sess.Messages
// WITHOUT histMu while the agent goroutine appended to the same slice under
// histMu. /summarize is PreservesTurn=false, so it cancels the turn (async)
// and then runs summarizeCmd synchronously while the just-cancelled turn
// goroutine is still unwinding and appending. Run with -race.
func TestSummarizeCmd_SnapshotsUnderLock(t *testing.T) {
	m := newTestModel(t)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // agent Turn goroutine: locked appends (realloc churn)
		defer wg.Done()
		for range 3000 {
			m.histMu.Lock()
			m.sess.Messages = append(m.sess.Messages, adapter.Message{Role: adapter.RoleUser, Content: "u"})
			m.histMu.Unlock()
		}
	}()
	wg.Add(1)
	go func() { // Update goroutine: /summarize mid-turn → unlocked clone
		defer wg.Done()
		for range 3000 {
			_ = m.summarizeCmd(false)
		}
	}()
	wg.Wait()
}

// TestReloadMemoryNow_WritesSystemMsgUnderLock is a regression for the
// release review's memory-reload data race: reloadMemoryNow wrote
// m.sess.Messages[i].Content WITHOUT histMu — the structural twin of the
// skills_picker.go system-prompt write that IS locked. Reachable via /model
// and /memory (both PreservesTurn=false → cancel-then-run). Run with -race.
func TestReloadMemoryNow_WritesSystemMsgUnderLock(t *testing.T) {
	m := newTestModel(t)
	// Ensure a system message exists so the Content write executes.
	m.sess.Messages = append([]adapter.Message{{Role: adapter.RoleSystem, Content: "sys"}}, m.sess.Messages...)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // agent Turn goroutine: locked appends
		defer wg.Done()
		for range 3000 {
			m.histMu.Lock()
			m.sess.Messages = append(m.sess.Messages, adapter.Message{Role: adapter.RoleUser, Content: "u"})
			m.histMu.Unlock()
		}
	}()
	wg.Add(1)
	go func() { // Update goroutine: memory reload rewrites system Content
		defer wg.Done()
		for range 3000 {
			_, _ = reloadMemoryNow(m, "")
		}
	}()
	wg.Wait()
}
