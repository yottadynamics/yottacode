package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/agent"
)

func TestPathTrustModal_ClickTrustSessionRow(t *testing.T) {
	m := newTestModel(t)
	m.eventsCh = make(chan agent.Event, 1)
	m.turnErrCh = make(chan error, 1)
	m.decisions = make(chan agent.Decision, 1)
	m.turnActive = true
	m.awaitingPathTrust = true
	m.pathTrustReq = agent.PathTrustElevationNeeded{Path: "/tmp/outside.md"}

	hits := &pickerHits{}
	box := renderPathTrustModal(m, hits)
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitHotkey && r.Key == "2" {
			x, y, found = ox+2+r.ColStart, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the [2] trust-session hotkey")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.awaitingPathTrust {
		t.Errorf("clicking [2] should clear awaitingPathTrust, like the keyboard hotkey does")
	}
	select {
	case got := <-m.decisions:
		if got != agent.PathTrustSession {
			t.Errorf("decisions got %v, want PathTrustSession", got)
		}
	default:
		t.Error("nothing on decisions channel — click was swallowed")
	}
}
