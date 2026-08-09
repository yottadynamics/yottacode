package wizard

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

// Wizard-local styles. Kept separate from internal/tui/styles.go so
// the wizard can render correctly even when the chat TUI hasn't been
// loaded (e.g. when init runs from a fresh shell without launching
// the chat program).
var (
	colorBrand = compat.AdaptiveColor{Light: lipgloss.Color("2"), Dark: lipgloss.Color("10")}
	// Accent: Go "Gopher Blue" (#00ADD8) — the official Go brand
	// blue from go.dev's brand guidelines. We dim it slightly for
	// light terminals (#0086a8) so it reads with enough contrast
	// against white. Used for section headings and "interactive
	// value / focus" callouts (focused field marker, configured
	// names off-cursor, plan summary values, paths, "Setup
	// complete", the `provider · model` line on the Done screen,
	// and the `yottacode` launch callout). Brand green carries
	// chrome / progress / completion; Gopher Blue carries
	// everything labelled / selectable — also a subtle nod to the
	// language the project is written in.
	colorAccent = compat.AdaptiveColor{Light: lipgloss.Color("#0086a8"), Dark: lipgloss.Color("#00add8")}
	colorMuted  = compat.AdaptiveColor{Light: lipgloss.Color("#7a7a7a"), Dark: lipgloss.Color("#888888")}
	colorDim    = compat.AdaptiveColor{Light: lipgloss.Color("#9a9a9a"), Dark: lipgloss.Color("#666666")}
	colorWarn   = compat.AdaptiveColor{Light: lipgloss.Color("#af5f00"), Dark: lipgloss.Color("#ffaf5f")}
	colorErr    = compat.AdaptiveColor{Light: lipgloss.Color("#af0000"), Dark: lipgloss.Color("#ff5f5f")}
	// colorOK aliases colorBrand so ✓ markers, env-present badges, and
	// validation OK glyphs all read as the same single green as the
	// rest of the wizard's brand chrome — no second shade in play.
	colorOK     = colorBrand
	colorRule   = compat.AdaptiveColor{Light: lipgloss.Color("#b0b0b0"), Dark: lipgloss.Color("#444444")}
	colorChipBG = compat.AdaptiveColor{Light: lipgloss.Color("#e6e6e6"), Dark: lipgloss.Color("#262626")}
	colorTierC  = compat.AdaptiveColor{Light: lipgloss.Color("#005f00"), Dark: lipgloss.Color("#87d787")}
	colorTierB  = compat.AdaptiveColor{Light: lipgloss.Color("#5f5f00"), Dark: lipgloss.Color("#d7d787")}
	colorTierE  = compat.AdaptiveColor{Light: lipgloss.Color("#5f0000"), Dark: lipgloss.Color("#ff8787")}

	styleTitle    = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	styleHeading  = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleHint     = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleMuted    = lipgloss.NewStyle().Foreground(colorMuted)
	styleDim      = lipgloss.NewStyle().Foreground(colorDim)
	styleWarn     = lipgloss.NewStyle().Foreground(colorWarn).Bold(true)
	styleErr      = lipgloss.NewStyle().Foreground(colorErr).Bold(true)
	styleOK       = lipgloss.NewStyle().Foreground(colorOK).Bold(true)
	styleSelected = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleStep     = lipgloss.NewStyle().Foreground(colorBrand)
	styleProgress = lipgloss.NewStyle().Foreground(colorBrand)
	// styleOptionSelected mirrors the TUI slash palette's selected-row
	// pattern (internal/tui/styles.go::stylePaletteSelected): green
	// background, black foreground, bold. No padding — the bar wraps
	// the text tightly so that cursor rows align column-for-column with
	// non-cursor rows in the same list (a left-pad would push selected
	// labels one column right of unselected siblings).
	styleOptionSelected = lipgloss.NewStyle().
				Background(colorBrand).
				Foreground(lipgloss.Color("0")).
				Bold(true)
	styleRule   = lipgloss.NewStyle().Foreground(colorRule)
	styleChip   = lipgloss.NewStyle().Background(colorChipBG).Foreground(colorAccent).Padding(0, 1)
	stylePathHL = lipgloss.NewStyle().Foreground(colorAccent).Underline(true)
)

// tierStyle returns a style appropriate for rendering a tier badge.
// Empty tier renders muted; cheap is green, balanced is amber,
// expensive is red. Names are short ("c" / "b" / "e") for visual
// density on the model picker.
func tierStyle(tier string) lipgloss.Style {
	switch tier {
	case "cheap":
		return lipgloss.NewStyle().Foreground(colorTierC)
	case "balanced":
		return lipgloss.NewStyle().Foreground(colorTierB)
	case "expensive":
		return lipgloss.NewStyle().Foreground(colorTierE)
	}
	return styleDim
}

// tierBadge returns "[cheap]" / "[balanced]" / "[expensive]" / "" in
// the right color. Used in lists where a single line carries the
// tier label inline.
func tierBadge(tier string) string {
	if tier == "" {
		return ""
	}
	return tierStyle(tier).Render("[" + tier + "]")
}
