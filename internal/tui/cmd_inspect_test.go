package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/session"
)

// TestResolveInspectSession_NoRefUsesLive confirms an empty ref targets the
// live in-memory session directly, without touching disk.
func TestResolveInspectSession_NoRefUsesLive(t *testing.T) {
	live := &session.Session{ID: "live-session"}
	got, err := resolveInspectSession(live, "")
	if err != nil {
		t.Fatalf("resolveInspectSession: %v", err)
	}
	if got != live {
		t.Errorf("expected the live session back, got a different pointer")
	}
}

// TestResolveInspectSession_NoRefNoLiveErrors confirms a nil live session
// with no ref is a clear error, not a nil-pointer panic downstream.
func TestResolveInspectSession_NoRefNoLiveErrors(t *testing.T) {
	if _, err := resolveInspectSession(nil, ""); err == nil {
		t.Error("expected an error with no live session and no ref")
	}
}

// TestResolveInspectSession_ExactIDAndPrefix covers the two ways a past
// session can be referenced: the exact id (session.Load's own match) and a
// unique prefix of it (the short id /usage's today rollup now prints,
// which session.Load alone can't resolve).
func TestResolveInspectSession_ExactIDAndPrefix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	other, err := session.New("m", "/proj")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	// session.List (used for prefix resolution) skips sessions with no
	// user/assistant exchange — a bare AddUsage isn't enough.
	other.Messages = append(other.Messages, adapter.Message{Role: adapter.RoleUser, Content: "hi"})
	other.AddUsage("m", &adapter.Usage{InputTokens: 10})
	if err := other.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	t.Run("exact id", func(t *testing.T) {
		got, err := resolveInspectSession(nil, other.ID)
		if err != nil {
			t.Fatalf("resolveInspectSession: %v", err)
		}
		if got.ID != other.ID {
			t.Errorf("got session %q, want %q", got.ID, other.ID)
		}
	})

	t.Run("unique prefix", func(t *testing.T) {
		got, err := resolveInspectSession(nil, other.ID[:8])
		if err != nil {
			t.Fatalf("resolveInspectSession: %v", err)
		}
		if got.ID != other.ID {
			t.Errorf("got session %q, want %q", got.ID, other.ID)
		}
	})
}

// TestResolveInspectSession_NoMatchErrors confirms a ref matching nothing
// on disk is a clear error rather than a nil session downstream.
func TestResolveInspectSession_NoMatchErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := resolveInspectSession(nil, "nonexistent-ref"); err == nil {
		t.Error("expected an error for a ref matching no session")
	}
}

// TestResolveInspectSession_AmbiguousPrefixErrors confirms a prefix
// matching more than one session refuses to guess rather than silently
// picking one.
func TestResolveInspectSession_AmbiguousPrefixErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Two sessions that happen to share a first character is enough to
	// force an ambiguous single-character prefix.
	for range 2 {
		s, err := session.New("m", "/proj")
		if err != nil {
			t.Fatalf("session.New: %v", err)
		}
		s.Messages = append(s.Messages, adapter.Message{Role: adapter.RoleUser, Content: "hi"})
		s.AddUsage("m", &adapter.Usage{InputTokens: 10})
		if err := s.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	infos, err := session.List()
	if err != nil || len(infos) < 2 {
		t.Fatalf("expected at least 2 sessions on disk, got %d (err=%v)", len(infos), err)
	}
	// Every session id in this store shares the same first character (see
	// session.New's id scheme — a leading timestamp), so a 1-char prefix
	// is ambiguous across any two of them.
	prefix := infos[0].ID[:1]
	if _, err := resolveInspectSession(nil, prefix); err == nil {
		t.Errorf("expected an ambiguous-prefix error for %q matching %d sessions", prefix, len(infos))
	}
}

