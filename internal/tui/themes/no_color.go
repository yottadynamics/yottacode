package themes

import "github.com/charmbracelet/lipgloss"

// ansi returns an AdaptiveColor that names ANSI palette indices on
// each side. Numbers are strings on purpose — lipgloss.Color accepts
// decimal strings for the 16-color palette. Lives here because
// no-color is the only remaining caller after the terminal theme
// was reverted to AdaptiveColor hex pairs.
func ansi(light, dark string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: light, Dark: dark}
}

// noColor collapses every role to default-foreground (ANSI 7) so
// the output reads as plain text. Honors the spirit of the
// NO_COLOR env var convention (https://no-color.org/) without the
// startup-only constraint — useful when piping output to a tool
// that doesn't strip ANSI, or just for users who prefer a
// monochrome surface.
//
// Highlight: "bw" — chroma's black-and-white style, so even code
// blocks stay monochrome.
func init() {
	register(Palette{
		Name:        "no-color",
		Description: "monochrome — every role renders as default terminal foreground",
		Highlight:   "bw",
		Monochrome:  true,

		// "7" is default-foreground in the ANSI 16-color table; on
		// both light and dark terminals it picks the readable
		// foreground the user already chose, so the result is
		// truly plain text on the user's own background.
		Accent:    ansi("7", "7"),
		Success:   ansi("7", "7"),
		Warning:   ansi("7", "7"),
		Error:     ansi("7", "7"),
		Content:   ansi("7", "7"),
		Dim:       ansi("7", "7"),
		Rule:      ansi("7", "7"),
		Assistant: ansi("7", "7"),
		Code:      ansi("7", "7"),
		Warm:      ansi("7", "7"),
	})
}
