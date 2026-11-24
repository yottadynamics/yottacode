package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// ClickOptions selects a non-default click. The zero value is a plain left click.
type ClickOptions struct {
	Button string // "left" (default), "right", or "middle"
	Double bool
}

func (o ClickOptions) plain() bool {
	return (o.Button == "" || o.Button == "left") && !o.Double
}

// Validate rejects an unknown button before any browser work happens.
func (o ClickOptions) Validate() error {
	switch o.Button {
	case "", "left", "right", "middle":
		return nil
	}
	return fmt.Errorf("unknown mouse button %q (use left, right, or middle)", o.Button)
}

func (o ClickOptions) mouseOptions() []chromedp.MouseOption {
	var opts []chromedp.MouseOption
	switch o.Button {
	case "right":
		opts = append(opts, chromedp.ButtonType(input.Right))
	case "middle":
		opts = append(opts, chromedp.ButtonType(input.Middle))
	}
	if o.Double {
		opts = append(opts, chromedp.ClickCount(2))
	}
	return opts
}

const (
	settleDOMQuiet    = 250 * time.Millisecond
	settleDOMCap      = 1500 * time.Millisecond
	settleNetworkCap  = 3 * time.Second
	settleNetworkIdle = 200 * time.Millisecond
)

// settle waits, bounded, for the page to stop changing: no request in flight
// and no DOM mutation for a short quiet period. It never fails; a page that
// never goes quiet (animation, polling) just proceeds after the cap.
func (s *session) settle(ctx context.Context, p *trackedPage) {
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	nc, ncancel := context.WithTimeout(c, settleNetworkCap)
	_ = s.waitNetworkIdle(nc, p, settleNetworkIdle)
	ncancel()
	dc, dcancel := context.WithTimeout(c, settleDOMCap)
	defer dcancel()
	var ok bool
	_ = chromedp.Run(dc, chromedp.Evaluate(fmt.Sprintf(`new Promise(function(resolve){
		var t, mo = new MutationObserver(function(){ clearTimeout(t); t = setTimeout(done, %d); });
		function done(){ mo.disconnect(); resolve(true); }
		mo.observe(document, {subtree:true, childList:true, attributes:true, characterData:true});
		t = setTimeout(done, %d);
	})`, settleDOMQuiet.Milliseconds(), settleDOMQuiet.Milliseconds()), &ok, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}))
}

// PageDigest is a cheap snapshot of the visible, actionable state of the page,
// taken before and after an action so the model can see what the action did
// without re-reading the whole tree.
type PageDigest struct {
	Valid    bool
	URL      string
	Title    string
	Elements []string // "radio \"B\" (checked)", "button \"Submit\"", …
	Alerts   []string // text of role=alert/status regions
	Dialogs  []string // JS dialogs the session auto-answered
}

const digestJS = `(function(){
	function vis(el){ var r = el.getBoundingClientRect(); if (!r.width && !r.height) return false;
		var s = getComputedStyle(el); return s.visibility !== 'hidden' && s.display !== 'none'; }
	function clean(t){ return (t||'').replace(/\s+/g,' ').trim().slice(0,80); }
	function name(el){
		var t = clean(el.getAttribute('aria-label'));
		var control = el.tagName === 'INPUT' && !/^(button|submit|reset|image)$/.test(el.type) || el.tagName === 'SELECT' || el.tagName === 'TEXTAREA';
		if (!t && control) {
			if (el.labels && el.labels.length) t = clean(el.labels[0].innerText);
			if (!t && el.closest) { var l = el.closest('label'); if (l) t = clean(l.innerText); }
			if (!t) t = clean(el.placeholder || el.title);
			return t;
		}
		return t || clean(el.innerText || el.value || el.title);
	}
	var els = [], q = 'a[href],button,input:not([type=hidden]),select,textarea,[role=button],[role=link],[role=radio],[role=checkbox],[role=tab],[role=menuitem],[role=switch]';
	document.querySelectorAll(q).forEach(function(el){
		if (els.length >= 150) return;
		var isInput = el.tagName === 'INPUT' && (el.type === 'radio' || el.type === 'checkbox');
		if (!isInput && !vis(el)) return;
		var role = el.getAttribute('role') || (isInput ? el.type : el.tagName === 'A' ? 'link' : el.tagName === 'INPUT' ? 'textbox' : el.tagName.toLowerCase());
		var st = [];
		if (el.checked || el.getAttribute('aria-checked') === 'true') st.push('checked');
		if (el.disabled || el.getAttribute('aria-disabled') === 'true') st.push('disabled');
		if (el.getAttribute('aria-selected') === 'true') st.push('selected');
		if (el.getAttribute('aria-expanded') === 'true') st.push('expanded');
		var line = role; var n = name(el); if (n) line += ' "' + n + '"';
		if ((el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') && !isInput && el.type !== 'password' && el.value) line += ' ="' + el.value.slice(0,40) + '"';
		if (st.length) line += ' (' + st.join(', ') + ')';
		els.push(line);
	});
	var alerts = [];
	document.querySelectorAll('[role=alert],[role=status],[aria-live=assertive]').forEach(function(el){
		var t = (el.innerText||'').replace(/\s+/g,' ').trim().slice(0,200); if (t && alerts.length < 5) alerts.push(t);
	});
	return JSON.stringify({url: location.href, title: document.title, els: els, alerts: alerts});
})()`

