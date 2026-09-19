package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

// maxAnnotations bounds how many controls one annotated screenshot labels —
// past a few dozen the labels cover the page they are meant to explain.
const maxAnnotations = 80

// annotationOverlayID is the DOM id of the temporary overlay container, so
// it can be removed again — and so the page under test can be told apart
// from it if a script trips over it.
const annotationOverlayID = "__yottacode_annotations"

// AnnotatedShot is the result of an annotated screenshot.
type AnnotatedShot struct {
	PNG []byte
	// Labeled is how many controls got a numbered box; Skipped how many
	// ref'd controls did not (no on-screen box, off the visible viewport, or
	// past maxAnnotations).
	Labeled, Skipped int
}

// annotation is one labeled box in page (document) coordinates.
type annotation struct {
	N int     `json:"n"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// screenshotAnnotated captures the page with a numbered box drawn over every
// interactive control that has an @eN ref, where box N is ref @eN — set-of-
// marks prompting: it lets a vision model say "click 7" without guessing
// pixel positions or CSS selectors. It refreshes the page's refs first (a
// whole-page snapshot, iframes included), so the numbers on the image are
// exactly the refs the next action tool call can use. The overlay is
// injected for the capture and removed again, even if the capture fails.
func (s *session) screenshotAnnotated(ctx context.Context, fullPage bool) (AnnotatedShot, error) {
	tp := s.activeTrackedPage()
	pg := tp.page.Context(ctx)
	if _, err := s.takeSnapshot(ctx, tp, "", InspectOptions{InteractiveOnly: true}); err != nil {
		return AnnotatedShot{}, err
	}

	metrics, err := proto.PageGetLayoutMetrics{}.Call(pg)
	if err != nil {
		return AnnotatedShot{}, fmt.Errorf("browser screenshot: %w", err)
	}
	var pageX, pageY, viewW, viewH float64
	if v := metrics.CSSVisualViewport; v != nil {
		pageX, pageY, viewW, viewH = v.PageX, v.PageY, v.ClientWidth, v.ClientHeight
	}

	nums := tp.refNumbers()
	var boxes []annotation
	skipped := 0
	for _, n := range nums {
		if len(boxes) >= maxAnnotations {
			skipped++
			continue
		}
		rt, _ := tp.lookupRef("e" + strconv.Itoa(n))
		if rt.session != "" {
			// A control in a cross-process frame reports its box in that
			// frame's own coordinates, not the page's, so a box drawn from it
			// would land in the wrong place. Not drawing beats misleading.
			skipped++
			continue
		}
		q, err := proto.DOMGetContentQuads{BackendNodeID: rt.node}.Call(pg)
		if err != nil || len(q.Quads) == 0 || len(q.Quads[0]) < 8 {
			skipped++
			continue
		}
		x0, y0, x1, y1 := quadBounds(q.Quads[0])
		w, h := x1-x0, y1-y0
		// Quads are viewport-relative. Off the visible viewport there is
		// nothing to box — unless the whole page is being captured.
		if w <= 0 || h <= 0 || (!fullPage && (x1 < 0 || y1 < 0 || x0 > viewW || y0 > viewH)) {
			skipped++
			continue
		}
		boxes = append(boxes, annotation{N: n, X: x0 + pageX, Y: y0 + pageY, W: w, H: h})
	}

	payload, err := json.Marshal(boxes)
	if err != nil {
		return AnnotatedShot{}, fmt.Errorf("browser screenshot: %w", err)
	}
	if _, err := (proto.RuntimeEvaluate{Expression: overlayJS(string(payload)), ReturnByValue: true}).Call(pg); err != nil {
		return AnnotatedShot{}, fmt.Errorf("browser screenshot: draw annotations: %w", err)
	}
	// Remove the overlay even when ctx has expired mid-capture.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = proto.RuntimeEvaluate{Expression: removeOverlayJS()}.Call(tp.page.Context(cleanup))
	}()

	png, err := pg.Screenshot(fullPage, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return AnnotatedShot{}, fmt.Errorf("browser screenshot: %w", err)
	}
	return AnnotatedShot{PNG: png, Labeled: len(boxes), Skipped: skipped}, nil
}

// quadBounds returns the bounding box (x0,y0,x1,y1) of a CDP quad: eight
// numbers, four x/y corner pairs.
func quadBounds(q []float64) (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = q[0], q[1], q[0], q[1]
	for i := 2; i+1 < len(q); i += 2 {
		x, y := q[i], q[i+1]
		x0, x1 = min(x0, x), max(x1, x)
		y0, y1 = min(y0, y), max(y1, y)
	}
	return x0, y0, x1, y1
}

// refNumbers lists the ref numbers currently registered on tp, ascending —
// the order they appear in the snapshot.
func (tp *trackedPage) refNumbers() []int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	nums := make([]int, 0, len(tp.refs.nodes))
	for ref := range tp.refs.nodes {
		if n, err := strconv.Atoi(ref[1:]); err == nil {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	return nums
}

// overlayJS builds the expression that draws the labeled boxes. Absolutely
// positioned at document coordinates so it lines up for a viewport capture
// and a full-page one alike, above everything, and inert to the mouse.
func overlayJS(boxesJSON string) string {
	return `((boxes) => {
  const root = document.createElement('div');
  root.id = '` + annotationOverlayID + `';
  root.style.cssText = 'position:absolute;left:0;top:0;width:0;height:0;z-index:2147483647;pointer-events:none';
  for (const b of boxes) {
    const box = document.createElement('div');
    box.style.cssText = 'position:absolute;box-sizing:border-box;border:2px solid #e11d48;left:' + b.x + 'px;top:' + b.y + 'px;width:' + b.w + 'px;height:' + b.h + 'px';
    const tag = document.createElement('span');
    tag.textContent = String(b.n);
    tag.style.cssText = 'position:absolute;left:-2px;top:-15px;background:#e11d48;color:#fff;font:bold 11px/15px monospace;padding:0 4px;border-radius:2px';
    box.appendChild(tag);
    root.appendChild(box);
  }
  document.documentElement.appendChild(root);
})(` + boxesJSON + `)`
}

func removeOverlayJS() string {
	return `(() => { const e = document.getElementById('` + annotationOverlayID + `'); if (e) e.remove(); })()`
}
