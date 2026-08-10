package themes

// yottacodeDark is the polished house dark palette, grounded in the
// public GrokNight token set and Cursor Dark's muted editor chrome. It
// keeps the canvas at Grok/Cursor's shared #141414 black, uses soft
// Nordic/Cursor foregrounds, and leans on blue/cyan/green accents instead
// of a loud neon ramp so long model sessions stay calm.
//
// HasBackground=true pins both the in-app chrome and OSC 11 terminal
// canvas to the same #141414 backdrop. That exact base is important: it
// matches the documented GrokNight background and Cursor Dark's outer UI,
// while the TUI's framed surfaces still get contrast from charcoal rules
// and the syntax highlighter's darker code-block treatment.
func init() {
	register(Palette{
		Name:        "yottacode-dark",
		Description: "yottacode dark — Grok/Cursor-inspired #141414 canvas with muted blue, cyan, and green accents",
		Highlight:   "github-dark",

		Accent:    pin("#7aa2f7"), // GrokNight blue for primary focus without Cursor's brighter button-blue pop
		Success:   pin("#9ece6a"), // GrokNight green for approvals and completed state
		Warning:   pin("#e0af68"), // GrokNight yellow/amber, close to Cursor Dark's warning tone
		Error:     pin("#f7768e"), // GrokNight red/rose, softer than pure alert red
		Content:   pin("#d8dee9"), // Cursor Dark editor foreground; softer than Grok's brighter #e1e1e1
		Dim:       pin("#cccccc99"), // Cursor Dark secondary foreground for low-noise metadata
		Rule:      pin("#3a3a3a"), // Cursor Dark border/input chrome charcoal, lifted for readable lines on #141414
		Assistant: pin("#7dcfff"), // GrokNight cyan for model/assistant identity
		Code:      pin("#e1e1e1"), // GrokNight foreground for code and preformatted text
		Warm:      pin("#ff9e64"), // GrokNight orange for active/warm UI emphasis

		HasBackground: true,
		Background:    pin("#141414"),
	})
}
