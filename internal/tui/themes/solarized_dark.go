package themes

// solarizedDark is Ethan Schoonover's canonical Solarized Dark palette
// (https://ethanschoonover.com/solarized/). Designed as a precise
// 16-color scheme with deliberate L*a*b* relationships — the dim/rule
// values use the in-between "base01/base02" tones rather than greys
// pulled from a separate ramp.
//
// Pairs with the chroma solarized-dark style for matching code-block
// colors; without that pairing the UI palette would clash with chroma's
// default monokai output. Pinned (Light = Dark) because the theme is
// only meaningful on a dark terminal — the matching solarized-light
// is a separate registration if a user wants that look.
func init() {
	register(Palette{
		Name:        "solarized-dark",
		Description: "Ethan Schoonover's classic — deliberate L*a*b* tones, blue base, amber/violet accents",
		Highlight:   "solarized-dark",

		Accent:    pin("#268bd2"), // blue
		Success:   pin("#859900"), // green
		Warning:   pin("#b58900"), // yellow
		Error:     pin("#dc322f"), // red
		Content:   pin("#93a1a1"), // base1 — primary content
		Dim:       pin("#586e75"), // base01 — comments / secondary
		Rule:      pin("#073642"), // base02 — borders / decoration
		Assistant: pin("#2aa198"), // cyan
		Code:      pin("#eee8d5"), // base2 — high-contrast for fenced code
		Warm:      pin("#cb4b16"), // orange
	})
}
