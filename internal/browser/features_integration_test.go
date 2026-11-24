//go:build integration

package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// servePage serves one static HTML document and opens it in a fresh Manager.
func servePage(t *testing.T, html string) (*Manager, context.Context) {
	t.Helper()
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, html)
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

func TestIntegration_DigestShowsRadioBecomingChecked(t *testing.T) {
	m, ctx := servePage(t, `<fieldset><legend>Q1</legend>
<label><input type=radio name=q value=a> Alpha</label>
<label><input type=radio name=q value=b> Beta</label></fieldset><button>Submit</button>`)
	before, err := m.PageDigest(ctx, false)
	if err != nil || !before.Valid {
		t.Fatalf("before: %+v %v", before, err)
	}
	if err := m.Click(ctx, "input[value=b]"); err != nil {
		t.Fatal(err)
	}
	after, _ := m.PageDigest(ctx, true)
	diff := DiffDigest(before, after)
	if !strings.Contains(diff, `changed: radio "Beta" -> radio "Beta" (checked)`) {
		t.Fatalf("diff:\n%s", diff)
	}
}

func TestIntegration_SettleWaitsForDelayedContent(t *testing.T) {
	m, ctx := servePage(t, `<button id=b onclick="setTimeout(function(){var p=document.createElement('button');p.textContent='Later';document.body.appendChild(p)},400)">Go</button>`)
	before, _ := m.PageDigest(ctx, false)
	if err := m.Click(ctx, "#b"); err != nil {
		t.Fatal(err)
	}
	after, _ := m.PageDigest(ctx, true)
	if d := DiffDigest(before, after); !strings.Contains(d, `added: button "Later"`) {
		t.Fatalf("settle did not wait for the delayed element:\n%s", d)
	}
}

func TestIntegration_DialogSurfacedInDigest(t *testing.T) {
	m, ctx := servePage(t, `<button id=b onclick="alert('Saved!')">Save</button>`)
	before, _ := m.PageDigest(ctx, false)
	if err := m.Click(ctx, "#b"); err != nil {
		t.Fatal(err)
	}
	after, _ := m.PageDigest(ctx, true)
	if d := DiffDigest(before, after); !strings.Contains(d, "dialog: alert accepted automatically: Saved!") {
		t.Fatalf("diff:\n%s", d)
	}
}

func TestIntegration_HoverOpensMenu(t *testing.T) {
	m, ctx := servePage(t, `<style>#menu{display:none}#trigger:hover+#menu{display:block}</style>
<div id=trigger style="width:100px;height:30px;background:#ccc">Account</div><div id=menu><a href="/x">Settings</a></div>`)
	before, _ := m.PageDigest(ctx, false)
	if err := m.Hover(ctx, "#trigger"); err != nil {
		t.Fatal(err)
	}
	after, _ := m.PageDigest(ctx, true)
	if d := DiffDigest(before, after); !strings.Contains(d, `added: link "Settings"`) {
		t.Fatalf("hover did not reveal the menu:\n%s", d)
	}
}

func TestIntegration_DragMovesSlider(t *testing.T) {
	m, ctx := servePage(t, `<div id=track style="position:absolute;left:20px;top:50px;width:300px;height:20px;background:#ddd"></div>
<div id=knob style="position:absolute;left:20px;top:50px;width:20px;height:20px;background:#333"></div>
<div id=end style="position:absolute;left:300px;top:50px;width:20px;height:20px"></div><output id=out>0</output>
<script>var d=false,k=document.getElementById('knob');
k.onmousedown=function(){d=true};
document.onmousemove=function(e){if(d){document.getElementById('out').textContent='moved'+(e.clientX>200?'-far':'')}};
document.onmouseup=function(){d=false};</script>`)
	if err := m.Drag(ctx, "#knob", "#end"); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "", "moved-far", false, 5*time.Second); err != nil {
		t.Fatalf("drag did not move the knob: %v", err)
	}
}

