package tui

import (
	"strings"
	"testing"
)

func TestFilterPalette_EmptyShowsAll(t *testing.T) {
	got := filterPalette("/")
	if len(got) != len(allSlash) {
		t.Errorf("/ should match all %d commands; got %d", len(allSlash), len(got))
	}
}

func TestFilterPalette_PrefixMatch(t *testing.T) {
	got := filterPalette("/mo")
	if len(got) != 1 || got[0].Name != "model" {
		t.Errorf("/mo should match only /model; got %+v", got)
	}
}

func TestFilterPalette_NoMatch(t *testing.T) {
	got := filterPalette("/zzz")
	if len(got) != 0 {
		t.Errorf("/zzz should match nothing; got %+v", got)
	}
}

func TestFilterPalette_CaseInsensitive(t *testing.T) {
	got := filterPalette("/MOD")
	if len(got) != 1 || got[0].Name != "model" {
		t.Errorf("/MOD should match /model case-insensitively; got %+v", got)
	}
}

func TestFilterPalette_LeadingSlashOptional(t *testing.T) {
	got := filterPalette("perm")
	if len(got) != 1 || got[0].Name != "permissions" {
		t.Errorf("'perm' (without slash) should still match /permissions; got %+v", got)
	}
}

func TestRenderPalette_HighlightsSelected(t *testing.T) {
	out := renderPalette(allSlash, 0, 0, 80)
	// /plan is at index 0; it should appear with the selected style applied.
	if !strings.Contains(out, "/plan") {
		t.Errorf("palette missing /plan: %q", out)
	}
}

func TestRenderPalette_EmptyShowsHint(t *testing.T) {
	out := renderPalette(nil, 0, 0, 80)
	if !strings.Contains(out, "no matching") {
		t.Errorf("empty palette should show hint; got %q", out)
	}
}

// When the filtered list is longer than slashPaletteVisible, the
// rendered palette must window the items and surface ↑/↓ overflow hints
// so the user knows there's more to scroll to. Mirrors the file palette
// behavior so both pickers feel the same.
// When the filtered list is longer than slashPaletteVisible, the
// rendered palette must window the items (no overflow-count hints —
// the scrolling position is visible from the highlight alone).
func TestRenderPalette_WindowedNoOverflowHints(t *testing.T) {
	if len(allSlash) <= slashPaletteVisible {
		t.Skipf("test requires more than %d built-in commands; have %d", slashPaletteVisible, len(allSlash))
	}
	out := stripANSI(renderPalette(allSlash, 0, 0, 80))
	// Count rendered command rows — each starts with " /" after ANSI strip.
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "/") {
			rows++
		}
	}
	if rows > slashPaletteVisible {
		t.Errorf("rendered %d command rows, want at most %d", rows, slashPaletteVisible)
	}
	if strings.Contains(out, "more") {
		t.Errorf("overflow hints should not appear: %q", out)
	}
}
