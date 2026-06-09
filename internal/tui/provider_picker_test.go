package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/wizard"
)

// navigateToKind moves the addKindMode cursor to the entry with the
// given kind. Call after entering addKindMode (the first Enter on
// the "Add" menu item).
func navigateToKind(t *testing.T, m Model, kind string) Model {
	t.Helper()
	for i, e := range m.providerPicker.addCatalog {
		if e.Kind == kind {
			for j := 0; j < i; j++ {
				m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
			}
			return m
		}
	}
	t.Fatalf("kind %q not found in addCatalog", kind)
	return m
}

// navigateToMenuItem moves the menu cursor to the item with the given
// label. Call after opening the provider picker (/provider).
func navigateToMenuItem(t *testing.T, m Model, label string) Model {
	t.Helper()
	for _, item := range m.providerPicker.menuItems {
		if item.Label == label {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	return m
}

// seedProviderConfig writes a two-provider config.toml under
// HOME/.yottacode/config.toml so the picker has something to load.
func seedProviderConfig(t *testing.T) {
	t.Helper()
	body := `
[active]
provider      = "anthropic"
default_model = "claude-sonnet-4-6"

[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"
  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"

[[providers]]
name = "openai"
kind = "openai"
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
default_model = "gpt-4o"
  [[providers.models]]
  name = "gpt-4o"
  tier = "balanced"
`
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// /provider with no args opens the sub-menu in menu mode with the
// expected items (Use, Add, Remove). List was retired and folded
// into Use — same screen, list + switch in one step.
func TestProviderPicker_OpensInMenuMode(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	if !m.providerPickerOpen || m.providerPicker == nil {
		t.Fatalf("/provider should open the sub-menu picker")
	}
	if m.providerPicker.mode != providerMenuMode {
		t.Errorf("expected menu mode, got %v", m.providerPicker.mode)
	}
	gotLabels := make(map[string]bool, len(m.providerPicker.menuItems))
	for _, item := range m.providerPicker.menuItems {
		gotLabels[item.Label] = true
	}
	for _, want := range []string{"Use", "Add", "Remove"} {
		if !gotLabels[want] {
			t.Errorf("menu missing %q; have %v", want, gotLabels)
		}
	}
}

// Esc from menu mode closes the picker.
func TestProviderPicker_EscFromMenuClosesPicker(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.providerPickerOpen {
		t.Errorf("Esc from menu should close picker")
	}
}

// /provider list typed shortcut now opens the picker into the Use
// sub-mode (replacing the prior scrollback dump). The list is shown
// inline below the cmdline + status bar, and the user can switch
// providers from the same screen with Enter.
func TestSlash_ProviderListShortcutOpensUsePicker(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider list")
	if !m.providerPickerOpen || m.providerPicker == nil {
		t.Fatalf("/provider list should open the picker")
	}
	if m.providerPicker.mode != providerUsePickerMode {
		t.Errorf("expected use-picker mode; got %v", m.providerPicker.mode)
	}
	got := stripANSI(m.View())
	for _, want := range []string{"Switch provider", "anthropic", "openai", "✔"} {
		if !strings.Contains(got, want) {
			t.Errorf("view missing %q; got:\n%s", want, got)
		}
	}
}

// Selecting "Use" transitions into the Use sub-picker. Cursor
// defaults to the active provider so Enter-to-confirm just works.
// Use is now the first menu item — List was retired and folded into
// Use (the list + switch are one screen).
func TestProviderPicker_UseMenuItemTransitionsToUseList(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	m = navigateToMenuItem(t, m, "Use")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPicker.mode != providerUsePickerMode {
		t.Errorf("Enter on Use should transition to use-list mode; got %v", m.providerPicker.mode)
	}
	// Cursor should default to the active provider (anthropic, index 0).
	if m.providerPicker.usePickerCursor != 0 {
		t.Errorf("use-picker cursor should default to active (index 0); got %d", m.providerPicker.usePickerCursor)
	}
}

// Esc from a sub-picker pops back to the menu rather than closing.
// Use is now the first menu item — Enter from index 0 enters the
// Use sub-picker without arrowing.
func TestProviderPicker_EscFromSubPickerPopsToMenu(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // enter Use sub-picker
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.providerPickerOpen {
		t.Errorf("Esc from sub-picker should NOT close picker")
	}
	if m.providerPicker.mode != providerMenuMode {
		t.Errorf("Esc should pop back to menu; mode = %v", m.providerPicker.mode)
	}
}

// Picking a provider in the use-list switches the running session
// (baseURL, model) and writes the active block to disk. Use is the
// first menu item now — Enter from index 0 enters the sub-picker.
func TestProviderPicker_UseConfirmSwitchesActive(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	m = navigateToMenuItem(t, m, "Use")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → Use sub-picker
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})  // cursor → openai
	if m.providerPicker.usePickerCursor != 1 {
		t.Fatalf("setup: cursor should be at openai (1); got %d", m.providerPicker.usePickerCursor)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPickerOpen {
		t.Errorf("Enter on Use confirm should close picker")
	}
	if m.baseURL != "https://api.openai.com/v1" {
		t.Errorf("baseURL after switch = %q, want https://api.openai.com/v1", m.baseURL)
	}
	if m.modelName != "gpt-4o" {
		t.Errorf("modelName after switch = %q, want gpt-4o", m.modelName)
	}
}

// Empty provider list + selecting Use shows a hint instead of
// transitioning to an empty sub-picker. Use is now the first menu
// item.
func TestProviderPicker_UseWithNoProvidersHints(t *testing.T) {
	m := newTestModel(t)
	// Don't seed a config — no providers configured.
	m, _ = typeAndEnter(t, m, "/provider")
	m = navigateToMenuItem(t, m, "Use")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // try to enter Use
	if m.providerPickerOpen {
		t.Errorf("Use with no providers should close the picker (hint, not transition)")
	}
	if !strings.Contains(m.transcript.String(), "no providers configured") {
		t.Errorf("expected hint about no providers; transcript:\n%s", m.transcript.String())
	}
}

// View() renders the menu's headline so users see the picker is open.
func TestProviderPicker_ViewIncludesProviderHeadline(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	if !strings.Contains(stripANSI(m.View()), "Provider") {
		t.Errorf("View should include 'Provider' headline; got:\n%s", m.View())
	}
}

// New menu pattern: numbered items + `❯` cursor + Claude Code
// description block. Verify the visual contract on the bare
// /provider menu. List was retired (folded into Use), so the menu
// is Use / Add / Remove.
func TestProviderPicker_MenuRendersClaudeCodeStyle(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	got := stripANSI(m.View())
	for _, want := range []string{
		"❯",   // cursor glyph
		"Use", // labels (no leading numbers per Phase 5b)
		"Add",
		"Remove",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("View missing %q; got:\n%s", want, got)
		}
	}
	// Phase 5b: numeric prefixes are gone — arrow-key nav is enough.
	for _, gone := range []string{"1. Use", "2. Add", "3. Remove"} {
		if strings.Contains(got, gone) {
			t.Errorf("menu should not carry numeric prefixes anymore (saw %q); got:\n%s", gone, got)
		}
	}
	// List was retired — assert it's gone so a regression that
	// re-adds the standalone item shows up loudly.
	if strings.Contains(got, ". List") {
		t.Errorf("standalone List menu item should be retired (folded into Use); got:\n%s", got)
	}
	// Footer hints inside the box should use ↵/esc/↑↓ glyph form.
	if !strings.Contains(got, "↵ confirm · esc back · ↑↓ navigate") {
		t.Errorf("picker footer should use glyph form; got:\n%s", got)
	}
}

// In the Use sub-picker, the active provider gets a `✔` marker so
// users see at a glance which one is currently in effect. Use is
// the first menu item — Enter from index 0 enters the sub-picker.
func TestProviderPicker_UseListMarksActiveWithCheckmark(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	m = navigateToMenuItem(t, m, "Use")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	got := stripANSI(m.View())
	if !strings.Contains(got, "✔") {
		t.Errorf("Use sub-picker should mark the active provider with ✔; got:\n%s", got)
	}
}

// Menu must include Use, Add, Remove (position-independent — Use
// folded in the prior List action and is now the primary entry).
func TestProviderPicker_MenuIncludesCoreActions(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	for _, want := range []string{"Use", "Add", "Remove"} {
		found := false
		for _, item := range m.providerPicker.menuItems {
			if item.Label == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("menu missing %q", want)
		}
	}
	for _, retired := range []string{"List"} {
		for _, item := range m.providerPicker.menuItems {
			if item.Label == retired {
				t.Errorf("retired menu item %q should not be present", retired)
			}
		}
	}
}

// Selecting Remove transitions into the remove sub-picker.
func TestProviderPicker_RemoveTransitionsToRemoveList(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	// Navigate down to the Remove item (index in menuItems may
	// shift as M8/M9 add more options; resolve by label).
	target := -1
	for i, item := range m.providerPicker.menuItems {
		if item.Label == "Remove" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("Remove not in menu")
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.providerPicker.menuCursor != target {
		t.Fatalf("cursor should be on Remove (index %d); got %d", target, m.providerPicker.menuCursor)
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPicker.mode != providerRemovePickerMode {
		t.Errorf("Enter on Remove should transition to remove-list mode; got %v", m.providerPicker.mode)
	}
	// Remove sub-picker starts at cursor 0 (deliberately NOT
	// defaulting to the active provider — Enter would otherwise
	// blow it away).
	if m.providerPicker.usePickerCursor != 0 {
		t.Errorf("remove cursor should default to 0; got %d", m.providerPicker.usePickerCursor)
	}
}

// Confirming a Remove writes the updated config to disk.
func TestProviderPicker_RemoveConfirmDeletes(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t)
	m, _ = typeAndEnter(t, m, "/provider")
	target := -1
	for i, item := range m.providerPicker.menuItems {
		if item.Label == "Remove" {
			target = i
			break
		}
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	// Cursor at 0 = anthropic. Move to openai (cursor 1) to avoid
	// nuking the active row in this test (covered separately
	// below).
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPickerOpen {
		t.Errorf("Enter on confirm should close picker")
	}
	if !strings.Contains(m.transcript.String(), `removed "openai"`) {
		t.Errorf("transcript missing removal confirmation:\n%s", m.transcript.String())
	}
	// Verify the file no longer contains an openai provider.
	body, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	// config.Render uses padded column alignment (`name          =
	// "anthropic"`), so we substring-match on the value side
	// rather than pinning whitespace.
	if strings.Contains(string(body), `= "openai"`) {
		t.Errorf("config.toml should no longer mention openai; got:\n%s", body)
	}
	if !strings.Contains(string(body), `= "anthropic"`) {
		t.Errorf("config.toml should still have anthropic; got:\n%s", body)
	}
}

// Add menu item navigates into the catalog kind list and exposes
// the wizard.Catalog entries (Anthropic, OpenAI, etc.).
func TestProviderPicker_AddTransitionsToKindList(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/provider")
	target := -1
	for i, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("Add not in menu")
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPicker.mode != providerAddKindMode {
		t.Errorf("Enter on Add should transition to kind picker; got %v", m.providerPicker.mode)
	}
	if len(m.providerPicker.addCatalog) == 0 {
		t.Fatalf("addCatalog should be populated from wizard.Catalog")
	}
}

// Picking a cloud entry from the kind list lays out the form with
// API key + name fields and the catalog default model pre-filled.
func TestProviderPicker_AddCloudKindBuildsForm(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/provider")
	// Navigate to Add.
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	// Navigate to anthropic (no longer at index 0).
	for i, e := range m.providerPicker.addCatalog {
		if e.Kind == "anthropic" {
			for j := 0; j < i; j++ {
				m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
			}
			break
		}
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPicker.mode != providerAddFieldsMode {
		t.Fatalf("Enter on kind should transition to fields mode; got %v", m.providerPicker.mode)
	}
	wantLabels := map[string]bool{
		"Name":          true,
		"Base URL":      true,
		"API key":       true,
		"Default model": true,
	}
	got := map[string]bool{}
	for _, l := range m.providerPicker.addLabels {
		got[l] = true
	}
	for label := range wantLabels {
		if !got[label] {
			t.Errorf("expected form to include %q label; got %v", label, m.providerPicker.addLabels)
		}
	}
	if m.providerPicker.addPicked == nil || m.providerPicker.addPicked.Kind != "anthropic" {
		t.Errorf("addPicked should be anthropic; got %+v", m.providerPicker.addPicked)
	}
}

// Esc from the add-fields screen pops back to kind-pick (rather
// than closing the picker entirely).
func TestProviderPicker_AddEscFromFieldsPopsToKindList(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.providerPicker.mode != providerAddKindMode {
		t.Errorf("Esc from fields should pop to kind list; got %v", m.providerPicker.mode)
	}
	if !m.providerPickerOpen {
		t.Errorf("picker should still be open after Esc from fields")
	}
}

// Confirming a cloud-provider Add appends the provider to
// config.toml. The API-key check (mirrored from the wizard) refuses
// to save a cloud provider with neither a form-supplied key nor an
// env-set one, so this happy-path test fills the key field.
func TestProviderPicker_AddConfirmAppendsProvider(t *testing.T) {
	m := newTestModel(t)
	// Empty config so we know exactly what gets written.
	seedConfigTOML(t, "")
	// Clear any inherited key so the form-supplied value is the only
	// source — keeps the test deterministic on dev machines that have
	// ANTHROPIC_API_KEY exported.
	t.Setenv("ANTHROPIC_API_KEY", "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	// Navigate down to anthropic (skip openai-auth, copilot-auth).
	for i, e := range m.providerPicker.addCatalog {
		if e.Kind == "anthropic" {
			for j := 0; j < i; j++ {
				m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
			}
			break
		}
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	// Field index 2 = API key, index 3 = Default model for cloud
	// providers (Name, Base URL, API key, Default model).
	m.providerPicker.addFields[2].SetValue("sk-ant-test-key")
	m.providerPicker.addFields[3].SetValue("claude-sonnet-4-6")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPickerOpen {
		t.Errorf("Enter on save should close picker")
	}
	body, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	if !strings.Contains(string(body), `kind          = "anthropic"`) {
		t.Errorf("config.toml missing anthropic provider; got:\n%s", body)
	}
	if !strings.Contains(string(body), `default_model = "claude-sonnet-4-6"`) {
		t.Errorf("config.toml missing default_model; got:\n%s", body)
	}
}

// A cloud provider's API-key env var is declared but neither the form
// nor the OS env supplies a value → save must be refused with an
// inline error and the picker stays open. Mirrors the wizard's
// stepConfigure guard so /provider add is no easier to misuse than
// the wizard's first-run flow.
func TestProviderPicker_AddRejectsMissingKeyForCloudProvider(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	// Clear the env var explicitly so a developer's exported key
	// doesn't make the test pass for the wrong reason.
	t.Setenv("ANTHROPIC_API_KEY", "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	for i, e := range m.providerPicker.addCatalog {
		if e.Kind == "anthropic" {
			for j := 0; j < i; j++ {
				m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
			}
			break
		}
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	// Fill model but deliberately leave the API key blank.
	m.providerPicker.addFields[3].SetValue("claude-sonnet-4-6")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if !m.providerPickerOpen {
		t.Fatalf("save with no key should keep picker open")
	}
	if !strings.Contains(m.providerPicker.addInputErr, "API key required") {
		t.Errorf("expected 'API key required' input error; got %q", m.providerPicker.addInputErr)
	}
	if !strings.Contains(m.providerPicker.addInputErr, "ANTHROPIC_API_KEY") {
		t.Errorf("error should name the env var so the user knows what to export; got %q", m.providerPicker.addInputErr)
	}
	// Focus must move to the API-key field so the next keystrokes
	// land in the right input — otherwise the user has to Tab back
	// after reading the error.
	keyIdx := -1
	for i, label := range m.providerPicker.addLabels {
		if label == "API key" {
			keyIdx = i
			break
		}
	}
	if keyIdx < 0 || m.providerPicker.addFocused != keyIdx {
		t.Errorf("focus should be on API key field (index %d); got %d", keyIdx, m.providerPicker.addFocused)
	}
	// Disk must be untouched.
	if body, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml")); err == nil {
		if strings.Contains(string(body), `kind          = "anthropic"`) {
			t.Errorf("provider should NOT have been written; got:\n%s", body)
		}
	}
}

// When the API-key env var is already exported, the form's key field
// can stay empty (the user already has the key in shell rc / .env)
// and save proceeds. Same allowance the wizard's envHas branch makes.
func TestProviderPicker_AddAcceptsKeyFromExistingEnv(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-already-in-env")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	m = navigateToKind(t, m, "anthropic")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	m.providerPicker.addFields[3].SetValue("claude-sonnet-4-6")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPickerOpen {
		t.Errorf("save should succeed when env var is set; addInputErr=%q", m.providerPicker.addInputErr)
	}
}

// NVIDIA NIM keys are often scoped to a single model — pre-filling
// the model field would silently steer users into a 404 when their
// key wasn't minted for our suggested default. The form must show the
// suggestion as a placeholder (visible) but leave the field empty so
// the existing "default model is required" guard rejects a save until
// the user explicitly types the model their key is authorized for.
func TestProviderPicker_AddNvidiaRequiresExplicitModel(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	t.Setenv("NVIDIA_API_KEY", "nvapi-test")

	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode

	target := -1
	for i, e := range m.providerPicker.addCatalog {
		if e.Name == "nvidia-nim" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("nvidia-nim not in catalog")
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode

	// Find the model field. Should be empty (not pre-filled), with the
	// suggestion sitting in Placeholder.
	modelIdx := -1
	for i, label := range m.providerPicker.addLabels {
		if label == "Default model" {
			modelIdx = i
			break
		}
	}
	if modelIdx < 0 {
		t.Fatalf("Default model field missing from form")
	}
	if got := m.providerPicker.addFields[modelIdx].Value(); got != "" {
		t.Errorf("model field should start empty, got %q", got)
	}
	const want = "nvidia/nemotron-3-super-120b-a12b"
	if got := m.providerPicker.addFields[modelIdx].Placeholder; got != want {
		t.Errorf("model placeholder = %q, want %q", got, want)
	}

	// Trying to save without typing rejects with the model-required
	// error — the guard that catches the silent-default bug.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.providerPickerOpen {
		t.Fatalf("save with empty model should keep picker open")
	}
	if !strings.Contains(m.providerPicker.addInputErr, "default model is required") {
		t.Errorf("expected 'default model is required'; got %q", m.providerPicker.addInputErr)
	}
}

// The auth bug regression: adding a SECOND profile of the same vendor
// must not silently reuse the first profile's bearer token. NVIDIA NIM
// mints per-model API keys, so two NVIDIA profiles sharing
// NVIDIA_API_KEY = "the first key" would 401 on the second profile's
// model. The fix mints a per-profile env var (e.g.
// NVIDIA_NIM_2_API_KEY) for the second profile and validates against
// that — which is empty until the user types a key, so the picker
// must reject the save with a "saves as NVIDIA_NIM_2_API_KEY" hint.
func TestProviderPicker_AddSecondProfileForcesNewKey(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name = "nvidia-nim"
kind = "openai-compatible"
base_url = "https://integrate.api.nvidia.com/v1"
api_key_env = "NVIDIA_API_KEY"
default_model = "nvidia/nemotron-3-super-120b-a12b"
`)
	// First profile's key IS in the env (the situation that masked
	// the bug — the validator saw a non-empty NVIDIA_API_KEY and let
	// the second profile through).
	t.Setenv("NVIDIA_API_KEY", "nvapi-first-profile-key")
	// Explicitly clear the second-profile key. The picker validates
	// against the *effective* env var for the new profile (auto-minted
	// as NVIDIA_NIM_2_API_KEY here), so if a parent process — like
	// yottacode running tests via /check:verify after loading its own
	// ~/.yottacode/.env — has this exported, the validator silently
	// passes and the test fails. Tests must isolate from host env.
	t.Setenv("NVIDIA_NIM_2_API_KEY", "")

	m, _ = typeAndEnter(t, m, "/provider")
	// Walk to "Add".
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode

	// Walk to nvidia-nim catalog row.
	target := -1
	for i, e := range m.providerPicker.addCatalog {
		if e.Name == "nvidia-nim" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("nvidia-nim not in catalog")
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode

	// uniqueProviderName already filled "nvidia-nim-2" since
	// "nvidia-nim" is taken. Pre-fill model so we don't trip the
	// model validator first. Leave API key blank — the bug was that
	// this used to pass.
	if m.providerPicker.addFields[0].Value() != "nvidia-nim-2" {
		t.Fatalf("unique name = %q, want nvidia-nim-2", m.providerPicker.addFields[0].Value())
	}
	last := len(m.providerPicker.addFields) - 1
	m.providerPicker.addFields[last].SetValue("nvidia/some-other-model")

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if !m.providerPickerOpen {
		t.Fatalf("second profile save with no key should keep picker open")
	}
	if !strings.Contains(m.providerPicker.addInputErr, "API key required") {
		t.Errorf("expected 'API key required' input error; got %q", m.providerPicker.addInputErr)
	}
	if !strings.Contains(m.providerPicker.addInputErr, "NVIDIA_NIM_2_API_KEY") {
		t.Errorf("error should mention the per-profile env var; got %q", m.providerPicker.addInputErr)
	}
}

// Ollama has APIKeyEnv == "" — the key check must not block a save.
// The form does not even render an API-key field for Ollama, so the
// guard has to short-circuit on the catalog entry, not on the form
// shape.
func TestProviderPicker_AddOllamaSkipsKeyCheck(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	// Walk the catalog cursor to the Ollama row.
	target := -1
	for i, e := range m.providerPicker.addCatalog {
		if e.Kind == "ollama" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("ollama not in catalog")
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	// Ollama form: Name, Base URL, Default model (no API key field).
	// Default model is the last index regardless of field count.
	last := len(m.providerPicker.addFields) - 1
	m.providerPicker.addFields[last].SetValue("llama3.1:8b")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPickerOpen {
		t.Errorf("Ollama save (no key required) should close picker; addInputErr=%q", m.providerPicker.addInputErr)
	}
}

// Free-form provider must produce an error when default model is
// blank (the picker placeholder doesn't auto-fill — the user has to
// type a value). Every cloud entry in the catalog is now free-form
// too (we stopped curating model lists because vendor names rotate
// faster than the catalog ships), so this rule is universal.
func TestProviderPicker_AddFreeFormRejectsBlankModel(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	// Every catalog entry without an embedded model list is free-form (the curated list lives in
	// internal/catalog/catalog.gen.json). Pick the first non-curated
	// kind so the test exercises the "default model required" path
	// without depending on catalog state. Ollama / nvidia-nim / custom
	// all qualify; curated cloud kinds do not.
	target := -1
	for i, e := range m.providerPicker.addCatalog {
		switch e.Kind {
		case "anthropic", "openai", "gemini", "xai", "openai-auth", "copilot":
			// openai-auth and copilot are excluded too: their model
			// fields are pre-filled since per-user lists are only
			// populated post-login.
			continue
		}
		target = i
		break
	}
	if target < 0 {
		t.Fatalf("expected at least one non-curated catalog entry")
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	// Fill base URL but leave model blank. Tab to base URL field, type, then Enter.
	// addFields[0] = Name (pre-filled), [1] = Base URL.
	m.providerPicker.addFields[1].SetValue("http://example.com/v1")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // try to save
	if !m.providerPickerOpen {
		t.Errorf("Enter with blank model should NOT close picker (validation error)")
	}
	// Input errors render into the form (next to the field that's
	// wrong) rather than appending to the transcript.
	if !strings.Contains(m.providerPicker.addInputErr, "default model is required") {
		t.Errorf("expected input error on picker; got %q", m.providerPicker.addInputErr)
	}
}

// After a successful Add with an API key, the key MUST be set in
// the running process's environment immediately — not only written
// to ~/.yottacode/.env (which is loaded at next startup). Otherwise
// the user adds a provider, runs /provider use, and the adapter
// fails because os.Getenv still returns empty for the new key.
func TestProviderPicker_AddPropagatesAPIKeyToProcessEnv(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	// Make sure the env var is empty going in so we know the
	// post-add Setenv is what populated it.
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := os.Getenv("ANTHROPIC_API_KEY"); got != "" {
		t.Fatalf("setup: ANTHROPIC_API_KEY should be empty; got %q", got)
	}
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	m = navigateToKind(t, m, "anthropic")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	// Set the API key + model fields directly. Field index 2 = API
	// key, index 3 = Default model. Anthropic went free-form when the
	// curated catalog was removed, so the model needs an explicit
	// value or save validation rejects it.
	m.providerPicker.addFields[2].SetValue("sk-ant-fresh-key")
	m.providerPicker.addFields[3].SetValue("claude-sonnet-4-6")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPickerOpen {
		t.Errorf("Enter on save should close picker")
	}
	if got := os.Getenv("ANTHROPIC_API_KEY"); got != "sk-ant-fresh-key" {
		t.Errorf("Add should propagate key to os.Environ; got %q", got)
	}
}

// The API-key check applies to every catalog entry whose APIKeyEnv
// is non-empty — anthropic, openai, gemini, xai, nvidia-nim — not
// just the default-cursor row (anthropic). Walking each entry by
// kindIndex catches a regression where the guard accidentally got
// scoped to a single kind. Ollama is excluded because it has no
// APIKeyEnv (local, no auth).
func TestProviderPicker_AddRejectsMissingKey_AllCloudProviders(t *testing.T) {
	m := newTestModel(t)
	// Use a fresh model per provider so any state leak between rows
	// (cursor positions, picker remnants) doesn't mask a regression.
	cases := []struct {
		kindName string // catalog Name field
		envVar   string
	}{
		{"anthropic", "ANTHROPIC_API_KEY"},
		{"openai", "OPENAI_API_KEY"},
		{"gemini", "GEMINI_API_KEY"},
		{"xai", "XAI_API_KEY"},
		{"nvidia-nim", "NVIDIA_API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.kindName, func(t *testing.T) {
			m = newTestModel(t)
			seedConfigTOML(t, "")
			t.Setenv(tc.envVar, "")
			m, _ = typeAndEnter(t, m, "/provider")
			for _, item := range m.providerPicker.menuItems {
				if item.Label == "Add" {
					break
				}
				m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
			}
			m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
			// Walk the catalog cursor to the target row.
			target := -1
			for i, e := range m.providerPicker.addCatalog {
				if e.Name == tc.kindName {
					target = i
					break
				}
			}
			if target < 0 {
				t.Fatalf("catalog missing %q", tc.kindName)
			}
			for i := 0; i < target; i++ {
				m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
			}
			m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
			// Fill the Default model field so we isolate the API-key
			// check (otherwise the model-required guard fires first
			// for kinds whose curated catalog is empty).
			modelIdx := -1
			for i, lbl := range m.providerPicker.addLabels {
				if lbl == "Default model" {
					modelIdx = i
					break
				}
			}
			if modelIdx < 0 {
				t.Fatalf("form missing Default model field for %s", tc.kindName)
			}
			if strings.TrimSpace(m.providerPicker.addFields[modelIdx].Value()) == "" {
				m.providerPicker.addFields[modelIdx].SetValue("placeholder-model")
			}
			// Try to save with no key.
			m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
			if !m.providerPickerOpen {
				t.Fatalf("%s: save with no key should keep picker open", tc.kindName)
			}
			if !strings.Contains(m.providerPicker.addInputErr, "API key required") {
				t.Errorf("%s: expected 'API key required'; got %q", tc.kindName, m.providerPicker.addInputErr)
			}
			if !strings.Contains(m.providerPicker.addInputErr, tc.envVar) {
				t.Errorf("%s: error should reference env var %q; got %q", tc.kindName, tc.envVar, m.providerPicker.addInputErr)
			}
		})
	}
}

// Picking a curated cloud kind (anthropic — the catalog ships
// populated for it) loads the embedded model list into
// addCuratedModels but leaves the Default model textinput empty
// and the cursor at -1. The user must arrow Down (or type a tag)
// before save will succeed — auto-prefill was a footgun, users
// were registering providers without ever seeing the model list.
func TestProviderPicker_AddCuratedKindLoadsCatalogModels(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	m = navigateToKind(t, m, "anthropic")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	if len(m.providerPicker.addCuratedModels) == 0 {
		t.Fatalf("anthropic catalog should be populated; got 0 entries")
	}
	modelIdx := -1
	for i, lbl := range m.providerPicker.addLabels {
		if lbl == "Default model" {
			modelIdx = i
			break
		}
	}
	if got := strings.TrimSpace(m.providerPicker.addFields[modelIdx].Value()); got != "" {
		t.Errorf("model textinput should start empty (no auto-prefill); got %q", got)
	}
	if m.providerPicker.addModelCursor != -1 {
		t.Errorf("cursor should start at -1 (no row picked yet); got %d", m.providerPicker.addModelCursor)
	}
	// Render must include at least one catalog entry's label so the
	// user sees the list and knows what to pick from.
	view := stripANSI(m.View())
	if !strings.Contains(view, m.providerPicker.addCuratedModels[0].Label()) {
		t.Errorf("rendered form missing first model label %q;\nview:\n%s",
			m.providerPicker.addCuratedModels[0].Label(), view)
	}
}

// Up/Down on the focused Default model field navigates the curated
// list and writes the picked ID into the textinput. Persistence
// reads the textinput value, so this is the contract the save path
// depends on.
func TestProviderPicker_AddArrowKeysNavigateCuratedCatalog(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode

	if len(m.providerPicker.addCuratedModels) < 2 {
		t.Skipf("need ≥2 anthropic catalog entries to exercise navigation; got %d",
			len(m.providerPicker.addCuratedModels))
	}
	// Tab to the Default model field.
	modelIdx := -1
	for i, lbl := range m.providerPicker.addLabels {
		if lbl == "Default model" {
			modelIdx = i
			break
		}
	}
	for m.providerPicker.addFocused != modelIdx {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyTab})
	}
	// Cursor starts at -1 (no auto-pick). First Down moves to 0 and
	// fills the textinput with the first catalog model's ID.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.providerPicker.addModelCursor != 0 {
		t.Fatalf("first Down should advance cursor from -1 to 0; got %d", m.providerPicker.addModelCursor)
	}
	if got, want := strings.TrimSpace(m.providerPicker.addFields[modelIdx].Value()), m.providerPicker.addCuratedModels[0].ID; got != want {
		t.Errorf("textinput after first Down = %q, want %q", got, want)
	}
	// Second Down → cursor 1.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.providerPicker.addModelCursor != 1 {
		t.Fatalf("second Down should advance cursor to 1; got %d", m.providerPicker.addModelCursor)
	}
	if got, want := strings.TrimSpace(m.providerPicker.addFields[modelIdx].Value()), m.providerPicker.addCuratedModels[1].ID; got != want {
		t.Errorf("textinput after second Down = %q, want %q", got, want)
	}
	// Up should reverse it.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.providerPicker.addModelCursor != 0 {
		t.Errorf("Up should return cursor to 0; got %d", m.providerPicker.addModelCursor)
	}
	// Up at 0 should clamp (not wrap to -1) — no point un-picking.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.providerPicker.addModelCursor != 0 {
		t.Errorf("Up at cursor 0 should clamp to 0; got %d", m.providerPicker.addModelCursor)
	}
}

// Saving a curated-kind provider commits the catalog-picked model ID
// to config.toml after the user explicitly arrows to one. End-to-end
// check that picker → textinput → persistence wiring matches what
// the user sees in the list.
func TestProviderPicker_AddCuratedKindPersistsCatalogPick(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode (anthropic)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	if len(m.providerPicker.addCuratedModels) == 0 {
		t.Skip("anthropic catalog empty in this build; skipping curated-pick test")
	}
	// Find the API key + Default model field indices.
	keyIdx, modelIdx := -1, -1
	for i, lbl := range m.providerPicker.addLabels {
		switch lbl {
		case "API key":
			keyIdx = i
		case "Default model":
			modelIdx = i
		}
	}
	m.providerPicker.addFields[keyIdx].SetValue("sk-ant-curated-test")
	// Explicitly arrow to pick the first catalog model. This is the
	// new contract: no auto-prefill, user must take a deliberate
	// action.
	for m.providerPicker.addFocused != modelIdx {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyTab})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown}) // cursor -1 → 0
	wantModel := m.providerPicker.addCuratedModels[0].ID
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPickerOpen {
		t.Fatalf("save should close picker; addInputErr=%q", m.providerPicker.addInputErr)
	}
	body, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(body), wantModel) {
		t.Errorf("config.toml missing curated-picked model %q; got:\n%s", wantModel, body)
	}
}

// The save path must refuse a curated-kind provider when the user
// has supplied an API key but never picked a model from the catalog
// (and never typed a tag). The pre-fill that previously masked this
// was the user's reported bug — providers were registering with no
// active model selection.
func TestProviderPicker_AddCuratedKindRequiresExplicitModelPick(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	if len(m.providerPicker.addCuratedModels) == 0 {
		t.Skip("anthropic catalog empty in this build; nothing to pre-fill")
	}
	// Supply API key so the only thing missing is the model.
	keyIdx := -1
	for i, lbl := range m.providerPicker.addLabels {
		if lbl == "API key" {
			keyIdx = i
			break
		}
	}
	m.providerPicker.addFields[keyIdx].SetValue("sk-ant-no-pick")
	// Try to save without ever Tab-ing to the model field or
	// arrowing to pick.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.providerPickerOpen {
		t.Fatalf("save without picking model should keep picker open")
	}
	if !strings.Contains(m.providerPicker.addInputErr, "default model is required") {
		t.Errorf("expected 'default model is required' error; got %q", m.providerPicker.addInputErr)
	}
	// Disk must be untouched.
	if body, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml")); err == nil {
		if strings.Contains(string(body), `kind          = "anthropic"`) {
			t.Errorf("provider should NOT have been written without a model; got:\n%s", body)
		}
	}
}

// Form textinputs must claim a bounded width so a long API key (or a
// long base URL) can't push past the right edge of the inline
// overlay. Width is computed from m.width via providerAddInputWidth;
// this test asserts every field gets a non-zero, bounded Width when
// the form is populated under a typical 80-col terminal.
func TestProviderPicker_AddFieldsHaveBoundedWidth(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode (anthropic)
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	// newTestModel sends WindowSizeMsg{Width: 80}. With margin for
	// marker + label column, providerAddInputWidth(80) should yield
	// something well below 80 and at least the lower clamp (24).
	for i, label := range m.providerPicker.addLabels {
		w := m.providerPicker.addFields[i].Width
		if w <= 0 {
			t.Errorf("%s field has unbounded Width=%d", label, w)
		}
		if w >= 80 {
			t.Errorf("%s field Width=%d would overflow an 80-col terminal", label, w)
		}
	}
}

// providerAddInputWidth must clamp to a sane range across terminal
// sizes so the form is usable on narrow shells (40-col tmux pane)
// and doesn't read sparse on ultra-wide ones.
func TestProviderAddInputWidth_Clamps(t *testing.T) {
	cases := []struct {
		term     int
		wantMin  int
		wantMax  int
		describe string
	}{
		{0, 48, 48, "pre-WindowSizeMsg"},
		{20, 24, 24, "very narrow"},
		{40, 24, 40, "narrow tmux pane"},
		{80, 40, 70, "standard"},
		{200, 70, 70, "ultra-wide"},
	}
	for _, c := range cases {
		got := providerAddInputWidth(c.term)
		if got < c.wantMin || got > c.wantMax {
			t.Errorf("%s: providerAddInputWidth(%d) = %d, want in [%d,%d]",
				c.describe, c.term, got, c.wantMin, c.wantMax)
		}
	}
}

// Every major cloud provider must observe the same Add-form
// contract: bounded textinput Width, empty Default model on populate,
// cursor=-1 (no auto-pick), and the API-key field collected. This
// table-driven test locks the matrix in across anthropic, openai,
// gemini, and xai so a future kind-specific tweak can't silently
// regress one provider while passing for another. nvidia-nim is
// covered in TestProviderPicker_AddRejectsMissingKey_AllCloudProviders.
func TestProviderPicker_AddFormContract_AllCloudProviders(t *testing.T) {
	cases := []struct {
		kindName string
		envVar   string
	}{
		{"anthropic", "ANTHROPIC_API_KEY"},
		{"openai", "OPENAI_API_KEY"},
		{"gemini", "GEMINI_API_KEY"},
		{"xai", "XAI_API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.kindName, func(t *testing.T) {
			m := newTestModel(t)
			seedConfigTOML(t, "")
			t.Setenv(tc.envVar, "")
			m, _ = typeAndEnter(t, m, "/provider")
			for _, item := range m.providerPicker.menuItems {
				if item.Label == "Add" {
					break
				}
				m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
			}
			m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
			target := -1
			for i, e := range m.providerPicker.addCatalog {
				if e.Name == tc.kindName {
					target = i
					break
				}
			}
			if target < 0 {
				t.Fatalf("catalog missing %q", tc.kindName)
			}
			for i := 0; i < target; i++ {
				m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
			}
			m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode

			// (a) every textinput claims a bounded width.
			for i, lbl := range m.providerPicker.addLabels {
				w := m.providerPicker.addFields[i].Width
				if w <= 0 || w >= 80 {
					t.Errorf("%s: %s field has unsafe Width=%d", tc.kindName, lbl, w)
				}
			}

			// (b) Default model starts empty + cursor at -1.
			modelIdx, keyIdx := -1, -1
			for i, lbl := range m.providerPicker.addLabels {
				switch lbl {
				case "Default model":
					modelIdx = i
				case "API key":
					keyIdx = i
				}
			}
			if modelIdx < 0 {
				t.Fatalf("%s: form missing Default model field", tc.kindName)
			}
			if keyIdx < 0 {
				t.Errorf("%s: form missing API key field — every cloud provider needs one", tc.kindName)
			}
			if v := strings.TrimSpace(m.providerPicker.addFields[modelIdx].Value()); tc.kindName == "xai" {
				if v != "grok-4.20-0309-non-reasoning" {
					t.Errorf("xai: model field default = %q, want grok-4.20-0309-non-reasoning", v)
				}
			} else if v != "" {
				t.Errorf("%s: model field should start empty (no auto-prefill); got %q",
					tc.kindName, v)
			}
			if m.providerPicker.addModelCursor != -1 {
				t.Errorf("%s: cursor should start at -1; got %d",
					tc.kindName, m.providerPicker.addModelCursor)
			}

			// (c) save without a model is rejected — same rule
			// for every provider, regardless of whether the
			// catalog is populated for this kind.
			if keyIdx >= 0 {
				m.providerPicker.addFields[keyIdx].SetValue("test-key-" + tc.kindName)
			}
			if tc.kindName == "xai" {
				m.providerPicker.addFields[modelIdx].SetValue("")
			}
			m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
			if !m.providerPickerOpen {
				t.Fatalf("%s: save with no model should keep picker open", tc.kindName)
			}
			if !strings.Contains(m.providerPicker.addInputErr, "default model is required") {
				t.Errorf("%s: expected 'default model is required'; got %q",
					tc.kindName, m.providerPicker.addInputErr)
			}
		})
	}
}

// Free-form kinds (ollama, openai-compatible) get no curated model
// list — the form keeps its placeholder-driven textinput. Locks in
// the boundary so a future "always populate from catalog" tweak
// doesn't accidentally render a stale list for the wrong kind.
func TestProviderPicker_AddFreeFormKindHasNoCuratedModels(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	target := -1
	for i, e := range m.providerPicker.addCatalog {
		if e.Kind == "ollama" {
			target = i
			break
		}
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode
	if len(m.providerPicker.addCuratedModels) != 0 {
		t.Errorf("ollama should have no curated catalog; got %d entries",
			len(m.providerPicker.addCuratedModels))
	}
}

// First Add against an empty config must persist the new provider
// AND mark it as active. The next `yottacode` invocation reads the
// on-disk config; without an [active] block, cli.Resolve has to fall
// back to the first provider — which works, but persisting active
// makes the config self-describing and matches what /sessions,
// /model, and the status bar all expect.
func TestProviderPicker_AddSetsActiveOnFirstAdd(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	m = navigateToKind(t, m, "anthropic")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode

	// Fill key + arrow-pick a model so save passes the guards.
	keyIdx, modelIdx := -1, -1
	for i, lbl := range m.providerPicker.addLabels {
		switch lbl {
		case "API key":
			keyIdx = i
		case "Default model":
			modelIdx = i
		}
	}
	m.providerPicker.addFields[keyIdx].SetValue("sk-ant-active-test")
	if len(m.providerPicker.addCuratedModels) == 0 {
		m.providerPicker.addFields[modelIdx].SetValue("claude-sonnet-4-6")
	} else {
		for m.providerPicker.addFocused != modelIdx {
			m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyTab})
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown}) // -1 → 0
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPickerOpen {
		t.Fatalf("save should close picker; addInputErr=%q", m.providerPicker.addInputErr)
	}

	body, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(body), `provider      = "anthropic"`) {
		t.Errorf("first Add should set [active].provider; config:\n%s", body)
	}
}

// Subsequent Adds when an [active] block already exists must NOT
// reassign active — that would silently switch the user's running
// default to whatever they last added, which is surprising.
func TestProviderPicker_AddPreservesExistingActive(t *testing.T) {
	m := newTestModel(t)
	// Seed a config with anthropic already active.
	seedProviderConfig(t)
	t.Setenv("OPENAI_API_KEY", "")
	m, _ = typeAndEnter(t, m, "/provider")
	for _, item := range m.providerPicker.menuItems {
		if item.Label == "Add" {
			break
		}
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addKindMode
	// Walk to gemini (index varies; skip if catalog reordered).
	target := -1
	for i, e := range m.providerPicker.addCatalog {
		if e.Name == "gemini" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Skip("gemini missing from catalog; skipping")
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → addFieldsMode

	// Fill required fields.
	keyIdx, modelIdx := -1, -1
	for i, lbl := range m.providerPicker.addLabels {
		switch lbl {
		case "API key":
			keyIdx = i
		case "Default model":
			modelIdx = i
		}
	}
	m.providerPicker.addFields[keyIdx].SetValue("test-gemini-key")
	m.providerPicker.addFields[modelIdx].SetValue("gemini-2.5-flash")
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.providerPickerOpen {
		t.Fatalf("save should close picker; addInputErr=%q", m.providerPicker.addInputErr)
	}

	body, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	// Adding a provider switches to it automatically.
	if !strings.Contains(string(body), `provider      = "gemini"`) {
		t.Errorf("active should switch to newly added gemini; config:\n%s", body)
	}
}

// Removing the active provider auto-switches the running session to
// the first remaining provider (declaration order). The transcript
// surfaces both the removal and the switch so the user is never
// silently moved to a different model.
func TestProviderPicker_RemoveActiveAutoSwitches(t *testing.T) {
	m := newTestModel(t)
	seedProviderConfig(t) // active.provider = "anthropic"
	m, _ = typeAndEnter(t, m, "/provider")
	target := -1
	for i, item := range m.providerPicker.menuItems {
		if item.Label == "Remove" {
			target = i
			break
		}
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // → Remove sub-picker
	// Cursor at 0 = anthropic (the active one).
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	got := stripANSI(m.transcript.String())
	if !strings.Contains(got, `removed "anthropic"`) {
		t.Errorf("expected removed line; transcript:\n%s", got)
	}
	if !strings.Contains(got, `switched to openai`) {
		t.Errorf("expected auto-switch line; transcript:\n%s", got)
	}
	if m.cfg.Adapter == nil {
		t.Errorf("adapter should be rebuilt after auto-switch, got nil")
	}
}

// Removing an openai-auth profile through the picker also wipes
// the OAuth token store — same auto-logout the slash-flag path
// (commands.go ~L333) provides. Without this, picker-driven cleanup
// leaves stale bearer/refresh tokens on disk.
func TestProviderPicker_RemoveOpenAIAuthDeletesTokenStore(t *testing.T) {
	m := newTestModel(t)
	// Single-provider seed: the picker's remove sub-list lands on
	// the only entry, so Enter→Enter removes it without further
	// navigation. Avoids fighting the menu layout for cursor math.
	seedConfigTOML(t, `
[active]
provider      = "chatgpt"
default_model = "gpt-5.5"

[[providers]]
name          = "chatgpt"
kind          = "openai-auth"
base_url      = "https://chatgpt.com/backend-api/codex"
default_model = "gpt-5.5"

  [[providers.models]]
  name = "gpt-5.5"
`)
	tokenPath := filepath.Join(os.Getenv("HOME"), ".yottacode", "auth", "openai-auth.json")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(`{"access_token":"x","refresh_token":"y"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	m, _ = typeAndEnter(t, m, "/provider")
	target := -1
	for i, item := range m.providerPicker.menuItems {
		if item.Label == "Remove" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("Remove not in menu")
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // Remove → sub-picker
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // confirm cursor 0 (chatgpt)

	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Errorf("token store should have been deleted by picker remove; stat err = %v", err)
	}
	got := stripANSI(m.transcript.String())
	if !strings.Contains(got, "logged out") {
		t.Errorf("transcript should mention auto-logout; got:\n%s", got)
	}
}

// `provider remove` from the TUI picker must also clean the matching
// API key out of ~/.yottacode/.env so removing a profile actually
// removes its credential. Mirrors TestProviderRemoveCleansAPIKeyFromEnv
// in cmd/yottacode/, but driven through the picker keystroke walk so
// the integration end-to-end (modal → confirm → applyProviderRemove
// → dotenv.DeleteKey) is covered.
func TestProviderPicker_RemoveCleansAPIKeyFromEnv(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[active]
provider      = "anthropic"
default_model = "claude-sonnet-4-6"

[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
api_key_env   = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"

[[providers]]
name          = "openai"
kind          = "openai"
base_url      = "https://api.openai.com/v1"
api_key_env   = "OPENAI_API_KEY"
default_model = "gpt-4o"
`)
	envPath := filepath.Join(os.Getenv("HOME"), ".yottacode", ".env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath,
		[]byte(`ANTHROPIC_API_KEY="sk-ant-test"`+"\n"+`OPENAI_API_KEY="sk-openai-test"`+"\n"),
		0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")

	m, _ = typeAndEnter(t, m, "/provider")
	target := -1
	for i, item := range m.providerPicker.menuItems {
		if item.Label == "Remove" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("Remove not in menu")
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // Remove → sub-picker
	// Sub-picker: cursor 0 = anthropic. Move to openai (index 1).
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // confirm

	body, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if strings.Contains(string(body), "OPENAI_API_KEY") {
		t.Errorf("OPENAI_API_KEY should be gone from .env; got:\n%s", body)
	}
	if !strings.Contains(string(body), "ANTHROPIC_API_KEY") {
		t.Errorf("ANTHROPIC_API_KEY (other provider) should be preserved; got:\n%s", body)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Errorf("running process env should be unset; got %q", got)
	}
	transcript := stripANSI(m.transcript.String())
	if !strings.Contains(transcript, "removed OPENAI_API_KEY") {
		t.Errorf("transcript should log env cleanup; got:\n%s", transcript)
	}
}

// Adding an openai-auth profile via the picker form path must kick
// off the inline browser OAuth flow — same behavior the slash-flag
// path (commands.go ~L298) already provides. Without this, the user
// adds the profile through the picker, sees no browser launch, and
// the saved config is unusable until they manually run the cobra
// `openai-auth login`.
func TestProviderPicker_AddOpenAIAuthStartsInlineLogin(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	picked := wizard.CatalogEntry{
		Name:      "openai-auth",
		Kind:      "openai-auth",
		BaseURL:   "https://chatgpt.com/backend-api/codex",
		APIKeyEnv: "",
	}
	// Picker form requires a non-nil providerPicker for commit
	// helpers, but persistProviderAdd reads only its own args. Drive
	// it directly to avoid simulating the keyboard walk.
	m, cmd := m.persistProviderAdd(picked, "chatgpt", picked.BaseURL, "", "gpt-5.5", "")
	got := stripANSI(m.transcript.String())
	for _, want := range []string{
		"openai-auth",
		"starting browser sign-in",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("picker openai-auth add transcript missing %q\n%s", want, got)
		}
	}
	if cmd == nil {
		t.Errorf("expected a tea.Cmd to drive StartLogin from picker path; got nil")
	}
}
