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
	cursor int
	items  []skillsMenuItem
	mode   skillsMenuMode
	input  textinput.Model
	status string
}

// openSkillsMenu builds the top-level menu and attaches it to the
// Model. Items are static — no per-session filtering yet (a future
// version could grey out Check/Update when the lockfile is empty).
func (m *Model) openSkillsMenu() {
	state := &skillsMenuState{
		items: []skillsMenuItem{
			{
				label:  "Catalog",
				desc:   "browse + enable skills (tabs: built-in / installed)",
				action: skillsMenuOpenCatalog,
			},
			{
				label:  "Install",
				desc:   "install from a local path, URL, or owner/repo[/path]",
				action: skillsMenuStartInstall,
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
	switch m.skillsMenu.mode {
	case skillsMenuInstallInput:
		return updateSkillsMenuInstallInput(m, msg)
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
	res, err := skills.Install(skills.InstallOptions{Source: source})
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
	// Auto-enable so the next picker open shows [x] and the next
	// turn sees the skill in its tool list. Same reasoning as the
	// slash-form install path.
	if m.skillTool != nil {
		m.skillTool.Enable(res.Skill.Name)
	}
	return reloadSkillsRegistry(m), nil
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
	m.skillsMenuOpen = false
	m.skillsMenu = nil
	return cmdSkillsUpdate(m, nil)
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

	if state.mode == skillsMenuInstallInput {
		b.WriteString(state.input.View())
		b.WriteString("\n\n")
		b.WriteString(stylePaletteEmpty.Render("  Enter installs · Esc returns to the menu"))
		if state.status != "" {
			b.WriteString("\n")
			b.WriteString(styleError.Render("  " + state.status))
		}
		return b.String()
	}

	maxLabel := 6
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
		b.WriteString(stylePaletteEmpty.Render("  " + state.status))
	}
	return strings.TrimRight(b.String(), "\n")
}
