package tui

import (
	"fmt"
	"strings"
)

// helpVisibleLines returns how many help-panel body lines fit inside the popup.
// It mirrors /usage scrolling so PgUp/PgDn can move through a tall command map
// instead of treating the first scroll key as a generic dismiss.
func (m Model) helpVisibleLines() int {
	n := m.height - 2 - usageScrollReserve
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
	lines := strings.Count(m.helpPanel, "\n") + 1
	if lines <= m.helpFullFitLines() {
		return 0
	}
	return lines - m.helpVisibleLines()
}

func (m Model) windowedHelpPanel() string {
	if m.helpPanel == "" {
		return m.helpPanel
	}
	allLines := strings.Split(m.helpPanel, "\n")
	total := len(allLines)
	if total <= m.helpFullFitLines() {
		return m.helpPanel
	}
	visible := m.helpVisibleLines()
	offset := min(max(m.helpScrollOffset, 0), total-visible)
	end := min(total, offset+visible)
	shown := strings.Join(allLines[offset:end], "\n")
	hint := fmt.Sprintf("── %d-%d of %d lines · wheel/click ↑↓ · PgUp/PgDn · End ──", offset+1, end, total)
	return shown + "\n" + styleHint.Render(hint)
}
