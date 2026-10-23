//go:build integration

package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Ordinary pages redirect. networkidle must not mistake the finished chain for
// a request still in flight and sit out its whole 10 second cap.
func TestIntegration_NetworkIdleSurvivesRedirects(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hop1":
			http.Redirect(w, r, "/hop2", http.StatusFound)
		case "/hop2":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			_, _ = fmt.Fprint(w, `{"ok":true}`)
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<script>fetch('/hop1').then(r => r.text());</script>page`)
		}
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	start := time.Now()
	if _, err := m.Navigate(ctx, srv.URL, "networkidle"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("networkidle took %s on a page whose redirects had finished; it ran into the cap", d)
	}

	// And the body lookup must resolve to the final hop, not the first.
	reqs, _ := m.NetworkRequests(ctx, 0)
	var last NetworkEntry
	for _, r := range reqs {
		if strings.Contains(r.URL, "/final") {
			last = r
		}
	}
	if last.RequestID == "" {
		t.Fatalf("final hop not recorded: %+v", reqs)
	}
	body, err := m.ResponseBody(ctx, last.RequestID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(body.URL, "/final") || body.Status != 200 {
		t.Errorf("body resolved to %s (%d), want the final hop", body.URL, body.Status)
	}
}

// A ref number is reused after every full inspect. The old tag must go, and a
// page must not be able to pre-plant the attribute to steal a ref.
func TestIntegration_RefsCannotCollideOrBePlanted(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// The decoy comes first in DOM order and carries the attribute value of the ref the test is about to use (Alpha is e2); a naive
		// implementation would match it first.
		_, _ = fmt.Fprint(w, `<button data-yottacode-ref="e2" onclick="document.getElementById('out').textContent='DECOY'">Decoy</button>
<button id=a onclick="document.getElementById('out').textContent='A'">Alpha</button>
<button id=b onclick="document.getElementById('out').textContent='B'">Beta</button><div id=out></div>`)
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}

	tree, _ := m.Inspect(ctx, "")
	if err := m.Click(ctx, "ref="+refFor(t, tree, "Alpha")); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "", "A", false, 5*time.Second); err != nil {
		t.Fatalf("clicking Alpha's ref did not click Alpha: %v", err)
	}

	// Inspect again: numbering restarts, so e-numbers now name different nodes.
	tree2, _ := m.Inspect(ctx, "")
	if err := m.Click(ctx, "ref="+refFor(t, tree2, "Beta")); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "", "B", false, 5*time.Second); err != nil {
		t.Fatalf("a reused ref number hit the wrong element: %v", err)
	}
}
