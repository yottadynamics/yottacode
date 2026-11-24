package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Selector forms every selector-taking browser tool accepts:
//
//	#login                      plain CSS (unchanged behavior)
//	ref=e12                     an element ref printed by browser_inspect
//	iframe#pay >>> #card        CSS inside an iframe (chain with >>> for nesting)
//	                            (refs also reach into iframes: browser_inspect lists frame
//	                            content, and a ref there is used as plain ref=eN)
//
// Refs exist because CSS selectors guessed from an accessibility tree are the
// main way agents click the wrong thing on dynamic pages. A ref is bound to the
// exact DOM node the snapshot described and stays valid until the next
// browser_inspect or navigation.

const frameSep = ">>>"

// selectorLookupTimeout bounds how long a frame or <select> lookup waits for
// its element to appear, so a typo fails in seconds rather than consuming the
// whole action timeout. A var so tests can shorten it.
var selectorLookupTimeout = 10 * time.Second

var refPattern = regexp.MustCompile(`^ref=(e[0-9]+)$`)

// refAttr is the DOM attribute a resolved ref is tagged with so that every
// existing CSS-based action can address the node unchanged.
const refAttr = "data-yottacode-ref"

// interactiveRoles are the AX roles that get a ref in browser_inspect output.
var interactiveRoles = map[string]bool{
	"button": true, "link": true, "textbox": true, "searchbox": true,
	"checkbox": true, "radio": true, "combobox": true, "listbox": true,
	"option": true, "menuitem": true, "menuitemcheckbox": true,
	"menuitemradio": true, "tab": true, "switch": true, "slider": true,
	"spinbutton": true, "treeitem": true,
}

// refTable maps refs to DOM nodes for one session. Replaced wholesale on every
// full-tree inspect so a stale ref can never silently address a different node.
type refTable struct {
	byRef  map[string]cdp.BackendNodeID
	byNode map[cdp.BackendNodeID]string
	next   int

	// chains maps a node inside an iframe to the backend ids of the iframe
	// elements that contain it, outermost first. The caller of a full render
	// fills it in before assign runs; chainOf keeps the result per ref.
	chains  map[cdp.BackendNodeID][]cdp.BackendNodeID
	chainOf map[string][]cdp.BackendNodeID

	// meta remembers what each ref pointed at (role, name, and which of the
	// identical-looking elements it was) so a ref whose node was re-rendered
	// can be re-resolved instead of failing as stale. groups counts, per
	// role+name, how many such elements the snapshot had.
	meta   map[string]refMeta
	groups map[string]*int
	// scoped marks a partial snapshot: positions inside it say nothing about
	// the whole page, so new refs get no position (nth = -1).
	scoped bool
}

type refMeta struct {
	role, name string
	nth        int  // position among same role+name in the snapshot, -1 if unknown
	group      *int // total same role+name in the snapshot
}

func newRefTable() *refTable {
	return &refTable{
		byRef:   map[string]cdp.BackendNodeID{},
		byNode:  map[cdp.BackendNodeID]string{},
		chainOf: map[string][]cdp.BackendNodeID{},
		meta:    map[string]refMeta{},
		groups:  map[string]*int{},
	}
}

func (t *refTable) assign(n *accessibility.Node) string {
	if t == nil || n == nil || n.BackendDOMNodeID == 0 || !interactiveRoles[axValueString(n.Role)] {
		return ""
	}
	id := n.BackendDOMNodeID
	role, name := axValueString(n.Role), axValueString(n.Name)
	// A node that already has a ref keeps it, so a ref stays valid across
	// re-inspects for as long as the node itself lives.
	ref, known := t.byNode[id]
	if !known {
		t.next++
		ref = fmt.Sprintf("e%d", t.next)
		t.byNode[id] = ref
	}
	t.byRef[ref] = id
	if chain, ok := t.chains[id]; ok {
		t.chainOf[ref] = chain
	} else {
		delete(t.chainOf, ref)
	}
	key := role + "\x00" + name
	switch {
	case !t.scoped:
		g := t.groups[key]
		if g == nil {
			g = new(int)
			t.groups[key] = g
		}
		t.meta[ref] = refMeta{role: role, name: name, nth: *g, group: g}
		*g++
	case !known:
		t.meta[ref] = refMeta{role: role, name: name, nth: -1}
	}
	return ref
}

