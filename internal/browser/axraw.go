package browser

import (
	"context"
	"encoding/json"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
)

// cdproto decodes Accessibility.getFullAXTree into closed enums (property
// names, value types). Chrome adds members to those enums regularly, and when
// it does the whole reply fails to decode — "unknown PropertyName value:
// uninteresting" — even though the fields browser_inspect prints (role, name,
// value, child ids, DOM node id) are unchanged. Against a Chrome newer than the
// pinned cdproto that silently demoted every inspect to the DOM-text fallback,
// which has no roles and no element refs.
//
// So the AX tree is decoded here, reading only the fields we use and ignoring
// the rest. The nodes are returned as cdproto types so renderAXTree is
// unchanged.

type rawAXValue struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type rawAXNode struct {
	NodeID           accessibility.NodeID   `json:"nodeId"`
	Ignored          bool                   `json:"ignored"`
	Role             *rawAXValue            `json:"role"`
	Name             *rawAXValue            `json:"name"`
	Value            *rawAXValue            `json:"value"`
	ParentID         accessibility.NodeID   `json:"parentId"`
	ChildIDs         []accessibility.NodeID `json:"childIds"`
	BackendDOMNodeID cdp.BackendNodeID      `json:"backendDOMNodeId"`
}

type rawAXTree struct {
	Nodes []rawAXNode `json:"nodes"`
}

func (v *rawAXValue) toValue() *accessibility.Value {
	if v == nil {
		return nil
	}
	out := &accessibility.Value{Type: accessibility.ValueType(v.Type)}
	out.Value = append(out.Value[:0], v.Value...)
	return out
}

func (t rawAXTree) nodes() []*accessibility.Node {
	out := make([]*accessibility.Node, len(t.Nodes))
	for i, n := range t.Nodes {
		out[i] = &accessibility.Node{
			NodeID:           n.NodeID,
			Ignored:          n.Ignored,
			Role:             n.Role.toValue(),
			Name:             n.Name.toValue(),
			Value:            n.Value.toValue(),
			ParentID:         n.ParentID,
			ChildIDs:         n.ChildIDs,
			BackendDOMNodeID: n.BackendDOMNodeID,
		}
	}
	return out
}

// fullAXTree fetches the whole page's accessibility tree. ctx must be a
// chromedp action context (the executor is installed by chromedp.Run).
func fullAXTree(ctx context.Context) ([]*accessibility.Node, error) {
	var res rawAXTree
	if err := cdp.Execute(ctx, accessibility.CommandGetFullAXTree, accessibility.GetFullAXTree(), &res); err != nil {
		return nil, err
	}
	return res.nodes(), nil
}

// partialAXTree fetches the subtree around one DOM node, with relatives.
func partialAXTree(ctx context.Context, id cdp.NodeID) ([]*accessibility.Node, error) {
	var res rawAXTree
	p := accessibility.GetPartialAXTree().WithNodeID(id).WithFetchRelatives(true)
	if err := cdp.Execute(ctx, accessibility.CommandGetPartialAXTree, p, &res); err != nil {
		return nil, err
	}
	return res.nodes(), nil
}
