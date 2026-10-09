//go:build integration

package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A page the agent opened (even one on a dev server it was allowed to visit)
// must not be able to make the browser reach cloud metadata or the private
// network, whether by redirect or by its own subresource/fetch requests. The
// URL policy only ever saw the URL the agent typed; the request-level network
// policy sees everything after.
func TestIntegration_NetworkPolicyBlocksRedirectsAndSubrequests(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect-metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/redirect-private":
			http.Redirect(w, r, "http://10.255.255.1/admin", http.StatusFound)
		case "/probe":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<title>probe</title><div id=out>pending</div><script>
Promise.allSettled([
  fetch('http://169.254.169.254/latest/meta-data/').then(()=> 'meta-ok', ()=> 'meta-blocked'),
  fetch('http://10.255.255.1/').then(()=> 'priv-ok', ()=> 'priv-blocked'),
  fetch('/self').then(r=>r.text()).then(()=> 'self-ok', ()=> 'self-blocked'),
]).then(r => { document.getElementById('out').textContent = r.map(x=>x.value).join(','); });
</script>`)
		case "/self":
			_, _ = fmt.Fprint(w, "hi")
		default:
			_, _ = fmt.Fprint(w, "<title>ok</title>")
		}
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	for _, path := range []string{"/redirect-metadata", "/redirect-private"} {
		_, err := m.Navigate(ctx, srv.URL+path, "load")
		if !errors.Is(err, ErrBlockedURL) {
			t.Errorf("Navigate(%s) = %v, want ErrBlockedURL from the network policy", path, err)
		}
	}

	if _, err := m.Navigate(ctx, srv.URL+"/probe", "load"); err != nil {
		t.Fatalf("Navigate /probe: %v", err)
	}
	if err := m.Wait(ctx, "", "self-ok", false, 10*time.Second); err != nil {
		t.Fatalf("probe never finished: %v", err)
	}
	out, err := m.Inspect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "meta-blocked,priv-blocked,self-ok") {
		t.Errorf("probe result missing; want metadata+private blocked and same-origin allowed, got:\n%s", out)
	}
	logs, _ := m.ConsoleLogs(ctx, 0)
	var sawBlocked bool
	for _, l := range logs {
		if l.Level == "blocked" {
			sawBlocked = true
		}
	}
	if !sawBlocked {
		t.Errorf("blocked requests should be visible to the agent in console logs; got %+v", logs)
	}
}
