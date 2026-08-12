package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/session"
)

// sessionsPageSize is both the backend page size and the number of visible rows
// for /sessions and /inspect browsing. Fetching one page at a time keeps large
// session stores from blocking the picker while still giving PgUp/PgDn a simple
// mental model: one keypress equals one saved-session page.
const sessionsPageSize = 15

// Session-row layout. sessionsLabelWidth is the id/name column; the gist
// column takes what's left after the trailing metadata, clamped to
// [minSummaryWidth, maxSummaryWidth]. When that leaves less than the
// minimum, sessionsRowLayout first drops the model name from the metadata
// to buy the gist its column, and only gives up on the gist entirely when
// even that doesn't fit — a stub of six characters of prompt helps nobody.
const (
	sessionsLabelWidth = 28
	minSummaryWidth    = 16
	maxSummaryWidth    = 72
	summarySep         = "  ·  "
	// defaultPickerWidth stands in before the first WindowSizeMsg lands,
	// when m.width is still 0.
	defaultPickerWidth = 100
)

// sessionsPickerMode is the picker's screen. The state machine starts
// in sessionsMenuMode (Load/Resume/Rename/Export). Load drops into a
// recent-sessions list. Resume drops directly into a textinput that
// accepts an id-or-name reference (the same shape the cobra
// `yottacode sessions resume <ref>` subcommand takes). Rename and
// Export each drop into a list sub-picker, then a textinput. Esc pops
// back one level at every step.
type sessionsPickerMode int

const (
	sessionsMenuMode sessionsPickerMode = iota
	sessionsLoadListMode
	sessionsResumeInputMode
	sessionsRenameListMode
	sessionsRenameInputMode
	sessionsExportListMode
	sessionsExportInputMode
)

type sessionsMenuItem struct {
	Label    string
	Subtitle string
	Action   sessionsPickerMode
}

// sessionsPickerState holds the in-flight overlay. Lifetime is one
// /sessions invocation; closed on Esc from the menu or after a
// successful Resume/Rename/Export.
type sessionsPickerState struct {
	mode sessionsPickerMode

	menuItems  []sessionsMenuItem
	menuCursor int

	// sessions is a metadata snapshot captured each time the picker
	// enters a list mode. We refresh on every transition rather than
	// only on open so a freshly-renamed entry shows up if the user
	// goes Rename → menu → Resume in the same picker session.
	sessions            []session.SessionInfo
	listCursor          int
	listOffset          int
	listPage            int
	listHasPrev         bool
	listHasNext         bool
	listIncludeArchived bool

	// picked is the row the user confirmed in a list sub-picker;
	// rename/export forms read it to know which session to mutate.
	picked *session.SessionInfo

	// input is the textinput for both rename and export modes —
	// they share the focus/echo plumbing, only the prompt and
	// initial value differ. inputErr surfaces validation failures
	// (empty rename, unwritable export path) without dropping the
	// form.
	input    textinput.Model
	inputErr string

	// summarized toggles --summarized for both Load (list) and
	// Resume (ref-input). `s` flips it on the Load list; the Resume
	// input has a checkbox-style footer hint instead. Carries no
	// meaning outside those two modes.
	summarized bool

	// activeID is the running session id at the time the picker
	// opens. List rows matching it get a ✓ marker so the user can
	// see "this is the one I'm in right now."
	activeID string
}

// openSessionsPicker installs a fresh picker on m. Called by both
// cmdSessions (no args) and cmdSessions (positional shortcut after
// the shortcut path completes — the latter doesn't actually open the
// picker, but we keep the entry point unified for clarity).
func (m *Model) openSessionsPicker() {
	p := &sessionsPickerState{
		mode: sessionsMenuMode,
		menuItems: []sessionsMenuItem{
			{Label: "Load", Subtitle: "pick a session from the recent list", Action: sessionsLoadListMode},
			{Label: "Resume", Subtitle: "type a session id or name to resume directly", Action: sessionsResumeInputMode},
			{Label: "Rename", Subtitle: "label the current or another session for quick reference", Action: sessionsRenameListMode},
			{Label: "Export", Subtitle: "write a session out as a Markdown transcript", Action: sessionsExportListMode},
		},
		activeID: m.sess.ID,
	}
	m.sessionsPicker = p
	m.sessionsPickerOpen = true
}

