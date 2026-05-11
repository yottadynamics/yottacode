package tui

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// planBannerInfo carries the state-derived inputs the plan-mode banner
// needs. Built fresh on every View() call from the live Model so the
// banner reflects the active plan file without caching.
//
// The banner intentionally does NOT show transient agent activity
// (thinking…, running grep, etc.) — the live thinking row above the
// cmdline already surfaces that signal. Doubling it up in the banner
// was noisy. The basename is the more useful at-a-glance info: "which
// plan am I in?".
type planBannerInfo struct {
	PlanFileBasename string // truncated basename of the resolved plan file; "" before slug is set
}

// PlanModeIcon is the glyph that marks plan mode across all surfaces
// (banner, entry log, resume log, exit log, plan-file lines, approval
// card title). Single source of truth so changing the icon is one
// edit. Diamond-with-dot — reads as "marked / centered intent"
// without the edit-mode overtones the literal pencil glyph carries.
const PlanModeIcon = "◈"

// renderPlanModeBanner is the one-line indicator rendered immediately
// above the cmdline while plan mode is active. Two states + an
// optional yolo suffix when the yolo overlay is on:
//
//	◈ plan mode · i-want-to-imple…0dae3.md
//	◈ plan mode · awaiting your message
//	◈ plan mode · i-want-to-imple…0dae3.md · ⚠ yolo
//
// Lowercase label is intentional — quieter than shouting PLAN MODE,
// matches the surrounding "plan mode active" / "plan resumed"
// entry-log voice. The plan-file basename (truncated) is the
// persistent middle text once the slug is resolved.
func renderPlanModeBanner(info planBannerInfo, yoloOn bool, width int) string {
	if width <= 0 {
		width = 80
	}
	label := stylePlanBannerLabel.Render(PlanModeIcon + " plan mode")
	dot := stylePlanBannerSep.Render(" · ")

	mid := planBannerMiddleText(info)
	out := label
	if mid != "" {
		out += dot + stylePlanBannerActivity.Render(mid)
	}
	if yoloOn {
		out += dot + renderYoloTag()
	}
	if ansi.StringWidth(out) <= width {
		return out
	}
	// Narrow terminal: drop the mid segment; keep label + (optional)
	// yolo so the warning never disappears.
	tight := label
	if yoloOn {
		tight += dot + renderYoloTag()
	}
	if ansi.StringWidth(tight) <= width {
		return tight
	}
	return label
}

// planBannerMiddleText is the segment after the mode label. Shows the
// plan-file basename once a slug is resolved, or the "awaiting your
// message" hint before that. Never returns transient agent activity —
// the live thinking row owns that signal.
func planBannerMiddleText(info planBannerInfo) string {
	if info.PlanFileBasename != "" {
		return info.PlanFileBasename
	}
	return "awaiting your message"
}

// computePlanBannerInfo gathers the live inputs the banner needs from
// the model. The basename is truncated so a long slug (which can be
// 70+ chars after the SHA-256 hash suffix lands) doesn't push the
// banner past the terminal width.
func computePlanBannerInfo(m Model) planBannerInfo {
	info := planBannerInfo{}
	if m.cfg.PlanMode != nil && m.cfg.PlanMode.PlanFile != "" {
		info.PlanFileBasename = compactPlanBasename(m.cfg.PlanMode.PlanFile)
	}
	return info
}

// compactPlanBasename truncates a long plan file basename for the
// banner. Keeps the first ~14 chars of the slug + the 8-char hash
// suffix so two plans sharing an opening phrase stay distinguishable.
// Appends ".md" so the result reads as a filename.
func compactPlanBasename(planPath string) string {
	base := filepath.Base(planPath)
	const maxBasenameWidth = 33 // "first14…last8.md" → 14 + 1 + 8 + 3
	if len(base) <= maxBasenameWidth {
		return base
	}
	slug := strings.TrimSuffix(base, ".md")
	return truncateSlug(slug, maxBasenameWidth-3) + ".md"
}

