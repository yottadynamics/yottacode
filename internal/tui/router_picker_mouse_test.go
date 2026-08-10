package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/config"
)

func TestRouterPicker_ClickModelRowCommitsAndReturnsToMenu(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel(t)
	m.routerPickerOpen = true
	m.routerPicker = &routerPickerState{
		selecting: "fast",
		models: []routerModelEntry{
			{ref: "anthropic:claude-haiku-4-5", label: "anthropic:claude-haiku-4-5"},
			{ref: "anthropic:claude-opus-4-6", label: "anthropic:claude-opus-4-6"},
		},
		visibleRows: 12,
	}

	hits := &pickerHits{}
	box := popupBox(renderRouterPicker(m.routerPicker, m.popupWidth(), hits))
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

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.routerPicker.selecting != "" {
		t.Errorf("clicking a model row should commit and return to the menu, got selecting=%q", m.routerPicker.selecting)
	}
	if chainPrimary(m.routerPicker.fastChain) != "anthropic:claude-opus-4-6" {
		t.Errorf("fastChain primary = %v, want anthropic:claude-opus-4-6", m.routerPicker.fastChain)
	}
}

func TestRouterPicker_ClickMenuRowOpensModelList(t *testing.T) {
	m := newTestModel(t)
	m.routerPickerOpen = true
	m.routerPicker = &routerPickerState{
		mode:       config.RouterModeOff,
		fastChain:  []string{"anthropic:claude-haiku-4-5"},
		smartChain: []string{"anthropic:claude-opus-4-6"},
	}

	hits := &pickerHits{}
	box := popupBox(renderRouterPicker(m.routerPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == rowFastPrimary {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the Implementer row")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.routerPicker.selecting != "fast" {
		t.Errorf("clicking the Implementer row should open the fast model sub-list, got selecting=%q", m.routerPicker.selecting)
	}
}