// updateSessionsPicker handles keystrokes while the picker is the
// foreground modal. Returns the new model + any cmd to spawn.
func (m Model) updateSessionsPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.sessionsPicker == nil {
		m.sessionsPickerOpen = false
		return m, nil
	}
	p := m.sessionsPicker
	switch msg.Code {
	case tea.KeyEsc:
		switch p.mode {
		case sessionsMenuMode:
			m.sessionsPickerOpen = false
			m.sessionsPicker = nil
			m.openSlashPalette()
			return m, nil
		case sessionsRenameInputMode:
			p.mode = sessionsRenameListMode
			p.inputErr = ""
			return m, nil
		case sessionsExportInputMode:
			p.mode = sessionsExportListMode
			p.inputErr = ""
			return m, nil
		default:
			p.mode = sessionsMenuMode
			return m, nil
		}
	case tea.KeyUp:
		switch p.mode {
		case sessionsMenuMode:
			if p.menuCursor > 0 {
				p.menuCursor--
			}
		case sessionsLoadListMode, sessionsRenameListMode, sessionsExportListMode:
			if p.listCursor > 0 {
				p.listCursor--
			}
			p.ensureListCursorVisible()
		}
		return m, nil
	case tea.KeyDown:
		switch p.mode {
		case sessionsMenuMode:
			if p.menuCursor < len(p.menuItems)-1 {
				p.menuCursor++
			}
		case sessionsLoadListMode, sessionsRenameListMode, sessionsExportListMode:
			if p.listCursor < len(p.sessions)-1 {
				p.listCursor++
			}
			p.ensureListCursorVisible()
		}
		return m, nil
	case tea.KeyPgUp:
		switch p.mode {
		case sessionsLoadListMode, sessionsRenameListMode, sessionsExportListMode:
			if p.listCursor == 0 && p.listHasPrev {
				p.loadListPage(p.listIncludeArchived, p.listPage-1)
				p.listCursor = max(0, len(p.sessions)-1)
				p.ensureListCursorVisible()
				return m, nil
			}
			p.listCursor = max(0, p.listCursor-sessionsPageSize)
			p.ensureListCursorVisible()
		}
		return m, nil
	case tea.KeyPgDown:
		switch p.mode {
		case sessionsLoadListMode, sessionsRenameListMode, sessionsExportListMode:
			if p.listCursor >= len(p.sessions)-1 && p.listHasNext {
				p.loadListPage(p.listIncludeArchived, p.listPage+1)
				return m, nil
			}
			p.listCursor = min(len(p.sessions)-1, p.listCursor+sessionsPageSize)
			p.ensureListCursorVisible()
		}
		return m, nil
	case tea.KeyEnter:
		switch p.mode {
		case sessionsMenuMode:
			return m.dispatchSessionsMenu()
		case sessionsLoadListMode:
			return m.commitSessionsLoad()
		case sessionsResumeInputMode:
			return m.commitSessionsResume()
		case sessionsRenameListMode:
			return m.enterSessionsRenameInput()
		case sessionsRenameInputMode:
			return m.commitSessionsRename()
		case sessionsExportListMode:
			return m.enterSessionsExportInput()
		case sessionsExportInputMode:
			return m.commitSessionsExport()
		}
	}
	// `s` toggles --summarized in both Load (list) and Resume
	// (ref-input) modes. In Resume mode the textinput is focused, so
	// the keypress would normally type the rune; we intercept it
	// when there's no modifier so the user can still flip the toggle
	// without leaving the form.
	if msg.Text == "s" {
		if p.mode == sessionsLoadListMode {
			p.summarized = !p.summarized
			return m, nil
		}
	}
	// In input modes, forward to the textinput so typing fills it.
	if (p.mode == sessionsResumeInputMode || p.mode == sessionsRenameInputMode || p.mode == sessionsExportInputMode) && p.input.Focused() {
		// In Resume mode, Ctrl+S toggles --summarized. We use a control
		// chord rather than a bare `s` here because `s` is a legal
		// character in id strings and session names.
		if p.mode == sessionsResumeInputMode && msg.String() == "ctrl+s" {
			p.summarized = !p.summarized
			return m, nil
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// dispatchSessionsMenu transitions from the top-level menu into the
// chosen sub-picker. List-based actions (Load, Rename, Export) refresh
// the sessions snapshot so the list reflects on-disk state. Resume
// (ref-input) skips the list entirely and opens a textinput
// immediately — that's its whole reason for existing.
func (m Model) dispatchSessionsMenu() (Model, tea.Cmd) {
	p := m.sessionsPicker
	if p == nil || p.menuCursor >= len(p.menuItems) {
		return m, nil
	}
	item := p.menuItems[p.menuCursor]
	if item.Action == sessionsResumeInputMode {
		return m.enterSessionsResumeInput()
	}
	// Rename is the one list action an archive can't serve — see
	// loadSessionPage.
	p.loadListPage(item.Action != sessionsRenameListMode, 0)
	if len(p.sessions) == 0 {
		m.sessionsPickerOpen = false
		m.sessionsPicker = nil
		m.appendLine(styleAuto.Render("(no saved sessions yet)"))
		return m, nil
	}
	p.mode = item.Action
	p.listCursor = 0
	p.listOffset = 0
	// Default the cursor to the running session for Rename — most
	// "name this session" intents target the one you're in. Load and
	// Export start at the top of the list (newest first), since
	// "load a different session" and "export the latest" are the
	// likely intents.
	if item.Action == sessionsRenameListMode {
		for i, s := range p.sessions {
			if s.ID == p.activeID {
				p.listCursor = i
				break
			}
		}
	}
	return m, nil
}

// loadListPage refreshes one saved-session page for a list-mode picker. It asks
// for one extra row as a cheap has-next probe, but keeps only the visible page.
func (p *sessionsPickerState) loadListPage(includeArchived bool, page int) {
	if page < 0 {
		page = 0
	}
	rows, hasNext := loadSessionPage(includeArchived, page)
	p.sessions = rows
	p.listCursor = 0
	p.listOffset = 0
	p.listPage = page
	p.listHasPrev = page > 0
	p.listHasNext = hasNext
	p.listIncludeArchived = includeArchived
}

// loadSessionPage returns one newest-first page of metadata. includeArchived is
// per-mode rather than global, because an archive is only meaningful for some
// actions:
//
//   - Load / Export / Inspect: yes. The archive is frequently the only surviving copy
//     of pre-compaction history, so both opening and exporting it are the
//     point of surfacing it at all.
//   - Rename: no. An archive has no persisted Name field to set — renaming
//     one resolves the id through loadSnapshot and would write a brand-new
//     duplicate session instead of labelling anything.
func loadSessionPage(includeArchived bool, page int) ([]session.SessionInfo, bool) {
	if page < 0 {
		page = 0
	}
	all, err := session.ListPage(session.ListOptions{IncludeArchived: includeArchived}, page*sessionsPageSize, sessionsPageSize+1)
	if err != nil {
		return nil, false
	}
	hasNext := len(all) > sessionsPageSize
	if hasNext {
		all = all[:sessionsPageSize]
	}
	return all, hasNext
}

// commitSessionsLoad loads the picked session into the running
// window, saving the current one first. The list-driven counterpart
// of commitSessionsResume (which takes an id/name from a textinput).
func (m Model) commitSessionsLoad() (Model, tea.Cmd) {
	p := m.sessionsPicker
	if p == nil || p.listCursor >= len(p.sessions) {
		return m, nil
	}
	chosen := p.sessions[p.listCursor]
	summarized := p.summarized
	m.sessionsPickerOpen = false
	m.sessionsPicker = nil
	return m.resumeSession(chosen.ID, summarized)
}

// enterSessionsResumeInput opens an empty textinput for the user to
// type a session id or name. No list is shown — the whole point of
// Resume (vs. Load) is to skip the recent-N list and go directly to
// a known reference. Use Load when you want to scroll a list or
// /recall when you want to search older sessions by content.
func (m Model) enterSessionsResumeInput() (Model, tea.Cmd) {
	p := m.sessionsPicker
	if p == nil {
		return m, nil
	}
	in := textinput.New()
	in.Placeholder = "session id or name"
	in.Prompt = ""
	in.CharLimit = 64
	in.Focus()
	p.input = in
	p.inputErr = ""
	p.mode = sessionsResumeInputMode
	return m, nil
}

// commitSessionsResume reads the typed id-or-name and resumes that
// session. Validation is delegated to session.Load — empty refs
// produce an inline error in the form rather than dropping back to
// the menu.
func (m Model) commitSessionsResume() (Model, tea.Cmd) {
	p := m.sessionsPicker
	if p == nil {
		return m, nil
	}
	ref := strings.TrimSpace(p.input.Value())
	if ref == "" {
		p.inputErr = "type a session id or name"
		return m, nil
	}
	summarized := p.summarized
	m.sessionsPickerOpen = false
	m.sessionsPicker = nil
	return m.resumeSession(ref, summarized)
}

// resumeSession is the shared resume implementation: save current,
// load target, optionally inject a summary, rebuild transcript, and
// reseed the context-token counter so the status bar isn't stuck at
// the pre-load value. Used by both the picker commit path and the
// /sessions <id|name> shortcut.
func (m Model) resumeSession(id string, summarized bool) (Model, tea.Cmd) {
	// Same rule as the at-exit save and /clear: a session with no exchange
	// is a system-prompt-only shell, and persisting it here would undo the
	// whole point of not creating them. This site matters most of the three
	// — "launch yottacode, open /sessions, load a previous conversation" is
	// the picker's most common flow, and the session being replaced is
	// almost always empty.
	if m.sess.HasExchange() {
		if err := m.sess.Save(); err != nil {
			m.appendLine(styleError.Render(SysMsg(SysWarning, "sessions", "save current", err.Error())))
		}
	}
	loaded, err := session.Load(id)
	if err != nil {
		m.appendLine(styleError.Render(SysMsg(SysFailure, "sessions", "load failed", err.Error())))
		return m, nil
	}
	if summarized {
		newSess, _, note, err := loadSummarizedSession(m.summarizeDeps(), loaded)
		if err != nil {
			m.appendLine(styleError.Render(SysMsg(SysFailure, "sessions", "summarize failed", err.Error())))
			return m, nil
		}
		loaded = newSess
		if note != "" {
			m.appendLine(styleAuto.Render(SysMsg(SysContext, "sessions", "summarized resume", note)))
		}
	}
	m.sess = loaded
	m.transcript.Reset()
	m.streaming.Reset()
	m.streamingMode = streamIdle
	rebuildTranscript(&m)
	m.refreshContextTokens()
	// Restoring an archive is reported differently on purpose: the id the
	// user picked is NOT the id they're now in. loadSnapshot mints a fresh
	// session so continuing can't overwrite the archive — which for most
	// snapshots is the only surviving copy of that history.
	if session.IsSnapshotID(id) {
		m.appendLine(styleAuto.Render(SysMsg(SysSuccess, "restore", "archived history", id+" → "+loaded.ID, fmt.Sprintf("%d msgs", len(loaded.Messages)), "archive unchanged")))
	} else {
		m.appendLine(styleAuto.Render(SysMsg(SysSuccess, "resume", "loaded", loaded.ID, fmt.Sprintf("%d msgs", len(loaded.Messages)))))
	}
	// Switching sessions must not carry an armed /loop into the loaded one —
	// it was armed against the old conversation's context.
	m.disarmAllLoops("[loop] stopped — switched session")
	// A resumed transcript can already sit past the auto threshold —
	// run the watermark check now so an over-window session heals
	// before the first send instead of after a context-overflow
	// failure (the check is otherwise bound to turn ends).
	if ctxCmd := m.updateContextUsage(true); ctxCmd != nil {
		return m, ctxCmd
	}
	return m, nil
}

// enterSessionsRenameInput moves the picker from list-pick to
// textinput-fill, prefilling the input with the current Name so an
// "edit, don't replace" flow works (vim-style — typing replaces, but
// arrow keys + edits work too).
func (m Model) enterSessionsRenameInput() (Model, tea.Cmd) {
	p := m.sessionsPicker
	if p == nil || p.listCursor >= len(p.sessions) {
		return m, nil
	}
	chosen := p.sessions[p.listCursor]
	p.picked = &chosen
	in := textinput.New()
	in.Placeholder = "session name (used by /sessions <name> shortcut)"
	in.SetValue(chosen.Name)
	in.Prompt = ""
	in.CharLimit = 64
	in.Focus()
	p.input = in
	p.inputErr = ""
	p.mode = sessionsRenameInputMode
	return m, nil
}

// commitSessionsRename writes the new name onto the picked session.
// If the picked session is the running one, mutate in-memory + save;
// otherwise, load → set → save without disturbing the running state.
func (m Model) commitSessionsRename() (Model, tea.Cmd) {
	p := m.sessionsPicker
	if p == nil || p.picked == nil {
		return m, nil
	}
	name := strings.TrimSpace(p.input.Value())
	if name == "" {
		p.inputErr = "name is required"
		return m, nil
	}
	target := *p.picked
	if target.ID == m.sess.ID {
		// Renaming the current session — mutate the live struct so
		// the next auto-save picks up the new name and resumeHint
		// (the on-exit "Session saved. Resume with: …" line) uses
		// the friendly form.
		m.sess.Name = name
		// Only flush it if there's a conversation to attach the name to.
		// Naming an empty session would write a shell that List then
		// filters back out — an on-disk file the picker never shows, which
		// reads as "the rename did nothing". The name is live on the
		// in-memory session either way, so the first real turn persists it.
		if m.sess.HasExchange() {
			if err := m.sess.Save(); err != nil {
				m.appendLine(styleError.Render(SysMsg(SysFailure, "rename", "save session", err.Error())))
				return m, nil
			}
		}
	} else {
		loaded, err := session.Load(target.ID)
		if err != nil {
			m.appendLine(styleError.Render(SysMsg(SysFailure, "rename", "load session", err.Error())))
			return m, nil
		}
		loaded.Name = name
		if err := loaded.Save(); err != nil {
			m.appendLine(styleError.Render(SysMsg(SysFailure, "rename", "save session", err.Error())))
			return m, nil
		}
	}
	m.sessionsPickerOpen = false
	m.sessionsPicker = nil
	m.appendLine(styleAuto.Render(SysMsg(SysSuccess, "rename", "saved", target.ID+" → "+name)))
	return m, nil
}

// enterSessionsExportInput captures the picked session and moves the
// picker into the path-prompt sub-mode. Default value is a sensible
// guess: yottacode-<id>[-<name>].md inside cwd, so Enter-to-confirm
// works for the common "give me the current session as a file"
// case.
func (m Model) enterSessionsExportInput() (Model, tea.Cmd) {
	p := m.sessionsPicker
	if p == nil || p.listCursor >= len(p.sessions) {
		return m, nil
	}
	chosen := p.sessions[p.listCursor]
	p.picked = &chosen
	in := textinput.New()
	in.Placeholder = "path/to/transcript.md"
	in.SetValue(defaultExportPath(m.cwd, chosen))
	in.Prompt = ""
	in.CharLimit = 256
	in.Focus()
	p.input = in
	p.inputErr = ""
	p.mode = sessionsExportInputMode
	return m, nil
}

// defaultExportPath builds a file name from the session id (and name
// when set) inside cwd. The resulting path is always absolute so the
// confirmation line is self-explanatory regardless of where the user
// later cd's to.
func defaultExportPath(cwd string, info session.SessionInfo) string {
	base := info.ID
	if info.Name != "" {
		base = fmt.Sprintf("%s-%s", info.Name, info.ID)
	}
	// An archive's own id is the recognizable one. Exporting it goes through
	// Load, which mints a fresh session, so naming the file after the loaded
	// session would produce a timestamp the user has never seen and cannot
	// tie back to anything.
	if info.Archived {
		base = info.ID
	}
	return filepath.Join(cwd, base+".md")
}

// commitSessionsExport loads the picked session (or reuses the
// in-memory one when picking the active session), renders to
// markdown, and writes to the path the user typed. Relative paths
// are resolved against cwd.
func (m Model) commitSessionsExport() (Model, tea.Cmd) {
	p := m.sessionsPicker
	if p == nil || p.picked == nil {
		return m, nil
	}
	path := strings.TrimSpace(p.input.Value())
	if path == "" {
		p.inputErr = "path is required"
		return m, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.cwd, path)
	}

	var sess *session.Session
	if p.picked.ID == m.sess.ID {
		sess = m.sess
	} else {
		loaded, err := session.Load(p.picked.ID)
		if err != nil {
			p.inputErr = err.Error()
			return m, nil
		}
		sess = loaded
	}
	md := session.ExportMarkdown(sess)
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		p.inputErr = err.Error()
		return m, nil
	}
	m.sessionsPickerOpen = false
	m.sessionsPicker = nil
	m.appendLine(styleAuto.Render(SysMsg(SysSuccess, "export", "wrote transcript", fmt.Sprintf("%d bytes", len(md)), path)))
	return m, nil
}

