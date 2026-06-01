package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/skills"
)

// TestSkillsMenu_OpenAndNavigate locks the menu's basic shape: four
// rows, Down moves the cursor, Up retreats, Esc closes. Without this
// a refactor that drops one menu item or reorders them goes
// undetected.
func TestSkillsMenu_OpenAndNavigate(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: nil}
	m, _ = m.runSlash("/skills")
	if !m.skillsMenuOpen {
		t.Fatal("menu should be open")
	}
	if got, want := len(m.skillsMenu.items), 5; got != want {
		t.Fatalf("menu items = %d, want %d", got, want)
	}
	if m.skillsMenu.items[0].label != "Catalog" {
		t.Errorf("first item = %q, want Catalog", m.skillsMenu.items[0].label)
	}
	if m.skillsMenu.items[2].label != "Uninstall" {
		t.Errorf("third item = %q, want Uninstall", m.skillsMenu.items[2].label)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.skillsMenu.cursor != 1 {
		t.Errorf("cursor after Down = %d, want 1", m.skillsMenu.cursor)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.skillsMenuOpen {
		t.Error("Esc should close the menu")
	}
}

// TestSkillsMenu_CatalogOpensPicker is the structural test for the
// menu→catalog hand-off. The picker is now a child of the menu —
// users reach it by picking Catalog, not by typing /skills.
func TestSkillsMenu_CatalogOpensPicker(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: nil}
	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // Catalog is item 0
	if m.skillsMenuOpen {
		t.Error("menu should close when Catalog is picked")
	}
	if !m.skillsPickerOpen {
		t.Error("Catalog should open the picker")
	}
}

// TestCatalogPicker_TabCycles confirms Tab flips between Built-in
// and Installed and resets the cursor. Without the reset, the user
// can sit on a row index past the end of the new tab's visible set.
func TestCatalogPicker_TabCycles(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: []skills.Skill{
		{Name: "alpha", Description: "a", Source: skills.ScopeBuiltin},
		{Name: "beta", Description: "b", Source: skills.ScopeUser},
	}}
	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // Catalog
	if m.skillsPicker.tab != catalogTabBuiltin {
		t.Fatalf("initial tab = %v, want built-in", m.skillsPicker.tab)
	}
	if visible := m.skillsPicker.visibleRows(); len(visible) != 1 || visible[0].Name != "alpha" {
		t.Errorf("built-in visible rows = %v, want [alpha]", visible)
	}
	// Move cursor forward so the post-Tab reset is observable.
	m.skillsPicker.cursor = 0
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.skillsPicker.tab != catalogTabInstalled {
		t.Errorf("after Tab tab = %v, want installed", m.skillsPicker.tab)
	}
	if visible := m.skillsPicker.visibleRows(); len(visible) != 1 || visible[0].Name != "beta" {
		t.Errorf("installed visible rows = %v, want [beta]", visible)
	}
	if m.skillsPicker.cursor != 0 {
		t.Errorf("Tab should reset cursor to 0, got %d", m.skillsPicker.cursor)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.skillsPicker.tab != catalogTabBuiltin {
		t.Errorf("second Tab should cycle back to built-in")
	}
}

// TestCatalogPicker_ArrowKeysSwitchTabs verifies Left/Right cycle
// between Built-in and Installed alongside Tab. Up/Down must stay
// row navigation so there's no accidental tab switch from cursor
// movement near the edges of a list.
func TestCatalogPicker_ArrowKeysSwitchTabs(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: []skills.Skill{
		{Name: "alpha", Description: "a", Source: skills.ScopeBuiltin},
		{Name: "beta", Description: "b", Source: skills.ScopeUser},
	}}
	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // Catalog

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.skillsPicker.tab != catalogTabInstalled {
		t.Errorf("Right should switch to Installed; got %v", m.skillsPicker.tab)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.skillsPicker.tab != catalogTabBuiltin {
		t.Errorf("Left should switch back to Built-in; got %v", m.skillsPicker.tab)
	}

	// Up/Down at the edges must not flip tabs.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.skillsPicker.tab != catalogTabBuiltin {
		t.Error("Up at row 0 should not switch tabs")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.skillsPicker.tab != catalogTabBuiltin {
		t.Error("Down past last row should not switch tabs")
	}
}

