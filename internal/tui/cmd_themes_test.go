package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

// /theme (bare) opens the picker, attaches state, and live-applies
// the highlighted theme so the right pane shows a preview without
// waiting for the first arrow press.
func TestThemes_BareOpensPicker(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	m, _ = m.runSlash("/theme")
	if !m.themePickerOpen {
		t.Fatalf("/theme should open the picker")
	}
	if m.themePicker == nil {
		t.Fatalf("picker state should be attached")
	}
	// Cursor should land on the active theme, so the picker doesn't
	// disorient a user reopening it to confirm what they have.
	want := ActiveTheme()
	got := m.themePicker.entries[m.themePicker.cursor]
	if got != want {
		t.Errorf("picker cursor opened on %q, want active theme %q", got, want)
	}
}

// Esc closes the picker AND reverts the live-applied palette to
// whatever was active at open time. Navigating in the picker and
// then cancelling must be non-destructive.
func TestThemesPicker_EscRevertsLivePreview(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	originalTheme := ActiveTheme()

	m, _ = m.runSlash("/theme")
	// Walk the cursor down twice so the live-applied theme differs
	// from where we started.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	if ActiveTheme() == originalTheme {
		t.Fatalf("navigation should live-apply a different theme; still on %q", ActiveTheme())
	}

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.themePickerOpen {
		t.Errorf("Esc should close the picker")
	}
	if ActiveTheme() != originalTheme {
		t.Errorf("Esc should revert to %q, still on %q", originalTheme, ActiveTheme())
	}
}

// Enter commits the highlighted theme and persists to config.toml.
// Same HOME-after-newTestModel ordering as the prior persistence
// test — newTestModel overrides HOME in its own t.Setenv.
func TestThemesPicker_EnterCommitsAndPersists(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".yottacode"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m, _ = m.runSlash("/theme")
	// Walk to a non-default entry — the cursor lands on the active
	// theme at open, so one Down arrow is enough to differ.
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	chosen := m.themePicker.entries[m.themePicker.cursor]
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.themePickerOpen {
		t.Errorf("Enter should close the picker")
	}
	if ActiveTheme() != chosen {
		t.Errorf("after Enter, ActiveTheme = %q, want %q", ActiveTheme(), chosen)
	}

	body, err := os.ReadFile(filepath.Join(home, ".yottacode", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	if !strings.Contains(string(body), `name = "`+chosen+`"`) {
		t.Errorf("config.toml missing theme persistence (chose %q):\n%s", chosen, body)
	}
	if m.fileCfg.Theme.Name != chosen {
		t.Errorf("model.fileCfg.Theme.Name = %q, want %q", m.fileCfg.Theme.Name, chosen)
	}
}

// j/k vim-style navigation should match Up/Down semantics, since
// other pickers in the codebase honor them.
func TestThemesPicker_VimKeysNavigate(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	m, _ = m.runSlash("/theme")
	start := m.themePicker.cursor

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.themePicker.cursor != start+1 && start+1 < len(m.themePicker.entries) {
		t.Errorf("j should advance the cursor by 1 (%d → %d)", start, m.themePicker.cursor)
	}

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.themePicker.cursor != start {
		t.Errorf("k should retreat the cursor by 1 (now %d, want %d)", m.themePicker.cursor, start)
	}
}

// Home/End jump to the first/last entry.
func TestThemesPicker_HomeEndJumps(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	m, _ = m.runSlash("/theme")

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnd})
	last := len(m.themePicker.entries) - 1
	if m.themePicker.cursor != last {
		t.Errorf("End should jump to %d, got %d", last, m.themePicker.cursor)
	}

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyHome})
	if m.themePicker.cursor != 0 {
		t.Errorf("Home should jump to 0, got %d", m.themePicker.cursor)
	}
}