// renderSessionsPicker dispatches to the per-mode renderer and
// stitches the footer. Mirrors renderProviderPicker's layout so the
// two pickers feel like the same UI.
func renderSessionsPicker(p *sessionsPickerState, width int, hits ...*pickerHits) string {
	var h *pickerHits
	if len(hits) > 0 {
		h = hits[0]
	}
	var body, footerText string
	switch p.mode {
	case sessionsMenuMode:
		body = renderSessionsMenu(p, width, h)
		footerText = "↵ confirm · esc cancel · ↑↓ navigate"
	case sessionsLoadListMode:
		body = renderSessionsList(p, "Load session", loadListDescription(p), width, h)
		state := "off"
		if p.summarized {
			state = "on"
		}
		footerText = fmt.Sprintf("↵ load · s toggle summarized (%s) · esc back · ↑↓ navigate", state)
	case sessionsResumeInputMode:
		body = renderSessionsResumeInput(p, width)
		state := "off"
		if p.summarized {
			state = "on"
		}
		footerText = fmt.Sprintf("↵ resume · ctrl+s toggle summarized (%s) · esc back", state)
	case sessionsRenameListMode:
		body = renderSessionsList(p, "Rename session",
			"Pick a session to label. The current session is marked ✓.", width, h)
		footerText = "↵ rename · esc back · ↑↓ navigate"
	case sessionsRenameInputMode:
		body = renderSessionsRenameInput(p, width)
		footerText = "↵ save · esc back"
	case sessionsExportListMode:
		body = renderSessionsList(p, "Export session",
			"Pick a session to export as Markdown. The current session is marked ✓.", width, h)
		footerText = "↵ export · esc back · ↑↓ navigate"
	case sessionsExportInputMode:
		body = renderSessionsExportInput(p, width)
		footerText = "↵ write · esc back"
	default:
		body = styleEmpty.Render("(unknown picker state)")
		footerText = "esc cancel"
	}
	footer := styleFooter.Render(footerText)
	return body + "\n\n" + footer
}

