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

// maxAXFullNodes and maxAXFullChars bound the *complete* outline that is
// built even when the shown text is truncated (so refs past the cut still
// resolve, and the whole thing can be spilled to a file). They are a
// safety ceiling against a pathological page, not a display budget.
var (
	maxAXFullNodes = 20000
	maxAXFullChars = 2_000_000
)

// axInteractiveRoles are the accessibility roles that get an @eN ref: the
// elements a model can meaningfully click, type into, select or toggle.
var axInteractiveRoles = map[string]bool{
	"button": true, "link": true, "textbox": true, "searchbox": true,
	"checkbox": true, "radio": true, "combobox": true, "listbox": true,
	"option": true, "menuitem": true, "menuitemcheckbox": true,
	"menuitemradio": true, "tab": true, "switch": true, "slider": true,
	"spinbutton": true, "treeitem": true, "DisclosureTriangle": true,
	"PopUpButton": true, "ColorWell": true,
}

// axStateProps are the AX properties worth surfacing on an interactive
// node's line — the state a model needs to decide what to do next
// ("is this checkbox already ticked?", "is the button disabled?").
var axStateProps = []proto.AccessibilityAXPropertyName{
	proto.AccessibilityAXPropertyNameDisabled,
	proto.AccessibilityAXPropertyNameChecked,
	proto.AccessibilityAXPropertyNameExpanded,
	proto.AccessibilityAXPropertyNameSelected,
	proto.AccessibilityAXPropertyNamePressed,
	proto.AccessibilityAXPropertyNameRequired,
	proto.AccessibilityAXPropertyNameReadonly,
	proto.AccessibilityAXPropertyNameInvalid,
}

// axOptions tunes renderAX.
type axOptions struct {
	// interactiveOnly prints only the interactive nodes as a flat list,
	// dropping structure and static text — the compact view for "what can
	// I click?" questions.
	interactiveOnly bool
	// withRefs assigns an @eN ref to every interactive node that has a
	// backing DOM node. Off for the legacy renderAXTree wrapper.
	withRefs bool
	// refStart is the number the first assigned ref continues from
	// (refs are e<refStart+1>, e<refStart+2>, …), so a scoped snapshot can
	// add to an existing registry without colliding with it.
	refStart int
}

// axRender is renderAX's result.
type axRender struct {
	// Text is the outline within maxAXNodes/maxAXChars, ending in
	// "…[truncated]" when the cut applied.
	Text string
	// Full is the complete outline (up to the maxAXFull* ceiling). Equal to
	// Text when nothing was cut.
	Full      string
	Truncated bool
	// Refs maps every ref assigned in Full — including those past the cut in
	// Text — to its backing DOM node.
	Refs map[string]refTarget
	// RefCount is the highest ref number assigned (refStart + len(Refs)).
	RefCount int
}

// axSection is one accessibility tree to render: the page's own, or one
// iframe's. Label is printed as a header line for a frame section and empty
// for the top-level document.
type axSection struct {
	Label string
	Nodes []*proto.AccessibilityAXNode
	// Session is the CDP session this tree was read from: empty for the page's
	// own session (including same-process frames), or a cross-process
	// iframe's target id. Refs issued from the section remember it, because a
	// DOM backend node id only means something on the session that produced it.
	Session proto.TargetSessionID
}

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

// axStateFlags renders the truthy state properties of n as "[disabled]
// [checked]"-style flags. A tri-state "mixed" checked value is shown as-is.
func axStateFlags(n *proto.AccessibilityAXNode) string {
	if len(n.Properties) == 0 {
		return ""
	}
	var flags []string
	for _, want := range axStateProps {
		for _, p := range n.Properties {
			if p.Name != want {
				continue
			}
			switch v := axValueString(p.Value); v {
			case "", "false":
			case "true":
				flags = append(flags, string(want))
			default:
				flags = append(flags, string(want)+"="+v)
			}
		}
	}
	if len(flags) == 0 {
		return ""
	}
	return " [" + strings.Join(flags, "] [") + "]"
}

// axRenderer accumulates one snapshot across any number of sections, so the
// display budget, the full-outline ceiling and the ref numbering are shared
// between the page and its frames.
type axRenderer struct {
	opts axOptions

	full, shown strings.Builder
	shownCount  int
	fullCount   int
	refs        map[string]refTarget
	refCount    int
	cut         bool // shown text hit its display budget
	stop        bool // full outline hit the safety ceiling
}

