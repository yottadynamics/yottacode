package tui

import (
	"fmt"
	"strings"
	"testing"
)

// boundInputForWrapCost bounds wrapInputRows' cost so a large paste sitting
// in the prompt box doesn't cost full-buffer ansi.Hardwrap work on every
// render — including the perpetual 530ms cursor-blink tick while
// completely idle. These tests cover both pathological shapes (one huge
// unbroken line, and many short lines) and check CORRECTNESS of what ends
// up on screen around the cursor, not just speed — a fast but wrong window
// would be worse than the original bug.

// TestWrapInputRows_HugeSingleLineStaysBoundedAndCorrect builds one 2MB
// line with a distinctive marker placed exactly at the cursor's column,
// and checks that (a) wrapping completes quickly regardless of the line's
// total size, and (b) the marker text is actually present in the windowed
// rows around the cursor — proving the bound is centered correctly, not
// just fast.
func TestWrapInputRows_HugeSingleLineStaysBoundedAndCorrect(t *testing.T) {
	const marker = "<<CURSOR-IS-HERE>>"
	pad := strings.Repeat("x", 1<<20)
	val := pad + marker + pad
	cursorCol := len(pad) + len(marker)/2 // rune offset lands inside the marker

	rows, cursorVisRow := wrapInputRows(val, 80, 0, cursorCol)
	windowed, _ := windowInputRows(rows, cursorVisRow, inputMaxRows)

	if got, max := len(rows), inputMaxRows*2+1; got > max {
		t.Fatalf("wrapped rows = %d, want at most %d from the bounded cursor window", got, max)
	}
	if cursorVisRow < 0 {
		t.Fatalf("cursor row not found (cursorVisRow=-1)")
	}
	var joined strings.Builder
	for _, r := range windowed {
		joined.WriteString(r.text)
	}
	if !strings.Contains(joined.String(), marker) {
		t.Errorf("windowed rows around the cursor should contain the marker text near the cursor's column; got %q", joined.String())
	}
}

// TestWrapInputRows_ManyShortLinesStaysBoundedAndCorrect builds 200,000
// short, individually-numbered lines and positions the cursor deep in the
// middle, checking both speed and that the displayed window shows lines
// near the cursor's actual line number.
func TestWrapInputRows_ManyShortLinesStaysBoundedAndCorrect(t *testing.T) {
	const n = 200_000
	const cursorLine = 100_000
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line-%06d\n", i)
	}
	val := strings.TrimSuffix(b.String(), "\n")

	rows, cursorVisRow := wrapInputRows(val, 80, cursorLine, 4)
	windowed, _ := windowInputRows(rows, cursorVisRow, inputMaxRows)

	if got, max := len(rows), inputMaxRows*2+1; got > max {
		t.Fatalf("wrapped rows = %d, want at most %d from the bounded logical-line window", got, max)
	}
	if cursorVisRow < 0 {
		t.Fatalf("cursor row not found (cursorVisRow=-1)")
	}
	var joined strings.Builder
	for _, r := range windowed {
		joined.WriteString(r.text)
		joined.WriteByte('\n')
	}
	if !strings.Contains(joined.String(), fmt.Sprintf("line-%06d", cursorLine)) {
		t.Errorf("windowed rows should show lines near line %d, got %q", cursorLine, joined.String())
	}
	// The window must NOT show unrelated content from the start of a
	// 200,000-line document — that would mean the bound picked the wrong
	// slice of the document.
	if strings.Contains(joined.String(), "line-000000") {
		t.Errorf("windowed rows around line %d should not include line 0: %q", cursorLine, joined.String())
	}
}

// TestWrapInputRows_SmallInputUnaffected locks in that the bound is a
// true no-op below its size threshold: same rows, same cursorVisRow, same
// logical numbering as an unbounded wrap would produce. Guards against a
// regression that silently changes behavior for the overwhelmingly common
// case (normal-sized input).
func TestWrapInputRows_SmallInputUnaffected(t *testing.T) {
	val := "first line\nsecond line with the cursor\nthird line"
	rows, cursorVisRow := wrapInputRows(val, 80, 1, 7)
	if cursorVisRow != 1 {
		t.Fatalf("cursorVisRow = %d, want 1", cursorVisRow)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows for 3 short logical lines, got %d", len(rows))
	}
	for i, want := range []string{"first line", "second line with the cursor", "third line"} {
		if rows[i].text != want || rows[i].logical != i {
			t.Errorf("row %d = %+v, want text=%q logical=%d", i, rows[i], want, i)
		}
	}
}