func renderSessionsMenu(p *sessionsPickerState, width int, h *pickerHits) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Sessions",
		"Resume, rename, or export a saved session.", width))
	b.WriteString("\n")
	for i, item := range p.menuItems {
		row := strings.Count(b.String(), "\n")
		h.row(row, i)
		b.WriteString(renderMenuItem(menuItemOpts{
			Label:      item.Label,
			LabelWidth: 10,
			Desc:       item.Subtitle,
			Cursor:     i == p.menuCursor,
		}))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// loadListDescription tweaks the description string to mention the
// summarized toggle so users discover it without having to read the
// footer's `s` hint.
func loadListDescription(p *sessionsPickerState) string {
	desc := "Pick a session to load. Press `s` to toggle --summarized for large sessions."
	if p.summarized {
		desc = "Pick a session to load. --summarized is ON; `s` to disable. Short sessions load verbatim either way."
	}
	return desc
}

func (p *sessionsPickerState) ensureListCursorVisible() {
	if p.listCursor < p.listOffset {
		p.listOffset = p.listCursor
	}
	if p.listCursor >= p.listOffset+sessionsPageSize {
		p.listOffset = p.listCursor - sessionsPageSize + 1
	}
	maxOffset := max(len(p.sessions)-sessionsPageSize, 0)
	p.listOffset = min(max(p.listOffset, 0), maxOffset)
}

// renderSessionsList draws the latest-N session table for resume,
// rename, and export. Each row: id-or-name (label column) · model ·
// N msgs · relative age. The footer note "showing the N most
// recent" sits under the rows so users know /recall is the path for
// older sessions.
func renderSessionsList(p *sessionsPickerState, title, description string, width int, h *pickerHits) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader(title, description, width))
	b.WriteString("\n")
	if len(p.sessions) == 0 {
		b.WriteString(styleEmpty.Render("  (no saved sessions yet)"))
		return strings.TrimRight(b.String(), "\n")
	}
	layout := sessionsRowLayout(p.sessions, width)
	p.ensureListCursorVisible()
	end := min(p.listOffset+sessionsPageSize, len(p.sessions))
	for i := p.listOffset; i < end; i++ {
		s := p.sessions[i]
		row := strings.Count(b.String(), "\n")
		h.row(row, i)
		b.WriteString(renderMenuItem(menuItemOpts{
			Label:      sessionPickerLabel(s),
			LabelWidth: sessionsLabelWidth,
			Desc:       sessionPickerDesc(s, layout),
			Cursor:     i == p.listCursor,
			Checked:    s.ID == p.activeID,
		}))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styleMeta.Render(
		fmt.Sprintf("  page %d · showing %d-%d · %s · PgUp/PgDn page · /recall searches all sessions",
			p.listPage+1, p.listPage*sessionsPageSize+p.listOffset+1, p.listPage*sessionsPageSize+end, pageAvailability(p.listHasPrev, p.listHasNext))))
	return strings.TrimRight(b.String(), "\n")
}

