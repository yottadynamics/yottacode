package tui

// Top-level /skills overlay. Replaces the picker-as-entry-point with a
// menu whose items are: Catalog (the existing picker with built-in vs
// installed tabs), Install (textinput for source), Check, Update.
//
// The menu doesn't *do* anything itself — it dispatches into the same
// helpers the slash subcommands already call (cmdSkillsCheck,
// cmdSkillsUpdate, the picker open/render path). That keeps the
// behavior consistent whether the user reached a subcommand via
// `/skills install foo` or via the menu's Install row.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/skills"
)

// skillsMenuMode tells updateSkillsMenu which keystroke handler is
// active. Selecting is the default vertical-list state; installing
// hands keys to the textinput until the user submits or cancels.
type skillsMenuMode int

const (
	skillsMenuSelect skillsMenuMode = iota
	skillsMenuInstallInput
	skillsMenuUninstallPick
)

// skillsMenuItem is one row in the top-level menu. Action is the
// dispatch hook for Enter; runs against the live Model and returns
// the updated Model + any deferred command (a re-render, a turn
// start, etc.).
type skillsMenuItem struct {
	label  string
	desc   string
	action func(m Model) (Model, tea.Cmd)
}

// skillsMenuState backs the /skills inline overlay. Lifetime is one
// /skills invocation — the user picks a subcommand, it runs, the
// menu closes.
type skillsMenuState struct {
	cursor    int
	items     []skillsMenuItem
	mode      skillsMenuMode
	input     textinput.Model
	status    string
	busy      string
	busyKind  string
	busyFrame string

	// uninstallRows is the removable (user-scope) skill set shown while
	// mode == skillsMenuUninstallPick; uninstallCursor is its focused
	// row. Rebuilt from the live registry each time the picker opens and
	// after every removal so the list always reflects disk reality.
	uninstallRows   []skills.Skill
	uninstallCursor int
}

type skillsMenuInstallDoneMsg struct {
	source string
	res    skills.InstallResult
	err    error
}

type skillsMenuUpdateDoneMsg struct {
	results []skills.UpdateResult
	err     error
}

func installSkillFromMenuCmd(source string) tea.Cmd {
	return func() tea.Msg {
		res, err := skills.Install(skills.InstallOptions{Source: source})
		return skillsMenuInstallDoneMsg{source: source, res: res, err: err}
	}
}

func updateSkillsFromMenuCmd() tea.Cmd {
	return func() tea.Msg {
		results, err := skills.Update(skills.UpdateOptions{})
		return skillsMenuUpdateDoneMsg{results: results, err: err}
	}
}

// openSkillsMenu builds the top-level menu and attaches it to the
// Model. Items are static — no per-session filtering yet (a future
// version could grey out Check/Update when the lockfile is empty).
func (m *Model) openSkillsMenu() {
	state := &skillsMenuState{
		items: []skillsMenuItem{
			{
				label:  "Catalog",
				desc:   "browse official skills, installed skills, and bundled fallback",
				action: skillsMenuOpenCatalog,
			},
			{
				label:  "Install",
				desc:   "install from a local path, URL, or owner/repo[/path]",
				action: skillsMenuStartInstall,
			},
			{
				label:  "Uninstall",
				desc:   "remove an installed (user-scope) skill",
				action: skillsMenuStartUninstall,
			},
			{
				label:  "Check",
				desc:   "report drift between installed bytes and the lockfile",
				action: skillsMenuRunCheck,
			},
			{
				label:  "Update",
				desc:   "re-fetch installed skills from their recorded source",
				action: skillsMenuRunUpdate,
			},
		},
	}
	m.skillsMenu = state
	m.skillsMenuOpen = true
}

// updateSkillsMenu routes keystrokes while the menu is open. Two
// modes — vertical-list selection and the inline install textinput.
func (m Model) updateSkillsMenu(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.skillsMenu == nil {
		m.skillsMenuOpen = false
		return m, nil
	}
	if m.skillsMenu.busy != "" {
		return m, nil
	}
	switch m.skillsMenu.mode {
	case skillsMenuInstallInput:
		return updateSkillsMenuInstallInput(m, msg)
	case skillsMenuUninstallPick:
		return updateSkillsMenuUninstallPick(m, msg)
	default:
		return updateSkillsMenuSelect(m, msg)
	}
}

