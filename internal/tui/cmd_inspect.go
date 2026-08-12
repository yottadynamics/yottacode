package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/session"
)

// inspectPickerRow is one selectable target in bare /inspect. The live row is
// kept first so the previous no-argument behavior is still one Enter away.
type inspectPickerRow struct {
	info session.SessionInfo
	live bool
}

type inspectPickerState struct {
	rows   []inspectPickerRow
	cursor int
	offset int
}

// inspectArgsPreviewChars and inspectTextPreviewChars bound how much of a
// tool call's arguments, and of a user/assistant message, /inspect shows
// per turn — this is a scan-the-sequence view, not a transcript replay.
const (
	inspectArgsPreviewChars = 40
	inspectTextPreviewChars = 90
)

// cmdInspect opens a scrollable, read-only turn-by-turn view of a session:
// what the user asked, what the assistant said, which tools ran with what
// arguments and outcome, and per-turn token cost — the "what did it
// actually do" answer /usage's aggregate tables can't give on their own.
// With no argument it opens a session picker with the live session first;
// passing an id/name/prefix still opens that session directly. The inspected
// session is never assigned to m.sess, so this can never accidentally switch the
// live conversation.
func cmdInspect(m Model, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		m.openInspectPicker()
		return m, nil
	}
	ref := args[0]
	s, err := resolveInspectSession(m.sess, ref)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[inspect] %v", err)))
		return m, nil
	}
	return m.openInspectSession(s), nil
}

// openInspectPicker builds the bare-/inspect chooser. It intentionally reuses
// the session list row renderer so /inspect and /sessions scan the same way.
func (m *Model) openInspectPicker() {
	rows := []inspectPickerRow{}
	if m.sess != nil {
		rows = append(rows, inspectPickerRow{info: session.SessionInfo{
			ID:       m.sess.ID,
			Name:     m.sess.Name,
			Model:    m.sess.Model,
			Created:  m.sess.Created,
			Messages: len(m.sess.Messages),
			Summary:  "current live session",
		}, live: true})
	}
	for _, info := range loadRecentSessions(true) {
		if m.sess != nil && info.ID == m.sess.ID {
			continue
		}
		rows = append(rows, inspectPickerRow{info: info})
	}
	if len(rows) == 0 {
		m.appendLine(styleAuto.Render("(no sessions to inspect yet)"))
		return
	}
	m.inspectPicker = &inspectPickerState{rows: rows}
	m.inspectPickerOpen = true
}

func (m Model) updateInspectPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.inspectPicker == nil {
		m.inspectPickerOpen = false
		return m, nil
	}
	p := m.inspectPicker
	switch msg.Code {
	case tea.KeyEsc:
		m.inspectPickerOpen = false
		m.inspectPicker = nil
		m.openSlashPalette()
		return m, nil
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		p.ensureCursorVisible()
		return m, nil
	case tea.KeyDown:
		if p.cursor < len(p.rows)-1 {
			p.cursor++
		}
		p.ensureCursorVisible()
		return m, nil
	case tea.KeyPgUp:
		p.cursor = max(0, p.cursor-sessionsPageSize)
		p.ensureCursorVisible()
		return m, nil
	case tea.KeyPgDown:
		p.cursor = min(len(p.rows)-1, p.cursor+sessionsPageSize)
		p.ensureCursorVisible()
		return m, nil
	case tea.KeyEnter:
		return m.commitInspectPick()
	}
	return m, nil
}

func (m Model) commitInspectPick() (Model, tea.Cmd) {
	p := m.inspectPicker
	if p == nil || p.cursor >= len(p.rows) {
		return m, nil
	}
	row := p.rows[p.cursor]
	m.inspectPickerOpen = false
	m.inspectPicker = nil
	if row.live {
		return m.openInspectSession(m.sess), nil
	}
	s, err := session.Load(row.info.ID)
	if err != nil {
		m.appendLine(styleError.Render(SysMsg(SysFailure, "inspect", "load failed", err.Error())))
		return m, nil
	}
	return m.openInspectSession(s), nil
}