func pageAvailability(hasPrev, hasNext bool) string {
	switch {
	case hasPrev && hasNext:
		return "prev/next available"
	case hasPrev:
		return "prev available"
	case hasNext:
		return "next available"
	default:
		return "end of list"
	}
}

// sessionPickerLabel is the left-column identifier. Prefer Name when
// set (friendlier — matches how the user thinks of the session); fall
// back to the timestamp id. Long names truncate via the LabelWidth
// pass in renderMenuItem.
func sessionPickerLabel(s session.SessionInfo) string {
	if s.Name != "" {
		return s.Name
	}
	// An archive is labelled with the conversation it came from, not its own
	// (much longer) snapshot id. Since ids sort so an archive lands directly
	// under its parent, the pair reads as "this session, and its archived
	// pre-compaction history" — with the metadata column saying which is which.
	if s.Archived {
		return s.ArchivedOf
	}
	return s.ID
}

// sessionPickerDesc is the right-hand column: the session's one-line gist
// followed by model · N msgs · age. Age is computed from
// time.Since(s.Created) at render time so the picker reads correctly even
// after sitting open for a while.
//
// The gist leads because it's the thing that actually answers "which one do
// I want?" — a timestamp id and a model name don't. It's padded to a fixed
// column so the metadata still lines up down the list, and it's dropped
// entirely on a narrow terminal rather than squeezing the metadata out.
func sessionPickerDesc(s session.SessionInfo, lay sessionRowLayout) string {
	meta := sessionPickerMeta(s, lay.compactMeta)
	if lay.gistWidth <= 0 {
		return meta
	}
	// Pad even when this row has no gist, so the metadata column stays
	// aligned with the rows that do.
	return fmt.Sprintf("%-*s%s%s", lay.gistWidth, truncateLabel(s.Summary, lay.gistWidth), summarySep, meta)
}

