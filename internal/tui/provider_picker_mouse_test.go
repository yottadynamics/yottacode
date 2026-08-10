package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestProviderPicker_ClickMenuItemTransitionsToUseList(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")

	var useIdx int
	for i, item := range m.providerPicker.menuItems {
		if item.Label == "Use" {
			useIdx = i
		}
	}

	hits := &pickerHits{}
	box := popupBox(renderProviderPicker(m.providerPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == useIdx {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the Use row")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.providerPicker.mode != providerUsePickerMode {
		t.Errorf("clicking Use should transition to use-list mode; got %v", m.providerPicker.mode)
	}
}

func TestProviderPicker_ClickUseListItemSwitchesActive(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	m = navigateToMenuItem(t, m, "Use")
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // → Use sub-picker

	hits := &pickerHits{}
	box := popupBox(renderProviderPicker(m.providerPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 1 { // openai
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for row 1")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.providerPickerOpen {
		t.Errorf("clicking a use-list row should commit and close the picker, like Enter does")
	}
	if m.baseURL != "https://api.openai.com/v1" {
		t.Errorf("baseURL after click-switch = %q, want https://api.openai.com/v1", m.baseURL)
	}
}