// emit appends one line to the full outline and, while the display budget
// lasts, to the shown text.
func (r *axRenderer) emit(line string) {
	r.full.WriteString(line)
	r.full.WriteByte('\n')
	r.fullCount++
	if r.cut {
		return
	}
	if r.shownCount >= maxAXNodes || r.shown.Len() >= maxAXChars {
		r.cut = true
		return
	}
	r.shown.WriteString(line)
	r.shown.WriteByte('\n')
	r.shownCount++
}

// addSection renders one tree. Callers pass the sections in reading order
// (the page first, then its frames).
func (r *axRenderer) addSection(sec axSection) {
	if r.stop {
		return
	}
	nodes := sec.Nodes
	if len(nodes) == 0 {
		return
	}
	// A frame's header is printed only once the frame contributes a line, so
	// an interactive_only snapshot doesn't list every empty ad frame.
	header := ""
	if sec.Label != "" {
		header = fmt.Sprintf("--- %s ---", sec.Label)
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

	var walk func(n *proto.AccessibilityAXNode, depth int)
	walk = func(n *proto.AccessibilityAXNode, depth int) {
		if n == nil || r.stop {
			return
		}
		if r.fullCount >= maxAXFullNodes || r.full.Len() >= maxAXFullChars {
			r.stop = true
			return
		}
		if !n.Ignored {
			role := axValueString(n.Role)
			interactive := axInteractiveRoles[role]
			if !r.opts.interactiveOnly || interactive {
				line := ""
				if !r.opts.interactiveOnly {
					line = strings.Repeat("  ", depth)
				}
				if r.opts.withRefs && interactive && n.BackendDOMNodeID != 0 {
					r.refCount++
					ref := fmt.Sprintf("e%d", r.refCount)
					r.refs[ref] = refTarget{node: n.BackendDOMNodeID, session: sec.Session}
					line += "@" + ref + " "
				}
				line += role
				if name := strings.TrimSpace(axValueString(n.Name)); name != "" {
					line += fmt.Sprintf(" %q", name)
				}
				if val := axValueString(n.Value); val != "" {
					line += fmt.Sprintf(" =%q", val)
				}
				if interactive {
					line += axStateFlags(n)
				}
				if header != "" {
					r.emit(header)
					header = ""
				}
				r.emit(line)
			}
		}
		for _, cid := range n.ChildIDs {
			walk(byID[cid], depth+1)
		}
	}
	walk(root, 0)
}

// result finalizes the accumulated snapshot.
func (r *axRenderer) result() axRender {
	if r.full.Len() == 0 {
		return axRender{Text: "(empty accessibility tree)", Full: "(empty accessibility tree)", RefCount: r.opts.refStart}
	}
	out := axRender{Text: r.shown.String(), Full: r.full.String(), Truncated: r.cut || r.stop, Refs: r.refs, RefCount: r.refCount}
	if out.Truncated {
		out.Text += "…[truncated]"
		out.Full += "…[truncated]"
		if !r.cut {
			// The full ceiling hit but the shown budget didn't: the shown
			// text is the whole (already bounded) outline.
			out.Text = out.Full
		}
	}
	return out
}

// renderAXSections flattens CDP accessibility trees (as returned by
// Accessibility.getFullAXTree/getPartialAXTree) into an indented role/name/
// value outline — a compact substitute for a screenshot when the model
// only needs to know what's on the page, not what it looks like. With
// opts.withRefs every interactive node also carries an "@eN" handle the
// action tools accept anywhere they accept a CSS selector; the numbering
// runs on across sections, so a control inside an iframe has a ref that can
// never collide with one on the page.
func renderAXSections(sections []axSection, opts axOptions) axRender {
	r := &axRenderer{opts: opts, refs: map[string]refTarget{}, refCount: opts.refStart}
	for _, sec := range sections {
		r.addSection(sec)
	}
	return r.result()
}

// renderAX renders a single tree — see renderAXSections.
func renderAX(nodes []*proto.AccessibilityAXNode, opts axOptions) axRender {
	return renderAXSections([]axSection{{Nodes: nodes}}, opts)
}

// renderAXTree is the ref-less, non-compact rendering — the original
// behavior, kept for callers and tests that only want the outline text.
func renderAXTree(nodes []*proto.AccessibilityAXNode) string {
	return renderAX(nodes, axOptions{}).Text
}
