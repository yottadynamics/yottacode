package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chromedp/chromedp"
)

// sparseTree reports whether a rendered full-page tree gives the agent nothing
// to act on: no element carries a ref and there is next to no text.
func sparseTree(rendered string) bool {
	if strings.Contains(rendered, "[ref=") {
		return false
	}
	return len(rendered) < 600
}

const pageFactsJS = `(function(){
	var f = {ready: document.readyState, iframes: 0, crossOrigin: 0, canvases: document.querySelectorAll('canvas').length,
		videos: document.querySelectorAll('video').length, closedShadow: 0, bodyText: 0};
	document.querySelectorAll('iframe,frame').forEach(function(fr){
		f.iframes++;
		try { if (!fr.contentDocument) f.crossOrigin++; } catch (e) { f.crossOrigin++; }
	});
	document.querySelectorAll('*').forEach(function(e){
		if (e.tagName.indexOf('-') > 0 && !e.shadowRoot) f.closedShadow++;
	});
	f.bodyText = document.body ? (document.body.innerText || '').trim().length : 0;
	return JSON.stringify(f);
})()`

// emptyTreeNote explains, from cheap page facts, why the accessibility tree
// came back (nearly) empty and what to try next. It contains only counts — no
// page text — so it is safe to show next to untrusted content.
func (s *session) emptyTreeNote(c context.Context) string {
	var raw string
	if err := chromedp.Run(c, chromedp.Evaluate(pageFactsJS, &raw)); err != nil {
		return ""
	}
	var f struct {
		Ready                                                          string
		Iframes, CrossOrigin, Canvases, Videos, ClosedShadow, BodyText int
	}
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return ""
	}
	var why []string
	if f.Ready != "complete" {
		why = append(why, fmt.Sprintf("the page is still loading (readyState=%s): wait for it with browser_wait, then inspect again", f.Ready))
	}
	if f.CrossOrigin > 0 {
		why = append(why, fmt.Sprintf("%d of %d iframe(s) are cross-origin, so their content cannot be read here", f.CrossOrigin, f.Iframes))
	} else if f.Iframes > 0 {
		why = append(why, fmt.Sprintf("%d iframe(s) present but none produced readable content", f.Iframes))
	}
	if f.Canvases > 0 {
		why = append(why, fmt.Sprintf("%d canvas element(s): drawn content has no accessibility tree", f.Canvases))
	}
	if f.Videos > 0 {
		why = append(why, fmt.Sprintf("%d video element(s)", f.Videos))
	}
	if f.ClosedShadow > 0 {
		why = append(why, fmt.Sprintf("%d custom element(s) without a readable shadow root (possibly closed)", f.ClosedShadow))
	}
	if f.BodyText > 0 {
		why = append(why, fmt.Sprintf("the page has %d characters of visible text that are not in the tree: browser_extract reads them", f.BodyText))
	}
	if len(why) == 0 {
		why = append(why, "the page has no visible text or controls yet; content may be added by a timer or script: browser_wait for it")
	}
	return "\nnote: this tree is nearly empty. " + strings.Join(why, "; ") +
		". If a screenshot shows content that is missing here, try browser_screenshot with annotate, browser_extract, or ask the user to act on it via browser_handoff."
}
