package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestTranscriptSelection_HighlightsCorrectTableRow is a regression guard
// for a bubbles v2.1.1 upstream bug (viewport/highlight.go's parseMatches):
// its cross-line byte walk indexes newline-boundary checks into the raw
// ANSI-styled content using byte positions that only ever advance in
// ansi.Strip'd-string units, so it silently misattributes highlights to an
// earlier line whenever any row the walk crosses is ANSI-styled — exactly
// what a glamour-rendered markdown table's per-cell coloring produces.
// applyTranscriptHighlight (transcript_select.go) now bypasses that path
// entirely by applying lipgloss.StyleRanges directly per selected row.
func TestTranscriptSelection_HighlightsCorrectTableRow(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	m.width = 100
	m.height = 30

	raw := "| Feature | Description |\n|---|---|\n| Retry — logic | Handles errors → gracefully |\n| Cache | Speeds things up |\n"
	m.tableBuf.WriteString(raw)
	m.inTable = true
	m.flushTable()

	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	targetLine := -1
	for i, row := range m.transcriptRows {
		if strings.Contains(ansi.Strip(row), "Retry") {
			targetLine = i
		}
	}
	if targetLine < 0 {
		t.Fatal("test setup: could not find the table row containing \"Retry\"")
	}
	if targetLine < 2 {
		t.Fatal("test setup: target row must sit behind at least one ANSI-styled row for this to be a meaningful regression check")
	}

	plain := ansi.Strip(m.transcriptRows[targetLine])
	startCol := strings.Index(plain, "Retry")
	endCol := startCol + len("Retry")

	m.transcriptSelectionAnchorLine, m.transcriptSelectionAnchorCol = targetLine, startCol
	m.transcriptSelectionHeadLine, m.transcriptSelectionHeadCol = targetLine, endCol
	m.applyTranscriptHighlight()

	lines := strings.Split(m.transcriptViewport.GetContent(), "\n")
	if targetLine >= len(lines) {
		t.Fatalf("target line %d out of range of %d rendered lines", targetLine, len(lines))
	}
	got := lines[targetLine]
	if ansi.Strip(got) != plain {
		t.Fatalf("highlighting must not change the target row's visible text:\nwant %q\ngot  %q", plain, ansi.Strip(got))
	}
	if !strings.Contains(got, "\x1b[7") {
		t.Errorf("target row (containing %q) should carry a reverse-video escape after highlighting; got %q", "Retry", got)
	}

	// And no OTHER row should have picked up the highlight instead.
	for i, l := range lines {
		if i == targetLine {
			continue
		}
		if strings.Contains(l, "\x1b[7") {
			t.Errorf("row %d unexpectedly carries the highlight (want only row %d): %q", i, targetLine, l)
		}
	}
}
