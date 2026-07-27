package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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
	m.experimentalBody = renderExperimentalOverlay(m.experimentalEnabled, m.width)
	m.experimentalOpen = true
	return m, nil
}

// renderExperimentalOverlay formats the feature catalog for the inline submenu
// box. The parent renderInlineOverlay supplies the green frame; this body uses
// the shared submenu title/divider/row visual language so /experimental feels
// like /router, /skills, /permissions, and the other transient TUI menus.
func renderExperimentalOverlay(enabled []string, width int) string {
	on := make(map[string]bool, len(enabled))
	for _, name := range enabled {
		on[name] = true
	}

	var b strings.Builder
	b.WriteString(renderMenuHeader("Experimental features",
		"Enable at startup: --experimental <name> · YOTTACODE_EXPERIMENTAL=<name> · [experimental] <name> = true"))
	b.WriteString("\n")

	labelWidth := 0
	for _, f := range experimental.All() {
		if n := len(string(f)); n > labelWidth {
			labelWidth = n
		}
	}
	innerWidth := width - 4
	if innerWidth <= 0 {
		innerWidth = 96
	}
	descWidth := innerWidth - labelWidth - len("  [off] ")
	if descWidth < 32 {
		descWidth = 32
	}

	for _, f := range experimental.All() {
		state := "[off]"
		if on[string(f)] {
			state = "[ON ]"
		}
		writeExperimentalRow(&b, state, string(f), experimental.Description(f), labelWidth, descWidth)
	}
	b.WriteString("\n")
	if len(enabled) == 0 {
		b.WriteString(styleFooter.Render("Enabled this session: none"))
	} else {
		fmt.Fprintf(&b, "%s %s", styleFooter.Render("Enabled this session:"), stylePaletteItem.Render(strings.Join(enabled, ", ")))
	}
	b.WriteString("\n")
	b.WriteString(styleFooter.Render("esc/any key close"))

	return strings.TrimRight(b.String(), "\n")
}

// writeExperimentalRow wraps long descriptions under the description column so
// feature text stays inside the submenu frame on normal terminal widths.
func writeExperimentalRow(b *strings.Builder, state, name, desc string, labelWidth, descWidth int) {
	name = fmt.Sprintf("%-*s", labelWidth, name)
	wrapped := strings.Split(ansi.Wrap(desc, descWidth, ""), "\n")
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	status := styleMeta.Render(state)
	if state == "[ON ]" {
		status = styleSystemSuccess.Render(state)
	}
	prefix := "  " + status + " " + styleSplashTitle.Render(name) + styleMeta.Render(" — ")
	continuation := "  " + strings.Repeat(" ", len(state)+1+labelWidth+3)
	for i, line := range wrapped {
		if i == 0 {
			b.WriteString(prefix)
		} else {
			b.WriteString(continuation)
		}
		b.WriteString(stylePaletteItem.Render(line))
		b.WriteString("\n")
	}
}
