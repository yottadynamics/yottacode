package browser

import (
	"context"
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// Cross-process frames.
//
// With site isolation on, an iframe from another *site* runs in its own
// process and is its own CDP target: it does not appear in the page's frame
// tree, its accessibility tree cannot be read over the page's session, and
// its DOM node ids mean nothing there. Site isolation is a real security
// boundary, and a browser that visits arbitrary sites should keep it on — so
// frame support must not depend on it being off. (rod's launcher turns it off
// by default; see childFrames for the in-process case.)
//
// Chrome refuses Target.attachToTarget for an iframe target, so such a frame
// is reachable only through the session the *parent page* auto-attaches to it
// with. rod cannot build a *Page on that session (its element helpers need
// state only its own attach sets up), so a cross-process frame is read, and
// driven, with raw session-scoped CDP: enough for what matters there —
// listing its controls and clicking/typing into them — and nothing more
// (see ErrCrossProcessFrame).
//
// Each frame is attached with waitForDebuggerOnStart so the request guard can
// be armed on it *before* it makes its first request — a hostile iframe
// pointed at a cloud-metadata endpoint must not win that race — and it is
// resumed only afterwards.

// oopif is one cross-process iframe of a tracked page.
type oopif struct {
	// target is the frame's CDP target id, which is also its frame id.
	target  proto.TargetTargetID
	session proto.TargetSessionID
	url     string
}

// sessionClient sends CDP commands on one specific session of a browser —
// here the auto-attach session of a cross-process frame. It implements
// proto.Client (and proto.Contextable, so calls honor ctx).
type sessionClient struct {
	br  *rod.Browser
	sid proto.TargetSessionID
	ctx context.Context
}

// Call implements proto.Client.
func (c sessionClient) Call(ctx context.Context, _ string, method string, params any) ([]byte, error) {
	return c.br.Call(ctx, string(c.sid), method, params)
}

// GetContext implements proto.Contextable.
func (c sessionClient) GetContext() context.Context {
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

// startOOPIFTracking makes pg auto-attach to its cross-process iframes and
// keeps tp's list of them current. Best effort: if auto-attach can't be
// enabled the page simply has no cross-process frame support.
func startOOPIFTracking(pg *rod.Page, tp *trackedPage) {
	br := pg.Browser()
	err := proto.TargetSetAutoAttach{
		AutoAttach:             true,
		WaitForDebuggerOnStart: true,
		Flatten:                true,
		// Only iframes: workers and the rest are left alone, so nothing is
		// paused that this code would then have to resume.
		Filter: proto.TargetTargetFilter{{Type: "iframe"}},
	}.Call(pg)
	if err != nil {
		return
	}
	go pg.EachEvent(func(e *proto.TargetAttachedToTarget) {
		if e.TargetInfo == nil || e.TargetInfo.Type != "iframe" {
			// Not ours to track, but if it is paused it must still run.
			if e.WaitingForDebugger {
				_ = proto.RuntimeRunIfWaitingForDebugger{}.Call(sessionClient{br: br, sid: e.SessionID})
			}
			return
		}
		armChildGuard(br, e.SessionID, tp)
		tp.addOOPIF(oopif{target: e.TargetInfo.TargetID, session: e.SessionID, url: e.TargetInfo.URL})
		// Resume no matter what: a frame left waiting for the debugger never runs.
		if e.WaitingForDebugger {
			_ = proto.RuntimeRunIfWaitingForDebugger{}.Call(sessionClient{br: br, sid: e.SessionID})
		}
	}, func(e *proto.TargetDetachedFromTarget) {
		tp.removeOOPIF(e.SessionID)
	})()
}

// armChildGuard arms the metadata-endpoint guard on a cross-process frame's
// session, exactly as armRequestGuard does on a page's. Events for the frame
// arrive on the browser's stream tagged with its session id; the loop ends
// when the frame detaches (or the browser closes).
func armChildGuard(br *rod.Browser, sid proto.TargetSessionID, tp *trackedPage) {
	c := sessionClient{br: br, sid: sid}
	auth := tp.proxyAuth
	if err := (proto.FetchEnable{Patterns: guardPatterns(auth != nil), HandleAuthRequests: auth != nil}).Call(c); err != nil {
		return
	}
	bctx, cancel := br.WithCancel()
	events := bctx.Event()
	go func() {
		defer cancel()
		for msg := range events {
			switch {
			case msg.SessionID == sid && msg.Method == (proto.FetchRequestPaused{}).ProtoEvent():
				var e proto.FetchRequestPaused
				msg.Load(&e)
				answerPaused(c, tp, &e, auth != nil, "")
			case msg.SessionID == sid && msg.Method == (proto.FetchAuthRequired{}).ProtoEvent():
				var e proto.FetchAuthRequired
				msg.Load(&e)
				answerAuth(c, auth, &e)
			case msg.Method == (proto.TargetDetachedFromTarget{}).ProtoEvent():
				var d proto.TargetDetachedFromTarget
				msg.Load(&d)
				if d.SessionID == sid {
					return
				}
			}
		}
	}()
}

func (tp *trackedPage) addOOPIF(o oopif) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	for i, existing := range tp.oopifs {
		if existing.target == o.target {
			tp.oopifs[i] = o
			return
		}
	}
	tp.oopifs = append(tp.oopifs, o)
}

func (tp *trackedPage) removeOOPIF(session proto.TargetSessionID) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	for i, o := range tp.oopifs {
		if o.session == session {
			tp.oopifs = append(tp.oopifs[:i], tp.oopifs[i+1:]...)
			return
		}
	}
}

