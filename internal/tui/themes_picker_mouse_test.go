package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

func TestThemesPicker_ClickListRowCommitsAndPersists(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".yottacode"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m, _ = m.runSlash("/theme")
	if len(m.themePicker.entries) < 2 {
		t.Fatal("test needs at least 2 registered themes")
	}
	target := 1
	chosen := m.themePicker.entries[target]

	hits := &pickerHits{}
	box := popupBox(renderThemePicker(m.themePicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if (r.Kind == hitItem || r.Kind == hitTab) && r.Index == target {
			x, y, found = ox+2, oy+1+r.Row, true
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for row %d", target)
	}

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.themePickerOpen {
		t.Errorf("clicking a theme row should commit and close the picker, like Enter does")
	}
	if ActiveTheme() != chosen {
		t.Errorf("after click, ActiveTheme = %q, want %q", ActiveTheme(), chosen)
	}

	body, err := os.ReadFile(filepath.Join(home, ".yottacode", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	if !strings.Contains(string(body), `name = "`+chosen+`"`) {
		t.Errorf("config.toml missing theme persistence (chose %q):\n%s", chosen, body)
	}
}

func TestThemesPicker_HoverCurrentRowDoesNotRefreshPreview(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = m.runSlash("/theme")

	hits := &pickerHits{}
	box := popupBox(renderThemePicker(m.themePicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	var x, y int
	found := false
	for _, r := range hits.regions {
		if (r.Kind == hitItem || r.Kind == hitTab) && r.Index == m.themePicker.cursor {
			x, y, found = ox+2, oy+1+r.Row, true
			break
		}
	}
	if !found {
		t.Fatalf("could not locate a screen point for current theme row %d", m.themePicker.cursor)
	}

	beforeRenderer := m.md
	beforeTheme := ActiveTheme()
	m, _ = applyMsg(m, tea.MouseMotionMsg{X: x, Y: y})

	if ActiveTheme() != beforeTheme {
		t.Fatalf("hovering the already-selected theme changed ActiveTheme from %q to %q", beforeTheme, ActiveTheme())
	}
	if m.md != beforeRenderer {
		t.Fatal("hovering the already-selected theme should not refresh component styles")
	}
}

func TestThemesPicker_ClickPreviewPaneIsNoop(t *testing.T) {
	t.Cleanup(func() { ApplyTheme(themes.DefaultName) })
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = m.runSlash("/theme")

	hits := &pickerHits{}
	box := popupBox(renderThemePicker(m.themePicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)

	// A click far to the right of the list pane's column bound lands in
	// the preview pane, which shares the same rows as the list but must
	// not resolve to a list item.
	var maxColEnd int
	var row int
	for _, r := range hits.regions {
		if r.Kind == hitTab && r.ColEnd > maxColEnd {
			maxColEnd = r.ColEnd
			row = r.Row
		}
	}
	if maxColEnd == 0 {
		t.Fatal("expected column-bounded hit regions in the wide layout")
	}
	x, y := ox+2+maxColEnd+10, oy+1+row
	originalCursor := m.themePicker.cursor

	m, _ = applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if !m.themePickerOpen {
		t.Errorf("clicking the preview pane should not close the picker")
	}
	if m.themePicker.cursor != originalCursor {
		t.Errorf("clicking the preview pane should not move the cursor; got %d, want %d", m.themePicker.cursor, originalCursor)
	}
}
