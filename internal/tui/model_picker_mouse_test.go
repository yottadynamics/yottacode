package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
)

func TestMouseHover_MovesModelPickerSelectorWithoutSelecting(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/model")
	m, _ = applyMsg(m, modelPickerLoadedMsg{
		entries: []catalog.Model{
			{ID: "a", Provider: "session"},
			{ID: "b", Provider: "session"},
			{ID: "c", Provider: "session"},
		},
	})
	m.modelPicker.cursor = 0
	x, y, ok := screenPointForItem(m, 2)
	if !ok {
		t.Fatalf("could not locate a screen point for model-picker row 2")
	}

	m, cmd := applyMsg(m, tea.MouseMotionMsg{X: x, Y: y})
	if cmd != nil {
		t.Fatalf("hover should not trigger a command, got %T", cmd)
	}
	if got := m.modelPicker.cursor; got != 2 {
		t.Fatalf("model picker cursor after hover = %d, want 2", got)
	}
	if !m.modelPickerOpen {
		t.Fatal("hover should only move the selector, not select and close the picker")
	}
}

// screenPointForItem re-renders the open model picker with a fresh hit
// accumulator (mirroring handleModelPickerClick's own approach) and
// returns an absolute screen point that lands on the given item index —
// exercising the real geometry math end to end instead of hardcoding a
// row number that could silently drift from the actual layout.
func screenPointForItem(m Model, index int) (x, y int, ok bool) {
	hits := &pickerHits{}
	box := popupBox(renderModelPicker(m.modelPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	for _, r := range hits.regions {
		if r.Kind == hitItem && r.Index == index {
			return ox + 2, oy + 1 + r.Row, true
		}
	}
	return 0, 0, false
}

// screenPointForTab is screenPointForItem's sibling for the provider
// tab strip — clicks the midpoint of the tab's column span.
func screenPointForTab(m Model, index int) (x, y int, ok bool) {
	hits := &pickerHits{}
	box := popupBox(renderModelPicker(m.modelPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	for _, r := range hits.regions {
		if r.Kind == hitTab && r.Index == index {
			mid := (r.ColStart + r.ColEnd) / 2
			return ox + 2 + mid, oy + 1 + r.Row, true
		}
	}
	return 0, 0, false
}

func TestModelPicker_ClickItemSelectsAndCommits(t *testing.T) {
	m := newTestModel(t)
	// commitModelChoice needs a real [[providers]] entry to write the
	// choice against (SetActiveModel errors otherwise) — same config
	// seeding TestModelPicker_SelectingModelAdoptsProviderAsActive uses.
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// SetActiveModel validates the chosen ID against config.toml's own
	// providers.models list, independent of what the picker's (stubbed)
	// catalog fetch returned — all three IDs need a declared entry here,
	// not just the one being clicked.
	body := `
[[providers]]
name = "ollama"
kind = "ollama"
base_url = "http://localhost:11434/v1"
default_model = "a"
  [[providers.models]]
  name = "a"
  tier = "cheap"
  [[providers.models]]
  name = "b"
  tier = "cheap"
  [[providers.models]]
  name = "c"
  tier = "cheap"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	prov := *cfg.FindProvider("ollama")
	m.pickerList = stubPickerList([]catalog.Model{
		{ID: "a", Provider: "session"}, {ID: "b", Provider: "session"}, {ID: "c", Provider: "session"},
	}, nil)
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())

	x, y, ok := screenPointForItem(m, 2)
	if !ok {
		t.Fatalf("could not locate a screen point for item 2 in the rendered picker")
	}
	m, clickCmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.modelPickerOpen {
		t.Errorf("clicking an item should commit and close the picker, like Enter does")
	}
	if m.modelName != "c" {
		t.Errorf("clicking item 2 should select %q, got %q", "c", m.modelName)
	}
	if clickCmd == nil {
		t.Errorf("commit should still return the usual post-commit cmd batch (provider probe etc.)")
	}
}

func TestModelPicker_ClickOutsideBoxIsNoop(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/model")
	m, _ = applyMsg(m, modelPickerLoadedMsg{entries: []catalog.Model{{ID: "a", Provider: "session"}}})

	m, _ = applyMsg(m, tea.MouseClickMsg{X: 0, Y: 0})
	if !m.modelPickerOpen {
		t.Errorf("a click outside the popup box should not close the picker")
	}
	if m.modelName == "a" {
		t.Errorf("a click outside the popup box should not select anything")
	}
}

func seedTwoProviderConfig(t *testing.T) config.Provider {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
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
name = "ollama"
kind = "ollama"
base_url = "http://localhost:11434/v1"
default_model = "llama3.1:8b"
  [[providers.models]]
  name = "llama3.1:8b"
  tier = "cheap"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	return *cfg.FindProvider("anthropic")
}

func TestModelPicker_ClickTabSwitchesProvider(t *testing.T) {
	m := newTestModel(t)
	prov := seedTwoProviderConfig(t)
	m.pickerList = stubPickerList([]catalog.Model{{ID: "from-fake", Provider: "session"}}, nil)
	cmd := m.openModelPicker(prov)
	m, _ = applyMsg(m, cmd())
	if m.modelPicker.providerIdx != 0 {
		t.Fatalf("test setup: expected providerIdx=0 (anthropic), got %d", m.modelPicker.providerIdx)
	}

	x, y, ok := screenPointForTab(m, 1) // ollama
	if !ok {
		t.Fatalf("could not locate a screen point for tab 1 in the rendered picker")
	}
	m, fetchCmd := applyMsg(m, tea.MouseClickMsg{X: x, Y: y})
	if m.modelPicker.provider.Name != "ollama" {
		t.Errorf("clicking the ollama tab should switch the picker to it; provider = %q", m.modelPicker.provider.Name)
	}
	if fetchCmd == nil {
		t.Errorf("a tab click should return a fetch cmd for the new provider, same as Right does")
	}
	if !m.modelPickerOpen {
		t.Errorf("clicking a tab should not close the picker")
	}
}

func TestBodyPoint_OutsideBoxReturnsNotOK(t *testing.T) {
	box := popupBox("hello\nworld")
	if _, _, ok := bodyPoint(box, 10, 10, 0, 0); ok {
		t.Error("a screen point far outside the box should not resolve")
	}
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	if _, _, ok := bodyPoint(box, 0, 0, bw+5, bh+5); ok {
		t.Error("a screen point past the box's bottom-right corner should not resolve")
	}
}

func TestBodyPoint_InsideBoxResolvesToBodyRowCol(t *testing.T) {
	box := popupBox("hello\nworld")
	// popupBox: rounded border (+1 row/col) + Padding(0,1) (+1 col) —
	// body row 0 col 0 ("h" of "hello") sits at box-relative (row=1, col=2).
	row, col, ok := bodyPoint(box, 0, 0, 2, 1)
	if !ok || row != 0 || col != 0 {
		t.Errorf("bodyPoint at box interior top-left = (%d,%d,%v), want (0,0,true)", row, col, ok)
	}
}
