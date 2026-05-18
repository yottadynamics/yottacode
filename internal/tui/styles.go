package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/tui/themes"
)

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
//
// THEMING NOTE: every color and style below is package-mutable. The
// var block holds the live values; buildStyles repopulates them from
// a themes.Palette. /themes set calls ApplyTheme which rebuilds the
// whole surface — that's why styles must be set with `=` (assign),
// not `:=` (decl), inside buildStyles.
var (
	// --- live color slots ---------------------------------------
	colorAccent    lipgloss.AdaptiveColor
	colorSuccess   lipgloss.AdaptiveColor
	colorWarning   lipgloss.AdaptiveColor
	colorError     lipgloss.AdaptiveColor
	colorContent   lipgloss.AdaptiveColor
	colorDim       lipgloss.AdaptiveColor
	colorMuted     lipgloss.AdaptiveColor // legacy alias: dim text
	colorBrand     lipgloss.AdaptiveColor // legacy alias: success/brand mark
	colorAssistant lipgloss.AdaptiveColor
	colorWarn      lipgloss.AdaptiveColor // legacy alias of colorWarning
	colorErr       lipgloss.AdaptiveColor // legacy alias of colorError
	colorRule      lipgloss.AdaptiveColor
	colorCode      lipgloss.AdaptiveColor
	colorWarm      lipgloss.AdaptiveColor

	// --- optional surface tint ----------------------------------
	// Themes that paint a backdrop (currently only "dimmed") set
	// hasThemeBackground=true; styles that want to honor it (code
	// block, palette overlay box, approval box, watermark box) read
	// themeBackground when painting a Background. Other styles
	// ignore it — most of the TUI stays on the user's terminal bg.
	hasThemeBackground bool
	themeBackground    lipgloss.AdaptiveColor

	// --- chroma highlight style name ----------------------------
	// Mirrored from themes.Palette.Highlight; falls back to
	// highlightStyleNameDefault when the palette doesn't specify
	// one (terminal theme). Read by highlight.go.
	themeHighlightStyle string

	// --- live style slots (rebuilt by buildStyles) --------------
	styleLogo             lipgloss.Style
	styleSplashTitle      lipgloss.Style
	styleSplashInfo       lipgloss.Style
	styleSeparator        lipgloss.Style
	styleFooter           lipgloss.Style
	styleUserHeader       lipgloss.Style
	styleAssistantHeader  lipgloss.Style
	styleUserBar          lipgloss.Style
	styleUserBody         lipgloss.Style
	styleAssistantBody    lipgloss.Style
	styleAssistantBold    lipgloss.Style
	styleAssistantHeading lipgloss.Style
	styleAssistantQuote   lipgloss.Style
	styleInlineCode       lipgloss.Style
	styleInlinePath       lipgloss.Style
	styleInlineCommand    lipgloss.Style
	styleListMarker       lipgloss.Style
	styleBullet           lipgloss.Style
	styleActionVerb       lipgloss.Style
	styleThinking         lipgloss.Style
	styleToolCall         lipgloss.Style
	styleToolMeta         lipgloss.Style
	styleCodeBlock        lipgloss.Style
	styleStatusModel      lipgloss.Style
	styleAuto             lipgloss.Style
	styleError            lipgloss.Style
	styleApprovalBox      lipgloss.Style
	stylePaletteBox       lipgloss.Style
	stylePaletteItem      lipgloss.Style
	stylePaletteSelected  lipgloss.Style
	stylePaletteEmpty     lipgloss.Style
	styleDiffAdd          lipgloss.Style
	styleDiffDel          lipgloss.Style
	stylePathHeader       lipgloss.Style
	styleSpinner          lipgloss.Style
	styleInputPrompt      lipgloss.Style
	styleInputPlaceholder lipgloss.Style
	styleInputHint        lipgloss.Style
	styleOverlayRule      lipgloss.Style
	styleWatermark        lipgloss.Style
	styleWatermarkAlert   lipgloss.Style
	styleWatermarkBox     lipgloss.Style
	styleTurnFooter       lipgloss.Style

	// activeThemeName tracks the palette in effect. Read by
	// /themes show and config persistence so the user sees what's
	// active without re-parsing config.toml.
	activeThemeName string
)

func init() {
	// Bring up the default palette so any code that touches style
	// vars before ApplyTheme runs (tests building a Model
	// directly, helper functions called from package init in
	// dependent files) sees a populated surface.
	buildStyles(themes.MustGet(themes.DefaultName))
}

