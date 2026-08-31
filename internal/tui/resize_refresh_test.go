package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestDispatchSlash_BatchesResizeRefreshWhenPopupOpens locks the gating
// rule: a command that leaves a popup open (here /help, which sets
// m.helpOpen and returns a nil Cmd of its own) gets refreshWindowSizeCmd
// batched in, so the returned Cmd is non-nil even though the command
// itself had nothing to run. Regression for a stale m.height leaving
// popups like /inspect overflowing the real terminal with no visible
// border or scroll hint — see refreshWindowSizeCmd's doc comment.
func TestDispatchSlash_BatchesResizeRefreshWhenPopupOpens(t *testing.T) {
	m := newTestModel(t)
	m, cmd := m.dispatchSlash("/help")
	if !m.helpOpen {
		t.Fatal("expected /help to open the help popup")
	}
	if cmd == nil {
		t.Error("expected a non-nil Cmd (refreshWindowSizeCmd) even though /help's own Cmd is nil")
	}
}

// TestDispatchSlash_NoResizeRefreshWithoutPopup confirms the gate actually
// gates: a command that leaves no popup open (here /quit) is NOT wrapped
// — its own Cmd passes through unchanged. Without this, /quit's Cmd would
// come back as a tea.BatchMsg instead of the tea.QuitMsg callers (and
// bubbletea's own runtime) expect directly.
func TestDispatchSlash_NoResizeRefreshWithoutPopup(t *testing.T) {
	m := newTestModel(t)
	_, cmd := m.dispatchSlash("/quit")
	if cmd == nil {
		t.Fatal("expected /quit to return its own Cmd")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("/quit's Cmd should be unwrapped (produce tea.QuitMsg directly), got %T", msg)
	}
}

// TestRefreshWindowSizeCmd_NeverPanics confirms the Cmd is safe to run
// even against a non-tty stdout (the normal case under `go test`, where
// term.GetSize errors) — it should degrade to a nil Msg, not panic.
func TestRefreshWindowSizeCmd_NeverPanics(t *testing.T) {
	msg := refreshWindowSizeCmd()()
	if msg != nil {
		if _, ok := msg.(tea.WindowSizeMsg); !ok {
			t.Errorf("expected nil or a tea.WindowSizeMsg, got %T", msg)
		}
	}
}
