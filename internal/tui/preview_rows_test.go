package tui

import (
	"strings"
	"testing"
)

// Regression test for a real user report: CPU pegged near/over 100% and the
// TUI appeared frozen whenever the agent streamed back a large amount of
// text (e.g. narrating a big API payload). Root cause was previewRows
// reprocessing the ENTIRE live-stream buffer (m.streaming / m.reasoning) —
// word-splitting plus grapheme-aware width measurement — on every single
// call, while every streamed token/reasoning chunk triggers exactly one
// call via View(). That is O(total streamed bytes) of work per call, O(n^2)
// over the life of a long response, even though previewRows only ever
// returns the last one or two rows. tailForPreview bounds the input before
// any wrap work happens, making the cost independent of total buffer size.
func TestPreviewRows_BoundedForHugeSingleBuffer(t *testing.T) {
	var b strings.Builder
	b.Grow(10 << 20)
	for b.Len() < 10<<20 { // 10MB, well beyond anything a real stream produces before this test would matter
		b.WriteString("the quick brown fox jumps over the lazy dog and keeps narrating a very large API payload ")
	}
	s := b.String()

	bounded := tailForPreview(s, 80, 2)
	if got, max := len(bounded), 80*2*8; got > max {
		t.Fatalf("bounded preview bytes = %d, want at most %d", got, max)
	}
	rows := previewRows(s, 80, 2)
	if len(rows) == 0 || len(rows) > 2 {
		t.Fatalf("expected 1-2 rows, got %d: %q", len(rows), rows)
	}
	if !strings.HasPrefix(rows[0], "…") {
		t.Errorf("truncated preview should lead with an ellipsis, got %q", rows[0])
	}
}

// TestPreviewRows_IncrementalStreamStaysBounded mirrors the exact failure
// mode: many small chunks appended one at a time (like agent.ContentToken
// deltas), each followed by a previewRows call (like one View() per
// streamed chunk). Before the fix, this pattern was quadratic and 2,000
// chunks (~64KB total) alone took well over two minutes; it must now
// complete quickly regardless of how many chunks accumulate.
func TestPreviewRows_IncrementalStreamStaysBounded(t *testing.T) {
	var b strings.Builder
	chunk := "the quick brown fox jumps over "
	for i := 0; i < 20_000; i++ {
		b.WriteString(chunk)
		bounded := tailForPreview(b.String(), 80, 2)
		if got, max := len(bounded), 80*2*8; got > max {
			t.Fatalf("iteration %d: bounded preview bytes = %d, want at most %d", i, got, max)
		}
		rows := previewRows(b.String(), 80, 2)
		if len(rows) == 0 || len(rows) > 2 {
			t.Fatalf("iteration %d: expected 1-2 rows, got %d", i, len(rows))
		}
	}
}
