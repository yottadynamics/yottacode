package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/experimental"
)

// TestCmdExperimental_ListsCatalogAndState: the overlay lists every feature
// in experimental.All(), marks the session's enabled ones ON, and names the
// enabled set. Both the catalog (so users discover features) and the on/off
// state (so the gate has an in-session confirmation) must be present.
func TestCmdExperimental_ListsCatalogAndState(t *testing.T) {
	m := newTestModel(t)
	m.experimentalEnabled = []string{string(experimental.CodeMap)}
	beforeLines := len(m.historyLines)

	out, _ := cmdExperimental(m, nil)
	got := out.experimentalPanel

	if !out.experimentalOpen {
		t.Fatal("/experimental should open the inline overlay")
	}
	if len(out.historyLines) != beforeLines {
		t.Fatalf("/experimental must not append to chat history: %d -> %d", beforeLines, len(out.historyLines))
	}
	if !strings.Contains(got, "──") || !strings.Contains(got, "Experimental") {
		t.Errorf("/experimental should use submenu chrome; got:\n%s", got)
	}

	// Every catalog feature appears.
	for _, f := range experimental.All() {
		if !strings.Contains(got, string(f)) {
			t.Errorf("/experimental must list feature %q; got:\n%s", f, got)
		}
	}
	// The enabled one is marked ON and named in the summary.
	if !strings.Contains(got, "[ON]") {
		t.Errorf("/experimental must mark the enabled feature ON; got:\n%s", got)
	}
	if !strings.Contains(got, "Active: "+string(experimental.CodeMap)) {
		t.Errorf("/experimental must name the enabled feature in the summary; got:\n%s", got)
	}
}

// TestCmdExperimental_NoneEnabled: with nothing enabled, the overlay still
// lists the catalog and says none are on (not a blank/confusing surface).
func TestCmdExperimental_NoneEnabled(t *testing.T) {
	got := renderExperimentalPanel(nil)
	if !strings.Contains(got, "No active experiments in this session") {
		t.Errorf("/experimental must state when nothing is enabled; got:\n%s", got)
	}
	if !strings.Contains(got, "[GA]") || !strings.Contains(got, string(experimental.BackgroundSubagents)) {
		t.Errorf("/experimental must mark graduated compatibility flags as GA; got:\n%s", got)
	}
	if !strings.Contains(got, "yottacode --experimental <name>") || !strings.Contains(got, "YOTTACODE_EXPERIMENTAL=<name>") {
		t.Errorf("/experimental should explain startup enablement; got:\n%s", got)
	}
}

func TestCmdExperimental_AnyKeyDismisses(t *testing.T) {
	m := newTestModel(t)
	m.experimentalOpen = true
	m.experimentalPanel = "stale body"

	m, _ = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})

	if m.experimentalOpen {
		t.Error("any key should close the experimental overlay")
	}
	if m.experimentalPanel != "" {
		t.Errorf("closing should clear the cached panel; got %q", m.experimentalPanel)
	}
}
