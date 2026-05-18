package themes

// nord is Arctic Ice Studio's Nord palette (https://www.nordtheme.com).
// Cool blue-grey "northern" tones — polar nights, snow storms, frost,
// aurora — minimal and restrained, dialed down a few notches from
// Tokyo Night's saturation. Pairs with the chroma nord style for
// matching code-block colors.
//
// All accents pull from Nord's "Frost" group (cyan-blues) plus the
// "Aurora" group for state colors (green/yellow/orange/red/purple),
// so the theme stays cohesive even when warnings or errors fire.
func init() {
	register(Palette{
		Name:        "nord",
		Description: "Arctic Ice Studio's cool blue-grey — restrained northern tones",
		Highlight:   "nord",

		Accent:    pin("#88c0d0"), // frost-1 (cyan)
		Success:   pin("#a3be8c"), // aurora green
		Warning:   pin("#ebcb8b"), // aurora yellow
		Error:     pin("#bf616a"), // aurora red
		Content:   pin("#eceff4"), // snow storm 1
		Dim:       pin("#4c566a"), // polar night 4
		Rule:      pin("#3b4252"), // polar night 2
		Assistant: pin("#8fbcbb"), // frost-0 (teal)
		Code:      pin("#d8dee9"), // snow storm 3
		Warm:      pin("#d08770"), // aurora orange
	})
}