// digest snapshots the page. With settle, it first waits for the page to go quiet.
func (s *session) digest(ctx context.Context, settle bool) (PageDigest, error) {
	p := s.activePage()
	if p == nil {
		return PageDigest{}, errors.New("no active page")
	}
	if settle {
		s.settle(ctx, p)
	}
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	var raw string
	if err := chromedp.Run(c, chromedp.Evaluate(digestJS, &raw)); err != nil {
		return PageDigest{}, err
	}
	var v struct {
		URL, Title string
		Els        []string
		Alerts     []string
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return PageDigest{}, err
	}
	d := PageDigest{Valid: true, URL: v.URL, Title: v.Title, Elements: v.Els, Alerts: v.Alerts}
	for _, e := range p.consoleSnapshot(maxBufferedEntries) {
		if e.Level == "dialog" {
			d.Dialogs = append(d.Dialogs, e.Text)
		}
	}
	return d, nil
}

// DiffDigest describes what changed between two digests, or "" if nothing did.
// The result contains page-controlled text and must be treated as untrusted.
func DiffDigest(before, after PageDigest) string {
	if !before.Valid || !after.Valid {
		return ""
	}
	var lines []string
	if before.URL != after.URL {
		lines = append(lines, fmt.Sprintf("url: %s -> %s", before.URL, after.URL))
	}
	if before.Title != after.Title {
		lines = append(lines, fmt.Sprintf("title: %q -> %q", before.Title, after.Title))
	}
	added, removed := multisetDiff(before.Elements, after.Elements)
	lines = append(lines, capLines("removed", removed, 8)...)
	lines = append(lines, capLines("added", added, 8)...)
	newAlerts, _ := multisetDiff(before.Alerts, after.Alerts)
	for _, a := range newAlerts {
		lines = append(lines, "alert: "+a)
	}
	newDialogs, _ := multisetDiff(before.Dialogs, after.Dialogs)
	for _, d := range newDialogs {
		lines = append(lines, "dialog: "+d)
	}
	return strings.Join(lines, "\n")
}

func capLines(label string, items []string, max int) []string {
	var out []string
	for i, it := range items {
		if i == max {
			out = append(out, fmt.Sprintf("%s: …and %d more", label, len(items)-max))
			break
		}
		out = append(out, label+": "+it)
	}
	return out
}

// multisetDiff returns items only in after (added) and only in before (removed).
func multisetDiff(before, after []string) (added, removed []string) {
	count := map[string]int{}
	for _, b := range before {
		count[b]++
	}
	for _, a := range after {
		if count[a] > 0 {
			count[a]--
		} else {
			added = append(added, a)
		}
	}
	seen := map[string]int{}
	for _, a := range after {
		seen[a]++
	}
	for _, b := range before {
		if seen[b] > 0 {
			seen[b]--
		} else {
			removed = append(removed, b)
		}
	}
	return added, removed
}

