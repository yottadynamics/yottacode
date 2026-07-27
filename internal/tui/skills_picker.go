package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/skills"
)

// skillsCatalogTab is the visible-rows filter for the Catalog picker.
// Official browses the public yottacode-skills repo; Installed shows
// disk-loaded skills; Bundled shows the embedded fallback library.
type skillsCatalogTab int

const (
	catalogTabOfficial skillsCatalogTab = iota
	catalogTabBundled
)

// skillsPickerState backs the Catalog overlay. Lifetime is one
// `/skills` → Catalog invocation. Multi-select via spacebar — the
// user can toggle any number of rows in either direction before
// pressing `c` to commit. Esc cancels without applying changes.
//
// Pattern mirrors subagentsPickerState but with two additions: a
// per-row enablement map (so the original SkillTool state is the
// source of truth at open and the picker mutates a working copy
// only), and a tab filter so the same picker renders either the
// built-in catalog or the user-installed set without two parallel
// overlays.
type skillsPickerState struct {
	// rows is the full universe (built-in + user + project), sorted
	// by name. Stable across navigation; commit flushes the
	// enablement set against this list. The tab decides which subset
	// renders, but rows itself is never re-sliced — that way Tab
	// switching is O(1) and the working enablement map carries
	// correctly across views.
	rows []skills.Skill

	// officialRows is the remote public catalog snapshot. These rows are
	// installable, not toggleable, until installed locally.
	officialRows []skills.Skill

	// enabled is the working copy of which skills are turned on. Keys
	// are skill names; missing == disabled. Initialized from
	// SkillTool.IsEnabled at open. The Tool's own enablement map is
	// only updated on commit, so Esc-cancel restores cleanly.
	enabled map[string]bool

	// tab selects which subset of rows is currently visible.
	tab skillsCatalogTab

	// filter is a substring narrower applied on top of the tab
	// filter. Set by typing in filter mode (entered via `/`). Empty
	// string disables filtering. Matched case-insensitively against
	// each row's name + description so users can search by either.
	filter string

	// filterMode is true while the user is typing into the filter
	// buffer. Keystrokes go to the buffer instead of moving the
	// cursor; Esc cancels and clears the filter, Enter commits and
	// resumes normal navigation.
	filterMode bool

	cursor    int
	status    string
	busy      string
	busyKind  string
	busyFrame string
}

type skillsCatalogRefreshDoneMsg struct {
	rows []skills.Skill
	err  error
}

type skillsOfficialInstallDoneMsg struct {
	name string
	res  skills.InstallResult
	err  error
}

func refreshOfficialCatalogCmd() tea.Cmd {
	return func() tea.Msg {
		rows, err := skills.RefreshOfficialCatalog(skills.OfficialCatalogOptions{})
		return skillsCatalogRefreshDoneMsg{rows: rows, err: err}
	}
}

func installOfficialSkillCmd(name string) tea.Cmd {
	return func() tea.Msg {
		res, err := skills.Install(skills.InstallOptions{Source: skills.OfficialPrefix + name})
		return skillsOfficialInstallDoneMsg{name: name, res: res, err: err}
	}
}

// visibleRows returns the slice of rows the current tab should render. Official
// shows the public catalog and annotates rows that are already installed; Bundled
// shows embedded fallback skills. The filter buffer is applied on top.
func (p *skillsPickerState) visibleRows() []skills.Skill {
	q := strings.ToLower(strings.TrimSpace(p.filter))
	source := p.officialRows
	if p.tab == catalogTabBundled {
		source = p.rows
	}
	out := make([]skills.Skill, 0, len(source))
	matches := func(sk skills.Skill) bool {
		if q == "" {
			return true
		}
		return strings.Contains(strings.ToLower(sk.Name), q) ||
			strings.Contains(strings.ToLower(sk.Description), q)
	}
	for _, sk := range source {
		switch p.tab {
		case catalogTabOfficial:
			if matches(sk) {
				out = append(out, sk)
			}
		case catalogTabBundled:
			if sk.Source == skills.ScopeBuiltin && matches(sk) {
				out = append(out, sk)
			}
		}
	}
	return out
}

