package browser

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
)

func axVal(v any) *accessibility.Value {
	b, _ := json.Marshal(v)
	return &accessibility.Value{Value: b}
}

func TestRenderAXTree_Empty(t *testing.T) {
	if got := renderAXTree(nil); got != "(empty accessibility tree)" {
		t.Errorf("got %q", got)
	}
}

func TestRenderAXTree_RoleNameValue(t *testing.T) {
	nodes := []*accessibility.Node{
		{
			NodeID:   "1",
			Role:     axVal("RootWebArea"),
			Name:     axVal("Example Page"),
			ChildIDs: []accessibility.NodeID{"2"},
		},
		{
			NodeID:   "2",
			ParentID: "1",
			Role:     axVal("button"),
			Name:     axVal("Submit"),
			Value:    axVal("clicked"),
		},
	}
	out := renderAXTree(nodes)
	if !strings.Contains(out, `RootWebArea "Example Page"`) {
		t.Errorf("missing root line, got:\n%s", out)
	}
	if !strings.Contains(out, `button "Submit" ="clicked"`) {
		t.Errorf("missing button line, got:\n%s", out)
	}
}

func TestRenderAXTree_SkipsIgnoredButRecursesChildren(t *testing.T) {
	nodes := []*accessibility.Node{
		{NodeID: "1", Role: axVal("RootWebArea"), ChildIDs: []accessibility.NodeID{"2"}},
		{NodeID: "2", ParentID: "1", Ignored: true, Role: axVal("generic"), ChildIDs: []accessibility.NodeID{"3"}},
		{NodeID: "3", ParentID: "2", Role: axVal("link"), Name: axVal("Home")},
	}
	out := renderAXTree(nodes)
	if strings.Contains(out, "generic") {
		t.Errorf("ignored node should not be printed, got:\n%s", out)
	}
	if !strings.Contains(out, `link "Home"`) {
		t.Errorf("descendant of an ignored node should still be printed, got:\n%s", out)
	}
}

func TestRenderAXTree_Truncates(t *testing.T) {
	origNodes, origChars := maxAXNodes, maxAXChars
	maxAXNodes = 2
	maxAXChars = 1_000_000
	defer func() { maxAXNodes, maxAXChars = origNodes, origChars }()

	nodes := []*accessibility.Node{
		{NodeID: "1", Role: axVal("RootWebArea"), ChildIDs: []accessibility.NodeID{"2", "3", "4"}},
		{NodeID: "2", ParentID: "1", Role: axVal("link"), Name: axVal("A")},
		{NodeID: "3", ParentID: "1", Role: axVal("link"), Name: axVal("B")},
		{NodeID: "4", ParentID: "1", Role: axVal("link"), Name: axVal("C")},
	}
	out := renderAXTree(nodes)
	if !strings.HasSuffix(out, "…[truncated]") {
		t.Errorf("expected truncation marker, got:\n%s", out)
	}
}

func TestAxValueString_NilAndNull(t *testing.T) {
	if got := axValueString(nil); got != "" {
		t.Errorf("axValueString(nil) = %q", got)
	}
	if got := axValueString(&accessibility.Value{}); got != "" {
		t.Errorf("axValueString(zero value) = %q", got)
	}
}

func TestRenderAXTree_CompactsNoise(t *testing.T) {
	nodes := []*accessibility.Node{
		{NodeID: "1", Role: axVal("RootWebArea"), Name: axVal("P"), ChildIDs: []accessibility.NodeID{"2"}},
		{NodeID: "2", ParentID: "1", Role: axVal("generic"), ChildIDs: []accessibility.NodeID{"3"}},
		{NodeID: "3", ParentID: "2", Role: axVal("link"), Name: axVal("Home"), BackendDOMNodeID: 7, ChildIDs: []accessibility.NodeID{"4"}},
		{NodeID: "4", ParentID: "3", Role: axVal("StaticText"), Name: axVal("Home"), ChildIDs: []accessibility.NodeID{"5"}},
		{NodeID: "5", ParentID: "4", Role: axVal("InlineTextBox"), Name: axVal("Home")},
	}
	out := renderAXTreeRefs(nodes, newRefTable())
	want := "RootWebArea \"P\"\n  link \"Home\" [ref=e1]\n"
	if out != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestRenderAXTree_InteractiveOnlyAndState(t *testing.T) {
	nodes := []*accessibility.Node{
		{NodeID: "1", Role: axVal("RootWebArea"), ChildIDs: []accessibility.NodeID{"2", "3"}},
		{NodeID: "2", ParentID: "1", Role: axVal("paragraph"), Name: axVal("long prose")},
		{NodeID: "3", ParentID: "1", Role: axVal("radio"), Name: axVal("B"), BackendDOMNodeID: 9,
			Properties: []*accessibility.Property{{Name: "checked", Value: axVal("true")}}},
	}
	out := renderAXTreeOpts(nodes, newRefTable(), axRenderOpts{interactiveOnly: true})
	if strings.Contains(out, "prose") || !strings.Contains(out, `radio "B" (checked) [ref=e1]`) {
		t.Fatalf("got:\n%s", out)
	}
}
