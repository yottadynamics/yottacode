package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestPopupBox_AddsRoundedBorder(t *testing.T) {
	box := stripANSI(popupBox("hello"))
	if !strings.Contains(box, "╭") || !strings.Contains(box, "╰") {
		t.Errorf("popupBox should add a rounded border, got %q", box)
	}
	if !strings.Contains(box, "hello") {
		t.Errorf("popupBox should preserve body content, got %q", box)
	}
}

func TestComposePopup_OccludesBackgroundAtItsPosition(t *testing.T) {
	m := newTestModel(t)
	m.width = 40
	m.height = 10

	background := strings.Repeat(strings.Repeat("b", m.width)+"\n", m.height)
	background = strings.TrimRight(background, "\n")
	box := popupBox("PICKER")

	out := stripANSI(m.composePopup(background, box))
	if !strings.Contains(out, "PICKER") {
		t.Fatalf("composited frame should contain the popup body, got %q", out)
	}
	if lipgloss.Height(out) != m.height {
		t.Errorf("composited frame height = %d, want %d (background height)", lipgloss.Height(out), m.height)
	}
	// Background content should still be visible around the popup — a
	// true floating window occludes only the rectangle it covers, unlike
	// the old renderInlineOverlay which replaced the whole footer.
	if !strings.Contains(out, "b") {
		t.Errorf("composited frame should still show background content around the popup, got %q", out)
	}
}

func TestComposePopup_CentersBox(t *testing.T) {
	m := newTestModel(t)
	m.width = 40
	m.height = 12

	background := strings.Repeat(strings.Repeat(".", m.width)+"\n", m.height)
	background = strings.TrimRight(background, "\n")
	box := popupBox("X")

	out := stripANSI(m.composePopup(background, box))
	lines := strings.Split(out, "\n")
	boxTop := -1
	for i, line := range lines {
		if strings.Contains(line, "╭") {
			boxTop = i
			break
		}
	}
	if boxTop < 0 {
		t.Fatalf("could not locate popup top border in composited frame:\n%s", out)
	}
	// Roughly vertically centered: some background rows above and below.
	if boxTop == 0 {
		t.Errorf("popup should not be flush against the top row, got boxTop=%d in:\n%s", boxTop, out)
	}
	boxHeight := lipgloss.Height(box)
	if boxTop+boxHeight >= m.height {
		t.Errorf("popup should not be flush against the bottom row, got boxTop=%d boxHeight=%d height=%d", boxTop, boxHeight, m.height)
	}
}

func TestPopupWidth_ClampsToMaxAndFloor(t *testing.T) {
	cases := []struct {
		name      string
		termWidth int
		wantMax   int
		wantMin   int
	}{
		{"wide terminal caps at popupMaxWidth", 300, popupMaxWidth, popupMaxWidth},
		{"narrow terminal never goes below something usable", 24, 20, 1},
	}
	for _, c := range cases {
		m := newTestModel(t)
		m.width = c.termWidth
		got := m.popupWidth()
		if got > c.wantMax {
			t.Errorf("%s: popupWidth() = %d, want <= %d", c.name, got, c.wantMax)
		}
		if got < c.wantMin {
			t.Errorf("%s: popupWidth() = %d, want >= %d", c.name, got, c.wantMin)
		}
	}
}