// TestBuildInspectTurns_GroupsMessagesAndTracksStatus is the core
// regression: turns are grouped by RoleAssistant message, the preceding
// RoleUser content attaches to the right turn, a tool call's status
// updates from its matching RoleTool result by ToolCallID (not position),
// the repeated-failure guard marker is distinguished from an ordinary
// error, and the low-signal flag matches the same thresholds /usage's
// efficiency section uses.
func TestBuildInspectTurns_GroupsMessagesAndTracksStatus(t *testing.T) {
	s := &session.Session{
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "please check the build"},
			{Role: adapter.RoleAssistant, Content: "Checking now.", Usage: &adapter.Usage{InputTokens: 26_000, OutputTokens: 10}, ToolCalls: []adapter.ToolCall{
				{ID: "call1", Name: "run_tests", ArgsJSON: `{}`},
				{ID: "call2", Name: "grep", ArgsJSON: `{"pattern":"FAIL"}`},
			}},
			{Role: adapter.RoleTool, ToolCallID: "call1", Content: "ok: all tests passed"},
			{Role: adapter.RoleTool, ToolCallID: "call2", Content: "error: hunk mismatch\n\n" + agent.RepeatedToolFailureMarker + "3×): rebuild a valid unified diff"},
			{Role: adapter.RoleUser, Content: "now check lint"},
			{Role: adapter.RoleAssistant, Content: "Running lint.", Usage: &adapter.Usage{InputTokens: 500, OutputTokens: 300}, ToolCalls: []adapter.ToolCall{
				{ID: "call3", Name: "lint", ArgsJSON: `{}`},
			}},
			{Role: adapter.RoleTool, ToolCallID: "call3", Content: "error: 2 warnings"},
		},
	}
	turns := buildInspectTurns(s)
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}

	t1 := turns[0]
	if t1.n != 1 || t1.userPreview != "please check the build" || t1.assistant != "Checking now." {
		t.Errorf("turn 1 shape wrong: %+v", t1)
	}
	if !t1.lowSignal {
		t.Errorf("turn 1 should be low-signal (26k input, 10 output); got %+v", t1.usage)
	}
	if len(t1.toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls in turn 1, got %d", len(t1.toolCalls))
	}
	if t1.toolCalls[0].status != "ok" {
		t.Errorf("call1 (run_tests) should be ok, got %q", t1.toolCalls[0].status)
	}
	if t1.toolCalls[1].status != "error — guidance fired" {
		t.Errorf("call2 (grep, guard marker present) should be flagged guidance-fired, got %q", t1.toolCalls[1].status)
	}

	t2 := turns[1]
	if t2.n != 2 || t2.userPreview != "now check lint" {
		t.Errorf("turn 2 shape wrong: %+v", t2)
	}
	if t2.lowSignal {
		t.Errorf("turn 2 should not be low-signal (500 input, 300 output); got %+v", t2.usage)
	}
	if len(t2.toolCalls) != 1 || t2.toolCalls[0].status != "error" {
		t.Errorf("call3 (lint, ordinary error, no guard marker) should be plain error, got %+v", t2.toolCalls)
	}
}

// TestRenderInspectPanel_ShowsSessionAndTurns locks the panel shape: a
// header naming the (truncated) session id and turn count, then each
// turn's content.
func TestRenderInspectPanel_ShowsSessionAndTurns(t *testing.T) {
	s := &session.Session{
		ID: "abcdefgh-1234",
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "hi"},
			{Role: adapter.RoleAssistant, Content: "hello", Usage: &adapter.Usage{InputTokens: 100, OutputTokens: 50}, ToolCalls: []adapter.ToolCall{
				{ID: "call1", Name: "read_file", ArgsJSON: `{"path":"a.txt"}`},
			}},
			{Role: adapter.RoleTool, ToolCallID: "call1", Content: "ok"},
		},
	}
	got := renderInspectPanel(s)
	for _, want := range []string{"abcdefgh", "1 turn", "turn 1", "hi", "hello", "read_file", "esc to close"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestRenderInspectPanel_EmptySession confirms an empty session renders an
// explanatory message instead of a blank or panicking panel.
func TestRenderInspectPanel_EmptySession(t *testing.T) {
	got := renderInspectPanel(&session.Session{ID: "empty-session"})
	if !strings.Contains(got, "no turns") {
		t.Errorf("expected a no-turns message; got %q", got)
	}
}

// TestCmdInspect_NoArgsOpensSessionPicker confirms bare /inspect is a TUI
// chooser now, rather than immediately opening the live session panel.
func TestCmdInspect_NoArgsOpensSessionPicker(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "hello", Usage: &adapter.Usage{InputTokens: 10, OutputTokens: 5}},
	)
	m, _ = cmdInspect(m, nil)
	if !m.inspectPickerOpen {
		t.Fatal("bare /inspect should open the session picker")
	}
	if m.inspectOpen {
		t.Fatal("bare /inspect should not immediately open the inspect overlay")
	}
	if len(m.inspectPicker.rows) == 0 || !m.inspectPicker.rows[0].live {
		t.Fatalf("first inspect picker row should be the live session: %+v", m.inspectPicker)
	}
}

