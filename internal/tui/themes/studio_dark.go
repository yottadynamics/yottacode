package themes

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

// studioDark is tuned for recorded terminal demos and screenshares.
// It uses a near-black charcoal backdrop instead of pure black so
// video compression has gradients to work with, pairs soft off-white
// text with yottacode green accents, and keeps secondary roles muted
// enough that cursor movement, approvals, and tool cards remain clear
// on YouTube/X without harsh white-on-black bloom.
//
// HasBackground=true makes chrome surfaces paint the same charcoal
// used in recording recommendations. The inline transcript still
// inherits the terminal background outside rendered regions, so users
// should pair this theme with a dark terminal profile for the full
// studio look.
func init() {
	register(Palette{
		Name:        "studio-dark",
		Description: "studio recording — charcoal backdrop, soft text, yottacode green accents",
		Highlight:   "github-dark",

		Accent:    pin("#00ff66"), // punchy yottacode green; vivid enough for demos without fading
		Success:   pin("#00e676"), // bright green with enough separation from the primary accent
		Warning:   pin("#ffd166"), // warm yellow that survives H.264 without neon glare
		Error:     pin("#ff5c5c"), // clear demo red without pure-red oversaturation
		Content:   pin("#f2fff7"), // crisp off-white to keep recorded terminals readable
		Dim:       pin("#a8cbb7"), // brighter metadata/tool detail text for screen recordings
		Rule:      pin("#008f4a"), // stronger green separators without overpowering recorded content
		Assistant: pin("#5cff9d"), // assistant identity stays close to the brand accent
		Code:      pin("#e8f7ee"), // code text remains readable but below body text
		Warm:      pin("#ffb86b"), // orange emphasis for active/warm UI states

		HasBackground: true,
		Background:    compat.AdaptiveColor{Light: lipgloss.Color("#0b0f0e"), Dark: lipgloss.Color("#0b0f0e")},
	})
}
