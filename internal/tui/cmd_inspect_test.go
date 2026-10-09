package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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
// error (and stripped out of the error preview, since it's boilerplate
// guidance text rather than the actual failure), and the low-signal flag
// matches the same thresholds /usage's efficiency section uses.
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
	if t1.n != 1 || t1.user != "please check the build" || t1.assistant != "Checking now." {
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
	if want := "error: hunk mismatch"; t1.toolCalls[1].errorText != want {
		t.Errorf("call2 error preview should stop before the guard marker, got %q, want %q", t1.toolCalls[1].errorText, want)
	}

	t2 := turns[1]
	if t2.n != 2 || t2.user != "now check lint" {
		t.Errorf("turn 2 shape wrong: %+v", t2)
	}
	if t2.lowSignal {
		t.Errorf("turn 2 should not be low-signal (500 input, 300 output); got %+v", t2.usage)
	}
	if len(t2.toolCalls) != 1 || t2.toolCalls[0].status != "error" {
		t.Errorf("call3 (lint, ordinary error, no guard marker) should be plain error, got %+v", t2.toolCalls)
	}
	if want := "error: 2 warnings"; t2.toolCalls[0].errorText != want {
		t.Errorf("call3 error preview should be the full tool content, got %q, want %q", t2.toolCalls[0].errorText, want)
	}
}

