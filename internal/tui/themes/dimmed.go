package themes

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

// dimmed is GitHub Dark Dimmed
// (https://primer.style/foundations/color/overview, "dark_dimmed"
// variant). Slate-graphite backdrop (#22272e) with muted blue/green
// accents on a soft #adbac7 foreground — engineered by GitHub as
// the "easier on the eyes" alternative to full dark mode. The
// "soft graphite, neutral chroma" niche, pulled from a published
// design system rather than a hand-rolled neutral ramp.
//
// HasBackground=true: chrome surfaces (palette overlay, approval
// modal, watermark box) paint themselves on the slate backdrop so
// the theme identity is visible even though the chat area itself
// inherits the terminal's bg. Fenced code blocks intentionally
// stay on the terminal bg so chroma syntax highlighting reads clean.
//
// Pairs with the chroma github-dark style — chroma has no exact
// dimmed variant, but github-dark is the closest cousin and shares
// the muted-on-slate visual vocabulary.
func init() {
	register(Palette{
		Name:        "dimmed",
		Description: "GitHub Dark Dimmed — soft graphite, muted accents on a slate backdrop",
		Highlight:   "github-dark",

		Accent:    pin("#539bf5"), // fg.accent (dimmed blue)
		Success:   pin("#57ab5a"), // success.fg
		Warning:   pin("#c69026"), // attention.fg (yellow)
		Error:     pin("#f47067"), // danger.fg (red)
		Content:   pin("#adbac7"), // fg.default
		Dim:       pin("#768390"), // fg.muted
		Rule:      pin("#444c56"), // border.default
		Assistant: pin("#6cb6ff"), // syntax constant (blue-2, distinct from Accent)
		Code:      pin("#adbac7"),
		Warm:      pin("#f69d50"), // severe.fg (orange)

		HasBackground: true,
		// #22272e is GitHub Dark Dimmed's canvas.default — the
		// signature slate that distinguishes "dimmed" from full
		// dark mode (#0d1117). Far enough from terminal black to
		// read as a painted surface, gentle enough that it doesn't
		// fight the user's own terminal bg on chrome regions.
		Background: compat.AdaptiveColor{Light: lipgloss.Color("#22272e"), Dark: lipgloss.Color("#22272e")},
	})
}