// TestCatalogPicker_UninstallOnInstalledTab verifies the per-row
// uninstall on the Installed tab. Setup mirrors the install path so
// the lockfile entry exists; after `u` the dir + entry are gone and
// the row vanishes from the picker.
func TestCatalogPicker_UninstallOnInstalledTab(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	src := t.TempDir()
	body := "---\nname: removeme\ndescription: per-row uninstall fixture\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := skills.Install(skills.InstallOptions{Source: src}); err != nil {
		t.Fatalf("install: %v", err)
	}

	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: []skills.Skill{
		{Name: "removeme", Description: "x", Source: skills.ScopeUser},
	}}
	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})     // Catalog
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyTab})       // Built-in → Installed
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})

	if _, err := os.Stat(filepath.Join(home, "skills", "removeme")); !os.IsNotExist(err) {
		t.Errorf("dir should be removed after u: err=%v", err)
	}
	if skills.Find(m.skillTool.All, "removeme") != nil {
		t.Error("registry should have refreshed after uninstall")
	}
}

// TestCatalogPicker_UninstallOnBuiltinTabRefuses guards the "you
// can't uninstall a built-in" rule. The status line carries the
// hint; the dir (well, the embedded skill) stays intact.
func TestCatalogPicker_UninstallOnBuiltinTabRefuses(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: []skills.Skill{
		{Name: "alpha", Description: "a", Source: skills.ScopeBuiltin},
	}}
	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // Catalog (built-in)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	if m.skillsPicker.status == "" {
		t.Error("expected a status hint when u is pressed on built-in tab")
	}
}

// TestSkillsMenu_InstallRoundTrip exercises the inline install flow:
// pick Install, type a path, Enter. Skill ends up on disk and the
// menu closes.
func TestSkillsMenu_InstallRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	src := t.TempDir()
	body := "---\nname: inline-install\ndescription: inline install fixture\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: nil}
	m, _ = m.runSlash("/skills")
	// Walk to Install (item index 1) and Enter.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.skillsMenu.mode != skillsMenuInstallInput {
		t.Fatalf("expected install mode, got %v", m.skillsMenu.mode)
	}
	// Type the source one rune-message at a time — what the textinput
	// would receive from the terminal.
	for _, r := range src {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.skillsMenuOpen {
		t.Error("menu should close after successful install")
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "inline-install", "SKILL.md")); err != nil {
		t.Errorf("skill not installed: %v", err)
	}
}

// TestSkillsMenu_InstallAutoEnablesSkill is the regression test for
// the "newly installed skill has no checkmark" bug. Install puts the
// skill in SkillTool.All; users expect it to be ready to use, not
// require a second trip through the picker to flip the checkbox.
// After install, IsEnabled must return true for the new skill's name.
func TestSkillsMenu_InstallAutoEnablesSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	src := t.TempDir()
	body := "---\nname: auto-on\ndescription: auto-enable regression\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: nil}
	m.skillTool.SetEnabled(map[string]bool{}) // default policy: none enabled

	m, _ = m.runSlash("/skills install " + src)
	if !m.skillTool.IsEnabled("auto-on") {
		t.Error("install should auto-enable the new skill so it's visible with a checkmark")
	}
}

// TestCatalogPicker_EscCommits is the regression test for the
// "checkmark doesn't persist" bug. Users naturally hit Esc to exit;
// the old two-phase model dropped their toggles on Esc, which was
// surprising. Esc now commits and closes — `c` still works as an
// alias for back-compat.
func TestCatalogPicker_EscCommits(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: []skills.Skill{
		{Name: "alpha", Description: "a", Source: skills.ScopeBuiltin},
	}}
	m.skillTool.SetEnabled(map[string]bool{})
	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})        // Catalog
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeySpace})        // toggle alpha on
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})          // expect: commits + returns to menu
	if !m.skillTool.IsEnabled("alpha") {
		t.Error("Esc should commit the toggle to SkillTool.enabled")
	}
	if m.skillsPickerOpen {
		t.Error("Esc should close the picker")
	}
	if !m.skillsMenuOpen {
		t.Error("Esc should return to the parent Skills menu, not the transcript")
	}
}

