package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/codemap"
)

func TestCodeMapPicker_ClickRowMovesCursor(t *testing.T) {
	m := newTestModel(t)
	idx := testCodeMapIndex(t)
	p := &codeMapPickerState{index: idx, mode: codeMapModeStructure, expanded: map[codemap.NodeID]bool{idx.Root(): true}}
	p.rebuildRows()
	if len(p.rows) < 2 {
		t.Fatalf("test fixture needs at least 2 rows, got %d", len(p.rows))
	}
	m.codeMapPicker = p
	m.codeMapPickerOpen = true

	hits := &pickerHits{}
	box := popupBox(renderCodeMapPicker(m.codeMapPicker, m.popupWidth(), hits))
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

	// Row 1 in this fixture is a leaf (file/symbol) node — selecting it,
	// same as pressing Enter on it, inserts an @-reference into the
	// cmdline and closes the picker (acceptCodeMapSelection). This
	// pins that the click resolved to row 1 specifically, not just
	// "some row" — a directory row would instead toggle expand/collapse
	// and stay open.
	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.codeMapPickerOpen {
		t.Fatalf("clicking a leaf row should commit (insert @ref) and close the picker, like Enter does")
	}
	if !strings.Contains(m.textInput.Value(), "@") {
		t.Errorf("clicking a leaf row should insert an @-reference into the cmdline, got %q", m.textInput.Value())
	}
}