// /theme set <name> bypasses the picker — applies and persists in
// one shot. Useful for scripts and muscle memory; mirrors /model
// <name>'s positional shortcut behavior.
func TestThemes_SetAppliesAndPersists(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })

	m := newTestModel(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".yottacode"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m, _ = m.runSlash("/theme set low-contrast")
	if ActiveTheme() != "low-contrast" {
		t.Errorf("after /theme set low-contrast, ActiveTheme() = %q, want \"light\"", ActiveTheme())
	}
	if m.themePickerOpen {
		t.Errorf("/theme set should NOT open the picker")
	}
	body, err := os.ReadFile(filepath.Join(home, ".yottacode", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	if !strings.Contains(string(body), `name = "low-contrast"`) {
		t.Errorf("config.toml missing theme persistence:\n%s", body)
	}
}

// Positional shortcut: /theme <name> behaves like /theme set <name>.
func TestThemes_PositionalNameActsAsSet(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".yottacode"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m, _ = m.runSlash("/theme dimmed")
	if ActiveTheme() != "dimmed" {
		t.Errorf("/theme dimmed should apply dimmed theme; ActiveTheme() = %q", ActiveTheme())
	}
	if m.themePickerOpen {
		t.Errorf("/theme <name> should NOT open the picker")
	}
}

// Unknown theme name is rejected — neither opens the picker nor
// silently falls back to the default.
func TestThemes_UnknownNameReportsError(t *testing.T) {
	before := ActiveTheme()
	m, _ := newTestModel(t).runSlash("/theme set not-a-real-theme")
	content := m.transcript.String()
	if !strings.Contains(content, "unknown theme") {
		t.Errorf("/theme set unknown should say 'unknown theme': %q", content)
	}
	if ActiveTheme() != before {
		t.Errorf("unknown /theme set should NOT change ActiveTheme (was %q, now %q)", before, ActiveTheme())
	}
}

// The preview pane renders for every registered theme without
// panicking and surfaces recognizable sample content from each role
// bucket. This is our visual-regression net for the showcase
// rendering — the user-facing equivalent of "every theme has a
// usable surface."
func TestRenderThemePreviewPane_EmitsAllRoleSamples(t *testing.T) {
	for _, name := range themes.Names() {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
			ApplyTheme(name)
			state := &themePickerState{
				entries:       themes.Names(),
				originalTheme: themes.DefaultName,
			}
			for i, n := range state.entries {
				if n == name {
					state.cursor = i
					break
				}
			}
			out := renderThemePreviewPane(state, 60)
			for _, marker := range []string{
				name,            // theme name in header
				"Inline tokens", // inline-tokens heading
				"Code block",    // code-block heading
				"Diff",          // diff heading
				"Status",        // status heading
				"thinking",      // thinking sample at the bottom
			} {
				if !strings.Contains(out, marker) {
					t.Errorf("preview pane for %q missing marker %q", name, marker)
				}
			}
		})
	}
}

// Regression: cross-file styles (plan banner, tool card, subagent
// card, approval modal) used to be declared with var-block
// initializers that captured the ZERO VALUE of palette colors at
// package init — Go evaluates var initializers before any init()
// function runs, but the color vars are only populated when
// styles.go's init() calls buildStyles. Result: every cross-file
// style rendered as terminal default fg regardless of which theme
// was active. The fix moved the initializations into buildStyles so
// they pick up the current palette and rebuild on ApplyTheme.
//
// This test pins the foreground on a representative style from each
// affected file across two theme swaps. If the regression resurfaces
// (someone reverts to var-block initializers), the captured fg
// would stay empty and these assertions would fire.
func TestCrossFileStyles_FollowThemeChanges(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })

	// gruvbox has fully-populated AdaptiveColor pairs; any style
	// referencing colorWarning should reflect gruvbox's Warning
	// value after ApplyTheme("gruvbox"), not an empty AdaptiveColor.
	ApplyTheme("gruvbox")
	if fg := stylePlanBannerLabel.GetForeground(); fg == nil {
		t.Errorf("stylePlanBannerLabel.GetForeground() is nil after ApplyTheme(\"gruvbox\")")
	}
	if got := lipgloss.NewStyle().Foreground(colorWarning).GetForeground(); got == nil {
		t.Fatalf("test setup wrong: colorWarning is empty after ApplyTheme(\"gruvbox\")")
	}

	// Swap to terminal theme — same expectation. If the styles were
	// declared with capturing initializers, this side would still
	// hold gruvbox's Warning value (or the empty zero value,
	// depending on init order quirks).
	ApplyTheme("terminal")
	wantFg := lipgloss.NewStyle().Foreground(colorWarning).GetForeground()
	gotFg := stylePlanBannerLabel.GetForeground()
	if gotFg != wantFg {
		t.Errorf("after ApplyTheme(\"terminal\"), stylePlanBannerLabel fg = %v, want %v (palette-following)", gotFg, wantFg)
	}

	// Spot-check one style from each affected file so a future bug
	// in any single file's declarations surfaces here.
	for name, style := range map[string]lipgloss.Style{
		"stylePlanBannerLabel": stylePlanBannerLabel,
		"styleCardOKFooter":    styleCardOKFooter,
		"styleSubagentLabel":   styleSubagentLabel,
		"styleApprovalTitle":   styleApprovalTitle,
	} {
		if style.GetForeground() == nil {
			t.Errorf("%s has nil foreground after ApplyTheme — initializer probably captured zero value", name)
		}
	}
}

// Config round-trip: writing then reading [theme] name preserves the
// value. Belt-and-suspenders for the picker's persistence path.
func TestConfig_ThemeRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")

	want := config.Default()
	want.Theme.Name = "dimmed"

	if err := config.Save(want, cfgPath); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Theme.Name != "dimmed" {
		t.Errorf("Theme.Name round-trip = %q, want \"dimmed\"", got.Theme.Name)
	}
}

// Config rejects unknown theme names — same strictness as unknown
// keys. Stale config.toml entries should fail loudly at load time
// rather than silently rendering the default.
func TestConfig_UnknownThemeRejected(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	body := "[theme]\nname = \"not-a-theme\"\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := config.Load(cfgPath); err == nil {
		t.Errorf("Load should reject unknown theme.name")
	}
}