func (m Model) openInspectSession(s *session.Session) Model {
	m.inspectSession = s
	m.inspectPanel = renderInspectPanel(s)
	m.inspectOpen = true
	m.inspectScrollOffset = 0
	return m
}

func (m Model) updateInspectPanel(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyUp:
		m.inspectScrollOffset = max(0, m.inspectScrollOffset-1)
		return m, nil
	case tea.KeyDown:
		m.inspectScrollOffset = min(m.inspectMaxScrollOffset(), m.inspectScrollOffset+1)
		return m, nil
	case tea.KeyPgUp:
		m.inspectScrollOffset = max(0, m.inspectScrollOffset-m.inspectVisibleLines())
		return m, nil
	case tea.KeyPgDown:
		m.inspectScrollOffset = min(m.inspectMaxScrollOffset(), m.inspectScrollOffset+m.inspectVisibleLines())
		return m, nil
	}
	if msg.Text == "e" {
		return m.exportInspectedSession()
	}
	m.inspectOpen = false
	m.inspectSession = nil
	m.inspectPanel = ""
	m.inspectScrollOffset = 0
	return m, nil
}

func (m Model) exportInspectedSession() (Model, tea.Cmd) {
	if m.inspectSession == nil {
		m.appendLine(styleError.Render(SysMsg(SysFailure, "inspect", "export failed", "no inspected session")))
		return m, nil
	}
	path := defaultInspectExportPath(m.cwd, m.inspectSession)
	md := session.ExportMarkdown(m.inspectSession)
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		m.appendLine(styleError.Render(SysMsg(SysFailure, "inspect", "export failed", err.Error())))
		return m, nil
	}
	m.appendLine(styleAuto.Render(SysMsg(SysSuccess, "inspect", "exported", fmt.Sprintf("%d bytes", len(md)), path)))
	return m, nil
}

func defaultInspectExportPath(cwd string, s *session.Session) string {
	if s == nil {
		return filepath.Join(cwd, "inspect-session.md")
	}
	return defaultExportPath(cwd, session.SessionInfo{ID: s.ID, Name: s.Name})
}

// resolveInspectSession finds the session /inspect should render. No ref
// targets the live in-memory session (so a running conversation can be
// inspected without a disk round-trip). A ref tries session.Load's exact
// id-or-name match first, then falls back to a unique id-prefix match
// against every saved session — without this fallback there'd be no way
// to act on the short id /usage's today rollup prints, since Load only
// accepts a full id.
func resolveInspectSession(live *session.Session, ref string) (*session.Session, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if live == nil {
			return nil, fmt.Errorf("no active session")
		}
		return live, nil
	}
	if live != nil && (ref == live.ID || (live.Name != "" && ref == live.Name)) {
		return live, nil
	}
	if s, err := session.Load(ref); err == nil {
		return s, nil
	}
	infos, err := session.List()
	if err != nil {
		return nil, err
	}
	var matches []session.SessionInfo
	for _, info := range infos {
		if strings.HasPrefix(info.ID, ref) {
			matches = append(matches, info)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no session matches %q", ref)
	case 1:
		return session.Load(matches[0].ID)
	default:
		return nil, fmt.Errorf("%q matches %d sessions — use a longer prefix", ref, len(matches))
	}
}

// inspectToolCallView is one tool call rendered inside a turn: its name, a
// truncated argument preview, and the outcome once the matching tool
// result lands ("ok" until then).
type inspectToolCallView struct {
	name   string
	args   string
	status string
}

// inspectTurnView is one assistant turn: the user message that preceded it
// (if any arrived since the last turn), the assistant's own text, its tool
// calls, per-turn usage, and the same low-signal flag /usage's efficiency
// section uses.
type inspectTurnView struct {
	n           int
	userPreview string
	usage       *adapter.Usage
	assistant   string
	toolCalls   []inspectToolCallView
	lowSignal   bool
}

