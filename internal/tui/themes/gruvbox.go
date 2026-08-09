package themes

// gruvbox is the Gruvbox Dark palette (originally a Vim colorscheme).
// Warm retro tones — burnt orange, mustard yellow, sage green — on
// dark brown rather than pure black. The "dark medium" contrast
// variant; the original ships hard / medium / soft. Picks up the
// chroma gruvbox style for consistent code-block colors.
//
// Distinct from Solarized in feel even though both lean warm:
// Solarized is precise and cool-leaning, Gruvbox is unapologetically
// warm and saturated.
func init() {
	register(Palette{
		Name:        "gruvbox",
		Description: "Warm retro — burnt orange and sage on dark brown",
		Highlight:   "gruvbox",

		Accent:    pin("#83a598"), // bright_aqua
		Success:   pin("#b8bb26"), // bright_green
		Warning:   pin("#fabd2f"), // bright_yellow
		Error:     pin("#fb4934"), // bright_red
		Content:   pin("#ebdbb2"), // fg
		Dim:       pin("#928374"), // gray
		Rule:      pin("#504945"), // bg2
		Assistant: pin("#8ec07c"), // bright_aqua-2
		Code:      pin("#ebdbb2"),
		Warm:      pin("#fe8019"), // bright_orange

		// #282828 is Gruvbox's canonical "dark medium" bg0 — the
		// signature dark-brown backdrop the whole palette is built
		// around. Real terminal-background theming (OSC 11) repaints
		// the terminal canvas itself to this, not just in-app chrome.
		HasBackground: true,
		Background:    pin("#282828"),
	})
}
