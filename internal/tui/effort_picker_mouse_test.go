package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestEffortPicker_ClickRowSelectsAndCommits(t *testing.T) {
	m := newTestModel(t)
	m.openEffortPicker()

	hits := &pickerHits{}
	box := popupBox(renderEffortPicker(m.effortPicker, hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 2 {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for row 2")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.effortPickerOpen {
		t.Errorf("clicking a row should commit and close the picker, like Enter does")
	}
	if m.reasoningEffort != "medium" {
		t.Errorf("expected reasoningEffort=%q, got %q", "medium", m.reasoningEffort)
	}
}

func TestEffortPicker_ClickOutsideBoxNoOp(t *testing.T) {
	m := newTestModel(t)
	m.openEffortPicker()

	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if !m.effortPickerOpen {
		t.Errorf("clicking outside the popup box should not close the picker")
	}
}
