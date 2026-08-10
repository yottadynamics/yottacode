package tui

import "testing"

func TestDisplayColumnToRuneIndexWideGlyphs(t *testing.T) {
	line := "A界B"
	cases := []struct {
		col  int
		want int
	}{
		{0, 0},
		{1, 1},
		{2, 1}, // second display cell of the wide glyph still maps to 界.
		{3, 2},
		{4, 3},
	}
	for _, tc := range cases {
		if got := displayColumnToRuneIndex(line, tc.col); got != tc.want {
			t.Errorf("displayColumnToRuneIndex(%q, %d) = %d, want %d", line, tc.col, got, tc.want)
		}
	}
}

func TestRuneIndexToDisplayColumnWideGlyphs(t *testing.T) {
	line := "A界B"
	cases := []struct {
		idx  int
		want int
	}{
		{0, 0},
		{1, 1},
		{2, 3},
		{3, 4},
	}
	for _, tc := range cases {
		if got := runeIndexToDisplayColumn(line, tc.idx); got != tc.want {
			t.Errorf("runeIndexToDisplayColumn(%q, %d) = %d, want %d", line, tc.idx, got, tc.want)
		}
	}
}