func (p *skillsPickerState) installedSkill(name string) (skills.Skill, bool) {
	for _, sk := range p.rows {
		if sk.Name == name && sk.Source != skills.ScopeBuiltin && sk.Source != skills.ScopeOfficial {
			return sk, true
		}
	}
	return skills.Skill{}, false
}

// openSkillsPicker captures a snapshot of the SkillTool's universe
// and enablement state and installs the picker on m. Empty universe
// still opens the picker with an empty-state hint so the user can
// confirm "no skills configured" without re-typing the command.
func (m *Model) openSkillsPicker() {
	if m.skillTool == nil {
		m.appendLine(styleError.Render("skills are not wired in this session (Skill tool unavailable)"))
		return
	}
	all := append([]skills.Skill(nil), m.skillTool.All...)
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	enabled := make(map[string]bool, len(all))
	for _, sk := range all {
		enabled[sk.Name] = m.skillTool.IsEnabled(sk.Name)
	}
	officialRows, err := loadOfficialSkillsCatalog()
	status := ""
	if err != nil {
		status = "official catalog unavailable: " + err.Error()
	}
	m.skillsPicker = &skillsPickerState{
		rows:         all,
		officialRows: officialRows,
		enabled:      enabled,
		status:       status,
	}
	m.skillsPickerOpen = true
}

