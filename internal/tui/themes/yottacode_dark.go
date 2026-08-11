package themes

// yottacodeDark is the YottaCode signature dark theme — a neutral dark
// gray base with Tokyo Night-inspired accents, modeled closely on
// GrokNight (Grok Build's default dark theme). The base is a calm
// charcoal ramp (#0a0a0a → #2a2a2a) that stays readable for long
// sessions and quantizes cleanly on 256/16-color terminals, while
// the accent palette keeps GrokNight's familiar magenta/blue/cyan
// vocabulary so the surface feels professional rather than flashy.
//
// Identity choices that distinguish this from tokyo-night:
//   - A neutral gray base instead of Tokyo Night's deep navy, so
//     the theme reads as "carbon dark" rather than "blue dark."
//   - The primary accent stays Grok magenta (#bb9af7) — the same
//     hue GrokNight uses for commands, headings, and emphasis.
//   - The Assistant role is the Yotta signature: a bright cyan-teal
//     (#00e5ff) that gives the agent its own identity without
//     clashing with the magenta-led accent ramp.
//
// HasBackground=true: chrome surfaces (palette overlay, approval
// modal, watermark box) paint the #121212 charcoal so the theme's
// identity is visible even though the chat area itself inherits the
// terminal's bg. Paired with the chroma "tokyonight-night" style
// for fenced code blocks — the code-block colors share the same
// Tokyo Night accent family, keeping the whole surface cohesive.
func init() {
	register(Palette{
		Name:        "yottacode-dark",
		Description: "YottaCode dark — neutral charcoal base with Tokyo Night accents and a cyan-teal Yotta signature",
		Highlight:   "tokyonight-night",

		Accent:    pin("#bb9af7"), // Grok magenta — primary accent / commands / headings
		Success:   pin("#9ece6a"), // Tokyo Night green
		Warning:   pin("#e0af68"), // Tokyo Night yellow
		Error:     pin("#f7768e"), // Tokyo Night red
		Content:   pin("#e8e8e8"), // primary text — slightly brighter than Grok's #e1e1e1 for clarity
		Dim:       pin("#6a6a6a"), // muted / comments — neutral gray, not blue-tinted
		Rule:      pin("#1e1e1e"), // subtle elevation for borders / separators
		Assistant: pin("#00e5ff"), // Yotta signature cyan-teal — agent identity
		Code:      pin("#e8e8e8"), // matches Content for consistent code text
		Warm:      pin("#ff9e64"), // Tokyo Night orange for paths / warm states

		HasBackground: true,
		// #121212 sits one shade deeper than GrokNight's #141414 —
		// a touch more "pure night" while staying off pure black so
		// video compression has gradients to work with.
		Background: pin("#121212"),
	})
}