// splitFrames separates "a >>> b >>> c" into frame selectors [a b] and the
// innermost selector c.
func splitFrames(sel string) (frames []string, inner string) {
	parts := strings.Split(sel, frameSep)
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts[:len(parts)-1], parts[len(parts)-1]
}

// plainQueryOpts picks the lookup strategy for a plain selector. chromedp's
// default (BySearch) runs DOM.performSearch, which also matches the page's own
// <style>/<script> text: "#menu" matches the CSS rule "#menu:hover{…}", and
// since that node is never visible, waiting for visibility times out. CSS is
// therefore resolved with querySelector; only an XPath expression keeps search.
func plainQueryOpts(sel string) []chromedp.QueryOption {
	t := strings.TrimSpace(sel)
	if strings.HasPrefix(t, "/") || strings.HasPrefix(t, "(") {
		return nil
	}
	return []chromedp.QueryOption{chromedp.ByQuery}
}

// query resolves an agent selector into the selector string and chromedp query
// options to run it with. A plain CSS selector returns unchanged with no
// options, so existing behavior is untouched.
func (s *session) query(c context.Context, sel string) (string, []chromedp.QueryOption, error) {
	frames, inner := splitFrames(sel)
	if len(frames) == 0 {
		if m := refPattern.FindStringSubmatch(inner); m != nil {
			css, err := s.tagRef(c, m[1])
			if err != nil {
				return "", nil, err
			}
			if !strings.Contains(css, frameSep) {
				return css, []chromedp.QueryOption{chromedp.ByQuery}, nil
			}
			// A ref inside an iframe resolves to "frame >>> element", which the
			// frame handling below walks like a hand-written path.
			frames, inner = splitFrames(css)
		} else {
			return inner, plainQueryOpts(inner), nil
		}
	} else if refPattern.MatchString(inner) {
		return "", nil, fmt.Errorf("%w: write a ref on its own (ref=eN reaches into iframes by itself); %q cannot prefix a ref", ErrSelectorNotFound, frameSep)
	}
	var opts []chromedp.QueryOption
	for _, f := range frames {
		if f == "" {
			return "", nil, fmt.Errorf("%w: empty frame selector before %q", ErrSelectorNotFound, frameSep)
		}
		var nodes []*cdp.Node
		qo := append(append([]chromedp.QueryOption{}, opts...), chromedp.ByQuery, chromedp.AtLeast(1))
		lc, lcancel := context.WithTimeout(c, selectorLookupTimeout)
		err := chromedp.Run(lc, chromedp.Nodes(f, &nodes, qo...))
		lcancel()
		if err != nil {
			if errors.Is(lc.Err(), context.DeadlineExceeded) {
				return "", nil, fmt.Errorf("%w: frame %q did not appear within %s", ErrSelectorNotFound, f, selectorLookupTimeout)
			}
			return "", nil, classifySelectorErr(fmt.Errorf("frame %q: %w", f, err))
		}
		if len(nodes) == 0 {
			return "", nil, fmt.Errorf("%w: frame %q matched nothing", ErrSelectorNotFound, f)
		}
		opts = []chromedp.QueryOption{chromedp.FromNode(nodes[0])}
	}
	return inner, append(opts, chromedp.ByQuery), nil
}

// frameAttr marks an iframe element on the way to a ref inside it.
const frameAttr = "data-yottacode-frame"