// updateSkillsPicker handles keystrokes while the picker is open.
// Navigation: Up/Down moves within the current tab; Tab cycles tabs.
// Space toggles the cursor row's enablement. `a` enables all rows
// in the current tab; `n` disables all in the current tab. Enter
// opens the body in $PAGER. `u` uninstalls the cursor row when the
// Installed tab is active (built-ins can't be uninstalled — they're
// embedded). `c` commits the enablement toggles and closes; Esc
// cancels.
func (m Model) updateSkillsPicker(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.skillsPicker == nil {
		m.skillsPickerOpen = false
		return m, nil
	}
	p := m.skillsPicker
	if p.busy != "" {
		return m, nil
	}
	// Filter mode hijacks keystrokes for the filter buffer. Esc
	// clears + exits; Enter commits + exits; Backspace deletes one
	// rune; printable runes append. Cursor resets to 0 on any edit
	// so it can't sit past the last visible row.
	if p.filterMode {
		return updateSkillsPickerFilter(m, p, msg)
	}
	visible := p.visibleRows()
	switch msg.Type {
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		p.status = ""
		return m, nil
	case tea.KeyDown:
		if p.cursor < len(visible)-1 {
			p.cursor++
		}
		p.status = ""
		return m, nil
	case tea.KeyTab, tea.KeyRight:
		// Cycle tabs forward. Up/Down remain row navigation; Left/Right
		// move between tabs so the catalog feels like a browser tab
		// bar. Tab is kept as an alias for keyboards where Right is
		// awkward (e.g. tiling-WM users). Reset cursor + status so
		// the user lands on a row that exists and any stale "enabled
		// all" message clears.
		p.tab = (p.tab + 1) % 2
		p.cursor = 0
		p.status = ""
		return m, nil
	case tea.KeyShiftTab, tea.KeyLeft:
		// Cycle tabs backward across Official and Bundled.
		p.tab = (p.tab + 1) % 2
		p.cursor = 0
		p.status = ""
		return m, nil
	case tea.KeyEnter:
		if len(visible) == 0 {
			return m, nil
		}
		sk := visible[p.cursor]
		if p.tab == catalogTabOfficial {
			if installed, ok := p.installedSkill(sk.Name); ok {
				path, err := stageSkillBodyForPager(installed)
				if err != nil {
					p.status = "could not open body: " + err.Error()
					return m, nil
				}
				m.skillsPickerOpen = false
				m.skillsPicker = nil
				return m.openTranscriptInPager(path, installed.Name, false)
			}
			return installOfficialFromSkillsPicker(m, p, sk)
		}
		// Enter views bundled bodies in $PAGER. Official rows install, or view the
		// installed body when the row is already present locally.
		path, err := stageSkillBodyForPager(sk)
		if err != nil {
			p.status = "could not open body: " + err.Error()
			return m, nil
		}
		m.skillsPickerOpen = false
		m.skillsPicker = nil
		return m.openTranscriptInPager(path, sk.Name, false)
	case tea.KeyEsc:
		// Esc commits and closes. Two-phase commit (toggle then `c`)
		// was the original design, but users naturally reach for Esc
		// to exit and were losing their toggles to the cancel path.
		// Committing on Esc matches every other picker in the
		// codebase. `c` stays wired as an alias below.
		return commitSkillsPicker(m, p)
	case tea.KeySpace:
		if len(visible) == 0 {
			return m, nil
		}
		if p.tab == catalogTabOfficial {
			sk := visible[p.cursor]
			if _, ok := p.installedSkill(sk.Name); ok {
				p.enabled[sk.Name] = !p.enabled[sk.Name]
				p.status = ""
				return m, nil
			}
			p.status = "press Enter to install catalog skill"
			return m, nil
		}
		name := visible[p.cursor].Name
		p.enabled[name] = !p.enabled[name]
		p.status = ""
		return m, nil
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "r":
			if p.tab == catalogTabOfficial {
				return refreshOfficialCatalogFromPicker(m, p)
			}
			return m, nil
		case "a":
			if p.tab == catalogTabOfficial {
				changed := 0
				for _, sk := range visible {
					if _, ok := p.installedSkill(sk.Name); ok {
						p.enabled[sk.Name] = true
						changed++
					}
				}
				p.status = fmt.Sprintf("enabled %d installed official skills", changed)
				return m, nil
			}
			for _, sk := range visible {
				p.enabled[sk.Name] = true
			}
			p.status = "enabled all in tab"
			return m, nil
		case "n":
			if p.tab == catalogTabOfficial {
				changed := 0
				for _, sk := range visible {
					if _, ok := p.installedSkill(sk.Name); ok {
						p.enabled[sk.Name] = false
						changed++
					}
				}
				p.status = fmt.Sprintf("disabled %d installed official skills", changed)
				return m, nil
			}
			for _, sk := range visible {
				p.enabled[sk.Name] = false
			}
			p.status = "disabled all in tab"
			return m, nil
		case "c":
			return commitSkillsPicker(m, p)
		case "u":
			return uninstallFromSkillsPicker(m, p, visible)
		case "/":
			// Enter filter mode. Existing filter is preserved so
			// pressing / a second time appends to the prior query
			// instead of resetting it — matches how /sessions and
			// /help filters behave.
			p.filterMode = true
			p.status = ""
			return m, nil
		}
	}
	return m, nil
}

// updateSkillsPickerFilter handles keystrokes while the filter
// buffer has focus. Kept separate from the main update path so the
// regular cursor/Tab/Space bindings don't have to add filter-mode
// guards everywhere.
func updateSkillsPickerFilter(m Model, p *skillsPickerState, msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		// Esc on filter mode is "clear and exit filter" — distinct
		// from Esc on the main picker which commits and closes.
		// Without this, a stuck filter would force users to backspace
		// each rune.
		p.filter = ""
		p.filterMode = false
		p.cursor = 0
		p.status = ""
		return m, nil
	case tea.KeyEnter:
		// Commit the filter, drop the cursor onto the first
		// matching row, and resume normal navigation.
		p.filterMode = false
		p.cursor = 0
		p.status = ""
		return m, nil
	case tea.KeyBackspace, tea.KeyDelete:
		if len(p.filter) > 0 {
			// Trim by rune to keep multi-byte characters whole.
			r := []rune(p.filter)
			p.filter = string(r[:len(r)-1])
			p.cursor = 0
		}
		return m, nil
	case tea.KeyRunes:
		p.filter += string(msg.Runes)
		p.cursor = 0
		return m, nil
	case tea.KeySpace:
		p.filter += " "
		p.cursor = 0
		return m, nil
	}
	return m, nil
}

