package tui

import (
	"fmt"
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

// Fuzzy subsequence matching: a scattered abbreviation of the command
// name itself ("ml" → m…odel) surfaces the command even though it's
// neither a prefix nor a substring match.
func TestFilterPalette_FuzzyMatchesScatteredAbbreviation(t *testing.T) {
	got := filterPalette("/ml")
	if len(got) != 1 || got[0].Name != "model" {
		t.Errorf("/ml should fuzzy-match /model via scattered m…l; got %+v", got)
	}
}

// Fuzzy ranks below prefix and substring: a query that's already a
// prefix match for one command shouldn't lose that command's usual
// top spot to fuzzy noise from other commands.
func TestFilterPalette_FuzzyRanksBelowPrefixAndSubstring(t *testing.T) {
	got := filterPalette("/gcr")
	if len(got) == 0 || !strings.HasPrefix(got[0].Name, "git-create-") {
		t.Errorf("/gcr should fuzzy-match the git-create-* commands; got %+v", got)
	}
}

// Regression guard: naive unbounded subsequence matching turns any
// short query into a near-universal match (every letter of "mo"
// appears, in order, in "permissions", "plan", "provider", "doctor",
// "yolo"...). The first-character anchor + span bound must keep a
// short, common query from dragging in unrelated commands.
func TestFilterPalette_FuzzyDoesNotMatchUnrelatedCommands(t *testing.T) {
	got := filterPalette("/mo")
	for _, c := range got {
		if c.Name == "permissions" || c.Name == "plan" || c.Name == "yolo" || c.Name == "doctor" {
			t.Errorf("/mo should not fuzzy-match unrelated command %q; got %+v", c.Name, got)
		}
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

func TestRenderInlineOverlayInsetsBodyAndRule(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	out := stripANSI(m.renderInlineOverlay("Sessions\nbody"))
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("overlay rendered too few lines: %q", out)
	}
	if strings.TrimRight(lines[0], " ") != " Sessions" || strings.TrimRight(lines[1], " ") != " body" {
		t.Fatalf("overlay body should be inset by one column, got %q / %q", lines[0], lines[1])
	}
	if !strings.HasPrefix(lines[2], " ──") {
		t.Fatalf("overlay separator should be inset by one column, got %q", lines[2])
	}
	if got := lipgloss.Width(strings.TrimRight(lines[2], " ")); got != m.width-inlineOverlayInset {
		t.Fatalf("overlay separator width = %d, want %d", got, m.width-inlineOverlayInset)
	}
}

func TestRenderInlineOverlayExpandsMenuRules(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	out := stripANSI(m.renderInlineOverlay("────\nSessions\n────"))
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("overlay rendered too few lines: %q", out)
	}
	for _, idx := range []int{0, 2} {
		trimmed := strings.TrimRight(lines[idx], " ")
		if !strings.HasPrefix(trimmed, " ──") {
			t.Fatalf("rule line %d should be inset and expanded, got %q", idx, lines[idx])
		}
		if got := lipgloss.Width(trimmed); got != m.width-inlineOverlayInset {
			t.Fatalf("rule line %d width = %d, want %d", idx, got, m.width-inlineOverlayInset)
		}
	}
}

// clampOverlayBodyHeight keeps a live overlay's rendered content within the
// terminal height. Bubble Tea's inline renderer falls back to a buggy
// tail-clip when a live View() is taller than the terminal, and that
// fallback desyncs across a resize that also changes the content's wrapped
// row count (reproduced via a scripted pty + terminal emulator: /help open
// on a terminal shorter than its full content, then widened, corrupted the
// screen into dozens of repeated lines). Staying within the terminal height
// avoids that renderer path entirely.
func TestClampOverlayBodyHeight_NoOpWhenFits(t *testing.T) {
	body := "a\nb\nc"
	if got := clampOverlayBodyHeight(body, 24, 5); got != body {
		t.Errorf("body under budget should pass through unchanged, got %q", got)
	}
}

func TestClampOverlayBodyHeight_TruncatesWithNotice(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	body := strings.Join(lines, "\n")
	got := clampOverlayBodyHeight(body, 24, 4) // budget = 24-4 = 20 rows
	gotLines := strings.Split(got, "\n")
	if len(gotLines) != 20 {
		t.Fatalf("clamped body should have exactly 20 rows (19 content + notice), got %d: %q", len(gotLines), got)
	}
	for i := 0; i < 19; i++ {
		if gotLines[i] != lines[i] {
			t.Errorf("row %d = %q, want %q (kept rows must stay in order)", i, gotLines[i], lines[i])
		}
	}
	if !strings.Contains(stripANSI(gotLines[19]), "11 more line(s)") {
		t.Errorf("last row should note the 11 hidden lines (30 - 19 kept), got %q", gotLines[19])
	}
}

func TestClampOverlayBodyHeight_NoOpWhenHeightUnknown(t *testing.T) {
	body := strings.Repeat("x\n", 200)
	if got := clampOverlayBodyHeight(body, 0, 4); got != body {
		t.Errorf("termHeight<=0 (no WindowSizeMsg yet) should not clamp, got %q", got)
	}
}

// Integration-level: renderInlineOverlay must never hand Bubble Tea a live
// View() taller than the terminal, regardless of how tall the overlay's own
// body content is.
func TestRenderInlineOverlay_NeverExceedsTerminalHeight(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.height = 20
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("skill entry %d", i))
	}
	out := m.renderInlineOverlay(strings.Join(lines, "\n"))
	got := len(strings.Split(out, "\n"))
	if got > m.height {
		t.Errorf("overlay rendered %d rows, want at most terminal height %d", got, m.height)
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