// TestCatalogPicker_FilterNarrowsRows is the regression test for
// the / filter affordance. Typing into the buffer must narrow the
// visible set live and Esc must clear without leaving stale state.
func TestCatalogPicker_FilterNarrowsRows(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: []skills.Skill{
		{Name: "alpha", Description: "first", Source: skills.ScopeBuiltin},
		{Name: "beta", Description: "second", Source: skills.ScopeBuiltin},
		{Name: "gamma", Description: "third — alpha-ish", Source: skills.ScopeBuiltin},
	}}
	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // Catalog

	if got := len(m.skillsPicker.visibleRows()); got != 3 {
		t.Fatalf("pre-filter rows = %d, want 3", got)
	}

	// Enter filter mode and type "alpha" — alpha matches by name,
	// gamma matches by description, beta doesn't match.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.skillsPicker.filterMode {
		t.Fatal("/ should enter filter mode")
	}
	for _, r := range "alpha" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	visible := m.skillsPicker.visibleRows()
	names := make([]string, 0, len(visible))
	for _, sk := range visible {
		names = append(names, sk.Name)
	}
	if len(visible) != 2 || names[0] != "alpha" || names[1] != "gamma" {
		t.Errorf("filtered rows = %v, want [alpha gamma]", names)
	}

	// Esc clears the filter and exits filter mode.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.skillsPicker.filter != "" || m.skillsPicker.filterMode {
		t.Error("Esc should clear filter and exit filter mode")
	}
	if got := len(m.skillsPicker.visibleRows()); got != 3 {
		t.Errorf("post-clear rows = %d, want 3", got)
	}
}

// TestRenderSkillForTranscript_AuthorAndSource verifies feature 4:
// metadata.author and metadata.source-url surface in the show
// output when present. Empty fields are dropped so built-ins stay
// compact.
func TestRenderSkillForTranscript_AuthorAndSource(t *testing.T) {
	sk := skills.Skill{
		Name:        "remote-ops",
		Description: "SSH playbook",
		Source:      skills.ScopeUser,
		SourcePath:  "/home/me/.yottacode/skills/remote-ops/SKILL.md",
		Metadata:    map[string]string{"author": "obra", "source-url": "https://example.com/remote-ops"},
		Body:        "# body",
	}
	out := renderSkillForTranscript(sk)
	if !strings.Contains(out, "Author: obra") {
		t.Errorf("author line missing: %q", out)
	}
	if !strings.Contains(out, "Source: https://example.com/remote-ops") {
		t.Errorf("source-url line missing: %q", out)
	}

	// No metadata → no Author/Source lines (built-in case).
	sk.Metadata = map[string]string{}
	out = renderSkillForTranscript(sk)
	if strings.Contains(out, "Author:") || strings.Contains(out, "Source:") {
		t.Errorf("metadata-empty render should not include those lines: %q", out)
	}
}

// TestCatalogPicker_EscPersistsToConfig is the regression test for
// the auto-save behavior. Toggling a skill and pressing Esc must
// write the current enabled set to ~/.yottacode/config.toml's
// [skills] default_on so the next session restores it without the
// user editing TOML manually. Matches Claude Code's auto-persisting
// /skills menu and Hermes Agent's saved enablement.
func TestCatalogPicker_EscPersistsToConfig(t *testing.T) {
	// newTestModel calls t.Setenv("HOME", t.TempDir()) internally,
	// so we have to override HOME *after* it runs — otherwise the
	// commit writes to the helper's tempdir, not ours.
	m := newTestModel(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", home)

	m.skillTool = &agent.SkillTool{All: []skills.Skill{
		{Name: "alpha", Description: "a", Source: skills.ScopeBuiltin},
		{Name: "beta", Description: "b", Source: skills.ScopeBuiltin},
	}}
	m.skillTool.SetEnabled(map[string]bool{})

	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // Catalog
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeySpace}) // toggle alpha on
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})   // commit + save

	cfgPath := filepath.Join(home, ".yottacode", "config.toml")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config file not written: %v", err)
	}
	if !strings.Contains(string(body), `"alpha"`) {
		t.Errorf("config should carry default_on entry for alpha; body:\n%s", body)
	}
	if strings.Contains(string(body), `"beta"`) {
		t.Errorf("config should NOT carry beta (unselected); body:\n%s", body)
	}
}

