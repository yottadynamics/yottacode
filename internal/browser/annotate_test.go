package browser

import (
	"context"
	"strings"
	"testing"
)

func TestQuadBounds(t *testing.T) {
	// A rotated-ish quad: the bounding box spans the extremes of all corners.
	x0, y0, x1, y1 := quadBounds([]float64{10, 20, 50, 15, 55, 60, 12, 65})
	if x0 != 10 || y0 != 15 || x1 != 55 || y1 != 65 {
		t.Errorf("quadBounds = %v %v %v %v, want 10 15 55 65", x0, y0, x1, y1)
	}
}

func TestOverlayJS_EmbedsBoxesAndUsesTheKnownID(t *testing.T) {
	js := overlayJS(`[{"n":3,"x":1,"y":2,"w":30,"h":10}]`)
	for _, want := range []string{annotationOverlayID, `[{"n":3,"x":1,"y":2,"w":30,"h":10}]`, "pointer-events:none", "z-index:2147483647"} {
		if !strings.Contains(js, want) {
			t.Errorf("overlay script missing %q", want)
		}
	}
	if rm := removeOverlayJS(); !strings.Contains(rm, annotationOverlayID) || !strings.Contains(rm, ".remove()") {
		t.Errorf("remove script does not target the overlay: %s", rm)
	}
}

func TestTrackedPage_RefNumbersAreSortedNumerically(t *testing.T) {
	tp := &trackedPage{}
	tp.registerRefs(axRender{Refs: map[string]refTarget{"e10": {node: 1}, "e2": {node: 2}, "e1": {node: 3}, "e33": {node: 4}}, RefCount: 33}, true)
	got := tp.refNumbers()
	want := []int{1, 2, 10, 33}
	if len(got) != len(want) {
		t.Fatalf("refNumbers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("refNumbers = %v, want %v (numeric, not lexical, order)", got, want)
		}
	}
}

func TestManager_ScreenshotAnnotatedDelegates(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	shot, err := m.ScreenshotAnnotated(context.Background(), true)
	if err != nil {
		t.Fatalf("ScreenshotAnnotated: %v", err)
	}
	if string(shot.PNG) != "png" || shot.Labeled != 2 || shot.Skipped != 1 {
		t.Errorf("result = %+v", shot)
	}
	if got := fake.calls[len(fake.calls)-1]; got != "screenshotAnnotated:full=true" {
		t.Errorf("last call = %q", got)
	}
	_ = m.Close(context.Background())
	if _, err := m.ScreenshotAnnotated(context.Background(), false); err == nil {
		t.Error("ScreenshotAnnotated after Close must be denied")
	}
}
