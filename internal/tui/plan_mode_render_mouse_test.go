package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func TestExitPlanApprovalCard_ClickKeepPlanning(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m.awaitingApproval = true
	m.approvalTool = "exit_plan_mode"

	hits := &pickerHits{}
	box := renderPlanApprovalCard(m.width, hits)
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitHotkey && r.Key == "k" {
			x, y, found = ox+2+r.ColStart, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the [K] keep-planning hotkey")
	}

	m, cmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if cmd == nil {
		t.Fatalf("clicking [K] must return a Cmd that resumes the event pump")
	}
	if m.awaitingApproval {
		t.Errorf("clicking [K] should clear awaitingApproval")
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

func TestEnterPlanApprovalCard_ClickYes(t *testing.T) {
	m := newTestModel(t)
	m.turnActive = true
	m.eventsCh = make(chan agent.Event, 4)
	m.decisions = make(chan agent.Decision, 1)
	m.turnErrCh = make(chan error, 1)
	m.awaitingApproval = true
	m.approvalTool = "enter_plan_mode"

	hits := &pickerHits{}
	box := renderEnterPlanApprovalCard(m.width, hits)
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitHotkey && r.Key == "y" {
			x, y, found = ox+2+r.ColStart, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the [Y] hotkey")
	}

	m, cmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if cmd == nil {
		t.Fatalf("clicking [Y] must return a Cmd that resumes the event pump")
	}
	if m.awaitingApproval {
		t.Errorf("clicking [Y] should clear awaitingApproval")
	}
	select {
	case d := <-m.decisions:
		if d != agent.AllowOnce {
			t.Errorf("decision = %v, want AllowOnce", d)
		}
	default:
		t.Errorf("expected a decision on the channel")
	}
}
