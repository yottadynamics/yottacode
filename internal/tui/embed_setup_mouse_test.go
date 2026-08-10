package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestEmbedSetup_ClickRowStartsPull(t *testing.T) {
	m := newTestModel(t)
	m.embedSetupOpen = true

	hits := &pickerHits{}
	box := popupBox(m.renderEmbedSetup(hits))
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

	m, cmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.embedSetupCursor != 1 {
		t.Errorf("clicking row 1 should move the cursor there; got %d", m.embedSetupCursor)
	}
	if !m.embedSetupPulling {
		t.Errorf("clicking a row should start the pull, like Enter does")
	}
	if cmd == nil {
		t.Errorf("starting the pull should return a cmd")
	}
}
