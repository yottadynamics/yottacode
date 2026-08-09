package themes

// oneDark is the One Dark palette (originally Atom's default dark
// theme, since ported to nearly every editor). Cool slate backdrop
// with muted blue, green, and a distinctive coral red. Sits between
// Tokyo Night (cooler, navy) and Gruvbox (warmer, retro) — the
// "default dark" muscle memory for a large slice of devs.
//
// Pairs with the chroma onedark style for matching code-block
// colors.
func init() {
	register(Palette{
		Name:        "one-dark",
		Description: "One Dark — Atom's classic slate with cool blue, green, and coral",
		Highlight:   "onedark",

		Accent:    pin("#61afef"), // blue
		Success:   pin("#98c379"), // green
		Warning:   pin("#e5c07b"), // yellow
		Error:     pin("#e06c75"), // coral red
		Content:   pin("#abb2bf"), // foreground
		Dim:       pin("#5c6370"), // comment grey
		Rule:      pin("#3e4451"), // selection
		Assistant: pin("#56b6c2"), // cyan
		Code:      pin("#abb2bf"),
		Warm:      pin("#d19a66"), // orange

		// #282c34 is One Dark's canonical editor background — the
		// slate backdrop the theme is recognized by across every port.
		// Real terminal-background theming (OSC 11) repaints the
		// terminal canvas itself to this, not just in-app chrome.
		HasBackground: true,
		Background:    pin("#282c34"),
	})
}
