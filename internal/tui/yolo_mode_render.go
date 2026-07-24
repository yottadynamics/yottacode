package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// YoloModeIcon marks the yolo mode overlay across all surfaces.
// Warning sign — signals "this is the danger setting"
// visually before any text is read. Red color reinforces the meaning.
const YoloModeIcon = "⚠"

// renderYoloTag returns the inline "⚠ yolo mode" decoration appended
// to the active mode's banner when the yolo mode overlay is on. Short
// form for the inline tag — the full "yolo mode" phrase appears on the
// entry banner and the standalone banner; here the icon + label
// does the job without crowding the mode label. Red so it stands out
// against the mode label even at a glance.
func renderYoloTag() string {
	return styleYoloBannerLabel.Render(YoloModeIcon + " yolo mode")
}

// renderYoloStandaloneBanner is shown when the yolo mode overlay is
// on AND no mode (plan/auto) is active. Yolo isn't a mode itself, but
// the user still needs a banner signal that "rails are off." Compact
// form:
//
//	⚠ yolo mode · all tools auto-allow · no iteration cap
//
// Narrow terminals collapse to just `⚠ yolo mode`.
func renderYoloStandaloneBanner(width int) string {
	if width <= 0 {
		width = 80
	}
	label := styleYoloBannerLabel.Render(YoloModeIcon + " yolo mode")
	dot := styleYoloBannerSep.Render(" · ")
	mid := styleYoloBannerActivity.Render("all tools auto-allow · no iteration cap")

	out := label + dot + mid
	if ansi.StringWidth(out) <= width {
		return out
	}
	return label
}

var (
	// Red + bold so this banner is unmistakable. We deliberately
	// don't reuse the warning yellow that auto/plan use — yolo is
	// the danger setting and the color must say so.
	styleYoloBannerLabel    = lipgloss.NewStyle().Foreground(colorError).Bold(true)
	styleYoloBannerHint     = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	styleYoloBannerSep      = lipgloss.NewStyle().Foreground(colorRule)
	styleYoloBannerActivity = lipgloss.NewStyle().Foreground(colorContent)
)