// maxModelWidth caps the model name inside the metadata. Without it a single
// outlier ("nvidia/nemotron-3-ultra-550b-a55b") sets the column width for the
// whole list and can squeeze the gist out entirely.
const maxModelWidth = 22

// sessionPickerMeta is the trailing metadata: model · N msgs · age, or just
// N msgs · age when compact. Age is computed from time.Since(s.Created) at
// render time so the picker reads correctly even after sitting open a while.
func sessionPickerMeta(s session.SessionInfo, compact bool) string {
	plural := "msgs"
	if s.Messages == 1 {
		plural = "msg"
	}
	age := formatRecallAge(time.Since(s.Created))
	// Archived rows say so where the model would go. The model matters less
	// for an archive than the fact that it IS one — these restore into a new
	// session rather than reopening the one named in the label.
	lead := truncateLabel(s.Model, maxModelWidth)
	if s.Archived {
		lead = "archived"
	}
	// Assembled from parts rather than a fixed format so an absent model
	// drops its segment instead of rendering an empty one ("·   · 12 msgs").
	// Model is genuinely empty for a session restored from an ORPHANED
	// archive: loadSnapshot inherits model/cwd from the parent, and an
	// orphan has no parent left to inherit from.
	parts := make([]string, 0, 3)
	if compact {
		if s.Archived {
			parts = append(parts, "archived")
		}
	} else if lead != "" {
		parts = append(parts, lead)
	}
	parts = append(parts, fmt.Sprintf("%d %s", s.Messages, plural), age)
	return strings.Join(parts, " · ")
}

