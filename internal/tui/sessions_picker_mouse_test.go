package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
)

func TestSessionsPicker_ClickMenuItemTransitionsToLoadList(t *testing.T) {
	m := newTestModel(t)
	fixture, err := session.New("test-model", "/cwd")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	fixture.Messages = append(fixture.Messages, adapter.Message{Role: adapter.RoleUser, Content: "hi"})
	if err := fixture.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m.openSessionsPicker()

	var loadIdx int
	for i, item := range m.sessionsPicker.menuItems {
		if item.Label == "Load" {
			loadIdx = i
		}
	}

	hits := &pickerHits{}
	box := popupBox(renderSessionsPicker(m.sessionsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == loadIdx {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for the Load row")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.sessionsPicker.mode != sessionsLoadListMode {
		t.Errorf("clicking Load should transition to sessionsLoadListMode; got %v", m.sessionsPicker.mode)
	}
}

func TestSessionsPicker_ClickListRowLoadsAndCloses(t *testing.T) {
	m := newTestModel(t)
	fixture, err := session.New("test-model", "/cwd")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	fixture.Messages = append(fixture.Messages, adapter.Message{Role: adapter.RoleUser, Content: "hi"})
	if err := fixture.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m.sessionsPicker = &sessionsPickerState{
		mode:     sessionsLoadListMode,
		sessions: []session.SessionInfo{{ID: fixture.ID, Model: fixture.Model, Messages: 1}},
	}
	m.sessionsPickerOpen = true

	hits := &pickerHits{}
	box := popupBox(renderSessionsPicker(m.sessionsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == 0 {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for row 0")
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.sessionsPickerOpen {
		t.Errorf("clicking a session row should commit and close the picker, like Enter does")
	}
	if m.sess.ID != fixture.ID {
		t.Errorf("m.sess.ID = %q, want %q after click-load", m.sess.ID, fixture.ID)
	}
}