// oopifList is a snapshot of tp's cross-process iframes.
func (tp *trackedPage) oopifList() []oopif {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return append([]oopif(nil), tp.oopifs...)
}

// oopifBySession finds the tracked frame that owns a session.
func (tp *trackedPage) oopifBySession(session proto.TargetSessionID) (oopif, bool) {
	for _, o := range tp.oopifList() {
		if o.session == session {
			return o, true
		}
	}
	return oopif{}, false
}

// oopifSections reads the accessibility tree of each of tp's cross-process
// iframes, bounded like childFrames. A frame that can't be read is skipped,
// not fatal: it may have been navigated away or destroyed since it attached.
func (s *session) oopifSections(ctx context.Context, tp *trackedPage, budget int) []axSection {
	var out []axSection
	for _, o := range tp.oopifList() {
		if budget <= 0 {
			break
		}
		c := sessionClient{br: s.browser, sid: o.session, ctx: ctx}
		_ = proto.AccessibilityEnable{}.Call(c)
		res, err := proto.AccessibilityGetFullAXTree{}.Call(c)
		if err != nil || len(res.Nodes) == 0 {
			continue
		}
		out = append(out, axSection{Label: oopifLabel(tp.page.Context(ctx), c, o), Nodes: res.Nodes, Session: o.session})
		budget--
	}
	return out
}

// oopifLabel names a cross-process frame like childFrames names the others:
// `frame "name" url`, from the frame's own view of itself when it answers.
func oopifLabel(parent proto.Client, c sessionClient, o oopif) string {
	url, name := o.url, ""
	if ft, err := (proto.PageGetFrameTree{}).Call(c); err == nil && ft.FrameTree != nil && ft.FrameTree.Frame != nil {
		name = ft.FrameTree.Frame.Name
		if ft.FrameTree.Frame.URL != "" {
			url = ft.FrameTree.Frame.URL
		}
	}
	if name == "" {
		// OOPIF frame targets do not always expose the iframe's HTML name in
		// their own frame tree. Recover it from the parent frame owner so the
		// inspect output remains stable across Chrome's process boundary.
		if owner, err := (proto.DOMGetFrameOwner{FrameID: proto.PageFrameID(o.target)}).Call(parent); err == nil && owner != nil && owner.BackendNodeID != 0 {
			if node, err := (proto.DOMDescribeNode{BackendNodeID: owner.BackendNodeID}).Call(parent); err == nil && node.Node != nil {
				for i := 0; i+1 < len(node.Node.Attributes); i += 2 {
					if node.Node.Attributes[i] == "id" || node.Node.Attributes[i] == "name" {
						name = node.Node.Attributes[i+1]
						break
					}
				}
			}
		}
	}
	return frameLabel(&proto.PageFrame{Name: name, URL: url})
}

