package tui

import "github.com/charmbracelet/lipgloss"

// Canonical palette — see docs/TUI.md "Color Palette". Seven semantic roles:
//
//	Accent   — primary accent: prompts, active selection, focus
//	Success  — ✓ ok, healthy status dot
//	Warning  — ⚠ approval prompts, watermark notices
//	Error    — failures, errors
//	Content  — primary text, values
//	Dim      — labels, hints, separators, telemetry  (mid-gray, readable text)
//	Muted    — borders, gutters, disabled chrome      (dark-gray, decoration only)
//
// The single most important rule: labels are always Dim, values are always
// Content, and never the reverse. State colors (Success/Warning/Error)
// are reserved for state — never used for decoration.
//
// Values are AdaptiveColor pairs so the terminal's light/dark mode picks
// the readable variant.
//
// Naming caveat: the legacy `colorMuted` token (kept as an alias below)
// historically meant "dim text" — it maps to the spec's `Dim` role, NOT
// the spec's `Muted` (which is dark-gray for borders). The canonical
// dark-gray border color is `colorRule`. New code should use the
// canonical names; the legacy aliases stay until Phase 4 sweeps them.
var (
	// Accent: cyan — drives prompts, active selections, focus highlights.
	colorAccent = lipgloss.AdaptiveColor{Light: "#0077a3", Dark: "#5fd7ff"}
	// Success: green — ✓ ok, healthy status dot. ANSI green on the
	// terminal palette so it matches the user's theme.
	colorSuccess = lipgloss.AdaptiveColor{Light: "2", Dark: "10"}
	// Warning: amber — ⚠ approval modal border, watermark notices.
	colorWarning = lipgloss.AdaptiveColor{Light: "#af5f00", Dark: "#ffaf5f"}
	// Error: red — failures, error notices.
	colorError = lipgloss.AdaptiveColor{Light: "#af0000", Dark: "#ff5f5f"}
	// Content: primary text and values. Slightly off from pure white
	// on dark terminals so it doesn't visually clip against the cursor.
	colorContent = lipgloss.AdaptiveColor{Light: "#202020", Dark: "#e4e4e4"}
	// Dim: mid-gray — readable text used for labels, hints, separators,
	// telemetry, and the status bar baseline. Same value as the legacy
	// colorMuted on purpose; the two are aliases.
	colorDim = lipgloss.AdaptiveColor{Light: "#7a7a7a", Dark: "#787878"}

	// --- Legacy aliases. Keep until Phase 4 sweeps the rest of the
	// codebase onto the canonical names. ---
	colorMuted     = colorDim                                                // legacy "muted text" = spec's Dim
	colorBrand     = colorSuccess                                            // brand mark = green; success state shares it
	colorAssistant = lipgloss.AdaptiveColor{Light: "#005f5f", Dark: "#87cdcd"} // teal — assistant header (kept distinct for now)
	colorWarn      = colorWarning
	colorErr       = colorError
	// colorRule is the dark-gray decoration color (spec's "Muted" role)
	// — borders, overlay rules, code-block left bar.
	colorRule = lipgloss.AdaptiveColor{Light: "#b0b0b0", Dark: "#444444"}
	colorCode = lipgloss.AdaptiveColor{Light: "#404040", Dark: "#c0c0c0"}
	colorWarm = lipgloss.AdaptiveColor{Light: "#875f00", Dark: "#d7af00"}

	styleLogo             = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	styleSplashTitle      = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	styleSplashInfo       = lipgloss.NewStyle().Foreground(colorMuted)
	styleSeparator        = lipgloss.NewStyle().Foreground(colorMuted)
	styleFooter           = lipgloss.NewStyle().Foreground(colorMuted)
	styleUserHeader       = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleAssistantHeader  = lipgloss.NewStyle().Bold(true).Foreground(colorAssistant)
	// Thin colored left border replaces the old inverse-pill user block.
	// The user already knows what they typed; the bar is enough of a
	// visual anchor when scrolling back. Body renders Dim so it sits
	// politely above the assistant's brighter response.
	styleUserBar  = lipgloss.NewStyle().Foreground(colorRule).Bold(true)
	styleUserBody = lipgloss.NewStyle().Foreground(colorDim)
	styleAssistantBody    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#202020", Dark: "#d7d7d7"}).PaddingLeft(2)
	styleAssistantBold    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#202020", Dark: "#ffffff"}).Bold(true)
	styleAssistantHeading = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true).Underline(true).PaddingLeft(2)
	styleAssistantQuote   = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleInlineCode       = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	styleInlinePath       = lipgloss.NewStyle().Foreground(colorAccent).Underline(true)
	styleInlineCommand    = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	styleListMarker       = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	styleBullet           = styleListMarker
	styleActionVerb       = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	styleThinking         = lipgloss.NewStyle().Faint(true).Foreground(colorMuted)
	styleToolCall         = lipgloss.NewStyle().Foreground(colorWarn)
	styleToolMeta         = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	styleCodeBlock        = lipgloss.NewStyle().Foreground(colorCode).BorderLeft(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(colorRule).PaddingLeft(2)
	// styleStatusModel renders the model-name segment of the status bar.
	// The connection dot already carries the "active" signal; demoting the
	// model name to plain Content keeps the bar from competing with the
	// dot for attention. (Earlier versions painted this in Accent + Bold
	// — too loud once the dot took over the active-state job.)
	styleStatusModel      = lipgloss.NewStyle().Foreground(colorContent)
	styleAuto             = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleError            = lipgloss.NewStyle().Foreground(colorErr).Bold(true)
	styleApprovalBox      = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorWarn).
				Padding(0, 1)
	stylePaletteBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBrand).
			Padding(0, 1)
	stylePaletteItem     = lipgloss.NewStyle().Foreground(colorMuted)
	stylePaletteSelected = lipgloss.NewStyle().
				Background(colorBrand).
				Foreground(lipgloss.Color("0")).
				Bold(true)
	stylePaletteEmpty = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	// Diff marker styles, used by both the approval modal's renderEditDiff
	// and the tool card's editFileDiffRows. Bold + canonical state colors
	// (Error red for `-`, Success green for `+`) so the marker carries the
	// add/remove signal without needing a row-wide background tint —
	// background-painted rows read as too loud when stacked next to one
	// another, and Chroma's per-token `\x1b[0m` resets fight any bg the
	// row tries to hold across the line.
	styleDiffAdd = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	styleDiffDel = lipgloss.NewStyle().Foreground(colorError).Bold(true)
	stylePathHeader   = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleSpinner      = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)

	// Cmdline framing: the input is borderless by default — the chevron
	// prompt + content carry enough visual weight, and a saturated frame
	// drowns out everything inside it. If a border becomes desirable
	// again as a focus indicator, the rule is: no border idle, Muted
	// border when focused with content, Accent only on validation error.
	// Prompt char rendered before the input value. Accent (cyan) + bold
	// so the chevron pops as the focal point of the input row.
	styleInputPrompt = lipgloss.NewStyle().Foreground(colorRule).Bold(true)
	// Placeholder text shown when the input is empty: dim italic so it
	// recedes the moment the user starts typing.
	styleInputPlaceholder = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	// First-session hint footer below the input — `/ commands · @ files
	// · ↑↓ history`. Dim, no italic, indented to align with the input
	// text. Disappears permanently after the first user message.
	styleInputHint = lipgloss.NewStyle().Foreground(colorDim)

	// styleOverlayRule draws the thin horizontal rule between the
	// status bar and an inline picker overlay (cheatsheet, model,
	// provider, memory). Faint/dim so it reads as a separator rather
	// than competing with content above and below.
	styleOverlayRule = lipgloss.NewStyle().Foreground(colorRule).Faint(true)

	// Context-window watermark styling. styleWatermark is the muted
	// notice line when usage crosses warn_threshold; styleWatermarkAlert
	// is the louder banner that announces auto-summarization. The alert
	// sits in a bordered box so it stands out from the normal "·"
	// notices the agent emits during a turn. Status-bar threshold
	// tiers used to live here too — they're now inlined into
	// renderContextBar (the bar fill + percentage take the same color
	// directly, no separate style needed).
	styleWatermark      = lipgloss.NewStyle().Foreground(colorWarn).Italic(true).PaddingLeft(2)
	styleWatermarkAlert = lipgloss.NewStyle().Foreground(colorWarn).Bold(true).PaddingLeft(2)
	styleWatermarkBox   = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorWarn).
				Foreground(colorWarn).
				Bold(true).
				Padding(0, 1).
				MarginLeft(2)

	// styleTurnFooter is the small italic-muted indent used by inline
	// notices (summarize results, the soft "writing code" preview
	// while a fenced block is mid-stream, and the end-of-turn
	// "› Thought for Ns" footnote).
	styleTurnFooter = lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Faint(true).PaddingLeft(2).MarginTop(1)
)
