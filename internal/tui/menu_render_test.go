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

func TestRenderMenuDividerHandlesNarrowWidth(t *testing.T) {
	if got := stripANSI(renderMenuDivider(0)); got != "─" {
		t.Fatalf("zero-width divider = %q, want one rule char", got)
	}
}
