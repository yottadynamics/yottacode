package themes

import "github.com/charmbracelet/lipgloss"

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

		Accent:    lipgloss.AdaptiveColor{Light: "#0077a3", Dark: "#5fd7ff"},
		// ANSI green so the success dot matches the user's terminal
		// palette exactly — picked over a hex value on purpose.
		Success:   lipgloss.AdaptiveColor{Light: "2", Dark: "10"},
		Warning:   lipgloss.AdaptiveColor{Light: "#af5f00", Dark: "#ffaf5f"},
		Error:     lipgloss.AdaptiveColor{Light: "#af0000", Dark: "#ff5f5f"},
		Content:   lipgloss.AdaptiveColor{Light: "#202020", Dark: "#e4e4e4"},
		Dim:       lipgloss.AdaptiveColor{Light: "#7a7a7a", Dark: "#787878"},
		Rule:      lipgloss.AdaptiveColor{Light: "#b0b0b0", Dark: "#444444"},
		Assistant: lipgloss.AdaptiveColor{Light: "#005f5f", Dark: "#87cdcd"},
		Code:      lipgloss.AdaptiveColor{Light: "#404040", Dark: "#c0c0c0"},
		Warm:      lipgloss.AdaptiveColor{Light: "#875f00", Dark: "#d7af00"},
	})
}
