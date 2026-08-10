package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestMemoryPicker_ClickRowOpensReindex(t *testing.T) {
	m := newTestModel(t)
	m.openMemoryPicker()
	// Row 4 ("Reindex embeddings") doesn't need a real file on disk —
	// runMemoryReindex always returns a cmd, unlike the vim-launching
	// rows which need a writable path.
	hits := &pickerHits{}
	box := popupBox(renderMemoryPicker(m.memoryPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 4 {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for row 4")
	}

	m, cmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.memoryPickerOpen {
		t.Errorf("clicking a row should commit and close the picker, like Enter does")
	}
	if cmd == nil {
		t.Errorf("committing the reindex row should return a cmd")
	}
}
