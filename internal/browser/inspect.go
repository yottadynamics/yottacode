package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-rod/rod/lib/proto"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// InspectOptions tunes browser_inspect.
type InspectOptions struct {
	// InteractiveOnly lists just the interactive elements as a flat,
	// ref-annotated list — the compact "what can I click or fill?" view —
	// instead of the full outline that also carries headings and text.
	InteractiveOnly bool
}

// takeSnapshot renders the accessibility tree of tp's page (or of one element's
// neighborhood when selector is set) and registers the @eN refs it prints
// on tp, so the action tools can target those elements directly. A
// whole-page snapshot also walks the page's iframes (see childFrames) and
// replaces tp's refs; a scoped one adds to them.
func (s *session) takeSnapshot(ctx context.Context, tp *trackedPage, selector string, opts InspectOptions) (axRender, error) {
	activePg := tp.page
	pg := activePg.Context(ctx)
	_ = proto.AccessibilityEnable{}.Call(pg)

	scoped := strings.TrimSpace(selector) != ""
	var nodes []*proto.AccessibilityAXNode
	var frames []axSection
	if !scoped {
		res, err := proto.AccessibilityGetFullAXTree{}.Call(pg)
		if err != nil {
			return axRender{}, fmt.Errorf("browser inspect: %w", err)
		}
		nodes = res.Nodes
		// The page's own tree stops at each <iframe>; walk the frames so
		// controls inside them (payment forms, embedded widgets) get refs too.
		// A frame that can't be read is skipped rather than failing the whole
		// snapshot.
		for _, f := range childFrames(activePg) {
			fres, err := proto.AccessibilityGetFullAXTree{FrameID: f.ID}.Call(pg)
			if err != nil || len(fres.Nodes) == 0 {
				continue
			}
			frames = append(frames, axSection{Label: f.Label, Nodes: fres.Nodes})
		}
		// Frames in other processes (site isolation) are separate targets that
		// the page's own frame tree never lists.
		frames = append(frames, s.oopifSections(ctx, tp, maxInspectFrames-len(frames))...)
	} else {
		el, err := s.element(ctx, activePg, selector)
		if err != nil {
			return axRender{}, err
		}
		res, err := proto.AccessibilityGetPartialAXTree{ObjectID: el.Object.ObjectID, FetchRelatives: true}.Call(pg)
		if err != nil {
			return axRender{}, fmt.Errorf("browser inspect: %w", err)
		}
		nodes = res.Nodes
	}

	ao := axOptions{interactiveOnly: opts.InteractiveOnly, withRefs: true}
	if scoped {
		ao.refStart = tp.refCount()
	}
	r := renderAXSections(append([]axSection{{Nodes: nodes}}, frames...), ao)
	tp.registerRefs(r, !scoped)
	return r, nil
}

// inspect is browser_inspect: takeSnapshot, then — when the outline exceeds the
// display budget — spill the complete version to a file (see spill) and say
// where, rather than silently losing the elements, and refs, past the cut.
func (s *session) inspect(ctx context.Context, selector string, opts InspectOptions) (string, error) {
	r, err := s.takeSnapshot(ctx, s.activeTrackedPage(), selector, opts)
	if err != nil {
		return "", err
	}
	out := r.Text
	if r.Truncated {
		if path, err := s.spills.spill(r.Full); err == nil {
			out += fmt.Sprintf("\n[full snapshot saved to %s — read_file it for the elements past the cut; its @eN refs are valid]", path)
		}
	}
	return out, nil
}

// spillStore writes oversized snapshots to numbered files in a private
// scratch directory (created lazily, removed when the session closes). The
// directory lives outside the workspace on purpose: it is scratch output,
// not a project file, and read_file can read it while write_file's boundary
// still keeps agents out of it. The zero value is ready to use.
type spillStore struct {
	mu  syncutil.Mutex
	dir string
	n   int
}

// spill writes content to the next numbered file and returns its path.
func (sp *spillStore) spill(content string) (string, error) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.dir == "" {
		dir, err := os.MkdirTemp("", "yottacode-browser-snapshots-*")
		if err != nil {
			return "", err
		}
		sp.dir = dir
	}
	sp.n++
	path := filepath.Join(sp.dir, fmt.Sprintf("snapshot-%d.txt", sp.n))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// remove deletes the scratch directory, if one was made.
func (sp *spillStore) remove() {
	sp.mu.Lock()
	dir := sp.dir
	sp.dir = ""
	sp.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}