// refreshOfficialCatalogFromPicker updates the local metadata cache on explicit
// user request. Normal Catalog browsing is offline; this is the only picker path
// that contacts GitHub without installing a skill.
func refreshOfficialCatalogFromPicker(m Model, p *skillsPickerState) (Model, tea.Cmd) {
	p.busy = "refreshing official catalog…"
	p.busyKind = "refresh"
	p.busyFrame = m.spinner.View()
	p.status = p.busy
	return m, tea.Batch(m.spinner.Tick, refreshOfficialCatalogCmd())
}

func handleSkillsCatalogRefreshDone(m Model, msg skillsCatalogRefreshDoneMsg) (Model, tea.Cmd) {
	if !m.skillsPickerOpen || m.skillsPicker == nil || m.skillsPicker.busyKind != "refresh" {
		return m, nil
	}
	p := m.skillsPicker
	p.busy = ""
	p.busyKind = ""
	p.busyFrame = ""
	if msg.err != nil {
		p.status = "refresh failed: " + msg.err.Error()
		return m, nil
	}
	p.officialRows = msg.rows
	p.cursor = 0
	p.status = fmt.Sprintf("refreshed official catalog (%d skills)", len(msg.rows))
	return m, nil
}

// installOfficialFromSkillsPicker installs the selected public catalog row and
// refreshes the in-session registry so it immediately appears in Installed.
func installOfficialFromSkillsPicker(m Model, p *skillsPickerState, sk skills.Skill) (Model, tea.Cmd) {
	if dir, ok := installedUserSkillDir(sk.Name); ok {
		m = switchPickerToInstalledSkill(m, p, sk.Name)
		p.status = fmt.Sprintf("%s is already installed at %s", sk.Name, dir)
		return m, nil
	}
	p.busy = "installing " + sk.Name + "…"
	p.busyKind = "install:" + sk.Name
	p.busyFrame = m.spinner.View()
	p.status = p.busy
	return m, tea.Batch(m.spinner.Tick, installOfficialSkillCmd(sk.Name))
}

func handleSkillsOfficialInstallDone(m Model, msg skillsOfficialInstallDoneMsg) (Model, tea.Cmd) {
	if !m.skillsPickerOpen || m.skillsPicker == nil || m.skillsPicker.busyKind != "install:"+msg.name {
		return m, nil
	}
	p := m.skillsPicker
	p.busy = ""
	p.busyKind = ""
	p.busyFrame = ""
	res, err := msg.res, msg.err
	if err != nil {
		if already, ok := skills.IsAlreadyInstalled(err); ok {
			m = switchPickerToInstalledSkill(m, p, already.Name)
			p.status = fmt.Sprintf("%s is already installed at %s", already.Name, already.Dir)
			return m, nil
		}
		p.status = "install failed: " + err.Error()
		return m, nil
	}
	label := "official"
	autoEnable := true
	return finishOfficialInstallFromPicker(m, p, res, label, autoEnable)
}

