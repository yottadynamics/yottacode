package themes

import "github.com/charmbracelet/lipgloss"

// lowContrast is the deliberate inverse of `high-contrast`. Where
// high-contrast pushes every role toward its bright pole (pure red
// error, electric cyan accent, pure white content) so nothing
// blends into anything else, low-contrast pulls every role toward
// mid-grey so the entire surface sits in a narrow tonal band and
// nothing pops. The intent is a "calm" reading surface for long
// ambient sessions and users who find the default's saturation
// fatiguing.
//
// All foregrounds — accents, state colors, content — land in a
// ~30-luminance window so the eye doesn't get pulled to any one
// element. State colors are barely distinguishable from accents on
// purpose: in a low-contrast theme, you GLANCE to read content,
// not to spot status. That's a trade — errors aren't loud — and
// users who need state legibility should pick `dark` or
// `high-contrast` instead.
//
// Uses AdaptiveColor so the band shifts between terminal
// backgrounds (light terminals get darker mid-greys; dark terminals
// get lighter mid-greys), keeping "low contrast vs the terminal"
// consistent on both. Pairs with the chroma github style for code
// blocks — github's restrained palette already sits in the same
// tonal band as the UI chrome.
func init() {
	register(Palette{
		Name:        "low-contrast",
		Description: "deliberate calm — every role pulled toward mid-grey, nothing pops",
		Highlight:   "github",

		// State colors carry a faint hue cast (cool / green / amber /
		// rose) but stay desaturated and tonally close to Content.
		// Distinguishable on close reading, invisible at a glance.
		Accent:    lipgloss.AdaptiveColor{Light: "#5a7080", Dark: "#7a8a9a"}, // muted slate blue
		Success:   lipgloss.AdaptiveColor{Light: "#5a7060", Dark: "#7a8a78"}, // muted sage
		Warning:   lipgloss.AdaptiveColor{Light: "#7a6850", Dark: "#9a8a70"}, // muted tan
		Error:     lipgloss.AdaptiveColor{Light: "#80606a", Dark: "#9a7a80"}, // muted rose
		Content:   lipgloss.AdaptiveColor{Light: "#505050", Dark: "#a0a0a0"}, // mid-grey body text
		// Dim sits ONE step away from Content (not five, like in `dark`)
		// — the whole point is that labels and content are not loudly
		// differentiated.
		Dim:       lipgloss.AdaptiveColor{Light: "#707070", Dark: "#808080"},
		// Rule (borders) drops a second step away so chrome reads as
		// decoration without disappearing entirely.
		Rule:      lipgloss.AdaptiveColor{Light: "#909090", Dark: "#606060"},
		Assistant: lipgloss.AdaptiveColor{Light: "#506068", Dark: "#7a8a90"}, // muted teal
		Code:      lipgloss.AdaptiveColor{Light: "#505050", Dark: "#a0a0a0"},
		Warm:      lipgloss.AdaptiveColor{Light: "#706050", Dark: "#9a8a78"},
	})
}
