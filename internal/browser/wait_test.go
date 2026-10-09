package browser

import (
	"testing"
	"time"
)

func TestPendingRequestsCountsOnlyUnansweredRecentOnes(t *testing.T) {
	p := &trackedPage{}
	p.recordRequestStart("done", "GET", "https://e/a")
	p.recordRequestUpdate("done", func(n *NetworkEntry) { n.Status = 200 })
	p.recordRequestStart("failed", "GET", "https://e/b")
	p.recordRequestUpdate("failed", func(n *NetworkEntry) { n.Failed = true })
	p.recordRequestStart("inflight", "GET", "https://e/c")
	p.recordRequestStart("hung", "GET", "https://e/d")
	p.recordRequestUpdate("hung", func(n *NetworkEntry) { n.At = time.Now().Add(-time.Minute) })

	if got := p.pendingRequests(30 * time.Second); got != 1 {
		t.Errorf("pending = %d, want 1 (only the recent unanswered request)", got)
	}
}
