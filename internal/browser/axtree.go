package browser

import (
	"fmt"
	"strings"

	"github.com/go-rod/rod/lib/proto"
)

// maxAXNodes and maxAXChars bound browser_inspect's output — the whole
// point of the accessibility-tree snapshot is being the token-cheap way to
// "read" a page, so an unbounded tree on a complex page would defeat that.
// Vars (not consts) so tests can shrink them to exercise truncation
// without building a 500-node fixture.
var (
	maxAXNodes = 500
	maxAXChars = 20000
)

// axValueString renders an AXValue's underlying JSON value (string, number,
// or bool) as plain text, regardless of type. Returns "" for a nil value or
// a JSON null.
func axValueString(v *proto.AccessibilityAXValue) string {
	if v == nil {
		return ""
	}
	raw := v.Value.Val()
	if raw == nil {
		return ""
	}
	return fmt.Sprint(raw)
}

// renderAXTree flattens a CDP accessibility-tree node list (as returned by
// Accessibility.getFullAXTree/getPartialAXTree) into an indented role/name/
// value outline — a compact substitute for a screenshot when the model
// only needs to know what's on the page, not what it looks like.
func renderAXTree(nodes []*proto.AccessibilityAXNode) string {
	if len(nodes) == 0 {
		return "(empty accessibility tree)"
	}
	byID := make(map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	var root *proto.AccessibilityAXNode
	for _, n := range nodes {
		if n.ParentID == "" {
			root = n
			break
		}
	}
	if root == nil {
		root = nodes[0]
	}

	var sb strings.Builder
	count := 0
	truncated := false
	var walk func(n *proto.AccessibilityAXNode, depth int)
	walk = func(n *proto.AccessibilityAXNode, depth int) {
		if n == nil || truncated {
			return
		}
		if count >= maxAXNodes || sb.Len() >= maxAXChars {
			truncated = true
			return
		}
		if !n.Ignored {
			role := axValueString(n.Role)
			name := axValueString(n.Name)
			val := axValueString(n.Value)
			line := strings.Repeat("  ", depth) + role
			if name != "" {
				line += fmt.Sprintf(" %q", name)
			}
			if val != "" {
				line += fmt.Sprintf(" =%q", val)
			}
			sb.WriteString(line)
			sb.WriteByte('\n')
			count++
		}
		for _, cid := range n.ChildIDs {
			walk(byID[cid], depth+1)
		}
	}
	walk(root, 0)

	out := sb.String()
	if out == "" {
		return "(empty accessibility tree)"
	}
	if truncated {
		out += "…[truncated]"
	}
	return out
}
