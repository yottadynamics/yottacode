package browser

import (
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

// axNode builds a test AX node with a backing DOM node id.
func axNode(id string, backend int, role, name string, children ...string) *proto.AccessibilityAXNode {
	n := &proto.AccessibilityAXNode{
		NodeID:           proto.AccessibilityAXNodeID(id),
		Role:             axVal(role),
		BackendDOMNodeID: proto.DOMBackendNodeID(backend),
	}
	if name != "" {
		n.Name = axVal(name)
	}
	for _, c := range children {
		n.ChildIDs = append(n.ChildIDs, proto.AccessibilityAXNodeID(c))
	}
	return n
}

func withParent(n *proto.AccessibilityAXNode, parent string) *proto.AccessibilityAXNode {
	n.ParentID = proto.AccessibilityAXNodeID(parent)
	return n
}

func sampleFormTree() []*proto.AccessibilityAXNode {
	disabled := withParent(axNode("5", 105, "button", "Nope"), "1")
	disabled.Properties = []*proto.AccessibilityAXProperty{
		{Name: proto.AccessibilityAXPropertyNameDisabled, Value: axVal(true)},
		{Name: proto.AccessibilityAXPropertyNameRequired, Value: axVal(false)},
	}
	checked := withParent(axNode("6", 106, "checkbox", "Agree"), "1")
	checked.Properties = []*proto.AccessibilityAXProperty{
		{Name: proto.AccessibilityAXPropertyNameChecked, Value: axVal("mixed")},
	}
	return []*proto.AccessibilityAXNode{
		axNode("1", 101, "RootWebArea", "Form", "2", "3", "4", "5", "6"),
		withParent(axNode("2", 102, "heading", "Sign in"), "1"),
		withParent(axNode("3", 103, "textbox", "Email"), "1"),
		withParent(axNode("4", 104, "button", "Go"), "1"),
		disabled,
		checked,
	}
}

func TestRenderAX_AssignsRefsToInteractiveNodesOnly(t *testing.T) {
	r := renderAX(sampleFormTree(), axOptions{withRefs: true})
	for _, want := range []string{
		"RootWebArea \"Form\"\n",
		"  heading \"Sign in\"\n", // no ref on a heading
		"  @e1 textbox \"Email\"\n",
		"  @e2 button \"Go\"\n",
		"  @e3 button \"Nope\" [disabled]\n", // required=false is not shown
		"  @e4 checkbox \"Agree\" [checked=mixed]\n",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in:\n%s", want, r.Text)
		}
	}
	if len(r.Refs) != 4 || r.RefCount != 4 {
		t.Fatalf("Refs = %v (RefCount %d), want 4 refs", r.Refs, r.RefCount)
	}
	if r.Refs["e1"].node != 103 || r.Refs["e2"].node != 104 {
		t.Errorf("refs map to the wrong DOM nodes: %v", r.Refs)
	}
}

func TestRenderAX_InteractiveOnlyIsFlat(t *testing.T) {
	r := renderAX(sampleFormTree(), axOptions{withRefs: true, interactiveOnly: true})
	want := "@e1 textbox \"Email\"\n@e2 button \"Go\"\n@e3 button \"Nope\" [disabled]\n@e4 checkbox \"Agree\" [checked=mixed]\n"
	if r.Text != want {
		t.Errorf("interactive_only text:\n%q\nwant:\n%q", r.Text, want)
	}
}

func TestRenderAX_RefStartContinuesNumbering(t *testing.T) {
	r := renderAX(sampleFormTree(), axOptions{withRefs: true, interactiveOnly: true, refStart: 10})
	if !strings.HasPrefix(r.Text, "@e11 textbox") {
		t.Errorf("refs should continue from refStart, got:\n%s", r.Text)
	}
	if _, ok := r.Refs["e1"]; ok {
		t.Errorf("refs below refStart must not be reissued: %v", r.Refs)
	}
	if r.RefCount != 14 {
		t.Errorf("RefCount = %d, want 14", r.RefCount)
	}
}

func TestRenderAX_RefsPastTheCutStillRegistered(t *testing.T) {
	origNodes := maxAXNodes
	maxAXNodes = 3
	defer func() { maxAXNodes = origNodes }()

	r := renderAX(sampleFormTree(), axOptions{withRefs: true, interactiveOnly: true})
	if !r.Truncated || !strings.HasSuffix(r.Text, "…[truncated]") {
		t.Fatalf("expected a truncated Text, got %q", r.Text)
	}
	if strings.Contains(r.Text, "Agree") {
		t.Errorf("Text should be cut before the 4th control:\n%s", r.Text)
	}
	if !strings.Contains(r.Full, `@e4 checkbox "Agree"`) {
		t.Errorf("Full must hold every line, got:\n%s", r.Full)
	}
	if r.Refs["e4"].node != 106 {
		t.Errorf("a ref past the cut must still resolve; Refs = %v", r.Refs)
	}
}

func TestRenderAX_NoRefsWithoutOption(t *testing.T) {
	r := renderAX(sampleFormTree(), axOptions{})
	if strings.Contains(r.Text, "@e") || len(r.Refs) != 0 {
		t.Errorf("legacy rendering must not carry refs, got:\n%s", r.Text)
	}
}

func TestRenderAX_TrimsNameWhitespace(t *testing.T) {
	nodes := []*proto.AccessibilityAXNode{
		axNode("1", 1, "RootWebArea", "", "2"),
		withParent(axNode("2", 2, "checkbox", " I agree "), "1"),
	}
	if out := renderAXTree(nodes); !strings.Contains(out, `checkbox "I agree"`) {
		t.Errorf("label whitespace should be trimmed, got:\n%s", out)
	}
}

func TestIsRef(t *testing.T) {
	for _, s := range []string{"@e1", "@e42", "  @e7 "} {
		if !isRef(s) {
			t.Errorf("isRef(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "e1", "@e", "@x1", "#e1", "@e1x", "button@e1", ".a @e1"} {
		if isRef(s) {
			t.Errorf("isRef(%q) = true, want false", s)
		}
	}
}

func TestTrackedPage_RegisterRefsReplaceVsMerge(t *testing.T) {
	tp := &trackedPage{}
	tp.registerRefs(axRender{Refs: map[string]refTarget{"e1": {node: 10}, "e2": {node: 20}}, RefCount: 2}, true)
	tp.registerRefs(axRender{Refs: map[string]refTarget{"e3": {node: 30}}, RefCount: 3}, false)
	for ref, want := range map[string]proto.DOMBackendNodeID{"e1": 10, "e2": 20, "e3": 30} {
		if got, ok := tp.lookupRef(ref); !ok || got.node != want {
			t.Errorf("after a scoped add, lookupRef(%s) = %d,%t want %d", ref, got.node, ok, want)
		}
	}
	if tp.refCount() != 3 {
		t.Errorf("refCount = %d, want 3", tp.refCount())
	}

	// A whole-page snapshot replaces everything.
	tp.registerRefs(axRender{Refs: map[string]refTarget{"e1": {node: 99}}, RefCount: 1}, true)
	if _, ok := tp.lookupRef("e2"); ok {
		t.Error("a whole-page snapshot must invalidate the previous refs")
	}
	if got, _ := tp.lookupRef("e1"); got.node != 99 {
		t.Errorf("e1 = %d, want the new node 99", got.node)
	}
	if tp.refCount() != 1 {
		t.Errorf("refCount = %d, want 1 after replace", tp.refCount())
	}
}
