package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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

// After settleQuickAfter consecutive settles that spent their whole budget on
// the same page without it ever going quiet (an animation, a poller, a ticking
// clock), later settles on that page use a short budget instead, so such a page
// does not cost seconds on every read. A settle that finishes normally, or a
// navigation to another page, brings the full budget back.
const (
	settleQuickAfter   = 2
	settleQuickNetwork = 500 * time.Millisecond
	settleQuickDOM     = 300 * time.Millisecond
)

// settleKeyOf identifies "the same page" for settle tuning: scheme, host and path.
func settleKeyOf(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

// settleBudget returns the caps to use for the page at url.
func (p *trackedPage) settleBudget(url string) (network, dom time.Duration, quick bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.settleKey == settleKeyOf(url) && p.settleMisses >= settleQuickAfter {
		return settleQuickNetwork, settleQuickDOM, true
	}
	return settleNetworkCap, settleDOMCap, false
}

// noteSettle records whether a settle that used the budget returned by
// settleBudget ran out of it. A quick-mode miss changes nothing: only a clean
// finish lifts quick mode.
func (p *trackedPage) noteSettle(url string, capped, quick bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := settleKeyOf(url)
	if p.settleKey != key {
		p.settleKey, p.settleMisses = key, 0
	}
	switch {
	case !capped:
		p.settleMisses = 0
	case !quick:
		p.settleMisses++
	}
}

// settle waits, bounded, for the page to stop changing: no request in flight
// and no DOM mutation for a short quiet period. It never fails; a page that
// never goes quiet just proceeds after the cap (see the tuning above).
func (s *session) settle(ctx context.Context, p *trackedPage) {
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	url := p.getURL()
	netCap, domCap, quick := p.settleBudget(url)
	nc, ncancel := context.WithTimeout(c, netCap)
	netErr := s.waitNetworkIdle(nc, p, settleNetworkIdle)
	ncancel()
	dc, dcancel := context.WithTimeout(c, domCap)
	defer dcancel()
	var ok bool
	domErr := chromedp.Run(dc, chromedp.Evaluate(fmt.Sprintf(`new Promise(function(resolve){
		var t, mo = new MutationObserver(function(){ clearTimeout(t); t = setTimeout(done, %d); });
		function done(){ mo.disconnect(); resolve(true); }
		mo.observe(document, {subtree:true, childList:true, attributes:true, characterData:true});
		t = setTimeout(done, %d);
	})`, settleDOMQuiet.Milliseconds(), settleDOMQuiet.Milliseconds()), &ok, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}))
	if c.Err() != nil {
		return // the caller gave up; that says nothing about the page
	}
	p.noteSettle(url, netErr != nil || domErr != nil || !ok, quick)
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
	// querySelectorAll that also looks inside open shadow roots.
	function deepAll(root, sel, out){
		out = out || [];
		root.querySelectorAll(sel).forEach(function(e){ out.push(e); });
		root.querySelectorAll('*').forEach(function(e){ if (e.shadowRoot) deepAll(e.shadowRoot, sel, out); });
		return out;
	}
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
	deepAll(document, q).forEach(function(el){
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
	deepAll(document, '[role=alert],[role=status],[aria-live=assertive]').forEach(function(el){
		var t = (el.innerText||'').replace(/\s+/g,' ').trim().slice(0,200); if (t && alerts.length < 5) alerts.push(t);
	});
	var opaque = 0;
	document.querySelectorAll('*').forEach(function(e){ if (e.tagName.indexOf('-') > 0 && !e.shadowRoot) opaque++; });
	return JSON.stringify({url: location.href, title: document.title, els: els, alerts: alerts, opaque: opaque});
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
		Opaque     int
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return PageDigest{}, err
	}
	if v.Opaque > 0 {
		// Custom elements whose shadow root script cannot read (closed roots):
		// the accessibility tree still shows what is inside, so take the
		// element list from it instead.
		if els, ok := s.axDigestElements(c); ok {
			v.Els = els
		}
	}
	d := PageDigest{Valid: true, URL: v.URL, Title: v.Title, Elements: v.Els, Alerts: v.Alerts}
	for _, e := range p.consoleSnapshot(maxBufferedEntries) {
		if e.Level == "dialog" {
			d.Dialogs = append(d.Dialogs, e.Text)
		}
	}
	return d, nil
}

// axDigestElements lists the interactive elements from the accessibility tree
// (page, iframes, and shadow roots including closed ones) in the digest's
// `role "name" ="value" (state)` form.
func (s *session) axDigestElements(c context.Context) ([]string, bool) {
	var forest *axForest
	if err := runChromedp(c, func(ctx context.Context) error {
		var err error
		forest, err = collectAXForest(ctx)
		return err
	}); err != nil {
		return nil, false
	}
	var out []string
	for _, n := range forest.dfs() {
		role := axValueString(n.Role)
		if n.Ignored || !interactiveRoles[role] {
			continue
		}
		line := role
		if name := axValueString(n.Name); name != "" {
			line += fmt.Sprintf(" %q", truncRunes(name, 80))
		}
		if v := axValueString(n.Value); v != "" && role != "radio" && role != "checkbox" && role != "button" {
			line += fmt.Sprintf(" =%q", truncRunes(v, 40))
		}
		out = append(out, line+axStateSuffix(n))
		if len(out) >= 150 {
			break
		}
	}
	return out, true
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
	// An element that only changed state ("radio "B"" -> "radio "B" (checked)")
	// is one change, not a removal plus an addition.
	var changed []string
	added, removed, changed = pairChanges(added, removed)
	lines = append(lines, capLines("changed", changed, 8)...)
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

// elementKey identifies an element across a state change: its role and quoted
// name, i.e. the line up to the closing quote of the name.
func elementKey(line string) string {
	i := strings.IndexByte(line, '"')
	if i < 0 {
		return line
	}
	j := strings.IndexByte(line[i+1:], '"')
	if j < 0 {
		return line
	}
	return line[:i+1+j+1]
}

// pairChanges matches removed and added lines that describe the same element
// (same role and name) one-to-one, in order, and returns them as "old -> new"
// changes; what is left over stays removed or added.
func pairChanges(added, removed []string) (restAdded, restRemoved, changed []string) {
	byKey := map[string][]int{}
	for i, a := range added {
		byKey[elementKey(a)] = append(byKey[elementKey(a)], i)
	}
	used := map[int]bool{}
	for _, r := range removed {
		k := elementKey(r)
		if idx := byKey[k]; len(idx) > 0 {
			changed = append(changed, r+" -> "+added[idx[0]])
			used[idx[0]] = true
			byKey[k] = idx[1:]
			continue
		}
		restRemoved = append(restRemoved, r)
	}
	for i, a := range added {
		if !used[i] {
			restAdded = append(restAdded, a)
		}
	}
	return restAdded, restRemoved, changed
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

// currentDragData returns the data of an intercepted native drag, if any.
func (p *trackedPage) currentDragData() *input.DragData {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dragData
}

func (p *trackedPage) resetDragData() {
	p.mu.Lock()
	p.dragData = nil
	p.mu.Unlock()
}

// drag presses on the centre of from, moves in steps and releases over to.
// It works for both kinds of drag. Sliders, sortable lists and canvas widgets
// listen to mouse events, which the stepped moves drive. A native HTML5
// draggable never sees synthetic mouse moves finish a drag: Chrome starts an
// OS-level drag instead, so interception is switched on, the drag data Chrome
// reports is captured, and dragEnter/dragOver/drop are dispatched at the target
// by hand.
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
	p.resetDragData()
	if err := chromedp.Run(c, input.SetInterceptDrags(true)); err != nil {
		return classifySelectorErr(err)
	}
	defer func() {
		rc, rcancel := context.WithTimeout(p.ctx, 2*time.Second)
		defer rcancel()
		_ = chromedp.Run(rc, input.SetInterceptDrags(false))
	}()

	left := chromedp.ButtonType(input.Left)
	if err := chromedp.Run(c,
		chromedp.MouseEvent(input.MouseMoved, x1, y1),
		chromedp.MouseEvent(input.MousePressed, x1, y1, left, chromedp.ClickCount(1)),
	); err != nil {
		return classifySelectorErr(err)
	}
	const steps = 8
	var data *input.DragData
	for i := 1; i <= steps && data == nil; i++ {
		f := float64(i) / steps
		if err := chromedp.Run(c, chromedp.MouseEvent(input.MouseMoved, x1+(x2-x1)*f, y1+(y2-y1)*f, left)); err != nil {
			return classifySelectorErr(err)
		}
		// The interception event arrives asynchronously after the move that
		// crosses the browser's drag threshold.
		select {
		case <-c.Done():
			return ctxFailure(c, "drag", c.Err())
		case <-time.After(20 * time.Millisecond):
		}
		data = p.currentDragData()
	}
	if data == nil {
		// Not a native drag: the moves already reached the target; release there.
		return classifySelectorErr(chromedp.Run(c, chromedp.MouseEvent(input.MouseReleased, x2, y2, left, chromedp.ClickCount(1))))
	}
	return classifySelectorErr(chromedp.Run(c,
		input.DispatchDragEvent(input.DragEnter, x2, y2, data),
		input.DispatchDragEvent(input.DragOver, x2, y2, data),
		input.DispatchDragEvent(input.DragOver, x2+1, y2+1, data),
		input.DispatchDragEvent(input.Drop, x2, y2, data),
		chromedp.MouseEvent(input.MouseReleased, x2, y2, left, chromedp.ClickCount(1)),
	))
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
	// Children as rendered: an open shadow root replaces the host's own
	// children, and a <slot> shows the nodes assigned to it.
	function kids(el){
		if (el.shadowRoot) return Array.from(el.shadowRoot.childNodes);
		if (el.tagName === 'SLOT') {
			var a = el.assignedNodes({flatten:true});
			if (a.length) return a;
		}
		return Array.from(el.childNodes);
	}
	function inline(el){
		var s = '';
		kids(el).forEach(function(n){
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
				else if (/^(DIV|P|UL|OL|LI|TR|SECTION|ARTICLE|HEADER|FOOTER|ASIDE|FORM|FIELDSET|DL|DT|DD|TABLE|H[1-6])$/.test(t)) s += ' ' + inline(n) + ' ';
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
		var ks = kids(el).filter(function(k){ return k.nodeType === 1; });
		for (var i = 0; i < ks.length; i++) { if (!/^(A|SPAN|B|STRONG|EM|I|CODE|IMG|BR|SMALL|INPUT)$/.test(ks[i].tagName)) { hasBlock = true; break; } }
		if (!hasBlock) { var s2 = inline(el).trim(); if (s2) push(s2 + '\n\n'); return; }
		kids(el).forEach(function(n){
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
		// Script cannot read closed shadow roots, but the accessibility tree
		// can: add any text it has that the DOM walk did not find.
		if extra := s.closedShadowText(c, res); extra != "" {
			res = strings.TrimSpace(res + "\n\n" + extra)
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
		lc, lcancel := context.WithTimeout(c, selectorLookupTimeout)
		defer lcancel()
		err = chromedp.Run(lc, chromedp.QueryAfter(inner, func(ctx context.Context, _ runtime.ExecutionContextID, nodes ...*cdp.Node) error {
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

// closedShadowText returns, under a heading, the accessible text of the page
// that is not already in have. It does nothing unless the page has custom
// elements whose shadow root script cannot read (closed roots), so ordinary
// pages pay nothing. The text comes from the accessibility tree, which sees
// into closed shadow roots; text already extracted is skipped.
func (s *session) closedShadowText(c context.Context, have string) string {
	var opaque int
	if err := chromedp.Run(c, chromedp.Evaluate(`(function(){var n=0;document.querySelectorAll('*').forEach(function(e){if(e.tagName.indexOf('-')>0&&!e.shadowRoot)n++});return n})()`, &opaque)); err != nil || opaque == 0 {
		return ""
	}
	var forest *axForest
	if err := runChromedp(c, func(ctx context.Context) error {
		var err error
		forest, err = collectAXForest(ctx)
		return err
	}); err != nil {
		return ""
	}
	var lines []string
	seen := map[string]bool{}
	for _, n := range forest.dfs() {
		if n.Ignored || axValueString(n.Role) != "StaticText" {
			continue
		}
		t := strings.TrimSpace(axValueString(n.Name))
		if t == "" || seen[t] || strings.Contains(have, t) {
			continue
		}
		seen[t] = true
		lines = append(lines, t)
		if len(lines) >= 200 {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Text from custom elements the page script cannot read\n\n" + strings.Join(lines, "\n\n")
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

// ScrollResult describes a scroll-until-the-end run.
type ScrollResult struct {
	Steps       int  // load-and-scroll cycles performed
	StartHeight int  // scrollable height before, in px
	EndHeight   int  // scrollable height after, in px
	ReachedEnd  bool // the height stopped growing (the list is exhausted)
	TimedOut    bool // the action deadline hit first
	// LoadMore describes a visible "Load more"-style control found afterwards,
	// as `button "Load more" [ref=e12]`, or "" if there is none. Lists that grow
	// on a click, not on reaching the bottom, stop growing here.
	LoadMore string
}

var loadMoreRE = regexp.MustCompile(`(?i)^\s*(load|show|view|see|get|fetch)\s+(more|older|next|additional)\b|\b(more|older)\s+(results|posts|comments|items|stories|replies|articles|products|reviews)\b|^\s*(more|next page)\s*[»›>…]*\s*$`)

// findLoadMore looks for a "Load more"-style button or link and returns its
// description with a usable ref, or "".
func (s *session) findLoadMore(c context.Context) string {
	var forest *axForest
	if err := runChromedp(c, func(ctx context.Context) error {
		var err error
		forest, err = collectAXForest(ctx)
		return err
	}); err != nil {
		return ""
	}
	refs := s.nextRefs(true)
	refs.chains = forest.chains
	for _, n := range forest.dfs() {
		role := axValueString(n.Role)
		if n.Ignored || (role != "button" && role != "link") {
			continue
		}
		name := axValueString(n.Name)
		if name == "" || !loadMoreRE.MatchString(name) {
			continue
		}
		if ref := refs.assign(n); ref != "" {
			return fmt.Sprintf("%s %q%s [ref=%s]", role, truncRunes(name, 60), axStateSuffix(n), ref)
		}
	}
	return ""
}

const (
	scrollEndStalls    = 2 // consecutive cycles without growth that mean "the end"
	scrollStepSettle   = 250 * time.Millisecond
	scrollStepNetwork  = 2 * time.Second
	DefaultScrollSteps = 10
	MaxScrollSteps     = 30
)

// scrollUntilEnd scrolls the page (or the scrollable element sel) to its bottom
// repeatedly, waiting for lazy-loaded content after each jump, until the
// scrollable height stops growing for two cycles, maxSteps cycles have run, or
// the action deadline hits. Infinite feeds load more as the bottom is reached.
func (s *session) scrollUntilEnd(ctx context.Context, sel string, maxSteps int) (ScrollResult, error) {
	p := s.activePage()
	c, cancel := actionContext(p.ctx, ctx)
	defer cancel()
	if maxSteps <= 0 {
		maxSteps = DefaultScrollSteps
	}
	if maxSteps > MaxScrollSteps {
		maxSteps = MaxScrollSteps
	}

	// step jumps to the bottom and returns the scrollable height.
	var step func() (int, error)
	if sel == "" {
		step = func() (int, error) {
			var h float64
			err := chromedp.Run(c, chromedp.Evaluate(`(function(){var d=document.scrollingElement||document.documentElement;d.scrollTo(0,d.scrollHeight);return d.scrollHeight})()`, &h))
			return int(h), err
		}
	} else {
		inner, opts, err := s.query(c, sel)
		if err != nil {
			return ScrollResult{}, err
		}
		var objID runtime.RemoteObjectID
		lc, lcancel := context.WithTimeout(c, selectorLookupTimeout)
		defer lcancel()
		err = chromedp.Run(lc, chromedp.QueryAfter(inner, func(ctx context.Context, _ runtime.ExecutionContextID, nodes ...*cdp.Node) error {
			if len(nodes) == 0 {
				return fmt.Errorf("selector %q did not return any nodes", sel)
			}
			obj, err := dom.ResolveNode().WithNodeID(nodes[0].NodeID).Do(ctx)
			if err != nil {
				return err
			}
			objID = obj.ObjectID
			return nil
		}, append(append([]chromedp.QueryOption{}, opts...), chromedp.NodeReady)...))
		if err != nil {
			return ScrollResult{}, classifySelectorErr(err)
		}
		step = func() (int, error) {
			var h int
			err := chromedp.Run(c, chromedp.ActionFunc(func(ctx context.Context) error {
				r, _, err := runtime.CallFunctionOn(`function(){this.scrollTo(0,this.scrollHeight);return this.scrollHeight}`).
					WithObjectID(objID).WithReturnByValue(true).Do(ctx)
				if err != nil {
					return err
				}
				if r != nil {
					var f float64
					if json.Unmarshal(r.Value, &f) == nil {
						h = int(f)
					}
				}
				return nil
			}))
			return h, err
		}
	}

	var res ScrollResult
	h, err := step()
	if err != nil {
		return res, classifySelectorErr(err)
	}
	res.StartHeight, res.EndHeight = h, h
	stalls := 0
	for res.Steps < maxSteps {
		res.Steps++
		select {
		case <-c.Done():
			res.TimedOut = true
			return res, nil
		case <-time.After(scrollStepSettle):
		}
		nc, ncancel := context.WithTimeout(c, scrollStepNetwork)
		_ = s.waitNetworkIdle(nc, p, settleNetworkIdle)
		ncancel()
		next, err := step()
		if err != nil {
			if c.Err() != nil {
				res.TimedOut = true
				return res, nil
			}
			return res, classifySelectorErr(err)
		}
		if next > res.EndHeight {
			res.EndHeight = next
			stalls = 0
			continue
		}
		if stalls++; stalls >= scrollEndStalls {
			res.ReachedEnd = true
			break
		}
	}
	res.LoadMore = s.findLoadMore(c)
	return res, nil
}
