package tui

import (
	"strings"
	"testing"
)

func TestRenderMenuHeaderIncludesDivider(t *testing.T) {
	out := stripANSI(renderMenuHeader("Provider", "pick a configured provider"))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("header lines = %d, want 4: %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "──") {
		t.Fatalf("top divider line missing horizontal rule: %q", lines[0])
	}
	if lines[1] != "Provider" {
		t.Fatalf("title line = %q", lines[1])
	}
	if lines[2] != "pick a configured provider" {
		t.Fatalf("description line = %q", lines[2])
	}
	if !strings.Contains(lines[3], "──") {
		t.Fatalf("bottom divider line missing horizontal rule: %q", lines[3])
	}
}

func TestRenderMenuHeaderDividerWithoutDescription(t *testing.T) {
	out := stripANSI(renderMenuHeader("Model", ""))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("header lines = %d, want 3: %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "──") {
		t.Fatalf("top divider line missing horizontal rule: %q", lines[0])
	}
	if lines[1] != "Model" {
		t.Fatalf("title line = %q", lines[1])
	}
	if !strings.Contains(lines[2], "──") {
		t.Fatalf("bottom divider line missing horizontal rule: %q", lines[2])
	}
}

func TestRenderMenuHeaderCustomWidth(t *testing.T) {
	out := stripANSI(renderMenuHeader("Sessions", "Resume, rename, or export.", 96))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("header lines = %d, want 4: %q", len(lines), out)
	}
	if got := runeLen(lines[0]); got != 96 {
		t.Fatalf("top divider width = %d, want 96", got)
	}
	if got := runeLen(lines[3]); got != 96 {
		t.Fatalf("bottom divider width = %d, want 96", got)
	}
}

func TestRenderMenuDividerHandlesNarrowWidth(t *testing.T) {
	if got := stripANSI(renderMenuDivider(0)); got != "─" {
		t.Fatalf("zero-width divider = %q, want one rule char", got)
	}
}
