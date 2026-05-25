package themes

// catppuccin is the Catppuccin Mocha palette
// (https://github.com/catppuccin/catppuccin). The "Mocha" variant is
// the darkest of the four official flavors (Latte / Frappé /
// Macchiato / Mocha); we ship Mocha because the TUI is dark-leaning
// and Mocha is by far the most-used variant in the wild.
//
// Pastel-on-dark with low-saturation lavender, peach, and teal —
// reads as "warm dark" without Gruvbox's heavy retro feel. Pairs
// with the chroma catppuccin-mocha style for matching code-block
// colors.
func init() {
	register(Palette{
		Name:        "catppuccin",
		Description: "Catppuccin Mocha — pastel lavender, peach, and teal on warm dark",
		Highlight:   "catppuccin-mocha",

		Accent:    pin("#89b4fa"), // blue
		Success:   pin("#a6e3a1"), // green
		Warning:   pin("#f9e2af"), // yellow
		Error:     pin("#f38ba8"), // red
		Content:   pin("#cdd6f4"), // text
		Dim:       pin("#6c7086"), // overlay0
		Rule:      pin("#45475a"), // surface1
		Assistant: pin("#94e2d5"), // teal
		Code:      pin("#cdd6f4"), // text
		Warm:      pin("#fab387"), // peach
	})
}
