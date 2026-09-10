package browser

import (
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

func axVal(v any) *proto.AccessibilityAXValue {
	return &proto.AccessibilityAXValue{Value: gson.New(v)}
}

func TestRenderAXTree_Empty(t *testing.T) {
	if got := renderAXTree(nil); got != "(empty accessibility tree)" {
		t.Errorf("got %q", got)
	}
}

func TestRenderAXTree_RoleNameValue(t *testing.T) {
	nodes := []*proto.AccessibilityAXNode{
		{
			NodeID:   "1",
			Role:     axVal("RootWebArea"),
			Name:     axVal("Example Page"),
			ChildIDs: []proto.AccessibilityAXNodeID{"2"},
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
	nodes := []*proto.AccessibilityAXNode{
		{NodeID: "1", Role: axVal("RootWebArea"), ChildIDs: []proto.AccessibilityAXNodeID{"2"}},
		{NodeID: "2", ParentID: "1", Ignored: true, Role: axVal("generic"), ChildIDs: []proto.AccessibilityAXNodeID{"3"}},
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

	nodes := []*proto.AccessibilityAXNode{
		{NodeID: "1", Role: axVal("RootWebArea"), ChildIDs: []proto.AccessibilityAXNodeID{"2", "3", "4"}},
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
	if got := axValueString(&proto.AccessibilityAXValue{}); got != "" {
		t.Errorf("axValueString(zero value) = %q", got)
	}
}
