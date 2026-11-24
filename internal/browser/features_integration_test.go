//go:build integration

package browser

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	if !strings.Contains(diff, `added: radio "Beta" (checked)`) || !strings.Contains(diff, `removed: radio "Beta"`) {
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
