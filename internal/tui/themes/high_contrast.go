package themes

// highContrast pushes every foreground to its terminal-bright pole
// and uses saturated state colors. Designed for low-vision users
// and bright-room terminals where the standard dark theme washes
// out. Pairs with the chroma "github-high-contrast" style — falls
// back to "monokai" if that's unavailable on the user's chroma
// build.
func init() {
	register(Palette{
		Name:        "high-contrast",
		Description: "maximum legibility — bright primaries on transparent bg, no mid-greys",
		Highlight:   "github-high-contrast",

		Accent:    pin("#00d7ff"), // bright cyan
		Success:   pin("#00ff5f"), // bright green
		Warning:   pin("#ffd700"), // bright amber
		Error:     pin("#ff0000"), // pure red
		Content:   pin("#ffffff"), // pure white
		// Dim collapses toward white so "muted" text is still readable
		// — the canonical Dim role is mid-gray, but mid-gray on a
		// black terminal is exactly what a low-vision user can't
		// read. Trade off: hint/label contrast drops vs. Content
		// since both are near-white now. Acceptable for this theme.
		Dim:       pin("#cccccc"),
		Rule:      pin("#888888"), // borders stay grey — they're decoration
		Assistant: pin("#5fffff"),
		Code:      pin("#ffffff"),
		Warm:      pin("#ffaf00"),
	})
}
