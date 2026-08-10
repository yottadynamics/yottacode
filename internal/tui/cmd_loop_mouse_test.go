package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestLoopExitConfirm_ClickExitAnywayStopsLoops(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m, _ = cmdLoop(m, []string{"30s", "/help"})
	m.turnActive = false
	out, cmd := requestGracefulExit(m)
	m = out.(Model)
	if cmd != nil || !m.loopExitConfirmOpen {
		t.Fatal("active loops should open exit confirmation")
	}

	hits := &pickerHits{}
	box := popupBox(renderLoopExitConfirm(m, hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 0 { // "Exit anyway"
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the Exit anyway row")
	}

	m, cmd = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.activeLoopCount() != 0 || cmd == nil {
		t.Fatal("clicking Exit anyway should stop loops and continue graceful exit, like Enter does")
	}
	if m.loopExitConfirmOpen {
		t.Error("clicking Exit anyway should close the confirm dialog")
	}
}