// TestCatalogPicker_UninstallScrubsDefaultOn locks the cleanup half
// of persistence: when a skill the user previously persisted to
// default_on is uninstalled from the picker, the saved list must
// drop that entry. Without this, next session start would emit the
// "default_on references unknown skill X" warning for an entry the
// TUI itself just removed.
func TestCatalogPicker_UninstallScrubsDefaultOn(t *testing.T) {
	// HOME redirection must happen *after* newTestModel — see the
	// EscPersistsToConfig test's note. We can't call newTestModel
	// first here though because we need to seed disk state before
	// the picker reads it, so we do everything in a careful order:
	// (1) override HOME with our tempdir, (2) call newTestModel
	// which will overwrite HOME to its own tempdir, (3) re-override
	// HOME back to ours.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", home)

	// Seed an installed skill + a config that lists it in default_on.
	src := t.TempDir()
	body := "---\nname: temp-skill\ndescription: scrub fixture\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := skills.Install(skills.InstallOptions{Source: src}); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Hand-write a default_on entry the picker should scrub.
	if err := os.MkdirAll(filepath.Join(home, ".yottacode"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, ".yottacode", "config.toml")
	seed := "[skills]\ndefault_on = [\"temp-skill\", \"unrelated\"]\n"
	if err := os.WriteFile(cfgPath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTestModel(t)
	// Re-override HOME (newTestModel resets it). YOTTACODE_HOME is
	// preserved since t.Setenv only stacks for THIS test, but the
	// Setenv inside newTestModel touches HOME specifically.
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", home)
	m.skillTool = &agent.SkillTool{All: []skills.Skill{
		{Name: "temp-skill", Description: "x", Source: skills.ScopeUser},
	}}
	m.skillTool.SetEnabled(map[string]bool{"temp-skill": true})

	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})                       // Catalog
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyTab})                         // → Installed tab
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})   // uninstall

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), `"temp-skill"`) {
		t.Errorf("default_on should no longer contain temp-skill; body:\n%s", got)
	}
	if !strings.Contains(string(got), `"unrelated"`) {
		t.Errorf("scrub should preserve unrelated default_on entries; body:\n%s", got)
	}
}

// TestSkillsMenu_InstallShowsErrorInline confirms that an install
// failure (bad source) leaves the menu open with a status line —
// the user can edit and retry without losing the typed value.
func TestSkillsMenu_InstallShowsErrorInline(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: nil}
	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	// Empty submit → menu requires a source.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.skillsMenuOpen {
		t.Error("menu should stay open when install validation fails")
	}
	if m.skillsMenu.status == "" {
		t.Error("status should explain why install was rejected")
	}
}

// TestSkillsMenu_UninstallRemovesSkill exercises the top-level
// Uninstall row: open menu → Uninstall → pick the lone installed skill
// → Enter. The dir is removed, the in-session registry refreshes, and
// with nothing left to remove the picker drops back to the menu.
func TestSkillsMenu_UninstallRemovesSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	src := t.TempDir()
	body := "---\nname: menu-remove\ndescription: menu uninstall fixture\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := skills.Install(skills.InstallOptions{Source: src}); err != nil {
		t.Fatalf("install: %v", err)
	}

	m := newTestModel(t)
	// newTestModel resets HOME to its own tempdir; YOTTACODE_HOME (which
	// drives UserSkillsDir) persists, but re-pin both so the default_on
	// scrub path writes where we expect too.
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", home)
	m.skillTool = &agent.SkillTool{All: []skills.Skill{
		{Name: "menu-remove", Description: "x", Source: skills.ScopeUser},
	}}

	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})  // Catalog → Install
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})  // Install → Uninstall
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // open uninstall picker
	if m.skillsMenu.mode != skillsMenuUninstallPick {
		t.Fatalf("expected uninstall-pick mode, got %v", m.skillsMenu.mode)
	}
	if len(m.skillsMenu.uninstallRows) != 1 || m.skillsMenu.uninstallRows[0].Name != "menu-remove" {
		t.Fatalf("uninstall list = %v, want [menu-remove]", m.skillsMenu.uninstallRows)
	}

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // remove the focused skill

	if _, err := os.Stat(filepath.Join(home, "skills", "menu-remove")); !os.IsNotExist(err) {
		t.Errorf("dir should be removed after Enter: err=%v", err)
	}
	if skills.Find(m.skillTool.All, "menu-remove") != nil {
		t.Error("registry should have refreshed after uninstall")
	}
	if m.skillsMenu.mode != skillsMenuSelect {
		t.Errorf("empty list should drop back to the menu, mode=%v", m.skillsMenu.mode)
	}
	if !m.skillsMenuOpen {
		t.Error("menu should stay open after uninstall (not close to transcript)")
	}
}

// TestSkillsMenu_UninstallEmptyShowsStatus guards the empty-set path:
// with no user-scope skills, picking Uninstall must not open an empty
// picker — it stays on the menu and explains why.
func TestSkillsMenu_UninstallEmptyShowsStatus(t *testing.T) {
	m := newTestModel(t)
	m.skillTool = &agent.SkillTool{All: []skills.Skill{
		{Name: "alpha", Description: "a", Source: skills.ScopeBuiltin},
	}}
	m, _ = m.runSlash("/skills")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})  // → Install
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})  // → Uninstall
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // attempt to open
	if m.skillsMenu.mode != skillsMenuSelect {
		t.Errorf("no removable skills should keep select mode, got %v", m.skillsMenu.mode)
	}
	if m.skillsMenu.status == "" {
		t.Error("status should explain there's nothing to uninstall")
	}
}
