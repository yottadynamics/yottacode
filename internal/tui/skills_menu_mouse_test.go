package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSkillsMenu_ClickRowRunsAction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	m := newTestModel(t)
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", home)
	m, _ = m.runSlash("/skills")
	if !m.skillsMenuOpen {
		t.Fatal("expected skillsMenuOpen=true")
	}

	var checkIdx int
	for i, it := range m.skillsMenu.items {
		if it.label == "Check" {
			checkIdx = i
		}
	}

	hits := &pickerHits{}
	box := popupBox(renderSkillsMenu(m.skillsMenu, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == checkIdx {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the Check row")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.skillsMenuOpen {
		t.Errorf("clicking Check should run the action and close the menu, like Enter does")
	}
}
