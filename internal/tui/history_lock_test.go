package tui

import (
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// TestHistMu_ConcurrentMessagesAccess models the live race: the agent
// goroutine appends to sess.Messages under histMu while this Update
// goroutine reads it (the IterationStart token estimate, /context,
// /system). With the lock both sides are serialized; run with -race.
// Regression for the release audit's session-history data race.
func TestHistMu_ConcurrentMessagesAccess(t *testing.T) {
	m := newTestModel(t)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // stand-in for the agent Turn goroutine's locked appends
		defer wg.Done()
		for range 1000 {
			m.histMu.Lock()
			m.sess.Messages = append(m.sess.Messages, adapter.Message{Role: adapter.RoleUser, Content: "u"})
			m.histMu.Unlock()
		}
	}()
	wg.Add(1)
	go func() { // Update-goroutine reads via the locked accessor
		defer wg.Done()
		for range 1000 {
			_ = m.estimatedContextTokens(m.lockedMessages())
		}
	}()
	wg.Wait()
}
