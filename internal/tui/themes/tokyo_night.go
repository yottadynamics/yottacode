package themes

// tokyoNight is Enno Lohmeier's Tokyo Night palette
// (https://github.com/enkia/tokyo-night-vscode-theme). Deep navy
// backdrop with vivid blue, purple, and cyan — much more saturated
// than Nord while staying on the cool side of the spectrum. The
// "Night" variant (vs. Storm / Moon) is the canonical one; chroma's
// tokyonight-night style pairs with it for fenced code.
//
// Note the Dim role uses #565f89 (Tokyo Night's "comment" color, a
// muted blue-grey) rather than a neutral grey — keeping comments on
// a desaturated cousin of the accent ramp is what makes the theme
// read as cohesive rather than "blue accents on monochrome chrome."
func init() {
	register(Palette{
		Name:        "tokyo-night",
		Description: "Tokyo Night — deep navy with vivid blue, purple, and cyan",
		Highlight:   "tokyonight-night",

		Accent:    pin("#7aa2f7"), // blue
		Success:   pin("#9ece6a"), // green
		Warning:   pin("#e0af68"), // yellow
		Error:     pin("#f7768e"), // red
		Content:   pin("#c0caf5"), // foreground
		Dim:       pin("#565f89"), // comment (muted blue-grey)
		Rule:      pin("#3b4261"), // selection background
		Assistant: pin("#7dcfff"), // cyan
		Code:      pin("#c0caf5"),
		Warm:      pin("#ff9e64"), // orange

		// #1a1b26 is Tokyo Night's canonical "Night" variant
		// background — the deep navy the theme is recognized by. Real
		// terminal-background theming (OSC 11) repaints the terminal
		// canvas itself to this, not just in-app chrome.
		HasBackground: true,
		Background:    pin("#1a1b26"),
	})
}
