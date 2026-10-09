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

// networkidle used to mean "document.readyState is complete", so a page that
// fetches data after load looked idle immediately.
func TestIntegration_NetworkIdleWaitsForInFlightRequests(t *testing.T) {
	skipIfNoBrowser(t)
	const delay = 1500 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			time.Sleep(delay)
			_, _ = fmt.Fprint(w, "late")
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<div id=out>waiting</div><script>
setTimeout(() => fetch('/slow').then(r => r.text()).then(t => document.getElementById('out').textContent = t), 100);
</script>`)
		}
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL, "networkidle"); err != nil {
		t.Fatal(err)
	}
	out, err := m.Inspect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "late") {
		t.Errorf("networkidle returned before the in-flight fetch finished:\n%s", out)
	}
}

func TestIntegration_WaitForTextInsideIframe(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newCapabilitiesServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	if err := m.Type(ctx, "iframe#frame >>> #card", "9999", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Click(ctx, "iframe#frame >>> #pay"); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "iframe#frame >>> #res", "paid:9999", false, 5*time.Second); err != nil {
		t.Fatalf("text inside the iframe not found: %v", err)
	}
	if err := m.Wait(ctx, "iframe#frame >>> #res", "never-appears", false, time.Second); err == nil {
		t.Error("missing text must time out")
	}
}
