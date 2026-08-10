package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// [Y] and [N] share one row in the hotkey grid — this pins that a click
// resolves to whichever bracket it's actually over, not just "the row."
func TestApprovalModal_ClickNoOnSharedRow(t *testing.T) {
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
	if cmd == nil {
		t.Fatalf("clicking [N] must return a Cmd that resumes the event pump")
	}
	if m.awaitingApproval {
		t.Errorf("clicking [N] should clear awaitingApproval")
	}
	select {
	case d := <-m.decisions:
		if d != agent.Deny {
			t.Errorf("decision = %v, want Deny", d)
		}
	default:
		t.Errorf("expected a decision on the channel")
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
