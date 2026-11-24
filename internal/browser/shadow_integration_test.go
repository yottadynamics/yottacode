//go:build integration

package browser

import (
	"strings"
	"testing"
	"time"
)

// An open shadow root (with a slot and a nested root) holding a button and a radio.
const shadowPage = `<h1>Shop</h1><my-widget><span slot=label>Slotted label</span></my-widget><script>
customElements.define('inner-box', class extends HTMLElement {
  constructor(){ super(); var r=this.attachShadow({mode:'open'});
    r.innerHTML='<button id=nb>Nested Go</button>';
    r.getElementById('nb').onclick=function(){ document.title='nested'; };
  }});
customElements.define('my-widget', class extends HTMLElement {
  constructor(){ super(); var r=this.attachShadow({mode:'open'});
    r.innerHTML='<p>Inside shadow text</p><slot name=label></slot><button id=sb>Shadow Buy</button><label><input type=radio name=s> Opt</label><inner-box></inner-box>';
    r.getElementById('sb').onclick=function(){ document.title='bought'; };
  }});
</script>`

func TestIntegration_ShadowDOMRefsClickAndFind(t *testing.T) {
	m, ctx := servePage(t, shadowPage)
	tree, err := m.Inspect(ctx, "")
	if err != nil || !strings.Contains(tree, `button "Shadow Buy"`) || !strings.Contains(tree, `button "Nested Go"`) {
		t.Fatalf("shadow content missing: %v\n%s", err, tree)
	}
	if f, _ := m.Find(ctx, "shadow buy", "", 0); !strings.Contains(f, "[ref=e") {
		t.Fatalf("find: %s", f)
	}
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "button", "Shadow Buy")); err != nil {
		t.Fatalf("click ref in shadow root: %v", err)
	}
	if d, _ := m.PageDigest(ctx, true); d.Title != "bought" {
		t.Fatalf("title = %q", d.Title)
	}
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "button", "Nested Go")); err != nil {
		t.Fatalf("click ref in nested shadow root: %v", err)
	}
	if d, _ := m.PageDigest(ctx, true); d.Title != "nested" {
		t.Fatalf("title = %q", d.Title)
	}
}

func TestIntegration_ShadowDOMDigestAndExtract(t *testing.T) {
	m, ctx := servePage(t, shadowPage)
	before, _ := m.PageDigest(ctx, false)
	joined := strings.Join(before.Elements, "|")
	if !strings.Contains(joined, `button "Shadow Buy"`) || !strings.Contains(joined, `button "Nested Go"`) || !strings.Contains(joined, `radio "Opt"`) {
		t.Fatalf("digest misses shadow controls: %v", before.Elements)
	}
	tree, _ := m.Inspect(ctx, "")
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "radio", "Opt")); err != nil {
		t.Fatal(err)
	}
	after, _ := m.PageDigest(ctx, true)
	if d := DiffDigest(before, after); !strings.Contains(d, `changed: radio "Opt" -> radio "Opt" (checked)`) {
		t.Fatalf("diff:\n%s", d)
	}
	md, err := m.Extract(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Shop", "Inside shadow text", "Slotted label", "Shadow Buy", "Nested Go"} {
		if !strings.Contains(md, want) {
			t.Errorf("extract missing %q:\n%s", want, md)
		}
	}
}

// Closed shadow roots are invisible to script but not to the accessibility
// tree; the digest must still see state changes inside them.
func TestIntegration_ClosedShadowRootDigestSeesStateChange(t *testing.T) {
	m, ctx := servePage(t, `<closed-box></closed-box><script>
customElements.define('closed-box', class extends HTMLElement {
  constructor(){ super(); var r=this.attachShadow({mode:'closed'});
    r.innerHTML='<label><input type=radio name=z> Hidden option</label><button id=b>Closed Go</button>';
    r.getElementById('b').onclick=function(){ document.title='closed-clicked'; };
  }});
</script>`)
	before, _ := m.PageDigest(ctx, false)
	if joined := strings.Join(before.Elements, "|"); !strings.Contains(joined, `radio "Hidden option"`) || !strings.Contains(joined, `button "Closed Go"`) {
		t.Fatalf("digest misses closed-shadow controls: %v", before.Elements)
	}
	tree, _ := m.Inspect(ctx, "")
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "radio", "Hidden option")); err != nil {
		t.Fatal(err)
	}
	after, _ := m.PageDigest(ctx, true)
	if d := DiffDigest(before, after); !strings.Contains(d, `changed: radio "Hidden option" -> radio "Hidden option" (checked)`) {
		t.Fatalf("diff:\n%s", d)
	}
}

func TestIntegration_ShadowRootInsideIframeRefClick(t *testing.T) {
	m, ctx := serveSite(t, map[string]string{
		"/":      `<iframe id=f src="/inner" width=400 height=200></iframe>`,
		"/inner": `<output id=o></output><my-w></my-w><script>customElements.define('my-w',class extends HTMLElement{constructor(){super();var r=this.attachShadow({mode:'open'});r.innerHTML='<button id=b>In Shadow In Frame</button>';r.getElementById('b').onclick=function(){document.getElementById('o').textContent='frame-shadow-ok'}}})</script>`,
	})
	tree, _ := m.Inspect(ctx, "")
	if err := m.Click(ctx, "ref="+refForNode(t, tree, "button", "In Shadow In Frame")); err != nil {
		t.Fatalf("ref into a shadow root inside an iframe: %v", err)
	}
	if err := m.Wait(ctx, "iframe#f >>> #o", "frame-shadow-ok", false, 5*time.Second); err != nil {
		t.Fatalf("the click did not reach the shadow button inside the frame: %v", err)
	}
}

func TestIntegration_ExtractReadsClosedShadowRootText(t *testing.T) {
	m, ctx := servePage(t, `<main><h1>Store</h1><p>Visible paragraph.</p><secret-box></secret-box></main><script>
customElements.define('secret-box', class extends HTMLElement {
  constructor(){ super(); this.attachShadow({mode:'closed'}).innerHTML='<p>Price is 42 dollars</p><p>Visible paragraph.</p>'; }});
</script>`)
	md, err := m.Extract(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "# Store") || !strings.Contains(md, "Price is 42 dollars") {
		t.Fatalf("closed-shadow text missing:\n%s", md)
	}
	if strings.Count(md, "Visible paragraph.") != 1 {
		t.Errorf("text the DOM walk already had must not be repeated:\n%s", md)
	}
}

func TestIntegration_ExtractOrdinaryPageHasNoShadowSection(t *testing.T) {
	m, ctx := servePage(t, `<main><h1>Plain</h1><p>Text.</p></main>`)
	md, err := m.Extract(ctx, "", 0)
	if err != nil || strings.Contains(md, "custom elements") {
		t.Fatalf("%v\n%s", err, md)
	}
}
