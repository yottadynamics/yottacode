package browser

import (
	"testing"
	"time"
)

// Chrome reuses one request id for every hop of a redirect chain. The earlier
// hops must be closed out and the response must land on the LAST hop, or the
// final request looks "pending" forever.
func TestRedirectHopsReuseARequestID(t *testing.T) {
	p := &trackedPage{}
	p.recordRequestStart("r1", "GET", "https://e/a")
	p.recordRequestUpdate("r1", func(n *NetworkEntry) { n.Status = 302 }) // hop 1 redirected
	p.recordRequestStart("r1", "GET", "https://e/b")                      // hop 2, same id
	p.recordRequestUpdate("r1", func(n *NetworkEntry) { n.Status = 200 }) // hop 2 answered

	if got := p.pendingRequests(30 * time.Second); got != 0 {
		t.Errorf("pending = %d after the chain finished, want 0", got)
	}
	snap := p.networkSnapshot(0)
	if len(snap) != 2 || snap[0].Status != 302 || snap[1].Status != 200 || snap[1].URL != "https://e/b" {
		t.Errorf("hops recorded wrong: %+v", snap)
	}
}

func TestStreamsDoNotHoldThePageBusy(t *testing.T) {
	p := &trackedPage{}
	for i, typ := range []string{"WebSocket", "EventSource", "Preflight"} {
		id := string(rune('a' + i))
		p.recordRequestStart(id, "GET", "https://e/"+id)
		p.recordRequestUpdate(id, func(n *NetworkEntry) { n.Type = typ })
	}
	if got := p.pendingRequests(30 * time.Second); got != 0 {
		t.Errorf("pending = %d, want 0: streams and preflights never get a plain response", got)
	}
	p.recordRequestStart("xhr", "GET", "https://e/x")
	p.recordRequestUpdate("xhr", func(n *NetworkEntry) { n.Type = "XHR" })
	if got := p.pendingRequests(30 * time.Second); got != 1 {
		t.Errorf("an ordinary unanswered request must still count, got %d", got)
	}
}