// ApplyTheme swaps the active palette and rebuilds every style. An
// unknown name falls back to the default and returns false so the
// caller (/themes set, startup loader) can decide whether to log a
// warning. The TUI's render loop is "everything looks up globals
// at render time" — the next View() call picks up the new palette
// automatically. Components that captured a style by value at
// construction time (textarea prompt, spinner) need a separate
// refresh hook; Model.RefreshThemeStyles() does that work.
func ApplyTheme(name string) bool {
	p, ok := themes.Get(name)
	if !ok {
		p = themes.MustGet(themes.DefaultName)
	}
	buildStyles(p)
	return ok
}

// ActiveTheme returns the currently applied theme name. Used by
// /themes show and the preview header so the user can confirm what
// they're looking at without re-parsing config.toml.
func ActiveTheme() string {
	return activeThemeName
}

// buildStyles populates every package-level color and style slot
// from a palette. Idempotent — calling it twice with the same
// palette is a no-op visually; calling it with a different palette
// swaps the surface for every subsequent render.
//
// The body is intentionally flat (one assignment per slot) rather
// than table-driven. Each style carries its own modifier mix
// (Bold/Italic/Underline/Border/Padding) and trying to compress
// them through a registry obscures more than it saves.
func buildStyles(p themes.Palette) {
	activeThemeName = p.Name

	colorAccent = p.Accent
	colorSuccess = p.Success
	colorWarning = p.Warning
	colorError = p.Error
	colorContent = p.Content
	colorDim = p.Dim
	colorMuted = p.Dim         // legacy alias: "muted text" maps to Dim
	colorBrand = p.Success     // legacy alias: brand mark = green/success
	colorAssistant = p.Assistant
	colorWarn = p.Warning
	colorErr = p.Error
	colorRule = p.Rule
	colorCode = p.Code
	colorWarm = p.Warm

	hasThemeBackground = p.HasBackground
	themeBackground = p.Background

	themeHighlightStyle = p.Highlight

	styleLogo = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	styleSplashTitle = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	styleSplashInfo = lipgloss.NewStyle().Foreground(colorMuted)
	styleSeparator = lipgloss.NewStyle().Foreground(colorMuted)
	styleFooter = lipgloss.NewStyle().Foreground(colorMuted)
	styleUserHeader = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleAssistantHeader = lipgloss.NewStyle().Bold(true).Foreground(colorAssistant)
	styleUserBar = lipgloss.NewStyle().Foreground(colorRule).Bold(true)
	styleUserBody = lipgloss.NewStyle().Foreground(colorDim)
	styleAssistantBody = lipgloss.NewStyle().Foreground(colorContent).PaddingLeft(2)
	styleAssistantBold = lipgloss.NewStyle().Foreground(colorContent).Bold(true)
	styleAssistantHeading = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true).Underline(true).PaddingLeft(2)
	styleAssistantQuote = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleInlineCode = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	styleInlinePath = lipgloss.NewStyle().Foreground(colorAccent).Underline(true)
	styleInlineCommand = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	styleListMarker = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	styleBullet = styleListMarker
	styleActionVerb = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	styleThinking = lipgloss.NewStyle().Faint(true).Foreground(colorMuted)
	styleToolCall = lipgloss.NewStyle().Foreground(colorWarn)
	styleToolMeta = lipgloss.NewStyle().Foreground(colorDim).Italic(true)

	// Code block intentionally does NOT pick up the theme
	// background: chroma already emits its own ANSI colors for
	// syntax highlighting, and painting a uniform backdrop behind
	// every fenced block (markdown, YAML, Go, JSON…) muddies those
	// colors and reads as "you broke syntax highlighting." The
	// theme background still lands on chrome surfaces below
	// (palette overlay, approval/watermark boxes) where no
	// chroma-highlighted content competes with it.
	styleCodeBlock = lipgloss.NewStyle().
		Foreground(colorCode).
		BorderLeft(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(colorRule).
		PaddingLeft(2)

	styleStatusModel = lipgloss.NewStyle().Foreground(colorContent)
	styleAuto = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleError = lipgloss.NewStyle().Foreground(colorErr).Bold(true)
	styleApprovalBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorWarn).
		Padding(0, 1)
	if hasThemeBackground {
		styleApprovalBox = styleApprovalBox.Background(themeBackground)
	}

	paletteBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(0, 1)
	if hasThemeBackground {
		paletteBox = paletteBox.Background(themeBackground)
	}
	stylePaletteBox = paletteBox

	stylePaletteItem = lipgloss.NewStyle().Foreground(colorMuted)
	stylePaletteSelected = lipgloss.NewStyle().
		Background(colorBrand).
		Foreground(lipgloss.Color("0")).
		Bold(true)
	stylePaletteEmpty = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)

	styleDiffAdd = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	styleDiffDel = lipgloss.NewStyle().Foreground(colorError).Bold(true)
	stylePathHeader = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleSpinner = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)

	styleInputPrompt = lipgloss.NewStyle().Foreground(colorRule).Bold(true)
	styleInputPlaceholder = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	styleInputHint = lipgloss.NewStyle().Foreground(colorDim)
	styleOverlayRule = lipgloss.NewStyle().Foreground(colorRule).Faint(true)

	styleWatermark = lipgloss.NewStyle().Foreground(colorWarn).Italic(true).PaddingLeft(2)
	styleWatermarkAlert = lipgloss.NewStyle().Foreground(colorWarn).Bold(true).PaddingLeft(2)
	styleWatermarkBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorWarn).
		Foreground(colorWarn).
		Bold(true).
		Padding(0, 1).
		MarginLeft(2)
	if hasThemeBackground {
		styleWatermarkBox = styleWatermarkBox.Background(themeBackground)
	}

	styleTurnFooter = lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Faint(true).PaddingLeft(2).MarginTop(1)

	// --- Styles whose declarations live in other files ---------
	// These were previously declared with var-block initializers in
	// plan_mode_render.go / tool_card.go / subagent_card.go /
	// approval_modal.go — but Go evaluates var initializers BEFORE
	// any init() function runs, so they captured the zero
	// AdaptiveColor of every palette role and rendered as terminal
	// default fg (which on many terminals reads as a stuck yellow).
	// Building them HERE inside buildStyles fixes two things at once:
	// they pick up the current palette, AND they rebuild on every
	// ApplyTheme swap rather than staying frozen.

	// Plan-mode banner + approval (plan_mode_render.go)
	stylePlanBannerLabel = lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	stylePlanBannerHint = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	stylePlanBannerSep = lipgloss.NewStyle().Foreground(colorRule)
	stylePlanBannerActivity = lipgloss.NewStyle().Foreground(colorContent)
	stylePlanApprovalTitle = lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	stylePlanApprovalTool = lipgloss.NewStyle().Foreground(colorDim)
	stylePlanApprovalHotkey = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	stylePlanApprovalChoice = lipgloss.NewStyle().Foreground(colorContent)

	// Approval modal (approval_modal.go)
	styleApprovalTitle = lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	styleApprovalTool = lipgloss.NewStyle().Foreground(colorDim)
	styleApprovalCommand = lipgloss.NewStyle().Foreground(colorContent).Bold(true)
	styleApprovalHotkey = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleApprovalChoice = lipgloss.NewStyle().Foreground(colorContent)
	styleApprovalChoiceDim = lipgloss.NewStyle().Foreground(colorDim)
	styleApprovalToast = lipgloss.NewStyle().Foreground(colorSuccess)

	// Tool card + todo rows (tool_card.go)
	styleCardGutter = lipgloss.NewStyle().Foreground(colorRule)
	styleCardHeader = lipgloss.NewStyle().Foreground(colorContent).Bold(true)
	styleCardBody = lipgloss.NewStyle().Foreground(colorContent)
	styleCardMeta = lipgloss.NewStyle().Foreground(colorDim)
	styleCardOKFooter = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	styleCardErrFooter = lipgloss.NewStyle().Foreground(colorError).Bold(true)
	styleTodoDone = lipgloss.NewStyle().Foreground(colorDim).Strikethrough(true)
	styleTodoInProgress = lipgloss.NewStyle().Foreground(colorContent).Bold(true)
	styleTodoPending = lipgloss.NewStyle().Foreground(colorContent)
	styleTodoCheckDone = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	styleTodoArrow = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	styleTodoBullet = lipgloss.NewStyle().Foreground(colorDim)

	// Subagent card (subagent_card.go)
	styleSubagentLabel = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleSubagentMeta = lipgloss.NewStyle().Foreground(colorMuted)
	styleSubagentActivity = lipgloss.NewStyle().Foreground(colorContent)
	styleSubagentOK = lipgloss.NewStyle().Foreground(colorSuccess)
	styleSubagentErr = lipgloss.NewStyle().Foreground(colorError)
	styleSubagentRunning = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleSubagentCanceled = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleSubagentTableHeader = lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
}