// renderPlanApprovalCard renders the decision UI for an exit_plan_mode
// approval. The plan body itself is NOT inside the box — it's emitted
// to scrollback as a quoted block when ApprovalNeeded fires (see
// handleAgentEvent), so this box only carries the four hotkeys.
// Separating body from decision keeps the box small, lets the body
// persist naturally after the user dismisses the modal, and avoids
// truncation when the plan is long.
func renderPlanApprovalCard(width int) string {
	hotkeys := stylePlanApprovalHotkey.Render("[A]") + " " +
		stylePlanApprovalChoice.Render("approve and implement") +
		"\n" +
		stylePlanApprovalHotkey.Render("[Y]") + " " +
		stylePlanApprovalChoice.Render("approve and auto-implement") +
		"\n\n" +
		stylePlanApprovalHotkey.Render("[L]") + " " +
		stylePlanApprovalChoice.Render("later") +
		"\n" +
		stylePlanApprovalHotkey.Render("[K]") + " " +
		stylePlanApprovalChoice.Render("keep planning")

	const sideIndent = "  "
	const sideIndentW = 2
	cap := width - 4
	if cap > 120 {
		cap = 120
	}
	hardWrap := func(s string) []string {
		lines := strings.Split(s, "\n")
		if cap <= sideIndentW {
			return lines
		}
		wrapW := cap - sideIndentW
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			if ansi.StringWidth(line) <= wrapW {
				out = append(out, line)
				continue
			}
			out = append(out, strings.Split(ansi.Hardwrap(line, wrapW, true), "\n")...)
		}
		return out
	}

	bodyLines := []string{""}
	for _, line := range hardWrap(hotkeys) {
		bodyLines = append(bodyLines, sideIndent+line)
	}
	bodyLines = append(bodyLines, "")

	leftLabel := " " + stylePlanApprovalTitle.Render(PlanModeIcon+" Approve plan?") + " "
	rightLabel := " " + stylePlanApprovalTool.Render("exit_plan_mode") + " "

	innerW := 0
	for _, line := range bodyLines {
		if w := ansi.StringWidth(line); w > innerW {
			innerW = w
		}
	}
	headW := ansi.StringWidth(leftLabel) + ansi.StringWidth(rightLabel) + 2
	if headW > innerW {
		innerW = headW
	}
	if cap > 0 && innerW > cap {
		innerW = cap
	}

	border := lipgloss.NewStyle().Foreground(colorWarning)
	fill := innerW - ansi.StringWidth(leftLabel) - ansi.StringWidth(rightLabel)
	if fill < 1 {
		fill = 1
	}
	top := border.Render("╭─") + leftLabel +
		border.Render(strings.Repeat("─", fill)) +
		rightLabel + border.Render("─╮")
	sideL := border.Render("│ ")
	sideR := border.Render(" │")
	rows := []string{top}
	for _, line := range bodyLines {
		pad := innerW - ansi.StringWidth(line)
		if pad < 0 {
			pad = 0
		}
		rows = append(rows, sideL+line+strings.Repeat(" ", pad)+sideR)
	}
	rows = append(rows, border.Render("╰"+strings.Repeat("─", innerW+2)+"╯"))
	return strings.Join(rows, "\n")
}

var (
	stylePlanBannerLabel    = lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	stylePlanBannerHint     = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	stylePlanBannerSep      = lipgloss.NewStyle().Foreground(colorRule)
	// stylePlanBannerActivity is the middle segment of the plan-mode
	// banner — the basename or the "awaiting your message" hint.
	// Plain content color (off-white on dark, dark on light) so it
	// reads as neutral status text rather than a call-to-action; the
	// label on the left is the only saturated element on the row.
	stylePlanBannerActivity = lipgloss.NewStyle().Foreground(colorContent)

	stylePlanApprovalTitle  = lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	stylePlanApprovalTool   = lipgloss.NewStyle().Foreground(colorDim)
	stylePlanApprovalHotkey = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	stylePlanApprovalChoice = lipgloss.NewStyle().Foreground(colorContent)
)
