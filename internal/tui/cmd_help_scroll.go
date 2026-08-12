package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// helpVisibleLines returns how many help-panel body lines fit inside the popup.
// It mirrors /usage scrolling so PgUp/PgDn can move through a tall command map
// instead of treating the first scroll key as a generic dismiss.
const helpPopupMaxWidth = 120

func (m Model) helpPopupWidth() int {
	w := m.width - 8
	if w > helpPopupMaxWidth {
		w = helpPopupMaxWidth
	}
	if w < 20 && m.width-4 > w {
		w = m.width - 4
	}
	if w < 1 {
		w = 1
	}
	return w
}

func (m Model) helpVisibleLines() int {
	// Help includes dense headers that can wrap inside the framed popup on narrow
	// or short terminals. Keep a two-row reserve for the scroll hint/wrapping so
	// the floating panel never extends past the terminal bottom.
	n := m.height - 2 - 2
	if n < 1 {
		n = 1
	}
	return n
}

func (m Model) helpFullFitLines() int {
	n := m.height - 2
	if n < 1 {
		n = 1
	}
	return n
}

func (m Model) helpMaxScrollOffset() int {
	lines := m.helpRenderedLines()
	if lines <= m.helpFullFitLines() {
		return 0
	}
	return lines - m.helpVisibleLines()
}

func (m Model) helpRenderedLines() int {
	if m.helpPanel == "" {
		return 0
	}
	return len(m.helpPanelRows())
}

func (m Model) helpPanelRows() []string {
	wrapWidth := m.helpPopupWidth()
	if wrapWidth < 1 {
		wrapWidth = 1
	}
	var rows []string
	for _, line := range strings.Split(m.helpPanel, "\n") {
		if ansi.StringWidth(line) <= wrapWidth {
			rows = append(rows, line)
			continue
		}
		wrapped := ansi.Wrap(line, wrapWidth, "")
		rows = append(rows, strings.Split(wrapped, "\n")...)
	}
	return rows
}

func (m Model) windowedHelpPanel() string {
	if m.helpPanel == "" {
		return m.helpPanel
	}
	allRows := m.helpPanelRows()
	total := len(allRows)
	if total <= m.helpFullFitLines() {
		return m.helpPanel
	}
	visible := m.helpVisibleLines()
	offset := min(max(m.helpScrollOffset, 0), total-visible)
	end := min(total, offset+visible)
	shown := strings.Join(allRows[offset:end], "\n")
	hint := fmt.Sprintf("── %d-%d of %d rows · wheel/click ↑↓ · PgUp/PgDn · End ──", offset+1, end, total)
	return shown + "\n" + styleHint.Render(hint)
}
