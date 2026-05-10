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
	out := renderPalette(allSlash, 0, 80)
	// /help is at index 0; it should appear with the selected style applied.
	// We can't easily detect ANSI here without a parser, but we can at least
	// confirm it rendered and contains the command names.
	if !strings.Contains(out, "/help") {
		t.Errorf("palette missing /help: %q", out)
	}
	if !strings.Contains(out, "/model") {
		t.Errorf("palette missing /model: %q", out)
	}
}

func TestRenderPalette_EmptyShowsHint(t *testing.T) {
	out := renderPalette(nil, 0, 80)
	if !strings.Contains(out, "no matching") {
		t.Errorf("empty palette should show hint; got %q", out)
	}
}
