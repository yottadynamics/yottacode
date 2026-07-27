package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/experimental"
)

// TestCmdExperimental_OpensInlineOverlay: /experimental is a transient submenu,
// not chat scrollback. The command snapshots the body and opens the inline
// overlay so View wraps it in the same green box as the other TUI submenus.
func TestCmdExperimental_OpensInlineOverlay(t *testing.T) {
	m := Model{
		transcript:          &strings.Builder{},
		experimentalEnabled: []string{string(experimental.BackgroundSubagents)},
		ready:               true,
		width:               100,
	}
	out, _ := cmdExperimental(m, nil)
	if !out.experimentalOpen {
		t.Fatal("/experimental should open the inline overlay")
	}
	if got := out.transcript.String(); got != "" {
		t.Fatalf("/experimental should not append catalog text to chat scrollback, got %q", got)
	}
	view := stripANSI(out.View())
	if !strings.Contains(view, "┌") || !strings.Contains(view, "Experimental features") {
		t.Fatalf("/experimental should render in a bordered submenu; got:\n%s", view)
	}
}

// TestRenderExperimentalOverlay_ListsCatalogAndState: the overlay lists every
// feature in experimental.All(), marks the session's enabled ones ON, and names
// the enabled set. Both the catalog and the on/off state must be present.
func TestRenderExperimentalOverlay_ListsCatalogAndState(t *testing.T) {
	got := stripANSI(renderExperimentalOverlay([]string{string(experimental.BackgroundSubagents)}, 100))

	for _, f := range experimental.All() {
		if !strings.Contains(got, string(f)) {
			t.Errorf("/experimental must list feature %q; got:\n%s", f, got)
		}
	}
	if !strings.Contains(got, "[ON ]") {
		t.Errorf("/experimental must mark the enabled feature ON; got:\n%s", got)
	}
	if !strings.Contains(got, "Enabled this session: "+string(experimental.BackgroundSubagents)) {
		t.Errorf("/experimental must name the enabled feature in the summary; got:\n%s", got)
	}
}

// TestRenderExperimentalOverlay_NoneEnabled: with nothing enabled, the overlay
// still lists the catalog and says none are on (not a blank/confusing surface).
func TestRenderExperimentalOverlay_NoneEnabled(t *testing.T) {
	got := stripANSI(renderExperimentalOverlay(nil, 100))
	if !strings.Contains(got, "Enabled this session: none") {
		t.Errorf("/experimental must state when nothing is enabled; got:\n%s", got)
	}
	if !strings.Contains(got, string(experimental.BackgroundSubagents)) {
		t.Errorf("/experimental must still list the catalog when nothing is enabled; got:\n%s", got)
	}
}
