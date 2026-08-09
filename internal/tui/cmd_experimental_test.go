package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// TestRenderExperimentalPanel_StateMarkersAlign: the catalog padded
// names to a hardcoded 18 columns, but the longest name
// (lsp_code_intelligence, 21 cells) overflowed that and shoved its own
// [GA] marker out of the column, so the list rendered ragged. The name
// column must be sized from the catalog itself.
func TestRenderExperimentalPanel_StateMarkersAlign(t *testing.T) {
	lines := strings.Split(renderExperimentalPanel(nil, 100), "\n")

	cols := map[string]int{}
	for _, f := range experimental.All() {
		for _, line := range lines {
			plain := ansi.Strip(line)
			if strings.HasPrefix(plain, "  "+string(f)+" ") {
				cols[string(f)] = strings.Index(plain, "[")
				break
			}
		}
	}
	if len(cols) != len(experimental.All()) {
		t.Fatalf("found %d catalog rows, want %d", len(cols), len(experimental.All()))
	}

	want := -1
	for name, col := range cols {
		if col < 0 {
			t.Fatalf("row for %q has no state marker", name)
		}
		if want == -1 {
			want = col
			continue
		}
		if col != want {
			t.Errorf("state marker for %q at column %d, want %d (all markers must align)", name, col, want)
		}
	}
}

// TestRenderExperimentalPanel_WrapsToWidth: descriptions are full
// sentences and used to run off the right edge, getting clipped
// mid-word by the terminal. They must wrap to the overlay width.
func TestRenderExperimentalPanel_WrapsToWidth(t *testing.T) {
	const width = 72
	got := renderExperimentalPanel(nil, width)

	for line := range strings.SplitSeq(got, "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("line is %d cells wide, exceeds width %d: %q", w, width, ansi.Strip(line))
		}
	}

	// Wrapping must not drop the prose: the opening words of a known
	// description still have to be present.
	if !strings.Contains(ansi.Strip(got), "Repository code map.") {
		t.Errorf("wrapping dropped the description text; got:\n%s", ansi.Strip(got))
	}
}

func TestCmdExperimental_AnyKeyDismisses(t *testing.T) {
	m := newTestModel(t)
	m.experimentalOpen = true
	m.experimentalPanel = "stale body"

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	if m.experimentalOpen {
		t.Error("any key should close the experimental overlay")
	}
	if m.experimentalPanel != "" {
		t.Errorf("closing should clear the cached panel; got %q", m.experimentalPanel)
	}
}
