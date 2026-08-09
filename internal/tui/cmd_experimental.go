package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/experimental"
)

// cmdExperimental lists the experimental-feature catalog and which ones are
// switched on this session. It is read-only and informational: toggling a
// feature stays a startup concern (--experimental, YOTTACODE_EXPERIMENTAL, or
// the [experimental] config block), so this surfaces state rather than changing
// it.
func cmdExperimental(m Model, _ []string) (Model, tea.Cmd) {
	m.experimentalPanel = renderExperimentalPanel(m.experimentalEnabled, m.popupWidth())
	m.experimentalOpen = true
	return m, nil
}

// renderExperimentalPanel draws the catalog. Callers that know the live
// overlay width pass it so descriptions wrap to the terminal instead of
// running off the right edge; the variadic form keeps the width optional
// for tests, matching renderMenuHeader's convention.
func renderExperimentalPanel(enabled []string, widths ...int) string {
	width := menuDividerWidth
	if len(widths) > 0 && widths[0] > 0 {
		width = widths[0]
	}

	on := make(map[string]bool, len(enabled))
	for _, name := range enabled {
		on[name] = true
	}

	var b strings.Builder
	b.WriteString(renderMenuHeader("Experimental", "Opt-in features for this session; toggles apply at startup.", width))
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

	// Size the name column from the catalog itself. A hardcoded %-18s
	// was narrower than the longest name (lsp_code_intelligence, 21
	// cells), so those rows pushed their own [GA] marker past the
	// column and the whole list read as ragged.
	nameW := 0
	for _, f := range experimental.All() {
		nameW = max(nameW, ansi.StringWidth(string(f)))
	}

	// Descriptions are full sentences — too long to share a row with the
	// name at any realistic terminal width, which is why they used to
	// run off the right edge and get clipped mid-word. Give them their
	// own wrapped, indented lines: the name+state pair stays scannable
	// as a column, and the prose stays whole.
	const descIndent = "      "
	descW := width - len(descIndent)

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
		fmt.Fprintf(&b, "  %-*s  %s\n",
			nameW, string(f), stateStyle.Render(fmt.Sprintf("[%s]", state)))

		desc := experimental.Description(f)
		if desc == "" {
			continue
		}
		if descW > 0 {
			desc = ansi.Wrap(desc, descW, "")
		}
		for line := range strings.SplitSeq(desc, "\n") {
			b.WriteString(descIndent)
			b.WriteString(styleMeta.Render(line))
			b.WriteString("\n")
		}
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