func finishOfficialInstallFromPicker(m Model, p *skillsPickerState, res skills.InstallResult, label string, autoEnable bool) (Model, tea.Cmd) {
	m.appendLine(styleAuto.Render(fmt.Sprintf("[skills] installed %s %s at %s", label, res.Skill.Name, res.Dir)))
	if !autoEnable {
		m.appendLine(styleMeta.Render("[skills] external skill installed disabled; press Space in Catalog to enable after review"))
	}
	for _, w := range res.Warnings {
		m.appendLine(styleError.Render("[skills] warning: " + w))
	}
	if autoEnable && m.skillTool != nil {
		m.skillTool.Enable(res.Skill.Name)
		m, _ = recomposeSystemPromptWithSkills(m, m.skillTool.Active())
	}
	m = reloadSkillsRegistry(m)
	if m.skillTool != nil {
		p.rows = append([]skills.Skill(nil), m.skillTool.All...)
		if autoEnable {
			p.enabled[res.Skill.Name] = true
			m.skillTool.Enable(res.Skill.Name)
		} else {
			p.enabled[res.Skill.Name] = false
		}
	}
	p.cursor = 0
	p.status = "installed " + res.Skill.Name
	if !autoEnable {
		p.status = "installed " + res.Skill.Name + " (disabled until enabled)"
	}
	m.skillsPicker = p
	m.skillsPickerOpen = true
	return m, nil
}

func installedUserSkillDir(name string) (string, bool) {
	dir, err := skills.UserSkillsDir()
	if err != nil {
		return "", false
	}
	path := filepath.Join(dir, name)
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		return path, true
	}
	return "", false
}

func switchPickerToInstalledSkill(m Model, p *skillsPickerState, name string) Model {
	m = reloadSkillsRegistry(m)
	if m.skillTool != nil {
		p.rows = append([]skills.Skill(nil), m.skillTool.All...)
		p.enabled[name] = true
		m.skillTool.Enable(name)
	}
	p.tab = catalogTabOfficial
	p.cursor = 0
	for i, row := range p.visibleRows() {
		if row.Name == name {
			p.cursor = i
			break
		}
	}
	m.skillsPicker = p
	m.skillsPickerOpen = true
	return m
}

// uninstallFromSkillsPicker removes the cursor row's skill from
// ~/.yottacode/skills/ when invoked on the Installed tab. Built-ins
// surface a status hint instead — the user gets feedback in the
// same overlay rather than losing the menu to an error appendLine.
// On success the picker is rebuilt against the refreshed registry so
// the row disappears immediately.
func uninstallFromSkillsPicker(m Model, p *skillsPickerState, visible []skills.Skill) (Model, tea.Cmd) {
	if p.tab != catalogTabOfficial {
		p.status = "uninstall only works on installed Official rows"
		return m, nil
	}
	if len(visible) == 0 {
		return m, nil
	}
	sk := visible[p.cursor]
	installed, ok := p.installedSkill(sk.Name)
	if !ok {
		p.status = "only installed Official rows can be uninstalled here"
		return m, nil
	}
	sk = installed
	if sk.Source == skills.ScopeProject {
		// Project skills are committed source in <cwd>/.yottacode/skills/
		// — yottacode doesn't own that file; remove via git/rm.
		p.status = "project-scope skills must be removed via git/rm (committed source)"
		return m, nil
	}
	if _, err := skills.Uninstall(sk.Name); err != nil {
		p.status = "uninstall failed: " + err.Error()
		return m, nil
	}
	m.appendLine(styleAuto.Render("[skills] removed " + sk.Name))
	m = reloadSkillsRegistry(m)
	// Rebuild the picker's rows + cursor against the refreshed
	// universe so the just-removed skill drops out of view.
	if m.skillTool != nil {
		newRows := append([]skills.Skill(nil), m.skillTool.All...)
		p.rows = newRows
		visible = p.visibleRows()
		if p.cursor >= len(visible) && len(visible) > 0 {
			p.cursor = len(visible) - 1
		} else if len(visible) == 0 {
			p.cursor = 0
		}
		delete(p.enabled, sk.Name)
	}
	// Scrub the just-uninstalled name from persisted default_on if it
	// was there — otherwise next startup emits a "default_on
	// references unknown skill" warning for an entry the TUI just
	// removed. Best-effort: failure to save is non-fatal (uninstall
	// already succeeded on disk).
	if err := removeFromDefaultOn(sk.Name); err != nil {
		m.appendLine(styleError.Render("[skills] persistence scrub failed: " + err.Error()))
	}
	return m, nil
}

