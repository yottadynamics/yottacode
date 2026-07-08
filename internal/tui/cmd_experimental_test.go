package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/experimental"
)

// TestCmdExperimental_ListsCatalogAndState: the overlay lists every feature
// in experimental.All(), marks the session's enabled ones ON, and names the
// enabled set. Both the catalog (so users discover features) and the on/off
// state (so the gate has an in-session confirmation) must be present.
func TestCmdExperimental_ListsCatalogAndState(t *testing.T) {
	m := Model{
		transcript:          &strings.Builder{},
		experimentalEnabled: []string{string(experimental.BackgroundSubagents)},
	}
	out, _ := cmdExperimental(m, nil)
	got := out.transcript.String()

	// Every catalog feature appears.
	for _, f := range experimental.All() {
		if !strings.Contains(got, string(f)) {
			t.Errorf("/experimental must list feature %q; got:\n%s", f, got)
		}
	}
	// The enabled one is marked ON and named in the summary.
	if !strings.Contains(got, "ON") {
		t.Errorf("/experimental must mark the enabled feature ON; got:\n%s", got)
	}
	if !strings.Contains(got, "Enabled this session: "+string(experimental.BackgroundSubagents)) {
		t.Errorf("/experimental must name the enabled feature in the summary; got:\n%s", got)
	}
}

// TestCmdExperimental_NoneEnabled: with nothing enabled, the overlay still
// lists the catalog and says none are on (not a blank/confusing surface).
func TestCmdExperimental_NoneEnabled(t *testing.T) {
	m := Model{transcript: &strings.Builder{}}
	out, _ := cmdExperimental(m, nil)
	got := out.transcript.String()
	if !strings.Contains(got, "None enabled this session") {
		t.Errorf("/experimental must state when nothing is enabled; got:\n%s", got)
	}
	if !strings.Contains(got, string(experimental.BackgroundSubagents)) {
		t.Errorf("/experimental must still list the catalog when nothing is enabled; got:\n%s", got)
	}
}
