package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestFilterPalette_EmptyShowsAll(t *testing.T) {
	got := filterPalette("/")
	if len(got) != len(allSlash) {
		t.Errorf("/ should match all %d commands; got %d", len(allSlash), len(got))
	}
}

func TestFilterPalette_PrefixMatch(t *testing.T) {
	got := filterPalette("/mo")
	if len(got) == 0 || got[0].Name != "model" {
		t.Errorf("/mo should rank the prefix match /model first; got %+v", got)
	}
	for _, c := range got {
		if !strings.Contains(c.Name, "mo") {
			t.Errorf("/mo matched %q, which does not contain 'mo'", c.Name)
		}
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

// Substring matching: the verb the user thinks in is often mid-name
// ("review" for /git-review-pr). Prefix-only matching made every
// /git-* command invisible to its verb.
func TestFilterPalette_SubstringFindsMidName(t *testing.T) {
	got := filterPalette("/review")
	found := false
	for _, c := range got {
		if c.Name == "git-review-pr" {
			found = true
		}
	}
	if !found {
		t.Errorf("/review should surface /git-review-pr via substring match; got %+v", got)
	}
}

// Ranking: every prefix match precedes every substring-only match, so
// muscle-memory completions keep their position at the top.
func TestFilterPalette_PrefixRanksAboveSubstring(t *testing.T) {
	got := filterPalette("/pr")
	if len(got) < 2 {
		t.Fatalf("/pr should match several commands (provider, git-*-pr); got %+v", got)
	}
	if got[0].Name != "provider" {
		t.Errorf("/pr should rank the prefix match /provider first; got %+v", got)
	}
	seenSubstr := false
	for _, c := range got {
		isPrefix := strings.HasPrefix(c.Name, "pr")
		if !isPrefix {
			seenSubstr = true
		}
		if isPrefix && seenSubstr {
			t.Errorf("prefix match %q appeared after a substring match; order: %+v", c.Name, got)
		}
	}
}

// The Model-bound variant applies the same ranking across built-ins +
// custom commands: a custom prefix match outranks a built-in
// substring match.
func TestFilterPaletteAll_RanksAcrossCustomCommands(t *testing.T) {
	m := Model{customSlash: []slashCommand{{Name: "pretty-print", Help: "custom"}}}
	got := m.filterPaletteAll("/pr")
	idxCustom, idxSubstr := -1, -1
	for i, c := range got {
		if c.Name == "pretty-print" {
			idxCustom = i
		}
		if idxSubstr == -1 && !strings.HasPrefix(c.Name, "pr") {
			idxSubstr = i
		}
	}
	if idxCustom == -1 {
		t.Fatalf("custom command should match /pr; got %+v", got)
	}
	if idxSubstr != -1 && idxCustom > idxSubstr {
		t.Errorf("custom prefix match (idx %d) should rank above built-in substring matches (first at %d)", idxCustom, idxSubstr)
	}
}

func TestFilterPalette_LeadingSlashOptional(t *testing.T) {
	got := filterPalette("perm")
	if len(got) != 1 || got[0].Name != "permissions" {
		t.Errorf("'perm' (without slash) should still match /permissions; got %+v", got)
	}
}

// Both palettes hover directly above the full-width input frame; their
// right edges must align with it at EVERY terminal width. Two past
// bugs pinned here: a 120-column cap on liveContentWidth left the
// boxes ~26 columns short of the cmdline box on wide terminals, and
// before that the border-exclusive lipgloss Width made them overflow
// the terminal by two columns.
func TestRenderPalette_MatchesInputFrameWidth(t *testing.T) {
	files := []fileEntry{{Path: "main.go"}, {Path: "docs", IsDir: true}}
	for _, width := range []int{80, 124, 150, 200} {
		m := newTestModel(t)
		m.width = width
		frameW := lipgloss.Width(strings.Split(m.renderInputFrame(), "\n")[0])

		slash := strings.Split(renderPalette(allSlash, 0, 0, liveContentWidth(m.width)+4), "\n")[0]
		if got := lipgloss.Width(slash); got != frameW {
			t.Errorf("width %d: slash palette box is %d cols, input frame is %d — right edges must align",
				width, got, frameW)
		}
		file := strings.Split(renderFilePalette(files, 0, 0, liveContentWidth(m.width)+4), "\n")[0]
		if got := lipgloss.Width(file); got != frameW {
			t.Errorf("width %d: file palette box is %d cols, input frame is %d — right edges must align",
				width, got, frameW)
		}
	}
}

func TestRenderPalette_HighlightsSelected(t *testing.T) {
	out := stripANSI(renderPalette(allSlash, 0, 0, 80))
	// /plan is at index 0; it should use the same arrow-cursor picker
	// affordance as slash-command submenus.
	if !strings.Contains(out, "❯ /plan") {
		t.Errorf("palette selected row should use ❯ cursor for /plan: %q", out)
	}
}

func TestRenderPalette_IncludesHeaderDivider(t *testing.T) {
	out := stripANSI(renderPalette(allSlash, 0, 0, 80))
	if !strings.Contains(out, slashPaletteTitle) || !strings.Contains(out, "──") {
		t.Fatalf("slash palette should include title and divider: %q", out)
	}
}

func TestRenderFilePalette_IncludesHeaderDivider(t *testing.T) {
	files := []fileEntry{{Path: "main.go"}}
	out := stripANSI(renderFilePalette(files, 0, 0, 80))
	if !strings.Contains(out, filePaletteTitle) || !strings.Contains(out, "──") {
		t.Fatalf("file palette should include title and divider: %q", out)
	}
}

func TestRenderPalette_EmptyShowsHint(t *testing.T) {
	out := renderPalette(nil, 0, 0, 80)
	if !strings.Contains(out, "no matching") {
		t.Errorf("empty palette should show hint; got %q", out)
	}
}

// When the filtered list is longer than slashPaletteVisible, the
// rendered palette must window the items and surface ▲/▼ overflow hints
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
	// Count rendered command rows. Only actual menu rows start with the
	// picker cursor or its unselected spacer prefix.
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "❯ /") || strings.HasPrefix(line, "  /") {
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
