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

// Smoke test of the tab cap against a real browser. It cannot prove the
// in-flight accounting: Chrome's popup blocker lets only one window.open
// through per click, so a true burst never arrives. That accounting is covered
// directly by TestReserveTabCountsTabsStillBeingSetUp.
func TestIntegration_PopupBurstRespectsTheTabCap(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/bomb" {
			_, _ = fmt.Fprint(w, `<button id="go" onclick="for (let i=0;i<60;i++) window.open('/leaf?'+i)">go</button>`)
			return
		}
		_, _ = fmt.Fprint(w, "<title>leaf</title>leaf")
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL+"/bomb", "load"); err != nil {
		t.Fatal(err)
	}
	_ = m.Click(ctx, "#go")
	time.Sleep(3 * time.Second)
	tabs, _ := m.Tabs(ctx)
	if len(tabs) > MaxTabs {
		t.Errorf("tracked %d tabs from a 60-popup burst; the cap is %d", len(tabs), MaxTabs)
	}
}

// Refs address the top document only, so an inspect scoped to a frame must not
// hand out refs that could never be used.
func TestIntegration_FrameScopedInspectHandsOutNoRefs(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newCapabilitiesServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	inFrame, err := m.Inspect(ctx, "iframe#frame >>> body")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inFrame, "[ref=") {
		t.Errorf("a frame-scoped inspect handed out refs:\n%s", inFrame)
	}
	top, err := m.Inspect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(top, "[ref=") {
		t.Errorf("a top-level inspect must still hand out refs:\n%s", top)
	}
}
