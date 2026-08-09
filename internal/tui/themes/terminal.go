package themes

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

// terminal is the main-branch look — the original AdaptiveColor
// palette yottacode shipped with before the themes refactor. Every
// role spells out a light/dark pair so the foreground adjusts to
// the terminal's reported background: dark text on a light terminal,
// light text on a dark terminal. The user's terminal palette
// (iTerm theme, kitty config, xterm scheme) still drives the actual
// rendered colors for the ANSI-named slots (Success), so this
// theme reads as "the look that respects your terminal's choices."
//
// Restored verbatim from the pre-themes styles.go after the prior
// terminal-theme implementation (ANSI 16-color only) drifted away
// from what main-branch users were used to seeing.
//
// No HasBackground — yottacode renders inline and the canonical
// look has always been to inherit the user's terminal backdrop.
// No Highlight override — falls through to the styles.go default
// ("monokai"), which is what main-branch shipped with for fenced
// code blocks.
func init() {
	register(Palette{
		Name:        "terminal",
		Description: "the main-branch look — adaptive colors that respect your terminal background",
		Highlight:   "",

		Accent: compat.AdaptiveColor{Light: lipgloss.Color("#0077a3"), Dark: lipgloss.Color("#5fd7ff")},
		// ANSI green so the success dot matches the user's terminal
		// palette exactly — picked over a hex value on purpose.
		Success:   compat.AdaptiveColor{Light: lipgloss.Color("2"), Dark: lipgloss.Color("10")},
		Warning:   compat.AdaptiveColor{Light: lipgloss.Color("#af5f00"), Dark: lipgloss.Color("#ffaf5f")},
		Error:     compat.AdaptiveColor{Light: lipgloss.Color("#af0000"), Dark: lipgloss.Color("#ff5f5f")},
		Content:   compat.AdaptiveColor{Light: lipgloss.Color("#202020"), Dark: lipgloss.Color("#e4e4e4")},
		Dim:       compat.AdaptiveColor{Light: lipgloss.Color("#7a7a7a"), Dark: lipgloss.Color("#787878")},
		Rule:      compat.AdaptiveColor{Light: lipgloss.Color("#b0b0b0"), Dark: lipgloss.Color("#444444")},
		Assistant: compat.AdaptiveColor{Light: lipgloss.Color("#005f5f"), Dark: lipgloss.Color("#87cdcd")},
		Code:      compat.AdaptiveColor{Light: lipgloss.Color("#404040"), Dark: lipgloss.Color("#c0c0c0")},
		Warm:      compat.AdaptiveColor{Light: lipgloss.Color("#875f00"), Dark: lipgloss.Color("#d7af00")},
	})
}