// nodeCenter scrolls n into view and returns the centre of its first content quad.
func nodeCenter(ctx context.Context, n *cdp.Node) (x, y float64, err error) {
	if err := dom.ScrollIntoViewIfNeeded().WithNodeID(n.NodeID).Do(ctx); err != nil {
		return 0, 0, err
	}
	boxes, err := dom.GetContentQuads().WithNodeID(n.NodeID).Do(ctx)
	if err != nil {
		return 0, 0, err
	}
	if len(boxes) == 0 || len(boxes[0]) < 2 || len(boxes[0])%2 != 0 {
		return 0, 0, chromedp.ErrInvalidDimensions
	}
	q := boxes[0]
	for i := 0; i < len(q); i += 2 {
		x += q[i]
		y += q[i+1]
	}
	k := float64(len(q) / 2)
	return x / k, y / k, nil
}

// elementCenter resolves an agent selector to the on-screen centre of its element.
func (s *session) elementCenter(c context.Context, sel string) (x, y float64, err error) {
	inner, opts, err := s.query(c, sel)
	if err != nil {
		return 0, 0, err
	}
	vc, vcancel := context.WithTimeout(c, clickVisibleWait)
	defer vcancel()
	err = chromedp.Run(vc, chromedp.QueryAfter(inner, func(ctx context.Context, _ runtime.ExecutionContextID, nodes ...*cdp.Node) error {
		if len(nodes) == 0 {
			return fmt.Errorf("selector %q did not return any nodes", sel)
		}
		var e error
		x, y, e = nodeCenter(ctx, nodes[0])
		return e
	}, append(append([]chromedp.QueryOption{}, opts...), chromedp.NodeVisible)...))
	if err != nil {
		if c.Err() != nil {
			return 0, 0, ctxFailure(c, fmt.Sprintf("locating %q", sel), err)
		}
		return 0, 0, classifySelectorErr(err)
	}
	return x, y, nil
}

func (s *session) hover(ctx context.Context, sel string) error {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	x, y, err := s.elementCenter(c, sel)
	if err != nil {
		return err
	}
	return classifySelectorErr(chromedp.Run(c, chromedp.MouseEvent(input.MouseMoved, x, y)))
}

// drag presses on the centre of from, moves in steps and releases over to.
// It synthesises mouse events, which drives sliders, sortable lists and
// canvas-style widgets; native HTML5 draggable elements may not respond.
func (s *session) drag(ctx context.Context, from, to string) error {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	x1, y1, err := s.elementCenter(c, from)
	if err != nil {
		return err
	}
	x2, y2, err := s.elementCenter(c, to)
	if err != nil {
		return err
	}
	const steps = 8
	acts := []chromedp.Action{
		chromedp.MouseEvent(input.MouseMoved, x1, y1),
		chromedp.MouseEvent(input.MousePressed, x1, y1, chromedp.ButtonType(input.Left), chromedp.ClickCount(1)),
	}
	for i := 1; i <= steps; i++ {
		f := float64(i) / steps
		acts = append(acts, chromedp.MouseEvent(input.MouseMoved, x1+(x2-x1)*f, y1+(y2-y1)*f, chromedp.ButtonType(input.Left)))
	}
	acts = append(acts, chromedp.MouseEvent(input.MouseReleased, x2, y2, chromedp.ButtonType(input.Left), chromedp.ClickCount(1)))
	return classifySelectorErr(chromedp.Run(c, acts...))
}

// mouseClick performs a click that plain chromedp.Click cannot: right/middle
// button or a double click.
func (s *session) mouseClick(c context.Context, inner string, opts []chromedp.QueryOption, o ClickOptions) error {
	return chromedp.Run(c, chromedp.QueryAfter(inner, func(ctx context.Context, _ runtime.ExecutionContextID, nodes ...*cdp.Node) error {
		if len(nodes) == 0 {
			return fmt.Errorf("selector %q did not return any nodes", inner)
		}
		return chromedp.MouseClickNode(nodes[0], o.mouseOptions()...).Do(ctx)
	}, append(append([]chromedp.QueryOption{}, opts...), chromedp.NodeVisible)...))
}

