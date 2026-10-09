//go:build integration

package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A cross-site iframe lives in its own renderer process (an OOPIF), which a
// per-page interceptor never sees. The guard attaches to it before it runs, so
// its requests are vetted like any other — including ones fired by the very
// first script, which is what a late attach loses the race against.
func TestIntegration_CrossSiteIframeRequestsAreVetted(t *testing.T) {
	skipIfNoBrowser(t)
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<script>
fetch('http://169.254.169.254/latest/meta-data/iframe-probe').catch(()=>{});
new Image().src = 'http://10.255.255.1/iframe-img';
</script>inner`)
	}))
	t.Cleanup(inner.Close)
	// "localhost" vs "127.0.0.1" are different sites, so with site isolation on
	// Chrome puts the frame in its own process.
	innerURL := strings.Replace(inner.URL, "127.0.0.1", "localhost", 1)
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<iframe src="%s"></iframe>outer`, innerURL)
	}))
	t.Cleanup(outer.Close)

	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	// The unroutable <img> would hold the load event for a minute if it were
	// not refused, so a prompt load is itself part of the assertion.
	start := time.Now()
	if _, err := m.Navigate(ctx, outer.URL, "load"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("load took %s; the iframe's private-network request was not refused", d)
	}
	time.Sleep(time.Second)
	logs, _ := m.ConsoleLogs(ctx, 0)
	var meta, img bool
	for _, l := range logs {
		if l.Level != "blocked" {
			continue
		}
		meta = meta || strings.Contains(l.Text, "iframe-probe")
		img = img || strings.Contains(l.Text, "iframe-img")
	}
	if !meta || !img {
		t.Errorf("iframe requests not refused: metadata=%v private=%v; logs=%+v", meta, img, logs)
	}
}

// Popups are new targets too: they must come up guarded.
func TestIntegration_PopupRequestsAreVetted(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/popup":
			_, _ = fmt.Fprint(w, `<script>fetch('http://169.254.169.254/popup-probe').catch(()=>{});</script>popup`)
		default:
			_, _ = fmt.Fprint(w, `<a id="l" href="/popup" target="_blank">open</a>`)
		}
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	if err := m.Click(ctx, "#l"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	logs, _ := m.ConsoleLogs(ctx, 0) // the popup became the active tab
	for _, l := range logs {
		if l.Level == "blocked" && strings.Contains(l.Text, "popup-probe") {
			return
		}
	}
	t.Errorf("popup's metadata request was not refused: %+v", logs)
}

// If the guard's connection drops, the policy is no longer enforced, so the
// session must report itself dead (and relaunch guarded) rather than carry on.
func TestIntegration_SessionDiesWhenGuardDies(t *testing.T) {
	skipIfNoBrowser(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, "about:blank", "load"); err != nil {
		t.Fatal(err)
	}
	s := m.sess.(*session)
	if !s.alive() {
		t.Fatal("fresh session should be alive")
	}
	pid := s.pid
	s.guard.close() // simulates the connection being lost
	if s.alive() {
		t.Error("session must not be alive without its guard")
	}
	// Not only "reports dead": the browser itself must stop at once, so pages
	// that are already open cannot keep running unvetted until the next action.
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("browser process %d still running 10s after the guard was lost", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := m.Navigate(ctx, "about:blank", "load"); err != nil {
		t.Errorf("next action should relaunch a guarded session: %v", err)
	}
}
