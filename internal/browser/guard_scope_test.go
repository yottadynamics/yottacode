package browser

import (
	"context"
	"testing"
)

func (f *fakeDevTools) pausedAs(session, reqID, url, frameID, resourceType string) {
	f.event("Fetch.requestPaused", session, map[string]any{
		"requestId": reqID, "frameId": frameID, "resourceType": resourceType,
		"request": map[string]any{"url": url},
	})
}

// End to end through the guard: a dev page is opened explicitly, the page then
// goes public, and from that moment the public page must not reach localhost —
// the earlier visit is not a standing grant.
func TestGuardLocalAccessEndsWhenThePageGoesPublic(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.onBrowserAttach = func(f *fakeDevTools) { f.attach("S1", "T1", "page", false) }
	pol := testPolicy(map[string][]string{"example.com": {"93.184.216.34"}})
	pol.allowExplicit("http://127.0.0.1:3000/")
	g, err := startGuard(context.Background(), dir, pol, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)

	verdicts := func() (fails, conts int) { return f.count("Fetch.failRequest"), f.count("Fetch.continueRequest") }

	// The page's own requests only start once its document has been allowed.
	f.pausedAs("S1", "nav1", "http://127.0.0.1:3000/", "T1", "Document")
	f.await(func() bool { _, c := verdicts(); return c == 1 }, "dev page allowed")
	f.pausedAs("S1", "api1", "http://127.0.0.1:8080/api", "T1", "XHR")
	f.await(func() bool { _, c := verdicts(); return c == 2 }, "its API call allowed")

	f.pausedAs("S1", "nav2", "https://example.com/", "T1", "Document")
	f.await(func() bool { _, c := verdicts(); return c == 3 }, "public navigation allowed")

	f.pausedAs("S1", "probe", "http://127.0.0.1:9222/json", "T1", "XHR")
	f.await(func() bool { fl, _ := verdicts(); return fl == 1 }, "local probe from the public page refused")
	if _, c := verdicts(); c != 3 {
		t.Errorf("the probe must not have been continued (continues=%d)", c)
	}
}

// A subframe's Document request is not a page navigation: it must not move the
// page's location (which would let a public iframe on a dev page mask it).
func TestGuardSubframeDocumentIsNotAPageNavigation(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.onBrowserAttach = func(f *fakeDevTools) { f.attach("S1", "T1", "page", false) }
	pol := testPolicy(map[string][]string{"example.com": {"93.184.216.34"}})
	pol.allowExplicit("http://127.0.0.1:3000/")
	g, err := startGuard(context.Background(), dir, pol, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)

	f.pausedAs("S1", "nav", "http://127.0.0.1:3000/", "T1", "Document")
	f.await(func() bool { return f.count("Fetch.continueRequest") == 1 }, "dev page allowed")
	f.pausedAs("S1", "frame", "https://example.com/widget", "FRAME-1", "Document") // an iframe, not the page
	f.await(func() bool { return f.count("Fetch.continueRequest") == 2 }, "widget allowed")
	f.pausedAs("S1", "api", "http://127.0.0.1:8080/api", "T1", "XHR")
	f.await(func() bool { return f.count("Fetch.continueRequest") == 3 }, "API call allowed")
	if f.count("Fetch.failRequest") != 0 {
		t.Error("an embedded public widget must not cut the dev page off from its own API")
	}
}
