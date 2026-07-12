package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/session"
)

// sessionsRecentLimit caps the picker list to the N most recent saved
// sessions. The picker is for the just-here-recently case; reaching
// further back is /recall's job. The list footer tells users this so
// older sessions don't feel hidden.
const sessionsRecentLimit = 10

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
	sessions   []session.SessionInfo
	listCursor int

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
	// opens. List rows matching it get a ✔ marker so the user can
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
func (m Model) updateSessionsPicker(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.sessionsPicker == nil {
		m.sessionsPickerOpen = false
		return m, nil
	}
	p := m.sessionsPicker
	switch msg.Type {
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
	if msg.Type == tea.KeyRunes && string(msg.Runes) == "s" {
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
		if p.mode == sessionsResumeInputMode && msg.Type == tea.KeyCtrlS {
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
	p.sessions = loadRecentSessions()
	if len(p.sessions) == 0 {
		m.sessionsPickerOpen = false
		m.sessionsPicker = nil
		m.appendLine(styleAuto.Render("(no saved sessions yet)"))
		return m, nil
	}
	p.mode = item.Action
	p.listCursor = 0
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

// loadRecentSessions returns up to sessionsRecentLimit metadata rows,
// newest first. Wraps session.List with the truncation so callers
// don't have to remember the cap. Returns nil on error or empty.
func loadRecentSessions() []session.SessionInfo {
	all, err := session.List()
	if err != nil {
		return nil
	}
	if len(all) > sessionsRecentLimit {
		all = all[:sessionsRecentLimit]
	}
	return all
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
	if err := m.sess.Save(); err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("⚠ saving current: %v", err)))
	}
	loaded, err := session.Load(id)
	if err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("✗ %v", err)))
		return m, nil
	}
	if summarized {
		newSess, _, note, err := loadSummarizedSession(m.summarizeDeps(), loaded)
		if err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("✗ %v", err)))
			return m, nil
		}
		loaded = newSess
		if note != "" {
			m.appendLine(styleAuto.Render(note))
		}
	}
	m.sess = loaded
	m.transcript.Reset()
	m.streaming.Reset()
	m.streamingMode = streamIdle
	rebuildTranscript(&m)
	m.refreshContextTokens()
	m.appendLine(styleAuto.Render(fmt.Sprintf("[resume] loaded %s (%d msgs)", loaded.ID, len(loaded.Messages))))
	// Switching sessions must not carry an armed /loop into the loaded one —
	// it was armed against the old conversation's context.
	m.disarmLoop("[loop] stopped — switched session")
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
		if err := m.sess.Save(); err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("✗ %v", err)))
			return m, nil
		}
	} else {
		loaded, err := session.Load(target.ID)
		if err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("✗ %v", err)))
			return m, nil
		}
		loaded.Name = name
		if err := loaded.Save(); err != nil {
			m.appendLine(styleError.Render(fmt.Sprintf("✗ %v", err)))
			return m, nil
		}
	}
	m.sessionsPickerOpen = false
	m.sessionsPicker = nil
	m.appendLine(styleAuto.Render(fmt.Sprintf("[rename] %s → %q", target.ID, name)))
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
	m.appendLine(styleAuto.Render(fmt.Sprintf("[export] wrote %d bytes to %s", len(md), path)))
	return m, nil
}

// renderSessionsPicker dispatches to the per-mode renderer and
// stitches the footer. Mirrors renderProviderPicker's layout so the
// two pickers feel like the same UI.
func renderSessionsPicker(p *sessionsPickerState, width int) string {
	_ = width
	var body, footerText string
	switch p.mode {
	case sessionsMenuMode:
		body = renderSessionsMenu(p)
		footerText = "↵ confirm · esc cancel · ↑↓ navigate"
	case sessionsLoadListMode:
		body = renderSessionsList(p, "Load session", loadListDescription(p))
		state := "off"
		if p.summarized {
			state = "on"
		}
		footerText = fmt.Sprintf("↵ load · s toggle summarized (%s) · esc back · ↑↓ navigate", state)
	case sessionsResumeInputMode:
		body = renderSessionsResumeInput(p)
		state := "off"
		if p.summarized {
			state = "on"
		}
		footerText = fmt.Sprintf("↵ resume · ctrl+s toggle summarized (%s) · esc back", state)
	case sessionsRenameListMode:
		body = renderSessionsList(p, "Rename session",
			"Pick a session to label. The current session is marked ✔.")
		footerText = "↵ rename · esc back · ↑↓ navigate"
	case sessionsRenameInputMode:
		body = renderSessionsRenameInput(p)
		footerText = "↵ save · esc back"
	case sessionsExportListMode:
		body = renderSessionsList(p, "Export session",
			"Pick a session to export as Markdown. The current session is marked ✔.")
		footerText = "↵ export · esc back · ↑↓ navigate"
	case sessionsExportInputMode:
		body = renderSessionsExportInput(p)
		footerText = "↵ write · esc back"
	default:
		body = stylePaletteEmpty.Render("(unknown picker state)")
		footerText = "esc cancel"
	}
	footer := styleFooter.Render(footerText)
	return body + "\n\n" + footer
}

