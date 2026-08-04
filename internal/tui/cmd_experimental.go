package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/experimental"
)

// cmdExperimental lists the experimental-feature catalog and which ones are
// switched on this session. It is read-only and informational: toggling a
// feature stays a startup concern (--experimental, YOTTACODE_EXPERIMENTAL, or
// the [experimental] config block), so this surfaces state rather than changing
// it.
func cmdExperimental(m Model, _ []string) (Model, tea.Cmd) {
	m.experimentalPanel = renderExperimentalPanel(m.experimentalEnabled)
	m.experimentalOpen = true
	return m, nil
}

func renderExperimentalPanel(enabled []string) string {
	on := make(map[string]bool, len(enabled))
	for _, name := range enabled {
		on[name] = true
	}

	var b strings.Builder
	b.WriteString(renderMenuHeader("Experimental", "Opt-in features for this session; toggles apply at startup."))
	b.WriteString("\n")

	b.WriteString(styleSplashTitle.Render("Session state"))
	b.WriteString("\n")
	if len(enabled) == 0 {
		b.WriteString("  ")
		b.WriteString(styleMeta.Render("No active experiments in this session."))
	} else {
		names := append([]string(nil), enabled...)
		sort.Strings(names)
		fmt.Fprintf(&b, "  Active: %s", strings.Join(names, ", "))
	}
	b.WriteString("\n\n")

	b.WriteString(styleSplashTitle.Render("Feature catalog"))
	b.WriteString("\n")
	for _, f := range experimental.All() {
		state := "off"
		stateStyle := styleMeta
		switch {
		case experimental.IsGraduated(f):
			state = "GA"
			stateStyle = styleAuto
		case on[string(f)]:
			state = "ON"
			stateStyle = styleAuto
		}
		fmt.Fprintf(&b, "  %-18s %s  %s\n",
			string(f), stateStyle.Render(fmt.Sprintf("[%s]", state)), experimental.Description(f))
	}

	b.WriteString("\n")
	b.WriteString(styleSplashTitle.Render("Enable at startup"))
	b.WriteString("\n")
	b.WriteString("  CLI:    yottacode --experimental <name>\n")
	b.WriteString("  Env:    YOTTACODE_EXPERIMENTAL=<name>\n")
	b.WriteString("  Config: [experimental] <name> = true\n\n")
	b.WriteString(styleHint.Render("esc to close"))

	return strings.TrimRight(b.String(), "\n")
}
