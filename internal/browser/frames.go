package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/chromedp"
)

// runChromedp runs f with chromedp's executor installed on its context.
func runChromedp(c context.Context, f func(ctx context.Context) error) error {
	return chromedp.Run(c, chromedp.ActionFunc(f))
}

const (
	maxAXFrames   = 8 // iframes expanded per snapshot
	maxFrameDepth = 3 // iframe nesting followed
)

// axForest is the page's accessibility tree with the content of its iframes
// grafted under their Iframe nodes, so one render shows (and gives refs to)
// everything on screen.
type axForest struct {
	nodes []*accessibility.Node
	// chains maps a node inside an iframe to the backend ids of the iframe
	// elements containing it, outermost first. Top-document nodes are absent.
	chains map[cdp.BackendNodeID][]cdp.BackendNodeID
}

// oopifReader fetches the accessibility tree of an out-of-process iframe by its
// frame id, or reports that it cannot.
type oopifReader func(ctx context.Context, frame cdp.FrameID) ([]*accessibility.Node, bool)

// oopifFrameNote marks a cross-process frame whose content is shown but cannot
// be acted on through refs.
const oopifFrameNote = "cross-origin frame: content is readable but has no refs; use browser_handoff to act inside it"

// collectAXForest fetches the main tree, then each iframe's tree, and links
// them. ctx must be a chromedp action context. A frame that cannot be read (a
// cross-process iframe, or one that went away) is left empty rather than
// failing the whole snapshot.
func collectAXForest(ctx context.Context, oopif oopifReader) (*axForest, error) {
	main, err := fullAXTree(ctx)
	if err != nil {
		return nil, err
	}
	f := &axForest{nodes: main, chains: map[cdp.BackendNodeID][]cdp.BackendNodeID{}}
	count := 0
	var expand func(level []*accessibility.Node, chain []cdp.BackendNodeID, prefix string, depth int)
	expand = func(level []*accessibility.Node, chain []cdp.BackendNodeID, prefix string, depth int) {
		for _, n := range level {
			if n.Ignored || axValueString(n.Role) != "Iframe" || n.BackendDOMNodeID == 0 || depth >= maxFrameDepth || count >= maxAXFrames {
				continue
			}
			d, err := dom.DescribeNode().WithBackendNodeID(n.BackendDOMNodeID).Do(ctx)
			if err != nil || d == nil || d.FrameID == "" {
				continue
			}
			sub, err := fullAXTreeFrame(ctx, d.FrameID)
			readOnly := false
			if err != nil || len(sub) == 0 {
				// Usually a cross-origin frame living in its own process. Its tree
				// comes from that process; its node ids belong to it, so they must
				// not become refs (they could collide with the page's own).
				if oopif != nil {
					if nodes, ok := oopif(ctx, d.FrameID); ok && len(nodes) > 0 {
						sub, readOnly = nodes, true
						for _, sn := range sub {
							sn.BackendDOMNodeID = 0
						}
					}
				}
				if !readOnly {
					// Say so, so an empty frame is not mistaken for an empty page.
					n.Value = &accessibility.Value{Type: "string", Value: []byte(`"content not readable (cross-origin frame)"`)}
					continue
				}
				n.Value = &accessibility.Value{Type: "string", Value: []byte(`"` + oopifFrameNote + `"`)}
			}
			count++
			pfx := fmt.Sprintf("%sf%d:", prefix, count)
			var root *accessibility.Node
			for _, sn := range sub {
				sn.NodeID = accessibility.NodeID(pfx) + sn.NodeID
				if sn.ParentID != "" {
					sn.ParentID = accessibility.NodeID(pfx) + sn.ParentID
				} else if root == nil {
					root = sn
				}
				for i, c := range sn.ChildIDs {
					sn.ChildIDs[i] = accessibility.NodeID(pfx) + c
				}
			}
			if root == nil {
				continue
			}
			root.ParentID = n.NodeID
			n.ChildIDs = append(n.ChildIDs, root.NodeID)
			inner := append(append([]cdp.BackendNodeID{}, chain...), n.BackendDOMNodeID)
			for _, sn := range sub {
				if sn.BackendDOMNodeID != 0 {
					f.chains[sn.BackendDOMNodeID] = inner
				}
			}
			f.nodes = append(f.nodes, sub...)
			if !readOnly { // a cross-process frame's own iframes are not reachable from here
				expand(sub, inner, pfx, depth+1)
			}
		}
	}
	expand(main, nil, "", 0)
	return f, nil
}

