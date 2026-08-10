package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSandboxPicker_ClickRowPreviewsThenConfirms(t *testing.T) {
	m := newTestModel(t)
	m.sandboxPicker = &sandboxPickerState{cursor: sandboxModeOff, current: sandboxModeOff, detected: true}
	m.sandboxPickerOpen = true

	hits := &pickerHits{}
	box := popupBox(renderSandboxPicker(m.sandboxPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == int(sandboxModeRegular) {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the regular-permissions row")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.sandboxPicker.cursor != sandboxModeRegular {
		t.Fatalf("first click should move cursor to sandboxModeRegular; got %v", m.sandboxPicker.cursor)
	}
	if !m.sandboxPicker.confirming {
		t.Fatalf("first click on a non-current row should preview (confirming=true), not commit")
	}
	if !m.sandboxPickerOpen {
		t.Fatalf("picker should stay open during preview")
	}

	// The box grows once confirming is true (adds a "Press Enter again…"
	// line), which shifts the vertically-centered popup origin — so the
	// second click's screen point must be recomputed fresh, same as a
	// real user clicking where the row now visually sits, rather than
	// reusing the first click's now-stale coordinates.
	hits = &pickerHits{}
	box = popupBox(renderSandboxPicker(m.sandboxPicker, m.popupWidth(), hits))
	ox, oy = m.popupOrigin(box)
	found = false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == int(sandboxModeRegular) {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the regular-permissions row after preview")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.sandboxPickerOpen {
		t.Errorf("second click on the same row should confirm and close the picker")
	}
}