// crossProcessRef resolves selector, when it is an @eN ref into a
// cross-process frame, to the frame and node it names. ok is false for
// anything else (a CSS selector, a page or same-process ref), which the
// ordinary rod path handles.
func (s *session) crossProcessRef(tp *trackedPage, selector string) (rt refTarget, o oopif, ok bool) {
	if !isRef(selector) {
		return refTarget{}, oopif{}, false
	}
	rt, found := tp.lookupRef(trimRef(selector))
	if !found || rt.session == "" {
		return refTarget{}, oopif{}, false
	}
	o, live := tp.oopifBySession(rt.session)
	if !live {
		// The frame is gone: report the ref as stale rather than fall through
		// to the page's own session, where the node id names something else.
		return rt, oopif{}, true
	}
	return rt, o, true
}

// errFrameGone is returned for a ref into a cross-process frame that has
// since detached.
func errFrameGone(selector string) error {
	return fmt.Errorf("%w: %s is in a frame that has gone away — run browser_inspect again", ErrStaleRef, selector)
}

// crossProcessClick clicks a control inside a cross-process frame. It works
// in two coordinate spaces: the control's box is read on the frame's own
// session (relative to the frame's viewport), the frame's origin is read on
// the page's session (the <iframe> element's content box), and the click is
// dispatched through the *page's* mouse at the sum — Chrome hit-tests that
// point into the right frame.
func (s *session) crossProcessClick(ctx context.Context, tp *trackedPage, selector string, rt refTarget, o oopif) error {
	if o.session == "" {
		return errFrameGone(selector)
	}
	c := sessionClient{br: s.browser, sid: o.session, ctx: ctx}
	_ = proto.DOMScrollIntoViewIfNeeded{BackendNodeID: rt.node}.Call(c)
	q, err := proto.DOMGetContentQuads{BackendNodeID: rt.node}.Call(c)
	if err != nil || len(q.Quads) == 0 || len(q.Quads[0]) < 8 {
		return fmt.Errorf("%w: %s has no on-screen box in its frame — run browser_inspect again", ErrStaleRef, selector)
	}
	lx0, ly0, lx1, ly1 := quadBounds(q.Quads[0])

	pg := tp.page.Context(ctx)
	owner, err := proto.DOMGetFrameOwner{FrameID: proto.PageFrameID(o.target)}.Call(pg)
	if err != nil {
		return errFrameGone(selector)
	}
	oq, err := proto.DOMGetContentQuads{BackendNodeID: owner.BackendNodeID}.Call(pg)
	if err != nil || len(oq.Quads) == 0 || len(oq.Quads[0]) < 8 {
		return fmt.Errorf("browser click: the frame containing %s is not visible", selector)
	}
	ox, oy, _, _ := quadBounds(oq.Quads[0])

	pt := proto.NewPoint(ox+(lx0+lx1)/2, oy+(ly0+ly1)/2)
	return s.followNewPage(ctx, tp.page, func() error {
		if err := pg.Mouse.MoveTo(pt); err != nil {
			return fmt.Errorf("browser click: %w", err)
		}
		if err := pg.Mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return fmt.Errorf("browser click: %w", err)
		}
		return nil
	})
}

// crossProcessType focuses a field inside a cross-process frame, selects its
// current contents so the typed text replaces them, and inserts the text —
// all on the frame's own session, where the focus lives. submit then presses
// Enter through the page (keyboard input follows focus).
func (s *session) crossProcessType(ctx context.Context, tp *trackedPage, selector, text string, submit bool, rt refTarget, o oopif) error {
	if o.session == "" {
		return errFrameGone(selector)
	}
	c := sessionClient{br: s.browser, sid: o.session, ctx: ctx}
	if err := (proto.DOMFocus{BackendNodeID: rt.node}).Call(c); err != nil {
		return fmt.Errorf("%w: %s can't be focused in its frame: %v", ErrStaleRef, selector, err)
	}
	if res, err := (proto.DOMResolveNode{BackendNodeID: rt.node}).Call(c); err == nil {
		_, _ = proto.RuntimeCallFunctionOn{
			ObjectID:            res.Object.ObjectID,
			FunctionDeclaration: "function() { if (this.select) { this.select(); } }",
		}.Call(c)
	}
	if err := (proto.InputInsertText{Text: text}).Call(c); err != nil {
		return fmt.Errorf("browser type: %w", err)
	}
	if !submit {
		return nil
	}
	pg := tp.page.Context(ctx)
	return s.followNewPage(ctx, tp.page, func() error {
		if err := pg.KeyActions().Type(input.Enter).Do(); err != nil {
			return fmt.Errorf("browser type: submit: %w", err)
		}
		return nil
	})
}
