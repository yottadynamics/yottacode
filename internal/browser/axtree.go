package browser

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
)

var (
	maxAXNodes = 500
	maxAXChars = 20000
)

func axValueString(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var out any
	if json.Unmarshal(v.Value, &out) == nil {
		return fmt.Sprint(out)
	}
	return string(v.Value)
}
func renderAXTree(nodes []*accessibility.Node) string { return renderAXTreeRefs(nodes, nil) }

// axRenderOpts tunes renderAXTreeOpts.
type axRenderOpts struct {
	// interactiveOnly prints only nodes that carry a ref plus headings and
	// group labels, flattened, instead of the whole tree.
	interactiveOnly bool
}

// noiseRoles never add information beyond what their parent already says.
var noiseRoles = map[string]bool{"InlineTextBox": true, "LineBreak": true}

// wrapperRoles are structural containers that, without a name or a ref, only
// add indentation.
var wrapperRoles = map[string]bool{"generic": true, "none": true, "presentation": true, "GenericContainer": true}

// renderAXTreeRefs renders the tree, tagging each interactive node with a
// "[ref=eN]" the other browser tools accept as a selector (see selector.go).
func renderAXTreeRefs(nodes []*accessibility.Node, refs *refTable) string {
	return renderAXTreeOpts(nodes, refs, axRenderOpts{})
}

// renderAXTreeOpts renders a compact tree: text-run duplicates (InlineTextBox,
// StaticText repeating its parent's name) are dropped and anonymous wrapper
// nodes are collapsed so their children are not indented for nothing.
func renderAXTreeOpts(nodes []*accessibility.Node, refs *refTable, opt axRenderOpts) string {
	if len(nodes) == 0 {
		return "(empty accessibility tree)"
	}
	by := map[accessibility.NodeID]*accessibility.Node{}
	for _, n := range nodes {
		by[n.NodeID] = n
	}
	root := nodes[0]
	for _, n := range nodes {
		if n.ParentID == "" {
			root = n
			break
		}
	}
	var b strings.Builder
	n := 0
	tr := false
	var walk func(x *accessibility.Node, d int, parentName string)
	walk = func(x *accessibility.Node, d int, parentName string) {
		if x == nil || tr {
			return
		}
		if n >= maxAXNodes || b.Len() >= maxAXChars {
			tr = true
			return
		}
		childDepth, childParent := d, parentName
		if !x.Ignored {
			role := axValueString(x.Role)
			name := axValueString(x.Name)
			value := axValueString(x.Value)
			isRef := refs != nil && interactiveRoles[role] && x.BackendDOMNodeID != 0
			skip := noiseRoles[role]
			// A text run repeating what the parent already says is noise.
			if role == "StaticText" && (name == "" || strings.Contains(parentName, name)) {
				skip = true
			}
			if wrapperRoles[role] && name == "" && value == "" && !isRef {
				skip, childDepth = true, d
			}
			if opt.interactiveOnly && !isRef && role != "heading" && role != "RootWebArea" && role != "radiogroup" && role != "group" && role != "form" && role != "alert" && role != "dialog" {
				skip = true
			}
			if !skip {
				line := strings.Repeat("  ", d) + role
				if name != "" {
					line += fmt.Sprintf(" %q", name)
				}
				if value != "" {
					line += fmt.Sprintf(" =%q", value)
				}
				line += axStateSuffix(x)
				if ref := refs.assign(x); ref != "" {
					line += " [ref=" + ref + "]"
				}
				b.WriteString(line + "\n")
				n++
				childDepth = d + 1
				if name != "" {
					childParent = name
				}
				if opt.interactiveOnly {
					childDepth = 0 // flat list
				}
			}
		}
		for _, id := range x.ChildIDs {
			walk(by[id], childDepth, childParent)
		}
	}
	walk(root, 0, "")
	if b.Len() == 0 {
		return "(empty accessibility tree)"
	}
	if tr {
		b.WriteString("…[truncated]")
	}
	return b.String()
}

// axStateSuffix renders the properties a model needs to verify an action took
// effect (a radio is checked, a field is disabled) as " (checked, disabled)".
func axStateSuffix(x *accessibility.Node) string {
	var st []string
	for _, p := range x.Properties {
		v := axValueString(p.Value)
		switch string(p.Name) {
		case "checked":
			if v == "true" || v == "mixed" {
				st = append(st, "checked")
			}
		case "selected", "expanded", "pressed", "disabled", "required", "invalid":
			if v == "true" {
				st = append(st, string(p.Name))
			}
		}
	}
	if len(st) == 0 {
		return ""
	}
	return " (" + strings.Join(st, ", ") + ")"
}