// tagRef marks the DOM node behind ref with a unique attribute and returns the
// CSS selector for it ("frame >>> element" for a node inside an iframe). If the
// node was re-rendered since the snapshot, the ref is re-resolved by role and
// name when that is unambiguous; otherwise it fails as stale.
func (s *session) tagRef(c context.Context, ref string) (string, error) {
	s.mu.Lock()
	var id cdp.BackendNodeID
	var chain []cdp.BackendNodeID
	ok := false
	if s.refs != nil {
		id, ok = s.refs.byRef[ref]
		chain = s.refs.chainOf[ref]
	}
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("%w: unknown element ref %q — refs come from the latest browser_inspect; run it again", ErrSelectorNotFound, ref)
	}
	sel, err := s.tagBackend(c, id, chain)
	if err != nil && c.Err() == nil {
		if rid, rchain, found := s.recoverRef(c, ref); found {
			sel, err = s.tagBackend(c, rid, rchain)
		}
	}
	if err != nil {
		if c.Err() != nil {
			return "", classifySelectorErr(c.Err())
		}
		return "", fmt.Errorf("%w: element ref %q is stale (the page changed) and no single element matches it any more; run browser_inspect again", ErrSelectorNotFound, ref)
	}
	return sel, nil
}

// tagBackend tags the element (and the iframe elements leading to it) and
// returns the selector. It fails when a node is gone or no longer in the document.
func (s *session) tagBackend(c context.Context, id cdp.BackendNodeID, chain []cdp.BackendNodeID) (string, error) {
	newTag := func(prefix string) string {
		var nonce [8]byte
		_, _ = rand.Read(nonce[:])
		return prefix + "-" + hex.EncodeToString(nonce[:])
	}
	// The tag value carries a random nonce so a page cannot pre-plant an element
	// that the selector would match first; and any earlier ref tag is removed so
	// a reused ref number can never match two nodes.
	var parts []string
	err := chromedp.Run(c, chromedp.ActionFunc(func(ctx context.Context) error {
		set := func(backend cdp.BackendNodeID, attr, tag string, clearOld bool) error {
			obj, err := dom.ResolveNode().WithBackendNodeID(backend).Do(ctx)
			if err != nil {
				return err
			}
			arg, _ := json.Marshal(tag)
			clear := ""
			if clearOld {
				clear = `this.ownerDocument.querySelectorAll('[` + attr + `]').forEach(function(e){e.removeAttribute('` + attr + `')});`
			}
			res, _, err := runtime.CallFunctionOn(`function(r){` + clear + `this.setAttribute('` + attr + `', r); return this.isConnected;}`).
				WithObjectID(obj.ObjectID).
				WithArguments([]*runtime.CallArgument{{Value: arg}}).
				WithReturnByValue(true).Do(ctx)
			if err != nil {
				return err
			}
			if res == nil || string(res.Value) != "true" {
				return fmt.Errorf("node is detached from the document")
			}
			return nil
		}
		for _, owner := range chain {
			tag := newTag("f")
			if err := set(owner, frameAttr, tag, false); err != nil {
				return err
			}
			parts = append(parts, `[`+frameAttr+`="`+tag+`"]`)
		}
		tag := newTag("r")
		if err := set(id, refAttr, tag, true); err != nil {
			return err
		}
		parts = append(parts, `[`+refAttr+`="`+tag+`"]`)
		return nil
	}))
	if err != nil {
		return "", err
	}
	return strings.Join(parts, " "+frameSep+" "), nil
}

// nextRefs starts a new inspect generation. Numbering continues and nodes that
// already had a ref keep it. A full inspect keeps only the refs it sees again;
// a scoped one (keepAll) keeps every earlier ref usable. Refs are dropped
// wholesale on navigation, and a ref whose node has gone away fails as stale.
func (s *session) nextRefs(keepAll bool) *refTable {
	t := newRefTable()
	t.scoped = keepAll
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.refs; old != nil {
		t.next = old.next
		for id, ref := range old.byNode {
			t.byNode[id] = ref
		}
		if keepAll {
			for ref, id := range old.byRef {
				t.byRef[ref] = id
				t.chainOf[ref] = old.chainOf[ref]
				t.meta[ref] = old.meta[ref]
			}
		}
	}
	s.refs = t
	return t
}
