package browser

import (
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Bounds on how much of a page's frame tree browser_inspect walks, so an
// ad-heavy page can't turn one snapshot into dozens of CDP round-trips.
const (
	maxInspectFrames = 20
	maxFrameDepth    = 3
	maxFrameURLChars = 120
)

// frameRef names one child frame of the active page.
type frameRef struct {
	ID    proto.PageFrameID
	Label string // "frame \"name\" https://…", printed as the section header
}

// childFrames lists the active page's descendant frames in document order,
// bounded by maxInspectFrames/maxFrameDepth. It works for cross-origin
// frames as well: rod's launcher disables site isolation, so every frame of
// a page lives in the page's own process and is reachable over the page's
// own CDP session (Accessibility.getFullAXTree takes a frameId; the
// backend node ids it returns resolve on that same session, and rod's
// ElementFromObject attaches an element to the right frame's JS context).
func childFrames(pg *rod.Page) []frameRef {
	ft, err := proto.PageGetFrameTree{}.Call(pg)
	if err != nil || ft.FrameTree == nil {
		return nil
	}
	var out []frameRef
	var walk func(n *proto.PageFrameTree, depth int)
	walk = func(n *proto.PageFrameTree, depth int) {
		for _, c := range n.ChildFrames {
			if len(out) >= maxInspectFrames || depth+1 > maxFrameDepth {
				return
			}
			out = append(out, frameRef{ID: c.Frame.ID, Label: frameLabel(c.Frame)})
			walk(c, depth+1)
		}
	}
	walk(ft.FrameTree, 0)
	return out
}

// frameLabel renders a frame as `frame "name" url`, dropping the name when
// the frame has none and bounding a long URL.
func frameLabel(f *proto.PageFrame) string {
	url := f.URL
	if len(url) > maxFrameURLChars {
		url = url[:maxFrameURLChars] + "…"
	}
	if f.Name != "" {
		return fmt.Sprintf("frame %q %s", f.Name, url)
	}
	return fmt.Sprintf("frame %s", url)
}
