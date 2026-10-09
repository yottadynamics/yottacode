package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
//	iframe#pay >>> ref=e3       not supported: refs only address the top document
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
	byRef map[string]cdp.BackendNodeID
	next  int
}

func newRefTable() *refTable { return &refTable{byRef: map[string]cdp.BackendNodeID{}} }

func (t *refTable) assign(n *accessibility.Node) string {
	if t == nil || n == nil || n.BackendDOMNodeID == 0 || !interactiveRoles[axValueString(n.Role)] {
		return ""
	}
	t.next++
	ref := fmt.Sprintf("e%d", t.next)
	t.byRef[ref] = n.BackendDOMNodeID
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
			return css, []chromedp.QueryOption{chromedp.ByQuery}, nil
		}
		return inner, nil, nil
	}
	if refPattern.MatchString(inner) {
		return "", nil, fmt.Errorf("%w: refs address the top document only; use a CSS selector after %q", ErrSelectorNotFound, frameSep)
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
			return "", nil, classifySelectorErr(fmt.Errorf("frame %q: %w", f, err))
		}
		if len(nodes) == 0 {
			return "", nil, fmt.Errorf("%w: frame %q matched nothing", ErrSelectorNotFound, f)
		}
		opts = []chromedp.QueryOption{chromedp.FromNode(nodes[0])}
	}
	return inner, append(opts, chromedp.ByQuery), nil
}

// tagRef marks the DOM node behind ref with a unique attribute and returns the
// CSS selector for it.
func (s *session) tagRef(c context.Context, ref string) (string, error) {
	s.mu.Lock()
	var id cdp.BackendNodeID
	ok := false
	if s.refs != nil {
		id, ok = s.refs.byRef[ref]
	}
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("%w: unknown element ref %q — refs come from the latest browser_inspect; run it again", ErrSelectorNotFound, ref)
	}
	// The tag value carries a random nonce so a page cannot pre-plant an element
	// that the selector would match first; and any earlier tag is removed so a
	// reused ref number can never match two nodes.
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	tag := ref + "-" + hex.EncodeToString(nonce[:])
	arg, _ := json.Marshal(tag)
	err := chromedp.Run(c, chromedp.ActionFunc(func(ctx context.Context) error {
		obj, err := dom.ResolveNode().WithBackendNodeID(id).Do(ctx)
		if err != nil {
			return err
		}
		_, _, err = runtime.CallFunctionOn(`function(r){
			this.ownerDocument.querySelectorAll('[` + refAttr + `]').forEach(function(e){e.removeAttribute('` + refAttr + `')});
			this.setAttribute('` + refAttr + `', r);
		}`).
			WithObjectID(obj.ObjectID).
			WithArguments([]*runtime.CallArgument{{Value: arg}}).Do(ctx)
		return err
	}))
	if err != nil {
		return "", fmt.Errorf("%w: element ref %q is stale (the page changed); run browser_inspect again", ErrSelectorNotFound, ref)
	}
	return `[` + refAttr + `="` + tag + `"]`, nil
}

func (s *session) resetRefs() *refTable {
	t := newRefTable()
	s.mu.Lock()
	s.refs = t
	s.mu.Unlock()
	return t
}