// TestInspectStopFlag locks the keyword mapping from a provider's raw
// StopReason to /inspect's short label: silent for normal completions,
// "truncated" for a cut-off response, "filtered" for a safety/content
// intervention — covering the differently-spelled variants each adapter
// actually emits (see inspectStopFlag's doc comment).
func TestInspectStopFlag(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"", ""},
		{"end_turn", ""},
		{"stop", ""},
		{"tool_use", ""},
		{"tool_calls", ""},
		{"max_tokens", "truncated"},    // Anthropic
		{"MAX_TOKENS", "truncated"},    // Gemini
		{"length", "truncated"},        // OpenAI-compatible (copilot, ollama)
		{"incomplete", "truncated"},    // ChatGPT/Responses API
		{"refusal", "filtered"},        // Anthropic
		{"content_filter", "filtered"}, // OpenAI-compatible
		{"SAFETY", "filtered"},         // Gemini
		{"RECITATION", "filtered"},     // Gemini
		{"PROHIBITED_CONTENT", "filtered"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if got := inspectStopFlag(tc.reason); got != tc.want {
				t.Errorf("inspectStopFlag(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

// TestRenderInspectPanel_ShowsErrorPreviewAndStopFlag confirms both are
// actually wired into the rendered panel, not just correct in isolation:
// a failed tool call's line wraps to a second, indented line with the
// truncated failure content, and an abnormal StopReason renders as a
// turn-level flag alongside low-signal.
func TestRenderInspectPanel_ShowsErrorPreviewAndStopFlag(t *testing.T) {
	s := &session.Session{
		ID: "err-session",
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "run the vuln scan"},
			{Role: adapter.RoleAssistant, Content: "on it", StopReason: "max_tokens", ToolCalls: []adapter.ToolCall{
				{ID: "call1", Name: "run_tests", ArgsJSON: `{"command":"govulncheck"}`},
			}},
			{Role: adapter.RoleTool, ToolCallID: "call1", Content: "error: 3 vulnerabilities found"},
		},
	}
	got := ansi.Strip(renderInspectPanel(s))
	for _, want := range []string{"trunc", "run_tests ✗1", "✗ run_tests  turn 1", "3 vulnerabilities found"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
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
	for _, want := range []string{"abcdefgh", "1 turn", "turn", "100", "read_file", "input/turn"} {
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

// TestInspectDetailTokens_CacheAndReasoning locks the per-turn cache/
// reasoning clause: dropped entirely when usage carries neither, shows
// write and/or read when present, and reasoning trails after cache.
func TestInspectDetailTokens_CacheAndReasoning(t *testing.T) {
	cases := []struct {
		name string
		u    *adapter.Usage
		want string
	}{
		{"nil usage", nil, ""},
		{"plain usage", &adapter.Usage{InputTokens: 100, OutputTokens: 50}, ""},
		{"read only", &adapter.Usage{CacheReadTokens: 144_000}, "  cache 144K read"},
		{"write only", &adapter.Usage{CacheCreationTokens: 13_000}, "  cache 13K write"},
		{"write and read", &adapter.Usage{CacheCreationTokens: 13_000, CacheReadTokens: 141_000}, "  cache 13K write · 141K read"},
		{"reasoning only", &adapter.Usage{ReasoningTokens: 25}, "  reasoning 25"},
		{"cache and reasoning", &adapter.Usage{CacheReadTokens: 144_000, ReasoningTokens: 25}, "  cache 144K read · reasoning 25"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inspectDetailTokens(tc.u); got != tc.want {
				t.Errorf("inspectDetailTokens(%+v) = %q, want %q", tc.u, got, tc.want)
			}
		})
	}
}

// TestRenderInspectPanel_ShowsCacheDetail confirms the cache clause is
// actually wired into the rendered panel, not just correct in isolation.
func TestRenderInspectPanel_ShowsCacheDetail(t *testing.T) {
	s := &session.Session{
		ID: "cache-session",
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "go"},
			{Role: adapter.RoleAssistant, Content: "ok", Usage: &adapter.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 144_000}},
		},
	}
	got := renderInspectPanel(s)
	if !strings.Contains(got, "100%") {
		t.Errorf("expected cache metric in rendered panel:\n%s", got)
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
	if !strings.Contains(m.inspectPanel, "1 turn") {
		t.Errorf("expected inspected session content in panel:\n%s", m.inspectPanel)
	}
}

func TestInspectPanel_HomeEndScrollReachesFooter(t *testing.T) {
	m := newTestModel(t)
	m.height = 12
	m.inspectPanel = strings.Join([]string{"summary", "turn 1", "turn 2", "turn 3", "turn 4", "turn 5", "turn 6", "turn 7", "turn 8", "turn 9", "exports live under /sessions · esc to close"}, "\n")
	m.inspectOpen = true
	m, _ = m.updateInspectPanel(tea.KeyPressMsg{Code: tea.KeyEnd})
	shown := m.windowedInspectPanel()
	if !strings.Contains(shown, "turn 5") {
		t.Fatalf("End should reveal the inspect footer:\n%s", shown)
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
	if !strings.Contains(m.inspectPanel, "1 turn") {
		t.Errorf("expected the live session's turn in the panel:\n%s", m.inspectPanel)
	}
}

// inspectTestSession builds a session with a repeated run (turns 2-4), an
// error (turn 5), a sharp input drop (turn 6), and distinct tools.
func inspectTestSession() *session.Session {
	lat := func(v int64) *int64 { return &v }
	asst := func(content string, in, out int64, calls ...adapter.ToolCall) adapter.Message {
		return adapter.Message{Role: adapter.RoleAssistant, Content: content, Usage: &adapter.Usage{InputTokens: in, OutputTokens: out}, ToolCalls: calls}
	}
	call := func(id, name string) adapter.ToolCall {
		return adapter.ToolCall{ID: id, Name: name, ArgsJSON: `{"k":"` + strings.Repeat("v", 80) + `"}`, LatencyMS: lat(3000)}
	}
	return &session.Session{
		ID:               "view-session",
		CompactionEvents: []session.CompactionRecord{{Before: 100, After: 10}},
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "start the work"},
			asst("planning", 20_000, 500, call("a", "todo_write")),
			asst("g1", 40_000, 300, call("b", "grep")),
			asst("g2", 41_000, 300, call("c", "grep")),
			asst("g3", 42_000, 300, call("d", "grep")),
			asst("tests", 43_000, 300, call("e", "run_tests")),
			{Role: adapter.RoleTool, ToolCallID: "e", Content: "error: boom happened here"},
			asst("after compaction", 12_000, 300, call("f", "read_file")),
		},
	}
}