// buildInspectTurns walks a session's messages once, in order, and groups
// them into turns: each RoleAssistant message starts a new turn and
// inherits any RoleUser content seen since the previous turn; each
// RoleTool result is matched back to the tool call that produced it (by
// ToolCallID) and updates that call's status in place — a duplicate of
// the small callID->location map /usage's retained-context and repeated-
// failure renderers each already build for the same reason.
func buildInspectTurns(s *session.Session) []inspectTurnView {
	if s == nil {
		return nil
	}
	var turns []inspectTurnView
	pendingUser := ""
	type callLoc struct{ turn, call int }
	loc := map[string]callLoc{}
	for _, msg := range s.Messages {
		switch msg.Role {
		case adapter.RoleUser:
			pendingUser = truncateForRender(msg.Content, inspectTextPreviewChars)
		case adapter.RoleAssistant:
			t := inspectTurnView{
				n:           len(turns) + 1,
				userPreview: pendingUser,
				usage:       msg.Usage,
				assistant:   truncateForRender(msg.Content, inspectTextPreviewChars),
			}
			pendingUser = ""
			if msg.Usage != nil && msg.Usage.InputTokens > lowSignalInputTokens && msg.Usage.OutputTokens < lowSignalOutputTokens {
				t.lowSignal = true
			}
			for _, call := range msg.ToolCalls {
				callIdx := len(t.toolCalls)
				t.toolCalls = append(t.toolCalls, inspectToolCallView{
					name:   call.Name,
					args:   truncateForRender(call.ArgsJSON, inspectArgsPreviewChars),
					status: "ok",
				})
				if call.ID != "" {
					loc[call.ID] = callLoc{turn: len(turns), call: callIdx}
				}
			}
			turns = append(turns, t)
		case adapter.RoleTool:
			at, ok := loc[msg.ToolCallID]
			if !ok {
				continue
			}
			status := "ok"
			if strings.HasPrefix(msg.Content, "error:") {
				status = "error"
				if strings.Contains(msg.Content, agent.RepeatedToolFailureMarker) {
					status = "error — guidance fired"
				}
			}
			turns[at.turn].toolCalls[at.call].status = status
		}
	}
	return turns
}

