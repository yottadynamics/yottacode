//go:build integration

package browser

import (
	"strings"
	"testing"
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
