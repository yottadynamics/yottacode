//go:build integration

package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// serveSite serves several documents by path and opens "/" in a fresh Manager.
func serveSite(t *testing.T, pages map[string]string) (*Manager, context.Context) {
	t.Helper()
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	return m, ctx
}

func refForNode(t *testing.T, tree, role, name string) string {
	t.Helper()
	r := regexp.MustCompile(role + ` "` + regexp.QuoteMeta(name) + `"[^\n]*\[ref=(e\d+)\]`).FindStringSubmatch(tree)
	if r == nil {
		t.Fatalf("no %s %q with a ref in:\n%s", role, name, tree)
	}
	return r[1]
}

var frameSite = map[string]string{
	"/": `<button>Top</button><iframe id=pay src="/frame" width=400 height=200></iframe>`,
	"/frame": `<h1>Payment</h1><p>Enter details.</p><button id=fb onclick="document.getElementById('out').textContent='frame-clicked'">Pay now</button>
<output id=out></output><iframe id=inner src="/deep" width=300 height=100></iframe>`,
	"/deep": `<a href="#" id=dl onclick="document.getElementById('o2').textContent='deep-clicked';return false">Deep link</a><output id=o2></output>`,
}

func TestIntegration_RefsReachIntoIframes(t *testing.T) {
	m, ctx := serveSite(t, frameSite)
	tree, err := m.Inspect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`button "Top"`, `button "Pay now"`, `link "Deep link"`} {
		if !strings.Contains(tree, want) {
			t.Fatalf("frame content missing %q:\n%s", want, tree)
		}
	}
	// A plain ref is enough, whatever depth the element is at.
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "button", "Pay now")); err != nil {
		t.Fatalf("click ref in iframe: %v", err)
	}
	if err := m.Wait(ctx, "iframe#pay >>> #out", "frame-clicked", false, 5*time.Second); err != nil {
		t.Fatalf("iframe click had no effect: %v", err)
	}
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "link", "Deep link")); err != nil {
		t.Fatalf("click ref in nested iframe: %v", err)
	}
	if err := m.Wait(ctx, "iframe#pay >>> iframe#inner >>> #o2", "deep-clicked", false, 5*time.Second); err != nil {
		t.Fatalf("nested iframe click had no effect: %v", err)
	}
	// The top-document element still works alongside.
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "button", "Top")); err != nil {
		t.Fatal(err)
	}
}

func TestIntegration_FindAndExtractInsideIframe(t *testing.T) {
	m, ctx := serveSite(t, frameSite)
	out, err := m.Find(ctx, "pay now", "button", 0)
	if err != nil || !strings.Contains(out, `button "Pay now"`) || !strings.Contains(out, "[ref=e") {
		t.Fatalf("find in iframe: %v\n%s", err, out)
	}
	md, err := m.Extract(ctx, "iframe#pay >>>", 0)
	if err != nil || !strings.Contains(md, "# Payment") || !strings.Contains(md, "Enter details.") {
		t.Fatalf("extract frame body: %v\n%s", err, md)
	}
	md, err = m.Extract(ctx, "iframe#pay >>> #fb", 0)
	if err != nil || !strings.Contains(md, "Pay now") || strings.Contains(md, "Payment") {
		t.Fatalf("extract frame element: %v\n%s", err, md)
	}
	if main, _ := m.Extract(ctx, "", 0); strings.Contains(main, "Payment") {
		t.Errorf("page extract must not include frame content:\n%s", main)
	}
}

func TestIntegration_ExtractByRefInsideIframe(t *testing.T) {
	m, ctx := serveSite(t, frameSite)
	tree, _ := m.Inspect(ctx, "")
	md, err := m.Extract(ctx, "ref="+refForNode(t, tree, "button", "Pay now"), 0)
	if err != nil || !strings.Contains(md, "Pay now") {
		t.Fatalf("extract by ref: %v\n%s", err, md)
	}
}

const rerenderPage = `<button id=rerender onclick="
 var s=document.getElementById('save'); var n=document.createElement('button'); n.id='save'; n.textContent='Save';
 n.onclick=function(){document.title='saved'}; s.replaceWith(n);">Rerender</button>
<button id=save onclick="document.title='saved-old'">Save</button><output id=o></output>`

func TestIntegration_StaleRefRecoversWhenUnique(t *testing.T) {
	m, ctx := serveSite(t, map[string]string{"/": rerenderPage})
	tree, _ := m.Inspect(ctx, "")
	save, rerender := refForNode(t, tree, "button", "Save"), refForNode(t, tree, "button", "Rerender")
	if err := m.Click(ctx, "ref="+rerender); err != nil {
		t.Fatal(err)
	}
	// The element behind `save` was replaced. Before recovery this failed as stale.
	if err := m.Click(ctx, "ref="+save); err != nil {
		t.Fatalf("stale ref should be re-resolved by role+name: %v", err)
	}
	if err := m.Wait(ctx, "", "", false, 50*time.Millisecond); err != nil {
		_ = err
	}
	d, _ := m.PageDigest(ctx, true)
	if d.Title != "saved" {
		t.Fatalf("title = %q; the click must hit the NEW button", d.Title)
	}
}