// updateSkillsMenuSelect handles arrow navigation + Enter on the
// menu. Esc closes; Enter runs the focused item's action.
func updateSkillsMenuSelect(m Model, msg tea.KeyMsg) (Model, tea.Cmd) {
	state := m.skillsMenu
	switch msg.Type {
	case tea.KeyUp:
		if state.cursor > 0 {
			state.cursor--
		}
		state.status = ""
		return m, nil
	case tea.KeyDown:
		if state.cursor < len(state.items)-1 {
			state.cursor++
		}
		state.status = ""
		return m, nil
	case tea.KeyEsc:
		m.skillsMenuOpen = false
		m.skillsMenu = nil
		return m, nil
	case tea.KeyEnter:
		if len(state.items) == 0 {
			return m, nil
		}
		return state.items[state.cursor].action(m)
	}
	return m, nil
}

// updateSkillsMenuInstallInput hands keystrokes to the install
// textinput. Enter submits the source; Esc returns to the menu
// without running the install.
func updateSkillsMenuInstallInput(m Model, msg tea.KeyMsg) (Model, tea.Cmd) {
	state := m.skillsMenu
	switch msg.Type {
	case tea.KeyEsc:
		state.mode = skillsMenuSelect
		state.status = ""
		return m, nil
	case tea.KeyEnter:
		return commitSkillsMenuInstall(m)
	}
	var cmd tea.Cmd
	state.input, cmd = state.input.Update(msg)
	return m, cmd
}

// skillsMenuOpenCatalog closes the menu and opens the existing
// picker. The picker carries its own tab state so the layered
// overlay model stays simple: one inline overlay at a time.
func skillsMenuOpenCatalog(m Model) (Model, tea.Cmd) {
	m.skillsMenuOpen = false
	m.skillsMenu = nil
	m.openSkillsPicker()
	return m, nil
}

// skillsMenuStartInstall flips the menu into textinput mode. The
// textinput is constructed lazily so reopening the menu doesn't
// carry over stale input from a prior session.
func skillsMenuStartInstall(m Model) (Model, tea.Cmd) {
	state := m.skillsMenu
	in := textinput.New()
	in.Placeholder = "./path | https://.../SKILL.md | owner/repo[/path]"
	in.Prompt = "source: "
	in.CharLimit = 512
	in.Focus()
	state.input = in
	state.mode = skillsMenuInstallInput
	state.status = ""
	return m, nil
}

// commitSkillsMenuInstall runs the installer against the typed
// source. Refreshes the in-session registry on success so the new
// skill is immediately visible. Errors stay in the menu's status
// line so the user can edit and resubmit without retyping the whole
// command.
func commitSkillsMenuInstall(m Model) (Model, tea.Cmd) {
	state := m.skillsMenu
	source := strings.TrimSpace(state.input.Value())
	if source == "" {
		state.status = "source required"
		return m, nil
	}
	state.busy = "installing from " + source + "…"
	state.busyKind = "install:" + source
	state.busyFrame = m.spinner.View()
	state.status = state.busy
	return m, tea.Batch(m.spinner.Tick, installSkillFromMenuCmd(source))
}

func handleSkillsMenuInstallDone(m Model, msg skillsMenuInstallDoneMsg) (Model, tea.Cmd) {
	if !m.skillsMenuOpen || m.skillsMenu == nil || m.skillsMenu.busyKind != "install:"+msg.source {
		return m, nil
	}
	state := m.skillsMenu
	state.busy = ""
	state.busyKind = ""
	state.busyFrame = ""
	res, err := msg.res, msg.err
	if err != nil {
		state.status = err.Error()
		return m, nil
	}
	m.skillsMenuOpen = false
	m.skillsMenu = nil
	m.appendLine(styleAuto.Render(fmt.Sprintf(
		"[skills] installed %s (%s) at %s", res.Skill.Name, res.SourceType, res.Dir)))
	for _, w := range res.Warnings {
		m.appendLine(styleError.Render("[skills] warning: " + w))
	}
	m = reloadSkillsRegistry(m)
	if m.skillTool != nil && (res.SourceType == skills.SourceOfficial || res.SourceType == skills.SourceLocal) {
		m.skillTool.Enable(res.Skill.Name)
		m, _ = recomposeSystemPromptWithSkills(m, m.skillTool.Active())
	} else if m.skillTool != nil && (res.SourceType == skills.SourceGitHub || res.SourceType == skills.SourceURL) {
		m.skillTool.Disable(res.Skill.Name)
		m, _ = recomposeSystemPromptWithSkills(m, m.skillTool.Active())
		m.appendLine(styleMeta.Render("[skills] external skill installed disabled; enable it from /skills Catalog after review"))
	}
	return m, nil
}