// sessionRowLayout is the column plan for one render of the list. Computed
// once for the whole list, never per row: sizing per row would let each
// row's model name shift its own gist column, leaving the list ragged and
// harder to scan — the opposite of what the gist is for.
type sessionRowLayout struct {
	gistWidth   int  // 0 = no room, render metadata only
	compactMeta bool // drop the model name to buy the gist its column
}

// sessionsRowLayout budgets the row. The gist is what actually answers
// "which session is this?", so when space is tight the model name is
// sacrificed for it before the gist is dropped: full → drop model → gist
// only as a last resort.
func sessionsRowLayout(sessions []session.SessionInfo, width int) sessionRowLayout {
	anyGist := false
	for _, s := range sessions {
		if s.Summary != "" {
			anyGist = true
			break
		}
	}
	if !anyGist {
		return sessionRowLayout{}
	}
	if width <= 0 {
		width = defaultPickerWidth
	}
	// Row overhead ahead of Desc, per renderMenuItem's layout:
	// cursor(2) + label column + gap(1) + check(2).
	const rowOverhead = 2 + sessionsLabelWidth + 1 + 2
	budget := func(compact bool) int {
		metaWidth := 0
		for _, s := range sessions {
			metaWidth = max(metaWidth, runeLen(sessionPickerMeta(s, compact)))
		}
		return min(width-rowOverhead-metaWidth-runeLen(summarySep)-1, maxSummaryWidth)
	}
	if b := budget(false); b >= minSummaryWidth {
		return sessionRowLayout{gistWidth: b}
	}
	if b := budget(true); b >= minSummaryWidth {
		return sessionRowLayout{gistWidth: b, compactMeta: true}
	}
	return sessionRowLayout{}
}

