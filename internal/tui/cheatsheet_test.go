package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCheatsheet_RendersAllEntries(t *testing.T) {
	out := renderCheatsheet(80)
	for _, e := range cheatsheet {
		// Strip surrounding spaces in the multi-key combos for the contains
		// check (e.g., "↑ / ↓" should show up).
		if !strings.Contains(out, e.Key) {
			t.Errorf("cheatsheet missing key %q: %q", e.Key, out)
		}
	}
}

func TestCheatsheet_ShowsDismissHint(t *testing.T) {
	out := renderCheatsheet(80)
	if !strings.Contains(out, "press any key to close") {
		t.Errorf("dismiss hint missing: %q", out)
	}
}

func TestCheatsheet_QuestionMarkOpensOverlay(t *testing.T) {
	m := newTestModel(t)
	if m.cheatsheetOpen {
		t.Fatalf("fresh model shouldn't have cheatsheet open")
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !m.cheatsheetOpen {
		t.Errorf("? on empty input should open cheatsheet")
	}
	v := m.View()
	if !strings.Contains(v, "Keyboard shortcuts") {
		t.Errorf("View should render cheatsheet overlay: %q", v)
	}
}

func TestCheatsheet_QuestionMarkInsideMessageDoesNotOpen(t *testing.T) {
	m := newTestModel(t)
	// Type some content first so input is not empty.
	for _, r := range "hi" {
		m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.cheatsheetOpen {
		t.Errorf("? inside a message should pass through to textinput, not open overlay")
	}
}

func TestCheatsheet_AnyKeyDismisses(t *testing.T) {
	m := newTestModel(t)
	m.cheatsheetOpen = true
	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.cheatsheetOpen {
		t.Errorf("any key should close the cheatsheet")
	}
}