// skillsMenuStartUninstall gathers the removable skills and flips the
// menu into the uninstall picker. Only user-scope skills are listed:
// built-ins are embedded in the binary and project-scope skills are
// committed source the user removes via git/rm — neither is removable
// through skills.Uninstall, so listing them would only offer a button
// that errors. With nothing removable the menu stays put and explains
// why rather than opening an empty picker.
func skillsMenuStartUninstall(m Model) (Model, tea.Cmd) {
	state := m.skillsMenu
	if m.skillTool == nil {
		state.status = "skills are not wired in this session"
		return m, nil
	}
	rows := removableUserSkills(m)
	if len(rows) == 0 {
		state.status = "no installed skills to remove (built-ins and project skills aren't removable here)"
		return m, nil
	}
	state.uninstallRows = rows
	state.uninstallCursor = 0
	state.mode = skillsMenuUninstallPick
	state.status = ""
	return m, nil
}

// removableUserSkills returns the user-scope subset of the live
// registry, sorted by name — the set skills.Uninstall can act on.
func removableUserSkills(m Model) []skills.Skill {
	if m.skillTool == nil {
		return nil
	}
	var rows []skills.Skill
	for _, sk := range m.skillTool.All {
		if sk.Source == skills.ScopeUser {
			rows = append(rows, sk)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// updateSkillsMenuUninstallPick handles navigation in the uninstall
// list. Up/Down move within the list, Enter removes the focused skill,
// Esc returns to the menu without removing anything.
func updateSkillsMenuUninstallPick(m Model, msg tea.KeyMsg) (Model, tea.Cmd) {
	state := m.skillsMenu
	switch msg.Type {
	case tea.KeyUp:
		if state.uninstallCursor > 0 {
			state.uninstallCursor--
		}
		return m, nil
	case tea.KeyDown:
		if state.uninstallCursor < len(state.uninstallRows)-1 {
			state.uninstallCursor++
		}
		return m, nil
	case tea.KeyEsc:
		state.mode = skillsMenuSelect
		state.status = ""
		return m, nil
	case tea.KeyEnter:
		return commitSkillsMenuUninstall(m)
	}
	return m, nil
}

// commitSkillsMenuUninstall removes the focused skill from
// ~/.yottacode/skills/, refreshes the in-session registry, and scrubs
// the name from persisted default_on. This is the same removal +
// reload + scrub sequence the Catalog `u` path runs
// (uninstallFromSkillsPicker) so the two surfaces stay in lockstep. On
// success it stays in the list — rebuilt against the refreshed registry
// — so several removals in a row don't need re-entry; the list emptying
// drops back to the menu. Errors stay in the status line.
func commitSkillsMenuUninstall(m Model) (Model, tea.Cmd) {
	state := m.skillsMenu
	if len(state.uninstallRows) == 0 {
		state.mode = skillsMenuSelect
		return m, nil
	}
	sk := state.uninstallRows[state.uninstallCursor]
	if _, err := skills.Uninstall(sk.Name); err != nil {
		state.status = "uninstall failed: " + err.Error()
		return m, nil
	}
	m.appendLine(styleAuto.Render("[skills] removed " + sk.Name))
	m = reloadSkillsRegistry(m)
	// Best-effort default_on cleanup: a failure here doesn't undo the
	// removal (the dir is already gone) — surface it and move on.
	if err := removeFromDefaultOn(sk.Name); err != nil {
		m.appendLine(styleError.Render("[skills] persistence scrub failed: " + err.Error()))
	}
	// Rebuild the removable list from the refreshed registry so the
	// just-removed row drops out. An empty list returns to the menu.
	state = m.skillsMenu
	state.uninstallRows = removableUserSkills(m)
	if len(state.uninstallRows) == 0 {
		state.mode = skillsMenuSelect
		state.status = "removed " + sk.Name + " (no more installed skills)"
		return m, nil
	}
	if state.uninstallCursor >= len(state.uninstallRows) {
		state.uninstallCursor = len(state.uninstallRows) - 1
	}
	state.status = "removed " + sk.Name
	return m, nil
}

// skillsMenuRunCheck dispatches to the existing check helper and
// closes the menu. The output renders into the transcript exactly
// like `/skills check` does — same code path, same formatting.
func skillsMenuRunCheck(m Model) (Model, tea.Cmd) {
	m.skillsMenuOpen = false
	m.skillsMenu = nil
	return cmdSkillsCheck(m, nil)
}

func skillsMenuRunUpdate(m Model) (Model, tea.Cmd) {
	if m.skillsMenu == nil {
		return m, nil
	}
	m.skillsMenu.busy = "updating installed skills…"
	m.skillsMenu.busyKind = "update"
	m.skillsMenu.busyFrame = m.spinner.View()
	m.skillsMenu.status = m.skillsMenu.busy
	return m, tea.Batch(m.spinner.Tick, updateSkillsFromMenuCmd())
}

func handleSkillsMenuUpdateDone(m Model, msg skillsMenuUpdateDoneMsg) (Model, tea.Cmd) {
	if !m.skillsMenuOpen || m.skillsMenu == nil || m.skillsMenu.busyKind != "update" {
		return m, nil
	}
	m.skillsMenu.busy = ""
	m.skillsMenu.busyKind = ""
	m.skillsMenu.busyFrame = ""
	if msg.err != nil {
		m.skillsMenu.status = "update failed: " + msg.err.Error()
		return m, nil
	}
	if len(msg.results) == 0 {
		m.skillsMenuOpen = false
		m.skillsMenu = nil
		m.appendLine("(no tracked skills to update)")
		return m, nil
	}
	maxName := 4
	for _, r := range msg.results {
		if l := len(r.Name); l > maxName {
			maxName = l
		}
	}
	var b strings.Builder
	for _, r := range msg.results {
		fmt.Fprintf(&b, "  %-*s  %s", maxName, r.Name, r.Status)
		if r.Message != "" {
			b.WriteString("  (")
			b.WriteString(r.Message)
			b.WriteString(")")
		}
		b.WriteByte('\n')
	}
	m.skillsMenuOpen = false
	m.skillsMenu = nil
	m.appendLine(strings.TrimRight(b.String(), "\n"))
	return reloadSkillsRegistry(m), nil
}

// renderSkillsMenu draws the overlay. Two layouts — list and install
// prompt — share the same header so the user always knows which
// overlay they're in.
func renderSkillsMenu(state *skillsMenuState, _ int) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader(
		"Skills",
		"Manage Agent Skills · Up/Down · Enter selects · Esc closes",
	))
	b.WriteString("\n")
	if state.busy != "" {
		frame := state.busyFrame
		if frame == "" {
			frame = "⣾"
		}
		b.WriteString(styleMeta.Render("  " + frame + " " + state.busy))
		b.WriteString("\n\n")
	}

	if state.mode == skillsMenuInstallInput {
		b.WriteString(state.input.View())
		b.WriteString("\n\n")
		b.WriteString(styleHint.Render("  Enter installs · Esc returns to the menu"))
		if state.status != "" {
			b.WriteString("\n")
			b.WriteString(styleError.Render("  " + state.status))
		}
		return b.String()
	}

	if state.mode == skillsMenuUninstallPick {
		b.WriteString(styleMeta.Render(
			"  Remove an installed skill · Up/Down · Enter removes · Esc returns"))
		b.WriteString("\n\n")
		if len(state.uninstallRows) == 0 {
			b.WriteString(styleEmpty.Render("  (no installed skills)"))
			return strings.TrimRight(b.String(), "\n")
		}
		maxName := 6
		for _, sk := range state.uninstallRows {
			if l := len(sk.Name); l > maxName {
				maxName = l
			}
		}
		for i, sk := range state.uninstallRows {
			label := sk.Name + strings.Repeat(" ", maxName-len(sk.Name)) +
				"  [" + string(sk.Source) + "]"
			b.WriteString(renderMenuItem(menuItemOpts{
				Label:  label,
				Desc:   truncateForRender(sk.Description, 80),
				Cursor: i == state.uninstallCursor,
			}))
			b.WriteByte('\n')
		}
		if state.status != "" {
			b.WriteByte('\n')
			b.WriteString(styleMeta.Render("  " + state.status))
		}
		return strings.TrimRight(b.String(), "\n")
	}

	maxLabel := 16
	for _, it := range state.items {
		if l := len(it.label); l > maxLabel {
			maxLabel = l
		}
	}
	for i, it := range state.items {
		b.WriteString(renderMenuItem(menuItemOpts{
			Label:      it.label,
			LabelWidth: maxLabel,
			Desc:       it.desc,
			Cursor:     i == state.cursor,
		}))
		b.WriteByte('\n')
	}
	if state.status != "" {
		b.WriteByte('\n')
		b.WriteString(styleMeta.Render("  " + state.status))
	}
	return strings.TrimRight(b.String(), "\n")
}
