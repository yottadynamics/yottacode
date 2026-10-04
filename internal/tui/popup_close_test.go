package tui

import (
	"strings"
	"testing"
)

func TestPopupBoxHasNoCloseGlyph(t *testing.T) {
	box := popupBox(renderMenuHeader("Help", "esc to close", 40))
	lines := strings.Split(stripANSI(box), "\n")
	if len(lines) == 0 {
		t.Fatal("popup rendered no lines")
	}
	if strings.Contains(lines[0], "×") {
		t.Fatalf("popup top border should have no close glyph: %q", lines[0])
	}
}

func TestTurnEndedClearsPendingToolAndTitleIcon(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.pendingToolName = "pr_watch_checks"
	m, _ = applyMsg(m, turnEndedMsg{})
	if m.pendingToolName != "" {
		t.Fatalf("pendingToolName = %q after turn end", m.pendingToolName)
	}
	if got := m.terminalTitleIcon(); got != "○" {
		t.Fatalf("title icon = %q, want idle ○", got)
	}
}
