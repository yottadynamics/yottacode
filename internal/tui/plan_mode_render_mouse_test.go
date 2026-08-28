package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func TestPlanApprovalCardsHaveNoPopupCloseGlyph(t *testing.T) {
	for _, card := range []struct {
		name   string
		render func(int) string
	}{
		{"exit", func(width int) string { return renderPlanApprovalCard(width) }},
		{"enter", func(width int) string { return renderEnterPlanApprovalCard(width) }},
	} {
		t.Run(card.name, func(t *testing.T) {
			first := strings.SplitN(stripANSI(card.render(80)), "\n", 2)[0]
			if strings.Contains(first, "×") {
				t.Fatalf("plan approval top border should not include a mouse-only close glyph: %q", first)
			}
		})
	}
}

func TestExitPlanApprovalCard_ClickKeepPlanningIsIgnored(t *testing.T) {
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
	if cmd != nil {
		t.Fatalf("clicking [K] should not resume the event pump")
	}
	if !m.awaitingApproval {
		t.Errorf("clicking [K] should leave awaitingApproval true")
	}
	select {
	case d := <-m.decisions:
		t.Errorf("mouse click should not send a decision; got %v", d)
	default:
		// Expected: plan approvals are keyboard-only.
	}
}

func TestEnterPlanApprovalCard_ClickYesIsIgnored(t *testing.T) {
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
	if cmd != nil {
		t.Fatalf("clicking [Y] should not resume the event pump")
	}
	if !m.awaitingApproval {
		t.Errorf("clicking [Y] should leave awaitingApproval true")
	}
	select {
	case d := <-m.decisions:
		t.Errorf("mouse click should not send a decision; got %v", d)
	default:
		// Expected: plan approvals are keyboard-only.
	}
}