func renderSessionsMenu(p *sessionsPickerState) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Sessions",
		"Resume, rename, or export a saved session."))
	b.WriteString("\n")
	for i, item := range p.menuItems {
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

// renderSessionsList draws the latest-N session table for resume,
// rename, and export. Each row: id-or-name (label column) · model ·
// N msgs · relative age. The footer note "showing the N most
// recent" sits under the rows so users know /recall is the path for
// older sessions.
func renderSessionsList(p *sessionsPickerState, title, description string) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader(title, description))
	b.WriteString("\n")
	if len(p.sessions) == 0 {
		b.WriteString(stylePaletteEmpty.Render("  (no saved sessions yet)"))
		return strings.TrimRight(b.String(), "\n")
	}
	for i, s := range p.sessions {
		b.WriteString(renderMenuItem(menuItemOpts{
			Label:      sessionPickerLabel(s),
			LabelWidth: 28,
			Desc:       sessionPickerDesc(s),
			Cursor:     i == p.listCursor,
			Checked:    s.ID == p.activeID,
		}))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(stylePaletteEmpty.Render(
		fmt.Sprintf("  showing the %d most recent · use /recall <query> to search older sessions",
			len(p.sessions))))
	return strings.TrimRight(b.String(), "\n")
}

// sessionPickerLabel is the left-column identifier. Prefer Name when
// set (friendlier — matches how the user thinks of the session); fall
// back to the timestamp id. Long names truncate via the LabelWidth
// pass in renderMenuItem.
func sessionPickerLabel(s session.SessionInfo) string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

// sessionPickerDesc is the right-column metadata: model · N msgs ·
// age. Age is computed from time.Since(s.Created) at render time so
// the picker reads correctly even after sitting open for a while.
func sessionPickerDesc(s session.SessionInfo) string {
	age := formatRecallAge(time.Since(s.Created))
	plural := "msgs"
	if s.Messages == 1 {
		plural = "msg"
	}
	return fmt.Sprintf("%s · %d %s · %s", s.Model, s.Messages, plural, age)
}

func renderSessionsResumeInput(p *sessionsPickerState) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Resume session",
		"Type a session id (e.g. 2026-04-29T14-22-08) or a name set via /sessions Rename."))
	b.WriteString("\n")
	if p.inputErr != "" {
		b.WriteString(styleError.Render("✘ " + p.inputErr))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "❯ %-14s %s", "Ref:", p.input.View())
	return strings.TrimRight(b.String(), "\n")
}

func renderSessionsRenameInput(p *sessionsPickerState) string {
	var b strings.Builder
	title := "Rename session"
	if p.picked != nil {
		title = "Rename — " + sessionPickerLabel(session.SessionInfo{
			ID: p.picked.ID, Name: p.picked.Name,
		})
	}
	b.WriteString(renderMenuHeader(title,
		"Type a label and press Enter. Names are not unique; the canonical key is the session id."))
	b.WriteString("\n")
	if p.inputErr != "" {
		b.WriteString(styleError.Render("✘ " + p.inputErr))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "❯ %-14s %s", "Name:", p.input.View())
	return strings.TrimRight(b.String(), "\n")
}

func renderSessionsExportInput(p *sessionsPickerState) string {
	var b strings.Builder
	title := "Export session"
	if p.picked != nil {
		title = "Export — " + sessionPickerLabel(session.SessionInfo{
			ID: p.picked.ID, Name: p.picked.Name,
		})
	}
	b.WriteString(renderMenuHeader(title,
		"Confirm or edit the path. Relative paths resolve against the current directory."))
	b.WriteString("\n")
	if p.inputErr != "" {
		b.WriteString(styleError.Render("✘ " + p.inputErr))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "❯ %-14s %s", "Path:", p.input.View())
	return strings.TrimRight(b.String(), "\n")
}