const twinPage = `<button id=r onclick="
 var box=document.getElementById('box'); box.innerHTML='';
 for (var i=1;i<=%d;i++){var b=document.createElement('button');b.textContent='Delete';(function(i){b.onclick=function(){document.title='del'+i}})(i);box.appendChild(b);}">Rerender</button>
<div id=box><button onclick="document.title='old1'">Delete</button><button onclick="document.title='old2'">Delete</button></div>`

func TestIntegration_StaleRefPicksSamePositionWhenCountUnchanged(t *testing.T) {
	m, ctx := serveSite(t, map[string]string{"/": fmt.Sprintf(twinPage, 2)})
	tree, _ := m.Inspect(ctx, "")
	deletes := regexp.MustCompile(`button "Delete" \[ref=(e\d+)\]`).FindAllStringSubmatch(tree, -1)
	if len(deletes) != 2 {
		t.Fatalf("want 2 Delete refs:\n%s", tree)
	}
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "button", "Rerender")); err != nil {
		t.Fatal(err)
	}
	if err := m.Click(ctx, "ref="+deletes[1][1]); err != nil {
		t.Fatalf("recovery by position: %v", err)
	}
	if d, _ := m.PageDigest(ctx, true); d.Title != "del2" {
		t.Fatalf("title = %q, want del2 (the second button)", d.Title)
	}
}

func TestIntegration_StaleRefRefusesWhenAmbiguous(t *testing.T) {
	// After the rerender there are 3 look-alikes where the snapshot had 2: which
	// one the ref meant is unknowable, so it must fail rather than guess.
	m, ctx := serveSite(t, map[string]string{"/": fmt.Sprintf(twinPage, 3)})
	tree, _ := m.Inspect(ctx, "")
	deletes := regexp.MustCompile(`button "Delete" \[ref=(e\d+)\]`).FindAllStringSubmatch(tree, -1)
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "button", "Rerender")); err != nil {
		t.Fatal(err)
	}
	err := m.Click(ctx, "ref="+deletes[0][1])
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("ambiguous stale ref must fail as stale, got %v", err)
	}
}

func TestIntegration_StaleRefGoneFailsClearly(t *testing.T) {
	m, ctx := serveSite(t, map[string]string{"/": `<button id=x onclick="this.remove()">Vanish</button><button>Other</button>`})
	tree, _ := m.Inspect(ctx, "")
	vanish := refForNode(t, tree, "button", "Vanish")
	if err := m.Click(ctx, "ref="+vanish); err != nil {
		t.Fatal(err)
	}
	err := m.Click(ctx, "ref="+vanish)
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("got %v", err)
	}
}

// A cross-site iframe lives in its own process. Its content is read through the
// guard's session for that target; its node ids belong to that process, so they
// must never become refs.
func TestIntegration_CrossOriginIframeContentIsReadableWithoutRefs(t *testing.T) {
	skipIfNoBrowser(t)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<h1>Payment widget</h1><p>Card number goes here</p><button>Pay 20 dollars</button>`)
	}))
	t.Cleanup(other.Close)
	// localhost and 127.0.0.1 are different sites, so Chrome isolates the frame.
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	m, ctx := servePage(t, `<main><button onclick="document.title='top-hit'">Top</button><iframe src="`+otherURL+`"></iframe></main>`)
	tree, err := m.Inspect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`heading "Payment widget"`, `"Card number goes here"`, `button "Pay 20 dollars"`, "browser_handoff"} {
		if !strings.Contains(tree, want) {
			t.Errorf("cross-origin frame content missing %q:\n%s", want, tree)
		}
	}
	if strings.Contains(tree, `"Pay 20 dollars" [ref=`) {
		t.Errorf("a cross-process element must not get a ref:\n%s", tree)
	}
	// The page's own refs are untouched by the grafted frame.
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "button", "Top")); err != nil {
		t.Fatal(err)
	}
	if d, _ := m.PageDigest(ctx, true); d.Title != "top-hit" {
		t.Fatalf("title = %q", d.Title)
	}
	f, err := m.Find(ctx, "pay", "button", 0)
	if err != nil || !strings.Contains(f, `button "Pay 20 dollars"`) || strings.Contains(f, "[ref=") {
		t.Fatalf("find in a cross-origin frame: %v\n%s", err, f)
	}
}
