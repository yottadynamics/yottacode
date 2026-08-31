package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// Approval modals are safety prompts, so mouse clicks must not resolve to a
// decision. Keyboard input remains the only approval path.
func TestApprovalModal_ClickHotkeyIsIgnored(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m.awaitingApproval = true
	m.approvalTool = "write_file"

	hits := &pickerHits{}
	box := renderApprovalModal(m, hits)
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitHotkey && r.Key == "n" {
			x, y, found = ox+2+r.ColStart, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the [N] hotkey")
	}

	m, cmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if cmd != nil {
		t.Fatalf("clicking [N] should not resume the event pump")
	}
	if !m.awaitingApproval {
		t.Errorf("clicking [N] should leave awaitingApproval true")
	}
	select {
	case d := <-m.decisions:
		t.Errorf("mouse click should not send a decision; got %v", d)
	default:
		// Expected: approval decisions are keyboard-only.
	}
}

func TestApprovalModal_ClickOutsideHotkeysIsNoop(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m.awaitingApproval = true
	m.approvalTool = "write_file"

	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if !m.awaitingApproval {
		t.Errorf("a click outside any hotkey token should not resolve to a decision")
	}
}

func TestApprovalModal_ClickCloseIsIgnored(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m.awaitingApproval = true
	m.approvalTool = "write_file"

	box := renderApprovalModal(m)
	ox, oy := m.popupOrigin(box)
	w := lipgloss.Width(box)
	m, cmd := applyMsg(m, tea.MouseClickMsg{X: ox + w - 2, Y: oy})
	if cmd != nil {
		t.Fatalf("clicking × should not resume the event pump")
	}
	if !m.awaitingApproval {
		t.Errorf("clicking × should leave awaitingApproval true")
	}
	select {
	case d := <-m.decisions:
		t.Errorf("mouse close should not send a decision; got %v", d)
	default:
		// Expected: approval decisions are keyboard-only.
	}
}
