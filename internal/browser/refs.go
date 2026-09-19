package browser

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// refPattern matches an element ref token as printed by browser_inspect,
// e.g. "@e12". '@' can never start a valid CSS selector, so a ref and a
// selector are unambiguous in the same argument.
var refPattern = regexp.MustCompile(`^@e[0-9]+$`)

// isRef reports whether selector is an @eN element ref rather than CSS.
func isRef(selector string) bool {
	return refPattern.MatchString(strings.TrimSpace(selector))
}

// refTarget is what an @eN ref points at: a backend DOM node id, and the CDP
// session that id belongs to. The session is empty for the page's own —
// which covers same-process frames too — and names an iframe's auto-attach
// session when the frame runs in another process (site isolation), since a
// backend node id is only meaningful on the session that issued it.
type refTarget struct {
	node    proto.DOMBackendNodeID
	session proto.TargetSessionID
}

// trimRef strips the "@" (and surrounding space) from a ref token: "@e5" → "e5".
func trimRef(selector string) string {
	return strings.TrimPrefix(strings.TrimSpace(selector), "@")
}

// refTable holds the element refs handed out for one tracked page: each
// maps to the node it was issued for. Guarded by the owning trackedPage's mu.
type refTable struct {
	nodes map[string]refTarget
	count int // highest ref number issued, so a scoped snapshot continues past it
}

// registerRefs records the refs from one snapshot. An unscoped (whole-page)
// snapshot replaces the table — refs are only valid until the next one,
// which the tool descriptions say — while a scoped snapshot adds to it, so
// inspecting one subtree doesn't invalidate the refs of the last full view.
func (tp *trackedPage) registerRefs(r axRender, replace bool) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if replace || tp.refs.nodes == nil {
		tp.refs.nodes = make(map[string]refTarget, len(r.Refs))
	}
	maps.Copy(tp.refs.nodes, r.Refs)
	tp.refs.count = r.RefCount
}

// refCount is the highest ref number issued on this page so far.
func (tp *trackedPage) refCount() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.refs.count
}

func (tp *trackedPage) lookupRef(ref string) (refTarget, bool) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	rt, ok := tp.refs.nodes[ref]
	return rt, ok
}

// trackedFor returns the registry entry that owns pg, or nil.
func (s *session) trackedFor(pg *rod.Page) *trackedPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tp := range s.pages {
		if tp.page == pg {
			return tp
		}
	}
	return nil
}

// elementByRef resolves an @eN ref to a live element on pg. The ref maps to
// a backend DOM node id (stable while the node lives, across DOM mutations
// and re-renders), which CDP resolves to a remote object; a navigation or
// removal makes that resolution fail, which is reported as ErrStaleRef so
// the model knows to inspect again rather than retry.
func (s *session) elementByRef(ctx context.Context, pg *rod.Page, selector string) (*rod.Element, error) {
	ref := trimRef(selector)
	tp := s.trackedFor(pg)
	if tp == nil {
		return nil, fmt.Errorf("%w: %s (no tracked page)", ErrStaleRef, selector)
	}
	rt, ok := tp.lookupRef(ref)
	if !ok {
		return nil, fmt.Errorf("%w: %s was not issued for this tab — run browser_inspect first", ErrStaleRef, selector)
	}
	// A control in a cross-process frame has no rod element (see oopif.go);
	// click and type reach it by another route, and everything else says so.
	if rt.session != "" {
		return nil, fmt.Errorf("%w: %s is inside a cross-process iframe, where only browser_click and browser_type work", ErrCrossProcessFrame, selector)
	}
	bound := pg.Context(ctx)
	res, err := proto.DOMResolveNode{BackendNodeID: rt.node}.Call(bound)
	if err != nil {
		return nil, fmt.Errorf("%w: %s no longer exists on the page — run browser_inspect again", ErrStaleRef, selector)
	}
	el, err := bound.ElementFromObject(res.Object)
	if err != nil {
		return nil, fmt.Errorf("%w: %s could not be attached: %v", ErrStaleRef, selector, err)
	}
	return el, nil
}