// removeFromDefaultOn drops name from the persisted default_on list
// if present, and rewrites the config file. No-op when the name
// isn't there or the config doesn't exist — both leave the saved
// state correct.
func removeFromDefaultOn(name string) error {
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	if len(cfg.Skills.DefaultOn) == 0 {
		return nil
	}
	kept := make([]string, 0, len(cfg.Skills.DefaultOn))
	changed := false
	for _, n := range cfg.Skills.DefaultOn {
		if n == name {
			changed = true
			continue
		}
		kept = append(kept, n)
	}
	if !changed {
		return nil
	}
	cfg.Skills.DefaultOn = kept
	return config.Save(cfg, "")
}

// commitSkillsPicker pushes the working enablement set into the live
// SkillTool, recomposes the system prompt so the next turn reflects
// the new "Available skills" section, persists the enabled set as
// `[skills] default_on` in config.toml so the next session restores
// it automatically, closes the picker, and reopens the parent
// /skills menu so navigation matches the sessions picker's "Esc
// returns to the parent menu" pattern.
//
// Persistence is best-effort: a save failure surfaces a warning in
// the transcript but does NOT roll back the in-memory enablement —
// the user's toggles take effect for the current session regardless,
// they just won't survive restart. Matches Claude Code's
// auto-persisting `/skills` and Hermes Agent's saved enablement
// model so explicit user choices don't evaporate on session boundary.
func commitSkillsPicker(m Model, p *skillsPickerState) (Model, tea.Cmd) {
	if m.skillTool == nil {
		m.skillsPickerOpen = false
		m.skillsPicker = nil
		m.openSkillsMenu()
		return m, nil
	}
	// Build the SetEnabled map: include only entries that are on so
	// SkillTool.Active() filters cleanly. Passing the raw map (which
	// includes false entries) would also work — SetEnabled drops
	// falses — but the explicit on-only set is easier to reason about
	// when debugging.
	on := make(map[string]bool, len(p.enabled))
	enabledCount := 0
	for name, isOn := range p.enabled {
		if isOn {
			on[name] = true
			enabledCount++
		}
	}
	m.skillTool.SetEnabled(on)
	// Recompose the session's system prompt so the next turn sees the
	// updated "Available skills" section. Reuses the same composition
	// path memory reload uses, sharing one builder ensures the two
	// can't drift.
	m, _ = recomposeSystemPromptWithSkills(m, m.skillTool.Active())
	m.skillsPickerOpen = false
	m.skillsPicker = nil
	total := len(p.rows)
	m.appendLine(styleAuto.Render(fmt.Sprintf("[skills] %d of %d enabled (next turn will see the updated set)", enabledCount, total)))
	if err := persistDefaultOn(on); err != nil {
		m.appendLine(styleError.Render("[skills] persistence failed: " + err.Error()))
	}
	m.openSkillsMenu()
	return m, nil
}

// persistDefaultOn writes the currently-enabled set into config.toml
// as `[skills] default_on`. Sorted for deterministic file output so
// reopening the file in git produces no spurious diffs.
//
// Reads the full config first so unrelated sections (theme,
// providers, retrieval, etc.) round-trip without being clobbered.
// Save is atomic (tmp + rename) so a crash mid-write can't leave
// the user with a half-written config.
func persistDefaultOn(on map[string]bool) error {
	names := make([]string, 0, len(on))
	for name := range on {
		names = append(names, name)
	}
	sort.Strings(names)
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	cfg.Skills.DefaultOn = names
	return config.Save(cfg, "")
}

