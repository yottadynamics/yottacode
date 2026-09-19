package browser

import (
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func frameTree(role, name string, backend int) []*proto.AccessibilityAXNode {
	return []*proto.AccessibilityAXNode{
		axNode("r", 900+backend, "RootWebArea", "", "c"),
		withParent(axNode("c", backend, role, name), "r"),
	}
}

func TestRenderAXSections_RefsRunOnAcrossFramesAndHeadersLabelThem(t *testing.T) {
	page := frameTree("button", "Top", 1)
	f1 := frameTree("textbox", "Card number", 2)
	f2 := frameTree("button", "Pay", 3)
	r := renderAXSections([]axSection{
		{Nodes: page},
		{Label: `frame "stripe" https://js.stripe.test/`, Nodes: f1},
		{Label: "frame https://ads.test/", Nodes: f2},
	}, axOptions{withRefs: true, interactiveOnly: true})

	want := "@e1 button \"Top\"\n" +
		"--- frame \"stripe\" https://js.stripe.test/ ---\n" +
		"@e2 textbox \"Card number\"\n" +
		"--- frame https://ads.test/ ---\n" +
		"@e3 button \"Pay\"\n"
	if r.Text != want {
		t.Errorf("sections rendered as:\n%s\nwant:\n%s", r.Text, want)
	}
	if r.Refs["e2"].node != 2 || r.Refs["e3"].node != 3 || r.RefCount != 3 {
		t.Errorf("refs = %v (count %d), want e2->2 e3->3 count 3", r.Refs, r.RefCount)
	}
}

func TestRenderAXSections_FrameWithNothingToShowGetsNoHeader(t *testing.T) {
	page := frameTree("button", "Top", 1)
	// In interactive_only mode a frame holding only text contributes no
	// lines, so its header must not appear either.
	textOnly := frameTree("StaticText", "just an ad", 2)
	empty := []*proto.AccessibilityAXNode(nil)
	r := renderAXSections([]axSection{
		{Nodes: page},
		{Label: "frame textonly", Nodes: textOnly},
		{Label: "frame empty", Nodes: empty},
	}, axOptions{withRefs: true, interactiveOnly: true})
	if strings.Contains(r.Text, "frame") {
		t.Errorf("headers for contentless frames leaked into:\n%s", r.Text)
	}

	// In the full outline the same text-only frame does show.
	full := renderAXSections([]axSection{{Nodes: page}, {Label: "frame textonly", Nodes: textOnly}}, axOptions{})
	if !strings.Contains(full.Text, "--- frame textonly ---") || !strings.Contains(full.Text, `StaticText "just an ad"`) {
		t.Errorf("full outline should include the frame:\n%s", full.Text)
	}
}

func TestRenderAXSections_BudgetIsSharedAcrossFrames(t *testing.T) {
	origNodes := maxAXNodes
	maxAXNodes = 2
	defer func() { maxAXNodes = origNodes }()

	r := renderAXSections([]axSection{
		{Nodes: frameTree("button", "A", 1)},
		{Label: "frame two", Nodes: frameTree("button", "B", 2)},
	}, axOptions{withRefs: true, interactiveOnly: true})
	// Budget of 2 lines: A, then the frame header; B is cut but still has a ref.
	if !r.Truncated || strings.Contains(r.Text, `"B"`) {
		t.Errorf("expected B to be cut from the shown text:\n%s", r.Text)
	}
	if !strings.Contains(r.Full, `@e2 button "B"`) || r.Refs["e2"].node != 2 {
		t.Errorf("the cut frame's control must remain in Full and Refs:\n%s\n%v", r.Full, r.Refs)
	}
}

func TestFrameLabel(t *testing.T) {
	cases := []struct {
		f    *proto.PageFrame
		want string
	}{
		{&proto.PageFrame{Name: "f2", URL: "http://x.test/a"}, `frame "f2" http://x.test/a`},
		{&proto.PageFrame{URL: "http://x.test/a"}, `frame http://x.test/a`},
		{&proto.PageFrame{URL: "http://x.test/" + strings.Repeat("a", 300)}, `frame http://x.test/` + strings.Repeat("a", maxFrameURLChars-len("http://x.test/")) + "…"},
	}
	for _, c := range cases {
		if got := frameLabel(c.f); got != c.want {
			t.Errorf("frameLabel = %q, want %q", got, c.want)
		}
	}
}
