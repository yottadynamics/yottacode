package tui

import (
	"strings"
	"testing"
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