// stageSkillBodyForPager writes the skill body to a temp file so it
// can be opened in $PAGER. Built-in skills don't have a real disk
// path (their SourcePath is "embed:..."), so we always stage to a
// temp file rather than try to open in place. Returns the staged
// path.
func stageSkillBodyForPager(sk skills.Skill) (string, error) {
	dir, err := os.MkdirTemp("", "yottacode-skill-")
	if err != nil {
		return "", err
	}
	// File name carries the skill slug so the pager title bar is
	// meaningful.
	path := filepath.Join(dir, sk.Name+".md")
	body := renderSkillForTranscript(sk)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// renderSkillsPicker draws the Catalog overlay. The header carries
// the tab bar (Built-in | Installed) so the user always sees which
// subset is rendered + which key (Tab) cycles. Each row is one skill
// with a check/uncheck glyph reflecting the working enablement
// state, the skill name, its source tag, and a truncated description.
// Cursor highlighting is handled by renderMenuItem.
func renderSkillsPicker(state *skillsPickerState, _ int) string {
	hint := "←/→ switches view · / filters · Enter installs · r refreshes catalog · Esc closes"
	if state.tab == catalogTabBundled {
		hint = "←/→ switches view · / filters · Space toggles · Enter views body · a/n · Esc saves and closes"
	}
	if state.filterMode {
		hint = "Type to filter · Backspace edits · Enter applies · Esc clears filter"
	}
	header := renderMenuHeader("Catalog", hint)
	// Selection semantics — shown above the tab bar on both tabs because the
	// checkbox is the model-autonomy gate; disk presence alone keeps slash
	// invocation alive either way. Keep the status legend inline so the Official
	// catalog's installed/enabled wording is self-explanatory.
	tabIntro := styleMeta.Render(
		"  Official status: not installed, installed, installed/enabled (available to the model this session). Bundled rows toggle model access.")
	body := header + "\n" + tabIntro + "\n\n" + renderSkillsTabs(state.tab) + "\n"
	if state.filter != "" || state.filterMode {
		filterLine := "  filter: " + state.filter
		if state.filterMode {
			// Trailing cursor block tells the user the buffer has
			// focus. Plain text only (no emoji per UI convention).
			filterLine += "_"
		}
		body += styleMeta.Render(filterLine) + "\n"
	}
	body += "\n"
	if state.busy != "" {
		frame := state.busyFrame
		if frame == "" {
			frame = "⣾"
		}
		body += styleMeta.Render("  "+frame+" "+state.busy) + "\n\n"
	}

	visible := state.visibleRows()
	if len(visible) == 0 {
		empty := "  official catalog unavailable or empty — try /skills install official/<name>"
		if state.tab == catalogTabBundled {
			empty = "  no bundled fallback skills compiled into this binary"
		}
		body += styleEmpty.Render(empty) + "\n"
		if state.status != "" {
			body += "\n" + styleMeta.Render("  "+state.status)
		}
		return body
	}
	// Compute the longest name within the *visible* set so columns
	// align per-tab. Cap at 32 to prevent a pathological 64-char
	// name from pushing the description column off the screen.
	maxName := 6
	for _, sk := range visible {
		if l := runeCount(sk.Name); l > maxName {
			maxName = l
		}
	}
	if maxName > 32 {
		maxName = 32
	}
	const sourceW = 19 // "[installed/enabled]" — widest status/source tag.

	for i, sk := range visible {
		mark := "[ ]"
		status := "not installed"
		if state.tab == catalogTabOfficial {
			if _, ok := state.installedSkill(sk.Name); ok {
				mark = "[x]"
				status = "installed"
				if state.enabled[sk.Name] {
					status = "installed/enabled"
				}
			}
		} else if state.enabled[sk.Name] {
			mark = "[x]"
		}
		// Pad by visual column count (rune count), not byte length.
		// Names that would overflow maxName are truncated by rune
		// position with an ellipsis suffix; this keeps the source
		// column aligned without bleeding into bytes-vs-runes confusion
		// downstream. We deliberately do NOT pass LabelWidth into
		// menuItemOpts — that path would re-truncate by len(label)
		// bytes and chop the source column when the name's ellipsis
		// is multi-byte.
		name := truncateRunes(sk.Name, maxName)
		namePad := strings.Repeat(" ", maxName-runeCount(name))
		source := "[" + string(sk.Source) + "]"
		if state.tab == catalogTabOfficial {
			source = "[" + status + "]"
		}
		sourcePad := strings.Repeat(" ", max(0, sourceW-runeCount(source)))
		label := mark + " " + name + namePad + " " + source + sourcePad
		body += renderMenuItem(menuItemOpts{
			Label:  label,
			Desc:   truncateForRender(sk.Description, 80),
			Cursor: i == state.cursor,
		}) + "\n"
	}
	if state.status != "" {
		body += "\n" + styleMeta.Render("  "+state.status)
	}
	return body
}

// renderSkillsTabs draws the three-tab bar shown above the catalog
// rows. Active tab is bright + bracketed; inactive is muted. Plain
// text only — no emoji, per the project's UI convention.
func renderSkillsTabs(active skillsCatalogTab) string {
	tabs := []struct {
		label string
		tab   skillsCatalogTab
	}{
		{"Official", catalogTabOfficial},
		{"Bundled", catalogTabBundled},
	}
	var parts []string
	for _, t := range tabs {
		if t.tab == active {
			parts = append(parts, lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("["+t.label+"]"))
		} else {
			parts = append(parts, styleMeta.Render(" "+t.label+" "))
		}
	}
	return "  " + strings.Join(parts, "  ")
}

// runeCount returns the number of Unicode codepoints in s. Using
// utf8.RuneCountInString here means our column math agrees with how
// the terminal renders the string, even when the string contains
// multi-byte runes like the ellipsis (`…` = 3 bytes / 1 column).
func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// truncateRunes returns at most n runes of s, suffixed with "…" when
// truncation happens. Distinct from truncateForRender (which counts
// bytes) so the picker's column layout doesn't get thrown off when a
// truncated string carries a multi-byte ellipsis.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	end := 0
	for i := range s {
		if count == n {
			break
		}
		count++
		end = i + 1
		// Advance end past the current rune. range on a string yields
		// the byte index of each rune's first byte; we need the byte
		// position one past the last byte of the nth rune.
		_ = i
	}
	if count < n || end == len(s) {
		return s
	}
	// Replace the last rune with "…" to make truncation visible.
	last := 0
	count = 0
	for i := range s {
		if count == n-1 {
			last = i
			break
		}
		count++
	}
	return s[:last] + "…"
}

