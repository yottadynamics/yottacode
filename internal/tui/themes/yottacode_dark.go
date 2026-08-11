package themes

// yottacodeDark is the polished house dark palette, mapped directly
// from xai-org/grok-build's GrokNight theme: a neutral #141414 main
// background, #e1e1e1 primary text, the GrokNight grayscale ramp, and
// TokyoNight accent colors. It keeps the TUI roles on source palette
// constants instead of sampled/approximate screenshot colors.
//
// HasBackground=true pins both the in-app chrome and OSC 11 terminal
// canvas to GrokNight's BG_STORM (#141414). Grok Build also defines a
// darker terminal bg (#0a0a0a) and highlight bg (#242424); yottacode's
// single Background role uses the main TUI canvas color so the visible
// surface matches the reference screenshot rather than pure black.
func init() {
	register(Palette{
		Name:        "yottacode-dark",
		Description: "yottacode dark — exact GrokNight #141414 canvas with #e1e1e1 text and TokyoNight accents",
		Highlight:   "github-dark",

		Accent:    pin("#e0af68"), // GrokNight command/warning yellow for primary focus
		Success:   pin("#9ece6a"), // GrokNight green
		Warning:   pin("#e0af68"), // GrokNight yellow
		Error:     pin("#f7768e"), // GrokNight red
		Content:   pin("#e1e1e1"), // GrokNight primary text
		Dim:       pin("#6c6c6c"), // GrokNight comment/muted gray
		Rule:      pin("#323237"), // GrokNight prompt_border chrome
		Assistant: pin("#bb9af7"), // GrokNight assistant/thinking magenta
		Code:      pin("#c8c8c8"), // GrokNight secondary text for code/variables
		Warm:      pin("#ff9e64"), // GrokNight path/orange

		HasBackground: true,
		Background:    pin("#141414"),
	})
}
