package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/skills"
)

func TestSkillsPicker_ClickTabSwitchesView(t *testing.T) {
	m := newTestModel(t)
	m.skillsPicker = &skillsPickerState{
		tab:     catalogTabOfficial,
		rows:    []skills.Skill{{Name: "bundled-one", Source: skills.ScopeBuiltin}},
		enabled: map[string]bool{},
	}
	m.skillsPickerOpen = true

	hits := &pickerHits{}
	box := popupBox(renderSkillsPicker(m.skillsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitTab && r.Index == int(catalogTabBundled) {
			mid := (r.ColStart + r.ColEnd) / 2
			x, y, found = ox+2+mid, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the Bundled tab")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.skillsPicker.tab != catalogTabBundled {
		t.Errorf("clicking the Bundled tab should switch to it, got tab=%v", m.skillsPicker.tab)
	}
	if !m.skillsPickerOpen {
		t.Errorf("clicking a tab should not close the picker")
	}
}

func TestSkillsPicker_ClickItemViewsBody(t *testing.T) {
	m := newTestModel(t)
	m.skillsPicker = &skillsPickerState{
		tab:     catalogTabBundled,
		rows:    []skills.Skill{{Name: "one", Source: skills.ScopeBuiltin, Body: "# one"}, {Name: "two", Source: skills.ScopeBuiltin, Body: "# two"}},
		enabled: map[string]bool{},
	}
	m.skillsPickerOpen = true

	hits := &pickerHits{}
	box := popupBox(renderSkillsPicker(m.skillsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 1 {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for item 1")
	}

	m, cmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.skillsPickerOpen {
		t.Errorf("clicking a bundled-tab item should view it (Enter's behavior) and close the picker")
	}
	if cmd == nil {
		t.Errorf("viewing a skill body should return a cmd (staged pager open)")
	}
}
