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

const capabilitiesPage = `<!DOCTYPE html><html><head><title>Caps</title></head><body>
<button id="a" onclick="document.getElementById('out').textContent='A'">Alpha</button>
<button id="b" onclick="document.getElementById('out').textContent='B'">Beta</button>
<select id="size" onchange="document.getElementById('out').textContent='size:'+this.value">
  <option value="s">Small</option><option value="m">Medium</option><option value="l">Large</option>
</select>
<iframe id="frame" src="/frame"></iframe>
<div id="out"></div>
<script>
fetch('/api/data').then(r => r.text());
document.getElementById('a').addEventListener('dblclick', () => {});
</script></body></html>`

const framePage = `<!DOCTYPE html><html><body>
<input id="card" type="text">
<button id="pay" onclick="document.getElementById('res').textContent='paid:'+document.getElementById('card').value">Pay</button>
<div id="res"></div></body></html>`

func newCapabilitiesServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/frame":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, framePage)
		case "/api/data":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"secret":"value-123"}`)
		case "/dialogs":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<button id="go" onclick="var c = confirm('really?'); document.getElementById('out').textContent = 'confirm:' + c">go</button><div id="out"></div>`)
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, capabilitiesPage)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func refFor(t *testing.T, tree, name string) string {
	t.Helper()
	for _, line := range strings.Split(tree, "\n") {
		if strings.Contains(line, `"`+name+`"`) {
			if i := strings.Index(line, "[ref="); i >= 0 {
				return strings.TrimSuffix(line[i+len("[ref="):], "]")
			}
		}
	}
	t.Fatalf("no ref for %q in:\n%s", name, tree)
	return ""
}

func TestIntegration_RefsAddressTheExactElement(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newCapabilitiesServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	tree, err := m.Inspect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	beta := "ref=" + refFor(t, tree, "Beta")
	if err := m.Click(ctx, beta); err != nil {
		t.Fatalf("click by ref: %v", err)
	}
	if err := m.Wait(ctx, "", "B", false, 5*time.Second); err != nil {
		t.Fatalf("ref click did not hit Beta: %v", err)
	}

	// A ref the last snapshot never issued, and a ref from before a navigation,
	// must fail loudly rather than address some other node.
	if err := m.Click(ctx, "ref=e999"); err == nil || !strings.Contains(err.Error(), "browser_inspect") {
		t.Errorf("unknown ref error = %v, want hint to re-inspect", err)
	}
	if _, err := m.Navigate(ctx, srv.URL+"/dialogs", "load"); err != nil {
		t.Fatal(err)
	}
	if err := m.Click(ctx, beta); err == nil {
		t.Error("stale ref after navigation must not resolve")
	}
}

func TestIntegration_IframeSelectors(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newCapabilitiesServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	if err := m.Type(ctx, "iframe#frame >>> #card", "4242", false); err != nil {
		t.Fatalf("type into iframe: %v", err)
	}
	if err := m.Click(ctx, "iframe#frame >>> #pay"); err != nil {
		t.Fatalf("click in iframe: %v", err)
	}
	if err := m.Wait(ctx, "iframe#frame >>> #res", "", false, 5*time.Second); err != nil {
		t.Fatalf("iframe action did not land: %v", err)
	}
	old := selectorLookupTimeout
	selectorLookupTimeout = time.Second
	defer func() { selectorLookupTimeout = old }()
	start := time.Now()
	if err := m.Click(ctx, "iframe#nope >>> #pay"); !errors.Is(err, ErrSelectorNotFound) {
		t.Errorf("missing iframe error = %v, want ErrSelectorNotFound", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("missing iframe took %s to fail; lookups must be bounded", d)
	}
}

func TestIntegration_SelectOption(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newCapabilitiesServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	got, err := m.SelectOption(ctx, "#size", "", "Medium")
	if err != nil || got != "Medium" {
		t.Fatalf("select by label = %q, %v", got, err)
	}
	if err := m.Wait(ctx, "", "size:m", false, 5*time.Second); err != nil {
		t.Fatalf("change event did not fire: %v", err)
	}
	if _, err := m.SelectOption(ctx, "#size", "l", ""); err != nil {
		t.Fatalf("select by value: %v", err)
	}
	if _, err := m.SelectOption(ctx, "#size", "xl", ""); err == nil || !strings.Contains(err.Error(), "available") {
		t.Errorf("unknown option error = %v, want the available options listed", err)
	}
	if _, err := m.SelectOption(ctx, "#a", "x", ""); err == nil || !strings.Contains(err.Error(), "not a <select>") {
		t.Errorf("non-select error = %v", err)
	}
}

func TestIntegration_ResponseBody(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newCapabilitiesServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "networkidle"); err != nil {
		t.Fatal(err)
	}
	reqs, _ := m.NetworkRequests(ctx, 0)
	var id string
	for _, r := range reqs {
		if strings.HasSuffix(r.URL, "/api/data") {
			id = r.RequestID
		}
	}
	if id == "" {
		t.Fatalf("api request not captured: %+v", reqs)
	}
	body, err := m.ResponseBody(ctx, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Body, "value-123") || body.Binary {
		t.Errorf("body = %+v", body)
	}
	if small, _ := m.ResponseBody(ctx, id, 5, 0); !small.Truncated || len(small.Body) != 5 {
		t.Errorf("truncation: %+v", small)
	}
	if _, err := m.ResponseBody(ctx, "nope", 0, 0); err == nil {
		t.Error("unknown request id must error")
	}
}

func TestIntegration_DialogPolicyIsRecorded(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newCapabilitiesServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL+"/dialogs", "load"); err != nil {
		t.Fatal(err)
	}
	if err := m.Click(ctx, "#go"); err != nil {
		t.Fatal(err)
	}
	// confirm() is cancelled: the page must see false, never an answer made on
	// the user's behalf.
	if err := m.Wait(ctx, "", "confirm:false", false, 5*time.Second); err != nil {
		t.Fatalf("confirm should be cancelled: %v", err)
	}
	logs, _ := m.ConsoleLogs(ctx, 0)
	var saw bool
	for _, l := range logs {
		if l.Level == "dialog" && strings.Contains(l.Text, "cancelled") && strings.Contains(l.Text, "really?") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("dialog not recorded in console logs: %+v", logs)
	}
}

func TestIntegration_PopupBombIsCapped(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/bomb" {
			_, _ = fmt.Fprint(w, `<button id="go" onclick="for (let i=0;i<20;i++) window.open('/leaf?'+i)">go</button>`)
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
	time.Sleep(2 * time.Second)
	tabs, _ := m.Tabs(ctx)
	if len(tabs) > MaxTabs {
		t.Errorf("tracked %d tabs, cap is %d", len(tabs), MaxTabs)
	}
}