// renderInspectPanel is the full popup body for /inspect: a header naming
// the inspected session, then one block per turn. Built entirely from
// buildInspectTurns; self-describes rather than self-hiding (unlike the
// /usage sub-sections) since /inspect is only ever invoked explicitly.
func renderInspectPanel(s *session.Session) string {
	if s == nil {
		return styleEmpty.Render("session not found")
	}
	turns := buildInspectTurns(s)
	label := s.ID
	if len(label) > 8 {
		label = label[:8]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "inspect  session %s · %d %s\n\n", label, len(turns), usagePluralize("turn", len(turns)))
	if len(turns) == 0 {
		b.WriteString(styleEmpty.Render("no turns in this session yet"))
		b.WriteByte('\n')
	}
	for _, t := range turns {
		tokens := ""
		if t.usage != nil {
			tokens = fmt.Sprintf("  %s tokens (in %s · out %s)",
				formatTokens(int(totalTokensFor(*t.usage))), formatTokens(int(t.usage.InputTokens)), formatTokens(int(t.usage.OutputTokens)))
		}
		flag := ""
		if t.lowSignal {
			flag = "  " + styleNoticeWarn.Render("low-signal")
		}
		fmt.Fprintf(&b, "turn %d%s%s\n", t.n, tokens, flag)
		if t.userPreview != "" {
			fmt.Fprintf(&b, "  you        %s\n", t.userPreview)
		}
		if t.assistant != "" {
			fmt.Fprintf(&b, "  assistant  %s\n", t.assistant)
		}
		for _, c := range t.toolCalls {
			line := fmt.Sprintf("  %s(%s)", c.name, c.args)
			if c.status != "ok" {
				line = styleNoticeWarn.Render(fmt.Sprintf("%s  %s", line, c.status))
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	b.WriteString(styleHint.Render("e export · esc to close"))
	return strings.TrimRight(b.String(), "\n")
}

// inspectScrollReserve reserves one row for windowedInspectPanel's
// scroll-position hint line, mirroring usageScrollReserve.
const inspectScrollReserve = 1

// inspectVisibleLines and inspectFullFitLines mirror usageVisibleLines /
// usageFullFitLines: the popup has no height clamp of its own, so a long
// turn history needs to window rather than overflow the terminal.
func (m Model) inspectVisibleLines() int {
	return max(m.height-2-inspectScrollReserve, 1)
}

func (m Model) inspectFullFitLines() int {
	return max(m.height-2, 1)
}

// inspectMaxScrollOffset mirrors usageMaxScrollOffset.
func (m Model) inspectMaxScrollOffset() int {
	lines := strings.Count(m.inspectPanel, "\n") + 1
	if lines <= m.inspectFullFitLines() {
		return 0
	}
	return lines - m.inspectVisibleLines()
}

// windowedInspectPanel mirrors windowedUsagePanel.
func (m Model) windowedInspectPanel() string {
	if m.inspectPanel == "" {
		return m.inspectPanel
	}
	allLines := strings.Split(m.inspectPanel, "\n")
	total := len(allLines)
	if total <= m.inspectFullFitLines() {
		return m.inspectPanel
	}
	visible := m.inspectVisibleLines()
	offset := min(max(m.inspectScrollOffset, 0), total-visible)
	end := min(total, offset+visible)
	shown := strings.Join(allLines[offset:end], "\n")
	hint := fmt.Sprintf("── %d-%d of %d lines · wheel/click ↑↓ · PgUp/PgDn ──", offset+1, end, total)
	return shown + "\n" + styleHint.Render(hint)
}

func (p *inspectPickerState) ensureCursorVisible() {
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+sessionsPageSize {
		p.offset = p.cursor - sessionsPageSize + 1
	}
	maxOffset := max(len(p.rows)-sessionsPageSize, 0)
	p.offset = min(max(p.offset, 0), maxOffset)
}

func renderInspectPicker(p *inspectPickerState, width int, hits ...*pickerHits) string {
	var h *pickerHits
	if len(hits) > 0 {
		h = hits[0]
	}
	var b strings.Builder
	b.WriteString(renderMenuHeader("Inspect session", "Pick a recent session to inspect without resuming it. The live session is marked ✓.", width))
	b.WriteString("\n")
	if len(p.rows) == 0 {
		b.WriteString(styleEmpty.Render("  (no sessions to inspect yet)"))
	} else {
		infos := make([]session.SessionInfo, 0, len(p.rows))
		for _, row := range p.rows {
			infos = append(infos, row.info)
		}
		layout := sessionsRowLayout(infos, width)
		p.ensureCursorVisible()
		end := min(p.offset+sessionsPageSize, len(p.rows))
		for i := p.offset; i < end; i++ {
			row := p.rows[i]
			if h != nil {
				h.row(strings.Count(b.String(), "\n"), i)
			}
			label := sessionPickerLabel(row.info)
			if row.live {
				label = "Current live session"
			}
			b.WriteString(renderMenuItem(menuItemOpts{
				Label:      label,
				LabelWidth: sessionsLabelWidth,
				Desc:       sessionPickerDesc(row.info, layout),
				Cursor:     i == p.cursor,
				Checked:    row.live,
			}))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(styleFooter.Render("↵ inspect · esc cancel · ↑↓ navigate · PgUp/PgDn page"))
	return strings.TrimRight(b.String(), "\n")
}