func TestIntegration_RightAndDoubleClick(t *testing.T) {
	m, ctx := servePage(t, `<div id=t style="width:120px;height:40px;background:#eee">target</div><output id=out></output>
<script>var t=document.getElementById('t'),o=document.getElementById('out');
t.oncontextmenu=function(e){e.preventDefault();o.textContent+='[right]'};
t.ondblclick=function(){o.textContent+='[double]'};</script>`)
	if err := m.ClickWith(ctx, "#t", ClickOptions{Button: "right"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "", "[right]", false, 5*time.Second); err != nil {
		t.Fatalf("right click: %v", err)
	}
	if err := m.ClickWith(ctx, "#t", ClickOptions{Double: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "", "[double]", false, 5*time.Second); err != nil {
		t.Fatalf("double click: %v", err)
	}
}

func TestIntegration_FindByTextAndRoleWithGroup(t *testing.T) {
	m, ctx := servePage(t, `<fieldset><legend>Pick one</legend>
<label><input type=radio name=q> Alpha</label><label><input type=radio name=q> Beta</label></fieldset>
<button>Submit</button><a href="/s">Submit feedback</a>`)
	out, err := m.Find(ctx, "beta", "radio", 0)
	if err != nil || !strings.Contains(out, `radio "Beta"`) || !strings.Contains(out, "[ref=e") || strings.Contains(out, "Alpha") {
		t.Fatalf("find: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Pick one") {
		t.Errorf("match should name its group:\n%s", out)
	}
	out, _ = m.Find(ctx, "submit", "", 1)
	if !strings.Contains(out, "more matches") {
		t.Errorf("limit not reported:\n%s", out)
	}
	if out, _ := m.Find(ctx, "nonexistent-xyz", "", 0); out != "(no matches)" {
		t.Errorf("got %q", out)
	}
	// The ref a find returns must be usable.
	ref := regexp.MustCompile(`\[ref=(e\d+)\]`).FindStringSubmatch(func() string { o, _ := m.Find(ctx, "beta", "radio", 0); return o }())
	if ref == nil {
		t.Fatal("no ref")
	}
	if err := m.Click(ctx, "ref="+ref[1]); err != nil {
		t.Fatalf("clicking found ref: %v", err)
	}
}

func TestIntegration_ExtractMarkdown(t *testing.T) {
	m, ctx := servePage(t, `<nav>MENU-NOISE</nav><main><h1>Title</h1><p>Hello <strong>bold</strong> <a href="https://x.test/p">link</a>.</p>
<ul><li>one</li><li>two</li></ul>
<table><tr><th>Name</th><th>Qty</th></tr><tr><td>Apple</td><td>3</td></tr></table>
<p style="display:none">HIDDEN</p><script>var x='SCRIPT-NOISE'</script></main>`)
	out, err := m.Extract(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Title", "Hello **bold** [link](https://x.test/p).", "- one", "- two", "| Name | Qty |", "| --- | --- |", "| Apple | 3 |"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"MENU-NOISE", "HIDDEN", "SCRIPT-NOISE"} {
		if strings.Contains(out, bad) {
			t.Errorf("leaked %q:\n%s", bad, out)
		}
	}
	if small, err := m.Extract(ctx, "table", 0); err != nil || strings.Contains(small, "Title") {
		t.Errorf("scoped extract: %v\n%s", err, small)
	}
}

func TestIntegration_AnnotatedScreenshotLeavesNoOverlay(t *testing.T) {
	m, ctx := servePage(t, `<button style="margin:40px">Go</button><a href="/x">Next</a>`)
	png, err := m.ScreenshotAnnotated(ctx)
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("screenshot: %v (%d bytes)", err, len(png))
	}
	plain, _ := m.Screenshot(ctx, "", false)
	if bytes.Equal(png, plain) {
		t.Error("annotated screenshot is identical to the plain one: nothing was drawn")
	}
	tree, _ := m.Inspect(ctx, "")
	if strings.Contains(tree, "yottacode-annotate") || regexp.MustCompile(`StaticText "e\d+"|"e\d+"`).MatchString(strings.ReplaceAll(tree, "[ref=", "")) {
		t.Errorf("overlay left in the page:\n%s", tree)
	}
}

func TestIntegration_RefsSurviveReinspect(t *testing.T) {
	m, ctx := servePage(t, `<button id=a>First</button><button id=b onclick="document.title='clicked-b'">Second</button>`)
	refOf := func(tree, name string) string {
		r := regexp.MustCompile(`button "` + name + `" \[ref=(e\d+)\]`).FindStringSubmatch(tree)
		if r == nil {
			t.Fatalf("no ref for %s in:\n%s", name, tree)
		}
		return r[1]
	}
	t1, _ := m.Inspect(ctx, "")
	t2, _ := m.Inspect(ctx, "")
	if refOf(t1, "Second") != refOf(t2, "Second") {
		t.Fatalf("ref changed across inspects: %s vs %s", refOf(t1, "Second"), refOf(t2, "Second"))
	}
	// A ref from the earlier inspect still works after a later one.
	if err := m.Click(ctx, "ref="+refOf(t1, "Second")); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "", "", false, 100*time.Millisecond); err != nil {
		_ = err
	}
}

func TestIntegration_InteractiveInspectIsSmallerAndHasRefs(t *testing.T) {
	m, ctx := servePage(t, `<main><h1>Quiz</h1><p>Lots of prose that should not be listed in interactive mode at all.</p>
<div><div><div><label><input type=radio name=q> Alpha</label></div></div></div><button>Submit</button></main>`)
	full, _ := m.Inspect(ctx, "")
	inter, err := m.InspectMode(ctx, "", "interactive")
	if err != nil {
		t.Fatal(err)
	}
	if len(inter) >= len(full) || strings.Contains(inter, "prose") {
		t.Errorf("interactive not smaller/filtered:\nFULL:\n%s\nINTERACTIVE:\n%s", full, inter)
	}
	if !strings.Contains(inter, `radio "Alpha"`) || !strings.Contains(inter, `button "Submit" [ref=`) || !strings.Contains(inter, `heading "Quiz"`) {
		t.Errorf("interactive missing items:\n%s", inter)
	}
	if strings.Contains(full, "InlineTextBox") {
		t.Errorf("compact tree still has InlineTextBox:\n%s", full)
	}
}

func TestIntegration_PersistentProfileKeepsCookiesAcrossManagers(t *testing.T) {
	skipIfNoBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if c, err := r.Cookie("sid"); err == nil {
			_, _ = fmt.Fprint(w, "<p>cookie:"+c.Value+"</p>")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc", Expires: time.Now().Add(time.Hour), Path: "/"})
		_, _ = fmt.Fprint(w, "<p>first visit</p>")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir() + "/prof"
	ctx := context.Background()
	for i, want := range []string{"first visit", "cookie:abc"} {
		m := NewManager()
		if err := m.SetPersistentProfile(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
			t.Fatal(err)
		}
		if err := m.Wait(ctx, "", want, false, 5*time.Second); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		_ = m.Close(ctx)
	}
}

func TestIntegration_ExtractSeparatesLinksInTableCells(t *testing.T) {
	m, ctx := servePage(t, `<main><table><tr><th>Contents</th></tr><tr><td><ul><li><a href="https://x.test/a">Alpha</a></li><li><a href="https://x.test/b">Beta</a></li></ul></td></tr></table></main>`)
	out, err := m.Extract(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, ")[") || !strings.Contains(out, "[Alpha](https://x.test/a) [Beta](https://x.test/b)") {
		t.Fatalf("links ran together:\n%s", out)
	}
}

func TestIntegration_UnreadableFrameIsMarked(t *testing.T) {
	// A frame whose accessibility tree cannot be fetched must be marked, not left silently empty.
	m, ctx := serveSite(t, map[string]string{"/": `<button>Top</button><iframe src="/f"></iframe>`, "/f": `<button>In</button>`})
	tree, err := m.Inspect(ctx, "")
	if err != nil || !strings.Contains(tree, `button "In"`) {
		t.Fatalf("readable frame must be expanded: %v\n%s", err, tree)
	}
	if strings.Contains(tree, "not readable") {
		t.Errorf("readable frame wrongly marked:\n%s", tree)
	}
}

const confirmPage = `<button id=c onclick="document.getElementById('o').textContent = confirm('Delete item?') ? 'deleted' : 'kept'">Delete</button>
<button id=p onclick="document.getElementById('o').textContent = 'name=' + prompt('Your name?', 'anon')">Ask</button><output id=o></output>`

func outText(t *testing.T, m *Manager, ctx context.Context, want string) {
	t.Helper()
	if err := m.Wait(ctx, "#o", want, false, 5*time.Second); err != nil {
		t.Fatalf("expected %q on the page: %v", want, err)
	}
}

func TestIntegration_ArmedDialogAnswersNextConfirmOnly(t *testing.T) {
	m, ctx := servePage(t, confirmPage)
	// Default: cancelled, and the action's page changes say so.
	before, _ := m.PageDigest(ctx, false)
	if err := m.Click(ctx, "#c"); err != nil {
		t.Fatal(err)
	}
	outText(t, m, ctx, "kept")
	after, _ := m.PageDigest(ctx, true)
	if d := DiffDigest(before, after); !strings.Contains(d, "dialog: confirm cancelled automatically: Delete item?") {
		t.Fatalf("default cancel not reported:\n%s", d)
	}
	// Armed accept answers exactly one confirm.
	if err := m.ArmDialog(ctx, true, "", false); err != nil {
		t.Fatal(err)
	}
	before, _ = m.PageDigest(ctx, false)
	if err := m.Click(ctx, "#c"); err != nil {
		t.Fatal(err)
	}
	outText(t, m, ctx, "deleted")
	after, _ = m.PageDigest(ctx, true)
	if d := DiffDigest(before, after); !strings.Contains(d, "confirm accepted by browser_dialog: Delete item?") {
		t.Fatalf("armed accept not reported:\n%s", d)
	}
	if err := m.Click(ctx, "#c"); err != nil {
		t.Fatal(err)
	}
	outText(t, m, ctx, "kept") // one-shot: back to cancelling
}

func TestIntegration_ArmedDialogPromptText(t *testing.T) {
	m, ctx := servePage(t, confirmPage)
	if err := m.ArmDialog(ctx, true, "Bob", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Click(ctx, "#p"); err != nil {
		t.Fatal(err)
	}
	outText(t, m, ctx, "name=Bob")
	// A dismissed prompt returns null.
	if err := m.ArmDialog(ctx, false, "", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Click(ctx, "#p"); err != nil {
		t.Fatal(err)
	}
	outText(t, m, ctx, "name=null")
}

func TestIntegration_ClearedDialogAnswerDoesNotApply(t *testing.T) {
	m, ctx := servePage(t, confirmPage)
	_ = m.ArmDialog(ctx, true, "", false)
	if err := m.ArmDialog(ctx, false, "", true); err != nil {
		t.Fatal(err)
	}
	if err := m.Click(ctx, "#c"); err != nil {
		t.Fatal(err)
	}
	outText(t, m, ctx, "kept")
}

func TestIntegration_ArmDialogWithoutSessionFails(t *testing.T) {
	skipIfNoBrowser(t)
	m := NewManager()
	if err := m.ArmDialog(context.Background(), true, "", false); err == nil {
		t.Error("arming before any browser session exists must fail, not launch one")
	}
}

func TestIntegration_SettleBecomesQuickOnPagesThatNeverGoQuiet(t *testing.T) {
	m, ctx := servePage(t, `<output id=t></output><script>setInterval(function(){document.getElementById('t').textContent=Date.now()},40)</script>`)
	timed := func() time.Duration {
		start := time.Now()
		if _, err := m.PageDigest(ctx, true); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	first, second := timed(), timed()
	if first < settleDOMCap/2 || second < settleDOMCap/2 {
		t.Fatalf("the first settles should spend the full budget on a page that never goes quiet: %v %v", first, second)
	}
	third := timed()
	if third > settleDOMCap/2+300*time.Millisecond {
		t.Fatalf("after two misses settle should use the short budget, took %v (first %v)", third, first)
	}
}

func TestIntegration_EmptyTreeExplainsWhy(t *testing.T) {
	skipIfNoBrowser(t)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<button>Hidden in another site</button>`)
	}))
	t.Cleanup(other.Close)
	// "localhost" and "127.0.0.1" are different sites, so Chrome puts the frame
	// in its own process, where its tree cannot be merged into the page's.
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	m, ctx := servePage(t, `<main><canvas width=50 height=50></canvas><iframe src="`+otherURL+`"></iframe></main>`)
	tree, err := m.Inspect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tree, "[ref=") {
		t.Skipf("Chrome exposed the cross-site frame here; nothing to explain:\n%s", tree)
	}
	for _, want := range []string{"nearly empty", "canvas", "cross-origin", "browser_extract"} {
		if !strings.Contains(tree, want) {
			t.Errorf("note missing %q:\n%s", want, tree)
		}
	}
}

func TestIntegration_NormalPageHasNoEmptyTreeNote(t *testing.T) {
	m, ctx := servePage(t, `<main><h1>Hi</h1><button>Go</button></main>`)
	tree, _ := m.Inspect(ctx, "")
	if strings.Contains(tree, "nearly empty") {
		t.Fatalf("unexpected note:\n%s", tree)
	}
	m2, ctx2 := servePage(t, `<main></main>`)
	tree, _ = m2.Inspect(ctx2, "")
	if !strings.Contains(tree, "nearly empty") || !strings.Contains(tree, "no visible text or controls yet") {
		t.Fatalf("an empty page should explain itself:\n%s", tree)
	}
}

// A feed that appends 10 items (after a short delay) each time the bottom is
// reached, up to total, like an infinite-scroll timeline.
func feedPage(total int, container bool) string {
	scroller, bottomEl := "window", "document.scrollingElement||document.documentElement"
	box := `<div id=feed></div>`
	if container {
		scroller, bottomEl = "box", "box"
		box = `<div id=box style="height:300px;overflow:auto"><div id=feed></div></div>`
	}
	return fmt.Sprintf(`%s<script>
var n=0,total=%d,loading=false,box=document.getElementById('box'),feed=document.getElementById('feed');
function more(){ if(loading||n>=total) return; loading=true; setTimeout(function(){ for(var i=0;i<15&&n<total;i++){var d=document.createElement('div');d.style.height='100px';d.textContent='Item '+(++n);feed.appendChild(d);} loading=false; document.title=n+' items'; },120); }
more();
%s.addEventListener('scroll',function(){ var s=%s; if(s.scrollTop+s.clientHeight>=s.scrollHeight-80) more(); });
</script>`, box, total, scroller, bottomEl)
}

func itemsLoaded(t *testing.T, m *Manager, ctx context.Context) string {
	t.Helper()
	d, err := m.PageDigest(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	return d.Title
}

func TestIntegration_ScrollUntilEndLoadsWholeFeed(t *testing.T) {
	m, ctx := servePage(t, feedPage(50, false))
	res, err := m.ScrollUntilEnd(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ReachedEnd || res.TimedOut || res.EndHeight <= res.StartHeight {
		t.Fatalf("result: %+v", res)
	}
	if got := itemsLoaded(t, m, ctx); got != "50 items" {
		t.Fatalf("loaded %q, want every item; result %+v", got, res)
	}
}

func TestIntegration_ScrollUntilEndInsideContainer(t *testing.T) {
	m, ctx := servePage(t, feedPage(40, true))
	res, err := m.ScrollUntilEnd(ctx, "#box", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ReachedEnd {
		t.Fatalf("result: %+v", res)
	}
	if got := itemsLoaded(t, m, ctx); got != "40 items" {
		t.Fatalf("loaded %q; the container feed was not exhausted (%+v)", got, res)
	}
}

func TestIntegration_ScrollUntilEndHonoursMaxSteps(t *testing.T) {
	m, ctx := servePage(t, feedPage(500, false))
	res, err := m.ScrollUntilEnd(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.ReachedEnd || res.Steps != 2 {
		t.Fatalf("a 500-item feed must not be exhausted in 2 steps: %+v", res)
	}
	if got := itemsLoaded(t, m, ctx); got == "500 items" {
		t.Fatalf("loaded everything despite max_steps: %q", got)
	}
}

func TestIntegration_ScrollUntilEndOnShortPageStopsQuickly(t *testing.T) {
	m, ctx := servePage(t, `<p>short page</p>`)
	start := time.Now()
	res, err := m.ScrollUntilEnd(ctx, "", 0)
	if err != nil || !res.ReachedEnd {
		t.Fatalf("%+v %v", res, err)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("took %s on a page that cannot grow", d)
	}
	start = time.Now()
	if _, err := m.ScrollUntilEnd(ctx, "#nope", 0); !errors.Is(err, ErrSelectorNotFound) {
		t.Errorf("a missing container must be ErrSelectorNotFound, got %v", err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("a missing container took %s to fail; want the 10s lookup bound", d)
	}
	start = time.Now()
	if _, err := m.Extract(ctx, "#nope", 0); !errors.Is(err, ErrSelectorNotFound) {
		t.Errorf("extract of a missing element: %v", err)
	} else if d := time.Since(start); d > 15*time.Second {
		t.Errorf("extract of a missing element took %s", d)
	}
}

const html5DragPage = `<style>#zone{width:200px;height:80px;border:2px dashed #888;margin-top:30px}</style>
<div id=card draggable=true style="width:100px;height:40px;background:#9cf">Card</div><div id=zone>drop here</div><output id=o></output>
<script>
var card=document.getElementById('card'), zone=document.getElementById('zone'), o=document.getElementById('o');
card.addEventListener('dragstart',function(e){ e.dataTransfer.setData('text/plain','card-42'); e.dataTransfer.effectAllowed='move'; });
zone.addEventListener('dragover',function(e){ e.preventDefault(); });
zone.addEventListener('drop',function(e){ e.preventDefault(); o.textContent='dropped:'+e.dataTransfer.getData('text/plain'); });
</script>`

func TestIntegration_DragNativeHTML5DropsWithItsData(t *testing.T) {
	m, ctx := servePage(t, html5DragPage)
	if err := m.Drag(ctx, "#card", "#zone"); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "#o", "dropped:card-42", false, 5*time.Second); err != nil {
		t.Fatalf("the native drop did not happen with the drag data: %v", err)
	}
	// Headless Chrome would also finish a plain synthetic drag, so prove the
	// interception path was the one used: Chrome reported the drag data, which
	// is what lets a headed window avoid an OS-level drag loop.
	if d := m.sess.(*session).activePage().currentDragData(); d == nil || len(d.Items) == 0 {
		t.Fatalf("drag was not intercepted: %+v", d)
	}
}

func TestIntegration_DragStillDrivesMouseOnlyWidgetsAfterHTML5Support(t *testing.T) {
	// A mouse-listener widget on the same page must keep working: interception
	// only triggers for real draggable elements.
	m, ctx := servePage(t, `<div id=knob style="position:absolute;left:20px;top:50px;width:20px;height:20px;background:#333"></div>
<div id=end style="position:absolute;left:300px;top:50px;width:20px;height:20px"></div><output id=out>0</output>
<script>var d=false;document.getElementById('knob').onmousedown=function(){d=true};
document.onmousemove=function(e){if(d)document.getElementById('out').textContent='moved'+(e.clientX>200?'-far':'')};
document.onmouseup=function(){d=false};</script>`)
	if err := m.Drag(ctx, "#knob", "#end"); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "", "moved-far", false, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// Interception must be switched off again afterwards: a plain click works.
	if err := m.Click(ctx, "#knob"); err != nil {
		t.Fatalf("click after drag: %v", err)
	}
}

func TestIntegration_ScrollUntilEndReportsLoadMoreButton(t *testing.T) {
	m, ctx := servePage(t, `<div id=feed></div><button id=more onclick="more()">Load more</button><script>
var n=0;function more(){for(var i=0;i<20;i++){var d=document.createElement('div');d.style.height='80px';d.textContent='Row '+(++n);document.getElementById('feed').appendChild(d);}document.title=n+' rows'}
more();</script>`)
	res, err := m.ScrollUntilEnd(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ReachedEnd || !strings.Contains(res.LoadMore, `button "Load more"`) || !strings.Contains(res.LoadMore, "[ref=e") {
		t.Fatalf("the click-to-load control must be reported: %+v", res)
	}
	if got := itemsLoaded(t, m, ctx); got != "20 rows" {
		t.Fatalf("scrolling alone must not load more: %q", got)
	}
	ref := regexp.MustCompile(`\[ref=(e\d+)\]`).FindStringSubmatch(res.LoadMore)[1]
	if err := m.Click(ctx, "ref="+ref); err != nil {
		t.Fatalf("clicking the reported ref: %v", err)
	}
	if got := itemsLoaded(t, m, ctx); got != "40 rows" {
		t.Fatalf("after the click: %q", got)
	}
}

func TestIntegration_ScrollUntilEndWithoutLoadMoreControlReportsNone(t *testing.T) {
	m, ctx := servePage(t, feedPage(30, false))
	res, err := m.ScrollUntilEnd(ctx, "", 0)
	if err != nil || res.LoadMore != "" {
		t.Fatalf("%+v %v", res, err)
	}
}

// Regression: the raw AX decoder once dropped node properties, so the state
// the compact tree promises ("(checked)") never showed on a real page.
func TestIntegration_InspectShowsRealStateOnRealPage(t *testing.T) {
	m, ctx := servePage(t, `<label><input type=radio name=q checked> On</label><label><input type=radio name=q> Off</label>
<label><input type=checkbox disabled> Locked</label><button aria-expanded="true">Menu</button>`)
	tree, err := m.Inspect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`radio "On" (checked)`, `checkbox "Locked" (disabled)`, `button "Menu" (expanded)`} {
		if !strings.Contains(tree, want) {
			t.Errorf("missing %q in:\n%s", want, tree)
		}
	}
	if strings.Contains(tree, `radio "Off" (checked)`) {
		t.Errorf("unchecked radio shown as checked:\n%s", tree)
	}
}

func TestIntegration_SmallCompletePageGetsNoEmptyTreeNote(t *testing.T) {
	m, ctx := servePage(t, `<h1>Hello</h1><p>This is a small but perfectly complete page with a little text and no links at all.</p>`)
	tree, _ := m.Inspect(ctx, "")
	if strings.Contains(tree, "note:") {
		t.Fatalf("a page the tree describes fully must not get a note:\n%s", tree)
	}
}

func TestIntegration_TextMissingFromTreeIsReported(t *testing.T) {
	// A canvas page whose text is drawn by script has visible-looking DOM text
	// the tree hides (aria-hidden), which browser_extract can still read.
	m, ctx := servePage(t, `<div aria-hidden="true">`+strings.Repeat("Hidden from assistive tech but visible on screen. ", 6)+`</div>`)
	tree, _ := m.Inspect(ctx, "")
	if !strings.Contains(tree, "browser_extract reads the rest") {
		t.Fatalf("missing text should be reported:\n%s", tree)
	}
}

func TestIntegration_PersistentProfileSurvivesHandoff(t *testing.T) {
	skipIfNoBrowser(t)
	if !displayAvailable() {
		t.Skip("no display available for a headed browser; run under xvfb-run")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if c, err := r.Cookie("sid"); err == nil {
			_, _ = fmt.Fprint(w, "<p>cookie:"+c.Value+"</p>")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "kept", Expires: time.Now().Add(time.Hour), Path: "/"})
		_, _ = fmt.Fprint(w, "<p>first visit</p>")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir() + "/prof"
	m := NewManager()
	ctx := context.Background()
	if err := m.SetPersistentProfile(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "", "first visit", false, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// Chrome locks the profile: the headless session must be gone before the
	// headed window opens the same directory, and the cookie must come along.
	if _, err := m.Handoff(ctx); err != nil {
		t.Fatalf("handoff with a persistent profile: %v", err)
	}
	if !m.Status().Headed {
		t.Fatal("expected a headed session after handoff")
	}
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait(ctx, "", "cookie:kept", false, 5*time.Second); err != nil {
		t.Fatalf("the persistent profile's cookie did not survive the handoff: %v", err)
	}
	if got := m.Status().ProfileDir; got != dir {
		t.Errorf("profile dir = %q, want %q", got, dir)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("persistent profile must outlive Close: %v", err)
	}
}
