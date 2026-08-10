package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/recall"
)

func TestRecallPicker_ClickRowPreviews(t *testing.T) {
	m := newTestModel(t)
	m.openRecallPicker("auth", []recall.Hit{
		{SessionID: "sess-a", SessionName: "first", Snippet: "about auth"},
		{SessionID: "sess-b", SessionName: "second", Snippet: "more auth"},
	})

	hits := &pickerHits{}
	box := popupBox(renderRecallPicker(m.recallPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 1 {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for row 1")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if !m.recallPickerOpen {
		t.Fatalf("clicking a row should preview, not close, the picker")
	}
	if !m.recallPicker.preview {
		t.Errorf("expected preview=true after clicking row 1")
	}
	if m.recallPicker.cursor != 1 {
		t.Errorf("cursor = %d, want 1", m.recallPicker.cursor)
	}
}
