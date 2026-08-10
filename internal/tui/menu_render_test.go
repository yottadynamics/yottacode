package tui

import (
	"strings"
	"testing"
)

func TestRenderMenuHeaderIncludesDivider(t *testing.T) {
	out := stripANSI(renderMenuHeader("Provider", "pick a configured provider"))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("header lines = %d, want 3: %q", len(lines), out)
	}
	if lines[0] != "Provider" {
		t.Fatalf("title line = %q", lines[0])
	}
	if lines[1] != "pick a configured provider" {
		t.Fatalf("description line = %q", lines[1])
	}
	if !strings.Contains(lines[2], "──") {
		t.Fatalf("divider line missing horizontal rule: %q", lines[2])
	}
}

func TestRenderMenuHeaderDividerWithoutDescription(t *testing.T) {
	out := stripANSI(renderMenuHeader("Model", ""))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("header lines = %d, want 2: %q", len(lines), out)
	}
	if lines[0] != "Model" {
		t.Fatalf("title line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "──") {
		t.Fatalf("divider line missing horizontal rule: %q", lines[1])
	}
}

func TestRenderMenuHeaderCustomWidth(t *testing.T) {
	out := stripANSI(renderMenuHeader("Sessions", "Resume, rename, or export.", 96))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("header lines = %d, want 3: %q", len(lines), out)
	}
	if got := runeLen(lines[2]); got != 96 {
		t.Fatalf("divider width = %d, want 96", got)
	}
}

func TestRenderMenuDividerHandlesNarrowWidth(t *testing.T) {
	if got := stripANSI(renderMenuDivider(0)); got != "─" {
		t.Fatalf("zero-width divider = %q, want one rule char", got)
	}
}

// computeMenuDividerWidth must degrade on narrow terminals like the status
// bar and tool cards already do, instead of overflowing/wrapping at a fixed
// 72 columns — and must stay pinned at the historical 72 on any terminal
// wide enough to afford it, so existing wide-terminal layouts don't shift.
func TestComputeMenuDividerWidth_DegradesOnNarrowTerminals(t *testing.T) {
	cases := []struct {
		terminalWidth int
		want          int
	}{
		{200, menuDividerWidthCap},
		{76, menuDividerWidthCap}, // 76-4=72, exactly the cap
		{60, 56},                  // 60-4, below the cap
		{30, 26},                  // 30-4, below the cap, above the floor
		{10, menuDividerWidthFloor},
	}
	for _, tc := range cases {
		if got := computeMenuDividerWidth(tc.terminalWidth); got != tc.want {
			t.Errorf("computeMenuDividerWidth(%d) = %d, want %d", tc.terminalWidth, got, tc.want)
		}
	}
}

// A picker's default-width header (no explicit width passed to
// renderMenuHeader) must not overflow the terminal it's rendered in — the
// regression this whole mechanism exists to prevent.
func TestRenderMenuHeaderDefaultWidth_TracksNarrowTerminal(t *testing.T) {
	prev := menuDividerWidth
	t.Cleanup(func() { menuDividerWidth = prev })

	menuDividerWidth = computeMenuDividerWidth(40)
	out := stripANSI(renderMenuHeader("Model", ""))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if got := runeLen(lines[len(lines)-1]); got > 40 {
		t.Fatalf("divider width = %d, overflows a 40-col terminal", got)
	}
}
