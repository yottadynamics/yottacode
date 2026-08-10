package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPermissionsOverlay_ClickLocalRowMovesCursorAndOpensVim(t *testing.T) {
	m := newTestModel(t)
	p, err := permissionsLoadHelper(t, m.cwd, nil, nil, nil)
	if err != nil {
		t.Fatalf("permissionsLoadHelper: %v", err)
	}
	m.perms = p
	m, _ = typeAndEnter(t, m, "/permissions")
	if !m.permissionsOpen {
		t.Fatalf("/permissions should open the inline picker")
	}

	hits := &pickerHits{}
	box := popupBox(renderPermissionsOverlay(m, hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 1 {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for row 1 (local)")
	}

	m, cmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.permissionsOpen {
		t.Errorf("clicking a row should commit and close the picker, like Enter does")
	}
	if cmd == nil {
		t.Errorf("committing should return the openInVim exec cmd")
	}
}
