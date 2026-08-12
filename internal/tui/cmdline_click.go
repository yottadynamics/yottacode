package tui

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
)

// inputFrameOrigin returns the screen (x,y) of the input frame's own
// top-left corner (the "┌" of renderInputFrame's border) — the anchor
// resolveInputClick needs to convert a screen click into a row/col
// inside the frame. x is always 0: the input frame spans the full
// terminal width. In the conversation layout, y is the transcript
// viewport's height plus any above-cmdline footer rows. In the launch
// hero, y is the startup card block height plus the spacer above the
// cmdline. Keeping both paths here lets cmdline click-to-focus work
// before the first message as well as after conversation starts.
func (m Model) inputFrameOrigin() (x, y int) {
	if !m.enteredConversation {
		box := renderStartupBox(m.version, m.commit, m.dirty, m.startupTip(), m.welcomeCursor, m.width)
		// renderHero joins: leading blank, startup box, spacer, above-input rows,
		// input frame. The block is top-placed, so y is just the joined height above
		// the input frame.
		y = 1 + lipgloss.Height(box) + 1
		above := m.aboveInputRows()
		if len(above) > 0 {
			y += lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, above...))
		}
		return 0, y
	}
	footerHeight := lipgloss.Height(m.renderFooter())
	transcriptHeight := m.height - footerHeight
	above := m.footerPartsAboveInputFrame()
	aboveHeight := 0
	if len(above) > 0 {
		aboveHeight = lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, above...))
	}
	return 0, transcriptHeight + aboveHeight
}

// resolveInputClick converts a screen point into a (logicalLine, col)
// target inside the cmdline's textarea value — the inverse of
// renderInputBody's own wrap/window geometry, built from the exact same
// wrapInputRows/windowInputRows helpers so the two can never disagree
// about where a given row/column actually is. Empty input still resolves
// to (0,0) so clicks can focus the cmdline and trigger visual feedback.
// ok=false when the point falls outside the frame's interior (border,
// status bar, dock).
func (m Model) resolveInputClick(screenX, screenY int) (logicalLine, col int, ok bool) {
	val := m.textInput.Value()
	ox, oy := m.inputFrameOrigin()
	// renderInputFrame: row 0 is the top border, body rows start at 1;
	// column 0 is "│ " (border + one padding cell) — same +1/+2 offsets
	// bodyPoint uses for the popup box family.
	row := screenY - oy - 1
	bodyCol := screenX - ox - 2
	if row < 0 || bodyCol < 0 {
		return 0, 0, false
	}

	contentW := inputContentWidth(m.width)
	wrapW := contentW - inputPromptW
	if wrapW < 1 {
		wrapW = 1
	}
	if val == "" {
		if row == 0 {
			return 0, 0, true
		}
		return 0, 0, false
	}
	cursorLogicalRow := m.textInput.Line()
	info := m.textInput.LineInfo()
	cursorLogicalCol := info.StartColumn + info.ColumnOffset

	rows, cursorVisRow := wrapInputRows(val, wrapW, cursorLogicalRow, cursorLogicalCol)
	rows, _ = windowInputRows(rows, cursorVisRow, inputMaxRows)
	if row >= len(rows) {
		return 0, 0, false
	}
	r := rows[row]
	textCol := bodyCol - inputPromptW
	if textCol < 0 {
		textCol = 0
	}
	rs := []rune(r.text)
	if textCol > len(rs) {
		textCol = len(rs)
	}
	return r.logical, r.startChar + textCol, true
}

// setTextInputCursor moves ti's cursor to (targetLine, col). textarea.Model
// has no direct row-jump API — only CursorUp/CursorDown (which move by the
// textarea's own internal, possibly-wrapped visual row — not necessarily
// our custom wrapW's notion of a row) and SetCursorColumn (a logical
// column, clamped to the CURRENT logical row). So this walks
// CursorDown/CursorUp one step at a time until Line() reports the target
// logical row — guaranteed to converge, since each step eventually
// crosses into the next/previous logical line even when a line spans
// several internal-wrapped sub-rows — then sets the column. The
// iteration cap is generous headroom over any realistic cmdline draft,
// purely to guard a future textarea behavior change from spinning
// forever rather than a bound expected to bind in practice.
func setTextInputCursor(ti *textarea.Model, targetLine, col int) {
	const guard = 512
	for i := 0; i < guard && ti.Line() != targetLine; i++ {
		if ti.Line() < targetLine {
			ti.CursorDown()
		} else {
			ti.CursorUp()
		}
	}
	ti.SetCursorColumn(col)
}
