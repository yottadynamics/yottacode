package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestCapLabeledBoxWidth(t *testing.T) {
	cases := []struct {
		termWidth int
		want      int
	}{
		{0, 0},      // unknown width — uncapped
		{-5, 0},     // defensive: negative treated as unknown
		{50, 46},    // termWidth-4, under the ceiling
		{1000, 120}, // clamped to labeledBoxCap
		{124, 120},  // exactly at the ceiling after -4
	}
	for _, c := range cases {
		if got := capLabeledBoxWidth(c.termWidth); got != c.want {
			t.Errorf("capLabeledBoxWidth(%d) = %d, want %d", c.termWidth, got, c.want)
		}
	}
}

func TestHardWrapLabeled_UncappedPassesThrough(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := hardWrapLabeled(long, 0)
	if len(got) != 1 || got[0] != long {
		t.Fatalf("capW=0 should pass the line through unwrapped, got %v", got)
	}
}

func TestHardWrapLabeled_WrapsLongLineToCap(t *testing.T) {
	long := strings.Repeat("x", 500)
	capW := 40
	lines := hardWrapLabeled(long, capW)
	if len(lines) < 2 {
		t.Fatalf("500-char line at capW=%d should wrap to multiple lines, got %d", capW, len(lines))
	}
	wrapW := capW - labeledBoxIndentW
	for i, line := range lines {
		if w := ansi.StringWidth(line); w > wrapW {
			t.Errorf("line %d width %d exceeds wrap width %d: %q", i, w, wrapW, line)
		}
	}
}

func TestRenderLabeledBox_ClampsToCapDespiteLongLabel(t *testing.T) {
	leftLabel := " Title "                            // 7 cols
	rightLabel := " " + strings.Repeat("y", 20) + " " // 22 cols — longer than the body, but under capW
	capW := 30

	box := renderLabeledBox(leftLabel, rightLabel, []string{"  short body"}, capW, colorWarning)
	lines := strings.Split(box, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected a multi-line box, got %q", box)
	}
	// Every framed row (│ ... │, and the top/bottom border) should fit
	// within capW+4 (2-col border + 2-col interior padding on each
	// side) — this is the clamp the approval/plan-mode boxes always
	// applied to header expansion and the LSP advisory box previously
	// lacked entirely. (Note: this only holds while each label's raw
	// width stays under capW — a single label wider than capW by
	// itself still forces the top row wider, since renderLabeledBox
	// never truncates a label. No real caller produces that: tool
	// names and language labels are short, fixed strings.)
	for i, line := range lines {
		if w := ansi.StringWidth(line); w > capW+4 {
			t.Errorf("row %d width %d exceeds capW+4=%d: %q", i, w, capW+4, stripANSI(line))
		}
	}
}

func TestRenderLabeledBox_ZeroCapDoesNotClamp(t *testing.T) {
	rightLabel := " " + strings.Repeat("y", 100) + " "
	box := renderLabeledBox(" Title ", rightLabel, []string{"  x"}, 0, colorWarning)
	lines := strings.Split(box, "\n")
	if w := ansi.StringWidth(lines[0]); w < 100 {
		t.Errorf("capW=0 should not clamp the header, got top row width %d: %q", w, stripANSI(lines[0]))
	}
}
