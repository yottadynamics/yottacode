package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// AutoModeIcon marks auto mode across all surfaces (banner, entry log,
// exit log). Small right-triangle — reads as "play / run" without
// the visual weight of the full ▶. Visually distinct from
// PlanModeIcon (◈) so users glance and know which mode is on.
const AutoModeIcon = "▸"

// renderAutoModeBanner is the one-line indicator above the cmdline
// while auto mode is active. The optional yolo suffix means the
// permissions-bypass overlay is also on; in that case we drop the
// activity detail (bypass overrides it — bash auto-allows too) so the
// banner doesn't say something misleading.
//
// Activity wording reflects the safe-bash carve-out: read-only shell
// commands (cd, ls, cat, grep, find, …) auto-allow alongside edits;
// only mutating bash and commits still prompt. The earlier
// "bash & commits prompt" wording was a footgun once that bypass
// landed — it implied every shell call interrupted the flow.
//
//	▸ auto mode · edits + read-only bash auto-allow; commits prompt   (no bypass)
//	▸ auto mode · ⚠ bypass                                            (bypass on)
func renderAutoModeBanner(yoloOn bool, width int) string {
	if width <= 0 {
		width = 80
	}
	label := styleAutoBannerLabel.Render(AutoModeIcon + " auto mode")
	dot := styleAutoBannerSep.Render(" · ")

	if yoloOn {
		out := label + dot + renderYoloTag()
		if ansi.StringWidth(out) <= width {
			return out
		}
		return label
	}

	mid := styleAutoBannerActivity.Render("edits + read-only bash auto-allow; commits prompt")
	out := label + dot + mid
	if ansi.StringWidth(out) <= width {
		return out
	}
	tight := label + dot + styleAutoBannerActivity.Render("edits auto-allow")
	if ansi.StringWidth(tight) <= width {
		return tight
	}
	return label
}

var (
	styleAutoBannerLabel    = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleAutoBannerHint     = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	styleAutoBannerSep      = lipgloss.NewStyle().Foreground(colorRule)
	styleAutoBannerActivity = lipgloss.NewStyle().Foreground(colorContent)
)
