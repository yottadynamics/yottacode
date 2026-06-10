package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

// Regression: the path-trust styles were declared with var-block
// initializers carrying hardcoded 256-color indices, so the modal
// ignored the active theme and never rebuilt on ApplyTheme. They now
// live in buildStyles like every other late-bound style — verify they
// track the palette's semantic roles across a theme swap.
func TestPathTrustStyles_FollowTheme(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })

	for _, name := range []string{themes.DefaultName, "nord"} {
		if !ApplyTheme(name) {
			t.Fatalf("ApplyTheme(%q) reported unknown theme", name)
		}
		p, _ := themes.Get(name)
		checks := []struct {
			label string
			got   lipgloss.TerminalColor
			want  lipgloss.AdaptiveColor
		}{
			{"border", stylePathTrustBorder.GetBorderTopForeground(), p.Warning},
			{"title", stylePathTrustTitle.GetForeground(), p.Warning},
			{"hint", stylePathTrustBodyHint.GetForeground(), p.Dim},
			{"accept", stylePathTrustAccept.GetForeground(), p.Success},
			{"reject", stylePathTrustReject.GetForeground(), p.Error},
		}
		for _, c := range checks {
			if c.got != lipgloss.TerminalColor(c.want) {
				t.Errorf("theme %s: %s = %v, want palette role %v", name, c.label, c.got, c.want)
			}
		}
	}
}

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
