package tui

import (
	"strings"
	"testing"
)

// codeBlockBufLines / tableBufLines replace a strings.Count(buf.String(),
// "\n") rescan inside codeBlockNoticeText/tableNoticeText — the live
// "…writing code (N lines…)" / "…formatting table (N rows…)" notice text.
// Same "reprocess the whole live buffer on every render" shape as the
// previewRows bug, just with a cheaper per-call scan (strings.Count is a
// fast byte-level scan, not grapheme-width math), so lower severity — but
// worth fixing the same way: track the count incrementally at each write
// site instead of rescanning.
//
// handleStreamLine (and thus codeBlockBuf/tableBuf) only actually runs in
// one synchronous burst inside commitStreaming, at the end of a streamed
// response — not per streamed token — so these tests exercise it directly
// (white-box) rather than through a single ContentToken event, which
// wouldn't reach this code at all.
func TestCodeBlockBufLines_TracksLinesWrittenByHandleStreamLine(t *testing.T) {
	m := newTestModel(t)
	m.handleStreamLine("```go")
	if !m.inCodeBlock {
		t.Fatalf("fence-open line should set inCodeBlock")
	}
	if m.codeBlockBufLines != 0 {
		t.Fatalf("codeBlockBufLines = %d, want 0 right after the fence-open line (no body lines yet)", m.codeBlockBufLines)
	}
	m.handleStreamLine("line one")
	m.handleStreamLine("line two")
	m.handleStreamLine("line three")
	if m.codeBlockBufLines != 3 {
		t.Fatalf("codeBlockBufLines = %d, want 3 after 3 body lines", m.codeBlockBufLines)
	}
	if got := strings.Count(m.codeBlockBuf.String(), "\n"); got != m.codeBlockBufLines {
		t.Errorf("codeBlockBufLines (%d) drifted from an actual rescan (%d)", m.codeBlockBufLines, got)
	}
	if got := m.codeBlockNoticeText(); !strings.Contains(got, "(3 lines") {
		t.Errorf("notice text should report 3 lines, got %q", got)
	}

	// Closing the fence flushes to scrollback and resets the counter.
	m.handleStreamLine("```")
	if m.codeBlockBufLines != 0 {
		t.Errorf("codeBlockBufLines should reset to 0 once the fence closes, got %d", m.codeBlockBufLines)
	}
	if m.inCodeBlock {
		t.Errorf("inCodeBlock should be false after the closing fence")
	}
}

func TestTableBufLines_TracksRowsWrittenByHandleStreamLine(t *testing.T) {
	m := newTestModel(t)
	m.handleStreamLine("| H1 | H2 |")
	m.handleStreamLine("| --- | --- |")
	m.handleStreamLine("| a | b |")
	if !m.inTable {
		t.Fatalf("table lines should set inTable")
	}
	if m.tableBufLines != 3 {
		t.Fatalf("tableBufLines = %d, want 3 after header+separator+one row", m.tableBufLines)
	}
	if got := strings.Count(m.tableBuf.String(), "\n"); got != m.tableBufLines {
		t.Errorf("tableBufLines (%d) drifted from an actual rescan (%d)", m.tableBufLines, got)
	}
	if got := m.tableNoticeText(); !strings.Contains(got, "(3 rows") {
		t.Errorf("notice text should report 3 rows, got %q", got)
	}

	// A non-table line ends the table (flush) and resets the counter.
	m.handleStreamLine("done.")
	if m.tableBufLines != 0 {
		t.Errorf("tableBufLines should reset to 0 once the table flushes, got %d", m.tableBufLines)
	}
	if m.inTable {
		t.Errorf("inTable should be false after the table flushes")
	}
}

// TestCodeBlockBufLines_ResetsOnDiscard guards the third reset site
// (discardStreaming, the tool-call/pre-tool-scratch path) alongside the
// two flush paths covered above.
func TestCodeBlockBufLines_ResetsOnDiscard(t *testing.T) {
	m := newTestModel(t)
	m.handleStreamLine("```go")
	m.handleStreamLine("line one")
	m.handleStreamLine("line two")
	if m.codeBlockBufLines != 2 {
		t.Fatalf("codeBlockBufLines = %d, want 2", m.codeBlockBufLines)
	}
	m.discardStreaming()
	if m.codeBlockBufLines != 0 {
		t.Errorf("codeBlockBufLines should reset to 0 on discardStreaming, got %d", m.codeBlockBufLines)
	}
	if m.tableBufLines != 0 {
		t.Errorf("tableBufLines should also reset to 0 on discardStreaming, got %d", m.tableBufLines)
	}
}
