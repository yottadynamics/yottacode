package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestPopupBoxShowsCloseGlyph(t *testing.T) {
	box := popupBox(renderMenuHeader("Help", "esc to close", 40))
	lines := strings.Split(stripANSI(box), "\n")
	if len(lines) == 0 {
		t.Fatal("popup rendered no lines")
	}
	if !strings.Contains(lines[0], "×") {
		t.Fatalf("popup top border missing close glyph: %q", lines[0])
	}
}

func TestPopupCloseHitDismissesHelp(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: 30})
	m.helpPanel = renderHelpPanel(m)
	m.helpOpen = true

	box, ok := m.activePopupBody()
	if !ok {
		t.Fatal("expected active help popup")
	}
	ox, oy := m.popupOrigin(box)
	m, _ = m.handleMouseClick(tea.MouseClickMsg{X: ox + lipgloss.Width(box) - 2, Y: oy})
	if m.helpOpen {
		t.Fatal("clicking popup close glyph should dismiss help")
	}
}
