package browser

import (
	"context"
	"testing"
)

// attachChild announces a target attached beneath parentSession (an iframe or
// worker found by that session's own auto-attach).
func (f *fakeDevTools) attachChild(parentSession, session, targetID, typ string) {
	f.event("Target.attachedToTarget", parentSession, map[string]any{
		"sessionId": session, "waitingForDebugger": false,
		"targetInfo": map[string]any{"targetId": targetID, "type": typ},
	})
}

// A frame or worker is judged by the page that owns it. The review's scenario:
// tab A (public) embeds a cross-site iframe and spawns a worker; the agent opens
// tab B on localhost. Nothing in tab A may inherit B's local access.
func TestGuardJudgesFramesAndWorkersByTheirOwningPage(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.onBrowserAttach = func(f *fakeDevTools) {
		f.attach("SA", "TA", "page", false) // tab A
		f.attach("SB", "TB", "page", false) // tab B
	}
	pol := testPolicy(map[string][]string{"example.com": {"93.184.216.34"}})
	g, err := startGuard(context.Background(), dir, pol, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)
	// Children announced on their parent's session, as Chrome does.
	f.attachChild("SA", "SA-frame", "FA", "iframe")
	f.attachChild("SA", "SA-worker", "WA", "worker")
	f.attachChild("SB", "SB-frame", "FB", "iframe")
	f.attach("SW", "SHARED", "shared_worker", false) // attached at browser level: no page above it
	f.await(func() bool {
		return indexOf(f.seq("SB-frame"), "Fetch.enable") >= 0 && indexOf(f.seq("SW"), "Runtime.runIfWaitingForDebugger") < 0 && indexOf(f.seq("SW"), "Fetch.enable") >= 0 && indexOf(f.seq("SA-worker"), "Fetch.enable") >= 0
	}, "children guarded")

	// Tab A is on a public site; tab B is the dev server.
	// The agent navigates one tab at a time: A goes public, then B is opened
	// on the dev server explicitly.
	f.pausedAs("SA", "a-nav", "https://example.com/", "TA", "Document")
	f.await(func() bool { return f.count("Fetch.continueRequest") == 1 }, "tab A allowed")
	pol.allowExplicit("http://localhost:3000/")
	f.pausedAs("SB", "b-nav", "http://localhost:3000/", "TB", "Document")
	f.await(func() bool { return f.count("Fetch.continueRequest") == 2 }, "tab B allowed")

	probe := "http://localhost:9222/json"
	f.pausedAs("SA-frame", "a-frame", probe, "FA", "XHR")   // A's cross-site iframe
	f.pausedAs("SA-worker", "a-worker", probe, "WA", "XHR") // A's worker
	f.pausedAs("SW", "shared", probe, "SHARED", "XHR")      // a worker with no page above it
	f.await(func() bool { return f.count("Fetch.failRequest") == 3 }, "tab A's frame, A's worker and the orphan worker refused")

	// Tab B is local, so its own frame keeps working.
	f.pausedAs("SB-frame", "b-frame", "http://localhost:8080/api", "FB", "XHR")
	f.await(func() bool { return f.count("Fetch.continueRequest") == 3 }, "tab B's frame allowed")
	if f.count("Fetch.failRequest") != 3 {
		t.Errorf("refusals = %d, want exactly the three from tab A and the orphan", f.count("Fetch.failRequest"))
	}
}

// A refusal inside a frame is attributed to the tab that owns the frame, not to
// whichever tab happens to be active.
func TestGuardAttributesFrameRefusalsToTheOwningTab(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.onBrowserAttach = func(f *fakeDevTools) { f.attach("SA", "TA", "page", false) }
	pol := testPolicy(nil)
	var got blockRec
	done := make(chan struct{}, 1)
	g, err := startGuard(context.Background(), dir, pol, func(tid, kind, url string, err error) {
		got = blockRec{tid, kind, url}
		done <- struct{}{}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)
	f.attachChild("SA", "SA-frame", "FA", "iframe")
	f.await(func() bool { return indexOf(f.seq("SA-frame"), "Fetch.enable") >= 0 }, "frame guarded")

	f.pausedAs("SA-frame", "r", "http://169.254.169.254/", "FA", "XHR")
	<-done
	if got.target != "TA" || got.kind != "page" {
		t.Errorf("refusal attributed to %+v, want the owning page TA", got)
	}
}
