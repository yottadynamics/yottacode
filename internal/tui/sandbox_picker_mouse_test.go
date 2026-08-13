package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSandboxPicker_ClickRowAppliesSelection(t *testing.T) {
	m := newTestModel(t)
	m.sandboxPicker = &sandboxPickerState{cursor: sandboxModeOff, current: sandboxModeOff, configured: sandboxModeOff, experimentalOn: true, detected: true}
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
	if m.sandboxPickerOpen {
		t.Fatal("clicking a row should apply the selected sandbox mode and close the picker")
	}
}