// find lists accessibility nodes whose name or value contains text (case
// insensitive) and/or whose role equals role, with a ref for interactive ones.
func (s *session) find(ctx context.Context, text, role string, limit int) (string, error) {
	p := s.activePage()
	s.settle(ctx, p)
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	var forest *axForest
	if err := runChromedp(c, func(execCtx context.Context) error {
		var err error
		forest, err = collectAXForest(execCtx)
		return err
	}); err != nil {
		return "", classifySelectorErr(fmt.Errorf("browser find: %w", err))
	}
	nodes := forest.nodes
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	refs := s.nextRefs(true)
	refs.chains = forest.chains
	by := map[accessibility.NodeID]*accessibility.Node{}
	for _, n := range nodes {
		by[n.NodeID] = n
	}
	needle := strings.ToLower(strings.TrimSpace(text))
	var out []string
	total := 0
	for _, n := range nodes {
		if n.Ignored {
			continue
		}
		r := axValueString(n.Role)
		if noiseRoles[r] || wrapperRoles[r] || r == "StaticText" || r == "RootWebArea" {
			continue
		}
		if role != "" && !strings.EqualFold(r, role) {
			continue
		}
		name, val := axValueString(n.Name), axValueString(n.Value)
		if needle != "" && !strings.Contains(strings.ToLower(name), needle) && !strings.Contains(strings.ToLower(val), needle) {
			continue
		}
		total++
		if len(out) >= limit {
			continue
		}
		line := r
		if name != "" {
			line += fmt.Sprintf(" %q", name)
		}
		if val != "" {
			line += fmt.Sprintf(" =%q", val)
		}
		line += axStateSuffix(n)
		if ref := refs.assign(n); ref != "" {
			line += " [ref=" + ref + "]"
		}
		// Name the nearest labelled ancestor so "which question is this
		// option under?" is answerable without a full tree.
		for a := by[n.ParentID]; a != nil; a = by[a.ParentID] {
			if an := axValueString(a.Name); an != "" && !wrapperRoles[axValueString(a.Role)] && axValueString(a.Role) != "RootWebArea" {
				line += fmt.Sprintf(" in %s %q", axValueString(a.Role), truncRunes(an, 60))
				break
			}
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return "(no matches)", nil
	}
	res := strings.Join(out, "\n")
	if total > len(out) {
		res += fmt.Sprintf("\n…%d more matches; narrow with role or text", total-len(out))
	}
	return res, nil
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

const extractJS = `(function(root, max){
	if (!root) return '';
	var out = [];
	var total = 0;
	function push(s){ if (total < max) { out.push(s); total += s.length; } }
	function hidden(el){
		if (el.hidden || el.getAttribute('aria-hidden') === 'true') return true;
		var s = getComputedStyle(el); return s.display === 'none' || s.visibility === 'hidden';
	}
	function inline(el){
		var s = '';
		el.childNodes.forEach(function(n){
			if (n.nodeType === 3) s += n.textContent;
			else if (n.nodeType === 1 && !hidden(n)) {
				var t = n.tagName;
				if (t === 'SCRIPT' || t === 'STYLE' || t === 'NOSCRIPT' || t === 'SVG') return;
				if (t === 'A') { var h = n.href, tx = inline(n).trim(); s += (h && /^https?:/.test(h) && tx) ? '[' + tx + '](' + h + ')' : tx; }
				else if (t === 'BR') s += '\n';
				else if (t === 'IMG') s += n.alt ? '[image: ' + n.alt + ']' : '';
				else if (t === 'STRONG' || t === 'B') { var b = inline(n).trim(); s += b ? '**' + b + '**' : ''; }
				else if (t === 'CODE') s += '` + "`" + `' + n.textContent + '` + "`" + `';
				else if (t === 'INPUT' && (n.type === 'radio' || n.type === 'checkbox')) s += n.checked ? '[x] ' : '[ ] ';
				else s += inline(n);
			}
		});
		return s.replace(/[ \t\r\f]+/g, ' ');
	}
	function table(el){
		var rows = [];
		el.querySelectorAll('tr').forEach(function(tr){
			var cells = [];
			tr.querySelectorAll('th,td').forEach(function(c){ cells.push(inline(c).replace(/\s+/g,' ').replace(/\|/g,'\\|').trim()); });
			if (cells.length) rows.push('| ' + cells.join(' | ') + ' |');
		});
		if (rows.length > 1) rows.splice(1, 0, '|' + rows[0].split('|').slice(1, -1).map(function(){ return ' --- |'; }).join(''));
		return rows.join('\n');
	}
	function walk(el){
		if (total >= max) return;
		if (hidden(el)) return;
		var t = el.tagName;
		if (t === 'SCRIPT' || t === 'STYLE' || t === 'NOSCRIPT' || t === 'SVG' || t === 'NAV' && el !== root) return;
		if (/^H[1-6]$/.test(t)) { push('\n' + '#'.repeat(+t[1]) + ' ' + inline(el).replace(/\s+/g,' ').trim() + '\n'); return; }
		if (t === 'TABLE') { push('\n' + table(el) + '\n'); return; }
		if (t === 'PRE') { push('\n` + "```" + `\n' + el.textContent + '\n` + "```" + `\n'); return; }
		if (t === 'LI') { push('- ' + inline(el).replace(/\s+/g,' ').trim() + '\n'); return; }
		if (t === 'P' || t === 'LABEL' || t === 'BUTTON' || t === 'FIGCAPTION' || t === 'BLOCKQUOTE' || t === 'DT' || t === 'DD') {
			var s = inline(el).trim(); if (s) push(s + '\n\n'); return;
		}
		var hasBlock = false;
		for (var i = 0; i < el.children.length; i++) { if (!/^(A|SPAN|B|STRONG|EM|I|CODE|IMG|BR|SMALL|INPUT)$/.test(el.children[i].tagName)) { hasBlock = true; break; } }
		if (!hasBlock) { var s2 = inline(el).trim(); if (s2) push(s2 + '\n\n'); return; }
		el.childNodes.forEach(function(n){
			if (n.nodeType === 1) walk(n);
			else if (n.nodeType === 3 && n.textContent.trim()) push(n.textContent.trim() + '\n\n');
		});
	}
	walk(root);
	return out.join('').replace(/\n{3,}/g, '\n\n').trim();
})`

// extract returns the readable content of the page (or of one element) as
// markdown: headings, paragraphs, lists, links and tables, without chrome.
func (s *session) extract(ctx context.Context, sel string, maxChars int) (string, error) {
	p := s.activePage()
	s.settle(ctx, p)
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	if maxChars <= 0 || maxChars > 100000 {
		maxChars = 30000
	}
	var res string
	if sel == "" {
		if err := chromedp.Run(c, chromedp.Evaluate(fmt.Sprintf("(%s)(document.querySelector('main,article,[role=main]') || document.body, %d)", extractJS, maxChars), &res)); err != nil {
			return "", classifySelectorErr(fmt.Errorf("browser extract: %w", err))
		}
	} else {
		// "iframe#x >>>" with nothing after it means the frame's whole body.
		if frames, inner := splitFrames(sel); len(frames) > 0 && inner == "" {
			sel += " body"
		}
		inner, opts, err := s.query(c, sel)
		if err != nil {
			return "", err
		}
		// Resolve the element (possibly inside an iframe) and run the converter
		// with it as `this`, so the same code serves the page and any frame.
		err = chromedp.Run(c, chromedp.QueryAfter(inner, func(ctx context.Context, _ runtime.ExecutionContextID, nodes ...*cdp.Node) error {
			if len(nodes) == 0 {
				return fmt.Errorf("selector %q did not return any nodes", sel)
			}
			obj, err := dom.ResolveNode().WithNodeID(nodes[0].NodeID).Do(ctx)
			if err != nil {
				return err
			}
			r, _, err := runtime.CallFunctionOn(fmt.Sprintf("function(max){ return (%s)(this, max); }", extractJS)).
				WithObjectID(obj.ObjectID).
				WithArguments([]*runtime.CallArgument{{Value: []byte(fmt.Sprint(maxChars))}}).
				WithReturnByValue(true).Do(ctx)
			if err != nil {
				return err
			}
			if r != nil {
				_ = json.Unmarshal(r.Value, &res)
			}
			return nil
		}, append(append([]chromedp.QueryOption{}, opts...), chromedp.NodeReady)...))
		if err != nil {
			return "", classifySelectorErr(fmt.Errorf("browser extract: %w", err))
		}
	}
	if strings.TrimSpace(res) == "" {
		return "", fmt.Errorf("%w: no readable content found", ErrSelectorNotFound)
	}
	return res, nil
}

const annotateOverlayID = "yottacode-annotate-overlay"

// screenshotAnnotated captures the viewport with each interactive element
// labelled by its ref, so a vision model can pair what it sees with refs it
// can pass to the other tools. The overlay is removed afterwards.
func (s *session) screenshotAnnotated(ctx context.Context) ([]byte, error) {
	p := s.activePage()
	s.settle(ctx, p)
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	var nodes []*accessibility.Node
	if err := chromedp.Run(c, chromedp.ActionFunc(func(execCtx context.Context) error {
		var err error
		nodes, err = fullAXTree(execCtx)
		return err
	})); err != nil {
		return nil, classifySelectorErr(fmt.Errorf("browser screenshot: %w", err))
	}
	refs := s.nextRefs(false)
	_ = renderAXTreeRefs(nodes, refs) // assigns refs as a side effect

	s.mu.Lock()
	ids := map[string]cdp.BackendNodeID{}
	for r, id := range refs.byRef {
		if len(refs.chainOf[r]) == 0 { // a frame's coordinates are relative to the frame
			ids[r] = id
		}
	}
	s.mu.Unlock()
	names := make([]string, 0, len(ids))
	for r := range ids {
		names = append(names, r)
	}
	sort.Slice(names, func(i, j int) bool {
		return len(names[i]) < len(names[j]) || len(names[i]) == len(names[j]) && names[i] < names[j]
	})
	var labels []map[string]any
	_ = chromedp.Run(c, chromedp.ActionFunc(func(ctx context.Context) error {
		for _, r := range names {
			if len(labels) >= 150 {
				break
			}
			obj, err := dom.ResolveNode().WithBackendNodeID(ids[r]).Do(ctx)
			if err != nil {
				continue
			}
			res, _, err := runtime.CallFunctionOn(`function(){
				var b = this.getBoundingClientRect();
				if (b.width < 2 || b.height < 2 || b.bottom < 0 || b.right < 0 || b.top > innerHeight || b.left > innerWidth) return '';
				var s = getComputedStyle(this); if (s.visibility === 'hidden' || s.display === 'none') return '';
				return JSON.stringify([b.left, b.top, b.width, b.height]);
			}`).WithObjectID(obj.ObjectID).WithReturnByValue(true).Do(ctx)
			if err != nil || res == nil {
				continue
			}
			var str string
			if json.Unmarshal(res.Value, &str) != nil || str == "" {
				continue
			}
			var box []float64
			if json.Unmarshal([]byte(str), &box) != nil || len(box) != 4 {
				continue
			}
			labels = append(labels, map[string]any{"r": r, "x": box[0], "y": box[1], "w": box[2], "h": box[3]})
		}
		return nil
	}))
	data, _ := json.Marshal(labels)
	var ignore any
	_ = chromedp.Run(c, chromedp.Evaluate(fmt.Sprintf(`(function(L){
		var old = document.getElementById(%q); if (old) old.remove();
		var root = document.createElement('div'); root.id = %q;
		root.style.cssText = 'position:fixed;left:0;top:0;width:100vw;height:100vh;pointer-events:none;z-index:2147483647;';
		L.forEach(function(l){
			var box = document.createElement('div');
			box.style.cssText = 'position:absolute;border:1px solid #e11;left:'+l.x+'px;top:'+l.y+'px;width:'+l.w+'px;height:'+l.h+'px;';
			var tag = document.createElement('span'); tag.textContent = l.r;
			tag.style.cssText = 'position:absolute;left:0;top:-14px;background:#e11;color:#fff;font:bold 11px monospace;padding:0 3px;line-height:14px;';
			if (l.y < 14) tag.style.top = '0';
			box.appendChild(tag); root.appendChild(box);
		});
		document.documentElement.appendChild(root); return true;
	})(%s)`, annotateOverlayID, annotateOverlayID, data), &ignore))
	defer func() {
		rc, rcancel := context.WithTimeout(p.ctx, 2*time.Second)
		defer rcancel()
		_ = chromedp.Run(rc, chromedp.Evaluate(fmt.Sprintf(`(function(){var e=document.getElementById(%q); if(e) e.remove();})()`, annotateOverlayID), nil))
	}()
	var b []byte
	if err := chromedp.Run(c, chromedp.CaptureScreenshot(&b)); err != nil {
		return nil, fmt.Errorf("browser screenshot: %w", err)
	}
	return b, nil
}