func TestInspectPicker_EnterOpensSelectedSession(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "hello", Usage: &adapter.Usage{InputTokens: 10, OutputTokens: 5}},
	)
	m, _ = cmdInspect(m, nil)

	m, _ = m.updateInspectPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.inspectOpen {
		t.Fatal("enter on inspect picker should open the selected session")
	}
	if m.inspectPickerOpen {
		t.Fatal("opening a session should close the inspect picker")
	}
	if !strings.Contains(m.inspectPanel, "turn 1") {
		t.Errorf("expected inspected session content in panel:\n%s", m.inspectPanel)
	}
}

func TestInspectExport_WritesCurrentInspectedSession(t *testing.T) {
	m := newTestModel(t)
	m.cwd = t.TempDir()
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "hello", Usage: &adapter.Usage{InputTokens: 10, OutputTokens: 5}},
	)
	m.inspectSession = m.sess
	m.inspectOpen = true
	m.inspectPanel = renderInspectPanel(m.sess)

	m, _ = m.updateInspectPanel(tea.KeyPressMsg{Text: "e"})
	path := defaultInspectExportPath(m.cwd, m.sess)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected inspect export at %s: %v", path, err)
	}
	if !strings.Contains(string(body), "# yottacode session") || !strings.Contains(string(body), "hello") {
		t.Fatalf("exported markdown missing session content:\n%s", string(body))
	}
}

func TestInspectTypedSlashOpensPicker(t *testing.T) {
	m := newTestModel(t)
	m.textInput.SetValue("/inspect")
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.inspectPickerOpen {
		t.Fatal("typing /inspect and pressing Enter should open the inspect picker")
	}
	if m.inspectOpen {
		t.Fatal("typing /inspect should not open the current-session inspect panel directly")
	}
}

func TestInspectSlashPaletteSelectionOpensPicker(t *testing.T) {
	m := newTestModel(t)
	m.textInput.SetValue("/ins")
	m.paletteFiltered = []slashCommand{{Name: "inspect", Args: "[session-id]", Run: cmdInspect, PreservesTurn: true}}
	m.paletteOpen = true
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.inspectPickerOpen {
		t.Fatal("selecting /inspect from the slash palette should execute it when the args are optional")
	}
}

func TestInspectPickerPagesSessionRows(t *testing.T) {
	p := &inspectPickerState{}
	for i := range sessionsPageSize + 3 {
		p.rows = append(p.rows, inspectPickerRow{info: session.SessionInfo{ID: fmt.Sprintf("20260101-%06d.000000", i)}})
	}
	m := newTestModel(t)
	m.inspectPicker = p
	m.inspectPickerOpen = true

	m, _ = m.updateInspectPicker(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if got, want := m.inspectPicker.cursor, sessionsPageSize; got != want {
		t.Fatalf("cursor after page down = %d, want %d", got, want)
	}
	if m.inspectPicker.offset == 0 {
		t.Fatal("page down should advance the visible inspect page")
	}
	rendered := renderInspectPicker(m.inspectPicker, 120)
	if strings.Contains(rendered, "20260101-000000") {
		t.Fatalf("paged inspect picker should not render first-page rows:\n%s", rendered)
	}
	if !strings.Contains(rendered, "PgUp/PgDn page") {
		t.Fatalf("paged inspect picker should expose page controls:\n%s", rendered)
	}
}

// TestCmdInspect_ArgOpensOverlayForReferencedSession confirms the explicit-ref
// path still opens directly, preserving the old quick lookup behavior.
func TestCmdInspect_ArgOpensOverlayForReferencedSession(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "hello", Usage: &adapter.Usage{InputTokens: 10, OutputTokens: 5}},
	)
	m, _ = cmdInspect(m, []string{m.sess.ID})
	if !m.inspectOpen {
		t.Fatal("cmdInspect should open the inspect overlay")
	}
	if !strings.Contains(m.inspectPanel, "turn 1") {
		t.Errorf("expected the live session's turn in the panel:\n%s", m.inspectPanel)
	}
}

// TestCmdInspect_UnresolvableRefReportsErrorWithoutOpening confirms a bad
// ref surfaces a clear error line and does NOT open the popup — silently
// opening an empty inspector would be more confusing than an error.
func TestCmdInspect_UnresolvableRefReportsErrorWithoutOpening(t *testing.T) {
	m := newTestModel(t)
	beforeLines := len(m.historyLines)
	m, _ = cmdInspect(m, []string{"nonexistent-ref"})
	if m.inspectOpen {
		t.Error("cmdInspect should not open the overlay for an unresolvable ref")
	}
	if len(m.historyLines) <= beforeLines {
		t.Error("expected an error line appended to scrollback")
	}
}
