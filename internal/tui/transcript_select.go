package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// beginTranscriptSelection anchors a new drag at the clicked screen
// point (already known to be within the transcript region — mouse.go's
// handleMouseClick checked msg.Y before calling this).
func (m Model) beginTranscriptSelection(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	line, col, ok := m.screenToContentPoint(msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	m.transcriptSelecting = true
	m.transcriptSelectionAnchorLine, m.transcriptSelectionAnchorCol = line, col
	m.transcriptSelectionHeadLine, m.transcriptSelectionHeadCol = line, col
	m.applyTranscriptHighlight()
	return m, nil
}

// extendTranscriptSelection updates the drag's current end point and
// re-renders the live highlight. A motion point past the top/bottom
// edge of the transcript clamps to the nearest visible row rather than
// ending the drag — dragging past the edge to auto-scroll is an
// explicit non-goal for v1 (see the plan), but clamping still lets a
// drag that briefly overshoots the viewport keep working.
func (m Model) extendTranscriptSelection(msg tea.MouseMotionMsg) (Model, tea.Cmd) {
	line, col, ok := m.screenToContentPoint(msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	m.transcriptSelectionHeadLine, m.transcriptSelectionHeadCol = line, col
	m.applyTranscriptHighlight()
	return m, nil
}

// finalizeTranscriptSelection ends the drag and copies the selected
// plain text to the system clipboard via OSC 52 (tea.SetClipboard). The
// highlight itself is left visible — cleared on the next click or Esc —
// so the user can see what just got copied.
func (m Model) finalizeTranscriptSelection() (Model, tea.Cmd) {
	m.transcriptSelecting = false
	_, _, text, ok := m.transcriptSelectionByteRange()
	if !ok || text == "" {
		m.clearTranscriptSelection()
		return m, nil
	}
	return m, tea.SetClipboard(text)
}

// clearTranscriptSelection drops the selection and its highlight.
func (m *Model) clearTranscriptSelection() {
	m.transcriptSelecting = false
	m.transcriptSelectionAnchorLine, m.transcriptSelectionAnchorCol = 0, 0
	m.transcriptSelectionHeadLine, m.transcriptSelectionHeadCol = 0, 0
	m.transcriptViewport.SetContentLines(m.transcriptRows)
}

// screenToContentPoint converts a screen point inside the transcript
// viewport (Y already relative to the top of the frame — the
// transcript viewport is always the first element joined into the
// background, so no additional row offset applies) into a
// (contentLineIndex, runeCol) pair. contentLineIndex = YOffset()+Y is
// exact arithmetic: transcriptRows are pre-wrapped to width at append
// time (queuePrintlnIndented) and the viewport never soft-wraps or uses
// a left gutter, so one content line is always exactly one visible row.
func (m Model) screenToContentPoint(screenX, screenY int) (line, col int, ok bool) {
	line = m.transcriptViewport.YOffset() + screenY
	if line < 0 || line >= len(m.transcriptRows) {
		return 0, 0, false
	}
	col = displayColumnToRuneIndex(ansi.Strip(m.transcriptRows[line]), screenX)
	return line, col, true
}

// displayColumnToRuneIndex maps a terminal display column to the nearest rune
// boundary in plain text. Box-drawing tables and other wide glyph output are
// measured by display cells, not bytes/runes, so raw screen X cannot be used as
// a slice index without making table selection feel jumpy.
func displayColumnToRuneIndex(s string, target int) int {
	if target <= 0 {
		return 0
	}
	col := 0
	for i, r := range []rune(s) {
		w := ansi.StringWidth(string(r))
		if w < 1 {
			w = 1
		}
		if target < col+w {
			return i
		}
		col += w
	}
	return len([]rune(s))
}

func runeIndexToDisplayColumn(s string, target int) int {
	if target <= 0 {
		return 0
	}
	col := 0
	for i, r := range []rune(s) {
		if i >= target {
			break
		}
		w := ansi.StringWidth(string(r))
		if w < 1 {
			w = 1
		}
		col += w
	}
	return col
}

// normalizedTranscriptSelection returns the current anchor/head
// selection normalized to reading order (top-to-bottom, left-to-right
// within a line) regardless of which direction the user actually
// dragged. ok=false for an empty selection (a click with no drag).
func (m Model) normalizedTranscriptSelection() (aLine, aCol, hLine, hCol int, ok bool) {
	aLine, aCol = m.transcriptSelectionAnchorLine, m.transcriptSelectionAnchorCol
	hLine, hCol = m.transcriptSelectionHeadLine, m.transcriptSelectionHeadCol
	if hLine < aLine || (hLine == aLine && hCol < aCol) {
		aLine, aCol, hLine, hCol = hLine, hCol, aLine, aCol
	}
	if aLine == hLine && aCol == hCol {
		return 0, 0, 0, 0, false
	}
	return aLine, aCol, hLine, hCol, true
}

// applyTranscriptHighlight re-renders the live selection highlight by
// rebuilding the viewport's content with lipgloss.StyleRanges applied
// directly to the selected rows' own text, rather than through
// viewport.Model's built-in SetHighlights/ClearHighlights path.
//
// That built-in path (bubbles v2.1.1, viewport/highlight.go's
// parseMatches) walks byte offsets computed against
// ansi.Strip(content), but indexes its newline-boundary check
// (`content[bytePos] == '\n'`) into the ORIGINAL, non-stripped content
// string using bytePos values that only ever advance in STRIPPED-string
// units — correct by coincidence for plain unstyled text (where
// content == ansi.Strip(content)), but silently misattributes the
// highlight to an earlier line once any row the walk crosses is
// ANSI-styled, since raw and stripped byte lengths then diverge before
// the walk reaches its target. Confirmed via a throwaway repro: dragging
// across a glamour-rendered table's 4th data row highlighted line 1
// instead. No newer bubbles version exists to pick up a fix (v2.1.1 is
// current), so this bypasses the buggy call path entirely rather than
// waiting on it. Applying StyleRanges per selected row against ONLY
// that row's own stripped-text offsets never needs a cross-line walk in
// the first place.
func (m *Model) applyTranscriptHighlight() {
	aLine, aCol, hLine, hCol, ok := m.normalizedTranscriptSelection()
	if !ok {
		m.transcriptViewport.SetContentLines(m.transcriptRows)
		return
	}
	highlighted := make([]string, len(m.transcriptRows))
	copy(highlighted, m.transcriptRows)
	for i := aLine; i <= hLine && i < len(highlighted); i++ {
		plainLen := len([]rune(ansi.Strip(highlighted[i])))
		start, end := 0, plainLen
		if i == aLine {
			start = min(aCol, plainLen)
		}
		if i == hLine {
			end = min(hCol, plainLen)
		}
		if start >= end {
			continue
		}
			// lipgloss.StyleRanges' Range.Start/End are display-cell
			// positions. Convert the selected rune boundaries back to display
			// columns so table glyphs and wide characters highlight under the
			// same cells the mouse drag crossed.
			plain := ansi.Strip(highlighted[i])
			highlighted[i] = lipgloss.StyleRanges(highlighted[i], lipgloss.NewRange(runeIndexToDisplayColumn(plain, start), runeIndexToDisplayColumn(plain, end), styleTranscriptSelection))
		}
		m.transcriptViewport.SetContentLines(highlighted)

}

// transcriptSelectionByteRange returns the [start,end) byte range
// against a plain (ANSI-stripped) join of m.transcriptRows, plus the
// plain selected text, for the anchor/head range currently set on m.
// Used only for the clipboard-copy text on release — this pure
// stripped-text computation never touches the buggy raw/stripped byte
// walk applyTranscriptHighlight works around, so it doesn't need the
// same fix.
func (m Model) transcriptSelectionByteRange() (start, end int, text string, ok bool) {
	aLine, aCol, hLine, hCol, ok := m.normalizedTranscriptSelection()
	if !ok {
		return 0, 0, "", false
	}

	var b strings.Builder
	startByte, endByte := -1, -1
	for i, row := range m.transcriptRows {
		plain := ansi.Strip(row)
		runes := []rune(plain)
		lineStartByte := b.Len()
		if i == aLine {
			startByte = lineStartByte + len(string(runes[:min(aCol, len(runes))]))
		}
		if i == hLine {
			endByte = lineStartByte + len(string(runes[:min(hCol, len(runes))]))
		}
		b.WriteString(plain)
		if i < len(m.transcriptRows)-1 {
			b.WriteByte('\n')
		}
	}
	if startByte < 0 || endByte < 0 || startByte >= endByte {
		return 0, 0, "", false
	}
	full := b.String()
	return startByte, endByte, full[startByte:endByte], true
}