// collectForest is collectAXForest with this session's out-of-process iframe reader.
func (s *session) collectForest(ctx context.Context) (*axForest, error) {
	return collectAXForest(ctx, s.oopifReader())
}

// oopifReader reads a cross-process iframe's accessibility tree through the
// guard's session for that target, or returns nil when there is no guard.
func (s *session) oopifReader() oopifReader {
	g := s.guard
	if g == nil {
		return nil
	}
	return func(ctx context.Context, frame cdp.FrameID) ([]*accessibility.Node, bool) {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, _ = g.callTarget(cctx, string(frame), "Accessibility.enable", map[string]any{})
		raw, err := g.callTarget(cctx, string(frame), "Accessibility.getFullAXTree", map[string]any{})
		if err != nil {
			return nil, false
		}
		var tree rawAXTree
		if json.Unmarshal(raw, &tree) != nil {
			return nil, false
		}
		return tree.nodes(), true
	}
}

// dfs returns the forest's nodes in render order (document order, depth first).
func (f *axForest) dfs() []*accessibility.Node {
	by := map[accessibility.NodeID]*accessibility.Node{}
	for _, n := range f.nodes {
		by[n.NodeID] = n
	}
	var root *accessibility.Node
	for _, n := range f.nodes {
		if n.ParentID == "" {
			root = n
			break
		}
	}
	var out []*accessibility.Node
	var walk func(*accessibility.Node)
	walk = func(n *accessibility.Node) {
		if n == nil {
			return
		}
		out = append(out, n)
		for _, id := range n.ChildIDs {
			walk(by[id])
		}
	}
	walk(root)
	return out
}

// recoverRef re-resolves a ref whose node is gone or detached (the page
// re-rendered it) by matching the role and name it had when it was handed
// out. It only answers when that is safe: exactly one element matches, or the
// page still has the same number of identical elements as the snapshot and the
// ref's position among them picks one. Anything else — including a changed
// number of look-alikes — fails, so a ref can never silently land on the
// wrong "Delete" button.
func (s *session) recoverRef(c context.Context, ref string) (cdp.BackendNodeID, []cdp.BackendNodeID, bool) {
	s.mu.Lock()
	var meta refMeta
	ok := false
	if s.refs != nil {
		meta, ok = s.refs.meta[ref]
	}
	s.mu.Unlock()
	if !ok || meta.role == "" {
		return 0, nil, false
	}
	var forest *axForest
	if err := runChromedp(c, func(ctx context.Context) error {
		var err error
		forest, err = s.collectForest(ctx)
		return err
	}); err != nil {
		return 0, nil, false
	}
	var cands []*accessibility.Node
	for _, n := range forest.dfs() {
		if n.Ignored || n.BackendDOMNodeID == 0 || !interactiveRoles[axValueString(n.Role)] {
			continue
		}
		if axValueString(n.Role) == meta.role && axValueString(n.Name) == meta.name {
			cands = append(cands, n)
		}
	}
	var pick *accessibility.Node
	switch {
	case len(cands) == 1:
		pick = cands[0]
	case len(cands) > 1 && meta.nth >= 0 && meta.group != nil && len(cands) == *meta.group && meta.nth < len(cands):
		pick = cands[meta.nth]
	default:
		return 0, nil, false
	}
	id, chain := pick.BackendDOMNodeID, forest.chains[pick.BackendDOMNodeID]
	s.mu.Lock()
	if s.refs != nil {
		if old, had := s.refs.byRef[ref]; had {
			delete(s.refs.byNode, old)
		}
		s.refs.byRef[ref] = id
		s.refs.byNode[id] = ref
		if chain != nil {
			s.refs.chainOf[ref] = chain
		} else {
			delete(s.refs.chainOf, ref)
		}
	}
	s.mu.Unlock()
	return id, chain, true
}