// recomposeSystemPromptWithSkills rebuilds the session's system
// message so it reflects the currently-active skills. Mirrors the
// reloadMemoryNow path (cmd_memory.go): reload memory, compose the
// base prompt + provider note + active skills section + memory, and
// patch the session's first system message in place. Errors fall
// soft — we surface them to the user but never block the picker
// commit, since the SkillTool's enablement set has already been
// applied and will be respected on the next turn regardless.
func recomposeSystemPromptWithSkills(m Model, active []skills.Skill) (Model, tea.Cmd) {
	mem, err := memory.Load(m.cwd)
	if err != nil {
		m.appendLine(styleError.Render("[skills] memory reload failed: " + err.Error()))
		return m, nil
	}
	base := appendSkillsSection(composeSystemPrompt(m.baseSystemPrompt, m.providerProfile), active)
	newSys := memory.SystemPrompt(base, mem)
	// /skills is PreservesTurn, so this can run while a turn's agent
	// goroutine is appending to sess.Messages. Hold histMu across the
	// element write — an in-flight append that reallocates copies every
	// element, which would race this Content assignment.
	m.histMu.Lock()
	for i := range m.sess.Messages {
		if m.sess.Messages[i].Role == adapter.RoleSystem {
			m.sess.Messages[i].Content = newSys
			break
		}
	}
	m.histMu.Unlock()
	return m, nil
}
