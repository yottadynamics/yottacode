package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestMCPPicker_ClickMenuItemNavigates(t *testing.T) {
	m := newTestModel(t)
	m.openMCPPicker()

	hits := &pickerHits{}
	box := popupBox(renderMCPPicker(m.mcpPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 1 { // "Add"
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for row 1")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if !m.mcpPickerOpen {
		t.Fatalf("clicking a menu row should stay open, navigating into that submode")
	}
	if m.mcpPicker.mode != mcpAddMode {
		t.Errorf("mode = %d, want mcpAddMode", m.mcpPicker.mode)
	}
}

func TestMCPPicker_ClickServerRowClosesList(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[mcp_servers]]
name    = "test-srv"
command = "echo"
`)
	m.openMCPPicker(mcpListMode)
	if len(m.mcpPicker.servers) == 0 {
		t.Fatal("expected at least 1 server entry")
	}

	hits := &pickerHits{}
	box := popupBox(renderMCPPicker(m.mcpPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 0 {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for row 0")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.mcpPickerOpen {
		t.Errorf("clicking a server row in list mode should commit and close, like Enter does")
	}
}
