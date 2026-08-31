package tui

import (
	"strings"
	"testing"

	"image/color"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2/compat"

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
			got   color.Color
			want  compat.AdaptiveColor
		}{
			{"title", stylePathTrustTitle.GetForeground(), p.Warning},
			{"hint", stylePathTrustBodyHint.GetForeground(), p.Dim},
			{"accept", stylePathTrustAccept.GetForeground(), p.Success},
			{"reject", stylePathTrustReject.GetForeground(), p.Error},
		}
		for _, c := range checks {
			if c.got != color.Color(c.want) {
				t.Errorf("theme %s: %s = %v, want palette role %v", name, c.label, c.got, c.want)
			}
		}
	}
}

func TestRenderPathTrustModal_UsesLabeledBox(t *testing.T) {
	m := newTestModel(t)
	m.width = 88
	m.pathTrustReq = agent.PathTrustElevationNeeded{
		ToolName:     "write_file",
		Path:         "/tmp/outside/file.txt",
		Cwd:          "/repo",
		AllowedRoots: []string{"/tmp/allowed"},
	}

	got := stripANSI(renderPathTrustModal(m))
	for _, want := range []string{
		"┌─ Write outside workspace",
		"write_file ─┐",
		"requested",
		"/tmp/outside/file.txt",
		"workspace",
		"/repo",
		"allowed roots",
		"/tmp/allowed",
		"[1] allow once",
		"[2] trust session",
		"[3] reject",
		"└",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("path trust modal missing %q\n%s", want, got)
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
		{"allow-once-1", tea.KeyPressMsg{Text: "1"}, agent.PathAllowOnce},
		{"trust-session-2", tea.KeyPressMsg{Text: "2"}, agent.PathTrustSession},
		{"reject-3", tea.KeyPressMsg{Text: "3"}, agent.Deny},
		{"reject-esc", tea.KeyPressMsg{Code: tea.KeyEsc}, agent.Deny},
		{"allow-once-o", tea.KeyPressMsg{Text: "o"}, agent.PathAllowOnce},
		{"trust-session-t", tea.KeyPressMsg{Text: "t"}, agent.PathTrustSession},
		{"reject-n", tea.KeyPressMsg{Text: "n"}, agent.Deny},
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

// TestAddAllowedPathToWriteTools_CoversCreateDocument is the regression
// for review finding #6: create_document was missing from both the
// tool-name list and the type-switch, so approving "allow once"/"trust
// session" in the path-trust modal never updated
// CreateDocumentTool.WriteOpts.AllowedPaths — the model's retry hit the
// identical rejection right after the UI said the path was trusted.
func TestAddAllowedPathToWriteTools_CoversCreateDocument(t *testing.T) {
	cwd := t.TempDir()
	cwdRef := agent.NewCwdRef(cwd)
	reg := agent.NewRegistry()
	tool := &agent.CreateDocumentTool{Cwd: cwdRef, WriteOpts: agent.WritePathOptions{Cwd: cwdRef}}
	reg.Register(tool)

	addAllowedPathToWriteTools(reg, "/outside/report.docx")

	found := false
	for _, p := range tool.WriteOpts.AllowedPaths {
		if p == "/outside/report.docx" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /outside/report.docx to be added to CreateDocumentTool.WriteOpts.AllowedPaths, got %v", tool.WriteOpts.AllowedPaths)
	}
}
