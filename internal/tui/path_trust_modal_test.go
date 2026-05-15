package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// Regression: while the path-trust elevation modal is up, the "1" /
// "2" / "3" / Esc keystrokes must reach its handler — NOT get
// swallowed by the mid-turn textarea path. The bug was a missing
// `!m.awaitingPathTrust` exception in the `m.turnActive &&
// !m.awaitingApproval` gate; the gate's `default` branch routed every
// key into the textarea, leaving the modal unresponsive until the
// process was killed.
//
// We arrange the post-elevation state by hand (turnActive, channels
// initialized, awaitingPathTrust set, pathTrustReq populated) rather
// than running a real turn because the path-trust event flow is
// integration-test territory and we just want to lock the input
// routing invariant down to one quick unit assertion.
func TestPathTrustModal_AcceptsHotkeyWhileTurnActive(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
		want agent.Decision
	}{
		{"allow-once-1", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}, agent.PathAllowOnce},
		{"trust-session-2", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}}, agent.PathTrustSession},
		{"reject-3", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}}, agent.Deny},
		{"reject-esc", tea.KeyMsg{Type: tea.KeyEsc}, agent.Deny},
		{"allow-once-o", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}, agent.PathAllowOnce},
		{"trust-session-t", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}, agent.PathTrustSession},
		{"reject-n", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}, agent.Deny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			// Mirror startTurn's channel setup; the path-trust answer
			// path returns waitForEvent(m.eventsCh, m.turnErrCh) so both
			// must be non-nil.
			m.eventsCh = make(chan agent.Event, 1)
			m.turnErrCh = make(chan error, 1)
			m.decisions = make(chan agent.Decision, 1)
			m.turnActive = true
			m.awaitingPathTrust = true
			m.pathTrustReq = agent.PathTrustElevationNeeded{Path: "/tmp/outside.md"}

			m, _ = applyMsg(m, tc.key)

			if m.awaitingPathTrust {
				t.Errorf("awaitingPathTrust should clear after answer; the modal was swallowed by another input handler")
			}
			select {
			case got := <-m.decisions:
				if got != tc.want {
					t.Errorf("decisions got %v, want %v", got, tc.want)
				}
			default:
				t.Errorf("nothing on decisions channel — handler never fired")
			}
		})
	}
}
