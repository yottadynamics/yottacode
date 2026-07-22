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

// autoCycleKeysHint is the "how do I leave" copy on the persistent
// auto banner. Shift+Tab from auto moves onward to plan (not straight
// to normal), so the honest verb is "cycles", matching the entry log's
// "Shift+Tab cycles onward: auto → plan → normal".
const autoCycleKeysHint = "Shift+Tab cycles"

// renderAutoModeBanner is the one-line indicator above the cmdline
// while auto mode is active. The optional yolo suffix means the yolo
// mode overlay is also on; in that case we drop the activity detail
// (yolo overrides it — bash auto-allows too) so the banner doesn't
// say something misleading. The cycle-keys hint rides along whenever
// there's room (mirrors Claude Code's persistent "shift+tab to cycle"
// indicator) and is the first segment dropped on narrow terminals.
//
// Activity wording reflects the safe-bash carve-out: read-only shell
// commands (cd, ls, cat, grep, find, …) auto-allow alongside edits;
// only mutating bash and commits still prompt. The earlier
// "bash & commits prompt" wording was a footgun once that yolo
// mode landed — it implied every shell call interrupted the flow.
//
//	▸ auto mode · edits + read-only bash auto-allow; commits prompt · Shift+Tab cycles   (no yolo)
//	▸ auto mode · ⚠ yolo mode · Shift+Tab cycles                                         (yolo on)
func renderAutoModeBanner(yoloOn bool, width int) string {
	if width <= 0 {
		width = 80
	}
	label := styleAutoBannerLabel.Render(AutoModeIcon + " auto mode")
	dot := styleAutoBannerSep.Render(" · ")
	hint := dot + styleAutoBannerHint.Render(autoCycleKeysHint)

	if yoloOn {
		core := label + dot + renderYoloTag()
		if ansi.StringWidth(core+hint) <= width {
			return core + hint
		}
		if ansi.StringWidth(core) <= width {
			return core
		}
		return label
	}

	core := label + dot + styleAutoBannerActivity.Render("edits + read-only bash auto-allow; commits prompt")
	if ansi.StringWidth(core+hint) <= width {
		return core + hint
	}
	if ansi.StringWidth(core) <= width {
		return core
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