func TestInspectView_ToolSummaryWindowAndCompaction(t *testing.T) {
	vs := newInspectView(inspectTestSession(), 100_000)
	got := ansi.Strip(renderInspect(vs))
	for _, want := range []string{"grep", "run_tests", "3s", "peak 43K/100K (43%)", "1 compaction", "-31K", "showing 6 of 6 turns"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestInspectView_CollapseFoldsOnlyUnflaggedRepeats(t *testing.T) {
	vs := newInspectView(inspectTestSession(), 0)
	if n := len(vs.rows()); n != 6 {
		t.Fatalf("uncollapsed rows = %d, want 6", n)
	}
	vs.collapse = true
	rows := vs.rows()
	if len(rows) != 4 {
		t.Fatalf("collapsed rows = %d, want 4 (todo, grep×3, tests, read)", len(rows))
	}
	if len(rows[1].idx) != 3 {
		t.Errorf("grep run should fold 3 turns, got %v", rows[1].idx)
	}
	if !strings.Contains(ansi.Strip(renderInspect(vs)), "2-4 ×3") {
		t.Errorf("collapsed label missing")
	}
}

func TestInspectView_FlaggedAndSearchFilter(t *testing.T) {
	vs := newInspectView(inspectTestSession(), 0)
	vs.flagged = true
	if got := vs.visible(); len(got) != 1 || vs.turns[got[0]].n != 5 {
		t.Errorf("flagged filter should keep only the error turn, got %v", got)
	}
	vs.flagged = false
	vs.query = "GREP"
	if got := vs.visible(); len(got) != 3 {
		t.Errorf("case-insensitive tool search should match 3 turns, got %v", got)
	}
	vs.query = "after compaction"
	if got := vs.visible(); len(got) != 1 {
		t.Errorf("assistant-text search should match 1 turn, got %v", got)
	}
}

func TestInspectView_KeysNavigateDetailAndJump(t *testing.T) {
	m := newTestModel(t)
	m.height = 40
	m.width = 140
	m = m.openInspectSession(inspectTestSession())

	press := func(k tea.KeyPressMsg) { m, _ = m.updateInspectPanel(k) }
	char := func(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

	press(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.inspectView.cursor != 1 {
		t.Fatalf("down should move the cursor, got %d", m.inspectView.cursor)
	}
	press(char('e'))
	if m.inspectView.cursor != 4 {
		t.Fatalf("e should jump to the error turn (row 4), got %d", m.inspectView.cursor)
	}
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.inspectView.detail != 4 {
		t.Fatalf("enter should open turn index 4, got %d", m.inspectView.detail)
	}
	detail := ansi.Strip(m.inspectPanel)
	for _, want := range []string{"turn 5 of 6", "error: boom happened here", strings.Repeat("v", 80)} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q:\n%s", want, detail)
		}
	}
	press(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.inspectView.detail != 5 {
		t.Errorf("right should show the next turn, got %d", m.inspectView.detail)
	}
	press(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.inspectView.detail != -1 || !m.inspectOpen {
		t.Fatal("esc in detail should return to the list, not close")
	}

	press(char('f'))
	if !m.inspectView.flagged || len(m.inspectView.rows()) != 1 {
		t.Errorf("f should filter to the single flagged (error) turn: %d rows", len(m.inspectView.rows()))
	}
	press(char('f'))
	press(char('/'))
	press(char('g'))
	press(char('r'))
	if !m.inspectView.searching || m.inspectView.query != "gr" {
		t.Fatalf("search typing failed: %+v", m.inspectView)
	}
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.inspectView.searching || len(m.inspectView.rows()) != 3 {
		t.Errorf("enter should keep the query and filter rows, got %d", len(m.inspectView.rows()))
	}
	press(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.inspectOpen {
		t.Error("esc in the list should close the panel")
	}
	if m.inspectView != nil {
		t.Error("closing should drop the view state")
	}
}

func inspectLongSession(n int) *session.Session {
	s := &session.Session{ID: "long-session"}
	for i := range n {
		id := fmt.Sprintf("c%d", i)
		s.Messages = append(s.Messages, adapter.Message{
			Role: adapter.RoleAssistant, Content: "step",
			Usage:     &adapter.Usage{InputTokens: int64(1000 * (i + 1)), OutputTokens: 100},
			ToolCalls: []adapter.ToolCall{{ID: id, Name: fmt.Sprintf("tool_%d", i), ArgsJSON: "{}"}},
		})
	}
	return s
}

// TestInspectView_CursorStaysVisibleWhileScrolling drives the cursor through a
// session far taller than the popup, at a wide and a narrow terminal (where
// rows wrap), and checks the selected row is always on screen.
func TestInspectView_CursorStaysVisibleWhileScrolling(t *testing.T) {
	for _, width := range []int{140, 60} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			m := newTestModel(t)
			m.width, m.height = width, 14
			m = m.openInspectSession(inspectLongSession(60))
			press := func(k tea.KeyPressMsg) { m, _ = m.updateInspectPanel(k) }
			check := func(step string) {
				if shown := m.windowedInspectPanel(); !strings.Contains(shown, "▸") {
					t.Fatalf("%s: cursor row (row %d) scrolled off screen, offset %d:\n%s", step, m.inspectView.cursor, m.inspectScrollOffset, shown)
				}
			}
			// The view opens at the top so the summary header is seen first; on a
			// short terminal the first row may start below the fold until the
			// first key press scrolls the cursor into view.
			for i := range 59 {
				press(tea.KeyPressMsg{Code: tea.KeyDown})
				check(fmt.Sprintf("down %d", i))
			}
			if m.inspectView.cursor != 59 {
				t.Fatalf("cursor = %d, want 59", m.inspectView.cursor)
			}
			for i := range 59 {
				press(tea.KeyPressMsg{Code: tea.KeyUp})
				check(fmt.Sprintf("up %d", i))
			}
			press(tea.KeyPressMsg{Code: tea.KeyEnd})
			check("end")
			if m.inspectView.cursor != 59 {
				t.Errorf("End should select the last row, got %d", m.inspectView.cursor)
			}
			press(tea.KeyPressMsg{Code: tea.KeyPgUp})
			check("pgup")
			press(tea.KeyPressMsg{Code: tea.KeyHome})
			if m.inspectView.cursor != 0 {
				t.Errorf("Home should select the first row, got %d", m.inspectView.cursor)
			}
			if m.inspectScrollOffset != 0 {
				t.Errorf("Home should scroll back to the top, offset %d", m.inspectScrollOffset)
			}
		})
	}
}

// TestInspectView_NarrowWidthKeepsEveryRowReachable confirms wrapping at a
// narrow width never loses content: every turn's tool name is still present
// in the wrapped panel.
func TestInspectView_NarrowWidthKeepsEveryRowReachable(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 50, 40
	m = m.openInspectSession(inspectLongSession(10))
	rows := strings.Join(m.inspectVisualLines(), "\n")
	for i := range 10 {
		if want := fmt.Sprintf("tool_%d", i); !strings.Contains(ansi.Strip(rows), want) {
			t.Errorf("%s missing from narrow render", want)
		}
	}
}

func TestInspectInputStyled_ColorFollowsWindowFill(t *testing.T) {
	prefix := func(s string) string { return s[:strings.Index(s, "m")+1] }
	if got := inspectInputStyled("50K", 50, 0, 6); strings.Contains(got, "\x1b") || got != "   50K" {
		t.Errorf("unknown window should leave the figure plain and padded, got %q", got)
	}
	low, mid, high := inspectInputStyled("30K", 30, 100, 6), inspectInputStyled("70K", 70, 100, 6), inspectInputStyled("90K", 90, 100, 6)
	if prefix(low) == prefix(mid) || prefix(mid) == prefix(high) || prefix(low) == prefix(high) {
		t.Errorf("low/mid/high fill should use three distinct colors: %q %q %q", prefix(low), prefix(mid), prefix(high))
	}
	if got := ansi.StringWidth(high); got != 6 {
		t.Errorf("styled figure should keep its column width, got %d", got)
	}
}

func TestInspectDeltaText(t *testing.T) {
	prefix := func(s string) string {
		if i := strings.Index(s, "m"); strings.HasPrefix(s, "\x1b") && i >= 0 {
			return s[:i+1]
		}
		return ""
	}
	cases := []struct {
		prev, cur int64
		want      string
	}{
		{33_000, 33_400, "·"},
		{33_000, 32_500, "·"},
		{33_000, 34_000, "+1.0K"},
		{33_000, 118_000, "+85K"},
		{136_000, 45_000, "-91K"},
	}
	for _, tc := range cases {
		got := inspectDeltaText(tc.prev, tc.cur, 6)
		if ansi.StringWidth(got) != 6 || strings.TrimSpace(ansi.Strip(got)) != tc.want {
			t.Errorf("inspectDeltaText(%d, %d) = %q, want %q padded to 6", tc.prev, tc.cur, ansi.Strip(got), tc.want)
		}
	}
	plain := prefix(inspectDeltaText(33_000, 34_000, 6))
	jump := prefix(inspectDeltaText(33_000, 118_000, 6))
	drop := prefix(inspectDeltaText(136_000, 45_000, 6))
	if plain != "" || jump == "" || drop == "" || jump == drop {
		t.Errorf("only jumps and compaction drops should be colored, with different colors: plain=%q jump=%q drop=%q", plain, jump, drop)
	}
}

func TestRenderInspect_ShowsDeltaColumn(t *testing.T) {
	got := ansi.Strip(renderInspect(newInspectView(inspectTestSession(), 100_000)))
	for _, want := range []string{"Δin", "+20K", "-31K"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestInspectErrorSection_GroupsRepeatsAndStaysOutOfTheTable confirms a
// failure that repeats across turns is listed once with every turn number,
// distinct failures stay separate, and the turn rows themselves carry only a
// ✗N marker (no inline error text).
func TestInspectErrorSection_GroupsRepeatsAndStaysOutOfTheTable(t *testing.T) {
	failing := func(id, tool, msg string) []adapter.Message {
		return []adapter.Message{
			{Role: adapter.RoleAssistant, Content: "x", ToolCalls: []adapter.ToolCall{{ID: id, Name: tool, ArgsJSON: "{}"}}},
			{Role: adapter.RoleTool, ToolCallID: id, Content: "error: " + tool + ": " + msg},
		}
	}
	s := &session.Session{ID: "errs"}
	s.Messages = append(s.Messages, failing("a", "edit_file", "old_string not found")...)
	s.Messages = append(s.Messages, failing("b", "edit_file", "old_string not found")...)
	s.Messages = append(s.Messages, failing("c", "apply_diff", "malformed patch")...)

	vs := newInspectView(s, 0)
	groups := inspectErrorGroups(vs)
	if len(groups) != 2 {
		t.Fatalf("want 2 distinct failures, got %+v", groups)
	}
	if g := groups[0]; g.tool != "edit_file" || len(g.turns) != 2 || g.turns[0] != 1 || g.turns[1] != 2 {
		t.Errorf("repeated failure should list turns 1 and 2, got %+v", g)
	}

	out := ansi.Strip(renderInspect(vs))
	for _, want := range []string{"errors · 2 failures in 3 turns", "✗ edit_file  turn 1, 2", "✗ apply_diff  turn 3", "old_string not found"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "old_string not found") != 1 {
		t.Errorf("a repeated message should appear once, got:\n%s", out)
	}

	vs.query = "apply_diff"
	if got := inspectErrorGroups(vs); len(got) != 1 || got[0].tool != "apply_diff" {
		t.Errorf("error section should follow the active filter, got %+v", got)
	}
}

func TestRenderInspect_RowGutterMarksSelectableRows(t *testing.T) {
	out := ansi.Strip(renderInspect(newInspectView(inspectTestSession(), 0)))
	tools, turns, _ := strings.Cut(out, "showing")
	if got := strings.Count(turns, "▸ "); got != 1 {
		t.Errorf("exactly one turn row should be selected (▸), got %d", got)
	}
	if got := strings.Count(turns, "› "); got != 5 {
		t.Errorf("the other 5 turn rows should carry a › gutter hint, got %d in:\n%s", got, turns)
	}
	// The tool table shares the gutter, but ▸ only shows once it has focus.
	if strings.Contains(tools, "▸ ") || strings.Count(tools, "› ") != 4 {
		t.Errorf("tool rows should show › (4 tools) and no ▸ until focused:\n%s", tools)
	}
}

// TestInspectView_ToolTableSelectionFiltersTurns drives the tool table like
// the turn table: tab focuses it, ↑↓ select, ↵ filters turns to that tool
// (marked ●), ↵ on the same tool or Esc clears the filter.
func TestInspectView_ToolTableSelectionFiltersTurns(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 140, 60
	m = m.openInspectSession(inspectTestSession())
	press := func(k tea.KeyPressMsg) { m, _ = m.updateInspectPanel(k) }

	press(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.inspectView.focusTools {
		t.Fatal("tab should focus the tool table")
	}
	tools := ansi.Strip(m.inspectPanel)
	// Failing tools rank first, so run_tests (1 error) leads grep (3 calls).
	if !strings.Contains(tools, "▸ run_tests") {
		t.Fatalf("first tool (run_tests, failing) should be selected:\n%s", tools)
	}
	if strings.Count(strings.SplitN(tools, "showing", 2)[1], "▸ ") != 0 {
		t.Error("turn rows should not show ▸ while the tool table has focus")
	}

	press(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.inspectView.toolNames[m.inspectView.toolCursor]; got != "grep" {
		t.Fatalf("down should move to the next tool (grep), got %q", got)
	}
	press(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.inspectView.toolNames[m.inspectView.toolCursor]; got != "run_tests" {
		t.Fatalf("up should move back to run_tests, got %q", got)
	}
	press(tea.KeyPressMsg{Code: tea.KeyDown})
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	vs := m.inspectView
	if vs.toolFilter != "grep" || vs.focusTools {
		t.Fatalf("enter should filter to grep and return focus to turns: %+v", vs)
	}
	if got := len(vs.visible()); got != 3 {
		t.Errorf("grep filter should leave its 3 turns, got %d", got)
	}
	out := ansi.Strip(m.inspectPanel)
	if !strings.Contains(out, "showing 3 of 6 turns") || !strings.Contains(out, "tool: grep") || !strings.Contains(out, "● grep") {
		t.Errorf("filtered view should say so and mark the tool ●:\n%s", out)
	}

	press(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.inspectView.toolFilter != "" || !m.inspectOpen {
		t.Fatal("esc should clear the tool filter without closing the panel")
	}
	press(tea.KeyPressMsg{Code: tea.KeyTab})
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	press(tea.KeyPressMsg{Code: tea.KeyTab})
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.inspectView.toolFilter != "" {
		t.Errorf("enter on the active tool should toggle the filter off, got %q", m.inspectView.toolFilter)
	}
}

// TestInspectView_OtherKeysDropToolFocus confirms a turn-table key pressed
// while the tool table is focused is applied to the turns, not swallowed.
func TestInspectView_OtherKeysDropToolFocus(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 140, 60
	m = m.openInspectSession(inspectTestSession())
	m, _ = m.updateInspectPanel(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.updateInspectPanel(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if m.inspectView.focusTools || !m.inspectView.flagged {
		t.Errorf("f should drop tool focus and apply: %+v", m.inspectView)
	}
}

// TestRenderInspect_FlagColumnAndTurnWidthStayAligned covers the layout
// regressions from a real session: a long compaction/low-output note used to
// wrap off the right edge, and a collapsed range label wider than the turn
// column pushed the others out of line.
func TestRenderInspect_FlagColumnAndTurnWidthStayAligned(t *testing.T) {
	s := inspectTestSession()
	// A low-output turn (huge input, tiny output) to exercise the flag column.
	s.Messages = append(s.Messages, adapter.Message{
		Role: adapter.RoleAssistant, Content: "ok",
		Usage:     &adapter.Usage{InputTokens: 60_000, OutputTokens: 5},
		ToolCalls: []adapter.ToolCall{{ID: "z", Name: "lsp_status", ArgsJSON: "{}"}},
	})
	for _, collapse := range []bool{false, true} {
		vs := newInspectView(s, 100_000)
		vs.collapse = collapse
		lines := strings.Split(ansi.Strip(renderInspect(vs)), "\n")

		header, flagged := "", ""
		for _, l := range lines {
			switch {
			case strings.Contains(l, "turn") && strings.Contains(l, "flag"):
				header = l
			case strings.Contains(l, "lsp_status") && strings.Contains(l, "low-out"):
				flagged = l
			}
		}
		if header == "" || flagged == "" {
			t.Fatalf("collapse=%v: missing header or low-out row in:\n%s", collapse, strings.Join(lines, "\n"))
		}
		// The tools text starts in the same column on every row, including the
		// collapsed "2-4 ×3" row whose label is wider than a single turn number,
		// and the flag lands in the flag column rather than trailing off the end.
		// Display columns, not byte offsets: ›, ▸ and Δ are multi-byte.
		col := func(l, sub string) int { return ansi.StringWidth(l[:strings.Index(l, sub)]) }
		toolsCol := col(header, "tools")
		checked := 0
		seenHeader := false
		for _, l := range lines {
			if l == header {
				seenHeader = true
				continue
			}
			if !seenHeader || strings.HasPrefix(strings.TrimSpace(l), "errors") || strings.HasPrefix(strings.TrimSpace(l), "✗") {
				continue
			}
			for _, name := range []string{"todo_write", "grep", "run_tests", "read_file", "lsp_status"} {
				if strings.Contains(l, name) {
					checked++
					if got := col(l, name); got != toolsCol {
						t.Errorf("collapse=%v: %q starts at col %d, header's tools column is %d:\n%s", collapse, name, got, toolsCol, l)
					}
				}
			}
		}
		if checked < 5 {
			t.Errorf("collapse=%v: only checked %d rows, expected every turn row", collapse, checked)
		}
		if got, want := col(flagged, "low-out"), col(header, "flag"); got != want {
			t.Errorf("collapse=%v: low-out at col %d, header's flag column is %d", collapse, got, want)
		}
		for _, l := range lines {
			if strings.Contains(l, "context -") {
				t.Errorf("rows should not carry a long trailing note any more: %q", l)
			}
		}
	}
}

// TestInspectToolTable_FailingToolsRankFirstAndTailIsSummarized builds more
// tools than the table shows, with the only failing one being the least
// used, and checks it still makes the cut and the tail is totaled.
func TestInspectToolTable_FailingToolsRankFirstAndTailIsSummarized(t *testing.T) {
	s := &session.Session{ID: "tools"}
	for i := range inspectToolRowsMax + 3 {
		for range 3 {
			id := fmt.Sprintf("c%d-%d", i, len(s.Messages))
			s.Messages = append(s.Messages, adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: id, Name: fmt.Sprintf("busy_%02d", i), ArgsJSON: "{}"}}})
		}
	}
	s.Messages = append(s.Messages,
		adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: "bad", Name: "rare_failer", ArgsJSON: "{}"}}},
		adapter.Message{Role: adapter.RoleTool, ToolCallID: "bad", Content: "error: nope"},
	)
	aggs := inspectToolAggregates(buildInspectTurns(s))
	if aggs[0].name != "rare_failer" {
		t.Fatalf("a failing tool should rank first, got %q", aggs[0].name)
	}
	out := ansi.Strip(renderInspect(newInspectView(s, 0)))
	if !strings.Contains(out, "rare_failer") {
		t.Errorf("failing tool should be in the capped table:\n%s", out)
	}
	// 12 tools total, 8 shown cap is inspectToolRowsMax → the rest are summarized.
	want := fmt.Sprintf("%d other tools · %d calls · 0 errors", len(aggs)-inspectToolRowsMax, 3*(len(aggs)-inspectToolRowsMax))
	if !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
}

func TestInspectToolTable_TKeyShowsEveryTool(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 140, 60
	m = m.openInspectSession(inspectLongSession(inspectToolRowsMax + 4))

	capped := ansi.Strip(m.inspectPanel)
	last := fmt.Sprintf("tool_%d", inspectToolRowsMax+3)
	if strings.Contains(strings.SplitN(capped, "showing", 2)[0], last) {
		t.Fatalf("capped table should not list %s before the turn table:\n%s", last, capped)
	}
	if !strings.Contains(capped, "4 other tools · 4 calls · 0 errors · ↵ show all") {
		t.Errorf("capped table should summarize the tail and point at t:\n%s", capped)
	}

	m, _ = m.updateInspectPanel(tea.KeyPressMsg{Code: 't', Text: "t"})
	full := ansi.Strip(m.inspectPanel)
	if !strings.Contains(strings.SplitN(full, "showing", 2)[0], last) {
		t.Errorf("t should list every tool in the table:\n%s", full)
	}
	if strings.Contains(full, "other tools") {
		t.Errorf("expanded table should drop the summary line:\n%s", full)
	}
	if m.inspectScrollOffset != 0 {
		t.Errorf("t should scroll to the table, offset %d", m.inspectScrollOffset)
	}

	m, _ = m.updateInspectPanel(tea.KeyPressMsg{Code: 't', Text: "t"})
	if !strings.Contains(ansi.Strip(m.inspectPanel), "↵ show all") {
		t.Error("t again should collapse back to the capped table")
	}
}

// TestInspectToolTable_ToggleRowExpandsAndCollapses covers the discoverable
// path: tab to the tool table, move onto the "N other tools" row, ↵ expands,
// and the same row (now "show top N only") collapses it again.
func TestInspectToolTable_ToggleRowExpandsAndCollapses(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 140, 80
	m = m.openInspectSession(inspectLongSession(inspectToolRowsMax + 4))
	press := func(k tea.KeyPressMsg) { m, _ = m.updateInspectPanel(k) }
	last := fmt.Sprintf("tool_%d", inspectToolRowsMax+3)
	tableOnly := func() string { return strings.SplitN(ansi.Strip(m.inspectPanel), "showing", 2)[0] }

	press(tea.KeyPressMsg{Code: tea.KeyTab})
	for range inspectToolRowsMax {
		press(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	vs := m.inspectView
	if vs.toolNames[vs.toolCursor] != inspectToolsToggle {
		t.Fatalf("cursor should be on the toggle row, on %q", vs.toolNames[vs.toolCursor])
	}
	if !strings.Contains(tableOnly(), "▸ 4 other tools") {
		t.Fatalf("toggle row should show the ▸ cursor:\n%s", tableOnly())
	}

	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.inspectView.allTools || !m.inspectView.focusTools {
		t.Fatalf("enter should expand and keep tool focus: %+v", m.inspectView)
	}
	if !strings.Contains(tableOnly(), last) || m.inspectView.toolFilter != "" {
		t.Errorf("expanded table should list %s without filtering turns:\n%s", last, tableOnly())
	}

	// Move to the trailing "show top N only" row and collapse.
	press(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !strings.Contains(tableOnly(), "▸ show top") {
		t.Fatalf("End should land on the collapse row:\n%s", tableOnly())
	}
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.inspectView.allTools || strings.Contains(tableOnly(), last) {
		t.Errorf("enter on the collapse row should shrink the table:\n%s", tableOnly())
	}
	if !strings.Contains(tableOnly(), "▸ 4 other tools") {
		t.Errorf("cursor should settle on the toggle row after collapsing:\n%s", tableOnly())
	}
}

func TestInspectMS_KeepsSubSecondPrecision(t *testing.T) {
	if got := inspectMS(240); got != "240ms" {
		t.Errorf("inspectMS(240) = %q, want 240ms", got)
	}
	if got := inspectMS(3000); got != "3s" {
		t.Errorf("inspectMS(3000) = %q, want 3s", got)
	}
}

func TestInspectErrorPreview_StripsRedundantPrefixes(t *testing.T) {
	cases := map[string]string{
		"error: edit_anchored: operation 2: stale anchor": "operation 2: stale anchor",
		"error: boom":   "boom",
		"plain failure": "plain failure",
	}
	for in, want := range cases {
		if got := inspectErrorPreview("edit_anchored", in); got != want {
			t.Errorf("inspectErrorPreview(%q) = %q, want %q", in, got, want)
		}
	}
	long := inspectErrorPreview("t", "error: "+strings.Repeat("x", 200))
	if n := len([]rune(long)); n > inspectErrPreviewChars {
		t.Errorf("preview should be capped at %d chars, got %d", inspectErrPreviewChars, n)
	}
}

func TestInspectSparklineAndBar(t *testing.T) {
	if got := inspectSparkline([]int64{0, 0}); got != "" {
		t.Errorf("all-zero sparkline should be empty, got %q", got)
	}
	if got := inspectSparkline([]int64{1, 8}); got != "▁█" {
		t.Errorf("sparkline = %q, want ▁█", got)
	}
	long := make([]int64, inspectSparkMax*3)
	long[len(long)-1] = 5
	if n := len([]rune(inspectSparkline(long))); n != inspectSparkMax {
		t.Errorf("long sparkline should bucket to %d cols, got %d", inspectSparkMax, n)
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