func renderSessionsResumeInput(p *sessionsPickerState, width int) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Resume session",
		"Type a session id (e.g. 2026-04-29T14-22-08) or a name set via /sessions Rename.", width))
	b.WriteString("\n")
	if p.inputErr != "" {
		b.WriteString(styleError.Render("✗ " + p.inputErr))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "❯ %-14s %s", "Ref:", p.input.View())
	return strings.TrimRight(b.String(), "\n")
}

func renderSessionsRenameInput(p *sessionsPickerState, width int) string {
	var b strings.Builder
	title := "Rename session"
	if p.picked != nil {
		title = "Rename — " + sessionPickerLabel(session.SessionInfo{
			ID: p.picked.ID, Name: p.picked.Name,
		})
	}
	b.WriteString(renderMenuHeader(title,
		"Type a label and press Enter. Names are not unique; the canonical key is the session id.", width))
	b.WriteString("\n")
	if p.inputErr != "" {
		b.WriteString(styleError.Render("✗ " + p.inputErr))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "❯ %-14s %s", "Name:", p.input.View())
	return strings.TrimRight(b.String(), "\n")
}

func renderSessionsExportInput(p *sessionsPickerState, width int) string {
	var b strings.Builder
	title := "Export session"
	if p.picked != nil {
		title = "Export — " + sessionPickerLabel(session.SessionInfo{
			ID: p.picked.ID, Name: p.picked.Name,
		})
	}
	b.WriteString(renderMenuHeader(title,
		"Confirm or edit the path. Relative paths resolve against the current directory.", width))
	b.WriteString("\n")
	if p.inputErr != "" {
		b.WriteString(styleError.Render("✗ " + p.inputErr))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "❯ %-14s %s", "Path:", p.input.View())
	return strings.TrimRight(b.String(), "\n")
}
