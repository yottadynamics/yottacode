package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/experimental"
)

// cmdExperimental lists the experimental-feature catalog and which ones are
// switched on this session — the overlay the gate-rejection messages and the
// docs refer to. Read-only and informational: toggling a feature stays a
// startup concern (the --experimental CLI flag, $YOTTACODE_EXPERIMENTAL, or
// the [experimental] config block), so this surfaces state rather than
// changing it. It reads m.experimentalEnabled (the resolved set captured at
// startup) against experimental.All(), which is also the one live consumer of
// experimental.Set.EnabledNames — previously dead, so the gate left no
// in-session confirmation it was on.
func cmdExperimental(m Model, _ []string) (Model, tea.Cmd) {
	on := make(map[string]bool, len(m.experimentalEnabled))
	for _, name := range m.experimentalEnabled {
		on[name] = true
	}

	var b strings.Builder
	b.WriteString("Experimental features")
	b.WriteString(" — enable active experiments at startup with `--experimental <name>`, `YOTTACODE_EXPERIMENTAL=<name>`, or `[experimental]` `<name> = true` in ~/.yottacode/config.toml. Graduated entries are GA/no-op compatibility flags:\n")
	for _, f := range experimental.All() {
		state := "off"
		if experimental.IsGraduated(f) {
			state = "GA"
		} else if on[string(f)] {
			state = "ON"
		}
		fmt.Fprintf(&b, "  [%-3s] %s — %s\n", state, string(f), experimental.Description(f))
	}
	if len(m.experimentalEnabled) == 0 {
		b.WriteString("\nNone enabled this session.")
	} else {
		fmt.Fprintf(&b, "\nEnabled this session: %s", strings.Join(m.experimentalEnabled, ", "))
	}

	m.appendLine(styleAuto.Render(strings.TrimRight(b.String(), "\n")))
	return m, nil
}
